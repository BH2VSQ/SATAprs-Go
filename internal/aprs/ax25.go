package aprs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	AX25ControlUI   = 0x03
	AX25PIDNoLayer3 = 0xF0
	MaxPathHops     = 8
)

// Packet is a decoded AX.25 UI frame carrying an APRS information field.
type Packet struct {
	Destination string   `json:"destination"`
	Source      string   `json:"source"`
	Path        []string `json:"path"`
	Info        string   `json:"info"`
	Raw         []byte   `json:"-"`
}

// EncodeUIFrame creates an AX.25 UI frame and appends its FCS.
// path entries may contain a trailing '*' to indicate a digipeated address.
func EncodeUIFrame(source, destination string, path []string, info []byte) ([]byte, error) {
	if len(path) > MaxPathHops {
		return nil, fmt.Errorf("路径最多支持 %d 跳", MaxPathHops)
	}
	addresses := make([][]byte, 0, 2+len(path))
	for _, pair := range []struct {
		value string
		last  bool
	}{
		{value: destination}, {value: source, last: len(path) == 0},
	} {
		addr, err := encodeAddress(pair.value, pair.last)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, addr)
	}
	for i, p := range path {
		last := i == len(path)-1
		addr, err := encodeAddress(p, last)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, addr)
	}
	// The end bit belongs to the final address only. Rebuild address SSID bytes now
	// so source and path end markers are independent of the number of hops.
	for i := range addresses {
		addresses[i][6] &^= 0x01
		if i == len(addresses)-1 {
			addresses[i][6] |= 0x01
		}
	}
	frame := make([]byte, 0, 16+7*len(addresses)+len(info))
	for _, a := range addresses {
		frame = append(frame, a...)
	}
	frame = append(frame, AX25ControlUI, AX25PIDNoLayer3)
	frame = append(frame, info...)
	fcs := CRC16X25(frame)
	frame = append(frame, byte(fcs&0xFF), byte(fcs>>8))
	return frame, nil
}

func encodeAddress(value string, last bool) ([]byte, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return nil, errors.New("AX.25 地址不能为空")
	}
	used := false
	if strings.HasSuffix(value, "*") {
		used = true
		value = strings.TrimSuffix(value, "*")
	}
	// A slash-number operating suffix is equivalent to the numeric AX.25 SSID.
	// Text suffixes such as /QRP cannot be carried in an AX.25 address field.
	if slash := strings.LastIndex(value, "/"); slash >= 0 {
		base, suffix := value[:slash], value[slash+1:]
		if suffix == "" {
			return nil, fmt.Errorf("无效特殊呼号后缀: %q", value)
		}
		n, err := strconv.Atoi(suffix)
		if err != nil {
			return nil, fmt.Errorf("AX.25 地址不能直接编码文本后缀 /%s；请使用基础呼号和数字 SSID", suffix)
		}
		if n < 0 || n > 15 {
			return nil, fmt.Errorf("特殊呼号数字后缀必须为 /0 到 /15: %q", suffix)
		}
		value = fmt.Sprintf("%s-%d", base, n)
	}
	ssid := 0
	if i := strings.LastIndex(value, "-"); i >= 0 {
		n := value[i+1:]
		if n == "" {
			return nil, fmt.Errorf("无效地址 %q", value)
		}
		for _, c := range n {
			if c < '0' || c > '9' {
				return nil, fmt.Errorf("无效 SSID: %q", n)
			}
		}
		var err error
		_, err = fmt.Sscanf(n, "%d", &ssid)
		if err != nil || ssid < 0 || ssid > 15 {
			return nil, fmt.Errorf("SSID 必须在 0 到 15 之间: %q", n)
		}
		value = value[:i]
	}
	if len(value) == 0 || len(value) > 6 {
		return nil, fmt.Errorf("呼号/地址长度必须为 1 到 6 个字符: %q", value)
	}
	out := make([]byte, 7)
	for i := 0; i < 6; i++ {
		var c byte = ' '
		if i < len(value) {
			c = value[i]
		}
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == ' ') {
			return nil, fmt.Errorf("地址包含不支持的字符: %q", string(c))
		}
		out[i] = c << 1
	}
	out[6] = 0x60 | byte(ssid<<1)
	if used {
		out[6] |= 0x80
	}
	if last {
		out[6] |= 0x01
	}
	return out, nil
}

// CRC16X25 implements the reflected CRC-16/X-25 used by AX.25.
func CRC16X25(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

// DecodeUIFrame validates the FCS, then parses the address chain and APRS payload.
func DecodeUIFrame(frame []byte) (Packet, error) {
	if len(frame) < 18 {
		return Packet{}, errors.New("AX.25 帧长度过短")
	}
	payload := frame[:len(frame)-2]
	got := binary.LittleEndian.Uint16(frame[len(frame)-2:])
	if CRC16X25(payload) != got {
		return Packet{}, errors.New("AX.25 FCS 校验失败")
	}
	idx := 0
	addresses := make([]string, 0, 4)
	for count := 0; count < 2+MaxPathHops; count++ {
		if idx+7 > len(payload) {
			return Packet{}, errors.New("AX.25 地址字段不完整")
		}
		name := decodeAddress(payload[idx:idx+7], len(addresses) >= 2)
		addresses = append(addresses, name)
		last := payload[idx+6]&0x01 != 0
		idx += 7
		if last {
			break
		}
		if count == 1+MaxPathHops {
			return Packet{}, errors.New("AX.25 地址链过长")
		}
	}
	if len(addresses) < 2 || idx+2 > len(payload) {
		return Packet{}, errors.New("AX.25 地址链无效")
	}
	control, pid := payload[idx], payload[idx+1]
	if control != AX25ControlUI || pid != AX25PIDNoLayer3 {
		return Packet{}, fmt.Errorf("不支持的 AX.25 控制/PID: %02X/%02X", control, pid)
	}
	path := []string{}
	if len(addresses) > 2 {
		path = append(path, addresses[2:]...)
	}
	return Packet{Destination: addresses[0], Source: addresses[1], Path: path, Info: string(payload[idx+2:]), Raw: append([]byte(nil), frame...)}, nil
}

func decodeAddress(a []byte, allowRepeatedMarker bool) string {
	var call strings.Builder
	for i := 0; i < 6; i++ {
		c := byte(a[i] >> 1)
		if c == ' ' {
			break
		}
		call.WriteByte(c)
	}
	ssid := int((a[6] >> 1) & 0x0F)
	name := call.String()
	if ssid != 0 {
		name += fmt.Sprintf("-%d", ssid)
	}
	if allowRepeatedMarker && a[6]&0x80 != 0 {
		name += "*"
	}
	return name
}

// BuildHDLCBits emits LSB-first HDLC bits, including flags and bit stuffing.
func BuildHDLCBits(frame []byte, preambleFlags, tailFlags int) []byte {
	flag := []byte{0, 1, 1, 1, 1, 1, 1, 0}
	bits := make([]byte, 0, (preambleFlags+tailFlags+len(frame))*8+16)
	for i := 0; i < preambleFlags; i++ {
		bits = append(bits, flag...)
	}
	ones := 0
	for _, b := range frame {
		for bit := 0; bit < 8; bit++ {
			v := (b >> bit) & 1
			bits = append(bits, v)
			if v == 1 {
				ones++
				if ones == 5 {
					bits = append(bits, 0)
					ones = 0
				}
			} else {
				ones = 0
			}
		}
	}
	for i := 0; i < tailFlags; i++ {
		bits = append(bits, flag...)
	}
	return bits
}

// ParseAPRSMessage extracts an APRS message's addressee and text when applicable.
func ParseAPRSMessage(info string) (addressee, message string, ok bool) {
	if len(info) < 11 || info[0] != ':' || info[10] != ':' {
		return "", "", false
	}
	return strings.TrimSpace(info[1:10]), info[11:], true
}
