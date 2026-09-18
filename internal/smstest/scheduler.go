// Package smstest runs end-to-end SMS delivery tests: it submits a message
// carrying a one-time code through an external SMS gateway API, then watches
// this server's own inbound SMS history for a message containing that code and
// records the round-trip latency.
package smstest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/store"
)

const (
	// tickInterval is how often due schedules are started and pending results
	// are reconciled against inbound SMS.
	tickInterval = 10 * time.Second
	// pendingTimeout is how long a sent test waits for its code to arrive
	// before it is recorded as failed.
	pendingTimeout = 5 * time.Minute
	// delayedThreshold separates a healthy round trip from a late one.
	delayedThreshold = 60 * time.Second
	// responseLimit caps how much of the gateway's reply is stored per result.
	responseLimit = 1024
	// inboundScanLimit caps how many inbound messages one reconcile pass reads.
	inboundScanLimit = 500
	requestTimeout   = 30 * time.Second
)

type Scheduler struct {
	store  *store.Store
	logger *slog.Logger
	client *http.Client
	now    func() time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}

	// runMu serializes test execution so a manual trigger cannot overlap the
	// ticker's own pass over the same schedule.
	runMu sync.Mutex
}

func New(database *store.Store, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		store:  database,
		logger: logger,
		client: &http.Client{Timeout: requestTimeout},
		now:    func() time.Time { return time.Now().UTC() },
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (s *Scheduler) Start() {
	s.startOnce.Do(func() {
		go func() {
			defer close(s.doneCh)
			ticker := time.NewTicker(tickInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), tickInterval*3)
					s.Tick(ctx)
					cancel()
				case <-s.stopCh:
					return
				}
			}
		}()
		s.logger.Info("SMS test scheduler started")
	})
}

// Stop halts the ticker and waits for an in-flight pass to finish.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh
	})
}

// Tick runs one full pass. It is exported so tests can drive the scheduler
// deterministically instead of waiting on the ticker.
func (s *Scheduler) Tick(ctx context.Context) {
	s.reconcilePending(ctx)
	s.runDueSchedules(ctx)
}

// reconcilePending matches pending results against inbound SMS and expires the
// ones that waited too long. Matching polls the SMS history rather than hooking
// the modem receive path, so nothing in the inbound ingest path can be delayed
// or broken by a test.
func (s *Scheduler) reconcilePending(ctx context.Context) {
	pending, err := s.store.ListPendingSMSTestResults(ctx)
	if err != nil {
		s.logger.Warn("smstest: list pending results failed", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	// Pending results are ordered oldest first, so the first entry bounds the
	// window of inbound messages worth scanning.
	oldest := pending[0].SentAt
	messages, err := s.store.ListSMSMessages(ctx, store.SMSFilter{
		Since: oldest.Add(-time.Minute),
		Limit: inboundScanLimit,
	})
	if err != nil {
		s.logger.Warn("smstest: list inbound messages failed", "error", err)
		messages = nil
	}

	matched := make(map[int64]bool, len(pending))
	for _, message := range messages {
		if !isInbound(message.Direction) || strings.TrimSpace(message.Body) == "" {
			continue
		}
		for _, result := range pending {
			if matched[result.ID] || result.Code == "" {
				continue
			}
			if !strings.Contains(message.Body, result.Code) {
				continue
			}
			// CreatedAt is this server's own ingest time. The message
			// timestamp carries the SMSC's clock, which can be skewed enough
			// to produce a negative round trip.
			receivedAt := message.CreatedAt
			if receivedAt.Before(result.SentAt) {
				receivedAt = s.now()
			}
			status := "received"
			if receivedAt.Sub(result.SentAt) > delayedThreshold {
				status = "delayed"
			}
			if err := s.store.SettleSMSTestResult(ctx, result.ID, receivedAt, status, message.DeviceID); err != nil {
				s.logger.Warn("smstest: settle result failed", "result", result.ID, "error", err)
				continue
			}
			matched[result.ID] = true
			s.logger.Info("smstest: test message matched",
				"result", result.ID,
				"code", result.Code,
				"status", status,
				"elapsed", receivedAt.Sub(result.SentAt).Round(time.Second).String(),
				"device", message.DeviceID,
			)
			break
		}
	}

	cutoff := s.now().Add(-pendingTimeout)
	for _, result := range pending {
		if matched[result.ID] || result.SentAt.After(cutoff) {
			continue
		}
		if err := s.store.SettleSMSTestResult(ctx, result.ID, s.now(), "failed", ""); err != nil {
			s.logger.Warn("smstest: expire result failed", "result", result.ID, "error", err)
			continue
		}
		s.logger.Info("smstest: test expired without a matching message",
			"result", result.ID, "code", result.Code)
	}
}

func (s *Scheduler) runDueSchedules(ctx context.Context) {
	schedules, err := s.store.ListSMSTestSchedules(ctx)
	if err != nil {
		s.logger.Warn("smstest: list schedules failed", "error", err)
		return
	}
	now := s.now()
	for _, schedule := range schedules {
		if !schedule.Enabled || !scheduleIsDue(schedule, now) {
			continue
		}
		if _, err := s.RunSchedule(ctx, schedule); err != nil {
			s.logger.Warn("smstest: run schedule failed", "schedule", schedule.ID, "error", err)
		}
	}
}

// scheduleIsDue reports whether a schedule should start a test now. A schedule
// that has never run waits for its daily start time; afterwards it simply runs
// every FrequencyMinutes.
func scheduleIsDue(schedule store.SMSTestSchedule, now time.Time) bool {
	if schedule.LastRunAt == nil {
		return afterStartTime(schedule.StartTime, now)
	}
	interval := time.Duration(schedule.FrequencyMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	return now.Sub(*schedule.LastRunAt) >= interval
}

func afterStartTime(startTime string, now time.Time) bool {
	hour, minute, ok := parseStartTime(startTime)
	if !ok {
		return true
	}
	if now.Hour() != hour {
		return now.Hour() > hour
	}
	return now.Minute() >= minute
}

func parseStartTime(value string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

// RunScheduleByID starts one test immediately, regardless of its schedule.
func (s *Scheduler) RunScheduleByID(ctx context.Context, id string) (store.SMSTestResult, error) {
	schedule, err := s.store.SMSTestSchedule(ctx, id)
	if err != nil {
		return store.SMSTestResult{}, err
	}
	return s.RunSchedule(ctx, schedule)
}

// RunSchedule generates a code, submits it through the schedule's endpoint and
// records the attempt. A gateway failure is recorded as a failed result rather
// than returned, so a broken endpoint shows up in the statistics instead of
// silently skipping the run.
func (s *Scheduler) RunSchedule(ctx context.Context, schedule store.SMSTestSchedule) (store.SMSTestResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	endpoint, err := s.store.SMSTestEndpoint(ctx, schedule.EndpointID)
	if err != nil {
		return store.SMSTestResult{}, fmt.Errorf("load endpoint %q: %w", schedule.EndpointID, err)
	}

	code := generateCode(schedule.CodeType, schedule.CodeLength)
	content := strings.ReplaceAll(schedule.ContentTemplate, "{{code}}", code)

	sentAt := s.now()
	response, sendErr := s.submit(ctx, endpoint, schedule, content)

	status := "pending"
	switch {
	case sendErr != nil:
		status = "failed"
		response = "ERROR: " + sendErr.Error()
	case schedule.IsExternal:
		// Nothing will arrive on our own modems, so there is no round trip to
		// wait for; the submission itself is the result.
		status = "sent"
	}

	result, err := s.store.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: schedule.ID,
		SentAt:     sentAt,
		Code:       code,
		Status:     status,
		// Redacted before it is stored, not before it is shown: the result
		// row is returned by the API and is the one place the gateway
		// password could come back out. A gateway that takes credentials in
		// the query string puts them in the URL, and Go's own transport
		// errors quote that URL back -- so a failed send would otherwise
		// write the password into a record anyone with the page open can
		// read.
		SendResponse: truncate(redactSecret(response, endpoint.Password), responseLimit),
	})
	if err != nil {
		return store.SMSTestResult{}, fmt.Errorf("record result: %w", err)
	}
	if err := s.store.TouchSMSTestScheduleRun(ctx, schedule.ID, sentAt); err != nil {
		s.logger.Warn("smstest: update schedule run time failed", "schedule", schedule.ID, "error", err)
	}

	s.logger.Info("smstest: test submitted",
		"schedule", schedule.ID, "code", code, "recipient", schedule.Recipient, "status", status)
	return result, nil
}

type keyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *Scheduler) submit(
	ctx context.Context,
	endpoint store.SMSTestEndpoint,
	schedule store.SMSTestSchedule,
	content string,
) (string, error) {
	replacer := strings.NewReplacer(
		"{{username}}", endpoint.Username,
		"{{password}}", endpoint.Password,
		"{{to}}", schedule.Recipient,
		"{{from}}", schedule.Sender,
		"{{content}}", content,
	)

	method := strings.ToUpper(strings.TrimSpace(endpoint.Method))
	if method == "" {
		method = http.MethodPost
	}

	var body io.Reader
	bodyParams := decodeKeyValues(endpoint.BodyParams)
	if len(bodyParams) > 0 {
		fields := make(map[string]string, len(bodyParams))
		for _, param := range bodyParams {
			fields[param.Key] = replacer.Replace(param.Value)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return "", fmt.Errorf("encode body: %w", err)
		}
		body = strings.NewReader(string(encoded))
	}

	request, err := http.NewRequestWithContext(ctx, method, replacer.Replace(endpoint.URL), body)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	for _, header := range decodeKeyValues(endpoint.Headers) {
		request.Header.Set(header.Key, replacer.Replace(header.Value))
	}
	if endpoint.Username != "" || endpoint.Password != "" {
		request.SetBasicAuth(endpoint.Username, endpoint.Password)
	}
	if body != nil && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := s.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit*2))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	text := string(raw)
	if response.StatusCode >= http.StatusBadRequest {
		return text, fmt.Errorf("gateway returned HTTP %d", response.StatusCode)
	}
	return text, nil
}

func decodeKeyValues(raw string) []keyValue {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var pairs []keyValue
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		return nil
	}
	return pairs
}

func isInbound(direction string) bool {
	return direction == "inbound" || direction == "received"
}

// secretMask is what replaces a credential in anything stored or returned.
const secretMask = "<redacted>"

// redactSecret removes a secret from text that will be persisted. It is
// deliberately a blunt substring replacement: the secret can reach a response
// body by being echoed, quoted in an error, or reflected in a redirect URL,
// and guessing which of those happened is less reliable than removing it
// wherever it appears.
//
// A very short secret is left alone. Blanking every occurrence of a two-
// character password would mangle the response into something unreadable
// while protecting a secret that is not one.
func redactSecret(value, secret string) string {
	if len(secret) < 4 || value == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, secretMask)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// NewID returns an identifier for a new endpoint or schedule record.
func NewID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UTC().UnixNano(), 16)
	}
	return hex.EncodeToString(raw[:])
}

func generateCode(codeType string, length int) string {
	const (
		digits  = "0123456789"
		letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		mixed   = letters + digits
	)
	if length <= 0 {
		length = 6
	}
	switch strings.ToLower(strings.TrimSpace(codeType)) {
	case "letters":
		return randomString(letters, length)
	case "mixed":
		return randomString(mixed, length)
	default:
		return randomString(digits, length)
	}
}

func randomString(alphabet string, length int) string {
	var builder strings.Builder
	builder.Grow(length)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand does not fail in practice; degrade to a fixed
			// character rather than panicking inside a background loop.
			builder.WriteByte(alphabet[0])
			continue
		}
		builder.WriteByte(alphabet[index.Int64()])
	}
	return builder.String()
}
