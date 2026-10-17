package aprs

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func stage3FilteredRMS(freqHz float64) float64 {
	const sampleRate = 48000
	hp := newAFSKBiquad(sampleRate, 700, true)
	lp := newAFSKBiquad(sampleRate, 2800, false)
	const count = sampleRate * 2
	sum := 0.0
	used := 0
	for i := 0; i < count; i++ {
		x := 0.5 * math.Sin(2*math.Pi*freqHz*float64(i)/sampleRate)
		y := lp.process(hp.process(x))
		if i >= 2000 { // ignore startup transient
			sum += y * y
			used++
		}
	}
	return math.Sqrt(sum / float64(used))
}

func TestStage3BandpassPreservesBell202Tones(t *testing.T) {
	for _, freq := range []float64{1200, 2200} {
		t.Run(fmtFrequency(freq), func(t *testing.T) {
			if got := stage3FilteredRMS(freq); got < 0.20 {
				t.Fatalf("filtered RMS for %.0f Hz = %.4f, want at least 0.20", freq, got)
			}
		})
	}
}

func TestStage3BandpassAttenuatesOutOfBandTones(t *testing.T) {
	low := stage3FilteredRMS(300)
	high := stage3FilteredRMS(6000)
	if low >= 0.12 {
		t.Fatalf("300 Hz output RMS = %.4f, want < 0.12", low)
	}
	if high >= 0.12 {
		t.Fatalf("6000 Hz output RMS = %.4f, want < 0.12", high)
	}
}

func TestStage3AFSKDecodeWithStrongOutOfBandInterference(t *testing.T) {
	info := []byte("STAGE3 FILTER TEST")
	frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, info)
	if err != nil {
		t.Fatal(err)
	}
	samples := EncodeAFSK1200(frame, DefaultSampleRate)
	input := make([]int16, len(samples))
	for i, sample := range samples {
		interference := 0.30 * math.Sin(2*math.Pi*6000*float64(i)/DefaultSampleRate)
		v := float64(sample) + interference
		if v > 0.99 {
			v = 0.99
		} else if v < -0.99 {
			v = -0.99
		}
		input[i] = int16(math.Round(v * 32767))
	}

	decoder := NewAFSK1200Decoder(DefaultSampleRate)
	chunkSizes := []int{137, 511, 43, 997, 320, 71}
	found := false
	for offset, chunkIndex := 0, 0; offset < len(input); chunkIndex++ {
		end := offset + chunkSizes[chunkIndex%len(chunkSizes)]
		if end > len(input) {
			end = len(input)
		}
		for _, packet := range decoder.Process(input[offset:end]) {
			if packet.Source == "N0CALL-1" && strings.Contains(packet.Info, string(info)) {
				found = true
			}
		}
		offset = end
	}
	if !found {
		t.Fatal("decoder did not recover the AX.25 frame with 6 kHz interference and irregular audio chunks")
	}
}

func TestStage3FilterResetClearsIIRState(t *testing.T) {
	f := newAFSKBiquad(48000, 700, true)
	for i := 0; i < 128; i++ {
		_ = f.process(math.Sin(2 * math.Pi * 900 * float64(i) / 48000))
	}
	f.reset()
	fresh := newAFSKBiquad(48000, 700, true)
	for i := 0; i < 16; i++ {
		x := math.Sin(2 * math.Pi * 900 * float64(i) / 48000)
		got, want := f.process(x), fresh.process(x)
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("reset filter output[%d] = %.15f, fresh = %.15f", i, got, want)
		}
	}
}

func fmtFrequency(freq float64) string {
	return fmt.Sprintf("%.0fHz", freq)
}
