package recon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadCSV 业务：两个来源读取，表头识别、side 兜底、
// 非法行明确报错而不是悄悄丢数据，行顺序不影响后续匹配（结果排序）。
func TestLoadCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.csv")
	content := strings.Join([]string{
		"id,side,time,currency,amount,ref",
		"L2,local,2026-10-01T10:00:00Z,CNY,200.00,O2",
		"L1,,2026-10-01T09:00:00Z,cny,100.00,O1", // side 留空走兜底；币种小写归一
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadCSV(path, SideLocal)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("读到 %d 条, 期望 2", len(got))
	}
	// 结果按时间排序：L1 在前，与文件行序无关。
	if got[0].ID != "L1" || got[1].ID != "L2" {
		t.Fatalf("排序错误: %s, %s", got[0].ID, got[1].ID)
	}
	if got[0].Side != SideLocal {
		t.Error("side 空值未用 defaultSide 兜底")
	}
	if got[0].Currency != "CNY" {
		t.Errorf("币种未归一大写: %s", got[0].Currency)
	}

	// 非法金额必须报错，不能静默丢弃。
	bad := filepath.Join(dir, "bad.csv")
	_ = os.WriteFile(bad, []byte("id,side,time,currency,amount,ref\nX,local,2026-10-01T09:00:00Z,CNY,abc,O\n"), 0o644)
	if _, err := LoadCSV(bad, SideLocal); err == nil {
		t.Error("非法金额应当报错")
	}

	// ReadSources 校验来源归属与文件内 ID 唯一。
	remote := filepath.Join(dir, "remote.csv")
	_ = os.WriteFile(remote, []byte("id,side,time,currency,amount,ref\nR1,remote,2026-10-01T09:01:00Z,CNY,100.00,O1\n"), 0o644)
	l, r, err := ReadSources(path, remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || len(r) != 1 {
		t.Fatal("ReadSources 读取数量错误")
	}
}
