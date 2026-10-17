package aprs

import (
	"math"
)

const (
	DefaultSampleRate = 48000
	AFSKBitRate       = 1200
	AFSKMarkHz        = 1200 // Bell 202 mark: logical one after NRZI decoding
	AFSKSpaceHz       = 2200 // Bell 202 space: logical zero after NRZI decoding
)

// EncodeAFSK1200 turns a complete AX.25 frame into mono float32 audio samples.
// It uses phase-continuous Bell-202 tones and NRZI encoding.
func EncodeAFSK1200(frame []byte, sampleRate int) []float32 {
	if sampleRate < AFSKBitRate || sampleRate%AFSKBitRate != 0 {
		sampleRate = DefaultSampleRate
	}
	bitsPerSymbol := sampleRate / AFSKBitRate
	bits := BuildHDLCBits(frame, 40, 4)
	samples := make([]float32, 0, len(bits)*bitsPerSymbol)
	phase := 0.0
	mark := true
	const amplitude = 0.48
	for _, bit := range bits {
		if bit == 0 {
			mark = !mark
		}
		freq := float64(AFSKSpaceHz)
		if mark {
			freq = float64(AFSKMarkHz)
		}
		step := 2 * math.Pi * freq / float64(sampleRate)
		for i := 0; i < bitsPerSymbol; i++ {
			samples = append(samples, float32(amplitude*math.Sin(phase)))
			phase += step
			if phase >= 2*math.Pi {
				phase -= 2 * math.Pi
			}
		}
	}
	// A small quiet tail prevents the final sine wave from being cut abruptly.
	for i := 0; i < sampleRate/20; i++ {
		samples = append(samples, 0)
	}
	return samples
}

// AFSK1200Decoder is a practical basic Bell-202 decoder. It searches every
// possible sample phase and uses Goertzel tone energy, NRZI decoding, HDLC
// bit-destuffing and AX.25 FCS validation. It should be field-tested with the
// target radio/audio path before relying on it for weak-signal reception.
type AFSK1200Decoder struct {
	sampleRate int
	pending    []float64
	phaseStart []int
	phases     []phaseDecoder
	markCoeff  float64
	spaceCoeff float64
}

type phaseDecoder struct {
	haveTone  bool
	lastMark  bool
	assembler frameAssembler
}

type frameAssembler struct {
	inFrame bool
	bits    []byte
}

func NewAFSK1200Decoder(sampleRate int) *AFSK1200Decoder {
	if sampleRate < AFSKBitRate || sampleRate%AFSKBitRate != 0 {
		sampleRate = DefaultSampleRate
	}
	step := sampleRate / AFSKBitRate
	starts := make([]int, step)
	phases := make([]phaseDecoder, step)
	for i := range starts {
		starts[i] = i
	}
	markCoeff := 2 * math.Cos(2*math.Pi*float64(AFSKMarkHz)/float64(sampleRate))
	spaceCoeff := 2 * math.Cos(2*math.Pi*float64(AFSKSpaceHz)/float64(sampleRate))
	return &AFSK1200Decoder{
		sampleRate: sampleRate,
		phaseStart: starts,
		phases:     phases,
		markCoeff:  markCoeff,
		spaceCoeff: spaceCoeff,
	}
}

// Reset discards all buffered samples and synchronization state. Call it when
// the capture stream has a gap, because treating non-contiguous audio as
// contiguous can corrupt NRZI and HDLC framing until a later flag is found.
func (d *AFSK1200Decoder) Reset() {
	if d == nil {
		return
	}
	d.pending = d.pending[:0]
	for i := range d.phaseStart {
		d.phaseStart[i] = i
		d.phases[i] = phaseDecoder{}
	}
}

func (d *AFSK1200Decoder) Process(input []int16) []Packet {
	for _, v := range input {
		d.pending = append(d.pending, float64(v)/32768.0)
	}
	var out []Packet
	step := d.sampleRate / AFSKBitRate
	for phase := range d.phases {
		for d.phaseStart[phase]+step <= len(d.pending) {
			start := d.phaseStart[phase]
			markPower := goertzelPowerWithCoeff(d.pending[start:start+step], d.markCoeff)
			spacePower := goertzelPowerWithCoeff(d.pending[start:start+step], d.spaceCoeff)
			mark := markPower >= spacePower
			p := &d.phases[phase]
			if p.haveTone {
				bit := byte(1)
				if mark != p.lastMark {
					bit = 0
				}
				if raw := p.assembler.push(bit); raw != nil {
					packet, err := DecodeUIFrame(raw)
					if err == nil {
						out = append(out, packet)
					}
				}
			}
			p.lastMark = mark
			p.haveTone = true
			d.phaseStart[phase] += step
		}
	}
	minStart := d.phaseStart[0]
	for _, start := range d.phaseStart[1:] {
		if start < minStart {
			minStart = start
		}
	}
	if minStart > 0 {
		copy(d.pending, d.pending[minStart:])
		d.pending = d.pending[:len(d.pending)-minStart]
		for i := range d.phaseStart {
			d.phaseStart[i] -= minStart
		}
	}
	return out
}

func (a *frameAssembler) push(bit byte) []byte {
	a.bits = append(a.bits, bit)
	if len(a.bits) < 8 {
		return nil
	}
	n := len(a.bits)
	flag := a.bits[n-8] == 0 && a.bits[n-7] == 1 && a.bits[n-6] == 1 && a.bits[n-5] == 1 && a.bits[n-4] == 1 && a.bits[n-3] == 1 && a.bits[n-2] == 1 && a.bits[n-1] == 0
	if !flag {
		// Bound memory when there is only silence/noise and no HDLC flags.
		// AX.25 UI packets are far shorter than this limit.
		if len(a.bits) > 8192 {
			a.bits = a.bits[:0]
			a.inFrame = false
		}
		return nil
	}
	frameBits := a.bits[:n-8]
	var result []byte
	if a.inFrame && len(frameBits) >= 8*18 {
		result = bitsToFrame(destuffBits(frameBits))
	}
	a.inFrame = true
	a.bits = a.bits[:0]
	return result
}

func destuffBits(bits []byte) []byte {
	out := make([]byte, 0, len(bits))
	ones := 0
	for _, b := range bits {
		if ones == 5 {
			// The next zero is an inserted bit. A one means malformed/flag-like data.
			if b == 0 {
				ones = 0
				continue
			}
			// preserve malformed data so FCS validation will reject it.
		}
		out = append(out, b)
		if b == 1 {
			ones++
		} else {
			ones = 0
		}
	}
	return out
}

func bitsToFrame(bits []byte) []byte {
	if len(bits)%8 != 0 {
		return nil
	}
	out := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b != 0 {
			out[i/8] |= 1 << uint(i%8)
		}
	}
	return out
}

func goertzelPower(samples []float64, targetHz, sampleRate float64) float64 {
	coeff := 2 * math.Cos(2*math.Pi*targetHz/sampleRate)
	return goertzelPowerWithCoeff(samples, coeff)
}

func goertzelPowerWithCoeff(samples []float64, coeff float64) float64 {
	q0, q1, q2 := 0.0, 0.0, 0.0
	for _, x := range samples {
		q0 = coeff*q1 - q2 + x
		q2, q1 = q1, q0
	}
	return q1*q1 + q2*q2 - coeff*q1*q2
}
