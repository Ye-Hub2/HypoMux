package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// 冻结契约：reports/vnic/70-frozen-hyperv-interface.md §3，本文件逐字实现。
// 归属判定、台账、提权边界、DHCP 预算、出口池写入顺序都按该文档的硬约束实现。

const (
	// hypervState* 是 §3.2 冻结的状态取值。skipped 只出现在脚本结果里（冻结契约
	// §3.4「未登记的一律跳过」），不会成为任何一行的 State。
	hypervStateAbsent   = "absent"
	hypervStateCreating = "creating"
	hypervStateReady    = "ready"
	hypervStateFailed   = "failed"
	hypervStateSkipped  = "skipped"

	// §3.5 冻结的等待预算：网卡出现 15s / IP 就绪 45s / 整体 60s，轮询 500ms。
	hypervPollInterval     = 500 * time.Millisecond
	hypervInterfaceTimeout = 15 * time.Second
	hypervAddressTimeout   = 45 * time.Second
	hypervOverallTimeout   = 60 * time.Second

	// 父进程侧脚本上限。脚本自带界（waitip 内部就是 500ms 轮询 + 截止时间），这里只
	// 兜住「脚本彻底卡死」。提权子进程杀不掉，所以超时的语义是「报错不改状态」。
	hypervCreateScriptTimeout = 150 * time.Second
	hypervRemoveScriptTimeout = 90 * time.Second
	hypervReadScriptTimeout   = 60 * time.Second

	// hypervMaxResultBytes 卡住结果文件：脚本只写小 JSON，超过 1 MiB 一律不解析。
	hypervMaxResultBytes = 1 << 20
	// hypervInventoryTTL 摊薄 PowerShell 冷启动：前端按 1s 轮询 List()，没有缓存的话
	// 每秒要拉起一个 powershell.exe。5s 缓存对 UI 刷新无感，成本降到 1/5。
	hypervInventoryTTL = 5 * time.Second

	// hypervExternalSwitchType 是唯一允许建卡的交换机类型（reports/vnic/62 §1 实测
	// 本机的 XuniUplink 正是 External + AllowManagementOS）。
	hypervExternalSwitchType = "External"
)

// reports/vnic/62 §4.3 冻结的错误码，前端按 code 前缀映射。
const (
	hypervCodeUnavailable        = "hyperv_unavailable"
	hypervCodeSwitchNotFound     = "switch_not_found"
	hypervCodeSwitchNotAllowed   = "switch_not_allowed"
	hypervCodeElevationCancelled = "elevation_cancelled"
	hypervCodeBatchTooLarge      = "batch_too_large"
	hypervCodeTotalLimitReached  = "total_limit_reached"
	hypervCodeNameConflict       = "name_conflict"
	hypervCodeCreateFailed       = "create_failed"
	hypervCodeCreateTimeout      = "create_timeout"
	hypervCodeNotManaged         = "not_managed"
	hypervCodeRemoveFailed       = "remove_failed"
	hypervCodeRemoveTimeout      = "remove_timeout"
	hypervCodeDHCPTimeout        = "dhcp_timeout"
	hypervCodeScriptFailed       = "script_failed"
	hypervCodeShuttingDown       = "shutting_down"
	hypervCodeLedger             = "ledger_unavailable"
)

// HyperVError 让前端可以按 code 前缀分支。Wails 侧 error 会被序列化成字符串，所以
// Error() 里显式带上 code —— reports/vnic/62 §4.3 注明的「前缀匹配」才真正可用。
type HyperVError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *HyperVError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return e.Code + "：" + e.Message + "（" + e.Detail + "）"
	}
	return e.Code + "：" + e.Message
}

func hypervErrorf(code string, format string, args ...any) *HyperVError {
	return &HyperVError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func hypervUnsupportedError() *HyperVError {
	return hypervErrorf(hypervCodeUnavailable, "Hyper-V 虚拟网卡仅在 Windows 宿主机上可用")
}

var (
	// 提权执行链路的哨兵错误，平台文件（hyperv_adapter_windows.go / _other.go）返回，
	// 共享层据此映射成冻结错误码。
	errHypervUnsupported        = errors.New("Hyper-V 虚拟网卡仅在 Windows 宿主机上可用")
	errHypervScriptTimeout      = errors.New("提权脚本执行超时")
	errHypervElevationCancelled = errors.New("用户取消了管理员权限请求")
	errHypervResultMissing      = errors.New("提权脚本没有返回结果文件")
	errHypervServiceUnavailable = errors.New("Hyper-V 服务不可用")
)

// 两条文案常量：模型里没有 restartRequired/dhcp 字段（§3.2 冻结），
// 所以「需要重启聚合」和「DHCP 超时原因」只能落在 LastError 上。
const (
	hypervDHCPTimeoutHint = "等待 60 秒仍未从交换机拿到可用 IPv4（DHCP 未就绪）；网卡已保留，可稍后重试或直接删除"
	hypervPoolRestartHint = "已加入出口池，重启 HypoMux 聚合后生效"
)

// HyperVAdapterStatus 是 Hyper-V vNIC 在 Wails 边界的投影。字段与 camelCase JSON tag
// 由 reports/vnic/70-frozen-hyperv-interface.md §3.2 逐字冻结，前端 binding 由 wails3
// 从这里生成，改名即破坏契约。
type HyperVAdapterStatus struct {
	Name          string `json:"name"`          // Hyper-V 对象名，如 HypoMux-vnic-01
	InterfaceName string `json:"interfaceName"` // 宿主机网卡名 vEthernet (HypoMux-vnic-01) ← 出口池的键
	AdapterID     string `json:"adapterId"`     // DeviceId == Get-NetAdapter.InterfaceGuid
	MacAddress    string `json:"macAddress"`
	SwitchName    string `json:"switchName"`
	State         string `json:"state"` // absent|creating|ready|failed
	Address       string `json:"address"`
	PrefixLength  int    `json:"prefixLength"`
	Gateway       string `json:"gateway"`
	Managed       bool   `json:"managed"` // 在台账里 ⇒ 本工具创建
	InPool        bool   `json:"inPool"`  // 已在出口池 selected_adapter_ids
	BatchID       string `json:"batchId"`
	CreatedAt     string `json:"createdAt"`
	LastError     string `json:"lastError"`
}

// HyperVSwitch 同样由 §3.2 冻结。Uplink 是外部交换机绑定的物理网卡描述，
// NetAdapterName 是那块物理网卡在宿主上的别名。
type HyperVSwitch struct {
	Name              string `json:"name"`
	Type              string `json:"type"` // External | Internal
	AllowManagementOS bool   `json:"allowManagementOs"`
	Uplink            string `json:"uplink"`
	NetAdapterName    string `json:"netAdapterName"`
}

// HyperVAdapterService 管理宿主机上的 Hyper-V vNIC。它只做四件事：读台账、读系统实况、
// 通过一次性提权脚本创建/删除、把新卡并进出口池。它不持有任何路由或网卡状态。
type HyperVAdapterService struct {
	settings *SettingsService
	// adapters 仅作保留依赖（reports/vnic/62 §4.2）：List() 不能复用
	// AdapterService.List()，因为 adapters.go:80-82 会丢掉没有 IPv4 的网卡，而
	// creating/failed 状态恰恰只出现在这些网卡上。网关/DNS 走同包的
	// adapterPlatformMetadata()（adapter_metadata_windows.go:19），无需经它中转。
	adapters *AdapterService

	// opMu 是「独占操作」互斥：创建/删除整段事务（含提权）串行执行，避免两个 Create
	// 读到同一个 NextSeq 而分配出重名卡。
	// 它刻意不是数据锁 —— §3.6.4「不得持锁跨越提权调用」的含义是：UAC 弹出的那几十秒
	// 里不能把 settings/台账的读路径堵死。List() 与出口池读取只抢 mu，毫秒级进出。
	opMu sync.Mutex

	mu             sync.Mutex
	ledgerMu       sync.Mutex
	closed         bool
	cancel         context.CancelFunc
	ctx            context.Context
	inventory      hypervInventory
	inventoryErr   error
	inventoryAt    time.Time
	inventoryValid bool
	// inventoryHook 仅供单测注入「系统实况查询失败」的异常路径，生产恒为 nil。
	inventoryHook func() (hypervInventory, error)
}

// NewHyperVAdapterService 绑定共享的设置与网卡服务。同包内直接访问私有字段是既有做法
// （MTUService 也这样用），因此不需要 SettingsService / AdapterService 增加访问器。
func NewHyperVAdapterService(settings *SettingsService, adapters *AdapterService) *HyperVAdapterService {
	ctx, cancel := context.WithCancel(context.Background())
	return &HyperVAdapterService{settings: settings, adapters: adapters, ctx: ctx, cancel: cancel}
}

// ---------------------------------------------------------------- 台账（权威归属）

const (
	hypervLedgerVersion     = 1
	hypervAdapterNamePrefix = "HypoMux-vnic-"
	// §3.7 的硬上限。
	hypervMaxBatchSize     = 16
	hypervMaxTotalAdapters = 32
	hypervLedgerFileName   = "adapters.json"
	hypervJobDirName       = "jobs"
)

// hypervMACOUI 是 HypoMux 自用的本地管理前缀（首字节 0x02，最低位为 0 ⇒ 单播 + LAA）。
// reports/vnic/60 实测 Hyper-V 的动态 MAC 池只有 00:15:5D:10:90:00-FF 共 256 个，
// 撑不住 32 张上限，所以必须显式 -StaticMacAddress 并自己生成。
const hypervMACOUI = "021A2B"

// hypervLedgerEntry 是台账一行。AdapterID 存 Hyper-V 的 DeviceId（等价于
// Get-NetAdapter.InterfaceGuid），它是真机上唯一稳定的主键：Name 会重复（本机实测
// xuni-01 有两条，其中一条 MAC 为空的幽灵记录），只有 DeviceId 能定位到具体那条对象。
type hypervLedgerEntry struct {
	Name       string `json:"name"`
	AdapterID  string `json:"adapterId"`
	MACAddress string `json:"macAddress"`
	SwitchName string `json:"switchName"`
	BatchID    string `json:"batchId"`
	State      string `json:"state"`
	CreatedAt  string `json:"createdAt"`
	LastError  string `json:"lastError"`
}

// hypervLedger 是 adapters.json 的完整结构。NextSeq / NextMAC 只增不减：删除后序号与
// MAC 都不回收，避免与路由器侧的 MAC↔IP 租约记忆错位（下一次重建拿到同一个 MAC 却是
// 另一个 IP，会让「按 MAC 限速」的白名单直接失效）。
type hypervLedger struct {
	Version  int                 `json:"version"`
	NextSeq  int                 `json:"nextSeq"`
	NextMAC  int                 `json:"nextMAC"`
	Adapters []hypervLedgerEntry `json:"adapters"`
}

// hypervDirectory 复用 settings.go:136 的数据目录解析：HYPOMUX_DATA_DIR 优先，否则
// ~/.hypomux。台账与设置同根，重装应用不会丢归属记录。
func hypervDirectory() string {
	return filepath.Join(settingsDirectory(), "hyperv")
}

func hypervLedgerPath() string {
	return filepath.Join(hypervDirectory(), hypervLedgerFileName)
}

func hypervJobDirectory() string {
	return filepath.Join(hypervDirectory(), hypervJobDirName)
}

// loadHypervLedger 读台账。文件不存在是正常的首启状态，返回空台账而不是错误 ——
// 「没有记录」与「读不出来」的处置完全不同，前者照常跑，后者必须让 List() 报错。
func loadHypervLedger() (hypervLedger, error) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	data, err := os.ReadFile(hypervLedgerPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ledger, nil
		}
		return ledger, hypervErrorf(hypervCodeLedger, "读取 Hyper-V 台账失败：%v", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return ledger, nil
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}},
			hypervErrorf(hypervCodeLedger, "Hyper-V 台账已损坏，请检查 %s：%v", hypervLedgerPath(), err)
	}
	if ledger.Version == 0 {
		ledger.Version = hypervLedgerVersion
	}
	if ledger.Adapters == nil {
		ledger.Adapters = []hypervLedgerEntry{}
	}
	return ledger, nil
}

// save 走 atomic_file.go:9 的原子写（临时文件 + Rename）。台账是归属的唯一依据，一次
// 半截写入会让整个批次变成「系统里有卡但台账不知道」——那是最难恢复的状态。
func (l *hypervLedger) save() error {
	l.Version = hypervLedgerVersion
	if l.Adapters == nil {
		l.Adapters = []hypervLedgerEntry{}
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return hypervErrorf(hypervCodeLedger, "序列化 Hyper-V 台账失败：%v", err)
	}
	if err := atomicWriteFile(hypervLedgerPath(), data, 0o600); err != nil {
		return hypervErrorf(hypervCodeLedger, "写入 Hyper-V 台账失败：%v", err)
	}
	return nil
}

// find 按对象名取记录；名字比较不区分大小写（Windows 本身也不区分）。
func (l *hypervLedger) find(name string) (hypervLedgerEntry, int) {
	trimmed := strings.TrimSpace(name)
	for index, entry := range l.Adapters {
		if strings.EqualFold(strings.TrimSpace(entry.Name), trimmed) {
			return entry, index
		}
	}
	return hypervLedgerEntry{}, -1
}

func (l *hypervLedger) upsert(entry hypervLedgerEntry) {
	if _, index := l.find(entry.Name); index >= 0 {
		l.Adapters[index] = entry
		return
	}
	l.Adapters = append(l.Adapters, entry)
}

// removeEntry 丢掉记录，但绝不回退 NextSeq / NextMAC（序号不回收）。
func (l *hypervLedger) removeEntry(name string) {
	_, index := l.find(name)
	if index < 0 {
		return
	}
	l.Adapters = append(l.Adapters[:index], l.Adapters[index+1:]...)
}

// allocateNames 为一次批次预留 count 组 (名字, MAC)。先预留、再提权创建：任何一步
// 中途崩溃，账面上都已经是我们「打算创建」的卡，List() 只会显示 creating/absent，
// 而不会出现一张我们不敢认领的孤儿卡。
func (l *hypervLedger) allocateNames(count int, batchID string, createdAt string) []hypervLedgerEntry {
	entries := make([]hypervLedgerEntry, 0, count)
	for i := 0; i < count; i++ {
		seq := l.NextSeq
		l.NextSeq++
		mac := l.NextMAC
		l.NextMAC++
		entries = append(entries, hypervLedgerEntry{
			Name:       fmt.Sprintf("%s%02d", hypervAdapterNamePrefix, seq),
			MACAddress: formatHypervMAC(hypervMACValue(mac)),
			BatchID:    batchID,
			State:      hypervStateCreating,
			CreatedAt:  createdAt,
		})
	}
	return entries
}

// hypervMACValue 把计数器渲染成 12 位十六进制：02:1A:2B + 3 字节计数器。24 位计数器
// 远高于 32 张硬上限，不会绕回。
func hypervMACValue(counter int) string {
	return fmt.Sprintf("%s%06X", hypervMACOUI, uint64(counter)&0xFFFFFF)
}

// hypervCheckBatchCapacity 落实 §3.7 的 16 张/批、32 张/总两级上限。抽成纯函数是为了能
// 在不触碰真实 Hyper-V 的前提下做表驱动单测。
func hypervCheckBatchCapacity(existing int, count int) error {
	if count < 1 || count > hypervMaxBatchSize {
		return hypervErrorf(hypervCodeBatchTooLarge, "单次创建张数必须在 1–%d 之间（收到 %d）", hypervMaxBatchSize, count)
	}
	if existing+count > hypervMaxTotalAdapters {
		return hypervErrorf(hypervCodeTotalLimitReached, "当前已有 %d 张，再创建 %d 张会超过 %d 张上限", existing, count, hypervMaxTotalAdapters)
	}
	return nil
}

// ---------------------------------------------------------------- 名字与 MAC 工具

// isHyperVAdapterName 校验命名规范（归属判定的第二条）。删除路径先用它挡掉任意名字，
// 绝不让用户自己建的网卡进入删除流程。
func isHyperVAdapterName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if len(trimmed) <= len(hypervAdapterNamePrefix) {
		return false
	}
	if !strings.EqualFold(trimmed[:len(hypervAdapterNamePrefix)], hypervAdapterNamePrefix) {
		return false
	}
	digits := trimmed[len(hypervAdapterNamePrefix):]
	if len(digits) > 4 {
		return false
	}
	for _, item := range digits {
		if item < '0' || item > '9' {
			return false
		}
	}
	return true
}

// hypervAdapterSequence 取出名字里的序号，供 List() 按序号排序。
func hypervAdapterSequence(name string) int {
	trimmed := strings.TrimSpace(name)
	if !isHyperVAdapterName(trimmed) {
		return 1 << 30
	}
	value := 0
	for _, item := range trimmed[len(hypervAdapterNamePrefix):] {
		value = value*10 + int(item-'0')
	}
	return value
}

// hypervHostInterfaceName 拼出宿主机网卡名。reports/vnic/60 实测 Hyper-V vNIC 在宿主
// 上恒为 vEthernet (<对象名>)，而出口池 selected_adapter_ids 用的就是这个别名。
func hypervHostInterfaceName(name string) string {
	return "vEthernet (" + strings.TrimSpace(name) + ")"
}

// hypervObjectNameFromInterface 反解 vEthernet (HypoMux-vnic-01) -> HypoMux-vnic-01。
// 非本命名规范一律拒绝，绝不把用户自己的 vEthernet 网卡带进归属判定。
func hypervObjectNameFromInterface(alias string) (string, bool) {
	trimmed := strings.TrimSpace(alias)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "vethernet (") || !strings.HasSuffix(lower, ")") {
		return "", false
	}
	name := strings.TrimSpace(trimmed[len("vEthernet (") : len(trimmed)-1])
	if !isHyperVAdapterName(name) {
		return "", false
	}
	return name, true
}

// normalizeHypervMAC 把任意写法的 MAC 折叠成 12 位小写十六进制，供「逐字节一致」比较。
// 三种写法都要照顾：Hyper-V 读回裸 12 位大写十六进制（F6E6556A4D17）、台账与
// net.HardwareAddr.String() 是 aa:bb:cc:dd:ee:ff、Windows 注册表里还有 aa-bb-cc-…。
// reports/vnic/60 实测幽灵记录 MAC 为空，这里一并判为无效。
func normalizeHypervMAC(value string) string {
	var builder strings.Builder
	for _, item := range strings.TrimSpace(value) {
		switch {
		case item >= '0' && item <= '9':
			builder.WriteRune(item)
		case item >= 'a' && item <= 'f':
			builder.WriteRune(item)
		case item >= 'A' && item <= 'F':
			builder.WriteRune(item - 'A' + 'a')
		case item == ':' || item == '-' || item == '.' || item == ' ':
		default:
			return ""
		}
	}
	if builder.Len() != 12 {
		return ""
	}
	return builder.String()
}

// formatHypervMAC 归一成 Get-NetAdapter 同款的 aa:bb:cc:dd:ee:ff。
func formatHypervMAC(value string) string {
	normalized := normalizeHypervMAC(value)
	if normalized == "" {
		return ""
	}
	return normalized[0:2] + ":" + normalized[2:4] + ":" + normalized[4:6] + ":" +
		normalized[6:8] + ":" + normalized[8:10] + ":" + normalized[10:12]
}

// hypervMACBytes 给出逐字节比较用的形式；写法不合法一律返回 nil（=不可比）。
func hypervMACBytes(value string) []byte {
	normalized := normalizeHypervMAC(value)
	if normalized == "" {
		return nil
	}
	decoded := make([]byte, 6)
	for i := 0; i < 6; i++ {
		var pair byte
		for offset := 0; offset < 2; offset++ {
			digit := normalized[i*2+offset]
			var nibble byte
			switch {
			case digit >= '0' && digit <= '9':
				nibble = digit - '0'
			case digit >= 'a' && digit <= 'f':
				nibble = digit - 'a' + 10
			default:
				return nil
			}
			pair = pair<<4 | nibble
		}
		decoded[i] = pair
	}
	return decoded
}

// hypervMACEqual 是归属判定的第三条：删除时 MAC 必须逐字节相等。两侧任一不合法都
// 判为「不等」——拿不到 MAC 时绝不放行删除。
func hypervMACEqual(left string, right string) bool {
	leftBytes := hypervMACBytes(left)
	rightBytes := hypervMACBytes(right)
	if len(leftBytes) != 6 || len(rightBytes) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		if leftBytes[i] != rightBytes[i] {
			return false
		}
	}
	return true
}

// newHyperVBatchID 生成批次号；前端用 batchId 定位「撤销本次创建」的一批行。
func newHyperVBatchID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand 失败是可忽略的：批次号只需要「本次会话内唯一」，不是安全材料。
		return fmt.Sprintf("batch-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// ---------------------------------------------------------------- 提权脚本协议

// hypervEnvelope 是「唯一可变数据」的载体。脚本正文是 Go 常量，所有可变内容都经
// base64 注入（§3.3）：base64 字母表不可能逃出 PowerShell 单引号字面量，所以注入
// 防护靠结构、不靠转义。
type hypervEnvelope struct {
	Op             string               `json:"op"`
	ResultPath     string               `json:"resultPath"`
	SwitchName     string               `json:"switchName"`
	BatchID        string               `json:"batchId"`
	Items          []hypervEnvelopeItem `json:"items"`
	Aliases        []string             `json:"aliases"`
	TimeoutSeconds int                  `json:"timeoutSeconds"`
}

type hypervEnvelopeItem struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	AdapterID  string `json:"adapterId"`
	SwitchName string `json:"switchName"`
}

type hypervScriptAdapter struct {
	Name       string `json:"name"`
	AdapterID  string `json:"adapterId"`
	MAC        string `json:"mac"`
	SwitchName string `json:"switchName"`
	Status     string `json:"status"`
}

type hypervScriptSwitch struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	AllowManagementOS bool   `json:"allowManagementOs"`
	Uplink            string `json:"uplink"`
	NetAdapterName    string `json:"netAdapterName"`
}

type hypervScriptAddress struct {
	Alias        string `json:"alias"`
	Address      string `json:"address"`
	PrefixLength int    `json:"prefixLength"`
	PrefixOrigin string `json:"prefixOrigin"`
	AddressState string `json:"addressState"`
}

type hypervScriptFailure struct {
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	Error string `json:"error"`
}

// hypervScriptResult 是结果文件的内容。ShellExecuteExW + runas 拿不到子进程 stdout，
// 结果只能走文件；因此这个结构就是唯一的回传通道。
type hypervScriptResult struct {
	OK        bool                  `json:"ok"`
	Code      string                `json:"code"`
	Error     string                `json:"error"`
	Adapters  []hypervScriptAdapter `json:"adapters"`
	Switches  []hypervScriptSwitch  `json:"switches"`
	Addresses []hypervScriptAddress `json:"addresses"`
	Failures  []hypervScriptFailure `json:"failures"`
}

// hypervInventory 是「系统实况」的缓存体：Hyper-V 对象 + 交换机，一次读齐。
type hypervInventory struct {
	Adapters []hypervScriptAdapter
	Switches []hypervScriptSwitch
}

// find 按 DeviceId 优先、名字兜底定位一个 Hyper-V 对象。DeviceId 是稳定主键；名字
// 兜底时必须要求 MAC 非空，否则会命中 reports/vnic/60 实测到的同名幽灵记录。
func (i hypervInventory) find(adapterID string, name string) (hypervScriptAdapter, bool) {
	trimmedID := strings.TrimSpace(adapterID)
	if trimmedID != "" {
		for _, item := range i.Adapters {
			if strings.EqualFold(strings.TrimSpace(item.AdapterID), trimmedID) {
				return item, true
			}
		}
	}
	trimmedName := strings.TrimSpace(name)
	if trimmedName != "" {
		for _, item := range i.Adapters {
			if strings.EqualFold(strings.TrimSpace(item.Name), trimmedName) && normalizeHypervMAC(item.MAC) != "" {
				return item, true
			}
		}
	}
	return hypervScriptAdapter{}, false
}

func (i hypervInventory) hasName(name string) bool {
	trimmed := strings.TrimSpace(name)
	for _, item := range i.Adapters {
		if strings.EqualFold(strings.TrimSpace(item.Name), trimmed) && normalizeHypervMAC(item.MAC) != "" {
			return true
		}
	}
	return false
}

// newHypervJobPath 造一个本次调用的结果文件路径。文件名带纳秒时间戳，天然不会撞车。
func newHypervJobPath() (string, error) {
	directory := hypervJobDirectory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", hypervErrorf(hypervCodeScriptFailed, "创建 Hyper-V 任务目录失败：%v", err)
	}
	return filepath.Join(directory, fmt.Sprintf("job-%d-%s.json", time.Now().UnixNano(), newHyperVBatchID()[:8])), nil
}

// readHypervResult 读结果文件。读之前先 os.Stat 卡 1 MiB —— 脚本只写小 JSON，超过就
// 说明写错了，宁可当失败也不要拿一个半截 JSON 去改台账。
func readHypervResult(path string) (*hypervScriptResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > hypervMaxResultBytes {
		return nil, fmt.Errorf("结果文件超过 %d 字节上限", hypervMaxResultBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := &hypervScriptResult{}
	if err := json.Unmarshal(data, result); err != nil {
		return nil, fmt.Errorf("结果文件解析失败：%w", err)
	}
	return result, nil
}

// removeHypervJobDirectory 清掉任务目录。Shutdown 用它收尾：无副作用，不碰网卡、
// 不碰台账、不碰设置。
func removeHypervJobDirectory() {
	_ = os.RemoveAll(hypervJobDirectory())
}

// ---------------------------------------------------------------- 宿主网卡快照

type hypervHostInterface struct {
	alias        string
	mac          string // 已归一为 12 位小写十六进制
	address      string
	prefixLength int
	gateway      string
	hasIPv4      bool
}

type hypervHostSnapshot struct {
	byMAC   map[string]hypervHostInterface
	byAlias map[string]hypervHostInterface
}

// scanHypervHostInterfaces 自己扫 net.Interfaces()，不复用 AdapterService.List()：
// adapters.go:80-82 会把「还没有 IPv4」的网卡整个丢掉，那样 creating / failed 状态
// 永远不可见，而那恰恰是用户最需要看到的两行。网关/DNS 直接取同包的
// adapterPlatformMetadata()（复用 iphlpapi 的既有实现，不再冷启 PowerShell）。
func scanHypervHostInterfaces() hypervHostSnapshot {
	snapshot := hypervHostSnapshot{
		byMAC:   map[string]hypervHostInterface{},
		byAlias: map[string]hypervHostInterface{},
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return snapshot
	}
	metadata := adapterPlatformMetadata()
	for _, item := range interfaces {
		details := metadata[item.Index]
		host := hypervHostInterface{
			alias:   item.Name,
			mac:     normalizeHypervMAC(item.HardwareAddr.String()),
			gateway: details.Gateway,
		}
		// APIPA 169.254.0.0/16 与 adapters.go:66-69 同规则排除：拿不到 DHCP 地址时
		// Windows 会自分配一个 169.254.x.x，那不构成「网卡已就绪」。
		if addresses, addrErr := item.Addrs(); addrErr == nil {
			for _, address := range addresses {
				ip, network, parseErr := net.ParseCIDR(address.String())
				if parseErr != nil {
					continue
				}
				value := ip.To4()
				if value == nil || (value[0] == 169 && value[1] == 254) {
					continue
				}
				host.address = value.String()
				host.hasIPv4 = true
				if ones, bits := network.Mask.Size(); bits == 32 {
					host.prefixLength = ones
				}
				break
			}
		}
		snapshot.byAlias[strings.ToLower(item.Name)] = host
		if host.mac != "" {
			snapshot.byMAC[host.mac] = host
		}
	}
	return snapshot
}

// ---------------------------------------------------------------- 状态派生

// hypervEntryExpired 实现 §3.5 的整体 60s 预算：creating 超过 60s 仍未拿到 IPv4 就
// 判 failed（dhcp_timeout），但绝不删卡 —— 路由器可能稍后才分配，失败项还要留着 MAC
// 让用户去加白名单。
func hypervEntryExpired(entry hypervLedgerEntry, now time.Time) bool {
	created := strings.TrimSpace(entry.CreatedAt)
	if created == "" {
		return true
	}
	parsed, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return false
	}
	return now.Sub(parsed) >= hypervOverallTimeout
}

// deriveHyperVState 每次 List() 都从系统实况重算状态，台账只提供归属与历史 MAC。
func deriveHyperVState(entry hypervLedgerEntry, hostPresent bool, hasIPv4 bool, hypervPresent bool, now time.Time) string {
	// 台账里带错误或已标 failed 的，优先保持 failed：用户没重试之前不该被"恢复"成
	// ready，那会把一次真实失败糊弄过去。
	if strings.TrimSpace(entry.LastError) != "" || strings.TrimSpace(entry.State) == hypervStateFailed {
		return hypervStateFailed
	}
	if hostPresent && hasIPv4 {
		return hypervStateReady
	}
	if hostPresent || hypervPresent {
		if hypervEntryExpired(entry, now) {
			return hypervStateFailed
		}
		return hypervStateCreating
	}
	// 台账有、系统里完全没有：Hyper-V 对象已消失（例如用户手工删了）。
	return hypervStateAbsent
}

// buildHyperVStatus 把台账一行 + 系统实况投影成 Wails 边界的一行。
func buildHyperVStatus(entry hypervLedgerEntry, inventory hypervInventory, hosts hypervHostSnapshot, pool map[string]bool, now time.Time) HyperVAdapterStatus {
	name := strings.TrimSpace(entry.Name)
	status := HyperVAdapterStatus{
		Name:          name,
		InterfaceName: hypervHostInterfaceName(name),
		AdapterID:     strings.TrimSpace(entry.AdapterID),
		MacAddress:    formatHypervMAC(entry.MACAddress),
		SwitchName:    strings.TrimSpace(entry.SwitchName),
		Managed:       true,
		BatchID:       strings.TrimSpace(entry.BatchID),
		CreatedAt:     strings.TrimSpace(entry.CreatedAt),
		LastError:     strings.TrimSpace(entry.LastError),
	}
	// 先用台账里的 MAC 去认领宿主网卡：名字可以被人改，MAC 不会凭空变。只有台账还没
	// 记上 MAC（刚预留、脚本未回填）时才退回按别名认领。
	if host, ok := hosts.byMAC[normalizeHypervMAC(entry.MACAddress)]; ok {
		status.InterfaceName = host.alias
		applyHyperVHost(&status, host)
	} else if host, ok := hosts.byAlias[strings.ToLower(status.InterfaceName)]; ok {
		applyHyperVHost(&status, host)
	}
	_, live := inventory.find(entry.AdapterID, entry.Name)
	host, hostPresent := hosts.byAlias[strings.ToLower(status.InterfaceName)]
	status.State = deriveHyperVState(entry, hostPresent, host.hasIPv4, live, now)
	if status.State == hypervStateFailed && status.LastError == "" {
		status.LastError = hypervDHCPTimeoutHint
	}
	status.InPool = pool[strings.ToLower(status.InterfaceName)]
	return status
}

func applyHyperVHost(status *HyperVAdapterStatus, host hypervHostInterface) {
	status.Address = host.address
	status.PrefixLength = host.prefixLength
	status.Gateway = host.gateway
}

// buildUnmanagedHyperVStatus 展示「系统里有、但台账没有」的同规范网卡：只读可见，
// Managed=false ⇒ Remove 会直接拒绝。这是绝误删的最后一道闸门。
func buildUnmanagedHyperVStatus(adapter hypervScriptAdapter, hosts hypervHostSnapshot, pool map[string]bool) HyperVAdapterStatus {
	name := strings.TrimSpace(adapter.Name)
	status := HyperVAdapterStatus{
		Name:          name,
		InterfaceName: hypervHostInterfaceName(name),
		AdapterID:     strings.TrimSpace(adapter.AdapterID),
		MacAddress:    formatHypervMAC(adapter.MAC),
		SwitchName:    strings.TrimSpace(adapter.SwitchName),
		State:         hypervStateAbsent,
	}
	if host, ok := hosts.byMAC[normalizeHypervMAC(adapter.MAC)]; ok {
		status.InterfaceName = host.alias
		applyHyperVHost(&status, host)
	} else if host, ok := hosts.byAlias[strings.ToLower(status.InterfaceName)]; ok {
		applyHyperVHost(&status, host)
	}
	host, hostPresent := hosts.byAlias[strings.ToLower(status.InterfaceName)]
	switch {
	case hostPresent && host.hasIPv4:
		status.State = hypervStateReady
	case hostPresent:
		status.State = hypervStateCreating
	default:
		status.State = hypervStateAbsent
	}
	status.InPool = pool[strings.ToLower(status.InterfaceName)]
	return status
}

func sortHyperVStatuses(rows []HyperVAdapterStatus) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := hypervAdapterSequence(rows[i].Name)
		right := hypervAdapterSequence(rows[j].Name)
		if left != right {
			return left < right
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
}

func findHyperVEntry(list []hypervLedgerEntry, name string) (hypervLedgerEntry, bool) {
	for _, entry := range list {
		if strings.EqualFold(strings.TrimSpace(entry.Name), strings.TrimSpace(name)) {
			return entry, true
		}
	}
	return hypervLedgerEntry{}, false
}

// ---------------------------------------------------------------- 服务方法

func (s *HyperVAdapterService) opened() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

// checkOpen 拒绝已关闭服务上的调用。Shutdown 之后 Wails 仍可能把在途调用投递进来。
func (s *HyperVAdapterService) checkOpen() error {
	if s == nil {
		return errHypervServiceUnavailable
	}
	if !s.opened() {
		return hypervErrorf(hypervCodeShuttingDown, "HypoMux 正在退出")
	}
	return nil
}

func (s *HyperVAdapterService) invalidateInventory() {
	s.mu.Lock()
	s.inventoryValid = false
	s.inventoryErr = nil
	s.mu.Unlock()
}

// readLedger / updateLedger 把「读-改-写」整段收在 ledgerMu 里。§3.6.4 的顺序约束要求
// 台账落盘必须是一次原子读改写，否则两个并发的 Create 会互相覆盖对方的预留。
func (s *HyperVAdapterService) readLedger() (hypervLedger, error) {
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	return loadHypervLedger()
}

func (s *HyperVAdapterService) updateLedger(mutate func(*hypervLedger) error) error {
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	ledger, err := loadHypervLedger()
	if err != nil {
		return err
	}
	if err := mutate(&ledger); err != nil {
		return err
	}
	return ledger.save()
}

// readInventory 读「系统实况」并做 5s 缓存。冷启一个 powershell.exe 要几百毫秒，前端
// 1s 一轮的 List() 每次都付这个代价会把 CPU 打满。
//
// 关键约束（§3.8）：这里走的是 **非提权** 脚本路径，永远不会弹 UAC。即便未提权读不到
// Hyper-V（reports/vnic/60 记录该场景本机尚未验证），表现也只是「状态列偏保守」，
// 绝不会在 List() 时突然弹一个管理员对话框。
func (s *HyperVAdapterService) readInventory() (hypervInventory, error) {
	if override := s.inventoryOverride(); override != nil {
		return override()
	}
	s.mu.Lock()
	if s.inventoryValid && time.Since(s.inventoryAt) < hypervInventoryTTL {
		cached, cachedErr := s.inventory, s.inventoryErr
		s.mu.Unlock()
		return cached, cachedErr
	}
	s.mu.Unlock()

	inventory := hypervInventory{}
	var readErr error
	if !hypervPlatformSupported() {
		readErr = errHypervUnsupported
	} else if result, err := s.runScript(hypervEnvelope{Op: "inventory"}, hypervReadScriptTimeout, false); err != nil {
		readErr = err
	} else if result != nil {
		inventory.Adapters = append(inventory.Adapters, result.Adapters...)
		inventory.Switches = append(inventory.Switches, result.Switches...)
	}

	s.mu.Lock()
	s.inventory = inventory
	s.inventoryErr = readErr
	s.inventoryAt = time.Now()
	s.inventoryValid = true
	s.mu.Unlock()
	return inventory, readErr
}

// inventoryOverride 是**仅供测试注入**的钩子，用来复现「查询系统实况失败」这条异常路径
// （审计 M2：Remove 在查询失败时若继续清台账，会留下用户永远删不掉的孤儿网卡）。生产
// 路径永远是 nil，走下面的真脚本。
func (s *HyperVAdapterService) inventoryOverride() func() (hypervInventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inventoryHook
}

// runScript 是所有脚本调用的唯一入口。elevated=true 才会触发 UAC，只有 Create 与
// Remove 传 true。
//
// 超时语义（§3.3）：提权子进程杀不掉，所以超时只是「放弃等待」，绝不改台账状态。
// 脚本本身按 op 自带界且幂等，重试安全。
//
// 注意返回值组合：**结果文件缺失时 result 为 nil**，此时调用方无法确知脚本到底做了什么，
// 必须按「不可知」处理（见 hypervScriptOutcomeUncertain）；**结果文件存在但 ok=false 时
// result 非 nil**，脚本跑完了并逐条交代了成败，可以放心按它的说法 reconcile。
func (s *HyperVAdapterService) runScript(envelope hypervEnvelope, timeout time.Duration, elevated bool) (*hypervScriptResult, error) {
	if !hypervPlatformSupported() {
		return nil, errHypervUnsupported
	}
	resultPath, err := newHypervJobPath()
	if err != nil {
		return nil, err
	}
	defer os.Remove(resultPath)
	envelope.ResultPath = resultPath

	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, hypervErrorf(hypervCodeScriptFailed, "序列化脚本参数失败：%v", err)
	}

	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	var runErr error
	if elevated {
		runErr = hypervExecuteElevated(runCtx, payload, timeout)
	} else {
		runErr = hypervExecuteUnelevated(runCtx, payload, timeout)
	}

	result, readErr := readHypervResult(resultPath)
	if readErr != nil {
		if runErr != nil {
			return nil, mapHypervRunError(runErr, elevated, envelope.Op)
		}
		return nil, hypervErrorf(hypervCodeScriptFailed, "脚本未产出结果文件：%v", readErr)
	}
	if !result.OK {
		detail := strings.TrimSpace(result.Error)
		if detail == "" {
			detail = "结果标记为失败但未给出原因"
		}
		// 关键：结果文件本身是可信的回报，脚本**跑完了**并逐条交代了成败。把它连同错误
		// 一起返回，调用方才能拿到 failures 里每张卡的具体原因（审计 M3：必须能区分
		// 「脚本明确回报失败」与「父进程放弃等待」）。
		return result, hypervErrorf(hypervCodeScriptFailed, "脚本返回失败：%s", detail)
	}
	if runErr != nil {
		return nil, mapHypervRunError(runErr, elevated, envelope.Op)
	}
	return result, nil
}

// hypervTimeoutCode 把「脚本超时」按操作映射成可辨识的错误码（冻结契约 §3.7 要求硬失败
// 返回可辨识错误码，前端才能区分「去刷新看看」和「彻底失败了」）。
func hypervTimeoutCode(op string) string {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "create":
		return hypervCodeCreateTimeout
	case "remove":
		return hypervCodeRemoveTimeout
	}
	return hypervCodeUnavailable
}

func mapHypervRunError(err error, elevated bool, op string) *HyperVError {
	switch {
	case errors.Is(err, errHypervElevationCancelled):
		return hypervErrorf(hypervCodeElevationCancelled, "已取消管理员权限请求")
	case errors.Is(err, errHypervScriptTimeout), errors.Is(err, context.DeadlineExceeded):
		if elevated {
			return hypervErrorf(hypervTimeoutCode(op), "提权脚本执行超时；Hyper-V 操作可能仍在后台进行，请刷新列表确认")
		}
		return hypervErrorf(hypervCodeUnavailable, "读取 Hyper-V 状态超时")
	case errors.Is(err, context.Canceled):
		return hypervErrorf(hypervCodeShuttingDown, "HypoMux 正在退出")
	case errors.Is(err, errHypervUnsupported):
		return hypervUnsupportedError()
	}
	return hypervErrorf(hypervCodeScriptFailed, "脚本执行失败：%v", err)
}

func (s *HyperVAdapterService) currentPool() map[string]bool {
	pool := map[string]bool{}
	if s == nil || s.settings == nil {
		return pool
	}
	for _, item := range s.settings.Get().SelectedAdapterIDs {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		pool[strings.ToLower(trimmed)] = true
	}
	return pool
}

func toHyperVSwitches(raw []hypervScriptSwitch) []HyperVSwitch {
	rows := make([]HyperVSwitch, 0, len(raw))
	for _, item := range raw {
		rows = append(rows, HyperVSwitch{
			Name:              strings.TrimSpace(item.Name),
			Type:              strings.TrimSpace(item.Type),
			AllowManagementOS: item.AllowManagementOS,
			Uplink:            strings.TrimSpace(item.Uplink),
			NetAdapterName:    strings.TrimSpace(item.NetAdapterName),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return rows
}

func findHyperVSwitch(rows []HyperVSwitch, name string) (HyperVSwitch, bool) {
	for _, item := range rows {
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return item, true
		}
	}
	return HyperVSwitch{}, false
}

// List 返回台账与系统实况的合并视图。绝不触发 UAC，也绝不调用 AdapterService.List()
// （后者会丢掉没有 IPv4 的网卡，而那正是 creating/failed 的唯一来源）。
func (s *HyperVAdapterService) List() ([]HyperVAdapterStatus, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	// 读不到 Hyper-V 也不该让整个面板挂掉：降级成「只有台账」视图，状态偏保守而已。
	inventory, _ := s.readInventory()
	hosts := scanHypervHostInterfaces()
	pool := s.currentPool()
	now := time.Now()

	rows := make([]HyperVAdapterStatus, 0, len(inventory.Adapters)+4)
	claimed := map[string]bool{}
	ledger, ledgerErr := s.readLedger()
	if ledgerErr != nil {
		return nil, ledgerErr
	}
	for _, entry := range ledger.Adapters {
		name := strings.TrimSpace(entry.Name)
		// 台账里混进陌生名字时宁可不可见：它不可能是本工具建的，更不该进入删除流程。
		if !isHyperVAdapterName(name) {
			continue
		}
		rows = append(rows, buildHyperVStatus(entry, inventory, hosts, pool, now))
		claimed[strings.ToLower(name)] = true
	}
	// 台账外但命名合规的卡：只读展示（Managed=false ⇒ Remove 直接拒绝）。这是绝
	// 误删用户自己网卡的最后一道闸门，也是 §3.4「未登记的一律跳过」的可见化。
	for _, adapter := range inventory.Adapters {
		name := strings.TrimSpace(adapter.Name)
		if !isHyperVAdapterName(name) || claimed[strings.ToLower(name)] {
			continue
		}
		rows = append(rows, buildUnmanagedHyperVStatus(adapter, hosts, pool))
	}
	sortHyperVStatuses(rows)
	return rows, nil
}

// Switches 列出可用的虚拟交换机。命令面 §4：只有同时满足 External +
// AllowManagementOS 的交换机才能承载宿主机可用的 vNIC。
func (s *HyperVAdapterService) Switches() ([]HyperVSwitch, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !hypervPlatformSupported() {
		return nil, hypervUnsupportedError()
	}
	inventory, err := s.readInventory()
	if err != nil {
		return nil, hypervErrorf(hypervCodeUnavailable, "无法读取 Hyper-V 虚拟交换机：%v", err.Error())
	}
	return toHyperVSwitches(inventory.Switches), nil
}

// hypervScriptOutcomeUncertain 报告这次提权脚本的结果是否**不可知**。
//
// 审计 M3 的根因：父进程杀不掉提权子进程（契约 §3.3 引 engineclient/privileged_windows.go:390-404，
// hypervRunElevatedShellExecute 在 WAIT_TIMEOUT 时只返回 errHypervScriptTimeout 不终止子进程），
// 于是父进程超时后子进程**仍可能把 N 张卡全建出来**。此时若按「脚本失败」处理、把预留
// 条目从台账抹掉，就产生 N 张用户永远删不掉的孤儿网卡（List() 只能当 Managed=false 只读行
// 展示，Remove() 又永远返回 not_managed）—— 这也是字面违反 §3.3「超时只报错不改状态」。
//
// 判定原则：**台账宁可多，不可少**。多出来的行用户能删，少掉的行用户删不掉。
// 只有「脚本一次都没跑起来」（UAC 被取消 / 平台不支持 / 启动前就失败）才算可知。
func hypervScriptOutcomeUncertain(result *hypervScriptResult, runErr error) bool {
	if result != nil {
		// 脚本产出了结果文件：不管 ok 与否，它都知道自己做了什么。
		return false
	}
	if runErr == nil {
		return false
	}
	var coded *HyperVError
	if !errors.As(runErr, &coded) {
		// 连 HyperVError 都不是（例如创建任务目录失败）⇒ 脚本根本没启动。
		return false
	}
	switch coded.Code {
	case hypervCodeElevationCancelled, hypervCodeUnavailable:
		return false
	default:
		// create_timeout / shutting_down / script_failed（结果文件缺失或坏 JSON）⇒ 不可知。
		return true
	}
}

// hypervBatchOutcome 是 §3.7 批次回滚的裁决结果。抽成数据结构是为了把「哪几条写回台账、
// 哪几条抹掉」这条最容易出错的规则拆成可测的纯函数（hypervReconcileBatch）。
type hypervBatchOutcome struct {
	// finished 是要写回台账的条目（已回填 DeviceId/MAC 或标了 failed）。
	finished []hypervLedgerEntry
	// purge 是要从台账里抹掉的名字——**只在脚本明确回报失败时**才非空。
	purge []string
	// abandoned 为 true 表示父进程已放弃等待、结果不可知：整批标 failed、不抹台账、
	// 也不启动 awaitBatch（状态未知的卡绝不能自动进出口池）。
	abandoned bool
	failure   *HyperVError
}

// hypervReconcileBatch 落地 §3.7「硬失败停本批、保留已成功、返回可辨识错误码」。
//
// 两种失败必须分开（审计 M3）：
//   - 脚本**明确回报**失败（有结果文件）：逐条 reconcile，硬失败之后未处理的条目从未存在
//     过，可以从台账抹掉。
//   - 父进程**放弃等待**（超时 / 退出 / 结果文件缺失）：结果不可知，整批保留在台账里并标
//     failed，用户随后仍能正常 Remove 掉它们。
func hypervReconcileBatch(
	reserved []hypervLedgerEntry,
	created map[string]hypervScriptAdapter,
	failures []hypervScriptFailure,
	runErr error,
	result *hypervScriptResult,
) hypervBatchOutcome {
	outcome := hypervBatchOutcome{}
	if hypervScriptOutcomeUncertain(result, runErr) {
		// 不可知：一条都不能抹。全部标 failed + 明确文案，让用户刷新列表核对后再操作。
		message := "未知原因"
		if runErr != nil {
			message = runErr.Error()
		}
		entryError := fmt.Sprintf("提权脚本未在时限内返回，创建结果未知（%s）；已保留台账归属，可刷新列表核对后删除", message)
		for _, entry := range reserved {
			entry.State = hypervStateFailed
			entry.LastError = entryError
			outcome.finished = append(outcome.finished, entry)
		}
		outcome.abandoned = true
		outcome.failure = &HyperVError{
			Code:    hypervTimeoutCode("create"),
			Message: "提权脚本未在时限内返回，创建结果未知",
			Detail:  entryError,
		}
		return outcome
	}

	var failure *HyperVError
	for index, entry := range reserved {
		row, ok := created[strings.ToLower(entry.Name)]
		if ok {
			entry.AdapterID = strings.TrimSpace(row.AdapterID)
			if mac := normalizeHypervMAC(row.MAC); mac != "" {
				entry.MACAddress = formatHypervMAC(mac)
			}
			if strings.TrimSpace(row.SwitchName) != "" {
				entry.SwitchName = strings.TrimSpace(row.SwitchName)
			}
			outcome.finished = append(outcome.finished, entry)
			continue
		}
		if failure != nil {
			// 硬失败之后脚本根本没处理到的条目：它们从未存在过，留着只会是幻影。
			outcome.purge = append(outcome.purge, entry.Name)
			continue
		}
		message := "未知原因"
		if runErr != nil {
			message = runErr.Error()
		}
		for _, item := range failures {
			if strings.EqualFold(strings.TrimSpace(item.Name), entry.Name) && strings.TrimSpace(item.Error) != "" {
				message = strings.TrimSpace(item.Error)
			}
		}
		entry.State = hypervStateFailed
		entry.LastError = fmt.Sprintf("第 %d/%d 张创建失败：%s", index+1, len(reserved), message)
		outcome.finished = append(outcome.finished, entry)
		failure = &HyperVError{Code: hypervCodeCreateFailed, Message: entry.LastError, Detail: message}
	}
	outcome.failure = failure
	return outcome
}

// Create 在外部交换机上批量创建 vNIC。返回本批的立即态（通常 creating），DHCP 等待
// 由 awaitBatch 在后台完成，前端轮询 List() 看 ready/failed（reports/vnic/62 §6.1）。
//
// 顺序严格按 §3.6.4：提权创建 → 台账落盘 → UpdateHome。但台账的 **预留** 刻意写在
// 提权之前（见函数体内注释），使「系统里出现的卡一定在台账里」这个不变量成立。
func (s *HyperVAdapterService) Create(switchName string, count int) ([]HyperVAdapterStatus, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	target := strings.TrimSpace(switchName)
	if target == "" {
		return nil, hypervErrorf(hypervCodeSwitchNotFound, "请选择一个 Hyper-V 外部交换机")
	}
	if count < 1 || count > hypervMaxBatchSize {
		return nil, hypervErrorf(hypervCodeBatchTooLarge, "单次创建张数必须在 1–%d 之间（收到 %d）", hypervMaxBatchSize, count)
	}
	if !hypervPlatformSupported() {
		return nil, hypervUnsupportedError()
	}

	inventory, inventoryErr := s.readInventory()
	if inventoryErr != nil {
		return nil, hypervErrorf(hypervCodeUnavailable, "无法读取 Hyper-V 虚拟交换机：%v", inventoryErr.Error())
	}
	info, found := findHyperVSwitch(toHyperVSwitches(inventory.Switches), target)
	if !found {
		return nil, hypervErrorf(hypervCodeSwitchNotFound, "找不到 Hyper-V 交换机 %q", target)
	}
	if !strings.EqualFold(info.Type, hypervExternalSwitchType) {
		return nil, hypervErrorf(hypervCodeSwitchNotAllowed, "交换机 %q 是 %s 类型，只能在外部交换机上创建网卡", info.Name, info.Type)
	}
	if !info.AllowManagementOS {
		return nil, hypervErrorf(hypervCodeSwitchNotAllowed, "交换机 %q 未开启「允许管理操作系统」，宿主机拿不到这张卡的 IP", info.Name)
	}

	// opMu 覆盖整个创建事务（含提权）。它只与 opMu 互斥，不与 List()/settings 的读
	// 路径互斥，所以 UAC 弹出的那几十秒里面板依然能刷新。
	s.opMu.Lock()
	defer s.opMu.Unlock()

	batchID := newHyperVBatchID()
	createdAt := time.Now().Format(time.RFC3339)
	var reserved []hypervLedgerEntry
	if err := s.updateLedger(func(ledger *hypervLedger) error {
		if err := hypervCheckBatchCapacity(len(ledger.Adapters), count); err != nil {
			return err
		}
		// 烧掉已被占用的序号：台账里没有、但系统里已经有同名卡（用户手工建的同名卡）
		// 时绝不覆盖，直接把序号跳过去。
		for {
			candidate := fmt.Sprintf("%s%02d", hypervAdapterNamePrefix, ledger.NextSeq)
			_, claimedAt := ledger.find(candidate)
			if claimedAt < 0 && !inventory.hasName(candidate) {
				break
			}
			ledger.NextSeq++
		}
		reserved = ledger.allocateNames(count, batchID, createdAt)
		for _, entry := range reserved {
			ledger.upsert(entry)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	items := make([]hypervEnvelopeItem, 0, len(reserved))
	for _, entry := range reserved {
		items = append(items, hypervEnvelopeItem{Name: entry.Name, MAC: formatHypervMAC(entry.MACAddress)})
	}
	result, runErr := s.runScript(hypervEnvelope{
		Op:         "create",
		SwitchName: info.Name,
		BatchID:    batchID,
		Items:      items,
	}, hypervCreateScriptTimeout, true)
	// reported 是脚本**真正产出**的结果（可能为 nil）。判定「结果是否不可知」必须看它，
	// 不能看后面那个兜底出来的空壳 —— 否则超时会又被当成「脚本明确回报失败」。
	reported := result
	if result == nil {
		result = &hypervScriptResult{}
	}
	created := make(map[string]hypervScriptAdapter, len(result.Adapters))
	for _, row := range result.Adapters {
		if key := strings.ToLower(strings.TrimSpace(row.Name)); key != "" {
			created[key] = row
		}
	}

	// §3.7 批次语义 + 审计 M3：裁决逻辑全在纯函数 hypervReconcileBatch 里。关键区别：
	// 脚本明确回报失败 → 未处理的条目可以抹；父进程放弃等待（超时/退出/结果文件缺失）
	// → 结果不可知，一条都不能抹，否则会留下用户永远删不掉的孤儿网卡。
	outcome := hypervReconcileBatch(reserved, created, result.Failures, runErr, reported)

	if err := s.updateLedger(func(ledger *hypervLedger) error {
		for _, entry := range outcome.finished {
			ledger.upsert(entry)
		}
		for _, name := range outcome.purge {
			ledger.removeEntry(name)
		}
		return nil
	}); err != nil && outcome.failure == nil {
		outcome.failure = &HyperVError{Code: hypervCodeLedger, Message: "Hyper-V 台账写入失败", Detail: err.Error()}
	}

	s.invalidateInventory()
	// abandoned 时不启动后台等待：状态未知的卡绝不能自动进出口池。台账已经把它们标成
	// failed，用户刷新列表核对后自行 Remove 即可。
	if !outcome.abandoned {
		s.awaitBatch(outcome.finished)
	}

	hosts := scanHypervHostInterfaces()
	pool := s.currentPool()
	rows := make([]HyperVAdapterStatus, 0, len(outcome.finished))
	for _, entry := range outcome.finished {
		row := buildHyperVStatus(entry, inventory, hosts, pool, time.Now())
		// 冻结模型没有 restartRequired 字段，LastError 是唯一能把「需要重启聚合」送到
		// 前端的通道；只在这一刻出现，List() 后续轮询时台账是干净的，提示自然消失。
		if row.State != hypervStateFailed {
			row.LastError = hypervPoolRestartHint
		}
		rows = append(rows, row)
	}
	return rows, outcome.failure
}

// awaitBatch 落地 §3.5 的等待预算：Create 立即返回（不阻塞调用方 45s），实际等待在
// 后台进行——网卡出现 15s → 出现后并进出口池 → DHCP 地址就绪 45s → 整体不超过 60s。
// goroutine 挂在服务 ctx 上，Shutdown 会立刻停掉它。
func (s *HyperVAdapterService) awaitBatch(entries []hypervLedgerEntry) {
	if len(entries) == 0 {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	go func() {
		appeared := s.awaitInterfaces(names, hypervInterfaceTimeout)
		if len(appeared) > 0 && s.opened() {
			// 出口池的键是 vEthernet 别名，必须等网卡真的在宿主上出现才能写。
			_ = s.applyPoolUpdate(appeared, nil)
		}
		s.awaitAddresses(appeared)
	}()
}

// awaitInterfaces 每 500ms 扫一次宿主网卡，等齐 names 或超时。返回实际出现的那些。
func (s *HyperVAdapterService) awaitInterfaces(names []string, timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	appeared := make([]string, 0, len(names))
	for {
		hosts := scanHypervHostInterfaces()
		for _, name := range names {
			if _, exists := findString(appeared, name); exists {
				continue
			}
			if _, ok := hosts.byAlias[strings.ToLower(hypervHostInterfaceName(name))]; ok {
				appeared = append(appeared, name)
			}
		}
		if len(appeared) == len(names) || !time.Now().Before(deadline) || !s.opened() {
			break
		}
		time.Sleep(hypervPollInterval)
	}
	return appeared
}

func findString(list []string, value string) (string, bool) {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return item, true
		}
	}
	return "", false
}

// hypervAddressState 是单个网卡的 DHCP 判定结果。
type hypervAddressState struct {
	ready        bool
	address      string
	prefixLength int
	// fallback 为 true 表示结论来自宿主扫描兜底而非脚本（§3.5 判据只在脚本完全没提到
	// 该网卡时才允许兜底）。
	fallback bool
}

// hypervResolveAddresses 是 §3.5 成功判据的**唯一实现**，抽成纯函数是为了能在不跑
// PowerShell、不动真实网卡的前提下测这条硬规则。
//
// 审计 M4：宿主扫描兜底只能**补齐**脚本没提到的网卡，绝不能**推翻**脚本的显式判定。
// 原实现在脚本成功返回时无条件跑兜底，只要宿主上有任意非 APIPA 的 IPv4 就判 ready ——
// 于是脚本明确回报 PrefixOrigin=Static 或 AddressState=Duplicate 被正确拒绝之后，又被
// 翻回 ready，一张从未拿到 DHCP 租约的卡被标成 ready、进入出口池、重启后聚合静默失效。
func hypervResolveAddresses(aliases []string, reported []hypervScriptAddress, hosts hypervHostSnapshot) map[string]hypervAddressState {
	// mentioned 记录脚本**见过**哪些 alias（不论判定通过与否）。
	mentioned := map[string]bool{}
	accepted := map[string]hypervAddressState{}
	for _, row := range reported {
		key := strings.ToLower(strings.TrimSpace(row.Alias))
		if key == "" {
			continue
		}
		mentioned[key] = true
		// §3.5 硬判据，两个条件缺一不可（reports/vnic/60 实测 APIPA 与 Duplicate 都不算就绪）。
		if !strings.EqualFold(strings.TrimSpace(row.PrefixOrigin), "Dhcp") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(row.AddressState), "Preferred") {
			continue
		}
		value := strings.TrimSpace(row.Address)
		if value == "" {
			continue
		}
		accepted[key] = hypervAddressState{ready: true, address: value, prefixLength: row.PrefixLength}
	}

	out := make(map[string]hypervAddressState, len(aliases))
	for _, alias := range aliases {
		key := strings.ToLower(strings.TrimSpace(alias))
		if key == "" {
			continue
		}
		if state, ok := accepted[key]; ok {
			out[key] = state
			continue
		}
		state := hypervAddressState{}
		if !mentioned[key] {
			// 脚本压根没提到这张卡，才允许用宿主扫描兜底（应对脚本内部竞态：DHCP 刚
			// 成交、脚本那轮还没抓到）。脚本提过但判否定的，绝不翻案。
			//
			// hasIPv4 由 scanHypervHostInterfaces 保证已排除 169.254/16，但这里再加一道
			// 本地判定：判据是本函数自己的不变量，不能依赖调用方的实现细节。
			if host, found := hosts.byAlias[key]; found && host.hasIPv4 && !isHypervLinkLocalIPv4(host.address) {
				state = hypervAddressState{
					ready:        true,
					address:      host.address,
					prefixLength: host.prefixLength,
					fallback:     true,
				}
			}
		}
		out[key] = state
	}
	return out
}

// isHypervLinkLocalIPv4 判断是否 APIPA（169.254.0.0/16）。Windows 自分配的链路本地地址
// 不代表 DHCP 成功，reports/vnic/60 实测它与 Duplicate 一样要判失败。
func isHypervLinkLocalIPv4(address string) bool {
	parsed := net.ParseIP(strings.TrimSpace(address))
	if parsed == nil {
		return false
	}
	value := parsed.To4()
	return value != nil && value[0] == 169 && value[1] == 254
}

// awaitAddresses 用 **非提权** 脚本等 DHCP。成功判据必须是 PrefixOrigin=Dhcp 且
// AddressState=Preferred（reports/vnic/60 实测 APIPA 与 Duplicate 都不算就绪），
// 判定逻辑集中在 hypervResolveAddresses。
// 脚本整体失败/超时时什么都不改：条目留在 creating，由 List() 在 60s 预算耗尽后派生
// 成 failed（§3.5「超时只报错不改状态」）。
func (s *HyperVAdapterService) awaitAddresses(names []string) {
	if len(names) == 0 || !s.opened() || !hypervPlatformSupported() {
		return
	}
	aliases := make([]string, 0, len(names))
	for _, name := range names {
		aliases = append(aliases, hypervHostInterfaceName(name))
	}
	result, err := s.runScript(hypervEnvelope{
		Op:             "waitip",
		Aliases:        aliases,
		TimeoutSeconds: int(hypervAddressTimeout / time.Second),
	}, hypervAddressTimeout+hypervReadScriptTimeout, false)
	if err != nil {
		return
	}
	var reported []hypervScriptAddress
	if result != nil {
		reported = result.Addresses
	}
	// 补一轮宿主扫描仅供「脚本没提到的网卡」兜底，见 hypervResolveAddresses 的注释。
	verdict := hypervResolveAddresses(aliases, reported, scanHypervHostInterfaces())
	_ = s.updateLedger(func(ledger *hypervLedger) error {
		for _, name := range names {
			key := strings.ToLower(hypervHostInterfaceName(name))
			state := verdict[key]
			entry, index := ledger.find(name)
			if index < 0 {
				continue
			}
			if state.ready {
				entry.State = hypervStateReady
				entry.LastError = ""
			} else {
				entry.State = hypervStateFailed
				entry.LastError = fmt.Sprintf("%s：%s", hypervHostInterfaceName(name), hypervDHCPTimeoutHint)
			}
			ledger.Adapters[index] = entry
		}
		return nil
	})
}

// Remove 删除一张由本工具创建的网卡。归属三重判定缺一不可：命名规范 → 台账命中 →
// 删除时 MAC 与系统实况逐字节一致。任一条不成立就拒绝，绝不动用户的网卡。
func (s *HyperVAdapterService) Remove(name string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	target := strings.TrimSpace(name)
	if !isHyperVAdapterName(target) {
		return hypervErrorf(hypervCodeNotManaged, "%q 不符合 HypoMux 命名规范（%sNN），已跳过删除", name, hypervAdapterNamePrefix)
	}
	if !hypervPlatformSupported() {
		return hypervUnsupportedError()
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	ledger, err := s.readLedger()
	if err != nil {
		return err
	}
	entry, index := ledger.find(target)
	if index < 0 {
		return hypervErrorf(hypervCodeNotManaged, "%s 不在 HypoMux 台账中，已跳过删除", target)
	}

	// 审计 M2：查询失败与「网卡确实不存在」是两回事，绝不能混为一谈。
	// readInventory 失败的现实场景不少：策略禁止非提权读 Get-VMNetworkAdapter（72 §7.2
	// 自己就把这条列为未验证风险）、powershell.exe 被策略拦截、结果文件缺失或超过
	// hypervMaxResultBytes。此时若继续往下走，found=false 会跳过整段提权删除，却照样
	// 清台账 + 移出池 ⇒ Hyper-V 对象与宿主接口都还在、台账没了 ⇒ List() 把它当
	// Managed=false 的只读行展示，Remove() 又永远返回 not_managed，用户在 UI 里彻底
	// 删不掉这张卡。方向上是失败安全的（不误删用户网卡），但归属系统不自洽。
	// 这里选择中止并原样保留台账：台账宁可多，不可少。
	inventory, invErr := s.readInventory()
	if invErr != nil {
		return hypervErrorf(hypervCodeUnavailable,
			"查询 Hyper-V 现有网卡失败：%v；已中止删除，台账与出口池保持不变", invErr.Error())
	}
	if live, found := inventory.find(entry.AdapterID, entry.Name); found {
		// 第三重判定。台账 MAC 与系统实况不一致，意味着这张卡被重建或复用过；按旧
		// 记录去删就是在删别人的东西，直接拒绝。
		if !hypervMACEqual(entry.MACAddress, live.MAC) {
			return hypervErrorf(hypervCodeNotManaged,
				"%s 的 MAC（%s）与台账记录（%s）不一致，已跳过删除",
				target, formatHypervMAC(live.MAC), formatHypervMAC(entry.MACAddress))
		}
		if _, err := s.runScript(hypervEnvelope{
			Op: "remove",
			Items: []hypervEnvelopeItem{{
				Name:       entry.Name,
				MAC:        formatHypervMAC(entry.MACAddress),
				AdapterID:  entry.AdapterID,
				SwitchName: entry.SwitchName,
			}},
		}, hypervRemoveScriptTimeout, true); err != nil {
			return err
		}
	}

	// 幂等：走到这里说明 inventory 查询是**成功**的、只是没找到这张卡 —— 用户手工删过，
	// 或上一轮脚本成功但父进程被杀（runScript 返回成功、found=false）。两种情况下清掉
	// 台账与出口池都是对的，不让用户卡在一个永远删不掉的行上。
	if err := s.updateLedger(func(current *hypervLedger) error {
		current.removeEntry(target)
		return nil
	}); err != nil {
		return err
	}
	s.invalidateInventory()
	return s.applyPoolUpdate(nil, []string{hypervHostInterfaceName(target)})
}

// Shutdown 只停自己的后台等待并清理任务目录。刻意不做任何有副作用的动作：不删网卡、
// 不改台账、不动出口池 —— 退出应用绝不能波及用户的系统网络。
func (s *HyperVAdapterService) Shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	removeHypervJobDirectory()
}

// applyPoolUpdate 合并式写出口池：以 settings.Get() 的最新值为基线（reports/vnic/62
// §8），只增删本次涉及的网卡，绝不整体覆盖别人的选择。
//
// 必须走 updateHomeStrategy 而不是 UpdateFields：UpdateFields 按字段白名单更新，写
// selected_adapter_ids 会连带绕过整个 Home 策略校验，权重/模式的约束就失效了。
//
// 而且必须把 current.Strategy 原样回传：UpdateHome(mode, weighted, ids, weights) 的
// 第 5 个参数在 settings.go:413 被硬编码成 ""，scheduling.go:162-168 的
// normalizeSchedulingStrategy("") 会把 Strategy 归一成 weighted/round-robin。
// 那意味着「延迟优先 / 自适应吞吐」的用户只要建删一张虚拟网卡，调度策略就被静默清零
// （独立审计 M1）。仓库内既有正确范例：engine.go:787 与 adaptive_scheduling_test.go:38。
// current.Strategy 为空（老版本 settings.json 没写这个键）时仍按 weighted 回落，
// 与 UpdateHome 的历史行为一致，不引入新的兼容性问题。
func (s *HyperVAdapterService) applyPoolUpdate(add []string, remove []string) error {
	if s == nil || s.settings == nil {
		return errHypervServiceUnavailable
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	current := s.settings.Get()
	selected := make([]string, 0, len(current.SelectedAdapterIDs)+len(add))
	weights := make(map[string]int, len(current.AdapterWeights))
	for key, value := range current.AdapterWeights {
		weights[key] = value
	}
	seen := map[string]bool{}
	for _, item := range current.SelectedAdapterIDs {
		trimmed := strings.TrimSpace(item)
		lower := strings.ToLower(trimmed)
		if trimmed == "" || seen[lower] {
			continue
		}
		if _, drop := findString(remove, trimmed); drop {
			delete(weights, trimmed)
			continue
		}
		seen[lower] = true
		selected = append(selected, trimmed)
	}
	for _, item := range add {
		trimmed := strings.TrimSpace(item)
		lower := strings.ToLower(trimmed)
		if trimmed == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		selected = append(selected, trimmed)
	}
	// 新入选的网卡给默认权重 1（settings.go 的合法区间是 1–100），已有权重的保持不变。
	for _, item := range selected {
		if _, ok := weights[item]; !ok {
			weights[item] = AdapterWeightDefault
		}
	}
	mode := strings.TrimSpace(current.Mode)
	if mode != "proxy" && mode != "tun" {
		mode = DefaultSettings().Mode
	}
	// 出口池与权重已变更时才落盘，避免无谓地重写 settings.json。
	if len(selected) == len(current.SelectedAdapterIDs) {
		same := true
		for index := range selected {
			if !strings.EqualFold(selected[index], current.SelectedAdapterIDs[index]) {
				same = false
				break
			}
		}
		if same {
			changed := false
			for key, value := range weights {
				if current.AdapterWeights[key] != value {
					changed = true
					break
				}
			}
			if !changed {
				return nil
			}
		}
	}
	// 第 5 个参数必须是 current.Strategy：传 "" 会被 normalizeSchedulingStrategy 归一成
	// weighted/round-robin，把用户选的 latency-first / adaptive-throughput 静默清零（审计 M1）。
	if _, err := s.settings.updateHomeStrategy(mode, current.Weighted, selected, weights, current.Strategy); err != nil {
		return hypervErrorf(hypervCodeUnavailable, "更新出口池失败：%v", err.Error())
	}
	return nil
}

// ---------------------------------------------------------------- 提权脚本常量

// hypervUTF16LE 与 hypervPowerShellScript 刻意放在**跨平台**文件里：它们是纯数据与
// 纯编码，不碰任何系统调用，共享层与单测都要用（非 Windows 上也应能对脚本正文做
// 「不得含危险 cmdlet」的静态断言）。执行它们的地方仍然只在 hyperv_adapter_windows.go。

// hypervUTF16LE 手工编码：PowerShell 的 -EncodedCommand 只认 UTF-16LE + base64。
func hypervUTF16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	buffer := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		buffer = append(buffer, byte(unit), byte(unit>>8))
	}
	return buffer
}

// hypervPowerShellScript 是**唯一的常量脚本**。它自己带界、自己幂等，绝不含任何来自
// 用户的数据 —— 所有可变内容都从 $args[0] 的 base64 JSON 读取。
//
// 允许调用的 cmdlet 就这五个（reports/vnic/60 逐条复核过命令面）：
//
//	Import-Module Hyper-V / Get-VMSwitch / Get-VMNetworkAdapter -ManagementOS
//	Add-VMNetworkAdapter -ManagementOS / Remove-VMNetworkAdapter（走对象管道）
//
// 绝不允许出现：Remove-VMSwitch、Set-VMSwitch、New-VMSwitch、Restart-NetAdapter、
// Disable-WindowsOptionalFeature、netcfg，以及任何物理网卡操作。
const hypervPowerShellScript = `
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$resultPath = ''
$state = [pscustomobject]@{
  ok = $false
  code = 'script_failed'
  error = ''
  adapters = @()
  switches = @()
  addresses = @()
  failures = @()
}

function Publish([bool]$ok, [string]$code, [string]$message) {
  $state.ok = $ok
  $state.code = $code
  $state.error = $message
  $json = ConvertTo-Json -InputObject $state -Compress -Depth 8
  $tmp = $resultPath + '.tmp'
  [IO.File]::WriteAllText($tmp, $json, (New-Object Text.UTF8Encoding($false)))
  Move-Item -LiteralPath $tmp -Destination $resultPath -Force
}

function Mac-Normalize([string]$value) {
  if ($null -eq $value) { return '' }
  return ($value -replace '[:\-.\s]', '').ToLowerInvariant()
}

function Read-Envelope() {
  if ($args.Count -lt 1) { return $null }
  try {
    $raw = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([string]$args[0]))
    return ($raw | ConvertFrom-Json)
  } catch {
    return $null
  }
}

$envelope = Read-Envelope
if ($null -eq $envelope) { exit 2 }
$resultPath = [string]$envelope.resultPath
if ([string]::IsNullOrWhiteSpace($resultPath)) { exit 2 }

$items = @()
if ($null -ne $envelope.items) { $items = @($envelope.items) }
$aliases = @()
if ($null -ne $envelope.aliases) { $aliases = @($envelope.aliases) }

$op = [string]$envelope.op

try {
  if ($op -eq 'inventory') {
    Import-Module Hyper-V -ErrorAction Stop
    $adapters = @()
    foreach ($adapter in @(Get-VMNetworkAdapter -ManagementOS -ErrorAction Stop)) {
      $mac = [string]$adapter.MacAddress
      $device = [string]$adapter.DeviceId
      # 幽灵记录：reports/vnic/60 实测本机有 MAC/DeviceId/AdapterId 三空、Status 却
      # 是 Ok 的 xuni-01 残留项。宿主上并没有这张卡，必须过滤掉，否则 List() 会报出
      # 一张永远 creating 的假卡，Remove 也可能命中它。
      if ([string]::IsNullOrWhiteSpace($mac)) { continue }
      if ([string]::IsNullOrWhiteSpace($device)) { continue }
      $adapters += [pscustomobject]@{
        name = [string]$adapter.Name
        adapterId = $device
        mac = $mac
        switchName = [string]$adapter.SwitchName
        status = [string]$adapter.Status
      }
    }
    $state.adapters = $adapters
    $switches = @()
    foreach ($item in @(Get-VMSwitch -ErrorAction Stop)) {
      $uplink = [string]$item.NetAdapterInterfaceDescription
      $nic = [string]$item.NetAdapterName
      if ([string]$item.SwitchType -eq 'External' -and [string]::IsNullOrWhiteSpace($nic)) {
        $probe = @(Get-NetAdapter -Name $uplink -ErrorAction SilentlyContinue)
        if ($probe.Count -gt 0) { $nic = [string]$probe[0].Name }
      }
      $switches += [pscustomobject]@{
        name = [string]$item.Name
        type = [string]$item.SwitchType
        allowManagementOs = [bool]$item.AllowManagementOS
        uplink = $uplink
        netAdapterName = $nic
      }
    }
    $state.switches = $switches
    Publish $true 'ok' ''
  } elseif ($op -eq 'create') {
    Import-Module Hyper-V -ErrorAction Stop
    $switchName = [string]$envelope.switchName
    $targets = @(Get-VMSwitch -Name $switchName -ErrorAction Stop)
    if ($targets.Count -eq 0) { throw ('switch not found: ' + $switchName) }
    if ([string]$targets[0].SwitchType -ne 'External') { throw ('switch is not External: ' + $switchName) }
    if (-not $targets[0].AllowManagementOS) { throw ('switch does not allow the management OS: ' + $switchName) }
    $rows = @()
    $failures = @()
    $halted = $false
    foreach ($item in $items) {
      if ($halted) { break }
      $name = [string]$item.name
      $mac = [string]$item.mac
      $wanted = Mac-Normalize $mac
      # 幂等：已经存在同 MAC 的对象就复用，绝不重复建卡。
      $existing = @(Get-VMNetworkAdapter -ManagementOS -ErrorAction Stop | Where-Object { (Mac-Normalize ([string]$_.MacAddress)) -eq $wanted })
      if ($existing.Count -gt 0) {
        $hit = $existing[0]
        $rows += [pscustomobject]@{
          name = [string]$hit.Name
          adapterId = [string]$hit.DeviceId
          mac = [string]$hit.MacAddress
          switchName = [string]$hit.SwitchName
          status = [string]$hit.Status
        }
        continue
      }
      try {
        $made = Add-VMNetworkAdapter -ManagementOS -SwitchName $switchName -Name $name -StaticMacAddress $mac -PassThru -ErrorAction Stop
        $rows += [pscustomobject]@{
          name = [string]$made.Name
          adapterId = [string]$made.DeviceId
          mac = [string]$made.MacAddress
          switchName = [string]$made.SwitchName
          status = [string]$made.Status
        }
      } catch {
        # 首个硬失败即停本批：已成功的保留，剩余的交给父进程从台账里抹掉。
        $failures += [pscustomobject]@{ name = $name; mac = $mac; error = [string]$_.Exception.Message }
        $halted = $true
      }
    }
    $state.adapters = $rows
    $state.failures = $failures
    Publish $true 'ok' ''
  } elseif ($op -eq 'remove') {
    Import-Module Hyper-V -ErrorAction Stop
    $rows = @()
    $failures = @()
    foreach ($item in $items) {
      $deviceId = [string]$item.adapterId
      $wanted = Mac-Normalize ([string]$item.mac)
      $targets = @()
      if ([string]::IsNullOrWhiteSpace($deviceId)) {
        $targets = @(Get-VMNetworkAdapter -ManagementOS -ErrorAction Stop | Where-Object { (Mac-Normalize ([string]$_.MacAddress)) -eq $wanted })
      } else {
        $targets = @(Get-VMNetworkAdapter -ManagementOS -ErrorAction Stop | Where-Object { ([string]$_.DeviceId) -eq $deviceId })
      }
      if ($targets.Count -eq 0) {
        # 系统里已经没有这张卡（用户手工删过）：按契约记 skipped，父进程照常清台账。
        $rows += [pscustomobject]@{ name = [string]$item.name; adapterId = $deviceId; mac = [string]$item.mac; switchName = [string]$item.switchName; status = 'skipped' }
        continue
      }
      try {
        # 必须走对象管道：Remove-VMNetworkAdapter 的 ResourceObject 参数集不接受
        # -ManagementOS，而 -ManagementOS -Name 会同时命中 MAC 为空的幽灵记录。
        $targets | Remove-VMNetworkAdapter -ErrorAction Stop
        $rows += [pscustomobject]@{ name = [string]$item.name; adapterId = $deviceId; mac = [string]$item.mac; switchName = [string]$item.switchName; status = 'removed' }
      } catch {
        $failures += [pscustomobject]@{ name = [string]$item.name; mac = [string]$item.mac; error = [string]$_.Exception.Message }
      }
    }
    $state.adapters = $rows
    $state.failures = $failures
    Publish $true 'ok' ''
  } elseif ($op -eq 'waitip') {
    $seconds = 45
    if ($null -ne $envelope.timeoutSeconds) { $seconds = [int]$envelope.timeoutSeconds }
    if ($seconds -lt 1) { $seconds = 1 }
    if ($seconds -gt 120) { $seconds = 120 }
    $deadline = (Get-Date).AddSeconds($seconds)
    $rows = @()
    do {
      $rows = @()
      foreach ($alias in $aliases) {
        # APIPA 169.254/16 一律不算就绪（与 adapters.go:66-69 同规则）。
        $found = @(Get-NetIPAddress -InterfaceAlias $alias -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { -not ( ([string]$_.IPAddress).StartsWith('169.254.') ) })
        if ($found.Count -eq 0) {
          $rows += [pscustomobject]@{ alias = $alias; address = ''; prefixLength = 0; prefixOrigin = ''; addressState = '' }
        } else {
          foreach ($hit in $found) {
            $rows += [pscustomobject]@{
              alias = $alias
              address = [string]$hit.IPAddress
              prefixLength = [int]$hit.PrefixLength
              prefixOrigin = [string]$hit.PrefixOrigin
              addressState = [string]$hit.AddressState
            }
          }
        }
      }
      $pending = 0
      foreach ($alias in $aliases) {
        # 成功判据必须是 PrefixOrigin=Dhcp 且 AddressState=Preferred：reports/vnic/60
        # 实测 Duplicate 状态的地址虽然存在，但这个网卡不能用来出口。
        $ready = @($rows | Where-Object { ([string]$_.alias) -eq $alias -and ([string]$_.prefixOrigin) -eq 'Dhcp' -and ([string]$_.addressState) -eq 'Preferred' })
        if ($ready.Count -eq 0) { $pending = $pending + 1 }
      }
      if ($pending -eq 0) { break }
      Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    $state.addresses = $rows
    Publish $true 'ok' ''
  } else {
    exit 3
  }
} catch {
  try { Publish $false 'script_failed' ([string]$_.Exception.Message) } catch { }
  exit 1
}
exit 0
`
