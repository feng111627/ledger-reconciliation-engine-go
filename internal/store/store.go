package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/feng111627/ledger-reconciliation-engine-go/internal/domain"
)

// Store 本地持久化：
//   - 进程内 sync.Mutex + 文件锁(flock)，保证并发/多进程运行串行化，
//     不会出现竞态导致的重复处理；
//   - 每次运行按输入指纹保存结果：相同指纹重复运行直接复用，不重复计数；
//   - 人工结论按差异的稳定 Key 单独保存，自动流程永不覆盖。
//
// 目录布局：
//
//	<dir>/.lock                 文件锁（运行期间独占）
//	<dir>/runs/<fingerprint>.json  每次运行的完整结果
//	<dir>/resolutions.json      人工处理结论 map[key]Resolution
type Store struct {
	dir      string
	lockFile *os.File
	mu       sync.Mutex
}

// Open 打开（必要时创建）本地存储并对整个存储目录取独占文件锁。
// 返回的 cleanup 释放锁；同一时刻只有一个进程能持有锁。
func Open(dir string) (*Store, func(), error) {
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create store dir: %w", err)
	}
	lf, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		lf.Close()
		return nil, nil, fmt.Errorf("acquire file lock: %w", err)
	}
	s := &Store{dir: dir, lockFile: lf}
	cleanup := func() {
		syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
	}
	return s, cleanup, nil
}

var errNotFound = errors.New("not found")

// LoadRun 按输入指纹读取已缓存的运行结果；不存在返回 (nil, nil)。
func (s *Store) LoadRun(fp string) (*domain.RunResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.runPath(fp))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r domain.RunResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode cached run %s: %w", fp, err)
	}
	return &r, nil
}

// SaveRunResult 幂等保存一次运行结果。
// 若相同指纹的结果已存在，直接返回已有 RunID，绝不覆盖（也不覆盖其结论）。
func (s *Store) SaveRunResult(fp string, r *domain.RunResult) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.runPath(fp)
	if data, err := os.ReadFile(path); err == nil {
		var existing domain.RunResult
		if err := json.Unmarshal(data, &existing); err == nil && existing.RunID != "" {
			return existing.RunID, nil
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := atomicWrite(path, data); err != nil {
		return "", err
	}
	return r.RunID, nil
}

// GetResolution 读取一条差异的人工结论；不存在返回 nil。
func (s *Store) GetResolution(key string) (*domain.Resolution, error) {
	all, err := s.readResolutions()
	if err != nil {
		return nil, err
	}
	if r, ok := all[key]; ok {
		return &r, nil
	}
	return nil, nil
}

// PutResolution 保存（更新）人工结论。只有显式的人工操作会写这里，
// 自动对账流程不会触碰，因此已处理结论不会被重跑覆盖。
func (s *Store) PutResolution(r domain.Resolution) error {
	if r.Key == "" {
		return errors.New("resolution key is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.readResolutionsLocked()
	if err != nil {
		return err
	}
	all[r.Key] = r
	return s.writeResolutionsLocked(all)
}

// ListResolutions 列出全部人工结论，按 Key 排序，输出确定。
func (s *Store) ListResolutions() ([]domain.Resolution, error) {
	all, err := s.readResolutions()
	if err != nil {
		return nil, err
	}
	out := make([]domain.Resolution, 0, len(all))
	for _, r := range all {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) runPath(fp string) string {
	return filepath.Join(s.dir, "runs", fp+".json")
}

func (s *Store) resolutionsPath() string {
	return filepath.Join(s.dir, "resolutions.json")
}

func (s *Store) readResolutions() (map[string]domain.Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readResolutionsLocked()
}

func (s *Store) readResolutionsLocked() (map[string]domain.Resolution, error) {
	data, err := os.ReadFile(s.resolutionsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]domain.Resolution{}, nil
	}
	if err != nil {
		return nil, err
	}
	var all map[string]domain.Resolution
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("decode resolutions: %w", err)
	}
	if all == nil {
		all = map[string]domain.Resolution{}
	}
	return all, nil
}

func (s *Store) writeResolutionsLocked(all map[string]domain.Resolution) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.resolutionsPath(), data)
}

// atomicWrite 先写临时文件再 rename，保证崩溃/并发下不会出现半截文件。
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
