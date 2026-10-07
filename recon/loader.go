package recon

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// LoadCSV 从 CSV 文件读取流水。
// 列格式（首行为表头）：id,side,time,currency,amount,ref
// side 取值 local / remote；side 为空时由 defaultSide 兜底。
// 读取结果按 (时间, ID) 排序，保证后续匹配不依赖文件中的行顺序。
func LoadCSV(path string, defaultSide Side) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return parseCSV(f, defaultSide, path)
}

func parseCSV(r io.Reader, defaultSide Side, path string) ([]Entry, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: read header: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(rec []string, name string) (string, bool) {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return "", false
		}
		return strings.TrimSpace(rec[i]), true
	}

	var out []Entry
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", path, line, err)
		}

		id, _ := get(rec, "id")
		if id == "" {
			return nil, fmt.Errorf("%s: line %d: missing id", path, line)
		}
		side := Side(strings.ToLower(mustGet(get, rec, "side", "")))
		if side == "" {
			side = defaultSide
		}
		if side != SideLocal && side != SideRemote {
			return nil, fmt.Errorf("%s: line %d: invalid side %q", path, line, side)
		}

		ts, _ := get(rec, "time")
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: invalid time %q: %w", path, line, ts, err)
		}
		cur, _ := get(rec, "currency")
		cur = strings.ToUpper(cur)
		if cur == "" {
			return nil, fmt.Errorf("%s: line %d: missing currency", path, line)
		}
		amtStr, _ := get(rec, "amount")
		amt, err := ParseMoney(amtStr)
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: invalid amount %q: %w", path, line, amtStr, err)
		}
		ref, _ := get(rec, "ref")

		out = append(out, Entry{
			ID:       id,
			Side:     side,
			Currency: cur,
			Amount:   amt,
			Time:     t.UTC(),
			Ref:      ref,
			Raw:      strings.Join(rec, ","),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].ID < out[j].ID
		}
		return out[i].Time.Before(out[j].Time)
	})
	return out, nil
}

func mustGet(get func([]string, string) (string, bool), rec []string, name, def string) string {
	if v, ok := get(rec, name); ok && v != "" {
		return v
	}
	return def
}

// ReadSources 一次读取我方账本与对方对账单两个来源，
// 并校验各文件内的 side 归属与 ID 不重复。
func ReadSources(localPath, remotePath string) (local []Entry, remote []Entry, err error) {
	local, err = LoadCSV(localPath, SideLocal)
	if err != nil {
		return nil, nil, err
	}
	remote, err = LoadCSV(remotePath, SideRemote)
	if err != nil {
		return nil, nil, err
	}
	if err := checkSide(local, SideLocal, localPath); err != nil {
		return nil, nil, err
	}
	if err := checkSide(remote, SideRemote, remotePath); err != nil {
		return nil, nil, err
	}
	if err := checkUniqueID(local, localPath); err != nil {
		return nil, nil, err
	}
	if err := checkUniqueID(remote, remotePath); err != nil {
		return nil, nil, err
	}
	return local, remote, nil
}

func checkSide(es []Entry, want Side, path string) error {
	for _, e := range es {
		if e.Side != want {
			return fmt.Errorf("%s: entry %s has side %s, want %s", path, e.ID, e.Side, want)
		}
	}
	return nil
}

func checkUniqueID(es []Entry, path string) error {
	seen := make(map[string]bool, len(es))
	for _, e := range es {
		if seen[e.ID] {
			return fmt.Errorf("%s: duplicate id %s", path, e.ID)
		}
		seen[e.ID] = true
	}
	return nil
}
