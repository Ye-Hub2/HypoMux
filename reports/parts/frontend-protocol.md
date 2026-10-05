# HypoMux 错误处理审计 — 协议契约 / 前端 / CI

审计人：`frontend-proto-errors` · 范围：`protocol/v1/`、`desktop/frontend/`、`desktop/portable/`、`desktop/scripts/`、`.github/`，以及 engine→desktop→前端 三跳错误载荷保真性验证
性质：只读审查（未修改任何 `.go` / `.ts` / `.tsx` / `.json` / `.vue` / `.cmd` / `.yml`）

---

## 跨层错误传播结论

**结论：错误码在第三跳（desktop services → Wails → 前端）丢失，根因文本在第四步（通知渲染）被压缩。engine 与 engineclient 两跳是保真的，前端完全看不到结构化错误。**

分三跳给出实测证据：

**第 1 跳 engine —— 保真。** `engine/internal/protocol/protocol.go` 定义
`type Error struct { Code string; Message string; Details map[string]any }`，
`Code` 是结构化首字段，`Failure(id, code, message, details)` 直接构造它。
`protocol/v1/fixtures/messages.json:883-897` 的 `dns_failed` 样例把根因放在
`details.message`（`"DoH resolution failed: context deadline exceeded"`），而 `error.message` 只是分类文本——
**这意味着"原始错误信息"必须连 `details` 一起传，只传 `Code`+`Message` 就是失真。**

**第 2 跳 desktop / engineclient —— 结构化保真，但立刻被拍扁。**
`desktop/internal/engineclient/client.go:24-28` 的 `RemoteError` 完整解码了三个字段；
`client.go:443-446` 把 `result.Error` 原样返回，所以 desktop 侧**本来可以**按 code 分支。
但 `client.go:30-49` 的 `Error()` 把它们拼成字符串 `Code + ": " + Message (+ Details["message"])`，
此后 Go 的 `error` 接口里 code 就只剩一个文本前缀。

**第 3 跳 desktop services → Wails → 前端 —— 丢失。**
Grep 全量 `desktop/**/*.go`，只有 **3 处**用 `errors.As` 取回 `*RemoteError` 并检查 `Code`：
- `desktop/internal/services/engine.go:874-875` → `if errors.As(cleanupErr, &remote) && remote.Code == "invalid_state" {`
- `desktop/internal/services/engine.go:1148-1149` → `if !errors.As(err, &remote) || remote.Code != "invalid_state" {`
- `desktop/internal/services/engine.go:1155-1156` → 同上

其余所有服务方法一律 `return err`，Wails 只把 `err.Error()` 的字符串送到 JS 侧。
前端唯一的"错误码"是自造的 `HM-Exxxx`（`desktop/frontend/src/components/notifications/errorCodes.ts:14-33`），
**由对人类可读字符串做子串匹配推导**（`includesAny(text,["timeout","timed out","deadline exceeded","超时"])`），
从不查询任何协议 code。

**决定性证据：** 对 `desktop/frontend/src` 全量 Grep 模式
`invalid_state|unsupported_mode|start_failed|stop_failed|dns_failed|tun_failed|security_policy_rejected|wfp_unavailable|elevation_required|method_not_found|invalid_params|unsupported_protocol|invalid_json|invalid_request`，
**唯一命中是 `desktop/frontend/src/pages/SettingsPage.tsx:438` 的假阳性**（`settings_autostart_failed` 里恰好含子串 `start_failed`）。
**契约里冻结的 14 个错误码，在整个前端 118 个源文件中出现次数为 0。**

**反向证据：契约被实际违反，且无人发现。** 引擎发出的错误码里
有 4 个不在 `protocol/v1/manifest.json:164-179` 的 `error_codes` 列表里
（详见 P0-1）；desktop 还自己合成一个契约外 code `"disconnected"`（详见 P1-4）。
`engine/internal/api/v1/contract_test.go:104-106` 只断言 `len(manifest.ErrorCodes) != 0`，
`:157` 只断言 `response.Error.Code != ""` —— **冻结列表从未被拿去校验任何真实发射的码。**

---

## 范围概览

整体健康度**偏好，但契约层有硬伤、前端有系统性静默失效**。分三层看：

1. **契约层最差。** `protocol/v1/` 定义清晰、README 规范明确，但 `error_codes` 是一张**死清单**——没有校验、没有测试守卫、引擎和 desktop 都在自由发射表外码。这直接导致下层无法按契约分支。
2. **engineclient 层中等。** 结构化错误解码正确，但事件通道用非阻塞发送丢弃"契约声明为不可合并、不可丢失"的事件；解析失败的处理是 `continue`；错误码在 `Error()` 里被拍扁。
3. **前端层是最干净的**：`desktop/frontend/src` 只有 **1 处**裸 `.catch(() => {})`（`MTUDetectionPage.tsx:40`，且在组件卸载清理路径上，合理），保存队列 `latestSaveQueue.ts` / `settingsQueue.ts` 设计正确（失败必 reject waiters、有 `authoritative` 回滚、`done` 必挂 handler 无 unhandled rejection），`VirtualAdaptersPage.tsx` 对部分失败/孤儿卡/超时预算的处理有逐行注释论证。**问题不在"写错了 catch"，而在两处基础设施级的静默 sink**：`serialPoll.ts:25` 和 `platform/desktop.ts:6-10`，它们把所有轮询/原生调用的失败吞成零信号。
4. **CI 层干净，未发现吞失败。** 4 个 workflow 全部检查过：无 `continue-on-error`、无被注释掉的检查、无裸 `|| true`（3 处 `|| true` 全在 `trap cleanup` 或被下游 `exit 1` 显式接管的 `curl`/`git ls-remote` 中）。`build.yml:144-150` 同时跑 `vitest run` 和 `tsc && vite build`，类型错误会 fail CI。
5. **`desktop/scripts/` 目录不存在** —— 该路径下无任何文件，实际存在的只有 `desktop/portable/` 下的 3 个文件（两个 `.cmd` + 一个 `.txt`），已全部读完。

### 实际检查过的文件清单（29 个）

**协议契约（4）**：`protocol/v1/manifest.json`、`protocol/v1/README.md`、`protocol/v1/fixtures/messages.json`、`protocol/v1` 错误码清单 `manifest.json:164-179`

**契约测试与引擎 DTO（3）**：`engine/internal/api/v1/contract_test.go`（全文 333 行）、`engine/internal/api/v1/types.go`（全文 352 行）、`engine/internal/protocol/protocol.go`

**引擎错误发射点（2）**：`engine/internal/server/server.go`（错误发射点 :183-299, :346-395, :422-653, :723-909）、`engine/internal/server/scheduling.go`

**desktop 桥接层（2）**：`desktop/internal/engineclient/client.go`、`desktop/internal/platform/wails/desktop.go`（签名清单 :38-247）

**desktop 服务层（1）**：`desktop/internal/services/engine.go`（RemoteError 使用点 :874-875, :1148-1156）

**前端核心 / 平台层（8）**：`desktop/frontend/src/platform/serialPoll.ts`、`desktop/frontend/src/platform/desktop.ts`、`desktop/frontend/src/platform/services.ts`（全文 414 行）、`desktop/frontend/src/platform/latestSaveQueue.ts`、`desktop/frontend/src/platform/settingsQueue.ts`、`desktop/frontend/src/platform/adapterSaveQueue.ts`

**前端状态与页面（10）**：`desktop/frontend/src/state/useEngineState.ts`（:180-359）、`desktop/frontend/src/App.tsx`（:100-144）、`desktop/frontend/src/pages/SettingsPage.tsx`（:350-459）、`desktop/frontend/src/pages/ToolsPage.tsx`（:48-97）、`desktop/frontend/src/pages/RoutingPage.tsx`（:220-479）、`desktop/frontend/src/pages/VirtualAdaptersPage.tsx`（:410-539）、`desktop/frontend/src/pages/BlockedDomainsPage.tsx`（:40-79）、`desktop/frontend/src/components/RuleSetsPanel.tsx`（:55-94）、`desktop/frontend/src/components/HotspotPanel.tsx`（:69-149）、`desktop/frontend/src/components/tray/TrayMenu.tsx`（:53-109）

**前端通知与主题（4）**：`desktop/frontend/src/components/notifications/errorCodes.ts`、`desktop/frontend/src/components/notifications/notificationMessage.ts`、`desktop/frontend/src/i18n/i18n.tsx`、`desktop/frontend/src/theme/background.service.ts`

**便携版脚本（2）**：`desktop/portable/launch-portable.cmd`（全文 257 行）、`desktop/portable/restore-environment.cmd`（全文 136 行）

**CI（4）**：`.github/workflows/build.yml`（全文 704 行）、`.github/workflows/go-engine.yml`（全文 67 行）、`.github/workflows/release-smoke.yml`（全文 91 行）、`.github/workflows/create-release-tag.yml`（全文 125 行）

**构建配置（1）**：`desktop/frontend/package.json`

**发现统计：P0 = 4，P1 = 9，P2 = 7，共 20 条。**

---

# P0 — 用户数据损坏 / 系统状态不一致 / 静默失效

## P0-1 引擎发射 4 个契约外错误码，而契约测试在结构上无法发现

**位置**
- `engine/internal/server/server.go:255`
  ```go
  			return protocol.Failure(request.ID, "engine_running", "请先停止网络服务", nil), false
  ```
- `engine/internal/server/server.go:265`
  ```go
  			return protocol.Failure(request.ID, "mtu_failed", err.Error(), nil), false
  ```
- `engine/internal/server/server.go:274`
  ```go
  			return protocol.Failure(request.ID, "hotspot_inspection_failed", err.Error(), nil), false
  ```
- `engine/internal/server/scheduling.go:57`
  ```go
  			return protocol.Failure(request.ID, "update_failed", err.Error(), nil), false
  ```
- 契约声明：`protocol/v1/manifest.json:164-179`，14 个码为 `invalid_json, unsupported_protocol, invalid_request, method_not_found, invalid_params, invalid_state, unsupported_mode, start_failed, stop_failed, dns_failed, tun_failed, security_policy_rejected, wfp_unavailable, elevation_required` —— **不含上述 4 个**
- 守卫失效点：`engine/internal/api/v1/contract_test.go:104-106`
  ```go
  	if len(manifest.ErrorCodes) == 0 {
  		t.Fatal("manifest must document structured error codes")
  	}
  ```
  和 `engine/internal/api/v1/contract_test.go:157`
  ```go
  				if response.Error == nil || response.Error.Code == "" || response.Error.Message == "" {
  ```
  —— 两处都只检查**非空**，从不检查 `Code ∈ manifest.ErrorCodes`。

**触发条件**：用户在引擎运行时改 MTU（→`engine_running`）、MTU 设置失败（→`mtu_failed`）、热点状态检查失败（→`hotspot_inspection_failed`）、或 `engine.scheduling` 热更新失败（→`update_failed`）。这四条路径没有任何前置门禁，日常即可触发。

**后果**：`protocol/v1/README.md:58-61` 规定"Protocol v1 is additive... 改变字段类型或含义需要重新协商协议版本"。发射清单外的新 code 不是 additive，而是一个**契约破坏**：任何按清单做 switch/穷举分派的客户端都会落到 default 分支。这 4 个码传到 `RemoteError.Code` 后，因为 desktop 只认 `invalid_state`（`desktop/internal/services/engine.go:874-875, 1148-1149, 1155-1156`），最终全部塌缩成一段文本，再被前端 `notificationMessage.ts:16-55` 的子串匹配改写成通用句——**用户看到"操作未完成，请重试；如仍失败，请导出支持日志。"，拿不到任何可用于定位的信息**。

**为什么没兜住**：`contract_test.go` 校验的是"清单非空"和"fixture 里 Code 非空"，而不是"实际发射的码属于清单"。契约测试与真实发射点之间没有任何交叉验证。Grep 全量 `engine/**/*.go` 确认：14 个清单码**全部**被使用，同时**额外**发射了这 4 个，说明清单是事后补录的且从未回归校验。

**建议改法**：
1. 在 `engine/internal/protocol/protocol.go` 的 `Failure()` 里加一个编译期/运行期白名单校验，传入的 code 不在集合内时打日志（开发构建直接 panic）。
2. 把这 4 个码补进 `protocol/v1/manifest.json:164-179`（如果它们确实是 v1 需要区分的语义），或者按语义复用已有码：`engine_running` → 语义上就是 `invalid_state`；`mtu_failed` → `tun_failed` 或新增；`update_failed` → `invalid_state` 或新增。
3. 在 `contract_test.go` 加一条：遍历所有 `server.go`/`scheduling.go` 里 `protocol.Failure(` 的第二个参数（可用 `go/ast` 或简单的 `go:generate` 清单），断言是清单子集。

---

## P0-2 engineclient 静默丢弃契约声明为 `lossless_ordered, coalescible:false` 的事件

**位置**：`desktop/internal/engineclient/client.go:492-498`
```go
			select {
			case c.events <- event:
			default:
			}
```
配合 `desktop/internal/engineclient/client.go:127` 的 `events: make(chan Event, 64)`。

**契约依据**：`protocol/v1/manifest.json:137-163` 把 `engine.state_changed`、`tun.state_changed`、`host.exiting` 全部声明为 `delivery: "lossless_ordered"`，且 `coalescible: false`；只有 `log.record` 是 `best_effort_ordered`。

**触发条件**：64 个槽位被填满。64 是个很小的缓冲——引擎在启动切换时会密集发 `engine.state_changed`，`log.record`（唯一的 best-effort 事件）也在竞争同一通道。一次 TUN 激活的日志洪峰，或 UI 侧 `Events()` 消费协程被阻塞（例如 Go 侧正在做一次 180s 的提权 Hyper-V 批处理），就能填满它。

**后果**：被丢掉的如果是 `tun.state_changed{state:"failed", last_error:...}`，前端就**永远看不到 TUN 失败**，UI 会停留在上一次的成功状态（`running`），而实际网卡处于 `failed`。这是典型的系统状态与 UI 不一致，且无任何日志。`host.exiting` 被丢则意味着引擎要退出而桌面不知道。

**为什么没兜住**：`select { case ...: default: }` 是非阻塞发送，写代码时通常是为了"不阻塞读循环"，但这里没有任何计数、告警或降级路径。契约明确说了这些事件不可丢，而实现用一个固定 64 槽缓冲 + 静默丢弃，与契约直接冲突。

**建议改法**：把 `events` 通道改为**无界队列**（`chan Event` 配 goroutine + slice 队列 + 条件变量），或至少在丢弃时 `log.Printf` 记录被丢弃的事件名与序列号，使问题可观测。若必须限长，应对 `log.record` 单独限长（它才是 best-effort），对其余三类保证不丢。

---

## P0-3 引擎输出行解析失败时被静默跳过，对应请求挂到超时

**位置**：`desktop/internal/engineclient/client.go:488-489`
```go
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
```

**触发条件**：引擎输出的某一行不是完整 JSON——引擎 panic 后打印的 Go 栈信息、非 JSON 的日志混进 stdout、编码器 bug 产生的截断行、或第三方库（sing-box）往 stdout 写的一行。

**后果**：这一行被 `continue` 跳过。如果它是某个 pending request 的响应，桌面侧那个请求**永远等不到应答**，只能阻塞到 `client.go:442` 的 ctx 超时 `"等待核心响应超时：%w"`。用户看到的是"超时"，而真实原因是"引擎输出了一行坏数据"——**排查方向被彻底带偏**。更糟的是解析错误本身一次都没被记录。

**为什么没兜住**：`continue` 之前既不 log 也不计数。同时它把"传输层损坏"和"业务层超时"折叠成同一个表象。注意 `protocol/v1/manifest.json` 声明了 `max_message_bytes`，但那约束的是单条消息大小，无法防止这一类情况。

**建议改法**：
```go
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			log.Printf("engineclient: dropping undecodable line (%d bytes): %v", len(scanner.Bytes()), err)
			continue
		}
```
并额外：连续丢弃 N 行后主动 `killCurrent`，把"引擎输出已损坏"直接报出去，而不是让调用方空等超时。

---

## P0-4 便携版启动器把"文件已存在"当成"配置写入成功"，截断的 settings.json 被当成成功

**位置**：`desktop/portable/launch-portable.cmd:225-241`
```cmd
> "%PP_DATA%\settings.json" (
echo {
echo   "mode": "proxy",
...
echo }
)
if not exist "%PP_DATA%\settings.json" (
  echo   [错误] 写入 settings.json 失败。
  exit /b 1
)
echo   [完成] 已写入安全的种子配置（TUN 模式需要你自己在设置里切换）。
```

**触发条件**：写入过程中磁盘满、杀软/OneDrive 实时占用该文件、或 `%PP_DATA%` 目录权限在 `mkdir` 成功后被改。`>` 重定向会**先创建/截断**文件，然后才逐条 `echo`。

**后果**：部分写入时文件仍然**存在**，`if not exist` 检查通过，脚本打印 `[完成] 已写入安全的种子配置` 并以 `exit /b 0` 正常退出。用户以为便携版配置就绪，实际得到一个截断/空的 `settings.json`。下一次启动时应用读取这个损坏的配置——这是**用户配置数据损坏 + 失败路径没有回滚**（旧文件已被 `>` 截断，无法恢复）。

**为什么没兜住**：验证手段选错了。文件存在性不是写入成功的证据；脚本手上有完整期望内容却没有做写后回读比对，也没有写临时文件再原子改名。

**建议改法**：改成原子写入 + 内容校验：
```cmd
> "%PP_DATA%\settings.json.tmp" ( ...echo... )
if errorlevel 1 ( echo [错误] 写入失败 & exit /b 1 )
findstr /c:"system_proxy_takeover" "%PP_DATA%\settings.json.tmp" >nul || ( del "%PP_DATA%\settings.json.tmp" & echo [错误] 写入不完整 & exit /b 1 )
move /y "%PP_DATA%\settings.json.tmp" "%PP_DATA%\settings.json" >nul
```
关键是：先写 `.tmp`，校验通过再 `move` 覆盖。这样失败时**原有的 `settings.json` 不受影响**，也就不存在"损坏且无法恢复"的路径。

---

# P1 — 错误难排查（错误码丢失、UI 卡在 loading、失败零信号）

## P1-1 前端所有轮询任务的错误被同一个基础设施 sink 吞成零信号

**位置**：`desktop/frontend/src/platform/serialPoll.ts:25`
```ts
      await task().catch(() => undefined);
```

**已读的调用方**（确认影响面）：
- `desktop/frontend/src/state/useEngineState.ts:318` — `const next = await appServices.engine.snapshot();`，任务体内**无 try/catch**
- `desktop/frontend/src/state/useEngineState.ts:325` — `const next = await appServices.adapters.list();`，同样无 try/catch
- `desktop/frontend/src/pages/BlockedDomainsPage.tsx:61` — `const stop = startSerialPoll(refresh, 3000);`

**触发条件**：引擎进程崩溃、被 `engineclient.killCurrent` 杀掉、或命名管道断开。此时 `appServices.engine.snapshot()` 持续 reject。

**后果**（最严重的一条）：首页的引擎错误提示**只有一个来源**——`desktop/frontend/src/state/useEngineState.ts:237` 的
```ts
      onErrorRef.current(next.reason);
```
也就是说，引擎故障提示只能来自**一次成功的 snapshot 的 `reason` 字段**。一旦 snapshot 开始失败，`applySnapshot` 根本不会被调用，`onErrorRef` 也就永远不会触发——而那些 reject 全被 `serialPoll.ts:25` 吞掉。结果：**引擎已经死了，首页仍然显示 `running`、流量数字冻结在最后一帧、没有任何错误提示、没有日志**。用户只能从"连不上网"去反推。同样地，网卡列表会静默停在最后一次成功的结果。

**为什么没兜住**：`startSerialPoll` 是为"隐藏页面暂停轮询"设计的工具函数，它对 rejection 的处理选了最省事的一种。这个函数被 5 个以上页面复用，把每一处的错误处理能力都降级成了零。

**建议改法**：把 `task` 的 rejection 交还给调用方决定，而不是在工具层统一丢弃：
```ts
export const startSerialPoll = (
  task: () => Promise<void>,
  intervalMs: number,
  options: SerialPollOptions & { onError?: (e: unknown) => void } = {},
) => { ... await task().catch((e) => options.onError?.(e)); ... }
```
然后至少在 `useEngineState.ts:317-320` 的 snapshot 轮询里接上：连续失败 N 次后调 `onErrorRef.current(...)` 并把 phase 置为 `failed`，让 UI 与真实引擎状态一致。

---

## P1-2 生产构建里所有原生平台调用的失败被静默吞掉（`import.meta.env.DEV` 为假时连日志都没有）

**位置**：`desktop/frontend/src/platform/desktop.ts:6-10`
```ts
const ignoreOutsideWails = (error: unknown) => {
  if (import.meta.env.DEV) {
    console.debug("Native desktop action is unavailable in browser preview.", error);
  }
};
```
使用它的导出（同一文件）：`:65 resizeTray`、`:68 minimise`、`:69 toggleMaximise`、`:72 hideToTray`、`:73 show`、`:74-77 showStartup`、`:78 quit`、`:79 openDirectory`、`:80-85 setEngineTrayStatus`、`:24 closeWithAnimation`。

**已读的调用方**：
- `desktop/frontend/src/components/tray/TrayMenu.tsx:101-109` 的 `act()` 调 `desktopPlatform.trayAction(action)` —— 这是**唯一没有**被吞的（`:66-67` 不带 catch），所以"退出"走 tray 菜单时报错；走别处调 `desktopPlatform.quit()`（`:78`）则静默。
- `desktop/frontend/src/state/useEngineState.ts:229` — `void desktopPlatform.setEngineTrayStatus(nextPhase, next.mode);`

**触发条件**：`import.meta.env.DEV === false`（即正式发布构建）下的任何原生调用失败。

**后果**：
- `desktopPlatform.quit()`（`:78`）失败时——比如引擎拒绝停止、runas 提权子进程卡住——用户点"退出"，**界面毫无变化，也没有任何提示**，看起来就是程序卡死。
- `hideToTray()`（`:72`）失败时，"关闭到托盘"静默无反应，用户以为程序崩了，实际窗口还在。
- `setEngineTrayStatus()`（`:80-85`）失败时，**托盘图标与托盘菜单会永久停留在上一次的状态**。用户从托盘看到的"引擎运行中"可能是几小时前的快照。`useEngineState.ts:229` 是 `void` 调用，连一个 handler 都没有。

**为什么没兜住**：`ignoreOutsideWails` 的原意是"浏览器预览环境下没有 Wails runtime，属于预期情况"，这个判断是对的。但它被当成了**通用错误 sink** 用在 `quit`/`hideToTray` 这类真实桌面路径上。函数名和注释（`:47-48` "Browser previews intentionally degrade to no-ops"）说明了它只该服务于 preview 路径。

**建议改法**：
1. 用 `isDesktopRuntime`（已导入自 `./runtime`，见 `desktop.ts:4`）区分两种场景：preview 下静默降级，**真实桌面运行时下必须上报**（至少 `console.error`，并对 `quit`/`hideToTray` 抛给调用方）。
2. `ignoreOutsideWails` 内部无条件 `console.error`，只在 `isDesktopRuntime === false` 时才降级为 `console.debug`。
3. `setEngineTrayStatus` 的失败应触发一次节流后的状态重试，因为托盘状态和主窗口状态不一致本身就是 P1。

---

## P1-3 设置页把"读取网卡失败"替换成"网卡列表为空"，用户看到可信的假数据

**位置**：`desktop/frontend/src/pages/SettingsPage.tsx:364`
```ts
        : appServices.adapters.list().catch(() => []),
```
配合 `desktop/frontend/src/pages/SettingsPage.tsx:373`
```ts
        setAdapters(adapterRuntimeRef.current !== undefined ? [...adapterRuntimeRef.current] : loadedAdapters ?? []);
```

**已读的调用方**：同一个 `Promise.all` 的其余三项（`settings.get()`、`configPath()`、`migrationStatus()`）失败会走 `:375-379` 的 `.catch` 弹错误通知；而网卡这一项被就地吞掉，`Promise.all` 依然 resolve，`settings` 正常加载。

**触发条件**：引擎正在重启/管道断开时打开设置页。`appServices.adapters.list()` reject。

**后果**：设置页正常显示已加载的设置项，但**网卡选择区域显示为"没有找到任何网卡"**——一个看起来完全合理的结论，而不是一个错误。用户会据此认为自己的网卡驱动出问题、或以为程序不支持他的网卡，甚至可能在别的界面里据此做出错误决策。同一批 `.catch(() => [])` 还出现在别处（`settingsQueue` 的相关路径），属于同一类错误。

**为什么没兜住**：这是为了不让一个次要数据源阻塞整个设置页加载（合理的可用性意图），但用 `.catch(() => [])` 实现时，**错误态和空数据态在类型上不可区分**——两者都是 `[]`。

**建议改法**：不要用 `[]` 兜底，改用哨兵值并单独渲染错误态：
```ts
: appServices.adapters.list().then(v => ({ ok: true as const, v }))
        .catch(e => ({ ok: false as const, e })),
```
在 `.then` 里对 `ok === false` 的情况 `notify(...)` 并在该区域显示"读取网卡失败 / 重试"按钮，而不是渲染一个空列表。

---

## P1-4 desktop 合成契约外错误码 `"disconnected"`，与引擎真实故障无法区分

**位置**：`desktop/internal/engineclient/client.go:543`
```go
			reply <- response{Error: &RemoteError{Code: "disconnected", Message: reason.Error()}}
```
所有会话终止路径都汇入这里：`client.go:146-160 Close()`、`:463-465 Kill()`、`:467-481 killCurrent(reason error)`、`:519-533 failSession(session, reason)`、`:541-545 failReplies`。

**触发条件**：引擎进程被杀掉、管道断开、启动失败、超时。

**后果**：`"disconnected"` **不在** `protocol/v1/manifest.json:164-179` 的 14 个码里。一个精心设计的、按码分派的客户端无法把"引擎管道断了"和"引擎回了一个业务错误"分开——两者都是 `RemoteError`，只有一个靠字符串前缀区分的 code，而 desktop 侧除了 `invalid_state` 之外没人查它（见"跨层错误传播结论"）。这直接支撑了 P1-1 的判定：snapshot 轮询失败时前端无法给出任何有针对性的提示。

**建议改法**：把 `"disconnected"` 加入 `protocol/v1/manifest.json` 的 `error_codes`（它确实是 v1 客户端需要区分的语义），并在 `engineclient` 中把它与 `protocol.Failure` 发出的码放进同一个常量集合，让"码在不在契约内"变成可编译检查的事实。

---

## P1-5 路由页的引擎快照轮询失败被吞，加载态的清理依赖请求序号

**位置**：`desktop/frontend/src/pages/RoutingPage.tsx:250-254`
```ts
        appServices.engine.snapshot().then((snapshot) => {
          ...
        }).catch(() => undefined);
```
配合 `desktop/frontend/src/pages/RoutingPage.tsx:292`
```ts
      if (request === loadRequest.current) setLoading(false);
```

**已读的调用方**：`:278-290` 的失败分支——`appServices.routing.snapshot()` 失败时会回落到 `browserRoutingFixture()` 或弹通知；而 `engine.snapshot()` 失败被 `:254` 吞掉，没有任何提示。

**触发条件**：引擎未运行时打开路由页，或路由页的自动刷新遇到引擎重启。

**后果**：加载态能正确解除（不会永久卡住），但引擎快照的失败完全无感。用户看到的是一份"看起来正常"的规则列表，不知道它可能已经过时。另外 `:278-290` 的 fixture 回落是一个**用假数据替换真实数据**的路径——只有在失败时才走，但用户无法分辨自己看到的是真数据还是 fixture。

**为什么没兜住**：两处失败的严重程度不同（路由数据是真数据，引擎快照是辅助数据），但用了同样的"吞掉"处理，区别只在 `:278-290` 有兜底。

**建议改法**：`:254` 的 `.catch` 至少置一个 `snapshotStale` 标记并在页面顶部显示"引擎状态未知，规则可能已过期"；`:278-290` 的 fixture 回落必须显式标注（例如下方加一条 `preview` 提示条），否则用户会把 fixture 当成真实路由。

---

## P1-6 语言设置读取失败被静默吞掉，且 localStorage 写入无保护

**位置**：`desktop/frontend/src/i18n/i18n.tsx:50`
```ts
  void appServices.settings.get()
    .then((loaded) => { ... })
    .catch(() => undefined);
```
以及 `desktop/frontend/src/i18n/i18n.tsx:41`
```ts
  window.localStorage.setItem(storageKey, next);
```
配套的默认来源 `desktop/frontend/src/i18n/i18n.tsx:26-27`
```ts
  window.localStorage.getItem(storageKey) === "en" ? "en" : "zh";
```

**触发条件**：启动时 `settings.get()` 失败（引擎/服务尚未就绪、配置损坏）。用户此前在设置里选过 English（`language: "en"` 持久化在 Go 侧配置里）。

**后果**：持久化的语言选择被静默丢弃，界面回落为 localStorage 默认或 `"zh"`，用户看不出发生了什么，也无从恢复。反向的写入侧同样有问题：`:41` 的 `localStorage.setItem` **没有任何 try/catch**，在隐私模式、存储配额耗尽、或 `localStorage` 被浏览器策略禁用时，`SecurityError`/`QuotaExceededError` 会直接抛进 React 事件处理器——切换语言这个纯 UI 操作会变成一个未捕获异常。

**为什么没兜住**：读取侧用了 `.catch(() => undefined)` 的静默 sink（同 P1-1/P1-2 的模式）；写入侧则完全没考虑 `localStorage` 会失败。注意 `desktop/frontend/src/theme/background.service.ts:21-27, 38-47` 是**做对了**的——它把 localStorage 操作包在 try/catch 里。所以这个仓库里已有正确范式，只是 i18n 没沿用。

**建议改法**：
1. `:41` 包 try/catch，失败时退回内存态（本次会话仍生效）并提示"语言偏好未能保存"。
2. `:50` 的读取失败改为记录并在下一次成功时校正，或至少在设置页显示"未能读取已保存的语言"。

---

## P1-7 托盘菜单的设置读取与外观读取失败被完全静默吞掉

**位置**：`desktop/frontend/src/components/tray/TrayMenu.tsx:80`
```ts
    void appearancePersistence.load().then((res) => { ... }).catch(() => undefined);
```
`desktop/frontend/src/components/tray/TrayMenu.tsx:83`
```ts
    void appServices.settings.get().then((settings) => { ... }).catch(() => undefined);
```

**已读的调用方**：同文件 `:53-66` 的 `refresh()` 对同一批数据的失败是有处理的（`:60-61` `catch { if (alive && active && revision === focusRevision) setUnavailable(true); }`），`:101-109` 的 `act()` 也捕获了 `trayAction` 的错误。唯独 `:80` 和 `:83` 这两条没有。

**触发条件**：托盘菜单获得焦点时，设置服务或外观服务不可用。

**后果**：托盘会显示**默认的语言和外观**，而不是用户保存的选择，且零提示。用户可能以为自己的设置丢了。同一份数据在主窗口里可能显示正确，两处 UI 不一致。

**为什么没兜住**：`.catch(() => undefined)` 的语义是"我知道会失败，但我不管"——这里并没有"不管也无所谓"的理由，托盘显示错误的状态比不显示更有害。

**建议改法**：`:80`/`:83` 失败时至少 `setUnavailable(true)`（复用 `:60-61` 已有的机制），或回退到 `desktopPlatform.engine.trayStatus()`（`desktop/frontend/src/platform/services.ts:300`）已能拿到的最小信息，并显示"设置不可用"。

---

## P1-8 便携版"恢复环境"脚本丢弃 `sc start` 的失败原因

**位置**：`desktop/portable/restore-environment.cmd:79`
```cmd
sc start HypoMuxCore >nul
```
后续 `:80-92` 是一个最多 10 秒的轮询，失败时输出：
```cmd
    echo   [错误] 10 秒内未能启动 HypoMuxCore。
```

**触发条件**：`HypoMuxCore` 服务存在但无法启动——依赖服务未运行、启动类型被改、`ImagePath` 损坏、或账户密码/依赖权限问题。这些情况下 `sc start` 会返回非零并打印**具体原因**（例如 1053 "The service did not respond in a timely fashion"、1067 "进程意外终止"）。

**后果**：这些信息全部被 `>nul` 丢弃。用户看到的只有"10 秒内未能启动 HypoMuxCore"，然后被建议"手动执行 sc start HypoMuxCore"——但如果他们真的手动执行，得到的错误码正是刚刚被脚本扔掉的那一条。**脚本把诊断答案扔掉，然后让用户自己去重算一遍。**

**为什么没兜住**：`>nul` 出现在这里大概是为了让输出整洁，但 `sc` 的错误输出恰恰是这个脚本存在的意义。同一文件的 `:69`、`:82` 也用 `>nul` 丢弃了 `sc query` 的输出（这两个是 grep 用的，尚可接受），`:103` 的最终复核 `sc query HypoMuxCore` 反而保留了输出——说明作者知道该保留。

**建议改法**：
```cmd
sc start HypoMuxCore
if errorlevel 1 (
  echo   [错误] sc start HypoMuxCore 返回 %errorlevel%，上面的错误信息是原因。
  goto pp_fail
)
```
并在 `pp_fail` 前把原始输出留在屏幕上。`launch-portable.cmd:183` 的 `sc stop HypoMuxCore >nul` 是同样的问题。

---

## P1-9 便携版"恢复环境"脚本的进程清理可能失败却报告成功

**位置**：`desktop/portable/restore-environment.cmd:57`
```cmd
  ... else { $p | ForEach-Object { Write-Host ('  [停止] PID ' + $_.ProcessId + '  ' + $_.ExecutablePath); Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; Write-Host ('  [完成] 已结束 ' + $p.Count + ' 个便携版进程。') }"
```

**触发条件**：目标进程正在退出/已退出（PID 复用）、或被另一个受保护进程持有。

**后果**：`-ErrorAction SilentlyContinue` 让 `Stop-Process` 的失败完全无声，然后脚本**无条件**打印 `[完成] 已结束 N 个便携版进程。`$p.Count` 统计的是**找到的**进程数，不是**杀掉的**数。用户被告知清理完成，实际上残留进程还在运行。

**为什么没兜住**：计数变量取自筛选结果 `$p.Count`，而不是实际成功次数。`-ErrorAction SilentlyContinue` 在需要区分"失败"和"本来就没事"的场景下是错的默认——这里两者都需要报告。

**建议改法**：统计成功数并对失败单独报告：
```powershell
$ok = 0; $fail = @(); $p | ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop; $ok++ } catch { $fail += $_ } }
Write-Host ("  [完成] 已结束 {0} 个便携版进程。" -f $ok)
if ($fail.Count) { Write-Host ("  [警告] {0} 个进程未能结束：{1}" -f $fail.Count, (($fail | ForEach-Object ProcessId) -join ', ')) ; exit 2 }
```

---

# P2 — 代码整洁度

## P2-1 通知错误码由字符串子串匹配推导，与真实故障类别不相关

**位置**：`desktop/frontend/src/components/notifications/errorCodes.ts:14-20`
```ts
const rules: ErrorCodeRule[] = [
  { code: "HM-E1001", matches: (text) => includesAny(text, ["timeout", "timed out", "deadline exceeded", "超时"]) },
  { code: "HM-E1002", matches: (text) => includesAny(text, ["named pipe", "connection refused", "failed to fetch", "服务不可用", "命名管道", "连接被拒绝"]) },
  { code: "HM-E1003", matches: (text) => includesAny(text, ["permission", "access denied", "administrator", "elevated", "权限", "拒绝访问", "管理员"]) },
```
兜底 `desktop/frontend/src/components/notifications/errorCodes.ts:33`
```ts
  return rules.find((rule) => rule.matches(text, key))?.code ?? "HM-E1900";
```

**后果**：这是一个给支持人员看的"支持码"，但它的推导依据是**已经被拍扁的英文/中文子串**，准确性完全取决于文案。`conciseDiagnosticMessage`（`notification/notificationMessage.ts:9-63`）已经把原始消息分类过一遍，所以 `HM-E1001..HM-E1004` 大致可用；但它与协议的 14 个码**没有任何对应关系**，用户报 `HM-E1001` 时无法确定是哪个协议错误。而 `HM-E1201..HM-E1700` 那一段（`:19-27`）只按 `dedupeKey` 前缀匹配，**完全不看错误内容**——同一个前缀下的所有错误都塌缩成同一个码。

**建议改法**：让 desktop 在跨层时把 `RemoteError.Code` 一起送到前端（Wails 支持结构化返回值），`errorCodes.ts` 改为优先按真实 code 匹配，字符串匹配降级为兜底。

---

## P2-2 热点配置面板在无配置时提前返回，不给用户任何反馈

**位置**：`desktop/frontend/src/components/HotspotPanel.tsx:118`
```ts
      if (!displayedConfig) return;
```
（位于 `:114-123` 的 `save()` 的 try 块内，`finally` 会正常清理 `busy`/`setPending`）

**后果**：保存操作静默无反应。加载态会解除，但既没有成功提示也没有错误——用户无法判断是保存了还是被跳过了。

**建议改法**：改为 `if (!displayedConfig) { setError(t("hotspot_no_config", "热点尚未配置")); return; }`。

---

## P2-3 外观配置反序列化无保护，损坏的后端数据会 reject 到所有调用方

**位置**：`desktop/frontend/src/theme/background.service.ts:57`
```ts
    const parsed = JSON.parse(payload) as PersistedBackground;
```
和 `desktop/frontend/src/theme/background.service.ts:70`（`JSON.parse(migrated)`，迁移路径）

**已读的调用方**：`desktop/frontend/src/components/tray/TrayMenu.tsx:80` — `void appearancePersistence.load().then(...).catch(() => undefined);`（这条被静默吞掉，见 P1-7）。

**后果**：Go 侧持久化的外观 JSON 一旦损坏，`load()` 直接抛 `SyntaxError`。同文件的 localStorage 路径（`:21-27`、`:38-47`）**都**做了 try/catch，唯独 `JSON.parse` 没有——同一个函数里保护标准不一致。

**建议改法**：把两处 `JSON.parse` 包进 try/catch，失败时按"无持久化外观"处理并记一条日志。

---

## P2-4 托盘动作失败时丢弃真实错误

**位置**：`desktop/frontend/src/components/tray/TrayMenu.tsx:106-107`
```ts
    try {
      await desktopPlatform.trayAction(action);
    } catch {
      setError(text("操作失败，请重试", "Action failed. Try again."));
    }
```

**后果**：`quit` 失败和 `show` 失败给用户完全相同的提示。退出失败是最需要区分的一种（见 P1-2）。

**建议改法**：`catch (reason)` 后 `setError(\`操作失败：${String(reason)}\`)`，至少让"退出失败"和"显示失败"在文案上可区分。

---

## P2-5 CI 中 `go install` 未检查退出码

**位置**：`.github/workflows/build.yml:120-121`
```yaml
          go install "github.com/wailsapp/wails/v3/cmd/wails3@${env:WAILS_VERSION}"
          choco install nsis --yes --no-progress
```

**为什么只是 P2**：紧邻的 `:125-131` 有显式的 NSIS 编译器存在性与版本校验（`throw "NSIS compiler was not found at $makensis."`），所以 `choco install` 的失败会在 6 行后被抓住。`go install` 没有对应的校验，但随后的 `wails3 generate bindings`（`:142`）与 `wails3 task windows:package`（`:191`）会立刻失败。这是本文件里唯一没有 `$LASTEXITCODE` 检查的原生命令，与全文件其余部分的严谨风格不一致。

**建议改法**：`go install` 后加 `if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }`，与 `build.yml:92`、`:106` 的写法保持一致。

---

## P2-6 engineclient 进程与传输清理的错误被丢弃

**位置**：`desktop/internal/engineclient/client.go:285`、`:291`、`:302`、`:477`、`:478`、`:531`
```go
	_ = session.process.Kill()
	_ = session.process.Wait()
	_ = session.closeTransport()
```

**后果**：清理阶段的失败（进程已退出、句柄已关闭）确实是良性的，忽略是合理的。但在 `:302` 的 `Wait()` 场景下，进程的非零退出码正是"引擎为什么死"的唯一线索（Windows 服务场景下会以异常退出码结束）。这与 P0-3 是同一类信息丢失。

**建议改法**：至少用 `log.Printf` 记下非零退出码（`Wait()` 的返回值），便于在事后日志里还原引擎的死亡原因。

---

## P2-7 热点面板的状态刷新失败后保留陈旧状态且无任何提示

**位置**：`desktop/frontend/src/components/HotspotPanel.tsx:147`
```ts
    } catch {
      /* Preserve the action error; the next poll retries status. */
    }
```
位于 `:134-149` 的 `change(start)` 失败处理中——操作本身失败时 `:142-143` 已经 `setError`，此处是为刷新状态做的**第二次**尝试。

**结论**：这个吞掉是**有正当理由且注释写清楚了**的（保留操作错误，避免覆盖）。但若第二次刷新也失败，`status` 就停留在旧值且不再有任何提示——注释里承诺的"下次轮询会重试"只在轮询继续时才成立。

**建议改法**：在第二次失败时设置一个 `statusStale` 标志，让 UI 明确显示"状态未知"，而不是让陈旧值看起来是当前值。

---

# 已检查且未发现问题的项

- **`.github/` 全部 4 个 workflow**：无 `continue-on-error`、无被注释掉的检查步骤、无会掩盖失败的管道构造。3 处 `|| true` 全部经过人工核实：`release-smoke.yml:79` 与 `create-release-tag.yml:49` 在 `trap cleanup` 清理函数中；`build.yml:555` 与 `build.yml:605`/`:634` 的结果被下游 `if [[ -z ... ]]`/`case` 分支以 `exit 1` 显式接管。`build.yml:152-166` 对 `go test`/`go vet` 的四个模块逐一检查 `$LASTEXITCODE`。
- **前端测试与类型检查是否在 CI 中执行**：`desktop/frontend/package.json:9` 的 `"build": "tsc && vite build --mode production"`，配合 `.github/workflows/build.yml:150`，类型错误会导致 CI 失败。未发现类型检查被绕过的路径。
- **`desktop/frontend/src/platform/latestSaveQueue.ts`（全文 86 行）**：设计正确。`pump()` 在失败时 `settleThrough(job.revision, waiter.reject)` 拒绝所有 `revision <=` 的 waiter，而更新的 pending 工作会被重试（`:65-74`）；`enqueue` 在 `:40-43` 挂了 `this.tail = done.then(ok, err)`，保证 `done` 永远有 handler，**不会产生 unhandled rejection**。
- **`desktop/frontend/src/platform/settingsQueue.ts`**：失败时 `applyMerged(outcome.restore)` 后 `throw outcome.error`，回滚由调用方通过 `restore` 字段提供，语义清晰。已验证调用方 `desktop/frontend/src/pages/SettingsPage.tsx:393-401` 正确实现了回滚：`catch` 中 `await appServices.settings.get().catch(() => settings)` 取回权威状态作为 `restore`，且 `.catch` 的兜底值是**保存前**的 `settings`（闭包捕获，`setSettings(next)` 不改变它）——回滚语义正确。这与 `protocol/v1/README.md:100-109` 中 `engine.scheduling` 的回滚契约一致。
- **`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:410-539`**：对"部分失败"（`:445` 空批次不触发重启提示、`:520` `partitionRemoveResults` 逐行报告）、"busy 标志滞留"（`:479-485` 注释明确说明为何 `setBusy(false)` 不能加 `mounted` 守卫）、"超时预算低于后端上限会导致重复 UAC"（`:506-513` 注释引用了具体的 Go 行号）都有逐行论证。**未发现问题。**
- **`desktop/frontend/src/pages/ToolsPage.tsx:52-75`**：加载失败有 `setError` + 重试按钮（`:92`）、保存失败有 `notify` + `setRevision` 触发重读（`:71-74`），loading/saving 在 `finally` 中清理。**未发现问题。**
- **`desktop/frontend/src/pages/BlockedDomainsPage.tsx:44-67`**、`desktop/frontend/src/components/RuleSetsPanel.tsx:60-94`：均为 `try/catch/finally` + 请求序号守卫（`sequence === requestSequence.current`）的正确范式，失败有 `notify`/`loadError`。**未发现问题。**
- **`desktop/frontend/src/pages/MTUDetectionPage.tsx:40`** 是全前端唯一的裸 `.catch(() => {})`：
  ```ts
      return () => { generation.current++; if (operation.current === "detect") void appServices.mtu.cancel().catch(() => {}); };
  ```
  位于组件**卸载清理**路径，取消失败时已无可通知的用户，且 `generation` 递增已使结果不可回写。**合理，不作为发现。**
- **`desktop/portable/PORTABLE-README.txt`** 与 `launch-portable.cmd` 的参数解析（`:43-54`）、前置检查（`:67-117`）均为"检查失败即 `goto pp_fail` + `exit /b 1`"的正确范式，`--check` 干跑模式也保持了一致的退出码约定（`:250-254`）。除 P0-4 与 P1-8 指出的一处外，未发现吞失败。
- **`desktop/scripts/`** — 该目录**不存在**，`Glob desktop/{portable,scripts}/**/*` 只返回 `desktop/portable/` 下的 3 个文件。