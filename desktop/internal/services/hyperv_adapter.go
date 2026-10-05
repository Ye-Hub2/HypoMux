package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

	// hypervRemoveBatchTimeoutMax 是**批量**删除的父进程超时上限。
	//
	// 不能沿用单张的 hypervRemoveScriptTimeout：脚本按 items 逐张串行执行，每张都要
	// 一次 Get-VMNetworkAdapter + 一次 Remove-VMNetworkAdapter，N 张的耗时是 N 倍。
	// 所以真实上限按 min(单张超时 × 张数, 本值) 放大（见 hypervRemoveBatchTimeout）。
	//
	// 但**必须封顶**：提权子进程杀不掉（见 runScript 的超时语义），父进程放弃等待后
	// 脚本仍可能在后台继续删。没有上限的话，用户一次勾选 20 张卡就能让这一轮挂住
	// 30 分钟，而期间 UI 完全没有反馈。180s 是「绝大多数机器删十几张绰绰有余」与
	// 「用户等不到失去耐心」之间的折中；超时后结果里每张都记失败，引擎有 watchdog 自愈。
	hypervRemoveBatchTimeoutMax = 180 * time.Second

	// 保护名单。三个前缀/全等值都是 Hyper-V 自己管的对象，删掉会直接打断宿主的
	// 网络基础设施，不是「本工具该不该管」的问题，而是「Hyper-V 会不会崩」的问题。
	//
	//	container nic 前缀 —— Windows 容器 / Docker Desktop 为每个容器网络合成一张。
	//	    删掉正在跑的容器立刻断网，容器编排随之崩溃。
	//	vEthernet (Default Switch) —— Hyper-V 自管 Internal 交换机在宿主上的接口。
	//	    它是所有未显式绑定交换机的虚拟机的默认网关，删掉 = 默认交换机整体失效。
	hypervContainerNICPrefix  = "container nic"
	hypervDefaultSwitchName   = "default switch"
	hypervHostInterfacePrefix = "vEthernet ("
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

// LastError 上承载的东西分两类，**必须**用机器可读前缀区分，否则前端没法本地化：
//
//  1. 标记（以 hypervHintCodePrefix 开头）：后端能确定语义的固定事实，交给前端查语言包。
//     en locale 曾全程显示中文，就是因为这里下发的是中文原文（reports/vnic/79 §A）。
//  2. 非标记的自由文本：只有后端才知道的运行时细节（PowerShell 退出码、驱动报错），
//     前端无从翻译，原样显示才是对的。
//
// hypervHintDetailSeparator 分隔标记与随附细节：左半段查语言包，右半段原样显示。
const (
	hypervHintCodePrefix       = "hypomux.hint."
	hypervHintDetailSeparator  = " | "
	hypervHintPoolRestart      = hypervHintCodePrefix + "pool_restart"
	hypervHintPoolUpdateFailed = hypervHintCodePrefix + "pool_update_failed"
)

// hypervDHCPTimeoutHint 是纯自由文本：它陈述的是真实运行时事实（DHCP 没就绪），
// 前端无从翻译，原样显示。
const hypervDHCPTimeoutHint = "等待 60 秒仍未从交换机拿到可用 IPv4（DHCP 未就绪）；网卡已保留，可稍后重试或直接删除"

// hypervHintWithDetail 拼出「机器可读标记 + 只有后端才知道的运行时细节」。
func hypervHintWithDetail(code, detail string) string {
	trimmed := strings.TrimSpace(detail)
	if trimmed == "" {
		return code
	}
	return code + hypervHintDetailSeparator + trimmed
}

// hypervHintCode 取出 LastError 的机器可读标记；不是标记就返回 ""。
func hypervHintCode(lastError string) string {
	trimmed := strings.TrimSpace(lastError)
	if !strings.HasPrefix(trimmed, hypervHintCodePrefix) {
		return ""
	}
	if index := strings.Index(trimmed, hypervHintDetailSeparator); index >= 0 {
		trimmed = trimmed[:index]
	}
	return trimmed
}

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

// HyperVRemoveResult 报告一次批量删除里**单个目标**的结果。
//
// 批量删除刻意不「一荣俱荣一损俱损」：一批 5 张里第 3 张失败，另外 4 张必须照删。
// 因此成败下沉到每张卡上，整批只把「连脚本都没能跑起来」这类致命问题当 error 返回。
type HyperVRemoveResult struct {
	Name      string `json:"name"`
	Removed   bool   `json:"removed"`
	Reason    string `json:"reason"`    // 非空 = 未删除的原因（面向用户的中文，直接展示）
	Interface string `json:"interface"` // 宿主网卡别名，如 vEthernet (xuni-01)
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
	// scriptHook 仅供单测注入「提权脚本」的假执行，生产恒为 nil。批量删除要在不碰
	// 真实 Hyper-V 的前提下断言「N 张卡只弹一次 UAC」，没有这个钩子就只能在真机上验证。
	scriptHook func(hypervEnvelope, time.Duration, bool) (*hypervScriptResult, error)
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

// hypervMACCounterMax 是 MAC 计数器的上界：hypervMACValue 只渲染 6 位十六进制，
// 超过 0xFFFFFF 就会绕回、重新发出已经用过的 MAC。
const hypervMACCounterMax = 0xFFFFFF

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

// hypervFirstAdapterSeq 是首张卡的编号。冻结契约（reports/vnic/60 §编号分配）规定编号
// 取 01–99，即 index = max(已记录 index) + 1。hypervLedger 的 NextSeq 零值是 0，
// 直接发号会得到 `HypoMux-vnic-00` —— 真机端到端实测到过（reports/vnic/76 §15），
// 与契约不符，这里统一抬到 1。
const hypervFirstAdapterSeq = 1

// normalize 抬高零值计数器。序号只增不减：本方法只把「从没发过号」的 0 抬到起始值，
// 绝不回收已经发出去的号；已经被 v2.7.0 发出过的 `-00` 仍由 Create 的重名探测跳过。
func (l *hypervLedger) normalize() {
	if l.NextSeq < hypervFirstAdapterSeq {
		l.NextSeq = hypervFirstAdapterSeq
	}
}

// loadHypervLedger 读台账。文件不存在是正常的首启状态，返回空台账而不是错误 ——
// 「没有记录」与「读不出来」的处置完全不同，前者照常跑，后者必须让 List() 报错。
func loadHypervLedger() (hypervLedger, error) {
	ledger := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
	ledger.normalize()
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
		fresh := hypervLedger{Version: hypervLedgerVersion, Adapters: []hypervLedgerEntry{}}
		fresh.normalize()
		return fresh,
			hypervErrorf(hypervCodeLedger, "Hyper-V 台账已损坏，请检查 %s：%v", hypervLedgerPath(), err)
	}
	if ledger.Version == 0 {
		ledger.Version = hypervLedgerVersion
	}
	if ledger.Adapters == nil {
		ledger.Adapters = []hypervLedgerEntry{}
	}
	ledger.normalize()
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
	// 发号是唯一入口，在这里兜底：即便调用方直接构造零值台账（单测、未来新调用点），
	// 也绝不会发出 `-00`。loadHypervLedger 已经抬过一次，这里是幂等的第二道闸。
	l.normalize()
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

// hypervPoolKey 把出口池的键统一成宿主别名。冻结契约 reports/vnic/70 §HyperVAdapterStatus
// 写明 InterfaceName（vEthernet (HypoMux-vnic-01)）才是出口池的键，但两个调用方给的
// 形式并不一致：Create 侧的 awaitInterfaces 返回 Hyper-V 对象名，Remove 侧传的是
// 已经拼好的别名。后果是真机端到端实测到的（reports/vnic/76 §16）：
//  1. 卡进了池但键是裸名，List() 认不出来（inPool 永远 false）；
//  2. 引擎按别名去绑定会找不到网卡；
//  3. Remove 按别名去删，删不到 Create 写进去的裸名 ⇒ settings.json 里留下永久悬空键。
//
// 在唯一的收口处归一，两个方向就对称了。
func hypervPoolKey(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if _, ok := hypervObjectNameFromInterface(trimmed); ok {
		return trimmed // 已经是宿主别名
	}
	if strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(hypervAdapterNamePrefix)) {
		return hypervHostInterfaceName(trimmed)
	}
	return trimmed // 以太网 这类真实网卡名原样保留
}

// hypervObjectNameFromInterface 反解 vEthernet (HypoMux-vnic-01) -> HypoMux-vnic-01。
// 非本命名规范一律拒绝，绝不把用户自己的 vEthernet 网卡带进归属判定。
// hypervObjectNameOrSelf 把 vEthernet (HypoMux-vnic-01) 还原成 HypoMux-vnic-01，
// 本来就是对象名就原样返回。awaitAddresses 同时要按别名查 DHCP、按对象名查台账，
// 两边都得拿到对的形式；调用方传哪种都不能让 hypervHostInterfaceName 再包一层。
func hypervObjectNameOrSelf(nameOrAlias string) string {
	if name, ok := hypervObjectNameFromInterface(nameOrAlias); ok {
		return name
	}
	return strings.TrimSpace(nameOrAlias)
}

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

// hasNameWithoutMAC 报「同名但 MAC 为空」的幽灵记录是否存在。存在它说明这张卡在
// Get-VMNetworkAdapter 里确实出现过，只是拿不到可定位的 MAC —— 与「压根没有这张卡」
// 是两回事，用户要看到的提示也必须不一样。
func (i hypervInventory) hasNameWithoutMAC(name string) bool {
	trimmed := strings.TrimSpace(name)
	for _, item := range i.Adapters {
		if strings.EqualFold(strings.TrimSpace(item.Name), trimmed) && normalizeHypervMAC(item.MAC) == "" {
			return true
		}
	}
	return false
}

// hasMAC 判宿主上是否已经存在这张 MAC 的网卡。MAC 不是「重复就复用」的软约束，而是
// -StaticMacAddress 的硬约束：撞车时 Add-VMNetworkAdapter 直接失败，整批一张都建不出来。
func (i hypervInventory) hasMAC(mac string) bool {
	target := normalizeHypervMAC(mac)
	if target == "" {
		return false
	}
	for _, item := range i.Adapters {
		if normalizeHypervMAC(item.MAC) == target {
			return true
		}
	}
	return false
}

// hypervSkipOccupiedMAC 从 start 起返回第一个「连续 count 个都未被占用」的 MAC 计数器；
// 找不到这样的连续段时返回 -1。
//
// 必须要求**连续**而不是单个：allocateNames 是按 NextMAC, NextMAC+1, … 递增发号的，
// 占用集合里只要有一处空洞（比如 4 空闲、5 被占），只跳过单个就会让本批第 2 张正好落在
// 5 上，等于把 bug 从发号挪到了第 2 张。
//
// 台账的 NextMAC 只增不减，**但台账本身会丢**：重装、换机、手删 adapters.json，都会让它
// 从 0 重新开始，而宿主上上一轮建出来的 HypoMux 卡还在（网卡在、台账没了）。这时发出的
// 第一个 MAC 会和遗留卡逐字节撞车，用户看到的是「第一次创建就打不出一张卡」。真机复现
// 见 reports/vnic/79-e2e-regression.md 的 D1：名字有 inventory.hasName 探测，MAC 原本
// 一道都没有。抽成纯函数是为了能在不触碰真实 Hyper-V 的前提下覆盖这条路径。
func hypervSkipOccupiedMAC(start int, count int, occupied map[string]bool) int {
	if count < 1 {
		return -1
	}
	if start < 0 {
		start = 0
	}
	for value := start; value <= hypervMACCounterMax; value++ {
		free := 0
		for offset := 0; offset < count; offset++ {
			candidate := value + offset
			if candidate > hypervMACCounterMax || occupied[normalizeHypervMAC(hypervMACValue(candidate))] {
				break
			}
			free++
		}
		if free == count {
			return value
		}
	}
	return -1
}

// hypervOccupiedMACs 汇总「这个 MAC 已经被占了」的所有来源。两个来源都不可省：
// 宿主实况覆盖台账丢失后残留的孤儿卡，台账覆盖正在创建中、宿主还没回报的预留条目。
func hypervOccupiedMACs(inventory hypervInventory, ledger *hypervLedger) map[string]bool {
	occupied := make(map[string]bool, len(inventory.Adapters)+len(ledger.Adapters))
	for _, item := range inventory.Adapters {
		if mac := normalizeHypervMAC(item.MAC); mac != "" {
			occupied[mac] = true
		}
	}
	for _, entry := range ledger.Adapters {
		if mac := normalizeHypervMAC(entry.MACAddress); mac != "" {
			occupied[mac] = true
		}
	}
	return occupied
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
	} else if result, err := s.readHypervInventory(); err != nil {
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

// scriptOverride 是**仅供测试注入**的钩子，用来在不弹 UAC、不动真实网卡的前提下断言
// 「一次批量删除只调一次脚本」与「被保护的目标不进 Items」。生产路径永远是 nil。
func (s *HyperVAdapterService) scriptOverride() func(hypervEnvelope, time.Duration, bool) (*hypervScriptResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scriptHook
}

// readHypervInventory 是读取路径的入口：一条常量 powershell 命令 + stdout JSON。
//
// 与 runScript 的关系是**刻意分开的**：runScript 走的是「常量脚本 + base64 注入可变
// 数据 + 结果文件 + 原子提交」这套给提权写操作用的重机制。读取路径没有可变数据，也就
// 没有理由背这套重机制 —— reports/vnic/76 记录的「虚拟网卡页读取 Hyper-V 失败」正是
// 出在这条重链路上（详见该报告）。写路径继续原样使用 runScript。
func (s *HyperVAdapterService) readHypervInventory() (*hypervScriptResult, error) {
	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, hypervReadScriptTimeout)
	defer cancel()

	result, err := hypervInventorySnapshot(ctx, hypervReadScriptTimeout)
	if err != nil {
		return nil, mapHypervRunError(err, false, "inventory")
	}
	if !result.OK {
		detail := strings.TrimSpace(result.Error)
		if detail == "" {
			detail = "结果标记为失败但未给出原因"
		}
		return nil, hypervErrorf(hypervCodeScriptFailed, "读取 Hyper-V 状态失败：%s", detail)
	}
	return result, nil
}

// runScript 是所有**写/等待**脚本调用的唯一入口。elevated=true 才会触发 UAC，只有
// Create、Remove 与 awaitAddresses 的 waitip 段传 true/false，见各调用点。
//
// 超时语义（§3.3）：提权子进程杀不掉，所以超时只是「放弃等待」，绝不改台账状态。
// 脚本本身按 op 自带界且幂等，重试安全。
//
// 注意返回值组合：**结果文件缺失时 result 为 nil**，此时调用方无法确知脚本到底做了什么，
// 必须按「不可知」处理（见 hypervScriptOutcomeUncertain）；**结果文件存在但 ok=false 时
// result 非 nil**，脚本跑完了并逐条交代了成败，可以放心按它的说法 reconcile。
func (s *HyperVAdapterService) runScript(envelope hypervEnvelope, timeout time.Duration, elevated bool) (*hypervScriptResult, error) {
	if override := s.scriptOverride(); override != nil {
		return override(envelope, timeout, elevated)
	}
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
	// 台账外的卡：只读展示（Managed=false ⇒ Remove 直接拒绝）。这是绝误删用户自己
	// 网卡的最后一道闸门，也是 §3.4「未登记的一律跳过」的可见化。
	//
	// 这里刻意**不**用 isHyperVAdapterName 过滤：用户自己手工建的 vNIC（xuni-01 之类）
	// 同样需要在这个页面看得见——否则「Hyper-V 虚拟网卡」页对既有用户是空的，他们只能
	// 回到首页去猜那张卡到底在不在。可见性不是纳管：命名不合规的卡在 Remove() 里仍被
	// :1650 的 not_managed 挡住，台账外的一律被 :1665 挡住，两道闸门都不依赖这个过滤。
	for _, adapter := range inventory.Adapters {
		if !hypervShowUnmanagedAdapter(adapter.Name, claimed) {
			continue
		}
		rows = append(rows, buildUnmanagedHyperVStatus(adapter, hosts, pool))
	}
	sortHyperVStatuses(rows)
	return rows, nil
}

// hypervShowUnmanagedAdapter 判定某张宿主 Hyper-V 网卡是否要作为「未纳管只读行」展示。
//
// **不要**在这里加 isHyperVAdapterName 过滤：看上去它能多挡一层误删，实际是零边际收益
// —— Remove() 已经有两道独立的闸门（:1650 命名规范、:1665 台账归属），两道都不看这个
// 谓词。代价却是把用户自己建的卡从页面上抹掉，导致这个页面只对「本工具建的卡」有意义。
// 本函数只回答「可见性」，不回答「可删除性」。
func hypervShowUnmanagedAdapter(name string, claimed map[string]bool) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	return !claimed[strings.ToLower(trimmed)]
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
		message := hypervFailureMessage(failures, entry.Name, runErr)
		entry.State = hypervStateFailed
		entry.LastError = fmt.Sprintf("第 %d/%d 张创建失败：%s", index+1, len(reserved), message)
		outcome.finished = append(outcome.finished, entry)
		failure = &HyperVError{Code: hypervCodeCreateFailed, Message: entry.LastError, Detail: message}
	}
	outcome.failure = failure
	return outcome
}

// hypervFailureMessage 取出这一条失败的可行动原因。
//
// 名字匹配是首选——一条批次里哪张卡、什么原因，一一对应。但**不能只有它**：脚本侧
// Add-VMNetworkAdapter 抛错时未必能把失败归到某个网卡名上（真机上就出现过名字对不上的
// 情况），这时若只认名字匹配，用户拿到的就是一句「未知原因」，等于没有原因，见
// reports/vnic/79-e2e-regression.md 的 D2。退而求其次也必须把脚本原文吐出来。
//
// 兜底顺序：名字精确匹配 → 单条无归属失败 → 多条拼接 → runErr → 「未知原因」。
func hypervFailureMessage(failures []hypervScriptFailure, name string, runErr error) string {
	var unassigned []string
	for _, item := range failures {
		text := strings.TrimSpace(item.Error)
		if text == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Name), name) {
			return text
		}
		unassigned = append(unassigned, text)
	}
	if len(unassigned) == 1 {
		return unassigned[0]
	}
	if len(unassigned) > 1 {
		return strings.Join(unassigned, "；")
	}
	if runErr != nil {
		return runErr.Error()
	}
	return "未知原因"
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
		// MAC 同理，而且是必须有的一道：台账丢了但宿主还留着上一轮的卡时，NextMAC 会从
		// 0 重新开始，发出的第一个 MAC 就逐字节撞车，Add-VMNetworkAdapter 整批失败
		// （reports/vnic/79 D1 真机复现）。名字能靠 hasName 探测，MAC 没有第二张表可查，
		// 只能靠宿主实况 + 台账两条来源现算。
		freeMAC := hypervSkipOccupiedMAC(ledger.NextMAC, count, hypervOccupiedMACs(inventory, ledger))
		if freeMAC < 0 {
			return hypervErrorf(hypervCodeUnavailable,
				"没有连续的 %d 个可用 MAC 计数器（从 %d 起算），请稍后重试", count, ledger.NextMAC)
		}
		ledger.NextMAC = freeMAC
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
			row.LastError = hypervHintPoolRestart
		}
		rows = append(rows, row)
	}
	return rows, hypervFailureError(outcome.failure)
}

// hypervFailureError 把可能为 nil 的 *HyperVError 转成真正的 error。直接写
// `return x`（x 是 *HyperVError）会把 nil 指针装箱成非 nil 接口：调用方看到
// err != nil 成立，而 HyperVError.Error() 对 nil 接收者返回空串 —— 表现为「卡建好了
// 却弹一个没有文字的错误」。真机端到端实测到过（reports/vnic/76 §15）。
func hypervFailureError(failure *HyperVError) error {
	if failure == nil {
		return nil
	}
	return failure
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
			//
			// 原来这里是 `_ = s.applyPoolUpdate(...)`，错误整个被丢掉（审计 M2）。后果：
			// 网卡状态「已就绪」、出口池徽章在、用户重启聚合后新卡却完全不生效，而全程
			// 零根因 —— 用户没有任何线索知道该去查哪里。现在把成败写回台账 LastError，
			// 前端每行已有的 role="alert" 直接显示它。
			if err := s.applyPoolUpdate(appeared, nil); err != nil {
				s.recordPoolHint(appeared, hypervHintWithDetail(hypervHintPoolUpdateFailed, err.Error()))
			} else {
				// 成功即自愈：清掉上一轮遗留的「没进出口池」标记，否则它会永久粘住。
				s.recordPoolHint(appeared, "")
			}
		}
		s.awaitAddresses(appeared)
	}()
}

// hypervLastErrorAfterReady 决定 creating→ready 这一跃迁时 LastError 该留下什么。
//
// 抽成纯函数是为了能脱离 Hyper-V 单测：awaitAddresses 里 awaitBatch 的
// applyPoolUpdate 之后才跑，无脑清空会把刚写进去的「没进出口池」标记抹掉 ——
// 那正是审计 M2 说的「用户全程零根因」。
func hypervLastErrorAfterReady(lastError string) string {
	if hypervHintCode(lastError) == hypervHintPoolUpdateFailed {
		return lastError
	}
	return ""
}

// recordPoolHint 把「这些卡的出口池写入结果」写回台账。lastError 非空时逐字写入；
// 为空时**只**清掉池写入失败标记，不碰其他任何来源的 LastError（创建失败、DHCP 超时
// 都不能被出口池的结果顺带抹掉）。
func (s *HyperVAdapterService) recordPoolHint(names []string, lastError string) {
	if s == nil || len(names) == 0 {
		return
	}
	keys := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	if lastError == "" {
		// 清除路径先只读确认真有标记，否则每次成功创建都要无谓地重写一次台账文件。
		// 这里退化成一次冗余写入是最坏情况，不会写错东西。
		ledger, err := s.readLedger()
		if err != nil {
			return
		}
		needed := false
		for _, key := range keys {
			if entry, index := ledger.find(key); index >= 0 &&
				hypervHintCode(entry.LastError) == hypervHintPoolUpdateFailed {
				needed = true
				break
			}
		}
		if !needed {
			return
		}
	}
	_ = s.updateLedger(func(ledger *hypervLedger) error {
		for _, key := range keys {
			entry, index := ledger.find(key)
			if index < 0 {
				continue
			}
			if lastError == "" {
				if hypervHintCode(entry.LastError) == hypervHintPoolUpdateFailed {
					entry.LastError = ""
					ledger.Adapters[index] = entry
				}
				continue
			}
			entry.LastError = lastError
			ledger.Adapters[index] = entry
		}
		return nil
	})
	// 台账本身写不进去时没有第二个上报通道，放弃：这条路径上的失败不该反过来
	// 打断 awaitBatch（网卡已经是好的，不能因为提示写不进去就把它标成 failed）。
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
		aliases = append(aliases, hypervHostInterfaceName(hypervObjectNameOrSelf(name)))
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
		// 按下标配对：aliases[i] 是 names[i] 的宿主别名，verdict 按别名索引，
		// 台账按对象名索引，两边都得拿到对的形式。
		//
		// 这里曾把名字包出双层串 "vEthernet (vEthernet (HypoMux-vnic-01))"：verdict 查不到
		// → ready 恒 false；ledger.find(别名) 在只存对象名的台账里恒返回 -1 → continue。
		// 两者叠加，台账 state 从头到尾没人改写过，卡永远停在 creating（真机端到端实测到，
		// reports/vnic/76 §16）。UI 之所以看着正常，只是 List() 会从宿主实况自愈出 ready，
		// 把这个坏状态盖住了。现在两侧都用 hypervObjectNameOrSelf / aliases 各自的正确形式，
		// 调用方传对象名还是别名都不会再包出双层串。
		for i, alias := range aliases {
			name := hypervObjectNameOrSelf(names[i])
			state := verdict[strings.ToLower(alias)]
			entry, index := ledger.find(name)
			if index < 0 {
				continue
			}
			if state.ready {
				entry.State = hypervStateReady
				// 「没进出口池」的标记必须留下：awaitAddresses 跑在 applyPoolUpdate
				// **之后**（awaitBatch 的顺序），无脑清空会把它抹掉。
				entry.LastError = hypervLastErrorAfterReady(entry.LastError)
			} else {
				entry.State = hypervStateFailed
				// 这里反过来让 DHCP 失败覆盖池标记：拿不到地址的卡整体就是坏的，
				// 「没进出口池」对它没有独立意义。
				entry.LastError = fmt.Sprintf("%s：%s", alias, hypervDHCPTimeoutHint)
			}
			ledger.Adapters[index] = entry
		}
		return nil
	})
}

// hypervRemoveRow 是删除流程里「一张目标卡」的全部中间状态。删掉一张卡要经过
// 保护名单 → 台账归属 → 系统实况确认 → 提权脚本 → 清台账/清池五步，每一步都可能
// 把它拦下来。把这些状态收进一个结构体，是为了让「批量」与「单张」共用同一条流水线
// 而不必在两处各写一遍判定。
type hypervRemoveRow struct {
	name    string
	alias   string // 宿主网卡别名，出口池的键
	managed bool   // 在台账里 ⇒ 本工具创建，删除成功后才清台账
	entry   hypervLedgerEntry
	item    hypervEnvelopeItem
	// cleanupOnly = 系统实况里已经没有这张卡（用户手工删过，或上一轮脚本成功但父进程
	// 被杀）。此时不重复进提权脚本，但仍要清台账与出口池 —— 幂等。
	cleanupOnly bool
	// reason 非空 = 被拦下，没删；这是最终展示给用户的那句话。
	reason string
	// scripted = 这一行进了提权脚本（cleanupOnly 的行不进）。
	scripted bool
	removed  bool
}

// normalizeHypervRemoveNames 归一化批量删除的输入：trim、丢空串、按小写去重、保序。
//
// 去重按小写走：Windows 的名字不区分大小写，"Xuni-01" 与 "xuni-01" 是同一张卡，
// 不去重就会把同一张卡塞进同一个 Items 两次，脚本第二遍必然报「找不到」失败。
// 同时把 vEthernet (...) 形式的宿主别名还原成 Hyper-V 对象名，让两个调用方传哪种都能删。
func normalizeHypervRemoveNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, item := range names {
		trimmed := hypervUnwrapHostInterface(strings.TrimSpace(item))
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, trimmed)
	}
	return out
}

// hypervUnwrapHostInterface 把 vEthernet (xuni-01) 还原成 xuni-01。非该形式原样返回。
// 注意它与 hypervObjectNameFromInterface 的区别：后者只认 HypoMux 命名规范的别名，
// 而这里要处理**任意**虚拟网卡名，因为删除入口现在对台账外的卡也开放。
func hypervUnwrapHostInterface(name string) string {
	trimmed := strings.TrimSpace(name)
	if len(trimmed) <= len(hypervHostInterfacePrefix) {
		return trimmed
	}
	if !strings.EqualFold(trimmed[:len(hypervHostInterfacePrefix)], hypervHostInterfacePrefix) {
		return trimmed
	}
	if !strings.HasSuffix(trimmed, ")") {
		return trimmed
	}
	return strings.TrimSpace(trimmed[len(hypervHostInterfacePrefix) : len(trimmed)-1])
}

// hypervProtectedReason 判定一张卡是否在**硬保护名单**上，返回可直接展示给用户的
// 中文原因；空串表示不在名单上。
//
// 保护名单与「归属判定」是两回事：前者是 Hyper-V 自己的基础设施，删了会打断宿主的
// 容器网络 / 默认交换机；后者只是「本工具该不该管」。放开后者不影响前者 —— 无论这张卡
// 在不在台账、名字合不合规范，都不删。
//
// 实现成纯函数（不碰 s、不碰系统）就是为了能直接单测；批量路径在读台账、读 inventory、
// 拼提权脚本**之前**调用它，因此受保护的目标连脚本都进不去。
func hypervProtectedReason(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "网卡名为空，无法确认要删的是哪一张，已跳过删除"
	}
	lower := strings.ToLower(trimmed)
	// 输入可能已经带 vEthernet (...) 外壳（如用户从池里直接选），两种形态都判。
	unwrapped := strings.ToLower(hypervUnwrapHostInterface(trimmed))
	if strings.HasPrefix(lower, hypervContainerNICPrefix) || strings.HasPrefix(unwrapped, hypervContainerNICPrefix) {
		return "这是 Windows 容器 / Docker 使用的合成网卡（Container NIC），删除会直接中断容器网络，已跳过删除"
	}
	if strings.EqualFold(trimmed, hypervHostInterfaceName(hypervDefaultSwitchName)) ||
		strings.EqualFold(unwrapped, hypervDefaultSwitchName) {
		return "这是 Hyper-V 默认交换机（Default Switch）在宿主上的接口，删除会断开所有未绑定交换机的虚拟机网络，已跳过删除"
	}
	return ""
}

// hypervProtectedAdapter 是保护名单的布尔形式，供不需要文案的调用点使用。
func hypervProtectedAdapter(name string) bool {
	return hypervProtectedReason(name) != ""
}

// hypervRemoveBatchTimeout 给出这一批的父进程超时：按张数线性放大，再封顶。
// 封顶理由见 hypervRemoveBatchTimeoutMax。
func hypervRemoveBatchTimeout(items int) time.Duration {
	if items < 1 {
		items = 1
	}
	scaled := hypervRemoveScriptTimeout * time.Duration(items)
	if scaled > hypervRemoveBatchTimeoutMax {
		return hypervRemoveBatchTimeoutMax
	}
	return scaled
}

// hypervMACMismatchReason 是第三重闸门的唯一文案来源。Remove() 的错误与
// RemoveAdapters() 的跳过原因必须逐字一致，否则同一个原因在两条路径上会显示成
// 两句话，用户会以为是两个不同的问题。
func hypervMACMismatchReason(target string, liveMAC string, ledgerMAC string) string {
	return fmt.Sprintf("%s 的 MAC（%s）与台账记录（%s）不一致，已跳过删除",
		target, formatHypervMAC(liveMAC), formatHypervMAC(ledgerMAC))
}

// hypervFailureByName 在脚本回报的失败里找有没有指名道姓地提到这张卡。
func hypervFailureByName(failures []hypervScriptFailure, name string) (hypervScriptFailure, bool) {
	for _, item := range failures {
		if strings.EqualFold(strings.TrimSpace(item.Name), name) {
			return item, true
		}
	}
	return hypervScriptFailure{}, false
}

// hypervFailuresNamed 判断失败列表里**至少有一条**带了名字。带名字才谈得上按名字归属；
// 全是匿名的（reports/vnic/79 §D2 实测过 Add 侧出现过）对不上任何一张卡，只能整体归因。
func hypervFailuresNamed(failures []hypervScriptFailure) bool {
	for _, item := range failures {
		if strings.TrimSpace(item.Name) != "" {
			return true
		}
	}
	return false
}

// RemoveAdapters 批量删除虚拟网卡，**不要求是本工具创建的**。
//
// 页面上能被 List() 看到的每张 Hyper-V 虚拟网卡都从这里删：命名规范与台账归属这两道
// 闸门对批量入口**不再生效**（这正是用户要的），换上来的是三道新约束：
//
//  1. 保护名单（hypervProtectedReason）：容器合成网卡与 Default Switch 接口永不删除。
//  2. 台账内的卡仍受第三重闸门约束（MAC 逐字节一致）—— 这道闸门**不能**一起推翻：
//     它挡的是「这张卡已经被重建或复用过」，按旧记录去删就是在删别人的东西。
//  3. 台账外的卡必须在系统实况里此刻确实存在，且 MAC 非空（幽灵记录不可删）。
//
// 整批只弹一次 UAC：所有通过判定的卡打进同一个 envelope 走一次提权脚本
// （脚本的 items 本来就是批量语义，Create 的批量创建就是这么用的）。
//
// 错误语义：**单张失败只体现在结果里，不冒泡成 error**。只有「连脚本都跑不起来」
// 这类整批级问题（服务已关闭、平台不支持、inventory 读取失败、台账/出口池落盘失败）
// 才返回 error。一张失败、其余照删，是这个 API 的核心承诺。
func (s *HyperVAdapterService) RemoveAdapters(names []string) ([]HyperVRemoveResult, error) {
	return s.removeAdapters(names, false)
}

// Remove 删除一张由本工具创建的网卡。归属三重判定缺一不可：命名规范 → 台账命中 →
// 删除时 MAC 与系统实况逐字节一致。任一条不成立就拒绝，绝不动用户的网卡。
//
// requireLedger=true 让本函数走与批量入口**完全相同**的流水线，但把每一处
// 「跳过」都还原成原来那个 error —— 错误码、错误文案、返回形态一个字节都没变。
// 批量入口要的是这些信息放进 Result.Reason 供逐条展示，单张入口要的是立刻返回，
// 两条路径共用判定逻辑，只在「怎么表达失败」这一处分叉。
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
	if _, err := s.removeAdapters([]string{target}, true); err != nil {
		return err
	}
	return nil
}

// removeAdapters 是删除的共同核心。
//
// requireLedger=false ⇒ RemoveAdapters：放开命名规范与台账两道闸门，逐条给原因。
// requireLedger=true  ⇒ Remove：任何一条被拦下都变成 error（语义见上）。
func (s *HyperVAdapterService) removeAdapters(names []string, requireLedger bool) ([]HyperVRemoveResult, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !hypervPlatformSupported() {
		return nil, hypervUnsupportedError()
	}
	// 归一化后为空是**正常**状态：用户重复点删除、或一批全被保护名单拦下，都不该报错。
	if len(normalizeHypervRemoveNames(names)) == 0 {
		return nil, nil
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	ledger, err := s.readLedger()
	if err != nil {
		return nil, err
	}

	targets := normalizeHypervRemoveNames(names)
	rows := make([]hypervRemoveRow, 0, len(targets))
	needInventory := false
	for _, target := range targets {
		row := hypervRemoveRow{name: target, alias: hypervHostInterfaceName(target)}
		// 保护名单第一道，且在读台账 / 读 inventory 之前：受保护的目标连脚本都进不去。
		if reason := hypervProtectedReason(target); reason != "" {
			row.reason = reason
			rows = append(rows, row)
			continue
		}
		entry, index := ledger.find(target)
		if requireLedger && index < 0 {
			return nil, hypervErrorf(hypervCodeNotManaged, "%s 不在 HypoMux 台账中，已跳过删除", target)
		}
		row.managed = index >= 0
		row.entry = entry
		needInventory = true
		rows = append(rows, row)
	}
	// 整批都被保护名单拦下时，压根不需要问系统一次 —— 不弹 PowerShell、不读台账以外的东西。
	if !needInventory {
		return hypervRemoveResults(rows), nil
	}

	// 审计 M2：查询失败与「网卡确实不存在」是两回事，绝不能混为一谈。
	// readInventory 失败的现实场景不少：策略禁止非提权读 Get-VMNetworkAdapter（72 §7.2
	// 自己就把这条列为未验证风险）、powershell.exe 被策略拦截、结果文件缺失或超过
	// hypervMaxResultBytes。此时若继续往下走，会跳过整段提权删除，却照样清台账 + 移出池
	// ⇒ Hyper-V 对象与宿主接口都还在、台账没了 ⇒ 用户在 UI 里彻底删不掉这张卡。
	// 方向上是失败安全的（不误删用户网卡），但归属系统不自洽。
	// 这里选择中止并原样保留台账与出口池：台账宁可多，不可少。
	inventory, invErr := s.readInventory()
	if invErr != nil {
		return nil, hypervErrorf(hypervCodeUnavailable,
			"查询 Hyper-V 现有网卡失败：%v；已中止删除，台账与出口池保持不变", invErr.Error())
	}
	for index := range rows {
		rows[index].resolve(inventory)
		// 单张路径把「跳过」翻译回「报错」。文案由 resolve 生成（与批量共用同一句），
		// 但错误码固定 not_managed —— 这正是改动前那道 MAC 闸门返回的码。
		if requireLedger && rows[index].reason != "" {
			return nil, hypervErrorf(hypervCodeNotManaged, "%s", rows[index].reason)
		}
	}

	// 一次提权脚本删 N 张。所有通过判定的行打进同一个 envelope、只调一次 runScript：
	// 每张卡各弹一次 UAC 的话，用户点 5 张就要确认 5 次管理员权限，且中途一旦有一张
	// 超时整批就卡死。脚本的 items 本来就是批量语义，Create 的批量创建就是这么用的。
	items := make([]hypervEnvelopeItem, 0, len(rows))
	for index := range rows {
		if rows[index].ready() {
			rows[index].scripted = true
			items = append(items, rows[index].item)
		}
	}
	if len(items) > 0 {
		result, runErr := s.runScript(hypervEnvelope{Op: "remove", Items: items},
			hypervRemoveBatchTimeout(len(items)), true)
		// 两条路径共用同一套成败判定，只有「怎么表达失败」在下面分叉。
		// 单张路径**必须**读 result.Failures：脚本把每张卡的异常逐条 catch 进
		// $failures 后仍然 Publish $true 'ok' 退出，所以「脚本没报错」根本不等于
		// 「这张卡删掉了」。漏读它会让脚本失败的网卡被标成 removed=true 并清掉台账与
		// 出口池 —— 卡还在，归属没了，用户此后永远删不掉它。
		hypervReconcileRemoveRows(rows, result, runErr)
		if requireLedger {
			// runErr（脚本根本跑不起来：提权被拒、平台不支持、结果文件读不到）与逐条失败
			// 都要变成 error。前者原样上抛，与改动前的 Remove 完全一致；后者是本次补上的
			// 缺口，用 remove_failed 报错——被删失败的张卡此刻仍在宿主机上，报 not_managed
			// 会把它说成「不是我们建的」，那是归属问题，不是删除失败。
			if runErr != nil {
				return nil, runErr
			}
			for index := range rows {
				row := &rows[index]
				if !row.scripted || row.removed {
					continue
				}
				return nil, hypervErrorf(hypervCodeRemoveFailed, "%s 删除失败：%s", row.name, row.reason)
			}
		}
	}

	// ---------------------------------------------------------------- 清台账与出口池
	//
	// 顺序：**先跑脚本，确认成功之后再清**。反过来（先摘池/清台账、再删网卡）一旦脚本
	// 失败，用户就会看到「网卡还在、池里没了、台账没了」——他既没法用这张卡，也没法
	// 让本工具认领它删掉，是最难恢复的状态。正序的窗口只有「一次 Remove-VMNetworkAdapter
	// 的时长」：脚本已经回报 removed、但台账还没来得及清时进程被杀，留下的只是台账里
	// 一条指向已删网卡的记录，List() 会把它显示成 absent，重删一次即幂等收干净，
	// 引擎侧还有 watchdog 自愈。这个方向的残留是自愈的，反方向的残留不是。
	var managedNames []string
	var poolAliases []string
	for _, row := range rows {
		if !row.removed {
			continue
		}
		if row.managed {
			managedNames = append(managedNames, row.name)
		}
		// 台账外的卡**不动台账**：它本来就不在里面，没有可清的记录。
		poolAliases = append(poolAliases, row.alias)
	}
	if len(managedNames) > 0 {
		if err := s.updateLedger(func(current *hypervLedger) error {
			for _, name := range managedNames {
				current.removeEntry(name)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	s.invalidateInventory()
	if err := s.applyPoolUpdate(nil, poolAliases); err != nil {
		return nil, err
	}
	return hypervRemoveResults(rows), nil
}

// resolve 用系统实况把这张卡判定成「可删 / 不可删 / 已不存在」，并拼好脚本条目。
func (r *hypervRemoveRow) resolve(inventory hypervInventory) {
	if r.managed {
		// DeviceId 优先、名字兜底：DeviceId 是真机上唯一稳定的主键，名字会重复。
		live, found := inventory.find(r.entry.AdapterID, r.entry.Name)
		if !found {
			// 幂等：inventory 查询是**成功**的、只是没找到这张卡 —— 用户手工删过，
			// 或上一轮脚本成功但父进程被杀。清掉台账与出口池都是对的，不让用户卡在
			// 一个永远删不掉的行上，也不用再弹一次 UAC 去删一张已经不在的卡。
			r.cleanupOnly = true
			r.removed = true
			return
		}
		// 第三重判定。台账 MAC 与系统实况不一致，意味着这张卡被重建或复用过；按旧
		// 记录去删就是在删别人的东西，直接拒绝。
		if !hypervMACEqual(r.entry.MACAddress, live.MAC) {
			r.reason = hypervMACMismatchReason(r.name, live.MAC, r.entry.MACAddress)
			return
		}
		r.item = hypervEnvelopeItem{
			Name:       r.entry.Name,
			MAC:        formatHypervMAC(r.entry.MACAddress),
			AdapterID:  r.entry.AdapterID,
			SwitchName: r.entry.SwitchName,
		}
		return
	}
	// 台账外：跳过前两道闸门后，唯一还能证明「这张卡此刻确实存在、且是可以被指名删除的
	// 那一条对象」的证据就是系统实况。find 的名字兜底路径要求 MAC 非空，恰好把
	// reports/vnic/60 实测到的「同名幽灵记录」（MAC 空、DeviceId 空）挡在外面。
	live, found := inventory.find("", r.name)
	if !found {
		if inventory.hasNameWithoutMAC(r.name) {
			r.reason = fmt.Sprintf("%s 在 Hyper-V 里的记录没有 MAC 与设备 ID（幽灵记录），无法精确定位，已跳过删除", r.name)
			return
		}
		r.reason = fmt.Sprintf("在 Hyper-V 的当前状态里找不到 %s，可能已被手工删除；未做任何改动", r.name)
		return
	}
	// DeviceId 留空：脚本的 remove 分支在 adapterId 为空时按 MAC 定位对象。台账外
	// 的卡我们没有权威的 DeviceId，与其猜一个不如用实况读回来的 MAC —— 它已经被
	// find() 验证过非空且确实是这张卡的。
	r.item = hypervEnvelopeItem{Name: r.name, MAC: formatHypervMAC(live.MAC)}
}

// ready 判定这一行是否要进提权脚本。
func (r hypervRemoveRow) ready() bool {
	return r.reason == "" && !r.cleanupOnly
}

// hypervReconcileRemoveRows 按脚本回报逐张定成败。**部分失败不中止整批**：一张失败，
// 其余照删。
func hypervReconcileRemoveRows(rows []hypervRemoveRow, result *hypervScriptResult, runErr error) {
	var failures []hypervScriptFailure
	if result != nil {
		failures = result.Failures
	}
	// certain = 脚本跑完了**并且**逐条交代了成败。只有这种情况下「没被点名失败」才能
	// 解读成「删成功了」。结果文件缺失意味着无从确知脚本到底做了什么（父进程放弃等待
	// 或提权进程被杀），此时一张都不能算成功，否则会清掉台账里那些其实还活着的卡。
	certain := runErr == nil && result != nil && result.OK
	// 匿名失败（failures 里有条目但都没写 name）对不上任何一张卡，只能整体归因。
	anonymous := len(failures) > 0 && !hypervFailuresNamed(failures)
	for index := range rows {
		row := &rows[index]
		if !row.scripted {
			continue
		}
		_, named := hypervFailureByName(failures, row.name)
		if !certain || named || anonymous {
			row.reason = hypervFailureMessage(failures, row.name, runErr)
			continue
		}
		row.removed = true
	}
}

func hypervRemoveResults(rows []hypervRemoveRow) []HyperVRemoveResult {
	out := make([]HyperVRemoveResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, HyperVRemoveResult{
			Name:      row.name,
			Removed:   row.removed,
			Reason:    row.reason,
			Interface: row.alias,
		})
	}
	return out
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
	// 两个调用方给的键形式不同（对象名 vs 宿主别名），统一在这里归一，
	// 否则 add 写进去的键 remove 删不掉。见 hypervPoolKey 的注释。
	//
	// 必须写进**新切片**而不是就地改 add/remove：awaitBatch 把 appeared 同时传给了
	// 本函数和 awaitAddresses。就地归一会把 names 里的对象名就地换成别名，
	// awaitAddresses 再包一层就成了 "vEthernet (vEthernet (HypoMux-vnic-01))"，
	// 台账 state 从此永远回写不了（真机端到端实测到，reports/vnic/76 §16）。
	// 参数切片不是本函数的所有物，就地改就是副作用。
	normalizedAdd := make([]string, len(add))
	for i := range add {
		normalizedAdd[i] = hypervPoolKey(add[i])
	}
	normalizedRemove := make([]string, len(remove))
	for i := range remove {
		normalizedRemove[i] = hypervPoolKey(remove[i])
	}
	add, remove = normalizedAdd, normalizedRemove
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
	// remove 名单里的权重键必须**无条件**删掉。上面那个 delete 只在「这个键当时还在
	// selected 里」时才会执行，而 settings.json 里完全可能存在「有权重、没被选中」的
	// 悬空键（用户手工改过、或早期版本写权重失败只写了一半）。留着它，调度器就会按
	// 一张已经不存在的网卡参与权重分配，删网卡反而把调度打歪。
	//
	// 放在补默认权重**之前**：万一某个键同时出现在 add 与 remove 里，「新增」赢 ——
	// 那种调用本就不存在（Create 只传 add、Remove 只传 remove），但顺序上必须有定论。
	for _, item := range remove {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			delete(weights, trimmed)
		}
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
			// 先比**键数**：下面那个循环只遍历新 map，**纯删除**（权重键没了、值没变）
			// 一个都遍历不到，changed 会一直是 false，于是这里直接 return nil —— 悬空
			// 权重键被算成「没有变化」而永远留在 settings.json 里。删网卡正好会走到
			// 这条路（网卡不在 selected 里、只有权重），所以必须先比长度。
			if len(weights) != len(current.AdapterWeights) {
				changed = true
			}
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
  # 这里不能用 $args：PowerShell 里函数的 $args 是这个函数自己的实参，会遮蔽脚本作用域的
  # $args（reports/vnic/76 实测 function_sees_args=NULL-EMPTY）。所以 payload 只能由 Go 侧
  # 在正文最前面播种进这个专用变量。变量不存在时 [string] 得到空串，这里 fail-closed 返回
  # $null，调用方 exit 2。
  $encoded = [string]$__hypervPayloadB64
  if ([string]::IsNullOrWhiteSpace($encoded)) { return $null }
  try {
    $raw = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))
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

// hypervPayloadVariable 是 Go 侧播种 payload 的目标变量名。刻意不叫 $args：PowerShell 里
// **函数的 $args 是这个函数自己的实参**，会遮蔽脚本作用域的 $args，真机实测
// （reports/vnic/76）播种 `$args = @('…')` 后脚本作用域确实是 SEEDED-B64，但函数里读到的
// 仍然是空 —— 而 Read-Envelope 正是函数。所以只能用这个不会被遮蔽的普通变量。
const hypervPayloadVariable = "__hypervPayloadB64"

// hypervSeededCommandBody 组装写路径 **-EncodedCommand 正文**：在纯常量脚本前面播种一行
// payload 赋值，脚本本体一个字不动。
//
// 为什么必须这样，而不是把 payload 当命令行尾随参数（修复前的做法）：PowerShell 5.1
// **不接受 -EncodedCommand 之后的位置参数**，会把那个 token 当成「第二条命令」，打印用法
// 横幅后以 0xFFFD0000 退出，脚本从未执行 —— 真机实测 reports/vnic/76。这条链路上的
// create / remove / waitip 当时因此全部必然失败。
//
// 注入面：payload 先过 base64.StdEncoding，字母表只有 A-Za-z0-9+/= ，不含单引号、反引号、
// 空格或 $ ，**结构上逃不出那个单引号字面量**；前缀本身是完全常量。hypervPowerShellScript
// 仍是纯常量，可变数据只出现在这一行播种里。
//
// 放在跨平台文件里是为了让单测能在没有 Hyper-V 的 runner 上直接断言命令正文的形状。
func hypervSeededCommandBody(payload []byte) string {
	return "$" + hypervPayloadVariable + " = '" +
		base64.StdEncoding.EncodeToString(payload) + "'\n" +
		hypervPowerShellScript
}

// hypervPowerShellArguments 返回 powershell.exe 的参数数组（不含可执行文件本身）。
//
// **-EncodedCommand 之后不允许有任何尾随 token**：PowerShell 5.1 会把尾随 token 当成
// 「第二条命令」，打印用法横幅后以 0xFFFD0000 退出，脚本从未执行 —— reports/vnic/76 的
// 原始故障。这条断言由 TestHypervPowerShellArgumentsHaveNoTrailingToken 守着。
//
// 放在跨平台文件里，使单测在没 Hyper-V 的 runner 上也能断言参数形状。
func hypervPowerShellArguments(payload []byte) []string {
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(hypervUTF16LE(hypervSeededCommandBody(payload))),
	}
}

// ---------------------------------------------------------------- 只读脚本常量

// hypervReadInventoryScript 是**读取路径**专用的常量脚本，与上面的提权脚本刻意分开。
//
// 为什么读取路径可以简化到「一条常量命令 + stdout」：它**没有任何可变数据** —— 正文
// 就是 Get-VMSwitch 与 Get-VMNetworkAdapter -ManagementOS 两条只读查询。于是整条命令行
// 100% 常量、零插值、零用户输入，注入面天然为零；也就顺理成章地不需要 base64 注入、
// 不需要结果文件协议、不需要原子提交、不需要 -EncodedCommand、不需要 runas。JSON 直接
// 走 stdout，由 Go 侧解析。
//
// 写路径（create / remove / waitip）有 MAC、名字、别名等可变数据且必须提权，那套
// base64 + 结果文件 + 原子提交是必需的，刻意保持原样不动。
//
// PowerShell 5.1 的两条硬约束（真机实测，reports/vnic/76）：
//  1. 传入的命令行正文里**不能出现双引号** —— exec 的参数转义与 PowerShell 自己的引号
//     解析会互相打架；正文一律用单引号。
//  2. ConvertTo-Json 吃管道里的单元素数组会退化成非数组，所以必须 -InputObject @(...)。
const hypervReadInventoryScript = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$state = $null
try {
  Import-Module Hyper-V -ErrorAction Stop
  $adapters = @()
  foreach ($adapter in @(Get-VMNetworkAdapter -ManagementOS -ErrorAction Stop)) {
    $mac = [string]$adapter.MacAddress
    $device = [string]$adapter.DeviceId
    # 幽灵记录：MAC/DeviceId 三空却报 Ok 的残留项（reports/vnic/60 实测本机存在）。
    if ([string]::IsNullOrWhiteSpace($mac)) { continue }
    if ([string]::IsNullOrWhiteSpace($device)) { continue }
    $adapters += [pscustomobject]@{ name = [string]$adapter.Name; adapterId = $device; mac = $mac; switchName = [string]$adapter.SwitchName; status = [string]$adapter.Status }
  }
  $switches = @()
  foreach ($item in @(Get-VMSwitch -ErrorAction Stop)) {
    $uplink = [string]$item.NetAdapterInterfaceDescription
    $nic = [string]$item.NetAdapterName
    # 本机实测（reports/vnic/76）：VMSwitch 上根本没有 NetAdapterName 属性（恒为空），
    # 真正的线索是 NetAdapterInterfaceDescription —— 它是网卡的 *InterfaceDescription*
    # （例如 Realtek Gaming 2.5GbE Family Controller），不是别名，所以查宿主网卡必须按
    # -InterfaceDescription 匹配，按 -Name 匹配永远落空。
    if ([string]$item.SwitchType -eq 'External' -and [string]::IsNullOrWhiteSpace($nic)) {
      $probe = @(Get-NetAdapter -InterfaceDescription $uplink -ErrorAction SilentlyContinue)
      if ($probe.Count -gt 0) { $nic = [string]$probe[0].Name }
    }
    $switches += [pscustomobject]@{ name = [string]$item.Name; type = [string]$item.SwitchType; allowManagementOs = [bool]$item.AllowManagementOS; uplink = $uplink; netAdapterName = $nic }
  }
  $state = [pscustomobject]@{ ok = $true; code = 'ok'; error = ''; adapters = @($adapters); switches = @($switches) }
} catch {
  $state = [pscustomobject]@{ ok = $false; code = 'script_failed'; error = [string]$_.Exception.Message; adapters = @(); switches = @() }
}
[Console]::Out.WriteLine((ConvertTo-Json -InputObject $state -Compress -Depth 4))
`

// hypervParseInventoryOutput 把只读脚本的 stdout 还原成 hypervScriptResult。纯函数：
// 不碰系统、不依赖 Hyper-V，因此可以在任何平台（含没有 Hyper-V 的 CI runner）上单测。
//
// 容错只有两处，且都是可解释的：
//  1. 去掉 UTF-8 BOM 与首尾空白 —— Windows 控制台/重定向偶尔会带 BOM。
//  2. 若 stdout 前面混进了非 JSON 噪声（PowerShell 的警告横幅等），退化成截取第一个
//     '{' 到最后一个 '}'。绝不做「猜」：截不出合法 JSON 就把原文带回错误里。
func hypervParseInventoryOutput(stdout []byte) (*hypervScriptResult, error) {
	raw := strings.TrimPrefix(string(stdout), "\ufeff")
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("只读脚本没有向 stdout 输出任何内容")
	}
	candidate := trimmed
	if !strings.HasPrefix(candidate, "{") {
		start := strings.Index(candidate, "{")
		end := strings.LastIndex(candidate, "}")
		if start < 0 || end <= start {
			return nil, hypervOutputRejected("stdout 不是 JSON", trimmed)
		}
		candidate = candidate[start : end+1]
	}
	result := &hypervScriptResult{}
	if err := json.Unmarshal([]byte(candidate), result); err != nil {
		return nil, hypervOutputRejected("stdout 解析失败", trimmed)
	}
	return result, nil
}

// hypervOutputRejected 把 stdout 原文按超短长度塞进错误里：报错必须能让人一眼看出
// PowerShell 到底吐了什么，同时不能让一条日志膨胀到不可读。
func hypervOutputRejected(reason string, raw string) error {
	const limit = 300
	if len(raw) > limit {
		return fmt.Errorf("%s（stdout 前 %d 字节：%q…）", reason, limit, raw[:limit])
	}
	return fmt.Errorf("%s（stdout：%q）", reason, raw)
}
