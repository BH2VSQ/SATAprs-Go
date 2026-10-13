package aprs

import "testing"

func TestBuildMessageInfoPreservesSpacesAndNormalizesSpecialRecipient(t *testing.T) {
	info, err := BuildMessageInfo("N0CALL/5", "  HELLO  WORLD  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(info) != ":N0CALL-5 :  HELLO  WORLD  " {
		t.Fatalf("message info lost spaces or did not normalize recipient: %q", info)
	}
	info, err = BuildMessageInfo("N0CALL/QRP", "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if string(info) != ":N0CALL   :TEST" {
		t.Fatalf("text alias should route to base callsign, got %q", info)
	}
}

func TestBuildMessageInfoPreservesOrdinarySpaceInText(t *testing.T) {
	info, err := BuildMessageInfo("N0CALL", "TEST MSG")
	if err != nil {
		t.Fatal(err)
	}
	if string(info) != ":N0CALL   :TEST MSG" {
		t.Fatalf("ordinary space was lost from message: %q", info)
	}
}
