package recon

import (
	"testing"
	"time"
)

func testEntry(id, side, cur, amt string, at time.Time, ref string) Entry {
	return Entry{
		ID:       id,
		Side:     Side(side),
		Currency: cur,
		Amount:   MustParseMoney(amt),
		Time:     at.UTC(),
		Ref:      ref,
	}
}

// TestClassifyPair 业务：匹配规则——金额、币种、时间窗口的判定优先级，
// 以及容差内外的自动核销/入队分类。
func TestClassifyPair(t *testing.T) {
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cfg := DefaultConfig() // 窗口 10m，容差 0.01

	t.Run("精确匹配", func(t *testing.T) {
		a := testEntry("L", "local", "CNY", "100.00", base, "O1")
		b := testEntry("R", "remote", "CNY", "100.00", base.Add(2*time.Minute), "O1")
		diff, delta, st, ok := ClassifyPair(a, b, cfg)
		if !ok || diff != DiffNone || st != StatusMatched || !delta.IsZero() {
			t.Fatalf("得到 diff=%q status=%q delta=%s ok=%v", diff, st, delta, ok)
		}
	})

	t.Run("容差内自动核销", func(t *testing.T) {
		a := testEntry("L", "local", "CNY", "100.005", base, "O1")
		b := testEntry("R", "remote", "CNY", "100.00", base, "O1")
		diff, _, st, ok := ClassifyPair(a, b, cfg)
		if !ok || diff != DiffNone || st != StatusTolerated {
			t.Fatalf("得到 diff=%q status=%q ok=%v, 期望 tolerated", diff, st, ok)
		}
	})

	t.Run("容差边界等于也算容差", func(t *testing.T) {
		a := testEntry("L", "local", "CNY", "100.01", base, "O1")
		b := testEntry("R", "remote", "CNY", "100.00", base, "O1")
		if _, _, st, ok := ClassifyPair(a, b, cfg); !ok || st != StatusTolerated {
			t.Fatalf("差额恰好等于容差，期望 tolerated, 得到 %v", st)
		}
	})

	t.Run("超容差进差异队列", func(t *testing.T) {
		a := testEntry("L", "local", "CNY", "100.02", base, "O1")
		b := testEntry("R", "remote", "CNY", "100.00", base, "O1")
		diff, _, st, ok := ClassifyPair(a, b, cfg)
		if !ok || diff != DiffAmountMismatch || st != StatusPending {
			t.Fatalf("得到 diff=%q status=%q, 期望 amount_mismatch/pending", diff, st)
		}
	})

	t.Run("时间超窗口归时间差", func(t *testing.T) {
		a := testEntry("L", "local", "CNY", "100.00", base, "O1")
		b := testEntry("R", "remote", "CNY", "100.00", base.Add(3*time.Hour), "O1")
		diff, _, st, ok := ClassifyPair(a, b, cfg)
		if !ok || diff != DiffTimeOutOfWindow || st != StatusPending {
			t.Fatalf("得到 diff=%q status=%q, 期望 time_out_of_window/pending", diff, st)
		}
	})

	t.Run("币种不同不构成配对", func(t *testing.T) {
		a := testEntry("L", "local", "CNY", "100.00", base, "O1")
		b := testEntry("R", "remote", "USD", "100.00", base, "O1")
		if _, _, _, ok := ClassifyPair(a, b, cfg); ok {
			t.Fatal("币种不同不应构成配对")
		}
	})

	t.Run("缺失侧类型", func(t *testing.T) {
		if MissingDiff(SideLocal) != DiffLocalMissing {
			t.Error("我方缺失类型错误")
		}
		if MissingDiff(SideRemote) != DiffRemoteMissing {
			t.Error("对方缺失类型错误")
		}
	})
}
