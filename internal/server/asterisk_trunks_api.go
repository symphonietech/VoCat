package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vocat/internal/ami"
	"vocat/internal/asteriskconf"
	"vocat/internal/store"
)

// asteriskTrunksKey holds the inbound trunk list in the app settings table,
// the same way routes and extensions are held: one ordered document, edited
// whole, needing no migration.
const asteriskTrunksKey = "asterisk.trunks"

const (
	// trunksFileName holds the PJSIP objects. It may contain credentials, so
	// it is written 0600.
	trunksFileName = "trunks.conf"
	// trunkRoutesFileName holds one dial plan context per trunk.
	trunkRoutesFileName = "trunk-routes.conf"
)

// asteriskTrunk is one external peer as the browser sees it. The password is
// accepted on the way in and never returned.
type asteriskTrunk struct {
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	Transport string   `json:"transport"`
	Match     []string `json:"match,omitempty"`
	Username  string   `json:"username,omitempty"`
	Password  string   `json:"password,omitempty"`
	// The outbound pair answers a challenge to an INVITE this side sends,
	// which is what a provider does when a call is forwarded out to it.
	OutboundUsername string   `json:"outbound_username,omitempty"`
	OutboundPassword string   `json:"outbound_password,omitempty"`
	Destinations     []string `json:"destinations,omitempty"`
	Devices          []string `json:"devices"`
	MaxConcurrent    int      `json:"max_concurrent"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	ShareRoutes      bool     `json:"share_routes,omitempty"`
	Comment          string   `json:"comment,omitempty"`
	// HasPassword and HasOutboundPassword are what the browser gets instead
	// of the passwords: enough to render "set" versus "not set" and nothing
	// more.
	HasPassword         bool `json:"has_password,omitempty"`
	HasOutboundPassword bool `json:"has_outbound_password,omitempty"`
}

func (t asteriskTrunk) toConfig() asteriskconf.Trunk {
	return asteriskconf.Trunk{
		Name:             strings.TrimSpace(t.Name),
		Host:             strings.TrimSpace(t.Host),
		Port:             t.Port,
		Transport:        strings.TrimSpace(t.Transport),
		Match:            t.Match,
		Username:         strings.TrimSpace(t.Username),
		Password:         t.Password,
		OutboundUsername: strings.TrimSpace(t.OutboundUsername),
		OutboundPassword: t.OutboundPassword,
		Destinations:     t.Destinations,
		Devices:          t.Devices,
		MaxConcurrent:    t.MaxConcurrent,
		TimeoutSeconds:   t.TimeoutSeconds,
		ShareRoutes:      t.ShareRoutes,
		Comment:          strings.TrimSpace(t.Comment),
	}
}

func toConfigTrunks(trunks []asteriskTrunk) []asteriskconf.Trunk {
	out := make([]asteriskconf.Trunk, 0, len(trunks))
	for _, trunk := range trunks {
		out = append(out, trunk.toConfig())
	}
	return out
}

func withoutTrunkPasswords(trunks []asteriskTrunk) []asteriskTrunk {
	out := make([]asteriskTrunk, 0, len(trunks))
	for _, trunk := range trunks {
		trunk.HasPassword = trunk.Password != ""
		trunk.HasOutboundPassword = trunk.OutboundPassword != ""
		trunk.Password = ""
		trunk.OutboundPassword = ""
		out = append(out, trunk)
	}
	return out
}

// trunkFiles is what one save renders: PJSIP objects and dial plan, which
// belong to different Asterisk modules and so to different files.
type trunkFiles struct {
	objects string
	routes  string
}

func renderAsteriskTrunks(trunks []asteriskTrunk) (trunkFiles, error) {
	configs := toConfigTrunks(trunks)
	objects, err := asteriskconf.RenderTrunks(configs)
	if err != nil {
		return trunkFiles{}, err
	}
	routes, err := asteriskconf.RenderTrunkRoutes(configs)
	if err != nil {
		return trunkFiles{}, err
	}
	return trunkFiles{objects: objects, routes: routes}, nil
}

func (s *Server) routeAsteriskTrunksAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	switch cleanPath {
	case "asterisk/trunks":
		switch r.Method {
		case http.MethodGet:
			s.handleGetAsteriskTrunks(w, r)
		case http.MethodPut:
			s.handlePutAsteriskTrunks(w, r)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return true
	case "asterisk/trunks/apply":
		if requireMethod(w, r, http.MethodPost) {
			s.handleApplyAsteriskTrunks(w, r)
		}
		return true
	}
	return false
}

// storedAsteriskTrunks reads the saved list. A missing or unreadable setting
// is an empty list rather than an error: the page must render so the operator
// can create the first trunk.
func (s *Server) storedAsteriskTrunks(ctx context.Context) []asteriskTrunk {
	setting, err := s.store.AppSetting(ctx, asteriskTrunksKey)
	if err != nil || len(setting.Value) == 0 {
		return nil
	}
	var trunks []asteriskTrunk
	if err := json.Unmarshal(setting.Value, &trunks); err != nil {
		s.logger.Warn("stored Asterisk trunks are unreadable",
			"category", "siptrunk", "error", err)
		return nil
	}
	return trunks
}

func (s *Server) asteriskTrunksPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, trunksFileName)
}

func (s *Server) asteriskTrunkRoutesPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, trunkRoutesFileName)
}

func (s *Server) handleGetAsteriskTrunks(w http.ResponseWriter, r *http.Request) {
	trunks := s.storedAsteriskTrunks(r.Context())
	payload := map[string]any{
		"trunks":    withoutTrunkPasswords(trunks),
		"path":      s.asteriskTrunksPath(),
		"writable":  s.asteriskTrunksPath() != "",
		"can_apply": strings.TrimSpace(s.asteriskAMI.Address) != "",
	}
	// A trunk naming a SIM that has been removed is the failure this page
	// cannot otherwise show: the config is valid, Apply succeeds, and every
	// call through that trunk fails at dial time.
	if unknown := s.unknownTrunkDevices(r.Context(), trunks); len(unknown) > 0 {
		payload["unknown_devices"] = unknown
	}
	if files, err := renderAsteriskTrunks(trunks); err == nil {
		payload["preview"] = redactPasswords(files.objects)
		payload["routes_preview"] = files.routes
		payload["pending"] = s.asteriskFileDiffers(s.asteriskTrunksPath(), files.objects) ||
			s.asteriskFileDiffers(s.asteriskTrunkRoutesPath(), files.routes)
	} else {
		payload["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
}

// asteriskFileDiffers reports whether a generated file on disk is out of step
// with what the stored configuration renders to, which is what the Apply
// button acts on.
func (s *Server) asteriskFileDiffers(path, rendered string) bool {
	if path == "" {
		return false
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return string(current) != rendered
}

// handlePutAsteriskTrunks replaces the trunk list.
//
// A trunk with no password keeps the one already stored, which is what makes
// the credential write-only usable: the browser never had it, so it cannot
// send it back, and every edit to a destination list would otherwise wipe the
// credential it never saw.
func (s *Server) handlePutAsteriskTrunks(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Trunks []asteriskTrunk `json:"trunks"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// Both credentials are kept, and independently: an operator rotating the
	// outbound one must not have the inbound one wiped for being blank.
	type storedSecrets struct{ inbound, outbound string }
	existing := map[string]storedSecrets{}
	for _, trunk := range s.storedAsteriskTrunks(r.Context()) {
		existing[strings.ToLower(strings.TrimSpace(trunk.Name))] = storedSecrets{
			inbound: trunk.Password, outbound: trunk.OutboundPassword,
		}
	}
	merged := make([]asteriskTrunk, 0, len(request.Trunks))
	for _, trunk := range request.Trunks {
		// The has_* fields are output markers; they must never be stored.
		trunk.HasPassword = false
		trunk.HasOutboundPassword = false
		previous := existing[strings.ToLower(strings.TrimSpace(trunk.Name))]
		// A blank password keeps the stored one, which is what makes the
		// field write-only usable. Clearing the *username* is the way to
		// remove the credential altogether: without that there is no way
		// back, and a trunk left with a stored password and no username
		// fails validation on every subsequent save.
		if strings.TrimSpace(trunk.Username) == "" {
			trunk.Password = ""
		} else if trunk.Password == "" {
			trunk.Password = previous.inbound
		}
		if strings.TrimSpace(trunk.OutboundUsername) == "" {
			trunk.OutboundPassword = ""
		} else if trunk.OutboundPassword == "" {
			trunk.OutboundPassword = previous.outbound
		}
		if trunk.Port == 0 {
			trunk.Port = asteriskconf.DefaultTrunkPort
		}
		if strings.TrimSpace(trunk.Transport) == "" {
			trunk.Transport = "udp"
		}
		if trunk.MaxConcurrent == 0 {
			trunk.MaxConcurrent = asteriskconf.DefaultTrunkConcurrent
		}
		// The only numeric field that had no default, so a body omitting it
		// was refused while port, transport and the cap were filled in.
		if trunk.TimeoutSeconds == 0 {
			trunk.TimeoutSeconds = asteriskconf.DefaultTrunkTimeoutSeconds
		}
		merged = append(merged, trunk)
	}

	// Render before storing. A rejected trunk must not be saved, or the page
	// would show a peer that can never be applied.
	files, err := renderAsteriskTrunks(merged)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trunk", err.Error())
		return
	}
	body, err := json.Marshal(merged)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not encode trunks")
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
		Key: asteriskTrunksKey, Value: body,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	written := false
	if s.asteriskDialplanDir != "" {
		if err := s.writeAsteriskTrunkFiles(files); err != nil {
			// Saved but not written: report it rather than claim success, or
			// Apply would reload a file that never changed.
			s.logger.Error("could not write the Asterisk trunk files",
				"category", "siptrunk", "error", err)
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = true
	}
	// Deleting a trunk a forward plan points at leaves inbound.conf dialling an
	// endpoint that no longer exists. Reading the plan already falls back in
	// that case, but the file on disk does not rewrite itself, so every call
	// on every SIM would keep dialling the dead trunk until something else
	// happened to save the extensions.
	s.rewriteInboundAfterTrunkChange(r.Context(), merged)

	s.recordAudit(r.Context(), "admin", "asterisk.trunks.save", "asterisk", "trunks", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"saved": true, "written": written,
		"trunks":         withoutTrunkPasswords(merged),
		"preview":        redactPasswords(files.objects),
		"routes_preview": files.routes,
	}})
}

// writeAsteriskTrunkFiles writes the PJSIP objects 0600 because they may hold
// credentials, and the dial plan 0644 because it holds none.
func (s *Server) writeAsteriskTrunkFiles(files trunkFiles) error {
	if path := s.asteriskTrunksPath(); path != "" {
		if err := writeAsteriskSecretFile(path, files.objects); err != nil {
			return err
		}
	}
	if path := s.asteriskTrunkRoutesPath(); path != "" {
		if err := writeAsteriskRoutesFile(path, files.routes); err != nil {
			return err
		}
	}
	return nil
}

// handleApplyAsteriskTrunks reloads both modules, because one save writes a
// PJSIP object list and a dial plan.
func (s *Server) handleApplyAsteriskTrunks(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.asteriskAMI.Address) == "" {
		writeError(w, http.StatusNotImplemented, "ami_not_configured",
			"the Asterisk manager interface is not configured, so VoCat cannot reload Asterisk; "+
				"run `docker compose restart asterisk` instead")
		return
	}
	trunks := s.storedAsteriskTrunks(r.Context())
	files, err := renderAsteriskTrunks(trunks)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trunk", err.Error())
		return
	}
	// Re-render and rewrite first: applying what is on disk when it differs
	// from what is stored would reload a stale configuration and report
	// success.
	if s.asteriskDialplanDir != "" {
		if err := s.writeAsteriskTrunkFiles(files); err != nil {
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

	// res_pjsip first: a peer that exists but whose context is not yet
	// reloaded reaches a context that does not answer, which is a smaller
	// window than a context that answers for a peer Asterisk does not know.
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
	s.recordAudit(r.Context(), "admin", "asterisk.trunks.apply", "asterisk", "trunks", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"applied": true,
		"at":      time.Now().UTC().Format(time.RFC3339),
	}})
}

// unknownTrunkDevices reports SIM names a trunk uses that match no configured
// device.
func (s *Server) unknownTrunkDevices(ctx context.Context, trunks []asteriskTrunk) []string {
	if len(trunks) == 0 {
		return nil
	}
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		// Reporting every SIM as unknown because the listing failed would be
		// worse than reporting none: the page would cry wolf about a
		// configuration that is fine.
		return nil
	}
	known := make(map[string]bool, len(configs)*2)
	for _, config := range configs {
		known[strings.ToLower(config.ID)] = true
		if name := strings.ToLower(strings.TrimSpace(config.Name)); name != "" {
			known[name] = true
		}
	}
	seen := map[string]bool{}
	unknown := []string{}
	for _, trunk := range trunks {
		for _, device := range trunk.Devices {
			device = strings.TrimSpace(device)
			if device == "" {
				continue
			}
			if known[strings.ToLower(device)] || seen[device] {
				continue
			}
			seen[device] = true
			unknown = append(unknown, device)
		}
	}
	sort.Strings(unknown)
	if len(unknown) == 0 {
		return nil
	}
	return unknown
}

// rewriteInboundAfterTrunkChange re-renders the extension files when the
// stored inbound plan forwards to a trunk that the new list no longer has.
//
// Only then: rewriting on every trunk save would touch endpoints.conf, which
// holds SIP passwords, for an edit that has nothing to do with it.
func (s *Server) rewriteInboundAfterTrunkChange(ctx context.Context, trunks []asteriskTrunk) {
	if s.asteriskDialplanDir == "" {
		return
	}
	setting, err := s.store.AppSetting(ctx, asteriskInboundKey)
	if err != nil || len(setting.Value) == 0 {
		return
	}
	var stored asteriskInbound
	if err := json.Unmarshal(setting.Value, &stored); err != nil {
		return
	}
	if asteriskconf.InboundMode(strings.TrimSpace(stored.Mode)) != asteriskconf.InboundForward {
		return
	}
	want := strings.ToLower(strings.TrimSpace(stored.ForwardTrunk))
	for _, trunk := range trunks {
		if strings.ToLower(strings.TrimSpace(trunk.Name)) == want {
			return
		}
	}

	extensions := s.storedAsteriskExtensions(ctx)
	plan := s.asteriskInboundPlan(ctx, toConfigExtensions(extensions))
	files, err := renderAsteriskExtensions(extensions, plan, s.asteriskSMSMode(ctx), s.asteriskTrunkHost())
	if err != nil {
		s.logger.Error("could not re-render the inbound dialplan after a trunk was removed",
			"category", "siptrunk", "error", err)
		return
	}
	if err := s.writeAsteriskExtensionFiles(files); err != nil {
		s.logger.Error("could not rewrite the inbound dialplan after a trunk was removed",
			"category", "siptrunk", "error", err)
		return
	}
	s.logger.Warn("a forwarded inbound plan lost its trunk; inbound routing fell back",
		"category", "siptrunk", "trunk", strings.TrimSpace(stored.ForwardTrunk))
}
