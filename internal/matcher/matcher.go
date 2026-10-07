package matcher

import (
	"fmt"
	"sort"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/money"
)

// side 把一侧的流水连同"是否被判重"标记放在一起，索引顺序即确定性顺序。
type side struct {
	entries []domain.Entry
	dup     []bool
}

// Reconcile 是无状态的纯匹配逻辑：读入两侧流水与配置，
// 输出确定性的配对、差异、未配对与重复列表。
//
// 确定性保证：不依赖 map 遍历顺序做决策，所有选择都按
// (时间差, 金额差, 时间, 流水号) 的全序比较进行，同一份输入永远得到同一份输出。
func Reconcile(ledger, statement []domain.Entry, cfg domain.Config) (pairs []domain.Pair, discrepancies []domain.Discrepancy, unpaired []domain.Entry, duplicates []domain.Entry) {
	L := prepareSide(ledger)
	S := prepareSide(statement)

	duplicates = collectDuplicates(L, S)

	usedL := make([]bool, len(L.entries))
	usedS := make([]bool, len(S.entries))
	// 被判重的记录不参与匹配，保证一条流水不会被配两次。
	for i, d := range L.dup {
		usedL[i] = d
	}
	for i, d := range S.dup {
		usedS[i] = d
	}

	pairs = matchByReference(L, S, usedL, usedS, cfg)
	pairs = append(pairs, matchGreedy(L, S, usedL, usedS, cfg)...)

	// 未配对流水：逐条落单边差异，任何一条都不会被悄悄丢掉。
	for i, e := range L.entries {
		if usedL[i] {
			continue
		}
		unpaired = append(unpaired, e)
		discrepancies = append(discrepancies, domain.Discrepancy{
			Key:      "LONLY:" + e.ID,
			Type:     domain.DiffLedgerOnly,
			LedgerID: e.ID,
			Amount:   e.Amount,
			Currency: e.Currency,
			Delta:    -e.Amount, // Statement(0) - Ledger
			Detail:   fmt.Sprintf("我方流水 %s 在对方对账单中不存在（%s %s, %s）", e.ID, e.Currency, money.Format(e.Amount), e.Time.Format(time.RFC3339)),
		})
	}
	for i, e := range S.entries {
		if usedS[i] {
			continue
		}
		unpaired = append(unpaired, e)
		discrepancies = append(discrepancies, domain.Discrepancy{
			Key:         "SONLY:" + e.ID,
			Type:        domain.DiffStatementOnly,
			StatementID: e.ID,
			Amount:      e.Amount,
			Currency:    e.Currency,
			Delta:       e.Amount, // Statement - Ledger(0)
			Detail:      fmt.Sprintf("对方流水 %s 在我方账本中不存在（%s %s, %s）", e.ID, e.Currency, money.Format(e.Amount), e.Time.Format(time.RFC3339)),
		})
	}

	// 重复入账差异。
	for i, e := range L.entries {
		if !L.dup[i] {
			continue
		}
		discrepancies = append(discrepancies, domain.Discrepancy{
			Key:      "DUP:LEDGER:" + e.ID,
			Type:     domain.DiffDuplicate,
			LedgerID: e.ID,
			Amount:   e.Amount,
			Currency: e.Currency,
			Delta:    -e.Amount, // 我方多记一笔，方向与 LEDGER_ONLY 相同
			Detail:   fmt.Sprintf("我方账本流水 %s 疑似重复入账（%s %s, %s）", e.ID, e.Currency, money.Format(e.Amount), e.Time.Format(time.RFC3339)),
		})
	}
	for i, e := range S.entries {
		if !S.dup[i] {
			continue
		}
		discrepancies = append(discrepancies, domain.Discrepancy{
			Key:         "DUP:STATEMENT:" + e.ID,
			Type:        domain.DiffDuplicate,
			StatementID: e.ID,
			Amount:      e.Amount,
			Currency:    e.Currency,
			Delta:       e.Amount, // 对方多记一笔
			Detail:      fmt.Sprintf("对方对账单流水 %s 疑似重复入账（%s %s, %s）", e.ID, e.Currency, money.Format(e.Amount), e.Time.Format(time.RFC3339)),
		})
	}

	for _, p := range pairs {
		if p.Diff == domain.DiffMatched || p.Diff == domain.DiffWithinTol {
			continue
		}
		discrepancies = append(discrepancies, pairDiscrepancy(p))
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].LedgerEntry.Time.Equal(pairs[j].LedgerEntry.Time) {
			return pairs[i].LedgerEntry.ID < pairs[j].LedgerEntry.ID
		}
		return pairs[i].LedgerEntry.Time.Before(pairs[j].LedgerEntry.Time)
	})
	sort.Slice(unpaired, func(i, j int) bool { return entryLess(unpaired[i], unpaired[j]) })
	sort.Slice(duplicates, func(i, j int) bool { return entryLess(duplicates[i], duplicates[j]) })
	sort.Slice(discrepancies, func(i, j int) bool { return discrepancies[i].Key < discrepancies[j].Key })
	return pairs, discrepancies, unpaired, duplicates
}

// prepareSide 按 (时间, 流水号, 行号) 的全序排序一侧流水，
// 使"第一条/下一条"的判断完全确定，与 map 遍历顺序无关。
func prepareSide(entries []domain.Entry) *side {
	cp := make([]domain.Entry, len(entries))
	copy(cp, entries)
	sort.Slice(cp, func(i, j int) bool { return entryLess(cp[i], cp[j]) })
	return &side{entries: cp, dup: make([]bool, len(cp))}
}

func entryLess(a, b domain.Entry) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.Before(b.Time)
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return a.RawLine < b.RawLine
}

// collectDuplicates 标记同一侧内的重复入账记录：
//  1. 同一流水号出现多次；
//  2. (金额, 币种, 时间, 业务参考号) 完全相同的多条记录。
//
// 排序后的第一条保留并参与匹配，其余全部标记为 DUPLICATE。
func collectDuplicates(L, S *side) []domain.Entry {
	var dups []domain.Entry
	mark := func(sd *side) {
		byID := map[string]int{}
		byBiz := map[string]int{}
		for i, e := range sd.entries {
			idKey := string(e.Source) + "|" + e.ID
			if _, ok := byID[idKey]; ok {
				if !sd.dup[i] {
					sd.dup[i] = true
					dups = append(dups, e)
				}
			} else {
				byID[idKey] = i
			}
			bizKey := fmt.Sprintf("%s|%s|%d|%d|%s", e.Source, e.Currency, e.Amount, e.Time.UnixNano(), e.Reference)
			if _, ok := byBiz[bizKey]; ok {
				if !sd.dup[i] {
					sd.dup[i] = true
					dups = append(dups, e)
				}
			} else {
				byBiz[bizKey] = i
			}
		}
	}
	mark(L)
	mark(S)
	sort.Slice(dups, func(i, j int) bool { return entryLess(dups[i], dups[j]) })
	return dups
}

// matchByReference 第一阶段：业务参考号(reference)非空且两侧相等，
// 视为同一笔业务凭证，优先一对一配对。参考号相同不受时间窗口限制。
func matchByReference(L, S *side, usedL, usedS []bool, cfg domain.Config) []domain.Pair {
	groupsL := map[string][]int{}
	groupsS := map[string][]int{}
	for i, e := range L.entries {
		if !usedL[i] && e.Reference != "" {
			groupsL[e.Currency+"|"+e.Reference] = append(groupsL[e.Currency+"|"+e.Reference], i)
		}
	}
	for i, e := range S.entries {
		if !usedS[i] && e.Reference != "" {
			groupsS[e.Currency+"|"+e.Reference] = append(groupsS[e.Currency+"|"+e.Reference], i)
		}
	}
	// 收集所有共同 key 后排序，保证处理顺序确定。
	keys := make([]string, 0)
	for k := range groupsL {
		if _, ok := groupsS[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var pairs []domain.Pair
	for _, k := range keys {
		li, si := groupsL[k], groupsS[k]
		type cand struct{ li, si int }
		var cands []cand
		for _, a := range li {
			for _, b := range si {
				cands = append(cands, cand{a, b})
			}
		}
		// 参考号相同的候选按 (时间差, 金额差, L时间, L号, S号) 排序后贪心选取。
		sort.Slice(cands, func(x, y int) bool {
			return candLess(L.entries[cands[x].li], S.entries[cands[x].si], L.entries[cands[y].li], S.entries[cands[y].si])
		})
		for _, c := range cands {
			if usedL[c.li] || usedS[c.si] {
				continue
			}
			diff := classifyRef(L.entries[c.li], S.entries[c.si], cfg)
			pairs = append(pairs, makePair(L.entries[c.li], S.entries[c.si], diff))
			usedL[c.li] = true
			usedS[c.si] = true
		}
	}
	return pairs
}

// matchGreedy 第二阶段：按我方流水（已按时间排序）逐条在对方流水中选择
// 全序意义上"最接近"的一条配对。候选资格：
//   - 币种相同；
//   - 时间差不超过扩展窗口 ExtendedWindow；
//   - 时间与金额至少一个维度在容忍范围内（两个维度都对不上则不配对，
//     两侧各自落 LEDGER_ONLY / STATEMENT_ONLY）。
func matchGreedy(L, S *side, usedL, usedS []bool, cfg domain.Config) []domain.Pair {
	var pairs []domain.Pair
	for li, le := range L.entries {
		if usedL[li] {
			continue
		}
		best := -1
		for si, se := range S.entries {
			if usedS[si] || se.Currency != le.Currency {
				continue
			}
			gap := absDuration(se.Time.Sub(le.Time))
			delta := se.Amount - le.Amount
			if !admitted(gap, delta, cfg) {
				continue
			}
			if best == -1 || candLess(le, se, le, S.entries[best]) {
				best = si
			}
		}
		if best >= 0 {
			le2, se2 := L.entries[li], S.entries[best]
			diff, ok := classify(le2, se2, cfg)
			if !ok {
				continue
			}
			pairs = append(pairs, makePair(le2, se2, diff))
			usedL[li] = true
			usedS[best] = true
		}
	}
	return pairs
}

// admitted 判断非参考号候选是否允许配对：
// 时间差必须在扩展窗口内，且时间/金额至少一维满足容忍条件。
func admitted(gap time.Duration, delta int64, cfg domain.Config) bool {
	if gap > cfg.ExtendedWindow {
		return false
	}
	timeOK := gap <= cfg.TimeWindow
	amountOK := i64abs(delta) <= cfg.AmountTolerance
	return timeOK || amountOK
}

// classify 对普通候选分类；两个维度都超出时返回 false（不应配对）。
func classify(le, se domain.Entry, cfg domain.Config) (domain.DiffType, bool) {
	gap := absDuration(se.Time.Sub(le.Time))
	delta := se.Amount - le.Amount
	timeOK := gap <= cfg.TimeWindow
	amountOK := i64abs(delta) <= cfg.AmountTolerance
	switch {
	case timeOK && amountOK:
		if delta == 0 {
			return domain.DiffMatched, true
		}
		return domain.DiffWithinTol, true
	case timeOK && !amountOK:
		return domain.DiffAmountMismatch, true
	case !timeOK && amountOK:
		return domain.DiffTimeMismatch, true
	default:
		return "", false
	}
}

// classifyRef 参考号强关联候选的分类：凭证相同即视为同一笔业务，
// 两个维度都超出时以金额不符为准（钱数对不上是硬差异）。
func classifyRef(le, se domain.Entry, cfg domain.Config) domain.DiffType {
	gap := absDuration(se.Time.Sub(le.Time))
	delta := se.Amount - le.Amount
	timeOK := gap <= cfg.TimeWindow
	amountOK := i64abs(delta) <= cfg.AmountTolerance
	switch {
	case timeOK && amountOK:
		if delta == 0 {
			return domain.DiffMatched
		}
		return domain.DiffWithinTol
	case !amountOK:
		return domain.DiffAmountMismatch
	default:
		return domain.DiffTimeMismatch
	}
}

func makePair(le, se domain.Entry, diff domain.DiffType) domain.Pair {
	return domain.Pair{
		LedgerEntry:    le,
		StatementEntry: se,
		Diff:           diff,
		AmountDelta:    se.Amount - le.Amount,
		TimeGapSeconds: int64(absDuration(se.Time.Sub(le.Time)).Seconds()),
	}
}

func pairDiscrepancy(p domain.Pair) domain.Discrepancy {
	d := domain.Discrepancy{
		Key:         "PAIR:" + p.LedgerEntry.ID + "|" + p.StatementEntry.ID,
		Type:        p.Diff,
		LedgerID:    p.LedgerEntry.ID,
		StatementID: p.StatementEntry.ID,
		Amount:      p.LedgerEntry.Amount,
		Currency:    p.LedgerEntry.Currency,
		Delta:       p.AmountDelta,
	}
	switch p.Diff {
	case domain.DiffAmountMismatch:
		d.Detail = fmt.Sprintf("金额对不上：我方 %s = %s，对方 %s = %s，差额 %s",
			p.LedgerEntry.ID, money.Format(p.LedgerEntry.Amount),
			p.StatementEntry.ID, money.Format(p.StatementEntry.Amount),
			money.Format(p.AmountDelta))
	case domain.DiffTimeMismatch:
		d.Detail = fmt.Sprintf("时间差超窗：我方 %s @ %s，对方 %s @ %s，相差 %d 秒（金额一致）",
			p.LedgerEntry.ID, p.LedgerEntry.Time.Format(time.RFC3339),
			p.StatementEntry.ID, p.StatementEntry.Time.Format(time.RFC3339),
			p.TimeGapSeconds)
	}
	return d
}

// candLess 候选排序：(时间差, 金额差绝对值, 对方时间, 对方流水号, 我方流水号)。
func candLess(la, sb, lc, sd domain.Entry) bool {
	ga := absDuration(sb.Time.Sub(la.Time))
	gc := absDuration(sd.Time.Sub(lc.Time))
	if ga != gc {
		return ga < gc
	}
	da := i64abs(sb.Amount - la.Amount)
	dc := i64abs(sd.Amount - lc.Amount)
	if da != dc {
		return da < dc
	}
	if !sb.Time.Equal(sd.Time) {
		return sb.Time.Before(sd.Time)
	}
	if sb.ID != sd.ID {
		return sb.ID < sd.ID
	}
	return la.ID < lc.ID
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func i64abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
