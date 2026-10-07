package recon

import "testing"

// TestMoneyParseAndArithmetic 业务：金额必须精确无浮点误差，
// 支撑"差多少"要能给出确定答案。
func TestMoneyParseAndArithmetic(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"100", "100"},
		{"100.00", "100"},
		{"200.005", "200.005"},
		{"-0.001", "-0.001"},
		{"0.1", "0.1"},
		{"  12.34  ", "12.34"},
	}
	for _, c := range cases {
		m, err := ParseMoney(c.in)
		if err != nil {
			t.Fatalf("ParseMoney(%q) 出错: %v", c.in, err)
		}
		if got := m.String(); got != c.want {
			t.Errorf("ParseMoney(%q) = %s, 期望 %s", c.in, got, c.want)
		}
	}

	// 0.1 + 0.2 在定点数下必须精确等于 0.3（浮点会得到 0.30000000000000004）。
	a := MustParseMoney("0.1")
	b := MustParseMoney("0.2")
	if got := a.Add(b).String(); got != "0.3" {
		t.Errorf("0.1+0.2 = %s, 期望 0.3", got)
	}

	if d := MustParseMoney("200.005").Sub(MustParseMoney("200.00")); d.String() != "0.005" {
		t.Errorf("差额 = %s, 期望 0.005", d.String())
	}

	if MustParseMoney("-3").Abs().String() != "3" {
		t.Error("Abs 负数错误")
	}

	// 超精度输入必须报错而不是悄悄丢钱。
	for _, bad := range []string{"", "abc", "1.2.3", "0.0000001"} {
		if _, err := ParseMoney(bad); err == nil {
			t.Errorf("ParseMoney(%q) 应当报错", bad)
		}
	}
}
