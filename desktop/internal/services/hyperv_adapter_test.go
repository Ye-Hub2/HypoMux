package services

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 桌面层 Hyper-V 服务的表驱动单测。只覆盖纯逻辑：MAC 归一化、命名规范、序号分配、
// 台账读写、状态派生、批次上限、出口池合并。
//
// 刻意**不**碰真实 Hyper-V：本轮不允许创建/删除真实网卡，也不允许弹 UAC。凡是需要
// 提权脚本的路径（Create/Remove 的提权段）不在单测覆盖内，见 reports/vnic/72 的
// 「未验证项」。

func TestNormalizeHypervMAC(t *testing.T) {
	tests := map[string]string{
		// Hyper-V 读回的是裸 12 位大写十六进制（reports/vnic/60 实测 F6E6556A4D17）。
		"F6E6556A4D17": "f6e6556a4d17",
		// 台账与 net.HardwareAddr.String() 是小写冒号。
		"02:1a:2b:00:00:01":  "021a2b000001",
		"02-1A-2B-00-00-01":  "021a2b000001",
		"02:1a:2b:00:00:0":   "",
		"02:1a:2b:00:00:011": "",
		"":                   "",
		"   ":                "",
		"zz:1a:2b:00:00:01":  "",
		// 幽灵记录的 MAC 为空（reports/vnic/60 实测），必须判为不可用。
		"::": "",
	}
	for input, expected := range tests {
		if actual := normalizeHypervMAC(input); actual != expected {
			t.Fatalf("normalizeHypervMAC(%q) = %q; want %q", input, actual, expected)
		}
	}
}

func TestFormatHypervMAC(t *testing.T) {
	tests := map[string]string{
		"F6E6556A4D17":      "f6:e6:55:6a:4d:17",
		"021A2B000001":      "02:1a:2b:00:00:01",
		"02:1a:2b:00:00:01": "02:1a:2b:00:00:01",
		"":                  "",
		"bad":               "",
	}
	for input, expected := range tests {
		if actual := formatHypervMAC(input); actual != expected {
			t.Fatalf("formatHypervMAC(%q) = %q; want %q", input, actual, expected)
		}
	}
}

// TestHypervMACEqual 覆盖归属判定的第三条：删除时 MAC 必须逐字节一致，两侧任一不合法
// 都必须判为「不相等」——拿不到 MAC 时绝不放行删除。
func TestHypervMACEqual(t *testing.T) {
	tests := map[string]struct {
		left     string
		right    string
		expected bool
	}{
		"same normalized":       {"021A2B000001", "02:1a:2b:00:00:01", true},
		"hyperv readback style": {"F6E6556A4D17", "f6-e6-55-6a-4d-17", true},
		"different value":       {"021A2B000001", "021A2B000002", false},
		"left empty":            {"", "021A2B000001", false},
		"right empty":           {"021A2B000001", "", false},
		"both empty":            {"", "", false},
		"both invalid":          {"ghost", "ghost", false},
	}
	for name, testCase := range tests {
		if actual := hypervMACEqual(testCase.left, testCase.right); actual != testCase.expected {
			t.Fatalf("%s: hypervMACEqual(%q, %q) = %v; want %v", name, testCase.left, testCase.right, actual, testCase.expected)
		}
	}
}

// TestIsHyperVAdapterName 覆盖归属判定的第二条，也是删除路径的第一道闸门。
func TestIsHyperVAdapterName(t *testing.T) {
	tests := map[string]bool{
		"HypoMux-vnic-01":   true,
		"HypoMux-vnic-1":    true,
		"hypomux-vnic-09":   true,
		" HypoMux-vnic-07 ": true,
		"HypoMux-vnic-":     false,
		"HypoMux-vnic":      false,
		"HypoMux-vnic-0a":   false,
		"HypoMux-vnic-01a":  false,
		// 前导零与 3–4 位数字仍接受：序号分配用 %02d，但用户可能自己改名。这一层只是
		// 预筛，真正的归属判定还要过台账 + MAC 逐字节比对。
		"HypoMux-vnic-0001":           true,
		"HypoMux-vnic-123":            true,
		"HypoMux-vnic-00001":          false,
		"xuni-01":                     false,
		"HypoMux-Tun":                 false,
		"以太网":                         false,
		"vEthernet (HypoMux-vnic-01)": false,
		"":                            false,
	}
	for input, expected := range tests {
		if actual := isHyperVAdapterName(input); actual != expected {
			t.Fatalf("isHyperVAdapterName(%q) = %v; want %v", input, actual, expected)
		}
	}
}

func TestHypervAdapterSequence(t *testing.T) {
	tests := map[string]int{
		"HypoMux-vnic-01": 1,
		"HypoMux-vnic-09": 9,
		"HypoMux-vnic-12": 12,
		"xuni-01":         1 << 30,
	}
	for input, expected := range tests {
		if actual := hypervAdapterSequence(input); actual != expected {
			t.Fatalf("hypervAdapterSequence(%q) = %d; want %d", input, actual, expected)
		}
	}
}

// TestHypervHostInterfaceName 锁死出口池的键：宿主机网卡别名恒为
// vEthernet (<Hyper-V 对象名>)（reports/vnic/60 实测）。
func TestHypervHostInterfaceName(t *testing.T) {
	if actual := hypervHostInterfaceName("HypoMux-vnic-01"); actual != "vEthernet (HypoMux-vnic-01)" {
		t.Fatalf("hypervHostInterfaceName = %q", actual)
	}
}

func TestHypervObjectNameFromInterface(t *testing.T) {
	tests := map[string]struct {
		alias    string
		expected string
		found    bool
	}{
		"hypomux":      {"vEthernet (HypoMux-vnic-01)", "HypoMux-vnic-01", true},
		"case folded":  {"vethernet (hypomux-vnic-07)", "hypomux-vnic-07", true},
		"not ours":     {"以太网", "", false},
		"user vnic":    {"vEthernet (WSL)", "", false},
		"unmanaged vm": {"vEthernet (xuni-01)", "", false},
		"no parens":    {"vEthernet HypoMux-vnic-01", "", false},
		"empty":        {"", "", false},
	}
	for name, testCase := range tests {
		actual, ok := hypervObjectNameFromInterface(testCase.alias)
		if ok != testCase.found || actual != testCase.expected {
			t.Fatalf("%s: hypervObjectNameFromInterface(%q) = (%q, %v); want (%q, %v)",
				name, testCase.alias, actual, ok, testCase.expected, testCase.found)
		}
	}
}

// TestHypervMACValueLocalAdministration 位规范（§3.7）：首字节 0x02 ⇒ 本地管理 + 单播。
func TestHypervMACValueLocalAdministration(t *testing.T) {
	first := normalizeHypervMAC(hypervMACValue(0))
	if len(first) != 12 {
		t.Fatalf("hypervMACValue(0) = %q; want 12 hex chars", first)
	}
	if first[:4] != "021a" {
		t.Fatalf("hypervMACValue(0) = %q; want prefix 021a (0x02 locally administered)", first)
	}
	second := normalizeHypervMAC(hypervMACValue(1))
	if first == second {
		t.Fatalf("hypervMACValue(0) == hypervMACValue(1) = %q; MAC 必须逐张不同", first)
	}
}

// TestHypervLedgerAllocateNamesNoReuse 锁死 §3.7 的「序号不回收」：删除后也不能把序号
// 退回给下一张卡，否则路由器侧的 MAC↔IP 租约记忆会错位。
func TestHypervLedgerAllocateNamesNoReuse(t *testing.T) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	first := ledger.allocateNames(2, "batch-a", "2026-01-01T00:00:00Z")
	if first[0].Name != "HypoMux-vnic-00" || first[1].Name != "HypoMux-vnic-01" {
		t.Fatalf("allocateNames names = %q, %q", first[0].Name, first[1].Name)
	}
	if first[0].MACAddress == first[1].MACAddress {
		t.Fatal("同一批次的 MAC 必须互不相同")
	}
	for _, entry := range first {
		ledger.upsert(entry)
	}
	ledger.removeEntry("HypoMux-vnic-00")
	if len(ledger.Adapters) != 1 {
		t.Fatalf("removeEntry 后应有 1 条，实际 %d 条", len(ledger.Adapters))
	}
	second := ledger.allocateNames(1, "batch-b", "2026-01-01T00:00:01Z")
	if second[0].Name == "HypoMux-vnic-00" {
		t.Fatalf("序号被回收了：%q", second[0].Name)
	}
	if second[0].Name != "HypoMux-vnic-02" {
		t.Fatalf("下一张应是 HypoMux-vnic-02，实际 %q", second[0].Name)
	}
	if second[0].MACAddress == first[0].MACAddress || second[0].MACAddress == first[1].MACAddress {
		t.Fatalf("MAC 被回收了：%q", second[0].MACAddress)
	}
}

func TestHypervLedgerRoundTrip(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())

	empty, err := loadHypervLedger()
	if err != nil {
		t.Fatalf("首次读取台账不应报错：%v", err)
	}
	if len(empty.Adapters) != 0 {
		t.Fatalf("首次读取应为空台账，实际 %d 条", len(empty.Adapters))
	}

	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	reserved := ledger.allocateNames(3, "batch-a", "2026-01-01T00:00:00Z")
	for _, entry := range reserved {
		ledger.upsert(entry)
	}
	if err := ledger.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded, err := loadHypervLedger()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(reloaded.Adapters) != 3 {
		t.Fatalf("重载后应有 3 条，实际 %d 条", len(reloaded.Adapters))
	}
	entry, index := reloaded.find("HypoMux-vnic-01")
	if index < 0 {
		t.Fatal("按名字查不到 HypoMux-vnic-01")
	}
	if entry.MACAddress != reserved[1].MACAddress || entry.BatchID != "batch-a" {
		t.Fatalf("重载内容不一致：%+v", entry)
	}
	// 大小写不敏感：Windows 本身也不区分。
	if _, index := reloaded.find("hypomux-vnic-01"); index < 0 {
		t.Fatal("名字比较应当不区分大小写")
	}
	// 不该留下临时文件（atomic_file.go 的 .tmp + Rename 语义）。
	entries, err := os.ReadDir(hypervDirectory())
	if err != nil {
		t.Fatalf("读取数据目录失败：%v", err)
	}
	for _, item := range entries {
		if filepath.Ext(item.Name()) == ".tmp" {
			t.Fatalf("原子写留下了临时文件：%s", item.Name())
		}
	}
}

// TestLoadHypervLedgerCorrupt 损坏的台账必须显式报错，而不是静默当成空台账 —— 后者
// 会让「系统里有卡、台账说没有」，那是最难恢复的状态。
func TestLoadHypervLedgerCorrupt(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HYPOMUX_DATA_DIR", directory)
	if err := os.MkdirAll(filepath.Join(directory, "hyperv"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(hypervLedgerPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadHypervLedger(); err == nil {
		t.Fatal("损坏的台账应当报错")
	} else if coded, ok := err.(*HyperVError); !ok || coded.Code != hypervCodeLedger {
		t.Fatalf("应当返回 ledger_unavailable 错误码，实际 %v", err)
	}
}

func TestHypervLedgerFindAndUpsert(t *testing.T) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	ledger.upsert(hypervLedgerEntry{Name: "HypoMux-vnic-03", AdapterID: "{a}", MACAddress: "02:1a:2b:00:00:03"})
	ledger.upsert(hypervLedgerEntry{Name: "HypoMux-vnic-03", AdapterID: "{b}", MACAddress: "02:1a:2b:00:00:03"})
	if len(ledger.Adapters) != 1 {
		t.Fatalf("upsert 同名应覆盖而不是追加，实际 %d 条", len(ledger.Adapters))
	}
	entry, _ := ledger.find("HypoMux-vnic-03")
	if entry.AdapterID != "{b}" {
		t.Fatalf("upsert 未覆盖：%q", entry.AdapterID)
	}
	ledger.removeEntry("does-not-exist")
	if len(ledger.Adapters) != 1 {
		t.Fatal("移除不存在的条目不应改变台账")
	}
}

// TestHypervCheckBatchCapacity 覆盖 §3.7 的两级上限。
func TestHypervCheckBatchCapacity(t *testing.T) {
	tests := map[string]struct {
		existing int
		count    int
		wantCode string
	}{
		"fresh one":      {0, 1, ""},
		"full batch":     {0, hypervMaxBatchSize, ""},
		"batch too big":  {0, hypervMaxBatchSize + 1, hypervCodeBatchTooLarge},
		"zero":           {0, 0, hypervCodeBatchTooLarge},
		"negative":       {0, -1, hypervCodeBatchTooLarge},
		"fills the pool": {hypervMaxTotalAdapters - hypervMaxBatchSize, hypervMaxBatchSize, ""},
		"over the pool":  {hypervMaxTotalAdapters - hypervMaxBatchSize + 1, hypervMaxBatchSize, hypervCodeTotalLimitReached},
		"almost full":    {hypervMaxTotalAdapters, 1, hypervCodeTotalLimitReached},
	}
	for name, testCase := range tests {
		err := hypervCheckBatchCapacity(testCase.existing, testCase.count)
		if testCase.wantCode == "" {
			if err != nil {
				t.Fatalf("%s: 不应报错，实际 %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: 应当报错", name)
		}
		coded, ok := err.(*HyperVError)
		if !ok || coded.Code != testCase.wantCode {
			t.Fatalf("%s: 错误码 = %v; want %s", name, err, testCase.wantCode)
		}
	}
}

// TestDeriveHyperVState 是状态机的完整矩阵（reports/vnic/62 §4.4）。
func TestDeriveHyperVState(t *testing.T) {
	now := time.Now()
	fresh := hypervLedgerEntry{State: hypervStateCreating, CreatedAt: now.Format(time.RFC3339)}
	old := hypervLedgerEntry{State: hypervStateCreating, CreatedAt: now.Add(-2 * hypervOverallTimeout).Format(time.RFC3339)}
	markedFailed := hypervLedgerEntry{State: hypervStateFailed, CreatedAt: now.Format(time.RFC3339), LastError: "boom"}
	noTimestamp := hypervLedgerEntry{State: hypervStateCreating}

	tests := map[string]struct {
		entry       hypervLedgerEntry
		hostPresent bool
		hasIPv4     bool
		hypervLive  bool
		expected    string
	}{
		"ready":              {fresh, true, true, true, hypervStateReady},
		"creating no ip":     {fresh, true, false, true, hypervStateCreating},
		"creating pending":   {fresh, false, false, true, hypervStateCreating},
		"expired no ip":      {old, true, false, true, hypervStateFailed},
		"expired no object":  {old, false, false, true, hypervStateFailed},
		"absent":             {fresh, false, false, false, hypervStateAbsent},
		"absent old":         {old, false, false, false, hypervStateAbsent},
		"failed is sticky":   {markedFailed, true, true, true, hypervStateFailed},
		"missing created at": {noTimestamp, false, false, false, hypervStateAbsent},
	}
	for name, testCase := range tests {
		actual := deriveHyperVState(testCase.entry, testCase.hostPresent, testCase.hasIPv4, testCase.hypervLive, now)
		if actual != testCase.expected {
			t.Fatalf("%s: deriveHyperVState = %q; want %q", name, actual, testCase.expected)
		}
	}
}

// TestHypervEntryExpired 锁死 60s 整体预算。
func TestHypervEntryExpired(t *testing.T) {
	now := time.Now()
	fresh := hypervLedgerEntry{CreatedAt: now.Add(-hypervOverallTimeout + 5*time.Second).Format(time.RFC3339)}
	expired := hypervLedgerEntry{CreatedAt: now.Add(-hypervOverallTimeout - time.Second).Format(time.RFC3339)}
	if hypervEntryExpired(fresh, now) {
		t.Fatal("59s 的条目不应判为超时")
	}
	if !hypervEntryExpired(expired, now) {
		t.Fatal("61s 的条目应判为超时")
	}
	// 时间戳坏掉时保持保守：不算超时，但 List() 会因为没有网卡而落到 creating/absent。
	broken := hypervLedgerEntry{CreatedAt: "not-a-time"}
	if hypervEntryExpired(broken, now) {
		t.Fatal("坏时间戳不应判为超时")
	}
}

func newTestHostSnapshot(alias string, mac string, address string, prefix int) hypervHostSnapshot {
	host := hypervHostInterface{
		alias:        alias,
		mac:          normalizeHypervMAC(mac),
		address:      address,
		prefixLength: prefix,
		hasIPv4:      address != "",
	}
	snapshot := hypervHostSnapshot{
		byMAC:   map[string]hypervHostInterface{},
		byAlias: map[string]hypervHostInterface{},
	}
	snapshot.byAlias[strings.ToLower(alias)] = host
	if host.mac != "" {
		snapshot.byMAC[host.mac] = host
	}
	return snapshot
}

// TestBuildHyperVStatus 覆盖 List() 的核心投影：归属、MAC 优先认领、出口池成员。
func TestBuildHyperVStatus(t *testing.T) {
	now := time.Now()
	entry := hypervLedgerEntry{
		Name:       "HypoMux-vnic-01",
		AdapterID:  "{guid-1}",
		MACAddress: "02:1a:2b:00:00:01",
		SwitchName: "XuniUplink",
		BatchID:    "batch-a",
		State:      hypervStateCreating,
		CreatedAt:  now.Format(time.RFC3339),
	}
	hosts := newTestHostSnapshot("vEthernet (HypoMux-vnic-01)", "021A2B000001", "192.168.7.31", 24)
	inventory := hypervInventory{Adapters: []hypervScriptAdapter{{
		Name: "HypoMux-vnic-01", AdapterID: "{guid-1}", MAC: "021A2B000001", SwitchName: "XuniUplink",
	}}}
	pool := map[string]bool{"vethernet (hypomux-vnic-01)": true}

	status := buildHyperVStatus(entry, inventory, hosts, pool, now)
	if !status.Managed {
		t.Fatal("台账内的条目必须是 managed")
	}
	if status.State != hypervStateReady {
		t.Fatalf("state = %q; want ready", status.State)
	}
	if status.InterfaceName != "vEthernet (HypoMux-vnic-01)" {
		t.Fatalf("interfaceName = %q", status.InterfaceName)
	}
	if status.Address != "192.168.7.31" || status.PrefixLength != 24 {
		t.Fatalf("地址/前缀 = %q/%d", status.Address, status.PrefixLength)
	}
	if status.MacAddress != "02:1a:2b:00:00:01" {
		t.Fatalf("macAddress = %q", status.MacAddress)
	}
	if !status.InPool {
		t.Fatal("interfaceName 在池中，inPool 应为 true")
	}
	if status.LastError != "" {
		t.Fatalf("ready 的条目不该有 lastError，实际 %q", status.LastError)
	}

	// 不在池里：inPool 必须为 false。
	status = buildHyperVStatus(entry, inventory, hosts, map[string]bool{}, now)
	if status.InPool {
		t.Fatal("空池时 inPool 应为 false")
	}

	// 网卡出现但还没有 IPv4 ⇒ creating，MAC 已认领。
	emptyHosts := newTestHostSnapshot("vEthernet (HypoMux-vnic-01)", "021A2B000001", "", 0)
	status = buildHyperVStatus(entry, inventory, emptyHosts, map[string]bool{}, now)
	if status.State != hypervStateCreating {
		t.Fatalf("state = %q; want creating", status.State)
	}
	if status.Address != "" {
		t.Fatalf("没有 IPv4 时不该有 address，实际 %q", status.Address)
	}

	// 失败条目要带上原因，而不是空白。
	failedEntry := entry
	failedEntry.State = hypervStateFailed
	failedEntry.LastError = "创建失败：交换机拒绝"
	status = buildHyperVStatus(failedEntry, inventory, hosts, map[string]bool{}, now)
	if status.State != hypervStateFailed || status.LastError == "" {
		t.Fatalf("failed 条目 = %q / %q", status.State, status.LastError)
	}

	// 超期的 creating 条目 → failed + DHCP 超时文案（不删卡）。
	staleEntry := entry
	staleEntry.CreatedAt = now.Add(-2 * hypervOverallTimeout).Format(time.RFC3339)
	status = buildHyperVStatus(staleEntry, inventory, emptyHosts, map[string]bool{}, now)
	if status.State != hypervStateFailed {
		t.Fatalf("超期条目 state = %q; want failed", status.State)
	}
	if status.LastError != hypervDHCPTimeoutHint {
		t.Fatalf("超期条目 lastError = %q", status.LastError)
	}

	// MAC 认领优先于别名认领：网卡被改名后仍要归位到正确的那张。
	renamedHosts := newTestHostSnapshot("以太网 3", "021A2B000001", "10.0.0.9", 8)
	status = buildHyperVStatus(entry, hypervInventory{}, renamedHosts, map[string]bool{}, now)
	if status.InterfaceName != "以太网 3" {
		t.Fatalf("MAC 认领失败，interfaceName = %q", status.InterfaceName)
	}
}

// TestBuildUnmanagedHyperVStatus 台账外的同规范网卡：可见但不可管。
func TestBuildUnmanagedHyperVStatus(t *testing.T) {
	adapter := hypervScriptAdapter{
		Name: "HypoMux-vnic-09", AdapterID: "{ghost}", MAC: "021A2B000009", SwitchName: "XuniUplink",
	}
	hosts := newTestHostSnapshot("vEthernet (HypoMux-vnic-09)", "021A2B000009", "192.168.7.39", 24)
	status := buildUnmanagedHyperVStatus(adapter, hosts, map[string]bool{"vethernet (hypomux-vnic-09)": true})
	if status.Managed {
		t.Fatal("台账外的条目必须是 managed=false（Remove 会直接拒绝）")
	}
	if status.State != hypervStateReady {
		t.Fatalf("state = %q; want ready", status.State)
	}
	if !status.InPool {
		t.Fatal("别名在池里时 inPool 应为 true")
	}
	if status.BatchID != "" || status.CreatedAt != "" {
		t.Fatal("台账外的条目不该有批次号与创建时间")
	}
}

// TestHypervInventoryFind DeviceId 是稳定主键；名字兜底时必须要求 MAC 非空，否则会命中
// reports/vnic/60 实测到的同名幽灵记录。
func TestHypervInventoryFind(t *testing.T) {
	inventory := hypervInventory{Adapters: []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "", MAC: ""},
		{Name: "xuni-01", AdapterID: "{real}", MAC: "F6E6556A4D17"},
	}}
	if _, ok := inventory.find("", "xuni-01"); !ok {
		t.Fatal("应当跳过幽灵记录命中真实那条")
	}
	found, ok := inventory.find("{real}", "")
	if !ok || found.MAC != "F6E6556A4D17" {
		t.Fatalf("按 DeviceId 查找失败：%+v", found)
	}
	if _, ok := inventory.find("{nope}", ""); ok {
		t.Fatal("不该命中")
	}
	// 只有幽灵记录时，hasName 必须返回 false。
	ghostOnly := hypervInventory{Adapters: []hypervScriptAdapter{{Name: "HypoMux-vnic-05", MAC: ""}}}
	if ghostOnly.hasName("HypoMux-vnic-05") {
		t.Fatal("MAC 为空的幽灵记录不算占用名字")
	}
	if !inventory.hasName("XUNI-01") {
		t.Fatal("hasName 应当不区分大小写")
	}
}

// TestScanHypervHostInterfacesCoversHost 拿 net.Interfaces() 当预言机：别名映射必须覆盖
// 宿主机上每一张网卡（§3.8 明确不能复用 AdapterService.List()，因为它会丢掉没有 IPv4
// 的网卡），MAC 键必须归一化成 12 位小写十六进制，APIPA 地址不得被当成就绪。
func TestScanHypervHostInterfacesCoversHost(t *testing.T) {
	snapshot := scanHypervHostInterfaces()
	if snapshot.byMAC == nil || snapshot.byAlias == nil {
		t.Fatal("映射不应为 nil")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}
	if len(interfaces) == 0 {
		t.Skip("当前环境没有网卡")
	}
	for _, item := range interfaces {
		alias := strings.ToLower(item.Name)
		host, ok := snapshot.byAlias[alias]
		if !ok {
			t.Fatalf("扫描漏掉了网卡 %q", item.Name)
		}
		wantMAC := normalizeHypervMAC(item.HardwareAddr.String())
		if host.mac != wantMAC {
			t.Fatalf("网卡 %q 的 MAC = %q; want %q", item.Name, host.mac, wantMAC)
		}
		if host.mac != "" {
			byMAC, found := snapshot.byMAC[host.mac]
			if !found || byMAC.alias != host.alias {
				t.Fatalf("MAC 索引缺失或错位：%s", host.mac)
			}
		}
		if host.hasIPv4 {
			if strings.HasPrefix(host.address, "169.254.") {
				t.Fatalf("网卡 %q 的 APIPA 地址不应被当作就绪：%s", item.Name, host.address)
			}
			if host.prefixLength <= 0 || host.prefixLength > 32 {
				t.Fatalf("网卡 %q 的前缀长度不合理：%d", item.Name, host.prefixLength)
			}
		} else if host.address != "" {
			t.Fatalf("网卡 %q 未就绪时不该带地址：%q", item.Name, host.address)
		}
	}
}

// TestCreateRejectsInvalidArguments 参数校验必须在平台检查之前，这样跨平台 CI 也能覆盖。
func TestCreateRejectsInvalidArguments(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	service := NewHyperVAdapterService(NewSettingsService(), NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	tests := map[string]struct {
		switchName string
		count      int
		wantCode   string
	}{
		"empty switch":   {"", 1, hypervCodeSwitchNotFound},
		"blank switch":   {"   ", 1, hypervCodeSwitchNotFound},
		"zero count":     {"XuniUplink", 0, hypervCodeBatchTooLarge},
		"negative count": {"XuniUplink", -3, hypervCodeBatchTooLarge},
		"too many":       {"XuniUplink", hypervMaxBatchSize + 1, hypervCodeBatchTooLarge},
	}
	for name, testCase := range tests {
		_, err := service.Create(testCase.switchName, testCase.count)
		if err == nil {
			t.Fatalf("%s: 应当报错", name)
		}
		coded, ok := err.(*HyperVError)
		if !ok || coded.Code != testCase.wantCode {
			t.Fatalf("%s: 错误码 = %v; want %s", name, err, testCase.wantCode)
		}
	}
}

// TestRemoveRejectsForeignAdapters 归属判定的第一、二道闸门：命名不合规或不在台账里的
// 名字一律拒绝，绝不进入提权删除流程。
func TestRemoveRejectsForeignAdapters(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	service := NewHyperVAdapterService(NewSettingsService(), NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	tests := map[string]string{
		"empty":       "",
		"user lan":    "以太网",
		"foreign vm":  "xuni-01",
		"wsl":         "vEthernet (WSL)",
		"tun legacy":  "HypoMux-Tun",
		"no prefix":   "Hyper-V Virtual Ethernet Adapter",
		"unknown own": "HypoMux-vnic-77",
	}
	for name, target := range tests {
		err := service.Remove(target)
		if err == nil {
			t.Fatalf("%s: Remove(%q) 应当被拒绝", name, target)
		}
		coded, ok := err.(*HyperVError)
		if !ok || coded.Code != hypervCodeNotManaged {
			t.Fatalf("%s: Remove(%q) 错误码 = %v; want %s", name, target, err, hypervCodeNotManaged)
		}
	}
}

// TestRemoveAfterShutdown 服务关闭后拒绝调用。
func TestRemoveAfterShutdown(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	service := NewHyperVAdapterService(NewSettingsService(), NewAdapterService(NewSettingsService()))
	service.Shutdown()
	service.Shutdown() // 幂等
	if _, err := service.List(); err == nil {
		t.Fatal("Shutdown 后 List 应当报错")
	}
	if err := service.Remove("HypoMux-vnic-01"); err == nil {
		t.Fatal("Shutdown 后 Remove 应当报错")
	}
}

// TestApplyPoolUpdateMerge 出口池的读-改-写合并（reports/vnic/62 §8）：只增删本次涉及的
// 网卡，其余选择与权重必须原样保留。
func TestApplyPoolUpdateMerge(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	if _, err := settings.UpdateHome("tun", true, []string{"以太网"}, map[string]int{"以太网": 42}); err != nil {
		t.Fatalf("预置出口池失败：%v", err)
	}
	if err := service.applyPoolUpdate([]string{"vEthernet (HypoMux-vnic-01)", "vEthernet (HypoMux-vnic-02)"}, nil); err != nil {
		t.Fatalf("applyPoolUpdate: %v", err)
	}
	current := settings.Get()
	if len(current.SelectedAdapterIDs) != 3 {
		t.Fatalf("selected = %v", current.SelectedAdapterIDs)
	}
	if current.SelectedAdapterIDs[0] != "以太网" {
		t.Fatalf("原有选择应保持在首位：%v", current.SelectedAdapterIDs)
	}
	if current.AdapterWeights["以太网"] != 42 {
		t.Fatalf("原有权重被改动了：%d", current.AdapterWeights["以太网"])
	}
	if current.AdapterWeights["vEthernet (HypoMux-vnic-01)"] != AdapterWeightDefault {
		t.Fatalf("新网卡应给默认权重：%v", current.AdapterWeights)
	}
	if current.Mode != "tun" || !current.Weighted {
		t.Fatalf("mode/weighted 必须被保留：%q/%v", current.Mode, current.Weighted)
	}

	// 重复添加应幂等。
	if err := service.applyPoolUpdate([]string{"vEthernet (HypoMux-vnic-01)"}, nil); err != nil {
		t.Fatalf("重复添加失败：%v", err)
	}
	if len(settings.Get().SelectedAdapterIDs) != 3 {
		t.Fatalf("重复添加改变了选择列表：%v", settings.Get().SelectedAdapterIDs)
	}

	// 移除要同时清掉权重，避免 settings.json 留下孤儿条目。
	if err := service.applyPoolUpdate(nil, []string{"vEthernet (HypoMux-vnic-02)"}); err != nil {
		t.Fatalf("移除失败：%v", err)
	}
	after := settings.Get()
	if len(after.SelectedAdapterIDs) != 2 {
		t.Fatalf("移除后 selected = %v", after.SelectedAdapterIDs)
	}
	if _, exists := after.AdapterWeights["vEthernet (HypoMux-vnic-02)"]; exists {
		t.Fatal("移除后不应残留权重条目")
	}

	// 空操作不落盘。
	before, err := os.Stat(settings.ConfigPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := service.applyPoolUpdate(nil, nil); err != nil {
		t.Fatalf("空操作不应报错：%v", err)
	}
	after1, err := os.Stat(settings.ConfigPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !before.ModTime().Equal(after1.ModTime()) {
		t.Fatal("空操作不应重写 settings.json")
	}
}

// TestReadHypervResultGuards 结果文件是唯一回传通道，必须有上限保护。
func TestReadHypervResultGuards(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	if _, err := readHypervResult(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("缺失的结果文件应当报错")
	}

	oversize := filepath.Join(t.TempDir(), "big.json")
	payload := make([]byte, hypervMaxResultBytes+1)
	payload[0] = '{'
	if err := os.WriteFile(oversize, payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readHypervResult(oversize); err == nil {
		t.Fatal("超过 1 MiB 的结果文件应当被拒绝")
	}

	broken := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readHypervResult(broken); err == nil {
		t.Fatal("坏 JSON 应当报错")
	}
}

// TestReadHypervResultShape 结果文件必须是无 BOM 的 UTF-8 JSON，且字段名与 Go 结构对齐。
func TestReadHypervResultShape(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "result.json")
	body := `{"ok":true,"code":"ok","error":"","adapters":[{"name":"HypoMux-vnic-01","adapterId":"{g}","mac":"021A2B000001","switchName":"XuniUplink","status":"Ok"}],"switches":[{"name":"XuniUplink","type":"External","allowManagementOs":true,"uplink":"以太网","netAdapterName":"以太网"}],"addresses":[],"failures":[]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := readHypervResult(path)
	if err != nil {
		t.Fatalf("readHypervResult: %v", err)
	}
	if !result.OK || len(result.Adapters) != 1 || len(result.Switches) != 1 {
		t.Fatalf("结果解析不正确：%+v", result)
	}
	if !result.Switches[0].AllowManagementOS {
		t.Fatal("allowManagementOs 解析丢失")
	}
	// 确认写出去的是标准 UTF-8（Go 侧只接受无 BOM）。
	roundTrip, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if roundTrip[0] != '{' {
		t.Fatalf("序列化结果应以 { 开头：%q", roundTrip[0])
	}
}

func TestMapHypervRunErrorCodes(t *testing.T) {
	tests := map[string]struct {
		err      error
		elevated bool
		op       string
		wantCode string
	}{
		"cancelled":         {errHypervElevationCancelled, true, "create", hypervCodeElevationCancelled},
		"create timeout":    {errHypervScriptTimeout, true, "create", hypervCodeCreateTimeout},
		"remove timeout":    {errHypervScriptTimeout, true, "remove", hypervCodeRemoveTimeout},
		"read timeout":      {errHypervScriptTimeout, false, "inventory", hypervCodeUnavailable},
		"ctx canceled":      {context.Canceled, true, "create", hypervCodeShuttingDown},
		"unsupported":       {errHypervUnsupported, true, "create", hypervCodeUnavailable},
		"deadline exceeded": {context.DeadlineExceeded, true, "create", hypervCodeCreateTimeout},
	}
	for name, testCase := range tests {
		mapped := mapHypervRunError(testCase.err, testCase.elevated, testCase.op)
		if mapped.Code != testCase.wantCode {
			t.Fatalf("%s: code = %q; want %q", name, mapped.Code, testCase.wantCode)
		}
	}
}

func TestHyperVErrorMessageCarriesCode(t *testing.T) {
	err := hypervErrorf(hypervCodeNotManaged, "%s 不在台账中", "HypoMux-vnic-01")
	if err.Error() != "not_managed：HypoMux-vnic-01 不在台账中" {
		t.Fatalf("Error() = %q", err.Error())
	}
	withDetail := &HyperVError{Code: hypervCodeCreateFailed, Message: "创建失败", Detail: "底层原因"}
	if withDetail.Error() != "create_failed：创建失败（底层原因）" {
		t.Fatalf("Error() = %q", withDetail.Error())
	}
	var nilError *HyperVError
	if nilError.Error() != "" {
		t.Fatal("nil 错误应返回空串")
	}
}

func TestToHyperVSwitchesSorted(t *testing.T) {
	rows := toHyperVSwitches([]hypervScriptSwitch{
		{Name: "Zeta", Type: "External", AllowManagementOS: true, Uplink: "  ", NetAdapterName: "以太网"},
		{Name: "Alpha", Type: "Internal"},
	})
	if len(rows) != 2 || rows[0].Name != "Alpha" || rows[1].Name != "Zeta" {
		t.Fatalf("排序不正确：%+v", rows)
	}
	if rows[0].Uplink != "" || rows[1].NetAdapterName != "以太网" {
		t.Fatalf("空白未清理：%+v", rows)
	}
	if _, ok := findHyperVSwitch(rows, "zeta"); !ok {
		t.Fatal("按名字查找应当不区分大小写")
	}
	if _, ok := findHyperVSwitch(rows, "nope"); ok {
		t.Fatal("不该命中")
	}
}

func TestSortHyperVStatuses(t *testing.T) {
	rows := []HyperVAdapterStatus{
		{Name: "HypoMux-vnic-10"},
		{Name: "xuni-01"},
		{Name: "HypoMux-vnic-02"},
		{Name: "HypoMux-vnic-01"},
	}
	sortHyperVStatuses(rows)
	if rows[0].Name != "HypoMux-vnic-01" || rows[1].Name != "HypoMux-vnic-02" || rows[2].Name != "HypoMux-vnic-10" {
		t.Fatalf("排序不正确：%v", rows)
	}
	if rows[3].Name != "xuni-01" {
		t.Fatalf("非本工具的名字应排到最后：%v", rows)
	}
}

// TestNewHypervJobPathUnique 任务目录必须自动创建且每次拿到不同路径。
func TestNewHypervJobPathUnique(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		path, err := newHypervJobPath()
		if err != nil {
			t.Fatalf("newHypervJobPath: %v", err)
		}
		if filepath.Dir(path) != hypervJobDirectory() {
			t.Fatalf("任务路径不在任务目录：%s", path)
		}
		if seen[path] {
			t.Fatalf("任务路径重复：%s", path)
		}
		seen[path] = true
	}
}

// TestHypervUTF16LE 只在 Windows 上有意义（-EncodedCommand 要求 UTF-16LE base64）。
func TestHypervUTF16LE(t *testing.T) {
	encoded := hypervUTF16LE("Add-VMNetworkAdapter")
	if len(encoded) != len("Add-VMNetworkAdapter")*2 {
		t.Fatalf("UTF-16LE 长度 = %d", len(encoded))
	}
	if encoded[0] != 'A' || encoded[1] != 0x00 || encoded[2] != 'd' || encoded[3] != 0x00 {
		t.Fatalf("UTF-16LE 字节序不对：%v", encoded[:4])
	}
}

// TestHypervPowerShellScriptIsConstantAndSafe 锁死 §3.3：脚本正文里绝不能出现任何
// 会改动系统/路由/交换机的 cmdlet，也绝不能出现反引号（Go 原始字符串字面量的边界）。
func TestHypervPowerShellScriptIsConstantAndSafe(t *testing.T) {
	script := hypervPowerShellScript
	forbidden := []string{
		"Remove-VMSwitch",
		"Set-VMSwitch",
		"New-VMSwitch",
		"Restart-NetAdapter",
		"Disable-WindowsOptionalFeature",
		"netcfg",
		"Set-DnsClientServerAddress",
		"New-NetRoute",
		"Remove-NetAdapter",
		"`",
	}
	for _, needle := range forbidden {
		if containsHypervScript(script, needle) {
			t.Fatalf("脚本里出现了禁止的内容：%q", needle)
		}
	}
	for _, required := range []string{
		"Add-VMNetworkAdapter -ManagementOS",
		"-StaticMacAddress",
		"-PassThru",
		"Remove-VMNetworkAdapter -ErrorAction Stop",
		"$targets | Remove-VMNetworkAdapter",
		"Get-VMNetworkAdapter -ManagementOS",
		"Get-VMSwitch",
		"New-Object Text.UTF8Encoding($false)",
		"Move-Item -LiteralPath $tmp",
	} {
		if !containsHypervScript(script, required) {
			t.Fatalf("脚本缺少必需的内容：%q", required)
		}
	}
}

func containsHypervScript(script string, needle string) bool {
	return len(script) >= len(needle) && indexOfHypervScript(script, needle) >= 0
}

func indexOfHypervScript(haystack string, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// =================================================================
// 审计修复轮（reports/vnic/75-audit-go.md 的 M1–M4）
// =================================================================

// TestApplyPoolUpdatePreservesSchedulingStrategy —— 审计 M1。
//
// 原实现调 SettingsService.UpdateHome(mode, weighted, selected, weights)，而
// settings.go:413 把第 5 个参数（strategy）硬编码成 ""，于是 scheduling.go:162-168
// 的 normalizeSchedulingStrategy("") 归一成 weighted/round-robin：选「延迟优先」或
// 「自适应吞吐」的用户，只要创建或删除一张虚拟网卡，调度策略就被静默清零。
//
// 这条测试正是旧版漏掉的那一条：旧 TestApplyPoolUpdateMerge 用 UpdateHome 预置
// （自身就把 Strategy 设成 weighted），且全文件零处断言 Strategy。
func TestApplyPoolUpdatePreservesSchedulingStrategy(t *testing.T) {
	for _, strategy := range []string{"latency-first", "adaptive-throughput", "round-robin", "weighted"} {
		t.Run(strategy, func(t *testing.T) {
			t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
			settings := NewSettingsService()
			service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
			t.Cleanup(service.Shutdown)

			// 预置成非默认策略 + 已有一个出口。
			if _, err := settings.updateHomeStrategy("tun", false, []string{"以太网"}, map[string]int{"以太网": 42}, strategy); err != nil {
				t.Fatalf("预置调度策略失败：%v", err)
			}
			if got := settings.Get().Strategy; got != strategy {
				t.Fatalf("预置后 Strategy = %q; want %q", got, strategy)
			}

			// 创建路径：加两张虚拟网卡。
			if err := service.applyPoolUpdate([]string{"vEthernet (HypoMux-vnic-01)", "vEthernet (HypoMux-vnic-02)"}, nil); err != nil {
				t.Fatalf("applyPoolUpdate(新增) 失败：%v", err)
			}
			if got := settings.Get().Strategy; got != strategy {
				t.Fatalf("新增虚拟网卡后 Strategy 被改成 %q; want %q", got, strategy)
			}

			// 删除路径：移除一张。
			if err := service.applyPoolUpdate(nil, []string{"vEthernet (HypoMux-vnic-02)"}); err != nil {
				t.Fatalf("applyPoolUpdate(移除) 失败：%v", err)
			}
			if got := settings.Get().Strategy; got != strategy {
				t.Fatalf("删除虚拟网卡后 Strategy 被改成 %q; want %q", got, strategy)
			}

			// 顺带确认 Mode / 权重也照样保留。
			current := settings.Get()
			if current.Mode != "tun" {
				t.Fatalf("Mode = %q; want tun", current.Mode)
			}
			if current.AdapterWeights["以太网"] != 42 {
				t.Fatalf("原有权重被改动：%v", current.AdapterWeights)
			}
			if len(current.SelectedAdapterIDs) != 2 {
				t.Fatalf("selected = %v", current.SelectedAdapterIDs)
			}
		})
	}
}

// TestRemoveKeepsLedgerWhenInventoryQueryFails —— 审计 M2 的注入式测试。
//
// 原实现 `inventory, _ := s.readInventory()` 丢弃了错误：查询失败时 found=false，提权
// 删除整段被跳过，却照样清台账 + 移出出口池 ⇒ Hyper-V 对象还在、归属记录没了，UI 里
// 再点删除永远返回 not_managed，用户彻底删不掉这张卡。
func TestRemoveKeepsLedgerWhenInventoryQueryFails(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	// 台账里登记一张卡，出口池里也有它。
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	allocated := ledger.allocateNames(1, "batch-a", "2026-01-01T00:00:00Z")
	for _, entry := range allocated {
		ledger.upsert(entry)
	}
	if err := ledger.save(); err != nil {
		t.Fatalf("写台账失败：%v", err)
	}
	target := allocated[0].Name
	if _, err := settings.UpdateHome("tun", true, []string{hypervHostInterfaceName(target)}, nil); err != nil {
		t.Fatalf("预置出口池失败：%v", err)
	}

	// 注入「系统实况查询失败」。
	service.mu.Lock()
	service.inventoryHook = func() (hypervInventory, error) {
		return hypervInventory{}, errors.New("模拟 Get-VMNetworkAdapter 查询失败")
	}
	service.mu.Unlock()

	err := service.Remove(target)
	if err == nil {
		t.Fatal("查询失败时 Remove 应当报错")
	}
	coded, ok := err.(*HyperVError)
	if !ok || coded.Code != hypervCodeUnavailable {
		t.Fatalf("错误码 = %v; want %s", err, hypervCodeUnavailable)
	}

	// 关键断言：台账条目必须还在（否则用户永远删不掉这张卡）。
	reloaded, loadErr := loadHypervLedger()
	if loadErr != nil {
		t.Fatalf("读台账失败：%v", loadErr)
	}
	if _, index := reloaded.find(target); index < 0 {
		t.Fatalf("台账条目 %s 被误删了——这会留下用户永远删不掉的孤儿网卡", target)
	}
	// 出口池也不该被动过。
	pool := settings.Get().SelectedAdapterIDs
	if _, found := findString(pool, hypervHostInterfaceName(target)); !found {
		t.Fatalf("出口池被误改了：%v", pool)
	}
}

// TestHypervScriptOutcomeUncertain —— 审计 M3 的判定表。
//
// 台账宁可多不可少：只有「脚本一次都没跑起来」（UAC 取消 / 平台不支持）才可安全地按
// 失败处理；超时、进程退出、结果文件缺失或坏 JSON 都属于「结果不可知」。
func TestHypervScriptOutcomeUncertain(t *testing.T) {
	produced := &hypervScriptResult{OK: true}
	producedFailed := &hypervScriptResult{OK: false, Code: "script_failed", Error: "boom"}
	tests := map[string]struct {
		result   *hypervScriptResult
		runErr   error
		expected bool
	}{
		"uac cancelled":     {nil, &HyperVError{Code: hypervCodeElevationCancelled, Message: "用户取消"}, false},
		"unsupported":       {nil, &HyperVError{Code: hypervCodeUnavailable, Message: "非 Windows"}, false},
		"script never ran":  {nil, errors.New("创建任务目录失败"), false},
		"no error":          {nil, nil, false},
		"create timeout":    {nil, &HyperVError{Code: hypervCodeCreateTimeout, Message: "超时"}, true},
		"shutting down":     {nil, &HyperVError{Code: hypervCodeShuttingDown, Message: "关闭中"}, true},
		"result file gone":  {nil, &HyperVError{Code: hypervCodeScriptFailed, Message: "没有结果文件"}, true},
		"script said ok":    {produced, nil, false},
		"script said fail":  {producedFailed, nil, false},
		"file even on fail": {producedFailed, &HyperVError{Code: hypervCodeScriptFailed}, false},
	}
	for name, testCase := range tests {
		if actual := hypervScriptOutcomeUncertain(testCase.result, testCase.runErr); actual != testCase.expected {
			t.Fatalf("%s: hypervScriptOutcomeUncertain = %v; want %v", name, actual, testCase.expected)
		}
	}
}

// TestHypervReconcileBatchTimeoutKeepsLedger —— 审计 M3 的核心回归。
//
// 父进程杀不掉提权子进程（§3.3 明文），超时后子进程仍可能把整批卡全建出来。原实现把
// 它们全部 removeEntry ⇒ N 张用户永远删不掉的孤儿网卡，同时字面违反「超时只报错不改
// 状态」。现在超时必须：一条都不抹、整批标 failed、返回可辨识的 create_timeout。
func TestHypervReconcileBatchTimeoutKeepsLedger(t *testing.T) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	reserved := ledger.allocateNames(3, "batch-a", "2026-01-01T00:00:00Z")
	for _, entry := range reserved {
		ledger.upsert(entry)
	}
	timeoutErr := &HyperVError{Code: hypervCodeCreateTimeout, Message: "提权脚本执行超时"}

	outcome := hypervReconcileBatch(reserved, map[string]hypervScriptAdapter{}, nil, timeoutErr, nil)

	if !outcome.abandoned {
		t.Fatal("超时应标记 abandoned（不启动 awaitBatch）")
	}
	if len(outcome.purge) != 0 {
		t.Fatalf("超时时绝不能抹台账，实际抹了 %v", outcome.purge)
	}
	if len(outcome.finished) != len(reserved) {
		t.Fatalf("finished = %d 条; want %d", len(outcome.finished), len(reserved))
	}
	for _, entry := range outcome.finished {
		if entry.State != hypervStateFailed {
			t.Fatalf("%s 的 State = %q; want failed", entry.Name, entry.State)
		}
		if entry.LastError == "" {
			t.Fatalf("%s 缺少说明文案", entry.Name)
		}
		// 归属必须完好：名字与 MAC 都还在，用户才能 Remove。
		if entry.MACAddress == "" {
			t.Fatalf("%s 的 MAC 被清空了", entry.Name)
		}
	}
	if outcome.failure == nil || outcome.failure.Code != hypervCodeCreateTimeout {
		t.Fatalf("failure = %v; want code %s", outcome.failure, hypervCodeCreateTimeout)
	}

	// 端到端：写回台账后条目仍然存在。
	for _, entry := range outcome.finished {
		ledger.upsert(entry)
	}
	for _, name := range outcome.purge {
		ledger.removeEntry(name)
	}
	if len(ledger.Adapters) != 3 {
		t.Fatalf("超时应保留 3 条归属，实际 %d 条", len(ledger.Adapters))
	}
}

// TestHypervReconcileBatchReportedFailure 保留 §3.7 的正常语义：脚本**明确回报**失败时，
// 硬失败之后未处理的条目从未存在过，可以抹掉。
func TestHypervReconcileBatchReportedFailure(t *testing.T) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	reserved := ledger.allocateNames(3, "batch-a", "2026-01-01T00:00:00Z")
	created := map[string]hypervScriptAdapter{
		"hypomux-vnic-00": {Name: "HypoMux-vnic-00", AdapterID: "{g0}", MAC: "021A2B000000", SwitchName: "XuniUplink"},
	}
	failures := []hypervScriptFailure{{Name: "HypoMux-vnic-01", Error: "交换机拒绝绑定"}}
	// 脚本跑完了并逐条交代了成败 ⇒ 结果可信。
	outcome := hypervReconcileBatch(reserved, created, failures, nil, &hypervScriptResult{OK: false, Failures: failures})

	if outcome.abandoned {
		t.Fatal("脚本明确回报失败不属于「不可知」")
	}
	if len(outcome.purge) != 1 || outcome.purge[0] != "HypoMux-vnic-02" {
		t.Fatalf("purge = %v; want [HypoMux-vnic-02]", outcome.purge)
	}
	if len(outcome.finished) != 2 {
		t.Fatalf("finished = %d 条; want 2（成功 + 首个失败）", len(outcome.finished))
	}
	if outcome.finished[0].AdapterID != "{g0}" || outcome.finished[0].State == hypervStateFailed {
		t.Fatalf("成功项应回填 DeviceId 且不标 failed：%+v", outcome.finished[0])
	}
	if outcome.finished[1].State != hypervStateFailed {
		t.Fatalf("失败项 State = %q; want failed", outcome.finished[1].State)
	}
	if outcome.failure == nil || outcome.failure.Code != hypervCodeCreateFailed {
		t.Fatalf("failure = %v; want code %s", outcome.failure, hypervCodeCreateFailed)
	}
	if !strings.Contains(outcome.finished[1].LastError, "第 2/3 张") {
		t.Fatalf("失败文案应指明第几张：%q", outcome.finished[1].LastError)
	}
}

// TestHypervResolveAddressesScriptVerdictWins —— 审计 M4 的核心回归。
//
// 原兜底只要宿主上有任意非 APIPA 的 IPv4 就翻案，脚本明确回报 PrefixOrigin=Static /
// AddressState=Duplicate 被正确拒绝之后又被翻回 ready ⇒ 从未拿到 DHCP 租约的卡被标
// ready、进入出口池、重启后聚合静默失效。
func TestHypervResolveAddressesScriptVerdictWins(t *testing.T) {
	const alias = "vEthernet (HypoMux-vnic-01)"
	// 宿主侧确实有非 APIPA 的 IPv4（静态地址也会产生），最容易误翻案。
	hosts := newTestHostSnapshot(alias, "021A2B000001", "192.168.7.31", 24)

	tests := map[string]struct {
		reported    []hypervScriptAddress
		wantReady   bool
		wantAddress string
	}{
		"dhcp preferred is ready": {
			[]hypervScriptAddress{{Alias: alias, Address: "192.168.7.31", PrefixLength: 24, PrefixOrigin: "Dhcp", AddressState: "Preferred"}},
			true, "192.168.7.31",
		},
		// ↓ 这三条是审计点：脚本已明确否决，兜底不得翻案。
		"static must not be rescued": {
			[]hypervScriptAddress{{Alias: alias, Address: "192.168.7.31", PrefixLength: 24, PrefixOrigin: "Static", AddressState: "Preferred"}},
			false, "",
		},
		"duplicate must not be rescued": {
			[]hypervScriptAddress{{Alias: alias, Address: "192.168.7.31", PrefixLength: 24, PrefixOrigin: "Dhcp", AddressState: "Duplicate"}},
			false, "",
		},
		"tentative must not be rescued": {
			[]hypervScriptAddress{{Alias: alias, Address: "192.168.7.31", PrefixLength: 24, PrefixOrigin: "Dhcp", AddressState: "Tentative"}},
			false, "",
		},
		// 脚本连这个 alias 都没提（这轮轮询里网卡还没出现）才允许兜底。
		"unmentioned alias may use fallback": {
			nil, true, "192.168.7.31",
		},
	}
	for name, testCase := range tests {
		key := strings.ToLower(alias)
		resolved := hypervResolveAddresses([]string{alias}, testCase.reported, hosts)
		state, found := resolved[key]
		if !found {
			t.Fatalf("%s: 结果里没有 %s", name, alias)
		}
		if state.ready != testCase.wantReady {
			t.Fatalf("%s: ready = %v; want %v", name, state.ready, testCase.wantReady)
		}
		if state.address != testCase.wantAddress {
			t.Fatalf("%s: address = %q; want %q", name, state.address, testCase.wantAddress)
		}
		wantFallback := testCase.reported == nil
		if state.fallback != wantFallback {
			t.Fatalf("%s: fallback = %v; want %v", name, state.fallback, wantFallback)
		}
	}
}

// TestHypervResolveAddressesSkipsAPIPAFallback 兜底也不能把 169.254/16 当成就绪。
func TestHypervResolveAddressesSkipsAPIPAFallback(t *testing.T) {
	const alias = "vEthernet (HypoMux-vnic-02)"
	apipa := newTestHostSnapshot(alias, "021A2B000002", "169.254.9.10", 16)
	resolved := hypervResolveAddresses([]string{alias}, nil, apipa)
	if state := resolved[strings.ToLower(alias)]; state.ready {
		t.Fatalf("APIPA 地址不应判就绪：%+v", state)
	}
}

// TestHypervResolveAddressesIsolatesAliases 多张卡时，一张被脚本否决不能连累别的卡。
func TestHypervResolveAddressesIsolatesAliases(t *testing.T) {
	const good = "vEthernet (HypoMux-vnic-01)"
	const bad = "vEthernet (HypoMux-vnic-02)"
	hosts := newTestHostSnapshot(good, "021A2B000001", "192.168.7.31", 24)
	hosts.byAlias[strings.ToLower(bad)] = hypervHostInterface{
		alias: bad, mac: "021a2b000002", address: "192.168.7.32", prefixLength: 24, hasIPv4: true,
	}
	resolved := hypervResolveAddresses(
		[]string{good, bad},
		[]hypervScriptAddress{{Alias: bad, Address: "192.168.7.32", PrefixLength: 24, PrefixOrigin: "Dhcp", AddressState: "Duplicate"}},
		hosts,
	)
	if state := resolved[strings.ToLower(good)]; !state.ready || !state.fallback {
		t.Fatalf("未被脚本提到的卡应走兜底并就绪：%+v", state)
	}
	if state := resolved[strings.ToLower(bad)]; state.ready {
		t.Fatalf("被脚本否决的卡不得翻案：%+v", state)
	}
}
