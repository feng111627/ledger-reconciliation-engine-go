package store

import (
	"testing"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
)

func TestSaveRunIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, cleanup, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	r := &domain.RunResult{RunID: "run-A", InputFingerprint: "fp1"}
	id1, err := st.SaveRunResult("fp1", r)
	if err != nil || id1 != "run-A" {
		t.Fatalf("首次保存异常: %s %v", id1, err)
	}
	// 相同指纹用不同 RunID 再存：必须返回原 RunID，绝不覆盖。
	id2, err := st.SaveRunResult("fp1", &domain.RunResult{RunID: "run-B", InputFingerprint: "fp1"})
	if err != nil || id2 != "run-A" {
		t.Fatalf("重复保存应返回原 RunID，得到 %s", id2)
	}
	loaded, err := st.LoadRun("fp1")
	if err != nil || loaded == nil || loaded.RunID != "run-A" {
		t.Fatalf("缓存结果被覆盖或读取异常: %+v err=%v", loaded, err)
	}
	t.Log("[业务:幂等持久化] 用例:相同指纹重复保存不覆盖原结论 ... PASS")

	miss, err := st.LoadRun("nope")
	if err != nil || miss != nil {
		t.Fatalf("不存在的指纹应返回 nil,nil，得到 %+v %v", miss, err)
	}
}

func TestResolutions(t *testing.T) {
	dir := t.TempDir()
	st, cleanup, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if got, err := st.GetResolution("k1"); err != nil || got != nil {
		t.Fatalf("初始无结论应返回 nil，得到 %+v", got)
	}
	r1 := domain.Resolution{Key: "k1", Status: domain.StatusAccepted, Note: "n1", UpdatedAt: time.Now()}
	if err := st.PutResolution(r1); err != nil {
		t.Fatal(err)
	}
	r2 := domain.Resolution{Key: "k2", Status: domain.StatusWriteOff, Note: "n2", UpdatedAt: time.Now()}
	if err := st.PutResolution(r2); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetResolution("k1")
	if err != nil || got == nil || got.Status != domain.StatusAccepted {
		t.Fatalf("读取 k1 异常: %+v %v", got, err)
	}
	// 同 Key 更新
	r1.Status = domain.StatusWriteOff
	r1.Note = "changed"
	if err := st.PutResolution(r1); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListResolutions()
	if err != nil || len(all) != 2 {
		t.Fatalf("应仍为 2 条结论，得到 %d err=%v", len(all), err)
	}
	if all[0].Key != "k1" || all[0].Note != "changed" {
		t.Fatalf("结论未按 Key 更新: %+v", all[0])
	}
	if all[1].Key != "k2" {
		t.Fatalf("结论应按 Key 排序，得到 %s,%s", all[0].Key, all[1].Key)
	}
	t.Log("[业务:差异处理] 用例:结论按稳定Key存取/更新/排序 ... PASS")
}
