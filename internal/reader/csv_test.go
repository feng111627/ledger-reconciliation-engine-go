package reader

import (
	"context"
	"strings"
	"testing"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
)

func TestReadCSV(t *testing.T) {
	csvData := `id,amount,currency,time,reference
L1,100.00,usd,2026-10-01 10:00:00,R1
L2,-5.50,USD,2026-10-01T11:00:00Z,
`
	es, err := read(context.Background(), strings.NewReader(csvData), domain.SourceLedger, "inline.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("期望 2 条，得到 %d", len(es))
	}
	if es[0].ID != "L1" || es[0].Currency != "USD" || es[0].Amount != 100_000_000 {
		t.Fatalf("第一条解析异常: %+v", es[0])
	}
	if es[0].Reference != "R1" || es[1].Reference != "" {
		t.Fatalf("reference 解析异常")
	}
	if es[1].Amount != -5_500_000 {
		t.Fatalf("负数金额解析异常: %d", es[1].Amount)
	}
	if es[0].Source != domain.SourceLedger || es[0].RawLine != 2 {
		t.Fatalf("来源/行号异常: %+v", es[0])
	}
	// 两种时间格式都归一到 UTC
	if es[0].Time.UTC().Format("15:04") != "10:00" || es[1].Time.UTC().Hour() != 11 {
		t.Fatalf("时间解析/UTC 归一异常: %v %v", es[0].Time, es[1].Time)
	}
	t.Log("[业务:CSV读取] 用例:表头映射/币种大写/金额整数化/时间UTC归一 ... PASS")

	// 缺少必需列
	bad := `id,amount
x,1
`
	if _, err := read(context.Background(), strings.NewReader(bad), domain.SourceLedger, "bad.csv"); err == nil {
		t.Fatal("缺少 currency/time 列应当报错")
	}
	t.Log("[业务:CSV读取] 用例:缺少必需列报错 ... PASS")
}
