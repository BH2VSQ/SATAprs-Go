package aprs

import "math"

// AFSK1200DPLLDecoder is a streaming Bell-202 decoder that mixes the input
// around 1700 Hz, uses quadrature phase-rate discrimination, and recovers the
// 1200 baud symbol clock with a transition-driven digital PLL.
//
// It deliberately lives beside AFSK1200Decoder so the old decoder remains
// available for A/B comparisons and regression tests.
type AFSK1200DPLLDecoder struct {
	sampleRate int

	inputHP biquadDPLL
	inputLP biquadDPLL
	iLP     biquadDPLL
	qLP     biquadDPLL

	carrierPhase  float64
	carrierStep   float64
	previousI     float64
	previousQ     float64
	haveIQ        bool
	discriminator float64

	symbolPhase float64 // fractional symbol position, normalized to [0, 1)
	symbolStep  float64 // symbols advanced per audio sample
	nominalStep float64

	haveTone       bool
	currentMark    bool
	haveSymbolTone bool
	lastSymbolMark bool

	assembler dpllFrameAssembler
}

// NewAFSK1200DPLLDecoder constructs the second receive demodulator. Invalid
// rates fall back to the same 48 kHz rate used by the existing encoder.
func NewAFSK1200DPLLDecoder(sampleRate int) *AFSK1200DPLLDecoder {
	if sampleRate < AFSKBitRate || sampleRate%AFSKBitRate != 0 {
		sampleRate = DefaultSampleRate
	}
	nominalStep := float64(AFSKBitRate) / float64(sampleRate)
	return &AFSK1200DPLLDecoder{
		sampleRate:  sampleRate,
		inputHP:     newBiquadDPLL(sampleRate, 700, true),
		inputLP:     newBiquadDPLL(sampleRate, 2800, false),
		iLP:         newBiquadDPLL(sampleRate, 900, false),
		qLP:         newBiquadDPLL(sampleRate, 900, false),
		carrierStep: 2 * math.Pi * 1700 / float64(sampleRate),
		symbolStep:  nominalStep,
		nominalStep: nominalStep,
	}
}

// Reset drops a partial frame and restarts all filter, carrier and timing state.
// Call it when the audio capture pipeline reports a dropped/non-contiguous block.
func (d *AFSK1200DPLLDecoder) Reset() {
	if d == nil {
		return
	}
	d.inputHP.reset()
	d.inputLP.reset()
	d.iLP.reset()
	d.qLP.reset()
	d.carrierPhase = 0
	d.previousI, d.previousQ = 0, 0
	d.haveIQ = false
	d.discriminator = 0
	d.symbolPhase = 0
	d.symbolStep = d.nominalStep
	d.haveTone = false
	d.currentMark = false
	d.haveSymbolTone = false
	d.lastSymbolMark = false
	d.assembler.reset()
}

// Process consumes arbitrary-sized audio blocks. Decoder state is preserved
// across calls, so callers may pass device callback blocks of varying lengths.
func (d *AFSK1200DPLLDecoder) Process(input []int16) []Packet {
	if d == nil || len(input) == 0 {
		return nil
	}
	out := make([]Packet, 0, 1)
	const (
		pllPhaseGain   = 0.18
		pllFreqGain    = 0.0005
		minPower       = 1e-12
		toneHysteresis = 0.003
	)
	minStep := d.nominalStep * 0.995
	maxStep := d.nominalStep * 1.005

	for _, pcm := range input {
		sample := float64(pcm) / 32768.0
		sample = d.inputHP.process(sample)
		sample = d.inputLP.process(sample)

		oscCos := math.Cos(d.carrierPhase)
		oscSin := math.Sin(d.carrierPhase)
		d.carrierPhase += d.carrierStep
		if d.carrierPhase >= 2*math.Pi {
			d.carrierPhase -= 2 * math.Pi
		}
		i := d.iLP.process(sample * oscCos)
		q := d.qLP.process(sample * oscSin)

		oldClockPhase := d.symbolPhase
		d.symbolPhase += d.symbolStep
		if d.symbolPhase >= 1 {
			d.symbolPhase -= 1
		}

		power := i*i + q*q
		if d.haveIQ {
			dot := i*d.previousI + q*d.previousQ
			cross := q*d.previousI - i*d.previousQ
			if dot != 0 || cross != 0 {
				phaseDelta := math.Atan2(cross, dot)
				// A light one-pole smoother reduces isolated discriminator flips
				// while keeping the Bell-202 tone transitions reasonably sharp.
				d.discriminator += 0.35 * (phaseDelta - d.discriminator)
			}
		}
		d.previousI, d.previousQ = i, q
		d.haveIQ = true

		transitioned := false
		if power > minPower {
			mark := d.currentMark
			if d.discriminator > toneHysteresis {
				mark = true
			} else if d.discriminator < -toneHysteresis {
				mark = false
			}
			if !d.haveTone {
				d.currentMark = mark
				d.haveTone = true
			} else if mark != d.currentMark {
				d.currentMark = mark
				transitioned = true
			}
		}

		if transitioned {
			// A tone transition should occur on a symbol boundary. Select the
			// nearest boundary and gently correct phase and rate, avoiding jumps.
			err := d.symbolPhase
			if err > 0.5 {
				err -= 1
			}
			d.symbolPhase -= pllPhaseGain * err
			if d.symbolPhase < 0 {
				d.symbolPhase += 1
			} else if d.symbolPhase >= 1 {
				d.symbolPhase -= 1
			}
			d.symbolStep -= d.nominalStep * pllFreqGain * err
			if d.symbolStep < minStep {
				d.symbolStep = minStep
			} else if d.symbolStep > maxStep {
				d.symbolStep = maxStep
			}
		}

		// Sample near the symbol center. The transition-driven PLL only nudges
		// phase at confident tone changes; the local clock free-runs otherwise.
		if oldClockPhase < 0.5 && d.symbolPhase >= 0.5 {
			if d.haveSymbolTone {
				bit := byte(1) // NRZI: unchanged tone is a logical one.
				if d.currentMark != d.lastSymbolMark {
					bit = 0
				}
				if raw := d.assembler.push(bit); raw != nil {
					if packet, err := DecodeUIFrame(raw); err == nil {
						out = append(out, packet)
					}
				}
			}
			d.lastSymbolMark = d.currentMark
			d.haveSymbolTone = d.haveTone
		}
	}
	return out
}

type dpllFrameAssembler struct {
	inFrame bool
	bits    []byte
}

func (a *dpllFrameAssembler) reset() {
	a.inFrame = false
	a.bits = a.bits[:0]
}

func (a *dpllFrameAssembler) push(bit byte) []byte {
	a.bits = append(a.bits, bit&1)
	if len(a.bits) < 8 {
		return nil
	}
	n := len(a.bits)
	flag := a.bits[n-8] == 0 && a.bits[n-7] == 1 && a.bits[n-6] == 1 &&
		a.bits[n-5] == 1 && a.bits[n-4] == 1 && a.bits[n-3] == 1 &&
		a.bits[n-2] == 1 && a.bits[n-1] == 0
	if !flag {
		if len(a.bits) > 8192 {
			a.reset()
		}
		return nil
	}

	frameBits := a.bits[:n-8]
	var result []byte
	if a.inFrame && len(frameBits) >= 8*18 {
		if destuffed, ok := destuffDPLLBits(frameBits); ok {
			result = dpllBitsToFrame(destuffed)
		}
	}
	a.inFrame = true
	a.bits = a.bits[:0]
	return result
}

func destuffDPLLBits(bits []byte) ([]byte, bool) {
	out := make([]byte, 0, len(bits))
	ones := 0
	for _, bit := range bits {
		if ones == 5 {
			if bit != 0 {
				return nil, false
			}
			ones = 0
			continue
		}
		out = append(out, bit)
		if bit == 1 {
			ones++
		} else {
			ones = 0
		}
	}
	return out, true
}

func dpllBitsToFrame(bits []byte) []byte {
	if len(bits)%8 != 0 {
		return nil
	}
	frame := make([]byte, len(bits)/8)
	for i, bit := range bits {
		if bit != 0 {
			frame[i/8] |= 1 << uint(i%8)
		}
	}
	return frame
}

// biquadDPLL is a transposed direct-form-II second-order Butterworth section.
type biquadDPLL struct {
	b0, b1, b2 float64
	a1, a2     float64
	z1, z2     float64
}

func newBiquadDPLL(sampleRate int, cutoffHz float64, highPass bool) biquadDPLL {
	const q = 0.7071067811865476
	w0 := 2 * math.Pi * cutoffHz / float64(sampleRate)
	c := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * q)
	a0 := 1 + alpha
	f := biquadDPLL{
		a1: -2 * c / a0,
		a2: (1 - alpha) / a0,
	}
	if highPass {
		f.b0 = (1 + c) / (2 * a0)
		f.b1 = -(1 + c) / a0
		f.b2 = (1 + c) / (2 * a0)
	} else {
		f.b0 = (1 - c) / (2 * a0)
		f.b1 = (1 - c) / a0
		f.b2 = (1 - c) / (2 * a0)
	}
	return f
}

func (f *biquadDPLL) process(x float64) float64 {
	y := f.b0*x + f.z1
	f.z1 = f.b1*x - f.a1*y + f.z2
	f.z2 = f.b2*x - f.a2*y
	return y
}

func (f *biquadDPLL) reset() {
	f.z1, f.z2 = 0, 0
}
