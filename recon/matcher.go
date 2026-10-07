package recon

import (
	"fmt"
	"sort"
	"time"
)

// MatchResult 匹配阶段的输出。
type MatchResult struct {
	Pairs   []Pair
	Entries []Entry
}

// groupKey 把流水划分为同一业务组：
// 有 Ref 时按 (Ref, 币种) 归组；无 Ref 时按 (无业务号, 币种) 归组，
// 组内依靠金额与时间窗口配对。
func groupKey(e Entry) string {
	if e.Ref != "" {
		return "R:" + e.Ref + "|" + e.Currency
	}
	return "N:|" + e.Currency
}

// Match 执行确定性匹配：
//  1. 按业务组划分，组内按时间顺序做 1:1 贪心配对，
//     优先在时间窗口内选择金额差最小的对方流水（平局按时间距离、ID 打破）；
//  2. 窗口内候选走 ClassifyPair：matched / tolerated / amount_mismatch；
//     窗口外但确属同业务：金额一致 -> time_out_of_window，金额也不符 -> amount_mismatch；
//  3. 两侧剩余项：同业务同金额已有配对的多余项 -> duplicate，
//     否则 -> 对方/我方缺失。
//
// 任何一条流水最终恰好出现在一条 Pair 中，不会被丢弃。
// 结果只取决于输入数据，重复运行结论一致。
func Match(local, remote []Entry, cfg Config) MatchResult {
	locals := append([]Entry(nil), local...)
	remotes := append([]Entry(nil), remote...)
	sortEntries(locals)
	sortEntries(remotes)

	groups := map[string]*bucket{}
	getGroup := func(e Entry) *bucket {
		k := groupKey(e)
		g := groups[k]
		if g == nil {
			g = &bucket{key: k}
			groups[k] = g
		}
		return g
	}
	for i := range locals {
		g := getGroup(locals[i])
		g.locals = append(g.locals, i)
	}
	for i := range remotes {
		g := getGroup(remotes[i])
		g.remotes = append(g.remotes, i)
	}

	// 组键排序，保证处理顺序确定。
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	usedRemote := make([]bool, len(remotes))
	usedLocal := make([]bool, len(locals))
	var pairs []Pair

	// 记录每个组中已经配对过的金额，用于判定"多余的同业务同金额"=重复入账。
	localPaidAmt := map[string]map[int64]bool{}
	remotePaidAmt := map[string]map[int64]bool{}
	markPaid := func(m map[string]map[int64]bool, gk string, v int64) {
		if m[gk] == nil {
			m[gk] = map[int64]bool{}
		}
		m[gk][v] = true
	}

	for _, gk := range keys {
		g := groups[gk]
		for _, li := range g.locals {
			l := locals[li]
			ri, inWindow := pickCandidate(l, g.remotes, remotes, usedRemote, cfg)
			if ri < 0 {
				continue
			}
			r := remotes[ri]
			usedLocal[li] = true
			usedRemote[ri] = true
			markPaid(localPaidAmt, gk, l.Amount.Scaled())
			markPaid(remotePaidAmt, gk, r.Amount.Scaled())

			p := Pair{LocalID: l.ID, RemoteID: r.ID, Delta: l.Amount.Sub(r.Amount)}
			d := l.Time.Sub(r.Time)
			if inWindow {
				diff, delta, status, ok := ClassifyPair(l, r, cfg)
				if !ok { // 理论上不会发生（同组必同币种），防御性处理
					usedLocal[li] = false
					usedRemote[ri] = false
					continue
				}
				p.Diff, p.Delta, p.Status = diff, delta, status
				switch status {
				case StatusMatched:
					p.Note = fmt.Sprintf("金额币种一致，时间在窗口内（时间差 %s），精确匹配", durStr(d))
				case StatusTolerated:
					p.Note = fmt.Sprintf("差额 %s 在容差 %s 内，自动视为已核对", delta.Abs(), cfg.ToleranceFor(l.Currency))
				default:
					p.Note = fmt.Sprintf("金额不一致：我方 %s，对方 %s，差额 %s 超出容差 %s",
						l.Amount, r.Amount, delta.Abs(), cfg.ToleranceFor(l.Currency))
				}
			} else {
				// 时间窗口外：按"时间对不上"归类；金额也差太多时仍算金额问题。
				if p.Delta.Abs().Cmp(cfg.ToleranceFor(l.Currency)) > 0 {
					p.Diff = DiffAmountMismatch
					p.Status = StatusPending
					p.Note = fmt.Sprintf("金额不一致且时间超出窗口（时间差 %s）：我方 %s，对方 %s",
						durStr(d), l.Amount, r.Amount)
				} else {
					p.Diff = DiffTimeOutOfWindow
					p.Status = StatusPending
					p.Note = fmt.Sprintf("金额币种一致但时间超出窗口：时间差 %s，窗口 %s",
						durStr(d), cfg.TimeWindow)
				}
			}
			pairs = append(pairs, p)
		}
	}

	// 剩余流水：重复入账 or 某侧缺失。
	for _, gk := range keys {
		g := groups[gk]
		for _, li := range g.locals {
			if usedLocal[li] {
				continue
			}
			l := locals[li]
			p := Pair{LocalID: l.ID, Delta: l.Amount}
			if localPaidAmt[gk][l.Amount.Scaled()] {
				p.Diff, p.Status = DiffDuplicate, StatusDuplicate
				p.Note = "我方重复入账：同业务同金额的流水已有一笔完成配对，本笔为多余项"
			} else {
				p.Diff, p.Status = DiffRemoteMissing, StatusPending
				p.Note = "我方有、对方没有：对方对账单缺少该笔"
			}
			pairs = append(pairs, p)
		}
		for _, ri := range g.remotes {
			if usedRemote[ri] {
				continue
			}
			r := remotes[ri]
			p := Pair{RemoteID: r.ID, Delta: r.Amount.Neg()}
			if remotePaidAmt[gk][r.Amount.Scaled()] {
				p.Diff, p.Status = DiffDuplicate, StatusDuplicate
				p.Note = "对方重复入账：同业务同金额的流水已有一笔完成配对，本笔为多余项"
			} else {
				p.Diff, p.Status = DiffLocalMissing, StatusPending
				p.Note = "对方有、我方没有：我方账本缺少该笔"
			}
			pairs = append(pairs, p)
		}
	}

	// 输出顺序确定：先我方 ID 再对方 ID。
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].LocalID != pairs[j].LocalID {
			return pairs[i].LocalID < pairs[j].LocalID
		}
		return pairs[i].RemoteID < pairs[j].RemoteID
	})

	return MatchResult{Pairs: pairs, Entries: append(append([]Entry(nil), locals...), remotes...)}
}

type bucket struct {
	key     string
	locals  []int // 指向排序后切片的下标
	remotes []int
}

func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].Time.Equal(es[j].Time) {
			return es[i].Time.Before(es[j].Time)
		}
		return es[i].ID < es[j].ID
	})
}

// pickCandidate 为一笔我方流水挑选最佳未占用的对方候选。
// 先在时间窗口内挑（金额差最小 -> 时间距离最近 -> ID 最小），
// 窗口内没有时退而选窗口外最近的同业务候选（用于时间差分类）。
// 返回候选下标；第二个返回值表示是否在时间窗口内。
func pickCandidate(l Entry, remoteIdxs []int, remotes []Entry, used []bool, cfg Config) (int, bool) {
	bestIn, bestOut := -1, -1
	var scoreIn, scoreOut candidateScore
	for _, ri := range remoteIdxs {
		if used[ri] {
			continue
		}
		r := remotes[ri]
		if r.Currency != l.Currency {
			continue
		}
		s := candidateScore{
			absDelta: l.Amount.Sub(r.Amount).Abs().Scaled(),
			timeDist: absDuration(l.Time.Sub(r.Time)),
			id:       r.ID,
		}
		if withinWindow(l, r, cfg) {
			if bestIn < 0 || s.less(scoreIn) {
				bestIn, scoreIn = ri, s
			}
		} else {
			if bestOut < 0 || s.less(scoreOut) {
				bestOut, scoreOut = ri, s
			}
		}
	}
	if bestIn >= 0 {
		return bestIn, true
	}
	return bestOut, false
}

type candidateScore struct {
	absDelta int64
	timeDist time.Duration
	id       string
}

func (a candidateScore) less(b candidateScore) bool {
	if a.absDelta != b.absDelta {
		return a.absDelta < b.absDelta
	}
	if a.timeDist != b.timeDist {
		return a.timeDist < b.timeDist
	}
	return a.id < b.id
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func durStr(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	return d.String()
}
