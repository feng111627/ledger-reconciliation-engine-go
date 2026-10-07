package recon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// ErrNotFound 表示要人工处理的差异配对不存在。
var ErrNotFound = errors.New("pair not found")

// StoredPair 是持久化的结论记录，幂等重跑时据此恢复"已处理"结论。
type StoredPair struct {
	LocalID   string   `json:"local_id"`
	RemoteID  string   `json:"remote_id"`
	Diff      DiffType `json:"diff"`
	Status    Status   `json:"status"`
	Delta     string   `json:"delta"`
	Note      string   `json:"note"`
	DecidedAt string   `json:"decided_at"`
}

type storeData struct {
	Pairs []StoredPair `json:"pairs"`
}

// Store 结论持久化（本地 JSON 文件）。
// 通过文件锁（flock）保证同一时刻只有一个对账实例写入，
// 避免并发/多次运行时重复处理或漏处理。
type Store struct {
	path string
	f    *os.File
}

// OpenStore 打开（必要时创建）结论库并对文件加排他锁。
// 锁在 Close 时释放；另一个进程并发打开会直接失败而不是互相覆盖。
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("store is locked by another run: %w", err)
	}
	return &Store{path: path, f: f}, nil
}

func (s *Store) readAll() (storeData, error) {
	var data storeData
	fi, err := s.f.Stat()
	if err != nil {
		return data, err
	}
	if fi.Size() == 0 {
		return data, nil
	}
	if _, err := s.f.Seek(0, 0); err != nil {
		return data, err
	}
	if err := json.NewDecoder(s.f).Decode(&data); err != nil {
		return data, fmt.Errorf("decode store: %w", err)
	}
	return data, nil
}

func (s *Store) writeAll(data storeData) error {
	sort.SliceStable(data.Pairs, func(i, j int) bool {
		if data.Pairs[i].LocalID != data.Pairs[j].LocalID {
			return data.Pairs[i].LocalID < data.Pairs[j].LocalID
		}
		return data.Pairs[i].RemoteID < data.Pairs[j].RemoteID
	})
	if err := s.f.Truncate(0); err != nil {
		return err
	}
	if _, err := s.f.Seek(0, 0); err != nil {
		return err
	}
	enc := json.NewEncoder(s.f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return err
	}
	return s.f.Sync()
}

// Load 读取全部已存结论。
func (s *Store) Load() ([]StoredPair, error) {
	data, err := s.readAll()
	if err != nil {
		return nil, err
	}
	return data.Pairs, nil
}

// PutIfAbsent 仅当该配对键不存在时写入，返回是否实际写入。
// 重跑时已存在的结论不会被覆盖（matched/tolerated/resolved 均原样保留）。
func (s *Store) PutIfAbsent(p StoredPair) (bool, error) {
	data, err := s.readAll()
	if err != nil {
		return false, err
	}
	key := pairKey(p.LocalID, p.RemoteID)
	for _, ex := range data.Pairs {
		if pairKey(ex.LocalID, ex.RemoteID) == key {
			return false, nil
		}
	}
	if p.DecidedAt == "" {
		p.DecidedAt = now().UTC().Format(time.RFC3339)
	}
	data.Pairs = append(data.Pairs, p)
	if err := s.writeAll(data); err != nil {
		return false, err
	}
	return true, nil
}

// Resolve 人工处理一条差异：仅允许 pending/duplicate -> resolved。
// 已 resolved/matched/tolerated 的结论不可被覆盖。
func (s *Store) Resolve(localID, remoteID string, note string) error {
	data, err := s.readAll()
	if err != nil {
		return err
	}
	key := pairKey(localID, remoteID)
	for i := range data.Pairs {
		if pairKey(data.Pairs[i].LocalID, data.Pairs[i].RemoteID) == key {
			switch data.Pairs[i].Status {
			case StatusResolved, StatusMatched, StatusTolerated:
				return fmt.Errorf("pair %s 已是 %s 状态，结论不可覆盖", key, data.Pairs[i].Status)
			}
			data.Pairs[i].Status = StatusResolved
			data.Pairs[i].Note = note
			data.Pairs[i].DecidedAt = now().UTC().Format(time.RFC3339)
			return s.writeAll(data)
		}
	}
	return fmt.Errorf("%w: %s", ErrNotFound, key)
}

// Close 释放锁与文件句柄。
func (s *Store) Close() error {
	if s.f == nil {
		return nil
	}
	_ = syscall.Flock(int(s.f.Fd()), syscall.LOCK_UN)
	return s.f.Close()
}

// pairKey 配对唯一键：至少一侧有 ID，用 \x00 分隔避免普通字符冲突。
func pairKey(localID, remoteID string) string {
	return localID + "\x00" + remoteID
}
