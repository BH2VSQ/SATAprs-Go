package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BH2VSQ/SatAPRS-Go/internal/aprs"
	"github.com/BH2VSQ/SatAPRS-Go/internal/audio"
	"github.com/BH2VSQ/SatAPRS-Go/internal/pttctl"
	"github.com/BH2VSQ/SatAPRS-Go/internal/rigctl"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const appName = "SatAPRS-Go"

type Settings struct {
	Callsign         string   `json:"callsign"`
	SSID             int      `json:"ssid"`
	QTH              string   `json:"qth"`
	Grid             string   `json:"grid"`
	Path             string   `json:"path"`
	FrameDestination string   `json:"frameDestination"`
	Recipient        string   `json:"recipient"`
	PresetMessages   []string `json:"presetMessages"`
	PresetNames      []string `json:"presetNames"`
	PresetResetCQ    []bool   `json:"presetResetCQ"`
	RigHost          string   `json:"rigHost"`
	RigPort          int      `json:"rigPort"`
	// UseRigPTT is retained for migration from older settings files.
	UseRigPTT          bool   `json:"useRigPTT"`
	PTTMode            string `json:"pttMode"`
	PTTSerialPort      string `json:"pttSerialPort"`
	PTTActiveHigh      bool   `json:"pttActiveHigh"`
	TxDelayMS          int    `json:"txDelayMs"`
	TxTailMS           int    `json:"txTailMs"`
	CaptureDeviceID    string `json:"captureDeviceID"`
	PlaybackDeviceID   string `json:"playbackDeviceID"`
	AutoStartListening bool   `json:"autoStartListening"`
	LogSaveMode        string `json:"logSaveMode"`
	Theme              string `json:"theme"`
}

type PacketView struct {
	UTC         string `json:"utc"`
	Timestamp   int64  `json:"timestamp"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Path        string `json:"path"`
	Info        string `json:"info"`
	MessageTo   string `json:"messageTo"`
	Message     string `json:"message"`
	RawHex      string `json:"rawHex"`
	PacketText  string `json:"packet"`
}

type TxView struct {
	UTC        string `json:"utc"`
	Timestamp  int64  `json:"timestamp"`
	Kind       string `json:"kind"`
	Info       string `json:"info"`
	Status     string `json:"status"`
	RawHex     string `json:"rawHex"`
	PacketText string `json:"packet"`
}

type State struct {
	Settings          Settings       `json:"settings"`
	Receiving         bool           `json:"receiving"`
	Starting          bool           `json:"starting"`
	AudioReady        bool           `json:"audioReady"`
	AudioError        string         `json:"audioError"`
	Status            string         `json:"status"`
	LastError         string         `json:"lastError"`
	Received          []PacketView   `json:"received"`
	Transmitted       []TxView       `json:"transmitted"`
	ConfigFile        string         `json:"configFile"`
	AudioInputs       []audio.Device `json:"audioInputs"`
	AudioOutputs      []audio.Device `json:"audioOutputs"`
	AudioSamples      uint64         `json:"audioSamples"`
	AudioLevelDB      float64        `json:"audioLevelDB"`
	AudioRuntimeError string         `json:"audioRuntimeError"`
	DiagnosticLog     string         `json:"diagnosticLog"`
	PTTTestActive     bool           `json:"pttTestActive"`
	PTTTestSeconds    int            `json:"pttTestSeconds"`
	RigctlTestInfo    string         `json:"rigctlTestInfo"`
	RigctlTestLevel   string         `json:"rigctlTestLevel"`
}

type App struct {
	mu                sync.Mutex
	txMu              sync.Mutex
	audioInitMu       sync.Mutex
	diagMu            sync.Mutex
	operationalLogMu  sync.Mutex
	pttTestMu         sync.Mutex
	pttTestActive     bool
	pttTestTimer      *time.Timer
	pttTestDeadline   time.Time
	pttTestSettings   Settings
	pttTestDuration   time.Duration
	rigPTTSession     *rigctl.PTTSession // guarded by txMu
	rigctlTestInfo    string
	rigctlTestLevel   string
	diagPath          string
	ctx               context.Context
	settings          Settings
	configFile        string
	audio             *audio.Engine
	serialPTT         *pttctl.Controller
	audioErr          string
	receiving         bool
	starting          bool
	startWorkerActive bool
	status            string
	lastError         string
	received          []PacketView
	transmitted       []TxView
	seen              map[string]time.Time
	audioInputs       []audio.Device
	audioOutputs      []audio.Device
}

func NewApp() *App {
	return &App{
		settings:        defaultSettings(),
		serialPTT:       pttctl.New(),
		pttTestDuration: 30 * time.Second,
		rigctlTestInfo:  "尚未运行 rigctld/PTT 测试。",
		rigctlTestLevel: "info",
		status:          "准备就绪，请先填写呼号并检查声卡和电台设置。",
		received:        make([]PacketView, 0, 100),
		transmitted:     make([]TxView, 0, 100),
		seen:            make(map[string]time.Time),
	}
}

func defaultSettings() Settings {
	return Settings{
		Path: "RS0ISS,SGATE,WIDE2-1", FrameDestination: "APRS", Recipient: "CQ", LogSaveMode: "all", Theme: "light",
		PresetNames: []string{"CQ", "5NN", "73", "F4", "F5", "F6"},
		PresetMessages: []string{
			"CQ {CALL} {GRID}",
			"UR 5NN",
			"TU 73",
			"", "", "",
		},
		PresetResetCQ: []bool{true, false, false, false, false, false},
		RigHost:       "127.0.0.1", RigPort: 4532, UseRigPTT: true,
		PTTMode: "hamlib", PTTSerialPort: "COM3", PTTActiveHigh: true,
		TxDelayMS: 300, TxTailMS: 250,
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	folder := filepath.Join(base, appName)
	_ = os.MkdirAll(folder, 0o700)
	a.configFile = filepath.Join(folder, "settings.json")
	a.diagMu.Lock()
	a.diagPath = filepath.Join(folder, "sataprs.log")
	a.diagMu.Unlock()
	a.logDiagnostic("应用启动，配置文件=%s", a.configFile)
	if data, err := os.ReadFile(a.configFile); err == nil {
		var loaded Settings
		if json.Unmarshal(data, &loaded) == nil {
			defaults := defaultSettings()
			var storedKeys map[string]json.RawMessage
			_ = json.Unmarshal(data, &storedKeys)
			if loaded.PTTMode == "" {
				if loaded.UseRigPTT {
					loaded.PTTMode = "hamlib"
				} else {
					loaded.PTTMode = "off"
				}
			}
			if loaded.PTTSerialPort == "" {
				loaded.PTTSerialPort = defaults.PTTSerialPort
			}
			if _, exists := storedKeys["pttActiveHigh"]; !exists {
				loaded.PTTActiveHigh = defaults.PTTActiveHigh
			}
			loaded.PresetMessages = normalizePresetMessages(loaded.PresetMessages, defaults.PresetMessages)
			loaded.PresetNames = normalizePresetNames(loaded.PresetNames, defaults.PresetNames)
			loaded.PresetResetCQ = normalizePresetResetCQ(loaded.PresetResetCQ, defaults.PresetResetCQ)
			if loaded.RigHost == "" {
				loaded.RigHost = defaults.RigHost
			}
			if loaded.RigPort == 0 {
				loaded.RigPort = defaults.RigPort
			}
			if loaded.Recipient == "" {
				loaded.Recipient = defaults.Recipient
			}
			loaded.FrameDestination = strings.ToUpper(strings.TrimSpace(loaded.FrameDestination))
			if !isValidFrameDestination(loaded.FrameDestination) {
				loaded.FrameDestination = defaults.FrameDestination
			}
			if loaded.Path == "" || loaded.Path == "ARISS" {
				// ARISS was the built-in default in older builds; migrate it to the
				// currently requested default while preserving custom digipeater paths.
				loaded.Path = defaults.Path
			}
			if loaded.LogSaveMode == "" {
				loaded.LogSaveMode = defaults.LogSaveMode
			}
			// Older configuration files have no theme field; migrate them to day mode.
			if loaded.Theme != "light" && loaded.Theme != "dark" {
				loaded.Theme = defaults.Theme
			}
			if loaded.TxDelayMS < 0 || loaded.TxDelayMS > 5000 {
				loaded.TxDelayMS = defaults.TxDelayMS
			}
			if loaded.TxTailMS < 0 || loaded.TxTailMS > 5000 {
				loaded.TxTailMS = defaults.TxTailMS
			}
			a.settings = loaded
		}
	}
	a.mu.Lock()
	_ = a.saveSettingsLocked()
	a.mu.Unlock()
	// Audio initialization is retryable. Some Windows audio endpoints are not
	// ready when Wails starts; StartReceive and RefreshAudioDevices retry it.
	if err := a.initializeAudioEngine(); err != nil {
		a.setError(err)
	} else {
		a.mu.Lock()
		a.lastError = ""
		a.status = "音频已初始化。请确认录音设备连接电台接收音频。"
		a.mu.Unlock()
	}
	a.mu.Lock()
	autoStart := a.settings.AutoStartListening
	a.mu.Unlock()
	if autoStart {
		if err := a.StartReceive(); err != nil {
			a.setError(err)
		}
	}
}

func (a *App) shutdown(ctx context.Context) {
	// Never leave hardware PTT asserted when the application exits.
	if err := a.stopPTTTest("应用退出，PTT 已停止"); err != nil {
		a.logDiagnostic("应用退出时释放 PTT 失败: %v", err)
	}
	a.logDiagnostic("应用开始关闭")
	a.mu.Lock()
	engine := a.audio
	a.audio = nil
	a.mu.Unlock()
	if engine != nil {
		engine.Close()
	}
	if a.serialPTT != nil {
		_ = a.serialPTT.Close()
	}
}

func (a *App) GetState() State {
	a.mu.Lock()
	engine := a.audio
	s := State{
		Settings: cloneSettings(a.settings), Receiving: a.receiving, Starting: a.starting,
		AudioReady: engine != nil, AudioError: a.audioErr, Status: a.status, LastError: a.lastError,
		Received: append([]PacketView(nil), a.received...), Transmitted: append([]TxView(nil), a.transmitted...), ConfigFile: a.configFile,
		AudioInputs: append([]audio.Device(nil), a.audioInputs...), AudioOutputs: append([]audio.Device(nil), a.audioOutputs...),
		RigctlTestInfo: a.rigctlTestInfo, RigctlTestLevel: a.rigctlTestLevel,
	}
	a.mu.Unlock()
	a.pttTestMu.Lock()
	s.PTTTestActive = a.pttTestActive
	if a.pttTestActive && !a.pttTestDeadline.IsZero() {
		remaining := time.Until(a.pttTestDeadline)
		if remaining > 0 {
			s.PTTTestSeconds = int((remaining + time.Second - 1) / time.Second)
		}
	}
	a.pttTestMu.Unlock()
	a.diagMu.Lock()
	s.DiagnosticLog = a.diagPath
	a.diagMu.Unlock()
	if engine != nil {
		s.AudioSamples, s.AudioLevelDB = engine.ReceiveStats()
		s.AudioRuntimeError = engine.RuntimeError()
	}
	return s
}

// initializeAudioEngine creates or recreates the audio context as needed.
// It is serialized because a failed device initialization can be retried from
// both the listen button and the audio-settings refresh action.
func (a *App) initializeAudioEngine() error {
	a.audioInitMu.Lock()
	defer a.audioInitMu.Unlock()
	a.mu.Lock()
	if a.audio != nil {
		a.mu.Unlock()
		return nil
	}
	captureID, playbackID := a.settings.CaptureDeviceID, a.settings.PlaybackDeviceID
	a.mu.Unlock()
	a.logDiagnostic("初始化音频上下文，录音设备 ID=%q，播放设备 ID=%q", captureID, playbackID)
	engine, err := audio.NewWithDevicesAndLogger(captureID, playbackID, a.logDiagnostic)
	if err != nil {
		a.logDiagnostic("音频上下文初始化失败: %v", err)
		a.mu.Lock()
		a.audioErr = err.Error()
		a.mu.Unlock()
		return fmt.Errorf("音频上下文初始化失败: %w", err)
	}
	inputs, outputs, err := engine.Devices()
	if err != nil {
		a.logDiagnostic("枚举音频设备失败: %v", err)
		engine.Close()
		a.mu.Lock()
		a.audioErr = err.Error()
		a.mu.Unlock()
		return fmt.Errorf("枚举声卡失败: %w", err)
	}
	a.mu.Lock()
	if a.audio == nil {
		a.audio = engine
		a.audioInputs, a.audioOutputs = inputs, outputs
		a.audioErr = ""
		a.logDiagnostic("音频上下文就绪，录音设备=%d，播放设备=%d", len(inputs), len(outputs))
		a.lastError = ""
	} else {
		engine.Close()
	}
	a.mu.Unlock()
	return nil
}

// RefreshAudioDevices rescans input and output endpoints without changing the
// currently saved device selection.
func (a *App) RefreshAudioDevices() error {
	a.mu.Lock()
	engine := a.audio
	a.mu.Unlock()
	if engine == nil {
		if err := a.initializeAudioEngine(); err != nil {
			a.setError(err)
			return err
		}
		a.mu.Lock()
		engine = a.audio
		a.mu.Unlock()
	}
	inputs, outputs, err := engine.Devices()
	if err != nil {
		a.setError(err)
		return err
	}
	a.mu.Lock()
	a.audioInputs, a.audioOutputs = inputs, outputs
	a.status = fmt.Sprintf("已扫描声卡：录音 %d 个，播放 %d 个。", len(inputs), len(outputs))
	a.lastError = ""
	a.mu.Unlock()
	return nil
}

// GetSpectrum returns live input spectrum levels for the UI visualizer.
func (a *App) GetSpectrum() []float64 {
	a.mu.Lock()
	engine := a.audio
	a.mu.Unlock()
	if engine == nil {
		return make([]float64, 2048)
	}
	return engine.Spectrum()
}

// SetTheme persists only the visual theme preference without applying unrelated
// unsaved form edits or interrupting an active PTT test.
func (a *App) SetTheme(theme string) error {
	theme = strings.ToLower(strings.TrimSpace(theme))
	if theme != "light" && theme != "dark" {
		return errors.New("界面主题必须是白天模式或黑夜模式")
	}
	a.mu.Lock()
	previous := a.settings.Theme
	a.settings.Theme = theme
	if err := a.saveSettingsLocked(); err != nil {
		a.settings.Theme = previous
		a.mu.Unlock()
		return fmt.Errorf("保存界面主题失败：%w", err)
	}
	a.mu.Unlock()
	return nil
}

func (a *App) SaveSettings(s Settings) error {
	// Applying new PTT settings while a test is held must first release the
	// old endpoint; otherwise a COM port or rigctld host change could orphan TX.
	if err := a.stopPTTTest("配置变更，PTT 已停止"); err != nil {
		return fmt.Errorf("无法保存配置：PTT 尚未成功释放：%w", err)
	}
	s.Callsign = strings.ToUpper(strings.TrimSpace(s.Callsign))
	s.QTH = strings.TrimSpace(s.QTH)
	s.Grid = strings.ToUpper(strings.TrimSpace(s.Grid))
	s.Path = strings.TrimSpace(s.Path)
	s.FrameDestination = strings.ToUpper(strings.TrimSpace(s.FrameDestination))
	if s.FrameDestination == "" {
		s.FrameDestination = defaultSettings().FrameDestination
	}
	if !isValidFrameDestination(s.FrameDestination) {
		return fmt.Errorf("帧目的地址必须是 CQ、APRS、BEACON、QST 或 APRSAT")
	}
	s.Recipient = strings.ToUpper(strings.TrimSpace(s.Recipient))
	s.Theme = strings.ToLower(strings.TrimSpace(s.Theme))
	if s.Theme == "" {
		s.Theme = defaultSettings().Theme
	}
	if s.Theme != "light" && s.Theme != "dark" {
		return errors.New("界面主题必须是白天模式或黑夜模式")
	}
	s.RigHost = strings.TrimSpace(s.RigHost)
	s.PTTMode = strings.ToLower(strings.TrimSpace(s.PTTMode))
	s.PTTSerialPort = strings.TrimSpace(s.PTTSerialPort)
	if s.LogSaveMode == "" {
		a.mu.Lock()
		currentMode := a.settings.LogSaveMode
		a.mu.Unlock()
		if currentMode == "" {
			currentMode = defaultSettings().LogSaveMode
		}
		s.LogSaveMode = currentMode
	}
	if s.LogSaveMode != "all" && s.LogSaveMode != "monthly" && s.LogSaveMode != "yearly" {
		return errors.New("日志保存方式必须是单文件、按月或按年")
	}
	if s.PTTMode == "" {
		if s.UseRigPTT {
			s.PTTMode = "hamlib"
		} else {
			s.PTTMode = "off"
		}
	}
	if s.PTTMode != "hamlib" && s.PTTMode != "vox" && s.PTTMode != "rts" && s.PTTMode != "dtr" && s.PTTMode != "off" {
		return errors.New("PTT 模式必须是 Hamlib、VOX、RTS、DTR 或手动")
	}
	if (s.PTTMode == "rts" || s.PTTMode == "dtr") && s.PTTSerialPort == "" {
		return errors.New("RTS/DTR PTT 模式需要填写 COM 端口，例如 COM3")
	}
	s.UseRigPTT = s.PTTMode == "hamlib"
	if s.SSID < 0 || s.SSID > 15 {
		return errors.New("SSID 必须在 0 到 15 之间")
	}
	if s.RigPort < 1 || s.RigPort > 65535 {
		return errors.New("Hamlib TCP 端口必须在 1 到 65535 之间")
	}
	if s.TxDelayMS < 0 || s.TxDelayMS > 5000 {
		return errors.New("发射前延迟应在 0 到 5000 ms 之间")
	}
	if s.TxTailMS < 0 || s.TxTailMS > 5000 {
		return errors.New("发射后延迟应在 0 到 5000 ms 之间")
	}
	if s.Callsign != "" {
		if _, _, err := parseConfiguredCallsign(s.Callsign); err != nil {
			return err
		}
	}
	if len(s.Grid) > 0 {
		if _, _, err := aprs.MaidenheadToLatLon(s.Grid); err != nil {
			return fmt.Errorf("网格定位符无效: %w", err)
		}
	}
	if len(s.PresetMessages) > 6 || len(s.PresetNames) > 6 || len(s.PresetResetCQ) > 6 {
		return errors.New("预设消息固定为 F1 至 F6 六条")
	}
	s.PresetNames = normalizePresetNames(s.PresetNames, defaultSettings().PresetNames)
	s.PresetResetCQ = normalizePresetResetCQ(s.PresetResetCQ, defaultSettings().PresetResetCQ)
	for i, name := range s.PresetNames {
		name = strings.TrimSpace(name)
		if name == "" {
			name = fmt.Sprintf("F%d 消息", i+1)
		}
		if len([]rune(name)) > 24 {
			return fmt.Errorf("第 %d 条预设备注名称最多 24 个字符", i+1)
		}
		s.PresetNames[i] = name
	}
	for len(s.PresetMessages) < 6 {
		s.PresetMessages = append(s.PresetMessages, "")
	}
	for i, msg := range s.PresetMessages {
		if len(msg) > aprs.MaxAPRSMessageText {
			return fmt.Errorf("第 %d 条预设消息超出 %d 字节", i+1, aprs.MaxAPRSMessageText)
		}
		s.PresetMessages[i] = msg
	}
	// Reconfigure endpoints when selected IDs change. If listening was active,
	// stop the native stream, apply the new endpoints, then restart RX after save.
	a.mu.Lock()
	deviceChange := s.CaptureDeviceID != a.settings.CaptureDeviceID || s.PlaybackDeviceID != a.settings.PlaybackDeviceID
	engine := a.audio
	a.mu.Unlock()
	restartReceive := false
	if deviceChange && engine != nil {
		a.txMu.Lock()
		a.mu.Lock()
		engine = a.audio
		deviceChange = s.CaptureDeviceID != a.settings.CaptureDeviceID || s.PlaybackDeviceID != a.settings.PlaybackDeviceID
		wasReceiving := a.receiving
		a.mu.Unlock()
		if deviceChange && engine != nil {
			if wasReceiving {
				engine.StopRX()
				restartReceive = true
				a.mu.Lock()
				a.receiving = false
				a.mu.Unlock()
			}
			engine.StopPlayback()
			if err := engine.SetDevices(s.CaptureDeviceID, s.PlaybackDeviceID); err != nil {
				a.txMu.Unlock()
				a.setError(err)
				return err
			}
			a.mu.Lock()
			a.receiving = false
			a.status = "声卡设备已切换。"
			a.mu.Unlock()
		}
		a.txMu.Unlock()
	}
	a.mu.Lock()
	a.settings = s
	if err := a.saveSettingsLocked(); err != nil {
		a.lastError = err.Error()
		a.mu.Unlock()
		return err
	}
	a.status = "设置已保存。"
	a.lastError = ""
	a.mu.Unlock()
	if restartReceive {
		a.setStatus("声卡设置已保存，正在重新启动监听…")
		if err := a.StartReceive(); err != nil {
			a.setError(fmt.Errorf("声卡已保存，但自动重启监听失败：%w", err))
			return err
		}
	}
	return nil
}

// ExportConfig returns the current settings as a portable JSON document.
// It remains available for internal callers; the desktop UI uses ExportConfigToFile
// so users can choose a destination with the operating system's native save dialog.
func (a *App) ExportConfig() (string, error) {
	data, err := json.MarshalIndent(a.settingsSnapshot(), "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ExportConfigToFile opens the native Save File dialog and writes the current
// settings to the selected JSON file. The boolean is false when the dialog is cancelled.
func (a *App) ExportConfigToFile() (bool, error) {
	ctx := a.ctx
	if ctx == nil {
		return false, errors.New("应用尚未完成启动，无法打开文件对话框")
	}
	a.mu.Lock()
	configFile := a.configFile
	a.mu.Unlock()
	dir := "."
	if configFile != "" {
		dir = filepath.Dir(configFile)
	} else if base, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(base, appName)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("无法准备配置目录：%w", err)
	}
	path, err := wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{
		Title:            "导出 SatAPRS-Go 配置",
		DefaultDirectory: dir,
		DefaultFilename:  "SatAPRS-Go-config-" + time.Now().Format("2006-01-02") + ".json",
		Filters: []wailsruntime.FileFilter{{
			DisplayName: "SatAPRS-Go 配置文件 (*.json)",
			Pattern:     "*.json",
		}},
	})
	if err != nil {
		return false, fmt.Errorf("打开配置导出对话框失败：%w", err)
	}
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		ext := filepath.Ext(path)
		if ext != "" {
			path = strings.TrimSuffix(path, ext)
		}
		path += ".json"
	}
	data, err := json.MarshalIndent(a.settingsSnapshot(), "", "  ")
	if err != nil {
		return false, fmt.Errorf("序列化配置失败：%w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return false, fmt.Errorf("写入配置文件失败：%w", err)
	}
	a.setStatus("配置已导出到：" + path)
	return true, nil
}

// ImportConfigFromFile opens the native Open File dialog, reads a JSON settings
// file, validates it, and applies it. The boolean is false when cancelled.
func (a *App) ImportConfigFromFile() (bool, error) {
	ctx := a.ctx
	if ctx == nil {
		return false, errors.New("应用尚未完成启动，无法打开文件对话框")
	}
	a.mu.Lock()
	configFile := a.configFile
	a.mu.Unlock()
	dir := "."
	if configFile != "" {
		dir = filepath.Dir(configFile)
	} else if base, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(base, appName)
	}
	path, err := wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:            "导入 SatAPRS-Go 配置",
		DefaultDirectory: dir,
		Filters: []wailsruntime.FileFilter{{
			DisplayName: "SatAPRS-Go 配置文件 (*.json)",
			Pattern:     "*.json",
		}},
	})
	if err != nil {
		return false, fmt.Errorf("打开配置导入对话框失败：%w", err)
	}
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("读取所选配置文件信息失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("所选路径不是普通文件")
	}
	if info.Size() > 1024*1024 {
		return false, errors.New("配置文件大小超过 1 MiB，已拒绝导入")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读取配置文件失败：%w", err)
	}
	if err := a.ImportConfig(string(contents)); err != nil {
		return false, err
	}
	return true, nil
}

// ImportConfig validates and applies a portable JSON configuration.
func (a *App) ImportConfig(contents string) error {
	settings := defaultSettings()
	if err := json.Unmarshal([]byte(contents), &settings); err != nil {
		return fmt.Errorf("配置文件不是有效的 SatAPRS-Go JSON：%w", err)
	}
	return a.SaveSettings(settings)
}

// OpenLogFolder opens the application's configuration/log directory in the OS file manager.
func (a *App) OpenLogFolder() error {
	a.mu.Lock()
	configFile := a.configFile
	a.mu.Unlock()
	folder := ""
	if configFile != "" {
		folder = filepath.Dir(configFile)
	} else {
		base, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("无法确定应用配置目录：%w", err)
		}
		folder = filepath.Join(base, appName)
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return fmt.Errorf("无法创建日志目录：%w", err)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", folder)
	case "darwin":
		cmd = exec.Command("open", folder)
	default:
		cmd = exec.Command("xdg-open", folder)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法打开日志文件夹 %q：%w", folder, err)
	}
	// Reap the short-lived shell opener asynchronously without blocking the UI.
	go func() { _ = cmd.Wait() }()
	a.setStatus("已打开日志文件夹：" + folder)
	return nil
}

// ClearConfig restores built-in defaults and persists them.
func (a *App) ClearConfig() error { return a.SaveSettings(defaultSettings()) }

// SetLogSaveMode selects one all.txt file or date-split files.
func (a *App) SetLogSaveMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "all" && mode != "monthly" && mode != "yearly" {
		return errors.New("日志保存方式必须是单文件、按月或按年")
	}
	a.mu.Lock()
	a.settings.LogSaveMode = mode
	err := a.saveSettingsLocked()
	if err != nil {
		a.lastError = err.Error()
	} else {
		a.lastError = ""
		a.status = "TX/RX 日志保存方式已更新。"
	}
	a.mu.Unlock()
	return err
}

// ClearTxRxLog removes session-log files generated by this application.
func (a *App) ClearTxRxLog() error {
	a.operationalLogMu.Lock()
	defer a.operationalLogMu.Unlock()
	a.mu.Lock()
	configFile := a.configFile
	a.mu.Unlock()
	if configFile == "" {
		return errors.New("应用配置目录尚未初始化")
	}
	folder := filepath.Dir(configFile)
	entries, err := os.ReadDir(folder)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "all.txt" && !(strings.HasPrefix(name, "all-") && strings.HasSuffix(name, ".txt")) {
			continue
		}
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(folder, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	a.setStatus("TX/RX 日志已清空。")
	return nil
}

func (a *App) appendTxRxLog(at time.Time, direction, detail string) {
	a.mu.Lock()
	mode, configFile := a.settings.LogSaveMode, a.configFile
	a.mu.Unlock()
	if configFile == "" {
		return
	}
	if mode == "" {
		mode = "all"
	}
	name := "all.txt"
	switch mode {
	case "monthly":
		name = fmt.Sprintf("all-%04d-%02d.txt", at.UTC().Year(), int(at.UTC().Month()))
	case "yearly":
		name = fmt.Sprintf("all-%04d.txt", at.UTC().Year())
	}
	line := fmt.Sprintf("%s %s %s\n", at.UTC().Format(time.RFC3339Nano), direction, strings.TrimSpace(strings.ReplaceAll(detail, "\n", " ")))
	a.operationalLogMu.Lock()
	defer a.operationalLogMu.Unlock()
	f, err := os.OpenFile(filepath.Join(filepath.Dir(configFile), name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		a.logDiagnostic("写入 TX/RX 日志失败: %v", err)
		return
	}
	if _, err := f.WriteString(line); err != nil {
		a.logDiagnostic("写入 TX/RX 日志失败: %v", err)
	}
	_ = f.Close()
}

func (a *App) TestRigctl() error {
	s := a.settingsSnapshot()
	address := fmt.Sprintf("%s:%d", s.RigHost, s.RigPort)
	command := "T 0"
	stamp := time.Now().Format("2006-01-02 15:04:05")
	client := rigctl.New(address)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := client.TestResponse(ctx)
	if err != nil {
		info := fmt.Sprintf("时间：%s\n端点：%s\n命令：%s\n结果：失败\n错误：%v", stamp, address, command, err)
		a.setRigctlTestResult("error", info)
		a.setError(fmt.Errorf("rigctld 连接/命令测试失败：%w", err))
		return err
	}
	info := fmt.Sprintf("时间：%s\n端点：%s\n命令：%s\n响应：%s\n结果：已收到 rigctld 成功响应。此测试验证 TCP 与命令处理，不代表电台实际 PTT 输出一定工作。", stamp, address, command, response)
	a.setRigctlTestResult("success", info)
	a.setStatus("Hamlib rigctld 连接与 PTT 释放命令测试成功。")
	a.logDiagnostic("rigctld 测试成功：端点=%s 命令=%q 响应=%q", address, command, response)
	return nil
}

func (a *App) TestPTT() error {
	s := a.settingsSnapshot()
	if s.PTTMode == "vox" {
		return errors.New("VOX 模式由电台根据发射音频自动触发，无法单独切换 PTT")
	}
	if s.PTTMode == "off" {
		return errors.New("当前选择了手动 PTT，软件不会切换发射状态")
	}
	if !pttIsAppControlled(s.PTTMode) {
		return fmt.Errorf("当前 PTT 模式不支持测试：%s", s.PTTMode)
	}
	// A second click is an immediate PTT-off request. Do not make the operator
	// wait for or acknowledge a confirmation dialog.
	if a.isPTTTestActive() {
		return a.stopPTTTest("测试 PTT 已手动停止")
	}
	if !a.txMu.TryLock() {
		return errors.New("当前有发射任务正在进行，请结束后再测试 PTT")
	}
	defer a.txMu.Unlock()

	a.pttTestMu.Lock()
	if a.pttTestActive {
		err := a.releasePTTTestLocked("测试 PTT 已手动停止")
		a.pttTestMu.Unlock()
		return err
	}

	pttCommand := fmt.Sprintf("T %d", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	response, err := a.setPTTDetailed(ctx, s, true)
	cancel()
	if err != nil {
		if s.PTTMode == "hamlib" {
			info := fmt.Sprintf("时间：%s\n端点：%s:%d\n命令：%s\n响应/结果：启动失败\n错误：%v", time.Now().Format("2006-01-02 15:04:05"), s.RigHost, s.RigPort, pttCommand, err)
			a.setRigctlTestResult("error", info)
		}
		a.pttTestMu.Unlock()
		a.setError(fmt.Errorf("PTT 启动失败：%w", err))
		return err
	}

	// Keep the rigctld TCP session open while keyed. Some backends release PTT
	// when the control socket closes, and an extra status query on another socket
	// can disturb their keying state. Do not query PTT while this session is held.
	duration := a.pttTestDuration
	if duration <= 0 {
		duration = 30 * time.Second
	}
	deadline := time.Now().Add(duration)
	a.pttTestActive = true
	a.pttTestSettings = s
	a.pttTestDeadline = deadline
	timeoutReason := fmt.Sprintf("PTT 测试达到 %d 秒上限，已自动停止", int((duration+time.Second-1)/time.Second))
	a.pttTestTimer = time.AfterFunc(duration, func() {
		if err := a.stopPTTTest(timeoutReason); err != nil {
			a.logDiagnostic("PTT 自动释放失败，将重试: %v", err)
		}
	})
	a.pttTestMu.Unlock()

	if s.PTTMode == "hamlib" {
		a.setRigctlTestResult("info", fmt.Sprintf("时间：%s\n端点：%s:%d\n命令：%s\n响应：%s\n结果：rigctld 已接受 PTT 开启命令，TCP 控制连接将保持打开，直到发送 T 0。保持发射期间不再执行状态查询，避免某些电台后端因连接变化而解除 PTT。再次点击可立即解除，最长持续 %d 秒。", time.Now().Format("2006-01-02 15:04:05"), s.RigHost, s.RigPort, pttCommand, response, int(duration.Seconds())))
	}
	a.setStatus(fmt.Sprintf("测试 PTT 已开启，最长持续 %d 秒；再次点击停止。", int(duration.Seconds())))
	return nil
}

// stopPTTTest serializes PTT-off against APRS transmission and the test toggle.
// If the radio/serial endpoint fails to release, it retries every second and
// keeps the UI in the active state so the failure is visible to the operator.
func (a *App) stopPTTTest(reason string) error {
	a.txMu.Lock()
	defer a.txMu.Unlock()
	a.pttTestMu.Lock()
	defer a.pttTestMu.Unlock()
	if !a.pttTestActive {
		return nil
	}
	return a.releasePTTTestLocked(reason)
}

// releasePTTTestLocked requires both txMu and pttTestMu.
func (a *App) releasePTTTestLocked(reason string) error {
	if a.pttTestTimer != nil {
		a.pttTestTimer.Stop()
		a.pttTestTimer = nil
	}
	pttSettings := a.pttTestSettings
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	response, err := a.setPTTDetailed(ctx, pttSettings, false)
	cancel()
	if pttSettings.PTTMode == "hamlib" {
		level := "success"
		message := fmt.Sprintf("时间：%s\n端点：%s:%d\n命令：T 0\n响应：%s\n结果：PTT 测试释放命令已发送。", time.Now().Format("2006-01-02 15:04:05"), pttSettings.RigHost, pttSettings.RigPort, response)
		if err != nil {
			level = "error"
			message += "\n错误：" + err.Error()
		}
		a.setRigctlTestResult(level, message)
	}
	if err != nil {
		a.pttTestDeadline = time.Now()
		a.pttTestTimer = time.AfterFunc(time.Second, func() {
			if retryErr := a.stopPTTTest("PTT 自动重试释放"); retryErr != nil {
				a.logDiagnostic("PTT 释放重试失败: %v", retryErr)
			}
		})
		err = fmt.Errorf("PTT 释放失败，软件将在 1 秒后重试，请立即检查电台：%w", err)
		a.setError(err)
		return err
	}
	a.pttTestActive = false
	a.pttTestDeadline = time.Time{}
	a.pttTestSettings = Settings{}
	a.setStatus(reason)
	return nil
}

func (a *App) setPTT(ctx context.Context, s Settings, enabled bool) error {
	_, err := a.setPTTDetailed(ctx, s, enabled)
	return err
}

func (a *App) setPTTDetailed(ctx context.Context, s Settings, enabled bool) (string, error) {
	switch s.PTTMode {
	case "hamlib":
		if enabled {
			if a.rigPTTSession != nil {
				return "", errors.New("已有 Hamlib PTT 会话处于开启状态")
			}
			client := rigctl.New(fmt.Sprintf("%s:%d", s.RigHost, s.RigPort))
			session, response, err := client.StartPTTSession(ctx, 1)
			if err != nil {
				return response, err
			}
			a.rigPTTSession = session
			return response, nil
		}
		if a.rigPTTSession != nil {
			session := a.rigPTTSession
			response, err := session.SetPTT(ctx, 0)
			closeErr := session.Close()
			a.rigPTTSession = nil
			if err != nil {
				return response, err
			}
			if closeErr != nil {
				return response, fmt.Errorf("PTT 已发送关闭命令，但关闭 rigctld 会话失败: %w", closeErr)
			}
			return response, nil
		}
		client := rigctl.New(fmt.Sprintf("%s:%d", s.RigHost, s.RigPort))
		return client.SetPTTResponse(ctx, false)
	case "rts", "dtr":
		if a.serialPTT == nil {
			return "", errors.New("串口 PTT 控制器未初始化")
		}
		return fmt.Sprintf("%s %s", s.PTTSerialPort, strings.ToUpper(s.PTTMode)), a.serialPTT.Set(s.PTTSerialPort, s.PTTMode, enabled, s.PTTActiveHigh)
	case "vox", "off":
		return "未发送控制命令", nil
	default:
		return "", fmt.Errorf("未知 PTT 模式: %s", s.PTTMode)
	}
}

func (a *App) setRigctlTestResult(level, info string) {
	a.mu.Lock()
	a.rigctlTestLevel = level
	a.rigctlTestInfo = info
	a.mu.Unlock()
	a.logDiagnostic("rigctld/PTT 测试（%s）：%s", level, strings.ReplaceAll(info, "\n", " | "))
}

func pttIsAppControlled(mode string) bool {
	return mode == "hamlib" || mode == "rts" || mode == "dtr"
}

func (a *App) isPTTTestActive() bool {
	a.pttTestMu.Lock()
	defer a.pttTestMu.Unlock()
	return a.pttTestActive
}

// parseConfiguredCallsign accepts a normal 1..6-character AX.25 callsign and
// an optional display/operating suffix such as /5 or /QRP. Numeric suffixes
// map to the AX.25 SSID. Text suffixes are retained in UI/template text, but
// AX.25 source addresses cannot carry them and use the base call + SSID.
func parseConfiguredCallsign(value string) (base, suffix string, err error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	parts := strings.Split(value, "/")
	if len(parts) > 2 {
		return "", "", errors.New("呼号只允许一个特殊后缀，例如 N0CALL/5 或 N0CALL/QRP")
	}
	base = parts[0]
	if len(base) < 1 || len(base) > 6 {
		return "", "", errors.New("基础呼号长度必须为 1 到 6 个字符")
	}
	for _, c := range base {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", "", errors.New("基础呼号只允许英文字母和数字；特殊后缀请使用斜线，例如 N0CALL/QRP")
		}
	}
	if len(parts) == 1 {
		return base, "", nil
	}
	suffix = parts[1]
	if len(suffix) < 1 || len(suffix) > 4 {
		return "", "", errors.New("特殊呼号后缀长度必须为 1 到 4 个字符")
	}
	for _, c := range suffix {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", "", errors.New("特殊呼号后缀只允许英文字母和数字")
		}
	}
	if _, parseErr := strconv.Atoi(suffix); parseErr == nil {
		n, _ := strconv.Atoi(suffix)
		if n < 0 || n > 15 {
			return "", "", errors.New("数字特殊后缀必须在 /0 到 /15 之间，以匹配 AX.25 SSID")
		}
	}
	return base, suffix, nil
}

func (a *App) StartReceive() error {
	a.logDiagnostic("收到开始监听请求")
	a.mu.Lock()
	if a.receiving {
		a.mu.Unlock()
		return nil
	}
	if a.startWorkerActive {
		if a.starting {
			a.mu.Unlock()
			return nil
		}
		a.mu.Unlock()
		return errors.New("上一次监听启动仍在等待音频设备响应，请检查录音设备后稍候重试")
	}
	a.starting = true
	a.startWorkerActive = true
	a.lastError = ""
	a.status = "正在启动监听，等待录音设备响应…"
	a.mu.Unlock()

	// Keep the Wails method responsive. Some Windows audio drivers can block
	// while opening a capture endpoint; the UI must remain usable and receive a
	// concrete timeout/error state instead of waiting forever on this call.
	go a.startReceiveAsync()
	return nil
}

func (a *App) startReceiveAsync() {
	result := make(chan error, 1)
	go func() {
		var operationErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				operationErr = fmt.Errorf("监听启动内部异常: %v", recovered)
				a.logDiagnostic("监听启动 goroutine 捕获异常: %v\n%s", recovered, debug.Stack())
			}
			result <- operationErr
		}()
		operationErr = a.startReceiveOperation()
	}()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		a.finishReceiveStart(err)
	case <-timer.C:
		a.logDiagnostic("监听启动超过 12 秒，进入超时等待清理流程")
		a.mu.Lock()
		a.starting = false
		a.lastError = "启动监听超时：录音设备 12 秒内没有响应。请检查声卡选择、占用状态和系统录音权限。"
		a.status = a.lastError
		a.mu.Unlock()
		// Native audio startup may return after the timeout. If it eventually
		// succeeds, stop that late-started stream instead of silently leaving a
		// capture device active while the UI reports failure.
		go func() {
			err := <-result
			a.logDiagnostic("超时后的音频启动最终返回，err=%v", err)
			if err == nil {
				a.mu.Lock()
				engine := a.audio
				a.receiving = false
				a.mu.Unlock()
				if engine != nil {
					engine.StopRX()
				}
			}
			a.mu.Lock()
			a.startWorkerActive = false
			a.starting = false
			a.receiving = false
			if err != nil {
				a.lastError = fmt.Sprintf("监听启动失败：%v", err)
				a.status = a.lastError
			} else {
				a.lastError = "启动监听超时，已清理迟到的音频启动。现在可以检查设备后重试。"
				a.status = a.lastError
			}
			a.mu.Unlock()
		}()
	}
}

func (a *App) startReceiveOperation() error {
	a.logDiagnostic("监听启动步骤 1/3：确保音频上下文就绪")
	if err := a.initializeAudioEngine(); err != nil {
		a.logDiagnostic("监听启动步骤 1/3 失败: %v", err)
		return err
	}
	a.mu.Lock()
	engine := a.audio
	a.mu.Unlock()
	if engine == nil {
		return errors.New("音频引擎尚未初始化，请在配置中的音频 / 声卡页检查设备")
	}
	a.logDiagnostic("监听启动步骤 2/3：调用录音设备 StartRX")
	if err := engine.StartRX(func(packet aprs.Packet) { a.onReceive(packet) }); err != nil {
		a.logDiagnostic("监听启动步骤 2/3 失败: %v", err)
		return fmt.Errorf("启动录音设备失败：%w", err)
	}
	a.logDiagnostic("监听启动步骤 3/3：录音流已启动")
	return nil
}

func (a *App) finishReceiveStart(err error) {
	if err != nil {
		a.logDiagnostic("监听启动失败: %v", err)
	} else {
		a.logDiagnostic("监听启动成功")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starting = false
	a.startWorkerActive = false
	if err != nil {
		a.receiving = false
		a.lastError = err.Error()
		a.status = "启动监听失败：" + err.Error()
		return
	}
	a.receiving = true
	a.lastError = ""
	a.status = "监听已启动，等待录音设备输入 1200 bps AFSK/APRS 音频。"
}

func (a *App) StopReceive() {
	a.logDiagnostic("收到停止监听请求")
	a.mu.Lock()
	engine := a.audio
	a.receiving = false
	if !a.startWorkerActive {
		a.starting = false
	}
	a.mu.Unlock()
	if engine != nil {
		engine.StopRX()
	}
	a.setStatus("已停止监听。")
}

func (a *App) SendPreset(index int, recipient string) error {
	s := a.settingsSnapshot()
	if index < 0 || index >= 6 || index >= len(s.PresetMessages) {
		return errors.New("预设消息索引无效，固定支持 F1 至 F6")
	}
	name := fmt.Sprintf("F%d", index+1)
	if index < len(s.PresetNames) && strings.TrimSpace(s.PresetNames[index]) != "" {
		name = strings.TrimSpace(s.PresetNames[index])
	}
	if strings.TrimSpace(s.PresetMessages[index]) == "" {
		return fmt.Errorf("%s 尚未设置消息正文，请先编辑预设", name)
	}
	return a.sendMessage(recipient, s.PresetMessages[index], fmt.Sprintf("预设消息 %s", name))
}

func (a *App) SendCustomMessage(recipient, message string) error {
	return a.sendMessage(recipient, message, "自定义消息")
}

func (a *App) sendMessage(recipient, message, kind string) error {
	s := a.settingsSnapshot()
	if s.Callsign == "" {
		return errors.New("请先设置本机呼号")
	}
	if strings.TrimSpace(recipient) == "" {
		recipient = s.Recipient
	}
	recipient = strings.ToUpper(strings.TrimSpace(recipient))
	if recipient == "" {
		return errors.New("请填写对方呼号，或输入 CQ")
	}
	message = expandTemplate(message, s, recipient)
	info, err := aprs.BuildMessageInfo(recipient, message)
	if err != nil {
		a.setError(err)
		return err
	}
	if err := a.sendInfo(info, kind); err != nil {
		return err
	}
	if wireRecipient, normErr := aprs.NormalizeMessageAddressee(recipient); normErr == nil && wireRecipient != recipient {
		a.setStatus(fmt.Sprintf("消息已发射。目标特殊呼号按标准 APRS 地址规范化为 %s。", wireRecipient))
	}
	return nil
}

func (a *App) sendInfo(info []byte, kind string) error {
	a.txMu.Lock()
	defer a.txMu.Unlock()
	if a.isPTTTestActive() {
		err := errors.New("测试 PTT 正在持续发射，请先再次点击测试 PTT 停止后再发送 APRS 消息")
		a.setError(err)
		a.addTX(kind, string(info), "发射被阻止：测试 PTT 仍处于开启状态", nil)
		return err
	}
	s := a.settingsSnapshot()
	if s.Callsign == "" {
		return errors.New("请先设置本机呼号")
	}
	baseCall, specialSuffix, callErr := parseConfiguredCallsign(s.Callsign)
	if callErr != nil {
		a.setError(callErr)
		return callErr
	}
	wireSSID := s.SSID
	if specialSuffix != "" {
		if n, parseErr := strconv.Atoi(specialSuffix); parseErr == nil && n >= 0 && n <= 15 {
			wireSSID = n // CALL/5 is represented by the AX.25-standard CALL-5.
		}
	}
	source := baseCall
	if wireSSID > 0 {
		source = fmt.Sprintf("%s-%d", source, wireSSID)
	}
	path := splitPath(s.Path)
	destination := strings.ToUpper(strings.TrimSpace(s.FrameDestination))
	if !isValidFrameDestination(destination) {
		destination = defaultSettings().FrameDestination
	}
	frame, err := aprs.EncodeUIFrame(source, destination, path, info)
	if err != nil {
		a.setError(err)
		return err
	}
	a.mu.Lock()
	engine := a.audio
	_audioErr := a.audioErr
	a.mu.Unlock()
	if engine == nil {
		err := fmt.Errorf("音频引擎不可用: %s", _audioErr)
		a.addTX(kind, string(info), "发射失败: "+err.Error(), frame)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pttOn := false
	if pttIsAppControlled(s.PTTMode) {
		if err := a.setPTT(ctx, s, true); err != nil {
			a.setError(err)
			a.addTX(kind, string(info), "PTT 失败: "+err.Error(), frame)
			return err
		}
		pttOn = true
		if err := audio.WaitForSilence(ctx, time.Duration(s.TxDelayMS)*time.Millisecond); err != nil {
			_ = a.setPTT(context.Background(), s, false)
			a.setError(err)
			a.addTX(kind, string(info), "PTT 延迟等待失败: "+err.Error(), frame)
			return err
		}
	}
	pttReleased := false
	defer func() {
		if pttOn && !pttReleased {
			if offErr := a.setPTT(context.Background(), s, false); offErr != nil {
				a.setError(fmt.Errorf("发射结束后释放 PTT 失败，请立即检查电台: %w", offErr))
			}
		}
	}()
	samples := aprs.EncodeAFSK1200(frame, audio.SampleRate)
	if err := engine.Play(ctx, samples); err != nil {
		a.setError(err)
		a.addTX(kind, string(info), "音频发射失败: "+err.Error(), frame)
		return err
	}
	if s.TxTailMS > 0 {
		if err := audio.WaitForSilence(ctx, time.Duration(s.TxTailMS)*time.Millisecond); err != nil {
			a.setError(err)
			a.addTX(kind, string(info), "发射尾延迟等待失败: "+err.Error(), frame)
			return err
		}
	}
	if pttOn {
		if err := a.setPTT(context.Background(), s, false); err != nil {
			a.setError(fmt.Errorf("音频已发出，但 PTT 释放失败，请立即检查电台: %w", err))
			a.addTX(kind, string(info), "已播放音频，PTT 释放失败", frame)
			return err
		}
		pttReleased = true
	}
	result := "音频已播放，PTT 已释放"
	switch s.PTTMode {
	case "vox":
		result = "音频已播放，由电台 VOX 检测音频触发发射"
	case "off":
		result = "音频已播放，PTT 由外部手动控制"
	}
	a.addTX(kind, string(info), result, frame)
	if specialSuffix != "" {
		if _, parseErr := strconv.Atoi(specialSuffix); parseErr != nil {
			a.setStatus(fmt.Sprintf("发射完成。AX.25 空中源地址使用 %s；/%s 为本地呼号别名，仅会出现在消息模板等文本中。", source, specialSuffix))
			return nil
		}
	}
	a.setStatus("发射完成。请检查接收端或卫星下行是否收到数据。")
	return nil
}

func (a *App) onReceive(packet aprs.Packet) {
	key := string(packet.Raw)
	now := time.Now().UTC()
	a.mu.Lock()
	if seenAt, ok := a.seen[key]; ok && now.Sub(seenAt) < 5*time.Second {
		a.mu.Unlock()
		return
	}
	a.seen[key] = now
	if len(a.seen) > 1000 {
		a.seen = make(map[string]time.Time)
	} else if len(a.seen) > 500 {
		for k, t := range a.seen {
			if now.Sub(t) > 2*time.Minute {
				delete(a.seen, k)
			}
		}
	}
	to, msg, hasMessage := aprs.ParseAPRSMessage(packet.Info)
	view := PacketView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), Source: packet.Source, Destination: packet.Destination,
		Path: strings.Join(packet.Path, ","), Info: packet.Info, RawHex: strings.ToUpper(hex.EncodeToString(packet.Raw)), PacketText: formatUISSFrame(packet, now)}
	if hasMessage {
		view.MessageTo, view.Message = to, msg
	}
	a.received = append([]PacketView{view}, a.received...)
	if len(a.received) > 100 {
		a.received = a.received[:100]
	}
	a.status = fmt.Sprintf("收到 AX.25/APRS 帧: %s → %s", packet.Source, packet.Destination)
	a.mu.Unlock()
	// Keep the on-disk operational log in TNC2 form; only the TX/RX panel uses UISS formatting.
	a.appendTxRxLog(now, "RX", fmt.Sprintf("%s | RAW=%s", formatTNC2Packet(packet), view.RawHex))
}

func (a *App) addTX(kind, info, status string, frame []byte) {
	now := time.Now().UTC()
	packetText := info
	logPacketText := info
	if packet, err := aprs.DecodeUIFrame(frame); err == nil {
		packetText = formatUISSFrame(packet, now)
		logPacketText = formatTNC2Packet(packet)
	}
	rawHex := strings.ToUpper(hex.EncodeToString(frame))
	a.mu.Lock()
	view := TxView{UTC: now.Format("15:04:05"), Timestamp: now.UnixMilli(), Kind: kind, Info: info, Status: status, RawHex: rawHex, PacketText: packetText}
	a.transmitted = append([]TxView{view}, a.transmitted...)
	if len(a.transmitted) > 100 {
		a.transmitted = a.transmitted[:100]
	}
	a.mu.Unlock()
	a.appendTxRxLog(now, "TX", fmt.Sprintf("kind=%s %s | status=%s | RAW=%s", kind, logPacketText, status, rawHex))
}

// formatTNC2Packet renders an AX.25 UI frame as the conventional APRS/TNC2
// monitor line: SOURCE>DESTINATION,PATH:INFO.
func formatTNC2Packet(packet aprs.Packet) string {
	line := packet.Source + ">" + packet.Destination
	if len(packet.Path) > 0 {
		line += "," + strings.Join(packet.Path, ",")
	}
	return line + ":" + packet.Info
}

// formatUISSFrame renders the decoded AX.25 UI frame in the two-line monitor
// style used by UISS. The information length is measured in bytes, as on air.
func formatUISSFrame(packet aprs.Packet, at time.Time) string {
	header := fmt.Sprintf("Fm %s To %s", packet.Source, packet.Destination)
	if len(packet.Path) > 0 {
		header += " Via " + strings.Join(packet.Path, ",")
	}
	header += fmt.Sprintf(" <UI pid=F0 Len=%d >[%s]", len([]byte(packet.Info)), at.Local().Format("15:04:05"))
	return header + "\n" + packet.Info
}

func isValidFrameDestination(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CQ", "APRS", "BEACON", "QST", "APRSAT":
		return true
	default:
		return false
	}
}

func (a *App) settingsSnapshot() Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneSettings(a.settings)
}

func cloneSettings(s Settings) Settings {
	s.PresetMessages = append([]string(nil), s.PresetMessages...)
	s.PresetNames = append([]string(nil), s.PresetNames...)
	s.PresetResetCQ = append([]bool(nil), s.PresetResetCQ...)
	return s
}

func (a *App) saveSettingsLocked() error {
	if a.configFile == "" {
		return nil
	}
	data, err := json.MarshalIndent(a.settings, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.configFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.configFile)
}

func (a *App) setStatus(status string) { a.mu.Lock(); a.status = status; a.mu.Unlock() }
func (a *App) setError(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	a.lastError = err.Error()
	a.status = err.Error()
	a.mu.Unlock()
}

func (a *App) logDiagnostic(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	a.diagMu.Lock()
	defer a.diagMu.Unlock()
	if a.diagPath == "" {
		return
	}
	f, err := os.OpenFile(a.diagPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339Nano), message)
	_ = f.Close()
}

func splitPath(value string) []string {
	value = strings.ReplaceAll(value, ";", ",")
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(strings.ToUpper(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func expandTemplate(text string, s Settings, hisCall string) string {
	grid, qth := s.Grid, s.QTH
	if grid == "" {
		grid = "NO-GRID"
	}
	if qth == "" {
		qth = "NO-QTH"
	}
	hisCall = strings.ToUpper(strings.TrimSpace(hisCall))
	if hisCall == "" {
		hisCall = strings.ToUpper(strings.TrimSpace(s.Recipient))
	}
	replacer := strings.NewReplacer(
		"{CALL}", s.Callsign, "{HisCall}", hisCall, "{GRID}", grid,
		"{QTH}", qth, "{UTC}", time.Now().UTC().Format("15:04Z"),
	)
	return replacer.Replace(text)
}

func normalizePresetMessages(messages, defaults []string) []string {
	return normalizeSixStrings(messages, defaults)
}

func normalizePresetNames(names, defaults []string) []string {
	return normalizeSixStrings(names, defaults)
}

func normalizePresetResetCQ(values, defaults []bool) []bool {
	out := make([]bool, 6)
	for i := range out {
		if i < len(values) {
			out[i] = values[i]
		} else if i < len(defaults) {
			out[i] = defaults[i]
		}
	}
	return out
}

func normalizeSixStrings(values, defaults []string) []string {
	out := make([]string, 6)
	for i := 0; i < len(out); i++ {
		if i < len(values) {
			out[i] = values[i]
		} else if i < len(defaults) {
			out[i] = defaults[i]
		}
	}
	return out
}
