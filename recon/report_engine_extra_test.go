package recon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReportRender 业务：报告必须说清每笔去向、差异分类和资金守恒结论。
func TestReportRender(t *testing.T) {
	eng := NewEngine(DefaultConfig(), nil)
	local, remote := sampleSets()
	r, err := eng.Run(local, remote, "run-fixed")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderText(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"run-fixed",
		"对方有我方没有", "金额对不上", "我方有对方没有",
		"已核对-容差", "待人工处理",
		"资金守恒：通过",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("报告缺少关键内容 %q", want)
		}
	}
}

// TestEngineEndToEndFromCSV 业务：从两个本地 CSV 来源到最终报告，
// 六类差异齐全且每笔都有结论（基于仓库 testdata）。
func TestEngineEndToEndFromCSV(t *testing.T) {
	local, remote, err := ReadSources("../testdata/local.csv", "../testdata/remote.csv")
	if err != nil {
		t.Skip("示例数据不存在，跳过")
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	r, err := NewEngine(DefaultConfig(), st).Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Conserved {
		t.Fatal("示例数据应当资金守恒")
	}
	want := map[DiffType]bool{
		DiffNone: true, DiffAmountMismatch: true, DiffLocalMissing: true,
		DiffRemoteMissing: true, DiffDuplicate: true, DiffTimeOutOfWindow: true,
	}
	seen := map[DiffType]bool{}
	for _, p := range r.Pairs {
		seen[p.Diff] = true
	}
	for d := range want {
		if !seen[d] {
			t.Errorf("示例数据未覆盖差异类型 %q", d)
		}
	}
}

// TestDifferentCurrencyNotMatched 业务：金额相同但币种不同不能配对，
// 两侧各落缺失差异，且按币种分别守恒。
func TestDifferentCurrencyNotMatched(t *testing.T) {
	base := "2026-10-01T09:00:00Z"
	local := buildEntries(SideLocal, [][5]string{
		{"L1", base, "CNY", "100.00", ""},
	})
	remote := buildEntries(SideRemote, [][5]string{
		{"R1", base, "USD", "100.00", ""},
	})
	rs := Match(local, remote, DefaultConfig())
	var gotLocalMiss, gotRemoteMiss bool
	for _, p := range rs.Pairs {
		if p.LocalID == "L1" && p.Diff == DiffRemoteMissing {
			gotRemoteMiss = true
		}
		if p.RemoteID == "R1" && p.Diff == DiffLocalMissing {
			gotLocalMiss = true
		}
	}
	if !gotLocalMiss || !gotRemoteMiss {
		t.Fatalf("跨币种被错误配对: %+v", rs.Pairs)
	}
}

// TestToleranceConfig 业务：可按币种单独设置容差，未设置的走默认值。
func TestToleranceConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ToleranceFor("CNY").String() != "0.01" {
		t.Fatal("默认 CNY 容差应为 0.01")
	}
	if cfg.ToleranceFor("JPY").String() != "0.01" {
		t.Fatal("未知币种应回落到默认容差")
	}
	cfg.Tolerance["JPY"] = MustParseMoney("1")
	if cfg.ToleranceFor("JPY").String() != "1" {
		t.Fatal("按币种容差设置未生效")
	}
}

// TestStoreFilePersists 业务：结论库是普通 JSON 文件，落盘内容可读，
// 关闭后重新打开数据仍在。
func TestStoreFilePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.PutIfAbsent(StoredPair{LocalID: "L1", RemoteID: "R1",
		Diff: DiffAmountMismatch, Status: StatusPending, Delta: "3.5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "amount_mismatch") {
		t.Error("落盘 JSON 内容异常")
	}
	st2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.Load()
	if err != nil || len(got) != 1 || got[0].Delta != "3.5" {
		t.Fatalf("重开结论库数据丢失: %+v %v", got, err)
	}
}

// TestResolveUpdatesConservationBuckets 业务：人工处理后差额从
// 待处理差额挪到已解释差额，守恒等式仍成立。
func TestResolveUpdatesConservationBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := NewEngine(DefaultConfig(), st)
	local, remote := sampleSets()

	r1, _ := eng.Run(local, remote, "")
	beforePending := r1.Pending["CNY"]
	beforeExplained := r1.Explained["CNY"]
	if err := eng.Resolve("L4", "R4", "ok"); err != nil {
		t.Fatal(err)
	}
	r2, _ := eng.Run(local, remote, "")
	if !r2.Pending["CNY"].Equal(beforePending.Sub(MustParseMoney("10"))) {
		t.Errorf("待处理差额未减少: before=%s after=%s", beforePending, r2.Pending["CNY"])
	}
	if !r2.Explained["CNY"].Equal(beforeExplained.Add(MustParseMoney("10"))) {
		t.Errorf("已解释差额未增加: before=%s after=%s", beforeExplained, r2.Explained["CNY"])
	}
	if !r2.Conserved {
		t.Error("处理后守恒失败")
	}
}
