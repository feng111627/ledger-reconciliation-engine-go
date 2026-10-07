package money

import (
	"errors"
	"strconv"
	"strings"
)

// Scale 金额放大倍数：1 个货币单位 = 1e6 个最小单位。
const Scale int64 = 1_000_000

// Parse 把十进制金额字符串解析为放大 1e6 的整数。
// 支持："12.34"、"-0.5"、"100"、" 12.30 "、"+3.10"。
// 禁止科学计数法与多个小数点，避免歧义。
func Parse(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty amount")
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, errors.New("invalid amount: missing digits")
	}
	intPart, fracPart := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		if strings.LastIndexByte(s, '.') != dot {
			return 0, errors.New("invalid amount: multiple decimal points")
		}
		intPart, fracPart = s[:dot], s[dot+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(fracPart) > 6 {
		return 0, errors.New("invalid amount: more than 6 decimal places")
	}
	for _, c := range intPart + fracPart {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid amount: non-digit character")
		}
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, errors.New("invalid amount: integer part out of range")
	}
	frac := fracPart + strings.Repeat("0", 6-len(fracPart))
	fracVal, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("invalid amount: fraction out of range")
	}
	v := whole*Scale + fracVal
	if neg {
		v = -v
	}
	return v, nil
}

// Format 把放大 1e6 的整数格式化为带两位小数的十进制字符串（保留符号语义）。
func Format(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	whole := v / Scale
	frac := v % Scale
	s := strconv.FormatInt(whole, 10) + "." + pad6(frac)
	if neg {
		s = "-" + s
	}
	return s
}

func pad6(v int64) string {
	s := strconv.FormatInt(v, 10)
	return strings.Repeat("0", 6-len(s)) + s
}
