package aprs

import (
	"testing"
)

func TestAFSKDecoderResetClearsBufferedAudioAndSyncState(t *testing.T) {
	decoder := NewAFSK1200Decoder(DefaultSampleRate)
	decoder.Process(make([]int16, 240))
	if len(decoder.pending) == 0 {
		t.Fatal("expected decoder to retain a partial sample window before reset")
	}

	decoder.Reset()
	if len(decoder.pending) != 0 {
		t.Fatalf("pending samples after reset = %d, want 0", len(decoder.pending))
	}
	for i := range decoder.phaseStart {
		if decoder.phaseStart[i] != i {
			t.Errorf("phaseStart[%d] = %d, want %d", i, decoder.phaseStart[i], i)
		}
		phase := decoder.phases[i]
		if phase.haveTone || phase.lastMark || phase.assembler.inFrame || len(phase.assembler.bits) != 0 {
			t.Errorf("phase %d retained state after reset: %#v", i, phase)
		}
	}
}
