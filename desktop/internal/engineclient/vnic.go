package engineclient

import (
	"context"
)

// Method names of the virtual-adapter (vnic) surface. They must stay
// byte-identical to protocol/v1/manifest.json and engine/internal/api/v1,
// otherwise the capability probe and the RPC both fail at runtime.
const (
	MethodVNICCreate = "vnic.create"
	MethodVNICStatus = "vnic.status"
	MethodVNICRemove = "vnic.remove"
)

// VNICCreateParams mirrors engine/internal/api/v1.VNICCreateParams. JSON tags
// are frozen by reports/vnic/00-frozen-interface.md §2.2: the engine rejects
// unknown or misspelled fields.
type VNICCreateParams struct {
	Executable       string `json:"executable"`
	ConfigPath       string `json:"config_path"`
	ConfigSHA256     string `json:"config_sha256,omitempty"`
	StartupTimeoutMS int    `json:"startup_timeout_ms"`
	InterfaceName    string `json:"interface_name"`
	Address          string `json:"address"`
	PrefixLength     int    `json:"prefix_length"`
	MTU              int    `json:"mtu"`
}

// VNICStatus mirrors engine/internal/api/v1.VNICStatus. State is one of
// absent, creating, present, removing or failed.
type VNICStatus struct {
	State         string `json:"state"`
	InterfaceName string `json:"interface_name"`
	Address       string `json:"address"`
	PrefixLength  int    `json:"prefix_length"`
	MTU           int    `json:"mtu"`
	AdapterGUID   string `json:"adapter_guid,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// VNICCreateResult mirrors engine/internal/api/v1.VNICCreateResult.
type VNICCreateResult struct {
	Accepted bool       `json:"accepted"`
	VNIC     VNICStatus `json:"vnic"`
}

// VNICCreate starts (or reuses) the Core-side keeper process that owns the
// virtual adapter. The Core must already be elevated; the caller is
// responsible for EnsureElevated, mirroring the mtu.set call path.
func (c *Client) VNICCreate(ctx context.Context, params VNICCreateParams) (VNICCreateResult, error) {
	var result VNICCreateResult
	if err := c.Request(ctx, MethodVNICCreate, params, &result); err != nil {
		return VNICCreateResult{}, err
	}
	return result, nil
}

// VNICStatus reads the keeper state. It starts nothing: without a running Core
// the caller cannot know the truth, which is why the service layer treats a
// missing Core as "absent".
func (c *Client) VNICStatus(ctx context.Context) (VNICStatus, error) {
	var status VNICStatus
	if err := c.Request(ctx, MethodVNICStatus, nil, &status); err != nil {
		return VNICStatus{}, err
	}
	return status, nil
}

// VNICRemove stops the keeper and returns the resulting status. It is
// idempotent on the engine side: removing a non-existent adapter reports
// state=absent instead of failing.
func (c *Client) VNICRemove(ctx context.Context) (VNICStatus, error) {
	var status VNICStatus
	if err := c.Request(ctx, MethodVNICRemove, nil, &status); err != nil {
		return VNICStatus{}, err
	}
	return status, nil
}
