//go:build !windows

package services

import (
	"context"
	"time"
)

// 非 Windows 平台的降级实现（参照同包 nat_firewall_other.go / adapter_metadata_other.go
// 的既有惯例）。没有 Hyper-V，也没有 powershell.exe，因此一切脚本调用直接报「不可用」。
//
// 存在的意义是跨平台编译：CI 的非 Windows 作业必须能 `go build ./...` 通过。前端拿到
// hyperv_unavailable 后会把整个 vNIC 面板标成不可用，而不是崩在「后端没注册」上。

func hypervPlatformSupported() bool { return false }

func hypervExecuteElevated(ctx context.Context, payload []byte, timeout time.Duration) error {
	return errHypervUnsupported
}

func hypervExecuteUnelevated(ctx context.Context, payload []byte, timeout time.Duration) error {
	return errHypervUnsupported
}
