package recon

import "time"

// ClassifyPair 对一对候选流水分类。
// 调用方应保证两笔属于同一业务（同 Ref 或同金额分组）。
//
// 判定优先级：
//  1. 币种不同   -> 不构成配对（返回 ok=false，由调用方按缺失处理）
//  2. 时间超窗   -> DiffTimeOutOfWindow（金额币种一致但跨批次，时间对不上）
//  3. 金额相等   -> DiffNone / matched
//  4. 差额在容差内 -> DiffNone / tolerated（自动视为已核对）
//  5. 差额超容差  -> DiffAmountMismatch / pending
func ClassifyPair(a, b Entry, cfg Config) (diff DiffType, delta Money, status Status, ok bool) {
	if a.Currency != b.Currency {
		return DiffNone, Money{}, StatusPending, false
	}

	// 时间窗口：两笔时间距离超过窗口，说明时间没对上（可能跨批次错配）。
	d := a.Time.Sub(b.Time)
	if d < 0 {
		d = -d
	}
	if cfg.TimeWindow > 0 && d > cfg.TimeWindow {
		return DiffTimeOutOfWindow, a.Amount.Sub(b.Amount), StatusPending, true
	}

	delta = a.Amount.Sub(b.Amount)
	abs := delta.Abs()
	tol := cfg.ToleranceFor(a.Currency)
	switch {
	case abs.IsZero():
		return DiffNone, delta, StatusMatched, true
	case abs.Cmp(tol) <= 0:
		return DiffNone, delta, StatusTolerated, true
	default:
		return DiffAmountMismatch, delta, StatusPending, true
	}
}

// MissingDiff 返回"某一侧缺失"的差异类型：
// 我方缺失（对方有我方没有）/ 对方缺失（我方有对方没有）。
func MissingDiff(missing Side) DiffType {
	if missing == SideLocal {
		return DiffLocalMissing
	}
	return DiffRemoteMissing
}

// withinWindow 判断两笔是否在时间窗口内。
func withinWindow(a, b Entry, cfg Config) bool {
	if cfg.TimeWindow <= 0 {
		return true
	}
	d := a.Time.Sub(b.Time)
	if d < 0 {
		d = -d
	}
	return d <= cfg.TimeWindow
}

var _ = time.Minute
