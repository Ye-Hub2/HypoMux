# 冻结接口 · v1（AI 移除 + 虚拟网卡创建 + CI 构建）

> 本文件是**唯一接口契约来源**。任何成员不得擅自改名、改签名、改 JSON 字段；如发现契约本身有误，先在报告里记录并通知 Lead，不要各自改一套。
> 基线：`main` / HEAD `e66016e` / HypoMux v2.7.0。目标远端：`https://github.com/Ye-Hub2/HypoMux`。

## 0. 全局约束（所有成员必须遵守）

1. **本机无 Go、无 pnpm、无 node_modules、无 `desktop/frontend/bindings/`**。因此：无法本地编译、无法本地跑测试。
   ⇒ 所有代码必须**逐行静态自查**：符号是否存在、导入是否被用到、字段名/方法名/大小写是否与契约逐字一致。禁止"大概对"。
2. **只写自己的文件**（见各任务 write scope）。跨文件冲突一律交 Lead 仲裁。
3. **不得修改** `.github/workflows/**`、`desktop/build/**`、`nsis/**`、`protocol/v1/manifest.json` 之外的契约文件。
4. 结论/交付写成 Markdown 放到 `reports/vnic/` 下，文件名见各自任务。禁止把 `reports/` 加入 git。
5. 删除文件用 `git rm` 或直接删除均可（CI runner 会自动 add -A），但**不要** commit / push（只有 ci-runner 任务负责）。
6. 报错/未验证项必须如实写明"未编译验证"。

## 1. 虚拟网卡：技术选型（已定，不要另起方案）

**选定：Wintun 适配器 + sing-box keeper 进程，由 Core（LocalSystem 服务或按需 runas 的核心）持有。**

理由（与 xuni-network 对照）：

| 方案 | 结论 |
|---|---|
| Hyper-V `Add-VMNetworkAdapter -ManagementOS`（xuni-network 路线） | **不采用**。需要启用 Hyper-V 功能 + 预先建好虚拟交换机；Windows 家庭版不可用；需要随包分发经审计的 .ps1（xuni 用 `lib\XVNic.Common.psm1` 等）；本仓库无先例。优点（重启后仍存在）不足以抵消依赖门槛。 |
| **Wintun（本方案）** | 复用仓库**已内置并已钉哈希**的 `bin/sing-box.exe` 1.14.2 + `bin/wintun.dll`；复用既有 `engine/internal/tun` 的 Supervisor 机制（配置暂存受保护目录 + 启动 + 就绪校验 + 优雅停机）；无 Hyper-V 依赖；Home/Pro 通用；由 Core 提权创建。 |
| 直接 `LoadLibrary(wintun.dll)` 调 `WintunCreateAdapter` | 记为**后续演进**（可去掉子进程）。本轮不做，因为本地无法编译验证 DLL 绑定代码。 |

实现要点：sing-box 配置只含**一个 tun inbound + 一个 direct outbound**，`auto_route:false`、`strict_route:false`；适配器名与地址由桌面端生成配置时指定。sing-box 进程存活期间适配器即存在于 Windows 网络适配器列表；`remove` = 停掉 keeper。

## 2. 协议契约（engine/internal/api/v1 + protocol/v1/manifest.json）

新增 **3 个方法**，插入位置：紧跟 `tun.deactivate` 之后、`dns.resolve` 之前。

| 方法 | 保序位置 | guard 语义（写进 manifest 现有字段风格） |
|---|---|---|
| `vnic.create` | 第 10 个 | 需要提权（Wintun 需要管理员）；受 state 保护；使用调用方 deadline + host 上下文 |
| `vnic.status` | 第 11 个 | read_only / none |
| `vnic.remove` | 第 12 个 | safe_retry / 需要提权 |

`manifest.json` 的 `error_codes` **保持不变（14 个）**：新方法只用既有码 `invalid_params` / `invalid_state` / `elevation_required` / `security_policy_rejected` / `start_failed` / `stop_failed`。

> **修正（由 `reports/vnic/90-verification.md` 的 F-2 触发，Lead 追认）**：`vnic.create` 的**最终启动失败**实际返回 `tun_failed`（`engine/internal/server/server.go:882-887`），不是上面列举的 `start_failed`。这是有意的：`tun_failed` 是主 TUN 启动路径的既有错误码，与「启动一个 Wintun 适配器失败」语义一致，且仍在 manifest 的 14 个码之内。契约以代码为准，`tun_failed` 属允许集合。

### 2.1 Go 常量（`engine/internal/api/v1`）

```go
const (
    MethodVNICCreate = "vnic.create"
    MethodVNICStatus = "vnic.status"
    MethodVNICRemove = "vnic.remove"
)
```

### 2.2 参数与结果（逐字使用这些字段名与 JSON tag）

```go
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

func (p VNICCreateParams) Config() tun.Config // Executable/ConfigPath/ConfigSHA256/StartupTimeout 映射，与 TunActivateParams.Config() 同形

type VNICStatus struct {
    State         string `json:"state"`                    // absent|creating|present|removing|failed
    InterfaceName string `json:"interface_name"`
    Address       string `json:"address"`
    PrefixLength  int    `json:"prefix_length"`
    MTU           int    `json:"mtu"`
    AdapterGUID   string `json:"adapter_guid,omitempty"`
    CreatedAt     string `json:"created_at,omitempty"`     // RFC3339
    LastError     string `json:"last_error,omitempty"`
}

type VNICCreateResult struct {
    Accepted bool       `json:"accepted"`
    VNIC     VNICStatus `json:"vnic"`
}
```

`vnic.status` 与 `vnic.remove` 的 result 均为 `VNICStatus`。`vnic.remove` 成功后 `state=absent`。

引擎侧状态映射：`tun.StateStopped` → `absent`（未创建过）或 `failed`（曾失败）；`tun.StateStarting` → `creating`；`tun.StateRunning` → `present`；`tun.StateStopping` → `removing`；`tun.StateFailed` → `failed`。

### 2.3 引擎行为

- 在 `Server` 上新增第二个 supervisor：**必须** `tun.NewSupervisor()` 的新实例（不要复用 `s.tun`，两者生命周期独立）。
- `vnic.create`：`authorizeTunConfig(params.Config())` 走同一套安全策略 → 若 `!s.identity.Elevated` 返回 `elevation_required` → 若已 `present` 且参数不同，先 `Stop` 再启（或直接返回 `invalid_state`，二选一并写进报告，必须幂等：同参数重复调用不得报错） → `Activate` → 复用 `isStaleTunAdapterError` 的一次性重试。
- `vnic.remove`：`Stop`（20s 超时），幂等；未创建时返回 `absent` 且不报错。
- `host.shutdown` / 引擎退出路径**必须**停掉 vnic keeper（顺序在 tun 停机之后、进程退出之前）。
- `vnic.create` 必须校验 `InterfaceName` 非空、`Address` 是合法 IPv4、`1<=PrefixLength<=32`、`MTU` 在 576..65535，否则 `invalid_params`。
- **不得**让既有 `tun.Recover()` / stale-adapter 清理误杀 vnic keeper（读 `engine/internal/tun/recover.go` 与 `cleanup_windows.go` 确认；若确有冲突，在报告里写明并给出最小规避）。

### 2.4 契约测试

`engine/internal/api/v1/contract_test.go` 断言方法名顺序 / 集合 / `max_message_bytes` / fixtures 解码 ⇒ 必须同步更新方法列表与 fixtures（若 `fixtures/` 下要求每个方法都有样例，则为 3 个新方法各加一份）。

## 3. 桌面服务契约（desktop/internal/services）

新增文件 `desktop/internal/services/virtual_adapter.go`（+ `virtual_adapter_other.go` 若需要非 Windows 兜底），并新增 `desktop/internal/services/vnic_config.go`。

```go
type VirtualAdapterStatus struct {
    State         string `json:"state"`          // absent|creating|present|removing|failed
    InterfaceName string `json:"interfaceName"`
    Address       string `json:"address"`
    PrefixLength  int    `json:"prefixLength"`
    MTU           int    `json:"mtu"`
    AdapterGUID   string `json:"adapterGuid"`
    CreatedAt     string `json:"createdAt"`
    LastError     string `json:"lastError"`
}

type VirtualAdapterService struct{ ... }

func NewVirtualAdapterService(engine *EngineService) *VirtualAdapterService
func (s *VirtualAdapterService) Create(interfaceName string, address string) (VirtualAdapterStatus, error)
func (s *VirtualAdapterService) Status() (VirtualAdapterStatus, error)
func (s *VirtualAdapterService) Remove() (VirtualAdapterStatus, error)
func (s *VirtualAdapterService) Shutdown()
```

- **Wails 绑定生成的文件名 = 类型名小写**：`VirtualAdapterService` → `virtualadapterservice.ts`（与 `TunService` → `tunservice` 一致）。
- `Create` 的默认值处理：`interfaceName` 空 → `"HypoMux-VNIC"`；`address` 空 → 从 `10.66.0.0/24` 里挑一个未被占用的 `10.66.0.1`；prefix 固定 24；MTU 固定 1420。参数留两个（名字、地址），其余走常量，减少前端与绑定面。
- `Create` 内部：生成配置 → 算 SHA-256 → 调 engineclient 的 `VNICCreate` → 映射结果。整体超时 60s。
- 注册：`desktop/main.go` 中按既有模式 `app.RegisterService(application.NewService(virtualAdapterService))`（该文件由 ai-backend 先改，desktop-vnic 后再改，避免冲突）。
- engineclient：`desktop/internal/engineclient/` 下新增方法 `func (c *Client) VNICCreate(ctx context.Context, params <ParamsStruct>) (VNICCreateResult, error)`，参数用与引擎 JSON 对应的本地结构体（字段名可与引擎同形，JSON tag 逐字一致）。若 client 已有 `call`/`request` 私有方法，复用之。

## 4. 前端契约（desktop/frontend/src）

- 门面：`platform/services.ts` 的 `appServices` 增加分组

```ts
virtualAdapter: {
  create: (interfaceName: string, address: string) => VirtualAdapterService.Create(interfaceName, address),
  status: () => VirtualAdapterService.Status(),
  remove: () => VirtualAdapterService.Remove(),
},
```

  导入行逐字：`import * as VirtualAdapterService from "../../bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/virtualadapterservice";`
  类型从同目录 `models` re-export（照抄 `TunService` 现有写法）。
- UI：**主页按钮**。`pages/HomePage.tsx` 增加"创建虚拟网卡"按钮（已存在时变为状态 + "移除虚拟网卡"），点击弹对话框可改名字与地址（默认 `HypoMux-VNIC` / `10.66.0.1`），提交时按钮进入 loading（最长 60s），成功后用既有通知层提示，失败走既有 `conciseDiagnosticMessage` 摘要路径。
- 新增组件放 `components/vnic/`（组件 + 其测试）。**不要**新建页面、不要改 `App.tsx` / `CompactNavigation.tsx` / `AppShell.tsx`（这些由 ai-frontend 任务负责 AI 相关改动）。
- 中英双语文案沿用仓库既有内联模式（`locale === "en" ? ... : ...`），不要引入新的 i18n 键（`legacy.messages.json` 已判定为死键过多，不扩面）。

## 5. AI 移除边界

- **移除**：`desktop/internal/services/ai_*.go`（含全部 `ai_*_test.go`）、`desktop/frontend/src/components/ai/**`、`platform/ai.ts`、`platform/companionExport.ts(+test)`、`state/aiAvailability.ts`、`docs/AI_*.md`、`appearance` 中仅服务于 Live2D 皮肤的依赖（`pixi.js`、`pixi-live2d-display` 及其 `pnpm-workspace.yaml` overrides）。
- **保留（不是 AI）**：`theme/**`、`components/appearance/AppearancePreview.tsx`、`components/material/WallpaperLayer.tsx`、`pages/AppearanceLab.tsx`、`state/appearance*`、`components/shell/**`（除 assistant 入口）、`platform/services.ts`。
- 遗留配置：`settings.json` 里可能已有 `ai_enabled` 字段。**必须保证旧配置文件仍能加载**（若解码器 `DisallowUnknownFields` 则需保留该字段的接受能力或加迁移）。删除后要在报告里写明采取哪种兼容策略。
- 引擎与 `protocol/v1` 零 AI 引用，**不得触碰**。

## 6. 验收（integration-verifier 负责）

1. 全仓文本扫描：`ai_enabled` / `AIService` / `aiService` / `AIAssistant` / `pixi-live2d-display` / `pixi.js` / `hypomux:ai-` / `companionExport` / `aiAvailability` 在**源码与测试**中零残留（`docs/**` 与 git 历史除外），且无悬挂导入。
2. 方法名三方一致：`protocol/v1/manifest.json` ↔ `engine/internal/api/v1` 常量 ↔ 桌面 engineclient 调用 ↔ `platform/services.ts`。
3. 新文件全部被引用；被删文件的所有引用点已清理（逐条 grep 证明）。
4. `HomePage.tsx` 的新按钮代码路径引用的每个符号都在同文件或已导入。
5. 输出 `reports/vnic/90-verification.md`：逐条给"证据命令 + 结果"，标出无法静态验证的部分。

## 7. CI 构建（ci-runner 负责，仅此一人可 git 写操作）

- 允许：`git add` 指定源码路径（**不要**加 `reports/`）、`git -c user.name/email` 提交（用 `git commit -F <消息文件>`，禁止 `-m` 内联含引号/`--` 的消息）、`git push origin main`、`gh workflow run build.yml -f signing_mode=none`、轮询 `gh run list` / `gh run view --log-failed`。
- **禁止**：`--force`、`credential.interactive=false`、`GCM_INTERACTIVE=Never`（会触发 GCM `unable to get password from user`）、改任何 workflow 文件、推送 tag。
- 失败时：拉取失败日志，把**首个编译/测试错误**原文与所在文件:行号写进 `reports/vnic/95-ci-run.md`，不要自行猜测修复。
