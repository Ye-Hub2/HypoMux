package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// vnicDefaultInterfaceName 与桌面对话框默认值、引擎侧契约默认值保持一致。
	vnicDefaultInterfaceName = "HypoMux-VNIC"
	// vnicDefaultAddress 位于为虚拟网卡预留的私网段，避免与物理 TUN 抢地址。
	vnicDefaultAddress = "10.66.0.1"
	// vnicPrefixLength 与 vnicMTU 由契约固定，UI 只暴露名称与地址两项输入。
	vnicPrefixLength = 24
	vnicMTU          = 1420
)

// vnicConfigPlan 是虚拟网卡 keeper 进程的一次性启动计划：引擎需要用可执行文件、
// 配置路径与配置摘要来拉起 sing-box，用名称/地址/前缀/MTU 来做参数校验与状态回显。
type vnicConfigPlan struct {
	Executable    string
	Path          string
	SHA256        string
	InterfaceName string
	Address       string
	PrefixLength  int
	MTU           int
}

// writeVNICSingBoxConfig 落盘虚拟网卡 keeper 的最小 sing-box 配置：一个 tun inbound
// 负责创建并持有 Wintun 适配器，一个 direct outbound 供 keeper 自检；auto_route 与
// strict_route 恒为 false，因此适配器不会改动系统路由表，它的生命周期就等于 keeper
// 进程的生命周期。参考 tun_config.go 的原子写入与 SHA-256 计算方式。
func writeVNICSingBoxConfig(interfaceName string, address string, prefixLength int, mtu int) (vnicConfigPlan, error) {
	name := strings.TrimSpace(interfaceName)
	if name == "" {
		name = vnicDefaultInterfaceName
	}
	if prefixLength < 1 || prefixLength > 32 {
		return vnicConfigPlan{}, fmt.Errorf("虚拟网卡前缀长度必须在 1..32 之间：%d", prefixLength)
	}
	if mtu < 576 || mtu > 65535 {
		return vnicConfigPlan{}, fmt.Errorf("虚拟网卡 MTU 必须在 576..65535 之间：%d", mtu)
	}
	normalizedAddress, err := normalizeVNICAddress(address, prefixLength)
	if err != nil {
		return vnicConfigPlan{}, err
	}
	stack, err := normalizeTunStack("")
	if err != nil {
		return vnicConfigPlan{}, err
	}
	executable, err := resolveRuntimeAsset("sing-box.exe")
	if err != nil {
		return vnicConfigPlan{}, err
	}
	config := map[string]any{
		"log": map[string]any{
			"level":     "warn",
			"timestamp": true,
		},
		"inbounds": []any{
			map[string]any{
				"type":           "tun",
				"tag":            "tun-in",
				"interface_name": name,
				"address":        []string{fmt.Sprintf("%s/%d", normalizedAddress, prefixLength)},
				"mtu":            mtu,
				"auto_route":     false,
				"strict_route":   false,
				"stack":          stack,
			},
		},
		"outbounds": []any{
			map[string]any{
				"type": "direct",
				"tag":  "direct",
			},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return vnicConfigPlan{}, fmt.Errorf("生成虚拟网卡配置失败：%w", err)
	}
	digest := sha256.Sum256(data)
	directory := filepath.Join(settingsDirectory(), "vnic")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return vnicConfigPlan{}, fmt.Errorf("创建虚拟网卡配置目录失败：%w", err)
	}
	path := filepath.Join(directory, "sing-box-vnic.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return vnicConfigPlan{}, fmt.Errorf("写入虚拟网卡配置失败：%w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return vnicConfigPlan{}, fmt.Errorf("提交虚拟网卡配置失败：%w", err)
	}
	return vnicConfigPlan{
		Executable:    executable,
		Path:          path,
		SHA256:        hex.EncodeToString(digest[:]),
		InterfaceName: name,
		Address:       normalizedAddress,
		PrefixLength:  prefixLength,
		MTU:           mtu,
	}, nil
}

// normalizeVNICAddress 只接受一个 IPv4 字面量；允许附带与契约一致的前缀长度
// （例如 10.66.0.1/24），空值回落到默认地址。返回纯地址，前缀由调用方单独传递。
func normalizeVNICAddress(address string, prefixLength int) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return vnicDefaultAddress, nil
	}
	if host, prefix, found := strings.Cut(trimmed, "/"); found {
		parsed, err := strconv.Atoi(strings.TrimSpace(prefix))
		if err != nil || parsed != prefixLength {
			return "", fmt.Errorf("虚拟网卡地址前缀固定为 /%d：%s", prefixLength, address)
		}
		trimmed = strings.TrimSpace(host)
	}
	ip := net.ParseIP(trimmed)
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("请输入有效的 IPv4 地址：%s", address)
	}
	return ip.To4().String(), nil
}
