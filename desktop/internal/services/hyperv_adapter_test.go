package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
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
// 冻结契约 reports/vnic/60 §编号分配：编号取 01–99，index = max(已记录) + 1。
// 零值台账的首张卡必须是 -01，绝不是 -00（真机端到端实测到过 -00，见 76 §15）。
func TestHypervLedgerAllocateNamesNoReuse(t *testing.T) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	first := ledger.allocateNames(2, "batch-a", "2026-01-01T00:00:00Z")
	if first[0].Name != "HypoMux-vnic-01" || first[1].Name != "HypoMux-vnic-02" {
		t.Fatalf("allocateNames names = %q, %q", first[0].Name, first[1].Name)
	}
	if first[0].MACAddress == first[1].MACAddress {
		t.Fatal("同一批次的 MAC 必须互不相同")
	}
	for _, entry := range first {
		ledger.upsert(entry)
	}
	ledger.removeEntry("HypoMux-vnic-01")
	if len(ledger.Adapters) != 1 {
		t.Fatalf("removeEntry 后应有 1 条，实际 %d 条", len(ledger.Adapters))
	}
	second := ledger.allocateNames(1, "batch-b", "2026-01-01T00:00:01Z")
	if second[0].Name == "HypoMux-vnic-01" {
		t.Fatalf("序号被回收了：%q", second[0].Name)
	}
	if second[0].Name != "HypoMux-vnic-03" {
		t.Fatalf("下一张应是 HypoMux-vnic-03，实际 %q", second[0].Name)
	}
	if second[0].MACAddress == first[0].MACAddress || second[0].MACAddress == first[1].MACAddress {
		t.Fatalf("MAC 被回收了：%q", second[0].MACAddress)
	}
}

// 首启台账（adapters.json 不存在）的首个编号必须是 01。零值 NextSeq 直接发号会得到
// HypoMux-vnic-00，与冻结契约 reports/vnic/60 §编号分配 冲突。
func TestHypervLedgerFirstSequenceStartsAtOne(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())

	fresh, err := loadHypervLedger()
	if err != nil {
		t.Fatalf("首启读台账不应报错：%v", err)
	}
	if fresh.NextSeq != hypervFirstAdapterSeq {
		t.Fatalf("首启 NextSeq = %d，期望 %d", fresh.NextSeq, hypervFirstAdapterSeq)
	}
	entry := fresh.allocateNames(1, "batch-first", "2026-01-01T00:00:00Z")[0]
	if entry.Name != "HypoMux-vnic-01" {
		t.Fatalf("首张卡应为 HypoMux-vnic-01，实际 %q", entry.Name)
	}
}

// v2.7.0 已经发出过 HypoMux-vnic-00 的台账，抬序号后不得回收 00，也不得跳过 01。
func TestHypervLedgerNormalizeKeepsAlreadyIssuedZeroSequence(t *testing.T) {
	ledger := hypervLedger{
		Version:  hypervLedgerVersion,
		NextSeq:  0,
		NextMAC:  1,
		Adapters: []hypervLedgerEntry{{Name: "HypoMux-vnic-00", MACAddress: "02:1a:2b:00:00:00"}},
	}
	ledger.normalize()
	if ledger.NextSeq != hypervFirstAdapterSeq {
		t.Fatalf("NextSeq = %d，期望被抬到 %d", ledger.NextSeq, hypervFirstAdapterSeq)
	}
	if ledger.NextMAC != 1 {
		t.Fatalf("normalize 不得改动 MAC 计数器，NextMAC = %d", ledger.NextMAC)
	}
	entry := ledger.allocateNames(1, "batch-b", "2026-01-01T00:00:00Z")[0]
	if entry.Name != "HypoMux-vnic-01" {
		t.Fatalf("下一张应是 HypoMux-vnic-01，实际 %q", entry.Name)
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
	if entry.MACAddress != reserved[0].MACAddress || entry.BatchID != "batch-a" {
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

// TestHypervShowUnmanagedAdapter 用户自己建的卡也必须出现在页面上。
//
// 这条断言的存在理由是**防止有人把命名过滤加回来**：那个过滤看起来是"多挡一层误删"，
// 但 Remove() 的两道闸门（命名规范 + 台账归属）都不依赖它，所以它只带来副作用——把
// xuni-01 这类用户自建卡从「虚拟网卡（Hyper-V）」页整个抹掉。
func TestHypervShowUnmanagedAdapter(t *testing.T) {
	cases := []struct {
		label   string
		name    string
		claimed map[string]bool
		want    bool
	}{
		{"用户自建的卡可见", "xuni-01", map[string]bool{}, true},
		{"大小写不同的自建卡也可见", "XUNI-01", map[string]bool{}, true},
		{"本工具建的但台账丢了也可见", "HypoMux-vnic-09", map[string]bool{}, true},
		{"已被台账认领的不重复展示", "xuni-01", map[string]bool{"xuni-01": true}, false},
		{"认领判定大小写不敏感", "Xuni-01", map[string]bool{"xuni-01": true}, false},
		{"空名不展示", "", map[string]bool{}, false},
		{"纯空白名不展示", "   ", map[string]bool{}, false},
	}
	for _, item := range cases {
		t.Run(item.label, func(t *testing.T) {
			if actual := hypervShowUnmanagedAdapter(item.name, item.claimed); actual != item.want {
				t.Fatalf("hypervShowUnmanagedAdapter(%q, %v) = %v; want %v",
					item.name, item.claimed, actual, item.want)
			}
		})
	}
}

// TestHypervSkipOccupiedMAC 台账归零但宿主留着上一轮的卡时，MAC 计数器必须跳过占用值。
//
// 真机复现见 reports/vnic/79-e2e-regression.md 的 D1：全新台账 nextMAC=0 发出
// 02:1a:2b:00:00:00，与宿主遗留卡逐字节撞车，Add-VMNetworkAdapter 整批失败。
func TestHypervSkipOccupiedMAC(t *testing.T) {
	macOf := func(counter int) string { return normalizeHypervMAC(hypervMACValue(counter)) }
	occupiedOf := func(counters ...int) map[string]bool {
		occupied := map[string]bool{}
		for _, counter := range counters {
			occupied[macOf(counter)] = true
		}
		return occupied
	}
	cases := []struct {
		label    string
		start    int
		count    int
		occupied map[string]bool
		want     int
	}{
		{"全空时原样返回", 0, 1, map[string]bool{}, 0},
		{"起点被占用则顺延", 0, 1, occupiedOf(0), 1},
		{"连续三个都被占用则跳到 3", 0, 1, occupiedOf(0, 1, 2), 3},
		{"中段被占用也能跨过去", 0, 1, occupiedOf(0, 2), 1},
		{
			"空洞不构成可用段：4 空、5 占时 count=2 必须退到 6",
			4, 2, occupiedOf(5), 6,
		},
		{"count=2 且前两个都空则返回起点", 4, 2, occupiedOf(6, 7), 4},
		{"负数起点按 0 处理", -5, 1, map[string]bool{}, 0},
		{"count 非正数直接失败", 3, 0, map[string]bool{}, -1},
	}
	for _, item := range cases {
		t.Run(item.label, func(t *testing.T) {
			actual := hypervSkipOccupiedMAC(item.start, item.count, item.occupied)
			if actual != item.want {
				t.Fatalf("hypervSkipOccupiedMAC(%d, %d, …) = %d; want %d",
					item.start, item.count, actual, item.want)
			}
		})
	}

	// 端到端接一次真实生产者：占用集合只能由 hypervOccupiedMACs 生成（它统一折叠成
	// 12 位小写十六进制）。这一条钉住「inventory 里那张遗留卡 ⇒ 首张卡跳过它」。
	t.Run("由 hypervOccupiedMACs 产出的集合可以直接跳过", func(t *testing.T) {
		inventory := hypervInventory{Adapters: []hypervScriptAdapter{
			{Name: "HypoMux-vnic-01", MAC: "021A2B000000"},
		}}
		occupied := hypervOccupiedMACs(inventory, &hypervLedger{})
		if actual := hypervSkipOccupiedMAC(0, 1, occupied); actual != 1 {
			t.Fatalf("宿主残留 02:1a:2b:00:00:00 时应从 1 起发号，实际 %d", actual)
		}
	})
}

// TestHypervSkipOccupiedMACExhausted 整个 24 位空间被占满时必须返回 -1，而不是绕回 0
// 重新发出已经用过的 MAC。
func TestHypervSkipOccupiedMACExhausted(t *testing.T) {
	if actual := hypervSkipOccupiedMAC(hypervMACCounterMax, 2, map[string]bool{}); actual != -1 {
		t.Fatalf("尾部只剩 1 个计数器却要 2 个时应返回 -1，实际 %d", actual)
	}
	if actual := hypervSkipOccupiedMAC(hypervMACCounterMax+1, 1, map[string]bool{}); actual != -1 {
		t.Fatalf("起点越过上界时应返回 -1，实际 %d", actual)
	}
}

// TestHypervOccupiedMACs 宿主实况与台账两条来源都要算进去：宿主覆盖台账丢失后的孤儿卡，
// 台账覆盖正在创建、宿主还没回报的预留条目。
func TestHypervOccupiedMACs(t *testing.T) {
	inventory := hypervInventory{Adapters: []hypervScriptAdapter{
		{Name: "xuni-01", MAC: "021A2B000001"},
		{Name: "ghost", MAC: ""},
	}}
	ledger := &hypervLedger{Adapters: []hypervLedgerEntry{
		{Name: "HypoMux-vnic-01", MACAddress: "02:1a:2b:00:00:02"},
		{Name: "HypoMux-vnic-09", MACAddress: "  "},
	}}
	occupied := hypervOccupiedMACs(inventory, ledger)
	for _, want := range []string{"021a2b000001", "021a2b000002"} {
		if !occupied[want] {
			t.Fatalf("MAC %s 应被视为已占用；实际集合 %v", want, occupied)
		}
	}
	for _, empty := range []string{"", "  "} {
		if _, exists := hypervOccupiedMACs(inventory, ledger)[empty]; exists {
			t.Fatalf("空 MAC %q 不应进入占用集合", empty)
		}
	}
}

// TestHypervFailureMessage 脚本回报了具体失败却对不上名字时，必须把原文吐出来，而不是
// 退化成「未知原因」（reports/vnic/79-e2e-regression.md 的 D2）。
func TestHypervFailureMessage(t *testing.T) {
	runErr := errors.New("PowerShell 以退出码 1 结束")
	cases := []struct {
		label    string
		failures []hypervScriptFailure
		name     string
		runErr   error
		want     string
	}{
		{
			"名字精确匹配优先",
			[]hypervScriptFailure{
				{Name: "other", Error: "别的卡的错"},
				{Name: "HypoMux-vnic-01", Error: "交换机忙"},
			},
			"HypoMux-vnic-01", nil, "交换机忙",
		},
		{
			"名字大小写不同也算匹配",
			[]hypervScriptFailure{{Name: "hypomux-vnic-01", Error: "交换机忙"}},
			"HypoMux-vnic-01", nil, "交换机忙",
		},
		{
			"名字对不上但只有一条失败时用它的原文（D2 回归点）",
			[]hypervScriptFailure{{Name: "", Error: "指定的 MAC 地址已存在"}},
			"HypoMux-vnic-01", runErr, "指定的 MAC 地址已存在",
		},
		{
			"多条无归属失败时拼接",
			[]hypervScriptFailure{
				{Name: "", Error: "MAC 已存在"},
				{Name: "", Error: "交换机忙"},
			},
			"HypoMux-vnic-01", nil, "MAC 已存在；交换机忙",
		},
		{
			"Error 为空的失败被忽略，退回 runErr",
			[]hypervScriptFailure{{Name: "HypoMux-vnic-01", Error: "   "}},
			"HypoMux-vnic-01", runErr, "PowerShell 以退出码 1 结束",
		},
		{
			"完全没有失败信息时才说未知原因",
			nil, "HypoMux-vnic-01", nil, "未知原因",
		},
	}
	for _, item := range cases {
		t.Run(item.label, func(t *testing.T) {
			actual := hypervFailureMessage(item.failures, item.name, item.runErr)
			if actual != item.want {
				t.Fatalf("hypervFailureMessage(…, %q, …) = %q; want %q", item.name, actual, item.want)
			}
		})
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

// TestHypervPoolKeyNormalizesObjectNameAndAlias —— 出口池的键必须恒等于宿主别名。
// Create 侧的 awaitInterfaces 传的是 Hyper-V 对象名，Remove 侧传的是拼好的别名，
// 真机端到端实测到过两者不对称的后果：Remove 删不到 Create 写进去的键，
// settings.json 留下永久悬空引用（reports/vnic/76 §16）。
func TestHypervPoolKeyNormalizesObjectNameAndAlias(t *testing.T) {
	const alias = "vEthernet (HypoMux-vnic-01)"
	cases := map[string]string{
		"HypoMux-vnic-01":     alias,                         // Create 侧：裸对象名
		alias:                 alias,                         // Remove 侧：已是别名
		"hypomux-vnic-01":     "vEthernet (hypomux-vnic-01)", // 保留输入大小写
		"  HypoMux-vnic-01  ": "vEthernet (HypoMux-vnic-01)",
		"以太网":                 "以太网",               // 真实网卡名原样
		"vEthernet (以太网 3)":   "vEthernet (以太网 3)", // 非本规范别名原样
		"":                    "",
	}
	for input, want := range cases {
		if got := hypervPoolKey(input); got != want {
			t.Fatalf("hypervPoolKey(%q) = %q，期望 %q", input, got, want)
		}
	}
	// 真正的下游判据是「忽略大小写能配上」：applyPoolUpdate 的 seen 用小写键、
	// findString 用 EqualFold、List() 的池查表也先转小写。大小写必须收敛到同一个键。
	if !strings.EqualFold(hypervPoolKey("hypomux-vnic-01"), hypervPoolKey(alias)) {
		t.Fatalf("大小写不同的两种写法必须收敛到同一个池键：%q vs %q",
			hypervPoolKey("hypomux-vnic-01"), hypervPoolKey(alias))
	}
}

// 对象名 / 宿主别名两种写法必须随时能互相还原，且**不可能**被包成双层串。
// awaitAddresses 要按别名查 DHCP、按对象名查台账；一旦哪一侧拿到双层的
// "vEthernet (vEthernet (HypoMux-vnic-01))"，台账的 state 就永远回写不了，
// 卡会一直停在 creating（reports/vnic/76 §16）。
func TestHypervObjectNameOrSelfNeverDoubleWraps(t *testing.T) {
	const object = "HypoMux-vnic-01"
	const alias = "vEthernet (HypoMux-vnic-01)"
	for _, in := range []string{object, alias, "  " + alias + "  "} {
		got := hypervObjectNameOrSelf(in)
		if got != object {
			t.Fatalf("hypervObjectNameOrSelf(%q) = %q，期望 %q", in, got, object)
		}
		// 再走一轮别名构造，必须稳定回到同一个别名（幂等，不累积括号）。
		if again := hypervHostInterfaceName(hypervObjectNameOrSelf(got)); again != alias {
			t.Fatalf("二次构造 %q 得到 %q，期望 %q", got, again, alias)
		}
	}
	// 非本规范的网卡名原样返回，绝不被误认成 HypoMux 卡。
	for _, foreign := range []string{"以太网", "vEthernet (以太网 3)", "Ethernet"} {
		if got := hypervObjectNameOrSelf(foreign); got != foreign {
			t.Fatalf("hypervObjectNameOrSelf(%q) = %q，应原样返回", foreign, got)
		}
	}
}

// applyPoolUpdate 绝不能就地改调用方传进来的切片：awaitBatch 把同一个 appeared
// 同时传给它和 awaitAddresses。就地归一过一次，awaitAddresses 再包一层就成了双层串，
// 台账 state 从此永远回写不了（reports/vnic/76 §16）。
func TestApplyPoolUpdateDoesNotMutateCallerSlice(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	add := []string{"HypoMux-vnic-01", "以太网"}
	remove := []string{"vEthernet (HypoMux-vnic-09)"}
	addCopy := append([]string(nil), add...)
	removeCopy := append([]string(nil), remove...)

	if err := service.applyPoolUpdate(add, remove); err != nil {
		t.Fatalf("applyPoolUpdate: %v", err)
	}
	for i := range add {
		if add[i] != addCopy[i] {
			t.Fatalf("applyPoolUpdate 改写了调用方的 add 切片：[%d] %q -> %q", i, addCopy[i], add[i])
		}
	}
	for i := range remove {
		if remove[i] != removeCopy[i] {
			t.Fatalf("applyPoolUpdate 改写了调用方的 remove 切片：[%d] %q -> %q", i, removeCopy[i], remove[i])
		}
	}
}

// 按真实调用点的形式走一遍：Create 用裸对象名并入，Remove 用别名移出。必须能配上。
func TestApplyPoolUpdateCreateThenRemoveIsSymmetric(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	if _, err := settings.UpdateHome("proxy", false, []string{"以太网"}, map[string]int{"以太网": 1}); err != nil {
		t.Fatalf("预置出口池失败：%v", err)
	}
	// Create 路径：awaitInterfaces 返回的是裸对象名。
	if err := service.applyPoolUpdate([]string{"HypoMux-vnic-01"}, nil); err != nil {
		t.Fatalf("applyPoolUpdate(并入) 失败：%v", err)
	}
	current := settings.Get()
	found := ""
	for _, id := range current.SelectedAdapterIDs {
		if id == "vEthernet (HypoMux-vnic-01)" {
			found = id
		}
		if id == "HypoMux-vnic-01" {
			t.Fatalf("出口池写进了裸对象名（引擎按别名绑定会失败）：%v", current.SelectedAdapterIDs)
		}
	}
	if found == "" {
		t.Fatalf("出口池里没有宿主别名：%v", current.SelectedAdapterIDs)
	}
	if current.AdapterWeights[found] != AdapterWeightDefault {
		t.Fatalf("权重键应与别名一致：%v", current.AdapterWeights)
	}

	// Remove 路径：传的是拼好的别名。
	if err := service.applyPoolUpdate(nil, []string{"vEthernet (HypoMux-vnic-01)"}); err != nil {
		t.Fatalf("applyPoolUpdate(移出) 失败：%v", err)
	}
	current = settings.Get()
	for _, id := range current.SelectedAdapterIDs {
		if id == "vEthernet (HypoMux-vnic-01)" || id == "HypoMux-vnic-01" {
			t.Fatalf("Remove 后仍留在出口池里（悬空引用）：%v", current.SelectedAdapterIDs)
		}
	}
	if len(current.SelectedAdapterIDs) != 1 || current.SelectedAdapterIDs[0] != "以太网" {
		t.Fatalf("Remove 后出口池应复原为 [以太网]：%v", current.SelectedAdapterIDs)
	}
	if _, ok := current.AdapterWeights["vEthernet (HypoMux-vnic-01)"]; ok {
		t.Fatalf("Remove 后权重项应一并清掉：%v", current.AdapterWeights)
	}
	if current.AdapterWeights["以太网"] != 1 {
		t.Fatalf("原有网卡权重被改动：%v", current.AdapterWeights)
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

// =================================================================
// 读取路径最小实现（reports/vnic/76：虚拟网卡页读取 Hyper-V 失败）
//
// 下面两条全是**纯函数**测试：既不 exec powershell.exe，也不读任何 Hyper-V 对象，
// 因此在没有 Hyper-V 的机器上（含 CI 的 windows runner）同样能编过并通过。
// =================================================================

// TestHypervReadInventoryScriptIsConstantAndReadOnly 锁死「读取路径零插值」这条前提。
//
// 只要命令行是常量，注入面就天然为零；一旦有人往正文里塞占位符或用户数据，这条测试
// 立刻红。同时它也复用了写路径那份「危险 cmdlet 黑名单」，防只读脚本被改成会动系统的版本。
func TestHypervReadInventoryScriptIsConstantAndReadOnly(t *testing.T) {
	script := hypervReadInventoryScript
	forbidden := []string{
		"Remove-VMNetworkAdapter",
		"Add-VMNetworkAdapter",
		"Remove-VMSwitch",
		"Set-VMSwitch",
		"New-VMSwitch",
		"Restart-NetAdapter",
		"Disable-NetAdapter",
		"Disable-WindowsOptionalFeature",
		"netcfg",
		"New-NetRoute",
		"Set-DnsClientServerAddress",
		// 命令行正文经 exec 参数转义与 PowerShell 引号解析两次处理，双引号会打架
		// （reports/vnic/76 实测约束）：只允许单引号。
		`"`,
		// 读取路径不做 base64 注入：出现它就说明有人把写路径那套搬回来了。
		"FromBase64String",
		"EncodedCommand",
		// 读取路径不落结果文件。
		"WriteAllText",
		"Move-Item",
	}
	for _, needle := range forbidden {
		if containsHypervScript(script, needle) {
			t.Fatalf("只读脚本里出现了禁止的内容：%q", needle)
		}
	}
	for _, required := range []string{
		"Get-VMNetworkAdapter -ManagementOS",
		"Get-VMSwitch",
		"ConvertTo-Json -InputObject $state -Compress -Depth 4",
		"adapters = @($adapters)",
		"switches = @($switches)",
	} {
		if !containsHypervScript(script, required) {
			t.Fatalf("只读脚本缺少必需的内容：%q", required)
		}
	}
}

// TestHypervParseInventoryOutput 覆盖 stdout → 结构这条纯函数边界。
//
// 真实机器上这份 JSON 长这样（reports/vnic/76 实测）：
// {"ok":true,"code":"ok","error":"","adapters":[{"name":"xuni-01","adapterId":"{...}","mac":"DE3192575DE6","switchName":"XuniUplink","status":"Ok"}],"switches":[{"name":"XuniUplink","type":"External","allowManagementOs":true,"uplink":"...","netAdapterName":"以太网"}]}
func TestHypervParseInventoryOutput(t *testing.T) {
	t.Run("真实形状", func(t *testing.T) {
		stdout := []byte(`{"ok":true,"code":"ok","error":"","adapters":[{"name":"Container NIC 9d844023","adapterId":"{A696632E-C105-43EC-A32B-529104EE054B}","mac":"00155D3EC386","switchName":"Default Switch","status":"Ok"}],"switches":[{"name":"Default Switch","type":"Internal","allowManagementOs":true,"uplink":"","netAdapterName":""},{"name":"XuniUplink","type":"External","allowManagementOs":true,"uplink":"Realtek Gaming 2.5GbE Family Controller","netAdapterName":"以太网"}]}`)
		result, err := hypervParseInventoryOutput(stdout)
		if err != nil {
			t.Fatalf("解析失败：%v", err)
		}
		if !result.OK || len(result.Adapters) != 1 || len(result.Switches) != 2 {
			t.Fatalf("形状不对：ok=%v adapters=%d switches=%d", result.OK, len(result.Adapters), len(result.Switches))
		}
		if result.Adapters[0].Name != "Container NIC 9d844023" || result.Adapters[0].MAC != "00155D3EC386" {
			t.Fatalf("适配器字段没对上：%+v", result.Adapters[0])
		}
		// 单元素数组必须仍然是数组 —— 这是 ConvertTo-Json 在 PS 5.1 下的老坑，
		// 解析侧若退化成对象，这里会直接暴露。
		if result.Switches[0].Name != "Default Switch" || result.Switches[0].Type != "Internal" {
			t.Fatalf("交换机字段没对上：%+v", result.Switches[0])
		}
		if !result.Switches[1].AllowManagementOS || result.Switches[1].NetAdapterName != "以太网" {
			t.Fatalf("外部交换机字段没对上：%+v", result.Switches[1])
		}
	})

	t.Run("空集合", func(t *testing.T) {
		result, err := hypervParseInventoryOutput([]byte(`{"ok":true,"code":"ok","error":"","adapters":[],"switches":[]}`))
		if err != nil {
			t.Fatalf("解析失败：%v", err)
		}
		if len(result.Adapters) != 0 || len(result.Switches) != 0 {
			t.Fatalf("空集合被解析成了非空：%+v", result)
		}
	})

	t.Run("脚本自报失败", func(t *testing.T) {
		result, err := hypervParseInventoryOutput([]byte(`{"ok":false,"code":"script_failed","error":"Hyper-V 未安装","adapters":[],"switches":[]}`))
		if err != nil {
			t.Fatalf("ok=false 不该在解析层报错：%v", err)
		}
		if result.OK || result.Error != "Hyper-V 未安装" {
			t.Fatalf("失败信息没传上来：%+v", result)
		}
	})

	t.Run("BOM 与噪声", func(t *testing.T) {
		result, err := hypervParseInventoryOutput([]byte("\ufeff  \nWARNING: something\r\n{\"ok\":true,\"adapters\":[],\"switches\":[]}\n"))
		if err != nil {
			t.Fatalf("解析失败：%v", err)
		}
		if !result.OK {
			t.Fatalf("噪声前缀把结果弄坏了：%+v", result)
		}
	})

	t.Run("无输出", func(t *testing.T) {
		if _, err := hypervParseInventoryOutput(nil); err == nil {
			t.Fatal("stdout 为空时必须报错")
		}
		if _, err := hypervParseInventoryOutput([]byte("   \r\n")); err == nil {
			t.Fatal("stdout 空白时必须报错")
		}
	})

	t.Run("坏 JSON 必须把原文带回", func(t *testing.T) {
		// 这条是本次故障的教训：报告里只有「脚本以退出码 X 结束」等于什么都没说，
		// 所以解析失败时必须把 stdout 原文塞进错误里。
		_, err := hypervParseInventoryOutput([]byte("PowerShell[.exe] [-PSConsoleFile <file> ..."))
		if err == nil {
			t.Fatal("非 JSON 输出必须报错")
		}
		if !strings.Contains(err.Error(), "PowerShell[.exe]") {
			t.Fatalf("错误里必须带 stdout 原文：%v", err)
		}
		if _, err := hypervParseInventoryOutput([]byte(`{"ok":true,"adapters":[}`)); err == nil {
			t.Fatal("截断的 JSON 必须报错")
		}
	})
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
		"hypomux-vnic-01": {Name: "HypoMux-vnic-01", AdapterID: "{g0}", MAC: "021A2B000000", SwitchName: "XuniUplink"},
	}
	failures := []hypervScriptFailure{{Name: "HypoMux-vnic-02", Error: "交换机拒绝绑定"}}
	// 脚本跑完了并逐条交代了成败 ⇒ 结果可信。
	outcome := hypervReconcileBatch(reserved, created, failures, nil, &hypervScriptResult{OK: false, Failures: failures})

	if outcome.abandoned {
		t.Fatal("脚本明确回报失败不属于「不可知」")
	}
	if len(outcome.purge) != 1 || outcome.purge[0] != "HypoMux-vnic-03" {
		t.Fatalf("purge = %v; want [HypoMux-vnic-03]", outcome.purge)
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

// ------------------------------------------------------ 写路径 payload 播种（回归）

// TestHypervSeededCommandBodyPutsPayloadInVariable 锁住 reports/vnic/76 的两个缺陷：
//  1. payload 曾经是 -EncodedCommand 之后的尾随位置参数 → PowerShell 5.1 拒绝执行脚本；
//  2. 改成播种 $args 也不行 —— PowerShell 里**函数的 $args 是它自己的实参**，会遮蔽
//     脚本作用域的 $args，而 Read-Envelope 正是函数（真机实测 function_sees_args=NULL-EMPTY）。
//
// 所以 payload 必须落在一个不会被遮蔽的专用变量上，且脚本本体保持纯常量。
func TestHypervSeededCommandBodyPutsPayloadInVariable(t *testing.T) {
	payload := []byte(`{"op":"create","resultPath":"C:\\tmp\\job.json","items":["O'Brien\"; rm -rf /","以太网"],"aliases":["以太网"]}`)
	body := hypervSeededCommandBody(payload)

	seed, script, found := strings.Cut(body, "\n")
	if !found {
		t.Fatalf("正文第一行之后必须是换行 + 常量脚本：%q", body[:80])
	}
	if script != hypervPowerShellScript {
		t.Fatalf("常量脚本本体必须逐字不变（得到 %d 字节，期望 %d 字节）", len(script), len(hypervPowerShellScript))
	}

	// 第一行是唯一承载可变数据的地方。
	prefix := "$" + hypervPayloadVariable + " = '"
	if !strings.HasPrefix(seed, prefix) || !strings.HasSuffix(seed, "'") {
		t.Fatalf("第一行形状不对：%q（期望 $%s = '<base64>'）", seed, hypervPayloadVariable)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(seed, prefix), "'")
	if encoded == "" {
		t.Fatal("播种的 base64 不能为空")
	}

	// 注入面论证的结构性前提：base64 字母表不含单引号/反引号/空格/$，逃不出那个单引号字面量。
	// 上面刻意塞了 O'Brien"; rm -rf / 这种恶意 payload，这一行就是它的守门人。
	for _, bad := range []rune{'\'', '`', '$', ' ', '\t', '\r', '\n', ';', '"'} {
		if strings.ContainsRune(encoded, bad) {
			t.Fatalf("播种值含非法字符 %q：%q", bad, encoded)
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("播种值不是合法 base64：%v", err)
	}
	if string(decoded) != string(payload) {
		t.Fatalf("播种值解回来不是原 payload：%q", decoded)
	}

	// 脚本侧必须读同一个变量，且不得再读 $args[0]。
	if !strings.Contains(hypervPowerShellScript, "$"+hypervPayloadVariable) {
		t.Fatalf("脚本必须读取 $%s", hypervPayloadVariable)
	}
	if strings.Contains(hypervPowerShellScript, "$args[0]") {
		t.Fatal("脚本不得再读 $args[0]：函数里的 $args 是函数自己的实参，恒为空")
	}
}

// TestHypervPowerShellArgumentsHaveNoTrailingToken 就是防这次故障回归的那条断言。
func TestHypervPowerShellArgumentsHaveNoTrailingToken(t *testing.T) {
	payload := []byte(`{"op":"create","resultPath":"C:\\tmp\\job.json","items":["以太网"],"aliases":[]}`)
	args := hypervPowerShellArguments(payload)

	want := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand"}
	if len(args) != len(want)+1 {
		t.Fatalf("参数个数 = %d，期望 %d（-EncodedCommand 之后不得有任何尾随 token）：%q",
			len(args), len(want)+1, func() []string {
				short := make([]string, len(args))
				for i, a := range args {
					if len(a) > 24 {
						short[i] = a[:24] + "..."
					} else {
						short[i] = a
					}
				}
				return short
			}())
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q，期望 %q", i, args[i], want[i])
		}
	}

	encoded := args[len(args)-1]
	if encoded == "" || containsHypervScript(encoded, " ") || containsHypervScript(encoded, "'") {
		t.Fatalf("-EncodedCommand 的值必须是无空格无单引号的 base64（提权路径会把它 join 回命令行）：%q", encoded)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("-EncodedCommand 必须是合法 base64：%v", err)
	}
	if len(decoded)%2 != 0 {
		t.Fatalf("-EncodedCommand 解码后应为 UTF-16LE（偶数字节），得到 %d 字节", len(decoded))
	}
	// 还原 UTF-16LE 正文，确认它就是播种后的那段命令。
	body := decodeHypervUTF16LEForTest(t, encoded)
	if body != hypervSeededCommandBody(payload) {
		t.Fatal("-EncodedCommand 正文与 hypervSeededCommandBody 不一致")
	}
	if !strings.HasPrefix(body, "$"+hypervPayloadVariable+" = '") {
		t.Fatalf("正文必须以 payload 播种开头：%q", body[:60])
	}
}

// decodeHypervUTF16LEForTest 是 hypervUTF16LE 的解码侧，让单测能反向验证命令正文。
func decodeHypervUTF16LEForTest(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE 字节数应为偶数，得到 %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

// Create 成功时返回的是 nil *HyperVError。若直接装箱进 error 接口，调用方会看到
// err != nil 且 err.Error() == "" ——「卡建好了却弹一个没有文字的错误」。真机端到端
// 实测到过（reports/vnic/76 §15）。本测试锁住 hypervFailureError 的装箱语义，
// 不依赖 Hyper-V 环境。
func TestHypervFailureErrorBoxesNilAsTrueNil(t *testing.T) {
	var failure *HyperVError
	if err := hypervFailureError(failure); err != nil {
		t.Fatalf("nil *HyperVError 必须转成真正的 nil error，实际得到 err != nil，Error()=%q", err.Error())
	}
	real := hypervErrorf(hypervCodeLedger, "Hyper-V 台账写入失败")
	err := hypervFailureError(real)
	if err == nil {
		t.Fatal("非 nil *HyperVError 必须原样保留成 error")
	}
	if err.Error() != real.Error() {
		t.Fatalf("错误文案被改写：%q != %q", err.Error(), real.Error())
	}
	if !errors.Is(err, real) {
		t.Fatal("包装后的 error 必须仍指向原 *HyperVError")
	}
}

// ---------------------------------------------------- 后端提示标记（i18n 契约）

// TestHypervHintCodeAndDetail 锁住「机器可读标记 + 运行时细节」的切分契约。
//
// 前端 managementAdapters.ts 按同一条规则切分后查语言包；两边不一致就是 en locale
// 又开始显示中文（reports/vnic/79 §A）。所以这里逐个 case 钉死。
func TestHypervHintCodeAndDetail(t *testing.T) {
	tests := map[string]struct {
		lastError  string
		wantCode   string
		wantDetail string
		wantIsHint bool
	}{
		"empty": {"", "", "", false},
		// 纯空白不是标记；细节按原文带回（前端自己折叠空白并 trim）。
		"blank":     {"   ", "", "   ", false},
		"free text": {"等待 60 秒仍未从交换机拿到可用 IPv4", "", "等待 60 秒仍未从交换机拿到可用 IPv4", false},
		// 自由文本里恰好含分隔符，不得被误当成「标记 + 细节」。
		"free text with separator": {"PowerShell | 退出码 1", "", "PowerShell | 退出码 1", false},
		"prefix lookalike":         {"hypomux_hint.pool_restart", "", "hypomux_hint.pool_restart", false},
		"code only":                {hypervHintPoolRestart, hypervHintPoolRestart, "", true},
		"code plus detail": {
			hypervHintWithDetail(hypervHintPoolUpdateFailed, "更新出口池失败：磁盘已满"),
			hypervHintPoolUpdateFailed, "更新出口池失败：磁盘已满", true,
		},
		"blank detail collapses": {
			hypervHintWithDetail(hypervHintPoolUpdateFailed, "   "),
			hypervHintPoolUpdateFailed, "", true,
		},
		// 细节里还有分隔符：按**第一个**切，后面的整体算细节。
		"detail keeps later separators": {
			hypervHintWithDetail(hypervHintPoolUpdateFailed, "a | b | c"),
			hypervHintPoolUpdateFailed, "a | b | c", true,
		},
	}
	for name, testCase := range tests {
		got := hypervHintCode(testCase.lastError)
		if got != testCase.wantCode {
			t.Fatalf("%s: hypervHintCode(%q) = %q; want %q", name, testCase.lastError, got, testCase.wantCode)
		}
		if isHint := got != ""; isHint != testCase.wantIsHint {
			t.Fatalf("%s: 是否标记 = %v; want %v", name, isHint, testCase.wantIsHint)
		}
		// 前端按同一个规则切：标记 + 第一个分隔符之后的部分。
		detail := testCase.lastError
		if isHint := got != ""; isHint {
			detail = ""
			if _, rest, found := strings.Cut(testCase.lastError, hypervHintDetailSeparator); found {
				detail = rest
			}
		}
		if detail != testCase.wantDetail {
			t.Fatalf("%s: 细节 = %q; want %q", name, detail, testCase.wantDetail)
		}
	}
}

// TestHypervLastErrorAfterReady —— creating→ready 时只有「没进出口池」的标记该活下来。
func TestHypervLastErrorAfterReady(t *testing.T) {
	tests := map[string]struct{ lastError, want string }{
		"empty":               {"", ""},
		"pool failure kept":   {hypervHintWithDetail(hypervHintPoolUpdateFailed, "磁盘已满"), hypervHintWithDetail(hypervHintPoolUpdateFailed, "磁盘已满")},
		"restart hint gone":   {hypervHintPoolRestart, ""},
		"dhcp hint gone":      {hypervDHCPTimeoutHint, ""},
		"create failure gone": {"第 1/2 张创建失败：交换机拒绝绑定", ""},
	}
	for name, testCase := range tests {
		if got := hypervLastErrorAfterReady(testCase.lastError); got != testCase.want {
			t.Fatalf("%s: 留下 %q; want %q", name, got, testCase.want)
		}
	}
}

// TestRecordPoolHintSurfacesAndSelfHeals —— 审计 M2 的回归测试。
//
// 原实现是 `_ = s.applyPoolUpdate(...)`：出口池写失败被整个吞掉，网卡状态照样是
// 「已就绪」、徽章照样在，用户重启聚合后新卡完全不生效，而全程零根因。现在写回台账。
func TestRecordPoolHintSurfacesAndSelfHeals(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	allocated := ledger.allocateNames(2, "batch-a", "2026-01-01T00:00:00Z")
	for _, entry := range allocated {
		ledger.upsert(entry)
	}
	if err := ledger.save(); err != nil {
		t.Fatalf("写台账失败：%v", err)
	}
	first, second := allocated[0].Name, allocated[1].Name

	load := func(name string) string {
		reloaded, err := loadHypervLedger()
		if err != nil {
			t.Fatalf("读台账失败：%v", err)
		}
		entry, index := reloaded.find(name)
		if index < 0 {
			t.Fatalf("台账里没有 %s", name)
		}
		return entry.LastError
	}

	// 1) 池写入失败 ⇒ 标记必须落到台账，用户看得到根因。
	hint := hypervHintWithDetail(hypervHintPoolUpdateFailed, "更新出口池失败：磁盘已满")
	service.recordPoolHint([]string{first}, hint)
	if got := load(first); got != hint {
		t.Fatalf("池写入失败的标记没有落到台账：%q", got)
	}
	if got := load(second); got != "" {
		t.Fatalf("未点名的卡不该被写：%q", got)
	}

	// 2) 池写入成功 ⇒ 标记自愈清除，否则它会永久粘住。
	service.recordPoolHint([]string{first}, "")
	if got := load(first); got != "" {
		t.Fatalf("池写入成功后标记应自愈清除，实际还留着 %q", got)
	}

	// 3) 清除动作不得顺带抹掉别的来源的 LastError（创建失败 / DHCP 超时）。
	service.recordPoolHint([]string{first}, hint)
	if err := service.updateLedger(func(l *hypervLedger) error {
		entry, index := l.find(first)
		if index < 0 {
			t.Fatal("找不到条目")
		}
		entry.LastError = "第 1/2 张创建失败：交换机拒绝绑定"
		l.Adapters[index] = entry
		return nil
	}); err != nil {
		t.Fatalf("改写台账失败：%v", err)
	}
	service.recordPoolHint([]string{first}, "")
	if got := load(first); got != "第 1/2 张创建失败：交换机拒绝绑定" {
		t.Fatalf("出口池的结果不得抹掉其它来源的错误，实际 %q", got)
	}

	// 4) 台账里没有的名字不得凭空造出条目。
	service.recordPoolHint([]string{"HypoMux-vnic-99"}, hint)
	reloaded, err := loadHypervLedger()
	if err != nil {
		t.Fatalf("读台账失败：%v", err)
	}
	if _, index := reloaded.find("HypoMux-vnic-99"); index >= 0 {
		t.Fatal("recordPoolHint 不得凭空创建条目")
	}
}

// =================================================================
// 批量删除（RemoveAdapters）
//
// 下面整节都靠两个注入钩子跑，**不弹 UAC、不碰真实 Hyper-V**：
//   - inventoryHook 伪造「系统实况」，覆盖台账外卡与幽灵记录；
//   - scriptHook    伪造提权脚本，把「一次批量只调一次」「Items 里有什么」断言成白盒。
//
// scriptHook 是本轮新加的：批量删除最要紧的两条性质（N 张卡只弹一次 UAC、被保护的目标
// 压根不进脚本）在没有它之前只能在真机上看，而真机上看不出「有没有进 Items」。
// =================================================================

type removeFixture struct {
	t         *testing.T
	settings  *SettingsService
	service   *HyperVAdapterService
	envelopes []hypervEnvelope
	timeouts  []time.Duration
	elevated  []bool
}

// newRemoveFixture 装好一个「inventory 固定、提权脚本可观测」的删除测试环境。
func newRemoveFixture(t *testing.T, adapters []hypervScriptAdapter) *removeFixture {
	t.Helper()
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	fixture := &removeFixture{t: t, settings: settings, service: service}
	service.mu.Lock()
	service.inventoryHook = func() (hypervInventory, error) {
		return hypervInventory{Adapters: adapters}, nil
	}
	service.scriptHook = func(envelope hypervEnvelope, timeout time.Duration, elevated bool) (*hypervScriptResult, error) {
		fixture.envelopes = append(fixture.envelopes, envelope)
		fixture.timeouts = append(fixture.timeouts, timeout)
		fixture.elevated = append(fixture.elevated, elevated)
		return &hypervScriptResult{OK: true}, nil
	}
	service.mu.Unlock()
	return fixture
}

// fail 换成「脚本回报指定网卡失败」的假执行，runErr 为 nil（脚本确实跑完了）。
func (f *removeFixture) fail(byName map[string]string) {
	f.service.mu.Lock()
	f.service.scriptHook = func(envelope hypervEnvelope, timeout time.Duration, elevated bool) (*hypervScriptResult, error) {
		f.envelopes = append(f.envelopes, envelope)
		f.timeouts = append(f.timeouts, timeout)
		f.elevated = append(f.elevated, elevated)
		result := &hypervScriptResult{OK: true}
		for name, message := range byName {
			result.Failures = append(result.Failures, hypervScriptFailure{Name: name, Error: message})
		}
		return result, nil
	}
	f.service.mu.Unlock()
}

// runErr 把提权脚本换成整体失败（模拟 UAC 被拒 / 结果文件缺失）。
func (f *removeFixture) runErr(err error) {
	f.service.mu.Lock()
	f.service.scriptHook = func(envelope hypervEnvelope, timeout time.Duration, elevated bool) (*hypervScriptResult, error) {
		f.envelopes = append(f.envelopes, envelope)
		f.timeouts = append(f.timeouts, timeout)
		f.elevated = append(f.elevated, elevated)
		return nil, err
	}
	f.service.mu.Unlock()
}

func (f *removeFixture) seedLedger(entries ...hypervLedgerEntry) {
	f.t.Helper()
	ledger, err := loadHypervLedger()
	if err != nil {
		f.t.Fatalf("读台账失败：%v", err)
	}
	for _, entry := range entries {
		ledger.upsert(entry)
	}
	if err := ledger.save(); err != nil {
		f.t.Fatalf("写台账失败：%v", err)
	}
}

func (f *removeFixture) seedPool(ids []string, weights map[string]int) {
	f.t.Helper()
	if _, err := f.settings.UpdateHome("tun", true, ids, weights); err != nil {
		f.t.Fatalf("预置出口池失败：%v", err)
	}
}

// poolState 返回出口池的「选中列表 + 权重表」，两条路径共用一份快照以便直接比对。
func (f *removeFixture) poolState() ([]string, map[string]int) {
	f.t.Helper()
	current := f.settings.Get()
	selected := make([]string, len(current.SelectedAdapterIDs))
	copy(selected, current.SelectedAdapterIDs)
	weights := make(map[string]int, len(current.AdapterWeights))
	for key, value := range current.AdapterWeights {
		weights[key] = value
	}
	return selected, weights
}

// removeBudget 读出唯一一次提权脚本拿到的父进程预算。
//
// fixture.timeouts 过去只被写、从不被读：把 hypervRemoveBatchTimeout 换成写死的
// 90s 整套测试照样全绿，于是「N 张放大 + 封顶」这条分级预算根本没有断言守着。
// 前端按「单卡 / 批量」两套预算发请求，Go 侧这个预算是那套分级的唯一依据，
// 因此每一次真实调用都必须把它读回来断言。
func (f *removeFixture) removeBudget(label string) time.Duration {
	f.t.Helper()
	if len(f.timeouts) != 1 {
		f.t.Fatalf("%s：提权脚本被调了 %d 次; want 1（实际预算 %v）", label, len(f.timeouts), f.timeouts)
	}
	if len(f.elevated) != 1 || !f.elevated[0] {
		f.t.Fatalf("%s：删除必须且只能走一次提权路径（elevated=%v）", label, f.elevated)
	}
	return f.timeouts[0]
}

func (f *removeFixture) ledgerNames() []string {
	f.t.Helper()
	ledger, err := loadHypervLedger()
	if err != nil {
		f.t.Fatalf("读台账失败：%v", err)
	}
	names := make([]string, 0, len(ledger.Adapters))
	for _, entry := range ledger.Adapters {
		names = append(names, entry.Name)
	}
	return names
}

// scriptedNames 返回唯一一次提权脚本 Items 里的名字集合。
func (f *removeFixture) scriptedNames() []string {
	f.t.Helper()
	if len(f.envelopes) == 0 {
		return nil
	}
	names := make([]string, 0, len(f.envelopes[0].Items))
	for _, item := range f.envelopes[0].Items {
		names = append(names, item.Name)
	}
	return names
}

func findRemoveResult(results []HyperVRemoveResult, name string) (HyperVRemoveResult, bool) {
	for _, item := range results {
		if strings.EqualFold(item.Name, name) {
			return item, true
		}
	}
	return HyperVRemoveResult{}, false
}

func hasName(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// TestHypervProtectedAdapter 锁死保护名单。名单是**硬排除**，且实现成纯函数就是为了
// 能在这里穷举 —— 它一旦被放宽，后果是删掉容器网络或默认交换机，不可逆。
func TestHypervProtectedAdapter(t *testing.T) {
	protected := []string{
		"Container NIC {abc-def}",
		"container nic",              // 大小写不敏感
		"CONTAINER NIC 1",            // 大小写不敏感
		"Container NIC",              // 前缀本身
		"vEthernet (Default Switch)", // 保护名单 §2：Hyper-V 自管 Internal 交换机的宿主接口
		"VETHERNET (DEFAULT SWITCH)", // 大小写不敏感
		"  vEthernet (Default Switch)  ",
		"Default Switch", // 对象名形态（真机上 Get-VMNetworkAdapter 就报这个名字）
		"   ",            // 空白名
		"",
	}
	for _, name := range protected {
		if !hypervProtectedAdapter(name) {
			t.Fatalf("hypervProtectedAdapter(%q) = false；它在保护名单上，绝不能被删", name)
		}
		if reason := hypervProtectedReason(name); reason == "" {
			t.Fatalf("hypervProtectedReason(%q) 返回空串：被保护必须带可展示的原因", name)
		}
	}
	// 反面：普通卡绝不能被误判进保护名单，否则用户又删不掉了。
	ordinary := []string{
		"HypoMux-vnic-01",
		"xuni-01",
		"xuni-01 container",  // 前缀不在开头
		"my container nic 1", // 前缀不在开头
		"vEthernet (xuni-01)",
		"vEthernet (WSL)", // 不在名单上：它压根不会出现在 List() 的输入域里
	}
	for _, name := range ordinary {
		if hypervProtectedAdapter(name) {
			t.Fatalf("hypervProtectedAdapter(%q) = true；普通卡被误判进保护名单，用户又删不掉了", name)
		}
	}
}

// TestRemoveAdaptersProtectedNeverEntersScript 保护名单的目标**不进提权脚本**。
//
// 这是保护名单与「只标记不拦截」的分界：光在结果里标个 Removed=false 不够，
// 一旦它进了 Items，脚本就会真的去删容器网卡。
//
// 表里刻意混进两种输入形态：Hyper-V 对象名（Container NIC …）与宿主别名
// （vEthernet (Default Switch)）。两者归一化后落在不同的 key 上，所以要显式给出
// 期望的结果名 —— 结果里 Name 是归一化后的对象名，Interface 才是宿主别名。
func TestRemoveAdaptersProtectedNeverEntersScript(t *testing.T) {
	protected := []struct{ input, want string }{
		{"Container NIC {abc}", "Container NIC {abc}"},
		{"container nic 2", "container nic 2"},
		{"vEthernet (Default Switch)", "Default Switch"},
	}
	adapters := []hypervScriptAdapter{
		{Name: "Container NIC {abc}", AdapterID: "guid-1", MAC: "00155D000001"},
		{Name: "container nic 2", AdapterID: "guid-2", MAC: "00155D000002"},
		{Name: "Default Switch", AdapterID: "guid-3", MAC: "00155D000003"},
		{Name: "xuni-01", AdapterID: "guid-4", MAC: "F6E6556A4D17"},
	}
	fixture := newRemoveFixture(t, adapters)

	inputs := []string{"xuni-01"}
	for _, item := range protected {
		inputs = append(inputs, item.input)
	}
	results, err := fixture.service.RemoveAdapters(inputs)
	if err != nil {
		t.Fatalf("批量删除不应报错：%v", err)
	}
	if len(results) != len(inputs) {
		t.Fatalf("结果条数 = %d; want %d（%v）", len(results), len(inputs), results)
	}
	for _, item := range protected {
		row, ok := findRemoveResult(results, item.want)
		if !ok {
			t.Fatalf("结果里缺 %s：%+v", item.want, results)
		}
		if row.Removed {
			t.Fatalf("%s 属于保护名单，却报告已删除", item.input)
		}
		if strings.TrimSpace(row.Reason) == "" {
			t.Fatalf("%s 未被删除却没有说明原因", item.input)
		}
		// 输入是宿主别名时，结果里必须把别名还回去，前端才知道自己点的哪一行。
		if strings.HasPrefix(strings.ToLower(item.input), "vethernet") && row.Interface != item.input {
			t.Fatalf("%s 的结果 Interface = %q; want %q", item.input, row.Interface, item.input)
		}
		if hasName(fixture.scriptedNames(), item.want) || hasName(fixture.scriptedNames(), item.input) {
			t.Fatalf("%s 属于保护名单，却被打进了提权脚本的 Items：%v", item.input, fixture.scriptedNames())
		}
	}
	// 唯一该进脚本的是那张普通卡。
	if got := fixture.scriptedNames(); len(got) != 1 || got[0] != "xuni-01" {
		t.Fatalf("提权脚本 Items = %v; want [xuni-01]", got)
	}
}

// TestRemoveAdaptersDeletesUnmanagedCard 台账外的卡必须可删 —— 这正是用户手工建的
// xuni-01..05 一直删不掉的场景。
func TestRemoveAdaptersDeletesUnmanagedCard(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "guid-x1", MAC: "F6E6556A4D17"},
	})
	// 台账里刻意什么都没有。
	fixture.seedLedger()

	results, err := fixture.service.RemoveAdapters([]string{"xuni-01"})
	if err != nil {
		t.Fatalf("台账外的卡应当可删：%v", err)
	}
	if len(results) != 1 || !results[0].Removed || results[0].Reason != "" {
		t.Fatalf("结果 = %+v; want Removed=true 且无原因", results)
	}
	if results[0].Interface != "vEthernet (xuni-01)" {
		t.Fatalf("Interface = %q; want vEthernet (xuni-01)", results[0].Interface)
	}
	if got := fixture.scriptedNames(); len(got) != 1 || got[0] != "xuni-01" {
		t.Fatalf("提权脚本 Items = %v; want [xuni-01]", got)
	}
	// DeviceId 必须留空：台账外的卡没有权威 DeviceId，脚本在 adapterId 为空时按 MAC 定位。
	item := fixture.envelopes[0].Items[0]
	if item.AdapterID != "" {
		t.Fatalf("台账外卡不该带 DeviceId（猜来的 DeviceId 会删错对象）：%q", item.AdapterID)
	}
	if item.MAC == "" {
		t.Fatal("台账外卡必须带 MAC，否则脚本无从定位")
	}
}

// TestRemoveAdaptersKeepsLedgerForUnmanagedCard 台账外的卡删完后**台账不变**。
func TestRemoveAdaptersKeepsLedgerForUnmanagedCard(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "guid-x1", MAC: "F6E6556A4D17"},
	})
	fixture.seedLedger(hypervLedgerEntry{Name: "HypoMux-vnic-01", MACAddress: "02:1a:2b:00:00:01"})

	if _, err := fixture.service.RemoveAdapters([]string{"xuni-01"}); err != nil {
		t.Fatalf("批量删除不应报错：%v", err)
	}
	names := fixture.ledgerNames()
	if len(names) != 1 || names[0] != "HypoMux-vnic-01" {
		t.Fatalf("台账外卡删除后台账被改了：%v", names)
	}
}

// TestRemoveAdaptersSkipsGhostRecord 幽灵记录（MAC 与 DeviceId 全空）不可删。
func TestRemoveAdaptersSkipsGhostRecord(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "xuni-02", AdapterID: "", MAC: ""}, // reports/vnic/60 实测到的幽灵记录
	})

	results, err := fixture.service.RemoveAdapters([]string{"xuni-02"})
	if err != nil {
		t.Fatalf("幽灵记录应当被跳过而不是报错：%v", err)
	}
	if len(results) != 1 {
		t.Fatalf("结果条数 = %d; want 1", len(results))
	}
	if results[0].Removed {
		t.Fatal("MAC 与 DeviceId 全空的幽灵记录不可删除")
	}
	if !strings.Contains(results[0].Reason, "幽灵记录") {
		t.Fatalf("原因应点明是幽灵记录，实际 %q", results[0].Reason)
	}
	if len(fixture.envelopes) != 0 {
		t.Fatalf("幽灵记录不得进提权脚本：%v", fixture.scriptedNames())
	}
}

// TestRemoveAdaptersSkipsMissingUnmanagedCard 系统实况里压根没有的名字同样跳过，
// 且提示要和幽灵记录区分开（用户需要知道该刷新列表还是该找 Hyper-V 排查）。
func TestRemoveAdaptersSkipsMissingUnmanagedCard(t *testing.T) {
	fixture := newRemoveFixture(t, nil)
	results, err := fixture.service.RemoveAdapters([]string{"xuni-99"})
	if err != nil {
		t.Fatalf("找不到的卡应当被跳过而不是报错：%v", err)
	}
	if results[0].Removed || results[0].Reason == "" {
		t.Fatalf("结果 = %+v; want Removed=false 且带原因", results)
	}
	if strings.Contains(results[0].Reason, "幽灵记录") {
		t.Fatalf("压根不存在的卡不该报成幽灵记录：%q", results[0].Reason)
	}
	if len(fixture.envelopes) != 0 {
		t.Fatal("找不到的卡不得进提权脚本")
	}
}

// TestRemoveAdaptersKeepsMACGateForManagedCard 第三重闸门对受管卡仍然生效。
// 前两道闸门被推翻了，这一道不能跟着一起推翻：它挡的是「这张卡已被重建或复用过」，
// 按旧记录去删就是在删别人的东西。
func TestRemoveAdaptersKeepsMACGateForManagedCard(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "HypoMux-vnic-01", AdapterID: "guid-1", MAC: "AA:BB:CC:DD:EE:FF"}, // 与台账不符
	})
	fixture.seedLedger(hypervLedgerEntry{Name: "HypoMux-vnic-01", AdapterID: "guid-1", MACAddress: "02:1a:2b:00:00:01"})

	results, err := fixture.service.RemoveAdapters([]string{"HypoMux-vnic-01"})
	if err != nil {
		t.Fatalf("MAC 不一致应当是跳过而不是整批报错：%v", err)
	}
	if results[0].Removed {
		t.Fatal("MAC 与台账不一致时不得删除")
	}
	if results[0].Reason != "%s 的 MAC（%s）与台账记录（%s）不一致，已跳过删除" &&
		!strings.Contains(results[0].Reason, "不一致") {
		t.Fatalf("原因应与单张路径同源（必须含「不一致」），实际 %q", results[0].Reason)
	}
	if len(fixture.envelopes) != 0 {
		t.Fatal("MAC 闸门拦下的卡不得进提权脚本")
	}
	if names := fixture.ledgerNames(); len(names) != 1 {
		t.Fatalf("MAC 闸门拦下时台账必须原样保留：%v", names)
	}
}

// TestRemoveAdaptersProtectedEvenWhenInLedger 保护名单优先于台账：即使这张卡在台账里、
// MAC 也对得上，依然不删。
func TestRemoveAdaptersProtectedEvenWhenInLedger(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "Container NIC {abc}", AdapterID: "guid-1", MAC: "00155D000001"},
	})
	fixture.seedLedger(hypervLedgerEntry{Name: "Container NIC {abc}", AdapterID: "guid-1", MACAddress: "00:15:5d:00:00:01"})

	results, err := fixture.service.RemoveAdapters([]string{"Container NIC {abc}"})
	if err != nil {
		t.Fatalf("保护名单应当是跳过而不是报错：%v", err)
	}
	if results[0].Removed {
		t.Fatal("台账里的容器网卡同样不可删除")
	}
	if len(fixture.envelopes) != 0 {
		t.Fatal("保护名单优先于台账：不得进提权脚本")
	}
	if names := fixture.ledgerNames(); len(names) != 1 {
		t.Fatalf("未删除时台账必须原样保留：%v", names)
	}
}

// TestRemoveAdaptersUsesOneScriptForMany N 张卡只弹一次 UAC。
func TestRemoveAdaptersUsesOneScriptForMany(t *testing.T) {
	names := []string{"xuni-01", "xuni-02", "xuni-03", "xuni-04", "xuni-05"}
	adapters := make([]hypervScriptAdapter, 0, len(names))
	for index, name := range names {
		adapters = append(adapters, hypervScriptAdapter{
			Name:       name,
			AdapterID:  "guid-" + name,
			MAC:        formatHypervMAC(hypervMACValue(index + 1)),
			SwitchName: "XuniUplink",
		})
	}
	fixture := newRemoveFixture(t, adapters)

	results, err := fixture.service.RemoveAdapters(names)
	if err != nil {
		t.Fatalf("批量删除不应报错：%v", err)
	}
	if len(fixture.envelopes) != 1 {
		t.Fatalf("提权脚本被调了 %d 次; want 1（用户点 5 张卡只该确认 1 次管理员权限）", len(fixture.envelopes))
	}
	if len(fixture.envelopes[0].Items) != len(names) {
		t.Fatalf("Items 条数 = %d; want %d", len(fixture.envelopes[0].Items), len(names))
	}
	if fixture.envelopes[0].Op != "remove" {
		t.Fatalf("Op = %q; want remove", fixture.envelopes[0].Op)
	}
	if !fixture.elevated[0] {
		t.Fatal("删除必须走提权路径")
	}
	if len(results) != len(names) {
		t.Fatalf("结果条数 = %d; want %d", len(results), len(names))
	}
	for _, row := range results {
		if !row.Removed {
			t.Fatalf("%s 应当删除成功，实际原因 %q", row.Name, row.Reason)
		}
	}
}

// TestRemoveAdaptersTimeoutScalesAndCaps 超时按张数放大并封顶。
func TestRemoveAdaptersTimeoutScalesAndCaps(t *testing.T) {
	cases := []struct {
		items int
		want  time.Duration
	}{
		{1, 90 * time.Second},
		{0, 90 * time.Second}, // 兜底按 1 张算，不返回 0
		{2, 180 * time.Second},
		{3, hypervRemoveBatchTimeoutMax}, // 270s 被压到上限
		{16, hypervRemoveBatchTimeoutMax},
	}
	for _, item := range cases {
		if got := hypervRemoveBatchTimeout(item.items); got != item.want {
			t.Fatalf("hypervRemoveBatchTimeout(%d) = %v; want %v", item.items, got, item.want)
		}
	}
	if hypervRemoveBatchTimeoutMax <= hypervRemoveScriptTimeout {
		t.Fatal("批量上限必须大于单张超时，否则放大没有意义")
	}
}

// TestRemoveAdaptersPartialFailure 一张失败，其余照删照清。这是批量删除的核心承诺。
func TestRemoveAdaptersPartialFailure(t *testing.T) {
	managed := []string{"HypoMux-vnic-01", "HypoMux-vnic-02", "HypoMux-vnic-03"}
	adapters := make([]hypervScriptAdapter, 0, len(managed))
	entries := make([]hypervLedgerEntry, 0, len(managed))
	aliases := make([]string, 0, len(managed))
	weights := map[string]int{}
	for index, name := range managed {
		mac := formatHypervMAC(hypervMACValue(index + 1))
		adapters = append(adapters, hypervScriptAdapter{Name: name, AdapterID: "guid-" + name, MAC: mac})
		entries = append(entries, hypervLedgerEntry{Name: name, AdapterID: "guid-" + name, MACAddress: mac})
		alias := hypervHostInterfaceName(name)
		aliases = append(aliases, alias)
		weights[alias] = 7
	}
	fixture := newRemoveFixture(t, adapters)
	fixture.seedLedger(entries...)
	fixture.seedPool(aliases, weights)
	fixture.fail(map[string]string{"HypoMux-vnic-02": "网卡正在被使用中"})

	results, err := fixture.service.RemoveAdapters(managed)
	if err != nil {
		t.Fatalf("单张失败不得冒泡成整批错误：%v", err)
	}
	failed, ok := findRemoveResult(results, "HypoMux-vnic-02")
	if !ok || failed.Removed || !strings.Contains(failed.Reason, "正在被使用中") {
		t.Fatalf("失败那张应带脚本原文原因，实际 %+v", results)
	}
	for _, name := range []string{"HypoMux-vnic-01", "HypoMux-vnic-03"} {
		row, found := findRemoveResult(results, name)
		if !found || !row.Removed {
			t.Fatalf("%s 应当照常删除成功，实际 %+v", name, row)
		}
	}

	// 台账：只清掉成功的两张。
	if left := fixture.ledgerNames(); len(left) != 1 || left[0] != "HypoMux-vnic-02" {
		t.Fatalf("台账应只剩失败那张，实际 %v", left)
	}
	// 出口池与权重：同样只清成功的两张。
	current := fixture.settings.Get()
	if len(current.SelectedAdapterIDs) != 1 || current.SelectedAdapterIDs[0] != aliases[1] {
		t.Fatalf("出口池应只剩失败那张，实际 %v", current.SelectedAdapterIDs)
	}
	for _, index := range []int{0, 2} {
		if _, ok := current.AdapterWeights[aliases[index]]; ok {
			t.Fatalf("已删网卡的权重键 %q 残留：%v", aliases[index], current.AdapterWeights)
		}
	}
	if current.AdapterWeights[aliases[1]] != 7 {
		t.Fatalf("失败那张的权重不该被动：%v", current.AdapterWeights)
	}
}

// TestRemoveAdaptersAllFailedReturnsNilError 全失败时整批仍返回 nil error。
func TestRemoveAdaptersAllFailedReturnsNilError(t *testing.T) {
	names := []string{"xuni-01", "xuni-02"}
	adapters := []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "guid-1", MAC: "F6E6556A4D17"},
		{Name: "xuni-02", AdapterID: "guid-2", MAC: "F6E6556A4D18"},
	}
	fixture := newRemoveFixture(t, adapters)
	fixture.fail(map[string]string{
		"xuni-01": "交换机拒绝",
		"xuni-02": "网卡正在被使用中",
	})

	results, err := fixture.service.RemoveAdapters(names)
	if err != nil {
		t.Fatalf("全部单张失败不得冒泡成整批错误：%v", err)
	}
	if len(results) != 2 {
		t.Fatalf("结果条数 = %d; want 2", len(results))
	}
	for _, row := range results {
		if row.Removed || row.Reason == "" {
			t.Fatalf("%s 应当失败并带原因，实际 %+v", row.Name, row)
		}
	}
}

// TestRemoveAdaptersUnattributedFailureBlocksSuccess 脚本回报 OK、但带着一条
// **对不上任何目标名字**的失败时，谁都不能算删成功。
//
// 这条路径没法用「按名字逐个匹配」来兜底：既然连失败属于哪张卡都说不清，
// 那就无从证明剩下几张真的删掉了。把它们记成 Removed=true 会连带把台账和
// 出口池清掉，用户刷新后发现卡还在、池子也没了。
func TestRemoveAdaptersUnattributedFailureBlocksSuccess(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "guid-1", MAC: "F6E6556A4D17"},
		{Name: "xuni-02", AdapterID: "guid-2", MAC: "F6E6556A4D18"},
	})
	fixture.service.mu.Lock()
	fixture.service.scriptHook = func(hypervEnvelope, time.Duration, bool) (*hypervScriptResult, error) {
		// OK=true 却带一条无主失败：这正是「不确定」的情形。
		return &hypervScriptResult{
			OK:       true,
			Failures: []hypervScriptFailure{{Name: "", Error: "某个说不清的对象失败了"}},
		}, nil
	}
	fixture.service.mu.Unlock()

	results, err := fixture.service.RemoveAdapters([]string{"xuni-01", "xuni-02"})
	if err != nil {
		t.Fatalf("单张失败不得冒泡成整批错误：%v", err)
	}
	for _, row := range results {
		if row.Removed {
			t.Fatalf("存在无主失败时 %s 不得记为已删除：%+v", row.Name, row)
		}
		if row.Reason == "" {
			t.Fatalf("%s 未删除却没给原因：%+v", row.Name, row)
		}
	}
}

// TestRemoveAdaptersRunErrorMarksAllFailed 提权脚本整体失败时一张都不能算成功 ——
// 无从确知脚本到底做了什么，绝不能清台账。
func TestRemoveAdaptersRunErrorMarksAllFailed(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "guid-1", MAC: "F6E6556A4D17"},
	})
	fixture.seedLedger(hypervLedgerEntry{Name: "HypoMux-vnic-01", AdapterID: "guid-2", MACAddress: "02:1a:2b:00:00:01"})
	fixture.runErr(hypervErrorf(hypervCodeElevationCancelled, "已取消管理员权限请求"))

	results, err := fixture.service.RemoveAdapters([]string{"xuni-01"})
	if err != nil {
		t.Fatalf("单张失败不得冒泡成整批错误：%v", err)
	}
	if results[0].Removed || results[0].Reason == "" {
		t.Fatalf("脚本整体失败时不得报告成功：%+v", results)
	}
	if names := fixture.ledgerNames(); len(names) != 1 {
		t.Fatalf("脚本失败时台账必须原样保留：%v", names)
	}
}

// TestRemoveAdaptersNormalizesInput 输入归一化：trim、丢空、去重、保序。
func TestRemoveAdaptersNormalizesInput(t *testing.T) {
	fixture := newRemoveFixture(t, []hypervScriptAdapter{
		{Name: "xuni-01", AdapterID: "guid-1", MAC: "F6E6556A4D17"},
		{Name: "xuni-02", AdapterID: "guid-2", MAC: "F6E6556A4D18"},
	})
	results, err := fixture.service.RemoveAdapters([]string{
		"  xuni-02 ", "", "xuni-01", "XUNI-01", "   ", "vEthernet (xuni-02)",
	})
	if err != nil {
		t.Fatalf("批量删除不应报错：%v", err)
	}
	if len(results) != 2 {
		t.Fatalf("归一化后应只剩 2 个目标，实际 %d：%+v", len(results), results)
	}
	// 保序：先来的是 xuni-02。
	if results[0].Name != "xuni-02" || results[1].Name != "xuni-01" {
		t.Fatalf("归一化必须保持原顺序，实际 %q / %q", results[0].Name, results[1].Name)
	}
	if got := fixture.scriptedNames(); len(got) != 2 {
		t.Fatalf("同一张卡不得进 Items 两次：%v", got)
	}
}

// TestRemoveAdaptersEmptyInputIsIdempotent 归一化后为空返回空切片 + nil error。
// 重复点删除不是错误。
func TestRemoveAdaptersEmptyInputIsIdempotent(t *testing.T) {
	fixture := newRemoveFixture(t, nil)
	for name, input := range map[string][]string{
		"nil":     nil,
		"empty":   {},
		"blanks":  {"", "   ", "\t"},
		"all dup": {"", " "},
	} {
		results, err := fixture.service.RemoveAdapters(input)
		if err != nil {
			t.Fatalf("%s: 应当幂等返回 nil error，实际 %v", name, err)
		}
		if len(results) != 0 {
			t.Fatalf("%s: 应当返回空切片，实际 %+v", name, results)
		}
	}
	if len(fixture.envelopes) != 0 {
		t.Fatal("空输入不得触发提权脚本")
	}
}

// TestRemoveAdaptersInventoryFailureAbortsBatch inventory 读失败是整批级错误：
// 无法确认「这张卡此刻是否存在」时，宁可什么都不做，也不能留下用户永远删不掉的孤儿。
func TestRemoveAdaptersInventoryFailureAbortsBatch(t *testing.T) {
	fixture := newRemoveFixture(t, nil)
	fixture.service.mu.Lock()
	fixture.service.inventoryHook = func() (hypervInventory, error) {
		return hypervInventory{}, errors.New("模拟 Get-VMNetworkAdapter 查询失败")
	}
	fixture.service.mu.Unlock()

	results, err := fixture.service.RemoveAdapters([]string{"xuni-01"})
	if err == nil {
		t.Fatal("inventory 读失败应当整批中止")
	}
	coded, ok := err.(*HyperVError)
	if !ok || coded.Code != hypervCodeUnavailable {
		t.Fatalf("错误码 = %v; want %s", err, hypervCodeUnavailable)
	}
	if results != nil {
		t.Fatalf("整批中止时不应返回结果：%+v", results)
	}
	if len(fixture.envelopes) != 0 {
		t.Fatal("inventory 读失败时不得触发提权脚本")
	}
}

// TestRemoveAdaptersIdempotentWhenAlreadyGone 受管卡在系统实况里已经不存在时，
// 走幂等路径：不弹 UAC，但照常清台账与出口池。
func TestRemoveAdaptersIdempotentWhenAlreadyGone(t *testing.T) {
	fixture := newRemoveFixture(t, nil) // inventory 里什么都没有
	fixture.seedLedger(hypervLedgerEntry{Name: "HypoMux-vnic-01", AdapterID: "guid-1", MACAddress: "02:1a:2b:00:00:01"})
	alias := hypervHostInterfaceName("HypoMux-vnic-01")
	fixture.seedPool([]string{alias}, map[string]int{alias: 3})

	results, err := fixture.service.RemoveAdapters([]string{"HypoMux-vnic-01"})
	if err != nil {
		t.Fatalf("幂等删除不应报错：%v", err)
	}
	if !results[0].Removed {
		t.Fatalf("已经手工删掉的卡应记为已删除（幂等）：%+v", results)
	}
	if len(fixture.envelopes) != 0 {
		t.Fatal("系统里已经没有的卡不必再弹 UAC")
	}
	if names := fixture.ledgerNames(); len(names) != 0 {
		t.Fatalf("台账应被清空，实际 %v", names)
	}
	if _, ok := fixture.settings.Get().AdapterWeights[alias]; ok {
		t.Fatalf("出口池权重应一并清掉：%v", fixture.settings.Get().AdapterWeights)
	}
}

// TestApplyPoolUpdateRemovesDanglingWeight 「有权重、没被选中」的悬空权重键也必须清掉。
//
// 这正是 applyPoolUpdate 原本漏掉的一半：它只在「该键当时还在 selected 里」时删权重。
// 而删网卡会同时命中两条路，因此必须无条件删 remove 名单里的权重键。
// 注意这里刻意把该键放进 selected —— 一旦放进 selected，上面的循环就会顺手删掉它，
// 测不出悬空权重这条路径。
func TestApplyPoolUpdateRemovesDanglingWeight(t *testing.T) {
	t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())
	settings := NewSettingsService()
	service := NewHyperVAdapterService(settings, NewAdapterService(NewSettingsService()))
	t.Cleanup(service.Shutdown)

	const alias = "vEthernet (xuni-01)"
	if _, err := settings.UpdateHome("tun", true, []string{}, map[string]int{alias: 9}); err != nil {
		t.Fatalf("预置悬空权重失败：%v", err)
	}
	if _, ok := settings.Get().AdapterWeights[alias]; !ok {
		t.Fatalf("预置失败：权重键不在：%v", settings.Get().AdapterWeights)
	}

	if err := service.applyPoolUpdate(nil, []string{alias}); err != nil {
		t.Fatalf("applyPoolUpdate 失败：%v", err)
	}
	current := settings.Get()
	if len(current.SelectedAdapterIDs) != 0 {
		t.Fatalf("出口池应为空：%v", current.SelectedAdapterIDs)
	}
	if _, ok := current.AdapterWeights[alias]; ok {
		t.Fatalf("悬空权重键残留：%v", current.AdapterWeights)
	}
}

// =================================================================
// 单张删除（Remove）与「脚本逐条回报失败」—— P1-1 回归
//
// 这一节钉的是这样一条缺陷：单张路径曾经只判 runErr != nil，**从不读
// result.Failures**。而提权脚本把每张卡的异常逐条 catch 进 $failures 之后，
// 仍然 Publish $true 'ok' 正常退出 —— 于是「脚本没报错」被当成了「这张卡删掉了」：
// 网卡还在宿主上，台账被清、出口池被清，用户此后**永远删不掉它**。
//
// 所以下面每条断言都落在「失败之后台账与出口池原样保留」上：那才是这条缺陷
// 真正伤人的地方，错误码与文案只是它的表面。
// =================================================================

// removeScriptFailureReason 是脚本逐条回报里用的原文。它必须能原样透传到 UI 上：
// 这就是用户在 Hyper-V 管理器里会看到的那句，把它吞掉等于让用户无从下手。
const removeScriptFailureReason = "Remove-VMNetworkAdapter: 交换机 HypoMux-Uplink 拒绝删除该网卡"

// newManagedRemoveFixture 造一个「N 张受管卡，台账 + 出口池全部就位」的删除环境。
//
// 成功与失败两条路径的前置状态必须**完全一致**，否则「成功清干净了」这条断言
// 没有对照意义 —— 它可能只是因为一开始就没东西可清。
func newManagedRemoveFixture(t *testing.T, names ...string) (*removeFixture, []string) {
	t.Helper()
	adapters := make([]hypervScriptAdapter, 0, len(names))
	entries := make([]hypervLedgerEntry, 0, len(names))
	aliases := make([]string, 0, len(names))
	weights := map[string]int{}
	for index, name := range names {
		mac := formatHypervMAC(hypervMACValue(index + 1))
		adapterID := "guid-" + name
		adapters = append(adapters, hypervScriptAdapter{
			Name:       name,
			AdapterID:  adapterID,
			MAC:        mac,
			SwitchName: "HypoMux-Uplink",
		})
		entries = append(entries, hypervLedgerEntry{
			Name:       name,
			AdapterID:  adapterID,
			MACAddress: mac,
			SwitchName: "HypoMux-Uplink",
			BatchID:    "batch-" + name,
			State:      hypervStateReady,
			CreatedAt:  "2026-01-01T00:00:00Z",
		})
		alias := hypervHostInterfaceName(name)
		aliases = append(aliases, alias)
		weights[alias] = 7
	}
	fixture := newRemoveFixture(t, adapters)
	fixture.seedLedger(entries...)
	fixture.seedPool(aliases, weights)
	return fixture, aliases
}

// TestRemoveReportsScriptFailureAsRemoveFailed 单张路径必须读 result.Failures。
//
// 前提条件刻意做成「脚本整体成功」：runErr == nil、result.OK == true，只有一条
// 逐条失败。这正是缺陷现场 —— 修复前 Remove 只看 runErr，于是把它当成成功。
//
// 断言：错误码必须是 remove_failed，且错误串里带上网卡名与脚本回报的原始原因。
// 码不能是 not_managed：卡此刻还在宿主上、归属也没问题，说成「不是我们建的」
// 会把用户引到完全错误的方向上。
func TestRemoveReportsScriptFailureAsRemoveFailed(t *testing.T) {
	const name = "HypoMux-vnic-01"
	fixture, _ := newManagedRemoveFixture(t, name)
	fixture.fail(map[string]string{name: removeScriptFailureReason})

	err := fixture.service.Remove(name)
	if err == nil {
		t.Fatalf("脚本逐条回报失败时 Remove 必须报错。修复前只判 runErr!=nil，"+
			"于是这张还在宿主上的网卡被标成已删除并清了台账（实际预算 %v）", fixture.timeouts)
	}
	coded, ok := err.(*HyperVError)
	if !ok {
		t.Fatalf("错误类型 = %T; want *HyperVError", err)
	}
	if coded.Code != hypervCodeRemoveFailed {
		t.Fatalf("错误码 = %q; want %q（卡还在宿主上、归属没问题，报 not_managed 是误导）",
			coded.Code, hypervCodeRemoveFailed)
	}
	// Error() 把 code 拼进字符串，前端按前缀映射文案（见 TestHyperVErrorMessageCarriesCode）。
	if !strings.HasPrefix(err.Error(), hypervCodeRemoveFailed+"：") {
		t.Fatalf("Error() 必须以 %q 开头，实际 %q", hypervCodeRemoveFailed+"：", err.Error())
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("错误信息必须点名是哪张卡：%q", err.Error())
	}
	if !strings.Contains(err.Error(), removeScriptFailureReason) {
		t.Fatalf("错误信息必须带脚本回报的原始原因 %q，实际 %q", removeScriptFailureReason, err.Error())
	}
	// 提权脚本确实跑过：否则这个错误可能来自某个更早的闸门，测的就不是这条修复了。
	if got := fixture.removeBudget("单张失败"); got != hypervRemoveScriptTimeout {
		t.Fatalf("单张删除的父进程预算 = %v; want %v", got, hypervRemoveScriptTimeout)
	}
}

// TestRemoveKeepsLedgerAndPoolOnScriptFailure —— P1-1 最要紧的一条。
//
// 删失败之后，台账里那条记录与出口池里那张卡都**必须还在**。一旦被清掉，网卡就
// 成了宿主上的孤儿：List() 把它当未纳管只读行展示，Remove() 从此永远返回
// not_managed —— 用户再也删不掉它，整条链路上没有任何一个恢复手段。
//
// 断言分三层：错误、台账（含逐字节比对）、出口池。
func TestRemoveKeepsLedgerAndPoolOnScriptFailure(t *testing.T) {
	const name = "HypoMux-vnic-01"
	fixture, aliases := newManagedRemoveFixture(t, name)
	alias := aliases[0]
	fixture.fail(map[string]string{name: removeScriptFailureReason})

	before, err := os.ReadFile(hypervLedgerPath())
	if err != nil {
		t.Fatalf("读台账文件失败：%v", err)
	}
	poolBefore, weightsBefore := fixture.poolState()
	if !hasName(poolBefore, alias) {
		t.Fatalf("前置条件不成立：出口池里必须有 %s，实际 %v", alias, poolBefore)
	}

	if err := fixture.service.Remove(name); err == nil {
		t.Fatalf("脚本逐条回报失败时 Remove 必须报错；返回 nil 就意味着台账与出口池正在被清")
	}
	// 台账：一个字节都不许变。
	after, err := os.ReadFile(hypervLedgerPath())
	if err != nil {
		t.Fatalf("读台账文件失败：%v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("删除失败后台账文件被改了：\n删除前 %s\n删除后 %s", before, after)
	}
	ledger, err := loadHypervLedger()
	if err != nil {
		t.Fatalf("读台账失败：%v", err)
	}
	entry, index := ledger.find(name)
	if index < 0 {
		t.Fatalf("删除失败后台账里 %s 必须还在，实际 %v —— 这正是用户此后永远删不掉它的原因", name, fixture.ledgerNames())
	}
	if entry.MACAddress == "" || entry.AdapterID == "" {
		t.Fatalf("台账记录不得被抹掉归属字段：%+v", entry)
	}
	// 出口池：选中项与权重都必须还在。
	selected, weights := fixture.poolState()
	if !hasName(selected, alias) {
		t.Fatalf("删除失败后出口池里的 %s 必须还在，实际 %v", alias, selected)
	}
	if len(selected) != len(poolBefore) {
		t.Fatalf("删除失败后出口池的选中项数量变了：%v → %v", poolBefore, selected)
	}
	if weights[alias] != weightsBefore[alias] {
		t.Fatalf("删除失败后 %s 的权重被改了：%v → %v", alias, weightsBefore[alias], weights[alias])
	}
	if len(weights) != len(weightsBefore) {
		t.Fatalf("删除失败后权重表变了：%v → %v", weightsBefore, weights)
	}
}

// TestRemoveClearsLedgerAndPoolOnScriptSuccess 成功路径不回归：脚本回报成功时，
// Remove 返回 nil，台账记录与出口池条目（连同权重）确实被清掉。
//
// 前置状态与失败用例完全一致（newManagedRemoveFixture），所以「清干净了」是这次
// 删除的成果，而不是一开始就没东西可清。
func TestRemoveClearsLedgerAndPoolOnScriptSuccess(t *testing.T) {
	const name = "HypoMux-vnic-01"
	fixture, aliases := newManagedRemoveFixture(t, name)
	alias := aliases[0]

	if err := fixture.service.Remove(name); err != nil {
		t.Fatalf("脚本回报成功时 Remove 不得报错：%v", err)
	}
	if left := fixture.ledgerNames(); len(left) != 0 {
		t.Fatalf("删除后台账应被清空，实际 %v", left)
	}
	selected, weights := fixture.poolState()
	if len(selected) != 0 {
		t.Fatalf("删除后出口池应为空，实际 %v", selected)
	}
	if _, ok := weights[alias]; ok {
		t.Fatalf("删除后 %s 的权重键应一并清掉：%v", alias, weights)
	}
	// 脚本确实按台账里的身份去删的：拿错 DeviceId 会删掉同名的另一张卡。
	if len(fixture.envelopes) != 1 || fixture.envelopes[0].Op != "remove" {
		t.Fatalf("应当恰好调一次 remove 提权脚本，实际 %d 次", len(fixture.envelopes))
	}
	items := fixture.envelopes[0].Items
	if len(items) != 1 {
		t.Fatalf("Items 条数 = %d; want 1", len(items))
	}
	if items[0].Name != name || items[0].AdapterID != "guid-"+name || items[0].MAC == "" {
		t.Fatalf("脚本条目必须带上台账里的身份（Name/MAC/DeviceId）：%+v", items[0])
	}
	if got := fixture.removeBudget("单张成功"); got != hypervRemoveScriptTimeout {
		t.Fatalf("单张删除的父进程预算 = %v; want %v", got, hypervRemoveScriptTimeout)
	}
}

// TestRemoveAdaptersPartialFailureKeepsBatchContract 批量路径的「一张失败、其余照删」
// 承诺不受单张路径的修复影响：整体不返回 error，成功的那几张照常清台账 / 移出池，
// 失败的那行 removed=false 且带原因。
//
// 这条断言守着的是**分叉点**本身：单张路径把失败翻译成 error，批量路径不能跟着一起
// 翻译，否则用户批量删 5 张时会被一张失败卡住其余 4 张。
func TestRemoveAdaptersPartialFailureKeepsBatchContract(t *testing.T) {
	names := []string{"HypoMux-vnic-01", "HypoMux-vnic-02", "HypoMux-vnic-03"}
	fixture, aliases := newManagedRemoveFixture(t, names...)
	fixture.fail(map[string]string{"HypoMux-vnic-02": removeScriptFailureReason})

	results, err := fixture.service.RemoveAdapters(names)
	if err != nil {
		t.Fatalf("批量路径下单张失败不得冒泡成整批错误：%v", err)
	}
	if len(results) != len(names) {
		t.Fatalf("结果条数 = %d; want %d（%v）", len(results), len(names), results)
	}
	failed, ok := findRemoveResult(results, "HypoMux-vnic-02")
	if !ok {
		t.Fatalf("结果里缺 HypoMux-vnic-02：%v", results)
	}
	if failed.Removed {
		t.Fatal("脚本回报失败的行不得记为已删除")
	}
	if !strings.Contains(failed.Reason, removeScriptFailureReason) {
		t.Fatalf("失败行必须带脚本原文原因，实际 %q", failed.Reason)
	}
	for _, name := range []string{"HypoMux-vnic-01", "HypoMux-vnic-03"} {
		row, found := findRemoveResult(results, name)
		if !found || !row.Removed || row.Reason != "" {
			t.Fatalf("%s 应当照常删除成功，实际 %+v", name, row)
		}
	}
	// 台账与出口池只清掉成功的那两张。
	if left := fixture.ledgerNames(); len(left) != 1 || left[0] != "HypoMux-vnic-02" {
		t.Fatalf("台账应只剩失败那张，实际 %v", left)
	}
	selected, weights := fixture.poolState()
	if len(selected) != 1 || selected[0] != aliases[1] {
		t.Fatalf("出口池应只剩失败那张，实际 %v", selected)
	}
	if len(weights) != 1 || weights[aliases[1]] != 7 {
		t.Fatalf("权重表应只剩失败那张，实际 %v", weights)
	}
	if got := fixture.removeBudget("批量部分失败"); got != hypervRemoveBatchTimeout(len(names)) {
		t.Fatalf("%d 张的父进程预算 = %v; want %v", len(names), got, hypervRemoveBatchTimeout(len(names)))
	}
}

// TestRemoveTimeoutBudgetScalesWithItems 把「N 张线性放大 + 封顶」这条分级预算
// 从纯函数搬到**真实调用**上断言。
//
// 有两处必须钉住：
//  1. 1 张 = 90s（hypervRemoveScriptTimeout），不是批量上限。前端的单卡删除预算
//     必须 ≥ Go 侧单张预算，否则用户会先看到前端超时而后端其实还在删。
//  2. 2 张起才开始放大并在 hypervRemoveBatchTimeoutMax 封顶，N 张只弹一次 UAC。
//
// 之前 fixture.timeouts 只写不读，把 hypervRemoveBatchTimeout 换成写死的 90s
// 整套测试照样全绿 —— 这条预算实际上没人守着。
func TestRemoveTimeoutBudgetScalesWithItems(t *testing.T) {
	if hypervRemoveScriptTimeout != 90*time.Second {
		t.Fatalf("单张预算 = %v; want 90s。改它之前先想清楚前端「单卡删除」那一档（reports/vnic/62 §3.3）",
			hypervRemoveScriptTimeout)
	}
	if hypervRemoveScriptTimeout >= hypervRemoveBatchTimeoutMax {
		t.Fatalf("单张预算 %v 必须严格小于批量上限 %v，否则下面「1 张 = 90s」那条断言无从与封顶区分",
			hypervRemoveScriptTimeout, hypervRemoveBatchTimeoutMax)
	}
	cases := []struct {
		label  string
		names  []string
		single bool
		want   time.Duration
	}{
		{"单张走 Remove", []string{"HypoMux-vnic-01"}, true, hypervRemoveScriptTimeout},
		{"单张走 RemoveAdapters", []string{"HypoMux-vnic-01"}, false, hypervRemoveScriptTimeout},
		{"两张走 RemoveAdapters", []string{"HypoMux-vnic-01", "HypoMux-vnic-02"}, false, 2 * hypervRemoveScriptTimeout},
		{"三张走 RemoveAdapters", []string{"HypoMux-vnic-01", "HypoMux-vnic-02", "HypoMux-vnic-03"}, false, hypervRemoveBatchTimeoutMax},
	}
	for _, item := range cases {
		t.Run(item.label, func(t *testing.T) {
			fixture, _ := newManagedRemoveFixture(t, item.names...)
			if item.single {
				if err := fixture.service.Remove(item.names[0]); err != nil {
					t.Fatalf("Remove 失败：%v", err)
				}
			} else if _, err := fixture.service.RemoveAdapters(item.names); err != nil {
				t.Fatalf("RemoveAdapters 失败：%v", err)
			}
			// 只调一次提权脚本：N 张卡仍然只弹一次 UAC。
			if len(fixture.envelopes) != 1 {
				t.Fatalf("提权脚本被调了 %d 次; want 1", len(fixture.envelopes))
			}
			if got := len(fixture.envelopes[0].Items); got != len(item.names) {
				t.Fatalf("Items 条数 = %d; want %d", got, len(item.names))
			}
			if got := fixture.removeBudget(item.label); got != item.want {
				t.Fatalf("父进程预算 = %v; want %v", got, item.want)
			}
		})
	}
}
