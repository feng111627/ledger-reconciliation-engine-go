package domain

import "time"

// Source 标识数据来源。
type Source string

const (
	SourceLedger    Source = "LEDGER"    // 我方账本导出
	SourceStatement Source = "STATEMENT" // 对方对账单
)

// Entry 是一次流水的标准化记录。
// Amount 为放大 1e6 倍后的整数金额，避免浮点误差；货币大小写不敏感。
type Entry struct {
	ID        string    // 来源文件中的唯一流水号
	Source    Source    // 来源
	Amount    int64     // 带符号的整数金额（放大 1e6）
	Currency  string    // 币种（统一大写存储）
	Time      time.Time // 记账时间（UTC）
	Reference string    // 对方流水号/业务参考号，可为空
	RawLine   int       // 源文件行号，便于排查
}

// DiffType 差异分类。
type DiffType string

const (
	DiffMatched        DiffType = "MATCHED"          // 完全匹配
	DiffWithinTol      DiffType = "WITHIN_TOLERANCE" // 金额差异在容差内，自动核对通过
	DiffAmountMismatch DiffType = "AMOUNT_MISMATCH"  // 金额对不上（超容差）
	DiffTimeMismatch   DiffType = "TIME_MISMATCH"    // 金额币种对得上但时间超出时间窗口
	DiffLedgerOnly     DiffType = "LEDGER_ONLY"      // 我方有、对方没有
	DiffStatementOnly  DiffType = "STATEMENT_ONLY"   // 对方有、我方没有
	DiffDuplicate      DiffType = "DUPLICATE"        // 同一来源内重复入账
)

// Pair 表示一条我方流水与一条对方流水的一对一配对结果。
type Pair struct {
	LedgerEntry    Entry
	StatementEntry Entry
	Diff           DiffType
	AmountDelta    int64 // Statement - Ledger（带符号，放大 1e6）
	TimeGapSeconds int64 // abs(Statement.Time - Ledger.Time)
}

// Discrepancy 是进入差异队列、需要解释去向的一条记录。
type Discrepancy struct {
	Key         string   // 稳定指纹：同一份输入永远得到同一个 Key（幂等基础）
	Type        DiffType // 差异类型
	LedgerID    string   // 涉及的我方流水号（可能为空）
	StatementID string   // 涉及的对方流水号（可能为空）
	Amount      int64    // 相关金额（放大 1e6）
	Currency    string
	Delta       int64 // 差额带符号（Statement - Ledger），单边缺失时按 0 - 已有侧 计算
	Detail      string
}

// ResolveStatus 人工处理结论。
type ResolveStatus string

const (
	StatusPending  ResolveStatus = "PENDING"
	StatusAccepted ResolveStatus = "ACCEPTED"  // 确认差异属实，接受
	StatusWriteOff ResolveStatus = "WRITE_OFF" // 核销
)

// Resolution 一条差异的人工处理结论（按 Discrepancy.Key 持久化）。
type Resolution struct {
	Key       string
	Status    ResolveStatus
	Note      string
	UpdatedAt time.Time
}

// Config 对账参数。
type Config struct {
	TimeWindow      time.Duration // 时间窗口：跨批次流水允许的最大时间差
	AmountTolerance int64         // 金额容差（放大 1e6 的绝对值）
	ExtendedWindow  time.Duration // 扩展窗口：窗口外但金额相同仍判 TIME_MISMATCH 的上限
}

// DefaultConfig 返回一组可用的默认参数。
func DefaultConfig() Config { return Config{} }

// RunResult 一次对账运行的完整、可解释结果。
type RunResult struct {
	RunID            string
	InputFingerprint string
	Config           Config
	Pairs            []Pair        // 所有成功配对（含 MATCHED / WITHIN_TOLERANCE / AMOUNT/TIME_MISMATCH）
	Discrepancies    []Discrepancy // 差异队列（不含已自动核对的 WITHIN_TOLERANCE）
	Unpaired         []Entry       // 未进入任何配对的流水（按来源即 LEDGER_ONLY / STATEMENT_ONLY）
	Duplicates       []Entry       // 被判为重复入账的流水
	// 守恒校验
	CurrencyTotals map[string]CurrencyTotal
	ConservationOK bool
}

// CurrencyTotal 单币种的来源总额与去向汇总。
type CurrencyTotal struct {
	LedgerTotal    int64
	StatementTotal int64
	MatchedDelta   int64 // 配对差异净额（Statement-Ledger 之和）
	OpenDelta      int64 // 未决（尚未人工处理）差异净额
	ResolvedDelta  int64 // 已人工处理（接受/核销）的差异净额
	Residual       int64 // StatementTotal - LedgerTotal
}
