package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"vocat/internal/store"
)

const maxCallMediaMessage = 16 << 10

// handleCallMedia upgrades an authenticated same-origin request to a binary
// PCM bridge. Each WebSocket message contains little-endian signed 16-bit,
// 8 kHz, mono samples. RTP and codec details remain inside the IMS provider.
func (s *Server) handleCallMedia(w http.ResponseWriter, r *http.Request, config store.Device) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	if s.callTransport(config.ID) != "vowifi" {
		writeError(w, http.StatusNotImplemented, "call_media_unavailable", "browser audio is only available for an active VoWiFi IMS call")
		return true
	}
	callID := strings.TrimSpace(r.URL.Query().Get("call_id"))
	if callID == "" || len(callID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_call_id", "call_id is required")
		return true
	}
	if s.trunkHoldsCall(config.ID, callID) {
		// One RTP bridge per call: a second reader would split the downlink
		// frames and leave the PBX's audio choppy as well as the browser's.
		writeError(w, http.StatusConflict, "call_media_in_use",
			"this call's audio is bridged to the SIP trunk; use the PBX's own client, or hang up from the PBX first")
		return true
	}
	controller, ok := s.vowifi.(VoWiFiCallMediaController)
	if !ok {
		writeError(w, http.StatusNotImplemented, "call_media_unavailable", "the active IMS session does not expose RTP media")
		return true
	}
	media, err := controller.CallMedia(r.Context(), config.ID, callID)
	if err != nil {
		writeError(w, http.StatusConflict, "call_media_unavailable", err.Error())
		return true
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return true
	}
	connection.SetReadLimit(maxCallMediaMessage)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer connection.Close(websocket.StatusNormalClosure, "call media closed")

	downlink := make(chan error, 1)
	go func() {
		defer cancel()
		for {
			samples, readErr := media.ReadPCM(ctx)
			if readErr != nil {
				downlink <- readErr
				return
			}
			payload := make([]byte, len(samples)*2)
			for index, sample := range samples {
				binary.LittleEndian.PutUint16(payload[index*2:], uint16(sample))
			}
			if writeErr := connection.Write(ctx, websocket.MessageBinary, payload); writeErr != nil {
				downlink <- writeErr
				return
			}
		}
	}()

	for {
		select {
		case err := <-downlink:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				s.logger.Debug("call media downlink closed", "device_id", config.ID, "call_id", callID, "error", err)
			}
			return true
		default:
		}
		messageType, payload, readErr := connection.Read(ctx)
		if readErr != nil {
			return true
		}
		if messageType != websocket.MessageBinary || len(payload) == 0 || len(payload)%2 != 0 {
			continue
		}
		samples := make([]int16, len(payload)/2)
		for index := range samples {
			samples[index] = int16(binary.LittleEndian.Uint16(payload[index*2:]))
		}
		if err := media.WritePCM(samples); err != nil {
			return true
		}
	}
}
