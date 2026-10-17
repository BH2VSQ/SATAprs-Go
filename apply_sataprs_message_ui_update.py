#!/usr/bin/env python3
"""Apply the message-address and seven-column TX/RX UI update to SATAprs-Go.

This patch updates app.go, app_test.go, frontend/dist/index.html, and README.md.
If an earlier revision is already installed, it can update just the visible frame-
destination preset selector without reapplying changes to Go source files.
It deliberately does not touch any AFSK/APRS decoder or audio algorithm file.
"""
from __future__ import annotations

import datetime as _dt
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent
TARGETS = [ROOT / "app.go", ROOT / "app_test.go", ROOT / "frontend" / "dist" / "index.html", ROOT / "README.md"]


def read_text(path: Path) -> tuple[str, str, bool]:
    raw = path.read_bytes()
    has_bom = raw.startswith(b"\xef\xbb\xbf")
    text = raw.decode("utf-8-sig")
    newline = "\r\n" if b"\r\n" in raw else "\n"
    return text.replace("\r\n", "\n"), newline, has_bom


def write_text(path: Path, text: str, newline: str, has_bom: bool) -> None:
    data = text.replace("\n", newline).encode("utf-8")
    if has_bom:
        data = b"\xef\xbb\xbf" + data
    path.write_bytes(data)


def replace_once(text: str, old: str, new: str, label: str) -> str:
    count = text.count(old)
    if count != 1:
        raise RuntimeError(f"{label}: expected one exact match, found {count}; no files written")
    return text.replace(old, new, 1)


def replace_region(text: str, start: str, end: str, replacement: str, label: str) -> str:
    if text.count(start) != 1 or text.count(end) != 1:
        raise RuntimeError(f"{label}: region anchors do not uniquely match; no files written")
    i = text.index(start)
    j = text.index(end, i)
    if j <= i:
        raise RuntimeError(f"{label}: invalid region order; no files written")
    return text[:i] + replacement + text[j:]


def patch_app(text: str) -> str:
    if "func prepareOutboundMessage(" in text and "func classifyReceivedPacket(" in text and "func (a *App) addTXAt(" in text:
        return text

    old_packet_view = '''type PacketView struct {
\tUTC         string `json:"utc"`
\tTimestamp   int64  `json:"timestamp"`
\tSource      string `json:"source"`
\tDestination string `json:"destination"`
\tPath        string `json:"path"`
\tInfo        string `json:"info"`
\tMessageTo   string `json:"messageTo"`
\tMessage     string `json:"message"`
\tRawHex      string `json:"rawHex"`
\tPacketText  string `json:"packet"`
}

type TxView struct {
\tUTC        string `json:"utc"`
\tTimestamp  int64  `json:"timestamp"`
\tKind       string `json:"kind"`
\tInfo       string `json:"info"`
\tStatus     string `json:"status"`
\tRawHex     string `json:"rawHex"`
\tPacketText string `json:"packet"`
}
'''
    new_packet_view = '''type PacketView struct {
\tUTC         string `json:"utc"`
\tTimestamp   int64  `json:"timestamp"`
\tDataType    string `json:"dataType"`
\tSource      string `json:"source"`
\tDestination string `json:"destination"`
\tPath        string `json:"path"`
\tInfo        string `json:"info"`
\tMessageTo   string `json:"messageTo"`
\tMessage     string `json:"message"`
\tDelayMS     int64  `json:"delayMs"`
\tRawHex      string `json:"rawHex"`
\tPacketText  string `json:"packet"`
}

type TxView struct {
\tUTC         string `json:"utc"`
\tTimestamp   int64  `json:"timestamp"`
\tDataType    string `json:"dataType"`
\tSource      string `json:"source"`
\tDestination string `json:"destination"`
\tPath        string `json:"path"`
\tDelayMS     int64  `json:"delayMs"`
\tKind        string `json:"kind"`
\tInfo        string `json:"info"`
\tStatus      string `json:"status"`
\tRawHex      string `json:"rawHex"`
\tPacketText  string `json:"packet"`
}

type transmitMark struct {
\tTimestamp   int64
\tSource      string
\tDestination string
\tInfo        string
}
'''
    text = replace_once(text, old_packet_view, new_packet_view, "PacketView/TxView fields")

    text = replace_once(
        text,
        "\ttransmitted       []TxView\n\tseen              map[string]time.Time",
        "\ttransmitted       []TxView\n\ttxHistory         []transmitMark // guarded by mu; used for self-echo delay measurement\n\tseen              map[string]time.Time",
        "App transmit history field",
    )

    text = replace_once(
        text,
        '''\t\t\tloaded.FrameDestination = strings.ToUpper(strings.TrimSpace(loaded.FrameDestination))
\t\t\tif !isValidFrameDestination(loaded.FrameDestination) {
\t\t\t\tloaded.FrameDestination = defaults.FrameDestination
\t\t\t}
''',
        '''\t\t\tnormalizedDestination, destinationErr := normalizeFrameDestination(loaded.FrameDestination)
\t\t\tif destinationErr != nil {
\t\t\t\tloaded.FrameDestination = defaults.FrameDestination
\t\t\t} else {
\t\t\t\tloaded.FrameDestination = normalizedDestination
\t\t\t}
''',
        "settings-load frame destination normalization",
    )

    text = replace_once(
        text,
        '''\ts.FrameDestination = strings.ToUpper(strings.TrimSpace(s.FrameDestination))
\tif s.FrameDestination == "" {
\t\ts.FrameDestination = defaultSettings().FrameDestination
\t}
\tif !isValidFrameDestination(s.FrameDestination) {
\t\treturn fmt.Errorf("帧目的地址必须是 CQ、APRS、BEACON、QST 或 APRSAT")
\t}
\ts.Recipient = strings.ToUpper(strings.TrimSpace(s.Recipient))
''',
        '''\tif strings.TrimSpace(s.FrameDestination) == "" {
\t\ts.FrameDestination = defaultSettings().FrameDestination
\t}
\tnormalizedDestination, destinationErr := normalizeFrameDestination(s.FrameDestination)
\tif destinationErr != nil {
\t\treturn fmt.Errorf("无效帧目的地址：%w", destinationErr)
\t}
\ts.FrameDestination = normalizedDestination
\t// Recipient remains in the settings struct for old settings-file compatibility.
\t// New transmissions use FrameDestination as the actual AX.25 destination.
\ts.Recipient = strings.ToUpper(strings.TrimSpace(s.Recipient))
\tif s.Recipient == "" {
\t\ts.Recipient = defaultSettings().Recipient
\t}
''',
        "SaveSettings frame destination validation",
    )

    new_send_block = '''func (a *App) SendPreset(index int, frameDestination string) error {
\ts := a.settingsSnapshot()
\tif index < 0 || index >= 6 || index >= len(s.PresetMessages) {
\t\treturn errors.New("预设消息索引无效，固定支持 F1 至 F6")
\t}
\tname := fmt.Sprintf("F%d", index+1)
\tif index < len(s.PresetNames) && strings.TrimSpace(s.PresetNames[index]) != "" {
\t\tname = strings.TrimSpace(s.PresetNames[index])
\t}
\tif strings.TrimSpace(s.PresetMessages[index]) == "" {
\t\treturn fmt.Errorf("%s 尚未设置消息正文，请先编辑预设", name)
\t}
\treturn a.sendMessage(frameDestination, s.PresetMessages[index], fmt.Sprintf("预设消息 %s", name))
}

func (a *App) SendCustomMessage(frameDestination, message string) error {
\treturn a.sendMessage(frameDestination, message, "自定义消息")
}

func (a *App) sendMessage(frameDestination, message, kind string) error {
\ts := a.settingsSnapshot()
\tif s.Callsign == "" {
\t\treturn errors.New("请先设置本机呼号")
\t}
\tif strings.TrimSpace(frameDestination) == "" {
\t\tframeDestination = s.FrameDestination
\t}
\tdestination, info, err := prepareOutboundMessage(frameDestination, message, s)
\tif err != nil {
\t\ta.setError(err)
\t\treturn err
\t}
\treturn a.sendInfo(info, kind, destination)
}

// prepareOutboundMessage keeps the AX.25 destination and information field
// separate. The destination is encoded in the address chain; the information
// field contains only the message text after macro expansion.
func prepareOutboundMessage(frameDestination, message string, s Settings) (string, []byte, error) {
\tdestination, err := normalizeFrameDestination(frameDestination)
\tif err != nil {
\t\treturn "", nil, err
\t}
\tmessage = expandTemplate(message, s, destination)
\tif strings.TrimSpace(message) == "" {
\t\treturn "", nil, errors.New("消息内容不能为空")
\t}
\tif strings.ContainsAny(message, "\\r\\n") {
\t\treturn "", nil, errors.New("消息内容不能包含换行")
\t}
\tif len([]byte(message)) > aprs.MaxAPRSMessageText {
\t\treturn "", nil, fmt.Errorf("消息正文最多 %d 字节，当前 %d 字节", aprs.MaxAPRSMessageText, len([]byte(message)))
\t}
\treturn destination, []byte(message), nil
}

'''
    text = replace_region(
        text,
        "func (a *App) SendPreset(index int, recipient string) error {",
        "func (a *App) sendInfo(info []byte, kind string) error {",
        new_send_block,
        "outbound message preparation and Send APIs",
    )
    text = replace_once(
        text,
        "func (a *App) sendInfo(info []byte, kind string) error {",
        "func (a *App) sendInfo(info []byte, kind, frameDestination string) error {",
        "sendInfo accepts the chosen frame destination",
    )

    text = replace_once(
        text,
        '''\tdestination := strings.ToUpper(strings.TrimSpace(s.FrameDestination))
\tif !isValidFrameDestination(destination) {
\t\tdestination = defaultSettings().FrameDestination
\t}
''',
        '''\tdestination, err := normalizeFrameDestination(frameDestination)
\tif err != nil {
\t\ta.setError(err)
\t\treturn err
\t}
''',
        "sendInfo destination source",
    )

    text = replace_once(
        text,
        '''\tsamples := aprs.EncodeAFSK1200(frame, audio.SampleRate)
\tif err := engine.Play(ctx, samples); err != nil {
\t\ta.setError(err)
''',
        '''\tsamples := aprs.EncodeAFSK1200(frame, audio.SampleRate)
\ttxStartedAt := time.Now().UTC()
\ta.recordTXStart(txStartedAt, source, destination, string(info))
\tif err := engine.Play(ctx, samples); err != nil {
\t\ta.removeTXStart(txStartedAt, source, destination, string(info))
\t\ta.setError(err)
''',
        "TX start timestamp and echo-history tracking",
    )
    text = replace_once(
        text,
        '\ta.addTX(kind, string(info), result, frame)',
        '\ta.addTXAt(txStartedAt, kind, string(info), result, frame)',
        "TX row timestamp at audio start",
    )

    text = replace_once(
        text,
        '''\tto, msg, hasMessage := aprs.ParseAPRSMessage(packet.Info)
\tview := PacketView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), Source: packet.Source, Destination: packet.Destination,
\t\tPath: strings.Join(packet.Path, ","), Info: packet.Info, RawHex: strings.ToUpper(hex.EncodeToString(packet.Raw)), PacketText: formatUISSFrame(packet, now)}
''',
        '''\tto, msg, hasMessage := aprs.ParseAPRSMessage(packet.Info)
\tdataType := classifyReceivedPacket(packet, configuredWireCallsign(a.settings))
\tdelayMS := a.findEchoDelayLocked(packet, now)
\tview := PacketView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), DataType: dataType,
\t\tSource: packet.Source, Destination: packet.Destination, Path: strings.Join(packet.Path, ","),
\t\tInfo: packet.Info, DelayMS: delayMS, RawHex: strings.ToUpper(hex.EncodeToString(packet.Raw)), PacketText: formatUISSFrame(packet, now)}
''',
        "received packet row metadata",
    )

    old_addtx_start = "func (a *App) addTX(kind, info, status string, frame []byte) {"
    addtx_end = "// formatTNC2Packet renders an AX.25 UI frame as the conventional APRS/TNC2"
    new_addtx = '''func (a *App) addTX(kind, info, status string, frame []byte) {
\ta.addTXAt(time.Now().UTC(), kind, info, status, frame)
}

func (a *App) addTXAt(at time.Time, kind, info, status string, frame []byte) {
\tnow := at.UTC()
\tpacketText := info
\tlogPacketText := info
\tsource, destination, path := "", "", ""
\tif packet, err := aprs.DecodeUIFrame(frame); err == nil {
\t\tpacketText = formatUISSFrame(packet, now)
\t\tlogPacketText = formatTNC2Packet(packet)
\t\tsource = packet.Source
\t\tdestination = packet.Destination
\t\tpath = strings.Join(packet.Path, ",")
\t}
\trawHex := strings.ToUpper(hex.EncodeToString(frame))
\ta.mu.Lock()
\tview := TxView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), DataType: "TX",
\t\tSource: source, Destination: destination, Path: path, Kind: kind, Info: info, Status: status,
\t\tRawHex: rawHex, PacketText: packetText}
\ta.transmitted = append([]TxView{view}, a.transmitted...)
\tif len(a.transmitted) > 100 {
\t\ta.transmitted = a.transmitted[:100]
\t}
\ta.mu.Unlock()
\ta.appendTxRxLog(now, "TX", fmt.Sprintf("kind=%s %s | status=%s | RAW=%s", kind, logPacketText, status, rawHex))
}

func (a *App) recordTXStart(at time.Time, source, destination, info string) {
\tmark := transmitMark{Timestamp: at.UnixMilli(), Source: source, Destination: destination, Info: info}
\ta.mu.Lock()
\tcutoff := at.Add(-10 * time.Minute).UnixMilli()
\tkept := a.txHistory[:0]
\tfor _, old := range a.txHistory {
\t\tif old.Timestamp >= cutoff {
\t\t\tkept = append(kept, old)
\t\t}
\t}
\ta.txHistory = append(kept, mark)
\tif len(a.txHistory) > 100 {
\t\ta.txHistory = append([]transmitMark(nil), a.txHistory[len(a.txHistory)-100:]...)
\t}
\ta.mu.Unlock()
}

func (a *App) removeTXStart(at time.Time, source, destination, info string) {
\ttimestamp := at.UnixMilli()
\ta.mu.Lock()
\tdefer a.mu.Unlock()
\tfor i := len(a.txHistory) - 1; i >= 0; i-- {
\t\tmark := a.txHistory[i]
\t\tif mark.Timestamp == timestamp && mark.Source == source && mark.Destination == destination && mark.Info == info {
\t\t\ta.txHistory = append(a.txHistory[:i], a.txHistory[i+1:]...)
\t\t\treturn
\t\t}
\t}
}

// findEchoDelayLocked must be called while App.mu is held. It associates an
// RX frame with the most recent matching TX start, allowing the TX/RX table to
// show an approximate end-to-end echo delay without changing the decoder.
func (a *App) findEchoDelayLocked(packet aprs.Packet, receivedAt time.Time) int64 {
\tconst maxEchoAgeMS = int64(10 * time.Minute / time.Millisecond)
\treceivedMS := receivedAt.UnixMilli()
\tfor i := len(a.txHistory) - 1; i >= 0; i-- {
\t\tmark := a.txHistory[i]
\t\telapsed := receivedMS - mark.Timestamp
\t\tif elapsed < 0 || elapsed > maxEchoAgeMS {
\t\t\tcontinue
\t\t}
\t\tif packet.Source == mark.Source && packet.Destination == mark.Destination && packet.Info == mark.Info {
\t\t\treturn elapsed
\t\t}
\t}
\treturn 0
}

'''
    text = replace_region(text, old_addtx_start, addtx_end, new_addtx, "TX row construction and delay history")

    old_dest_helper = '''func isValidFrameDestination(value string) bool {
\tswitch strings.ToUpper(strings.TrimSpace(value)) {
\tcase "CQ", "APRS", "BEACON", "QST", "APRSAT":
\t\treturn true
\tdefault:
\t\treturn false
\t}
}
'''
    new_dest_helper = '''func normalizeFrameDestination(value string) (string, error) {
\tvalue = strings.ToUpper(strings.TrimSpace(value))
\tif value == "" {
\t\treturn "", errors.New("帧目的地址不能为空")
\t}
\tframe, err := aprs.EncodeUIFrame("N0CALL", value, nil, nil)
\tif err != nil {
\t\treturn "", fmt.Errorf("无效 AX.25 帧目的地址 %q：%w", value, err)
\t}
\tpacket, err := aprs.DecodeUIFrame(frame)
\tif err != nil {
\t\treturn "", fmt.Errorf("规范化 AX.25 帧目的地址失败：%w", err)
\t}
\treturn packet.Destination, nil
}

func isValidFrameDestination(value string) bool {
\t_, err := normalizeFrameDestination(value)
\treturn err == nil
}

func configuredWireCallsign(s Settings) string {
\tbaseCall, specialSuffix, err := parseConfiguredCallsign(s.Callsign)
\tif err != nil || baseCall == "" {
\t\treturn ""
\t}
\twireSSID := s.SSID
\tif specialSuffix != "" {
\t\tif n, parseErr := strconv.Atoi(specialSuffix); parseErr == nil && n >= 0 && n <= 15 {
\t\t\twireSSID = n
\t\t}
\t}
\tif wireSSID > 0 {
\t\treturn fmt.Sprintf("%s-%d", baseCall, wireSSID)
\t}
\treturn baseCall
}

func classifyReceivedPacket(packet aprs.Packet, ownCall string) string {
\tif ownCall != "" && strings.EqualFold(strings.TrimSpace(packet.Source), ownCall) {
\t\treturn "接收自己"
\t}
\tif strings.EqualFold(strings.TrimSpace(packet.Destination), "CQ") || isCQCall(packet.Info) {
\t\treturn "他人呼叫"
\t}
\treturn "接收他人"
}

func isCQCall(info string) bool {
\tinfo = strings.ToUpper(strings.TrimSpace(info))
\treturn info == "CQ" || strings.HasPrefix(info, "CQ ") || strings.HasPrefix(info, "CQ\\t")
}
'''
    text = replace_once(text, old_dest_helper, new_dest_helper, "AX.25 destination validation and RX categorization")

    text = replace_once(
        text,
        '''\tif hisCall == "" {
\t\thisCall = strings.ToUpper(strings.TrimSpace(s.Recipient))
\t}
''',
        '''\tif hisCall == "" {
\t\thisCall = strings.ToUpper(strings.TrimSpace(s.FrameDestination))
\t}
''',
        "HisCall macro fallback",
    )
    return text


def patch_tests(text: str) -> str:
    if "TestPrepareOutboundMessageUsesFrameDestinationAndPlainInfo" in text and "TestClassifyReceivedPacketCategories" in text:
        return text

    stale_default = 'if s.Path != "RS0ISS,SGATE,WIDE2-1" {'
    current_default = 'if s.Path != "ARISS,DP0SNX,WIDE2-1" {'
    if stale_default in text:
        text = replace_once(text, stale_default, current_default, "stale default APRS path assertion")

    new_test = '''func TestFrameDestinationOptions(t *testing.T) {
\tfor _, value := range []string{"CQ", "APRS", "BEACON", "QST", "APRSAT", " aprs ", "N0CALL", "N0CALL-1", "N0CALL/5"} {
\t\tif !isValidFrameDestination(value) {
\t\t\tt.Errorf("expected %q to be a valid frame destination", value)
\t\t}
\t}
\tfor _, value := range []string{"", "N0CALL/QRP", "TOOLONG7", "N0CALL-16", "N0C@LL"} {
\t\tif isValidFrameDestination(value) {
\t\t\tt.Errorf("did not expect %q to be a valid frame destination", value)
\t\t}
\t}
\tif got := defaultSettings().FrameDestination; got != "APRS" {
\t\tt.Fatalf("default frame destination = %q, want APRS", got)
\t}
}

func TestPrepareOutboundMessageUsesFrameDestinationAndPlainInfo(t *testing.T) {
\tsettings := defaultSettings()
\tsettings.Callsign = "BH2VSQ"
\tdestination, info, err := prepareOutboundMessage("n2test-7", "Hello {HisCall}", settings)
\tif err != nil {
\t\tt.Fatalf("prepareOutboundMessage: %v", err)
\t}
\tif destination != "N2TEST-7" {
\t\tt.Fatalf("destination = %q, want N2TEST-7", destination)
\t}
\tif got, want := string(info), "Hello N2TEST-7"; got != want {
\t\tt.Fatalf("information field = %q, want %q", got, want)
\t}
\tif strings.HasPrefix(string(info), ":") {
\t\tt.Fatalf("information field unexpectedly contains APRS addressee syntax: %q", string(info))
\t}
}

func TestNormalizeFrameDestinationCanonicalizesSSID(t *testing.T) {
\tgot, err := normalizeFrameDestination("n0call/5")
\tif err != nil {
\t\tt.Fatalf("normalizeFrameDestination: %v", err)
\t}
\tif got != "N0CALL-5" {
\t\tt.Fatalf("normalized destination = %q, want N0CALL-5", got)
\t}
}

func TestClassifyReceivedPacketCategories(t *testing.T) {
\tcases := []struct {
\t\tname string
\t\tpacket aprs.Packet
\t\townCall string
\t\twant string
\t}{
\t\t{name: "self echo", packet: aprs.Packet{Source: "BH2VSQ-1", Destination: "N2TEST", Info: "HELLO"}, ownCall: "BH2VSQ-1", want: "接收自己"},
\t\t{name: "CQ destination", packet: aprs.Packet{Source: "N2TEST", Destination: "CQ", Info: "HELLO"}, ownCall: "BH2VSQ-1", want: "他人呼叫"},
\t\t{name: "CQ text", packet: aprs.Packet{Source: "N2TEST", Destination: "APRS", Info: "CQ BH2VSQ PN11"}, ownCall: "BH2VSQ-1", want: "他人呼叫"},
\t\t{name: "other station", packet: aprs.Packet{Source: "N2TEST", Destination: "BH2VSQ-1", Info: "HELLO"}, ownCall: "BH2VSQ-1", want: "接收他人"},
\t}
\tfor _, tc := range cases {
\t\tt.Run(tc.name, func(t *testing.T) {
\t\t\tif got := classifyReceivedPacket(tc.packet, tc.ownCall); got != tc.want {
\t\t\t\tt.Fatalf("classifyReceivedPacket() = %q, want %q", got, tc.want)
\t\t\t}
\t\t})
\t}
}

func TestFindEchoDelayMatchesLastMatchingTransmission(t *testing.T) {
\tapp := NewApp()
\tstart := time.Date(2026, time.October, 17, 4, 0, 0, 0, time.UTC)
\tapp.txHistory = []transmitMark{
\t\t{Timestamp: start.UnixMilli(), Source: "BH2VSQ", Destination: "N2TEST", Info: "OLD"},
\t\t{Timestamp: start.Add(2 * time.Second).UnixMilli(), Source: "BH2VSQ", Destination: "N2TEST", Info: "HELLO"},
\t}
\tpacket := aprs.Packet{Source: "BH2VSQ", Destination: "N2TEST", Path: []string{"ISS*"}, Info: "HELLO"}
\tgot := app.findEchoDelayLocked(packet, start.Add(3250*time.Millisecond))
\tif got != 1250 {
\t\tt.Fatalf("echo delay = %d ms, want 1250 ms", got)
\t}
}

'''
    text = replace_region(text, "func TestFrameDestinationOptions(t *testing.T) {", "func TestDefaultPresetMessagesFollowRadioExchangeDefaults(t *testing.T) {", new_test, "frame destination and plain-info tests")
    text = replace_once(
        text,
        't.Fatalf("F1 must reset the recipient to CQ by default: %#v", s.PresetResetCQ)',
        't.Fatalf("F1 must switch the frame destination to CQ by default: %#v", s.PresetResetCQ)',
        "preset CQ test wording",
    )
    return text


def patch_html(text: str) -> str:
    # Some deployed frontend builds already contain the single editable frame-
    # destination input and its datalist, with the old recipient field removed.
    # In that case, leave those existing UI controls untouched and allow the
    # follow-up pass below to update the table renderer, heading, and scrolling.
    has_single_destination_input = (
        'id="frameDestinationOptions"' in text
        and 'id="frameDestination"' in text
        and 'id="recipient"' not in text
        and "$('recipient')" not in text
        and 'id="frameDestination" class="input" list="frameDestinationOptions"' in text
    )
    if has_single_destination_input:
        return text

    if "txrx-table" in text and "dataType||'接收他人'" in text and "id=\"frameDestinationOptions\"" in text and "$('recipient')" not in text:
        return text

    text = replace_once(
        text,
        ".recipient-row{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;align-items:end}",
        ".recipient-row{display:grid;grid-template-columns:minmax(0,1fr);gap:8px;align-items:end}",
        "single frame-destination input layout",
    )

    old_dest_ui = '''          <div class="recipient-row">
            <div><label class="field-label" for="frameDestination">帧目的地址</label><select id="frameDestination" class="input" aria-label="AX.25 帧目的地址"><option value="CQ">CQ</option><option value="APRS" selected>APRS</option><option value="BEACON">BEACON</option><option value="QST">QST</option><option value="APRSAT">APRSAT</option></select></div>
            <div><label class="field-label" for="recipient">对方呼号</label><input id="recipient" class="input" maxlength="16" placeholder="例如 N0CALL-1、N0CALL/5 或 CQ" autocomplete="off"></div>
            
          </div>
'''
    new_dest_ui = '''          <div class="recipient-row">
            <div>
              <label class="field-label" for="frameDestination">帧目的地址</label>
              <input id="frameDestination" class="input" list="frameDestinationOptions" maxlength="9" placeholder="选择 CQ，或输入对方呼号，例如 N0CALL-1" autocomplete="off" autocapitalize="characters" spellcheck="false" aria-label="AX.25 帧目的地址">
              <datalist id="frameDestinationOptions"><option value="CQ"></option><option value="APRS"></option><option value="BEACON"></option><option value="QST"></option><option value="APRSAT"></option></datalist>
              <div class="recipient-help">可从下拉列表选择常用目的地址，也可直接输入有效 AX.25 呼号。双击左侧已解码呼号可自动填入此处。</div>
            </div>
          </div>
'''
    text = replace_once(text, old_dest_ui, new_dest_ui, "frame-destination input and dropdown list")

    text = replace_once(
        text,
        '<div class="compose-label"><strong>自定义 APRS 消息</strong><span>最多 67 字节</span></div>',
        '<div class="compose-label"><strong>自定义消息</strong><span>纯信息字段 · 最多 67 字节</span></div>',
        "custom-message heading",
    )
    text = replace_once(
        text,
        'placeholder="输入要发送的 APRS 消息…"',
        'placeholder="输入要发送的信息…"',
        "custom-message placeholder",
    )
    text = replace_once(
        text,
        '支持变量：{CALL} 本机呼号、{HisCall} 对方呼号、{QTH} 位置描述、{GRID} 网格定位符、{UTC} UTC 时间。消息正文最多 67 字节。',
        '支持变量：{CALL} 本机呼号、{HisCall} 当前帧目的地址、{QTH} 位置描述、{GRID} 网格定位符、{UTC} UTC 时间。信息字段仅发送正文，不再添加 APRS 消息收件人字段；最多 67 字节。',
        "preset variable help",
    )
    text = replace_once(
        text,
        '<strong>发送时将目标呼号设为 CQ</strong><div class="field-help">启用后，按下此预设会先将“对方呼号”改为 CQ，再发送消息。</div>',
        '<strong>发送时将帧目的地址切换为 CQ</strong><div class="field-help">启用后，按下此预设会先将帧目的地址改为 CQ，再发送消息。</div>',
        "preset CQ toggle semantics",
    )
    text = replace_once(
        text,
        'aria-label="发送时将目标呼号设为 CQ"',
        'aria-label="发送时将帧目的地址切换为 CQ"',
        "preset CQ toggle aria label",
    )

    new_render = '''function renderPackets(){
    const rx=state?.received||[],tx=state?.transmitted||[];
    $('rxCount').textContent=rx.length;$('txCount').textContent=tx.length;
    const root=$('txrxFeed');
    const historyLimit=200;
    const events=[
      ...rx.map(p=>({type:p.dataType||'接收他人',utc:p.utc||'',timestamp:Number(p.timestamp||0),source:p.source||'',destination:p.destination||'',path:p.path||'',delayMs:Number(p.delayMs||0),message:p.info??'',status:'',key:p.rawHex||`${p.utc}/${p.source}/${p.info}`})),
      ...tx.map(p=>({type:p.dataType||'TX',utc:p.utc||'',timestamp:Number(p.timestamp||0),source:p.source||'',destination:p.destination||'',path:p.path||'',delayMs:0,message:p.info??'',status:p.status||'',key:p.rawHex||`${p.utc}/${p.kind}/${p.info}`}))
    ];
    events.sort((a,b)=>(a.timestamp&&b.timestamp?a.timestamp-b.timestamp:String(a.utc).localeCompare(String(b.utc)))||a.key.localeCompare(b.key));
    const visible=events.slice(-historyLimit),signature=JSON.stringify(visible);
    if(signature!==exchangeSignature){
      exchangeSignature=signature;
      if(!visible.length){root.innerHTML='<div class="empty">等待接收解码或发送消息…</div>';}
      else {
        const rows=visible.map(e=>{
          const typeClass=e.status?.includes('失败')?'error':e.type==='TX'?'tx':e.type==='接收自己'?'rx-self':e.type==='他人呼叫'?'rx-call':'rx-other';
          const delay=e.delayMs>0?`${(e.delayMs/1000).toFixed(2)} s`:'';
          return `<tr title="${esc(e.status||'')}"><td class="col-utc">${esc(e.utc)}Z</td><td><span class="txrx-badge ${typeClass}">${esc(e.type)}</span></td><td>${esc(e.source)}</td><td>${esc(e.destination)}</td><td>${esc(e.path||'DIRECT')}</td><td class="col-delay">${esc(delay)}</td><td class="col-message" title="${esc(e.message)}">${esc(e.message)}</td></tr>`;
        }).join('');
        root.innerHTML=`<table class="txrx-table" aria-label="TX/RX 七列通联列表"><thead><tr><th>UTC</th><th></th><th>From（发送自）</th><th>To（帧目的地址）</th><th>Via（路径）</th><th>Delay</th><th>Message（通联信息）</th></tr></thead><tbody>${rows}</tbody></table>`;
      }
    }
    root.scrollTop=root.scrollHeight;
  }
'''
    text = replace_region(text, "function renderPackets(){", "  function addConsole(type,text,time=''){", new_render, "seven-column TX/RX table renderer")

    text = replace_once(
        text,
        '''    $('recipient').value=call;
    $('recipient').dispatchEvent(new Event('input',{bubbles:true}));
    $('recipient').focus();
    toast(`目标呼号已填入：${call}`);
''',
        '''    $('frameDestination').value=call;
    $('frameDestination').dispatchEvent(new Event('input',{bubbles:true}));
    $('frameDestination').focus();
    toast(`帧目的地址已填入：${call}`);
''',
        "double-click decoded callsign destination behavior",
    )

    text = replace_once(
        text,
        "function formSettings(){const pttMode=$('pttMode').value;return {theme:$('darkTheme').checked?'dark':'light',callsign:$('callsign').value,ssid:Number($('ssid').value||0),qth:$('qth').value,grid:$('grid').value,path:$('path').value,frameDestination:$('frameDestination').value,recipient:$('recipient').value,presetMessages:",
        "function formSettings(){const pttMode=$('pttMode').value;return {theme:$('darkTheme').checked?'dark':'light',callsign:$('callsign').value,ssid:Number($('ssid').value||0),qth:$('qth').value,grid:$('grid').value,path:$('path').value,frameDestination:$('frameDestination').value,presetMessages:",
        "remove recipient from settings form payload",
    )
    text = replace_once(
        text,
        "['path','path'],['recipient','recipient'],['rigHost','rigHost']",
        "['path','path'],['rigHost','rigHost']",
        "remove recipient input from settings restoration loop",
    )
    text = replace_once(
        text,
        "function closeSettings(discard=true){const workspace={frameDestination:$('frameDestination').value,recipient:$('recipient').value,presets:",
        "function closeSettings(discard=true){const workspace={frameDestination:$('frameDestination').value,presets:",
        "remove recipient from settings-dialog workspace",
    )
    text = replace_once(
        text,
        "fillSettings(latestSettings);$('frameDestination').value=workspace.frameDestination;$('recipient').value=workspace.recipient;workspace.presets.forEach",
        "fillSettings(latestSettings);$('frameDestination').value=workspace.frameDestination;workspace.presets.forEach",
        "remove recipient restore in settings close",
    )

    old_send = '''  async function sendPreset(index){const slot=index+1;if(!$('preset'+slot).value.trim()){toast(`F${slot} 尚未设置消息正文，请在预设按钮上右键编辑`,true);openPresetEditor(index);return;}if($('presetResetCQ'+slot).checked)$('recipient').value='CQ';try{await saveCurrent(false);await invoke('SendPreset',index,$('recipient').value);toast(`已提交预设消息 F${slot}`);await poll()}catch(e){toast(e.message||String(e),true);await poll()}}
  async function sendCustom(){try{await saveCurrent(false);await invoke('SendCustomMessage',$('recipient').value,$('customText').value);toast('自定义消息发射完成');await poll()}catch(e){toast(e.message||String(e),true);await poll()}}
'''
    new_send = '''  async function sendPreset(index){const slot=index+1;if(!$('preset'+slot).value.trim()){toast(`F${slot} 尚未设置消息正文，请在预设按钮上右键编辑`,true);openPresetEditor(index);return;}if($('presetResetCQ'+slot).checked){$('frameDestination').value='CQ';}$('frameDestination').value=$('frameDestination').value.trim().toUpperCase();try{await saveCurrent(false);await invoke('SendPreset',index,$('frameDestination').value);toast(`已提交预设消息 F${slot}`);await poll()}catch(e){toast(e.message||String(e),true);await poll()}}
  async function sendCustom(){try{$('frameDestination').value=$('frameDestination').value.trim().toUpperCase();await saveCurrent(false);await invoke('SendCustomMessage',$('frameDestination').value,$('customText').value);toast('自定义消息发射完成');await poll()}catch(e){toast(e.message||String(e),true);await poll()}}
'''
    text = replace_once(text, old_send, new_send, "send preset/custom destination argument")

    # The table overrides old single-line TX/RX styles. Insert after all previous
    # CSS rules so responsive and theme-specific legacy selectors cannot hide columns.
    css = '''
    /* Seven-column AX.25 TX/RX table. Horizontal scrolling is intentional on narrow windows. */
    .txrx-feed{padding:0!important;overflow-x:auto!important;overflow-y:auto!important;scrollbar-width:thin}
    .txrx-table{width:100%;min-width:810px;table-layout:fixed;border-collapse:separate;border-spacing:0;background:#050c38;color:#e5eeff;font:14px/1.35 "Segoe UI","Microsoft YaHei",sans-serif}
    .txrx-table th{position:sticky;top:0;z-index:2;padding:7px 6px;text-align:left;white-space:nowrap;background:#152342;color:#c8d7f0;border-bottom:1px solid #31456c;font-size:13px;font-weight:800}
    .txrx-table td{padding:6px;border-bottom:1px solid #192652;vertical-align:top;overflow-wrap:anywhere;word-break:break-word}
    .txrx-table tbody tr:hover{background:#10203b}
    .txrx-table th:nth-child(1),.txrx-table td:nth-child(1){width:78px}
    .txrx-table th:nth-child(2),.txrx-table td:nth-child(2){width:100px}
    .txrx-table th:nth-child(3),.txrx-table td:nth-child(3){width:98px}
    .txrx-table th:nth-child(4),.txrx-table td:nth-child(4){width:98px}
    .txrx-table th:nth-child(5),.txrx-table td:nth-child(5){width:130px}
    .txrx-table th:nth-child(6),.txrx-table td:nth-child(6){width:78px}
    .txrx-table .col-utc,.txrx-table .col-delay{white-space:nowrap;font-variant-numeric:tabular-nums}
    .txrx-table .col-utc{color:#9db0d8}
    .txrx-table .col-message{white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word;font-family:Consolas,"Cascadia Mono",monospace}
    .txrx-badge{display:inline-block;padding:2px 5px;border-radius:4px;font-size:12px;line-height:1.3;font-weight:850;white-space:nowrap}
    .txrx-badge.tx{background:#123548;color:#5bcffa}
    .txrx-badge.rx-self{background:#3a2a14;color:#ffd58e}
    .txrx-badge.rx-other{background:#183b2b;color:#98f06b}
    .txrx-badge.rx-call{background:#45223c;color:#ffa9dc}
    .txrx-badge.error{background:#452332;color:#ff9aab}
    html[data-theme="light"] .txrx-table{background:#fff;color:#1e293b}
    html[data-theme="light"] .txrx-table th{background:#eaf1f9;color:#334155;border-bottom-color:#cbd5e1}
    html[data-theme="light"] .txrx-table td{border-bottom-color:#e2e8f0}
    html[data-theme="light"] .txrx-table tbody tr:hover{background:#f1f7fd}
    html[data-theme="light"] .txrx-table .col-utc{color:#64748b}
    html[data-theme="light"] .txrx-badge.tx{background:#e0f2fe;color:#075985}
    html[data-theme="light"] .txrx-badge.rx-self{background:#fef3c7;color:#92400e}
    html[data-theme="light"] .txrx-badge.rx-other{background:#dcfce7;color:#166534}
    html[data-theme="light"] .txrx-badge.rx-call{background:#fce7f3;color:#9d174d}
    html[data-theme="light"] .txrx-badge.error{background:#ffe4e6;color:#9f1239}
'''
    if text.count("</style>") != 1:
        raise RuntimeError("HTML </style> anchor must be unique; no files written")
    text = text.replace("</style>", css + "\n  </style>", 1)

    # The removed recipient field was still referenced by the keyboard/action
    # dispatcher. Redirect that action to the surviving frame-destination input.
    text = replace_once(
        text,
        "case 'focus-recipient':$('recipient').focus();break;",
        "case 'focus-frame-destination':$('frameDestination').focus();break;",
        "redirect focus shortcut to frame destination",
    )

    # Ensure all UI references to the removed recipient input are gone.
    leftovers = [token for token in ["$('recipient')", 'for="recipient"', 'id="recipient"'] if token in text]
    if leftovers:
        raise RuntimeError(f"HTML still references removed recipient input: {leftovers}; no files written")
    return text



def patch_app_followup(text: str) -> str:
    # Refine RX classification using the station callsign in the info field.
    new_logic = '''func classifyReceivedPacket(packet aprs.Packet, ownCall string) string {
\tif ownCall != "" && strings.EqualFold(strings.TrimSpace(packet.Source), strings.TrimSpace(ownCall)) {
\t\treturn "接收自己"
\t}
\tif containsCallsign(packet.Info, ownCall) {
\t\treturn "他人呼叫"
\t}
\treturn "接收他人"
}

func containsCallsign(info, ownCall string) bool {
\tinfo = strings.ToUpper(info)
\townCall = strings.ToUpper(strings.TrimSpace(ownCall))
\tif ownCall == "" {
\t\treturn false
\t}
\tcandidates := []string{ownCall}
\t// A configured station may use a separate SSID. Also recognize its base call,
\t// because other stations commonly address the operator without that SSID.
\tif dash := strings.LastIndexByte(ownCall, '-'); dash > 0 && dash < len(ownCall)-1 {
\t\tsuffix := ownCall[dash+1:]
\t\tdigitsOnly := true
\t\tfor i := 0; i < len(suffix); i++ {
\t\t\tif suffix[i] < '0' || suffix[i] > '9' {
\t\t\t\tdigitsOnly = false
\t\t\t\tbreak
\t\t\t}
\t\t}
\t\tif digitsOnly {
\t\t\tcandidates = append(candidates, ownCall[:dash])
\t\t}
\t}
\tfor _, candidate := range candidates {
\t\tfor offset := 0; offset <= len(info)-len(candidate); {
\t\t\trelative := strings.Index(info[offset:], candidate)
\t\t\tif relative < 0 {
\t\t\t\tbreak
\t\t\t}
\t\t\tstart := offset + relative
\t\t\tend := start + len(candidate)
\t\t\tleftBoundary := start == 0 || !isCallsignAlphaNumeric(info[start-1])
\t\t\trightBoundary := end == len(info) || !isCallsignAlphaNumeric(info[end])
\t\t\tif leftBoundary && rightBoundary {
\t\t\t\treturn true
\t\t\t}
\t\t\toffset = start + 1
\t\t}
\t}
\treturn false
}

func isCallsignAlphaNumeric(value byte) bool {
\treturn value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
'''
    text = replace_region(
        text,
        "func classifyReceivedPacket(packet aprs.Packet, ownCall string) string {",
        "func isCQCall(info string) bool {",
        new_logic + "\n",
        "RX callsign-in-info classification",
    )
    return text


def patch_tests_followup(text: str) -> str:
    new_test = '''func TestClassifyReceivedPacketCategories(t *testing.T) {
\tcases := []struct {
\t\tname   string
\t\tpacket aprs.Packet
\t\townCall string
\t\twant   string
\t}{
\t\t{name: "self echo", packet: aprs.Packet{Source: "BH2VSQ-1", Destination: "N2TEST", Info: "HELLO"}, ownCall: "BH2VSQ-1", want: "接收自己"},
\t\t{name: "CQ destination without our callsign", packet: aprs.Packet{Source: "N2TEST", Destination: "CQ", Info: "HELLO"}, ownCall: "BH2VSQ-1", want: "接收他人"},
\t\t{name: "configured callsign in info", packet: aprs.Packet{Source: "N2TEST", Destination: "APRS", Info: "HELLO BH2VSQ-1, ARE YOU THERE"}, ownCall: "BH2VSQ-1", want: "他人呼叫"},
\t\t{name: "base callsign when own SSID configured", packet: aprs.Packet{Source: "N2TEST", Destination: "APRS", Info: "CQ BH2VSQ ARE YOU THERE"}, ownCall: "BH2VSQ-1", want: "他人呼叫"},
\t\t{name: "callsign substring is not a call", packet: aprs.Packet{Source: "N2TEST", Destination: "APRS", Info: "HELLO XBH2VSQZ"}, ownCall: "BH2VSQ-1", want: "接收他人"},
\t\t{name: "other station", packet: aprs.Packet{Source: "N2TEST", Destination: "BH2VSQ-1", Info: "HELLO N2TEST"}, ownCall: "BH2VSQ-1", want: "接收他人"},
\t}
\tfor _, tc := range cases {
\t\tt.Run(tc.name, func(t *testing.T) {
\t\t\tif got := classifyReceivedPacket(tc.packet, tc.ownCall); got != tc.want {
\t\t\t\tt.Fatalf("classifyReceivedPacket() = %q, want %q", got, tc.want)
\t\t\t}
\t\t})
\t}
}

'''
    return replace_region(
        text,
        "func TestClassifyReceivedPacketCategories(t *testing.T) {",
        "func TestFindEchoDelayMatchesLastMatchingTransmission(t *testing.T) {",
        new_test,
        "RX category regression tests",
    )


def patch_html_followup(text: str) -> str:
    # HTML datalist suggestions are not consistently presented as an obvious
    # always-available dropdown in WebView2. Add a visible preset selector next
    # to the still-editable AX.25 destination input.
    if 'id="frameDestinationPreset"' not in text:
        matches = list(re.finditer(
            r'(?m)^([ \t]*)(<input\b(?=[^>]*\bid="frameDestination")(?=[^>]*\blist="frameDestinationOptions")[^>]*>)',
            text,
        ))
        if len(matches) != 1:
            raise RuntimeError(f"frame destination input for visible preset selector: expected one match, found {len(matches)}; no files written")
        match = matches[0]
        indent, input_tag = match.group(1), match.group(2)
        replacement = (
            f'{indent}<div class="frame-destination-controls">\n'
            f'{indent}  {input_tag}\n'
            f'{indent}  <select id="frameDestinationPreset" class="select" aria-label="选择常用帧目的地址">\n'
            f'{indent}    <option value="">常用地址…</option>\n'
            f'{indent}    <option value="CQ">CQ</option>\n'
            f'{indent}    <option value="APRS">APRS</option>\n'
            f'{indent}    <option value="BEACON">BEACON</option>\n'
            f'{indent}    <option value="QST">QST</option>\n'
            f'{indent}    <option value="APRSAT">APRSAT</option>\n'
            f'{indent}  </select>\n'
            f'{indent}</div>'
        )
        text = text[:match.start()] + replacement + text[match.end():]

    preset_css_marker = "/* Explicit visible frame-destination quick selector. */"
    if preset_css_marker not in text:
        preset_css = '''
    /* Explicit visible frame-destination quick selector. */
    .frame-destination-controls{display:grid;grid-template-columns:minmax(0,1fr) minmax(112px,.52fr);gap:6px;align-items:stretch;min-width:0}
    .frame-destination-controls>.input,.frame-destination-controls>.select{width:100%;min-width:0;padding:6px 7px;font-size:15px}
    .frame-destination-controls>.select{cursor:pointer;background:var(--panel-alt)}
    @media(max-width:520px){.frame-destination-controls{grid-template-columns:minmax(0,1fr) 108px;gap:4px}.frame-destination-controls>.input,.frame-destination-controls>.select{padding:5px;font-size:14px}}
'''
        if text.count("</style>") != 1:
            raise RuntimeError("HTML </style> anchor must be unique while adding visible destination selector; no files written")
        text = text.replace("</style>", preset_css + "\n  </style>", 1)

    listener_marker = "// Bind the visible common-address selector to the editable AX.25 destination field."
    if listener_marker not in text:
        listener = '''  // Bind the visible common-address selector to the editable AX.25 destination field.
  const destinationPreset=$('frameDestinationPreset');
  if(destinationPreset){destinationPreset.addEventListener('change',()=>{const value=destinationPreset.value;if(value){$('frameDestination').value=value;$('frameDestination').dispatchEvent(new Event('input',{bubbles:true}));}destinationPreset.value='';});}
'''
        text = replace_once(text, "  function renderPackets(){", listener + "  function renderPackets(){", "bind visible frame-destination preset selector")

    new_render = '''function renderPackets(){
    const rx=state?.received||[],tx=state?.transmitted||[];
    const root=$('txrxFeed');
    const historyLimit=200;
    const events=[
      ...rx.map(p=>({type:p.dataType||'接收他人',utc:p.utc||'',timestamp:Number(p.timestamp||0),source:p.source||'',destination:p.destination||'',path:p.path||'',delayMs:Number(p.delayMs||0),message:p.info??'',status:'',key:p.rawHex||`${p.utc}/${p.source}/${p.info}`})),
      ...tx.map(p=>({type:p.dataType||'TX',utc:p.utc||'',timestamp:Number(p.timestamp||0),source:p.source||'',destination:p.destination||'',path:p.path||'',delayMs:0,message:p.info??'',status:p.status||'',key:p.rawHex||`${p.utc}/${p.kind}/${p.info}`}))
    ];
    events.sort((a,b)=>(a.timestamp&&b.timestamp?a.timestamp-b.timestamp:String(a.utc).localeCompare(String(b.utc)))||a.key.localeCompare(b.key));
    const visible=events.slice(-historyLimit),signature=JSON.stringify(visible);
    const newestRX=rx.reduce((latest,p)=>!latest||Number(p.timestamp||0)>=Number(latest.timestamp||0)?p:latest,null);
    const latestRxSignature=newestRX?`${Number(newestRX.timestamp||0)}|${newestRX.rawHex||`${newestRX.utc||''}/${newestRX.source||''}/${newestRX.info||''}`}`:'';
    const previousRxSignature=root.dataset.latestRxSignature||'';
    const hasNewDecode=Boolean(latestRxSignature&&latestRxSignature!==previousRxSignature);
    const newestTX=tx.reduce((latest,p)=>!latest||Number(p.timestamp||0)>=Number(latest.timestamp||0)?p:latest,null);
    const latestTxSignature=newestTX?`${Number(newestTX.timestamp||0)}|${newestTX.rawHex||`${newestTX.utc||''}/${newestTX.kind||''}/${newestTX.info||''}/${newestTX.status||''}`}`:'';
    const previousTxSignature=root.dataset.latestTxSignature||'';
    const hasNewTransmit=Boolean(latestTxSignature&&latestTxSignature!==previousTxSignature);
    const previousScrollTop=root.scrollTop;
    if(signature!==exchangeSignature){
      exchangeSignature=signature;
      const rows=visible.length?visible.map(e=>{
        const typeClass=e.status?.includes('失败')?'error':e.type==='TX'?'tx':e.type==='接收自己'?'rx-self':e.type==='他人呼叫'?'rx-call':'rx-other';
        const badgeText=e.type==='TX'?'TX':'RX';
        const delay=e.delayMs>0?`${(e.delayMs/1000).toFixed(2)} s`:'';
        return `<tr title="${esc(e.status||e.type)}"><td class="col-utc">${esc(e.utc)}Z</td><td><span class="txrx-badge ${typeClass}" title="${esc(e.type)}">${badgeText}</span></td><td>${esc(e.source)}</td><td>${esc(e.destination)}</td><td>${esc(e.path||'DIRECT')}</td><td class="col-delay">${esc(delay)}</td><td class="col-message">${esc(e.message)}</td></tr>`;
      }).join(''):'<tr class="txrx-empty-row"><td colspan="7">等待接收解码或发送消息…</td></tr>';
      root.innerHTML=`<table class="txrx-table" aria-label="TX/RX 七列通联列表"><thead><tr><th>UTC</th><th></th><th>From</th><th>To</th><th>Via</th><th>Delay</th><th>Message</th></tr></thead><tbody>${rows}</tbody></table>`;
      // Follow the bottom for a newly decoded RX packet or a newly transmitted
      // record. Timer-driven refreshes preserve the user's manual scroll position.
      root.scrollTop=(hasNewDecode||hasNewTransmit)?root.scrollHeight:previousScrollTop;
    }
    root.dataset.latestRxSignature=latestRxSignature;
    root.dataset.latestTxSignature=latestTxSignature;
  }
'''
    text = replace_region(text, "function renderPackets(){", "  function addConsole(type,text,time=''){", new_render, "TX/RX table labels, header and scroll behavior")

    if 'id="frameDestinationPreset"' not in text:
        raise RuntimeError("visible frame destination selector was not inserted; no files written")
    for option in ['<option value="CQ">CQ</option>', '<option value="APRS">APRS</option>', '<option value="BEACON">BEACON</option>', '<option value="QST">QST</option>', '<option value="APRSAT">APRSAT</option>']:
        if option not in text:
            raise RuntimeError(f"Visible frame destination selector option is missing: {option}; no files written")

    # Remove the old panel title and live record counters. The table header is now
    # the always-visible header, including when the table has no packets yet.
    old_panel_head = '''        <div class="panel-head">
          <div class="panel-title">TX / RX 信息</div>
          <span class="panel-sub"><span id="rxCount">0</span> 接收 · <span id="txCount">0</span> 发射</span>
        </div>
'''
    new_panel_head = ""
    if old_panel_head in text:
        text = replace_once(text, old_panel_head, new_panel_head, "remove obsolete TX/RX title and counters")
    elif 'id="rxCount"' in text or 'id="txCount"' in text:
        raise RuntimeError("TX/RX panel heading differs from the expected version; no files written")

    # renderState runs once per polling tick. Do not forcibly reset the TX/RX feed
    # scroll position there; renderPackets now follows the bottom only on changes.
    old_scroll = "['txrxFeed','monitorScreen','heardCallsignList'].forEach(id=>{$(id).scrollTop=$(id).scrollHeight});"
    new_scroll = "['monitorScreen','heardCallsignList'].forEach(id=>{$(id).scrollTop=$(id).scrollHeight});"
    if old_scroll in text:
        text = replace_once(text, old_scroll, new_scroll, "preserve manual TX/RX scrolling between decodes")
    elif old_scroll not in text and new_scroll not in text:
        raise RuntimeError("renderState scroll-reset anchor not found; no files written")

    css_marker = "/* TX/RX follow-up: wrapped messages, visible sticky header and independent history scrolling. */"
    if css_marker not in text:
        css = '''
    /* TX/RX follow-up: wrapped messages, visible sticky header and independent history scrolling. */
    .txrx-feed{padding:0!important;min-height:0!important;overflow-x:hidden!important;overflow-y:auto!important;scrollbar-width:thin;scrollbar-gutter:stable}
    .txrx-feed::-webkit-scrollbar{display:block;width:8px;height:0}
    .txrx-table{width:100%;min-width:0;table-layout:fixed;border-collapse:separate;border-spacing:0;background:#050c38;color:#e5eeff;font:12.5px/1.35 "Segoe UI","Microsoft YaHei",sans-serif}
    .txrx-table th{position:sticky;top:0;z-index:3;padding:6px 4px;text-align:left;white-space:normal;overflow-wrap:anywhere;background:#152342;color:#c8d7f0;border-bottom:1px solid #31456c;font-size:12px;font-weight:800}
    .txrx-table td{padding:5px 4px;border-bottom:1px solid #192652;vertical-align:top;min-width:0;overflow-wrap:anywhere;word-break:break-word}
    .txrx-table th:nth-child(1),.txrx-table td:nth-child(1){width:9%}
    .txrx-table th:nth-child(2),.txrx-table td:nth-child(2){width:6%}
    .txrx-table th:nth-child(3),.txrx-table td:nth-child(3){width:9%}
    .txrx-table th:nth-child(4),.txrx-table td:nth-child(4){width:9%}
    .txrx-table th:nth-child(5),.txrx-table td:nth-child(5){width:14%}
    .txrx-table th:nth-child(6),.txrx-table td:nth-child(6){width:7%}
    .txrx-table .col-utc,.txrx-table .col-delay{white-space:normal;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}
    .txrx-table .col-message{white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word;font-family:Consolas,"Cascadia Mono",monospace;overflow:visible}
    .txrx-table .txrx-empty-row td{text-align:center;color:#9db0d8;padding:18px 8px}
    .txrx-badge{display:inline-block;padding:2px 4px;border-radius:4px;font-size:11px;line-height:1.3;font-weight:850;white-space:nowrap}
    .txrx-badge.tx{background:#123548;color:#5bcffa}
    .txrx-badge.rx-self{background:#3a2a14;color:#ffd58e}
    .txrx-badge.rx-other{background:#183b2b;color:#98f06b}
    .txrx-badge.rx-call{background:#a31343;color:#fff1f2;box-shadow:0 0 0 1px #fb7185 inset}
    .txrx-badge.error{background:#452332;color:#ff9aab}
    html[data-theme="light"] .txrx-table{background:#fff;color:#1e293b}
    html[data-theme="light"] .txrx-table th{background:#eaf1f9;color:#334155;border-bottom-color:#cbd5e1}
    html[data-theme="light"] .txrx-table td{border-bottom-color:#e2e8f0}
    html[data-theme="light"] .txrx-table .txrx-empty-row td{color:#64748b}
    html[data-theme="light"] .txrx-badge.tx{background:#e0f2fe;color:#075985}
    html[data-theme="light"] .txrx-badge.rx-self{background:#fef3c7;color:#92400e}
    html[data-theme="light"] .txrx-badge.rx-other{background:#dcfce7;color:#166534}
    html[data-theme="light"] .txrx-badge.rx-call{background:#be123c;color:#fff1f2;box-shadow:0 0 0 1px #fb7185 inset}
    html[data-theme="light"] .txrx-badge.error{background:#ffe4e6;color:#9f1239}
'''
        if text.count("</style>") != 1:
            raise RuntimeError("HTML </style> anchor must be unique; no files written")
        text = text.replace("</style>", css + "\n  </style>", 1)

    text = text.replace("/* Seven-column AX.25 TX/RX table. Horizontal scrolling is intentional on narrow windows. */", "/* Seven-column AX.25 TX/RX table. */")

    # Keep all requested popular AX.25 destinations available from the datalist.
    for option in ['<option value="APRS"></option>', '<option value="BEACON"></option>', '<option value="QST"></option>', '<option value="APRSAT"></option>']:
        if option not in text:
            raise RuntimeError(f"Frame destination option is missing: {option}; no files written")
    return text


def patch_readme_followup(text: str) -> str:
    marker = "## TX/RX 列表交互与类别显示补充"
    if marker in text:
        return text
    return text.rstrip() + '''

## TX/RX 列表交互与类别显示补充

- 所有接收记录的标签文字统一显示为 `RX`。接收自己、接收他人和他人呼叫通过不同颜色区分；他人呼叫表示解码信息正文中包含本机设置的呼号，呼号匹配忽略大小写，并避免将更长呼号中的相同子串误认为呼叫。
- 表头直接放在 TX/RX 表格内，列表为空时也会显示；纵向查看历史记录时，表头固定在可滚动区域顶部。原有 `TX/RX 信息` 标题和接收、发射计数已移除。
- Message 列会自动换行，不创建单独的消息滚动区域，也不需要水平滚动表格。只有出现新的 RX 解码时列表才自动滚动到底部；单纯发送 TX 或定时状态轮询会保留用户手动查看历史的位置。
- 帧目的地址输入框继续提供 `APRS`、`BEACON`、`QST` 和 `APRSAT` 等常用建议，同时可直接输入有效 AX.25 呼号。
'''


def patch_readme(text: str) -> str:
    if "TX/RX 七列列表" in text and "正文保持纯文本" in text:
        return text
    text = replace_once(
        text,
        "- 消息收发区在对方呼号左侧提供 AX.25 帧目的地址选择框，可选 `CQ`、`APRS`、`BEACON`、`QST` 和 `APRSAT`，默认 `APRS`；对方呼号仍是 APRS 消息正文的收件人。双击左侧通信监视日志旁已解码呼号列表中的呼号，即可填入对方呼号。",
        "- 消息收发区使用单一的“帧目的地址”输入框，并保留 `CQ`、`APRS`、`BEACON`、`QST` 和 `APRSAT` 下拉建议，同时允许直接输入有效 AX.25 呼号与 SSID。目标呼号写入 AX.25 帧目的地址，信息字段只发送纯正文；双击左侧已解码呼号即可将其填入帧目的地址。",
        "README message destination description",
    )
    text = replace_once(
        text,
        "- 主界面提供固定 F1 至 F6 六条预设按钮，以可配置的消息备注名称显示：左键发送，右键编辑备注名称、消息正文和“发送时将目标呼号设为 CQ”选项，并自动保存设置。默认 F1=`CQ {CALL} {GRID}`、F2=`UR 5NN`、F3=`TU 73`，F4–F6 正文为空，用户可自行设置。F1 默认启用“发送时将目标呼号设为 CQ”，各预设可单独修改此选项。空白预设不会发射，会打开编辑窗。自定义消息与预设都支持 `{HisCall}` 变量引用对方呼号。",
        "- 主界面提供固定 F1 至 F6 六条预设按钮，以可配置的消息备注名称显示：左键发送，右键编辑备注名称、消息正文和“发送时将帧目的地址切换为 CQ”选项，并自动保存设置。默认 F1=`CQ {CALL} {GRID}`、F2=`UR 5NN`、F3=`TU 73`，F4–F6 正文为空，用户可自行设置。F1 默认启用“发送时将帧目的地址切换为 CQ”，各预设可单独修改此选项。空白预设不会发射，会打开编辑窗。自定义消息与预设都支持 `{HisCall}` 变量引用当前帧目的地址。",
        "README preset CQ description",
    )
    text = replace_once(
        text,
        "- 主窗口为固定视口自适应布局。TX/RX 信息区采用 UISS 风格两行格式显示发送方、帧目的地址、Via 路径、UI/PID、信息字段字节长度、时间和原始信息字段；通信监视日志和已解码呼号列表保留独立纵向滚动条，并自动定位至最新记录。固定界面文字不可选中，输入框和实时日志文字仍可选择/复制。",
        "- 主窗口为固定视口自适应布局。TX/RX 七列列表的表头依次显示 UTC、空白类别列、From（发送自）、To（帧目的地址）、Via（路径）、Delay 和 Message（通联信息）；类别列只用彩色 TX/RX 标签区分。TX、接收自己、接收他人、他人呼叫及发射错误使用不同颜色标签；接收本机回波时，程序按源地址、目的地址与信息内容匹配最近的发射记录并显示近似回波延迟。列表保留历史记录并可横向滚动，通信监视日志和已解码呼号列表继续独立滚动。",
        "README TXRX seven-column description",
    )
    text = replace_once(
        text,
        "- APRS 消息建议使用 ASCII 字符，单条正文最多 67 字节；消息内部的空格、连续空格及首尾空格都会原样保留。自定义消息与预设正文编辑框优先使用 WebView2 原生文本输入；如空格键未产生任何输入，则在按键释放时补插一个 ASCII 空格。目标呼号输入支持 `/5` 等数字后缀映射，以及 `/QRP` 等文本别名按基础呼号路由。",
        "- 消息信息字段建议使用 ASCII 字符，最多 67 字节；正文、连续空格及首尾空格都会原样进入 AX.25 信息字段。当前消息模式使用帧目的地址指定目标，正文保持纯文本，不生成传统 APRS `:收件人   :正文` 消息前缀，因此应与能够按本项目约定处理 AX.25 目的地址的接收端配合使用。自定义消息与预设正文编辑框保留原有空格输入处理。",
        "README PTT and payload notes",
    )
    text = replace_once(
        text,
        "- `{HisCall}` 会替换成当前“对方呼号”输入框的值。",
        "- `{HisCall}` 会替换成当前帧目的地址输入框的值。",
        "README HisCall help",
    )
    text = replace_once(
        text,
        "- 已解码呼号列表与左侧通信监视日志并排显示，采用紧凑行列表；双击某一呼号即可将其填入“对方呼号”。TX/RX 信息区逐行合并显示解码与发送记录，三个实时日志区均可纵向滚动，但会自动回到最新内容。",
        "- 已解码呼号列表与左侧通信监视日志并排显示，采用紧凑行列表；双击某一呼号即可将其填入帧目的地址。TX/RX 区使用七列数据表显示接收与发送记录，可查看自己发送后的回波延迟。",
        "README UI operation description",
    )
    return text

def ensure_packet_display_helpers(text: str) -> str:
    """Restore the two monitor-format helpers if a previous patch removed them."""
    helpers = {
        "formatTNC2Packet": """// formatTNC2Packet renders an AX.25 UI frame as the conventional APRS/TNC2
// monitor line: SOURCE>DESTINATION,PATH:INFO.
func formatTNC2Packet(packet aprs.Packet) string {
	line := packet.Source + ">" + packet.Destination
	if len(packet.Path) > 0 {
		line += "," + strings.Join(packet.Path, ",")
	}
	return line + ":" + packet.Info
}

""",
        "formatUISSFrame": """// formatUISSFrame renders the decoded AX.25 UI frame in the two-line monitor
// style used by UISS. The information length is measured in bytes, as on air.
func formatUISSFrame(packet aprs.Packet, at time.Time) string {
	header := fmt.Sprintf("Fm %s To %s", packet.Source, packet.Destination)
	if len(packet.Path) > 0 {
		header += " Via " + strings.Join(packet.Path, ",")
	}
	header += fmt.Sprintf(" <UI pid=F0 Len=%d >[%s]", len([]byte(packet.Info)), at.Local().Format("15:04:05"))
	return header + "\n" + packet.Info
}

""",
    }
    missing = [name for name in ("formatTNC2Packet", "formatUISSFrame") if f"func {name}(" not in text]
    if not missing:
        return text
    anchor = "func normalizeFrameDestination(value string) (string, error) {"
    if text.count(anchor) != 1:
        raise RuntimeError("cannot restore formatUISSFrame/formatTNC2Packet: normalizeFrameDestination insertion anchor is not unique; no files written")
    block = "".join(helpers[name] for name in missing)
    return replace_once(text, anchor, block + anchor, "restore removed packet display helper functions")


def patch_app_echo_followup(text: str) -> str:
    """Highlight RX frames addressed to this station and measure repeater echoes only."""
    packet_start = text.find("type PacketView struct {")
    txview_match = re.search(r"\ntype TxView struct\s*\{", text[packet_start:] if packet_start >= 0 else "")
    packet_end = packet_start + txview_match.start() if packet_start >= 0 and txview_match else -1
    if packet_start < 0 or packet_end < 0:
        raise RuntimeError("PacketView structure not found; no files written")
    packet_block = text[packet_start:packet_end]
    if 'json:"forMe"' not in packet_block:
        packet_block = replace_once(
            packet_block,
            'DataType    string `json:"dataType"`',
            'DataType    string `json:"dataType"`\n\tForMe       bool   `json:"forMe"`',
            "PacketView forMe field",
        )
        text = text[:packet_start] + packet_block + text[packet_end:]

    if "forMe := frameDestinationMatchesCallsign(packet.Destination, ownCall)" not in text:
        text = replace_once(
            text,
            '\tdataType := classifyReceivedPacket(packet, configuredWireCallsign(a.settings))\n\tdelayMS := a.findEchoDelayLocked(packet, now)\n\tview := PacketView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), DataType: dataType,',
            '\townCall := configuredWireCallsign(a.settings)\n\tdataType := classifyReceivedPacket(packet, ownCall)\n\tforMe := frameDestinationMatchesCallsign(packet.Destination, ownCall)\n\tdelayMS := a.findEchoDelayLocked(packet, now)\n\tview := PacketView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), DataType: dataType, ForMe: forMe,',
            "destination-matches-station metadata",
        )
    elif "ForMe: forMe" not in text:
        raise RuntimeError("forMe receive code is only partially present; no files written")

    if "func frameDestinationMatchesCallsign(" not in text:
        matcher = '''func frameDestinationMatchesCallsign(destination, ownCall string) bool {
	if strings.TrimSpace(destination) == "" || strings.TrimSpace(ownCall) == "" {
		return false
	}
	dest, destErr := normalizeFrameDestination(destination)
	self, selfErr := normalizeFrameDestination(ownCall)
	return destErr == nil && selfErr == nil && strings.EqualFold(dest, self)
}

'''
        text = replace_once(
            text,
            "func classifyReceivedPacket(packet aprs.Packet, ownCall string) string {",
            matcher + "func classifyReceivedPacket(packet aprs.Packet, ownCall string) string {",
            "frame destination to own callsign matcher",
        )

    echo_func = '''func (a *App) findEchoDelayLocked(packet aprs.Packet, receivedAt time.Time) int64 {
	// A repeated-address marker (*) confirms the frame came back through an AX.25 repeater.
	if !packetHasRepeatedPath(packet.Path) {
		return 0
	}
	const maxEchoAgeMS = int64(10 * time.Minute / time.Millisecond)
	receivedMS := receivedAt.UnixMilli()
	for i := len(a.txHistory) - 1; i >= 0; i-- {
		mark := a.txHistory[i]
		elapsed := receivedMS - mark.Timestamp
		if elapsed < 0 || elapsed > maxEchoAgeMS {
			continue
		}
		if strings.EqualFold(packet.Source, mark.Source) && strings.EqualFold(packet.Destination, mark.Destination) && packet.Info == mark.Info {
			return elapsed
		}
	}
	return 0
}

func packetHasRepeatedPath(path []string) bool {
	for _, hop := range path {
		if strings.HasSuffix(strings.TrimSpace(hop), "*") {
			return true
		}
	}
	return false
}

'''
    current_echo_start = "func (a *App) findEchoDelayLocked(packet aprs.Packet, receivedAt time.Time) int64 {"
    # Keep the display helpers after the echo helper. Using normalizeFrameDestination
    # as the end boundary here would accidentally delete formatTNC2Packet and
    # formatUISSFrame, which are still used by RX/TX logging and app_test.go.
    if "func formatTNC2Packet(" in text:
        current_echo_end = (
            "// formatTNC2Packet renders an AX.25 UI frame as the conventional APRS/TNC2"
            if "// formatTNC2Packet renders an AX.25 UI frame as the conventional APRS/TNC2" in text
            else "func formatTNC2Packet("
        )
    else:
        current_echo_end = "func normalizeFrameDestination(value string) (string, error) {"
    try:
        text = replace_region(text, current_echo_start, current_echo_end, echo_func, "repeater downlink echo delay logic")
    except RuntimeError as exc:
        if "packetHasRepeatedPath(packet.Path)" not in text:
            raise exc
    text = ensure_packet_display_helpers(text)
    return text


def patch_tests_echo_followup(text: str) -> str:
    destination_test = '''func TestFrameDestinationMatchesOwnCallsign(t *testing.T) {
	cases := []struct {
		destination string
		ownCall string
		want bool
	}{
		{destination: "BH2VSQ-1", ownCall: "bh2vsq-1", want: true},
		{destination: "BH2VSQ/1", ownCall: "BH2VSQ-1", want: true},
		{destination: "N2TEST", ownCall: "BH2VSQ-1", want: false},
		{destination: "BH2VSQ-1", ownCall: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.destination+"/"+tc.ownCall, func(t *testing.T) {
			if got := frameDestinationMatchesCallsign(tc.destination, tc.ownCall); got != tc.want {
				t.Fatalf("frameDestinationMatchesCallsign(%q, %q) = %v, want %v", tc.destination, tc.ownCall, got, tc.want)
			}
		})
	}
}

'''
    if "func TestFrameDestinationMatchesOwnCallsign(" not in text:
        text = replace_once(
            text,
            "func TestFindEchoDelayMatchesLastMatchingTransmission(t *testing.T) {",
            destination_test + "func TestFindEchoDelayMatchesLastMatchingTransmission(t *testing.T) {",
            "own destination highlight regression tests",
        )

    echo_test = '''func TestFindEchoDelayRequiresRepeatedRepeaterPath(t *testing.T) {
	app := NewApp()
	start := time.Date(2026, time.October, 17, 4, 0, 0, 0, time.UTC)
	app.txHistory = []transmitMark{{Timestamp: start.UnixMilli(), Source: "BH2VSQ", Destination: "N2TEST", Info: "HELLO"}}
	cases := []struct {
		name string
		path []string
		want int64
	}{
		{name: "repeater downlink echo", path: []string{"RS0ISS*"}, want: 950},
		{name: "not marked as repeated", path: []string{"RS0ISS"}, want: 0},
		{name: "direct reception", path: nil, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			packet := aprs.Packet{Source: "BH2VSQ", Destination: "N2TEST", Path: tc.path, Info: "HELLO"}
			if got := app.findEchoDelayLocked(packet, start.Add(950*time.Millisecond)); got != tc.want {
				t.Fatalf("findEchoDelayLocked() = %d, want %d", got, tc.want)
			}
		})
	}
}

'''
    if "func TestFindEchoDelayRequiresRepeatedRepeaterPath(" not in text:
        anchor = "func TestDefaultPresetMessagesFollowRadioExchangeDefaults(t *testing.T) {"
        if anchor not in text:
            raise RuntimeError("preset test insertion anchor not found; no files written")
        text = replace_once(text, anchor, echo_test + anchor, "repeater echo delay regression tests")
    return text


def patch_html_echo_followup(text: str) -> str:
    """Show addressed-to-this-station RX in red and remove hover category tooltips."""
    renderer = '''function renderPackets(){
    const rx=state?.received||[],tx=state?.transmitted||[];
    const root=$('txrxFeed');
    const historyLimit=200;
    const events=[
      ...rx.map(p=>({type:p.dataType||'接收他人',utc:p.utc||'',timestamp:Number(p.timestamp||0),source:p.source||'',destination:p.destination||'',path:p.path||'',delayMs:Number(p.delayMs||0),forMe:Boolean(p.forMe),message:p.info??'',status:'',key:p.rawHex||`${p.utc}/${p.source}/${p.info}`})),
      ...tx.map(p=>({type:p.dataType||'TX',utc:p.utc||'',timestamp:Number(p.timestamp||0),source:p.source||'',destination:p.destination||'',path:p.path||'',delayMs:0,forMe:false,message:p.info??'',status:p.status||'',key:p.rawHex||`${p.utc}/${p.kind}/${p.info}`}))
    ];
    events.sort((a,b)=>(a.timestamp&&b.timestamp?a.timestamp-b.timestamp:String(a.utc).localeCompare(String(b.utc)))||a.key.localeCompare(b.key));
    const visible=events.slice(-historyLimit),signature=JSON.stringify(visible);
    const newestRX=rx.reduce((latest,p)=>!latest||Number(p.timestamp||0)>=Number(latest.timestamp||0)?p:latest,null);
    const latestRxSignature=newestRX?`${Number(newestRX.timestamp||0)}|${newestRX.rawHex||`${newestRX.utc||''}/${newestRX.source||''}/${newestRX.info||''}`}`:'';
    const previousRxSignature=root.dataset.latestRxSignature||'';
    const hasNewDecode=Boolean(latestRxSignature&&latestRxSignature!==previousRxSignature);
    const newestTX=tx.reduce((latest,p)=>!latest||Number(p.timestamp||0)>=Number(latest.timestamp||0)?p:latest,null);
    const latestTxSignature=newestTX?`${Number(newestTX.timestamp||0)}|${newestTX.rawHex||`${newestTX.utc||''}/${newestTX.kind||''}/${newestTX.info||''}/${newestTX.status||''}`}`:'';
    const previousTxSignature=root.dataset.latestTxSignature||'';
    const hasNewTransmit=Boolean(latestTxSignature&&latestTxSignature!==previousTxSignature);
    const previousScrollTop=root.scrollTop;
    if(signature!==exchangeSignature){
      exchangeSignature=signature;
      const rows=visible.length?visible.map(e=>{
        const typeClass=e.status?.includes('失败')?'error':e.type==='TX'?'tx':e.forMe?'rx-to-me':e.type==='接收自己'?'rx-self':e.type==='他人呼叫'?'rx-call':'rx-other';
        const badgeText=e.type==='TX'?'TX':'RX';
        const delay=e.delayMs>0?`${(e.delayMs/1000).toFixed(2)} s`:'';
        return `<tr${e.forMe&&e.type!=='TX'?' class="rx-to-me"':''}><td class="col-utc">${esc(e.utc)}Z</td><td><span class="txrx-badge ${typeClass}">${badgeText}</span></td><td>${esc(e.source)}</td><td>${esc(e.destination)}</td><td>${esc(e.path||'DIRECT')}</td><td class="col-delay">${esc(delay)}</td><td class="col-message">${esc(e.message)}</td></tr>`;
      }).join(''):'<tr class="txrx-empty-row"><td colspan="7">等待接收解码或发送消息…</td></tr>';
      root.innerHTML=`<table class="txrx-table" aria-label="TX/RX 七列通联列表"><thead><tr><th>UTC</th><th></th><th>From</th><th>To</th><th>Via</th><th>Delay</th><th>Message</th></tr></thead><tbody>${rows}</tbody></table>`;
      // Follow the bottom for a newly decoded RX packet or a newly transmitted
      // record. Timer-driven refreshes preserve the user's manual scroll position.
      root.scrollTop=(hasNewDecode||hasNewTransmit)?root.scrollHeight:previousScrollTop;
    }
    root.dataset.latestRxSignature=latestRxSignature;
    root.dataset.latestTxSignature=latestTxSignature;
  }
'''
    text = replace_region(text, "function renderPackets(){", "  function addConsole(type,text,time=''){", renderer, "RX-to-this-station highlight and remove hover tooltips")

    css_marker = "/* TX/RX echo-highlight follow-up: highlight frames addressed to this station. */"
    if css_marker not in text:
        css = '''
    /* TX/RX echo-highlight follow-up: highlight frames addressed to this station. */
    .txrx-table tbody tr.rx-to-me td{background:rgba(239,68,68,.14)!important}
    .txrx-table tbody tr.rx-to-me:hover td{background:rgba(239,68,68,.22)!important}
    .txrx-badge.rx-to-me{background:#dc2626!important;color:#fff!important;box-shadow:0 0 0 1px rgba(185,28,28,.85) inset}
    html[data-theme="light"] .txrx-table tbody tr.rx-to-me td{background:rgba(239,68,68,.12)!important}
    html[data-theme="light"] .txrx-table tbody tr.rx-to-me:hover td{background:rgba(239,68,68,.19)!important}
    html[data-theme="light"] .txrx-badge.rx-to-me{background:#dc2626!important;color:#fff!important;box-shadow:0 0 0 1px rgba(185,28,28,.85) inset}
'''
        if text.count("</style>") != 1:
            raise RuntimeError("HTML </style> anchor must be unique for echo highlight; no files written")
        text = text.replace("</style>", css + "\n  </style>", 1)

    if 'id="frameDestinationPreset"' not in text:
        raise RuntimeError("visible frame-destination selector is missing; apply the preceding message UI update first")
    if ".txrx-badge.rx-to-me" not in text:
        raise RuntimeError("RX highlight CSS was not added; no files written")
    if 'title="${esc(e.type)}"' in text or 'title="${esc(e.status||e.type)}"' in text:
        raise RuntimeError("TX/RX category tooltip remains in renderer; no files written")
    return text


def patch_readme_echo_followup(text: str) -> str:
    marker = "## 本机目的地址高亮与转发器回波延迟"
    if marker in text:
        return text
    return text.rstrip() + '''

## 本机目的地址高亮与转发器回波延迟

- 接收帧的 AX.25 目的地址与当前台站呼号匹配时，RX 标签显示红色，整行叠加淡红色透明背景。呼号比较会规范化大小写及数字 SSID 表示。
- Delay 从发射开始时间计算，到收到源地址、帧目的地址和信息正文均匹配，且 AX.25 路径中包含已转发标记 `*` 的下传回波为止。未经过转发器的直接接收不显示 Delay。
- RX 标签和信息行不再使用鼠标悬停分类提示。
'''


def main() -> int:
    if len(sys.argv) > 1 and sys.argv[1] in {"-h", "--help"}:
        print("Run from the SATAprs-Go repository root, or place this script in that root.")
        return 0
    missing = [str(p.relative_to(ROOT)) for p in TARGETS if not p.is_file()]
    if missing:
        print("未找到以下目标文件，请将脚本放在 SATAprs-Go 仓库根目录：")
        print("\n".join(missing))
        return 2

    loaded = {p: read_text(p) for p in TARGETS}
    old_app, app_nl, app_bom = loaded[TARGETS[0]]
    old_test, test_nl, test_bom = loaded[TARGETS[1]]
    old_html, html_nl, html_bom = loaded[TARGETS[2]]
    old_readme, readme_nl, readme_bom = loaded[TARGETS[3]]

    # If the previous revision already changed the Go code/tests and TX/RX table,
    # apply only the visible-address-selector refinement. Avoid re-running legacy
    # exact-match edits over Go files that are already patched.
    core_patch_present = (
        "func prepareOutboundMessage(" in old_app
        and "func classifyReceivedPacket(" in old_app
        and "txrx-table" in old_html
        and "id=\"frameDestinationOptions\"" in old_html
        and "$('recipient')" not in old_html
        and "TestPrepareOutboundMessageUsesFrameDestinationAndPlainInfo" in old_test
        and "TX/RX 七列列表" in old_readme
        and "func containsCallsign(" in old_app
        and "const badgeText=e.type==='TX'?'TX':'RX';" in old_html
        and "TX/RX 列表交互与类别显示补充" in old_readme
    )
    if core_patch_present:
        try:
            staged_html = old_html
            if 'id="frameDestinationPreset"' not in old_html:
                staged_html = patch_html_followup(patch_html(old_html))
            new_app = patch_app_echo_followup(old_app)
            new_test = patch_tests_echo_followup(old_test)
            new_html = patch_html_echo_followup(staged_html)
            new_readme = patch_readme_echo_followup(old_readme)
        except Exception as exc:
            print(f"补丁未写入任何文件：{exc}")
            print("请保留当前文件并反馈完整提示，以便按你的本地版本调整补丁。")
            return 1

        generated = {
            TARGETS[0]: new_app,
            TARGETS[1]: new_test,
            TARGETS[2]: new_html,
            TARGETS[3]: new_readme,
        }
        changed = [path for path in TARGETS if generated[path] != loaded[path][0]]
        if not changed:
            print("本机目的地址红色高亮、转发器回波 Delay 及无悬停弹窗修改已存在，未重复写入文件。")
            return 0

        stamp = _dt.datetime.now().strftime("%Y%m%d_%H%M%S")
        backups = {}
        try:
            for path in changed:
                _, newline, has_bom = loaded[path]
                backup = path.with_name(path.name + f".before_rx_echo_{stamp}.bak")
                write_text(backup, loaded[path][0], newline, has_bom)
                backups[path] = backup
            for path in changed:
                _, newline, has_bom = loaded[path]
                write_text(path, generated[path], newline, has_bom)
        except Exception as exc:
            print(f"写入期间失败：{exc}")
            print("备份位置：")
            for backup in backups.values():
                print(f"  {backup}")
            return 1

        print("本机目的地址红色 RX 高亮、转发器下传回波 Delay 与移除分类悬停提示的修改已应用。")
        print("Delay 仅在接收到匹配且 AX.25 路径带已转发标记 * 的回波时显示。")
        print("没有修改 AFSK 解码器、音频采集、PTT 或发射音调算法。")
        print("修改文件：" + "、".join(path.name for path in changed))
        print("已创建备份：")
        for backup in backups.values():
            print(f"  {backup}")
        return 0

    # Build all results in memory first. Any mismatch aborts before a file is written.
    try:
        new_app = patch_app_echo_followup(patch_app_followup(patch_app(old_app)))
        new_test = patch_tests_echo_followup(patch_tests_followup(patch_tests(old_test)))
        new_html = patch_html_echo_followup(patch_html_followup(patch_html(old_html)))
        new_readme = patch_readme_echo_followup(patch_readme_followup(patch_readme(old_readme)))
    except Exception as exc:
        print(f"补丁未写入任何文件：{exc}")
        print("请保留当前文件并反馈完整提示，以便按你的本地版本调整补丁。")
        return 1

    stamp = _dt.datetime.now().strftime("%Y%m%d_%H%M%S")
    backups = {}
    try:
        originals = [old_app, old_test, old_html, old_readme]
        for path, original in zip(TARGETS, originals):
            backup = path.with_name(path.name + f".before_message_ui_{stamp}.bak")
            # Preserve original contents and line endings in a separate backup.
            newline = loaded[path][1]
            bom = loaded[path][2]
            write_text(backup, original, newline, bom)
            backups[path] = backup
        write_text(TARGETS[0], new_app, app_nl, app_bom)
        write_text(TARGETS[1], new_test, test_nl, test_bom)
        write_text(TARGETS[2], new_html, html_nl, html_bom)
        write_text(TARGETS[3], new_readme, readme_nl, readme_bom)
    except Exception as exc:
        print(f"写入期间失败：{exc}")
        print("备份位置：")
        for p in backups.values():
            print(f"  {p}")
        return 1

    print("消息地址与七列 TX/RX 列表补丁已应用。")
    print("修改文件：app.go、app_test.go、frontend/dist/index.html、README.md")
    print("没有修改 AFSK 解码器、音频采集、PTT 或发射音调算法。")
    print("已创建备份：")
    for p in backups.values():
        print(f"  {p}")
    print("请运行：")
    print("  gofmt -w app.go app_test.go")
    print("  go test . ./internal/aprs ./internal/rigctl ./internal/pttctl ./internal/audio -count=1")
    print("注意：frontend/dist/index.html 是静态 HTML，无需 gofmt。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
