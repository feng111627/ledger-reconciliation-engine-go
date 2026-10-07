package recon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/money"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/store"
)

// storeOpen 包内测试用的存储打开辅助。
func storeOpen(dir string) (*store.Store, func(), error) {
	return store.Open(dir)
}

func testCfg() domain.Config {
	return domain.Config{
		TimeWindow:      2 * time.Minute,
		AmountTolerance: mustMoney("0.01"),
		ExtendedWindow:  30 * time.Minute,
	}
}

func mustMoney(s string) int64 {
	v, err := money.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// writePairFiles 写入一对包含各种情形的 CSV，返回两个文件路径。
// 情形：1 笔完全匹配、1 笔容差内、1 笔金额差异、1 笔时间差异、
// 我方独有 1 笔、对方独有 1 笔、我方重复 1 笔。
func writePairFiles(t *testing.T, dir string) (string, string) {
	t.Helper()
	ledger := `id,amount,currency,time,reference
L1,100.00,USD,2026-10-01T10:00:00Z,
L2,200.00,USD,2026-10-01T10:05:00Z,
L3,50.00,USD,2026-10-01T10:10:00Z,
L4,80.00,USD,2026-10-01T10:15:00Z,
L9,42.00,USD,2026-10-01T10:20:00Z,
L9,42.00,USD,2026-10-01T10:20:00Z,
`
	statement := `id,amount,currency,time,reference
S1,100.00,USD,2026-10-01T10:00:30Z,
S2,200.005,USD,2026-10-01T10:05:00Z,
S3,50.00,USD,2026-10-01T10:25:00Z,
S8,77.00,USD,2026-10-01T13:00:00Z,
S9,42.00,USD,2026-10-01T10:20:00Z,
`
	lp := filepath.Join(dir, "ledger.csv")
	sp := filepath.Join(dir, "statement.csv")
	if err := os.WriteFile(lp, []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte(statement), 0o644); err != nil {
		t.Fatal(err)
	}
	return lp, sp
}

func openEngine(t *testing.T, dir string) (*Engine, func()) {
	t.Helper()
	st, cleanup, err := storeOpen(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	return New(st, testCfg()), cleanup
}

// 业务一：端到端主流程 + 资金守恒。
func TestEngine_EndToEndAndConservation(t *testing.T) {
	dir := t.TempDir()
	lp, sp := writePairFiles(t, dir)
	eng, cleanup := openEngine(t, dir)
	defer cleanup()

	r, cached, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("首次运行不应命中缓存")
	}
	// L1/S1 完全匹配、L9(首条)/S9 同额同时间也完全匹配；
	// L2 差 0.005 在 0.01 容差内 => WITHIN_TOL；L3/S3 相差 15 分钟 => TIME_MISMATCH；
	// L4 我方独有；S8 对方独有；L9 第二条 => DUPLICATE。
	counts := map[domain.DiffType]int{}
	for _, p := range r.Pairs {
		counts[p.Diff]++
	}
	if counts[domain.DiffMatched] != 2 {
		t.Fatalf("期望 2 MATCHED，得到 %+v", counts)
	}
	if counts[domain.DiffWithinTol] != 1 {
		t.Fatalf("期望 1 WITHIN_TOLERANCE，得到 %+v", counts)
	}
	if counts[domain.DiffTimeMismatch] != 1 {
		t.Fatalf("期望 1 TIME_MISMATCH，得到 %+v", counts)
	}
	dc := map[domain.DiffType]int{}
	for _, d := range r.Discrepancies {
		dc[d.Type]++
	}
	// AMOUNT_MISMATCH(L2 0.005 超出? 不——0.005 在 0.01 容差内！) -> 调整预期
	// L2 差 0.005 在容差内 => WITHIN_TOL；时间差异 L3/S3 进队列；
	// L4 LEDGER_ONLY；S8 STATEMENT_ONLY；L9 重复一条 => DUPLICATE。
	if dc[domain.DiffTimeMismatch] != 1 || dc[domain.DiffLedgerOnly] != 1 ||
		dc[domain.DiffStatementOnly] != 1 || dc[domain.DiffDuplicate] != 1 {
		t.Fatalf("差异分类数量不符: %+v", dc)
	}
	if len(r.Duplicates) != 1 {
		t.Fatalf("期望 1 条重复，得到 %d", len(r.Duplicates))
	}

	// 守恒恒等式必须成立。
	if err := VerifyConservation(r); err != nil {
		t.Fatalf("资金守恒不成立: %v", err)
	}
	if !r.ConservationOK {
		t.Fatal("ConservationOK 标志应为 true")
	}
	tot := r.CurrencyTotals["USD"]
	// 我方合计 = 100+200+50+80+42+42 = 514；对方 = 100+200.005+50+77+42 = 469.005
	if tot.LedgerTotal != mustMoney("514.00") {
		t.Fatalf("我方总额错误: %d", tot.LedgerTotal)
	}
	if tot.StatementTotal != mustMoney("469.005") {
		t.Fatalf("对方总额错误: %d", tot.StatementTotal)
	}
	// 残差 -44.995 = MatchedDelta(+0.005) + 未决(0 -80 +77 -42) = 0.005 -45 = -44.995
	if tot.Residual != mustMoney("-44.995") {
		t.Fatalf("残差错误: %d", tot.Residual)
	}
	if tot.OpenDelta != mustMoney("-45.00") || tot.MatchedDelta != mustMoney("0.005") {
		t.Fatalf("去向拆分错误: matched=%d open=%d", tot.MatchedDelta, tot.OpenDelta)
	}
	t.Logf("[业务:匹配主流程/守恒] 用例:端到端各差异类型+资金守恒 残差=%s=配对%s+未决%s ... PASS",
		money.Format(tot.Residual), money.Format(tot.MatchedDelta), money.Format(tot.OpenDelta))
}

// 业务二：幂等重跑 —— 相同输入重复跑，RunID/指纹/结论稳定，不重复计数。
func TestEngine_IdempotentRerun(t *testing.T) {
	dir := t.TempDir()
	lp, sp := writePairFiles(t, dir)
	eng, cleanup := openEngine(t, dir)
	defer cleanup()

	first, _, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	second, cached, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Fatal("第二次运行必须命中幂等缓存")
	}
	if first.RunID != second.RunID || first.InputFingerprint != second.InputFingerprint {
		t.Fatalf("重跑结论不稳定: %s/%s vs %s/%s",
			first.RunID, first.InputFingerprint, second.RunID, second.InputFingerprint)
	}
	if len(second.Pairs) != len(first.Pairs) || len(second.Discrepancies) != len(first.Discrepancies) {
		t.Fatal("重跑后配对/差异数量发生变化，疑似重复计数")
	}
	// runs 目录下只应有一个结果文件，证明没有重复落库。
	files, _ := os.ReadDir(filepath.Join(dir, "store", "runs"))
	if len(files) != 1 {
		t.Fatalf("期望 1 个持久化运行文件，得到 %d", len(files))
	}
	t.Log("[业务:幂等重跑] 用例:相同输入重跑返回同一RunID且只持久化一次 ... PASS")

	// 文件顺序打乱后重跑：指纹仍应一致（指纹按来源+ID 归一）。
	shuffled := `id,amount,currency,time,reference
L9,42.00,USD,2026-10-01T10:20:00Z,
L9,42.00,USD,2026-10-01T10:20:00Z,
L4,80.00,USD,2026-10-01T10:15:00Z,
L3,50.00,USD,2026-10-01T10:10:00Z,
L2,200.00,USD,2026-10-01T10:05:00Z,
L1,100.00,USD,2026-10-01T10:00:00Z,
`
	if err := os.WriteFile(lp, []byte(shuffled), 0o644); err != nil {
		t.Fatal(err)
	}
	third, cached, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	if !cached || third.RunID != first.RunID {
		t.Fatalf("打乱文件行序后指纹应保持一致，cached=%v runID=%s vs %s", cached, third.RunID, first.RunID)
	}
	t.Log("[业务:幂等重跑] 用例:行序打乱后指纹不变仍命中缓存 ... PASS")
}

// 业务三：人工处理结论持久化，重跑不覆盖，且资金去向从未决迁移到已处理。
func TestEngine_ResolutionPersistsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	lp, sp := writePairFiles(t, dir)
	eng, cleanup := openEngine(t, dir)
	defer cleanup()

	r, _, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	var target string
	var delta int64
	for _, d := range r.Discrepancies {
		if d.Type == domain.DiffLedgerOnly {
			target = d.Key
			delta = d.Delta
		}
	}
	if target == "" {
		t.Fatal("找不到 LEDGER_ONLY 差异用于处理")
	}
	if err := eng.Resolve(target, domain.StatusAccepted, "人工确认漏记"); err != nil {
		t.Fatal(err)
	}

	// 重跑（命中缓存）：结论必须保留，OpenDelta 减少、ResolvedDelta 增加，守恒仍成立。
	r2, cached, err := eng.Run(context.Background(), lp, sp)
	if err != nil || !cached {
		t.Fatalf("重跑异常 cached=%v err=%v", cached, err)
	}
	if err := VerifyConservation(r2); err != nil {
		t.Fatalf("处理差异后守恒不成立: %v", err)
	}
	before, after := r.CurrencyTotals["USD"], r2.CurrencyTotals["USD"]
	if after.OpenDelta != before.OpenDelta-delta || after.ResolvedDelta != delta {
		t.Fatalf("资金去向迁移错误: before(open=%d) after(open=%d resolved=%d) delta=%d",
			before.OpenDelta, after.OpenDelta, after.ResolvedDelta, delta)
	}
	rs, err := eng.Resolutions()
	if err != nil || len(rs) != 1 || rs[0].Key != target || rs[0].Status != domain.StatusAccepted {
		t.Fatalf("人工结论未持久化: %+v err=%v", rs, err)
	}
	t.Log("[业务:差异处理/可解释] 用例:处理后重跑结论保留且资金从未决迁移到已处理 ... PASS")

	// 对同一个 Key 再处理一次（改备注）：更新而不是产生重复结论。
	if err := eng.Resolve(target, domain.StatusWriteOff, "改判核销"); err != nil {
		t.Fatal(err)
	}
	rs, _ = eng.Resolutions()
	if len(rs) != 1 || rs[0].Status != domain.StatusWriteOff || rs[0].Note != "改判核销" {
		t.Fatalf("同 Key 重复处理应幂等更新: %+v", rs)
	}
	t.Log("[业务:幂等重跑] 用例:同一差异重复处理幂等更新不产生重复 ... PASS")
}

// 业务四：并发/多进程 —— 文件锁串行化，第二个 Open 阻塞等待，不会出现竞态写。
func TestStore_FileLockSerializesProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	st1, cleanup1, err := storeOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup1()

	acquired := make(chan struct{})
	go func() {
		// 同目录再开一个 Store：LOCK_EX 会阻塞直到第一个释放。
		_, cleanup2, err := storeOpen(dir)
		if err != nil {
			t.Errorf("第二个 Open 出错: %v", err)
			return
		}
		defer cleanup2()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("第一个持有者未释放时，第二个 Open 不应获得锁")
	case <-time.After(300 * time.Millisecond):
		// 预期：仍在阻塞等待。
	}
	cleanup1()
	_ = st1
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("释放锁后第二个 Open 应当获得锁")
	}
	t.Log("[业务:并发安全] 用例:文件锁互斥,释放后等待者获得锁 ... PASS")
}

// 业务五：同进程并发调用不会造成重复运行文件或数据竞争（-race 下验证）。
func TestEngine_ConcurrentRuns(t *testing.T) {
	dir := t.TempDir()
	lp, sp := writePairFiles(t, dir)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 每个 goroutine 独立打开存储（文件锁保证串行化）。
			st, cleanup, err := storeOpen(filepath.Join(dir, "store"))
			if err != nil {
				errs <- err
				return
			}
			defer cleanup()
			eng := New(st, testCfg())
			r, _, err := eng.Run(context.Background(), lp, sp)
			if err != nil {
				errs <- err
				return
			}
			if err := VerifyConservation(r); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(filepath.Join(dir, "store", "runs"))
	if len(files) != 1 {
		t.Fatalf("8 个并发运行后只应有 1 个结果文件，得到 %d", len(files))
	}
	t.Log("[业务:并发安全] 用例:8 并发运行串行化,结果唯一且守恒 ... PASS")
}

// 业务六：输入变化产生新指纹（不同数据不会复用旧结论）。
func TestEngine_DifferentInputNewFingerprint(t *testing.T) {
	dir := t.TempDir()
	lp, sp := writePairFiles(t, dir)
	eng, cleanup := openEngine(t, dir)
	defer cleanup()

	r1, _, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	// 改一笔金额，指纹/RunID 必须变化且不是缓存。
	orig, _ := os.ReadFile(lp)
	modified := string(orig)
	modified = replaceFirst(modified, "L4,80.00", "L4,81.00")
	if err := os.WriteFile(lp, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}
	r2, cached, err := eng.Run(context.Background(), lp, sp)
	if err != nil {
		t.Fatal(err)
	}
	if cached || r2.RunID == r1.RunID {
		t.Fatal("输入变化后必须产生新指纹与新运行，不能复用旧结论")
	}
	if err := VerifyConservation(r2); err != nil {
		t.Fatalf("新运行守恒不成立: %v", err)
	}
	_ = fmt.Sprint("")
	t.Log("[业务:幂等重跑] 用例:输入变化产生新指纹,旧结论不被复用 ... PASS")
}

func replaceFirst(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
