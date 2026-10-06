package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type MTUInfo struct {
	AdapterID string `json:"adapter_id"`
	GUID      string `json:"guid"`
	IfIndex   int    `json:"if_index"`
	Address   string `json:"address"`
	Current   int    `json:"current"`
	Original  int    `json:"original,omitempty"`
}
type MTUResult struct {
	MTUInfo
	Target      string    `json:"target"`
	Recommended int       `json:"recommended"`
	AtLimit     bool      `json:"at_limit"`
	TestedAt    time.Time `json:"tested_at"`
}
type MTUBatchItem struct {
	AdapterID string `json:"adapter_id"`
	GUID      string `json:"guid"`
	Address   string `json:"address"`
	Before    int    `json:"before"`
	After     int    `json:"after"`
	Original  int    `json:"original"`
	Changed   bool   `json:"changed"`
	Error     string `json:"error,omitempty"`
}

const (
	mtuBatchMax      = 32
	mtuSetRPC        = "mtu.set"
	errMTUOutOfRange = "请输入 576–65535 之间的 MTU 值"
	errMTUNoAdapter  = "请至少选择一张网卡"
	errMTUTooMany    = "一次最多修改 32 张网卡"
	errMTUNoOriginal = "没有已保存的原值"
)

type MTUService struct {
	mu     sync.Mutex
	engine *EngineService
	path   string
	result *MTUResult
	cancel context.CancelFunc
	// Injectable seams: nil means the real Windows read and the engine RPC.
	readInfo     func(context.Context, string) (MTUInfo, error)
	setMTU       func(context.Context, MTUInfo, int) error
	preflight    func(context.Context) (func(), error)
	preflightSet bool
	checkEngine  func(context.Context) error
}

func NewMTUService(engine *EngineService, settings *SettingsService) *MTUService {
	return &MTUService{engine: engine, path: filepath.Join(filepath.Dir(settings.ConfigPath()), "mtu-originals.json")}
}
func (s *MTUService) originals() (map[string]int, error) {
	values := map[string]int{}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("读取 MTU 恢复记录失败：%w", err)
	}
	if values == nil {
		values = map[string]int{}
	}
	return values, nil
}
func (s *MTUService) saveOriginal(guid string, value int) error {
	values, err := s.originals()
	if err != nil {
		return err
	}
	if values[guid] != 0 {
		return nil
	}
	values[guid] = value
	return s.writeOriginals(values)
}
func (s *MTUService) clearOriginal(guid string) error {
	values, err := s.originals()
	if err != nil {
		return err
	}
	delete(values, guid)
	return s.writeOriginals(values)
}
func (s *MTUService) writeOriginals(values map[string]int) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), "mtu-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.path)
}
func (s *MTUService) readCurrent(ctx context.Context, id string) (MTUInfo, error) {
	if s.readInfo != nil {
		return s.readInfo(ctx, id)
	}
	return readMTU(ctx, id)
}
func (s *MTUService) read(ctx context.Context, id string) (MTUInfo, error) {
	info, err := s.readCurrent(ctx, id)
	if err != nil {
		return info, err
	}
	values, err := s.originals()
	if err != nil {
		return info, err
	}
	info.Original = values[info.GUID]
	return info, nil
}
func (s *MTUService) Current(id string) (MTUInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.read(ctx, id)
}

// Hold the same lifecycle gate as Start/Stop for the whole operation.
func (s *MTUService) stopped(ctx context.Context) (func(), error) {
	if err := s.engine.acquireLifecycle(ctx); err != nil {
		return nil, err
	}
	release := s.engine.releaseLifecycle
	s.engine.mu.Lock()
	closing := s.engine.closing
	s.engine.mu.Unlock()
	if closing {
		release()
		return nil, errors.New("HypoMux 正在退出")
	}
	if s.engine.client.Hello().ProtocolVersion != 0 {
		var status engineStatusResult
		if err := s.engine.client.Request(ctx, "engine.status", nil, &status); err != nil {
			release()
			return nil, err
		}
		if status.Engine.State != "stopped" && status.Engine.State != "failed" {
			release()
			return nil, errors.New("请先停止网络服务，再检测或修改 MTU")
		}
	}
	return release, nil
}
func (s *MTUService) Detect(id, target string) (MTUResult, error) {
	ip := net.ParseIP(target)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || ip.Equal(net.IPv4bcast) {
		return MTUResult{}, errors.New("请输入有效的单播 IPv4 目标地址")
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return MTUResult{}, errors.New("MTU 检测已在运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	s.cancel = cancel
	s.result = nil
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
	release, err := s.stopped(ctx)
	if err != nil {
		return MTUResult{}, err
	}
	defer release()
	s.mu.Lock()
	info, err := s.read(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return MTUResult{}, err
	}
	if info.Address == target {
		return MTUResult{}, errors.New("目标地址不能是所选网卡自身")
	}
	value, err := findMTU(ctx, info.Current, func(ctx context.Context, size int) (bool, error) { return probeMTU(ctx, info.Address, target, size) })
	if err != nil {
		return MTUResult{}, err
	}
	// A changed address or interface invalidates the entire measurement.
	after, err := readMTU(ctx, id)
	if err != nil {
		return MTUResult{}, err
	}
	if after.GUID != info.GUID || after.Address != info.Address || after.Current != info.Current || after.IfIndex != info.IfIndex {
		return MTUResult{}, errors.New("检测期间网卡配置已变化，请重新检测")
	}
	result := MTUResult{MTUInfo: info, Target: target, Recommended: value, AtLimit: value == info.Current, TestedAt: time.Now()}
	s.mu.Lock()
	s.result = &result
	s.mu.Unlock()
	return result, nil
}
func (s *MTUService) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// Only an explicit packet-too-big response narrows the search. Loss is unknown,
// never evidence that a packet needs fragmentation. Include IPv4 + ICMP headers.
func findMTU(ctx context.Context, ceiling int, probe func(context.Context, int) (bool, error)) (int, error) {
	if ceiling < 576 || ceiling > 65535 {
		return 0, errors.New("当前 IPv4 MTU 超出支持范围（576–65535）")
	}
	check := func(size int) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return probe(ctx, size)
	}
	ok, err := check(576)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("最小探测包仍需分片，无法给出推荐值")
	}
	low, high := 576, ceiling
	for low < high {
		mid := (low + high + 1) / 2
		ok, err = check(mid)
		if err != nil {
			return 0, err
		}
		if ok {
			low = mid
		} else {
			high = mid - 1
		}
	}
	for i := 0; i < 3; i++ {
		ok, err = check(low)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, errors.New("复测结果不一致，请稍后重试")
		}
	}
	return low, nil
}
func (s *MTUService) Apply(id string) (MTUInfo, error)   { return s.change(id, false) }
func (s *MTUService) Restore(id string) (MTUInfo, error) { return s.change(id, true) }
func (s *MTUService) change(id string, restore bool) (MTUInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return MTUInfo{}, errors.New("请等待 MTU 检测完成")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	release, err := s.stopped(ctx)
	if err != nil {
		return MTUInfo{}, err
	}
	defer release()
	info, err := s.read(ctx, id)
	if err != nil {
		return info, err
	}
	value := info.Original
	if !restore {
		r := s.result
		if r == nil || r.AdapterID != id || r.GUID != info.GUID || r.Address != info.Address || r.IfIndex != info.IfIndex || r.Current != info.Current || time.Since(r.TestedAt) > 5*time.Minute {
			return info, errors.New("检测结果已失效，请重新检测")
		}
		value = r.Recommended
	}
	if value < 576 || value > 65535 {
		return info, errors.New("没有有效的 MTU 恢复记录或检测结果")
	}
	if value == info.Current {
		if restore {
			if err := s.clearOriginal(info.GUID); err != nil {
				return info, err
			}
			info.Original = 0
		}
		return info, nil
	}
	hello, err := s.engine.client.EnsureElevated(ctx)
	if err != nil {
		return info, err
	}
	if !slices.Contains(hello.Capabilities, "mtu.set") {
		return info, errors.New("请更新 Core 后再修改 MTU")
	}
	if !restore {
		if err = s.saveOriginal(info.GUID, info.Current); err != nil {
			return info, fmt.Errorf("保存原 MTU 失败，未执行修改：%w", err)
		}
	}
	var ignored map[string]any
	err = s.engine.client.Request(ctx, "mtu.set", map[string]any{"if_index": info.IfIndex, "guid": info.GUID, "expected": info.Current, "value": value}, &ignored)
	s.result = nil
	if err != nil {
		return info, fmt.Errorf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%w", err)
	}
	updated, err := s.read(ctx, id)
	if err != nil {
		return info, err
	}
	if updated.GUID != info.GUID || updated.Current != value {
		return updated, errors.New("修改后配置发生变化，请刷新并检查网卡；恢复记录已保留")
	}
	if restore {
		if err := s.clearOriginal(info.GUID); err != nil {
			return updated, fmt.Errorf("MTU 已恢复，但清理恢复记录失败：%w", err)
		}
		updated.Original = 0
	}
	return updated, nil
}

// Applied through the engine RPC, matching change(). Kept behind a method so
// batch tests can inject a fake instead of touching the host adapter.
func (s *MTUService) applyMTU(ctx context.Context, info MTUInfo, value int) error {
	if s.setMTU != nil {
		return s.setMTU(ctx, info, value)
	}
	var ignored map[string]any
	return s.engine.client.Request(ctx, mtuSetRPC, map[string]any{"if_index": info.IfIndex, "guid": info.GUID, "expected": info.Current, "value": value}, &ignored)
}

// dedupeIDs drops empty entries and repeats while keeping the selection order,
// so a batch never touches one adapter twice.
func dedupeIDs(ids []string) []string {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

func (s *MTUService) validateBatch(ids []string) error {
	if len(ids) == 0 {
		return errors.New(errMTUNoAdapter)
	}
	if len(ids) > mtuBatchMax {
		return errors.New(errMTUTooMany)
	}
	return nil
}

// SetBatch changes every selected adapter to one MTU value. Per-adapter failures
// stay in the returned items; the error is reserved for a failed batch check.
func (s *MTUService) SetBatch(ids []string, value int) ([]MTUBatchItem, error) {
	unique := dedupeIDs(ids)
	if err := s.validateBatch(unique); err != nil {
		return nil, err
	}
	if value < 576 || value > 65535 {
		return nil, errors.New(errMTUOutOfRange)
	}
	return s.batch(unique, value, false)
}

func (s *MTUService) RestoreBatch(ids []string) ([]MTUBatchItem, error) {
	unique := dedupeIDs(ids)
	if err := s.validateBatch(unique); err != nil {
		return nil, err
	}
	return s.batch(unique, 0, true)
}

func (s *MTUService) batch(ids []string, value int, restore bool) ([]MTUBatchItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil, errors.New("请等待 MTU 检测完成")
	}
	timeout := time.Duration(20+15*len(ids)) * time.Second
	if timeout > 300*time.Second {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if s.preflightSet {
		release, err := s.preflight(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		return s.run(ctx, ids, value, restore)
	}
	release, err := s.stopped(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.run(ctx, ids, value, restore)
}

// Require an elevated Core that announces the mtu.set capability, so a stale
// Core fails the whole batch before any adapter is touched.
func (s *MTUService) requireSetCapability(ctx context.Context) error {
	if s.checkEngine != nil {
		return s.checkEngine(ctx)
	}
	hello, err := s.engine.client.EnsureElevated(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(hello.Capabilities, mtuSetRPC) {
		return errors.New("请更新 Core 后再修改 MTU")
	}
	return nil
}

// Per-adapter execution under an already-held lifecycle gate.
func (s *MTUService) run(ctx context.Context, ids []string, value int, restore bool) ([]MTUBatchItem, error) {
	if err := s.requireSetCapability(ctx); err != nil {
		return nil, err
	}
	items := make([]MTUBatchItem, 0, len(ids))
	for _, id := range ids {
		info, err := s.read(ctx, id)
		if err != nil {
			items = append(items, MTUBatchItem{AdapterID: id, Error: err.Error()})
			continue
		}
		item := MTUBatchItem{AdapterID: info.AdapterID, GUID: info.GUID, Address: info.Address, Before: info.Current, Original: info.Original}
		target := value
		if restore {
			if info.Original <= 0 {
				item.Changed = false
				item.Error = errMTUNoOriginal
				items = append(items, item)
				continue
			}
			target = info.Original
		} else if info.Current == value {
			item.Changed = false
			item.Error = ""
			items = append(items, item)
			continue
		}
		if !restore {
			if err = s.saveOriginal(info.GUID, info.Current); err != nil {
				item.Error = fmt.Sprintf("保存原 MTU 失败，未执行修改：%v", err)
				items = append(items, item)
				continue
			}
		}
		if err = s.applyMTU(ctx, info, target); err != nil {
			item.Error = fmt.Sprintf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%v", err)
			items = append(items, item)
			continue
		}
		updated, err := s.read(ctx, id)
		if err != nil {
			item.Error = err.Error()
			items = append(items, item)
			continue
		}
		if updated.GUID != info.GUID || updated.Current != target {
			item.Error = "修改后配置发生变化，请刷新并检查网卡；恢复记录已保留"
			items = append(items, item)
			continue
		}
		item.Changed = true
		item.After = target
		if restore {
			// The MTU is already restored, so a failed cleanup keeps the record
			// (and the original value) and only adds a warning.
			if err = s.clearOriginal(info.GUID); err != nil {
				item.Error = fmt.Sprintf("MTU 已恢复，但清理恢复记录失败：%v", err)
			} else {
				item.Original = 0
			}
		}
		items = append(items, item)
	}
	return items, nil
}
