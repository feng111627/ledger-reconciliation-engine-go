package recon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/matcher"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/reader"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/store"
)

// Engine 对账编排：读取 -> 指纹 -> 幂等检查 -> 匹配 -> 守恒校验 -> 持久化。
type Engine struct {
	store *store.Store
	cfg   domain.Config
	now   func() time.Time
}

func New(st *store.Store, cfg domain.Config) *Engine {
	return &Engine{store: st, cfg: cfg, now: time.Now}
}

// Run 执行一次对账。
// 相同输入（两个文件内容 + 配置）重跑时直接返回上一次的稳定结果（cached=true），
// 不重新计数、不产生新 RunID；人工结论按差异稳定 Key 单独存储，永不被自动流程覆盖。
// 注意：差异 Key 完全由输入决定，因此即使命中缓存，资金汇总也会结合当前最新的
// 人工处理结论重新计算，报告里"未决/已处理"的去向始终与实际操作一致。
func (e *Engine) Run(ctx context.Context, ledgerPath, statementPath string) (result *domain.RunResult, cached bool, err error) {
	ledger, err := reader.ReadCSV(ctx, ledgerPath, domain.SourceLedger)
	if err != nil {
		return nil, false, err
	}
	statement, err := reader.ReadCSV(ctx, statementPath, domain.SourceStatement)
	if err != nil {
		return nil, false, err
	}

	fp := fingerprint(ledger, statement, e.cfg)

	existing, err := e.store.LoadRun(fp)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		// 幂等命中：复用同一份配对/差异结论，只按当前人工结论刷新资金去向。
		e.refreshTotals(existing, ledger, statement)
		return existing, true, nil
	}

	pairs, discrepancies, unpaired, duplicates := matcher.Reconcile(ledger, statement, e.cfg)
	result = &domain.RunResult{
		RunID:            "run-" + fp[:12],
		InputFingerprint: fp,
		Config:           e.cfg,
		Pairs:            pairs,
		Discrepancies:    discrepancies,
		Unpaired:         unpaired,
		Duplicates:       duplicates,
	}
	e.refreshTotals(result, ledger, statement)

	if _, err := e.store.SaveRunResult(fp, result); err != nil {
		return nil, false, err
	}
	return result, false, nil
}

// refreshTotals 结合当前所有人工结论，重新计算每个币种的资金去向与守恒结果。
func (e *Engine) refreshTotals(r *domain.RunResult, ledger, statement []domain.Entry) {
	resolved := map[string]domain.Resolution{}
	if rs, err := e.store.ListResolutions(); err == nil {
		for _, res := range rs {
			resolved[res.Key] = res
		}
	}
	r.CurrencyTotals = computeTotals(ledger, statement, r.Pairs, r.Discrepancies, resolved)
	ok := true
	for _, t := range r.CurrencyTotals {
		if t.Residual != t.MatchedDelta+t.OpenDelta+t.ResolvedDelta {
			ok = false
		}
	}
	r.ConservationOK = ok
}

// Resolve 对差异队列中的一条记录写入人工处理结论（按稳定 Key 幂等更新）。
func (e *Engine) Resolve(key string, status domain.ResolveStatus, note string) error {
	switch status {
	case domain.StatusAccepted, domain.StatusWriteOff:
	default:
		return fmt.Errorf("unknown resolve status %q", status)
	}
	return e.store.PutResolution(domain.Resolution{
		Key:       key,
		Status:    status,
		Note:      note,
		UpdatedAt: e.now().UTC(),
	})
}

// Resolutions 返回全部人工处理结论（按 Key 排序）。
func (e *Engine) Resolutions() ([]domain.Resolution, error) {
	return e.store.ListResolutions()
}

// ErrConservation 守恒校验失败时返回，附带币种明细。
var ErrConservation = errors.New("conservation check failed")

// VerifyConservation 显式校验资金守恒恒等式：
//
//	StatementTotal - LedgerTotal == MatchedDelta + OpenDelta + ResolvedDelta
//
// 即两个来源的总差额，必须能被"已配对差额 + 未决差异 + 已处理差异"完整解释，
// 每一笔流水的去向都有着落。
func VerifyConservation(r *domain.RunResult) error {
	for cur, t := range r.CurrencyTotals {
		if t.Residual != t.MatchedDelta+t.OpenDelta+t.ResolvedDelta {
			return fmt.Errorf("%w: currency %s: residual %d != matched %d + open %d + resolved %d",
				ErrConservation, cur, t.Residual, t.MatchedDelta, t.OpenDelta, t.ResolvedDelta)
		}
	}
	return nil
}

// computeTotals 按币种汇总来源总额（基于两侧全部流水，含未配对与重复记录）与去向：
//   - LedgerTotal/StatementTotal：两侧全部流水金额之和；
//   - MatchedDelta：所有配对（含容差内自动核对）的 Statement-Ledger 差额之和；
//   - OpenDelta：差异队列中尚未人工处理的差额之和；
//   - ResolvedDelta：已人工处理（接受/核销）的差异差额之和；
//   - Residual：StatementTotal - LedgerTotal。
func computeTotals(ledger, statement []domain.Entry, pairs []domain.Pair, discrepancies []domain.Discrepancy, resolved map[string]domain.Resolution) map[string]domain.CurrencyTotal {
	totals := map[string]domain.CurrencyTotal{}

	add := func(cur string, dl, ds, md, od, rd int64) {
		t := totals[cur]
		t.LedgerTotal += dl
		t.StatementTotal += ds
		t.MatchedDelta += md
		t.OpenDelta += od
		t.ResolvedDelta += rd
		totals[cur] = t
	}
	// 来源总额：每条流水都计入，无一遗漏。
	for _, e := range ledger {
		add(e.Currency, e.Amount, 0, 0, 0, 0)
	}
	for _, e := range statement {
		add(e.Currency, 0, e.Amount, 0, 0, 0)
	}
	// 配对差额：只有已自动核对的配对（MATCHED/WITHIN_TOLERANCE）计 MatchedDelta；
	// 需要人工处理的配对（AMOUNT/TIME_MISMATCH）差额走差异队列，避免重复计数。
	for _, p := range pairs {
		if p.Diff == domain.DiffMatched || p.Diff == domain.DiffWithinTol {
			add(p.LedgerEntry.Currency, 0, 0, p.AmountDelta, 0, 0)
		}
	}
	// 差异去向：已处理入 ResolvedDelta，未决入 OpenDelta。
	for _, d := range discrepancies {
		if _, done := resolved[d.Key]; done {
			add(d.Currency, 0, 0, 0, 0, d.Delta)
		} else {
			add(d.Currency, 0, 0, 0, d.Delta, 0)
		}
	}
	out := make(map[string]domain.CurrencyTotal, len(totals))
	for cur, t := range totals {
		t.Residual = t.StatementTotal - t.LedgerTotal
		out[cur] = t
	}
	return out
}

// fingerprint 对规范化后的两侧流水与配置计算 SHA-256。
// 相同输入（与文件顺序无关，按来源+ID 归一）永远得到相同哈希。
func fingerprint(ledger, statement []domain.Entry, cfg domain.Config) string {
	canon := func(entries []domain.Entry) []map[string]any {
		out := make([]map[string]any, len(entries))
		for i, e := range entries {
			out[i] = map[string]any{
				"id":        e.ID,
				"source":    string(e.Source),
				"amount":    e.Amount,
				"currency":  e.Currency,
				"time":      e.Time.UTC().Format(time.RFC3339Nano),
				"reference": e.Reference,
				// RawLine（源文件行号）只是排查信息，不进入指纹；
				// 同样的业务内容调换行序后指纹必须保持一致。
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i]["source"] != out[j]["source"] {
				return out[i]["source"].(string) < out[j]["source"].(string)
			}
			return out[i]["id"].(string) < out[j]["id"].(string)
		})
		return out
	}
	payload := struct {
		Ledger    []map[string]any `json:"ledger"`
		Statement []map[string]any `json:"statement"`
		Config    struct {
			TimeWindowSecs  int64 `json:"time_window_secs"`
			AmountTolerance int64 `json:"amount_tolerance"`
			ExtendedSecs    int64 `json:"extended_window_secs"`
		} `json:"config"`
	}{
		Ledger:    canon(ledger),
		Statement: canon(statement),
	}
	payload.Config.TimeWindowSecs = int64(cfg.TimeWindow / time.Second)
	payload.Config.AmountTolerance = cfg.AmountTolerance
	payload.Config.ExtendedSecs = int64(cfg.ExtendedWindow / time.Second)

	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
