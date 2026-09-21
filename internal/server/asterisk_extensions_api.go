package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vocat/internal/ami"
	"vocat/internal/asteriskconf"
	"vocat/internal/store"
)

// asteriskExtensionsKey holds the softphone accounts in the app settings
// table, marked sensitive so the generic settings API never hands the
// passwords out with everything else.
const asteriskExtensionsKey = "asterisk.extensions"

// endpointsFileName is written into the directory VoCat and Asterisk share.
// Not "extensions.conf": that is the dialplan's name in Asterisk, and these
// are PJSIP objects, which is a different file and a different reload.
const endpointsFileName = "endpoints.conf"

// internalFileName holds the extension-to-extension dialplan, generated from
// the same account list. Two files because they are two different Asterisk
// subsystems with two different reloads, not because they have two sources.
const internalFileName = "internal.conf"

// inboundFileName holds where a call arriving on a SIM rings.
const inboundFileName = "inbound.conf"

// messagesFileName holds the SMS dialplan: where a text from a SIM is
// delivered, and one context per extension naming that extension as the
// sender of anything it sends.
const messagesFileName = "messages.conf"

// asteriskInboundKey holds the inbound plan in the app settings table.
const asteriskInboundKey = "asterisk.inbound"

// asteriskSMSKey holds where a text arriving on a SIM goes. Separate from the
// inbound plan above because calls and texts are routed independently: a
// deployment can ring handsets without also delivering their SMS.
const asteriskSMSKey = "asterisk.sms"

// asteriskExtension is the wire shape. The password is write-only: it is
// accepted on PUT and never returned, so a browser session that can read the
// page cannot read the SIP credentials out of it.
type asteriskExtension struct {
	Name        string `json:"name"`
	Password    string `json:"password,omitempty"`
	CallerID    string `json:"caller_id,omitempty"`
	MaxContacts int    `json:"max_contacts"`
	Comment     string `json:"comment,omitempty"`
	// HasPassword is what the browser gets instead of the password: enough to
	// render "set" versus "not set" and nothing more.
	HasPassword bool `json:"has_password,omitempty"`
}

func (e asteriskExtension) toConfig() asteriskconf.Extension {
	return asteriskconf.Extension{
		Name:        strings.TrimSpace(e.Name),
		Password:    e.Password,
		CallerID:    strings.TrimSpace(e.CallerID),
		MaxContacts: e.MaxContacts,
		Comment:     strings.TrimSpace(e.Comment),
	}
}

func toConfigExtensions(extensions []asteriskExtension) []asteriskconf.Extension {
	out := make([]asteriskconf.Extension, 0, len(extensions))
	for _, extension := range extensions {
		out = append(out, extension.toConfig())
	}
	return out
}

func (s *Server) routeAsteriskExtensionsAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	switch cleanPath {
	case "asterisk/extensions":
		switch r.Method {
		case http.MethodGet:
			s.handleGetAsteriskExtensions(w, r)
		case http.MethodPut:
			s.handlePutAsteriskExtensions(w, r)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return true
	case "asterisk/extensions/apply":
		if requireMethod(w, r, http.MethodPost) {
			s.handleApplyAsteriskExtensions(w, r)
		}
		return true
	case "asterisk/extensions/candidates":
		s.handleAsteriskExtensionCandidates(w, r)
		return true
	case "asterisk/inbound":
		if requireMethod(w, r, http.MethodPut) {
			s.handlePutAsteriskInbound(w, r)
		}
		return true
	case "asterisk/sms":
		if requireMethod(w, r, http.MethodPut) {
			s.handlePutAsteriskSMS(w, r)
		}
		return true
	case "asterisk/sms/history":
		if requireMethod(w, r, http.MethodGet) {
			s.handleAsteriskSMSHistory(w, r)
		}
		return true
	}
	return false
}

// storedAsteriskExtensions reads the saved accounts, passwords included. This
// is the only thing that reads them, and nothing it returns reaches a
// response without going through withoutPasswords first.
func (s *Server) storedAsteriskExtensions(ctx context.Context) []asteriskExtension {
	setting, err := s.store.AppSetting(ctx, asteriskExtensionsKey)
	if err != nil || len(setting.Value) == 0 {
		return nil
	}
	var extensions []asteriskExtension
	if err := json.Unmarshal(setting.Value, &extensions); err != nil {
		s.logger.Warn("stored Asterisk extensions are unreadable",
			"category", "siptrunk", "error", err)
		return nil
	}
	return extensions
}

// withoutPasswords is what the API returns. Separate from the stored shape on
// purpose: a field added to the stored struct does not leak by default,
// because this builds the response value by value.
func withoutPasswords(extensions []asteriskExtension) []asteriskExtension {
	out := make([]asteriskExtension, 0, len(extensions))
	for _, extension := range extensions {
		out = append(out, asteriskExtension{
			Name:        extension.Name,
			CallerID:    extension.CallerID,
			MaxContacts: extension.MaxContacts,
			Comment:     extension.Comment,
			HasPassword: extension.Password != "",
		})
	}
	return out
}

// passwordLine matches the one line of the rendered file that must not be
// shown. The preview exists so a syntax question can be answered without
// shelling into the container, which does not require the secret.
var passwordLine = regexp.MustCompile(`(?m)^password=.*$`)

func redactPasswords(rendered string) string {
	return passwordLine.ReplaceAllString(rendered, "password=<hidden>")
}

func (s *Server) asteriskEndpointsPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, endpointsFileName)
}

func (s *Server) asteriskInternalPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, internalFileName)
}

func (s *Server) asteriskMessagesPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, messagesFileName)
}

func (s *Server) asteriskInboundPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, inboundFileName)
}

// asteriskInboundPlan reads the saved plan, falling back to the default.
// A stored plan that no longer validates -- an extension in the ring group was
// deleted, say -- also falls back rather than leaving the file unwritable:
// ringing everything is wrong in a smaller way than ringing nothing.
func (s *Server) asteriskInboundPlan(ctx context.Context, configured []asteriskconf.Extension) asteriskconf.InboundPlan {
	plan := asteriskconf.DefaultInboundPlan()
	setting, err := s.store.AppSetting(ctx, asteriskInboundKey)
	if err != nil || len(setting.Value) == 0 {
		return plan
	}
	var stored asteriskInbound
	if err := json.Unmarshal(setting.Value, &stored); err != nil {
		s.logger.Warn("the stored Asterisk inbound plan is unreadable",
			"category", "siptrunk", "error", err)
		return plan
	}
	candidate := stored.toConfig()
	if err := candidate.Validate(configured); err != nil {
		s.logger.Warn("the stored Asterisk inbound plan no longer applies",
			"category", "siptrunk", "error", err)
		return plan
	}
	// A forward plan naming a trunk that has since been deleted renders a
	// dial to an endpoint Asterisk has never heard of, which fails at call
	// time with nothing pointing at why. Same reasoning as the extension
	// check above: fall back rather than generate it.
	name, err := s.resolveInboundTrunk(ctx, candidate)
	if err != nil {
		s.logger.Warn("the stored Asterisk inbound plan names a trunk that is gone",
			"category", "siptrunk", "error", err)
		return plan
	}
	candidate.ForwardTrunk = name
	return candidate
}

// resolveInboundTrunk checks a forward plan against the configured trunks.
//
// It lives here rather than in asteriskconf because that package renders one
// file at a time and has no view of the trunk list, while the reason to
// refuse is precisely that two separately edited lists disagree.
// It returns the trunk's stored spelling, because the match is
// case-insensitive while the dial string is rendered verbatim: "ACME" would
// otherwise validate and then dial @trunk-ACME against a [trunk-acme] section.
func (s *Server) resolveInboundTrunk(ctx context.Context, plan asteriskconf.InboundPlan) (string, error) {
	if plan.Mode != asteriskconf.InboundForward {
		return plan.ForwardTrunk, nil
	}
	want := strings.ToLower(strings.TrimSpace(plan.ForwardTrunk))
	for _, trunk := range s.storedAsteriskTrunks(ctx) {
		if strings.ToLower(strings.TrimSpace(trunk.Name)) == want {
			return strings.TrimSpace(trunk.Name), nil
		}
	}
	return "", fmt.Errorf("trunk %s is not configured", strings.TrimSpace(plan.ForwardTrunk))
}

// asteriskInbound is the wire shape of the plan.
type asteriskInbound struct {
	Mode        string   `json:"mode"`
	Extensions  []string `json:"extensions,omitempty"`
	RingSeconds int      `json:"ring_seconds"`
	HuntSeconds int      `json:"hunt_seconds"`
	// Forward mode only: which trunk to send the call out to, and what to
	// dial there. An empty number passes the dialled number through.
	ForwardTrunk  string `json:"forward_trunk,omitempty"`
	ForwardNumber string `json:"forward_number,omitempty"`
}

func (i asteriskInbound) toConfig() asteriskconf.InboundPlan {
	plan := asteriskconf.InboundPlan{
		Mode:          asteriskconf.InboundMode(strings.TrimSpace(i.Mode)),
		RingSeconds:   i.RingSeconds,
		HuntSeconds:   i.HuntSeconds,
		ForwardTrunk:  strings.TrimSpace(i.ForwardTrunk),
		ForwardNumber: strings.TrimSpace(i.ForwardNumber),
	}
	for _, name := range i.Extensions {
		if name = strings.TrimSpace(name); name != "" {
			plan.Extensions = append(plan.Extensions, name)
		}
	}
	if plan.RingSeconds == 0 {
		plan.RingSeconds = asteriskconf.DefaultRingSeconds
	}
	if plan.HuntSeconds == 0 {
		plan.HuntSeconds = asteriskconf.DefaultHuntSeconds
	}
	return plan
}

func inboundToWire(plan asteriskconf.InboundPlan) asteriskInbound {
	return asteriskInbound{
		Mode:          string(plan.Mode),
		Extensions:    plan.Extensions,
		RingSeconds:   plan.RingSeconds,
		HuntSeconds:   plan.HuntSeconds,
		ForwardTrunk:  plan.ForwardTrunk,
		ForwardNumber: plan.ForwardNumber,
	}
}

// renderAsteriskExtensions produces both generated files. They always move
// together: an account that exists in one and not the other registers fine
// and is not dialable, with nothing anywhere saying why.
type asteriskFiles struct {
	endpoints string
	internal  string
	inbound   string
	messages  string
}

func renderAsteriskExtensions(
	extensions []asteriskExtension,
	plan asteriskconf.InboundPlan,
	sms asteriskconf.SMSMode,
	trunkHost string,
) (asteriskFiles, error) {
	config := toConfigExtensions(extensions)
	endpoints, err := asteriskconf.RenderExtensions(config)
	if err != nil {
		return asteriskFiles{}, err
	}
	internal, err := asteriskconf.RenderInternalDialplan(config)
	if err != nil {
		return asteriskFiles{}, err
	}
	inbound, err := asteriskconf.RenderInbound(plan, config)
	if err != nil {
		return asteriskFiles{}, err
	}
	messages, err := asteriskconf.RenderMessages(sms, trunkHost, config)
	if err != nil {
		return asteriskFiles{}, err
	}
	return asteriskFiles{
		endpoints: endpoints, internal: internal, inbound: inbound, messages: messages,
	}, nil
}

// asteriskSMSMode reads the saved SMS mode. Anything unreadable or unknown
// falls back to off, which forwards nothing: a mode nobody can parse must not
// start delivering texts to handsets.
func (s *Server) asteriskSMSMode(ctx context.Context) asteriskconf.SMSMode {
	setting, err := s.store.AppSetting(ctx, asteriskSMSKey)
	if err != nil || len(setting.Value) == 0 {
		return asteriskconf.SMSOff
	}
	var stored struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(setting.Value, &stored); err != nil {
		s.logger.Warn("the stored Asterisk SMS mode is unreadable",
			"category", "siptrunk", "error", err)
		return asteriskconf.SMSOff
	}
	mode := asteriskconf.SMSMode(strings.TrimSpace(stored.Mode))
	if mode.Validate() != nil {
		return asteriskconf.SMSOff
	}
	return mode
}

// asteriskTrunkHost is where the generated dialplan submits an SMS an
// extension is sending.
//
// A wildcard bind becomes loopback: the two containers share the host network
// namespace, so that is the address Asterisk can actually reach, and putting
// "0.0.0.0" in a dial string would produce a request that goes nowhere.
func (s *Server) asteriskTrunkHost() string {
	address := strings.TrimSpace(s.sipTrunkAddress)
	if address == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// writeAsteriskExtensionFiles writes both, endpoints first. Neither ordering
// is atomic across the pair, and this one fails on the side that is merely
// unreachable rather than the side that would let a stale account register.
func (s *Server) writeAsteriskExtensionFiles(files asteriskFiles) error {
	if path := s.asteriskEndpointsPath(); path != "" {
		if err := writeAsteriskSecretFile(path, files.endpoints); err != nil {
			return err
		}
	}
	// No secret in the dialplan halves, so they match the routes file rather
	// than the endpoints file.
	for path, contents := range map[string]string{
		s.asteriskInternalPath(): files.internal,
		s.asteriskInboundPath():  files.inbound,
		s.asteriskMessagesPath(): files.messages,
	} {
		if path == "" {
			continue
		}
		if err := writeAsteriskRoutesFile(path, contents); err != nil {
			return err
		}
	}
	return nil
}

// asteriskExtensionFilesDiffer reports whether either generated file is out
// of step with what the stored accounts render to.
func (s *Server) asteriskExtensionFilesDiffer(files asteriskFiles) bool {
	for path, want := range map[string]string{
		s.asteriskEndpointsPath(): files.endpoints,
		s.asteriskInternalPath():  files.internal,
		s.asteriskInboundPath():   files.inbound,
		s.asteriskMessagesPath():  files.messages,
	} {
		if path == "" {
			continue
		}
		current, err := os.ReadFile(path)
		if err != nil || string(current) != want {
			return true
		}
	}
	return false
}

// asteriskEndpointsAreSeeded reports whether the file Asterisk is using came
// from the container entrypoint rather than from VoCat. The entrypoint writes
// the account in .env on first start, and until something is saved here that
// account is the only way anyone's phone registers.
func (s *Server) asteriskEndpointsAreSeeded() bool {
	path := s.asteriskEndpointsPath()
	if path == "" {
		return false
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(string(current), asteriskconf.GeneratedHeader)
}

func (s *Server) handleGetAsteriskExtensions(w http.ResponseWriter, r *http.Request) {
	extensions := s.storedAsteriskExtensions(r.Context())
	payload := map[string]any{
		"extensions":          withoutPasswords(extensions),
		"path":                s.asteriskEndpointsPath(),
		"can_apply":           strings.TrimSpace(s.asteriskAMI.Address) != "",
		"min_password_length": asteriskconf.MinPasswordLength,
	}
	plan := s.asteriskInboundPlan(r.Context(), toConfigExtensions(extensions))
	payload["inbound"] = inboundToWire(plan)
	payload["sms"] = asteriskSMS{Mode: string(s.asteriskSMSMode(r.Context()))}
	// An empty host is the one condition that makes SMS unavailable: without
	// a trunk there is nothing to carry a text in either direction, and the
	// page says so rather than letting a save fail.
	payload["trunk_host"] = s.asteriskTrunkHost()
	files, err := renderAsteriskExtensions(extensions, plan, s.asteriskSMSMode(r.Context()), s.asteriskTrunkHost())
	if err != nil {
		payload["error"] = err.Error()
	} else {
		payload["preview"] = redactPasswords(files.endpoints)
		// The dialplan halves have no secret in them, so they are shown
		// whole. They are also what someone reads to answer "why can 1001
		// not reach 1003" and "why did that call not ring anything".
		payload["internal_preview"] = files.internal
		payload["inbound_preview"] = files.inbound
		payload["sms_preview"] = files.messages
		if s.asteriskDialplanDir != "" {
			payload["pending"] = s.asteriskExtensionFilesDiffer(files)
		}
	}
	if path := s.asteriskEndpointsPath(); path != "" {
		if current, readErr := os.ReadFile(path); readErr == nil {
			// A file Asterisk is using that VoCat did not write is the
			// account the container seeded from the environment on first
			// start. Saving here replaces it, so the page has to say so
			// before it happens rather than after the handset stops
			// registering.
			payload["seeded"] = !strings.HasPrefix(string(current), asteriskconf.GeneratedHeader)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
}

// handlePutAsteriskExtensions replaces the account list.
//
// An entry with no password keeps the one already stored, which is what makes
// the password write-only usable: the browser never had it, so it cannot send
// it back, and every edit to a name or a device count would otherwise wipe
// the credential it never saw.
func (s *Server) handlePutAsteriskExtensions(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Extensions []asteriskExtension `json:"extensions"`
		// ReplaceSeeded confirms wiping the account the container seeded from
		// .env. Only an empty list needs it -- see below.
		ReplaceSeeded bool `json:"replace_seeded"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	existing := map[string]string{}
	for _, extension := range s.storedAsteriskExtensions(r.Context()) {
		existing[strings.ToLower(strings.TrimSpace(extension.Name))] = extension.Password
	}
	merged := make([]asteriskExtension, 0, len(request.Extensions))
	for _, extension := range request.Extensions {
		extension.Name = strings.TrimSpace(extension.Name)
		if extension.Password == "" {
			extension.Password = existing[strings.ToLower(extension.Name)]
		}
		if extension.Password == "" {
			// A new account with no password cannot be rendered, and saying
			// so by name beats a generic length complaint.
			writeError(w, http.StatusBadRequest, "password_required",
				"extension "+extension.Name+" has no password; set one to create it")
			return
		}
		extension.HasPassword = false
		merged = append(merged, extension)
	}
	// Saving nothing over the seeded file leaves the PBX with no account at
	// all, which shows up as a handset that quietly stops registering some
	// time later. An empty list is a legitimate thing to want, so this asks
	// rather than refuses -- but it does ask.
	if len(merged) == 0 && s.asteriskEndpointsAreSeeded() && !request.ReplaceSeeded {
		writeError(w, http.StatusConflict, "seeded_extensions",
			"Asterisk is using the account seeded from ASTERISK_SIP_USER in .env, and saving an "+
				"empty list would remove it; every softphone would stop registering. "+
				"Send replace_seeded to do it anyway.")
		return
	}
	// Render before storing. A rejected account must not be saved, or the
	// page would show one that can never be applied.
	// The inbound plan is validated against the new list, so removing an
	// extension that a ring group names falls back to ringing everything
	// rather than rendering a file that rings nothing.
	plan := s.asteriskInboundPlan(r.Context(), toConfigExtensions(merged))
	files, err := renderAsteriskExtensions(merged, plan, s.asteriskSMSMode(r.Context()), s.asteriskTrunkHost())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_extension", err.Error())
		return
	}
	body, err := json.Marshal(merged)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not encode extensions")
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
		Key: asteriskExtensionsKey, Value: body, Sensitive: true,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	written := false
	if s.asteriskDialplanDir != "" {
		if err := s.writeAsteriskExtensionFiles(files); err != nil {
			s.logger.Error("could not write the Asterisk extension files",
				"category", "siptrunk", "dir", s.asteriskDialplanDir, "error", err)
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = true
	}
	s.recordAudit(r.Context(), "admin", "asterisk.extensions.save", "asterisk", "extensions", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"saved": true, "written": written,
		"extensions":       withoutPasswords(merged),
		"preview":          redactPasswords(files.endpoints),
		"internal_preview": files.internal,
		"inbound_preview":  files.inbound,
	}})
}

// writeAsteriskSecretFile replaces the file atomically and leaves it readable
// only by the user that wrote it. The contents are SIP passwords in the
// clear, which is what Asterisk needs to answer a digest challenge; 0644 --
// what the routes file uses -- would be wrong here.
func writeAsteriskSecretFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	// Written 0600 from the start rather than chmodded after: between the
	// two there would be a moment where the passwords are world-readable.
	if err := os.WriteFile(temporary, []byte(contents), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// handleApplyAsteriskExtensions asks Asterisk to reload PJSIP.
//
// res_pjsip rather than pbx_config: these are endpoints, auths and AORs, and
// reloading the dialplan would leave them exactly as they were while
// reporting success.
func (s *Server) handleApplyAsteriskExtensions(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.asteriskAMI.Address) == "" {
		writeError(w, http.StatusNotImplemented, "ami_not_configured",
			"the Asterisk manager interface is not configured, so VoCat cannot reload Asterisk; "+
				"run `docker compose restart asterisk` instead")
		return
	}
	extensions := s.storedAsteriskExtensions(r.Context())
	plan := s.asteriskInboundPlan(r.Context(), toConfigExtensions(extensions))
	files, err := renderAsteriskExtensions(extensions, plan, s.asteriskSMSMode(r.Context()), s.asteriskTrunkHost())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_extension", err.Error())
		return
	}
	// Re-render and rewrite first: applying what is on disk when it differs
	// from what is stored would reload a stale account list and report
	// success.
	if s.asteriskDialplanDir != "" {
		if err := s.writeAsteriskExtensionFiles(files); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), asteriskStatusTimeout)
	defer cancel()
	conn, err := ami.Dial(ctx, s.asteriskAMI)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ami_unreachable", err.Error())
		return
	}
	defer conn.Close()

	// Two modules, because saving an extension now writes two files: the
	// accounts, which res_pjsip owns, and the internal dialplan, which
	// pbx_config owns. Reloading only the first would leave a new account
	// registering perfectly and unreachable from every other handset.
	//
	// res_pjsip first: an account that exists but is not yet dialable is a
	// smaller window than one that is dialable and does not exist.
	for _, module := range []string{"res_pjsip", "pbx_config"} {
		response, err := conn.Action(ctx, "Reload", ami.Message{"Module": module})
		if err != nil {
			writeError(w, http.StatusBadGateway, "reload_failed", module+": "+err.Error())
			return
		}
		if !strings.EqualFold(response.Get("Response"), "Success") {
			writeError(w, http.StatusBadGateway, "reload_failed",
				module+": "+response.First("Message", "Response"))
			return
		}
	}
	s.recordAudit(r.Context(), "admin", "asterisk.extensions.apply", "asterisk", "extensions", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"applied": true,
		"at":      time.Now().UTC().Format(time.RFC3339),
		// A phone re-registers on its own timer, so an account whose password
		// just changed goes away and comes back rather than failing visibly.
		"note": "phones re-register on their own timer; a changed password shows up at the next one",
	}})
}
