package aprs

import (
	"math"
	"strings"
	"testing"
)

func TestAX25EncodeDecodeUI(t *testing.T) {
	info, err := BuildMessageInfo("CQ", "Hello satellite APRS")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := EncodeUIFrame("N0CALL-7", "APRS", []string{"WIDE1-1"}, info)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeUIFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "N0CALL-7" || p.Destination != "APRS" || len(p.Path) != 1 || p.Path[0] != "WIDE1-1" {
		t.Fatalf("unexpected addresses: %#v", p)
	}
	to, body, ok := ParseAPRSMessage(p.Info)
	if !ok || to != "CQ" || body != "Hello satellite APRS" {
		t.Fatalf("unexpected APRS message: %q %q %v", to, body, ok)
	}
	frame[len(frame)-1] ^= 1
	if _, err := DecodeUIFrame(frame); err == nil {
		t.Fatal("expected FCS validation error")
	}
}

func TestMaidenheadToLatLon(t *testing.T) {
	lat, lon, err := MaidenheadToLatLon("AA00")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(lat-(-89.5)) > 0.01 || math.Abs(lon-(-179.0)) > 0.01 {
		t.Fatalf("unexpected AA00 center %.4f, %.4f", lat, lon)
	}
}

func TestAFSKEncodeDecodeLoopback(t *testing.T) {
	info, _ := BuildMessageInfo("CQ", "TEST")
	frame, err := EncodeUIFrame("N0CALL-1", "APRS", []string{"WIDE1-1"}, info)
	if err != nil {
		t.Fatal(err)
	}
	samples := EncodeAFSK1200(frame, DefaultSampleRate)
	input := make([]int16, len(samples))
	for i, v := range samples {
		input[i] = int16(v * 30000)
	}
	decoder := NewAFSK1200Decoder(DefaultSampleRate)
	found := false
	for i := 0; i < len(input); i += 480 {
		end := i + 480
		if end > len(input) {
			end = len(input)
		}
		for _, p := range decoder.Process(input[i:end]) {
			if p.Source == "N0CALL-1" && strings.Contains(p.Info, "TEST") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("loopback AFSK decoder did not recover the encoded AX.25 packet")
	}
}

func TestAFSKBitStuffingRoundTrip(t *testing.T) {
	info := []byte(strings.Repeat("111110111111011111", 12))
	frame, err := EncodeUIFrame("N0CALL", "APRS", nil, info)
	if err != nil {
		t.Fatal(err)
	}
	samples := EncodeAFSK1200(frame, DefaultSampleRate)
	input := make([]int16, len(samples))
	for i, v := range samples {
		input[i] = int16(v * 30000)
	}
	decoder := NewAFSK1200Decoder(DefaultSampleRate)
	for i := 0; i < len(input); i += 960 {
		end := i + 960
		if end > len(input) {
			end = len(input)
		}
		for _, packet := range decoder.Process(input[i:end]) {
			if packet.Source == "N0CALL" && packet.Info == string(info) {
				return
			}
		}
	}
	t.Fatal("decoder did not recover a frame containing long one-bit sequences")
}

func TestBuildPositionInfo(t *testing.T) {
	info, err := BuildPositionInfo(10.5, 20.0, '/', '>', "Example QTH")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info, "!1030.00N/02000.00E>") || !strings.HasSuffix(info, "Example QTH") {
		t.Fatalf("unexpected position info: %q", info)
	}
}

func TestSlashNumericCallsignMapsToAX25SSID(t *testing.T) {
	info, err := BuildMessageInfo("CQ", "HELLO")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := EncodeUIFrame("N0CALL/5", "APRS", nil, info)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := DecodeUIFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Source != "N0CALL-5" {
		t.Fatalf("source = %q, want N0CALL-5", packet.Source)
	}
}

func TestTextCallsignAliasCannotBeEncodedDirectlyIntoAX25(t *testing.T) {
	info, _ := BuildMessageInfo("CQ", "HELLO")
	if _, err := EncodeUIFrame("N0CALL/QRP", "APRS", nil, info); err == nil {
		t.Fatal("expected AX.25 to reject textual source suffix")
	}
}
