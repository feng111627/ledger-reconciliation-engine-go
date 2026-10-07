package recon

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleSets() (local, remote []Entry) {
	local = buildEntries(SideLocal, [][5]string{
		{"L1", "2026-10-01T09:00:00Z", "CNY", "100.00", "O1"},
		{"L2", "2026-10-01T09:05:00Z", "CNY", "200.005", "O2"},
		{"L4", "2026-10-01T11:00:00Z", "CNY", "300.00", "O4"},
		{"L7", "2026-10-01T13:00:00Z", "CNY", "15.00", "O7"},
	})
	remote = buildEntries(SideRemote, [][5]string{
		{"R1", "2026-10-01T09:02:00Z", "CNY", "100.00", "O1"},
		{"R2", "2026-10-01T09:04:00Z", "CNY", "200.00", "O2"},
		{"R4", "2026-10-01T11:03:00Z", "CNY", "290.00", "O4"},
		{"R8", "2026-10-01T14:00:00Z", "CNY", "42.00", "O8"},
	})
	return local, remote
}

// pairStatus 从报告中生成 (localID,remoteID)->Pair 索引。
func pairIndex(r *Report) map[string]Pair {
	m := map[string]Pair{}
	for _, p := range r.Pairs {
		m[p.LocalID+"|"+p.RemoteID] = p
	}
	return m
}

// TestEngineIdempotentRerun 业务：同一批数据重复跑，结论稳定一致，
// 不重复计数，结论库条数不增长。
func TestEngineIdempotentRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(DefaultConfig(), st)
	local, remote := sampleSets()

	r1, err := eng.Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	n1 := storeSize(t, path)
	if n1 != len(r1.Pairs) {
		t.Fatalf("结论库 %d 条 != 报告 %d 条", n1, len(r1.Pairs))
	}

	// 第二次、第三次重跑：逐笔结论一致。
	r2, err := eng.Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	r3, err := eng.Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	i1, i2, i3 := pairIndex(r1), pairIndex(r2), pairIndex(r3)
	for k, p := range i1 {
		if i2[k] != p {
			t.Errorf("重跑后 %s 结论被改变: %+v -> %+v", k, p, i2[k])
		}
		if i3[k] != p {
			t.Errorf("第三次运行 %s 结论漂移", k)
		}
	}
	if n := storeSize(t, path); n != n1 {
		t.Errorf("重跑导致重复写入: %d -> %d", n1, n)
	}
	if r1.RunID != r2.RunID {
		t.Error("同一批数据 run id 不稳定")
	}
	if !r1.Conserved || !r2.Conserved || !r3.Conserved {
		t.Error("资金守恒校验未通过")
	}

	// 新开一个引擎/进程视角读同一个结论库，结论仍一致（持久化生效）。
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	eng2 := NewEngine(DefaultConfig(), st2)
	r4, err := eng2.Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	if p := pairIndex(r4)["L4|R4"]; p.Status != StatusPending {
		t.Errorf("重开后 L4|R4 = %s, 期望仍为 pending", p.Status)
	}
}

// TestEngineResolveProtected 业务：人工处理后进已处理；
// 再次重跑不能把它覆盖回 pending；matched/tolerated 也不可被人工改单覆盖。
func TestEngineResolveProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := NewEngine(DefaultConfig(), st)
	local, remote := sampleSets()

	if _, err := eng.Run(local, remote, ""); err != nil {
		t.Fatal(err)
	}
	if err := eng.Resolve("L4", "R4", "折扣已审批"); err != nil {
		t.Fatal(err)
	}
	r2, err := eng.Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	p := pairIndex(r2)["L4|R4"]
	if p.Status != StatusResolved || p.Note != "折扣已审批" {
		t.Fatalf("人工处理未生效或被覆盖: %+v", p)
	}

	// 已 resolved 的结论不能重复处理覆盖。
	if err := eng.Resolve("L4", "R4", "再改一次"); err == nil {
		t.Fatal("已处理结论不应允许覆盖")
	}
	// matched 的结论也不能被人工改单。
	if err := eng.Resolve("L1", "R1", "想改已匹配"); err == nil {
		t.Fatal("matched 结论不应允许覆盖")
	}
	// 不存在的配对报错。
	if err := eng.Resolve("X", "Y", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在配对应返回 ErrNotFound, 得到 %v", err)
	}
	r3, _ := eng.Run(local, remote, "")
	if p := pairIndex(r3)["L4|R4"]; p.Note != "折扣已审批" {
		t.Fatalf("结论被覆盖: %s", p.Note)
	}
	if !r3.Conserved {
		t.Error("人工处理后资金守恒仍应成立")
	}
}

// TestStoreConcurrentLock 业务：并发/多次运行时，文件锁保证
// 同一时刻只有一个实例写，另一个实例必须快速失败而不是互相覆盖。
func TestStoreConcurrentLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	holder, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()

	if _, err := OpenStore(path); err == nil {
		t.Fatal("持有锁时第二个实例应当打开失败，防止并发重复处理")
	} else if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("错误信息应提示锁定, 得到 %v", err)
	}

	// 持锁实例可以正常幂等写入。
	ok, err := holder.PutIfAbsent(StoredPair{LocalID: "L1", RemoteID: "R1", Status: StatusMatched, Diff: DiffNone})
	if err != nil || !ok {
		t.Fatalf("首次写入失败 ok=%v err=%v", ok, err)
	}
	ok, err = holder.PutIfAbsent(StoredPair{LocalID: "L1", RemoteID: "R1", Status: StatusPending})
	if err != nil || ok {
		t.Fatalf("重复写入应被忽略 ok=%v err=%v", ok, err)
	}
	got, err := holder.Load()
	if err != nil || len(got) != 1 || got[0].Status != StatusMatched {
		t.Fatalf("PutIfAbsent 破坏了既有结论: %+v err=%v", got, err)
	}
}

// TestStoreConcurrentGoroutines 业务：多个 goroutine 串行化使用结论库时
// （如任务队列），写操作保持幂等，最终条数确定。
func TestStoreConcurrentGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const n = 30
	var wg sync.WaitGroup
	var mu sync.Mutex // 模拟调用方对同一 Store 句柄串行使用
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			id := "L" + strconv.Itoa(i%5) // 故意制造重复键
			_, _ = st.PutIfAbsent(StoredPair{LocalID: id, RemoteID: "R", Status: StatusPending})
		}(i)
	}
	wg.Wait()
	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("并发幂等写入后应恰好 5 条, 得到 %d", len(got))
	}
}

// TestEngineConservationRandom 业务：任意数据下资金恒等式必须成立——
// 我方总额 - 对方总额 == 已解释差额 + 待处理差额，逐币种。
func TestEngineConservationRandom(t *testing.T) {
	eng := NewEngine(DefaultConfig(), nil) // 无状态，直接测汇总逻辑
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	mk := func(side Side, id string, cur string, amt int64, hour int, ref string) Entry {
		return Entry{ID: id, Side: side, Currency: cur,
			Amount: MoneyFromScaled(amt * 1_000_000), Time: base.Add(time.Duration(hour) * time.Hour), Ref: ref}
	}
	local := []Entry{
		mk(SideLocal, "L1", "CNY", 100, 0, "O1"),
		mk(SideLocal, "L2", "CNY", 200, 1, "O2"),
		mk(SideLocal, "L3", "USD", 50, 0, "O3"),
		mk(SideLocal, "L4", "CNY", 17, 2, "O4"), // 单边
	}
	remote := []Entry{
		mk(SideRemote, "R1", "CNY", 100, 0, "O1"),
		mk(SideRemote, "R2", "CNY", 195, 1, "O2"), // 超容差
		mk(SideRemote, "R3", "USD", 50, 0, "O3"),
		mk(SideRemote, "R5", "EUR", 9, 4, "O5"), // 对方单边
	}
	r, err := eng.Run(local, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	for cur := range map[string]bool{"CNY": true, "USD": true, "EUR": true} {
		gap := r.LocalTotal[cur].Sub(r.RemoteTotal[cur])
		expl := r.Explained[cur].Add(r.Pending[cur])
		if !gap.Equal(expl) {
			t.Errorf("币种 %s 资金不守恒: 我方-对方=%s, 已解释+待处理=%s", cur, gap, expl)
		}
	}
	if !r.Conserved {
		t.Error("报告保守性标志应为 true")
	}
}

func storeSize(t *testing.T, path string) int {
	t.Helper()
	data, err := readRawStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(data.Pairs)
}
