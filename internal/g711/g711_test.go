package g711

import (
	"math"
	"testing"
)

// G.711 is lossy by design: 16-bit samples are companded into 8 bits. What
// matters is that a decoded sample stays close to the original across the
// range, and that the sign survives -- a sign error inverts the waveform and
// sounds like distortion rather than silence.
func TestRoundTripStaysWithinCompandingError(t *testing.T) {
	for name, codec := range map[string]struct {
		encode    func(int16) byte
		decode    func(byte) int16
		tolerance float64
	}{
		"mu-law": {LinearToMuLaw, MuLawToLinear, 0.10},
		"A-law":  {LinearToALaw, ALawToLinear, 0.10},
	} {
		for sample := -32768; sample <= 32767; sample += 7 {
			original := int16(sample)
			decoded := codec.decode(codec.encode(original))
			if original > 64 && decoded < 0 || original < -64 && decoded > 0 {
				t.Fatalf("%s: sample %d decoded to %d with the sign flipped", name, original, decoded)
			}
			magnitude := math.Abs(float64(original))
			if magnitude < 256 {
				continue // quantisation dominates near zero; relative error is meaningless
			}
			difference := math.Abs(float64(decoded) - float64(original))
			if difference/magnitude > codec.tolerance {
				t.Fatalf("%s: sample %d decoded to %d, %.1f%% error",
					name, original, decoded, 100*difference/magnitude)
			}
		}
	}
}

func TestSilenceEncodesToTheStandardIdleBytes(t *testing.T) {
	// The bytes a G.711 stream carries when nothing is being said. Getting
	// these wrong makes "silence" audible as a tone or hiss.
	if got := LinearToMuLaw(0); got != 0xFF {
		t.Fatalf("mu-law silence is %#x, want 0xFF", got)
	}
	if got := LinearToALaw(0); got != 0xD5 {
		t.Fatalf("A-law silence is %#x, want 0xD5", got)
	}
	if got := MuLawToLinear(0xFF); got < -8 || got > 8 {
		t.Fatalf("mu-law 0xFF decodes to %d, want near zero", got)
	}
	if got := ALawToLinear(0xD5); got < -8 || got > 8 {
		t.Fatalf("A-law 0xD5 decodes to %d, want near zero", got)
	}
}

// Every byte must decode to something, since the wire delivers all 256.
func TestEveryEncodedByteDecodes(t *testing.T) {
	for value := 0; value < 256; value++ {
		_ = MuLawToLinear(byte(value))
		_ = ALawToLinear(byte(value))
	}
}
