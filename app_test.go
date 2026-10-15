package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BH2VSQ/SatAPRS-Go/internal/aprs"
)

func TestExpandTemplateUsesRecipientForHisCall(t *testing.T) {
	settings := Settings{Callsign: "N0CALL", QTH: "Example QTH", Grid: "AA00"}
	got := expandTemplate("Hi {HisCall}, this is {CALL} from {QTH} {GRID}", settings, "n1test-7")
	want := "Hi N1TEST-7, this is N0CALL from Example QTH AA00"
	if got != want {
		t.Fatalf("expandTemplate() = %q, want %q", got, want)
	}
}

func TestNormalizePresetMessagesAlwaysReturnsSix(t *testing.T) {
	got := normalizePresetMessages([]string{"one", "two"}, []string{"default-1", "default-2", "default-3", "default-4", "default-5", "default-6"})
	if len(got) != 6 {
		t.Fatalf("expected six preset slots, got %d", len(got))
	}
	if got[0] != "one" || got[1] != "two" || got[2] != "default-3" || got[5] != "default-6" {
		t.Fatalf("unexpected normalized presets: %#v", got)
	}
}

func TestNormalizePresetMessagesPreservesSpaces(t *testing.T) {
	got := normalizePresetMessages([]string{"TEST MSG", "TEST  MSG", "  LEADING", "TRAILING  "}, nil)
	if got[0] != "TEST MSG" || got[1] != "TEST  MSG" || got[2] != "  LEADING" || got[3] != "TRAILING  " {
		t.Fatalf("preset normalization altered spaces: %#v", got[:4])
	}
}

func TestDefaultAPRSPathAndOperationalLogMode(t *testing.T) {
	s := defaultSettings()
	if s.Path != "ARISS,DP0SNX,WIDE2-1" {
		t.Fatalf("default APRS path = %q", s.Path)
	}
	if s.LogSaveMode != "all" {
		t.Fatalf("default log save mode = %q, want all", s.LogSaveMode)
	}
}

func TestDefaultThemeIsDayMode(t *testing.T) {
	if got := defaultSettings().Theme; got != "light" {
		t.Fatalf("default theme = %q, want light", got)
	}
}

func TestSetThemePersistsWithoutChangingOtherSettings(t *testing.T) {
	app := NewApp()
	app.configFile = filepath.Join(t.TempDir(), "settings.json")
	app.settings.Callsign = "BH2VSQ"
	if err := app.SetTheme("dark"); err != nil {
		t.Fatalf("SetTheme(dark): %v", err)
	}
	if app.settings.Theme != "dark" || app.settings.Callsign != "BH2VSQ" {
		t.Fatalf("theme change unexpectedly modified settings: %#v", app.settings)
	}
	data, err := os.ReadFile(app.configFile)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var saved Settings
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode settings.json: %v", err)
	}
	if saved.Theme != "dark" || saved.Callsign != "BH2VSQ" {
		t.Fatalf("unexpected persisted settings: %#v", saved)
	}
	if err := app.SetTheme("sepia"); err == nil {
		t.Fatal("SetTheme should reject unsupported themes")
	}
}

func TestAutoStartListeningDisabledByDefault(t *testing.T) {
	if defaultSettings().AutoStartListening {
		t.Fatal("auto-start listening must be disabled by default")
	}
}

func TestDefaultPresetNamesHaveSixLabels(t *testing.T) {
	settings := defaultSettings()
	if len(settings.PresetNames) != 6 || len(settings.PresetMessages) != 6 {
		t.Fatalf("expected six preset names and messages; got %d names and %d messages", len(settings.PresetNames), len(settings.PresetMessages))
	}
	got := normalizePresetNames([]string{"Custom label"}, settings.PresetNames)
	if got[0] != "Custom label" || got[1] == "" {
		t.Fatalf("unexpected label normalization: %#v", got)
	}
}

func TestFormatTNC2Packet(t *testing.T) {
	packet := aprs.Packet{Source: "N0CALL", Destination: "APRS", Path: []string{"WIDE1-1", "WIDE2-2*"}, Info: ":N2TEST   :Hello {HisCall}"}
	want := "N0CALL>APRS,WIDE1-1,WIDE2-2*::N2TEST   :Hello {HisCall}"
	if got := formatTNC2Packet(packet); got != want {
		t.Fatalf("formatTNC2Packet() = %q, want %q", got, want)
	}
}

func TestFormatUISSFrame(t *testing.T) {
	packet := aprs.Packet{
		Source:      "BH2VSQ",
		Destination: "APRS",
		Path:        []string{"ARISS", "DP0SNX", "WIDE2-1"},
		Info:        ":CQ       :CQ BH2VSQ PN11",
	}
	at := time.Date(2026, time.October, 15, 14, 0, 11, 0, time.UTC)
	want := fmt.Sprintf("Fm BH2VSQ To APRS Via ARISS,DP0SNX,WIDE2-1 <UI pid=F0 Len=25 >[%s]\n:CQ       :CQ BH2VSQ PN11", at.Local().Format("15:04:05"))
	if got := formatUISSFrame(packet, at); got != want {
		t.Fatalf("formatUISSFrame() = %q, want %q", got, want)
	}
}

func TestFrameDestinationOptions(t *testing.T) {
	for _, value := range []string{"CQ", "APRS", "BEACON", "QST", "APRSAT", " aprs "} {
		if !isValidFrameDestination(value) {
			t.Errorf("expected %q to be a valid frame destination", value)
		}
	}
	for _, value := range []string{"", "N0CALL", "APRS-1", "APRS/5"} {
		if isValidFrameDestination(value) {
			t.Errorf("did not expect %q to be a valid frame destination", value)
		}
	}
	if got := defaultSettings().FrameDestination; got != "APRS" {
		t.Fatalf("default frame destination = %q, want APRS", got)
	}
}

func TestDefaultPresetMessagesFollowRadioExchangeDefaults(t *testing.T) {
	s := defaultSettings()
	want := []string{"CQ {CALL} {GRID}", "UR 5NN", "TU 73", "", "", ""}
	if len(s.PresetMessages) != len(want) {
		t.Fatalf("got %d preset messages, want %d", len(s.PresetMessages), len(want))
	}
	for i := range want {
		if s.PresetMessages[i] != want[i] {
			t.Errorf("F%d default message = %q, want %q", i+1, s.PresetMessages[i], want[i])
		}
	}
	if len(s.PresetResetCQ) != 6 || !s.PresetResetCQ[0] {
		t.Fatalf("F1 must reset the recipient to CQ by default: %#v", s.PresetResetCQ)
	}
	for i := 1; i < len(s.PresetResetCQ); i++ {
		if s.PresetResetCQ[i] {
			t.Errorf("F%d CQ-reset flag should be off by default", i+1)
		}
	}
}

func TestNormalizePresetResetCQDefaultsOnlyMissingEntries(t *testing.T) {
	defaults := []bool{true, false, false, false, false, false}
	got := normalizePresetResetCQ([]bool{false, true}, defaults)
	want := []bool{false, true, false, false, false, false}
	if len(got) != 6 {
		t.Fatalf("expected six CQ-reset flags, got %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("flag %d = %v, want %v", i+1, got[i], want[i])
		}
	}
}

func TestAppendTxRxLogSupportsSingleMonthlyAndYearlyFiles(t *testing.T) {
	folder := t.TempDir()
	a := NewApp()
	a.configFile = filepath.Join(folder, "settings.json")
	at := time.Date(2026, time.October, 13, 10, 20, 30, 0, time.UTC)
	a.settings.LogSaveMode = "all"
	a.appendTxRxLog(at, "RX", "N0CALL>APRS,WIDE1-1:test | RAW=ABCD")
	data, err := os.ReadFile(filepath.Join(folder, "all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), " RX N0CALL>APRS,WIDE1-1:test") || !strings.Contains(string(data), "RAW=ABCD") {
		t.Fatalf("unexpected all.txt log line: %s", data)
	}
	a.settings.LogSaveMode = "monthly"
	a.appendTxRxLog(at, "TX", "test")
	if _, err := os.Stat(filepath.Join(folder, "all-2026-10.txt")); err != nil {
		t.Fatalf("monthly log missing: %v", err)
	}
	a.settings.LogSaveMode = "yearly"
	a.appendTxRxLog(at, "TX", "test")
	if _, err := os.Stat(filepath.Join(folder, "all-2026.txt")); err != nil {
		t.Fatalf("yearly log missing: %v", err)
	}
}

func TestParseConfiguredCallsignAliases(t *testing.T) {
	cases := []struct{ input, base, suffix string }{
		{"N0CALL/5", "N0CALL", "5"},
		{"N0CALL/QRP", "N0CALL", "QRP"},
		{"N0CALL", "N0CALL", ""},
	}
	for _, tc := range cases {
		base, suffix, err := parseConfiguredCallsign(tc.input)
		if err != nil {
			t.Fatalf("parseConfiguredCallsign(%q): %v", tc.input, err)
		}
		if base != tc.base || suffix != tc.suffix {
			t.Errorf("parseConfiguredCallsign(%q) = %q/%q", tc.input, base, suffix)
		}
	}
	if _, _, err := parseConfiguredCallsign("N0CALL/TOOLONG"); err == nil {
		t.Fatal("expected long suffix to be rejected")
	}
}

type fakeRigctldResult struct {
	Commands    []string
	Connections int
}

func startFakeRigctld(t *testing.T, expectedCommands int) (int, <-chan fakeRigctldResult) {
	return startFakeRigctldWithPTTFeedback(t, expectedCommands, true)
}

func startFakeRigctldWithPTTFeedback(t *testing.T, expectedCommands int, reportPTT bool) (int, <-chan fakeRigctldResult) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	commands := make(chan fakeRigctldResult, 1)
	go func() {
		defer listener.Close()
		seen := make([]string, 0, expectedCommands)
		connections := 0
		pttState := 0
		for len(seen) < expectedCommands {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			connections++
			scanner := bufio.NewScanner(conn)
			for scanner.Scan() {
				command := strings.TrimSpace(scanner.Text())
				seen = append(seen, command)
				switch command {
				case "T 1", "T 2", "T 3":
					if reportPTT {
						pttState = int(command[len(command)-1] - '0')
					}
					_, _ = fmt.Fprintln(conn, "RPRT 0")
				case "T 0":
					pttState = 0
					_, _ = fmt.Fprintln(conn, "RPRT 0")
				case "t":
					_, _ = fmt.Fprintln(conn, pttState)
				default:
					_, _ = fmt.Fprintln(conn, "RPRT -1")
				}
				if len(seen) >= expectedCommands {
					_ = conn.Close()
					commands <- fakeRigctldResult{Commands: seen, Connections: connections}
					return
				}
			}
			_ = conn.Close()
		}
		commands <- fakeRigctldResult{Commands: seen, Connections: connections}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port, commands
}

func appWithFakeRigctld(port int) *App {
	a := NewApp()
	a.settings = defaultSettings()
	a.settings.PTTMode = "hamlib"
	a.settings.UseRigPTT = true
	a.settings.RigHost = "127.0.0.1"
	a.settings.RigPort = port
	return a
}

func TestRigctlConnectionTestStoresResultInState(t *testing.T) {
	port, commands := startFakeRigctld(t, 1)
	a := appWithFakeRigctld(port)
	if err := a.TestRigctl(); err != nil {
		t.Fatalf("rigctld test: %v", err)
	}
	state := a.GetState()
	if state.RigctlTestLevel != "success" || !strings.Contains(state.RigctlTestInfo, "T 0") || !strings.Contains(state.RigctlTestInfo, "RPRT 0") {
		t.Fatalf("expected successful test details in State, got level=%q info=%q", state.RigctlTestLevel, state.RigctlTestInfo)
	}
	select {
	case got := <-commands:
		if len(got.Commands) != 1 || got.Commands[0] != "T 0" || got.Connections != 1 {
			t.Fatalf("rigctld result = %#v, want one connection with [T 0]", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rigctld test command")
	}
}

func TestHamlibPTTUsesGenericCommand(t *testing.T) {
	port, commands := startFakeRigctld(t, 2)
	a := appWithFakeRigctld(port)
	a.pttTestDuration = 5 * time.Second
	if err := a.TestPTT(); err != nil {
		t.Fatalf("start PTT: %v", err)
	}
	if err := a.TestPTT(); err != nil {
		t.Fatalf("stop PTT: %v", err)
	}
	select {
	case got := <-commands:
		if len(got.Commands) != 2 || got.Commands[0] != "T 1" || got.Commands[1] != "T 0" || got.Connections != 1 {
			t.Fatalf("PTT commands = %#v, want generic [T 1 T 0] on one connection", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for generic PTT commands")
	}
}

func waitForPTTTestResult(t *testing.T, a *App, contains string) State {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := a.GetState()
		if strings.Contains(state.RigctlTestInfo, contains) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	state := a.GetState()
	t.Fatalf("timed out waiting for PTT test result %q; got level=%q info=%q", contains, state.RigctlTestLevel, state.RigctlTestInfo)
	return State{}
}

func TestPTTTestButtonTogglesOnAndOff(t *testing.T) {
	port, commands := startFakeRigctld(t, 2)
	a := appWithFakeRigctld(port)
	a.pttTestDuration = 5 * time.Second
	if err := a.TestPTT(); err != nil {
		t.Fatalf("start PTT: %v", err)
	}
	if state := a.GetState(); !state.PTTTestActive || state.PTTTestSeconds == 0 {
		t.Fatalf("expected active PTT countdown, got active=%v seconds=%d", state.PTTTestActive, state.PTTTestSeconds)
	}
	if state := a.GetState(); !strings.Contains(state.RigctlTestInfo, "TCP 控制连接将保持打开") {
		t.Fatalf("expected persistent-session diagnostic, got %q", state.RigctlTestInfo)
	}
	// The hold interval must not issue an extra `t` status query or release PTT.
	time.Sleep(250 * time.Millisecond)
	if state := a.GetState(); !state.PTTTestActive {
		t.Fatal("PTT should remain active while the persistent rigctld session is held")
	}
	if err := a.TestPTT(); err != nil {
		t.Fatalf("stop PTT: %v", err)
	}
	if state := a.GetState(); state.PTTTestActive {
		t.Fatal("PTT should be inactive after second click")
	}
	select {
	case got := <-commands:
		if len(got.Commands) != 2 || got.Commands[0] != "T 1" || got.Commands[1] != "T 0" || got.Connections != 1 {
			t.Fatalf("rigctld result = %#v, want one connection with [T 1 T 0]", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rigctld PTT commands")
	}
}

func TestPTTTestDoesNotQueryStatusWhileKeyed(t *testing.T) {
	port, commands := startFakeRigctldWithPTTFeedback(t, 2, false)
	a := appWithFakeRigctld(port)
	a.pttTestDuration = 5 * time.Second
	if err := a.TestPTT(); err != nil {
		t.Fatalf("start PTT: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if state := a.GetState(); !state.PTTTestActive {
		t.Fatal("PTT test must remain active without an in-transmit status query")
	}
	if err := a.TestPTT(); err != nil {
		t.Fatalf("stop PTT: %v", err)
	}
	select {
	case got := <-commands:
		if len(got.Commands) != 2 || got.Commands[0] != "T 1" || got.Commands[1] != "T 0" || got.Connections != 1 {
			t.Fatalf("rigctld result = %#v, want one connection with [T 1 T 0] and no `t` query", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rigctld PTT commands")
	}
}

func TestPTTTestAutomaticallyStopsAtTimeout(t *testing.T) {
	port, commands := startFakeRigctld(t, 2)
	a := appWithFakeRigctld(port)
	a.pttTestDuration = 120 * time.Millisecond
	if err := a.TestPTT(); err != nil {
		t.Fatalf("start PTT: %v", err)
	}
	select {
	case got := <-commands:
		if len(got.Commands) != 2 || got.Commands[0] != "T 1" || got.Commands[1] != "T 0" || got.Connections != 1 {
			t.Fatalf("rigctld result = %#v, want automatic [T 1 T 0] on one connection", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PTT did not automatically release at timeout")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if state := a.GetState(); !state.PTTTestActive {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("PTT should be inactive after timeout")
}
