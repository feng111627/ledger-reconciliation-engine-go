package recon

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// diffLabel 差异类型中文说明。
var diffLabel = map[DiffType]string{
	DiffNone:            "无差异",
	DiffAmountMismatch:  "金额对不上",
	DiffLocalMissing:    "对方有我方没有",
	DiffRemoteMissing:   "我方有对方没有",
	DiffDuplicate:       "重复入账",
	DiffTimeOutOfWindow: "时间差(超窗口)",
}

// statusLabel 结论状态中文说明。
var statusLabel = map[Status]string{
	StatusMatched:   "已核对-精确",
	StatusTolerated: "已核对-容差",
	StatusPending:   "待人工处理",
	StatusResolved:  "人工已处理",
	StatusDuplicate: "重复入账",
}

// RenderText 输出人读报告：逐笔去向 + 分类汇总 + 资金守恒校验。
func RenderText(w io.Writer, r *Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "===== 对账报告 %s =====\n", r.RunID)
	fmt.Fprintf(&b, "生成时间: %s   时间窗口: %s\n",
		r.RanAt.Format("2006-01-02 15:04:05 UTC"), r.Config.TimeWindow)
	fmt.Fprintf(&b, "流水笔数: 我方 %d 笔, 对方 %d 笔\n\n",
		countSide(r.Entries, SideLocal), countSide(r.Entries, SideRemote))

	b.WriteString("--- 逐笔去向 ---\n")
	b.WriteString(fmt.Sprintf("%-12s %-12s %-16s %-14s %12s  说明\n",
		"我方流水", "对方流水", "差异类型", "结论", "差额"))
	counts := map[DiffType]int{}
	statusCounts := map[Status]int{}
	for _, p := range r.Pairs {
		localID := p.LocalID
		if localID == "" {
			localID = "-"
		}
		remoteID := p.RemoteID
		if remoteID == "" {
			remoteID = "-"
		}
		fmt.Fprintf(&b, "%-12s %-12s %-16s %-14s %12s  %s\n",
			localID, remoteID, diffLabel[p.Diff], statusLabel[p.Status],
			p.Delta.String(), p.Note)
		counts[p.Diff]++
		statusCounts[p.Status]++
	}

	b.WriteString("\n--- 差异分类统计 ---\n")
	for _, d := range orderedDiffs() {
		if n := counts[d]; n > 0 {
			fmt.Fprintf(&b, "  %-16s %d 笔\n", diffLabel[d], n)
		}
	}
	b.WriteString("\n--- 结论统计 ---\n")
	for _, st := range orderedStatuses() {
		if n := statusCounts[st]; n > 0 {
			fmt.Fprintf(&b, "  %-14s %d 笔\n", statusLabel[st], n)
		}
	}

	b.WriteString("\n--- 资金守恒（按币种）---\n")
	currencies := map[string]bool{}
	for c := range r.LocalTotal {
		currencies[c] = true
	}
	for c := range r.RemoteTotal {
		currencies[c] = true
	}
	cs := make([]string, 0, len(currencies))
	for c := range currencies {
		cs = append(cs, c)
	}
	sort.Strings(cs)
	for _, c := range cs {
		fmt.Fprintf(&b, "  %s: 我方合计 %s | 对方合计 %s | 已解释差额 %s | 待处理差额 %s\n",
			c, r.LocalTotal[c], r.RemoteTotal[c], r.Explained[c], r.Pending[c])
		gap := r.LocalTotal[c].Sub(r.RemoteTotal[c])
		fmt.Fprintf(&b, "       校验 我方-对方=%s，已解释+待处理=%s，%s\n",
			gap, r.Explained[c].Add(r.Pending[c]), mark(gap.Equal(r.Explained[c].Add(r.Pending[c]))))
	}

	if r.Conserved {
		b.WriteString("\n资金守恒：通过（每笔流水均有去向，两来源差额等于待处理净额）\n")
	} else {
		b.WriteString("\n资金守恒：未通过，请检查匹配逻辑或数据！\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func countSide(es []Entry, s Side) int {
	n := 0
	for _, e := range es {
		if e.Side == s {
			n++
		}
	}
	return n
}

func mark(ok bool) string {
	if ok {
		return "一致 ✓"
	}
	return "不一致 ✗"
}

func orderedDiffs() []DiffType {
	return []DiffType{
		DiffNone, DiffAmountMismatch, DiffLocalMissing,
		DiffRemoteMissing, DiffDuplicate, DiffTimeOutOfWindow,
	}
}

func orderedStatuses() []Status {
	return []Status{
		StatusMatched, StatusTolerated, StatusPending,
		StatusResolved, StatusDuplicate,
	}
}
