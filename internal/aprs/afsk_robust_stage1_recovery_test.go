package aprs

import (
	"testing"
)

func TestStage1RecoveryAFSKDecoderResetClearsBufferedAudioAndSyncState(t *testing.T) {
	decoder := NewAFSK1200Decoder(DefaultSampleRate)
	// Seed the internal state directly so the test is deterministic and does
	// not depend on how many complete windows Process() consumes.
	decoder.pending = make([]float64, 17)
	decoder.phaseStart[0] = 80
	decoder.phases[0] = phaseDecoder{
		haveTone:  true,
		lastMark:  true,
		assembler: frameAssembler{inFrame: true, bits: []byte{1, 0, 1}},
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
