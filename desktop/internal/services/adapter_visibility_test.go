package services

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVirtualAdapterClassification(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		virtual           bool
	}{
		{"Ethernet 2", "VMware Virtual Ethernet Adapter for VMnet8", true},
		{"Renamed", "Hyper-V Virtual Ethernet Adapter", true},
		{"vEthernet (Default Switch)", "", true},
		{"Ethernet", "VirtualBox Host-Only Ethernet Adapter", true},
		{"Work", "WireGuard Tunnel", true},
		{"VPN", "TAP-Windows Adapter V9", true},
		{"Renamed", "Meta Tunnel", true},
		{"Mihomo", "", true},
		{"Clash", "", true},
		{"Radmin VPN", "", true},
		{"Ethernet 3", "Famatech Radmin VPN Ethernet Adapter", true},
		{"rAdMiN vPn", "", true},
		{"以太网", "Realtek PCIe GbE Family Controller", false},
		{"WLAN", "Intel(R) Wi-Fi 6 AX201 160MHz", false},
		{"USB Ethernet", "Remote NDIS based Internet Sharing Device", false},
		{"Unknown", "", false},
	} {
		if got := isVirtualAdapter(tc.name, tc.description); got != tc.virtual {
			t.Errorf("%q / %q: virtual=%t", tc.name, tc.description, got)
		}
	}
}

func TestHideVirtualAdaptersDefaultsAndPersistence(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	if DefaultSettings().HideVirtualAdapters {
		t.Fatal("fresh default should show virtual adapters")
	}
	path := filepath.Join(settingsDirectory(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"mode":"proxy","socks_port":10800,"http_port":10801}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewSettingsService()
	if s.Get().HideVirtualAdapters {
		t.Fatal("older settings did not get the default")
	}
	// Persisting true, not false: the default is now false, so asserting that an
	// explicit false survives would pass even if persistence were broken
	// entirely. Only an override of the default can actually fail here.
	next := s.Get()
	next.HideVirtualAdapters = true
	if _, err := s.Update(next); err != nil {
		t.Fatal(err)
	}
	if !NewSettingsService().Get().HideVirtualAdapters {
		t.Fatal("explicit true did not persist")
	}
	migrated, err := migrateLegacySettings([]byte(`{}`))
	if err != nil || migrated.HideVirtualAdapters {
		t.Fatalf("legacy default: %+v %v", migrated, err)
	}
}

// An explicitly persisted choice must survive the default flip: a user who
// turned hiding ON keeps it ON across upgrades, and one who left it alone keeps
// the new default.
func TestHideVirtualAdaptersExplicitChoiceSurvivesReload(t *testing.T) {
	for _, persisted := range []bool{true, false} {
		t.Run(fmt.Sprintf("persisted=%t", persisted), func(t *testing.T) {
			t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
			path := filepath.Join(settingsDirectory(), "settings.json")
			body := fmt.Sprintf(
				`{"mode":"proxy","socks_port":10800,"http_port":10801,"hide_virtual_adapters":%t}`,
				persisted,
			)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if got := NewSettingsService().Get().HideVirtualAdapters; got != persisted {
				t.Fatalf("stored %t, loaded %t", persisted, got)
			}
		})
	}
}
