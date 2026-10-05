# 61 · 引擎侧 vNIC 契约废弃评估

> 任务来源：Lead 工单 m00960（设计/侦察阶段，**本轮只读 + 只允许新建/修改报告文件**）
> 作者：teammate `engine-vnic-dev` ｜ 范围：**engine 侧**（desktop/protocol 仅做影响标注）
> 基线：`HEAD = 46680e0`（父提交 `37571e5` = 引入 vNIC 的提交）｜工作树状态：`git status --porcelain` 仅 `?? reports/`，源树全程零改动

---

## 0. 结论摘要（TL;DR）

| 问题 | 结论 |
|---|---|
| ①现有实现全貌 | 引擎 vnic 相关代码 = **2 个新文件 + 6 个文件的部分改动**，精确行号见 §1 |
| ②废弃范围 | **推荐「变体 B」**：删除 `engine/internal/vnic/` 整包 + 5 个文件里的 vnic 片段；`engine/internal/tun/interface_name.go` 及其泛化可**保留**（建议第二步「变体 A」再清） |
| ③协议影响 | manifest / fixtures / capabilities / contract_test **四处必须同步**；**已在隔离沙箱实跑验证：全绿**（见 §3.3） |
| ④替代方案 | **不保留**。现有出口池（`selected_adapter_ids`）已能消费 Hyper-V 宿主机 vNIC；引擎看不到 Hyper-V，只读 `vnic.*` 会与 desktop 的 PowerShell 查询重复且更弱 |
| ⑤已知缺陷 | F1 随整包删除**自然消失**，不需要单独修；F2/F3 一并消失；**F4（配置复制到 ProgramData）是主 TUN 共有路径，不会消失**；另发现一处**新**同类遗留（F5，见 §5.4） |

**推荐方案（一句话）**：整包删除 `engine/internal/vnic/`（713 行），连带删除 `server.go` 里 156 行 vnic 处理块与 `server_test.go` 里 293 行 vnic 测试，同步摘掉 `api/v1` 的 3 常量 + 3 capabilities + 4 个类型、`manifest.json` 的 3 个方法、`messages.json` 的 6 条 fixture；`engine/internal/protocol/protocol.go` **零改动**。合计 **-1237 行**（含测试），净变化与 `37571e5` 的 +1221 行基本抵消。

---

## 1. ① 现有实现全貌（精确 file:line）

### 1.1 `engine/internal/vnic/manager.go`（343 行，package vnic）

只 import `context/errors/fmt/net/strings/sync/time` + `engine/internal/tun`（**不 import `api/v1`，无循环依赖**）。

| 符号 | 行号 | 说明 |
|---|---|---|
| 包注释 | :1-23 | 声明「虚拟适配器只在其 keeper 进程存活期间存在」——**这是 F1 的设计前提** |
| `StateAbsent/StateCreating/StatePresent/StateRemoving/StateFailed` | :25-31 | `"absent"/"creating"/"present"/"removing"/"failed"` |
| `minMTU=576`/`maxMTU=65535`/`minPrefixLength=1`/`maxPrefixLength=32` | :33-39 | 参数阈值 |
| `stopTimeout=20*time.Second`/`stopPollInterval=25*time.Millisecond` | :41-45 | 停机预算（与契约 20s 一致） |
| `Sidecar` 接口 | :48-52 | `Activate(ctx, tun.Config) (tun.Status, error)` / `Stop(ctx) (tun.Status, error)` / `Status() tun.Status` |
| `Meta` 结构 | :56-65 | `Executable, ConfigPath, ConfigSHA256 string; StartupTimeout time.Duration; InterfaceName, Address string; PrefixLength, MTU int` |
| `Meta.Validate()` | :68-86 | 名字非空 / IPv4 / prefix 1..32 / MTU 576..65535 |
| `Status` 结构 | :90-99 | `State, InterfaceName, Address string; PrefixLength, MTU int; AdapterGUID string; CreatedAt time.Time; LastError string` |
| `Manager` 结构 | :102-110 | `controller Sidecar; mu sync.Mutex; deployed, failed bool; meta Meta; createdAt time.Time; lastError string` |
| `NewManager()` | :114-131 | 类型断言 `SetHandlers` 注册日志与意外退出回调 |
| `Create()` | :136-185 | 校验 → 幂等早返回(:145-147) → `m.meta = meta`(:151) → 清失败态 → `stopLocked` → `Activate`(:166) → 失败则 20s Stop 回滚 + `m.failed=true`(:170-179) |
| `Status()` | :188-192 | |
| `Remove()` | :196-208 | **幂等；未创建过返回 `absent`**；只 `stopLocked`，**不做 NIC 级删除**（F1 根因） |
| `stopLocked()` | :216-240 | 20s ctx + `waitForKeeperStop` + 复核状态 |
| `waitForKeeperStop()` | :246-268 | 首查 10ms、其后 25ms 轮询 |
| `handleUnexpectedExit()` | :270-278 | 记 `lastError` |
| `statusLocked()` | :280-307 | `m.failed` 时把 State 改写为 `failed`；仅 `tun.StateFailed` 时填 `LastError` |
| `matchesLocked()` | :309-317 | 比较 7 字段（**不含 StartupTimeout**） |
| `deriveState()` | :323-336 | `Starting→creating` / `Running→present` / `Stopping→removing` / `Failed→failed` / 其它→`absent` |
| `tunState()` | :338-343 | 空 State 视为 `tun.StateStopped` |

### 1.2 `engine/internal/server/server.go`（1071 行）——vnic 片段共 190 行

| 片段 | 行号 | 内容 |
|---|---|---|
| import | :11（`"net"`）、:25（`internal/vnic`） | `net` 在 server.go 内**唯一**用途是 :923 |
| `vnicManager` 接口 + 注释 | :44-51（+尾随空行 :52） | |
| `Server.vnic` 字段 | :61 | |
| `New()` 中构造 | :86-89（+注释 :86-88） | `vnic.NewManager(tun.NewSupervisor(), server.handleVnicLog, nil)`，**第二个独立 supervisor** |
| handle switch 三 case | :224-229 | 夹在 `MethodTunDeactivate`(:222-223) 与 `MethodDNSResolve`(:230-231) 之间 |
| `createVNIC()` | :811-893（注释 :811-813 + func :814-893） | 提权→`elevation_required`；JSON→`invalid_params`；`validateVNICCreate`→`invalid_params`；`params.Config()`+`config.InterfaceName = params.InterfaceName`(:841)→`authorizeTunConfig`→`security_policy_rejected`；**stale 重试 :868-880**；失败 `tun_failed`(:884) |
| `statusVNIC()` | :895-897 | |
| `removeVNIC()` | :899-915（注释 :899-901） | 失败码 `stop_failed`(:909) |
| `validateVNICCreate()` | :919-934 | :923 是 `net.ParseIP` 唯一用处 |
| `vnicStatus()` | :937-952 | 映射到 `api.VNICStatus`，RFC3339 |
| `isStaleVNICAdapterError()` | :954-966（注释 :954-957） | :963 用 `tun.ManagedInterfaceName`、:964 用 `tun.VNICInterfaceName` |
| `handleVnicLog()` | :975-981（+注释 :975） | |
| `stopProxyForHostExit()` 中停机块 | :1020-1027（+注释 :1020-1022） | 位于 :1016 `s.tun.Stop` 之后、:1028 proxy Stop 之前 |

（:968-973 是 `handleTunLog`，**非 vnic，必须保留**——它是 vnic 块内部唯一的「缺口」。）

### 1.3 `engine/internal/api/v1/types.go`（352 行）——vnic 片段 42 行

| 片段 | 行号 |
|---|---|
| `MethodVNICCreate/MethodVNICStatus/MethodVNICRemove` 常量 | :31-33 |
| `capabilities` 切片中的三项 | :61-63 |
| `VNICCreateParams`（8 字段）+ `func (p VNICCreateParams) Config() tun.Config` | :286-308 |
| `VNICStatus`（7 字段） | :310-321 |
| `VNICCreateResult` | :323-326 |

### 1.4 `engine/internal/api/v1/contract_test.go`（333 行）——vnic 片段 8 行

| 片段 | 行号 |
|---|---|
| `decodeRequestParams` 的 `case MethodVNICCreate:` | :214-215 |
| 无参白名单里的 `MethodVNICStatus, MethodVNICRemove,` | :229-230 |
| `decodeResult` 的 `case MethodVNICCreate:` | :269-270 |
| `decodeResult` 的 `case MethodVNICStatus, MethodVNICRemove:` | :271-272 |

关键断言：`TestManifestMatchesCompiledProtocol`(:46-107) 用 `slices.Equal(methods, Capabilities())`(:71) 要求 **manifest 方法名与顺序逐项等于 `Capabilities()`**；`TestCanonicalFixturesDecodeIntoTransportDTOs`(:109-) 要求 `Capabilities()` 中**每个**方法都有 request+response fixture；`decodeRequestParams` 的 `default`(:239-240) / `decodeResult` 的 `default`(:289-290) 会对未声明方法 `t.Fatalf`。

### 1.5 `engine/internal/protocol/protocol.go`（69 行）

**全文零 `vnic` 引用**（已 `git grep` 确认）。只有 Version/Transport/MaxMessageBytes、Request/Error/Response/Event 结构与三个构造函数。⇒ **移除 vnic 对 protocol.go 零改动**。

### 1.6 `protocol/v1/`

| 文件 | 片段 | 行号 |
|---|---|---|
| `manifest.json`（180 行，21 个方法） | `vnic.create`(state_guarded/administrator/caller_deadline_and_host_context) | :75-80 |
| | `vnic.status`(read_only/none/not_applicable) | :81-86 |
| | `vnic.remove`(safe_retry/administrator/caller_deadline_and_host_context) | :87-92 |
| `fixtures/messages.json`（997 行，54 条） | `engine_hello_response.result.capabilities` 中三项 | :50-52 |
| | `vnic_create_request` :405-425、`vnic_create_response` :426-445、`vnic_status_request` :446-455、`vnic_status_response` :456-472、`vnic_remove_request` :473-482、`vnic_remove_response` :483-497 | :405-497 |

### 1.7 `37571e5` 对引擎侧的全部改动（`git show --numstat 37571e5 -- engine/ protocol/`）

```
8	0	engine/internal/api/v1/contract_test.go
48	0	engine/internal/api/v1/types.go
195	0	engine/internal/server/server.go
296	0	engine/internal/server/server_test.go
1	1	engine/internal/tun/fakeip_windows_test.go
51	0	engine/internal/tun/interface_name.go      ← 新文件
2	2	engine/internal/tun/readiness_other.go
2	2	engine/internal/tun/readiness_windows.go
49	12	engine/internal/tun/supervisor.go
38	4	engine/internal/tun/supervisor_test.go
343	0	engine/internal/vnic/manager.go            ← 新文件
370	0	engine/internal/vnic/manager_test.go      ← 新文件
96	0	protocol/v1/fixtures/messages.json
18	0	protocol/v1/manifest.json
```
另有 desktop 侧大量改动（`desktop/internal/engineclient/vnic.go`、`desktop/internal/services/virtual_adapter.go`、`vnic_config.go`、`desktop/frontend/src/components/vnic/*`、`HomePage.tsx`、`desktop/portable/launch-portable.cmd`），**不在本报告范围**，但见 §3.5。

### 1.8 当初为 vnic 做的 tun 包泛化（`37571e5` 引入）

| 位置 | 内容 |
|---|---|
| `engine/internal/tun/supervisor.go:31-38` | 新增导出常量组 `ManagedInterfaceName = "HypoMux-Tun"`(:36)、`VNICInterfaceName = "HypoMux-VNIC"`(:37)；`:41 const tunInterfaceName = ManagedInterfaceName` |
| `engine/internal/tun/supervisor.go:59-62` | 新增 `Config.InterfaceName string` 字段 |
| `engine/internal/tun/supervisor.go:120` | `startupReady` 字段类型 `func(string) bool` → **`func(string, string) bool`** |
| `engine/internal/tun/supervisor.go:198,203,208-211,290` | `Activate` 内改为先解析「期望接口名」再传两参 |
| `engine/internal/tun/supervisor.go:357` | `configuredTunIPv4Address(path string)` → `(path string, expectedName string)` |
| `engine/internal/tun/supervisor.go:395-398` | `tunInterfaceWithExpectedAddress(expectedAddress)` → `(interfaceName, expectedAddress string)`，空名回退 `tunInterfaceName` |
| `engine/internal/tun/supervisor.go:704` | `normalizeConfig` 末尾新增 `InterfaceName: strings.TrimSpace(config.InterfaceName)` |
| `engine/internal/tun/interface_name.go`（51 行，新文件） | `expectedTunInterfaceName(config Config) (string, error)`(:16-21)、`configuredTunInterfaceName(path string) (string, error)`(:27-51) |
| `engine/internal/tun/readiness_windows.go:5` / `readiness_other.go:5` | `tunPlatformReady(expectedAddress string)` → `(interfaceName string, expectedAddress string)` |
| `engine/internal/tun/supervisor_test.go:124,134-157,72,103,309,312` | 新增 `TestSupervisorAcceptsConfiguredInterfaceNameOverride`(:134-157) 与 4 处签名更新 |
| `engine/internal/tun/fakeip_windows_test.go:99` | 匿名 `startupReady` 签名更新 |

**外部调用面核查**（决定泛化能否安全删除）：

```
$ git grep -n -E 'tun\.(ManagedInterfaceName|VNICInterfaceName)' -- '*.go'
engine/internal/server/server.go:963:   managed := strings.ToLower(tun.ManagedInterfaceName)
engine/internal/server/server.go:964:   virtual := strings.ToLower(tun.VNICInterfaceName)
```
⇒ 两个导出常量的**唯一**外部使用者就是 `isStaleVNICAdapterError`（vnic 专属）。删除 vnic 后它们在包外**零引用**。

`Config.InterfaceName` 的**唯一生产端写入点**是 `engine/internal/server/server.go:841`（vnic 路径，`config.InterfaceName = params.InterfaceName`）；`expectedTunInterfaceName` 的**唯一**调用点是 `engine/internal/tun/supervisor.go:198`。⇒ 整条泛化在 vnic 删除后**没有任何生产端会写入非空值**，退化为死灵活度（但**不是死代码**，见 §2.5）。

---

## 2. ② 废弃范围：按文件分组的删除清单

两个变体，**推荐先落地变体 B**。

### 2.1 变体 B —— 删除 vnic 全部代码，保留 tun 泛化

#### （B-1）整目录删除

| 操作 | 路径 | 规模 |
|---|---|---|
| **删除整个目录** | `engine/internal/vnic/` | `manager.go` 343 行 + `manager_test.go` 370 行 = **713 行**（含 12 个单测）。该目录**只服务 vnic**，无其它保留内容 |

#### （B-2）`engine/internal/api/v1/types.go`（352 → 305 行，-47 行）

| 操作 | 行号 | 说明 |
|---|---|---|
| 删除 | :31-33 | 3 个 `MethodVNIC*` 常量 |
| 删除 | :61-63 | `capabilities` 切片中三项 |
| 删除 | :286-326 | `VNICCreateParams` + `Config()` + `VNICStatus` + `VNICCreateResult`（含注释） |
| **保留** | import `time`、`tun` | 仍被 `TunLifecycleResult` / `TunActivateParams.Config()` 使用 |

#### （B-3）`engine/internal/api/v1/contract_test.go`（333 → 326 行，-7 行）

| 操作 | 行号 |
|---|---|
| 删除 | :214-215（`decodeRequestParams` 的 `case MethodVNICCreate:`） |
| 删除 | :229-230（无参白名单两项） |
| 删除 | :269-270、:271-272（`decodeResult` 两个 case） |

> **必须删**：这 4 处引用了即将被删除的 `VNICCreateParams`/`VNICCreateResult`/`VNICStatus` 类型，**不删则编译失败**。

#### （B-4）`engine/internal/server/server.go`（1071 → 881 行，-190 行）

| 操作 | 行号/范围 | 说明 |
|---|---|---|
| 删除 import | :11 `"net"` | server.go 内唯一用处 :923（`git grep '\bnet\.'` 只命中 :923） |
| 删除 import | :25 `internal/vnic` | |
| 删除 | :44-52 | `vnicManager` 接口 + 注释 + 尾随空行 |
| 删除 | :61 | `Server.vnic vnicManager` 字段 |
| 删除 | :86-89 | `New()` 中构造 + 三行注释 |
| 删除 | :224-229 | handle switch 三个 case |
| 删除 | **:811-966** | `createVNIC` / `statusVNIC` / `removeVNIC` / `validateVNICCreate` / `vnicStatus` / `isStaleVNICAdapterError` 六个函数及其注释（连续块） |
| 删除 | :975-981 | `handleVnicLog` + 注释 |
| 删除 | :1020-1027 | `stopProxyForHostExit()` 中 vnic 停机块 + 三行注释 |
| **保留** | :968-973 | **`handleTunLog` 必须保留**（它是 :811-966 与 :975-981 两块之间的唯一内容） |
| **收尾** | — | 删除后在 :44 与 :975 附近会各留一组连续空行，**需折叠为单空行**，否则 `gofmt -l` 会列出该文件 |

#### （B-5）`engine/internal/server/server_test.go`（1314 → 1021 行，-293 行）

| 操作 | 行号 | 说明 |
|---|---|---|
| 删除 import | :18 `internal/platform` | **该文件里 `platform.` 只出现在 :1025 与 :1162，两处都在 vnic 块内**（已 `git grep` 确认） |
| 删除 import | :22 `internal/vnic` | |
| 删除 | :1022-1314 | `newVNICLifecycleServer` / `vnicCreateRequest` / `testVNICCreateParams` 三个 helper + 9 个 vnic 测试（含 :300 的 `TestHostExitStopsVirtualAdapterKeeper`） |
| **保留** | :190-250 | `fakeTunController`（**共享**测试假件，非 vnic 专属） |

vnic 的 9 个测试（删除）：
`TestServerCreatesVirtualAdapterIdempotently` :1050、`TestServerReplacesVirtualAdapterWhenParametersChange` :1106、`TestServerRejectsInvalidVirtualAdapterParameters` :1134、`TestServerRequiresElevationForVirtualAdapterLifecycle` :1160、`TestServerReportsVirtualAdapterStatusAndRemoval` :1179、`TestServerReportsVirtualAdapterStartFailure` :1236、`TestServerRetriesVirtualAdapterAfterStaleWintunFailure` :1251、`TestStaleVirtualAdapterClassificationExcludesMainTunAdapter` :1272、`TestHostExitStopsVirtualAdapterKeeper` :1290。

#### （B-6）`protocol/v1/manifest.json`（180 → 162 行，-18 行）

| 操作 | 行号 |
|---|---|
| 删除三个方法对象 | :75-80 / :81-86 / :87-92 |

⇒ 方法数 21 → 18；`error_codes` 保持 **14** 个不变（vnic 只复用了既有码）。

#### （B-7）`protocol/v1/fixtures/messages.json`（997 → 901 行，-96 行）

| 操作 | 行号 |
|---|---|
| 删除 6 条 fixture | :405-497（`vnic_create_request/response`、`vnic_status_request/response`、`vnic_remove_request/response`） |
| 删除 | :50-52（`engine_hello_response.result.capabilities` 中的三项） |

⇒ fixture 54 → 48 条。

#### （B-8）零改动

`engine/internal/protocol/protocol.go`、`engine/internal/tun/**`、`engine/internal/platform/**`、`engine/cmd/**`、`.github/**`。

### 2.2 保留内容小结（变体 B 之后仍然存在的 vnic 痕迹）

| 路径 | 保留原因 |
|---|---|
| `engine/internal/tun/interface_name.go`（51 行） | 被 `supervisor.go:198` 调用，**不是死代码**；见 §2.5 |
| `engine/internal/tun/supervisor.go` 的 `Config.InterfaceName`、两参 `startupReady`、`configuredTunIPv4Address(path, name)`、`tunInterfaceWithExpectedAddress(name, addr)` | 同上；`supervisor_test.go:134` 的 `TestSupervisorAcceptsConfiguredInterfaceNameOverride` 覆盖它 |
| `engine/internal/tun/supervisor.go:36-37` 的 `ManagedInterfaceName` / `VNICInterfaceName` | `ManagedInterfaceName` 被 `:41 tunInterfaceName = ManagedInterfaceName` 使用，保留；`VNICInterfaceName` 在变体 B 下**包外零引用**（无编译错误，但语义上是 vnic 残留） |
| `engine/internal/tun/readiness_{windows,other}.go` 两参签名 | 同上 |

### 2.3 变体 A —— 再把 tun 泛化恢复为 `37571e5^` 版本（建议第二步）

做法：`git show 37571e5^:<path>` 取回 6 个文件的旧版本。

| 操作 | 文件 | 净变化 |
|---|---|---|
| 删除新文件 | `engine/internal/tun/interface_name.go` | -51 |
| 恢复 | `engine/internal/tun/supervisor.go` | -49/+12 |
| 恢复 | `engine/internal/tun/readiness_windows.go` | -2/+2 |
| 恢复 | `engine/internal/tun/readiness_other.go` | -2/+2 |
| 恢复 | `engine/internal/tun/supervisor_test.go` | -38/+4 |
| 恢复 | `engine/internal/tun/fakeip_windows_test.go` | -1/+1 |

恢复后 `ManagedInterfaceName` / `VNICInterfaceName` 两个导出常量随新 supervisor.go 一起消失（旧版没有它们），且**不会造成包外引用失效**（§1.8 已证唯一外部使用者是 `isStaleVNICAdapterError`，它已被删除）。

### 2.4 变体 A / B 的实测验证（详见 §3.3）

两个变体都在隔离沙箱里跑通了 `go build ./...`、`go vet ./...`、`go test -count=1 ./...`（13 包全 ok）、`gofmt -l`（无输出）。

### 2.5 变体 A 的安全性论证（为什么删除 tun 泛化不改变主 TUN 行为）

1. `Config.InterfaceName` 的唯一生产端写入点是 `engine/internal/server/server.go:841`（vnic）；
2. `expectedTunInterfaceName`（`interface_name.go:16-21`）的唯一调用点是 `supervisor.go:198`（vnic 路径进入的 `Activate`）；
3. 主 TUN 的配置由桌面端 `desktop/internal/services/tun_config.go:231` 硬编码 `interface_name: "HypoMux-Tun"`，因此 `configuredTunInterfaceName` 解析出的名字**恒等于**旧代码写死的 `tunInterfaceName` 常量；
4. ⇒ 变体 A 对主 TUN 是**行为等价**的，只是删掉了一条永远只被 vnic 走到的分支。

**建议顺序**：先落地变体 B（删除面最小、风险最低、可独立验证），确认 CI 与真机主 TUN 正常后，再择机落地变体 A 清理死灵活度。

---

## 3. ③ 协议影响 + 实跑测试验证

### 3.1 必须同步的四处（缺一即 CI 失败）

| # | 位置 | 约束 | 只改它的后果 |
|---|---|---|---|
| 1 | `engine/internal/api/v1/types.go` `Capabilities()` | 返回方法名切片 | — |
| 2 | `protocol/v1/manifest.json` `methods` | `slices.Equal(methods, Capabilities())`，**顺序也要一致**（:71） | 顺序错 → `TestManifestMatchesCompiledProtocol` 失败 |
| 3 | `protocol/v1/fixtures/messages.json` | 遍历 `Capabilities()`，每个方法必须有 request+response | 缺 fixture → `TestCanonicalFixturesDecodeIntoTransportDTOs` 失败 |
| 4 | `engine/internal/api/v1/contract_test.go` 的 4 处 case | **引用被删除的类型 → 不改则编译失败**（即使 fixture 已删也一样） | 编译失败 |

> 注意：#4 的失败模式是**编译错误**，不是测试断言失败。若只删 fixture 而不删 case，编译器会报 `undefined: VNICCreateParams` 等。
> `engine_hello_response.result.capabilities` 数组**不参与断言**（测试只解码），因此它不同步**不会**导致失败——但它与 `Capabilities()` 的偏差属于文档一致性问题。**该数组在 `37571e5` 之前就已与 `Capabilities()` 不同步**（既有问题，非本次引入）。

### 3.2 `error_codes` 影响

`protocol/v1/manifest.json` 的 `error_codes` 恰好 14 项。vnic 只复用既有码（`invalid_params` / `elevation_required` / `security_policy_rejected` / `tun_failed` / `stop_failed` / `invalid_state`），**没有新增**。⇒ 删除 vnic **不改变** `error_codes` 数量与内容。

### 3.3 实测结果（隔离沙箱，**不是推断**）

**方法学（重要）**：Lead 硬约束「本轮不改 `engine/**`」。为同时满足「实跑验证」，我没有碰源树，而是把 `engine/`、`protocol/`、`bin/` 复制到隔离沙箱
`%LOCALAPPDATA%\Temp\vnic-removal-lab`，在沙箱内执行删除并跑测试。源树全程 `git status --porcelain` = 仅 `?? reports/`。

**环境（与 Lead 给的命令有两处不同，务必注意）**：

```powershell
$env:GOROOT = Join-Path $env:TEMP 'go-full\go'          # go1.22.5 发行包
$env:PATH   = "$($env:GOROOT)\bin;$env:PATH"
$env:GOTOOLCHAIN = 'auto'                                # ← 不能设 local，见下
$env:GOFLAGS = '-mod=mod'
$env:GOPROXY = 'https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct'
$env:ErrorActionPreference = 'Continue'                  # 否则 pwsh 把 go 的 stderr 当 NativeCommandError
```

- **`GOTOOLCHAIN=local` 在本机不可用**：`engine/go.mod` 声明 `go 1.26.0`，本地 `go` 是 1.22.5，设 `local` 会直接报
  `go.mod requires go >= 1.26.0 (running go 1.22.5)`。用 `auto`（默认）时 Go 会切到已在 modcache 中的
  `golang.org/toolchain@v0.0.1-go1.26.8.windows-amd64`，实测正常。
- **沙箱必须带 `bin/`**：`engine/internal/tun/fakeip_windows_test.go:36` 会 `filepath.Abs("../../../bin/sing-box.exe")`，
  缺 `bin/`（约 91 MB）时 tun 包在**基线**（未改任何代码）就会失败。

**沙箱基线（未改任何代码，改前对照）**：

```
go test -count=1 ./internal/api/... ./internal/server/... ./internal/vnic/... ./internal/tun/...   → exit 0
```

**变体 B 实测（删除 vnic + 保留 tun 泛化）——全部通过**：

| 命令 | 结果 |
|---|---|
| `go build ./...` | `BUILD_EXIT=0` |
| `go vet ./...` | `VET_EXIT=0`（无「unused」类报错） |
| `go test -count=1 ./...` | `FULL_TEST_EXIT=0`，13 包全 `ok`（`cmd/hypomux-engine` / `api/v1` / `diagnostic` / `dns` / `expiry` / `fileintegrity` / `platform` / `proxy` / `runtime` / `server` / `tun` / `wfp`；`internal/protocol` 无测试文件） |
| `gofmt -l`（真 gofmt，递归所有 `*.go`） | **无输出** |

其中 `api/v1` 的契约测试（`TestManifestMatchesCompiledProtocol` + `TestCanonicalFixturesDecodeIntoTransportDTOs`）**通过**——证明 §3.1 的四处同步方案正确、且 `error_codes` 无需改动。

**变体 A 实测（再恢复 tun 泛化）——同样全部通过**：

| 命令 | 结果 |
|---|---|
| `go build ./...` | `BUILD_EXIT=0` |
| `go vet ./...` | `VET_EXIT=0` |
| `go test -count=1 ./...` | `FULL_TEST_EXIT=0`，13 包全 `ok`（`api/v1` 0.523s、`server` 0.833s、`tun` 5.010s、`proxy` 10.434s） |
| `gofmt -l` | **无输出** |

**删除后残留核查**：沙箱内对全部 `*.go`/`*.json` grep `vnic`（忽略大小写）= **0 命中**（变体 B 与变体 A 均是）。

### 3.4 contract 测试还能否通过？—— 能

**条件**：§3.1 四处同步。已在沙箱实测通过（§3.3）。删除后的方法集合（18 个，顺序）：
`engine.hello, engine.status, engine.start, engine.scheduling, engine.stop, engine.telemetry, steam_cdn.configure, tun.activate, tun.status, tun.deactivate, dns.resolve, dns.status, health.check, diagnostic.run, mtu.set, wfp.inspect, hotspot.inspect, host.shutdown`。

### 3.5 desktop 侧影响（**超出我的写权限，仅标注**）

`git grep -l -i vnic -- desktop` 命中 8 个文件，均需 desktop 负责人同步处理：

| 文件 | 内容 |
|---|---|
| `desktop/internal/engineclient/vnic.go` | `MethodVNICCreate/Status/Remove` 常量(:11-13)、`VNICCreateParams`(:19)、`VNICStatus`(:32)、`VNICCreateResult`(:44)、`VNICCreate`(:52)/`VNICStatus`(:63)/`VNICRemove`(:74) 三个客户端方法 |
| `desktop/internal/services/virtual_adapter.go` | `errVNICUnsupported`(:26)、能力探测 `slices.Contains(hello.Capabilities, engineclient.MethodVNICCreate)`(:75,:85)、`mapVNICStatus`(:211)，以及 :73-75 注释明确「缺 vnic.create 的旧核心直接拒绝」 |
| `desktop/internal/services/vnic_config.go` | 生成 vnic 用 sing-box 配置 |
| `desktop/frontend/src/components/vnic/VirtualAdapterPanel.tsx` + `.test.tsx` + `vnic.css` | 界面 |
| `desktop/frontend/src/pages/HomePage.tsx` | 挂载入口（`37571e5` 的提交标题即「replace the AI assistant with a virtual adapter action on home」） |
| `desktop/portable/launch-portable.cmd` | :23-31 注释说明便携版自带 vnic.create 的管理员引擎 |

**引擎侧先删会导致 desktop 编译失败**（能力探测会走 `errVNICUnsupported` 分支但类型仍在）。⇒ **两侧必须同批发布**，或 desktop 先降级为「不支持虚拟网卡」再删引擎方法。

---

## 4. ④ 替代方案评估：是否保留轻量只读 `vnic.*`？

### 4.1 候选方案

| 方案 | 描述 |
|---|---|
| **A（推荐）** | 完全删除 `vnic.*`，不做任何引擎侧替代 |
| B | 保留只读 `vnic.status`，语义改为「列出宿主机上由 HypoMux 创建的虚拟网卡」 |
| C | 新增 `vnic.list`，返回 `{name, mac, state, dhcp_address}` 供前端展示 |

### 4.2 逐项评估

**（1）引擎看不到 Hyper-V —— 这是决定性的架构事实。**
引擎的代码面里没有任何 Hyper-V / `VMS_` / `Get-VMNetworkAdapter` / `Msvm_` 的痕迹（`git grep -i 'hyper-v\|hyperv\|Msvm\|VMNetworkAdapter' -- engine/` = 0 命中）。引擎的平台层只有 `engine/internal/platform/{identity,mtu,sharing,wfp}.go`，全部围绕 WFP/MTU/共享，**没有任何网卡枚举 helper**（这也是 `VNICStatus.AdapterGUID` 当初只能恒为空的原因，见 `reports/vnic/12-engine-vnic.md` ③-F）。要让它查 Hyper-V，就必须在引擎里引入 PowerShell/COM 调用——与「Hyper-V 是 desktop 的职责」这一分工直接冲突。

**（2）新方案的 vNIC 不是引擎创建的，只读查询没有归属主体。**
方案 B/C 的前提是「引擎知道自己创建了哪些 vNIC」。新方案下 vNIC 由 desktop 通过 Hyper-V API 创建，引擎侧**没有创建记录**（既无持久化台账，也无进程句柄）。因此引擎要么(a)回退成「枚举所有 `vEthernet (xuni-*)` 接口」——那就是通用网卡枚举，(b)凭空维护一份台账——纯重复。

**（3）更关键：消费方已经存在，不需要新协议。**
Lead 工单已指出 `settings.json` 的 `selected_adapter_ids` + 现有网卡枚举/出口池已经能消费 `vEthernet (xuni-01..11)`。这些接口在真机已存在（`reports/vnic/22-engine-edge.md` §6.1：ifIndex 29/33/39/43/47/52/56/60/64/71/75）。
⇒ 引擎**不需要任何新 RPC** 就能把 Hyper-V vNIC 纳入流量调度。新增 `vnic.list` 只会是「给 UI 看的第二数据源」，且信息量严格弱于 desktop 直接查 Hyper-V（desktop 能拿到 MAC、DHCP 租约、交换机绑定，引擎拿不到）。

**（4）如果坚持要 UI 数据源，应该放在哪？**
放 **desktop 层**：`desktop/internal/platform` 或 `desktop/internal/services/` 直接用 PowerShell（`Get-VMNetworkAdapter -ManagementOS`、`Get-NetAdapter`、`Get-NetIPAddress`）或 WMI（`Msvm_*`），经 Wails binding 暴露给前端。那里本来就在做 Hyper-V 创建/删除，查询与创建同层，无跨进程协议开销，无版本协商（当前 desktop 还要靠 `hello.Capabilities` 探测 `vnic.create` 是否存在，见 `desktop/internal/services/virtual_adapter.go:75`）。

### 4.3 推荐

> **推荐方案 A：完全删除 `vnic.*`，引擎侧不保留任何替代 RPC。**
> 若日后确有「前端要显示 vNIC 列表」的需求，在 **desktop 层**新增一个 Wails service 方法（如 `VirtualAdapterService.List()`，内部 `Get-VMNetworkAdapter -ManagementOS` 过滤 `HypoMux-*`），**不要**新增引擎 RPC。

**若 Lead 仍希望保留一个最小只读契约**（备选，成本可接受时应显式记录为「已知冗余」）：
- 方法名：`vnic.status`（复用现有名字，避免破协议），语义改为「返回 HypoMux 管理的虚拟网卡摘要」
- params：无
- result：`{"vnics":[{"interface_name":string,"mac":string,"state":string,"dhcp_address":string}]}`
- **但必须明确**：引擎侧实现只能是「枚举网卡按 `HypoMux-*` 前缀过滤」（`net.Interfaces()`，无需 Hyper-V），`mac`/`dhcp_address` 可能为空——这已经说明它不如 desktop 直查。
- 结论：**不推荐**；保留它属于「为了不破协议而保留接口」，而该协议尚未有外部消费者。

---

## 5. ⑤ 已知缺陷记录

### 5.1 F1（真实缺陷，中）：stale 自愈无法恢复「孤儿 keeper 留下的适配器」

来源：`reports/vnic/22-engine-edge.md` §4-F1（§0 边界 9 判定 ❌）。

**现象**：适配器已存在但引擎不跟踪（模拟引擎重启后外部 sing-box 仍持锁）时，`vnic.create` 触发重试分支后**仍失败**，错误拼接 `stale virtual adapter retry failed` + `Cannot create a file when that file already exists.`；`vnic.remove` 也清不掉。

**源码证据链**：
1. `engine/internal/vnic/manager.go:136-185` `Create` → `Activate` 失败是 Wintun 的 "file already exists"；
2. `engine/internal/server/server.go:868-880` 命中 `isStaleVNICAdapterError` 后：先 `s.vnic.Remove(cleanupCtx)`(:870)，成功才重试一次 `s.vnic.Create`(:873)；
3. **`engine/internal/vnic/manager.go:196-208` 的 `Remove` 只 `stopLocked`**（:216-240），即只停**当前 `Manager` 跟踪的** keeper sidecar，**不做任何 NIC 级删除**（无 `Remove-NetAdapter`、无 pnputil、无 wintun 设备清理）；
4. `engine/internal/server/server.go:899-901` 的注释把这一点写成设计前提：
   > `// removeVNIC stops the existing keeper process. The virtual adapter exists only while that process is alive, so stopping it removes the adapter.`
5. 当宿主是**引擎不知道的**进程（外部 sing-box）时，前提 (4) 不成立 ⇒ `Remove` 无事可做 ⇒ 重试必然再撞同一个 "file already exists"。
6. 实测事件流证明重试分支确实执行：两次 sing-box 启动（PID=20732 → 失败 → PID=10648 → 再失败）。

**处置结论：整包删除后该缺陷与触发路径一并消失，不需要单独修。**
理由：缺陷的根因是「manager 以自己持有的 `deployed` 状态作为唯一的适配器归属判据」，而该判据只有在「引擎是唯一的适配器创建者」时才成立。产品方向反转后引擎不再是创建者 ⇒ **没有可修的语义**。任何「补丁式修复」（例如在 `Remove` 里加 `Remove-NetAdapter`）都会把引擎拖进 NIC 级管理，与 §4 的架构分工冲突。

### 5.2 F2（低）：create 失败后 `status` 停在 `failed` 而非 `absent`

`engine/internal/vnic/manager.go:280-307` `statusLocked` 在 `m.failed` 时强制把 State 改写为 `failed`；需显式 `vnic.remove` 才能归零（`Remove` :196-208 清 `failed`）。**随 vnic 删除消失。**

### 5.3 F3（信息/安全）：`config_sha256` 可选，不传即跳过摘要校验

`engine/internal/api/v1/types.go` 的 `VNICCreateParams.ConfigSHA256` JSON tag 为 `omitempty`，空值时 `tun.Config.ConfigSHA256` 为空，`stageTrustedConfig`（`engine/internal/tun/config_stage_windows.go:20-40`）随即原样返回不校验。⇒ 调用方可要求引擎用任意配置起 keeper。**随 vnic 删除消失**（主 TUN 路径由 `authorizeTunConfig` 强制 `pinnedDigest`，不受影响）。

### 5.4 F4（信息）：配置被复制到 `C:\ProgramData\HypoMuxCoreRuntime\tun-config-<random>.json`

`engine/internal/tun/config_stage_windows.go:96`（`trustedConfigDirectory`）+ `:42-80`（`copyPinnedConfig`）。**这不是 vnic 独有路径**——主 TUN 的 `tun.activate` 走同一个 `stageTrustedConfig`。⇒ **删除 vnic 不会消除它**，属独立议题（若 Lead 认为需要，应单独开工单）。

### 5.5 F5（新发现，信息）：`tun.VNICInterfaceName` 在变体 B 下成为包外零引用

`engine/internal/tun/supervisor.go:37` 导出 `VNICInterfaceName = "HypoMux-VNIC"`，其唯一外部使用者是 `engine/internal/server/server.go:964`（`isStaleVNICAdapterError`）。变体 B 删掉 `server.go` 里那个函数后，该常量在包外零引用（**不会**触发 Go 编译错误，导出符号允许无外部引用）。
- 影响：极低（一个未使用的导出字符串常量，`gofmt`/`go vet` 都不报）。
- 处置：**变体 A（§2.3）自然消除**；若只做变体 B，建议顺手在 `supervisor.go:31-38` 的注释里把 `VNICInterfaceName` 标注为 deprecated，或直接删该行（`go build` 已验证变体 A 可行，删除单行同样安全）。

### 5.6 同类遗留问题排查（回答「是否还有其他类似遗留问题」）

| 检查项 | 结论 |
|---|---|
| 是否有其它「只停自己的 sidecar、不做 NIC 级删除」的分支？ | **无**。`git grep -n 'Remove-NetAdapter\|pnputil' -- engine/` 只命中 `engine/internal/tun/cleanup_windows.go`（主 TUN 的 cleanup），它**确实**做 NIC 级清理（`Disable-PnpDevice` + `pnputil /remove-device`，:31-38）。vnic 当初没有对应的 cleanup，这就是 F1 与主 TUN 的差别 |
| 是否有其它依赖「引擎是唯一创建者」的假设？ | `engine/internal/tun/cleanup_windows.go:105-140` 的 `hasOwnedTunDevice()` 会枚举并比对 `tunInterfaceName`（"HypoMux-Tun"）+ InstanceId 含 "WINTUN"（:129,:136）。它**按名字精确匹配**，因此既不会误杀 `HypoMux-VNIC`，也不会误杀 `vEthernet (xuni-*)`。⇒ 主 TUN 的 stale 清理对 Hyper-V vNIC **安全** |
| `tun.Recover()`（`engine/internal/tun/recover.go:7`）是否会误杀 Hyper-V vNIC？ | **不会**。它只调 `cleanupPlatform(ctx)`（`cleanup_windows.go:61-96`），而后者先 `hasOwnedTunDevice()`(:105-140) 过滤，只处理 `FriendlyName == 'HypoMux-Tun'` 且 InstanceID 含 `WINTUN` 的设备 |
| `platform.SetMTU` 会不会拦 Hyper-V vNIC？ | **不会**。`engine/internal/platform/mtu_windows.go:26` 只对 `$a.Name -eq 'HypoMux-Tun'` 抛错拒绝；对 `vEthernet (xuni-*)` 正常设置 |
| 引擎是否会因为 `vEthernet (xuni-*)` 存在而改变行为？ | **不会**。引擎的网卡枚举由 desktop 通过 `selected_adapter_ids` 驱动，引擎不自动挑选接口 |
| `engine/internal/server/server.go:899-901` 的「适配器随进程存活」假设还被用在哪？ | 仅 `removeVNIC` 与 `stopProxyForHostExit():1020-1027`。两处随 vnic 删除一起消失 |

### 5.7 `.github/workflows/build.yml:346` 的便携包命名（**我不能改**）

`.github/workflows/build.yml:346` 有 `$stageName = "HypoMux-Portable-$version-vnic-preview"`。
vnic 废弃后这个后缀会成为误导，但 `.github/**` 属 Lead 明确禁止我改动的范围。⇒ **列为待办**，请 Lead 或 CI 负责人在合并删除补丁时一并改掉（建议改为无后缀，或按新方案改为 `-hyperv-preview`）。

---

## 6. 验证方法学与未验证项

### 6.1 已完成的实测（证据见 §3.3）

| 项 | 状态 |
|---|---|
| 沙箱基线（未改代码）测试 | ✅ exit 0 |
| 变体 B：`go build ./...` / `go vet ./...` / `go test -count=1 ./...` / `gofmt -l` | ✅ 全通过 |
| 变体 A：同上 | ✅ 全通过 |
| 删除后 `vnic` 残留 grep（`*.go` + `*.json`） | ✅ 0 命中 |
| 契约测试（manifest ↔ capabilities ↔ fixtures） | ✅ 通过 |
| `error_codes` 计数不变（14） | ✅ 已核对 |
| `engine/internal/protocol/protocol.go` 零改动 | ✅ 已 grep 确认 |
| 源树零改动 | ✅ `git status --porcelain` = `?? reports/` |

### 6.2 未验证项（如实标注）

1. **真机端到端未验证**：本轮严禁任何网络适配器/路由改动，也严禁创建/删除 Hyper-V 网卡或交换机 ⇒ 「删除引擎 vnic 后，desktop + Hyper-V 新方案在真机上是否工作」**未验证**。用户机器上 HypoMux 官方版正在运行（`HypoMux-Tun` ifIndex 51），全程未触碰。
2. **desktop 侧删除后能否编译/测试未验证**：要求 pnpm/node_modules，本机没有（`desktop/` 不在我的写权限内）。§3.5 只做了静态调用面清点。
3. **`GOTOOLCHAIN=local` 路径未验证**：本机无法用 `local`（§3.3）。沙箱结果来自 `GOTOOLCHAIN=auto` → go1.26.8。若 CI 用 `local` 且 CI 容器内 Go ≥ 1.26.0，结论应一致；**未验证**。
4. **变体 A 在真实主 TUN 上的行为等价未做端到端验证**：§2.5 是源码级论证（唯一生产端写入点 + 唯一调用点 + 桌面端硬编码），**未跑真机 TUN**。
5. **`.github/workflows/build.yml` 的实际执行未验证**（禁止改 `.github/**`，也只做了静态阅读）。§3.1 的失败模式（编译错误 / `slices.Equal` 失败 / fixture 缺失）是源码级推断 + 沙箱对等价改动的实测。
6. **沙箱不是全新 clone**：它从工作树复制（含未跟踪的 `reports/`，已 grep 排除影响）。`bin/` 为工作树现有二进制。

---

## 7. 附：执行清单（可直接照做）

```powershell
# 1) 删除整包
Remove-Item -Recurse -Force engine\internal\vnic

# 2) 四个文件手工编辑（按 §2.1 的行号）
#    engine\internal\api\v1\types.go          -47 行
#    engine\internal\api\v1\contract_test.go   -7 行
#    engine\internal\server\server.go        -190 行（注意保留 handleTunLog :968-973，收尾折叠双空行）
#    engine\internal\server\server_test.go   -293 行（注意保留 fakeTunController :190-250）

# 3) 协议两件
#    protocol\v1\manifest.json                删除 :75-92 三个方法对象
#    protocol\v1\fixtures\messages.json       删除 :50-52 三项 + :405-497 六条 fixture

# 4) 验证（本机命令，注意 GOTOOLCHAIN=auto 与 bin/ 存在）
$env:GOROOT = Join-Path $env:TEMP 'go-full\go'
$env:PATH   = "$($env:GOROOT)\bin;$env:PATH"
$env:GOTOOLCHAIN = 'auto'
$env:GOFLAGS = '-mod=mod'
$env:GOPROXY = 'https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct'
$env:ErrorActionPreference = 'Continue'
go -C engine build ./...
go -C engine vet ./...
go -C engine test -count=1 ./...
git ls-files -- '*.go' | Where-Object { $_ -like 'engine/*' } | ForEach-Object { gofmt -l $_ }
git status --porcelain    # 应只有预期的已修改文件，无 go.mod/go.sum 漂移
```

> 注意：`-mod=mod` 有潜在改动 `engine/go.mod`/`go.sum` 的风险，收尾前请用 `git diff --stat engine/go.mod engine/go.sum` 核对（本次沙箱实测无漂移）。
