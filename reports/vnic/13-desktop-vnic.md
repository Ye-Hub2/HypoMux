# T4 · 桌面虚拟网卡服务交付报告（task-11 / desktop-vnic-dev）

> 状态：**新增 3 个文件 + `desktop/main.go` 3 处注册改动（已获 Lead 授权，见 §5.1）**，代码侧已全部落地。
> **本机无 Go 工具链、无 pnpm/node_modules、无 `desktop/frontend/bindings/`** ⇒ 交付物**未编译、未测试**，全部结论来自逐行静态自查（见 §6）。
> 契约来源：`reports/vnic/00-frozen-interface.md`（v1，基线 HEAD `e66016e`），本报告所有行号以当前工作区文件为准。

---

## 1. 交付范围与文件清单

| 文件 | 行数 | 归属 | 说明 |
| --- | --- | --- | --- |
| `desktop/internal/services/virtual_adapter.go` | 226 | 本任务新增 | Wails 边界服务：`VirtualAdapterService` + `VirtualAdapterStatus` + 错误映射 |
| `desktop/internal/services/vnic_config.go` | 136 | 本任务新增 | keeper 的 sing-box 最小配置生成、原子落盘、SHA-256 |
| `desktop/internal/engineclient/vnic.go` | 80 | 本任务新增 | `vnic.create/status/remove` 三个 RPC 的客户端方法与本地结构体 |
| `desktop/main.go` | 391（原始 388，+3） | 本任务修改 3 行 | 构造 `virtualAdapterService`（`:150`）、关闭回调 `virtualAdapterService.Shutdown()`（`:156`）、`RegisterService`（`:188`） |

未改动（逐条确认过）：`desktop/internal/services/engine.go`、`settings.go`、`tun_config.go`、`ai_*.go`、`desktop/frontend/**`、`engine/**`、`.github/workflows/**`。`desktop/main.go` 只按 Lead 授权（T1 定稿、main.go 释放后）插入 3 行，**未触碰** T1 的 ai 相关改动。全程未执行 git commit/push，也未把 `reports/` 加入 git。

---

## 2. 关键函数清单（文件:行号）

### 2.1 `desktop/internal/services/virtual_adapter.go`

| 符号 | 行号 | 职责 |
| --- | --- | --- |
| `const vnicCreateTimeout/vnicRemoveTimeout/vnicStatusTimeout/vnicStartupTimeoutMS` | 15–24 | 60s / 30s / 10s / 20000ms 四档预算 |
| `var errVNICUnsupported` | 27 | 核心缺 `vnic.create` 时的统一错误（避免两处文案） |
| `type VirtualAdapterStatus struct` | 32–41 | Wails 投影，8 字段与契约 §3 逐字一致 |
| `type VirtualAdapterService struct` | 46–50 | `engine *EngineService` + `mu sync.Mutex` + `closed bool` |
| `func NewVirtualAdapterService(engine *EngineService)` | 55 | 绑定共享 `EngineService` |
| `func (s *VirtualAdapterService) Create(...)` | 61–102 | readyClient → 60s ctx → 写配置 → 能力预检 → `EnsureElevated` → 能力复检 → `VNICCreate` → 映射 |
| `func (s *VirtualAdapterService) Status()` | 107–123 | 只读；无核心或远端错误等价 absent 时返回 `{state:"absent"}`，不启动任何进程 |
| `func (s *VirtualAdapterService) Remove()` | 125–140 | 幂等停止 keeper |
| `func (s *VirtualAdapterService) Shutdown()` | 145–163 | 退出路径尽力而为地 remove（要求调用方在 `engineService.Shutdown()` 之前调用） |
| `func (s *VirtualAdapterService) readyClient()` | 166 | 服务/核心可用性硬检查（写路径） |
| `func (s *VirtualAdapterService) connectedClient()` | 180 | 弱检查（读路径），未连接返回 `nil` |
| `func vnicAbsentError(err error) bool` | 192 | `errors.As(*engineclient.RemoteError)`，Code ∈ {`disconnected`,`invalid_state`,`method_not_found`} |
| `func vnicAbsentStatus()` | 206 | 统一 absent 投影 |
| `func mapVNICStatus(status engineclient.VNICStatus)` | 211 | 逐字段搬运 + 空 state 规范化为 `absent` |

关键行号：能力预检 `:75`；`EnsureElevated` `:81`；能力复检 `:85`；`VNICCreate` 调用 `:88`（参数逐字 8 项 `:89–96`）。

### 2.2 `desktop/internal/services/vnic_config.go`

| 符号 | 行号 | 职责 |
| --- | --- | --- |
| `const vnicDefaultInterfaceName/vnicDefaultAddress/vnicPrefixLength/vnicMTU` | 15–23 | `"HypoMux-VNIC"` / `"10.66.0.1"` / `24` / `1420` |
| `type vnicConfigPlan struct` | 27–35 | 一次启动计划：可执行文件、配置路径、SHA-256、名称、地址、前缀、MTU |
| `func writeVNICSingBoxConfig(...)` | 41–115 | 校验 `1..32` / `576..65535` / 名称回落 → 生成配置（`:64–87`）→ `json.MarshalIndent`（`:88`）→ `sha256.Sum256`（`:92`）→ `settingsDirectory()/vnic`（`:93–96`）→ `.tmp`+`os.Rename` 原子提交（`:97–105`）|
| `func normalizeVNICAddress(...)` | 119–136 | 剥离合法的 `/24` 后缀、`net.ParseIP().To4()` 校验、空值回落默认地址 |

配置正文（`:64–87`）：`log{level:warn,timestamp:true}`、`inbounds[{type:tun,tag:tun-in,interface_name:<name>,address:["<addr>/24"],mtu:1420,auto_route:false,strict_route:false,stack:"system"}]`、`outbounds[{type:direct,tag:direct}]`，符合 §1「只含一个 tun inbound + 一个 direct outbound，`auto_route:false`、`strict_route:false`」。

### 2.3 `desktop/internal/engineclient/vnic.go`

| 符号 | 行号 | 职责 |
| --- | --- | --- |
| `const MethodVNICCreate/MethodVNICStatus/MethodVNICRemove` | 11–13 | `vnic.create` / `vnic.status` / `vnic.remove`（用于桌面能力探测） |
| `type VNICCreateParams struct` | 19–28 | 8 字段，JSON tag 与 §2.2 逐字 |
| `type VNICStatus struct` | 32–41 | 8 字段，JSON tag 与 §2.2 逐字 |
| `type VNICCreateResult struct` | 44–47 | `accepted` + `vnic` |
| `func (c *Client) VNICCreate(...)` | 52–58 | `c.Request(ctx, MethodVNICCreate, params, &result)` |
| `func (c *Client) VNICStatus(...)` | 63–69 | `c.Request(ctx, MethodVNICStatus, nil, &status)` |
| `func (c *Client) VNICRemove(...)` | 74–80 | `c.Request(ctx, MethodVNICRemove, nil, &status)` |

`engineclient.Client` 无私有 `call`/`request` 辅助，公开的 `Request(ctx, method string, params any, target any) error`（`client.go:379`）即复用点——契约 §3 末段「若 client 已有 call/request 私有方法，复用之」按事实落地为复用 `Request`。

---

## 3. 契约落地对照

### 3.1 §3 桌面服务契约（`00-frozen-interface.md:102–131`）

| 契约条目 | 实现位置 | 结论 |
| --- | --- | --- |
| 新增 `virtual_adapter.go` + `vnic_config.go` | 三个新文件 | ✅（`virtual_adapter_other.go` 未建，理由见 §5.4） |
| `VirtualAdapterStatus` 8 字段与 tag | `virtual_adapter.go:32–41` | ✅ 逐字（见 §3.2） |
| `type VirtualAdapterService struct{ ... }` | `:46–50` | ✅ |
| `NewVirtualAdapterService(engine *EngineService) *VirtualAdapterService` | `:55` | ✅ 签名逐字 |
| `Create(interfaceName string, address string) (VirtualAdapterStatus, error)` | `:61` | ✅ 签名逐字 |
| `Status() (VirtualAdapterStatus, error)` | `:107` | ✅ |
| `Remove() (VirtualAdapterStatus, error)` | `:125` | ✅ |
| `Shutdown()` | `:145` | ✅ |
| 默认值：名字空→`HypoMux-VNIC`；地址空→`10.66.0.1`；prefix 固定 24；MTU 固定 1420 | `vnic_config.go:17–23`、`:42–45`、`:121–123` | ✅（地址占用探测未做，见 §5.2） |
| `Create` 内部：生成配置 → 算 SHA-256 → 调 engineclient `VNICCreate` → 映射结果；整体超时 60s | `virtual_adapter.go:66–101`、`vnic_config.go:41–115` | ✅ ctx 在 `Create` 入口创建（`:67`），覆盖配置落盘、提权握手与 keeper 启动 |
| engineclient 新增 `VNICCreate(ctx, params) (VNICCreateResult, error)` | `vnic.go:52` | ✅ 参数用本地结构体，JSON tag 与引擎一致 |
| Wails 绑定名 `VirtualAdapterService` → `virtualadapterservice.ts` | 类型名已按契约命名 | ⏳ 绑定未生成（需 main.go 注册，见 §5.1） |
| `main.go` 注册 | **未落地** | ⏳ 等 Lead 通知（本轮禁改 main.go） |

### 3.2 JSON tag 逐字对照

| 引擎 §2.2 `VNICCreateParams` | `engineclient.VNICCreateParams` | 引擎 §2.2 `VNICStatus` | `engineclient.VNICStatus` | 桌面 §3 `VirtualAdapterStatus` |
| --- | --- | --- | --- | --- |
| `executable` | ✅ `vnic.go:20` | `state` | ✅ `:33` | ✅ `state` |
| `config_path` | ✅ `:21` | `interface_name` | ✅ `:34` | `interfaceName` ✅ |
| `config_sha256,omitempty` | ✅ `:22` | `address` | ✅ `:35` | `address` ✅ |
| `startup_timeout_ms` | ✅ `:23` | `prefix_length` | ✅ `:36` | `prefixLength` ✅ |
| `interface_name` | ✅ `:24` | `mtu` | ✅ `:37` | `mtu` ✅ |
| `address` | ✅ `:25` | `adapter_guid,omitempty` | ✅ `:38` | `adapterGuid` ✅ |
| `prefix_length` | ✅ `:26` | `created_at,omitempty` | ✅ `:39` | `createdAt` ✅ |
| `mtu` | ✅ `:27` | `last_error,omitempty` | ✅ `:40` | `lastError` ✅ |
| — | — | `VNICCreateResult{accepted, vnic}` | ✅ `:44–47` | — |

### 3.3 §2.3 引擎行为对应的桌面侧配合

| 引擎契约 | 桌面侧落地 |
| --- | --- |
| 需提权，未提权返回 `elevation_required` | `Create` 先 `EnsureElevated`（`:81`），失败文案「创建虚拟网卡需要管理员权限的独立聚合核心：…」 |
| 同参数重复 create 必须幂等 | 桌面不做去重，直接透传；重复点击只是复用同一 keeper（引擎保证） |
| remove 幂等（20s 超时） | 桌面 30s 预算（`:19`）留余量，`Remove`/`Shutdown` 都把「无 keeper」当成功 |
| `host.shutdown` 必须停 vnic keeper | 桌面在 `engineService.Shutdown()` **之前**调用 `VirtualAdapterService.Shutdown()`（`main.go` 补丁见 §5.1） |
| 校验 `InterfaceName` 非空 / IPv4 / `1<=Prefix<=32` / `576<=MTU<=65535` | 桌面在 `vnic_config.go:46–51`、`:119–136` 做同款前置校验（引擎仍会独立校验） |

### 3.4 §4 前端交叉期望（只读核对，未改前端）

- `desktop/frontend/src/platform/services.ts:7` 已 import `virtualadapterservice`，`:24` 从 `models` 导入 `VirtualAdapterStatus`，`:346–349` 暴露 `virtualAdapter.create/status/remove`。
- `VirtualAdapterPanel.test.tsx:32–42` 的 mock 形状 `{state, interfaceName, address, prefixLength, mtu, adapterGuid, createdAt, lastError}`（present 样例：`HypoMux-VNIC` / `10.66.0.1` / `24` / `1420` / RFC3339 `createdAt`）与 `virtual_adapter.go:32–41` **逐字一致**。
- 前端 `components/vnic/VirtualAdapterPanel.tsx:327–328` 的默认名/地址/前缀与 `vnic_config.go:17–23` 一致。

### 3.5 与 `protocol/v1/manifest.json` 的交叉核对（只读，task-9 已落该方法列表）

| manifest 条目（`protocol/v1/manifest.json:75–92`） | 声明语义 | 我的常量/行为 | 结论 |
| --- | --- | --- | --- |
| `vnic.create` | `state_guarded` / `administrator` / `caller_deadline_and_host_context` | `MethodVNICCreate`（`vnic.go:11`）；`Create` 传 60s caller deadline（`virtual_adapter.go:67`）并先 `EnsureElevated`（`:81`） | ✅ 一致 |
| `vnic.status` | `read_only` / `none` / `not_applicable` | `MethodVNICStatus`（`vnic.go:12`）；`Status` 不启动任何进程、无核心时直接 absent | ✅ 一致 |
| `vnic.remove` | `safe_retry` / `administrator` / `caller_deadline_and_host_context` | `MethodVNICRemove`（`vnic.go:13`）；`Remove` 幂等、30s deadline | ✅ 一致 |

三个方法名与位置（`tun.deactivate` 之后、`dns.resolve` 之前）和契约 §2 完全吻合。
`engine/internal/api/v1/**` 的 `vnic*.go` 目前**尚未落地**（glob `engine/**/vnic*.go` 无结果），因此引擎侧结构体与我的 JSON tag 还无法机械比对；见 §7.4。

### 3.6 客户端复用关系（已与 Lead 确认，需 engine 侧配合）

`Create`/`Status`/`Remove` 全部走 `s.engine.client`（即 `NewVirtualAdapterService(engine *EngineService)` 注入的那个**已经连接的 EngineService 共享客户端**，`virtual_adapter.go:60`、`:110`、`:128`），**不新建连接、不新起 Core 进程**。由此产生一条对引擎侧的硬要求：

> `vnic.create` 必须在**任何运行模式下**都能被处理，不要求当前处于 `tun_tcp_pool`（或任何 TUN）模式；`vnic.status`/`vnic.remove` 同理。

Lead 已把该条同步给 engine-vnic-dev。桌面侧因此**不做**任何「先切换到 TUN 模式」的前置动作，也不会因模式不符而提前拒绝。

---

## 4. 关键实现决策与理由

1. **同包直接访问 `EngineService.client`**：`client` 是私有字段，但 `mtu.go:137/287/300` 在同一包内直接使用是既有先例；因此**不需要**给 `EngineService` 加任何导出访问器，也**没有**改动 `engine.go`（满足 Lead 第 4 条约束）。
2. **能力探测顺序**：`EnsureElevated` **之前**先用 `client.Hello()` 做一次「已连接且缺 `vnic.create`」的快速拒绝（`:75`），**之后**再用 `EnsureElevated` 返回的 `Hello.Capabilities` 复检（`:85`）。理由：`EnsureElevated` 可能重启核心并打断正在运行的代理会话，不能为一个必然失败的请求先付这个代价；而复检覆盖「核心刚被拉起」的情况。用法与 `mtu.go:287–293` 的能力检查先例一致。
3. **无核心时 `Status`/`Remove` 返回 `absent` 而不是错误**：`connections.go:45` 的同款降级理由——主页会轮询，把「核心没开」当错误会持续弹 toast。
4. **错误码映射**：`disconnected`（`client.go:543` 合成）、`invalid_state`（引擎无效状态）、`method_not_found`（旧核心）三种 `RemoteError.Code` 都归一为 `absent`；判定用 `errors.As` + `*engineclient.RemoteError`（`engine.go:874–875`、`:1148–1156` 先例）。
5. **配置字节与摘要同源**：`sha256` 直接对将要写入的 `data` 计算（`vnic_config.go:92`），落盘同一 `data`（`:99`），避免引擎侧校验 `config_sha256` 时不一致。
6. **地址/参数一致性**：配置里的 `interface_name`/`address` 与 `VNICCreateParams` 使用**同一个** `plan`（`virtual_adapter.go:89–96`），配置内 `address` 形如 `10.66.0.1/24`，参数里拆成 `address` + `prefix_length`。
7. **原子写入**：`.tmp`（`0o600`）+ `os.Rename`，失败 `os.Remove`；目录 `0o755`，与 `tun_config.go:285–292` 一致（Windows 上 `os.Rename` 会覆盖既有目标）。
8. **`Shutdown` 幂等且尽力而为**：`closed` 置位用 `sync.Mutex` 保护；核心未连接或已关闭时直接返回，失败不阻塞窗口销毁。
9. **超时预算**：create 60s（契约）、remove 30s（引擎 20s 预算 + 余量）、status 10s、引擎侧 `startup_timeout_ms` 20000（对齐 `engine.go` 的 TUN 启动值）。

---

## 5. 决策项与结论（Lead 已于 m00266 全部定案）

### 5.1 【已落地】`main.go` 注册与关闭顺序（Lead 于 T1 定稿后授权）

Lead 批示（m00266）：T1 已把 ai 相关调用删掉、`main.go` 释放，授权我基于**磁盘上当前版本**（原 388 行）修改。以下即最终落地的三处改动（文件现为 391 行）：

```go
// 1) 构造：放在 services 构造区（最终落在 main.go:150，紧跟 engineService 构造）
virtualAdapterService := services.NewVirtualAdapterService(engineService)

// 2) 注册：最终落在 main.go:188（紧跟 tunService 的注册 :187）
app.RegisterService(application.NewService(virtualAdapterService))

// 3) 关闭回调（最终落在 main.go:152–158）：必须排在 engineService.Shutdown() 之前
desktop := wails.NewDesktopHost(app, mainWindow, startSilent, func() {
	if diagnosticsService != nil {
		diagnosticsService.Shutdown()
	}
	virtualAdapterService.Shutdown() // ← 新增（在 engineService.Shutdown() 之前，否则核心客户端已关闭，remove 到不了引擎）
	engineService.Shutdown()
}, func() bool {
	return settingsService.Get().CloseToTray
})
```

> 落地位置（当前 `main.go`，已 grep 复核）：构造 `:150`、注册 `:188`（紧跟 `tunService` 注册 `:187`，在 `blockedDomainService` `:189` 之前）、关闭回调 `:156`（在 `engineService.Shutdown()` `:157` 之前）⇒ **关闭顺序要求满足**。
> 三项自检：① **无新增 import**（`services`、`application` 均已在 `main.go:12/16` 导入，无未使用导入）；② **无未使用变量**（`virtualAdapterService` 在 `:156` 与 `:188` 被使用两次）；③ **闭包捕获合法**（闭包捕获外层已初始化的变量本身而非 `:=` 新变量，变量在闭包创建前的 `:150` 初始化，无 nil 风险）。
> **这是 CI 阻塞项**：`build.yml:142` 先跑 `wails3 generate bindings -clean=true -ts -i`，只有注册过的服务才会生成 `bindings/.../services/virtualadapterservice.ts`；`platform/services.ts:7` 已经 import 它 ⇒ 本次注册正是该前置条件，现已满足。

### 5.2 【已定案 · 已知限制】地址默认值不做「占用探测」

Lead 批示（m00266）：**v1 就用常量 `10.66.0.1`**，不做同网段占用探测。理由（Lead）：探测需要枚举本机路由/ARP，收益低、失败面大；地址真被占用时引擎会返回 `tun_failed`，前端会把 `conciseDiagnosticMessage` 摘要展示给用户，用户可自行改地址。

⇒ **已知限制（v1）**：`address` 为空时固定使用 `10.66.0.1`（`vnic_config.go:121–123`），**不检测该地址是否已被本机其它适配器占用**；冲突时表现为创建失败并把引擎诊断信息透出（`Create` 的错误文案为「创建虚拟网卡失败：%w」）。契约 `:128` 括注里「挑一个未被占用的」按上述定案**实现为常量**（记录于本报告，无隐藏行为）。

### 5.3 【已定案】`EnsureElevated` 的副作用与 UX

若核心此前以**非提权**方式运行，`Create` 会把它重启为管理员进程，正在进行的代理会话会短暂中断（桌面侧无法避免，否则引擎只会回 `elevation_required`）。Lead 批示（m00266）：**前端不改交互，只加一行提示文案**——「创建虚拟网卡需要管理员权限，可能弹出 UAC，并会短暂重启聚合核心」，由 `frontend-vnic-dev` 在对话框内落地（task-10 范围）。本任务（桌面侧）**无新增改动**。

### 5.4 是否需要 `virtual_adapter_other.go`

契约 `:104` 说「若需要非 Windows 兜底」。事实：我只依赖 `settingsDirectory`（`settings.go:136`）、`normalizeTunStack`（`tun_config.go:378`）、`resolveRuntimeAsset`（`tun_config.go:539`），三者所在文件**都没有 build tag**，因此新增代码在非 Windows 上同样能编译（只是运行时找不到 `sing-box.exe`，会以明确错误失败）。Go 校验 job 跑在 `windows-2025`（`build.yml:33`）⇒ **判定不需要**；若要与仓库 `*_other.go` 风格对称，请指示。

### 5.5 `Status` 轮询节流

`Status` 每次都会走一次 RPC（10s 超时）。若前端在 HomePage 以秒级频率轮询，建议前端做节流/可见性判断（task-10 范围）；桌面侧不做缓存以免状态过期。

---

## 6. 静态自查证据（命令 + 结果）

全部用 read 工具与 pwsh **只读**脚本完成；写文件只用 `read`/`edit`/`write` 工具（未用 `Set-Content`/`Out-File`）。

| 检查项 | 方法 | 结果 |
| --- | --- | --- |
| 契约签名/字段逐字 | 对照 `00-frozen-interface.md:102–131`、`:52–100` 人工逐行比对 | ✅ 见 §3.1/§3.2 |
| 被调符号存在 | grep 确认 `settingsDirectory`(`settings.go:136`)、`normalizeTunStack`(`tun_config.go:378`)、`resolveRuntimeAsset`(`tun_config.go:539`)、`Client.Request`(`client.go:379`)、`Client.Hello`(`:168`)、`Client.EnsureElevated`(`:188`)、`RemoteError`(`:24`，指针接收者 `:30`)、`Request` 以 `return result.Error` 返回 `*RemoteError`(`:445`) | ✅ 全部存在且类型匹配 |
| 包内符号冲突 | grep `VNIC`/`VirtualAdapter` 覆盖 `desktop/**/*.go`：除新文件外仅命中既有 `isVirtualAdapter`(`adapters.go:142`)、`HideVirtualAdapters`(`settings.go:41`) 等**不同名**符号 | ✅ 无冲突 |
| 导入是否全被使用 | 逐 import 统计 `pkg.` 出现次数：`virtual_adapter.go` — context 8、errors 4、fmt 4、slices 2、strings 2、sync 1、time 3、engineclient 7；`vnic_config.go` — sha256 1、hex 1、json 1、fmt 8、net 1、os 4、filepath 2、strconv 1、strings 5；`vnic.go` — context 3 | ✅ 无未使用导入 |
| 括号/花括号配平 | 逐字符计数 | ✅ 三者 brace=0、paren=0 |
| gofmt 风格（缩进/行尾/换行） | pwsh 逐行扫描：`final_newline=True`、`crlf=False`、`leading_space_indent=False`、`trailing_ws_lines=0`（三文件） | ✅ |
| gofmt 对齐（结构体列） | 自写列宽校验：`VirtualAdapterStatus` n=8 uniform、`VirtualAdapterService` n=3 uniform、`VNICCreateParams` n=8、`VNICStatus` n=8、`VNICCreateResult` n=2、`vnicConfigPlan` n=7，**列宽全部唯一** | ✅ |
| gofmt 对齐（键值 run） | 按「注释/空行/多行 value 打断 run、run 内列宽 = max(token)+1」规则校验（规则先用 `engine/internal/dns/config.go:11–18`、`tun_config.go:251–268`、`mtu.go:16–30` 取证） | ✅ 三文件 `colon_misalign=0` |
| 无死代码 | 每个新私有函数在 `desktop/**` 内被引用次数 ≥2（定义+调用） | ✅ |
| 中文错误文案可读性 | 与既有 `engineclient` 中文错误风格一致 | ✅ |

**结论**：除「未编译/未运行」外，上述可静态验证项**全部通过**。

---

## 7. 未验证项（如实声明）

1. **未编译、未测试、未 `go vet`**：本机无 Go 工具链（`go` 不存在），`go -C desktop test ./...`、`go vet ./...` 均未运行。
2. **未真正跑 `gofmt -l`**：CI（`build.yml:168–186`）以「列出即失败」为准；我只能用上表的近似脚本 + 人工保证，**不能替代真实 gofmt**。
3. **Wails 绑定未在本机生成**：`main.go:188` 已注册，`wails3 generate bindings` 的**前置条件已满足**；但本机无 `wails3`/Go/node ⇒ `bindings/.../services/virtualadapterservice.ts` 与 `models` 里的 `VirtualAdapterStatus` 仍**未实际产出**，生成文件里的方法名/参数名（预期 `Create(interfaceName, address)`、`Status()`、`Remove()`、`Shutdown()`）也未经机器核对。生成后 `platform/services.ts:7` 的 import 才能通过类型检查。
4. **引擎侧 `vnic.*` 未联通**：契约 §2 的三个 RPC 由 engine-vnic-dev（task-9）实现；我的调用路径**未经真实引擎验证**（含 `accepted`/`state` 实际取值、幂等行为）。
5. **真实适配器创建未验证**：需管理员权限 + `bin/sing-box.exe` + `wintun.dll`，本机无法执行；`resolveRuntimeAsset("sing-box.exe")` 的返回值也未被运行时验证。
6. **`json.MarshalIndent` 产出的配置未被 sing-box 解析验证**：字段名（`interface_name`/`auto_route`/`strict_route`/`stack`）与 `tun_config.go:230–238` 既有实现一致，但未实跑。
7. **`VNICCreateParams` 的 JSON 序列化未被真实 `Request` 验证**（no Core）：仅由 tag 与结构体字段保证。
8. **`main.go` 的 3 行改动未编译验证**：依赖 `services.NewVirtualAdapterService`（`virtual_adapter.go:52`）与既有 `application.NewService` 用法；§5.1 的三项自检（无新增 import / 无未使用变量 / 闭包捕获）是人工判定，未经 `go build` 确认。

---

## 8. 给 integration-verifier 的复核清单（可复制）

```powershell
# 1) 契约签名逐字
Select-String -Path desktop\internal\services\virtual_adapter.go -Pattern 'func (NewVirtualAdapterService|\(s \*VirtualAdapterService\) (Create|Status|Remove|Shutdown))'
Select-String -Path desktop\internal\engineclient\vnic.go -Pattern 'func \(c \*Client\) VNIC'

# 2) JSON tag 与契约一致（应输出 8+8+8 个 tag）
Select-String -Path desktop\internal\services\virtual_adapter.go,desktop\internal\engineclient\vnic.go -Pattern 'json:"'

# 3) 能力/方法名三方一致（桌面 ↔ manifest，manifest 由 task-9 更新）
Select-String -Path desktop\internal\engineclient\vnic.go -Pattern 'MethodVNIC'
Select-String -Path protocol\v1\manifest.json -Pattern 'vnic\.(create|status|remove)'

# 4) 引用链与关闭顺序（virtual_adapter.go 已由 main.go:150 构造、:188 注册）
Select-String -Path desktop\**\*.go -Pattern 'NewVirtualAdapterService'
Select-String -Path desktop\main.go -Pattern 'virtualAdapterService\.Shutdown|engineService\.Shutdown'   # 前者行号须小于后者

# 5) gofmt（有 Go 的机器上）
gofmt -l desktop\internal\services\virtual_adapter.go desktop\internal\services\vnic_config.go desktop\internal\engineclient\vnic.go
```

> 注：第 4 条现已落地（`main.go:150` 构造、`:188` 注册、`:156` 关闭回调）；顺序断言应显示 `virtualAdapterService.Shutdown()`（`:156`）**小于** `engineService.Shutdown()`（`:157`）。
