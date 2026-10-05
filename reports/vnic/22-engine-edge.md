# 22 真机引擎层边界矩阵（T11 / task-17）

> 执行者：`local-pipeline-verifier`（本地管理员，High Mandatory Level）
> 被测二进制：`desktop\bin\hypomux-engine.exe`（本地真打包产物，`engine_version=2.7.0`，`commit=37571e5a0531c16621cf4ac3e7511ac21135aa00`）、`desktop\bin\sing-box.exe`
> 隔离资源：适配器 `HypoMux-VNIC-Edge`、地址 `10.68.0.1/24`、MTU 1420、临时目录 `%TEMP%\vnic-edge\`
> 目的：用**真机**（非单元测试）确认 `vnic.create / vnic.status / vnic.remove` 的**幂等、失败语义、安全策略、非法输入、stale 自愈**行为，区分「符合契约」与「真实缺陷」。
> 未改任何源码/配置/锁文件；`git status --porcelain` 结束时仍仅 `?? reports/`。

---

## 0. 结论摘要

| # | 边界 | 实测结论 | 判定 |
| --- | --- | --- | --- |
| 1 | `engine.hello` | `elevated=true`；`capabilities` 含 `vnic.create` / `vnic.status` / `vnic.remove` | ✅ 符合契约 |
| 2 | 重复 `create`（同名同址同配置） | **幂等复用**：`accepted=true`、`created_at` 不变、**不产生第二个 sing-box 进程**、ifIndex 不变 | ✅ 符合契约（manager.go:133-147） |
| 3 | 引擎重启后 `vnic.status` | 返回 `state=absent` + 空描述符 → **状态在内存，重启即遗忘**；且旧引擎退出时已自动清理适配器 | ✅ 符合契约 |
| 4 | 无适配器时 `vnic.remove` | 幂等，返回 `state=absent`（连发两次均如此），**不报错** | ✅ 符合契约（server.go:897-899） |
| 5 | `executable` 非白名单（`cmd.exe` / 字节完全相同的 sing-box 副本） | 两者均 `security_policy_rejected`：「virtual adapter executable is not authorized by the Core security policy」 | ✅ 符合契约，且**路径级**比对有效 |
| 6a | `config_sha256` 写错 | `tun_failed` + `SHA-256 digest mismatch`，**不创建适配器**，status 转 `failed` | ✅ 符合契约 |
| 6b | `config_sha256` **不传** | **成功**（`accepted=true`）→ 摘要校验是「提供才校验」 | ⚠️ 符合契约但值得注意 |
| 6c | `config_path` 不存在 | `tun_failed` + `sing-box config is unavailable: GetFileAttributesEx ...: The system cannot find the file specified.` | ✅ 符合契约 |
| 7 | `startup_timeout_ms=500` | `tun_failed`：「TUN interface HypoMux-VNIC-Edge did not become ready within 500ms」；引擎**杀掉子进程、不留孤儿**；随后同名 create 用 20s **成功** | ✅ 符合契约 |
| 8 | 非法 params（5 种）+ 未知方法 | 全部精确拒绝：`invalid_params`（4 种 + MTU）/ `method_not_found` | ✅ 符合契约 |
| 9 | **stale 自愈**（同名适配器由**外部 sing-box** 持有） | 重试分支**确实触发**（两次启动 sing-box），但**两次都失败**：`stale virtual adapter retry failed` + `Cannot create a file when that file already exists.` | ❌ **真实缺陷/设计缺口**：见 §4 F1 |
| 10 | create 失败后的 `status` | 停留在 `state=failed` + `last_error`，不是 `absent`；需显式 `remove` 才归零 | ⚠️ 设计如此，但需前端知晓（F2） |

**结论：安全策略、输入校验、幂等性、超时与清理路径全部符合契约；唯一真实缺口是 stale 自愈（§4 F1）。**

> **执行状态（如实记录）**：§2 的 10 个边界项**全部执行完毕**后收到 Lead 的暂停指令（`m00443`：用户报告安装新构建后 TUN 模式无法上网，要求立即停止新增适配器并彻底清理）。**不存在「因用户环境故障中止」而没跑完的边界项**；收到指令后未再启动任何适配器或引擎，只做了只读核对与清理，最终状态见 **§6**。

---

## 1. 环境与隔离

| 项 | 值 |
| --- | --- |
| 权限 | `IsInRole(Administrator)=True`（High Mandatory Level，无需 UAC） |
| 引擎二进制 | `<repo>\desktop\bin\hypomux-engine.exe`（启动方式：`serve`，stdin/stdout JSONL） |
| sing-box | `<repo>\desktop\bin\sing-box.exe`（81,947,136 B） |
| 测试配置 | `%TEMP%\vnic-edge\edge.json`，SHA-256 `dd286b8f799dd6aae1e547bfba243c637ab5bf9f39941c8cea07143439ce7a57`，UTF-8 无 BOM，263 B |
| 未触碰 | `HypoMux-Tun`（ifIndex 51，主 TUN，属 PID 19216 的 `C:\Program Files\HypoMux\bin\sing-box.exe`）、`HypoMux-VNIC`(Lead)、`HypoMux-VNIC-Probe`(T9)、`HypoMux-VNIC-E2E`(task-16) |
| 观测工具 | `Get-NetAdapter`、`Get-NetIPAddress`、`Get-CimInstance Win32_Process` |

测试配置原文（与 `desktop/internal/services/vnic_config.go:64-87` 形状一致，`stack` 用 `normalizeTunStack("")` 的实测值 `"system"`）：

```json
{"log":{"level":"warn","timestamp":true},"inbounds":[{"type":"tun","tag":"tun-in","interface_name":"HypoMux-VNIC-Edge","address":["10.68.0.1/24"],"mtu":1420,"auto_route":false,"strict_route":false,"stack":"system"}],"outbounds":[{"type":"direct","tag":"direct"}]}
```

协议用法（与任务书一致，实测无 hello 门禁）：
- 用 `.NET ProcessStartInfo{UseShellExecute=$false, RedirectStandardInput/Output/Error=$true}` 起 `hypomux-engine.exe serve`；
- **响应按 `"id"` 过滤**，用 `ReadLineAsync().Wait(ms)` 带超时读；unsolicited `log.record` 事件单独收集（否则会误读成响应）。
- **观测时机**：`server.go:97 defer s.stopProxyForHostExit()` ⇒ stdin EOF 会立即清理适配器，因此所有"存活期观测"都在发完请求、**不关 stdin** 的情况下进行。

---

## 2. 矩阵逐条实测

### 边界 1 — `engine.hello`

请求：

```json
{"protocol":1,"id":"r1","method":"engine.hello"}
```

原始响应：

```json
{"protocol":1,"id":"r1","result":{"engine":"hypomux-engine","engine_version":"2.7.0","commit":"37571e5a0531c16621cf4ac3e7511ac21135aa00","protocol_version":1,"transport":"stdio-jsonl","scheduling_strategies":["round-robin","weighted","adaptive-throughput","latency-first"],"capabilities":["engine.hello","engine.status","engine.start","engine.scheduling","engine.stop","engine.telemetry","steam_cdn.configure","tun.activate","tun.status","tun.deactivate","vnic.create","vnic.status","vnic.remove","dns.resolve","dns.status","health.check","diagnostic.run","mtu.set","wfp.inspect","hotspot.inspect","host.shutdown"],"modes":["proxy","tun_tcp_pool"],"mode_features":{"proxy":["socks5_connect","http_connect","source_bound_dns","ipv6_egress","adaptive_health","domain_quarantine"],"tun_tcp_pool":["tcp_connect","udp_associate","ipv6_egress","adaptive_health","managed_tun_lifecycle","dynamic_nic_channels"]},"os":"windows","arch":"amd64","pid":16792,"elevated":true,"started_at":"2026-10-03T09:44:48.9292567Z"}}
```

判定：`elevated=true` ✅；`vnic.create` / `vnic.status` / `vnic.remove` 三个能力**均在列** ✅。

### 边界 2 — create 正常路径 + 重复 create（幂等？孤儿进程？）

请求 r2（原始 JSON，实际发送时 `%TEMP%` 已被展开为 `%LOCALAPPDATA%\Temp`）：

```json
{"protocol":1,"id":"r2","method":"vnic.create","params":{"executable":"<repo>\\desktop\\bin\\sing-box.exe","config_path":"%LOCALAPPDATA%\\Temp\\vnic-edge\\edge.json","config_sha256":"dd286b8f799dd6aae1e547bfba243c637ab5bf9f39941c8cea07143439ce7a57","startup_timeout_ms":20000,"interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420}}
```

响应 r2：

```json
{"protocol":1,"id":"r2","result":{"accepted":true,"vnic":{"state":"present","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"created_at":"2026-10-03T09:44:50Z"}}}
```

真实适配器与进程（引擎存活期观测）：

```
Name               ifIndex Status
HypoMux-VNIC-Edge       69 Up
InterfaceAlias    IPAddress PrefixLength
HypoMux-VNIC-Edge 10.68.0.1           24
pid=12400 %USERPROFILE%\desktop\bin\sing-box.exe run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-2710370387.json
```

> 观察点：引擎把 host 给的配置**复制到 `C:\ProgramData\HypoMuxCoreRuntime\tun-config-<random>.json`** 再交给 sing-box 加载（原路径只在 verify 阶段使用）。

第二次**完全相同**的 create（r3）响应：

```json
{"protocol":1,"id":"r3","result":{"accepted":true,"vnic":{"state":"present","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"created_at":"2026-10-03T09:44:50Z"}}}
```

- `created_at` **仍为 09:44:50**（未刷新）；`ifIndex` 仍为 69；sing-box 进程**仍只有 pid=12400 一个**（未新增）。
- 判定：**幂等复用，不报错、不产生孤儿进程** ✅。源码依据：`engine/internal/vnic/manager.go:133-135`（注释「It is idempotent: repeating the request while the same adapter is present is a no-op」）与 `manager.go:145-147`：

```go
if m.deployed && m.matchesLocked(meta) && tunState(m.controller.Status()) == tun.StateRunning {
    return m.statusLocked(), nil
}
```

### 边界 3 — status 在 create 前 / 后 / remove 后 / **引擎重启后**

| 时点 | 原始响应 |
| --- | --- |
| create 前（新引擎，无 vnic） | `{"protocol":1,"id":"a4","result":{"state":"absent","interface_name":"","address":"","prefix_length":0,"mtu":0}}` |
| create 后（同一引擎） | `{"protocol":1,"id":"a2","result":{"state":"present","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"created_at":"2026-10-03T09:45:48Z"}}` |
| remove 后 | `{"protocol":1,"id":"d3","result":{"state":"absent","interface_name":"","address":"","prefix_length":0,"mtu":0}}` |
| **引擎重启后**（新进程，旧引擎已退出） | `{"protocol":1,"id":"a4","result":{"state":"absent","interface_name":"","address":"","prefix_length":0,"mtu":0}}` |

重启流程的现场证据：

```
--- stopping engine #1 (stdin EOF) ---
[S3-after-engine1-exit] adapters: 只有 HypoMux-Tun / HypoMux-VNIC-Probe（我的 HypoMux-VNIC-Edge 已消失）
[S3-after-engine1-exit] 10.68.* : (none)
--- starting fresh engine #2 ---
{"protocol":1,"id":"a3","method":"engine.status"} →
{"protocol":1,"id":"a3","result":{"engine":{"state":"stopped","sequence":0,"state_changed_at":"2026-10-03T09:45:52.8075479Z"},"host_uptime_ms":400}}
{"protocol":1,"id":"a4","method":"vnic.status"} → state=absent
```

判定：**状态纯内存，重启即遗忘** ✅（符合设计）；同时确认 `server.go:97 defer s.stopProxyForHostExit()` 在 stdin EOF 时会**自动移除适配器并杀掉子进程** ✅（无孤儿：`sing-box` 进程数从 3 回到 2，且 10.68.0.1 消失）。

### 边界 4 — 无适配器时 `vnic.remove`（幂等？）

```json
REQ : {"protocol":1,"id":"a5","method":"vnic.remove"}
RESP: {"protocol":1,"id":"a5","result":{"state":"absent","interface_name":"","address":"","prefix_length":0,"mtu":0}}
REQ : {"protocol":1,"id":"a6","method":"vnic.remove"}
RESP: {"protocol":1,"id":"a6","result":{"state":"absent","interface_name":"","address":"","prefix_length":0,"mtu":0}}
```

判定：**幂等**，返回 `absent` 而非错误 ✅。源码依据：`engine/internal/server/server.go:897-899`（「It is idempotent and reports absent, not an error, when no adapter exists.」）。

### 边界 5 — `executable` 非白名单

请求 b1（`C:\Windows\System32\cmd.exe`）与 b2（把仓库里的 sing-box **原样复制**到 `%TEMP%\vnic-edge\sing-box-copy.exe`，大小同为 81,947,136 B）响应**完全一致**：

```json
{"protocol":1,"id":"b1","error":{"code":"security_policy_rejected","message":"virtual adapter executable is not authorized by the Core security policy","details":{"message":"requested sing-box executable does not match the trusted policy"}}}
{"protocol":1,"id":"b2","error":{"code":"security_policy_rejected","message":"virtual adapter executable is not authorized by the Core security policy","details":{"message":"requested sing-box executable does not match the trusted policy"}}}
```

随后 `vnic.status` → `state=absent`；适配器列表无新增。

判定：✅ 符合契约，且**按路径比对**（字节完全相同的副本照样拒绝），`serve` 模式的可信路径即 `<引擎目录>\sing-box.exe`，防替换有效。

### 边界 6 — `config_sha256` 相关

**6a 摘要故意写错**（`0000...0000`）：

```json
{"protocol":1,"id":"c1","error":{"code":"tun_failed","message":"could not create the virtual adapter","details":{"message":"verify requested sing-box config: SHA-256 digest mismatch","vnic":{"state":"failed","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"last_error":"verify requested sing-box config: SHA-256 digest mismatch"}}}}
```

随后 status → `state=failed`（带 `last_error`）；**未创建任何适配器**；`vnic.remove` 后回到 `absent`。

判定：✅ 提供摘要时会校验，且**校验发生在 keeper 启动之前**（没有 sing-box 进程、没有适配器）。

**6b 完全不传 `config_sha256`**：

```json
REQ : {"protocol":1,"id":"d1","method":"vnic.create","params":{"executable":"...\\sing-box.exe","config_path":"...\\edge.json","startup_timeout_ms":20000,"interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420}}
RESP: {"protocol":1,"id":"d1","result":{"accepted":true,"vnic":{"state":"present","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"created_at":"2026-10-03T09:46:18Z"}}}
```

判定：⚠️ 符合契约（任务书已预期：`serve` 模式不做钉扎摘要，`authorizeTunConfig` 只强制 executable）——但意味着**摘要校验是可选的**：调用方不传即不校验。对本机 host 而言 `desktop` 侧始终传摘要（见 `desktop/internal/services/vnic_config.go`），因此实际风险受限于「引擎被其他调用方驱动」的场景，记录备用。

**6c `config_path` 指向不存在的文件**：

```json
{"protocol":1,"id":"c4","error":{"code":"tun_failed","message":"could not create the virtual adapter","details":{"message":"sing-box config is unavailable: GetFileAttributesEx %LOCALAPPDATA%\\Temp\\vnic-edge\\does-not-exist.json: The system cannot find the file specified.","vnic":{"state":"failed","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"last_error":"sing-box config is unavailable: GetFileAttributesEx ...: The system cannot find the file specified."}}}}
```

判定：✅ 错误信息可定位（含 Win32 原文与被检查的绝对路径），无适配器残留，无 sing-box 进程。

### 边界 7 — `startup_timeout_ms=500`

```json
{"protocol":1,"id":"e1","error":{"code":"tun_failed","message":"could not create the virtual adapter","details":{"message":"TUN interface HypoMux-VNIC-Edge did not become ready within 500ms","vnic":{"state":"failed","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"last_error":"TUN interface HypoMux-VNIC-Edge did not become ready within 500ms"}}}}
```

事件流（unsolicited）：

```json
{"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] sing-box configuration check passed"}}
{"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] expecting adapter HypoMux-VNIC-Edge with IPv4 address 10.68.0.1"}}
{"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] sing-box process started (PID=2540), waiting for stable takeover"}}
{"event":"log.record","data":{"component":"vnic-keeper","message":"[sing-box:stderr] FATAL[0002] start service: context canceled"}}
```

- 引擎在超时后**主动取消并杀掉**了刚启动的 sing-box（子进程 `PID=2540` 自行退出，进程表里没有它）。
- **无孤儿**：超时失败后 `sing-box` 进程只剩 2 个（均非本次）；适配器列表无 `HypoMux-VNIC-Edge`。
- **恢复能力**：紧接着同名 create、`startup_timeout_ms=20000` → `accepted=true`（新 `PID=21576`，ifIndex 69）→ `status=present`。说明 `failed` 状态不阻塞重试 ✅。

判定：✅ 符合契约（错误码 `tun_failed`、消息含真实阈值 500ms、无残留）。

### 边界 8 — 非法 params / 未知方法（逐条原文）

| 请求 | 响应 |
| --- | --- |
| `{"protocol":1,"id":"i1","method":"vnic.create","params":"not-an-object"}` | `{"protocol":1,"id":"i1","error":{"code":"invalid_params","message":"virtual adapter params are not valid JSON"}}` |
| params 缺 `interface_name` | `{"protocol":1,"id":"i2","error":{"code":"invalid_params","message":"virtual adapter name is required"}}` |
| `"address":"not-an-ip"` | `{"protocol":1,"id":"i3","error":{"code":"invalid_params","message":"virtual adapter address must be a valid IPv4 address"}}` |
| `"prefix_length":33` | `{"protocol":1,"id":"i4","error":{"code":"invalid_params","message":"virtual adapter prefix length must be between 1 and 32"}}` |
| `"mtu":60`（补充项） | `{"protocol":1,"id":"i5","error":{"code":"invalid_params","message":"virtual adapter MTU must be between 576 and 65535"}}` |
| `{"id":"i6","method":"vnic.unknown"}` | `{"protocol":1,"id":"i6","error":{"code":"method_not_found","message":"unknown method","details":{"method":"vnic.unknown"}}}` |

判定：✅ 全部精确拒绝（`server.go:814-893` 的 `invalid_params` 体系），无副作用（未创建适配器、无新进程）。

### 边界 9 — stale 自愈（关键失败项）

步骤与证据：

1. 手工起独立 sing-box（**不经过引擎**）：

```
& desktop\bin\sing-box.exe run -c %TEMP%\vnic-edge\edge.json     → pid=9772
adapter appeared after 0s: True
manual adapter: name=HypoMux-VNIC-Edge ifIndex=69 status=Up
[procs] pid=9772 "...\desktop\bin\sing-box.exe" run -c "%LOCALAPPDATA%\Temp\vnic-edge\edge.json"
```

2. 引擎启动，`vnic.status` → `{"state":"absent",...}`（**引擎不知道这个适配器**）。

3. 引擎对同名同址发 `vnic.create`：

```json
{"protocol":1,"id":"g2","error":{"code":"tun_failed","message":"could not create the virtual adapter","details":{"message":"stale virtual adapter retry failed\nsing-box exited unexpectedly (code=1): FATAL[0000] start service: start inbound/tun[tun-in]: configure tun interface: Cannot create a file when that file already exists.","vnic":{"state":"failed","interface_name":"HypoMux-VNIC-Edge","address":"10.68.0.1","prefix_length":24,"mtu":1420,"last_error":"sing-box exited unexpectedly (code=1): ... Cannot create a file when that file already exists."}}}}
```

4. 事件流证明**重试分支确实执行了**（两次 sing-box 启动：`PID=20732` → 失败 → 再 `PID=10648` → 再次失败）：

```json
{"event":"log.record","data":{"message":"[TUN] sing-box process started (PID=20732), waiting for stable takeover"}}
{"event":"log.record","data":{"message":"[sing-box:stderr] FATAL[0000] start service: start inbound/tun[tun-in]: configure tun interface: Cannot create a file when that file already exists."}}
{"event":"log.record","data":{"message":"[TUN] sing-box process started (PID=10648), waiting for stable takeover"}}
{"event":"log.record","data":{"message":"[sing-box:stderr] FATAL[0000] ... Cannot create a file when that file already exists."}}
```

5. 失败后的现场：适配器 `HypoMux-VNIC-Edge ifIndex=69` **仍在**（ifIndex 未变），外部 sing-box `pid=9772` **仍活着**；引擎 `vnic.remove` 返回 `absent` 且**同样没能清掉这个外部适配器**。

判定：❌ **真实缺陷/设计缺口** → 详见 §4。

---

## 3. 清理与收尾（硬要求）

```
===== cleanup =====
killing manual sing-box pid=9772
[FINAL] sing-box procs:
    pid=19216 "C:\Program Files\HypoMux\bin\sing-box.exe" run -c ...\tun-config-254382414.json   ← 主 TUN，非本次（未触碰）
    pid=12420 "...\desktop\bin\sing-box.exe" run -c ...\tun-config-...  (其它队友的 E2E 会话)
[FINAL] HypoMux adapters: HypoMux-VNIC-Probe / HypoMux-Tun                ← 无 HypoMux-VNIC-Edge
```

收尾核验（独立命令）：

```
### git status            ?? reports/            （无任何源码/配置改动）
### Get-NetAdapter -Name HypoMux-VNIC-Edge   → "no"（不存在，无需 Remove-NetAdapter）
### 10.68.* 地址           none
### sing-box 进程         2 个，均非本次会话
```

- 手工 sing-box（pid=9772）已按**命令行含 `vnic-edge`** 校验后杀死；
- `HypoMux-VNIC-Edge` 适配器随其宿主进程退出自动消失，**未执行** `Remove-NetAdapter`（无需）；
- 未触碰 `HypoMux-Tun` / `HypoMux-VNIC` / `HypoMux-VNIC-Probe` / `HypoMux-VNIC-E2E`。

---

## 4. 缺陷清单（只报告，未改任何源码）

### F1（中）stale 自愈无法恢复「孤儿 keeper 留下的适配器」

**现象**：适配器已存在但引擎不跟踪（模拟引擎重启后 keeper 仍在跑的经典残留），`vnic.create` 触发重试分支后**仍然失败**，且失败信息把两段错误拼在一起：`stale virtual adapter retry failed` + `Cannot create a file when that file already exists.`。引擎的 `vnic.remove` 也无法清理它。

**源码证据**：
- `engine/internal/server/server.go:863-878`：命中 `isStaleVNICAdapterError` 时先 `s.vnic.Remove(cleanupCtx)`（20s 超时）再**重试一次** `Create`；
- `engine/internal/server/server.go:952-963` + `:774-783`：匹配 `"cannot create a file when that file already exists"` → 本次错误**确实命中**（所以走的是重试路径，不是 `retry cleanup failed`）；
- `engine/internal/vnic/manager.go:194-208`：`Remove` 只做 `stopLocked(ctx)` —— **只停当前 Manager 实例跟踪的 keeper sidecar**，不做任何 NIC 级（`Remove-NetAdapter`/wintun）删除；
- `engine/internal/server/server.go:897-899` 注释亦确认设计前提：「the virtual adapter exists only while that process is alive」——一旦适配器的宿主是**引擎不知道的进程**，该前提不成立。

**影响**：真实场景（引擎崩溃/重启、keeper 变孤儿并继续持有 `HypoMux-VNIC-Edge`）下，用户点「创建」会**持续失败**，且没有引擎侧的自愈路径；必须人工杀进程或 `Remove-NetAdapter`。这与 T11 的目标（真机可用性）直接相关。

**建议**（供 Lead 决策，未实施）：stale 分支在 `Remove` 之后、重试之前，增加一次**按名清理**（例如 `Remove-NetAdapter -Name <params.InterfaceName>`，或枚举并终止持有该 wintun 适配器的孤儿 keeper），并在清理失败时返回可区分的错误码（如 `stale_adapter_not_removable`）。

### F2（低/约定）create 失败后 `status` 停留在 `state=failed` 而非 `absent`

`config_sha256` 错误、`config_path` 缺失、`startup_timeout_ms` 过小三种情况后，`vnic.status` 均返回 `{"state":"failed",...,"last_error":"..."}`，直到显式 `vnic.remove` 才回到 `absent`（S6a 已验证）。这对诊断友好，但前端若把 `state=failed` 当作「适配器存在」渲染，会出现「失败态却无适配器」的显示。属设计选择，记录备查。

### F3（信息/安全边界）`config_sha256` 为可选

不传即跳过摘要校验（6b 实测成功）。`serve` 模式无钉扎摘要，`authorizeTunConfig` 只强制 `executable` 与受信路径相等。当前 host 侧总是传摘要，故风险有限；若未来放宽调用方，需要引擎侧改为强制。

### F4（信息）引擎会把 host 配置复制到 `C:\ProgramData\HypoMuxCoreRuntime\tun-config-<random>.json`

从子进程命令行观察得到（`pid=12400/21576` 等）。意味着宿主临时配置不是被 sing-box 直接读取，而 `config_path`/`config_sha256` 校验针对的是 host 侧原文件。正常行为，记录以便排查。

---

## 5. 符合契约的行为清单（可直接作为验收依据）

1. `engine.hello`：`elevated=true`，三个 `vnic.*` 能力在列（§2-1）。
2. `vnic.create` 正常路径：`accepted=true` → 真机出现 `HypoMux-VNIC-Edge`（ifIndex 69）+ `10.68.0.1/24`，子进程持有 `ProgramData` 下的配置副本（§2-2）。
3. **重复 create 幂等**：同 `created_at`、同 ifIndex、**不新增进程**（§2-2，manager.go:133-147）。
4. `vnic.status`：create 前 `absent`、create 后 `present`、remove 后 `absent`、**引擎重启后 `absent`**（§2-3）。
5. 引擎退出（stdin EOF）自动移除适配器、杀掉子进程（§2-3，server.go:97）。
6. 无适配器 `vnic.remove`：幂等返回 `absent`，不报错（§2-4）。
7. `executable` 白名单：路径级校验，`cmd.exe` 与**字节相同的副本**均 `security_policy_rejected`（§2-5）。
8. `config_sha256` 提供即校验：`tun_failed` + `SHA-256 digest mismatch`，无副作用（§2-6a）。
9. `config_path` 缺失：`tun_failed` + Win32 原文，无副作用（§2-6c）。
10. `startup_timeout_ms` 过小：`tun_failed` + `did not become ready within 500ms`，**不留孤儿**，同名重试可恢复（§2-7）。
11. 非法输入 5 种全部 `invalid_params` 且消息精确；未知方法 `method_not_found`（§2-8）。

---

## 附：本次使用的脚本

| 文件 | 作用 |
| --- | --- |
| `%TEMP%\vnic-edge\enginelib.ps1` | JSONL harness：`Start-EdgeEngine`（重定向 stdin/stdout/stderr + 异步收集 stderr）、`Send-EdgeRaw`（按 `id` 过滤响应、带超时）、`Show-EdgeState`、`New-CreateReq` |
| `%TEMP%\vnic-edge\s1_hello_dup.ps1` | 边界 1、2 与 create 后 status、stdin EOF 自动清理 |
| `%TEMP%\vnic-edge\run1.ps1` | 边界 3、4、5、6 |
| `%TEMP%\vnic-edge\run2.ps1` | 边界 6b、7、8 |

---

## 6. 收尾复核（按 Lead `m00443` 暂停指令，只读核对，2026-10-03）

收到暂停指令后**未再启动任何适配器/引擎/进程**，仅执行只读核对；最终状态如下。

### 6.1 `Get-NetAdapter | ft Name,Status,ifIndex`（全部网卡）

```
Name                       Status ifIndex
----                       ------ -------
以太网                        Up           8
vEthernet (Default Switch) Up          27
vEthernet (xuni-01)        Up          29
vEthernet (xuni-02)        Up          33
vEthernet (xuni-03)        Up          39
vEthernet (xuni-04)        Up          43
vEthernet (xuni-05)        Up          47
HypoMux-Tun                Up          51
vEthernet (xuni-06)        Up          52
vEthernet (xuni-07)        Up          56
vEthernet (xuni-08)        Up          60
vEthernet (xuni-09)        Up          64
vEthernet (xuni-10)        Up          71
vEthernet (xuni-11)        Up          75
```

- **`HypoMux-VNIC-Edge` 不存在**（`Get-NetAdapter -Name 'HypoMux-VNIC-Edge'` → 无结果），`HypoMux-VNIC-Probe` / `HypoMux-VNIC-E2E` 亦不存在（其它任务已自行清理）。
- 主 TUN **`HypoMux-Tun` ifIndex=51 未受影响**（我方从未对其发过任何指令）。
- `10.68.*` 地址：`none`。

### 6.2 残留 `sing-box.exe` 列表（全部，含父进程）

```
pid=19216 ppid=6920 start=17:41:11 cmd="C:\Program Files\HypoMux\bin\sing-box.exe" run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-254382414.json
```

只有 **1 个** `sing-box.exe`，属于**已安装的 HypoMux 主 TUN 路径**（`C:\Program Files\HypoMux\bin\`，父进程 pid=6920 即 `C:\Program Files\HypoMux\bin\hypomux-engine.exe`）——**不是**本次测试起的（我方子进程来自 `desktop\bin\sing-box.exe`，且已随引擎退出全部结束）。**本次会话产生的 sing-box / hypomux-engine 进程残留 = 0。**

### 6.3 `hypomux-engine.exe` 列表

```
pid=13308 ppid=688  start=17:40:53 cmd=C:\ProgramData\HypoMux\Core\bin\hypomux-engine.exe service   ← 已安装版服务
pid=6920  ppid=6240 start=17:41:05 cmd="C:\Program Files\HypoMux\bin\hypomux-engine.exe"            ← 已安装版主进程
```

均为**用户已安装的 HypoMux**（`Program Files` / `ProgramData`），**不是**我用的 `desktop\bin\hypomux-engine.exe serve`。

### 6.4 工作树与合规

- `git status --porcelain=v1` → 仅 `?? reports/`；**未改任何源码/配置/锁文件**。
- 全程**未执行**任何网络改动类命令（无 `Remove-NetRoute`、无 `Set-DnsClientServerAddress`、无 `netsh`、无 `Restart-NetAdapter`、未动系统代理）；唯一一次 `Remove-NetAdapter` 分支**未被触发**（适配器已随宿主进程退出消失）。
- 本报告 §3 的清理步骤发生在暂停指令到达之前；§6 是暂停后的独立复核，两者结论一致。
| `%TEMP%\vnic-edge\run3.ps1` | 边界 9（stale 自愈）与清理 |
| `%TEMP%\vnic-edge\edge.json` | 测试用 sing-box 配置（sha256 `dd286b8f...7a57`） |
