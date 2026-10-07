package matcher

import (
	"testing"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/money"
)

// 测试日志辅助：打印业务场景与每个用例的通过情况，go test -v 时可直接看出覆盖了哪些业务。
func logCase(t *testing.T, business, name string) {
	t.Helper()
	t.Logf("[业务:%s] 用例:%s ... PASS", business, name)
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

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func ledgerEntry(id string, amt string, cur string, ts string, ref string) domain.Entry {
	return domain.Entry{ID: id, Source: domain.SourceLedger, Amount: mustMoney(amt), Currency: cur, Time: at(ts), Reference: ref}
}
func stmtEntry(id string, amt string, cur string, ts string, ref string) domain.Entry {
	return domain.Entry{ID: id, Source: domain.SourceStatement, Amount: mustMoney(amt), Currency: cur, Time: at(ts), Reference: ref}
}

// diffCount 统计差异队列里各类型的数量。
func diffCount(ds []domain.Discrepancy) map[domain.DiffType]int {
	m := map[domain.DiffType]int{}
	for _, d := range ds {
		m[d.Type]++
	}
	return m
}

func pairTypes(ps []domain.Pair) map[domain.DiffType]int {
	m := map[domain.DiffType]int{}
	for _, p := range ps {
		m[p.Diff]++
	}
	return m
}

// 业务一：匹配主流程 —— 完全匹配、跨批次但在时间窗口内归位、参考号跨窗口归位。
func TestReconcile_MatchingMainFlow(t *testing.T) {
	cfg := testCfg()

	t.Run("金额币种时间完全一致则 MATCHED", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "100.00", "USD", "2026-10-01T10:00:30Z", "")}
		ps, ds, up, dups := Reconcile(L, S, cfg)
		if len(ps) != 1 || ps[0].Diff != domain.DiffMatched {
			t.Fatalf("期望 1 个 MATCHED，得到 %+v", pairTypes(ps))
		}
		if len(ds) != 0 || len(up) != 0 || len(dups) != 0 {
			t.Fatalf("期望无差异/未配对/重复，得到 ds=%d up=%d dups=%d", len(ds), len(up), len(dups))
		}
		logCase(t, "匹配主流程", "完全匹配")
	})

	t.Run("跨批次但时间差在窗口内的流水正确归位", func(t *testing.T) {
		// 两笔同金额流水，对方都晚到 90 秒（跨批次），必须按时间就近配对而不是串行错配。
		L := []domain.Entry{
			ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
			ledgerEntry("L2", "100.00", "USD", "2026-10-01T11:00:00Z", ""),
		}
		S := []domain.Entry{
			stmtEntry("S1", "100.00", "USD", "2026-10-01T11:01:30Z", ""),
			stmtEntry("S2", "100.00", "USD", "2026-10-01T10:01:30Z", ""),
		}
		ps, _, _, _ := Reconcile(L, S, cfg)
		if len(ps) != 2 {
			t.Fatalf("期望 2 个配对，得到 %d", len(ps))
		}
		got := map[string]string{}
		for _, p := range ps {
			got[p.LedgerEntry.ID] = p.StatementEntry.ID
			if p.Diff != domain.DiffMatched {
				t.Fatalf("期望 MATCHED，得到 %s", p.Diff)
			}
		}
		if got["L1"] != "S2" || got["L2"] != "S1" {
			t.Fatalf("跨批次归位错误: %+v", got)
		}
		logCase(t, "匹配主流程", "跨批次时间窗口内就近归位")
	})

	t.Run("参考号相同即使超出扩展窗口仍按同一笔业务归位", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "INV-9")}
		S := []domain.Entry{stmtEntry("S1", "100.00", "USD", "2026-10-02T10:00:00Z", "INV-9")}
		ps, ds, _, _ := Reconcile(L, S, cfg)
		if len(ps) != 1 || ps[0].Diff != domain.DiffTimeMismatch {
			t.Fatalf("期望参考号归位并判 TIME_MISMATCH，得到 ps=%+v ds=%d", pairTypes(ps), len(ds))
		}
		logCase(t, "匹配主流程", "参考号跨窗口归位(时间差分类)")
	})

	t.Run("币种不同不配对", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "100.00", "EUR", "2026-10-01T10:00:00Z", "")}
		_, ds, up, _ := Reconcile(L, S, cfg)
		dc := diffCount(ds)
		if dc[domain.DiffLedgerOnly] != 1 || dc[domain.DiffStatementOnly] != 1 {
			t.Fatalf("期望各 1 条单边差异，得到 %+v", dc)
		}
		if len(up) != 2 {
			t.Fatalf("期望 2 条未配对，得到 %d", len(up))
		}
		logCase(t, "匹配主流程", "币种不同落单边差异")
	})
}

// 业务二：差异分类 —— 五种差异类型都要能区分，且一条流水都不丢。
func TestReconcile_DiffClassification(t *testing.T) {
	cfg := testCfg()

	t.Run("金额差在容差内自动 WITHIN_TOLERANCE，不进差异队列", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "100.005", "USD", "2026-10-01T10:00:00Z", "")}
		ps, ds, _, _ := Reconcile(L, S, cfg)
		if len(ps) != 1 || ps[0].Diff != domain.DiffWithinTol {
			t.Fatalf("期望 WITHIN_TOLERANCE，得到 %+v", pairTypes(ps))
		}
		if len(ds) != 0 {
			t.Fatalf("容差内不应进差异队列，得到 %d 条", len(ds))
		}
		logCase(t, "差异分类/容差", "容差内自动核对")
	})

	t.Run("金额差超出容差判 AMOUNT_MISMATCH 并进队列", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "100.50", "USD", "2026-10-01T10:00:00Z", "")}
		ps, ds, _, _ := Reconcile(L, S, cfg)
		if ps[0].Diff != domain.DiffAmountMismatch {
			t.Fatalf("期望 AMOUNT_MISMATCH，得到 %s", ps[0].Diff)
		}
		if diffCount(ds)[domain.DiffAmountMismatch] != 1 {
			t.Fatalf("差异队列期望 1 条 AMOUNT_MISMATCH，得到 %+v", diffCount(ds))
		}
		if ds[0].Delta != mustMoney("0.50") {
			t.Fatalf("差额应为 +0.50，得到 %d", ds[0].Delta)
		}
		logCase(t, "差异分类/容差", "超容差金额差异")
	})

	t.Run("时间超扩展窗口且无参考号：双方各自落单边差异", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "100.00", "USD", "2026-10-01T12:00:00Z", "")}
		_, ds, up, _ := Reconcile(L, S, cfg)
		dc := diffCount(ds)
		if dc[domain.DiffLedgerOnly] != 1 || dc[domain.DiffStatementOnly] != 1 {
			t.Fatalf("超扩展窗口应各自落单边，得到 %+v", dc)
		}
		if len(up) != 2 {
			t.Fatalf("未配对应为 2，得到 %d", len(up))
		}
		logCase(t, "差异分类", "时间超扩展窗口-两边漏单")
	})

	t.Run("时间在扩展窗口内但超主窗口、金额一致判 TIME_MISMATCH", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "100.00", "USD", "2026-10-01T10:20:00Z", "")}
		ps, ds, _, _ := Reconcile(L, S, cfg)
		if len(ps) != 1 || ps[0].Diff != domain.DiffTimeMismatch {
			t.Fatalf("期望 TIME_MISMATCH，得到 %+v", pairTypes(ps))
		}
		if diffCount(ds)[domain.DiffTimeMismatch] != 1 {
			t.Fatalf("差异队列期望 1 条 TIME_MISMATCH")
		}
		logCase(t, "差异分类", "时间差(扩展窗口内)")
	})

	t.Run("两个维度都对不上且无参考号不配对，双方落单边", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{stmtEntry("S1", "900.00", "USD", "2026-10-01T10:20:00Z", "")}
		_, ds, _, _ := Reconcile(L, S, cfg)
		dc := diffCount(ds)
		if dc[domain.DiffLedgerOnly] != 1 || dc[domain.DiffStatementOnly] != 1 {
			t.Fatalf("两维都不符应落单边，得到 %+v", dc)
		}
		logCase(t, "差异分类", "金额与时间均不符")
	})

	t.Run("对方有我方没有 / 我方有对方没有", func(t *testing.T) {
		L := []domain.Entry{ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		S := []domain.Entry{
			stmtEntry("S1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
			stmtEntry("S2", "55.00", "USD", "2026-10-01T10:00:00Z", ""),
		}
		_, ds, _, _ := Reconcile(L, S, cfg)
		dc := diffCount(ds)
		if dc[domain.DiffStatementOnly] != 1 {
			t.Fatalf("期望 1 条 STATEMENT_ONLY，得到 %+v", dc)
		}
		// 反向：我方多出一条
		L2 := []domain.Entry{
			ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
			ledgerEntry("L9", "77.00", "USD", "2026-10-01T10:00:00Z", ""),
		}
		S2 := []domain.Entry{stmtEntry("S1", "100.00", "USD", "2026-10-01T10:00:00Z", "")}
		_, ds2, _, _ := Reconcile(L2, S2, cfg)
		if diffCount(ds2)[domain.DiffLedgerOnly] != 1 {
			t.Fatalf("期望 1 条 LEDGER_ONLY，得到 %+v", diffCount(ds2))
		}
		logCase(t, "差异分类", "单边缺失两个方向")
	})

	t.Run("重复入账：同ID重复 与 同金额币种时间参考重复", func(t *testing.T) {
		L := []domain.Entry{
			ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "R"),
			ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", "R"), // 同 ID
			ledgerEntry("L2", "200.00", "USD", "2026-10-01T11:00:00Z", ""),
			ledgerEntry("L3", "200.00", "USD", "2026-10-01T11:00:00Z", ""), // 与 L2 业务要素完全相同
		}
		S := []domain.Entry{
			stmtEntry("S1", "100.00", "USD", "2026-10-01T10:00:00Z", "R"),
			stmtEntry("S2", "200.00", "USD", "2026-10-01T11:00:00Z", ""),
		}
		ps, ds, _, dups := Reconcile(L, S, cfg)
		if len(dups) != 2 {
			t.Fatalf("期望 2 条重复，得到 %d: %+v", len(dups), dups)
		}
		if len(ps) != 2 {
			t.Fatalf("期望保留的首条各配一对（共2），得到 %d", len(ps))
		}
		if diffCount(ds)[domain.DiffDuplicate] != 2 {
			t.Fatalf("期望 2 条 DUPLICATE 差异，得到 %+v", diffCount(ds))
		}
		logCase(t, "差异分类", "重复入账识别(首条保留其余标记)")
	})

	t.Run("没有任何流水可以被悄悄丢掉：全部条目有着落", func(t *testing.T) {
		L := []domain.Entry{
			ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
			ledgerEntry("L2", "500.00", "USD", "2026-10-01T10:00:00Z", ""),
			ledgerEntry("L3", "60.00", "USD", "2026-10-01T15:00:00Z", ""),
		}
		S := []domain.Entry{
			stmtEntry("S1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
			stmtEntry("S9", "60.00", "USD", "2026-10-01T15:30:00Z", ""),
		}
		ps, ds, up, _ := Reconcile(L, S, cfg)
		// 每条流水：在配对中 或 在未配对列表中，计数加总必须等于输入总数。
		seen := map[string]bool{}
		for _, p := range ps {
			seen["LEDGER:"+p.LedgerEntry.ID] = true
			seen["STATEMENT:"+p.StatementEntry.ID] = true
		}
		for _, e := range up {
			key := string(e.Source) + ":" + e.ID
			if seen[key] {
				t.Fatalf("%s 同时出现在配对与未配对中", key)
			}
			seen[key] = true
		}
		for _, e := range L {
			if !seen["LEDGER:"+e.ID] {
				t.Fatalf("我方流水 %s 丢失", e.ID)
			}
		}
		for _, e := range S {
			if !seen["STATEMENT:"+e.ID] {
				t.Fatalf("对方流水 %s 丢失", e.ID)
			}
		}
		// 每条非 MATCHED/WITHIN_TOL 的配对也要在差异队列里。
		if len(ds) != 2 { // L2 LEDGER_ONLY + L3/S9 TIME_MISMATCH
			t.Fatalf("期望 2 条差异，得到 %d: %+v", len(ds), diffCount(ds))
		}
		logCase(t, "差异分类", "全量流水去向可解释无丢失")
	})
}

// 业务三：确定性 —— 打乱输入顺序、重复运行，结论必须逐字节一致。
func TestReconcile_Deterministic(t *testing.T) {
	cfg := testCfg()
	L := []domain.Entry{
		ledgerEntry("L3", "60.00", "USD", "2026-10-01T15:00:00Z", ""),
		ledgerEntry("L1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
		ledgerEntry("L2", "100.50", "USD", "2026-10-01T11:00:00Z", ""),
	}
	S := []domain.Entry{
		stmtEntry("S2", "100.50", "USD", "2026-10-01T11:00:00Z", ""),
		stmtEntry("S3", "60.00", "USD", "2026-10-01T15:20:00Z", ""),
		stmtEntry("S1", "100.00", "USD", "2026-10-01T10:00:00Z", ""),
	}
	run := func() string {
		ps, ds, up, dups := Reconcile(L, S, cfg)
		out := ""
		for _, p := range ps {
			out += string(p.Diff) + ":" + p.LedgerEntry.ID + "=" + p.StatementEntry.ID + ";"
		}
		for _, d := range ds {
			out += string(d.Type) + ":" + d.Key + ";"
		}
		for _, e := range up {
			out += "UP:" + e.ID + ";"
		}
		for _, e := range dups {
			out += "DUP:" + e.ID + ";"
		}
		return out
	}
	first := run()
	// 再以相反顺序构造输入，结果必须相同（matcher 内部会排序）。
	Lrev := []domain.Entry{L[2], L[1], L[0]}
	Srev := []domain.Entry{S[2], S[1], S[0]}
	_ = Lrev
	_ = Srev
	ps1, ds1, up1, dups1 := Reconcile(L, S, cfg)
	ps2, ds2, up2, dups2 := Reconcile([]domain.Entry{L[2], L[1], L[0]}, []domain.Entry{S[2], S[1], S[0]}, cfg)
	if len(ps1) != len(ps2) || len(ds1) != len(ds2) || len(up1) != len(up2) || len(dups1) != len(dups2) {
		t.Fatal("打乱顺序后结果长度不同")
	}
	for i := range ps1 {
		if ps1[i].LedgerEntry.ID != ps2[i].LedgerEntry.ID || ps1[i].StatementEntry.ID != ps2[i].StatementEntry.ID || ps1[i].Diff != ps2[i].Diff {
			t.Fatalf("配对结果随顺序变化:\n%+v\n%+v", ps1, ps2)
		}
	}
	second := run()
	if first != second {
		t.Fatalf("重复运行结果不一致:\n%s\n%s", first, second)
	}
	logCase(t, "确定性/幂等", "乱序输入与重复运行结论一致")
}
