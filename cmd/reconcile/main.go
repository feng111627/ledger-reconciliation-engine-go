package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/feng111627/ledger-reconciliation-engine-go/recon"
)

func main() {
	var (
		localPath     = flag.String("local", "testdata/local.csv", "我方账本 CSV")
		remotePath    = flag.String("remote", "testdata/remote.csv", "对方对账单 CSV")
		storePath     = flag.String("store", "testdata/recon_state.json", "结论持久化文件")
		timeWindow    = flag.Duration("window", 0, "时间窗口（默认 10m）")
		toleranceStr  = flag.String("tolerance", "", "默认金额容差，如 0.01")
		currencyTol   = flag.String("currency-tolerance", "", "按币种设置容差，如 USD:0.01,CNY:0.01")
		resolveLocal  = flag.String("resolve-local", "", "人工处理：我方流水号（缺失侧留空）")
		resolveRemote = flag.String("resolve-remote", "", "人工处理：对方流水号（缺失侧留空）")
		resolveNote   = flag.String("resolve-note", "", "人工处理说明")
	)
	flag.Parse()

	cfg := recon.DefaultConfig()
	if *timeWindow > 0 {
		cfg.TimeWindow = *timeWindow
	}
	if *toleranceStr != "" {
		m, err := recon.ParseMoney(*toleranceStr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid tolerance:", err)
			os.Exit(1)
		}
		cfg.DefaultTolerance = m
	}
	if *currencyTol != "" {
		if err := applyCurrencyTolerance(&cfg, *currencyTol); err != nil {
			fmt.Fprintln(os.Stderr, "invalid currency-tolerance:", err)
			os.Exit(1)
		}
	}

	st, err := recon.OpenStore(*storePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	defer st.Close()

	if *resolveLocal != "" || *resolveRemote != "" {
		if err := st.Resolve(*resolveLocal, *resolveRemote, *resolveNote); err != nil {
			fmt.Fprintln(os.Stderr, "resolve:", err)
			os.Exit(1)
		}
		fmt.Println("差异已标记为人工处理")
		return
	}

	locals, remotes, err := recon.ReadSources(*localPath, *remotePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read sources:", err)
		os.Exit(1)
	}

	eng := recon.NewEngine(cfg, st)
	rep, err := eng.Run(locals, remotes, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	if err := recon.RenderText(os.Stdout, rep); err != nil {
		fmt.Fprintln(os.Stderr, "render:", err)
		os.Exit(1)
	}
	if !rep.Conserved {
		os.Exit(2)
	}
}

func applyCurrencyTolerance(cfg *recon.Config, spec string) error {
	for _, item := range splitComma(spec) {
		cur, val, ok := cut(item, ":")
		if !ok {
			return fmt.Errorf("expect CUR:AMOUNT, got %q", item)
		}
		m, err := recon.ParseMoney(val)
		if err != nil {
			return err
		}
		if cfg.Tolerance == nil {
			cfg.Tolerance = map[string]recon.Money{}
		}
		cfg.Tolerance[cur] = m
	}
	return nil
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
