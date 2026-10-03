package vnic

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hypostasis-Cat/HypoMux/engine/internal/tun"
)

type fakeSidecar struct {
	mu              sync.Mutex
	status          tun.Status
	activateErr     error
	activateErrs    []error
	activateConfigs []tun.Config
	stopErr         error
	stubborn        bool
	stopCalls       int
	onStop          func()
	onLog           func(string)
	onUnexpected    func(tun.Status)
	nextGeneration  uint64
}

func (f *fakeSidecar) Activate(_ context.Context, config tun.Config) (tun.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activateConfigs = append(f.activateConfigs, config)
	if len(f.activateErrs) > 0 {
		err := f.activateErrs[0]
		f.activateErrs = f.activateErrs[1:]
		if err != nil {
			f.status = tun.Status{State: tun.StateFailed, LastError: err.Error()}
			return f.status, err
		}
	}
	if f.activateErr != nil {
		f.status = tun.Status{State: tun.StateFailed, LastError: f.activateErr.Error()}
		return f.status, f.activateErr
	}
	f.nextGeneration++
	f.status = tun.Status{State: tun.StateRunning, Generation: f.nextGeneration}
	return f.status, nil
}

func (f *fakeSidecar) Stop(context.Context) (tun.Status, error) {
	f.mu.Lock()
	f.stopCalls++
	onStop := f.onStop
	err := f.stopErr
	stubborn := f.stubborn
	if !stubborn {
		if err != nil {
			// A supervisor that could not tear the keeper down reports the
			// failure instead of claiming the adapter is gone.
			f.status = tun.Status{State: tun.StateFailed, LastError: err.Error()}
		} else {
			f.status = tun.Status{State: tun.StateStopped}
		}
	}
	status := f.status
	f.mu.Unlock()
	if onStop != nil {
		onStop()
	}
	return status, err
}

func (f *fakeSidecar) Status() tun.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeSidecar) SetHandlers(onLog func(string), onUnexpected func(tun.Status)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onLog = onLog
	f.onUnexpected = onUnexpected
}

func (f *fakeSidecar) setStatus(status tun.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeSidecar) configs() []tun.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tun.Config(nil), f.activateConfigs...)
}

func validMeta() Meta {
	return Meta{
		Executable:     `C:\HypoMux\bin\sing-box.exe`,
		ConfigPath:     `C:\Users\Example\.hypomux\vnic-config.json`,
		ConfigSHA256:   "0123456789abcdef",
		StartupTimeout: 20 * time.Second,
		InterfaceName:  "HypoMux-VNIC",
		Address:        "10.66.0.1",
		PrefixLength:   24,
		MTU:            1420,
	}
}

func TestManagerReportsAbsentWithoutKeeper(t *testing.T) {
	manager := NewManager(&fakeSidecar{}, nil, nil)
	status := manager.Status()
	if status.State != StateAbsent {
		t.Fatalf("state = %q, want %q", status.State, StateAbsent)
	}
	if status.InterfaceName != "" {
		t.Fatalf("interface name = %q, want empty", status.InterfaceName)
	}
}

func TestManagerCreatePublishesAdapterDescriptor(t *testing.T) {
	controller := &fakeSidecar{}
	manager := NewManager(controller, nil, nil)
	meta := validMeta()
	status, err := manager.Create(context.Background(), meta)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status.State != StatePresent {
		t.Fatalf("state = %q, want %q", status.State, StatePresent)
	}
	if status.InterfaceName != meta.InterfaceName {
		t.Fatalf("interface name = %q, want %q", status.InterfaceName, meta.InterfaceName)
	}
	if status.Address != meta.Address || status.PrefixLength != meta.PrefixLength || status.MTU != meta.MTU {
		t.Fatalf("descriptor = %+v, want address/prefix/mtu from the request", status)
	}
	if status.CreatedAt.IsZero() {
		t.Fatal("created_at must be set after a successful create")
	}
	if got := controller.configs(); len(got) != 1 {
		t.Fatalf("activate calls = %d, want 1", len(got))
	} else if got[0].InterfaceName != meta.InterfaceName {
		t.Fatalf("activate interface name = %q, want %q", got[0].InterfaceName, meta.InterfaceName)
	}
}

func TestManagerCreateIsIdempotentForTheSameRequest(t *testing.T) {
	controller := &fakeSidecar{}
	manager := NewManager(controller, nil, nil)
	meta := validMeta()
	first, err := manager.Create(context.Background(), meta)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := manager.Create(context.Background(), meta)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.CreatedAt != second.CreatedAt {
		t.Fatalf("repeat create moved created_at: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
	if got := len(controller.configs()); got != 1 {
		t.Fatalf("activate calls = %d, want 1 for a repeated identical create", got)
	}
	if controller.stopCalls != 0 {
		t.Fatalf("stop calls = %d, want 0 for a repeated identical create", controller.stopCalls)
	}
}

func TestManagerCreateReplacesAdapterWhenTheRequestChanges(t *testing.T) {
	controller := &fakeSidecar{}
	manager := NewManager(controller, nil, nil)
	if _, err := manager.Create(context.Background(), validMeta()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	replacement := validMeta()
	replacement.InterfaceName = "HypoMux-VNIC-2"
	replacement.Address = "10.77.0.1"
	status, err := manager.Create(context.Background(), replacement)
	if err != nil {
		t.Fatalf("replacement create: %v", err)
	}
	if status.InterfaceName != "HypoMux-VNIC-2" || status.Address != "10.77.0.1" {
		t.Fatalf("status = %+v, want the replacement descriptor", status)
	}
	if controller.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1 before the replacement activation", controller.stopCalls)
	}
	if got := len(controller.configs()); got != 2 {
		t.Fatalf("activate calls = %d, want 2", got)
	}
}

func TestManagerCreateRejectsInvalidDescriptors(t *testing.T) {
	manager := NewManager(&fakeSidecar{}, nil, nil)
	cases := map[string]func(*Meta){
		"missing name":       func(meta *Meta) { meta.InterfaceName = "  " },
		"missing address":    func(meta *Meta) { meta.Address = "" },
		"non ipv4 address":   func(meta *Meta) { meta.Address = "fd00::1" },
		"malformed address":  func(meta *Meta) { meta.Address = "10.66.0" },
		"prefix too small":   func(meta *Meta) { meta.PrefixLength = 0 },
		"prefix too large":   func(meta *Meta) { meta.PrefixLength = 33 },
		"mtu too small":      func(meta *Meta) { meta.MTU = 575 },
		"mtu too large":      func(meta *Meta) { meta.MTU = 65536 },
		"missing executable": func(meta *Meta) { meta.Executable = "" },
		"missing config":     func(meta *Meta) { meta.ConfigPath = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			meta := validMeta()
			mutate(&meta)
			if _, err := manager.Create(context.Background(), meta); err == nil {
				t.Fatal("create must reject an invalid descriptor")
			}
			if status := manager.Status(); status.State != StateAbsent {
				t.Fatalf("state = %q, want %q after a rejected create", status.State, StateAbsent)
			}
		})
	}
}

func TestManagerCreateFailureLeavesNoAdapter(t *testing.T) {
	activateErr := errors.New("cannot create a file when that file already exists")
	controller := &fakeSidecar{activateErr: activateErr}
	manager := NewManager(controller, nil, nil)
	status, err := manager.Create(context.Background(), validMeta())
	if err == nil {
		t.Fatal("create must report the keeper failure")
	}
	// The rollback stops the keeper, so no adapter is left behind; the failure is
	// still reported as failed with the reason until the next create or remove.
	if status.State != StateFailed {
		t.Fatalf("state = %q, want %q", status.State, StateFailed)
	}
	if status.LastError == "" {
		t.Fatal("last_error must describe the failed activation")
	}
	if controller.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1 to roll back the failed activation", controller.stopCalls)
	}
	if status.InterfaceName != "HypoMux-VNIC" {
		t.Fatalf("interface name = %q, want the requested descriptor", status.InterfaceName)
	}
}

func TestManagerRemoveIsIdempotentAndReportsAbsent(t *testing.T) {
	controller := &fakeSidecar{}
	manager := NewManager(controller, nil, nil)
	status, err := manager.Remove(context.Background())
	if err != nil {
		t.Fatalf("remove without an adapter: %v", err)
	}
	if status.State != StateAbsent {
		t.Fatalf("state = %q, want %q", status.State, StateAbsent)
	}
	if controller.stopCalls != 0 {
		t.Fatalf("stop calls = %d, want 0 when no keeper is running", controller.stopCalls)
	}
	if _, err := manager.Create(context.Background(), validMeta()); err != nil {
		t.Fatalf("create: %v", err)
	}
	status, err = manager.Remove(context.Background())
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if status.State != StateAbsent {
		t.Fatalf("state = %q, want %q after remove", status.State, StateAbsent)
	}
	if controller.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", controller.stopCalls)
	}
	if status.InterfaceName != "" {
		t.Fatalf("interface name = %q, want empty after remove", status.InterfaceName)
	}
}

func TestManagerRemoveReportsStopFailure(t *testing.T) {
	// A keeper that ignores the stop request keeps reporting a running adapter
	// until the stop deadline expires; that is a removal failure, not a removal.
	stubborn := &fakeSidecar{
		status:   tun.Status{State: tun.StateRunning, Generation: 1},
		stopErr:  errors.New("keeper refused to stop"),
		stubborn: true,
	}
	manager := NewManager(stubborn, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	status, err := manager.Remove(ctx)
	if err == nil {
		t.Fatal("remove must report a keeper that could not be stopped")
	}
	if status.LastError == "" {
		t.Fatalf("last_error must describe the failed removal: %+v", status)
	}
	if status.State != StateFailed {
		t.Fatalf("state = %q, want %q while the keeper is still running", status.State, StateFailed)
	}
}

func TestManagerMapsKeeperState(t *testing.T) {
	controller := &fakeSidecar{}
	manager := NewManager(controller, nil, nil)
	cases := []struct {
		name  string
		state tun.State
		want  string
	}{
		{"stopped", tun.StateStopped, StateAbsent},
		{"starting", tun.StateStarting, StateCreating},
		{"running", tun.StateRunning, StatePresent},
		{"stopping", tun.StateStopping, StateRemoving},
		{"failed", tun.StateFailed, StateFailed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			controller.setStatus(tun.Status{State: testCase.state})
			if got := manager.Status().State; got != testCase.want {
				t.Fatalf("state = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestManagerReportsUnexpectedKeeperExit(t *testing.T) {
	controller := &fakeSidecar{status: tun.Status{State: tun.StateRunning, Generation: 1}}
	manager := NewManager(controller, nil, nil)
	if _, err := manager.Create(context.Background(), validMeta()); err != nil {
		t.Fatalf("create: %v", err)
	}
	controller.mu.Lock()
	onUnexpected := controller.onUnexpected
	controller.mu.Unlock()
	if onUnexpected == nil {
		t.Fatal("manager must register an unexpected-exit handler")
	}
	controller.setStatus(tun.Status{
		State:     tun.StateFailed,
		LastError: "keeper died",
	})
	onUnexpected(controller.Status())
	status := manager.Status()
	if status.State != StateFailed {
		t.Fatalf("state = %q, want %q", status.State, StateFailed)
	}
	if status.LastError != "keeper died" {
		t.Fatalf("last_error = %q, want the keeper error", status.LastError)
	}
}

func TestManagerForwardsKeeperOutput(t *testing.T) {
	controller := &fakeSidecar{}
	var messages []string
	manager := NewManager(controller, func(message string) {
		messages = append(messages, message)
	}, nil)
	if _, err := manager.Create(context.Background(), validMeta()); err != nil {
		t.Fatalf("create: %v", err)
	}
	controller.mu.Lock()
	onLog := controller.onLog
	controller.mu.Unlock()
	if onLog == nil {
		t.Fatal("manager must register a log handler")
	}
	onLog("keeper ready")
	if len(messages) != 1 || messages[0] != "keeper ready" {
		t.Fatalf("forwarded messages = %#v, want the keeper line", messages)
	}
}
