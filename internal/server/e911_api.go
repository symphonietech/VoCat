package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"vocat/internal/store"
)

// websheetSession is a short-lived, token-authenticated E911 address
// provisioning session. The operator/carrier-hosted websheet VoHive embeds
// cannot be reproduced, so the backend self-hosts a minimal address form and
// relays the result into the device's VoWiFi record.
type websheetSession struct {
	id        string
	token     string
	deviceID  string
	createdAt time.Time
	expiresAt time.Time
	address   map[string]string
	done      bool
}

type websheetManager struct {
	mu       sync.Mutex
	sessions map[string]*websheetSession
}

func newWebsheetManager() *websheetManager {
	return &websheetManager{sessions: make(map[string]*websheetSession)}
}

func websheetToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (m *websheetManager) create(deviceID string) *websheetSession {
	now := time.Now().UTC()
	session := &websheetSession{
		id:        websheetToken()[:16],
		token:     websheetToken(),
		deviceID:  deviceID,
		createdAt: now,
		expiresAt: now.Add(15 * time.Minute),
	}
	m.mu.Lock()
	m.sessions[session.id] = session
	// Opportunistically drop expired sessions.
	for id, value := range m.sessions {
		if now.After(value.expiresAt) {
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	return session
}

func (m *websheetManager) get(id string) *websheetSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil || time.Now().UTC().After(session.expiresAt) {
		return nil
	}
	return session
}

// handleE911Websheet creates a self-hosted E911 websheet session for a device
// and returns the embeddable form URL (VoHive: POST /devices/{id}/vowifi/e911/websheet).
func (s *Server) handleE911Websheet(
	w http.ResponseWriter,
	r *http.Request,
	config store.Device,
) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	session := s.websheets.create(config.ID)
	embedURL := fmt.Sprintf("/websheets/%s?token=%s", session.id, session.token)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"id":         session.id,
			"token":      session.token,
			"embed_url":  embedURL,
			"expires_at": session.expiresAt,
		},
	})
	return true
}

// handleWebsheet serves and drives the self-hosted E911 address form. These
// paths are token-authenticated (the token in the URL is the credential), so
// they live outside the session-gated /api tree.
//
//	GET  /websheets/{id}?token=...        -> the address form
//	POST /websheets/{id}/callback         -> relay the entered address
//	POST /websheets/{id}/done             -> complete the session
func (s *Server) handleWebsheet(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/websheets/"), "/")
	segments := splitAPIPath(rest)
	if len(segments) == 0 || segments[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "websheet was not found")
		return
	}
	session := s.websheets.get(segments[0])
	if session == nil {
		writeError(w, http.StatusNotFound, "websheet_not_found", "websheet session was not found or has expired")
		return
	}
	action := ""
	if len(segments) > 1 {
		action = segments[1]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.serveWebsheetForm(w, r, session)
	case action == "callback" && r.Method == http.MethodPost:
		s.handleWebsheetCallback(w, r, session)
	case action == "done" && r.Method == http.MethodPost:
		s.handleWebsheetDone(w, r, session)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) websheetTokenOK(r *http.Request, session *websheetSession) bool {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Websheet-Token")
	}
	return token != "" && token == session.token
}

func (s *Server) serveWebsheetForm(w http.ResponseWriter, r *http.Request, session *websheetSession) {
	if !s.websheetTokenOK(r, session) {
		writeError(w, http.StatusForbidden, "invalid_token", "websheet token is invalid")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, websheetFormHTML(session))
}

func (s *Server) handleWebsheetCallback(w http.ResponseWriter, r *http.Request, session *websheetSession) {
	if !s.websheetTokenOK(r, session) {
		writeError(w, http.StatusForbidden, "invalid_token", "websheet token is invalid")
		return
	}
	var address map[string]string
	if err := s.decodeJSON(w, r, &address); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	clean := make(map[string]string, len(address))
	for key, value := range address {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" && len(trimmed) <= 256 {
			clean[key] = trimmed
		}
	}
	s.websheets.mu.Lock()
	session.address = clean
	s.websheets.mu.Unlock()
	s.persistE911Address(r, session)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"received": true}})
}

func (s *Server) handleWebsheetDone(w http.ResponseWriter, r *http.Request, session *websheetSession) {
	if !s.websheetTokenOK(r, session) {
		writeError(w, http.StatusForbidden, "invalid_token", "websheet token is invalid")
		return
	}
	s.websheets.mu.Lock()
	session.done = true
	s.websheets.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"done": true}})
}

// persistE911Address stores the provisioned E911 address against the device so
// the VoWiFi IMS registration can reference it later.
func (s *Server) persistE911Address(r *http.Request, session *websheetSession) {
	payload, err := json.Marshal(session.address)
	if err != nil {
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
		Key:   "e911_address:" + session.deviceID,
		Value: payload,
	}); err != nil {
		s.logger.Warn("persist e911 address failed", "device_id", session.deviceID, "error", err)
	}
}

// websheetFormHTML renders the self-contained E911 address form embedded by the
// frontend. On submit it relays the address to the callback, notifies the
// parent frame (vohive-websheet-callback), and completes the session.
func websheetFormHTML(session *websheetSession) string {
	callbackURL := fmt.Sprintf("/websheets/%s/callback?token=%s", session.id, session.token)
	doneURL := fmt.Sprintf("/websheets/%s/done?token=%s", session.id, session.token)
	callbackJSON, _ := json.Marshal(callbackURL)
	doneJSON, _ := json.Marshal(doneURL)
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>E911 Address</title>
<style>
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#f6f7f9;margin:0;padding:20px;color:#111}
.card{max-width:430px;margin:0 auto;background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:20px}
h1{font-size:16px;margin:0 0 4px}p.sub{font-size:12px;color:#6b7280;margin:0 0 16px}
label{display:block;font-size:12px;color:#4b5563;margin:10px 0 4px}
input{width:100%;height:34px;padding:0 10px;border:1px solid #dcdfe6;border-radius:8px;font-size:13px;box-sizing:border-box}
input:focus{outline:none;border-color:#5b5bd6;box-shadow:0 0 0 3px rgba(91,91,214,.18)}
button{margin-top:16px;width:100%;height:38px;border:0;border-radius:8px;background:#5b5bd6;color:#fff;font-size:14px;font-weight:600;cursor:pointer}
button:disabled{opacity:.6;cursor:not-allowed}.row{display:flex;gap:10px}.row>div{flex:1}
.msg{margin-top:12px;font-size:13px;text-align:center}.ok{color:#16a34a}.err{color:#dc2626}
</style></head><body>
<div class="card">
<h1>E911 紧急地址登记</h1>
<p class="sub">为 VoWiFi 服务登记紧急呼叫地址（Emergency Address）。</p>
<form id="f">
<label>姓名 / Name</label><input name="name" autocomplete="name">
<label>街道地址 / Street</label><input name="street" required autocomplete="street-address">
<label>城市 / City</label><input name="city" required>
<div class="row"><div><label>州/省 / State</label><input name="state"></div>
<div><label>邮编 / ZIP</label><input name="zip"></div></div>
<label>国家 / Country</label><input name="country" required>
<button type="submit" id="btn">提交地址</button>
<div class="msg" id="msg"></div>
</form></div>
<script>
var CALLBACK=` + string(callbackJSON) + `,DONE=` + string(doneJSON) + `;
document.getElementById('f').addEventListener('submit',async function(e){
e.preventDefault();var btn=document.getElementById('btn'),msg=document.getElementById('msg');
btn.disabled=true;msg.textContent='';var data={};
new FormData(e.target).forEach(function(v,k){data[k]=v});
try{
var r=await fetch(CALLBACK,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});
if(!r.ok)throw new Error('callback '+r.status);
try{if(window.parent)window.parent.postMessage({type:'vohive-websheet-callback',address:data},'*')}catch(_){}
await fetch(DONE,{method:'POST'}).catch(function(){});
msg.className='msg ok';msg.textContent='地址已提交 / Address saved';
}catch(err){btn.disabled=false;msg.className='msg err';msg.textContent='提交失败，请重试 / Submit failed';}
});
</script></body></html>`
}
