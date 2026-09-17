import { useEffect, useRef, useState } from "react";
import { callMediaProbeURL, callMediaURL } from "../api";

export type CallAudioState = "idle" | "connecting" | "live" | "error";

export interface CallAudioStatus {
  state: CallAudioState;
  error: string;
  /** Frames received from and sent to the bridge — the proof that both directions flow. */
  received: number;
  sent: number;
  /** Peak amplitude 0..1 over the last sampling window, for the level meters. */
  rxLevel: number;
  txLevel: number;
}

const IDLE: CallAudioStatus = { state: "idle", error: "", received: 0, sent: 0, rxLevel: 0, txLevel: 0 };

// The bridge speaks 8 kHz mono; Web Audio resamples to the hardware rate for us
// when an AudioBuffer declares this rate.
const BRIDGE_RATE = 8000;
// Scheduling lead. RTP arrives in 20 ms packets over a WebSocket, so a little
// queued audio absorbs network and event-loop jitter; too much just adds delay.
const JITTER_SECONDS = 0.06;

/**
 * useCallAudio bridges a live VoWiFi call's audio to the browser over the
 * WebSocket exposed by internal/server/call_media_api.go. Each message is
 * little-endian signed 16-bit, 8 kHz, mono PCM in both directions.
 *
 * The connection carries the session cookie and is same-origin checked by the
 * server, so it needs no token handling here.
 */
export function useCallAudio(
  deviceId: string,
  callId: string,
  enabled: boolean,
  micEnabled: boolean,
): CallAudioStatus {
  const [status, setStatus] = useState<CallAudioStatus>(IDLE);
  // Counters live in a ref so packet arrival (50/s) never triggers a render;
  // an interval samples them into state at a rate a human can read.
  const countersRef = useRef({ received: 0, sent: 0, rxPeak: 0, txPeak: 0 });
  const micRef = useRef(micEnabled);
  micRef.current = micEnabled;

  useEffect(() => {
    if (!enabled || !deviceId || !callId) {
      setStatus(IDLE);
      return;
    }

    let disposed = false;
    let context: AudioContext | null = null;
    let socket: WebSocket | null = null;
    let stream: MediaStream | null = null;
    let capture: AudioWorkletNode | null = null;
    let source: MediaStreamAudioSourceNode | null = null;
    let sink: GainNode | null = null;
    let output: GainNode | null = null;
    let playCursor = 0;

    countersRef.current = { received: 0, sent: 0, rxPeak: 0, txPeak: 0 };
    setStatus({ ...IDLE, state: "connecting" });

    const fail = (message: string) => {
      if (disposed) return;
      setStatus((previous) => ({ ...previous, state: "error", error: message }));
    };

    const start = async () => {
      context = new AudioContext();
      // Autoplay policy: the caller only enables this from a click, so the
      // context is resumable here.
      if (context.state === "suspended") await context.resume();
      output = context.createGain();
      output.connect(context.destination);

      socket = new WebSocket(callMediaURL(deviceId, callId));
      socket.binaryType = "arraybuffer";

      socket.onopen = () => {
        if (!disposed) setStatus((previous) => ({ ...previous, state: "live", error: "" }));
      };
      // A close carries no useful reason here (the server closes normally when
      // the call ends), so treat it as the end of audio rather than an error.
      socket.onclose = () => {
        if (!disposed) setStatus((previous) => ({ ...previous, state: previous.state === "error" ? "error" : "idle" }));
      };
      // The WebSocket API deliberately withholds the HTTP status from script,
      // so a refused upgrade surfaces as an eventless error. The server does
      // explain itself though -- 501 when IMS is not registered, 409 when the
      // call or its media is gone, 400 for a bad call id -- so ask the same
      // URL over plain HTTP and report what it says instead of "failed".
      socket.onerror = () => {
        fetch(callMediaProbeURL(deviceId, callId), { credentials: "include" })
          .then(async (response) => {
            const body = await response.json().catch(() => null);
            const detail = body?.error?.message || body?.message || "";
            fail(
              detail
                ? `media bridge refused (HTTP ${response.status}): ${detail}`
                : `media bridge refused (HTTP ${response.status})`,
            );
          })
          .catch(() => fail("media bridge unreachable"));
      };

      socket.onmessage = (event) => {
        if (!context || !output || !(event.data instanceof ArrayBuffer)) return;
        const pcm = new Int16Array(event.data);
        if (pcm.length === 0) return;
        const samples = new Float32Array(pcm.length);
        let peak = 0;
        for (let index = 0; index < pcm.length; index += 1) {
          const value = pcm[index] / 0x8000;
          samples[index] = value;
          const magnitude = value < 0 ? -value : value;
          if (magnitude > peak) peak = magnitude;
        }
        const counters = countersRef.current;
        counters.received += 1;
        if (peak > counters.rxPeak) counters.rxPeak = peak;

        const buffer = context.createBuffer(1, samples.length, BRIDGE_RATE);
        buffer.copyToChannel(samples, 0);
        const node = context.createBufferSource();
        node.buffer = buffer;
        node.connect(output);
        // Re-arm the cursor whenever it has fallen behind — first packet, or
        // after an underrun — so late audio plays now instead of compounding
        // the delay for the rest of the call.
        const now = context.currentTime;
        if (playCursor < now + 0.005) playCursor = now + JITTER_SECONDS;
        node.start(playCursor);
        playCursor += buffer.duration;
      };

      // Uplink is best-effort: without a microphone the downlink still plays,
      // so the call stays half-usable rather than failing outright.
      //
      // getUserMedia is gated on a secure context, and on an insecure origin
      // navigator.mediaDevices is not merely restricted but absent — so the
      // call below would throw a TypeError that reads like a missing device.
      // Name the real cause instead: this is the single most likely reason for
      // a working downlink with no uplink, and it is a deployment setting
      // rather than anything wrong with the call.
      if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) {
        fail(
          "microphone blocked: the page is not a secure context, so the browser " +
            "withholds it. Reach VoCat over HTTPS (Settings turns on a self-signed " +
            "certificate) or through http://localhost, which counts as secure. " +
            "Received audio keeps working.",
        );
        return;
      }
      try {
        stream = await navigator.mediaDevices.getUserMedia({
          audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
        });
      } catch (error) {
        // A denial, a device in use by another application, or no input at all.
        fail(
          "microphone unavailable, so nothing is sent: " +
            (error instanceof Error ? error.message : String(error)),
        );
        return;
      }
      if (disposed) return;

      await context.audioWorklet.addModule("/pcm-capture.js");
      if (disposed) return;

      source = context.createMediaStreamSource(stream);
      // The worklet must have an output and a path to the destination: a node
      // that cannot reach the destination is not guaranteed to be pulled by the
      // rendering graph, and a capture node with numberOfOutputs: 0 never can.
      // It writes nothing to that output, and the gain below is zero, so
      // nothing of the microphone is played back locally.
      capture = new AudioWorkletNode(context, "pcm-capture", {
        numberOfInputs: 1,
        numberOfOutputs: 1,
        outputChannelCount: [1],
      });
      capture.port.onmessage = (event) => {
        const frame = event.data as Int16Array;
        if (!micRef.current || !socket || socket.readyState !== WebSocket.OPEN) return;
        let peak = 0;
        for (let index = 0; index < frame.length; index += 1) {
          const magnitude = Math.abs(frame[index]) / 0x8000;
          if (magnitude > peak) peak = magnitude;
        }
        const counters = countersRef.current;
        counters.sent += 1;
        if (peak > counters.txPeak) counters.txPeak = peak;
        socket.send(frame.buffer);
      };
      sink = context.createGain();
      sink.gain.value = 0;
      sink.connect(context.destination);
      source.connect(capture);
      capture.connect(sink);
    };

    start().catch((error: unknown) => fail(error instanceof Error ? error.message : String(error)));

    const meter = window.setInterval(() => {
      const counters = countersRef.current;
      setStatus((previous) => ({
        ...previous,
        received: counters.received,
        sent: counters.sent,
        rxLevel: counters.rxPeak,
        txLevel: counters.txPeak,
      }));
      counters.rxPeak = 0;
      counters.txPeak = 0;
    }, 200);

    return () => {
      disposed = true;
      window.clearInterval(meter);
      if (capture) capture.port.onmessage = null;
      capture?.disconnect();
      source?.disconnect();
      sink?.disconnect();
      output?.disconnect();
      stream?.getTracks().forEach((track) => track.stop());
      if (socket && socket.readyState <= WebSocket.OPEN) socket.close();
      void context?.close();
    };
  }, [deviceId, callId, enabled]);

  return status;
}
