// AudioWorklet that turns microphone input into the frames VoCat's call media
// bridge expects: little-endian signed 16-bit, 8 kHz, mono.
//
// It lives in public/ rather than being generated at runtime because the app is
// served under `script-src 'self'` (internal/server/server.go), which blocks a
// blob: worklet module. Vite copies public/ verbatim into dist/, and dist/ is
// go:embed-ed by web/embed.go, so this ships inside the binary.

const TARGET_RATE = 8000;
// 160 samples at 8 kHz is 20 ms, matching the a=ptime:20 VoCat offers, so one
// posted frame maps to one RTP packet instead of fragmenting across them.
const FRAME_SAMPLES = 160;

class PCMCaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    // `sampleRate` is an AudioWorkletGlobalScope global — the real hardware
    // rate, which is 48000 on most devices but never guaranteed.
    this.step = sampleRate / TARGET_RATE;
    this.pending = new Float32Array(0);
    this.position = 0;
    this.frame = new Int16Array(FRAME_SAMPLES);
    this.filled = 0;
  }

  process(inputs) {
    const channel = inputs[0] && inputs[0][0];
    // No input connected yet: stay alive rather than ending the processor.
    if (!channel || channel.length === 0) return true;

    const merged = new Float32Array(this.pending.length + channel.length);
    merged.set(this.pending, 0);
    merged.set(channel, this.pending.length);

    // Box-filter decimation. Averaging across the whole source span (rather
    // than picking one sample per step) is a crude low-pass, which keeps
    // content above 4 kHz from aliasing down into the voice band. The ratio is
    // fractional for 44.1 kHz hardware, so the read position is carried across
    // render quanta instead of being reset each block.
    let position = this.position;
    while (position + this.step <= merged.length) {
      const start = Math.floor(position);
      const end = Math.floor(position + this.step);
      let sum = 0;
      let count = 0;
      for (let index = start; index < end && index < merged.length; index += 1) {
        sum += merged[index];
        count += 1;
      }
      const value = count > 0 ? Math.max(-1, Math.min(1, sum / count)) : 0;
      // Asymmetric scaling: int16 runs -32768..32767, so a full-scale negative
      // sample would clip if it used the positive multiplier.
      this.frame[this.filled] = value < 0 ? value * 0x8000 : value * 0x7fff;
      this.filled += 1;
      if (this.filled === FRAME_SAMPLES) {
        const copy = this.frame.slice(0);
        this.port.postMessage(copy, [copy.buffer]);
        this.filled = 0;
      }
      position += this.step;
    }

    const consumed = Math.floor(position);
    this.pending = merged.slice(consumed);
    this.position = position - consumed;
    return true;
  }
}

registerProcessor("pcm-capture", PCMCaptureProcessor);
