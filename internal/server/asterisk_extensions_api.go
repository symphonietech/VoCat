package server

import (
	"context"
	"encoding/json"
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
	rendered, err := asteriskconf.RenderExtensions(toConfigExtensions(extensions))
	if err != nil {
		payload["error"] = err.Error()
	} else {
		payload["preview"] = redactPasswords(rendered)
	}
	current, readErr := os.ReadFile(s.asteriskEndpointsPath())
	switch {
	case s.asteriskEndpointsPath() == "":
	case readErr != nil:
		payload["pending"] = true
	default:
		payload["pending"] = err == nil && string(current) != rendered
		// A file Asterisk is using that VoCat did not write is the account
		// the container seeded from the environment on first start. Saving
		// here replaces it, so the page has to say so before it happens
		// rather than after the handset stops registering.
		payload["seeded"] = !strings.HasPrefix(string(current), asteriskconf.GeneratedHeader)
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
	rendered, err := asteriskconf.RenderExtensions(toConfigExtensions(merged))
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
	if path := s.asteriskEndpointsPath(); path != "" {
		if err := writeAsteriskSecretFile(path, rendered); err != nil {
			s.logger.Error("could not write the Asterisk endpoints file",
				"category", "siptrunk", "path", path, "error", err)
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = true
	}
	s.recordAudit(r.Context(), "admin", "asterisk.extensions.save", "asterisk", "extensions", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"saved": true, "written": written,
		"extensions": withoutPasswords(merged),
		"preview":    redactPasswords(rendered),
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
			"the Asterisk manager interface is not configured, so VoCat cannot reload PJSIP; "+
				"run `docker compose restart asterisk` instead")
		return
	}
	extensions := s.storedAsteriskExtensions(r.Context())
	rendered, err := asteriskconf.RenderExtensions(toConfigExtensions(extensions))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_extension", err.Error())
		return
	}
	if path := s.asteriskEndpointsPath(); path != "" {
		if err := writeAsteriskSecretFile(path, rendered); err != nil {
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

	response, err := conn.Action(ctx, "Reload", ami.Message{"Module": "res_pjsip"})
	if err != nil {
		writeError(w, http.StatusBadGateway, "reload_failed", err.Error())
		return
	}
	if !strings.EqualFold(response.Get("Response"), "Success") {
		writeError(w, http.StatusBadGateway, "reload_failed", response.First("Message", "Response"))
		return
	}
	s.recordAudit(r.Context(), "admin", "asterisk.extensions.apply", "asterisk", "extensions", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"applied": true,
		"message": response.First("Message"),
		"at":      time.Now().UTC().Format(time.RFC3339),
		// A phone re-registers on its own timer, so an account whose password
		// just changed goes away and comes back rather than failing visibly.
		"note": "phones re-register on their own timer; a changed password shows up at the next one",
	}})
}
