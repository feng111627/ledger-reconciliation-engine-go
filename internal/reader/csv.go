package reader

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
	"github.com/feng111627/ledger-reconciliation-engine-go/internal/money"
)

const (
	colID        = "id"
	colAmount    = "amount"
	colCurrency  = "currency"
	colTime      = "time"
	colReference = "reference"
)

var timeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// ReadCSV 从 CSV 文件读取流水。
// 约定表头（顺序不限、大小写不敏感）：id,amount,currency,time,reference
// time 支持 RFC3339 / "2006-01-02T15:04:05" / "2006-01-02 15:04:05" / "2006-01-02"，
// 解析后统一转成 UTC 存储，保证两侧时间可比。
func ReadCSV(ctx context.Context, path string, src domain.Source) ([]domain.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return read(ctx, f, src, path)
}

func read(ctx context.Context, r io.Reader, src domain.Source, path string) ([]domain.Entry, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: read header: %w", path, err)
	}
	idx := mapHeader(header)
	for _, col := range []string{colID, colAmount, colCurrency, colTime} {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("%s: missing required column %q", path, col)
		}
	}
	if _, ok := idx[colReference]; !ok {
		idx[colReference] = -1
	}

	var entries []domain.Entry
	line := 1
	for {
		// header 是第 1 行，数据从第 2 行开始
		line++
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", path, line, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		// 跳过完全空白行
		if isBlankRecord(rec) {
			continue
		}
		get := func(col string) (string, error) {
			i := idx[col]
			if i < 0 || i >= len(rec) {
				return "", nil
			}
			return strings.TrimSpace(rec[i]), nil
		}
		id, err := get(colID)
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, fmt.Errorf("%s: line %d: empty id", path, line)
		}
		amtStr, _ := get(colAmount)
		amt, err := money.Parse(amtStr)
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: amount: %w", path, line, err)
		}
		cur, _ := get(colCurrency)
		cur = strings.ToUpper(cur)
		if cur == "" {
			return nil, fmt.Errorf("%s: line %d: empty currency", path, line)
		}
		ts, _ := get(colTime)
		t, err := parseTime(ts)
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: time: %w", path, line, err)
		}
		ref, _ := get(colReference)
		entries = append(entries, domain.Entry{
			ID:        id,
			Source:    src,
			Amount:    amt,
			Currency:  cur,
			Time:      t,
			Reference: ref,
			RawLine:   line,
		})
	}
	return entries, nil
}

func mapHeader(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(h))
		if key != "" {
			m[key] = i
		}
	}
	return m
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	var lastErr error
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

func isBlankRecord(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}
