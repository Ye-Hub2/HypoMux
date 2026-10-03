// Package vnic owns the virtual adapter lifecycle exposed through
// vnic.create / vnic.status / vnic.remove.
//
// A virtual adapter is a Wintun adapter kept alive by a dedicated sing-box
// "keeper" sidecar. The keeper is therefore a second managed sidecar instance
// that is completely independent of the main TUN lifecycle: it has its own
// supervisor, its own configuration and its own state machine. Stopping the
// main TUN sidecar never removes a virtual adapter, and removing the virtual
// adapter never touches the main TUN sidecar.
package vnic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Hypostasis-Cat/HypoMux/engine/internal/tun"
)

// State mirrors the wire states of api.VNICStatus.State.
const (
	StateAbsent   = "absent"
	StateCreating = "creating"
	StatePresent  = "present"
	StateRemoving = "removing"
	StateFailed   = "failed"
)

const (
	// minMTU and maxMTU bound a requested adapter MTU. Anything smaller cannot
	// carry IPv4 traffic and anything larger exceeds the Wintun limit.
	minMTU = 576
	maxMTU = 65535
	// minPrefixLength and maxPrefixLength bound the IPv4 prefix length.
	minPrefixLength = 1
	maxPrefixLength = 32
	// stopTimeout bounds a keeper shutdown triggered by vnic.remove.
	stopTimeout = 20 * time.Second
	// stopPollInterval is the keeper shutdown polling interval.
	stopPollInterval = 25 * time.Millisecond
)

// Sidecar is the subset of tun.Supervisor the virtual adapter lifecycle needs.
type Sidecar interface {
	Activate(context.Context, tun.Config) (tun.Status, error)
	Stop(context.Context) (tun.Status, error)
	Status() tun.Status
}

// Meta describes the adapter a caller asks the Core to keep alive. The name and
// address are owned by the caller; the engine never infers them.
type Meta struct {
	Executable     string
	ConfigPath     string
	ConfigSHA256   string
	StartupTimeout time.Duration
	InterfaceName  string
	Address        string
	PrefixLength   int
	MTU            int
}

// Validate rejects requests that cannot describe a usable IPv4 adapter.
func (m Meta) Validate() error {
	if strings.TrimSpace(m.InterfaceName) == "" {
		return errors.New("virtual adapter name is required")
	}
	address := net.ParseIP(strings.TrimSpace(m.Address))
	if address == nil || address.To4() == nil {
		return errors.New("virtual adapter address must be a valid IPv4 address")
	}
	if m.PrefixLength < minPrefixLength || m.PrefixLength > maxPrefixLength {
		return fmt.Errorf(
			"virtual adapter prefix length must be between %d and %d",
			minPrefixLength, maxPrefixLength,
		)
	}
	if m.MTU < minMTU || m.MTU > maxMTU {
		return fmt.Errorf("virtual adapter MTU must be between %d and %d", minMTU, maxMTU)
	}
	return nil
}

// Status is the engine-side virtual adapter status. The server maps it onto
// api.VNICStatus so the transport DTO stays free of engine-only fields.
type Status struct {
	State         string
	InterfaceName string
	Address       string
	PrefixLength  int
	MTU           int
	AdapterGUID   string
	CreatedAt     time.Time
	LastError     string
}

// Manager owns the virtual adapter keeper sidecar and its advertised state.
type Manager struct {
	controller Sidecar
	mu         sync.Mutex
	deployed   bool
	failed     bool
	meta       Meta
	createdAt  time.Time
	lastError  string
}

// NewManager wires a dedicated sidecar supervisor and forwards its output to
// the Core log stream. The caller keeps ownership of the supervisor.
func NewManager(
	controller Sidecar,
	onLog func(string),
	onUnexpected func(tun.Status),
) *Manager {
	manager := &Manager{controller: controller}
	if configurable, ok := controller.(interface {
		SetHandlers(func(string), func(tun.Status))
	}); ok {
		configurable.SetHandlers(onLog, func(status tun.Status) {
			manager.handleUnexpectedExit(status)
			if onUnexpected != nil {
				onUnexpected(status)
			}
		})
	}
	return manager
}

// Create brings the requested adapter up. It is idempotent: repeating the
// request while the same adapter is present is a no-op, and changing the name,
// address or configuration replaces the previous adapter instead of failing.
func (m *Manager) Create(ctx context.Context, meta Meta) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := meta.Validate(); err != nil {
		return m.statusLocked(), err
	}
	if strings.TrimSpace(meta.Executable) == "" || strings.TrimSpace(meta.ConfigPath) == "" {
		return m.statusLocked(), errors.New("virtual adapter activation requires executable and config_path")
	}
	if m.deployed && m.matchesLocked(meta) && tunState(m.controller.Status()) == tun.StateRunning {
		return m.statusLocked(), nil
	}
	// Publish the requested descriptor before touching the supervisor so a failed
	// activation still reports the requested adapter alongside its error instead
	// of an empty descriptor.
	m.meta = meta
	// A new attempt supersedes the previous failure; it is reinstated if this
	// activation also fails.
	m.failed = false
	m.lastError = ""
	// Any other case replaces the previous keeper. A different adapter must take
	// over the sidecar, and a keeper left running by an earlier partial attempt
	// must be torn down before the supervisor will start a new one. stopLocked is
	// a no-op while no keeper is running.
	if err := m.stopLocked(ctx); err != nil {
		return m.statusLocked(), err
	}
	config := tun.Config{
		Executable:     meta.Executable,
		ConfigPath:     meta.ConfigPath,
		ConfigSHA256:   meta.ConfigSHA256,
		InterfaceName:  meta.InterfaceName,
		StartupTimeout: meta.StartupTimeout,
	}
	if _, err := m.controller.Activate(ctx, config); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_, _ = m.controller.Stop(stopCtx)
		cancel()
		m.deployed = false
		m.failed = true
		m.createdAt = time.Time{}
		m.lastError = err.Error()
		return m.statusLocked(), err
	}
	m.deployed = true
	m.failed = false
	m.createdAt = time.Now().UTC()
	m.lastError = ""
	return m.statusLocked(), nil
}

// Status reports the current adapter state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

// Remove stops the keeper sidecar. It is idempotent and reports absent, not an
// error, when no adapter was ever created.
func (m *Manager) Remove(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.stopLocked(ctx); err != nil {
		return m.statusLocked(), err
	}
	m.deployed = false
	m.failed = false
	m.meta = Meta{}
	m.createdAt = time.Time{}
	m.lastError = ""
	return m.statusLocked(), nil
}

// stopLocked stops the keeper sidecar and waits for the supervisor to confirm it
// is gone. The supervisor is authoritative: Stop() is idempotent and a keeper
// that ignores its graceful interrupt is force-killed by the supervisor's own
// bounded fallback, so a clean stop that still reports an error means the keeper
// was already accounted for. Only a keeper that is still running once the stop
// deadline expires is a removal failure.
func (m *Manager) stopLocked(ctx context.Context) error {
	if m.controller == nil {
		return nil
	}
	if tunState(m.controller.Status()) == tun.StateStopped {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	_, stopErr := m.controller.Stop(stopCtx)
	if waitErr := m.waitForKeeperStop(stopCtx); waitErr != nil {
		m.failed = true
		m.lastError = waitErr.Error()
		return errors.Join(stopErr, waitErr)
	}
	if state := tunState(m.controller.Status()); state != tun.StateStopped && state != tun.StateFailed {
		failure := fmt.Errorf("virtual adapter keeper is still %s after shutdown", state)
		m.failed = true
		m.lastError = failure.Error()
		return errors.Join(stopErr, failure)
	}
	m.failed = false
	m.lastError = ""
	return nil
}

// waitForKeeperStop waits until the supervisor stops reporting an active
// keeper. A keeper that ignores the graceful interrupt keeps reporting running
// until its own force-kill fallback completes, so polling avoids clearing the
// advertised adapter before the process is really gone.
func (m *Manager) waitForKeeperStop(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for virtual adapter shutdown: %w", ctx.Err())
		case <-timer.C:
			state := tunState(m.controller.Status())
			if state == tun.StateStopped || state == tun.StateFailed {
				return nil
			}
			timer.Reset(stopPollInterval)
		}
	}
}

func (m *Manager) handleUnexpectedExit(status tun.Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	message := strings.TrimSpace(status.LastError)
	if message == "" {
		message = "virtual adapter keeper exited unexpectedly"
	}
	m.lastError = message
}

func (m *Manager) statusLocked() Status {
	if m.controller == nil {
		return Status{State: StateAbsent}
	}
	state := tunState(m.controller.Status())
	status := Status{
		State:         deriveState(state),
		InterfaceName: m.meta.InterfaceName,
		Address:       m.meta.Address,
		PrefixLength:  m.meta.PrefixLength,
		MTU:           m.meta.MTU,
	}
	// A failed activation or removal is the honest answer until the next create
	// or remove, even though the supervisor forgets a failure once it reports the
	// keeper stopped.
	if m.failed {
		status.State = StateFailed
	}
	if !m.createdAt.IsZero() {
		status.CreatedAt = m.createdAt
	}
	if status.State == StateFailed {
		if message := strings.TrimSpace(m.lastError); message != "" {
			status.LastError = message
		}
	}
	return status
}

func (m *Manager) matchesLocked(meta Meta) bool {
	return strings.EqualFold(m.meta.Executable, meta.Executable) &&
		m.meta.ConfigPath == meta.ConfigPath &&
		m.meta.ConfigSHA256 == meta.ConfigSHA256 &&
		m.meta.InterfaceName == meta.InterfaceName &&
		m.meta.Address == meta.Address &&
		m.meta.PrefixLength == meta.PrefixLength &&
		m.meta.MTU == meta.MTU
}

// deriveState maps the supervised keeper state onto the advertised adapter
// state. A stopped keeper means the adapter is absent: the adapter only exists
// while the Wintun-owning sidecar process is alive. A failed keeper reports
// failed until the next successful create or remove.
func deriveState(state tun.State) string {
	switch state {
	case tun.StateStarting:
		return StateCreating
	case tun.StateRunning:
		return StatePresent
	case tun.StateStopping:
		return StateRemoving
	case tun.StateFailed:
		return StateFailed
	default:
		return StateAbsent
	}
}

func tunState(status tun.Status) tun.State {
	if status.State == "" {
		return tun.StateStopped
	}
	return status.State
}
