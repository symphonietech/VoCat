package siptrunk

import (
	"context"
	"math"
	"net"
	"testing"
	"time"
)

func legPair(t *testing.T, payload byte) (*rtpLeg, *rtpLeg) {
	t.Helper()
	loopback := net.IPv4(127, 0, 0, 1)
	// Open the first leg pointed nowhere, learn its port, then point them at
	// each other.
	left, err := newRTPLeg(loopback, &net.UDPAddr{IP: loopback, Port: 1}, payload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = left.Close() })
	right, err := newRTPLeg(loopback, &net.UDPAddr{IP: loopback, Port: left.LocalPort()}, payload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = right.Close() })
	left.mu.Lock()
	left.remote = &net.UDPAddr{IP: loopback, Port: right.LocalPort()}
	left.mu.Unlock()
	return left, right
}

func readVoiced(ctx context.Context, leg *rtpLeg) ([]int16, error) {
	for {
		samples, err := leg.ReadPCM(ctx)
		if err != nil {
			return nil, err
		}
		for _, sample := range samples {
			if sample > 64 || sample < -64 {
				return samples, nil
			}
		}
	}
}

func TestRTPLegCarriesAudioInBothG711Flavours(t *testing.T) {
	for name, payload := range map[string]byte{"PCMU": payloadPCMU, "PCMA": payloadPCMA} {
		left, right := legPair(t, payload)
		want := make([]int16, frameSamples)
		for index := range want {
			want[index] = int16(9000 * math.Sin(float64(index)*2*math.Pi/40))
		}
		if err := left.WritePCM(want); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		got, err := readVoiced(ctx, right)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %d samples, want %d", name, len(got), len(want))
		}
		for index := range got {
			if difference := math.Abs(float64(got[index]) - float64(want[index])); difference > 700 {
				t.Fatalf("%s: sample %d differs by %.0f, beyond G.711 tolerance", name, index, difference)
			}
		}
	}
}

// A PBX watching for RTP treats a stream that stops as a dead call, so the leg
// has to keep sending while the other side of the bridge is quiet.
func TestRTPLegSendsSilenceWhenNothingIsQueued(t *testing.T) {
	_, right := legPair(t, payloadPCMU)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for received := 0; received < 3; received++ {
		samples, err := right.ReadPCM(ctx)
		if err != nil {
			t.Fatalf("packet %d: %v", received, err)
		}
		for _, sample := range samples {
			if sample > 64 || sample < -64 {
				t.Fatalf("packet %d carried audio, want silence", received)
			}
		}
	}
}

// Queued audio is bounded so a clock mismatch between the legs cannot grow an
// unbounded delay.
func TestRTPLegBoundsQueuedAudio(t *testing.T) {
	left, _ := legPair(t, payloadPCMU)
	for burst := 0; burst < 50; burst++ {
		if err := left.WritePCM(make([]int16, frameSamples)); err != nil {
			t.Fatal(err)
		}
	}
	left.writeMu.Lock()
	queued := len(left.queued)
	left.writeMu.Unlock()
	if queued > maxQueuedSamples {
		t.Fatalf("queued %d samples, cap is %d", queued, maxQueuedSamples)
	}
}
