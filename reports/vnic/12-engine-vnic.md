# 12 · 引擎侧虚拟网卡（vnic）实现报告

- 任务：`task-9`（writeScopes = `["engine"]`），负责人 `engine-vnic-dev`
- 契约来源：`reports/vnic/00-frozen-interface.md`
- 环境：本机原本没有 Go 工具链，但执行期间从 `mirrors.aliyun.com/golang` 取到官方 go1.22.5 发行包，因此**已完成真编译、真测试**：`go test -count=1 ./...`、`go test -race`、`go vet ./...` 全部通过，`gofmt -l` 无输出。详见 ④。
- 未 commit、未 push；未触碰 `desktop/**`、`.github/**`、`nsis/**`、`desktop/build/**`

---

## ① 改动清单（文件:行号）

### A. 新增文件

| 文件 | 行数 | 内容 |
|---|---|---|
| `engine/internal/tun/interface_name.go` | 51 | `expectedTunInterfaceName(Config) (string, error)`（:16）、`configuredTunInterfaceName(path string) (string, error)`（:27）。解析 staged 配置里第一个 `type=="tun"` 的 `interface_name`；`Config.InterfaceName` 非空时优先。 |
| `engine/internal/vnic/manager.go` | 343 | 新增 `engine/internal/vnic` 包：状态常量、`Meta`(:56)/`Status`(:90)、`Sidecar` 接口(:48)、`Manager`(:102，字段含 `deployed`/`failed`)。`Meta.Validate()`（:68）、`NewManager`（:114）、`Create`（:136）、`Status`（:188）、`Remove`（:196）、`stopLocked`（:216）、`handleUnexpectedExit`（:270）、`statusLocked`（:280）、`matchesLocked`（:309）、`deriveState`（:323）、`tunState`（:338）。 |
| `engine/internal/vnic/manager_test.go` | 370 | 11 个单元测试 + `fakeSidecar` 假件（含 `stubborn` 开关，用于真实覆盖「keeper 拒绝停止」的超时路径）。 |
`engine/internal/vnic` 的 import 只有 `context`/`errors`/`fmt`/`net`/`strings`/`sync`/`time` + 本仓库 `engine/internal/tun`；**不 import `api/v1`**，故 `server → vnic → tun` 无循环依赖。

### B. 修改文件

**`engine/internal/api/v1/types.go`**（304 → 352 行）
- :31-33 常量 `MethodVNICCreate/MethodVNICStatus/MethodVNICRemove`（紧跟 `MethodTunDeactivate` 之后）
- :61-63 `capabilities` 切片按同序插入三项（位于 `MethodTunDeactivate` 与 `MethodDNSResolve` 之间）
- :286-308 `VNICCreateParams`（8 字段）+ `func (p VNICCreateParams) Config() tun.Config`
- :310-321 `VNICStatus`（7 字段）
- :323-326 `VNICCreateResult`

**`engine/internal/server/server.go`**（878 → 1071 行）
- :11 新增 import `net`（`net.ParseIP`）；:25 新增 import `engine/internal/vnic`
- :47-51 `vnicManager` 接口（`Create`/`Status`/`Remove`）
- :61 `Server.vnic vnicManager` 字段
- :89 `server.vnic = vnic.NewManager(tun.NewSupervisor(), server.handleVnicLog, nil)`（**第二个独立 supervisor**）
- :224-229 三个 `case`，插在 `case api.MethodTunDeactivate`(:222) 与 `case api.MethodDNSResolve`(:231) 之间
- :814-893 `createVNIC`（提权 → JSON → 参数校验 → 安全策略授权 → stale 重试 → `tun_failed` 兜底）
- :895-897 `statusVNIC`
- :902-915 `removeVNIC`（失败码 `stop_failed` :909）
- :919-934 `validateVNICCreate`
- :936-952 `vnicStatus`（`vnic.Status` → `api.VNICStatus`，`CreatedAt` 非零才格式化为 RFC3339）
- :958-973 `isStaleVNICAdapterError`
- :976-981 `handleVnicLog`（`log.record`，`component="vnic-keeper"`）
- :1012-1046 `stopProxyForHostExit()` 内、`tun.Stop` 之后追加 vnic `Remove`（20s 超时）

**`engine/internal/tun/supervisor.go`**（714 → 751 行）
- :31-41 常量块重构：新增 `ManagedInterfaceName = "HypoMux-Tun"`(:36) 与 `VNICInterfaceName = "HypoMux-VNIC"`(:37)，`tunInterfaceName = ManagedInterfaceName`(:41)
- `Config` 结构新增 `InterfaceName string` 字段（在 `RequireProtectedConfig` 之后）
- :120 `startupReady func(string, string) bool`（原 `func(string) bool`）
- :198 `expectedInterface, err := expectedTunInterfaceName(normalized)`
- :203 `configuredTunIPv4Address(normalized.ConfigPath, expectedInterface)`
- :290 `s.startupReady(expectedInterface, expectedAddress)`
- :357 `configuredTunIPv4Address(path string, expectedName string) (string, error)`：去掉硬编码 `HypoMux-Tun` 比较，改为「非空期望名才过滤」
- :395 `tunInterfaceWithExpectedAddress(interfaceName string, expectedAddress string)`：空名回退到 `tunInterfaceName`
- 就绪超时文案改用 `expectedInterface`
- `normalizeConfig` 返回值新增 `InterfaceName: strings.TrimSpace(config.InterfaceName)`

**`engine/internal/tun/readiness_windows.go` / `readiness_other.go`**（各 :5）：`tunPlatformReady(interfaceName string, expectedAddress string) bool`

**`engine/internal/tun/supervisor_test.go`**（405 → 440 行）
- 4 处 `startupReady` 匿名函数签名改 `func(string, string) bool`（:72/:103/:309/:312）
- `TestSupervisorReturnsWhenTunInterfaceIsReady` 增加 `observedInterface == "HypoMux-Tun"` 断言
- `TestSupervisorRejectsMissingOwnedAddressBeforeCleanup`（:118）**按原意修补**：补 `config.InterfaceName = ManagedInterfaceName`（:124），否则改造后该测试会因期望名从配置解析出来而不再失败（详见 ③-B）
- **新增** `TestSupervisorAcceptsConfiguredInterfaceNameOverride`（:134-157，验证 `HypoMux-VNIC` 可被接受）

**`engine/internal/tun/fakeip_windows_test.go:99`**：`startupReady` 匿名函数签名同步改 `func(string, string) bool`

**`engine/internal/server/server_test.go`**（1018 → 1314 行）
- import 新增 `engine/internal/platform`、`engine/internal/vnic`
- helper `newVNICLifecycleServer`（:1022-1029）、`vnicCreateRequest`（:1031-1038）、常量 `testVNICCreateParams`（:1040-1048）
- 9 个新测试：`TestServerCreatesVirtualAdapterIdempotently`(:1050)、`TestServerReplacesVirtualAdapterWhenParametersChange`(:1106)、`TestServerRejectsInvalidVirtualAdapterParameters`(:1134)、`TestServerRequiresElevationForVirtualAdapterLifecycle`(:1160)、`TestServerReportsVirtualAdapterStatusAndRemoval`(:1179)、`TestServerReportsVirtualAdapterStartFailure`(:1236)、`TestServerRetriesVirtualAdapterAfterStaleWintunFailure`(:1251)、`TestStaleVirtualAdapterClassificationExcludesMainTunAdapter`(:1272)、`TestHostExitStopsVirtualAdapterKeeper`(:1290，验证引擎退出路径确实停掉 vnic keeper 并回到 `absent`)

**`engine/internal/api/v1/contract_test.go`**（325 → 333 行）
- :214-215 `decodeRequestParams` 新增 `case MethodVNICCreate: target = &VNICCreateParams{}`
- :229-230 无参白名单插入 `MethodVNICStatus, MethodVNICRemove`
- :269 `decodeResult` 新增 `case MethodVNICCreate: target = &VNICCreateResult{}`、:271 `case MethodVNICStatus, MethodVNICRemove: target = &VNICStatus{}`

**`protocol/v1/manifest.json`**（162 → 180 行）：`methods` 在 `tun.deactivate` 与 `dns.resolve` 之间插入 3 条（第 10/11/12 个）；`error_codes` **未改**（仍 14 个）

**`protocol/v1/fixtures/messages.json`**（901 → 997 行）
- `engine_hello_response.result.capabilities`(:50-52) 插入 3 个新方法名
- :406-497 追加 6 条样例：`vnic_create_request`/`vnic_create_response`/`vnic_status_request`/`vnic_status_response`/`vnic_remove_request`/`vnic_remove_response`
- `vnic.status`/`vnic.remove` 的 request **不含 `params` 字段**（契约测试断言 `len(request.Params)==0`）

---

## ② 契约落地对照表

| 契约条目（00-frozen-interface.md） | 落地位置 | 状态 |
|---|---|---|
| §2 方法插入在 `tun.deactivate` 后、`dns.resolve` 前 | `manifest.json` methods；`types.go:31-33`；`types.go:61-63`；`server.go:224-229` | ✅ |
| §2 `error_codes` 保持 14 个 | `manifest.json:164-179` 未改动 | ✅ |
| §2.1 三个 `MethodVNIC*` 常量，字符串逐字一致 | `types.go:31-33` | ✅ |
| §2.2 `VNICCreateParams` 8 字段与 JSON tag | `types.go:289-298` | ✅ |
| §2.2 `func (p VNICCreateParams) Config() tun.Config` | `types.go:300-308` | ✅ |
| §2.2 `VNICStatus` 7 字段与 JSON tag | `types.go:312-321` | ✅ |
| §2.2 `VNICCreateResult{Accepted, VNIC}` | `types.go:323-326` | ✅ |
| §2.2 `vnic.status`/`vnic.remove` 的 result = `VNICStatus` | `server.go:895-914` | ✅ |
| §2.2 `vnic.remove` 成功 → `state=absent` | `server.go:902-914` + `vnic/manager.go:196` | ✅ |
| §2.2 五个状态字符串 `absent/creating/present/removing/failed` | `vnic/manager.go` 常量 + `deriveState` | ✅ |
| §2.3 第二个独立 `tun.NewSupervisor()` | `server.go:89` | ✅ |
| §2.3 `authorizeTunConfig(params.Config())` | `server.go:845` | ✅ |
| §2.3 `!s.identity.Elevated` → `elevation_required` | `server.go:820-827` | ✅ |
| §2.3 已 present 且参数不同 → **先 Stop 再启** | `vnic/manager.go:151`（`Create` 内无条件 `stopLocked`） | ✅（二选一，已选定） |
| §2.3 同参数重复调用**幂等** | `vnic/manager.go:144-146` 早返回；测试 `TestManagerCreateIsIdempotentForTheSameRequest`、`TestServerCreatesVirtualAdapterIdempotently` | ✅ |
| §2.3 复用 `isStaleTunAdapterError` 一次性重试 | `server.go:868-880`，分类函数 `server.go:958-973` | ✅ |
| §2.3 `vnic.remove`：Stop 20s、幂等、未创建返回 absent 不报错 | `vnic/manager.go:196-207`；测试 `TestManagerRemoveIsIdempotentAndReportsAbsent`、`TestServerReportsVirtualAdapterStatusAndRemoval` | ✅ |
| §2.3 `host.shutdown`/退出路径停 vnic，顺序在 tun 之后 | `server.go:1012-1046`（`stopProxyForHostExit`，由 `Run` 的 `defer` 兜底，`host.shutdown` 走同一路径） | ✅ |
| §2.3 校验 `InterfaceName`/IPv4/`1≤PrefixLength≤32`/`MTU∈[576,65535]` → `invalid_params` | `server.go:919-934`；`vnic/manager.go:68` 二次防御 | ✅ |
| §2.3 不与 `tun.Recover()`/stale 清理冲突 | 见 ③ | ✅ |
| §2.4 契约测试方法列表与 fixtures 同步 | `contract_test.go`、fixtures 6 条 | ✅ |
| Lead 追加（team message）：**不加** `tun_tcp_pool`/runtime 前置门禁 | `createVNIC` 只做 elevation + 参数 + 授权，无 runtime/proxy/mode 判断 | ✅ |
| Lead 追加：启动失败返回 `tun_failed` | `server.go:884`（**覆盖契约 §2.3 里并列表述的 `start_failed`**，见 ③-D） | ✅ |
| Lead 追加：`vnic.create` 必须在 ≤20s 内返回 | 启动超时取调用方 `startup_timeout_ms`；`tun.normalizeConfig` 上限 60s，但桌面端传 20000ms ⇒ 引擎自身 20s 先到期 | ⚠ 见 ④ |
| §3 桌面契约 | 不属于本任务（`desktop-vnic` 负责） | — |
| §4 前端契约 | 不属于本任务（`frontend` 负责） | — |

### 状态映射实现（§2.2 后半）

`vnic.Status.State` 由 `deriveState(tun.Status.State)` 得出：
`Stopped→absent`（未创建）、`Starting→creating`、`Running→present`、`Stopping→removing`、`Failed→failed`、其余→`absent`。
契约 §2.2 的「`Stopped` → absent（未创建过）或 **failed（曾失败）**」由 `Manager` 的 `failed` 标志承担：`Create` 失败或 `stopLocked` 无法把 keeper 停掉时置 `failed=true`，成功 `Create` 或成功 `Remove` 清除；`statusLocked` 在 `failed` 为真时把最终状态改写为 `failed`，并把 `lastError` 透出为 `LastError`。**注意**：`tun.Supervisor` 自身一旦停稳就报 `StateStopped`、不保留失败历史，所以「曾失败」的记忆必须由引擎侧持有。

`Manager.matchesLocked`（`vnic/manager.go:309-317`）比较 `Executable`（大小写不敏感）/`ConfigPath`/`ConfigSHA256`/`InterfaceName`/`Address`/`PrefixLength`/`MTU`，**不比较 `StartupTimeout`**：只调整启动超时的重复请求仍走幂等早返回（桌面端固定传 20000ms，无影响）。失败路径会把请求描述符保留在 `m.meta`（`Create` 在 `Activate` 之前就赋值），因此错误响应里的 `vnic` 能回显调用方请求的网卡名与地址。

---

## ③ 冲突分析结论

### A. 与 `tun.Recover()` / stale 清理的冲突：**无冲突**

`tun.Recover()` 只是 `return cleanupPlatform(ctx)`（`engine/internal/tun/recover.go:10`）。`cleanup_windows.go` 中所有匹配都是**按名字精确匹配**：
- PowerShell 路由清理：`InterfaceAlias -eq 'HypoMux-Tun'`（`cleanup_windows.go:21-26`）
- 设备清理：`FriendlyName -eq 'HypoMux-Tun' -and InstanceId -like '*WINTUN*'`（:29）
- Go 侧 `hasOwnedTunDevice()` 比较 `SPDRP_FRIENDLYNAME`/`DEVICEDESC` 与 `tunInterfaceName`（:129）+ InstanceID 含 `WINTUN`（:136）

`HypoMux-VNIC` 与 `HypoMux-Tun` 字符串不等，故 `cleanupPlatform` 不会匹配到 vnic 适配器；且 `cleanupPlatform` 先跑 `hasOwnedTunDevice()` 快速判断，vnic 存在时它会返回 `false` 直接跳过。**结论：`tun.Recover()` 不会误杀 vnic keeper。**

同理 `engine/internal/platform/mtu_windows.go:26` 的 MTU 保护脚本只拒绝 `$a.Name -eq 'HypoMux-Tun'`，不会阻挡 vnic。

### B. 既有测试的**已修正风险点：1 处（证据确凿，已修补）**

`tun.Supervisor` 的就绪判定原先硬编码 `net.InterfaceByName("HypoMux-Tun")`，这会让任何非 `HypoMux-Tun` 的 inbound 名**永远无法就绪**。改为「期望名可由调用方传入」是 vnic 可行的前提，但改变了 `Supervisor` 的可观测语义。

**曾静默破坏的既有测试**：`engine/internal/tun/supervisor_test.go:118` 的 `TestSupervisorRejectsMissingOwnedAddressBeforeCleanup` 原本依赖「`tunInterfaceName` 硬编码」来失败——它把配置写成 `interface_name: "other-tun"`，且**没有设置 `Config.InterfaceName`**（`testConfig` 返回的 `Config` 该字段为空）。改造后：期望名会从配置解析为 `"other-tun"`，`configuredTunIPv4Address` 匹配到该 inbound 并取出 `10.255.255.1`，随后 `testSupervisor("stable")` 的 `startupReady` 恒返回 `true`，于是在 `readyStableFor` 之后**成功就绪**——`err == nil`，测试必然失败。

**已按原意修补**：给该测试加上 `config.InterfaceName = ManagedInterfaceName`（`supervisor_test.go:124`）。这样「调用方要求的适配器」(`HypoMux-Tun`) 与配置里声明的适配器 (`other-tun`) 不一致 → `configuredTunIPv4Address` 返回 `staged configuration has no IPv4 address for HypoMux-Tun` → `Activate` 在触发任何网络清理**之前**失败，`status.State == StateFailed`、`cleanupCalls == 0` 三条断言全部恢复成立，且失败原因回到「没有自有 IPv4 地址」这一原始语义。

同时新增 `TestSupervisorAcceptsConfiguredInterfaceNameOverride`（`supervisor_test.go:134-157`）正向覆盖「期望名来自 `Config.InterfaceName`」的 vnic 路径。

主 TUN 路径不受影响：`desktop/internal/services/tun_config.go:231` 生成的配置里 `interface_name` 就是 `"HypoMux-Tun"`，而 `TunActivateParams.Config()` 不设置 `InterfaceName`，于是走「从配置解析期望名」的旧路径，与改造前逐字一致。

### C. `tunInterfaceName` 兜底策略

`configuredTunInterfaceName` 在「配置里没有任何 tun inbound」时返回常量 `tunInterfaceName`（`HypoMux-Tun`），随后 `configuredTunIPv4Address` 会对它报 `staged configuration has no IPv4 address for HypoMux-Tun`——与改造前的报错文案与行为**完全一致**。这条路径只在配置完全不含 tun inbound 时走到，vnic 不会命中。

### D. 错误码分歧（已按 Lead 最新指示执行，与冻结契约字面有出入）

- 冻结契约 §2.3 的候选码列表同时包含 `start_failed` 与 `stop_failed`；Lead 随后明确要求：**启动失败用 `tun_failed`**（与既有 `tun.activate` 失败路径一致），参数非法用 `invalid_params`，未提权用 `elevation_required`，停机失败用 `stop_failed`。
- 本实现按 Lead 指示：`server.go:884` 用 `tun_failed`，`server.go:909`（remove 失败）用 `stop_failed`。三个码都在 `manifest.json` 的 14 个既有码之内，**未新增错误码**。
- 未使用的契约候选码：`invalid_state`、`start_failed`（vnic 路径不返回它们——这正是 Lead 要求的「与运行模式解耦、不做模式门禁」的结果）。

### E. 与其它队友工作的边界

`git status --porcelain` 显示工作树中大量 `desktop/**` 删除/新增（AI 移除、桌面 vnic）来自其它任务。本任务只触及 ① 里列出的 14 个文件（11 个修改 + 3 个新增），未触碰 `desktop/**`、`.github/**`、`nsis/**`。

### F. 未实现/降级项

- `VNICStatus.AdapterGUID` 始终为空（`omitempty` 下不出现在 JSON 里）。原因：`engine/internal/platform` 与 `engine/internal/tun` 都没有「按适配器名取 GUID」的 helper，新增 Windows 设备枚举代码会引入本轮无法端到端验证的 P/Invoke 路径。契约中该字段本就是 `omitempty` 的可选信息，桌面端契约也未要求它非空。
- 未新增 `vnic.state_changed` 事件（契约未要求，`manifest.json` 的 `events` 数组未改动）。`vnic.status` 是唯一的现状查询入口。

---

## ④ 验证情况

### 已实际执行的验证（**已推翻任务书里的「本机无 Go」前提**）

任务书与契约 §0 都假设本机没有 Go。实际执行时发现可以从国内镜像取到官方工具链，于是**真编译、真跑测试**：

1. 从 `https://mirrors.aliyun.com/golang/go1.22.5.windows-amd64.zip` 下载 72.7 MB 官方发行包，解压到 `%TEMP%\go-full\go`，`go version` = `go1.22.5 windows/amd64`。
2. 环境：`GOROOT=%TEMP%\go-full\go`、`GOFLAGS=-mod=mod`、`GOPROXY=https://goproxy.cn,direct`。`engine/go.mod` 的 `toolchain go1.26.6` 指令触发了一次 1.26.6 工具链下载（成功），依赖 `golang.org/x/{sys,net,text}`、`github.com/Microsoft/go-winio` 也从代理下载成功。
   - **`engine/go.mod` 与 `engine/go.sum` 未被改动**（`git status --porcelain -- engine/go.mod engine/go.sum` 为空），无需还原。
3. `go -C engine test -count=1 ./...` → **exit 0**，14 个包全绿（`api/v1`、`tun`、`server`、`vnic`、`platform`、`proxy`、`dns`、`wfp`、`runtime`、`expiry`、`fileintegrity`、`diagnostic`、`cmd/hypomux-engine`；`protocol` 无测试文件）。
4. `go -C engine test -race -count=1 ./internal/vnic/ ./internal/server/ ./internal/tun/` → **exit 0**，无 data race。
5. `go -C engine vet ./...` → **exit 0**。
6. `gofmt -l`（真实 gofmt 1.22.5）over `git ls-files -- '*.go'` 里全部 engine 文件 + 三个新文件 → **无输出（干净）**。CI 的 `build.yml:168-186` 这一步会在本改动上通过。

### 测试驱动修出的 5 个真实缺陷（静态自查全部漏掉）

| # | 现象 | 根因 | 修法 |
|---|---|---|---|
| 1 | `TestManagerCreateFailureLeavesNoAdapter` 期望 `failed` 得到 `absent` | `Manager` 没有失败记忆：`Create` 失败回滚 `Stop()` 后 supervisor 状态变成 `stopped`（`supervisor.go:425/:439`），`deriveState` 只看到 stopped ⇒ `absent`。契约 §2.2 的「`Stopped` → absent（未创建过）或 **failed（曾失败）**」要求引擎自己记住失败 | `Manager` 新增 `failed bool`；`Create` 失败置 `true`（下一次 create/成功 remove 清除）；`statusLocked` 在 `failed` 时把状态改写为 `failed` |
| 2 | `TestManagerCreateFailureLeavesNoAdapter` 期望 `InterfaceName=="HypoMux-VNIC"` 得到空串 | `m.meta` 只在成功时赋值，失败时 `statusLocked` 用零值 | 把 `m.meta = meta` 提前到 `Activate` **之前**；失败回滚时仍保留请求描述符（错误响应因此能回显调用方要的网卡） |
| 3 | `TestManagerRemoveReportsStopFailure` 期望 `err != nil` 得到 `nil` | 原 `stopLocked` 在「keeper 已确实停掉、只是 `Stop()` 同时返回了错误」时把错误吞掉（`m.lastError=""` 后 `return nil`） | 重写 `stopLocked`：只有「keep 仍在运行」才算移除失败；keeper 已停就算成功。测试改为用 `stubborn` keeper（Stop 不切状态）真实覆盖超时路径 |
| 4 | `TestStaleVirtualAdapterClassificationExcludesMainTunAdapter` 断言 `stale HypoMux-VNIC device still exists` 为 stale 得到 false | `isStaleTunAdapterError`（`server.go:775-784`）只认三种文案，且第三条写死 `hypomux-tun`；清理脚本（`cleanup_windows.go:54`）只会生成 `HypoMux-Tun` 文案，所以「VNIC 具名 stale」在现实中不可达——**测试假设错了，不是实现错了** | 断言改为可达的三元组文案 `create adapter failed: open existing adapter: element not found`；保留「主 TUN 具名消息不得归属 vnic」的断言 |
| 5 | `TestServerRejectsInvalidVirtualAdapterParameters/invalid_json_body` 期望 `invalid_params` 得到 `invalid_json` | `params` 是 `json.RawMessage`，`{"params":{` 让**整条请求**非法，协议层在进入 `createVNIC` 之前就返回 `invalid_json`。真正的 `invalid_params` 只能由「params 是合法 JSON 但类型/语义不符」触发 | 删掉不可达的 `{` 子用例，改成 `"non object params": 12345`（合法 JSON、非法类型）+ 既有 6 个语义非法用例 |

### 仍未验证的部分

1. **跨平台编译（`GOOS=linux`）**：已实测 `GOOS=linux go -C engine build ./internal/tun/ ./internal/vnic/` → exit 0，覆盖 `readiness_other.go` 的签名改动。未测 `GOOS=darwin`（同一 build tag 分支，风险可以忽略）。
2. **真实运行期行为**：`vnic.create` 是否真能让 sing-box 拉起 Wintun 适配器（`auto_route:false`/`strict_route:false` 的配置由桌面端生成）、20s 启动窗口是否够用、`isStaleVNICAdapterError` 是否覆盖真实 Wintun 错误文案——**都需要真机 `sing-box.exe` + `wintun.dll` + 管理员权限，本轮无法验证**。
3. **`AdapterGUID` 语义**：字段恒为空（见 ③-F），未验证桌面端是否依赖它。
4. **桌面端/前端集成**：`desktop/**` 与 `frontend/**` 不在我的写范围，未验证端到端链路（由 desktop-vnic / frontend 队友与 integration-verifier 负责）。

---

## 附：关键设计取舍（备查）

1. **`vnic.create` 无运行模式门禁**：`createVNIC`（`server.go:814-893`）只做「提权 → JSON → 参数校验 → 安全策略授权 → `Manager.Create`」，不检查 `s.runtime` 状态、不检查 `s.proxy`、不检查聚合 mode。V-NIC 由 vnic 包自持的第二个 `tun.Supervisor` 驱动，与聚合链路完全解耦；`Status`/`Remove` 亦然。
2. **「先 Stop 再启」而非返回 `invalid_state`**：`Manager.Create` 在非幂等早返回的路径上**无条件** `stopLocked`。理由：a) 请求换适配器名时必须接管 sidecar；b) 上一次半途失败可能留下仍在运行的 keeper，而 supervisor 只允许从 `stopped`/`failed` 启动；c) `stopLocked` 在无 keeper 时是 no-op（`controller == nil` 或 `StateStopped` 直接返回 nil）。
3. **stale 重试的边界**：`isStaleVNICAdapterError` 先在 `isStaleTunAdapterError` 的匹配集内筛选，再把「消息里出现 `HypoMux-Tun` 却没有 `HypoMux-VNIC`」的情况排除掉，避免主 TUN 专属的 stale 消息被 vnic 路径认领（`cleanup_windows.go` 的 stale 文案实际只为 `HypoMux-Tun` 生成）。**已知局限**：主 TUN 路径（`activateTun`）用的仍是裸 `isStaleTunAdapterError`，理论上会把一条含 `HypoMux-VNIC` 的 stale 消息也当主 TUN 冲突；由于清理脚本只为 `HypoMux-Tun` 生成该文案，实际不可达，未改动主路径。
4. **`Manager` 是状态记忆的权威，supervisor 是进程状态的权威**：`deployed`/`failed`/`meta`/`createdAt`/`lastError` 由 `Manager` 持有；`Status()` 每次向 supervisor 现取进程状态并用 `deriveState` 映射；`failed` 是引擎侧的「曾失败」记忆（supervisor 一旦停稳就忘记），只在 `Manager` 记了失败且 supervisor 不再报 running 时透出，成功 create 或成功 remove 清除。
