package recon

import (
	"errors"
	"strconv"
	"strings"
)

// Money 是十进制定点数金额，内部以"微元"（1 单位 = 1e-6）存储，
// 足够覆盖常见记账精度，且不存在浮点误差，保证结论可重复。
type Money struct {
	v int64 // 微元
}

const moneyScale = 6

var errMoneyFormat = errors.New("invalid money format")

// ParseMoney 解析如 "12.34"、"-0.001"、"100" 的金额字符串。
// 小数部分超过 6 位时报错，而不是悄悄截断，避免丢精度。
func ParseMoney(s string) (Money, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Money{}, errMoneyFormat
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
		return Money{}, errMoneyFormat
	}

	var intPart, fracPart string
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
		if strings.IndexByte(fracPart, '.') >= 0 {
			return Money{}, errMoneyFormat
		}
	} else {
		intPart = s
	}
	if intPart == "" {
		intPart = "0"
	}
	for _, c := range intPart + fracPart {
		if c < '0' || c > '9' {
			return Money{}, errMoneyFormat
		}
	}
	if len(fracPart) > moneyScale {
		return Money{}, errMoneyFormat
	}
	fracPart = fracPart + strings.Repeat("0", moneyScale-len(fracPart))

	hi, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return Money{}, errMoneyFormat
	}
	lo, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return Money{}, errMoneyFormat
	}
	v := hi*1_000_000 + lo
	if neg {
		v = -v
	}
	return Money{v: v}, nil
}

// MustParseMoney 解析失败时 panic，仅供测试与常量构造使用。
func MustParseMoney(s string) Money {
	m, err := ParseMoney(s)
	if err != nil {
		panic(err)
	}
	return m
}

// MoneyFromScaled 以微元构造金额。
func MoneyFromScaled(v int64) Money { return Money{v: v} }

// Scaled 返回微元表示。
func (m Money) Scaled() int64 { return m.v }

func (m Money) Add(o Money) Money { return Money{v: m.v + o.v} }
func (m Money) Sub(o Money) Money { return Money{v: m.v - o.v} }
func (m Money) Neg() Money        { return Money{v: -m.v} }

func (m Money) Abs() Money {
	if m.v < 0 {
		return Money{v: -m.v}
	}
	return m
}

// Cmp 返回 -1/0/1。
func (m Money) Cmp(o Money) int {
	switch {
	case m.v < o.v:
		return -1
	case m.v > o.v:
		return 1
	default:
		return 0
	}
}

func (m Money) Equal(o Money) bool { return m.v == o.v }
func (m Money) IsZero() bool       { return m.v == 0 }

func (m Money) String() string { return formatScaled(m.v) }

// formatScaled 把微元还原为十进制字符串，去掉小数尾部无效的 0。
func formatScaled(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	hi, lo := v/1_000_000, v%1_000_000
	s := strconv.FormatInt(hi, 10)
	if lo != 0 {
		frac := strconv.FormatInt(lo, 10)
		frac = strings.Repeat("0", 6-len(frac)) + frac
		frac = strings.TrimRight(frac, "0")
		s += "." + frac
	}
	if neg {
		s = "-" + s
	}
	return s
}
