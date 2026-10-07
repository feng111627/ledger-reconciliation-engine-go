package recon

import "time"

// Side 标识流水来源。
type Side string

const (
	SideLocal  Side = "local"  // 我方账本导出
	SideRemote Side = "remote" // 对方对账单
)

// Entry 是一条待对账流水。金额用定点数 Money 表示，避免浮点误差。
type Entry struct {
	ID       string
	Side     Side
	Currency string
	Amount   Money
	Time     time.Time
	Ref      string // 业务参考号（如订单号/流水号），相同 Ref 视为同一笔业务
	Raw      string // 原始行内容，便于解释与回溯
}

// DiffType 差异分类。
type DiffType string

const (
	DiffNone            DiffType = ""                   // 无差异
	DiffAmountMismatch  DiffType = "amount_mismatch"    // 金额对不上（超出容差）
	DiffLocalMissing    DiffType = "local_missing"      // 对方有、我方没有
	DiffRemoteMissing   DiffType = "remote_missing"     // 我方有、对方没有
	DiffDuplicate       DiffType = "duplicate"          // 同一侧重复入账
	DiffTimeOutOfWindow DiffType = "time_out_of_window" // 金额币种一致但时间超出窗口
)

// Status 结论状态。
type Status string

const (
	StatusMatched   Status = "matched"   // 已核对（精确匹配）
	StatusTolerated Status = "tolerated" // 容差内，自动视为已核对
	StatusPending   Status = "pending"   // 差异队列，待人工处理
	StatusResolved  Status = "resolved"  // 差异已人工处理
	StatusDuplicate Status = "duplicate" // 重复入账
)

// Pair 表示一次匹配尝试的结果记录，每条流水最终都有且仅有一条去向记录。
type Pair struct {
	LocalID   string
	RemoteID  string
	Diff      DiffType
	Status    Status
	Delta     Money // 金额差（local - remote），未配对时为该笔金额本身
	Note      string
	DecidedAt time.Time
}

// Report 对账报告：结论明细 + 资金守恒汇总。
type Report struct {
	RunID       string
	RanAt       time.Time
	Config      Config
	Entries     []Entry
	Pairs       []Pair
	LocalTotal  map[string]Money
	RemoteTotal map[string]Money
	Explained   map[string]Money // 已解释差额（有符号）：匹配/容差/人工已处理配对的 Delta 之和
	Pending     map[string]Money // 待处理差额（有符号）：pending/duplicate 配对的 Delta 之和
	Conserved   bool            // 每笔流水均有去向且 我方-对方 == 已解释差额 + 待处理差额
}
