package audio

import (
	"math"
	"testing"
)

func TestCalculateSpectrumDetectsTone(t *testing.T) {
	const (
		sampleRate = 48000.0
		toneHz     = 1500.0 // FFT bin 256 at 8192 samples
	)
	samples := make([]int16, 8192)
	for i := range samples {
		samples[i] = int16(12000 * math.Sin(2*math.Pi*toneHz*float64(i)/sampleRate))
	}
	bands := calculateSpectrum(samples)
	if len(bands) != 2048 {
		t.Fatalf("expected 2048 spectrum bands, got %d", len(bands))
	}
	peakBand, peak := 0, 0.0
	for i, value := range bands {
		if value > peak {
			peakBand, peak = i, value
		}
	}
	if peakBand < 126 || peakBand > 130 {
		t.Fatalf("expected 1500 Hz tone near high-resolution band 128, peak is band %d", peakBand)
	}
	if peak < 0.5 {
		t.Fatalf("expected visible tone level, got %.3f", peak)
	}
}

func TestCalculateSpectrumShowsQuietTone(t *testing.T) {
	const sampleRate = 48000.0
	samples := make([]int16, 8192)
	for i := range samples {
		samples[i] = int16(180 * math.Sin(2*math.Pi*1500*float64(i)/sampleRate))
	}
	bands := calculateSpectrum(samples)
	peak := 0.0
	for _, value := range bands {
		if value > peak {
			peak = value
		}
	}
	if peak < 0.2 {
		t.Fatalf("quiet -45 dBFS tone should remain visible, got peak %.3f", peak)
	}
}

func TestCalculateSpectrumEmptySignalIsQuiet(t *testing.T) {
	bands := calculateSpectrum(make([]int16, 8192))
	for i, value := range bands {
		if value != 0 {
			t.Fatalf("band %d should be quiet, got %.3f", i, value)
		}
	}
}

func TestLevelDBFSMeasuresSilenceAndSignal(t *testing.T) {
	if got := levelDBFS(make([]int16, 480)); got != -120 {
		t.Fatalf("silence should be clamped to -120 dBFS, got %.2f", got)
	}
	signal := make([]int16, 480)
	for i := range signal {
		signal[i] = 12000
	}
	got := levelDBFS(signal)
	if got < -10 || got > -5 {
		t.Fatalf("expected approximately -8.7 dBFS, got %.2f", got)
	}
}
