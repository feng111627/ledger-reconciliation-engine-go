package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/money"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/recon"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "reconcile", "recon":
		return cmdReconcile(args[1:])
	case "list":
		return cmdList(args[1:])
	case "resolve":
		return cmdResolve(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ledger-reconciliation: 本地双来源对账工具

用法:
  recon reconcile --ledger LEDGER.csv --statement STATEMENT.csv
                  [--store .recon] [--window 2m] [--ext-window 30m] [--tol 0.01]
  recon list      [--store .recon] [--resolved]
  recon resolve   --key DIFF_KEY --status ACCEPTED|WRITE_OFF [--note "..."] [--store .recon]

说明:
  CSV 表头: id,amount,currency,time,reference
  time 支持 RFC3339 / "2006-01-02 15:04:05" / "2006-01-02"，统一按 UTC 比较。
`)
}

func openStore(dir string) (*store.Store, func(), error) {
	return store.Open(dir)
}

func cmdReconcile(args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	ledgerPath := fs.String("ledger", "", "我方账本 CSV 路径")
	statementPath := fs.String("statement", "", "对方对账单 CSV 路径")
	storeDir := fs.String("store", ".recon", "本地状态目录")
	window := fs.Duration("window", 2*time.Minute, "时间窗口（窗口内跨批次流水视为同笔时间匹配）")
	extWindow := fs.Duration("ext-window", 30*time.Minute, "扩展窗口：超窗但金额一致时判 TIME_MISMATCH 而非两边漏单")
	tolStr := fs.String("tol", "0.01", "金额容差（货币单位，绝对值），容差内自动核对通过")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ledgerPath == "" || *statementPath == "" {
		return fmt.Errorf("--ledger 与 --statement 为必填参数")
	}
	tol, err := money.Parse(*tolStr)
	if err != nil {
		return fmt.Errorf("--tol: %w", err)
	}
	cfg := domain.Config{
		TimeWindow:      *window,
		AmountTolerance: tol,
		ExtendedWindow:  *extWindow,
	}

	st, cleanup, err := openStore(*storeDir)
	if err != nil {
		return err
	}
	defer cleanup()

	eng := recon.New(st, cfg)
	r, cached, err := eng.Run(context.Background(), *ledgerPath, *statementPath)
	if err != nil {
		return err
	}
	if err := recon.VerifyConservation(r); err != nil {
		fmt.Fprintln(os.Stderr, "⚠ 资金守恒校验未通过:", err)
	}

	mode := "新对账"
	if cached {
		mode = "幂等命中（复用既有结论，未重复计数）"
	}
	fmt.Printf("运行 %s | 输入指纹 %s | %s\n", r.RunID, r.InputFingerprint[:12], mode)
	fmt.Printf("参数: 时间窗口=%s 扩展窗口=%s 金额容差=%s\n",
		cfg.TimeWindow, cfg.ExtendedWindow, money.Format(cfg.AmountTolerance))

	counts := map[domain.DiffType]int{}
	for _, p := range r.Pairs {
		counts[p.Diff]++
	}
	fmt.Printf("配对 %d 笔（MATCHED=%d, WITHIN_TOLERANCE=%d, AMOUNT_MISMATCH=%d, TIME_MISMATCH=%d）\n",
		len(r.Pairs), counts[domain.DiffMatched], counts[domain.DiffWithinTol],
		counts[domain.DiffAmountMismatch], counts[domain.DiffTimeMismatch])
	fmt.Printf("差异 %d 条待处理，重复入账 %d 条，未配对流水 %d 条\n",
		len(r.Discrepancies), len(r.Duplicates), len(r.Unpaired))

	if len(r.Discrepancies) > 0 {
		fmt.Println("\n== 差异队列 ==")
		for _, d := range r.Discrepancies {
			res, _ := st.GetResolution(d.Key)
			status := string(domain.StatusPending)
			if res != nil {
				status = string(res.Status)
			}
			fmt.Printf("[%s] %s | %s %s | key=%s\n  %s\n",
				status, d.Type, d.Currency, money.Format(d.Delta), d.Key, d.Detail)
		}
	}

	fmt.Println("\n== 资金守恒（按币种）==")
	curs := make([]string, 0, len(r.CurrencyTotals))
	for c := range r.CurrencyTotals {
		curs = append(curs, c)
	}
	sort.Strings(curs)
	for _, c := range curs {
		t := r.CurrencyTotals[c]
		fmt.Printf("%s: 我方=%s 对方=%s 残差=%s | 已配对差=%s 未决=%s 已处理=%s\n",
			c, money.Format(t.LedgerTotal), money.Format(t.StatementTotal), money.Format(t.Residual),
			money.Format(t.MatchedDelta), money.Format(t.OpenDelta), money.Format(t.ResolvedDelta))
	}
	if r.ConservationOK {
		fmt.Println("守恒校验: ✅ 残差 == 已配对差 + 未决 + 已处理，资金去向全部可解释")
	} else {
		fmt.Println("守恒校验: ❌ 不成立")
	}
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	storeDir := fs.String("store", ".recon", "本地状态目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, cleanup, err := openStore(*storeDir)
	if err != nil {
		return err
	}
	defer cleanup()
	rs, err := st.ListResolutions()
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		fmt.Println("（暂无人工处理结论）")
		return nil
	}
	for _, r := range rs {
		fmt.Printf("%s\t%s\t%s\t%s\n", r.Key, r.Status, r.UpdatedAt.Format(time.RFC3339), r.Note)
	}
	return nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	key := fs.String("key", "", "差异稳定 Key（reconcile 输出中给出）")
	status := fs.String("status", "", "ACCEPTED 或 WRITE_OFF")
	note := fs.String("note", "", "处理备注")
	storeDir := fs.String("store", ".recon", "本地状态目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" || *status == "" {
		return fmt.Errorf("--key 与 --status 为必填参数")
	}
	st, cleanup, err := openStore(*storeDir)
	if err != nil {
		return err
	}
	defer cleanup()
	eng := recon.New(st, domain.Config{})
	if err := eng.Resolve(*key, domain.ResolveStatus(*status), *note); err != nil {
		return err
	}
	fmt.Printf("已记录处理结论: %s -> %s\n", *key, *status)
	return nil
}
