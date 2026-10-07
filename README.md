# 本地对账与差异处理引擎（Go）

用两个**本地来源**的数据模拟双边对账：

- 我方账本导出（`local.csv`）
- 对方对账单（`remote.csv`）

不需要任何外部服务、真实凭据或收费资源，只用 Go 标准库即可在本地直接运行。
输出会逐笔说明：**对上了没有、属于哪一类差异、差多少、最终去了哪里**，
并通过资金守恒校验保证"没有一笔流水被悄悄丢掉"。

## 快速开始

```bash
# 1. 跑对账（仓库自带覆盖六类场景的示例数据）
go run ./cmd/reconcile --local testdata/local.csv --remote testdata/remote.csv \
  --store testdata/recon_state.json

# 2. 同样的数据再跑一遍 —— 报告逐字节稳定，不重复计数
go run ./cmd/reconcile

# 3. 人工处理一条差异（缺失侧的 ID 留空即可）
go run ./cmd/reconcile --resolve-local L4 --resolve-remote R4 --resolve-note "折扣，财务已审批"
go run ./cmd/reconcile --resolve-remote R8 --resolve-note "对方误发，已退回"

# 4. 自定义时间窗口与容差
go run ./cmd/reconcile --window 30m --tolerance 0.05 --currency-tolerance JPY:1
```

退出码：`0` 正常且资金守恒通过；`1` 运行错误；`2` 资金守恒校验失败。

## 输入文件格式

两个来源都是 CSV，首行为表头：

```csv
id,side,time,currency,amount,ref
L1,local,2026-10-01T09:00:00Z,CNY,100.00,ORD-1
```

| 列 | 含义 |
|----|------|
| `id` | 该侧流水号，同一文件内不可重复 |
| `side` | `local`（我方）或 `remote`（对方），留空时由文件名归属兜底 |
| `time` | 入账时间，RFC3339（UTC），用于时间窗口判定 |
| `currency` | 币种，统一转为大写；**币种不同绝不配对** |
| `amount` | 金额字符串，内部按 6 位小数定点数处理（无浮点误差） |
| `ref` | 业务参考号（订单号/业务号），相同 `ref` 视为同一笔业务 |

文件内行会先按 `(时间, ID)` 排序后再匹配，因此结论与文件行序无关。

## 匹配规则

匹配是**确定性**的：同样的输入，跑多少次、输入行序如何打乱、是否并发，
结论完全一致。

1. **分组**：有 `ref` 时按 `(ref, 币种)` 归入同一业务组；无 `ref` 时按 `(币种)`
   归入匿名组，仅靠金额与时间配对。
2. **时间窗口归位**：组内为每笔我方流水在对方侧选一个最佳候选，
   排序键为 `金额差绝对值 -> 时间距离 -> 对方 ID`（平局全部被打破，保证稳定）。
   优先选择时间窗口内的候选；时间相近但实际跨批次的两笔，不会被错误归到一起。
3. **判定优先级**（对一对待定配对）：
   - 币种不同 → 不构成配对，各自走缺失差异；
   - 时间差 **> 时间窗口** 且金额一致 → `time_out_of_window`（时间差）；
   - 金额完全相等且在窗口内 → 精确匹配 `matched`；
   - 金额差 **≤ 该币种容差** → `tolerated`，自动视为已核对；
   - 金额差超出容差 → `amount_mismatch`，进差异队列。
4. **剩余流水**：两侧没配出去的条目，若同业务组内已有一笔同金额完成配对，
   多出来的判为 `duplicate`（重复入账）；否则判为某侧缺失。
5. 每条流水最终**恰好出现在一条结论记录**中，重复运行不会重复计数。

## 容差设置

- 默认时间窗口：**10 分钟**（`--window` 覆盖，如 `30m`、`2h`）。
- 默认金额容差：**0.01**（`--tolerance` 覆盖）。
- 可按币种单独设置：`--currency-tolerance JPY:1,USD:0.01`，
  未列出的币种使用默认容差。
- 容差判定是 `差额绝对值 <= 容差`（边界包含），容差内自动核销但小差额仍计入
  报告的"已解释差额"，保证两来源总额仍然对得平。

## 差异分类与结论状态

| 差异类型 | 含义 | 初始结论 |
|----------|------|----------|
| `none` | 金额/币种/时间全部吻合（或容差内核销） | `matched` / `tolerated` |
| `amount_mismatch` | 金额对不上且超出容差 | `pending`（待人工处理） |
| `local_missing` | 对方有、我方没有 | `pending` |
| `remote_missing` | 我方有、对方没有 | `pending` |
| `duplicate` | 同一侧同业务同金额重复入账 | `duplicate`（需人工核销） |
| `time_out_of_window` | 金额币种一致但时间差超出窗口（跨批次） | `pending` |

人工处理（`--resolve-local/--resolve-remote/--resolve-note`）后状态变为
`resolved`。**已 `matched`/`tolerated`/`resolved` 的结论不可被覆盖**，
重跑对账只会复用历史结论，不会把已经处理过的结论冲掉。

## 幂等、并发与结论持久化

- 结论写入本地 JSON 文件（默认 `testdata/recon_state.json`），
  按配对键 `(localID, remoteID)` 去重，`PutIfAbsent` 语义：已存在就不覆盖。
- 文件通过 `flock` 排他锁保护：第二个对账进程/实例同时运行会**立即报错退出**，
  而不是互相覆盖；测试中包含 goroutine 并发写与跨进程锁两组场景。
- 报告的 `run id` 由输入数据与配置哈希派生，同一批数据 run id 不变。

## 资金守恒

每条结论带一个有符号差额 `Delta`（我方 - 对方；单边条目为该笔金额的正负值）。
逐币种恒等式始终成立：

```
我方总额 - 对方总额 = Σ 所有结论 Delta = 已解释差额 + 待处理差额
```

- `matched`/`tolerated`/`resolved` 的 Delta 计入"已解释差额"；
- `pending`/`duplicate` 的 Delta 计入"待处理差额"；
- 所有差异都被人工处理后，"待处理差额"归零，两个来源的账即对平。
  守恒校验不通过时代码以退出码 `2` 失败（属于程序 bug，而不是业务差异）。

## 本地验证

```bash
# 构建与静态检查
go build ./...
go vet ./...

# 全部单测（带竞态检测）
go test ./... -race -count=1

# 查看业务覆盖日志（中文用例名直接说明覆盖了哪些业务）
go test ./... -v -count=1

# 覆盖率
go test ./... -cover -count=1
```

单测覆盖三大业务场景：

1. **匹配主流程**（`TestMatchMainFlow` 等）：精确匹配、容差匹配、跨批次时间窗、
   金额不符、双侧重复、双侧缺失、乱序输入结论一致、跨币种不串配；
2. **差异分类与容差**（`TestClassifyPair`、`TestToleranceConfig`）：
   判定优先级、容差边界、超容差入队、时间超窗、币种隔离；
3. **幂等重跑与并发**（`TestEngineIdempotentRerun`、`TestStoreConcurrentLock` 等）：
   重复运行结论逐字节一致、结论库不重复写入、历史结论不可覆盖、
   `flock` 并发互斥、人工处理后守恒桶正确迁移。

## 代码结构

```
recon/
  types.go     领域类型：流水、差异类型、结论状态、配对、报告
  money.go     6 位小数定点数金额（解析/四则/比较），杜绝浮点误差
  config.go    时间窗口与按币种容差配置
  loader.go    两个 CSV 来源读取、校验与确定性排序
  classify.go  单对流水的差异分类（币种/时间窗/容差优先级）
  matcher.go   确定性 1:1 匹配、重复入账与缺失判定
  store.go     JSON 结论持久化 + flock 文件锁 + 人工处理
  engine.go    编排：匹配 -> 合并历史结论 -> 守恒汇总
  report.go    人读报告渲染
cmd/reconcile/ 命令行入口
testdata/      覆盖六类场景的示例数据
```
