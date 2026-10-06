package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient"
)

type mtuBatchHarness struct {
	service *MTUService
	readIDs []string
	applied []mtuBatchApply
}

type mtuBatchApply struct {
	ID    string
	GUID  string
	From  int
	Value int
}

func newMTUBatchHarness(t *testing.T, fake map[string]MTUInfo, apply func(MTUInfo, int) error) *mtuBatchHarness {
	t.Helper()
	harness := &mtuBatchHarness{}
	// current mirrors the host state so the post-write read-back is verified.
	current := make(map[string]int, len(fake))
	for id, info := range fake {
		current[id] = info.Current
	}
	harness.service = &MTUService{
		path: filepath.Join(t.TempDir(), "mtu-originals.json"),
		// The lifecycle gate, elevation handshake, and mtu.set RPC all belong to
		// the engine; the batch unit tests replace the whole gate with a no-op.
		preflightSet: true,
		preflight:    func(context.Context) (func(), error) { return func() {}, nil },
		checkEngine:  func(context.Context) error { return nil },
		readInfo: func(_ context.Context, id string) (MTUInfo, error) {
			harness.readIDs = append(harness.readIDs, id)
			info, ok := fake[id]
			if !ok {
				return MTUInfo{}, fmt.Errorf("未知网卡 %s", id)
			}
			info.Current = current[id]
			return info, nil
		},
		setMTU: func(_ context.Context, info MTUInfo, value int) error {
			harness.applied = append(harness.applied, mtuBatchApply{ID: info.AdapterID, GUID: info.GUID, From: info.Current, Value: value})
			if apply != nil {
				if err := apply(info, value); err != nil {
					return err
				}
			}
			current[info.AdapterID] = value
			return nil
		},
	}
	return harness
}

func batchInfo(id string, current, original int) MTUInfo {
	return MTUInfo{AdapterID: id, GUID: "guid-" + id, IfIndex: 7, Address: "10.0.0.1", Current: current, Original: original}
}

func batchItems(items ...*MTUBatchItem) []MTUBatchItem {
	out := make([]MTUBatchItem, 0, len(items))
	for _, item := range items {
		out = append(out, *item)
	}
	return out
}

func TestMTUBatchRejectsInvalidValue(t *testing.T) {
	for _, value := range []int{0, 575, 65536, -1} {
		harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1500, 0)}, nil)
		if _, err := harness.service.SetBatch([]string{"eth-a"}, value); err == nil || err.Error() != errMTUOutOfRange {
			t.Fatalf("value=%d err=%v", value, err)
		}
		if len(harness.readIDs) != 0 || len(harness.applied) != 0 {
			t.Fatalf("value=%d touched adapters: %v %v", value, harness.readIDs, harness.applied)
		}
	}
}

func TestMTUBatchRejectsEmptySelection(t *testing.T) {
	harness := newMTUBatchHarness(t, nil, nil)
	if _, err := harness.service.SetBatch([]string{"", ""}, 1500); err == nil || err.Error() != errMTUNoAdapter {
		t.Fatalf("set err=%v", err)
	}
	if _, err := harness.service.RestoreBatch(nil); err == nil || err.Error() != errMTUNoAdapter {
		t.Fatalf("restore err=%v", err)
	}
	if len(harness.readIDs) != 0 || len(harness.applied) != 0 {
		t.Fatalf("touched adapters: %v %v", harness.readIDs, harness.applied)
	}
}

func TestMTUBatchDeduplicatesAndCapsAt32(t *testing.T) {
	fake := map[string]MTUInfo{}
	ids := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("eth-%02d", i)
		fake[id] = batchInfo(id, 1500, 0)
		ids = append(ids, id)
	}
	ids = append(ids, ids[0], ids[5], "")
	harness := newMTUBatchHarness(t, fake, nil)
	items, err := harness.service.SetBatch(ids, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 32 || len(harness.applied) != 32 {
		t.Fatalf("deduplicated batch: items=%d applied=%d", len(items), len(harness.applied))
	}
	for _, item := range items {
		if !item.Changed || item.After != 1400 || item.Before != 1500 {
			t.Fatalf("item=%+v", item)
		}
	}
	if len(harness.readIDs) != 64 {
		t.Fatalf("expected one read per adapter check, got %d reads", len(harness.readIDs))
	}
	tooMany := make([]string, 0, 33)
	for i := 0; i < 33; i++ {
		tooMany = append(tooMany, fmt.Sprintf("eth-%02d", i))
	}
	rejected := newMTUBatchHarness(t, fake, nil)
	if _, err := rejected.service.SetBatch(tooMany, 1400); err == nil || err.Error() != errMTUTooMany {
		t.Fatalf("33 adapters err=%v", err)
	}
	if len(rejected.readIDs) != 0 || len(rejected.applied) != 0 {
		t.Fatalf("33 adapters touched host: %v %v", rejected.readIDs, rejected.applied)
	}
}

// A failed adapter must not stop its neighbours, and the caller still gets one
// item per selection with a nil batch error.
func TestMTUBatchContinuesPastPerAdapterFailure(t *testing.T) {
	fake := map[string]MTUInfo{
		"eth-a": batchInfo("eth-a", 1500, 0),
		"eth-b": batchInfo("eth-b", 1500, 0),
		"eth-c": batchInfo("eth-c", 1500, 0),
		"eth-d": batchInfo("eth-d", 1500, 0),
	}
	harness := newMTUBatchHarness(t, fake, func(info MTUInfo, _ int) error {
		if info.AdapterID == "eth-b" {
			return errors.New("engine 拒绝修改")
		}
		return nil
	})
	original := harness.service.readInfo
	harness.service.readInfo = func(ctx context.Context, id string) (MTUInfo, error) {
		if id == "eth-a" {
			return MTUInfo{}, errors.New("读取网卡失败")
		}
		return original(ctx, id)
	}
	items, err := harness.service.SetBatch([]string{"eth-a", "eth-b", "eth-c", "eth-d"}, 1400)
	if err != nil {
		t.Fatal(err)
	}
	want := batchItems(
		&MTUBatchItem{AdapterID: "eth-a", Error: "读取网卡失败"},
		&MTUBatchItem{AdapterID: "eth-b", GUID: "guid-eth-b", Address: "10.0.0.1", Before: 1500, Error: "修改未确认，请刷新当前值；原值已保留，可重试恢复：engine 拒绝修改"},
		&MTUBatchItem{AdapterID: "eth-c", GUID: "guid-eth-c", Address: "10.0.0.1", Before: 1500, After: 1400, Changed: true},
		&MTUBatchItem{AdapterID: "eth-d", GUID: "guid-eth-d", Address: "10.0.0.1", Before: 1500, After: 1400, Changed: true},
	)
	if fmt.Sprintf("%+v", items) != fmt.Sprintf("%+v", want) {
		t.Fatalf("items=%+v", items)
	}
	if len(harness.applied) != 3 || harness.applied[2].ID != "eth-d" {
		t.Fatalf("batch did not continue in order: %+v", harness.applied)
	}
}

func TestMTUBatchSkipsAdaptersAlreadyAtTarget(t *testing.T) {
	fake := map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1400, 0)}
	harness := newMTUBatchHarness(t, fake, nil)
	items, err := harness.service.SetBatch([]string{"eth-a"}, 1400)
	if err != nil {
		t.Fatal(err)
	}
	want := batchItems(&MTUBatchItem{AdapterID: "eth-a", GUID: "guid-eth-a", Address: "10.0.0.1", Before: 1400, Changed: false})
	if fmt.Sprintf("%+v", items) != fmt.Sprintf("%+v", want) {
		t.Fatalf("items=%+v", items)
	}
	if len(harness.applied) != 0 {
		t.Fatalf("wrote an unchanged adapter: %+v", harness.applied)
	}
}

// Restoring without a saved baseline reports the adapter as skipped instead of
// writing an arbitrary value.
func TestMTUBatchRestoreSkipsAdaptersWithoutOriginal(t *testing.T) {
	fake := map[string]MTUInfo{
		"eth-a": batchInfo("eth-a", 1400, 0),
		"eth-b": batchInfo("eth-b", 1400, 0),
	}
	harness := newMTUBatchHarness(t, fake, nil)
	// Only eth-b carries a saved baseline in the recovery record.
	if err := harness.service.saveOriginal("guid-eth-b", 1500); err != nil {
		t.Fatal(err)
	}
	items, err := harness.service.RestoreBatch([]string{"eth-a", "eth-b"})
	if err != nil {
		t.Fatal(err)
	}
	want := batchItems(
		&MTUBatchItem{AdapterID: "eth-a", GUID: "guid-eth-a", Address: "10.0.0.1", Before: 1400, Changed: false, Error: errMTUNoOriginal},
		&MTUBatchItem{AdapterID: "eth-b", GUID: "guid-eth-b", Address: "10.0.0.1", Before: 1400, After: 1500, Changed: true},
	)
	if fmt.Sprintf("%+v", items) != fmt.Sprintf("%+v", want) {
		t.Fatalf("items=%+v", items)
	}
	if len(harness.applied) != 1 || harness.applied[0].Value != 1500 || harness.applied[0].ID != "eth-b" {
		t.Fatalf("applied=%+v", harness.applied)
	}
}

// A successful set persists the previous baseline; a successful restore clears
// it again so the next read has nothing to restore.
func TestMTUBatchSetSavesAndRestoreClearsOriginal(t *testing.T) {
	harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1500, 0)}, nil)
	items, err := harness.service.SetBatch([]string{"eth-a"}, 1400)
	if err != nil {
		t.Fatal(err)
	}
	// The item mirrors the caller's read, not the baseline just persisted; the
	// recovery record itself is asserted below.
	want := batchItems(&MTUBatchItem{AdapterID: "eth-a", GUID: "guid-eth-a", Address: "10.0.0.1", Before: 1500, After: 1400, Changed: true})
	if fmt.Sprintf("%+v", items) != fmt.Sprintf("%+v", want) {
		t.Fatalf("set items=%+v", items)
	}
	persisted, err := harness.service.originals()
	if err != nil || persisted["guid-eth-a"] != 1500 {
		t.Fatalf("baseline=%v err=%v", persisted, err)
	}
	// The adapter is now at the target, and the saved baseline drives the restore.
	stored := map[string]int{"guid-eth-a": 1400}
	harness.service.readInfo = func(_ context.Context, id string) (MTUInfo, error) {
		if _, err := harness.service.originals(); err != nil {
			return MTUInfo{}, err
		}
		return batchInfo(id, stored["guid-"+id], 0), nil
	}
	harness.service.setMTU = func(_ context.Context, info MTUInfo, value int) error {
		harness.applied = append(harness.applied, mtuBatchApply{ID: info.AdapterID, GUID: info.GUID, From: stored[info.GUID], Value: value})
		stored[info.GUID] = value
		return nil
	}
	items, err = harness.service.RestoreBatch([]string{"eth-a"})
	if err != nil {
		t.Fatal(err)
	}
	want = batchItems(&MTUBatchItem{AdapterID: "eth-a", GUID: "guid-eth-a", Address: "10.0.0.1", Before: 1400, After: 1500, Original: 0, Changed: true})
	if fmt.Sprintf("%+v", items) != fmt.Sprintf("%+v", want) {
		t.Fatalf("restore items=%+v", items)
	}
	if persisted, err = harness.service.originals(); err != nil || persisted["guid-eth-a"] != 0 {
		t.Fatalf("baseline not cleared: %v err=%v", persisted, err)
	}
	if len(harness.applied) != 2 || harness.applied[0].Value != 1400 || harness.applied[1].Value != 1500 {
		t.Fatalf("applied=%+v", harness.applied)
	}
}

// A rejected write must surface on its own item — with the same guidance the
// single-adapter path gives — while the rest of the batch is still executed.
func TestMTUBatchReportsFailedApply(t *testing.T) {
	harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1500, 0), "eth-b": batchInfo("eth-b", 1500, 0)}, func(info MTUInfo, _ int) error {
		if info.AdapterID == "eth-a" {
			return errors.New("mtu_failed: access denied")
		}
		return nil
	})
	items, err := harness.service.SetBatch([]string{"eth-a", "eth-b"}, 1400)
	if err != nil {
		t.Fatal(err)
	}
	guidance := "修改未确认，请刷新当前值；原值已保留，可重试恢复："
	if items[0].Changed || !strings.HasPrefix(items[0].Error, guidance) || !strings.Contains(items[0].Error, "mtu_failed: access denied") {
		t.Fatalf("failed item=%+v", items[0])
	}
	if !items[1].Changed || items[1].After != 1400 {
		t.Fatalf("healthy item=%+v", items[1])
	}
}

// Losing the recovery record after a successful restore must not look like a
// clean success: the MTU really is back, so the change is reported, but the
// leftover baseline is surfaced as a warning instead of being swallowed.
func TestMTUBatchReportsUnclearedOriginal(t *testing.T) {
	harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1400, 1500)}, nil)
	// The record stays readable, so the restore still knows the baseline, but its
	// replacement is denied and the cleanup after a successful restore fails.
	record := `{"guid-eth-a":1500}`
	if err := os.WriteFile(harness.service.path, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(harness.service.path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(harness.service.path, 0o644) })
	items, err := harness.service.RestoreBatch([]string{"eth-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%+v", items)
	}
	if !items[0].Changed || items[0].After != 1500 || items[0].Original != 1500 {
		t.Fatalf("restored item=%+v", items[0])
	}
	if !strings.HasPrefix(items[0].Error, "MTU 已恢复，但清理恢复记录失败：") {
		t.Fatalf("cleanup error=%q", items[0].Error)
	}
	if len(harness.applied) != 1 || harness.applied[0].Value != 1500 {
		t.Fatalf("applied=%+v", harness.applied)
	}
}

// A baseline that cannot be persisted must be reported on the item and must not
// be followed by a write, because the previous value could no longer be restored.
func TestMTUBatchReportsUnpersistableOriginal(t *testing.T) {
	harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1500, 0)}, nil)
	// The record stays readable but its replacement is denied, so only the
	// baseline save fails while reads keep working.
	if err := os.WriteFile(harness.service.path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(harness.service.path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(harness.service.path, 0o644) })
	items, err := harness.service.SetBatch([]string{"eth-a"}, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Changed || !strings.HasPrefix(items[0].Error, "保存原 MTU 失败，未执行修改：") {
		t.Fatalf("unpersistable item=%+v", items[0])
	}
	if len(harness.applied) != 0 {
		t.Fatalf("writes happened without a saved baseline: %+v", harness.applied)
	}
}

// An outdated Core must fail the whole batch before any adapter is touched.
func TestMTUBatchRejectsCoreWithoutCapability(t *testing.T) {
	harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1500, 0)}, nil)
	harness.service.checkEngine = func(context.Context) error { return errors.New("请更新 Core 后再修改 MTU") }
	if _, err := harness.service.SetBatch([]string{"eth-a"}, 1400); err == nil || err.Error() != "请更新 Core 后再修改 MTU" {
		t.Fatalf("outdated Core err=%v", err)
	}
	if len(harness.readIDs) != 0 || len(harness.applied) != 0 {
		t.Fatalf("outdated Core still touched adapters: %v %v", harness.readIDs, harness.applied)
	}
}

// Both whole-batch gates must fail before any adapter is read or written.
func TestMTUBatchPrecheckFailsWithNoChanges(t *testing.T) {
	harness := newMTUBatchHarness(t, map[string]MTUInfo{"eth-a": batchInfo("eth-a", 1500, 0)}, nil)
	harness.service.engine = &EngineService{settings: NewSettingsService(), client: engineclient.New(), lifecycleGate: make(chan struct{}, 1)}
	// Drop the fake gates so the real stopped()+EnsureElevated path runs.
	harness.service.preflightSet = false
	harness.service.preflight = nil
	harness.service.checkEngine = nil
	if _, err := harness.service.SetBatch([]string{"eth-a"}, 1400); err == nil {
		t.Fatal("batch ran without an elevated Core")
	}
	harness.service.cancel = func() {}
	if _, err := harness.service.SetBatch([]string{"eth-a"}, 1400); err == nil || err.Error() != "请等待 MTU 检测完成" {
		t.Fatalf("detection in progress err=%v", err)
	}
	if _, err := harness.service.RestoreBatch([]string{"eth-a"}); err == nil || err.Error() != "请等待 MTU 检测完成" {
		t.Fatalf("restore during detection err=%v", err)
	}
	if len(harness.readIDs) != 0 || len(harness.applied) != 0 {
		t.Fatalf("precheck failure still touched adapters: %v %v", harness.readIDs, harness.applied)
	}
}
