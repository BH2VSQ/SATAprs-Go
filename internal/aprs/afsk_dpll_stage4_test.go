package aprs

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

func runDPLLStage4(samples []float32, chunks []int, scale float64, noiseSigma float64, seed int64) []Packet {
	rng := rand.New(rand.NewSource(seed))
	input := make([]int16, len(samples))
	for i, sample := range samples {
		v := float64(sample)*scale + rng.NormFloat64()*noiseSigma
		if v > 0.999 {
			v = 0.999
		}
		if v < -0.999 {
			v = -0.999
		}
		input[i] = int16(math.Round(v * 32767))
	}
	d := NewAFSK1200DPLLDecoder(DefaultSampleRate)
	var packets []Packet
	for offset, ci := 0, 0; offset < len(input); ci++ {
		end := offset + chunks[ci%len(chunks)]
		if end > len(input) {
			end = len(input)
		}
		packets = append(packets, d.Process(input[offset:end])...)
		offset = end
	}
	return packets
}

func TestStage4DPLLDecodeLoopbackIrregularChunks(t *testing.T) {
	info := []byte("DPLL LOOPBACK 123")
	frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, info)
	if err != nil {
		t.Fatal(err)
	}
	samples := EncodeAFSK1200(frame, DefaultSampleRate)
	packets := runDPLLStage4(samples, []int{1, 39, 137, 480, 43, 997, 320, 71}, 1, 0, 1)
	for _, p := range packets {
		if p.Source == "N0CALL-1" && strings.Contains(p.Info, string(info)) {
			return
		}
	}
	t.Fatalf("DPLL failed to decode loopback frame; got %d packets: %#v", len(packets), packets)
}

func TestStage4DPLLDecodeWeakSignalNoise(t *testing.T) {
	info := []byte("WEAK SIGNAL TEST")
	frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, info)
	if err != nil {
		t.Fatal(err)
	}
	samples := EncodeAFSK1200(frame, DefaultSampleRate)
	// At scale 0.005, 0.48-FS transmit amplitude becomes roughly -52 dBFS.
	// sigma 0.0010 is a deterministic low-SNR white-noise test rather than
	// a claim about real receiver sensitivity.
	packets := runDPLLStage4(samples, []int{480, 960, 137, 511}, 0.005, 0.0010, 42)
	for _, p := range packets {
		if p.Source == "N0CALL-1" && strings.Contains(p.Info, string(info)) {
			return
		}
	}
	t.Fatalf("DPLL failed the deterministic low-level/noise test; got %d packets", len(packets))
}

func TestStage4DPLLResetClearsSynchronizationState(t *testing.T) {
	frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, []byte("RESET TEST"))
	if err != nil {
		t.Fatal(err)
	}
	samples := EncodeAFSK1200(frame, DefaultSampleRate)
	input := make([]int16, len(samples))
	for i, s := range samples {
		input[i] = int16(s * 32767)
	}
	d := NewAFSK1200DPLLDecoder(DefaultSampleRate)
	_ = d.Process(input[:len(input)/2])
	d.Reset()
	var packets []Packet
	for offset := 0; offset < len(input); {
		end := offset + 317
		if end > len(input) {
			end = len(input)
		}
		packets = append(packets, d.Process(input[offset:end])...)
		offset = end
	}
	for _, p := range packets {
		if p.Source == "N0CALL-1" && strings.Contains(p.Info, "RESET TEST") {
			return
		}
	}
	t.Fatalf("DPLL did not recover a frame after Reset; got %d packets", len(packets))
}

func timeWarpAFSKStage4(input []float32, scale float64) []float32 {
	out := make([]float32, int(math.Ceil(float64(len(input))*scale)))
	for i := range out {
		pos := float64(i) / scale
		left := int(pos)
		if left >= len(input)-1 {
			out[i] = input[len(input)-1]
			continue
		}
		frac := pos - float64(left)
		out[i] = input[left]*(1-float32(frac)) + input[left+1]*float32(frac)
	}
	return out
}

func TestStage4DPLLTracksSmallSymbolClockOffset(t *testing.T) {
	for _, scale := range []float64{0.995, 1.005} {
		t.Run(fmt.Sprintf("scale_%.3f", scale), func(t *testing.T) {
			info := []byte("CLOCK OFFSET TEST")
			frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, info)
			if err != nil {
				t.Fatal(err)
			}
			samples := timeWarpAFSKStage4(EncodeAFSK1200(frame, DefaultSampleRate), scale)
			packets := runDPLLStage4(samples, []int{480, 137, 991, 211}, 1, 0, 7)
			for _, p := range packets {
				if p.Source == "N0CALL-1" && strings.Contains(p.Info, string(info)) {
					return
				}
			}
			t.Fatalf("failed to decode at audio time scale %.3f; got %d packets", scale, len(packets))
		})
	}
}
