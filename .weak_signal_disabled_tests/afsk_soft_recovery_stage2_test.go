package aprs

import (
	"bytes"
	"testing"
)

func TestStage2RecoverWeakFrameOneAndTwoLowConfidenceBitErrors(t *testing.T) {
	info := []byte("APRS weak signal recovery test")
	frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, info)
	if err != nil {
		t.Fatal(err)
	}
	infoStart := len(frame) - 2 - len(info)
	corrupt := append([]byte(nil), frame...)
	bitA := infoStart*8 + 1
	bitB := (infoStart+4)*8 + 6
	corrupt[bitA/8] ^= 1 << uint(bitA%8)
	corrupt[bitB/8] ^= 1 << uint(bitB%8)
	confidence := make([]float64, len(frame)*8)
	for i := range confidence {
		confidence[i] = 0.9
	}
	confidence[bitA] = 0.01
	confidence[bitB] = 0.02

	if _, err := DecodeUIFrame(corrupt); err == nil {
		t.Fatal("corrupted frame unexpectedly passed FCS validation")
	}
	recovered := recoverWeakFrame(corrupt, confidence)
	if !bytes.Equal(recovered, frame) {
		t.Fatalf("recovered frame differs from original; got %X, want %X", recovered, frame)
	}
	packet, err := DecodeUIFrame(recovered)
	if err != nil {
		t.Fatalf("recovered frame did not pass AX.25 validation: %v", err)
	}
	if packet.Info != string(info) {
		t.Fatalf("recovered info = %q, want %q", packet.Info, info)
	}
}

func TestStage2DoesNotCorrectHighConfidenceFrameErrors(t *testing.T) {
	frame, err := EncodeUIFrame("N0CALL", "APRS", nil, []byte("SAFE"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), frame...)
	bit := (len(frame)-5)*8 + 2
	corrupt[bit/8] ^= 1 << uint(bit%8)
	confidence := make([]float64, len(frame)*8)
	for i := range confidence {
		confidence[i] = 0.9
	}
	if got := recoverWeakFrame(corrupt, confidence); got != nil {
		t.Fatalf("unexpected correction when all bit margins are high: %X", got)
	}
}

func TestStage2DestuffingPreservesConfidenceAlignment(t *testing.T) {
	bits := []byte{1, 1, 1, 1, 1, 0, 0, 1}
	confidence := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}
	gotBits, gotConfidence := destuffBitsWithConfidence(bits, confidence)
	wantBits := []byte{1, 1, 1, 1, 1, 0, 1}
	if !bytes.Equal(gotBits, wantBits) {
		t.Fatalf("destuffed bits = %v, want %v", gotBits, wantBits)
	}
	wantConfidence := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.7, 0.8}
	if len(gotConfidence) != len(wantConfidence) {
		t.Fatalf("confidence length = %d, want %d", len(gotConfidence), len(wantConfidence))
	}
	for i := range wantConfidence {
		if gotConfidence[i] != wantConfidence[i] {
			t.Errorf("confidence[%d] = %.2f, want %.2f", i, gotConfidence[i], wantConfidence[i])
		}
	}
}
