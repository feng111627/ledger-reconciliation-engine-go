package money

import "testing"

func TestParseAndFormat(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"100", 100_000_000},
		{"100.00", 100_000_000},
		{"0.5", 500_000},
		{"-0.01", -10_000},
		{"100.000001", 100_000_001},
		{" 12.30 ", 12_300_000},
		{"+3.1", 3_100_000},
		{"0", 0},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil || got != c.want {
			t.Fatalf("Parse(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
		t.Logf("[业务:金额精度] 用例:Parse(%q)=%d ... PASS", c.in, got)
	}
	// 拒绝非法输入
	for _, bad := range []string{"", "abc", "1.2.3", "1.0000007", "-"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) 应当报错", bad)
		}
		t.Logf("[业务:金额精度] 用例:非法输入 %q 被拒绝 ... PASS", bad)
	}
	// 往返一致
	if Format(100_000_000) != "100.000000" {
		t.Fatalf("Format 得到 %q", Format(100_000_000))
	}
	if Format(-10_000) != "-0.010000" {
		t.Fatalf("Format 负数得到 %q", Format(-10_000))
	}
	t.Log("[业务:金额精度] 用例:Format 含负数格式化 ... PASS")
}
