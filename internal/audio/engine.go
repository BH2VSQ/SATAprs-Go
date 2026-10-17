package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"

	"github.com/BH2VSQ/SatAPRS-Go/internal/aprs"
)

const SampleRate = aprs.DefaultSampleRate

// Device is one audio endpoint returned by miniaudio.
type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

// audioChunk carries one contiguous block of captured samples. Sequence advances
// for every callback block, including blocks dropped when the worker queue is full.
type audioChunk struct {
	sequence uint64
	samples  []int16
}

// Engine manages selectable audio endpoints for AFSK1200 receive and transmit.
type Engine struct {
	ctx              *malgo.AllocatedContext
	diagf            func(string, ...interface{})
	captureDeviceID  string
	playbackDeviceID string

	spectrumMu sync.RWMutex
	spectrum   []float64

	playbackMu sync.Mutex
	playback   *malgo.Device
	txSamples  []float32
	txOffset   int
	txDone     chan struct{}
	txRunning  bool

	captureMu            sync.Mutex
	capture              *malgo.Device
	rxChunks             chan audioChunk
	rxCancel             context.CancelFunc
	rxWG                 sync.WaitGroup
	captureSamples       atomic.Uint64
	captureDroppedChunks atomic.Uint64
	inputLevelBits       atomic.Uint64 // atomic.Float64 representation for the capture RMS level in dBFS
	runtimeErrMu         sync.RWMutex
	runtimeErr           string
}

func New() (*Engine, error) { return NewWithDevices("", "") }

func NewWithDevices(captureID, playbackID string) (*Engine, error) {
	return NewWithDevicesAndLogger(captureID, playbackID, nil)
}

// NewWithDevicesAndLogger allows the desktop host to persist native audio
// backend diagnostics, which is important when a Windows driver fails early.
func NewWithDevicesAndLogger(captureID, playbackID string, logf func(string, ...interface{})) (*Engine, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		if logf != nil {
			logf("音频后端: %s", message)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("初始化音频上下文失败: %w", err)
	}
	e := &Engine{
		ctx: ctx, diagf: logf, captureDeviceID: captureID, playbackDeviceID: playbackID,
		rxChunks: make(chan audioChunk, 96), spectrum: make([]float64, 2048),
	}
	e.inputLevelBits.Store(math.Float64bits(-120))
	return e, nil
}

// Devices lists available capture and playback endpoints. An empty device ID
// in settings means that the system default endpoint should be used.
func (e *Engine) Devices() (inputs, outputs []Device, err error) {
	if e == nil || e.ctx == nil {
		return nil, nil, errors.New("音频上下文尚未初始化")
	}
	inputs, err = listDevices(e.ctx.Context, malgo.Capture)
	if err != nil {
		return nil, nil, fmt.Errorf("枚举录音设备失败: %w", err)
	}
	outputs, err = listDevices(e.ctx.Context, malgo.Playback)
	if err != nil {
		return nil, nil, fmt.Errorf("枚举播放设备失败: %w", err)
	}
	return inputs, outputs, nil
}

func listDevices(ctx malgo.Context, kind malgo.DeviceType) ([]Device, error) {
	infos, err := ctx.Devices(kind)
	if err != nil {
		return nil, err
	}
	items := make([]Device, 0, len(infos))
	for _, info := range infos {
		items = append(items, Device{ID: info.ID.String(), Name: info.Name(), IsDefault: info.IsDefault != 0})
	}
	return items, nil
}

// Spectrum returns the latest 2048-band input spectrum normalized to [0,1].
func (e *Engine) Spectrum() []float64 {
	if e == nil {
		return make([]float64, 2048)
	}
	e.spectrumMu.RLock()
	defer e.spectrumMu.RUnlock()
	return append([]float64(nil), e.spectrum...)
}

// ReceiveStats reports the number of capture samples delivered by the OS and
// the measured input level in dBFS. It helps distinguish a running device from
// an audio route that is producing no samples.
func (e *Engine) ReceiveStats() (uint64, float64) {
	if e == nil {
		return 0, -120
	}
	return e.captureSamples.Load(), math.Float64frombits(e.inputLevelBits.Load())
}

// ReceiveDroppedChunks returns the number of capture blocks dropped in the
// current listening session because the non-blocking audio queue was full.
func (e *Engine) ReceiveDroppedChunks() uint64 {
	if e == nil {
		return 0
	}
	return e.captureDroppedChunks.Load()
}

// SetDevices applies the chosen device IDs. Callers must stop streaming first.
func (e *Engine) SetDevices(captureID, playbackID string) error {
	if e == nil || e.ctx == nil {
		return errors.New("音频上下文尚未初始化")
	}
	if captureID != "" {
		devices, err := e.ctx.Context.Devices(malgo.Capture)
		if err != nil {
			return fmt.Errorf("读取录音设备失败: %w", err)
		}
		found := false
		for _, d := range devices {
			if d.ID.String() == captureID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("选中的录音设备已不存在，请刷新设备列表")
		}
	}
	if playbackID != "" {
		devices, err := e.ctx.Context.Devices(malgo.Playback)
		if err != nil {
			return fmt.Errorf("读取播放设备失败: %w", err)
		}
		found := false
		for _, d := range devices {
			if d.ID.String() == playbackID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("选中的播放设备已不存在，请刷新设备列表")
		}
	}
	e.captureMu.Lock()
	e.playbackMu.Lock()
	e.captureDeviceID, e.playbackDeviceID = captureID, playbackID
	e.playbackMu.Unlock()
	e.captureMu.Unlock()
	e.setSpectrum(make([]float64, 2048))
	return nil
}

func (e *Engine) setSpectrum(values []float64) {
	e.spectrumMu.Lock()
	e.spectrum = append(e.spectrum[:0], values...)
	e.spectrumMu.Unlock()
}

// RuntimeError exposes recovered callback/decoder panics to the UI instead of
// allowing a Go panic on an audio thread to terminate the whole desktop app.
func (e *Engine) RuntimeError() string {
	if e == nil {
		return ""
	}
	e.runtimeErrMu.RLock()
	defer e.runtimeErrMu.RUnlock()
	return e.runtimeErr
}

func (e *Engine) setRuntimeError(message string) {
	e.runtimeErrMu.Lock()
	e.runtimeErr = message
	e.runtimeErrMu.Unlock()
}

func (e *Engine) diagnostic(format string, args ...interface{}) {
	if e != nil && e.diagf != nil {
		e.diagf(format, args...)
	}
}

func (e *Engine) Close() {
	e.StopRX()
	e.StopPlayback()
	if e.ctx != nil {
		e.ctx.Uninit()
		e.ctx.Free()
		e.ctx = nil
	}
}

func (e *Engine) StartRX(onPacket func(aprs.Packet)) (retErr error) {
	if e == nil {
		return errors.New("音频引擎未初始化")
	}
	e.captureMu.Lock()
	defer e.captureMu.Unlock()
	if e.ctx == nil {
		return errors.New("音频上下文已关闭，请重新初始化音频引擎")
	}
	if e.capture != nil {
		return nil
	}

	// Recover Go panics raised while preparing/starting the native device. Native
	// access violations cannot be recovered in Go, but callback/decoder panics
	// are separately guarded below.
	var dev *malgo.Device
	var cancel context.CancelFunc
	workerStarted := false
	defer func() {
		if recovered := recover(); recovered != nil {
			if cancel != nil {
				cancel()
			}
			if dev != nil {
				safeStopAndUninit(dev)
			}
			if workerStarted {
				e.rxWG.Wait()
			}
			e.capture = nil
			e.rxCancel = nil
			e.rxChunks = nil
			retErr = fmt.Errorf("启动录音设备时发生内部异常: %v", recovered)
			e.setRuntimeError(retErr.Error())
		}
	}()

	e.setRuntimeError("")
	e.captureSamples.Store(0)
	e.captureDroppedChunks.Store(0)
	e.inputLevelBits.Store(math.Float64bits(-120))
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = SampleRate
	// Malgo passes DeviceID through a C config struct. A pointer to the Go
	// DeviceID object here makes that struct contain a Go pointer, triggering
	// cgo's "Go pointer to unpinned Go pointer" runtime check. DeviceID.Pointer
	// makes a C-allocated copy which is released after InitDevice consumes it.
	var nativeDeviceID unsafe.Pointer
	defer func() {
		if nativeDeviceID != nil {
			freeNativeBuffer(nativeDeviceID)
		}
	}()
	if e.captureDeviceID != "" {
		id, err := e.resolveDeviceID(malgo.Capture, e.captureDeviceID)
		if err != nil {
			return err
		}
		nativeDeviceID = id.Pointer()
		cfg.Capture.DeviceID = nativeDeviceID
	}
	chunks := make(chan audioChunk, 96)
	var chunkSequence atomic.Uint64
	callbacks := malgo.DeviceCallbacks{Data: func(_, input []byte, _ uint32) {
		// A panic crossing a cgo callback can terminate the complete process.
		// Keep this handler intentionally small and isolate all Go failures here.
		defer func() {
			if recovered := recover(); recovered != nil {
				e.setRuntimeError(fmt.Sprintf("录音回调异常: %v", recovered))
			}
		}()
		if len(input) < 2 {
			return
		}
		count := len(input) / 2
		e.captureSamples.Add(uint64(count))
		sequence := chunkSequence.Add(1)
		block := make([]int16, count)
		for i := 0; i < count; i++ {
			block[i] = int16(binary.LittleEndian.Uint16(input[i*2 : i*2+2]))
		}
		select {
		case chunks <- audioChunk{sequence: sequence, samples: block}:
		default: // Never block the native callback; count the loss for recovery/diagnostics.
			e.captureDroppedChunks.Add(1)
		}
	}}
	var err error
	e.diagnostic("录音设备初始化开始，指定设备=%q", e.captureDeviceID)
	dev, err = malgo.InitDevice(e.ctx.Context, cfg, callbacks)
	if err != nil {
		e.diagnostic("录音设备初始化失败: %v", err)
		return fmt.Errorf("打开所选录音设备失败: %w", err)
	}
	e.diagnostic("录音设备初始化完成，开始调用 Device.Start")
	if err = dev.Start(); err != nil {
		e.diagnostic("录音设备 Device.Start 失败: %v", err)
		safeStopAndUninit(dev)
		dev = nil
		return fmt.Errorf("启动录音设备失败: %w", err)
	}
	e.diagnostic("录音设备 Device.Start 成功")
	rxCtx, stop := context.WithCancel(context.Background())
	cancel = stop
	e.capture, e.rxChunks, e.rxCancel = dev, chunks, stop
	e.rxWG.Add(1)
	workerStarted = true
	go func() {
		defer e.rxWG.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				e.setRuntimeError(fmt.Sprintf("APRS 解码线程异常: %v", recovered))
			}
		}()
		decoder := aprs.NewAFSK1200Decoder(SampleRate)
		fftSamples := make([]int16, 0, 16384)
		var lastSequence uint64
		for {
			select {
			case <-rxCtx.Done():
				return
			case chunk := <-chunks:
				if len(chunk.samples) == 0 {
					continue
				}
				if missing := missingAudioChunks(lastSequence, chunk.sequence); missing > 0 {
					decoder.Reset()
					fftSamples = fftSamples[:0]
					e.diagnostic("检测到音频数据块缺口：丢失约 %d 个块，累计队列丢块=%d", missing, e.captureDroppedChunks.Load())
				}
				lastSequence = chunk.sequence
				block := chunk.samples
				e.inputLevelBits.Store(math.Float64bits(levelDBFS(block)))
				for _, packet := range decoder.Process(block) {
					if onPacket != nil {
						onPacket(packet)
					}
				}
				fftSamples = append(fftSamples, block...)
				for len(fftSamples) >= 8192 {
					e.setSpectrum(calculateSpectrum(fftSamples[:8192]))
					// 75% overlap improves update smoothness while retaining 11.7 Hz
					// nominal display-band spacing at a 48 kHz sample rate.
					fftSamples = fftSamples[2048:]
				}
			}
		}
	}()
	return nil
}

func safeStopAndUninit(dev *malgo.Device) {
	if dev == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = dev.Stop()
	dev.Uninit()
}

func (e *Engine) StopRX() {
	e.captureMu.Lock()
	dev, cancel := e.capture, e.rxCancel
	e.capture, e.rxCancel = nil, nil
	e.captureMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if dev != nil {
		dev.Stop()
		dev.Uninit()
	}
	e.rxWG.Wait()
	e.setSpectrum(make([]float64, 2048))
}

func (e *Engine) Play(ctx context.Context, samples []float32) error {
	if len(samples) == 0 {
		return errors.New("没有待播放的音频样本")
	}
	if err := e.ensurePlayback(); err != nil {
		return err
	}
	e.playbackMu.Lock()
	if e.txRunning {
		e.playbackMu.Unlock()
		return errors.New("当前已有音频正在发射")
	}
	done := make(chan struct{})
	e.txSamples = append([]float32(nil), samples...)
	e.txOffset = 0
	e.txDone = done
	e.txRunning = true
	e.playbackMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		e.playbackMu.Lock()
		e.txSamples = nil
		e.txOffset = 0
		e.txRunning = false
		if e.txDone == done {
			close(done)
			e.txDone = nil
		}
		e.playbackMu.Unlock()
		return ctx.Err()
	}
}

func (e *Engine) ensurePlayback() error {
	e.playbackMu.Lock()
	defer e.playbackMu.Unlock()
	if e.playback != nil {
		return nil
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = 1
	cfg.SampleRate = SampleRate
	// Use a C-owned copy for the same cgo pointer-rule reason as capture.
	var nativeDeviceID unsafe.Pointer
	defer func() {
		if nativeDeviceID != nil {
			freeNativeBuffer(nativeDeviceID)
		}
	}()
	if e.playbackDeviceID != "" {
		id, err := e.resolveDeviceID(malgo.Playback, e.playbackDeviceID)
		if err != nil {
			return err
		}
		nativeDeviceID = id.Pointer()
		cfg.Playback.DeviceID = nativeDeviceID
	}
	callbacks := malgo.DeviceCallbacks{Data: func(output, _ []byte, frameCount uint32) {
		e.playbackMu.Lock()
		defer e.playbackMu.Unlock()
		frames := int(frameCount)
		for i := 0; i < frames; i++ {
			v := float32(0)
			if e.txRunning && e.txOffset < len(e.txSamples) {
				v = e.txSamples[e.txOffset]
				e.txOffset++
			}
			if i*4+4 <= len(output) {
				binary.LittleEndian.PutUint32(output[i*4:], math.Float32bits(v))
			}
		}
		if e.txRunning && e.txOffset >= len(e.txSamples) {
			e.txRunning = false
			e.txSamples = nil
			if e.txDone != nil {
				close(e.txDone)
				e.txDone = nil
			}
		}
	}}
	dev, err := malgo.InitDevice(e.ctx.Context, cfg, callbacks)
	if err != nil {
		return fmt.Errorf("打开所选播放设备失败: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return fmt.Errorf("启动播放设备失败: %w", err)
	}
	e.playback = dev
	return nil
}

func (e *Engine) StopPlayback() {
	e.playbackMu.Lock()
	dev := e.playback
	e.playback = nil
	if e.txDone != nil {
		close(e.txDone)
		e.txDone = nil
	}
	e.txSamples = nil
	e.txRunning = false
	e.playbackMu.Unlock()
	if dev != nil {
		dev.Stop()
		dev.Uninit()
	}
}

func (e *Engine) resolveDeviceID(kind malgo.DeviceType, wanted string) (*malgo.DeviceID, error) {
	devices, err := e.ctx.Context.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("枚举音频设备失败: %w", err)
	}
	for _, device := range devices {
		if device.ID.String() == wanted {
			id := device.ID
			return &id, nil
		}
	}
	return nil, errors.New("已选择的音频设备无法找到，请刷新设备列表")
}

// missingAudioChunks reports missing callback blocks between two delivered blocks.
// A zero previous sequence means this is the first delivered block in a session.
func missingAudioChunks(previous, current uint64) uint64 {
	if previous == 0 || current <= previous+1 {
		return 0
	}
	return current - previous - 1
}

// levelDBFS returns a block RMS level relative to full-scale signed 16-bit PCM.
func levelDBFS(samples []int16) float64 {
	if len(samples) == 0 {
		return -120
	}
	sum := 0.0
	for _, sample := range samples {
		n := float64(sample) / 32768.0
		sum += n * n
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	if rms < 1e-6 {
		return -120
	}
	return 20 * math.Log10(rms)
}

// calculateSpectrum runs an 8192-point Hann-window FFT and condenses the positive
// frequency range into 2048 bands (about 11.7 Hz per band at 48 kHz). Magnitudes
// are calibrated against the Hann coherent gain and mapped to a -96..-20 dBFS
// display range so quiet line-level audio remains visible. It runs on the RX worker.
func calculateSpectrum(samples []int16) []float64 {
	const n = 8192
	if len(samples) < n {
		return make([]float64, 2048)
	}
	values := make([]complex128, n)
	for i := 0; i < n; i++ {
		window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		values[i] = complex(float64(samples[i])/32768*window, 0)
	}
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			values[i], values[j] = values[j], values[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		angle := -2 * math.Pi / float64(size)
		root := complex(math.Cos(angle), math.Sin(angle))
		for start := 0; start < n; start += size {
			w := complex(1.0, 0.0)
			half := size >> 1
			for j := 0; j < half; j++ {
				even := values[start+j]
				odd := w * values[start+j+half]
				values[start+j] = even + odd
				values[start+j+half] = even - odd
				w *= root
			}
		}
	}
	out := make([]float64, 2048)
	for band := range out {
		maxMag := 0.0
		for bin := band*2 + 1; bin < (band+1)*2+1 && bin < n/2; bin++ {
			// A Hann window has approximately n/4 coherent gain for a real
			// sinusoid in the positive-frequency bin. Divide by that gain so
			// the dBFS scale responds consistently to quiet audio inputs.
			mag := math.Hypot(real(values[bin]), imag(values[bin])) / (n / 4)
			if mag > maxMag {
				maxMag = mag
			}
		}
		db := 20 * math.Log10(math.Max(maxMag, 1e-6))
		out[band] = math.Max(0, math.Min(1, (db+96)/76))
	}
	return out
}

// WaitForSilence is available to callers that need an audio tail after TX.
func WaitForSilence(ctx context.Context, duration time.Duration) error {
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
