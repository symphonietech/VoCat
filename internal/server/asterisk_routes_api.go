package server

import (
	"context"
	"encoding/json"
	"fmt"
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

// asteriskRoutesKey holds the route list in the app settings table. Routes are
// edited as one ordered document rather than row by row, so a key/value entry
// fits better than a table and needs no migration.
const asteriskRoutesKey = "asterisk.routes"

// routesFileName is written into the directory VoCat and Asterisk share. The
// shipped extensions.conf includes it.
const routesFileName = "routes.conf"

type asteriskRoute struct {
	Pattern        string   `json:"pattern"`
	Devices        []string `json:"devices"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Comment        string   `json:"comment,omitempty"`
}

func (r asteriskRoute) toConfig() asteriskconf.Route {
	return asteriskconf.Route{
		Pattern:        strings.TrimSpace(r.Pattern),
		Devices:        r.Devices,
		TimeoutSeconds: r.TimeoutSeconds,
		Comment:        strings.TrimSpace(r.Comment),
	}
}

func toConfigRoutes(routes []asteriskRoute) []asteriskconf.Route {
	out := make([]asteriskconf.Route, 0, len(routes))
	for _, route := range routes {
		out = append(out, route.toConfig())
	}
	return out
}

func (s *Server) routeAsteriskRoutesAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	switch cleanPath {
	case "asterisk/routes":
		switch r.Method {
		case http.MethodGet:
			s.handleGetAsteriskRoutes(w, r)
		case http.MethodPut:
			s.handlePutAsteriskRoutes(w, r)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return true
	case "asterisk/routes/apply":
		if requireMethod(w, r, http.MethodPost) {
			s.handleApplyAsteriskRoutes(w, r)
		}
		return true
	}
	return false
}

// storedAsteriskRoutes reads the saved list. A missing or unreadable setting
// is an empty list rather than an error: the page must render so the operator
// can create the first route.
func (s *Server) storedAsteriskRoutes(ctx context.Context) []asteriskRoute {
	setting, err := s.store.AppSetting(ctx, asteriskRoutesKey)
	if err != nil || len(setting.Value) == 0 {
		return nil
	}
	var routes []asteriskRoute
	if err := json.Unmarshal(setting.Value, &routes); err != nil {
		s.logger.Warn("stored Asterisk routes are unreadable",
			"category", "siptrunk", "error", err)
		return nil
	}
	return routes
}

func (s *Server) handleGetAsteriskRoutes(w http.ResponseWriter, r *http.Request) {
	routes := s.storedAsteriskRoutes(r.Context())
	if routes == nil {
		routes = []asteriskRoute{}
	}
	payload := map[string]any{
		"routes":    routes,
		"path":      s.asteriskRoutesPath(),
		"writable":  s.asteriskRoutesPath() != "",
		"can_apply": strings.TrimSpace(s.asteriskAMI.Address) != "",
	}
	// A route naming a SIM that has been removed is the one failure this page
	// cannot otherwise show: the dialplan is valid, Apply succeeds, and the
	// call fails at dial time with an error only the caller hears.
	if unknown := s.unknownRouteDevices(r.Context(), routes); len(unknown) > 0 {
		payload["unknown_devices"] = unknown
	}
	// The preview is what would be written. Showing it means a syntax
	// question can be answered without shelling into the container.
	if rendered, err := asteriskconf.RenderRoutes(toConfigRoutes(routes)); err == nil {
		payload["preview"] = rendered
		payload["pending"] = s.asteriskRoutesDiffer(rendered)
	} else {
		payload["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
}

// asteriskRoutesDiffer reports whether the file on disk is out of step with
// what the stored routes render to, which is what the Apply button acts on.
func (s *Server) asteriskRoutesDiffer(rendered string) bool {
	path := s.asteriskRoutesPath()
	if path == "" {
		return false
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return string(current) != rendered
}

func (s *Server) asteriskRoutesPath() string {
	if strings.TrimSpace(s.asteriskDialplanDir) == "" {
		return ""
	}
	return filepath.Join(s.asteriskDialplanDir, routesFileName)
}

func (s *Server) handlePutAsteriskRoutes(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Routes []asteriskRoute `json:"routes"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// Render before storing. A rejected route must not be saved, or the page
	// would show a rule that can never be applied.
	rendered, err := asteriskconf.RenderRoutes(toConfigRoutes(request.Routes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_route", err.Error())
		return
	}
	body, err := json.Marshal(request.Routes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not encode routes")
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
		Key: asteriskRoutesKey, Value: body,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	written := false
	if path := s.asteriskRoutesPath(); path != "" {
		if err := writeAsteriskRoutesFile(path, rendered); err != nil {
			// Saved but not written: report it rather than claim success, or
			// Apply would reload a file that never changed.
			s.logger.Error("could not write the Asterisk routes file",
				"category", "siptrunk", "path", path, "error", err)
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = true
	}
	s.recordAudit(r.Context(), "admin", "asterisk.routes.save", "asterisk", "routes", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"saved": true, "written": written, "routes": request.Routes, "preview": rendered,
	}})
}

// writeAsteriskRoutesFile replaces the file atomically. Asterisk may read it
// at any moment -- a qualify, an unrelated reload -- and a half-written
// dialplan is a broken one.
func writeAsteriskRoutesFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", temporary, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// handleApplyAsteriskRoutes asks Asterisk to reload the dialplan.
//
// It uses the Reload action rather than Command: Command runs arbitrary CLI
// inside the PBX container, which is shell-equivalent, and the manager
// account is deliberately not granted it.
func (s *Server) handleApplyAsteriskRoutes(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.asteriskAMI.Address) == "" {
		writeError(w, http.StatusNotImplemented, "ami_not_configured",
			"the Asterisk manager interface is not configured, so VoCat cannot reload the dialplan; "+
				"run `docker compose restart asterisk` instead")
		return
	}
	// Re-render and rewrite first: applying what is on disk when it differs
	// from what is stored would reload a stale dialplan and report success.
	routes := s.storedAsteriskRoutes(r.Context())
	rendered, err := asteriskconf.RenderRoutes(toConfigRoutes(routes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_route", err.Error())
		return
	}
	if path := s.asteriskRoutesPath(); path != "" {
		if err := writeAsteriskRoutesFile(path, rendered); err != nil {
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

	// pbx_config owns the static dialplan, so reloading it alone leaves calls
	// in progress and every other module untouched.
	response, err := conn.Action(ctx, "Reload", ami.Message{"Module": "pbx_config"})
	if err != nil {
		writeError(w, http.StatusBadGateway, "reload_failed", err.Error())
		return
	}
	if !strings.EqualFold(response.Get("Response"), "Success") {
		writeError(w, http.StatusBadGateway, "reload_failed",
			response.First("Message", "Response"))
		return
	}
	s.recordAudit(r.Context(), "admin", "asterisk.routes.apply", "asterisk", "routes", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"applied": true,
		"message": response.First("Message"),
		"at":      time.Now().UTC().Format(time.RFC3339),
	}})
}

// unknownRouteDevices reports device names a route uses that match no
// configured device.
//
// Matched the same way the trunk gateway matches at dial time -- by ID or by
// name, case-insensitively on the name -- so this says exactly what a real
// call would find, rather than a second opinion that can disagree with it.
func (s *Server) unknownRouteDevices(ctx context.Context, routes []asteriskRoute) []string {
	if len(routes) == 0 {
		return nil
	}
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		// Reporting every device as unknown because the listing failed would
		// be worse than reporting none: the page would cry wolf about a
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
	for _, route := range routes {
		for _, device := range route.Devices {
			device = strings.TrimSpace(device)
			// "*" is every device rather than a name, and resolves to
			// whatever is registered at the time.
			if device == "" || device == "*" {
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
