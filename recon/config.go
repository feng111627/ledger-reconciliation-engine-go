package recon

import "time"

// Config 对账配置。
type Config struct {
	// TimeWindow 时间窗口：金额币种相同、Ref 相同的两笔，
	// 时间差 <= TimeWindow 视为同一批次内；超出则归为时间差差异。
	TimeWindow time.Duration
	// Tolerance 每个币种允许的金额容差（绝对值），容差内自动视为已核对。
	Tolerance map[string]Money
	// DefaultTolerance 币种未配置时使用的默认容差。
	DefaultTolerance Money
}

// DefaultConfig 返回一份合理的默认配置：
// 时间窗口 10 分钟，默认容差 0.01，常用法币容差 0.01。
func DefaultConfig() Config {
	cent := MustParseMoney("0.01")
	return Config{
		TimeWindow:       10 * time.Minute,
		DefaultTolerance: cent,
		Tolerance: map[string]Money{
			"CNY": cent,
			"USD": cent,
			"EUR": cent,
		},
	}
}

// ToleranceFor 返回指定币种的容差。
func (c Config) ToleranceFor(cur string) Money {
	if t, ok := c.Tolerance[cur]; ok {
		return t
	}
	return c.DefaultTolerance
}
