package recon

import (
	"testing"
	"time"
)

// buildEntries 构造某一侧的流水集合。
func buildEntries(side Side, rows [][5]string) []Entry {
	var out []Entry
	for _, r := range rows {
		tm, err := time.Parse(time.RFC3339, r[1])
		if err != nil {
			panic(err)
		}
		out = append(out, testEntry(r[0], string(side), r[2], r[3], tm, r[4]))
	}
	return out
}

// findPair 按 (localID, remoteID) 取结论，空串表示该侧无流水。
func findPair(t *testing.T, rs MatchResult, localID, remoteID string) Pair {
	t.Helper()
	for _, p := range rs.Pairs {
		if p.LocalID == localID && p.RemoteID == remoteID {
			return p
		}
	}
	t.Fatalf("未找到配对 local=%q remote=%q", localID, remoteID)
	return Pair{}
}

// TestMatchMainFlow 业务主流程：金额/币种/时间一致才匹配；
// 跨批次但时间相近按时间窗口归位；五类差异均可区分；任何流水不丢失。
func TestMatchMainFlow(t *testing.T) {
	local := buildEntries(SideLocal, [][5]string{
		{"L1", "2026-10-01T09:00:00Z", "CNY", "100.00", "O1"},  // 精确
		{"L2", "2026-10-01T09:05:00Z", "CNY", "200.005", "O2"}, // 容差
		{"L3", "2026-10-01T10:00:00Z", "USD", "50.00", "O3"},   // 跨批次超窗
		{"L4", "2026-10-01T11:00:00Z", "CNY", "300.00", "O4"},  // 金额不符
		{"L5", "2026-10-01T12:00:00Z", "CNY", "80.00", "O5"},   // 正常匹配
		{"L6", "2026-10-01T12:30:00Z", "CNY", "80.00", "O5"},   // 我方重复
		{"L7", "2026-10-01T13:00:00Z", "CNY", "15.00", "O7"},   // 对方缺失
	})
	remote := buildEntries(SideRemote, [][5]string{
		{"R1", "2026-10-01T09:02:00Z", "CNY", "100.00", "O1"},
		{"R2", "2026-10-01T09:04:00Z", "CNY", "200.00", "O2"},
		{"R3", "2026-10-01T13:00:00Z", "USD", "50.00", "O3"},
		{"R4", "2026-10-01T11:03:00Z", "CNY", "290.00", "O4"},
		{"R5", "2026-10-01T12:01:00Z", "CNY", "80.00", "O5"},
		{"R8", "2026-10-01T14:00:00Z", "CNY", "42.00", "O8"}, // 我方缺失
	})

	rs := Match(local, remote, DefaultConfig())

	// 用例 1：精确匹配（时间差 2 分钟，在窗口内）。
	if p := findPair(t, rs, "L1", "R1"); p.Status != StatusMatched || p.Diff != DiffNone {
		t.Errorf("L1-R1: %s/%s", p.Status, p.Diff)
	}
	// 用例 2：容差内自动核销。
	if p := findPair(t, rs, "L2", "R2"); p.Status != StatusTolerated {
		t.Errorf("L2-R2: %s, 期望 tolerated, delta=%s", p.Status, p.Delta)
	}
	// 用例 3：跨批次金额一致但超时间窗口 -> 时间差差异。
	if p := findPair(t, rs, "L3", "R3"); p.Diff != DiffTimeOutOfWindow || p.Status != StatusPending {
		t.Errorf("L3-R3: %s/%s, 期望 time_out_of_window/pending", p.Diff, p.Status)
	}
	// 用例 4：金额对不上 -> 差异队列。
	if p := findPair(t, rs, "L4", "R4"); p.Diff != DiffAmountMismatch || p.Delta.String() != "10" {
		t.Errorf("L4-R4: %s delta=%s, 期望 amount_mismatch/10", p.Diff, p.Delta)
	}
	// 用例 5：正常匹配。
	if p := findPair(t, rs, "L5", "R5"); p.Status != StatusMatched {
		t.Errorf("L5-R5: %s", p.Status)
	}
	// 用例 6：我方重复入账。
	if p := findPair(t, rs, "L6", ""); p.Diff != DiffDuplicate || p.Status != StatusDuplicate {
		t.Errorf("L6: %s/%s, 期望 duplicate", p.Diff, p.Status)
	}
	// 用例 7：我方有对方没有。
	if p := findPair(t, rs, "L7", ""); p.Diff != DiffRemoteMissing {
		t.Errorf("L7: %s, 期望 remote_missing", p.Diff)
	}
	// 用例 8：对方有我方没有。
	if p := findPair(t, rs, "", "R8"); p.Diff != DiffLocalMissing {
		t.Errorf("R8: %s, 期望 local_missing", p.Diff)
	}

	// 任何一条流水都不能丢：7+6=13 条，1:1 配对 5 对 + 3 条单边 = 8 条结论，
	// 每条流水恰好覆盖一次。
	if len(rs.Pairs) != 8 {
		t.Fatalf("结论条数=%d, 期望 8", len(rs.Pairs))
	}
	covered := map[string]bool{}
	for _, p := range rs.Pairs {
		if p.LocalID != "" {
			if covered[p.LocalID] {
				t.Fatalf("流水 %s 被重复计数", p.LocalID)
			}
			covered[p.LocalID] = true
		}
		if p.RemoteID != "" {
			if covered[p.RemoteID] {
				t.Fatalf("流水 %s 被重复计数", p.RemoteID)
			}
			covered[p.RemoteID] = true
		}
	}
	if len(covered) != 13 {
		t.Fatalf("被覆盖流水=%d, 期望 13（有流水被悄悄丢掉）", len(covered))
	}
}

// TestMatchTimeWindowBatchPlacement 业务：时间接近但跨批次的流水，
// 必须按时间窗口归到正确批次，而不是错配。
func TestMatchTimeWindowBatchPlacement(t *testing.T) {
	// 窗口 1 小时：09:00 与 09:30 属同批应配对；
	// 15:00 那笔虽然金额相同但跨批次超窗，只能归时间差。
	cfg := DefaultConfig()
	cfg.TimeWindow = time.Hour
	local := buildEntries(SideLocal, [][5]string{
		{"L1", "2026-10-01T09:00:00Z", "CNY", "10.00", "B1"},
		{"L2", "2026-10-01T15:00:00Z", "CNY", "10.00", "B1"},
	})
	remote := buildEntries(SideRemote, [][5]string{
		{"R1", "2026-10-01T09:30:00Z", "CNY", "10.00", "B1"},
	})
	rs := Match(local, remote, cfg)
	if p := findPair(t, rs, "L1", "R1"); p.Status != StatusMatched {
		t.Errorf("同批应匹配: %s/%s", p.Status, p.Diff)
	}
	// L2 组内已有同金额配对，故 L2 判重复，而不是抢 R1 造成时间差。
	if p := findPair(t, rs, "L2", ""); p.Diff != DiffDuplicate {
		t.Errorf("L2: %s, 期望 duplicate（L1 更近，优先配对）", p.Diff)
	}
}

// TestMatchDeterministic 业务：同一批数据无论输入顺序如何，
// 跑多少次结论必须一致、可重复。
func TestMatchDeterministic(t *testing.T) {
	local := buildEntries(SideLocal, [][5]string{
		{"L1", "2026-10-01T09:00:00Z", "CNY", "100.00", "O1"},
		{"L2", "2026-10-01T09:05:00Z", "CNY", "200.005", "O2"},
		{"L3", "2026-10-01T11:00:00Z", "CNY", "300.00", "O4"},
	})
	remote := buildEntries(SideRemote, [][5]string{
		{"R4", "2026-10-01T11:03:00Z", "CNY", "290.00", "O4"},
		{"R1", "2026-10-01T09:02:00Z", "CNY", "100.00", "O1"},
		{"R2", "2026-10-01T09:04:00Z", "CNY", "200.00", "O2"},
	})

	first := Match(local, remote, DefaultConfig())
	// 把输入整体逆序再跑，结论序列必须完全相同。
	rev := func(es []Entry) []Entry {
		out := append([]Entry(nil), es...)
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}
	second := Match(rev(local), rev(remote), DefaultConfig())
	if len(first.Pairs) != len(second.Pairs) {
		t.Fatalf("两次结论条数不同: %d vs %d", len(first.Pairs), len(second.Pairs))
	}
	for i := range first.Pairs {
		a, b := first.Pairs[i], second.Pairs[i]
		if a != b {
			t.Fatalf("第 %d 条结论不一致:\n %+v\n %+v", i, a, b)
		}
	}
	// 连跑第三次也一致。
	third := Match(local, remote, DefaultConfig())
	for i := range first.Pairs {
		if first.Pairs[i] != third.Pairs[i] {
			t.Fatal("第三次运行结论发生变化")
		}
	}
}

// TestMatchRemoteDuplicate 业务：对方侧重复入账也要能识别。
func TestMatchRemoteDuplicate(t *testing.T) {
	local := buildEntries(SideLocal, [][5]string{
		{"L1", "2026-10-01T09:00:00Z", "CNY", "50.00", "O1"},
	})
	remote := buildEntries(SideRemote, [][5]string{
		{"R1", "2026-10-01T09:01:00Z", "CNY", "50.00", "O1"},
		{"R2", "2026-10-01T09:02:00Z", "CNY", "50.00", "O1"},
	})
	rs := Match(local, remote, DefaultConfig())
	if p := findPair(t, rs, "L1", "R1"); p.Status != StatusMatched {
		t.Errorf("L1-R1: %s", p.Status)
	}
	if p := findPair(t, rs, "", "R2"); p.Diff != DiffDuplicate {
		t.Errorf("R2: %s, 期望 duplicate", p.Diff)
	}
}
