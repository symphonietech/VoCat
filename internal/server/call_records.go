package server

import (
	"context"
	"strings"
	"time"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

const (
	// callRecorderInterval is how often live calls are sampled. The IMS
	// session keeps an ended call in its list for thirty seconds, so this is
	// comfortably fast enough to see every call's final state -- including a
	// rejection that never rang.
	callRecorderInterval = 2 * time.Second
	// callRecordRetention is how long history is kept. Records accumulate for
	// ever otherwise, and a year of them across several SIMs is a database
	// nobody asked for.
	callRecordRetention = 90 * 24 * time.Hour
	// callRecordPruneInterval spaces out the delete. It is a cheap query, but
	// not one worth running every two seconds.
	callRecordPruneInterval = time.Hour
	// callOriginMemory bounds the map of call IDs the trunk has touched. A
	// deployment does not have thousands of calls in flight, and an entry
	// only has to outlive the call plus the IMS session's own retention.
	callOriginMemory = 512
)

// StartCallRecorder writes a record for every call the IMS session reports.
//
// Observing state beats recording at the points that act on a call. Dial,
// answer and hangup each know one moment; none of them sees a call the far
// end ended, one that failed while ringing, or one the SIP trunk placed. The
// IMS session already tracks all of it and keeps a finished call around for
// thirty seconds, so sampling that is the one place that catches everything.
func (s *Server) StartCallRecorder(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(callRecorderInterval)
	defer ticker.Stop()
	prune := time.NewTicker(callRecordPruneInterval)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recordLiveCalls(ctx)
		case <-prune.C:
			if _, err := s.store.DeleteCallRecordsBefore(ctx, time.Now().Add(-callRecordRetention)); err != nil {
				s.logger.Warn("could not prune call records", "category", "call", "error", err)
			}
		}
	}
}

func (s *Server) recordLiveCalls(ctx context.Context) {
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok {
		return
	}
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return
	}
	for _, config := range devices {
		if s.callTransport(config.ID) != "vowifi" {
			continue
		}
		calls, err := controller.Calls(config.ID)
		if err != nil {
			continue
		}
		for _, call := range calls {
			record := callRecordFrom(config, call)
			record.Source = s.callSource(config.ID, call.ID)
			if err := s.store.UpsertCallRecord(ctx, record); err != nil {
				s.logger.Warn("could not write a call record",
					"category", "call", "device_id", config.ID, "call_id", call.ID, "error", err)
			}
		}
	}
}

// callRecordFrom turns one live call into a record.
func callRecordFrom(config store.Device, call vowifi.Call) store.CallRecord {
	record := store.CallRecord{
		CallID:     call.ID,
		DeviceID:   config.ID,
		DeviceName: strings.TrimSpace(config.Name),
		Direction:  call.Direction,
		PeerNumber: call.Number,
		StartedAt:  call.StartedAt,
		AnsweredAt: call.AnsweredAt,
		EndedAt:    call.EndedAt,
		SIPCode:    call.SIPCode,
		Reason:     call.Reason,
	}
	if call.AnsweredAt != nil && call.EndedAt != nil {
		// Answer to hang-up, which is what a carrier bills. Ringing is not
		// duration, however long it went on.
		if seconds := int(call.EndedAt.Sub(*call.AnsweredAt).Seconds()); seconds > 0 {
			record.DurationSeconds = seconds
		}
	}
	record.Disposition = callDisposition(call)
	return record
}

// callDisposition names the outcome of a call that has ended. A call still in
// progress gets no disposition rather than a wrong one, so the column means
// "how it finished" instead of "how it looked at the last poll".
func callDisposition(call vowifi.Call) string {
	if call.EndedAt == nil && call.State != "ended" && call.State != "failed" {
		return ""
	}
	if call.AnsweredAt != nil {
		return "answered"
	}
	switch call.SIPCode {
	case 486, 600:
		return "busy"
	case 480, 408, 487:
		// 487 is a CANCEL: whoever placed the call gave up while it rang.
		if call.SIPCode == 487 {
			return "cancelled"
		}
		return "no_answer"
	case 0:
		// No SIP code and never answered: the far end or the network ended it
		// without saying why, which from here is indistinguishable from a
		// missed call.
		return "no_answer"
	}
	return "failed"
}

// callSource says who drove a call. The trunk registers the IMS calls it
// places and answers, so anything else came from VoCat's own Calls page or an
// automatic task, both of which are the browser path.
func (s *Server) callSource(deviceID, callID string) string {
	if s.isTrunkCall(deviceID, callID) {
		return "trunk"
	}
	return "browser"
}

// noteTrunkCall records that the SIP trunk owns this call, so the recorder can
// label it. Called from the trunk gateway, which is the only place that knows.
func (s *Server) noteTrunkCall(deviceID, callID string) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(callID) == "" {
		return
	}
	s.callOriginMu.Lock()
	defer s.callOriginMu.Unlock()
	if s.callOrigin == nil {
		s.callOrigin = make(map[string]time.Time)
	}
	// Bounded by age rather than by eviction order: an entry only has to
	// outlive the call and the IMS session's own retention of a finished one,
	// so anything from yesterday is certainly spent.
	if len(s.callOrigin) >= callOriginMemory {
		cutoff := time.Now().Add(-24 * time.Hour)
		for key, seen := range s.callOrigin {
			if seen.Before(cutoff) {
				delete(s.callOrigin, key)
			}
		}
	}
	s.callOrigin[trunkCallKey(deviceID, callID)] = time.Now()
}

func (s *Server) isTrunkCall(deviceID, callID string) bool {
	s.callOriginMu.Lock()
	defer s.callOriginMu.Unlock()
	_, found := s.callOrigin[trunkCallKey(deviceID, callID)]
	return found
}
