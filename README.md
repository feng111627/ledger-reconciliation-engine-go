# Ledger Reconciliation Engine（本地对账引擎）

用两个**本地** CSV 数据源模拟对账：我方账本导出（ledger）与对方对账单（statement）。
不依赖任何外部服务、数据库或凭据，只用 Go 标准库即可在本地直接运行。

引擎回答三个问题：

1. 每一笔流水对上了没有；
2. 没对上的**属于哪一类差异、差了多少钱**；
3. 两个来源的总金额是否对得上（资金守恒），每笔流水的去向是否都能解释。

---

## 1. 快速开始

```bash
# 运行对账（示例数据覆盖全部差异类型）
go run ./cmd/recon reconcile \
  --ledger testdata/ledger.csv \
  --statement testdata/statement.csv \
  --store .recon \
  --window 2m --ext-window 30m --tol 0.01

# 查看/处理差异
go run ./cmd/recon list --store .recon
go run ./cmd/recon resolve --store .recon \
  --key LONLY:L4 --status ACCEPTED --note "确认我方漏记，已补录"

# 处理完再跑一次：结论幂等命中，资金去向自动从"未决"迁移到"已处理"
go run ./cmd/recon reconcile ...   # 参数同上

# 全部单元测试（-race 验证并发安全，-v 查看各业务用例覆盖日志）
go test ./... -race -v
```

CSV 格式（表头顺序不限、大小写不敏感）：

```csv
id,amount,currency,time,reference
L1,100.00,USD,2026-10-01T10:00:00Z,INV-001
```

| 列 | 含义 |
|---|---|
| `id` | 来源内唯一流水号 |
| `amount` | 十进制金额，可为负；内部放大 1e6 用**整数**运算，杜绝浮点误差 |
| `currency` | 币种，读取时统一转大写，仅同币种参与匹配 |
| `time` | 支持 RFC3339 / `2006-01-02T15:04:05` / `2006-01-02 15:04:05` / `2006-01-02`，统一转 **UTC** 比较 |
| `reference` | 业务参考号（对方流水号/发票号），可空；非空且两侧相等时视为同一笔业务凭证 |

---

## 2. 匹配规则

匹配分两个阶段，全程**确定性**：不依赖 map 遍历顺序，所有"二选一"都按
`(时间差, 金额差绝对值, 对方时间, 对方流水号, 我方流水号)` 的全序比较裁决。
**同一份输入，无论文件行序、运行多少次、多少并发，结论逐笔一致。**

### 阶段一：业务参考号匹配
两侧 `reference` 非空且相等（且币种相同）时，视为同一笔业务凭证优先一对一配对，
**不受时间窗口限制**（跨批次、跨天入账也能正确归位）。

### 阶段二：时间就近匹配
剩余流水按我方时间顺序，逐条在对方流水中挑选全序意义上"最接近"的一条。候选必须同时满足：

- 币种相同；
- 时间差 ≤ **扩展窗口** `--ext-window`；
- 时间与金额**至少一个维度**在容忍范围内（两个维度都对不上的不强行配对，
  两侧各自落 `LEDGER_ONLY` / `STATEMENT_ONLY`，避免把不相干的两笔错配成"差异"）。

### 配对分类

| 分类 | 条件 |
|---|---|
| `MATCHED` | 时间差 ≤ 时间窗口，且金额完全相等（差 0） |
| `WITHIN_TOLERANCE` | 时间差 ≤ 时间窗口，且金额差绝对值 ≤ 容差（自动核对通过，**不进差异队列**） |
| `AMOUNT_MISMATCH` | 时间在窗口内，但金额差超出容差 |
| `TIME_MISMATCH` | 金额差在容差内（含相等），但时间差超出主窗口（参考号配对时不受扩展窗口限制；普通配对需在扩展窗口内） |

### 不配对 / 单侧与重复

| 分类 | 含义 |
|---|---|
| `LEDGER_ONLY` | 我方有、对方没有（差异差额 `Delta = 0 - 我方金额`） |
| `STATEMENT_ONLY` | 对方有、我方没有（`Delta = 对方金额 - 0`） |
| `DUPLICATE` | 同一侧内重复入账：① 流水号重复；或 ② (金额, 币种, 时间, reference) 完全相同。排序后的**第一条保留**参与匹配，其余全部标记重复 |

**没有任何一条流水会被悄悄丢掉**：每条输入要么进入某个配对，要么出现在未配对/重复列表中，
并有对应的差异记录与稳定 Key。测试 `全量流水去向可解释无丢失` 对此做了断言。

---

## 3. 容差设置

| 参数 | CLI | 默认 | 说明 |
|---|---|---|---|
| 时间窗口 | `--window` | `2m` | 跨批次流水允许的最大时间差，窗口内视为时间匹配 |
| 扩展窗口 | `--ext-window` | `30m` | 窗口外但金额一致时仍判 `TIME_MISMATCH`（进人工队列）；超出扩展窗口且无 reference 则两侧按漏单处理 |
| 金额容差 | `--tol` | `0.01` | 金额差绝对值在此之内（且时间在窗口内）自动算作已核对；超出进差异队列 |

容差按"货币单位"传入（如 `0.01`），内部同样转成 1e6 放大整数比较。
容差只影响**自动核对**的边界，任何自动通过的配对仍会在报告中列出，可审计。

---

## 4. 差异分类与处理

运行 `reconcile` 后，所有需要人工介入的差异进入**差异队列**，每条带：

- 稳定 `Key`（如 `PAIR:L2|S2`、`LONLY:L4`、`SONLY:S7`、`DUP:LEDGER:L6`），
  Key 完全由输入决定，是幂等与结论持久化的基础；
- 差异类型、双方流水号、币种、带符号差额 `Delta`（恒为 `对方 - 我方`）；
- 人类可读的 `Detail`（差在哪、差多少、相差多少秒）。

人工处理：

```bash
go run ./cmd/recon resolve --key <DIFF_KEY> --status ACCEPTED --note "..."
# status: ACCEPTED（确认差异属实，接受） | WRITE_OFF（核销）
```

结论按 Key 单独保存在 `.recon/resolutions.json`，**自动对账流程永不覆盖它**；
对同一 Key 重复处理是幂等更新（改状态/备注），不会产生重复记录。

### 资金守恒（每笔钱都有着落）

对每个币种，引擎强制恒等式成立：

```
对方总额 - 我方总额 = MatchedDelta + OpenDelta + ResolvedDelta
```

- `MatchedDelta`：已自动核对配对（MATCHED / WITHIN_TOLERANCE）的 `对方-我方` 差额之和；
- `OpenDelta`：差异队列中**未决**差额之和；
- `ResolvedDelta`：已人工处理差异的差额之和。

任何一笔待处理的配对差异只在等式右侧出现一次（不重复计数）。
恒等式不成立时 CLI 会打印 `❌`，`recon.VerifyConservation` 返回错误；
正常输入下恒等式恒成立，单测对多个数据集做了数值断言。

---

## 5. 幂等、并发与本地存储

状态全部落在本地目录（默认 `.recon/`），无外部依赖：

```
.recon/
├── .lock                 # flock 独占文件锁（运行期间持有）
├── runs/<指纹>.json      # 每次对账的完整结果（按输入指纹命名）
└── resolutions.json      # 人工处理结论 map[差异Key]结论
```

- **输入指纹**：对两侧流水（id/金额/币种/时间/reference，按来源+id 归一排序）与配置求 SHA-256。
  相同输入（与文件行序无关）永远得到同一指纹。
- **幂等重跑**：命中指纹时直接返回上一次结果（CLI 标注 `幂等命中`），
  不重新计数、不生成新 RunID、`runs/` 下不新增文件；输入变化才产生新指纹。
- **结论不被覆盖**：差异 Key 稳定，人工结论独立存储；即使重跑命中缓存，
  资金汇总也会结合当前最新结论重新计算（未决↔已处理的迁移立即可见）。
- **并发安全**：进程内 `sync.Mutex` + 跨进程 `flock(LOCK_EX)` 双重串行化，
  原子写（临时文件 + rename）防半截文件。单测包含 8 goroutine 并发运行、
  文件锁互斥等待两个场景，使用 `go test -race` 验证无数据竞争。

---

## 6. 本地验证方法

```bash
go build ./...          # 编译
go vet ./...            # 静态检查
go test ./... -race -v  # 全量单测（含竞态检测；-v 日志按"业务:xxx 用例:yyy ... PASS"
                        # 打印覆盖了哪些业务及每个用例的通过情况）
```

单元测试覆盖的业务场景：

| 业务 | 代表用例 |
|---|---|
| 匹配主流程 | 完全匹配、跨批次时间窗口内就近归位、参考号跨窗口归位、币种不同不配对 |
| 差异分类 | 容差内自动核对、超容差金额差异、时间差（扩展窗口内外）、金额时间均不符、两种单边缺失、重复入账、全量流水无丢失 |
| 确定性 | 乱序输入与重复运行逐笔结论一致 |
| 幂等重跑 | 相同输入同一 RunID 且只落盘一次、行序打乱指纹不变、输入变化生成新指纹 |
| 差异处理 | 结论跨重跑保留、资金从未决迁移到已处理、同 Key 重复处理幂等更新 |
| 资金守恒 | 多币种/含全部差异类型数据的总额、残差与去向数值断言 |
| 并发安全 | flock 互斥与释放、8 并发结果唯一且守恒（-race） |
| 金额精度 | 整数放大解析/格式化、负数、非法输入拒绝 |
| CSV 读取 | 表头映射、币种大写、金额整数化、时间 UTC 归一、缺列报错 |

端到端手动验证：

```bash
rm -rf /tmp/recon-demo
go run ./cmd/recon reconcile --ledger testdata/ledger.csv \
  --statement testdata/statement.csv --store /tmp/recon-demo \
  --window 2m --ext-window 30m --tol 0.01      # 观察 MATCHED/差异/守恒
go run ./cmd/recon reconcile ...               # 第二次：幂等命中
go run ./cmd/recon resolve --store /tmp/recon-demo --key LONLY:L4 --status ACCEPTED --note 补录
go run ./cmd/recon reconcile ...               # 未决 -> 已处理，守恒仍 ✅
```

## 7. 目录结构

```
cmd/recon/main.go          CLI：reconcile / list / resolve
internal/domain/types.go   核心领域类型（Entry/Pair/Discrepancy/Config/RunResult）
internal/money/            十进制金额 <-> 1e6 整数
internal/reader/           CSV 读取与标准化
internal/matcher/          纯函数匹配与差异分类（确定性、无副作用）
internal/store/            本地持久化、文件锁、幂等存取
internal/recon/            编排：指纹、幂等、守恒校验、差异处理
testdata/                  示例 CSV（含全部差异类型）
```
