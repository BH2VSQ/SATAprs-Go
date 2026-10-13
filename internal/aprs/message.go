package aprs

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const MaxAPRSMessageText = 67

// NormalizeMessageAddressee accepts special callsign notation in the UI while
// keeping the on-air APRS message addressee compatible with the fixed 9-byte
// address field. Numeric slash suffixes become numeric SSIDs (CALL/5 -> CALL-5).
// Text aliases such as CALL/QRP route to the base callsign because they cannot
// be represented in a standard AX.25 callsign/SSID address.
func NormalizeMessageAddressee(addressee string) (string, error) {
	addressee = strings.ToUpper(strings.TrimSpace(addressee))
	if !strings.Contains(addressee, "/") {
		return addressee, nil
	}
	parts := strings.Split(addressee, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("特殊呼号格式无效，请使用 N0CALL/5 或 N0CALL/QRP")
	}
	base, suffix := parts[0], parts[1]
	if len(base) < 1 || len(base) > 6 {
		return "", errors.New("特殊呼号基础部分长度必须为 1 到 6 个字符")
	}
	for _, c := range base {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", errors.New("特殊呼号基础部分只允许英文字母和数字")
		}
	}
	if n, err := strconv.Atoi(suffix); err == nil {
		if n < 0 || n > 15 {
			return "", errors.New("数字特殊后缀必须在 /0 到 /15 之间")
		}
		return fmt.Sprintf("%s-%d", base, n), nil
	}
	if len(suffix) > 4 {
		return "", errors.New("文字特殊后缀最多 4 个字符")
	}
	for _, c := range suffix {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", errors.New("特殊后缀只允许英文字母和数字")
		}
	}
	return base, nil
}

func BuildMessageInfo(addressee, text string) ([]byte, error) {
	addressee = strings.ToUpper(strings.TrimSpace(addressee))
	if addressee == "" {
		return nil, errors.New("消息收件人不能为空")
	}
	var err error
	addressee, err = NormalizeMessageAddressee(addressee)
	if err != nil {
		return nil, err
	}
	if len(addressee) > 9 {
		return nil, errors.New("APRS 收件人最多 9 个字符")
	}
	if strings.ContainsAny(addressee, ":\r\n") {
		return nil, errors.New("收件人包含非法字符")
	}
	// Validate emptiness without trimming the body: APRS text may contain
	// intentional leading, repeated, and trailing spaces.
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("消息内容不能为空")
	}
	if strings.ContainsAny(text, "\r\n") {
		return nil, errors.New("APRS 消息不能包含换行")
	}
	if len(text) > MaxAPRSMessageText {
		return nil, fmt.Errorf("APRS 消息正文最多 %d 字节，当前 %d 字节", MaxAPRSMessageText, len(text))
	}
	return []byte(fmt.Sprintf(":%-9s:%s", addressee, text)), nil
}

// BuildPositionInfo builds an uncompressed APRS position from decimal degrees.
// symbolTable is typically '/' and symbolCode '>'.
func BuildPositionInfo(lat, lon float64, symbolTable, symbolCode byte, comment string) (string, error) {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return "", errors.New("经纬度超出范围")
	}
	if symbolTable == 0 {
		symbolTable = '/'
	}
	if symbolCode == 0 {
		symbolCode = '>'
	}
	latAbs := math.Abs(lat)
	lonAbs := math.Abs(lon)
	latDeg := int(latAbs)
	lonDeg := int(lonAbs)
	latMin := (latAbs - float64(latDeg)) * 60
	lonMin := (lonAbs - float64(lonDeg)) * 60
	info := fmt.Sprintf("!%02d%05.2f%c%c%03d%05.2f%c%c", latDeg, latMin, hemisphere(lat, 'N', 'S'), symbolTable, lonDeg, lonMin, hemisphere(lon, 'E', 'W'), symbolCode)
	if c := strings.TrimSpace(comment); c != "" {
		info += c
	}
	return info, nil
}

func hemisphere(v float64, positive, negative byte) byte {
	if v < 0 {
		return negative
	}
	return positive
}

// MaidenheadToLatLon converts a 4-, 6-, or 8-character Maidenhead locator
// to the center point of the represented square.
func MaidenheadToLatLon(grid string) (lat, lon float64, err error) {
	grid = strings.ToUpper(strings.TrimSpace(grid))
	if len(grid) != 4 && len(grid) != 6 && len(grid) != 8 {
		return 0, 0, errors.New("网格定位符需为 4、6 或 8 个字符")
	}
	lon = -180
	lat = -90
	lonStep, latStep := 20.0, 10.0
	for i := 0; i < len(grid); i += 2 {
		a, b := grid[i], grid[i+1]
		var x, y float64
		switch i {
		case 0:
			if a < 'A' || a > 'R' || b < 'A' || b > 'R' {
				return 0, 0, errors.New("网格定位符前两位需为 A 到 R")
			}
			x, y = float64(a-'A'), float64(b-'A')
			lon += x * lonStep
			lat += y * latStep
		case 2:
			if a < '0' || a > '9' || b < '0' || b > '9' {
				return 0, 0, errors.New("网格定位符数字位无效")
			}
			lonStep, latStep = 2.0, 1.0
			lon += float64(a-'0') * lonStep
			lat += float64(b-'0') * latStep
		case 4:
			if a < 'A' || a > 'X' || b < 'A' || b > 'X' {
				return 0, 0, errors.New("网格定位符子方格需为 A 到 X")
			}
			lonStep, latStep = 2.0/24.0, 1.0/24.0
			lon += float64(a-'A') * lonStep
			lat += float64(b-'A') * latStep
		case 6:
			if a < '0' || a > '9' || b < '0' || b > '9' {
				return 0, 0, errors.New("网格定位符扩展数字位无效")
			}
			lonStep, latStep = (2.0/24.0)/10, (1.0/24.0)/10
			lon += float64(a-'0') * lonStep
			lat += float64(b-'0') * latStep
		}
	}
	return lat + latStep/2, lon + lonStep/2, nil
}
