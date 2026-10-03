package tun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// expectedTunInterfaceName resolves the adapter name a staged sidecar
// configuration must bring up. The managed TUN lifecycle declares its adapter
// through Config.InterfaceName; older callers only ship a configuration file,
// so the tun inbound declared there remains authoritative. A configuration
// without any tun inbound keeps the historical HypoMux-Tun assumption.
func expectedTunInterfaceName(config Config) (string, error) {
	if name := strings.TrimSpace(config.InterfaceName); name != "" {
		return name, nil
	}
	return configuredTunInterfaceName(config.ConfigPath)
}

// configuredTunInterfaceName returns the interface_name of the first tun
// inbound in a staged sing-box configuration. A configuration without any tun
// inbound keeps the historical default so readiness and cleanup stay on the
// managed adapter.
func configuredTunInterfaceName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read staged TUN interface name: %w", err)
	}
	var config struct {
		Inbounds []struct {
			Type string `json:"type"`
			Name string `json:"interface_name"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse staged TUN interface name: %w", err)
	}
	for _, inbound := range config.Inbounds {
		if inbound.Type != "tun" {
			continue
		}
		if name := strings.TrimSpace(inbound.Name); name != "" {
			return name, nil
		}
		return "", errors.New("staged TUN inbound has no interface_name")
	}
	return tunInterfaceName, nil
}
