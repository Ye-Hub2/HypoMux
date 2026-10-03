package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient"
)

const (
	// vnicCreateTimeout 覆盖整次创建事务：配置落盘、提权核心握手与 keeper 启动。
	vnicCreateTimeout = 60 * time.Second
	// vnicRemoveTimeout 在引擎侧 20s 停止预算之上留出余量，避免桌面先超时。
	vnicRemoveTimeout = 30 * time.Second
	// vnicStatusTimeout 让主页轮询保持廉价。
	vnicStatusTimeout = 10 * time.Second
	// vnicStartupTimeoutMS 是引擎侧单次 sing-box 启动预算，单位毫秒。
	vnicStartupTimeoutMS = 20000
)

// errVNICUnsupported 在核心缺少 vnic.create 能力时统一返回，避免出现两处文案。
var errVNICUnsupported = errors.New("当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试")

// VirtualAdapterStatus 是引擎 VNICStatus 在 Wails 边界的投影。字段与 JSON tag 由
// reports/vnic/00-frozen-interface.md §3 冻结：前端 platform/services.ts 与它的
// vitest 用例都按这些小驼峰名读取，改动即破坏契约。
type VirtualAdapterStatus struct {
	State         string `json:"state"`
	InterfaceName string `json:"interfaceName"`
	Address       string `json:"address"`
	PrefixLength  int    `json:"prefixLength"`
	MTU           int    `json:"mtu"`
	AdapterGUID   string `json:"adapterGuid"`
	CreatedAt     string `json:"createdAt"`
	LastError     string `json:"lastError"`
}

// VirtualAdapterService 负责桌面侧的虚拟网卡：它把 keeper 的最小 sing-box 配置写到
// 数据目录，再让高权限聚合核心用一个独立进程持有 Wintun 适配器。适配器的存活时间
// 就等于该 keeper 进程的存活时间，桌面侧不持有任何路由或网卡状态。
type VirtualAdapterService struct {
	engine *EngineService
	mu     sync.Mutex
	closed bool
}

// NewVirtualAdapterService 绑定共享的聚合核心服务。EngineService.client 是私有字段，
// 但同包内直接访问是既有做法（MTUService 也这样用），因此不需要 EngineService 增加
// 访问器，也不必改动 engine.go。
func NewVirtualAdapterService(engine *EngineService) *VirtualAdapterService {
	return &VirtualAdapterService{engine: engine}
}

// Create 生成 keeper 配置并启动虚拟网卡。名字或地址为空时回落到契约默认值，前缀长度
// 与 MTU 是常量，所以前端绑定面只暴露两个入参。
func (s *VirtualAdapterService) Create(interfaceName string, address string) (VirtualAdapterStatus, error) {
	client, err := s.readyClient()
	if err != nil {
		return VirtualAdapterStatus{}, err
	}
	// 60s 预算覆盖整次创建事务：配置落盘、提权核心握手与 keeper 启动。
	ctx, cancel := context.WithTimeout(context.Background(), vnicCreateTimeout)
	defer cancel()
	plan, err := writeVNICSingBoxConfig(interfaceName, address, vnicPrefixLength, vnicMTU)
	if err != nil {
		return VirtualAdapterStatus{}, err
	}
	// 已连接但缺 vnic.create 的旧核心直接拒绝：EnsureElevated 会重启核心进程，不能为了
	// 一个必然失败的请求先打断正在运行的代理会话。
	if hello := client.Hello(); hello.ProtocolVersion != 0 && !slices.Contains(hello.Capabilities, engineclient.MethodVNICCreate) {
		return VirtualAdapterStatus{}, errVNICUnsupported
	}
	// 虚拟网卡只能由高权限核心创建（未提权的核心会回 elevation_required），这与 TUN
	// 启动路径一致：先把核心拉起为管理员进程。注意副作用——若核心此前以非提权方式
	// 运行，这一步会重启它，正在进行的代理会话随之结束；见交付报告中的 Lead 决策项。
	hello, err := client.EnsureElevated(ctx)
	if err != nil {
		return VirtualAdapterStatus{}, fmt.Errorf("创建虚拟网卡需要管理员权限的独立聚合核心：%w", err)
	}
	if !slices.Contains(hello.Capabilities, engineclient.MethodVNICCreate) {
		return VirtualAdapterStatus{}, errVNICUnsupported
	}
	result, err := client.VNICCreate(ctx, engineclient.VNICCreateParams{
		Executable:       plan.Executable,
		ConfigPath:       plan.Path,
		ConfigSHA256:     plan.SHA256,
		StartupTimeoutMS: vnicStartupTimeoutMS,
		InterfaceName:    plan.InterfaceName,
		Address:          plan.Address,
		PrefixLength:     plan.PrefixLength,
		MTU:              plan.MTU,
	})
	if err != nil {
		return VirtualAdapterStatus{}, fmt.Errorf("创建虚拟网卡失败：%w", err)
	}
	return mapVNICStatus(result.VNIC), nil
}

// Status 读取 keeper 状态，不启动任何进程。没有连接中的核心时直接返回 absent：主页会
// 轮询这个接口，把「核心没开」当成错误提示会持续弹 toast（connections.go 的降级做法
// 出于同一理由）。当宿主已经关闭、但引擎仍报告没有 keeper 时，同样按 absent 处理。
func (s *VirtualAdapterService) Status() (VirtualAdapterStatus, error) {
	client := s.connectedClient()
	if client == nil {
		return vnicAbsentStatus(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), vnicStatusTimeout)
	defer cancel()
	status, err := client.VNICStatus(ctx)
	if err != nil {
		if vnicAbsentError(err) || client.Hello().ProtocolVersion == 0 {
			return vnicAbsentStatus(), nil
		}
		return VirtualAdapterStatus{}, fmt.Errorf("读取虚拟网卡状态失败：%w", err)
	}
	return mapVNICStatus(status), nil
}

// Remove 停止 keeper。它幂等：没有核心、或本就没有虚拟网卡，都返回 absent 而不是错误。
func (s *VirtualAdapterService) Remove() (VirtualAdapterStatus, error) {
	client := s.connectedClient()
	if client == nil {
		return vnicAbsentStatus(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), vnicRemoveTimeout)
	defer cancel()
	status, err := client.VNICRemove(ctx)
	if err != nil {
		if vnicAbsentError(err) || client.Hello().ProtocolVersion == 0 {
			return vnicAbsentStatus(), nil
		}
		return VirtualAdapterStatus{}, fmt.Errorf("移除虚拟网卡失败：%w", err)
	}
	return mapVNICStatus(status), nil
}

// Shutdown 在应用退出时停掉 keeper。调用方必须在 engineService.Shutdown() 之前调用它，
// 否则核心客户端已经关闭，remove 到不了引擎；即便如此，引擎自身的退出路径也会回收
// keeper，所以这里只做尽力而为，失败不阻塞窗口销毁。
func (s *VirtualAdapterService) Shutdown() {
	if s == nil || s.engine == nil || s.engine.client == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	client := s.engine.client
	if client.Hello().ProtocolVersion == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), vnicRemoveTimeout)
	defer cancel()
	_, _ = client.VNICRemove(ctx)
}

// readyClient 返回可用的核心客户端，并在服务不可用或应用正在退出时给出明确错误。
func (s *VirtualAdapterService) readyClient() (*engineclient.Client, error) {
	if s == nil || s.engine == nil || s.engine.client == nil {
		return nil, errors.New("聚合核心客户端不可用")
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("HypoMux 正在退出")
	}
	return s.engine.client, nil
}

// connectedClient 是只读路径用的弱检查：核心没连接时返回 nil，由调用方降级为 absent。
func (s *VirtualAdapterService) connectedClient() *engineclient.Client {
	if s == nil || s.engine == nil || s.engine.client == nil {
		return nil
	}
	if s.engine.client.Hello().ProtocolVersion == 0 {
		return nil
	}
	return s.engine.client
}

// vnicAbsentError 判定远端错误是否等价于「当前没有虚拟网卡」：会话已断开、引擎报告
// invalid_state（没有 keeper 可删/可读）、或核心版本根本没有这三个方法。
func vnicAbsentError(err error) bool {
	var remote *engineclient.RemoteError
	if !errors.As(err, &remote) {
		return false
	}
	switch remote.Code {
	case "disconnected", "invalid_state", "method_not_found":
		return true
	default:
		return false
	}
}

// vnicAbsentStatus 是「当前没有虚拟网卡」的统一投影，前端只认 state=absent。
func vnicAbsentStatus() VirtualAdapterStatus {
	return VirtualAdapterStatus{State: "absent"}
}

// mapVNICStatus 逐字段搬运引擎结果，并把空状态规范化为 absent，避免前端收到空串。
func mapVNICStatus(status engineclient.VNICStatus) VirtualAdapterStatus {
	state := strings.ToLower(strings.TrimSpace(status.State))
	if state == "" {
		state = "absent"
	}
	return VirtualAdapterStatus{
		State:         state,
		InterfaceName: status.InterfaceName,
		Address:       status.Address,
		PrefixLength:  status.PrefixLength,
		MTU:           status.MTU,
		AdapterGUID:   status.AdapterGUID,
		CreatedAt:     status.CreatedAt,
		LastError:     status.LastError,
	}
}
