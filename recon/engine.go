package recon

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// Engine 对账引擎：读取两个来源 -> 确定性匹配 -> 合并历史结论 -> 输出报告。
type Engine struct {
	cfg   Config
	store *Store
}

// NewEngine 创建引擎。
func NewEngine(cfg Config, store *Store) *Engine {
	return &Engine{cfg: cfg, store: store}
}

// Run 执行一次对账。
// 历史已处理结论优先（matched/tolerated/resolved 原样保留，不会被覆盖），
// 本次新出现的配对才写入结论库；同一批数据重复跑，报告结论完全一致、不重复计数。
func (e *Engine) Run(local, remote []Entry, runID string) (*Report, error) {
	if runID == "" {
		runID = deterministicRunID(local, remote, e.cfg)
	}

	fresh := Match(local, remote, e.cfg)

	// 取历史结论（store 可为 nil，用于无状态场景/单测）。
	stored := map[string]StoredPair{}
	if e.store != nil {
		sp, err := e.store.Load()
		if err != nil {
			return nil, err
		}
		for _, p := range sp {
			stored[pairKey(p.LocalID, p.RemoteID)] = p
		}
	}

	entryIdx := map[string]Entry{}
	for _, en := range append(append([]Entry(nil), local...), remote...) {
		entryIdx[en.ID] = en
	}

	pairs := make([]Pair, 0, len(fresh.Pairs))
	for _, fp := range fresh.Pairs {
		key := pairKey(fp.LocalID, fp.RemoteID)
		if sp, ok := stored[key]; ok {
			// 历史结论不可覆盖；金额差按当前数据重算，保证金额展示准确。
			fp.Diff = sp.Diff
			fp.Status = sp.Status
			fp.Note = sp.Note
			if t, err := time.Parse(time.RFC3339, sp.DecidedAt); err == nil {
				fp.DecidedAt = t
			}
			fp.Delta = freshDelta(fp.LocalID, fp.RemoteID, entryIdx, sp)
		} else {
			// 统一秒精度，保证首次运行与重跑从 JSON 恢复后逐字段一致。
			fp.DecidedAt = now().UTC().Truncate(time.Second)
			if e.store != nil {
				if _, err := e.store.PutIfAbsent(toStored(fp)); err != nil {
					return nil, err
				}
			}
		}
		pairs = append(pairs, fp)
	}

	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].LocalID != pairs[j].LocalID {
			return pairs[i].LocalID < pairs[j].LocalID
		}
		return pairs[i].RemoteID < pairs[j].RemoteID
	})

	rep := &Report{
		RunID:       runID,
		RanAt:       now().UTC(),
		Config:      e.cfg,
		Entries:     fresh.Entries,
		Pairs:       pairs,
		LocalTotal:  aggregateByCurrency(local),
		RemoteTotal: aggregateByCurrency(remote),
		Explained:   map[string]Money{},
		Pending:     map[string]Money{},
	}

	// 资金守恒：按币种统计已解释差额/待处理差额（均为有符号）。
	// 恒等式：我方总额 - 对方总额 == Σ 所有 Pair 的 Delta
	//       == 已解释差额 + 待处理差额。
	// 容差配对虽然自动核销，其小差额仍计入已解释差额，保证账平。
	for _, p := range pairs {
		cur := pairCurrency(p, entryIdx)
		switch p.Status {
		case StatusMatched, StatusTolerated, StatusResolved:
			rep.Explained[cur] = rep.Explained[cur].Add(p.Delta)
		default: // pending / duplicate
			rep.Pending[cur] = rep.Pending[cur].Add(p.Delta)
		}
	}

	// 守恒等式：我方总额 - 对方总额 == 已解释差额 + 待处理差额（逐币种）。
	rep.Conserved = true
	currencies := map[string]bool{}
	for c := range rep.LocalTotal {
		currencies[c] = true
	}
	for c := range rep.RemoteTotal {
		currencies[c] = true
	}
	for c := range currencies {
		got := rep.LocalTotal[c].Sub(rep.RemoteTotal[c])
		want := rep.Explained[c].Add(rep.Pending[c])
		if !got.Equal(want) {
			rep.Conserved = false
		}
	}

	return rep, nil
}

// Resolve 暴露人工处理入口。
func (e *Engine) Resolve(localID, remoteID, note string) error {
	return e.store.Resolve(localID, remoteID, note)
}

func freshDelta(localID, remoteID string, idx map[string]Entry, sp StoredPair) Money {
	l, ok1 := idx[localID]
	r, ok2 := idx[remoteID]
	switch {
	case ok1 && ok2:
		return l.Amount.Sub(r.Amount)
	case ok1:
		return l.Amount
	case ok2:
		return r.Amount.Neg()
	default:
		m, _ := ParseMoney(sp.Delta)
		return m
	}
}

func pairCurrency(p Pair, idx map[string]Entry) string {
	if e, ok := idx[p.LocalID]; ok {
		return e.Currency
	}
	if e, ok := idx[p.RemoteID]; ok {
		return e.Currency
	}
	return ""
}

func toStored(p Pair) StoredPair {
	return StoredPair{
		LocalID:   p.LocalID,
		RemoteID:  p.RemoteID,
		Diff:      p.Diff,
		Status:    p.Status,
		Delta:     p.Delta.String(),
		Note:      p.Note,
		DecidedAt: p.DecidedAt.UTC().Format(time.RFC3339),
	}
}

// aggregateByCurrency 按币种汇总金额。
func aggregateByCurrency(entries []Entry) map[string]Money {
	out := map[string]Money{}
	for _, e := range entries {
		out[e.Currency] = out[e.Currency].Add(e.Amount)
	}
	return out
}

// deterministicRunID 由输入数据与配置派生 run id，
// 同一批数据重复跑得到相同标识。
func deterministicRunID(local, remote []Entry, cfg Config) string {
	h := sha256.New()
	writeEntries := func(es []Entry) {
		sorted := append([]Entry(nil), es...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Side != sorted[j].Side {
				return sorted[i].Side < sorted[j].Side
			}
			return sorted[i].ID < sorted[j].ID
		})
		for _, e := range sorted {
			h.Write([]byte(string(e.Side) + "|" + e.ID + "|" + e.Currency + "|" +
				e.Amount.String() + "|" + e.Time.UTC().Format(time.RFC3339) + "|" + e.Ref + "\n"))
		}
	}
	writeEntries(local)
	writeEntries(remote)
	h.Write([]byte(cfg.TimeWindow.String()))
	return "run-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// now 便于测试注入。
var now = time.Now
