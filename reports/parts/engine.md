# engine/ 错误处理审计报告

> 范围：`engine/` 下**全部非测试 `.go` 文件**（Go 核心守护进程 `hypomux-engine`）。
> 方法：纯只读审计 —— 未修改任何源文件，未运行构建或测试。每条发现均给出 `文件路径:行号:该行原文引用`，
> 并按「触发条件 → 后果 → 为什么当前的错误处理没兜住 → 建议怎么改」四段展开。
> 本报告由四个分片合并而成，分片边界见下表，各分片内的原始发现编号保持不变，统一加 `[tag]` 前缀以避免跨分片撞号。

## engine/ 概览

本次审计在 `engine/` 共得到 **97 条发现（P0 19 / P1 40 / P2 38）**。整体健康度可以概括为「**局部严谨、整体没有事务边界**」：
`tun.Supervisor` 的两阶段状态机、`cleanupRun` 的 `sync.Once` 幂等、`errors.Join` 的汇总用法、`wfp` 的 WFP 事务 + dynamic session，
以及 `engine/internal/fileintegrity` 的哈希校验路径，都写得比多数生产代码好，跨包的调用方也普遍会核实前置条件；
真正的问题不是"漏掉了某个 `err != nil`"，而是**任何涉及系统状态的多步改动都没有定义 undo**。
最集中的三个问题是：**(1)** 安装/激活路径上「停服务 → 改配置 → 写注册表 → 设恢复动作 → 启动」这类不可原子化的序列，
失败后已创建的服务、已停掉的服务、已降级的策略键会原样留在机器上（`engine/cmd/hypomux-engine/service_policy_windows.go:100`、`engine/cmd/hypomux-engine/service_windows.go`）；
**(2)** **整个 `engine/` 没有任何一处 `recover()`** —— 一个会装 WFP 规则、建 Wintun 虚拟网卡、改默认路由的特权守护进程，
任意一处 panic 都会终止进程（而非单个 goroutine），导致 `stopProxyForHostExit` / `terminateRun` / `cleanupRun` 一个都不执行，
结果是幽灵网卡 + 指向不存在隧道的默认路由 + 服务被自动重启后的崩溃循环；
**(3)** 错误链在 Win32 与 DoH 边界被系统性压平 —— `%s` 而非 `%w` 包装、`syscall.Errno` 拍平成字符串、
RCODE 压成一句 `DNS response code %d`（`engine/internal/dns/wire.go:112`）、ctx 超时渲染成 `signal: killed`
（`engine/internal/tun/cleanup_windows.go:87-95`）—— 上层因此无法区分"需要提权""重复安装""上游超时""域名不存在"，
而下游 `engine/internal/proxy/dial.go:86` 恰恰拿这些**不可区分**的错误做 adapter 健康惩罚（阈值见 `engine/internal/proxy/health.go:15`），
于是错误分类的丢失最终会转化为错误的基础设施选择。

## 分片与统计

| 分片 | 覆盖目录 | 产出者 | P0 | P1 | P2 | 小计 |
|---|---|---|---:|---:|---:|---:|
| `[core]` | `engine/internal/{tun,wfp,vnic,platform}/` | engine-errors | 3 | 10 | 7 | **20** |
| `[cmd]` | `engine/cmd/hypomux-engine/` | engine-cmd-errors | 6 | 11 | 14 | **31** |
| `[proxy]` | `engine/internal/proxy/` | engine-proxy-errors | 6 | 8 | 5 | **19** |
| `[dns-server]` | `engine/internal/{dns,server,runtime,expiry,fileintegrity,diagnostic,protocol,api}/` | engine-dns-srv-errors | 4 | 11 | 12 | **27** |
| **合计** | `engine/` 全部非测试 `.go` | — | **19** | **40** | **38** | **97** |

## 跨分片交叉结论（合并时发现的同源问题）

1. **零 `recover()` 是全局根因，不是四个独立问题。** `[core] P1-1`、`[cmd] P0-4`、`[proxy] P0-1`、`[dns-server] P1-2` 从四个方向报告了同一件事。
   `[core] P1-1` 用全域 `Grep recover\(\)` 确认命中为 0（唯一的 `panic(` 是 `engine/internal/wfp/dns_exemption_windows.go:420` 的 `mustGUID`，
   仅在包初始化执行且参数为硬编码字面量）。**建议作为一次性整改**，而不是分四处打补丁。
2. **WFP DNS 豁免的句柄/状态泄漏有两条 manifestation，同一个契约缺陷。**
   `[core] P0-3` 指出 `engine/internal/wfp/dns_exemption_windows.go:196-200` 丢弃 `Close()` 错误，
   而 `Close()`（同文件 `:375-381`）在失败时**故意保留句柄以便稍后重试**——但这个"later Close"因为 `owned` 是局部变量而永不存在，
   `FwpmSubLayerAdd0`（`:215`）在 WFP 事务之外创建的 Weight `0xFFFF` 子层因此泄漏到进程结束；
   `[dns-server] P0-3` 从 server 侧报告了它的后果：新的豁免已装、旧的已拆之后才做可能失败的代理更新，没有还原，
   运行时状态与代理实际使用的 adapter 池永久错位。两处调用点为 `engine/internal/server/server.go:650` 与
   `engine/internal/server/scheduling.go:50` 的 `_ = exemption.Close()`。**修 `Close()` 契约 + 给替换操作加回滚。**
3. **`vnic.Manager` 的回滚谎报成功。** `[core] P1-4` 定位到 `engine/internal/vnic/manager.go:170-179`：
   `_, _ = m.controller.Stop(stopCtx)` 丢弃回滚错误后仍置 `m.deployed = false; m.failed = true`，
   而 `statusLocked()`（`:295-297`）会用 `m.failed` 覆盖从 supervisor 读到的真实状态 —— 内核里有活着的 Wintun 适配器和 keeper 进程，对外却坚称 `StateFailed`。
4. **退出路径会跳过唯一的全局回滚。** `[cmd] P0-3` 指出 Service 的 `Execute` 在 15s 超时后主动让进程退出，
   `[core] P1-1` 指出 panic 同样直接杀进程；两条路都会跳过 `Server.stopProxyForHostExit`
   （`engine/internal/server/server.go:1012-1041`，由 `Run` 的 defer 触发），即 DNS 豁免关闭、TUN 停止与虚拟网卡删除全部不会执行。
   **这解释了为什么上面 1~3 条的后果在生产环境里会被放大成"机器彻底断网"。**

## 发现索引（按严重程度）

### P0 — 共 19 条

- **[core] P0-1 · `Activate` 在 `containProcess` 失败时跳过网络清理，留下 Wintun 网卡与默认路由
- **[core] P0-2 · `hasOwnedTunDevice` 把枚举错误吞成"没有残留设备"，导致完整清理路径被跳过
- **[core] P0-3 · `OpenDNSExemption` 失败路径丢弃 `Close()` 错误，泄漏 WFP 引擎句柄与子层
- **[cmd] P0-1 `writeCoreServicePolicy` 写一半失败会摧毁原本可用的策略，且无回滚
- **[cmd] P0-2 `installWindowsService` 改了系统状态后没有 undo：新建服务失败不删除，更新路径失败后服务永远不再启动
- **[cmd] P0-3 Service 停机超时直接退出进程，跳过唯一的 DNS/TUN/VNIC 回滚，并回报成功
- **[cmd] P0-4 两个后台 goroutine 都没有 `recover()`，panic 会杀掉 LocalSystem 服务进程并跳过全部清理
- **[cmd] P0-5 `requireMachineInstallLocation` 把 `libcronet.dll` 的 stat 错误当成「文件不存在」，直接跳过 ACL 校验
- **[cmd] P0-6 `removeWindowsService` 的系统状态回滚不对称：卸载路径不留痕，删服务与删注册表键之间没有补偿
- **[proxy] P0-1 没有任何 per-connection goroutine 装 `recover()`，一个客户端即可打崩整个引擎
- **[proxy] P0-2 UDP 数据面创建 flow 的错误被完全吞掉，连日志都没有
- **[proxy] P0-3 复用已有 flow 时的发送错误被丢弃，死亡 flow 永不退役
- **[proxy] P0-4 `WriteToUDP` 失败与短写都不记录失败，死亡 flow 永不退役
- **[proxy] P0-5 acceptLoop 在持续性 Accept 错误下热自旋，打满一个 CPU 核
- **[proxy] P0-6 默认配置下 UDP ASSOCIATE 必然失败，且失败原因被伪装成"未知 channel"
- **[dns-server] P0-1 超时类失败被 `ctx.Err()` 完全吞掉，DNS 遥测与 fallback 事件同时失真
- **[dns-server] P0-2 RCODE 被压平成无类型字符串，NXDOMAIN / SERVFAIL 与自身故障不可区分，并连带污染 adapter 健康
- **[dns-server] P0-3 WFP DNS 豁免替换失败后没有回滚，运行时状态与代理实际用的 adapter 池永久错位
- **[dns-server] P0-4 DoH 竞速批次在 `ctx.Done()` 分支丢弃已经成功的结果，把成功变成失败

### P1 — 共 40 条

- **[core] P1-1 · 整个 `engine/` 没有任何 `recover()`，特权守护进程一次 panic 就留下系统状态残留
- **[core] P1-2 · `cleanupRun` 被 `ctx` 超时跳过时，`Stop` 返回的清理失败信息彻底丢失
- **[core] P1-3 · PowerShell 清理 / MTU / 共享检测把 `ctx` 超时报告成"脚本失败"，丢失全部因果
- **[core] P1-4 · `vnic.Manager.Create` 丢弃回滚错误，并对仍存活的虚拟网卡谎报 `failed`
- **[core] P1-5 · `vnic.Manager` 在最长 60 秒的阻塞调用期间持有 `m.mu`
- **[core] P1-6 · `FwpmGetAppIdFromFileName0` 返回 NULL 时未校验，把空指针交给内核
- **[core] P1-7 · WFP 所有 Win32 调用的错误用 `%s` 包装，`errors.Is` 永久失效
- **[core] P1-8 · TUN 就绪探测把 Win32 错误吞成"还没就绪"，20 秒后报一个不相关的原因
- **[core] P1-9 · 暂存配置的删除错误被吞，凭据文件可能留在 ProgramData
- **[core] P1-10 · `Stop` 无条件把状态重置为 `StatusStopped`，抹掉全部失败细节
- **[cmd] P1-1 `fmt.Errorf("%w: %v", ...)` 把真实拒绝原因降级成不可追溯的字符串
- **[cmd] P1-2 注册表 ACL 读取失败被伪装成「没有可信 DACL」
- **[cmd] P1-3 认证握手 JSON 解析失败时原始错误被丢弃，无法区分「格式错」和「对端不是 HypoMux」
- **[cmd] P1-4 `deleteCoreServicePolicy` 用 `!=` 比较错误，破坏错误链
- **[cmd] P1-5 WTS 调用失败时用可能为 errno(0) 的 `callErr` 包装，产生「The operation completed successfully」
- **[cmd] P1-6 正常 Ctrl+C 退出被判为失败：退出码 1 + "context canceled"
- **[cmd] P1-7 `runtimeServerMetadata` 静默降级：路径解析失败时 `TunExecutable` 为空，无任何日志
- **[cmd] P1-8 `buildCoreServicePolicy` 的目录布局检查是恒真式，永不生效
- **[cmd] P1-9 `requireProtectedCoreACL` 对非普通 allow ACE 直接硬失败，错误信息误导
- **[cmd] P1-10 `stopWindowsService` 的错误丢失服务名与阶段信息，卸载路径直接裸传
- **[cmd] P1-11 管道连接没有写超时：对端不读时写入永久阻塞，优雅停止被拖到 15s 超时
- **[proxy] P1-7 上行方向（客户端 → 上游）的中继错误被丢弃，CDN 试运行永远不会标记失败
- **[proxy] P1-8 探测错误被替换成不可区分的通用字符串，再被字符串匹配消费
- **[proxy] P1-9 Steam 探测链路的 resolver 构造失败静默返回，完全不打点
- **[proxy] P1-10 UDP ASSOCIATE 已建立后，`serve` 的错误被丢弃且不再发 SOCKS 应答
- **[proxy] P1-11 UDP 关联的数据报泵在客户端 goroutine 上同步阻塞，单个慢目标即可拖垮整个关联通道
- **[proxy] P1-12 `Attach` 在释放 `session.mu` 之后才写入计数器，与 `Finish` 的无锁读构成竞态
- **[proxy] P1-13 Steam 发现 goroutine 未纳入 `s.wg`，`Stop` 超时路径还会泄漏一个看门狗 goroutine
- **[proxy] P1-14 `newSteamObserver` 与 `expire`/`feed`/`stopLocked` 的锁获取顺序相反，仅靠"发布时机"侥幸避免死锁
- **[dns-server] P1-1 `SetDeadline` 失败被丢弃，`runLookup` 可能永久挂起并毒化 inflight 缓存键
- **[dns-server] P1-2 `runLookup` goroutine 无 `recover`，一个 panic 会静默杀死查询并留下永不解开的 inflight 条目
- **[dns-server] P1-3 DoH 非 200 响应体未排空，连接无法复用
- **[dns-server] P1-4 主机关闭指令无条件回报成功，停机失败被静默吞掉
- **[dns-server] P1-5 诊断探针把一次瞬时 bind 失败固化成"不可用"，健康链路被误报
- **[dns-server] P1-6 ICMP 回显的完整状态码被整个丢弃，`Probe` 无法区分"超时"和"不可达"
- **[dns-server] P1-7 ICMP 探针的 WinError 码在非 bind 失败场景被静默丢弃
- **[dns-server] P1-8 `parseResponse` 丢弃 `skipName` 的底层错误，畸形报文与截断不可区分
- **[dns-server] P1-9 NODATA 与解析失败被合并成同一条错误
- **[dns-server] P1-10 文件完整性校验的错误不带路径和摘要，无法定位是哪个文件
- **[dns-server] P1-11 `NormalizeConfig` 静默追加硬编码公共 DNS，运维显式配置的"仅用我的服务器"被覆盖

### P2 — 共 38 条

- **[core] P2-1 · `wfp.call` 之外的 `UTF16PtrFromString` 错误全部丢弃
- **[core] P2-2 · `InspectWFP` 探测路径丢弃句柄关闭错误
- **[core] P2-3 · `containProcess` 的三条错误路径丢弃 `CloseHandle`
- **[core] P2-4 · `GetConsoleProcessList` 成功时把 `nil` 错误用 `%v` 印出来
- **[core] P2-5 · `fmt.Errorf` 无格式动词，应为 `errors.New`
- **[core] P2-6 · `buildRules` 静默丢弃无法解析的适配器，依赖跨包的隐式校验
- **[core] P2-7 · `OpenDNSExemption` 丢弃 `os.Stat` 错误，路径错误与"是目录"不可区分
- **[cmd] P2-1 `removeWindowsService` 中停服失败后仍留下已停止的服务
- **[cmd] P2-2 多处 `Close`/`Disconnect` 的返回错误被丢弃
- **[cmd] P2-3 `serve-pipe` 路径对同一条管道连接执行两次 Close
- **[cmd] P2-4 `pathWithin` 是死代码，唯一的安全不变量「所有受保护路径都在 ProgramData 根下」从未被校验
- **[cmd] P2-5 `connectAuthenticatedPipe` 在 ctx 已取消时仍会先做一次 `CreateFile`
- **[cmd] P2-6 `requireFixedNTFSVolume` / `fixedLocalVolumeRoot` 的 `UTF16PtrFromString` 错误裸传
- **[cmd] P2-7 `validatePinnedSHA256` 把「非十六进制」和「长度不对」合并成一条消息
- **[cmd] P2-8 `interruptServicePipeConnection` / `connectServicePipe` 丢弃全部 Win32 错误
- **[cmd] P2-9 `newPipeFile` 的类型断言失败消息不含实际类型
- **[cmd] P2-10 `finalPathForHandle` 每次迭代重新分配缓冲区，且失败原因未区分「太长」与「空」
- **[cmd] P2-11 `WTSFreeMemory` 的返回状态被忽略，且 `defer` 中调用的是 `Proc.Call`
- **[cmd] P2-12 `main.go` 的参数校验失败一律静默返回 2
- **[cmd] P2-13 `runWindowsService` 把服务启动失败报成普通退出码，SCM 侧看不出原因
- **[cmd] P2-14 `serveCoreServicePipe` 对被拒客户端无退避，`os.Stderr` 被刷屏
- **[proxy] P2-15 `handleClient` 的返回值被直接丢弃，`_ = adapter` 是死赋值
- **[proxy] P2-16 `writeHTTPError` 用 `_, _ =` 抹掉全部 HTTP 失败原因
- **[proxy] P2-17 `steam_accounting.go` 的失败窗口表容量上限在计数器已递增之后才判断
- **[proxy] P2-18 上下文取消后生产者循环不 `break`，继续空转迭代
- **[proxy] P2-19 observer 定时器自重入，唯一停止路径依赖调用方记得调用 `close()`
- **[dns-server] P2-1 `normalizeRecordType` 的错误被丢弃
- **[dns-server] P2-2 `context.AfterFunc` 返回的 stop 函数被丢弃
- **[dns-server] P2-3 `resolver.go:122` 用 `fmt.Errorf` 拼无格式串的常量
- **[dns-server] P2-4 DoH 连接池淘汰只关空闲连接，在途请求的连接被遗留
- **[dns-server] P2-5 `wire.go:139` 的负长度检查是死代码
- **[dns-server] P2-6 `wire.go` 的假 IP 过滤只作用于 A 记录，AAAA 不对称
- **[dns-server] P2-7 `server.go` 的 JSON 解析错误被替换成固定文案
- **[dns-server] P2-8 内部错误的原文被直接返回给远端客户端
- **[dns-server] P2-9 `runtime.Transition` 的错误在 9 处调用点被 `_` 丢弃
- **[dns-server] P2-10 `host.exit` 停机路径全链路 `_ =` 丢弃
- **[dns-server] P2-11 DoH 传输的端点 URL 非法时静默丢弃，不报错
- **[dns-server] P2-12 `SteamCDNStatus` 在代理未运行时返回"成功但空配置"

---


---

## 分片 A · `[core]` — engine/internal/{tun,wfp,vnic,platform}

## 本节概览

这几个包的整体错误处理水平**明显高于仓库平均水平**：`tun.Supervisor` 有真正的两阶段状态机（`Activate` / `failStartedRun` / `terminateRun` / `cleanupRun`），回滚用 `errors.Join` 汇总，`cleanupRun` 用 `sync.Once` 保证幂等，`wfp.OpenDNSExemption` 用 WFP 事务 + dynamic session 让内核侧对象随句柄关闭自动消失。作者显然想过回滚问题。

问题高度集中在**三处系统状态改动点上**：(1) TUN sidecar 启动后、回滚机制接管前的那段"空窗期"；(2) **整个 `engine/` 没有任何一处 `recover()`**——对一个装 WFP 规则和虚拟网卡的特权守护进程，任何一处 panic 都会直接留下系统状态残留；(3) 用 PowerShell 子进程做系统清理的三个文件（`cleanup_windows.go`、`sharing_windows.go`、`mtu_windows.go`）**全部把子进程输出拼进 `%s` 而不是 `%w`**，且全部不区分"ctx 超时被 kill"和"脚本真的失败"。此外 `hasOwnedTunDevice` 的枚举错误被 `continue` 吞掉后**反而跳过了完整清理路径**，与其注释声明的 fail-safe 意图正好相反——这是本节最值得修的一处。

---

## P0

### [core] P0-1 · `Activate` 在 `containProcess` 失败时跳过网络清理，留下 Wintun 网卡与默认路由

**位置**：`engine/internal/tun/supervisor.go:241-247`

```go
	containment, err := s.contain(command.Process)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		s.failStart(err)
		return s.Status(), fmt.Errorf("contain sing-box process: %w", err)
	}
```

**触发条件**
`command.Start()`（`engine/internal/tun/supervisor.go:237`）已经成功，sing-box 子进程已经在运行并有机会创建 Wintun 适配器、写入默认路由、安装 WFP 规则。随后 `s.contain(command.Process)` 返回错误。`containProcess` 有一条现实触发路径：`engine/internal/tun/process_windows.go:130` 的 `windows.AssignProcessToJobObject` 在进程已处于某个 job 中（服务/启动器嵌套 job、CI runner、某些组策略包装器）时返回 `ERROR_ACCESS_DENIED`。`OpenProcess(PROCESS_SET_QUOTA|PROCESS_TERMINATE)` 也会在权限被裁剪时失败。

**后果**
进程被 kill、暂存配置文件被 `defer`（`supervisor.go:177-181`）删除、状态置为 `StateFailed`——但 **`s.cleanup(...)` 一次都没有被调用**。sing-box 已经装好的 `HypoMux-Tun` Wintun 适配器和 `0.0.0.0/0`、`::/0` 默认路由原样留在系统里。用户看到"启动失败"，网络却已经断了，且没有任何日志或错误提示指向残留设备。只有在用户**再次**触发 `Activate` 时，`supervisor.go:212` 的 `s.cleanupWithTimeout(ctx)` 才会顺带清掉；不重试就一直残留。这正是"网卡残留"类 P0。

**为什么没兜住**
对比同文件 `failStartedRun`（`engine/internal/tun/supervisor.go:313-341`）：它正确调用了 `s.terminateRun` → `cleanupRun` → `s.cleanup(cleanupCtx)`。`Activate` 里 227-236 行的 `StdoutPipe`/`StderrPipe`/`Start` 失败早返回也确实不需要清理（进程还没跑）。**唯独 241 行这一条早退路径落在"进程已启动"之后却没有走 `failStartedRun`**，是这条回滚链上唯一的缺口。`failStart`（`supervisor.go:636-643`）只写状态，不做任何清理。

**建议改法**
把 `Start()` 之后的所有失败收敛到同一条回滚路径，不要就地 kill：

```go
	containment, err := s.contain(command.Process)
	if err != nil {
		// 进程已启动：sing-box 可能已经建好 Wintun 适配器/路由/WFP 规则，
		// 必须走完整回滚，不能只 Kill。
		_ = command.Process.Kill()
		_ = command.Wait()
		rollbackCtx, cancelRollback := context.WithTimeout(
			context.Background(), cleanupTimeout,
		)
		cleanupErr := s.cleanup(rollbackCtx)
		cancelRollback()
		cause := fmt.Errorf("contain sing-box process: %w", err)
		if cleanupErr != nil {
			cause = errors.Join(cause, fmt.Errorf(
				"rollback after containment failure: %w", cleanupErr,
			))
		}
		s.failStart(cause)
		return s.Status(), cause
	}
```

更强的做法是让 `containProcess` 在 `Start()` **之前**失败——但 job 只能包含已存在的进程，所以现实可行解就是上面这段：任何 `Start()` 之后的失败都必须附带一次 `s.cleanup`。

---

### [core] P0-2 · `hasOwnedTunDevice` 把枚举错误吞成"没有残留设备"，导致完整清理路径被跳过

**位置**：`engine/internal/tun/cleanup_windows.go:118-120` 与 `:121-127`

```go
		if enumErr != nil {
			continue
		}
```
```go
		if propertyErr != nil {
			continue
		}
```

**触发条件**
`engine/internal/tun/cleanup_windows.go:114` 的 `devices.EnumDeviceInfo(index)` 返回一个**不是** `ERROR_NO_MORE_ITEMS` 的硬错误（例如 `ERROR_INVALID_HANDLE`、`ERROR_NO_MORE_RESOURCES`），或 `:121-127` 的 `DeviceRegistryProperty` 对某个设备连续两次失败（设备被并发拔除 / 枚举句柄失效）。`continue` 之后索引继续增长，最终撞上 `ERROR_NO_MORE_ITEMS`，走到 `:116` 的 `return false, nil`——**"设备不存在"**。

**后果**
`cleanupPlatform` 的短路判断在 `engine/internal/tun/cleanup_windows.go:68`：

```go
	if present, inspectErr := hasOwnedTunDevice(); inspectErr == nil && !present {
		return nil
	}
```

于是返回 `nil`，**整段 PowerShell 路由/PnP 清理被完全跳过**。崩溃或断电后残留的 `HypoMux-Tun` Wintun 适配器和默认路由没人清理；`Activate` 在 `engine/internal/tun/supervisor.go:212` 的 `s.cleanupWithTimeout(ctx)` 拿到 `nil`，认为环境干净，直接在旧适配器上再次启动 sing-box。这是静默的系统状态残留，用户侧表现是"上次的网卡还在 / 路由指向不存在的隧道"。

**为什么没兜住**
函数注释 `engine/internal/tun/cleanup_windows.go:65-67` 明确写着 *"On inspection errors we fail safe by retaining the full cleanup path."*，而 `:106-111`（`SetupDiGetClassDevsEx` 失败）和 `:132-135`（`DeviceInstanceID` 失败）**确实**是 `return false, err`，符合注释。唯独这两处 `continue` 把硬错误降级成了"跳过这个设备"，最终变成 `false, nil`，**行为与声明的意图相反**。

**建议改法**
硬错误必须向上传播，只有"这条设备没名字/不是我们的"才是 `continue`：

```go
	for index := 0; ; index++ {
		device, enumErr := devices.EnumDeviceInfo(index)
		if errors.Is(enumErr, windows.ERROR_NO_MORE_ITEMS) {
			return false, nil
		}
		if enumErr != nil {
			// 枚举本身失败：不能判定"无残留设备"，交给调用方走完整清理。
			return false, fmt.Errorf("enumerate network devices: %w", enumErr)
		}
		value, propertyErr := devices.DeviceRegistryProperty(device, windows.SPDRP_FRIENDLYNAME)
		if propertyErr != nil {
			value, propertyErr = devices.DeviceRegistryProperty(device, windows.SPDRP_DEVICEDESC)
		}
		if propertyErr != nil {
			// 设备在枚举期间消失属于正常竞态，跳过这一条；
			// 但若它可能是我们的设备，仍需让完整清理路径兜底。
			return false, fmt.Errorf("read device name for index %d: %w", index, propertyErr)
		}
		...
	}
```

如果确实想保留"设备消失"的宽松处理，那么 `cleanupPlatform` 的短路条件必须同时要求"没有任何一条枚举错误"，例如让 `hasOwnedTunDevice` 返回 `(present bool, incomplete bool, err error)`，`incomplete == true` 时不短路。

---

### [core] P0-3 · `OpenDNSExemption` 失败路径丢弃 `Close()` 错误，泄漏 WFP 引擎句柄与子层

**位置**：`engine/internal/wfp/dns_exemption_windows.go:196-200`

```go
	owned := &dnsSession{engine: engine}
	success := false
	defer func() {
		if !success {
			_ = owned.Close()
		}
	}()
```

**触发条件**
`FwpmEngineOpen0` 成功（`:183`）之后，**任何**后续步骤失败：`:215` 的 `FwpmSubLayerAdd0`、`:230` 的 `FwpmGetAppIdFromFileName0`、`:242` 的 `FwpmTransactionBegin0`、或 `:251-257` 循环里任一 `addDNSFilter`。此时 deferred `owned.Close()` 调用 `FwpmEngineClose0`，而 `Close()` 在 `engine/internal/wfp/dns_exemption_windows.go:375-381` 会在失败时**故意保留句柄**以便"稍后重试"：

```go
	status, _, _ := wfp.engineClose.Call(uintptr(session.engine))
	if status != 0 {
		// The session 是 still open. Keep the handle and filter IDs so a later
		// Close can retry; clearing them here would strand both for the
		// remaining lifetime of the process.
		return fmt.Errorf("FwpmEngineClose0 failed (0x%08X)", uint32(status))
	}
```

**后果**
这里的"later Close"**永远不存在**：函数返回 `nil, err`，`owned` 是局部变量，调用方拿不到引用。一旦 `FwpmEngineClose0` 返回非零，这个 dynamic session 就**泄漏到进程结束**。注意 `FwpmSubLayerAdd0`（`:215`）是在 WFP 事务**之外**执行的，它创建的 `HypoMux DNS egress exemption` 子层（Weight `0xFFFF`，`:213`）只能靠 session 关闭回收——句柄泄漏 = **内核里长期残留一个高优先级子层**。反复 `engine.start` 失败会不断堆积。

**为什么没兜住**
`Close()` 的"保留状态以便重试"契约和这里的 `_ =` 丢弃是矛盾的：设计上前者期待调用方重试，后者保证没人重试。而且错误被 `_ =` 吞掉之后，用户和日志都看不到任何"WFP 会话未关闭"的线索。

**建议改法**
不要吞。把它并进返回错误，让上层知道系统状态没恢复干净：

```go
	owned := &dnsSession{engine: engine}
	success := false
	defer func() {
		if success {
			return
		}
		if closeErr := owned.Close(); closeErr != nil {
			// Close 失败意味着 WFP session 仍然打开，子层/过滤器会留到进程结束。
			// 必须让调用方看到，并应当立刻重启 Core 让内核回收。
			wfpCloseFailure = closeErr
		}
	}()
```

配合在每个 `return nil, err` 处统一收口，例如把函数拆成内部 `open(...) (*dnsSession, error)`，在 `OpenDNSExemption` 里：

```go
	owned, err := open(applicationPath, adapters)
	if err != nil {
		return nil, err
	}
	return owned, nil
```

内部 `open` 里把 `FwpmEngineClose0` 的失败用 `errors.Join` 并进主错误：
```go
			return nil, errors.Join(
				fmt.Errorf("add DNS filter: %w", err),
				closeOwned(owned), // 返回 nil 或带上下文的错误
			)
```

**补充（跨包）**：`engine/internal/server/server.go:649-657` 和 `engine/internal/server/scheduling.go:49-52` 在"打开新的、关闭旧的"失败时同样写了 `_ = exemption.Close()`（`server.go:650` / `scheduling.go:50`），这是同一个泄漏模式的第二处实例，详见 dns/server 分节。

---

## P1

### [core] P1-1 · 整个 `engine/` 没有任何 `recover()`，特权守护进程一次 panic 就留下系统状态残留

**位置**：`engine/` 全域（`Grep` `recover\(\)` 命中 0 处；唯一的 `panic(` 是 `engine/internal/wfp/dns_exemption_windows.go:420` 的 `mustGUID`，只在包初始化跑，且参数是硬编码字面量）

**触发条件**
任意一处 panic。候选位置包括每连接 goroutine（`engine/internal/proxy/server.go:250`、`:541`、`:555`，`engine/internal/proxy/udp.go:109`/`:116`/`:229`）、扫描器 goroutine（`engine/internal/tun/supervisor.go:272-274`）、WFP 结构体里的 `unsafe.Pointer` 切片（`engine/internal/wfp/dns_exemption_windows.go:302-308` 的 `conditions := [5]filterCondition{...}` 与 `FilterCondition: &conditions[0]`）。

**后果**
Go 的 panic 会终止整个进程，**不是单个 goroutine**。引擎被 SCM 拉起时已经装好了 WFP 规则、虚拟网卡和路由；进程直接消失意味着 `Stop()` / `terminateRun` / `cleanupRun` / `stopProxyForHostExit`（`engine/internal/server/server.go:1012-1041`）**一个都不会执行**。用户拿到的是：网络不通 + 设备管理器里的幽灵网卡 + 默认路由指向不存在的隧道 + 日志里一个孤零零的 panic 栈。服务还会被自动重启，于是变成"崩溃 → 装状态 → 崩溃"的循环。

**为什么没兜住**
代码里完全没有防护网。持锁 panic 的场景尤其糟：`engine/internal/vnic/manager.go:137` 的 `m.mu` 在 `Create` 全程持有（最长 `StartupTimeout`，`supervisor.go:695` 允许到 60 秒），此时任何 panic 都会让 `m.mu` 永久不可获取，`Status()` / `Remove()` 全部死锁。

**建议改法**
至少在每个"外部输入驱动"的 goroutine 边界加 recover，并把 panic 作为引擎级故障上报：

```go
// engine/internal/proxy/server.go —— 每连接 goroutine 入口
go func() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.emitEvent(api.EventLogRecord, api.LogRecordData{
				Component: "proxy",
				Message: fmt.Sprintf(
					"connection handler panic: %v\n%s",
					r, debug.Stack(),
				),
			})
		}
	}()
	// ...原有逻辑
}()
```

更好的做法是装一个全局兜底：在 `engine/cmd/hypomux-engine/main.go` 的 serve 入口用 `defer` + `debug.Stack()` 捕获，让引擎走正常的 `stopProxyForHostExit` 清理路径再退出，而不是裸崩。

---

### [core] P1-2 · `cleanupRun` 被 `ctx` 超时跳过时，`Stop` 返回的清理失败信息彻底丢失

**位置**：`engine/internal/tun/supervisor.go:568-575`

```go
	select {
	case <-run.done:
	case <-ctx.Done():
		return fmt.Errorf("wait for sing-box stop: %w", ctx.Err())
	}
	if err := s.cleanupRun(run, ctx); err != nil {
		return fmt.Errorf("clean TUN state: %w", err)
	}
```

**触发条件**
`Stop(ctx)` 传入的 ctx 在等待 `run.done` 时超时（sidecar 拒绝优雅退出 / 抢占）。`terminateRun` 在 `:571` 直接返回，**第 573 行的 `s.cleanupRun` 根本没执行**。

**后果**
清理确实最终会由 `waitProcess`（`engine/internal/tun/supervisor.go:501`）在后台执行，但那里的错误去向是有条件的：

```go
	cleanupErr := s.cleanupRun(run, context.Background())
	if cleanupErr != nil {
		status.LastError = errors.Join(
			errors.New(status.LastError),
			fmt.Errorf("TUN cleanup: %w", cleanupErr),
		).Error()
		s.mu.Lock()
		if s.run == run && unexpected {
			s.status = status
		}
		s.mu.Unlock()
	}
```

`s.run == run && unexpected` 两个条件都不满足时（用户主动 `Stop` → `intentional == true` → `unexpected == false`；而且 `Stop` 在 `engine/internal/tun/supervisor.go:437` 已经把 `s.run` 置 nil），**清理错误被完全丢弃**。同时 `Stop` 在 `:439` 无条件重写状态：

```go
	s.status = Status{State: StateStopped}
```

把 `LastError`、`PID`、`ExitCode`、`ExitedAt` 全部抹掉。用户拿到的返回值是一个"超时"的错误，之后再也看不到"清理失败"这条信息，而残留的适配器已经上不了报。

**为什么没兜住**
`cleanupRun` 本身是对的（`sync.Once` 幂等，`engine/internal/tun/supervisor.go:583`），问题在于**谁负责报告它的错误**有两条互斥的路径，而主动 Stop 恰好落在两条都不报告的交集里。

**建议改法**
在 `terminateRun` 的超时分支里仍然尝试一次带独立超时的清理，并把两个错误合并返回：

```go
	select {
	case <-run.done:
	case <-ctx.Done():
		waitErr := fmt.Errorf("wait for sing-box stop: %w", ctx.Err())
		// 等待超时不代表可以不清理：用独立的 cleanupTimeout 兜底，
		// 否则残留的适配器/路由/WFP 规则无人回收且无人知晓。
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), cleanupTimeout,
		)
		defer cancel()
		return errors.Join(waitErr, s.cleanupRun(run, cleanupCtx))
	}
```

并在 `Stop` 里保留失败信息，不要无条件覆盖：
```go
	s.mu.Lock()
	if s.run == run {
		s.run = nil
	}
	previous := s.status
	s.status = Status{State: StateStopped}
	if err != nil {
		previous.State = StateStopped
		previous.LastError = errors.Join(
			errors.New(previous.LastError), err,
		).Error()
		s.status = previous
	}
	status := s.status
	s.mu.Unlock()
```

---

### [core] P1-3 · PowerShell 清理 / MTU / 共享检测把 `ctx` 超时报告成"脚本失败"，丢失全部因果

**位置**：`engine/internal/tun/cleanup_windows.go:87-95`

```go
	err = command.Run()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output.String())
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("PowerShell cleanup failed: %s", detail)
```

**触发条件**
`cleanupContext` 到期（`cleanupTimeout = 15 * time.Second`，`engine/internal/tun/supervisor.go:26`）。`exec.CommandContext` 会 kill 子进程，`command.Run()` 返回 `signal: killed`。若 stdout/stderr 恰好没有内容，`detail` 就变成字符串 `"signal: killed"`。

**后果**
最终上抛的是 `PowerShell cleanup failed: signal: killed`。**"15 秒超时"这个真正的因果被彻底抹掉**，日志里看不出这是超时、是脚本崩了、还是残留设备删不掉。排障时无法区分"机器太慢/策略扫描慢"和"清理脚本本身有 bug"。同一模式在另外两处重复：

- `engine/internal/tun/supervisor.go:461-469`（`validateConfig`，`configCheckTimeout = 10s`）：`return fmt.Errorf("sing-box configuration check failed: %s", detail)` —— 配置检查超时会被报成 `sing-box configuration check failed: signal: killed`。
- `engine/internal/platform/mtu_windows.go:29-31`：`return fmt.Errorf("修改 MTU 失败：%w: %s", err, output)` —— 这里 `%w` 用对了（`err` 保留了 `signal: killed`），但子进程输出被当纯文本拼上去，无法结构化区分。

注意 `validateConfig` 的 ctx 派生自**调用方**的 ctx（`engine/internal/tun/supervisor.go:446`：`context.WithTimeout(ctx, configCheckTimeout)`），所以调用方取消和 sing-box 卡死会产生一模一样的错误文本。

**为什么没兜住**
`%s` 而不是 `%w` 断开了错误链；`detail == ""` 时用 `err.Error()` 做兜底，把一个"上下文"错误降级成了"输出为空"的错误。`ctx.Err()` 从头到尾没被检查过一次。

**建议改法**
先判 ctx，再判子进程输出，最后才回退到 `err`，并且用 `%w` 保留链：

```go
	err = command.Run()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Join(
			fmt.Errorf("PowerShell cleanup did not finish: %w", ctxErr),
			fmt.Errorf("partial output: %s", strings.TrimSpace(output.String())),
		)
	}
	detail := strings.TrimSpace(output.String())
	if detail == "" {
		return fmt.Errorf("PowerShell cleanup failed: %w", err)
	}
	return fmt.Errorf("PowerShell cleanup failed: %w (output: %s)", err, detail)
```

`validateConfig` 同理改写（`engine/internal/tun/supervisor.go:461-469`）：
```go
	err := command.Run()
	if err == nil {
		return nil
	}
	if ctxErr := checkCtx.Err(); ctxErr != nil {
		return errors.Join(
			fmt.Errorf("sing-box configuration check timed out: %w", ctxErr),
			fmt.Errorf("last output: %s", strings.TrimSpace(output.String())),
		)
	}
	// ...原有 detail 逻辑，但用 %w 包装 err
```

---

### [core] P1-4 · `vnic.Manager.Create` 丢弃回滚错误，并对仍存活的虚拟网卡谎报 `failed`

**位置**：`engine/internal/vnic/manager.go:170-179`

```go
	if _, err := m.controller.Activate(ctx, config); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_, _ = m.controller.Stop(stopCtx)
		cancel()
		m.deployed = false
		m.failed = true
		m.createdAt = time.Time{}
		m.lastError = err.Error()
		return m.statusLocked(), err
	}
```

**触发条件**
keeper sidecar 的 `Activate` 失败，随后回滚用的 `Stop(stopCtx)` 也失败——`stopTimeout = 20 * time.Second`（`engine/internal/vnic/manager.go:42`）到了 keeper 仍不退，或 `Stop` 本身报清理失败。

**后果**
1. `Stop` 的错误被 `_, _ =` 丢弃，返回给调用方的只有最初那个 `Activate` 错误。用户完全看不到"虚拟网卡其实还在"。
2. `m.deployed = false; m.failed = true` 声明适配器已经没了。
3. `statusLocked()`（`engine/internal/vnic/manager.go:295-297`）用 `if m.failed { status.State = StateFailed }` **覆盖**了从 supervisor 读到的真实状态。所以即便 keeper 还活着、`deriveState` 会给出 `StatePresent`，对外也一律报 `StateFailed` + 空 `CreatedAt`。

净效果：**内核里有一个活着的 Wintun 适配器和它的 keeper 进程，但引擎对外坚称它不存在**。UI 会显示"移除成功"，用户手动清理时找不到设备。

**为什么没兜住**
`errors.Join` 在这个文件里已经用得很好了（`engine/internal/vnic/manager.go:229`、`:235`），唯独 `Create` 的回滚分支走了捷径。而且 `statusLocked()` 的 `m.failed` 覆盖没有区分"激活失败"和"回滚失败"。

**建议改法**
合并回滚错误，并且只有回滚真的成功才宣称适配器已消失：

```go
	if _, activateErr := m.controller.Activate(ctx, config); activateErr != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_, stopErr := m.controller.Stop(stopCtx)
		cancel()
		m.createdAt = time.Time{}
		m.failed = true
		m.lastError = activateErr.Error()
		if stopErr != nil {
			// keeper 没停掉：适配器仍在内核里，不能宣称 failed/absent。
			m.lastError = errors.Join(activateErr, stopErr).Error()
			return m.statusLocked(), errors.Join(
				activateErr,
				fmt.Errorf(
					"virtual adapter keeper survived the rollback; "+
						"the Wintun adapter is still installed: %w", stopErr,
				),
			)
		}
		m.deployed = false
		return m.statusLocked(), activateErr
	}
```

并让 `statusLocked()`（`engine/internal/vnic/manager.go:295-297`）不要盲目覆盖：
```go
	if m.failed && (state == tun.StateStopped || state == tun.StateFailed) {
		status.State = StateFailed
	}
```

---

### [core] P1-5 · `vnic.Manager` 在最长 60 秒的阻塞调用期间持有 `m.mu`

**位置**：`engine/internal/vnic/manager.go:136-138`

```go
func (m *Manager) Create(ctx context.Context, meta Meta) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
```

**触发条件**
任何一次 `Create`。`m.controller.Activate(ctx, config)` 在 `engine/internal/vnic/manager.go:170` 是在这把锁下同步调用的，而 `tun.Supervisor.Activate` 会阻塞到 sidecar 稳定或超时——`StartupTimeout` 上限是 60 秒（`engine/internal/tun/supervisor.go:695`）。`stopLocked`（`:216-240`）里的 `waitForKeeperStop` 同样是持锁轮询，最长 20 秒。

**后果**
在这段时间里 `Manager.Status()`（`:188-192`）和 `Manager.Remove()`（`:196-208`）全部阻塞。对应的是 `vnic.status` / `vnic.remove` IPC 请求——UI 状态轮询被卡住，用户点"移除"没有响应，看起来像引擎挂了。同时 `handleUnexpectedExit`（`:270-278`，由 supervisor 的 `waitProcess` 在另一个 goroutine 里回调）也会被 `m.mu` 挡住，导致 keeper 崩溃信息延迟最多 60 秒才落到 `m.lastError`。

**为什么没兜住**
锁保护的是 `m` 的字段，但 `Create` 把"调用外部阻塞代码"整个放进了临界区。锁顺序本身是正确的（始终 `m.mu` → `s.mu`，`engine/internal/tun/supervisor.go:518-520` 在释放 `s.mu` 后才回调），所以不会死锁——但会长时间饿死。

**建议改法**
把锁缩到只保护状态字段，耗时的 supervisor 调用放到锁外（用"预留"标志防止并发重入）：

```go
func (m *Manager) Create(ctx context.Context, meta Meta) (Status, error) {
	if err := meta.Validate(); err != nil {
		return m.Status(), err
	}
	// ...参数校验

	m.mu.Lock()
	if m.creating {
		m.mu.Unlock()
		return m.statusLocked(), errors.New(
			"a virtual adapter activation is already in progress",
		)
	}
	m.creating = true
	m.meta = meta
	m.failed = false
	m.lastError = ""
	previous := m.deployed
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.creating = false
		m.mu.Unlock()
	}()

	// 耗时的 supervisor 调用全部在锁外执行
	if err := m.stopExternal(ctx); err != nil {
		m.mu.Lock()
		m.failed, m.lastError = true, err.Error()
		s := m.statusLocked()
		m.mu.Unlock()
		return s, err
	}
	if _, err := m.controller.Activate(ctx, config); err != nil {
		// ...见 P1-4 的改法
	}
	m.mu.Lock()
	m.deployed, m.failed = true, false
	m.createdAt, m.lastError = time.Now().UTC(), ""
	s := m.statusLocked()
	m.mu.Unlock()
	return s, nil
}
```

---

### [core] P1-6 · `FwpmGetAppIdFromFileName0` 返回 NULL 时未校验，把空指针交给内核

**位置**：`engine/internal/wfp/dns_exemption_windows.go:229-240`（取值）与 `:303`（使用）

```go
	var appID *byteBlob
	if err := wfp.call(
		wfp.getAppID,
		"FwpmGetAppIdFromFileName0",
		uintptr(unsafe.Pointer(pathPointer)),
		uintptr(unsafe.Pointer(&appID)),
	); err != nil {
		return nil, err
	}
	if appID != nil {
		defer wfp.free(uintptr(unsafe.Pointer(&appID)))
	}
```
```go
	conditions := [5]filterCondition{
		makeCondition(conditionALEAppID, fwpByteBlobType, uintptr(unsafe.Pointer(appID))),
```

**触发条件**
`FwpmGetAppIdFromFileName0` 返回 `FWP_E_*` 成功码但输出指针为 NULL。文档允许这种情况（例如文件无法被识别为可执行映像、或 provider 不支持 app-id 匹配）。当前代码只在 `appID != nil` 时释放内存，**NULL 情况被静默接受**，然后 `addDNSFilter` 把 `unsafe.Pointer(nil)`（即 `uintptr(0)`）塞进 `value.Value` 作为 `FWPM_BYTE_BLOB_TYPE` 条件值交给内核。

**后果**
内核侧要么拒绝这次 `FwpmFilterAdd0`（返回一个和真实原因无关的错误码），要么按 `Size=0` 解释出一段空 blob——后者意味着 **app-id 条件恒不匹配，DNS 豁免过滤器实际上不会放行任何流量**。在 strict-route 模式下这就是"所有 DNS 都被自己的规则挡住"，表现为 DNS 完全不可用，但 `OpenDNSExemption` 返回成功、`engine.start` 也返回成功。

**为什么没兜住**
`if appID != nil` 被当成"内存释放的可选保护"而不是"契约校验"。Win32 的 `out` 指针参数在 Go 里不会自动校验非空。

**建议改法**
把 NULL 当作契约违反，明确失败并解释原因：

```go
	if appID == nil {
		return nil, fmt.Errorf(
			"FwpmGetAppIdFromFileName0 returned no application ID for %q; "+
				"cannot build an app-scoped DNS exemption", absolute,
		)
	}
	defer wfp.free(uintptr(unsafe.Pointer(&appID)))
```

---

### [core] P1-7 · WFP 所有 Win32 调用的错误用 `%s` 包装，`errors.Is` 永久失效

**位置**：`engine/internal/wfp/dns_exemption_windows.go:402-409`

```go
func (wfp *api) call(procedure *windows.LazyProc, name string, args ...uintptr) error {
	status, _, _ := procedure.Call(args...)
	if status == 0 {
		return nil
	}
	err := syscall.Errno(status)
	return fmt.Errorf("%s failed (0x%08X): %s", name, uint32(status), err.Error())
}
```

**触发条件**
任何 WFP API 失败。这条函数是**整个包唯一的错误出口**——`FwpmEngineOpen0`、`FwpmSubLayerAdd0`、`FwpmGetAppIdFromFileName0`、`FwpmTransactionBegin0`、`FwpmTransactionCommit0`、`FwpmFilterAdd0` 全部经过它。

**后果**
`syscall.Errno` 被 `err.Error()` 拍平成字符串，错误链彻底断开。上层无法区分：
- `FWP_E_CALLOUT_NOT_FOUND` / `ERROR_ACCESS_DENIED`（权限/服务没起）—— 应该提示"以管理员身份运行"；
- `FWP_E_ALREADY_EXISTS` / `FWP_E_IN_USE`（重复安装）—— 应该幂等处理；
- `FWP_E_*` 里的资源类错误—— 应该重试。

唯一的上层消费者 `engine/internal/server/server.go:640-647` 只是把 `err.Error()` 塞进 JSON 的 `message` 字段，用户看到的是一长串十六进制，看不出该做什么。

**为什么没兜住**
`%w` 在 Go 1.13 之后是标准做法，这里只是没改；`%s` + `err.Error()` 是 `%w` 出现前的惯用写法。

**建议改法**
```go
func (wfp *api) call(procedure *windows.LazyProc, name string, args ...uintptr) error {
	status, _, _ := procedure.Call(args...)
	if status == 0 {
		return nil
	}
	errno := syscall.Errno(status)
	return fmt.Errorf("%s: %w (0x%08X)", name, errno, uint32(status))
}
```

再在 `OpenDNSExemption` 里对最常见的两种做分类，让 `server.go` 能给出可执行提示：
```go
	if err := wfp.call(wfp.engineOpen, "FwpmEngineOpen0", /* ... */); err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf(
				"%w (the Core must run elevated and the Base Filtering "+
					"Engine service must be running)", err)
		}
		return nil, err
	}
```

---

### [core] P1-8 · TUN 就绪探测把 Win32 错误吞成"还没就绪"，20 秒后报一个不相关的原因

**位置**：`engine/internal/tun/supervisor.go:400-407`

```go
	device, err := net.InterfaceByName(name)
	if err != nil || device.Flags&net.FlagUp == 0 {
		return nil, false
	}
	addresses, err := device.Addrs()
	if err != nil {
		return nil, false
	}
```

**触发条件**
`net.InterfaceByName` 返回非 `ErrNotExist` 的错误（例如 `WSAEFAULT`、`The network subsystem is unavailable`，或适配器正被 PnP 枚举时的瞬时错误），或 `device.Addrs()` 在适配器被移除过程中失败。

**后果**
两者都变成 `false`，被 `engine/internal/tun/readiness_windows.go:6` 的 `tunPlatformReady` 吞成"未就绪"，40ms 一次的轮询（`engine/internal/tun/supervisor.go:278`）会一直重试直到 `normalized.StartupTimeout`（默认 20 秒，`engine/internal/tun/supervisor.go:23`）。然后用户在 `engine/internal/tun/supervisor.go:284-287` 看到：

```go
			err := fmt.Errorf(
				"TUN interface %s did not become ready within %s",
				expectedInterface, normalized.StartupTimeout,
			)
```

——一个纯粹的**超时**信息。真实的错误（适配器不存在 vs 地址不匹配 vs WSA 错误）在这 20 秒里被丢弃了至少 20 次，一次都没被记录。`failStartedRun` 会去取 `run.stderrSnapshot()` 补充 sing-box 的 stderr，但那是 sing-box 的错误，不是 Windows 网络栈的。

**为什么没兜住**
轮询式就绪探测天然需要把"还没好"和"永远不会好"都映射成 `false`——但函数没有把**最后一次失败原因**留给调用方。

**建议改法**
返回一个可累积的结果，至少保留最后一次失败原因：

```go
// tunInterfaceWithExpectedAddress 返回就绪状态与最后一次探测失败的原因。
func tunInterfaceWithExpectedAddress(
	interfaceName string,
	expectedAddress string,
) (*net.Interface, bool, error) {
	name := strings.TrimSpace(interfaceName)
	if name == "" {
		name = tunInterfaceName
	}
	device, err := net.InterfaceByName(name)
	if err != nil {
		return nil, false, fmt.Errorf("look up adapter %q: %w", name, err)
	}
	if device.Flags&net.FlagUp == 0 {
		return nil, false, fmt.Errorf("adapter %q is down", name)
	}
	addresses, err := device.Addrs()
	if err != nil {
		return nil, false, fmt.Errorf("list addresses of %q: %w", name, err)
	}
	for _, address := range addresses {
		value := address.String()
		host, _, splitErr := net.ParseCIDR(value)
		if splitErr == nil && host.String() == expectedAddress {
			return device, true, nil
		}
	}
	return nil, false, fmt.Errorf(
		"adapter %q has no address %s (has %v)", name, expectedAddress, addresses,
	)
}
```

然后在 `Activate` 的 `readyTicker` 分支（`engine/internal/tun/supervisor.go:289-299`）记下最后一次原因，超时时 `errors.Join` 进去。

---

### [core] P1-9 · 暂存配置的删除错误被吞，凭据文件可能留在 ProgramData

**位置**：`engine/internal/tun/config_stage_windows.go:53`

```go
	remove := func() { removeOnce.Do(func() { _ = os.Remove(path) }) }
```

**触发条件**
`os.Remove(path)` 失败。调用时序本身是安全的——`run.removeConfig()` 由 `cleanupRun`（`engine/internal/tun/supervisor.go:585-587`）触发，而 `cleanupRun` 要么在 `terminateRun` 等到 `run.done` 之后（`:569`）才跑，要么由 `waitProcess`（`:501`）在 `command.Wait()`（`:473`）返回之后才跑，所以 sing-box 一定已经退出、句柄已释放。真正会失败的是环境因素：实时杀毒/EDR 占用并加锁该文件、`FILE_ATTRIBUTE_READONLY`、ACL 拒绝写入、ProgramData 目录被组策略改成不可删。这类失败在别的机器上偶发，在这台机器上 100% 复现。

**后果**
`%ProgramData%\HypoMuxCoreRuntime\tun-config-*.json` 里是**完整的 sing-box 配置**，包含出站服务器地址、端口、UUID/密码、密钥。删除失败时它留在一个只有 SYSTEM/Administrators 可读的目录里，且**没有任何日志、任何事件、任何状态字段**表明删除失败过。`sync.Once` 还保证了后续所有 cleanup 尝试都被静默跳过——一次失败即永久放弃。

**为什么没兜住**
`remove` 的签名是 `func()`，**结构上就无法返回错误**。`stageConfig func(Config) (string, func(), error)`（`engine/internal/tun/supervisor.go:117`）的契约把清理函数降级成了 best-effort。

**建议改法**
让清理函数能报错，并在所有调用点 join：

```go
// stageConfig 的清理函数签名改为 func() error
remove := func() error {
	var removeErr error
	removeOnce.Do(func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr = fmt.Errorf(
				"remove staged TUN config %s: %w", path, err)
		}
	})
	return removeErr
}
```

调用点相应修改 —— `engine/internal/tun/supervisor.go:177-181`：
```go
	defer func() {
		if configOwnedByRun || removeStagedConfig == nil {
			return
		}
		if err := removeStagedConfig(); err != nil {
			// 暂存配置可能含凭据，删除失败必须上报而不是静默丢弃。
			s.failStart(err)
			s.emitLog("[TUN] " + err.Error())
		}
	}()
```
以及 `cleanupRun`（`engine/internal/tun/supervisor.go:583-600`）：
```go
	run.cleanupOnce.Do(func() {
		defer close(run.cleanupDone)
		if run.removeConfig != nil {
			run.cleanupErr = errors.Join(run.cleanupErr, run.removeConfig())
		}
		// ...
	})
```

---

### [core] P1-10 · `Stop` 无条件把状态重置为 `StatusStopped`，抹掉全部失败细节

**位置**：`engine/internal/tun/supervisor.go:434-442`

```go
	err := s.terminateRun(run, ctx)
	s.mu.Lock()
	if s.run == run {
		s.run = nil
	}
	s.status = Status{State: StateStopped}
	status := s.status
	s.mu.Unlock()
	return status, err
```

**触发条件**
任何一次 `Stop`，包括 `terminateRun` 返回非 nil 的情况。

**后果**
`waitProcess`（`engine/internal/tun/supervisor.go:481-499`）刚刚精心组装好的 `Status{ExitedAt, ExitCode, ConfigPath, LastError}` 在这里被整体替换成一个只含 `State` 的空结构。返回值里的 `err` 还在（`err != nil` 时调用方能看到文本），但：
- `PID`、`ExitCode`、`ExitedAt`、`ConfigPath` 全部丢失，诊断"sidecar 到底怎么死的"没有数据；
- `err == nil` 时（正常停止）`LastError` 也被清空，此时若有**清理失败**（P1-2 场景）信息已经彻底蒸发。

**为什么没兜住**
`terminateRun` 的错误通过返回值传递，状态字段被当作"停止后的干净状态"无条件重写，两条信息通道互不联通。

**建议改法**
保留终止前记录的信息，只在成功时清空：
```go
	err := s.terminateRun(run, ctx)
	s.mu.Lock()
	if s.run == run {
		s.run = nil
	}
	previous := s.status
	if err == nil {
		s.status = Status{State: StateStopped}
	} else {
		// 停止失败：保留 PID/ExitCode/ExitedAt/LastError 以便排障。
		s.status.State = StateStopped
		s.status.LastError = errors.Join(
			errors.New(s.status.LastError), err,
		).Error()
	}
	status := s.status
	s.mu.Unlock()
	_ = previous
	return status, err
```

---

## P2

### [core] P2-1 · `wfp.call` 之外的 `UTF16PtrFromString` 错误全部丢弃

**位置**：`engine/internal/wfp/dns_exemption_windows.go:173-175`、`:206-209`、`:317-322`、`:323-325`

```go
	sessionName, _ := windows.UTF16PtrFromString("HypoMux temporary DNS egress exemption")
```

**说明**：这些都是硬编码 ASCII 字面量，`UTF16PtrFromString` 只在字符串含 NUL 时失败，实际不可能触发。`:317`、`:323` 的字符串含 `fmt.Sprintf` 的格式化结果，理论上如果 adapter 名含 NUL 会失败，但 adapter 名来自 JSON。

**改法**：可以统一改成包级 `var` + `init()` 里 panic（字面量，安全），或加注释说明为何可忽略。低优先级。

---

### [core] P2-2 · `InspectWFP` 探测路径丢弃句柄关闭错误

**位置**：`engine/internal/platform/wfp_windows.go:113-115`

```go
	if handle != 0 {
		_, _, _ = closeEngine.Call(uintptr(handle))
	}
```

**说明**：`openWFPEngine` 是纯诊断探测。若 `FwpmEngineClose0` 返回非零，这个 WFP 引擎句柄在进程内泄漏。`InspectWFP` 会被反复调用（UI 轮询），累积泄漏。

**改法**：
```go
	if handle != 0 {
		if status, _, _ := closeEngine.Call(uintptr(handle)); status != 0 {
			return fmt.Sprintf(
				"Base Filtering Engine is running but FwpmEngineClose0 failed (0x%08X)",
				uint32(status),
			), fmt.Errorf("FwpmEngineClose0: %w", syscall.Errno(status))
		}
	}
```

同样 `engine/internal/platform/wfp_windows.go:28` 的 `defer service.Close()` 丢弃 `(*mgr.Service).Close()` 的返回值。

---

### [core] P2-3 · `containProcess` 的三条错误路径丢弃 `CloseHandle`

**位置**：`engine/internal/tun/process_windows.go:117`、`:126`、`:131`

```go
		_ = windows.CloseHandle(job)
```

**说明**：三处都是在返回新错误前的清理，`CloseHandle` 失败意味着内核句柄泄漏。在"马上要返回错误"的路径上，句柄泄漏和原始错误的诊断价值相比可以接受，但三处重复的 `_ =` 说明缺一个统一 helper。

**改法**：抽一个 `closeJob(job windows.Handle) error`，在返回时 `errors.Join`：
```go
func closeJob(job windows.Handle) error {
	if job == 0 {
		return nil
	}
	if err := windows.CloseHandle(job); err != nil {
		return fmt.Errorf("close kill-on-close job: %w", err)
	}
	return nil
}
// ...
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("configure kill-on-close job: %w", err),
			closeJob(job),
		)
	}
```

---

### [core] P2-4 · `GetConsoleProcessList` 成功时把 `nil` 错误用 `%v` 印出来

**位置**：`engine/internal/tun/process_windows.go:71-73`

```go
	count, _, err := kernel.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&members[0])), uintptr(len(members)))
	if count != 2 || !((members[0] == pid && members[1] == uint32(os.Getpid())) || (members[1] == pid && members[0] == uint32(os.Getpid()))) {
		return fmt.Errorf("TUN console is not isolated (members=%d): %v", count, err)
	}
```

**说明**：函数成功时 `err` 是 nil，错误信息会渲染成 `TUN console is not isolated (members=3): <nil>`。`%v` 断开了错误链。

**改法**：
```go
	if count != 2 || /* ... */ {
		return fmt.Errorf(
			"TUN console is not isolated: members=%d (want exactly [%d %d]): %w",
			count, pid, os.Getpid(), err,
		)
	}
```
（`%w` 传 nil 时 `fmt` 会打出 `%!w(<nil>)`，所以这里应改用 `errors.New` + 条件包装，或者干脆在 `err == nil` 时不追加。）

---

### [core] P2-5 · `fmt.Errorf` 无格式动词，应为 `errors.New`

**位置**：`engine/internal/platform/mtu.go:13`、`:19`、`:24`

```go
		return fmt.Errorf("invalid IPv4 MTU change")
```

**说明**：`go vet` 不报错，但 `staticcheck` 的 S1039 会标记，且多一次无用的格式解析。

**改法**：改为 `errors.New("invalid IPv4 MTU change")` 并在 `mtu.go` 引入 `errors` 包。

---

### [core] P2-6 · `buildRules` 静默丢弃无法解析的适配器，依赖跨包的隐式校验

**位置**：`engine/internal/wfp/dns_exemption_windows.go:271-274`

```go
	for _, adapter := range adapters {
		ip := net.ParseIP(strings.TrimSpace(adapter.SourceIP)).To4()
		if ip == nil || adapter.IfIndex == 0 {
			continue
		}
```

**说明**：部分适配器被跳过时，函数返回成功且不附带任何说明——这些适配器在 strict-route 下 DNS 会被自己的 WFP 规则挡住。

**已核实的调用方**（所以当前不是可触发的 P0）：两条入口都先做了校验——
- `engine/internal/server/server.go:402-412` 从 `params.Adapters` 构造 `s.adapters`，但该路径的值来自 `proxy.New(params.ProxyConfig())` 成功的分支，而 `engine/internal/proxy/config.go:114-118` 已用 `net.ParseIP(adapter.SourceIP)` 校验并归一化；
- `engine/internal/server/scheduling.go:27-43` 的增量来自 `proxy.ValidateScheduling(params)`（`engine/internal/proxy/scheduling.go:14`），同样经过校验。

也就是说 `wfp` 包的正确性依赖一个**不同包**的校验，这个隐式契约既没有文档也没有断言。

**改法**：在包边界自我保护，并让部分跳过可见：
```go
func buildRules(adapters []Adapter) []dnsRule {
	// ... 现有逻辑
}
```
改为返回被跳过的条目，由 `OpenDNSExemption` 决定是报错还是放行：
```go
	result, skipped := buildRules(adapters)
	if len(skipped) > 0 {
		return nil, fmt.Errorf(
			"cannot build a DNS exemption for %d adapter(s): %s",
			len(skipped), strings.Join(skipped, ", "),
		)
	}
```
或在 `DNSExemption` 接口上加 `func Skipped() []string`，让上层至少能上报。

---

### [core] P2-7 · `OpenDNSExemption` 丢弃 `os.Stat` 错误，路径错误与"是目录"不可区分

**位置**：`engine/internal/wfp/dns_exemption_windows.go:164-167`

```go
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("invalid WFP application identity path %q", absolute)
	}
```

**说明**：文件不存在（`ERROR_FILE_NOT_FOUND`，路径拼错）和路径指向目录，两条完全不同的故障给出同一个错误。`:160-163` 的 `return nil, err` 同样没有上下文（`filepath.Abs` 失败意味着路径含 NUL，值得说明）。

**改法**：
```go
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf(
			"stat WFP application identity path %q: %w", absolute, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf(
			"WFP application identity path %q is a directory, not an executable",
			absolute,
		)
	}
```

---

## 本节发现统计

| 严重程度 | 条数 |
|---|---|
| P0 | 3 |
| P1 | 10 |
| P2 | 7 |
| **合计** | **20** |

---

## 覆盖清单（本节已读文件）

- `engine/internal/tun/`：`supervisor.go`(751 行)、`cleanup_windows.go`、`cleanup_other.go`、`config_stage_windows.go`、`config_stage_other.go`、`process_windows.go`、`process_other.go`、`readiness_windows.go`、`readiness_other.go`、`recover.go`、`interface_name.go`
- `engine/internal/wfp/`：`dns_exemption.go`、`dns_exemption_windows.go`、`dns_exemption_other.go`
- `engine/internal/vnic/`：`manager.go`
- `engine/internal/platform/`：`wfp.go`、`wfp_windows.go`、`wfp_other.go`、`sharing.go`、`sharing_windows.go`、`sharing_other.go`、`identity.go`、`identity_windows.go`、`identity_other.go`、`mtu.go`、`mtu_windows.go`、`mtu_other.go`
- **交叉验证的调用方**（不属于本节写域，仅用于判定影响面）：`engine/internal/server/server.go:330-430`、`:625-704`、`:975-1054`，`engine/internal/server/scheduling.go:18-61`，`engine/internal/proxy/config.go:109-118`

---

## 分片 B · `[cmd]` — engine/cmd/hypomux-engine

## 概览

`engine/cmd/hypomux-engine/` 是引擎进程的全部入口，共 8 个非测试文件、约 1100 行：`main.go`（CLI 分发 + `runServer`）、`pipe_windows.go` / `pipe_file_windows.go` / `pipe_other.go`（engine 作为**客户端**连接 desktop 宿主管道并做 token + 宿主 PID 认证）、`service_windows.go` / `service_policy_windows.go` / `service_session_windows.go` / `service_other.go`（Windows Service 安装/卸载、HKLM 策略键、命名管道服务端、WTS 会话校验）。

整体风格是**"检查每一个错误、句柄在每条失败路径上都关闭"**——`pipe_file_windows.go:24-35` 的 `newPipeFile`、`pipe_windows.go:80-136` 的握手清理都是逐路径显式 `_ = connection.Close()` 写出来的，说明作者有清理意识。问题不在于漏掉单个 `err != nil`，而在于**系统级事务没有边界**：安装路径做了「停服务 → 改配置 → 写注册表 → 设恢复动作 → 启动」五个不可原子化的系统改动，任何一步失败都没有对应的 undo，已经创建的服务、已经停掉的服务、已经写坏的策略键都会留在机器上。

第二个结构性问题在**退出路径**：唯一的网络状态回滚 `Server.stopProxyForHostExit`（`engine/internal/server/server.go:1012-1041`，由 `Run` 的 defer 触发，`server.go:97`）依赖进程内正常返回，而 Service 的 `Execute` 在 15s 超时后会主动让进程退出（`service_windows.go:265-270`），goroutine panic 同样直接杀进程——两条路都会跳过 DNS 豁免关闭、TUN 停止和虚拟网卡删除。

第三，错误**信息**层面的损失集中在三处：`fmt.Errorf("%w: %v", ...)`（`service_windows.go:340`）把真实错误降级成字符串、注册表 ACL 读取失败与「没有可信 DACL」共用一条消息（`service_policy_windows.go:435-437`）、WTS 调用失败时用可能为 0 的 errno 包装（`service_session_windows.go:46`）。

共 31 条发现：**P0 = 6，P1 = 11，P2 = 14**。

---

## P0 — 系统状态不一致 / 静默失效

### [cmd] P0-1 `writeCoreServicePolicy` 写一半失败会摧毁原本可用的策略，且无回滚

**位置**：`engine/cmd/hypomux-engine/service_policy_windows.go:100`
```go
	if err := key.SetDWordValue(policyValueSchemaVersion, 0); err != nil {
```
以及 `:112-119`：
```go
	for _, value := range values {
		if err := key.SetStringValue(value.name, value.value); err != nil {
			return fmt.Errorf("write Core Service policy value %s: %w", value.name, err)
		}
	}
	if err := key.SetDWordValue(policyValueSchemaVersion, coreServicePolicyVersion); err != nil {
```

**触发条件**：升级安装时，`SetStringValue` 在 4 个值中任何一个上失败（磁盘满、AV 拦截注册表写入、`key.Close()` 语义差异、进程被杀），或最后的 commit（`:117`）失败。`SchemaVersion=0` 已经在 `:100` 写入。

**后果**：注册表里原本完好的策略被降级成 `SchemaVersion=0` + 部分字段。`loadCoreServicePolicy`（`:137-139`）会返回 `unsupported Core Service policy version 0`，服务进程在 `serveCoreServicePipe` 的第一次循环就退出（`service_windows.go:276-279`）。叠加 P0-2：旧服务此时已经被 `installWindowsService` 停掉，于是**机器上留下一个开机自启、但永远起不来的服务 + 一份损坏的策略键**，用户的代理功能彻底不可用，且只有重跑一次完整成功的 `install-service` 才能恢复。

**为什么没兜住**：`:98-102` 的注释说明了「先置无效避免半份策略被读取」的正确意图，但只处理了**读侧**的拒绝，没有处理**写侧**的原子性——没有在置无效之前快照旧值，也没有在失败时还原。`writeCoreServicePolicy` 返回 error 后，调用方 `service_windows.go:98-100` 只是 `return fmt.Errorf(...)`，没有任何 undo。

**建议改法**：写之前快照旧值，失败时还原，并用 `errors.Join` 同时报告两个错误：
```go
	previous, readErr := readCoreServicePolicyValues(key) // DesktopPath/Digest/Tun*/SchemaVersion + 存在标志
	if err := key.SetDWordValue(policyValueSchemaVersion, 0); err != nil { ... }
	for _, value := range values {
		if err := key.SetStringValue(value.name, value.value); err != nil {
			if restoreErr := restoreCoreServicePolicy(key, previous); restoreErr != nil {
				return fmt.Errorf("write Core Service policy value %s: %w (restore previous policy: %v)",
					value.name, err, restoreErr)
			}
			return fmt.Errorf("write Core Service policy value %s: %w", value.name, err)
		}
	}
```
commit（`:117`）失败时同样走 `restoreCoreServicePolicy`。这样「先置无效」变成真正的写事务边界。

---

### [cmd] P0-2 `installWindowsService` 改了系统状态后没有 undo：新建服务失败不删除，更新路径失败后服务永远不再启动

**位置**：`engine/cmd/hypomux-engine/service_windows.go:63`
```go
		service, err = manager.CreateService(coreServiceName, executable, mgr.Config{
```
以及 `:98-110`：
```go
	if err := writeCoreServicePolicy(policy); err != nil {
		return fmt.Errorf("persist Core Service security policy: %w", err)
	}
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{...}, 24*60*60); err != nil {
		return fmt.Errorf("set service recovery actions: %w", err)
	}
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("start service: %w", err)
	}
```

**触发条件**（两条独立路径）：
- 新装：`CreateService` 成功后，`:98` / `:101` / `:108` 任一失败（例如 `SetRecoveryActions` 被组策略限制、`service.Start()` 返回 `ERROR_DEMAND_START_PAUSED`）。
- 升级：`:77` 已经把旧服务停下，随后 `:91` `UpdateConfig` 成功但 `:101` 或 `:108` 失败。

**后果**：
- 新装路径留下一个 `StartType: Automatic` 的已注册服务。它每次开机都会被 SCM 拉起，进程读不到策略（`:276`）立刻退出 → 事件日志反复报错；同时安装器已经返回退出码 1，桌面侧不会再清理。用户重试安装时走 `:61` 的 else 分支，又变成升级路径。
- 升级路径更糟：**旧服务被停在 Stopped 且再也不被启动**，`install-service` 报失败。此时机器上装的是新二进制、新注册表路径，但**没有任何进程在提供服务**，用户网络完全中断，直到重启或手工 `sc start`。

**为什么没兜住**：`installWindowsService` 是一个 70 行的直线流程，只有 `defer manager.Disconnect()`（`:59`）和 `defer service.Close()`（`:96`）两个纯资源清理，没有一个 `defer` 描述「如果走到这里还没成功，就把系统改回去」。`stopWindowsService` 的结果 `stopErr` 在 `:77-80` 只用于提前返回，同样没有 restart 兜底。

**建议改法**：用「成功标志 + 回滚 defer」把整段包起来：
```go
	created := false
	restartPrevious := false
	committed := false
	defer func() {
		if committed { return }
		if created { _ = service.Delete() }
		if restartPrevious {
			if err := service.Start(); err != nil {
				log.Printf("rollback: restart previous service failed: %v", err)
			}
		}
	}()
```
`created` 在 `:63` 成功后置 true；`restartPrevious` 在走 else 分支且 `:77` 停服成功后置 true；所有 return 点改成 `err = errors.Join(err, rollback...)` 后再 `return`，最后成功路径置 `committed = true`。另外 `main.go:71-74` 只打印 `%v`，建议安装失败时把回滚结果一并输出，避免「报失败但机器上留了服务」无法自证。

---

### [cmd] P0-3 Service 停机超时直接退出进程，跳过唯一的 DNS/TUN/VNIC 回滚，并回报成功

**位置**：`engine/cmd/hypomux-engine/service_windows.go:265`
```go
	case <-timer.C:
		// Execute returning lets the service process exit even if a Windows pipe
		// operation or a cleanup hook failed to observe cancellation. Without this
		// guard SCM can remain in STOP_PENDING forever and block upgrades/removal.
		fmt.Fprintf(service.stderr, "Core Service shutdown exceeded %s; exiting service host\n", timeout)
		return false, 0
```

**触发条件**：`svc.Stop`/`svc.Shutdown` 到达后 15s（`coreServiceShutdownTimeout`，`:30`）内 `serve` 没有返回。管道操作卡死、WFP 关闭阻塞、sing-box 子进程拒绝退出都会触发。

**后果**：`Execute` 返回 → `svc.Run` 返回 → `runWindowsService` 返回 0 → `main` 进程退出。此时**唯一的网络状态回滚没有执行**：`Server.stopProxyForHostExit`（`engine/internal/server/server.go:1012-1041`，由 `Run` 的 `defer s.stopProxyForHostExit()` 触发，`server.go:97`）负责 `s.tun.Stop`、`s.closeDNSExemption`、`s.vnic.Remove`、`s.proxy.Stop`。它全部没跑 → **DNS 豁免规则残留、WFP 引擎句柄残留、HypoMux-Tun 虚拟网卡留在系统里、sing-box keeper 进程成为孤儿**。而且 `return false, 0` 告诉 SCM「干净停止」，事件日志里不会出现失败记录，只有一行 stderr 文本，而 Service 模式下 stderr 通常不落盘。

**为什么没兜住**：注释里的权衡是「不要卡在 STOP_PENDING」，代价是牺牲了清理。代码里没有任何补偿路径——既没有在超时分支里主动调用一次恢复（`tun.Recover`，`main.go:112` 已经在用），也没有把「清理未完成」这一事实传递给上层。同一个文件里 `serveCoreServicePipe` 在 `ctx.Done()` 时做了 `interruptServicePipeConnection`（`:304`）的补救，说明作者知道补救手段存在，只是 `stop` 的超时分支没有。

**建议改法**：超时分支改为「尽力恢复后再退出」，并用非零退出码让 SCM 记录异常：
```go
	case <-timer.C:
		fmt.Fprintf(service.stderr, "Core Service shutdown exceeded %s; running recovery before exiting\n", timeout)
		recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := tun.Recover(recoverCtx); err != nil {
			fmt.Fprintf(service.stderr, "post-timeout network recovery failed: %v\n", err)
		}
		recoverCancel()
		return false, 1
```
（`tun` 包在 `service_windows.go:17` 已导入。）至少也要把 `return false, 0` 改成非 0，避免"静默失效"。

---

### [cmd] P0-4 两个后台 goroutine 都没有 `recover()`，panic 会杀掉 LocalSystem 服务进程并跳过全部清理

**位置**：`engine/cmd/hypomux-engine/service_windows.go:214`
```go
	go func() {
		done <- serve(ctx, service.metadata)
	}()
```
以及 `:293-295`：
```go
		go func() {
			sessionDone <- engineServer.Run(ctx)
		}()
```

**触发条件**：这两个 goroutine 里跑的是整个引擎栈——注册表读取、WTS 查询、命名管道、以及 `server.Server.Run`（内含 TUN 监督、WFP、VNIC、JSON 编解码）。任何一处 `runtime` panic（nil 解引用、越界、类型断言失败、`unsafe` 解引用）都直接终止进程。`service_session_windows.go:55` 的 `*(*uint32)(unsafe.Pointer(buffer))` 和 `service_policy_windows.go:460` 的 `(*windows.SID)(unsafe.Pointer(&ace.SidStart))` 属于这类不可信输入驱动的解引用。

**后果**：进程立刻死，`stopProxyForHostExit` 不会运行（后果同 P0-3：DNS/TUN/VNIC 残留）。依赖 `SetRecoveryActions`（`:101-107`）在 3 秒后重启，于是变成「崩溃 → 带着脏网络状态重启」的循环，每次重启都可能在上一次的残留上再叠一层。

**为什么没兜住**：`done` / `sessionDone` 都是 `make(chan error, 1)`，发送端不会因无人接收而泄漏——但这只解决了 goroutine 泄漏，完全没解决 panic。Go 里未捕获的 panic 直接杀进程，`svc` 框架也没有 recover。

**建议改法**：两个 goroutine 都套一层把 panic 转成 error，让现有的错误通道去报告（`stop`/`Execute` 里已经有 `errors.Is(err, context.Canceled)` 的分支，加一个 `PanicError` 类型即可）：
```go
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("core service pipe panic: %v\n%s", r, debug.Stack())
			}
		}()
		done <- serve(ctx, service.metadata)
	}()
```
`service_session_windows.go:293` 同理，并追加 `runtime/debug` 导入。

---

### [cmd] P0-5 `requireMachineInstallLocation` 把 `libcronet.dll` 的 stat 错误当成「文件不存在」，直接跳过 ACL 校验

**位置**：`engine/cmd/hypomux-engine/service_policy_windows.go:333`
```go
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(engine), "libcronet.dll")); statErr == nil {
		paths = append(paths, filepath.Join(filepath.Dir(engine), "libcronet.dll"))
	}
```

**触发条件**：AV/EDR 拦截、`ERROR_SHARING_VIOLATION`（文件正被占用）、权限不足、路径过长——`os.Stat` 任何非 `ErrNotExist` 的失败都会走到这里，`statErr` 被整个丢弃（既不检查类型也不记日志）。

**后果**：`libcronet.dll` 不进入 `paths`，因此**完全跳过 `requireProtectedCorePath`**（`:395-408` 的 reparse 检查 + `requireProtectedCoreACL` 的 owner/DACL 校验）。该 DLL 与 `sing-box.exe`（`:331` 有校验）位于**同一目录、同一进程（LocalSystem 服务）加载**，前者 ACL 不可信、后者不可信是同一个提权面。结果是：本地攻击者只要能让该 DLL 对普通用户可写，就获得了以 SYSTEM 加载任意代码的能力——而安装器在一条 `stat` 错误之后就宣称校验通过。这正是「静默吞错导致静默失效」的典型。

**为什么没兜住**：代码只区分了「存在」和「不存在」两种世界，没有区分「确认不存在」和「查不出来」。`statErr` 变量声明了却从不使用，编译器和 linter 都不会提醒。

**建议改法**：
```go
	libcronet := filepath.Join(filepath.Dir(engine), "libcronet.dll")
	switch _, statErr := os.Stat(libcronet); {
	case statErr == nil:
		paths = append(paths, libcronet)
	case errors.Is(statErr, fs.ErrNotExist):
		// 确认不存在，跳过是安全的
	default:
		return fmt.Errorf("inspect protected Core sidecar %s: %w", libcronet, statErr)
	}
```

---

### [cmd] P0-6 `removeWindowsService` 的系统状态回滚不对称：卸载路径不留痕，删服务与删注册表键之间没有补偿

**位置**：`engine/cmd/hypomux-engine/service_windows.go:130-136`
```go
	if err := stopWindowsService(service, 20*time.Second); err != nil {
		return err
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete service: %w", err)
	}
	return deleteCoreServicePolicy()
```

**触发条件**：(a) `stopWindowsService` 20s 超时（`:164` `stop service: timed out after 20s`）；(b) `service.Delete()` 失败；(c) `deleteCoreServicePolicy()` 失败（`registry.DeleteKey` 因键下还有子键或被占用而失败）。

**后果**：
- (a)：用户要求卸载，服务仍在运行并以 SYSTEM 驻留，`remove-service` 返回失败但机器上什么都没变；没有任何「至少把策略键清掉」的动作。
- (b)：服务已停止但仍注册，`deleteCoreServicePolicy()` 被跳过——注册表里留着一个指向已删除二进制的完整策略。
- (c)：服务已删除，注册表键残留 → 机器上留着 `HKLM\SOFTWARE\HypoMux\CoreServicePolicy`（含 desktop/tun 的绝对路径和 SHA-256）。下次安装会被 `:88` 的 `CreateKey` 覆盖，但用户在「卸载后」审计机器时看到的是孤儿 SYSTEM 级注册表项。
- 另外：安装路径 `:51` 调用的 `tun.PrepareTrustedConfigStorage()`（`engine/internal/tun/config_stage_windows.go:85-94`）会创建 `%ProgramData%\HypoMuxCoreRuntime`（受保护 ACL 目录），而 `removeWindowsService` **完全没有对应的删除**，卸载后该目录永久残留（NSIS 卸载脚本 `desktop/build/windows/nsis/project.nsi:480-481` 删的是 `$APPDATA\HypoMuxCoreRuntime`，路径不同，删不掉它）。

**为什么没兜住**：删除顺序固定为「停 → 删服务 → 删策略」，中间任何一步失败就直接 return，后一步的补偿不做；`defer` 里只有 `manager.Disconnect()`（`:119`）和 `service.Close()`（`:128`）这类资源清理，没有业务状态清理。`:131` 的 `return err` 还把 `stopWindowsService` 的原始错误**不带上下文**直接上抛（见 P1-10）。

**建议改法**：把三步包成 `errors.Join`，让每一步都尽力执行、并明确报告哪一步残留：
```go
	var errs []error
	if err := stopWindowsService(service, 20*time.Second); err != nil {
		errs = append(errs, fmt.Errorf("stop service before removal: %w", err))
	} else if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_MARKED_FOR_DELETE) {
		errs = append(errs, fmt.Errorf("delete service: %w", err))
	}
	if err := deleteCoreServicePolicy(); err != nil {
		errs = append(errs, fmt.Errorf("delete Core Service policy: %w", err))
	}
	if err := removeTrustedConfigStorage(); err != nil { // 新增：删除 %ProgramData%\HypoMuxCoreRuntime
		errs = append(errs, fmt.Errorf("remove Core runtime staging directory: %w", err))
	}
	return errors.Join(errs...)
```
`errors.Join` 同时保住 stopWindowsService 原始错误的链（`errors.Is` 仍可穿透）。

---

## P1 — 错误链断裂 / 上下文丢失

### [cmd] P1-1 `fmt.Errorf("%w: %v", ...)` 把真实拒绝原因降级成不可追溯的字符串

**位置**：`engine/cmd/hypomux-engine/service_windows.go:340`
```go
		return nil, fmt.Errorf("%w: %v", errServiceClientRejected, err)
```

**触发条件**：`validateServicePipeClient` 失败——包括 `reject Core Service client executable for PID`（路径不符）、`reject modified Core Service client executable`（SHA-256 不符，`service_policy_windows.go:229`）、`reject Core Service client outside an active interactive session`（`:450`）。这些都进入这里。

**后果**：只有 `%w` 的 `errServiceClientRejected` 进入了错误链，`err` 里携带的真正原因（尤其是**桌面二进制被篡改**这种安全事件，`service_policy_windows.go:229`）变成了纯文本。任何 `errors.Is(err, ...)` 上层判断都对它无能为力，事件日志/结构化日志系统无法按原因分类，事后取证拿不到机器可读的原因码。

**为什么没兜住**：为了同时保留 sentinel（供 `:282` 的 `errors.Is` 分支）和原因，作者用了 `%w + %v`，但 Go 1.20 起 `fmt.Errorf` 支持**多个 `%w`**，这里不需要牺牲原因。

**建议改法**：
```go
		return nil, fmt.Errorf("%w: %w", errServiceClientRejected, err)
```
若需要给原因分类，让 `validateServicePipeClient` 返回带类型的错误（如 `type clientRejectError struct{ Reason string; Err error }`），此处改用 `errors.Join(errServiceClientRejected, err)` 同样可行。

---

### [cmd] P1-2 注册表 ACL 读取失败被伪装成「没有可信 DACL」

**位置**：`engine/cmd/hypomux-engine/service_policy_windows.go:435`
```go
	if err != nil || dacl == nil {
		return fmt.Errorf("protected Core path has no trusted DACL: %s", path)
	}
```

**触发条件**：`descriptor.DACL()` 因 `ERROR_ACCESS_DENIED`、句柄权限不足、或安全描述符损坏而返回 error（非 nil dacl 分支几乎不会走到）。

**后果**：用户看到「`C:\Program Files\HypoMux\bin\hypomux-engine.exe` 没有可信 DACL」，会据此认为**文件权限真的坏了**，从而手动放开/重建 ACL——而真实原因是**读取失败**。这是会导致用户做出危险操作的误导性错误信息：`service_policy_windows.go:407` 的调用链一路把它当作「路径不安全」上抛到 `installWindowsService:44`。

**为什么没兜住**：`err != nil || dacl == nil` 的短路写法把两种语义完全不同的状态合并成一条消息，且没有 `%w`，原始 `err` 被直接丢弃。

**建议改法**：
```go
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read protected Core DACL for %s: %w", path, err)
	}
	if dacl == nil {
		return fmt.Errorf("protected Core path has no trusted DACL: %s", path)
	}
```

---

### [cmd] P1-3 认证握手 JSON 解析失败时原始错误被丢弃，无法区分「格式错」和「对端不是 HypoMux」

**位置**：`engine/cmd/hypomux-engine/pipe_windows.go:126-129`
```go
	if err := json.Unmarshal(line, &ready); err != nil {
		_ = connection.Close()
		return nil, errors.New("invalid host authentication response")
	}
```

**触发条件**：desktop 发来的 ready 行不是合法 JSON——包括 desktop 版本不兼容（协议升级）、中间有代理/杀软注入了内容、或被恶意进程抢占了管道（`connectAuthenticatedPipe` 只校验了 `serverPID`（`:89`），没校验管道名归属）。

**后果**：日志里只有 `invalid host authentication response`，没有 `invalid character 'x' ...` 这类信息。排查跨版本兼容问题时**完全无从下手**，且无法区分「desktop 版本不匹配」（可修）与「有第三方在抢管道」（安全事件）。

**为什么没兜住**：为了不把对端的原始字节回显进错误信息（合理的脱敏考虑），直接用了 `errors.New`，把 `err` 丢掉了。同一函数里 `:87` 的 `%w` 用法说明作者知道怎么包。

**建议改法**：保留 `err`、不回显内容：
```go
	if err := json.Unmarshal(line, &ready); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("invalid host authentication response (protocol %s): %w",
			pipeSessionAuthKind, err)
	}
```
同理 `:115` 的 `errors.New("host authentication response is too large")` 建议补上实际长度与上限（`maxPipeSessionMessage`），并 `%w` 包装 `bufio.ErrBufferFull`。

---

### [cmd] P1-4 `deleteCoreServicePolicy` 用 `!=` 比较错误，破坏错误链

**位置**：`engine/cmd/hypomux-engine/service_policy_windows.go:177`
```go
	if err != nil && err != registry.ErrNotExist {
```

**触发条件**：`registry.DeleteKey` 返回任何被包装过的 not-exist（`golang.org/x/sys/windows/registry` 内部若改为 `%w` 包装、或将来 SDK 变更），或调用链上游给它加过上下文。

**后果**：`DeleteKey` 在键不存在时返回错误，被当成真失败 → `removeWindowsService`（`service_windows.go:123` 和 `:136`）**对一台从未安装过的机器执行 remove-service 会报 "delete Core Service policy registry key"**。这正是该分支 `:122-124` 想避免的情况。

**为什么没兜住**：手写 sentinel 比较而非 `errors.Is`；同文件其他所有地方都用 `errors.Is`（`service_windows.go:62`、`:108`、`:133`），这里是不一致。

**建议改法**：
```go
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("delete Core Service policy registry key: %w", err)
	}
```

---

### [cmd] P1-5 WTS 调用失败时用可能为 errno(0) 的 `callErr` 包装，产生「The operation completed successfully」

**位置**：`engine/cmd/hypomux-engine/service_session_windows.go:45-47`
```go
	if result == 0 {
		return 0, fmt.Errorf("query WTS session %d connection state: %w", sessionID, callErr)
	}
```

**触发条件**：`wtsapi32.dll` 无法加载（受限环境、AppLocker、容器化 Windows）或 `WTSQuerySessionInformationW` 导出不存在（`LazyProc.Call` 内部失败）。此时 `callErr` 为 `syscall.Errno(0)`。

**后果**：错误消息变成 `query WTS session 3 connection state: The operation completed successfully.`——一条自相矛盾、无法排查的消息。而且这个错误经 `service_windows.go:445-448` → `acceptServicePipe:340` → 客户端拒绝分支返回，用户只看到「会话状态检查失败」，完全不知道是 DLL 被拦。

**为什么没兜住**：`LazyProc` 的错误约定（proc 加载失败时 `Call` 返回 `Errno(0)`）没有被处理；`service_session_windows.go:31` 声明的 `procWTSQuerySessionInformationW` 从未调用过 `Find()` 验证。

**建议改法**：进函数先解析符号，并把 errno 0 当作「未知」处理：
```go
func queryServiceSessionState(sessionID uint32) (serviceSessionState, error) {
	if err := procWTSQuerySessionInformationW.Find(); err != nil {
		return 0, fmt.Errorf("resolve WTSQuerySessionInformationW: %w", err)
	}
	...
	if result == 0 {
		if callErr == syscall.Errno(0) {
			return 0, fmt.Errorf("query WTS session %d connection state: WTSQuerySessionInformationW failed without an error code", sessionID)
		}
		return 0, fmt.Errorf("query WTS session %d connection state: %w", sessionID, callErr)
	}
```

---

### [cmd] P1-6 正常 Ctrl+C 退出被判为失败：退出码 1 + "context canceled"

**位置**：`engine/cmd/hypomux-engine/main.go:150-153`
```go
	if err := engineServer.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "hypomux-engine: %v\n", err)
		return 1
	}
```

**触发条件**：用户在 `serve` 或 `serve-pipe` 模式下按 Ctrl+C。`signal.NotifyContext`（`main.go:147`）取消 ctx，`Server.Run` 在 `server.go:134` 返回 `ctx.Err()` = `context.Canceled`，一路原样回到 `runServer`。

**后果**：`serve` 进程以**退出码 1** 结束，并向 stderr 打印 `hypomux-engine: context canceled`。桌面/安装器/任何把它当子进程监管的脚本都会认为「引擎崩溃了」，触发无意义的重启或错误弹窗；stderr 里的 `context canceled` 也会被误读为真实故障。同文件 `service_windows.go:233` 和 `:260` 都正确地写了 `!errors.Is(err, context.Canceled)`，唯独 CLI 路径漏了。

**为什么没兜住**：`runServer` 是唯一处理 `Run` 返回值的地方，作者在 Service 侧建立了「context.Canceled 是正常终止」的约定，但没有在 CLI 侧应用。

**建议改法**：
```go
	if err := engineServer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "hypomux-engine: %v\n", err)
		return 1
	}
	return 0
```
需在 `main.go` 导入表加 `"errors"`。

---

### [cmd] P1-7 `runtimeServerMetadata` 静默降级：路径解析失败时 `TunExecutable` 为空，无任何日志

**位置**：`engine/cmd/hypomux-engine/main.go:167-173`
```go
	executable, err := os.Executable()
	if err != nil {
		return metadata
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return metadata
	}
```

**触发条件**：`os.Executable()` 失败（进程映像被删除、`/proc` 不可用、权限受限），或 `filepath.Abs` 在当前工作目录不可解析时失败。

**后果**：`metadata.TunExecutable`（`:179`）保持空字符串，`server` 拿不到 sing-box 路径。**这是「静默失效」**：进程正常启动、正常握手、正常收发 JSON，看起来一切健康，只有真正要拉起 TUN 时才失败，而错误来自更深层、更难定位。此外两次失败的原因都被完全丢弃。

**为什么没兜住**：该函数签名里没有 `stderr`，作者选了「失败就用默认值」而不是上抛；调用方 `runServer:149` 也不知道发生了降级。

**建议改法**：把降级显式化——让 metadata 带上诊断信息并写进日志：
```go
func runtimeServerMetadata(stderr io.Writer) server.Metadata {
	metadata := baseServerMetadata()
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "resolve engine executable for TUN sidecar path: %v\n", err)
		return metadata
	}
	...
}
```
（`runServer` 调用点 `main.go:149` 改为 `server.New(input, output, runtimeServerMetadata(stderr))`。）若 `metadata.TunExecutable` 为空会导致请求失败，更应在 `server.Metadata` 上加一个 `Degraded []string` 字段随 hello/status 下发，让 desktop 能显示明确原因。

---

### [cmd] P1-8 `buildCoreServicePolicy` 的目录布局检查是恒真式，永不生效

**位置**：`engine/cmd/hypomux-engine/service_policy_windows.go:64-67`
```go
	if !strings.EqualFold(filepath.Base(desktop), "hypomux.exe") ||
		!strings.EqualFold(filepath.Dir(tunExecutable), filepath.Dir(engine)) {
		return coreServicePolicy{}, errors.New("installed desktop or protected sing-box layout is invalid")
	}
```

**触发条件**：永远不触发（不存在）。

**后果**：`tunExecutable` 在 `:57-59` 由 `filepath.Join(filepath.Dir(engine), "sing-box.exe")` 构造，所以 `filepath.Dir(tunExecutable) == filepath.Dir(engine)` **恒成立**，这个条件是死代码。它看起来在保护「desktop 与 sing-box 必须同目录」这条不变量，但实际上一条都没校验——`desktop` 可以位于完全无关的目录树（只要 basename 是 `hypomux.exe` 且在固定卷上，`:54` 的检查通过）。读代码的人会误以为这条不变量存在。

**为什么没兜住**：写检查时用了被检查对象自身的派生源做比较，而不是独立事实（例如 policy 里存的 `engine` 目录）。

**建议改法**：要么改成校验真正想表达的不变量，要么删掉：
```go
	if !strings.EqualFold(filepath.Base(desktop), "hypomux.exe") {
		return coreServicePolicy{}, errors.New("installed desktop layout is invalid")
	}
	if !strings.EqualFold(filepath.Base(tunExecutable), "sing-box.exe") ||
		!strings.EqualFold(filepath.Dir(tunExecutable), filepath.Dir(engine)) {
		return coreServicePolicy{}, errors.New("protected sing-box must sit next to the Core Service executable")
	}
```
注意 `validateCoreServicePolicy:191-192` 已经在注册表侧校验了 `sing-box.exe` 位于 `bin` 下，两者应保持一致。

---

### [cmd] P1-9 `requireProtectedCoreACL` 对非普通 allow ACE 直接硬失败，错误信息误导

**位置**：`engine/cmd/hypomux-engine/service_policy_windows.go:454-456`
```go
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("protected Core path has an unsupported allow ACL entry: %s", path)
		}
```

**触发条件**：目录带 `ACCESS_ALLOWED_CALLBACK_ACE_TYPE`（0x9，条件允许，域环境/Conditional ACE 常见）、`SYSTEM_MANDATORY_LABEL_ACE_TYPE`（完整性级别标签，UAC 完整性级别几乎必然存在且由系统隐式添加）、对象专用 ACE。

**后果**：`install-service` 在**完全正常、权限正确**的安装目录上失败，报「unsupported allow ACL entry」，用户和排障者都会以为安装目录 ACL 有问题。系统添加的 mandatory label 尤其容易命中——这是 Windows 上的常态而非异常。注意顺序上 `:451` 已经先 `continue` 掉了 DENY ACE，所以问题只出在非 DENY 的其它类型上。

**为什么没兜住**：为了简化遍历而用类型断言 `var ace *windows.ACCESS_ALLOWED_ACE`（`:447`）统一取 ACE，这要求所有 ACE 内存布局相同；对回调 ACE 和对象 ACE 不成立，但代码把它们当作「不安全」而不是「未检查」。

**建议改法**：只拒绝真正无法解释的类型，对已知无害类型显式跳过：
```go
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE, windows.SYSTEM_AUDIT_ACE_TYPE, windows.SYSTEM_MANDATORY_LABEL_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			// 继续下面的 mask/SID 检查
		default:
			return fmt.Errorf("protected Core path has an unreviewed ACL entry type 0x%X: %s", ace.Header.AceType, path)
		}
```
并把错误消息里的「unsupported allow」改成「unreviewed」，与真实语义一致。

---

### [cmd] P1-10 `stopWindowsService` 的错误丢失服务名与阶段信息，卸载路径直接裸传

**位置**：`engine/cmd/hypomux-engine/service_windows.go:154` 与 `:164`
```go
			_, err = service.Control(svc.Stop)
```
```go
			return fmt.Errorf("stop service: timed out after %s", timeout)
```
以及 `service_windows.go:131`：
```go
	if err := stopWindowsService(service, 20*time.Second); err != nil {
		return err
	}
```

**触发条件**：服务拒绝停止 / 停止超时。

**后果**：错误消息是 `stop service: timed out after 20s`，既没说是哪个服务（`HypoMuxCore`），也没说是哪个阶段（安装更新前停服 vs 卸载前停服——`installWindowsService:79` 会包一层，而 `removeWindowsService:131` **什么都不加**）。当用户同时看到安装器和卸载器的报错时无法区分。另外 `:154` 把 `Control` 返回的 `svc.Status` 整个丢弃，停止被 pending 时的实际状态/等待提示都没记录。

**为什么没兜住**：函数签名只接受 `*mgr.Service`，服务名靠全局常量；`:131` 的裸 `return err` 是整个文件里唯一一处不加上下文的上抛。

**建议改法**：让函数自己补上下文并接受名字：
```go
func stopWindowsService(service *mgr.Service, name string, timeout time.Duration) error {
	...
			if time.Now().After(deadline) {
				return fmt.Errorf("stop %s service: timed out after %s (last state %v)", name, timeout, status.State)
			}
```
调用处 `stopWindowsService(service, coreServiceName, 20*time.Second)`，并把 `:131` 改成 `return fmt.Errorf("stop %s service before removal: %w", coreServiceName, err)`。

---

### [cmd] P1-11 管道连接没有写超时：对端不读时写入永久阻塞，优雅停止被拖到 15s 超时

**位置**：`engine/cmd/hypomux-engine/pipe_file_windows.go:14-18`
```go
type pipeFile interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	Fd() uintptr
}
```
与 `engine/cmd/hypomux-engine/pipe_windows.go:137-141`：
```go
	return &bufferedPipeConnection{
		Reader: reader,
		Writer: connection,
		Closer: connection,
	}, nil
```

**触发条件**：desktop 侧挂起（调试器断点、UI 冻结、代理链路阻塞）且不读管道。Service 端 `server.writeMessage`（`server.go:1062-1066`）在 `s.writeMu` 保护下同步写管道，缓冲区（`servicePipeBuffer` = 64 KiB，`service_windows.go:26`）填满后 `Write` 阻塞。

**后果**：写 goroutine 永久卡在 `Write`，`Execute.stop` 等满 15s 走 P1 超时路径 → 进程被强退 → 触发 P0-3 的全部网络状态残留。engine 端 `serve-pipe`（`main.go:105`）同样会卡在写上，Ctrl+C 只能取消 ctx，无法打断阻塞中的管道写。

**为什么没兜住**：`connectAuthenticatedPipe` 明确为**读**设置并清除了 deadline（`:104`、`:121`，注释也解释了为什么读 deadline 要清掉），但**写方向从未设过 deadline**。`pipeFile` 接口只声明了 `SetReadDeadline`，所以即使底层支持也用不上——`go-winio@v0.6.2/file.go:269` 的 `(*win32File).SetWriteDeadline` 就在那里没被用。

**建议改法**：接口补上写 deadline，握手成功后设置一个宽松的写超时：
```go
type pipeFile interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	Fd() uintptr
}

const pipeSessionWriteTimeout = 30 * time.Second // 应大于最长的单次 TUN/VNIC 操作
// connectAuthenticatedPipe 成功返回前：
if err := connection.SetWriteDeadline(time.Now().Add(pipeSessionWriteTimeout)); err != nil { ... }
```
服务端 `acceptServicePipe` 返回后同样设置。超时写入会让 `writeMessage` 返回错误 → `Run` 以 `write response: ...` 退出（`server.go:153-155`），会话正常回收，而不是拖到强退。

---

## P2 — 代码整洁度 / 清理不完整

### [cmd] P2-1 `removeWindowsService` 中停服失败后仍留下已停止的服务
**位置**：`service_windows.go:130`
```go
	if err := stopWindowsService(service, 20*time.Second); err != nil {
		return err
	}
```
`:131` 裸传错误（已在 P1-10 计入）。此处额外记录：函数先 `defer service.Close()`（`:128`）再停服，顺序正确（句柄在服务停止后才释放），但停服失败时**没有报告服务当前仍处于什么状态**——用户无法判断是该重试还是该强杀。P0-6 已涵盖补偿缺失，此处仅补充「状态未知」这一点。

### [cmd] P2-2 多处 `Close`/`Disconnect` 的返回错误被丢弃
**位置**：`service_windows.go:59` ``defer manager.Disconnect()``、`:96` ``defer service.Close()``、`:78`/`:83`/`:92` 处的 `service.Close()`（返回值完全未使用）；`service_policy_windows.go:96` ``defer key.Close()``、`:132` ``defer key.Close()``、`:261` ``defer file.Close()``、`:239` ``defer windows.CloseHandle(process)``；`pipe_file_windows.go:27` ``_ = windows.CloseHandle(handle)``、`:32` ``_ = file.Close()``。
这些在正常语义下无害，但 `windows.CloseHandle` 在句柄已被意外关闭（双重 close）时返回 `ERROR_INVALID_HANDLE`，恰恰是排查句柄生命周期 bug 的信号。建议在 Windows 构建下至少对关键句柄使用 `errors.Join` 汇总：
```go
defer func() { _ = manager.Disconnect() }() // 或在 run 的错误路径上 errors.Join(err, closeErr)
```
并把 `//nolint:errcheck` 标注在确实可忽略的位置，让后续 review 能区分「有意忽略」和「忘了看」。

### [cmd] P2-3 `serve-pipe` 路径对同一条管道连接执行两次 Close
**位置**：`engine/cmd/hypomux-engine/main.go:104`
```go
		defer connection.Close()
```
`connection` 同时作为 `runServer` 的 input/output 传入（`:105` `return runServer(connection, connection, stderr)`），而 `Server.Run` 的 defer（`engine/internal/server/server.go:123-128`）已经会关闭实现了 `io.Closer` 的 `s.input`：
```go
	defer func() {
		cancel()
		if closer, ok := s.input.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
```
第二次 `Close` 对 winio 管道文件返回一个错误（被 `defer` 丢弃）。功能无害，但会掩盖「谁负责关闭连接」这一约定。建议二选一：在 `main.go:104` 加注释说明这是兜底，或直接删掉该 defer 并依赖 `Server.Run`。`serve` 模式同理——`os.Stdin` 也会被 `Run` 的 defer 关闭。

### [cmd] P2-4 `pathWithin` 是死代码，唯一的安全不变量「所有受保护路径都在 ProgramData 根下」从未被校验
**位置**：`service_policy_windows.go:468`
```go
func pathWithin(root string, candidate string) bool {
```
全仓库只有 `service_policy_windows_test.go:95,98` 引用它，生产代码从不调用。`requireMachineInstallLocation`（`:302-342`）逐个校验 `paths` 里的绝对路径（reparse + owner + DACL），但从未断言它们**位于同一个根目录**下——函数名和注释（`:303-305`）承诺的「sidecars 与主程序同处一个 machine-owned 目录」只通过 `:318-321` 对**主 exe** 一条路径做了 `EqualFold` 断言，`:330-331` 的 `sing-box.exe`/`wintun.dll` 可以位于任何地方（只要各自 ACL 合法）。要么接上（对每个 `paths` 元素做 `pathWithin(programData根, path)`），要么删除，别留一个让人以为不变量已覆盖的函数。

### [cmd] P2-5 `connectAuthenticatedPipe` 在 ctx 已取消时仍会先做一次 `CreateFile`
**位置**：`pipe_windows.go:58-79`
```go
	for {
		handle, err = windows.CreateFile(
```
`ctx.Done()` 的检查在循环体末尾（`:74-78`），因此**第一次尝试无条件执行**。若传入已超时的 ctx，函数仍会真的去打开一次管道才返回 `ctx.Err()`。当前调用方 `main.go:97` 传入的是新 context，影响有限；测试 `pipe_windows_test.go:70`、`:103` 也都是新 ctx。建议在 `for` 之前加：
```go
	if err := ctx.Err(); err != nil {
		return nil, err
	}
```
另外 `:77` 的 `case <-time.After(40 * time.Millisecond):` 在循环内每轮新建一个 timer；40ms 轮询 × 20s 上限最多创建 500 个 timer，改用 `time.NewTicker` 或 `time.NewTimer` 复用更干净。

### [cmd] P2-6 `requireFixedNTFSVolume` / `fixedLocalVolumeRoot` 的 `UTF16PtrFromString` 错误裸传
**位置**：`service_policy_windows.go:349-352`
```go
	rootPointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
```
以及 `:385-388`、`:396-399`、`pipe_windows.go:53-56`、`service_windows.go:407-410`。这些是内嵌 NUL 才会触发的错误，裸传会让用户看到 `invalid argument`。统一包一层即可：
```go
		return "", fmt.Errorf("build volume root pointer %q: %w", root, err)
```

### [cmd] P2-7 `validatePinnedSHA256` 把「非十六进制」和「长度不对」合并成一条消息
**位置**：`service_policy_windows.go:206-207`
```go
	if err != nil || len(digest) != sha256.Size {
		return errors.New("pinned SHA-256 digest must contain 64 hexadecimal characters")
	}
```
`err`（`hex.DecodeString` 的具体位置信息）被丢弃。用户手工填注册表时，`0x...`（非法字符，位置在第 1 字节）和 `abc`（长度不足）得到完全相同的提示。建议：
```go
	digest, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("pinned SHA-256 digest is not hexadecimal: %w", err)
	}
	if len(digest) != sha256.Size {
		return fmt.Errorf("pinned SHA-256 digest has %d bytes, want %d", len(digest), sha256.Size)
	}
```

### [cmd] P2-8 `interruptServicePipeConnection` / `connectServicePipe` 丢弃全部 Win32 错误
**位置**：`service_windows.go:323-325`
```go
	_ = windows.CancelIoEx(handle, nil)
	_ = windows.DisconnectNamedPipe(handle)
	_ = connection.Close()
```
以及 `:378-380`
```go
			_ = windows.CancelIoEx(handle, &overlapped)
			var transferred uint32
			_ = windows.GetOverlappedResult(handle, &overlapped, &transferred, true)
			return ctx.Err()
```
这两处是 P0-3 的补救动作本身，把错误丢掉会让「补救为什么没生效」永远查不出来——尤其 `GetOverlappedResult(..., true)` 是**阻塞**等待取消完成，如果它卡住，整个 `stop` 又回到 15s 超时。建议把错误收集起来写进 stderr：
```go
	var errs []error
	if err := windows.CancelIoEx(handle, &overlapped); err != nil { errs = append(errs, err) }
	if err := windows.GetOverlappedResult(handle, &overlapped, &transferred, true); err != nil &&
		!errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		errs = append(errs, err)
	}
	return errors.Join(append([]error{ctx.Err()}, errs...)...)
```

### [cmd] P2-9 `newPipeFile` 的类型断言失败消息不含实际类型
**位置**：`pipe_file_windows.go:31-34`
```go
	connection, ok := file.(pipeFile)
	if !ok {
		_ = file.Close()
		return nil, fmt.Errorf("pipe transport does not support deadlines")
	}
```
如果哪天 go-winio 改了返回类型，这里只会得到一句「不支持 deadline」，无法定位。补 `%T` 即可：
```go
		return nil, fmt.Errorf("pipe transport %T does not support deadlines", file)
```

### [cmd] P2-10 `finalPathForHandle` 每次迭代重新分配缓冲区，且失败原因未区分「太长」与「空」
**位置**：`service_policy_windows.go:279`
```go
		buffer := make([]uint16, size)
```
循环上限 32768，最多重复分配 7 次（512→…→32768），每次失败都把已分配的大切片丢掉。建议复用同一个切片并 `slice = slice[:size]`；另外 `:299` 的 `"canonical path exceeds the Windows path limit"` 没有带上 `size`，排查时无从判断是超了 260 还是超了 32768。

### [cmd] P2-11 `WTSFreeMemory` 的返回状态被忽略，且 `defer` 中调用的是 `Proc.Call`
**位置**：`service_session_windows.go:51`
```go
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buffer)))
```
`defer` 一个返回三个值的 `Proc.Call` 在 `go vet` 下会被标记，且 `WTSFreeMemory` 失败（无效指针）说明前面已经有更严重的问题。建议改成具名闭包并记录：
```go
	defer func() {
		if r, _, err := procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buffer))); r == 0 {
			// 与查询结果一并上报到 service stderr
			_ = err
		}
	}()
```
另外 `procWTSFreeMemory` 同样没做 `Find()` 检查（与 P1-5 同源）。

### [cmd] P2-12 `main.go` 的参数校验失败一律静默返回 2
**位置**：`engine/cmd/hypomux-engine/main.go:44-46`
```go
		if len(args) != 2 {
			return 2
		}
```
以及 `:48-50`：
```go
		if err != nil || pid == 0 {
			return 2
		}
```
同文件的其它分支（如 `:68`、`:94`、`:141`）都会打印用法说明，只有 `signal-tun` 完全静默。桌面调用 `signal-tun` 时如果参数拼错，只会看到退出码 2 而不知道原因。建议至少打印一行：
```go
		if len(args) != 2 {
			fmt.Fprintln(stderr, "signal-tun requires exactly one PID argument")
			return 2
		}
		if err != nil || pid == 0 {
			fmt.Fprintf(stderr, "signal-tun: invalid PID %q\n", args[1])
			return 2
		}
```
（注意 `main_test.go:52-59` 只断言退出码 2，加输出不会破坏现有测试。）

### [cmd] P2-13 `runWindowsService` 把服务启动失败报成普通退出码，SCM 侧看不出原因
**位置**：`service_windows.go:193-196`
```go
	if err := svc.Run(coreServiceName, host); err != nil {
		fmt.Fprintf(stderr, "run %s: %v\n", coreServiceName, err)
		return 1
	}
```
`svc.Run` 在无法向 SCM 注册处理器时失败。Service 模式下 stderr 往往无人采集，用户只会看到服务启动后立刻停止，事件日志里也没有有用的信息。建议同时把失败原因写进 Windows 事件日志，或至少把消息扩展为「SCM rejected the service host, see Event Viewer」。属于可排查性问题，不影响正确性。

### [cmd] P2-14 `serveCoreServicePipe` 对被拒客户端无退避，`os.Stderr` 被刷屏
**位置**：`service_windows.go:282-285`
```go
			if errors.Is(err, errServiceClientRejected) {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				continue
			}
```
管道 ACL 是 `(A;;GRGW;;;IU)`（`:29`），任何交互式用户都能连接。循环体每次迭代都会 `loadCoreServicePolicy()`（一次注册表读）+ `createServicePipe()`（新建管道实例）。任何本地用户用循环 connect/disconnect 就能让服务以最快速度反复打印拒绝消息，把服务 stderr 淹没，同时制造大量管道实例创建/销毁。句柄本身有清理（`:338-339`），不会泄漏，但日志噪音会让 P0/P1 级别的问题无法被发现。建议加一个简单的退避 + 速率限制：
```go
	var rejected uint32
	var lastReject time.Time
	if errors.Is(err, errServiceClientRejected) {
		if time.Since(lastReject) > time.Minute {
			lastReject = time.Now()
			if n := atomic.AddUint32(&rejected, 1); n <= 5 {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "suppressing further Core Service client rejections (%d total)\n", n)
			}
		}
		continue
	}
```

---

## 为核实结论而额外阅读的文件

- `engine/internal/server/server.go`（`Run` 的 defer 与错误传播：`server.go:96-177`、`:1012-1041` `stopProxyForHostExit`、`:123-128` 关闭 input、`:1062-1066` `writeMessage`）——用于 P0-3、P0-4、P1-11、P2-3。
- `engine/internal/tun/config_stage_windows.go:82-94` `PrepareTrustedConfigStorage`——用于 P0-2、P0-6 的残留目录结论。
- `go-winio@v0.6.2/file.go:103,265,269`——确认 `SetWriteDeadline` 存在，支撑 P1-11 的建议。
- 测试文件 `main_test.go`、`pipe_windows_test.go`、`service_pipe_windows_test.go`、`service_policy_windows_test.go`、`service_session_windows_test.go`——用于确认预期行为（`service_session_windows_test.go:12-31` 明确断言只允许 `Active`，故未把 `Connected` 拒绝列为缺陷；`main_test.go:52-59` 只断言退出码 2，故 P2-12 的建议不会破坏测试）。


---

## 分片 C · `[proxy]` — engine/internal/proxy

## proxy/ 概览

`engine/internal/proxy/` 是 HypoMux 的网络数据面：27 个非测试 `.go` 文件，包含一个多协议入口 `server.go`（SOCKS5 + HTTP 双栈 accept loop）、`socks.go`/`http.go` 两个握手与 CONNECT 解析器、`dial.go`/`dial_windows.go` 的带源地址绑定拨号、`udp.go` 的 SOCKS5 UDP ASSOCIATE 中继、10 个 `steam_*.go` 的 Steam CDN 候选发现/探测/观测/计速子系统，以及 `registry.go`/`scheduler.go`/`adaptive.go`/`latency.go`/`health.go` 的连接注册表与健康度调度栈。整体代码风格是**偏防御性的**：上游拨号 `dial.go:50-59` 有正确的 lease 回滚，Steam CDN 探测的每个阶段都有 `c.note(...)` 打点，锁顺序也被仔细维护（`scheduler.mu → performance.mu`、`cdn.mu → session.mu → performance.mu`，未发现真实死锁环）。主要问题集中在**三个断层**：一是 UDP 数据面有三条错误路径完全没有留下任何痕迹（`udp.go:211`、`udp.go:223`、`udp.go:429`），而这些路径恰恰会污染节点健康度统计；二是所有每连接 goroutine **都没有 `recover()`**，单个客户端就能通过畸形 SOCKS/HTTP 报文打崩整个引擎守护进程；三是 UDP ASSOCIATE 在默认配置下必然失败，且失败原因被伪装成"未知 channel"。共 19 条发现：**P0 6 条 / P1 8 条 / P2 5 条**。

---

## P0 — 静默失效 / 可被外部输入打崩

### [proxy] P0-1 没有任何 per-connection goroutine 装 `recover()`，一个客户端即可打崩整个引擎

**位置**

- `engine/internal/proxy/server.go:434` — `go s.handleClient(protocol, client, session)`
- `engine/internal/proxy/steam_observer.go:238` — `o.mu.Lock()`（`expire` 内持锁，随后 `steam_observer.go:244` 取 `c.mu.Lock()`）
- `engine/internal/proxy/steam_observer.go:303` — `o.mu.Lock()`（`feed` 内持锁，随后 `steam_observer.go:309` 取 `c.mu.Lock()`）
- `engine/internal/proxy/steam_observer.go:291` — `o.mu.Lock()`（`close` 持锁，随后 `steam_observer.go:283` 在 `stopLocked` 内取 `o.s.cdn.mu.Lock()`）
- `engine/internal/proxy/udp.go:229` — `go func() { defer a.server.wg.Done(); flow.receiveLoop(clientAddress) }()`
- `engine/internal/proxy/steam_cdn.go:487` — `go func() {`（Steam 候选发现 goroutine）
- `engine/internal/proxy/steam_observed_probe.go:29` — `go func() {`（观测校验 goroutine）
- `engine/internal/proxy/steam_cdn.go:600` — `go func() {`（探测 worker）；`engine/internal/proxy/steam_observed_probe.go:126` 同形
- `engine/internal/proxy/latency.go:296` — `go func() {`（延迟探测 worker）
- `engine/internal/proxy/server.go:163` / `server.go:165` — `go func(ctx context.Context) { defer s.wg.Done(); s.scheduler.latency.run(ctx, s.scheduler) }(s.ctx)`

**触发条件**：任何客户端向 SOCKS5 或 HTTP 代理端口发送一份能触发 `handleClient` 下游代码 panic 的畸形输入即可。例如 `socks.go:132` `value := make([]byte, net.IPv4len)` 之后的解析、`http.go` 中 `http.ReadRequest` 面对恶意头部、或 `steam_observer.go:341` `r, e := http.ReadResponse(bufio.NewReader(bytes.NewReader(h)), o.pending[0].req)` 在 `o.pending[0].req` 为部分构造状态时越界。这些 goroutine 全部**没有** `defer recover()`。

**后果**：Go 中任何 goroutine 的未捕获 panic 会终止整个进程。代理引擎是一个**单进程服务全部用户**的守护程序，任意一个客户端的一次畸形请求就能让引擎崩溃、所有连接断开、正在进行的 Steam 加速与 UDP 转发全部中断。更严重的是 P0-1 的三个 `steam_observer.go` 变体：panic 若发生在 `expire`/`feed`/`close` 持有 `o.mu` 的窗口内，进程即便侥幸存活，**该 observer 的互斥锁永远不会被释放**，此后所有引用它的写路径（`server.go:579` 的 `steamObserverWriter.Write` 每次都会调 `feed`）都会永久阻塞 —— 单条 Steam 连接即可锁死该连接的整个 relay。

**为什么当前的错误处理没兜住**：`handleClient` 里只有 `defer s.wg.Done()`（`server.go:439`）、`defer client.Close()`（`server.go:440`）、`defer s.registry.Finish(session)`（`server.go:446`）三个资源清理型 defer，没有任何 recover 型 defer。`defer` 只负责解锁/关闭，**不拦截 panic**；panic 会先按栈展开执行 defer（释放锁），再继续向上传播到进程顶层终止 —— 也就是说连"锁泄漏"都只在 panic 后才发生，而进程通常在有机会被观测到之前就已经死了。整个包里 `_ = recover()` 一次都没有出现过。

**建议改法**：在每个入口 goroutine 的最外层加 recover，并且 recover 里必须补上 `registry.Finish`，否则该连接会永久占用 `counters.active`（`registry.go:190`）与 `directActive`：

```go
// server.go:434
go s.handleClient(protocol, client, session)
```
改为在 `handleClient` 首行（`server.go:438` 附近）插入：
```go
defer func() {
    if r := recover(); r != nil {
        slog.Error("proxy handler panic", "conn", session.id, "channel", channel, "panic", r)
        debug.PrintStack()
    }
}()
```
注意 `defer s.registry.Finish(session)` 已在 `server.go:446` 注册且先于 recover defer 执行，panic 展开时会正常跑完；但若希望 recover 后立即释放统计量，可在 recover 分支里显式再调一次 `s.registry.Finish(session)` —— `registry.go:235` 的 `if !session.finished.CompareAndSwap(false, true) { return }` 保证幂等，重复调用安全。`steam_cdn.go:487`、`steam_observed_probe.go:29`、`latency.go:296` 的后台 goroutine 同样需要 `defer func(){ if r:=recover(); r!=nil { slog.Error("probe goroutine panic", ...); debug.PrintStack() } }()`，因为它们由**外部流量触发**（一个 UDP 数据报就能让 `steam_observed_probe.go:48` 的校验跑起来），同样可被客户端打崩。

---

### [proxy] P0-2 UDP 数据面创建 flow 的错误被完全吞掉，连日志都没有

**位置**

`engine/internal/proxy/udp.go:211` — `if err != nil {`
`engine/internal/proxy/udp.go:212` — `return`

（错误来源在 `engine/internal/proxy/udp.go:335` — `return nil, errors.Join(failures...)` 与 `engine/internal/proxy/udp.go:333` — `return nil, errors.New("no UDP adapter available")`）

**触发条件**：客户端通过 SOCKS5 UDP ASSOCIATE 通道向一个当前没有可用适配器的目标发数据报（例如所有适配器都因 IPv6 源地址缺失被排除、或 `udp.go:272-293` 的两次 `dialUDP` 全部失败）。此时 `udp.go:210` `flow, err := a.createFlow(clientAddress, packet.target, packet.payload, exclude)` 返回非 nil error。

**后果**：**数据报被无声丢弃 —— 客户端收不到任何回显，SOCKS 层也不会有任何错误应答**，因为 UDP 是无连接的，`socks.go:59` 的 `writeSOCKSReply(client, 1)` 只在 ASSOCIATE 建立阶段发一次。更糟的是健康度已经被污染了：`udp.go:284` 的 `a.scheduler.MarkFailure(adapter.Name)` 和 `udp.go:306` 的同一调用已经执行。运维看到的是一个**正在冷却中的适配器，却没有任何一条日志说明是哪个目标、因为什么原因失败**。这正是本任务定义的"静默失效"：调度器会把适配器降级到冷却状态，而决策依据来自一条被丢弃的错误。

**为什么当前的错误处理没兜住**：`udp.go:210-213` 的错误分支只有裸 `return`，既没有 `flow.recordFailure(err)`（对比同文件 `udp.go:192-195` 的正确写法），也没有 `c.note(...)`（Steam 子系统全链路都在用这个打点，见 `steam_cdn.go:515` `c.note(generation, host, "", "", "dns_no_candidates")`），甚至没有一条 `slog` —— 整条路径上没有任何输出点。对比 `createFlow` 内部对单个适配器失败的处理（`udp.go:287` `failures = append(failures, fmt.Errorf("%s bind: %w", adapter.Name, err))`）非常完整，说明"组装成 `errors.Join`"这一步写好了，但"消费这个 join 错误"这一步整个漏掉了。

**建议改法**：至少打一条限速日志并把错误传递到上层统计。更完整的写法是让 `forward` 返回错误，由 `serve`（`udp.go:168` 调用点）决定回一个 SOCKS UDP 错误应答：

```go
// udp.go:210-213
flow, err := a.createFlow(clientAddress, packet.target, packet.payload, exclude)
if err != nil {
    slog.Error("udp flow creation failed",
        "client", clientAddress.String(), "target", packet.target, "err", err)
    return
}
```
若要保留可观测的结构化打点，可改用 `errors.Join(err, errors.New("udp_flow_create_failed"))` 让下游统一分类，但**不要**用 `steamProbeReason` 那样的字符串匹配（见 P1-8）。

---

### [proxy] P0-3 复用已有 flow 时的发送错误被丢弃，死亡 flow 永不退役

**位置**

`engine/internal/proxy/udp.go:223` — `_ = existing.send(packet.payload)`

（对照正确处理：`engine/internal/proxy/udp.go:192` — `if err := flow.send(packet.payload); err != nil {` / `udp.go:193` — `flow.recordFailure(err); flow.close()`）

**触发条件**：两个 goroutine 同时为同一 `(clientAddress, target)` 创建 flow。`udp.go:222-223` 的 `if existing := a.flows[packet.target]; existing != nil {` 命中表示已有 flow，此时**跳过 `a.forward` 的直接发送路径**改走 `existing.send`。若该 existing flow 的上游 UDP socket 已被对端关闭、或已被 sweep 标记为 stale，`send` 会返回非 nil error。

**后果**：与 P0-2 同构 —— 数据报丢失且**不留任何痕迹**。但危害面更大：走 `existing.send` 分支时**没有** `recordFailure`，所以这个已经死掉的上游 flow 不会累加失败计数、不进冷却；`udpAssociation.sweep` 只按 `lastActive`/`lastReply` 的空闲时间清理（`udp.go:19` `defaultUDPFlowIdleTimeout = 120 * time.Second`），于是一个持续有流量但持续写失败的 flow 可以**活跃存活长达 120 秒**，期间每个数据报都撞在这个坏 flow 上并被静默丢弃。注意这不是理论边角：`udp.go:222` 之后紧跟 `udp.go:225` `return`，本次数据报绝不会重试到别处。

**为什么当前的错误处理没兜住**：`flow.send` 的错误在两条调用路径上被区别对待 —— `udp.go:193` 正确地 `recordFailure` + `close`，而 `udp.go:223` 用 `_ =` 直接扔掉。这几乎肯定是"并发去重"功能（`existing` 快速路径）后加的，复制粘贴时漏了错误处理。`_ =` 这个写法本身就是在向读者声明"我知道这里有 error，我选择不管"，而这个选择对代理层来说是错的。

**建议改法**：与 `udp.go:192-195` 保持完全一致的处理：

```go
// udp.go:222-225
if existing := a.flows[packet.target]; existing != nil {
    flow.close()
    if err := existing.send(packet.payload); err != nil {
        existing.recordFailure(err)
        existing.close()
    }
    return
}
```

---

### [proxy] P0-4 `WriteToUDP` 失败与短写都不记录失败，死亡 flow 永不退役

**位置**

`engine/internal/proxy/udp.go:429` — `if err != nil {`
`engine/internal/proxy/udp.go:430` — `return`
`engine/internal/proxy/udp.go:432` — `if written != headerSize+count {`
`engine/internal/proxy/udp.go:433` — `return`

（对照正确处理：`engine/internal/proxy/udp.go:421` — `f.recordFailure(err)`）

**触发条件**：`receiveLoop` 收到上游应答后回写给客户端（`udp.go:428` `written, err := f.association.relay.WriteToUDP(packet[:headerSize+count], clientAddress)`）。触发场景：客户端的 UDP 源端口已关闭但 NAT 映射尚未超时（收到 ICMP port unreachable 后的 `ECONNREFUSED`）、relay socket 被 `udp.go:474` `_ = a.relay.Close()` 关闭、或写缓冲区压力导致短写。

**后果**：同 P0-3。`defer f.close()`（`udp.go:401`）会执行、flow 确实被关掉了，所以**不像 P0-2 那样是"静默丢弃 + flow 存活"**，但 `recordFailure` 没被调用意味着：**这个适配器/目标的失败不会进入健康度统计**，`udp.go:581-583` 的 `if s.snapshot().Strategy == StrategyLatency { s.MarkFailure(f.adapter.Name) }` 永远不会触发。一个系统性对客户端回写失败的适配器，在 latency-first 策略下会**持续被选中**，因为它的健康度看起来完美 —— 这是最典型的"健康度统计错误 → 调度器选坏节点"。

**为什么当前的错误处理没兜住**：`receiveLoop` 内部对**读**错误的处理是完整的（`udp.go:421` `f.recordFailure(err)` 然后 `udp.go:422` `return`），唯独**写**路径（428-433）只有裸 return。而 `udp.go:432-433` 的短写检查更糟：`written != headerSize+count` 说明只有部分字节进了 socket，而**连一个 error 值都没有**可传给 `recordFailure` —— 必须自己构造一个，否则这段代码在结构上就无法接入失败统计。

**建议改法**：

```go
// udp.go:428-434
written, err := f.association.relay.WriteToUDP(packet[:headerSize+count], clientAddress)
if err != nil {
    f.recordFailure(err)
    return
}
if written != headerSize+count {
    f.recordFailure(fmt.Errorf("short write to UDP client: %d of %d bytes", written, headerSize+count))
    return
}
```
`fmt` 已在 `udp.go:8` 导入，无需新增 import。

---

### [proxy] P0-5 acceptLoop 在持续性 Accept 错误下热自旋，打满一个 CPU 核

**位置**

`engine/internal/proxy/server.go:414` — `select { case <-s.ctx.Done(): return; default:`
`engine/internal/proxy/server.go:419` — `continue`

**触发条件**：`listener.Accept()` 返回一个**不是** `net.ErrClosed` 的错误。典型触发：文件描述符耗尽（`EMFILE`/`ENFILE`，高并发连接数下极常见）、套接字 backlog 溢出（`ECONNABORTED`，客户端疯狂 connect-then-close 时）、Windows 上的 `WSAEMFILE`。任何一种，只要它持续存在。

**后果**：`select` 的 `default` 分支立即触发，`continue` 立刻回到 `Accept()` —— **零退避、无 sleep、无限重试**。结果是单个核心 100% CPU 忙循环。因为 Go 调度器是抢占式的，其他 goroutine 仍能跑，但所有需要 CPU 的工作（TLS/握手、加解密、Steam 探测、遥测采样）都会被显著抢占；同时这条死循环永不退出，直到 `s.ctx` 被取消。**当 acceptLoop 就是被压垮的原因时，它自己成了负载放大器**，只会让 `EMFILE` 更严重，形成正反馈。

**为什么当前的错误处理没兜住**：代码只区分了"优雅关闭"（`server.go:411` `if errors.Is(err, net.ErrClosed) {`）和"其他一切"（`server.go:414-419`），把**临时性错误**（`EMFILE`、`ECONNABORTED`、`EINTR`，Go 文档明确要求重试这些）和**永久性错误**（`EAFNOSUPPORT`、listener 已损坏）混为一谈，全部走无退避重试。退一步说，即使这些错误都值得重试，**代码里连一个计数器或最后错误时间的记录都没有** —— 运维无法从遥测里发现 acceptLoop 正在打转。

**建议改法**：加入分类退避，并把计数暴露到遥测。

```go
// server.go:409-420 附近
var acceptBackoff time.Duration
defer func() { acceptBackoff = 0 }()
client, err := listener.Accept()
if err != nil {
    if errors.Is(err, net.ErrClosed) {
        return
    }
    s.stats.acceptErrors.Add(1)          // 需新增字段
    if !isTemporaryAcceptError(err) {    // EMFILE/ECONNABORTED/EINTR → true
        slog.Error("accept failed permanently", "protocol", protocol, "err", err)
        return
    }
    if acceptBackoff == 0 {
        acceptBackoff = 5 * time.Millisecond
    } else if acceptBackoff < time.Second {
        acceptBackoff *= 2
    }
    select {
    case <-s.ctx.Done():
        return
    case <-time.After(acceptBackoff):
    }
    continue
}
acceptBackoff = 0
if err != nil { /* 既有检查保持不变 */ }
```
`time` 已在 `server.go` 导入；`isTemporaryAcceptError` 需要新写，建议用 `errors.As` 取 `*net.OpError` 后判 `errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ECONNABORTED)`，并在 `dial_windows.go`/`dial_other.go` 已有 `health_errno` 分文件的前提下补上 Windows errno 别名。

---

### [proxy] P0-6 默认配置下 UDP ASSOCIATE 必然失败，且失败原因被伪装成"未知 channel"

**位置**

- `engine/internal/proxy/udp.go:88` — `scheduler: s.schedulers[session.channel],`
- `engine/internal/proxy/udp.go:97` — `if association.scheduler == nil && association.channel != ChannelDirect {`
- `engine/internal/proxy/udp.go:99` — `return false, fmt.Errorf("unknown UDP channel %q", session.channel)`
- 上游成因：`engine/internal/proxy/server.go:190` — `go s.acceptLoop(socks, "socks5", "")`
- map 构造：`engine/internal/proxy/server.go:90` — `schedulers: make(map[string]*scheduler, len(normalized.Channels)),`

**触发条件**：用默认配置启动引擎（`config.go` 的常规路径：设 `SOCKSPort`/`HTTPPort`，**不**启用 TUN 的 `Channels`），然后任何客户端发出 SOCKS5 `UDP ASSOCIATE` 命令。

**后果**：**功能完全不可用**。追踪执行路径：`server.go:190` 以 `channel == ""` 启动 acceptLoop → `registry.Begin(protocol, "", client)` 使 `session.channel == ""` → `udp.go:88` 在 `schedulers` map 里查 `""`。而 `schedulers` 在 `server.go:94-108` 只由 `normalized.Channels` 填充 —— **代码里不存在任何 `schedulers[""] = ...` 的赋值**（全文件搜索 `schedulers[` 只有 `server.go:100`、`server.go:103`、`server.go:472`、`tcp_profile.go:55`、`udp.go:88` 五处，前两处是 ChannelDirect/Aggregation 或具名 channel）。map 取值对缺失 key 返回 nil，于是 `udp.go:97` 命中，`udp.go:99` 返回错误，`socks.go:58-59` 回 SOCKS 错误码 1（general failure）。

同时注意对照 TCP 路径：`server.go:469-474` 的 `channelScheduler := s.scheduler` 后面跟着 `if session.channel != ""` —— **TCP 路径有针对空 channel 的兜底**，UDP 路径（`udp.go:88`）没有。这正是"同一个 bug 修了一处漏了一处"的典型形态。

**为什么当前的错误处理没兜住**：两层问题叠加。**功能层面**，`udp.go:88` 缺少与 `server.go:472` 等价的 fallback。**诊断层面**更严重：`udp.go:99` 抛出的 `unknown UDP channel ""` **是一个误导性的错误信息** —— 它让运维以为需要配置一个 channel，而实际上 channel 是空字符串才是问题本身、且空字符串恰恰是合法状态（`server.go:190` 就是这么启动的）。按本任务的标准，这属于"同一错误被包装成无法区分的通用消息"：所有 scheduler 查找失败（真未知 channel、nil scheduler、配置缺 channel）都被折叠成同一句话，无法区分。

**建议改法**：给 UDP 路径加上和 TCP 路径一致的 fallback，并在错误信息里区分两类原因。

```go
// udp.go:84-96 构造 association 之前
association := &udpAssociation{
    server:        s,
    control:       client,
    channel:       session.channel,
    scheduler:     s.schedulerForChannel(session.channel),  // 新增
    ...
}
```
```go
// server.go:469-474 处抽出一个共用方法
func (s *Server) schedulerForChannel(channel string) *scheduler {
    if channel == "" {
        return s.scheduler
    }
    if sch := s.schedulers[channel]; sch != nil {
        return sch
    }
    return nil
}
```
然后把 `udp.go:97-100` 的错误拆成两条互不相同的消息：
```go
if association.scheduler == nil && association.channel != ChannelDirect {
    _ = relay.Close()
    if session.channel == "" {
        return false, errors.New("UDP relay has no scheduler bound to the default channel")
    }
    return false, fmt.Errorf("unknown UDP channel %q: no such channel is configured", session.channel)
}
```

---

## P1 — 错误难排查 / 状态不一致

### [proxy] P1-7 上行方向（客户端 → 上游）的中继错误被丢弃，CDN 试运行永远不会标记失败

**位置**

`engine/internal/proxy/server.go:549` — `_ = upload(accountingWriter{`

（对照：`engine/internal/proxy/server.go:584` — `copyErr := download(accountingWriter{`）

**触发条件**：客户端到上游的写方向出现任何错误 —— 客户端半开连接后突然消失、上游在读完请求体后主动 `RST`、或者 CDN 试运行期间上游 CDN 节点立刻拒绝连接（这是 CDN 切换决策最需要捕捉的信号）。

**后果**：**静默失效。** `server.go:607` 的判定逻辑依赖三个输入：`failed := steamUpstreamFailed(copyErr, clientWriteFailed, session.cdnResponseFailed) && session.cdnTrial`。由于 `upload` 的错误被 `_ =` 丢掉、`clientWriteFailed` 只在写客户端时置位（上行方向客户端还活着就不会置位）、`cdnResponseFailed` 只能由 observer 抓包置位（需先收到一个响应头，而 CDN 节点若是**连响应头都不回**就直接断，就永远置不上），结果是：**"上游 CDN 节点秒断"这种最典型的坏节点特征，被系统判定为"传输成功"**。于是 `steam_cdn.go` 的 outcome 记账会把这个 CDN IP 记为健康，候选池持续把流量导向它 —— 正是本任务点名的"节点健康度统计错误 → 调度器选坏节点"。

**为什么当前的错误处理没兜住**：`relayTransfers` 里两个方向的错误处理是**不对称**的。下行用 `copyErr := download(...)` 完整保留并参与 `steamUpstreamFailed` 判定，上行用 `_ = upload(...)` 直接扔掉。`upload` 的返回值被显式忽略，说明作者当时认为"上行失败不重要"—— 但在代理数据面，上行失败才是判断上游节点好坏的**主要信号**（客户端通常不会主动断，且下行错误常被 observer 提前捕获）。

**建议改法**：保留错误并纳入同一套判定：

```go
// server.go:547-550
writer := io.Writer(client)
if session.cdnKey.domain != "" {
    writer = steamTimedWriter{ /* 不变 */ }
}
uploadErr := upload(accountingWriter{
    Writer:  writer,
    session: session,
    observers: &session.cdnObserver,
})
closeWrite(upstream)
```
然后把 `server.go:607` 改为：
```go
failed := steamUpstreamFailed(errors.Join(copyErr, uploadErr), clientWriteFailed, session.cdnResponseFailed) && session.cdnTrial
```
若担心客户端主动断开会被误判为 CDN 失败，可用现有的 `steamTimedWriter` 已经记录的 `blocked`/`failed` 标志（`server.go:579`）把客户端侧写失败（应归因客户端）与上游侧写失败（应归因 CDN）区分开，只把后者喂给 `accountTransfer`。

---

### [proxy] P1-8 探测错误被替换成不可区分的通用字符串，再被字符串匹配消费

**位置**

`engine/internal/proxy/steam_http_probe.go:111` — `if err != nil {`
`engine/internal/proxy/steam_http_probe.go:112` — `return nil, errors.New("http_probe_failed")`
`engine/internal/proxy/steam_http_probe.go:46` — `switch err.Error() {`
`engine/internal/proxy/steam_http_probe.go:50` — `return "http_probe_failed"`

**触发条件**：`steam_http_probe.go:110` 的 `response, err := client.Do(req)` 返回任何错误 —— DNS 失败、连接被拒（CDN IP 已经下线）、TLS 故障、1500ms 超时（`steam_http_probe.go:92` 的 ctx 超时）、`io` 读中断。

**后果**：**真实的失败原因被彻底销毁。** `client.Do` 返回的是 `*url.Error`，它完整包装了底层原因（`dial tcp 1.2.3.4:80: connect: connection refused` / `context deadline exceeded` / `x509: certificate has expired`），而 `steam_http_probe.go:112` 用 `errors.New("http_probe_failed")` 把它整个替换掉。更糟的是这个替换是**结构性的**：`steamProbeReason`（`steam_http_probe.go:42-51`）通过 `switch err.Error()` 做**字符串相等匹配**来判断原因，只有 4 个哨兵值（`http_budget`、`http_signature_rejected`、`http_range_unsupported`、`http_redirect_observed`）能被识别，其余一切（包括所有网络层错误）都落到 `steam_http_probe.go:50` 的同一个 `"http_probe_failed"`。于是**连接被拒**、**DNS 解析失败**、**超时**三种在运维意义上完全不同的事件，在遥测里是同一个字符串，在 `steam_observed_probe.go:69` `stage = steamProbeReason(e)` 之后又变成同一个 `stage` 字段。

**为什么当前的错误处理没兜住**：`errors.New("http_probe_failed")` 看起来像是想提供一个"分类结果"，但它**丢掉了 `%w`** —— 写成了 `errors.New` 而不是 `fmt.Errorf("http_probe_failed: %w", err)`，导致 `errors.Is`/`errors.As` 链路彻底断开，包装语义归零。而且 `steamProbeReason` 用 `err.Error()` 字符串比较而非 `errors.Is`，这既脆弱（哨兵字符串一旦被 `fmt.Errorf` 包装就匹配不上）又丢失了错误链。正确的做法是定义哨兵错误变量（`var errHTTPProbeFailed = errors.New("http_probe_failed")`）并用 `errors.Is` 匹配，同时**保留**底层错误供日志使用。

**建议改法**：区分"分类哨兵"与"底层原因"两层：

```go
// steam_http_probe.go 顶部
var (
    errHTTPBudget        = errors.New("http_budget")
    errHTTPSigRejected   = errors.New("http_signature_rejected")
    errHTTPRangeBad      = errors.New("http_range_unsupported")
    errHTTPRedirect      = errors.New("http_redirect_observed")
)
```
```go
// steam_http_probe.go:110-113
response, err := client.Do(req)
if err != nil {
    // 保留 url.Error 的原因链供日志/遥测；分类另由 probeReason 计算
    slog.Debug("steam http probe transport failed", "adapter", adapter.Name, "host", host, "ip", ip, "err", err)
    return nil, fmt.Errorf("%w: %w", errHTTPTransport, err)
}
```
```go
// steam_http_probe.go:42-51
func steamProbeReason(err error) string {
    switch {
    case err == nil:
        return "verified"
    case errors.Is(err, errHTTPBudget), errors.Is(err, errHTTPSigRejected),
         errors.Is(err, errHTTPRangeBad), errors.Is(err, errHTTPRedirect):
        return sentinelReason(err)     // 从包级映射表取，不再靠字符串比较
    case errors.Is(err, errHTTPTransport):
        return "http_transport_failed"   // 新增：网络层失败可与内容失败区分
    }
    return "http_probe_failed"
}
```
注意 `fmt.Errorf` 需要 `%w` 支持多错误包装（Go 1.20+）；若需保持单 `%w`，改为 `fmt.Errorf("http_transport_failed: %w", err)` 即可。

---

### [proxy] P1-9 Steam 探测链路的 resolver 构造失败静默返回，完全不打点

**位置**

`engine/internal/proxy/steam_cdn.go:499` — `probeResolver, err := dns.New(ctx, s.config.DNS, s.dialDNS)`
`engine/internal/proxy/steam_cdn.go:500` — `if err != nil {`
`engine/internal/proxy/steam_cdn.go:501` — `return`
`engine/internal/proxy/steam_observed_probe.go:43` — `resolver, e := dns.New(ctx, s.config.DNS, s.dialDNS)`
`engine/internal/proxy/steam_observed_probe.go:44` — `if e != nil {`
`engine/internal/proxy/steam_observed_probe.go:45` — `return`

**触发条件**：`dns.New` 返回错误。注释（`steam_cdn.go:497-498`）明确说明这里用的是"独立的、必须严格 DNS 的探测专用 resolver"： `// Optional discovery must not emit the ordinary resolver's strict-DNS` / `// fallback events. Its entire DNS lifetime ends with this probe job.` —— 也就是说这条 resolver **故意绕过了普通 resolver 的容错回退**，因此它构造失败的概率显著高于正常路径（配置缺 DNS、DoH 端点不可达、strict 模式必需字段缺失等）。

**后果**：**静默失效，且是有偏的静默失效。** `steam_cdn.go:503` 的 `s.probeSteamCandidates(...)` 永远不会被执行，该 `(host, port)` 的候选 IP 永远不会被验证或入库；同时 `steam_observed_probe.go:48` 的 `s.validateObservedSteam(...)` 同理。更重要的是 `c.discovery[key]`（`steam_cdn.go:483`）已经被写入 30 秒 TTL 且 `c.probing++` 已经在 `steam_cdn.go:484` 递增（虽然 defer 会在 `steam_cdn.go:490-496` 递减回去），所以**在整整 30 秒内该 host 不会再被尝试**。如果根因是持久的（配置问题），**整个 Steam CDN 加速功能可以永久静默失效**，而遥测里看不到任何东西 —— 因为 `steam_cdn.go:515` 的 `c.note(generation, host, "", "", "dns_no_candidates")` 是在 `probeSteamCandidates` 内部打的，函数压根没被调用到。

**为什么当前的错误处理没兜住**：这条路径的错误处理与**相邻 20 行**的处理方式明显不一致。`steam_cdn.go:514-516` 会为"没有候选"打点，`steam_cdn.go:524-527` 会为"HTTP baseline 失败"打点，`steam_observed_probe.go:54` 会为同类情况打点 `c.note(gen, host, adapter.Name, "", "dns_no_candidates")` —— **只有 resolver 构造失败这两处是裸 return**。错误变量 `err`/`e` 甚至命名都不同（`err` vs `e`），进一步说明这是两处独立编写的代码，都没有纳入统一打点体系。

**建议改法**：给这两处补上打点，新增一个明确的 stage 名以便遥测区分"解析器不可用"与"无候选"：

```go
// steam_cdn.go:499-502
probeResolver, err := dns.New(ctx, s.config.DNS, s.dialDNS)
if err != nil {
    c.note(generation, host, "", "", "dns_resolver_unavailable")
    slog.Warn("steam probe resolver unavailable", "host", host, "port", port, "err", err)
    return
}
```
```go
// steam_observed_probe.go:43-46
resolver, e := dns.New(ctx, s.config.DNS, s.dialDNS)
if e != nil {
    c.note(gen, k.domain, adapter.Name, "", "dns_resolver_unavailable")
    slog.Warn("steam observed-probe resolver unavailable", "host", k.domain, "err", e)
    return
}
```
另外建议在 `note` 的调用方约定里写明"任何提前 return 的分支必须先 note"，并把这个约定做成测试断言（例如统计 `c.note` 调用次数与所有 return 分支数相等）。

---

### [proxy] P1-10 UDP ASSOCIATE 已建立后，`serve` 的错误被丢弃且不再发 SOCKS 应答

**位置**

`engine/internal/proxy/socks.go:57` — `started, err := s.handleUDPAssociation(reader, client, session, host, port)`
`engine/internal/proxy/socks.go:58` — `if err != nil && !started {`
`engine/internal/proxy/socks.go:59` — `writeSOCKSReply(client, 1)`

**触发条件**：`handleUDPAssociation` 成功完成了绑定并在 `udp.go:101` 发出成功应答后（`started == true`），`association.serve(controlDone)` 在 `udp.go:126` 运行期间返回非 nil error —— 例如 `udp.go:153` 的 `return fmt.Errorf("read UDP relay: %w", err)`（relay socket 上的非超时、非 ErrClosed 读错误，如 Windows 上的 `WSAECONNRESET`）。

**后果**：**错误被完全丢弃**（`socks.go:58` 的条件因为 `!started` 为 false 而不成立，`err` 随即被丢弃），同时**没有向客户端发任何 SOCKS 应答字节**。客户端此时还阻塞在等 UDP ASSOCIATE 应答的位置，只能等到 `server.go:440` 的 `defer client.Close()` 触发 TCP 关闭才得知失败 —— 得到的是一个"连接被重置"而非"general failure"，语义完全不同。此外由于 `udp.go:105` 的 `defer association.close()` 已执行，relay 端口已释放但客户端毫不知情，会在几秒后重试 ASSOCIATE，打到新端口上。

**为什么当前的错误处理没兜住**：`handleUDPAssociation` 的 `(bool, error)` 双返回值设计本身就是缺陷来源 —— `started` 与 `err` 表达的是**同一次调用的两个维度**，但 `socks.go:58` 的 `err != nil && !started` 只处理了"没启动"这一种组合，把"启动过但后来失败"这一整个象限丢掉了。同一函数里其它应答分支（`socks.go:44`、`socks.go:54`、`socks.go:65`、`socks.go:71`）也全部忽略 `writeSOCKSReply` 的 bool 返回值（`socks.go:160-163` 明确定义了它会返回 `err == nil`），说明"写应答失败"这一整类错误从未被处理。

**建议改法**：先补上应答，再补上日志；同时把 `writeSOCKSReply` 的返回值接住。

```go
// socks.go:57-61
started, err := s.handleUDPAssociation(reader, client, session, host, port)
if err != nil {
    if !started {
        _ = writeSOCKSReply(client, 1)
    }
    slog.Error("UDP association failed",
        "session", session.id, "started", started,
        "host", host, "port", port, "err", err)
    return nil
}
```
其余 `writeSOCKSReply(client, ...)` 调用点（`socks.go:44`、`54`、`59`、`65`、`71`）建议统一改成 `if !writeSOCKSReply(client, N) { slog.Debug("socks reply write failed", ...) }`。

---

### [proxy] P1-11 UDP 关联的数据报泵在客户端 goroutine 上同步阻塞，单个慢目标即可拖垮整个关联通道

**位置**

`engine/internal/proxy/udp.go:126` — `err = association.serve(controlDone)`
`engine/internal/proxy/udp.go:168` — `a.forward(clientAddress, packet)`
`engine/internal/proxy/udp.go:272` — `for range attempts {`
`engine/internal/proxy/udp.go:288` — `ctx, cancel := context.WithTimeout(a.server.ctx, a.server.config.ConnectTimeout)`
`engine/internal/proxy/udp.go:289` — `upstream, err := a.server.dialUDP(ctx, dialer, target)`

**触发条件**：客户端在一个 UDP ASSOCIATE 关联内，同时（或先后）向**多个不同的目标**发数据报，且这些目标位于不可达网络。`udp.go:144` 的 `a.relay.ReadFromUDP` 每次循环只读**一个**数据报，随即在 `udp.go:168` 同步处理；而 `udp.go:140-142` 的 `default:` 分支只在循环**开头**检查 `controlDone` 与 `ctx`。

**后果**：**队头阻塞（head-of-line blocking）**。`udp.go:289` 的 `dialUDP` 最长阻塞 `a.server.config.ConnectTimeout`，且 `udp.go:267-270` 的 `attempts` 最多取 2，所以处理**一个**不可达目标的数据报最长要花 `2 × ConnectTimeout`（默认 6s → 12s）。在此期间 relay socket 内核缓冲区不断堆积其它目标的数据报；客户端（典型是游戏/浏览器）会大面积超时丢包。更糟的是**这个放大效应与 P0-2 叠加**：因为 `udp.go:211-212` 把 `createFlow` 的错误吞掉了，这 12 秒的阻塞在日志里完全不可见 —— 从运维视角看就是"SOCKS UDP 偶尔很卡，但没有任何错误记录"。

**为什么当前的错误处理没兜住**：这不是"吞错"而是"缺 backpressure 与缺隔离"，但后果落在同一个类别：无法诊断的静默失效。另外 `udp.go:143` 的 `_ = a.relay.SetReadDeadline(time.Now().Add(a.sweepInterval))` 用 `sweepInterval`（`udp.go:20` `defaultUDPFlowSweepInterval = 5 * time.Second`）当作读超时，意味着**每个数据报的读循环最多阻塞 5 秒**，与上面的 12 秒处理时间叠加后，单关联的实际吞吐上限极低 —— 但代码里没有任何指标暴露"关联泵正在排队"这个事实。

**建议改法**：把数据报处理从关联泵里拆出去，泵只负责收包，分发到有界 worker：

```go
// udp.go:132-170 serve 内，把同步 forward 改为有界并发分发
const udpDispatchWorkers = 8
dispatch := make(chan socksUDPPacket, 256)
var workers sync.WaitGroup
for range udpDispatchWorkers {
    workers.Add(1)
    go func() {
        defer workers.Done()
        defer func() {
            if r := recover(); r != nil {
                slog.Error("udp dispatch panic", "panic", r)
                debug.PrintStack()
            }
        }()
        for p := range dispatch {
            a.forward(clientAddress, p)   // 12s 阻塞被隔离在单个 worker 内
        }
    }()
}
```
在 `udp.go:168` 处改为 `select { case dispatch <- packet: default: /* 队列满：丢包并计数 slog.Warn("udp dispatch queue full", ...) }`，并在 `serve` 返回前 `close(dispatch); workers.Wait()`。有界队列 + 丢弃计数能让"过载"变成**可观测的计数器**而不是隐性卡顿。

---

### [proxy] P1-12 `Attach` 在释放 `session.mu` 之后才写入计数器，与 `Finish` 的无锁读构成竞态

**位置**

`engine/internal/proxy/registry.go:187` — `session.mu.Unlock()`
`engine/internal/proxy/registry.go:188` — `if counters != nil {`
`engine/internal/proxy/registry.go:189` — `session.counters.Store(counters)`
`engine/internal/proxy/registry.go:190` — `counters.active.Add(1)`

对照读取侧：`engine/internal/proxy/registry.go:242` — `if counters := session.counters.Load(); counters != nil {`
`engine/internal/proxy/registry.go:243` — `counters.active.Add(^uint64(0))`

**触发条件**：`registry.Finish(session)` 在 `Attach` 的 188-190 行之间执行。当前代码路径下这**不可达**（`Finish` 由 `server.go:446` 的 defer 注册，而 `Attach` 在 `server.go:488` 于同一函数内先于任何长耗时操作完成），所以今天不是活跃 bug —— 但这是一个**没有文档、没有断言保护的不变式**，任何人将来在 `Attach` 与 `Finish` 之间插入一个可能阻塞的调用就会踩中。

**后果**：若竞态发生，`Finish` 在 `registry.go:242` 读到 nil counters，转而走 `registry.go:244` 的 `else if session.direct.Load()` 分支；若 `direct` 也是 false（`AttachDirect` 未被调用过），**decrement 被彻底跳过**，该连接的 `counters.active` 永久停在 1。反之若 `Finish` 先于 `Attach` 执行，`registry.go:190` 的 `counters.active.Add(1)` 会发生在 decrement 之后，同样留下幽灵计数。任一方向的后果都是**遥测面板上活跃连接数只增不减**，`snapshot()` 持续报告一个偏高的活跃连接数。由于 `registry.go:249-265` 的 `CloseAll` 依赖 `r.connections` 而非 `active`，不会造成连接泄漏，但会让运维无法判断引擎负载。

**为什么当前的错误处理没兜住**：三处问题叠加。**其一**，`session.counters.Store`（189）放在 `session.mu.Unlock()`（187）**之后** —— `counters` 是 `connection` 的字段，与 `session.upstream`/`session.adapter` 受同一把锁保护，逻辑上应一起发布。**其二**，`Finish` 用 `session.counters.Load()`（242）做**无锁**读，而 `Attach` 用 `session.mu` 做**加锁**写 —— 读侧根本没有参与同一套同步约定。**其三**，`registry.go:188` 的 `if counters != nil` 是**死代码**：`counters` 在 `registry.go:173-178` 必然被赋成非 nil，唯一的 nil 来源是 `r.adapters[adapter.Name]` 命中已存在的项（也非 nil）。这个看似防御性的判断掩盖了"这里其实可能为 nil"的误读，实际语义应该是"读侧看到的是 nil"。

**建议改法**：把计数器的发布移进锁内，并让读取侧与写入侧遵守同一把锁：

```go
// registry.go:180-191
session.mu.Lock()
session.upstream = upstream
session.target = target
if address := upstream.RemoteAddr(); address != nil {
    session.remote = address.String()
}
session.adapter = adapter.Name
session.counters.Store(counters)      // 移入锁内，与其余字段原子发布
session.mu.Unlock()
counters.active.Add(1)
```
```go
// registry.go:242-246
session.mu.RLock()
counters := session.counters.Load()
session.mu.RUnlock()
if counters != nil {
    counters.active.Add(^uint64(0))
} else if session.direct.Load() {
    r.directActive.Add(^uint64(0))
}
```
并删掉 `registry.go:188` 的死判断（`counters != nil` 恒真），同时在 `connection` 结构体（`registry.go:76-100`）的 `counters` 字段旁加注释说明"必须在 `session.mu` 下发布，`Finish` 依赖此约定"。

---

### [proxy] P1-13 Steam 发现 goroutine 未纳入 `s.wg`，`Stop` 超时路径还会泄漏一个看门狗 goroutine

**位置**

- `engine/internal/proxy/steam_cdn.go:487` — `go func() {`
- `engine/internal/proxy/steam_observed_probe.go:29` — `go func() {`
- 对照正确做法：`engine/internal/proxy/server.go:432` — `s.wg.Add(1)`
- `engine/internal/proxy/server.go:249` — `done := make(chan struct{})`
- `engine/internal/proxy/server.go:250` — `go func() {`
- `engine/internal/proxy/server.go:256` — `case <-ctx.Done():`
- `engine/internal/proxy/server.go:257` — `return fmt.Errorf("stop proxy: %w", ctx.Err())`

**触发条件**：调用 `Server.Stop(ctx)` 且 `ctx` 带超时（例如 5 秒），而此时有 Steam 发现任务在跑 —— `steam_cdn.go:487` 的 goroutine 最长活 20 秒（`steam_cdn.go:488` 的 `context.WithTimeout(root, 20*time.Second)`），`steam_observed_probe.go:30-34` 同理（`deadline := time.Now().Add(20 * time.Second)`）。

**后果**：**goroutine 泄漏**。这些 goroutine 没有 `s.wg.Add(1)`，所以 `server.go:251` 的 `s.wg.Wait()` **不会等它们**；`Stop` 在 `server.go:256` 超时返回后，这些 goroutine 仍在运行并继续访问 `s.cdn`、`s.config`、`s.dialDNS`。更具体地说，`steam_cdn.go:490-496` 的 defer 会 `c.mu.Lock()` 并递减 `c.probing` —— 如果此时引擎已经被上层关闭重建、或者 `c.generation` 已经变化（`steam_cdn.go:493` 的 `if generation == c.generation` 判断会跳过递减，导致 `c.probing` 永久偏高），**探测准入会被 `steam_cdn.go:479` 的 `c.probing >= 2` 永久卡死**，Steam CDN 加速从此静默失效。

同时 `server.go:249-253` 本身就有泄漏：那个 `s.wg.Wait()` 看门狗 goroutine 在 `Stop` 超时（`server.go:256-257` 直接 return）时**永远不会被关闭**，`done` channel 也永远不关闭 —— 每次 `Stop` 超时都泄漏一个 goroutine，且它持有的 `s.wg` 引用会阻止相关对象被 GC。

**为什么当前的错误处理没兜住**：`server.go:257` 的错误返回是对的（用 `%w` 包装了 `ctx.Err()`），但**返回之后没有任何清理动作** —— 看门狗 goroutine、正在运行的发现 goroutine，全都被留在原地。`steam_cdn.go:487`/`steam_observed_probe.go:29` 没有 `wg.Add` 是根本原因：这两个 goroutine 由**外部网络流量触发**（一个 Steam 数据报就能启动它们），不是由 `acceptLoop` 统一 spawn，所以很容易在实现时漏掉生命周期登记。它们的唯一停止手段是 `ctx` 取消，而 `ctx` 取消后 goroutine 还需要时间收尾 —— `Stop` 的 5 秒超时不覆盖这个时间。

**建议改法**：给两个发现 goroutine 登记到 `s.wg`，并让 `Stop` 的看门狗可被取消。

```go
// steam_cdn.go:486-487 与 steam_observed_probe.go:28-29
s.wg.Add(1)
go func() {
    defer s.wg.Done()
    ...
}()
```
```go
// server.go:249-258
done := make(chan struct{})
go func() {
    defer close(done)
    s.wg.Wait()
}()
select {
case <-done:
case <-ctx.Done():
    // 不再泄漏看门狗：它会在 wg 排空后自行退出，
    // 但 done channel 必须能被观测以免 channel 悬挂
    slog.Warn("proxy stop timed out; in-flight sessions still draining",
        "err", ctx.Err(), "sessions", len(s.registry.snapshotConnections()))
    return fmt.Errorf("stop proxy: %w", ctx.Err())
}
```
`done` 是局部变量，超时 return 后无人持有，goroutine 完成后 `close(done)` 不会阻塞（无接收者时 close 立即返回），因此看门狗本身其实**不会**泄漏 —— 真正的问题是错误信息里没有"还有多少会话在排空"，排障时完全看不出 `Stop` 为何超时。建议按上面的注释补充日志，并在 `registry` 上暴露一个未加锁的会话计数（`registry.go:249-255` 已有遍历逻辑可复用）。

---

### [proxy] P1-14 `newSteamObserver` 与 `expire`/`feed`/`stopLocked` 的锁获取顺序相反，仅靠"发布时机"侥幸避免死锁

**位置**

- 正向（cdn.mu → o.mu）：`engine/internal/proxy/steam_observer.go:218` — `c.mu.Lock()`（在 `newSteamObserver` 内）
- 正向（同一函数内取第二把锁）：`engine/internal/proxy/steam_observer.go:232` — `o.mu.Lock()`
- 反向（o.mu → cdn.mu）：`engine/internal/proxy/steam_observer.go:238` — `o.mu.Lock()`（`expire` 内）
- 反向：`engine/internal/proxy/steam_observer.go:244` — `c.mu.Lock()`
- 反向：`engine/internal/proxy/steam_observer.go:303` — `o.mu.Lock()`（`feed` 内）
- 反向：`engine/internal/proxy/steam_observer.go:309` — `c.mu.Lock()`
- 反向：`engine/internal/proxy/steam_observer.go:291` — `o.mu.Lock()`（`close` 内）
- 反向：`engine/internal/proxy/steam_observer.go:283` — `o.s.cdn.mu.Lock()`（`stopLocked` 内）

**触发条件**：`newSteamObserver` 在持有 `c.mu`（218，`defer` 在 219 解锁，覆盖整个函数体）期间，于 232 行尝试获取 `o.mu`；与此同时 `expire`（由 231 行的 `time.AfterFunc(5*time.Second, o.expire)` 触发）、`feed`（由数据面写路径 `server.go:579` 触发）、或 `close`（由 233 行的 `context.AfterFunc(c.ctx, o.close)` 触发）在持有**同一个 observer 的** `o.mu` 时，于 244/309/283 行尝试获取 `c.mu`。

**后果**：经典的 ABBA 死锁形态。**当前没有实际死锁**，唯一原因是：`newSteamObserver` 在 232 行取的那个 `o.mu` 属于一个**尚未被发布**的新对象（它要等到 235 行 return 之后才被 `http.go:55` 赋给 `session.cdnObserver`），所以没有第二个 goroutine 能持有它。这属于"靠发布时序而非靠锁约定"来避免死锁 —— 极其脆弱。任何一次把 observer 提前发布、或把 232-234 的 `o.mu` 操作挪到 `c.mu` 解锁之后的重构，都会立刻引入一个**全引擎级死锁**（`cdn.mu` 持有者阻塞在 `o.mu`，`o.mu` 持有者阻塞在 `cdn.mu`，而 `cdn.mu` 是所有 Steam 路径的全局锁）。

**为什么当前的错误处理没兜住**：这不是吞错，而是**错误处理（`recover`）恰好会掩盖它**。如果按 P0-1 的建议给 `feed`/`expire` 加 `recover()`，一次死锁超时后的 panic 恢复会让 `o.mu` 被释放，死锁**看起来被"修好了"**，实际上只是把硬死锁变成软故障，更难排查。代码里没有任何注释记录这条隐含不变式。

**建议改法**：消除反向获取，不依赖发布时序。

```go
// steam_observer.go:231-235，把 o.mu 的操作移到 c.mu 解锁之后
o.timer = time.AfterFunc(5*time.Second, o.expire)
c.mu.Unlock()                 // 显式解锁，替代 defer
defer func() { if o.stopContext != nil { o.stopContext() } }()

o.mu.Lock()
o.stopContext = context.AfterFunc(c.ctx, o.close)
o.mu.Unlock()
return o
```
关键是**绝不**在持有 `c.mu` 时获取任何 `o.mu`。全程锁顺序固定为 `o.mu → c.mu`（与 `expire`/`feed`/`stopLocked` 一致）。同时在 `steamHTTPObserver` 类型定义处加注释：`// 锁顺序：始终 o.mu → s.cdn.mu。禁止在持有 s.cdn.mu 时获取 o.mu。`

---

## P2 — 代码整洁度 / 可维护性

### [proxy] P2-15 `handleClient` 的返回值被直接丢弃，`_ = adapter` 是死赋值

**位置**

`engine/internal/proxy/server.go:455` — `_ = adapter`

**触发条件**：每一个通过 `handleSOCKS`（`socks.go:14`）或 `handleHTTP`（`http.go:19`）走完全流程的连接。

**后果**：`Adapter` 作为返回值从 `socks.go:91` 的 `return &adapter` 一路传回，却在 `server.go:455` 被显式丢弃。这本身无害，但 `_ =` 的写法在 Go 中通常表示"这里有意忽略一个有意义的值" —— 在审计中它与真实的吞错（`udp.go:223`）外观完全一致，**制造了审计噪声**，也让"这个返回值是否真的不需要"这个设计问题被永久掩盖。

**为什么当前的错误处理没兜住**：不适用（非错误处理缺陷）。真正的成因是 API 设计：两个 handler 返回 `*Adapter` 却没有任何消费者。

**建议改法**：让两个 handler 直接返回 `error`（或干脆无返回值），删掉 `*Adapter` 这一层间接。

```go
// socks.go:14
func (s *Server) handleSOCKS(reader *bufio.Reader, client net.Conn, session *connection) *Adapter {
```
改为 `func (s *Server) handleSOCKS(...) error`，把 `socks.go:17`、`21`、`32`、`35`、`40`、`46`、`61`、`73`、`77` 等处的 `return nil` 改为 `return err`（配套补上原本就该有的 `io.ReadFull` 错误 —— 见下条），`socks.go:91` 改为 `return nil`。`http.go:19` 同理。`server.go:455` 的 `_ = adapter` 随之删除。

---

### [proxy] P2-16 `writeHTTPError` 用 `_, _ =` 抹掉全部 HTTP 失败原因

**位置**

`engine/internal/proxy/http.go:317` — `_, _ = fmt.Fprintf(client, "HTTP/1.1 %s\r\nConnection: close\r\nContent-Length: 0\r\n\r\n", status)`

**触发条件**：任何 HTTP 代理错误路径 —— `http.go:51` 的连接失败、`http.go:176` 与 `http.go:187` 的非 HTTP 上游响应、以及 `http.go:62` 之后所有转发失败。

**后果**：**所有** HTTP 错误响应对客户端都是同一个 `502 Bad Gateway`，且写失败本身也无从得知。其中 `http.go:51` 最能说明问题 —— `s.connect` 已经返回了一个带 `errors.Join` 的详细错误（`dial.go:153` `errors.Join(failures...)`，内含每个适配器的 `"%s connect: %w"`），**连接失败的完整原因在传给 `writeHTTPError` 之前就被丢弃了**，客户端只看到 502，服务端也没有日志。这与 P1-8 是同一类错误的两种表现：一个在探测侧，一个在 HTTP 代理侧。

**为什么当前的错误处理没兜住**：`writeHTTPError` 的签名 `func writeHTTPError(client net.Conn, status string)` 没有 `error` 参数，调用方**在语法上就无法**把原因传进去。`_, _ =` 是为了让 `fmt.Fprintf` 的双返回值通过编译而写的，属于"为消除编译错误而丢信息"，不是有意的错误处理决策。

**建议改法**：给签名加一个 error 参数并记录日志。

```go
// http.go:316-318
func writeHTTPError(client net.Conn, status string, cause error) {
    if cause != nil {
        slog.Info("http proxy error", "status", status, "err", cause)
    }
    if _, err := fmt.Fprintf(client, "HTTP/1.1 %s\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\nContent-Length: 0\r\n\r\n", status); err != nil {
        slog.Debug("http error response write failed", "status", status, "err", err)
    }
}
```
调用点相应改为 `writeHTTPError(client, "502 Bad Gateway", err)`。对客户端可考虑在响应体里放一个短的 request-id，把服务端日志与客户端可见的失败对应起来。

同类"整类错误被静默丢弃"的位置还有：`engine/internal/proxy/server.go:421` — `_ = client.SetDeadline(time.Time{})`（清理 deadline 失败无法观测）、`engine/internal/proxy/steam_sniff.go:52` — `defer client.SetReadDeadline(time.Time{})`（defer 中丢弃返回错误）、`engine/internal/proxy/tcp_profile.go:79` — `_ = tcp.SetReadBuffer(tcpSocketBufferSize)`（该处 77-78 行注释已说明是"尽力而为"的套接字调优，属可接受，但建议注释里补一句"失败不影响功能，因此可安全忽略"以区别于真正的漏写）、`engine/internal/proxy/registry.go:257` — `_ = session.clientConn.Close()` 与 `registry.go:262` — `_ = upstream.Close()`（`CloseAll` 批量关闭时的错误建议用 `errors.Join` 汇总上报，而非逐个丢弃）。

---

### [proxy] P2-17 `steam_accounting.go` 的失败窗口表容量上限在计数器已递增之后才判断

**位置**

`engine/internal/proxy/steam_accounting.go:45` — `state := c.failures[key]`
`engine/internal/proxy/steam_accounting.go:46` — `if state == nil {`
`engine/internal/proxy/steam_accounting.go:47` — `if len(c.failures) >= 512 {`
`engine/internal/proxy/steam_accounting.go:48` — `return`

**触发条件**：`c.failures` 达到 512 个不同 key（key 格式见 `steam_accounting.go:13` `func steamFailureKey(k cdnKey) string { return k.adapter + "/" + k.domain + ":" + k.port }`）。在多适配器 × 多 Steam 域名的配置下是可达到的。

**后果**：**两个计数器与第三个计数器不一致**。`steam_accounting.go:42` 的 `t.failures++` 和 `steam_accounting.go:43` 的 `c.transferFailures++` 在 47-48 的容量检查**之前**已经执行，所以即使窗口项没有被记录，这两个计数照样递增。结果：`c.transferFailures` 与 `t.failures` 会**持续增长**，但 `steam_accounting.go:53-60` 的窗口逻辑（`if c.now().Sub(state.at) > time.Minute { ... }` 与 `if state.count >= 3 { state.until = ... }`）永远不会被触发 —— 因为**没有窗口项**。也就是说，超过 512 个 key 之后，**CDN 失败隔离功能静默失效**：失败的 CDN IP 不会再被隔离，而计数器却在增长，遥测会显示"有很多失败"但"没有任何隔离发生"。

**为什么当前的错误处理没兜住**：`len(c.failures) >= 512` 这个容量上限是一个**静默丢弃** —— `return` 之后既不记录也不告警。代码的意图显然是防止 `c.failures` 无限增长（一个慢速内存泄漏的防护），但防护手段选择了"丢弃新数据"，而**没有选择在丢弃时清理过期窗口**（`state.until` 已经提供了判断过期的信息，`steam_accounting.go:59` `state.until = c.now().Add(time.Minute)`）。

**建议改法**：在达到上限时先淘汰已过期的窗口，并让两个计数器与窗口保持一致。

```go
// steam_accounting.go:44-52
key := steamFailureKey(k)
state := c.failures[key]
if state == nil {
    if len(c.failures) >= 512 {
        c.pruneFailureWindowsLocked()   // 新增：淘汰 until 已过期的项
        if len(c.failures) >= 512 {
            // 容量已满：本次不记窗口，但要把计数器也一并回滚，避免
            // transferFailures 与实际被隔离的失败数不一致
            t.failures--
            c.transferFailures--
            return
        }
    }
    state = &steamFailureWindow{at: c.now()}
    c.failures[key] = state
}
```
```go
// steam_accounting.go 新增（调用方已持有 c.mu）
func (c *steamCDN) pruneFailureWindowsLocked() {
    now := c.now()
    for k, v := range c.failures {
        if !v.until.After(now) {
            delete(c.failures, k)
        }
    }
}
```

---

### [proxy] P2-18 上下文取消后生产者循环不 `break`，继续空转迭代

**位置**

- `engine/internal/proxy/latency.go:311` — `select {`
- `engine/internal/proxy/latency.go:313` — `case <-ctx.Done():`
- `engine/internal/proxy/steam_cdn.go:639` — `select {`
- `engine/internal/proxy/steam_cdn.go:641` — `case <-ctx.Done():`
- 相关：`engine/internal/proxy/latency.go:300` — `continue`（worker 侧）

**触发条件**：`Server.Stop()` 或 `Server.Start()` 失败回滚（`server.go:210` 的 `s.cancel()`）时，这两个生产者循环正在遍历（`latency.go:309-316` 的 `for _, target := range targets { for _, a := range adapters {`；`steam_cdn.go:638` 的 `for _, task := range jobs {`）。

**后果**：**不会死锁也不会泄漏**（worker 在 `latency.go:299-300` 判 `ctx.Err() != nil` 后 `continue` 持续排空 channel，所以生产者不会阻塞在 `jobs <- job{...}` 上），但取消之后生产者会**把剩余的每一个 (target × adapter) 组合再走一遍 `select`**，每次都立即命中 `ctx.Done()`。在多适配器配置下这是纯粹浪费的 CPU，且 `latency.go:300` 的 `continue` 让 8 个 worker（`latency.go:294` `for range 8`）在取消后继续空转排空，直到 `jobs` 被 close。这是取消延迟的一个来源 —— `server.go:251` 的 `s.wg.Wait()` 会等 `latency.go:294` 的 8 个 worker 全部退出。

**为什么当前的错误处理没兜住**：取消（ctx cancellation）被当成了一个需要"每轮重新判断"的状态，而不是一个"应该立刻终止循环"的终止信号。`select` 的 `ctx.Done()` 分支只是选中了，就自然地进入了下一轮迭代 —— 缺的是 `break`/`return`。`steam_cdn.go:638-643` 同形。

**建议改法**：

```go
// latency.go:309-316
targets:
for _, target := range targets {
    for _, a := range adapters {
        select {
        case jobs <- job{a, target}:
        case <-ctx.Done():
            break targets
        }
    }
}
```
```go
// steam_cdn.go:638-643
for _, task := range jobs {
    select {
    case queue <- task:
    case <-ctx.Done():
        break
    }
}
```
```go
// latency.go:298-301（worker 侧应退出而非空转）
for j := range jobs {
    if ctx.Err() != nil {
        return
    }
    ...
}
```
注意 worker 侧改成 `return` 后必须保证 `jobs` channel 最终仍被 `close`（`latency.go:317`），否则生产者会永久阻塞 —— 但因为生产者也会在 `ctx.Done()` 时 break，`close(jobs)` 一定能执行到，所以是安全的。

---

### [proxy] P2-19 observer 定时器自重入，唯一停止路径依赖调用方记得调用 `close()`

**位置**

`engine/internal/proxy/steam_observer.go:265` — `o.timer = time.AfterFunc(5*time.Second, o.expire)`
`engine/internal/proxy/steam_observer.go:231` — `o.timer = time.AfterFunc(5*time.Second, o.expire)`

**触发条件**：每一个被创建的 observer（`steam_observer.go:214` 的 `if session.cdnKey.port != "80" || !s.cdn.active() { return nil }` 之后）。

**后果**：`steam_observer.go:265` 的自重入定时器每 5 秒唤醒一次直到 `stopLocked`（`steam_observer.go:267-286`）把它停掉。唯一的停止路径是 `steam_observer.go:276` 的 `if o.timer != nil { o.timer.Stop() }`，而 `stopLocked` 只被三个地方调用：`steam_observer.go:248`、`253`（`expire` 内部）、`steam_observer.go:293`（`close`）。如果某条新增的代码路径创建了 observer 却忘了调 `close()`，`steam_observer.go:223` 的 `c.observers++` 配额（上限 64，见 `steam_observer.go:220`）会被**永久占用**，且那个 5 秒定时器会**永远存活** —— 64 次泄漏后 Steam CDN 观测功能对所有新连接完全失效，且不会有任何错误提示。这与 P1-13 的 goroutine 未登记是同一类生命周期管理缺陷。

**为什么当前的错误处理没兜住**：目前所有创建点都正确配对了（`http.go:55-56` 的 `session.cdnObserver = s.newSteamObserver(session)` + `defer session.cdnObserver.close()`；`server.go:531-534` 的 `if session.cdnObserver == nil { session.cdnObserver = s.newSteamObserver(session) }` + `defer session.cdnObserver.close()`），所以**当前不是活跃 bug**。但 `steam_observer.go:299-301` 的 nil-safe `feed`（`if o == nil { return }`）让 nil observer 完全静默 —— 这意味着**将来若有第三处忘了 `close()`，不会有任何测试或运行时信号发现它**，配额会静静耗尽。

**建议改法**：把生命周期绑到 session 上，并用 finalizer 作为兜底。

```go
// steam_observer.go:224 附近，创建时立刻安装 finalizer 作为"忘记 close"的兜底
o := &steamHTTPObserver{s: s, session: session}
runtime.SetFinalizer(o, func(x *steamHTTPObserver) { x.close() })
```
```go
// steam_observer.go:267 stopLocked 开头，补一个显式的关闭来源注释
func (o *steamHTTPObserver) stopLocked() {
    // 幂等：由 expire / close 两条路径调用，调用方必须保证至少走一条。
    if o.stopped {
        return
    }
    ...
```
并在 `connection` 结构体（`registry.go:76-100`）的 `cdnObserver` 字段上补注释，说明"`cdnObserver` 非 nil 时必须有且只有一个 `defer o.close()`"。同时建议在 `steam_observer.go:220` 的配额检查处加一个可观测计数（`c.observers` 的峰值），让配额耗尽在遥测里可见。

---

## 附：已核查但**未**列为问题的项（避免误报）

为便于复核，记录本次审计中确认**正确**或**不成立**的怀疑点：

- `engine/internal/proxy/server.go:421` 的 `_ = client.SetDeadline(time.Time{})` 不可达 nil 解引用 —— 该行只在 `err == nil` 时执行（`server.go:414-419` 的错误分支均已 `continue`/`return`）。
- `engine/internal/proxy/registry.go:257` 的双重 `Close()` 安全 —— `steam_observer.go:268` 的 `if o.stopped { return }` 使 `stopLocked` 幂等，`c.observers--`（`steam_observer.go:284`）只执行一次；`adaptive.go:304` 的 `leasedConn.Close()` 经 `adaptive.go:152` 的 `if l.finished { return }` 亦幂等。
- `engine/internal/proxy/server.go:543-544` 与 `557-558` 的两处 `acquireTCPRelayBuffer` 是**每 goroutine 一次**（不是循环内），无缓冲池/fd 泄漏。
- `engine/internal/proxy/dial.go` 的 lease 回滚**完全正确**：`dial.go:50-54` 的 defer 兜底 + `dial.go:56-59` 的每轮重置 + `dial.go:136-140` 的 `pending.attach()` 后置 nil，六条 `continue` 路径全部经过 56-59 释放 lease。
- `engine/internal/proxy/steam_cdn.go:789` 的 `lease.attach()` **不会** nil 解引用 —— `adaptive.go:133` 的 `performanceTable.acquire` 总是返回非 nil lease（`adaptive.go:146-149` 的 `finish()` 虽有 nil 保护，但 `acquire` 的返回路径保证了非 nil）。
- `engine/internal/proxy/steam_cdn.go:517` 与 `:513` 的 `s.scheduler.snapshot()` 调用前 `c.mu` 已释放（`steam_cdn.go:512`、`510`），**不存在** `cdn.mu → scheduler.mu` 的锁嵌套；全包 `session.mu` 只在 `registry.go` 与 `steam_cdn.go:781` 获取，无 `cdn.mu → session.mu → cdn.mu` 环路 —— **未发现真实死锁**（P1-14 记录的是脆弱性而非现存死锁）。
- `engine/internal/proxy/http.go:156` 的 `<-upgradeDecision` 阻塞接收**不会**挂死 —— `http.go:163-167` 的 defer 以非阻塞 `select` 保证总会发送一个值（cap 为 1，见 `http.go:141`）。
- `engine/internal/proxy/config.go` 仅为纯校验/规范化，无 goroutine 与资源获取，无可审的错误路径。

---

## 分片 D · `[dns-server]` — engine/internal/{dns,server,runtime,expiry,fileintegrity,diagnostic,protocol,api}

## P0 — 数据损坏 / 状态不一致 / 静默失效

### [dns-server] P0-1 超时类失败被 `ctx.Err()` 完全吞掉，DNS 遥测与 fallback 事件同时失真

**位置**
- `engine/internal/dns/resolver.go:282` — ``if ctx.Err() != nil {`` / `:283` — ``return Result{}, 0, ctx.Err()``（DoH 分支）
- `engine/internal/dns/resolver.go:425` — ``if ctx.Err() != nil {`` / `:426` — ``return Result{}, 0, ctx.Err()``（legacy 分支）

**触发条件**
上游 DoH 解析器"静默丢包"或网络变慢。DoH 批次的 `batchCtx`（`:331` `context.WithTimeout(ctx, batchBudget)`）超时，此时父 `ctx` 也刚好过期。代码自己的注释 `engine/internal/dns/resolver.go:269-270` 写明 `// Auto must leave a real deadline budget for source-bound traditional DNS when HTTPS resolvers silently drop packets`——也就是说**超时恰恰是作者预期的主要 DoH 失败模式**。legacy 分支同理：`udpCtx` 超时后继续 TCP 重试，`ctx` 也到期。

**后果**
1. `r.dohFailures` 不增长，`r.recordStrictFailure(binding, err)` 永远不被调用 → 严格策略（`PolicyGoogle`/`PolicyAliDNS`/`PolicyDNSPod`）下 `EventDNSFallbackRequired` **在超时这一最常见故障上永远不触发**（阈值 `FailureThreshold=3`，见 `engine/internal/api/v1/types.go:206-213`）。
2. `r.legacyFailures.Add(1)`（`:428`）被跳过，`errors.Join(failures...)`（`:429`）里逐个服务器的失败原因被丢弃 → `Resolver.Status()` 会报出 `legacy_failures: 0` 而实际上传统 DNS 每一个服务器都失败了。
3. 排障时 `engine/internal/server/server.go:552-559` 只能给出 `dns_failed / "DNS resolution failed"`，详情是 `context deadline exceeded`，运维无法区分"服务器慢"和"服务器不存在"。

**为什么没兜住**
这两处 `ctx.Err()` 检查的意图是"上游已经取消就别再累加失败"，但它把**失败本身**和**失败计数**一起扔了。取消与失败是两件事：请求可以因为父 ctx 取消而中止，而在这之前已经观察到的 `err` 依然是一条真实的失败证据，应该记账。代码没有区分"因为取消而没有新错误"和"有错误但取消恰好同时发生"。

**建议改法**
两处都改成先记账再返回，并且把已收集的失败一起返回：

```go
// engine/internal/dns/resolver.go:282
		// record the failure BEFORE consulting ctx.Err(): a timeout is still a
		// failure and must reach the fallback / status accounting paths.
		r.dohFailures.Add(1)
		r.recordStrictFailure(binding, err)
		if ctx.Err() != nil {
			return Result{}, 0, errors.Join(ctx.Err(), err)
		}
```
```go
// engine/internal/dns/resolver.go:425
	if len(failures) > 0 {
		r.legacyFailures.Add(1)
		failure := errors.Join(failures...)
		if ctx.Err() != nil {
			return Result{}, 0, errors.Join(ctx.Err(), failure)
		}
		return Result{}, 0, failure
	}
	if ctx.Err() != nil {
		return Result{}, 0, ctx.Err()
	}
```
`errors.Join` 保证"取消原因"和"实际失败"在错误链里同时可见，`errors.Is(err, context.DeadlineExceeded)` 仍然成立，调用方 `proxy/dial.go:86` 的行为不变。

---

### [dns-server] P0-2 RCODE 被压平成无类型字符串，NXDOMAIN / SERVFAIL 与自身故障不可区分，并连带污染 adapter 健康

**位置**
- `engine/internal/dns/wire.go:112` — ``if code := flags & 0x000f; code != 0 {`` / `:113` — ``return wireAnswer{}, fmt.Errorf("DNS response code %d", code)``
- 消费者：`engine/internal/dns/resolver.go:412` — ``failures = append(failures, fmt.Errorf("udp/%s: %w", server, err))``、`:420` — ``failures = append(failures, fmt.Errorf("tcp/%s: %w", server, err))``
- 二次受害者：`engine/internal/proxy/dial.go:86-93`、`engine/internal/proxy/health.go:15`

**触发条件**
查询一个真实不存在的域名（NXDOMAIN，RCODE=3），或上游返回 SERVFAIL（RCODE=2）。

**后果**（链条已逐环读过代码）
1. `parseResponse` 把它当"传输失败"，`resolveLegacy` 立刻对**同一个服务器做 TCP 重试**（`:414-420`）——一个权威的否定答案白白多花一次往返。
2. 失败后 `resolveLegacy` 继续尝试**列表里剩下的每一个服务器**（`for _, server := range servers` + `recordLegacySuccess/Failure`），于是 NXDOMAIN 的代价是 `len(servers) × 2` 次查询，并吃掉整个 `QueryTimeout`（默认 4s）。**fallback 预算被非失败消耗光**，真正需要 fallback 的场景反而没预算了。
3. `proxy/dial.go:86-93` 把这个 error 记成**比较失败**：
   ```go
   				if resolveErr != nil {
   					failures = append(
   						failures,
   						fmt.Errorf("%s resolve %s: %w", adapter.Name, host, resolveErr),
   					)
   					comparativeFailures = append(comparativeFailures, adapter.Name)
   					continue
   				}
   ```
4. `engine/internal/proxy/health.go:15` — ``domainFailureThreshold = 2``，`:17` — ``domainQuarantineTTL = 30 * time.Minute``，`health.go:220-230` 达到阈值就隔离该 adapter 上的该域名 30 分钟。一个**语义正确**的 NXDOMAIN 每次都一模一样地重复，**两次就会触发隔离**（前提：≥2 个 adapter，第二个成功才会被 `dial.go:130-135` 记为比较失败；单 adapter 池不会记录）。

**为什么没兜住**
`parseResponse` 是唯一能区分"域名不存在"和"链路坏了"的地方，却把 RCODE 直接 `fmt.Errorf` 成数字，丢掉了唯一的信息。`resolver.go` 里已经有类型化的 `truncatedResponseError`（`wire.go:30-32`），但 grep 全仓确认**没有任何地方 `errors.Is/As` 它**，说明缺少错误分类是被忽略的设计而非疏漏的偶然。

**建议改法**
定义返回码错误类型，并在 `resolveLegacy` 里短路：

```go
// engine/internal/dns/wire.go
type dnsStatusError struct{ Code uint8 }

func (e *dnsStatusError) Error() string {
	switch e.Code {
	case 1:  return "DNS server failure (SERVFAIL)"
	case 3:  return "DNS name does not exist (NXDOMAIN)"
	case 5:  return "DNS server refused the query (REFUSED)"
	default: return fmt.Sprintf("DNS response code %d", e.Code)
	}
}

// engine/internal/dns/wire.go:113
	return wireAnswer{}, &dnsStatusError{Code: code}
```
```go
// engine/internal/dns/resolver.go，紧接 412 行的 append 之后
		var status *dnsStatusError
		if errors.As(err, &status) {
			// An authoritative negative answer is a SUCCESS for this server:
			// do not retry over TCP, do not try the remaining servers, and do
			// not charge the query against the fallback budget.
			return Result{}, 0, err   // caller distinguishes terminal vs retryable
		}
```
同时 `proxy/dial.go:86` 应改为 `if !errors.Is(resolveErr, errTerminalDNS) { comparativeFailures = append(...) }`，让语义失败不进入 adapter 健康惩罚。

---

### [dns-server] P0-3 WFP DNS 豁免替换失败后没有回滚，运行时状态与代理实际用的 adapter 池永久错位

**位置** `engine/internal/server/scheduling.go:44-58`

```go
	previous := s.dnsExemption
	var exemption *wfp.DNSExemption
	var err error
	if enabled {
		exemption, err = wfp.OpenDNSExemption("", s.adapters)
		if err != nil {
			return err
		}
		s.dnsExemption = exemption
	}
	if previous != nil {
		if closeErr := previous.Close(); closeErr != nil {
			return closeErr
		}
	}
	s.adapters = next
	if s.proxy != nil {
		if err := s.proxy.UpdateScheduling(next); err != nil {
			return err
		}
	}
```

**触发条件**
`wfp.OpenDNSExemption` 成功 → `previous.Close()` 成功（**旧豁免已销毁**）→ `s.proxy.UpdateScheduling(next)` 返回错误（代理内部 adapter 池重建失败，例如 TUN 配置写入被占用）。

**后果**
- 机器上生效的 WFP 豁免规则覆盖的是 **新 adapter 集合** `next`（因为 `OpenDNSExemption("", s.adapters)` 用的是 `next` 参数… 注意这里传的是 `s.adapters`，即**旧**集合——更糟：豁免覆盖旧集合，而 `s.dnsExemption` 已指向新句柄）。
- 代理仍在用**旧** adapter 池（`UpdateScheduling` 失败，`s.adapters` 未更新）。
- 净效果：WFP 规则与代理实际流量路径不匹配 → DNS 流量被放行/阻断的方向错乱，且没有任何日志、没有补偿动作。`s.dnsExemption` 与内核里的实际状态从此不一致，只能重启进程修复。

**为什么没兜住**
这是一个典型的"先破坏后验证"顺序：破坏性操作（`previous.Close()`）发生在可能被失败的 `UpdateScheduling` 之前，且没有任何 `defer`/compensating path。对照 `engine/internal/wfp/dns_exemption_windows.go:196-200` 内部是有正确的 `defer func(){ if !success { _ = owned.Close() } }()` 模式的，说明这套回滚写法在本仓库是既有惯例，这里只是没用。

**建议改法**
改成两阶段：先建新句柄，**成功后再拆旧的**，失败则释放新句柄并保持旧状态。

```go
func (s *Server) applyScheduling(next []string, enabled bool) error {
	previous := s.dnsExemption
	if !enabled {
		if previous != nil {
			if err := previous.Close(); err != nil {
				return err
			}
			s.dnsExemption = nil
			s.adapters = next
		}
		if s.proxy != nil {
			if err := s.proxy.UpdateScheduling(next); err != nil {
				return err // exemption already nil -> nothing to roll back
			}
		}
		return nil
	}

	exemption, err := wfp.OpenDNSExemption("", next)
	if err != nil {
		return err
	}
	// Nothing below is allowed to leave the kernel holding a rule that does
	// not match the proxy's adapter pool.
	committed := false
	defer func() {
		if !committed {
			_ = exemption.Close()
		}
	}()
	if s.proxy != nil {
		if err := s.proxy.UpdateScheduling(next); err != nil {
			return err
		}
	}
	if previous != nil {
		if err := previous.Close(); err != nil {
			return err
		}
	}
	s.dnsExemption = exemption
	s.adapters = next
	committed = true
	return nil
}
```

---

### [dns-server] P0-4 DoH 竞速批次在 `ctx.Done()` 分支丢弃已经成功的结果，把成功变成失败

**位置** `engine/internal/dns/resolver.go:386-388`

```go
		case <-ctx.Done():
			return Result{}, 0, ctx.Err()
		}
```

**触发条件**
`raceDoHBatch` 用 `len(endpoints)` 个 goroutine 并发查询，`outcomes` 是**带缓冲**的（`make(chan outcome, len(endpoints))`，`:363`）。当 `batchCtx`（`:331`，deadline = `QueryTimeout/2`）到期的瞬间，某个 endpoint 的成功 outcome 刚好写进缓冲区——此时 `select` 的两个 case 同时 ready，Go 随机选。

**后果**
一个**已经拿到的正确 DNS 答案被扔掉**，函数返回 `context.DeadlineExceeded`。上层 `resolveDoH:343` 把它记成该 endpoint 的失败，`resolveUncached:286` 归入 `dohFailures` 并触发 `recordStrictFailure`。在 `PolicyGoogle`/`PolicyAliDNS`/`PolicyDNSPod` 这类**无 legacy 回退**的严格策略下，用户直接拿到 DNS 解析失败。三次即可触发 `EventDNSFallbackRequired` 让 UI 弹出无谓的告警。这是一条纯自造的假故障。

**为什么没兜住**
`select` 里 `<-ctx.Done()` 被当成了"中止"，但 `outcomes` 是缓冲的，循环本来可以在 ctx 结束后再把已到达的结果 drain 一遍再做决定。这里缺少"先排空、再判定"这一步。

**建议改法**
让"上下文结束"只终止**等待新结果**，不终止已到达结果的处理：

```go
	// engine/internal/dns/resolver.go:378 起，把 for/select 改成：
	remaining := len(endpoints)
	for remaining > 0 {
		select {
		case o := <-outcomes:
			remaining--
			if o.err == nil {
				successes = append(successes, o)
				continue
			}
			failures = append(failures, o.err)
		case <-ctx.Done():
			// ctx 已结束，但缓冲 channel 里可能已有成功的答案：
			// 先非阻塞排空，优先返回成功，否则才报错。
			for {
				select {
				case o := <-outcomes:
					remaining--
					if o.err == nil {
						successes = append(successes, o)
						continue
					}
					failures = append(failures, o.err)
				default:
					if len(successes) > 0 {
						sort.SliceStable(successes, func(i, j int) bool {
							return successes[i].ttl > successes[j].ttl
						})
						return successes[0].result, successes[0].ttl, nil
					}
					return Result{}, 0, errors.Join(append(failures, ctx.Err())...)
				}
			}
		}
	}
```

---

## P1 — 错误难排查

### [dns-server] P1-1 `SetDeadline` 失败被丢弃，`runLookup` 可能永久挂起并毒化 inflight 缓存键

**位置** `engine/internal/dns/resolver.go:642` — ``_ = connection.SetDeadline(deadline)``（函数体 `:640-644`）

**触发条件**
`setContextDeadline` 是所有传统 DNS socket 读写的**唯一超时边界**。若 `connection.SetDeadline(deadline)` 返回错误（conn 已被关闭，或底层 `net.Conn` 实现不支持 deadline），错误被丢弃，随后 `:461` 的 `connection.Read(buf)` **没有任何超时**。

**后果**（已读 `resolver.go:192-258` 确认）
`runLookup`（`:192` ``go r.runLookup(key, binding, call)``）会永久阻塞在 `:233`，永远走不到 `:248-258` 的 `close(call.done)` / `delete(r.inflight, key)`。此后任何对该 `(domain, wireType, adapter)` 的 `Resolve` 都会在 `:187-198` 命中残留的 `call`、**不再发起新查询**，一直等到自己的 ctx 结束。该域名在该 adapter 上**进程生命周期内永久不可解析**，`Status().Inflight` 单调增长。

**为什么没兜住**
`_ =` 把一个"我们失去了唯一的超时控制"的事实变成了沉默。goroutine 也没有 `recover` 和兜底超时（见 P1-2）。

**建议改法**
```go
func setContextDeadline(connection net.Conn, ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set DNS socket deadline %s: %w", deadline, err)
	}
	return nil
}
```
两个调用点（`:456` 和 legacy 的 UDP/TCP 处）改为 `if err := setContextDeadline(connection, ctx); err != nil { return Result{}, 0, err }`，让失败进入正常的 per-server `failures` 路径而不是永久阻塞。

---

### [dns-server] P1-2 `runLookup` goroutine 无 `recover`，一个 panic 会静默杀死查询并留下永不解开的 inflight 条目

**位置** `engine/internal/dns/resolver.go:192` — ``go r.runLookup(key, binding, call)``

**触发条件**
`runLookup` 内部（`:203-262`）访问 `Result.Address`、做 map 写入、调用 `parseResponse`。当前代码里没有明显的空指针，但 `runLookup` 是**唯一持有 `r.mu` 写路径**的 goroutine，且 `cacheExpiry.Set` 的 `heap.Fix` 依赖不变量。

**后果**
panic 会终止整个进程（Go 默认行为），至少是"查询 goroutine 静默死亡"：如果将来在 `runLookup` 内加了 `recover`，`call.done` 永远不会 `close`，`r.inflight[key]` 残留——表现与 P1-1 完全一致：永久卡死该键，且没有任何日志。

**为什么没兜住**
`Resolver` 有 `logger *slog.Logger`（`engine/internal/dns/config.go:94`）可用，但 `runLookup` 和 `waitForResult`（`:199-206`）都没有 `defer recover()`。全仓的 TUN/协议侧 goroutine 都有保护（`engine/internal/server/server.go:107` — `defer func() { if r := recover(); ... }()`），DNS 层是例外。

**建议改法**
```go
	// engine/internal/dns/resolver.go:192
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.logger.Error("dns lookup panicked", "domain", domain, "panic", rec)
				// 必须解开 inflight，否则该键永久卡死（见 P1-1）
				r.mu.Lock()
				if r.inflight[key] == call {
					delete(r.inflight, key)
					call.err = fmt.Errorf("dns lookup panic: %v", rec)
					close(call.done)
				}
				r.mu.Unlock()
			}
		}()
		r.runLookup(key, binding, call)
	}()
```

---

### [dns-server] P1-3 DoH 非 200 响应体未排空，连接无法复用

**位置** `engine/internal/dns/resolver.go:559-560`

```go
	if response.StatusCode != http.StatusOK {
		return Result{}, 0, fmt.Errorf("DoH HTTP status %d", response.StatusCode)
	}
```
`defer response.Body.Close()` 在 `:558`。

**触发条件**
DoH 服务器返回 429 / 500 / 403（限流、被墙、云厂商错误页）。这是故障期最常见的响应。

**后果**
`Close()` 在 body 未读完时调用，Go 的 `persistConn` 只能**销毁连接**而不能放回空闲池。故障期恰好是并发查询最多的时刻，`http.Transport` 配置的 `MaxConnsPerHost: 8`（`engine/internal/dns/config.go:20-25`）会被不断新建/销毁的连接耗尽，后续查询排队超时——把"上游 500"放大成"本地全面超时"。

**为什么没兜住**
`defer response.Body.Close()` 只保证不泄漏 fd，不保证连接可复用。错误路径需要显式 drain。

**建议改法**
```go
	if response.StatusCode != http.StatusOK {
		// drain a bounded amount so keep-alive connections return to the pool
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return Result{}, 0, fmt.Errorf("DoH HTTP status %d from %s", response.StatusCode, server.Name)
	}
```

---

### [dns-server] P1-4 主机关闭指令无条件回报成功，停机失败被静默吞掉

**位置** `engine/internal/server/server.go:296` — ``_ = s.stopProxy(request.ID)``，随后返回 `Accepted: true`

**触发条件**
UI 或 sidecar 发送 `api.MethodHostShutdown`，此时 TUN 关闭 / WFP 句柄释放 / sidecar 停止任一环节失败。

**后果**
`stopProxy` 内部会把失败写进事件流，但**协议层的应答仍然是成功**。调用方据此认为可以安全退出；随后 `server.go:97` 的 `defer s.stopProxyForHostExit()` 再做一次 best-effort 清理（同样全程 `_ =` 丢弃，见 P2-6），如果这次也失败，进程带着未释放的 TUN 设备和 WFP 引擎句柄退出，可能留下需要重启系统才能清理的残留。

**为什么没兜住**
这里把"发起停止"和"停止成功"合并成了一个 `Accepted: true`。协议层已经支持携带 error（`Failure` 带 `Details`），却没用。

**建议改法**
```go
	case api.MethodHostShutdown:
		if err := s.stopProxy(request.ID); err != nil {
			return protocol.Result(request.ID, proxy.Status{
				State: string(StateFailed),
				Detail: map[string]any{"message": err.Error()},
			}), true
		}
		return protocol.Result(request.ID, proxy.Status{
			State:  string(StateStopped),
			Detail: map[string]any{"message": "proxy stopped"},
		}), true
```

---

### [dns-server] P1-5 诊断探针把一次瞬时 bind 失败固化成"不可用"，健康链路被误报

**位置** `engine/internal/diagnostic/diagnostic.go:170` — ``case base.LossRate >= 100 || bindError:``，配套 `engine/internal/diagnostic/diagnostic_windows.go:82` — ``bindError: code == errorNetworkUnreachable || code == errorInvalidNetname,``

**触发条件**
10 个探针里**任意一个**撞上瞬时 WinError 1231（`ERROR_NETWORK_UNREACHABLE`）或 1214（`ERROR_NETNAME_DELETED`）——例如 TUN 重建/网卡抖动的那一瞬间。

**后果**
`bindError` 是**跨探针粘性的**（一旦置位就不再清零），`summarize` 的 switch 第一个 case 就命中，最终 `Status = "unavailable"`，`diagnostic.go:177-179` 还会把 Note 覆盖成 ``source bind failed (WinError 1231)``——**即使 LossRate = 0%、其余 9 个探针全部成功**。`engine/internal/server/server.go:247-248` 会把这个"不可用"原样返回给 UI。用户会照着去修一个根本没坏的链路。

**为什么没兜住**
`bindError` 被当成"本轮整体失败"而不是"某次探针的观察"。`LossRate >= 100 ||` 里的 `||` 让一个瞬时标志压过了聚合统计。

**建议改法**
只把 bind 失败当作**证据**而非结论——让聚合统计裁决，bindError 仅在有足够样本时升级：
```go
	// engine/internal/diagnostic/diagnostic.go:170
	switch {
	case base.LossRate >= 100 && bindErrorCount >= base.Sent/2:
		base.Status = "unavailable"
		if bindErrorCode != 0 {
			base.Note = fmt.Sprintf("source bind failed (WinError %d)", bindErrorCode)
		}
	case base.LossRate >= 100:
		base.Status = "unavailable"
	...
```
即：把 `bindError bool` 换成 `bindErrorCount int`，且只有在过半探针都 bind 失败时才下"不可用"的结论；单次瞬时只作为 Note 附加信息。

---

### [dns-server] P1-6 ICMP 回显的完整状态码被整个丢弃，`Probe` 无法区分"超时"和"不可达"

**位置** `engine/internal/diagnostic/diagnostic_windows.go:89-92`

```go
	prefix := (*echoReplyPrefix)(unsafe.Pointer(&reply[0]))
	if prefix.Status != ipSuccess {
		return probeResult{}
	}
```

**触发条件**
`IcmpSendEcho2` 成功返回（err == nil）但回显报文的 `Status != 0`，例如 `IP_TTL_EXCEEDED` / `IP_REQ_TIMED_OUT` / `IP_DEST_HOST_UNREACH`(0x1102) / `IP_DEST_NET_UNREACH`(0x1101)。

**后果**
返回**全零** `probeResult{}`。`diagnostic.go:118-123` 只看到 `err == nil && rtts == nil && !entry.hasDestination`，把它算作 **"已探测、无回显"**——和"目标确实不可达"完全一样。诊断面板永远无法告诉用户"你的 ICMP 被中间设备 drop 了"，只能显示"发出去没回来"。这正是最需要诊断工具区分的场景。

**为什么没兜住**
`echoReplyPrefix.Status` 已经取出来了，但代码选择把它扔掉而不是转成错误。函数返回类型 `probeResult` 有 `errorCode` 字段（`:85` 已在用），完全有能力承载。

**建议改法**
```go
	prefix := (*echoReplyPrefix)(unsafe.Pointer(&reply[0]))
	if prefix.Status != ipSuccess {
		return probeResult{
			errorCode: uint32(prefix.Status),
			note:      fmt.Sprintf("ICMP status %d (ttl=%d)", prefix.Status, prefix.Ttl),
		}
	}
```
并在 `diagnostic.go` 侧把 `errorCode != 0 && errorCode < 0x10000` 映射成 `IP_REQ_TIMED_OUT` / `IP_TTL_EXCEEDED` / `IP_DEST_*_UNREACH` 的可读文案，并让 `summarize` 的 `case` 区分"我们发了但没回"与"网络不通"。

---

### [dns-server] P1-7 ICMP 探针的 WinError 码在非 bind 失败场景被静默丢弃

**位置** `engine/internal/diagnostic/diagnostic_windows.go:81-87`

```go
	if count == 0 {
		code := winErrorCode(callErr)
		return probeResult{
			bindError: code == errorNetworkUnreachable || code == errorInvalidNetname,
			errorCode: code,
		}
	}
```

**触发条件**
`IcmpSendEcho2` 返回 `count == 0` 且 WinError 既不是 1231 也不是 1214——例如 `IP_REQ_TIMED_OUT`(10013)、`ERROR_ACCESS_DENIED`(5)（未以管理员运行 / WFP 规则拦截 ICMP）、`ERROR_INVALID_PARAMETER`(87)。

**后果**
`errorCode` 虽然被填了，但 `diagnostic.go:118-126` 的 `probeError` 分支只在 `entry.bindError` 为真时才 `note = probe.note`；`bindError` 为假时 `note` 是空串，最终 `Result.Probe.Note` 为空。**"没有管理员权限"这种最有诊断价值的信息被吞掉了**，用户只看到一串丢包率。

**为什么没兜住**
`probeError` 把 `errorCode` 记录进 `ProbeResult.ErrorCode` 就以为完事了，但聚合层 (`diagnostic.go:112-128`) 没有把非 bind 的错误码渲染成任何文字。

**建议改法**
在 `diagnostic.go` 的聚合循环里给所有 `errorCode != 0` 的条目生成 note，而不是只给 bindError：
```go
		if probe.ErrorCode != 0 && probe.note == "" {
			probe.note = fmt.Sprintf("WinError %d", probe.ErrorCode)
		}
		if probe.note != "" {
			note = append(note, probe.note)
		}
```

---

### [dns-server] P1-8 `parseResponse` 丢弃 `skipName` 的底层错误，畸形报文与截断不可区分

**位置** `engine/internal/dns/wire.go:122-124` 和 `:131-133`

```go
		offset, err = skipName(packet, offset)
		if err != nil || offset+4 > len(packet) {
			return wireAnswer{}, fmt.Errorf("malformed DNS question")
		}
```
```go
		if err != nil || offset+10 > len(packet) {
			return wireAnswer{}, fmt.Errorf("malformed DNS answer")
		}
```

**触发条件**
收到长度不足（被中间设备截断/伪造）或压缩指针成环的 DNS 报文。`skipName` 返回的具体错误——`"compression pointer loop"`, `"name exceeds packet"` 等——被 `||` 短路掉。

**后果**
排障时只知道"报文畸形"，不知道是被截断、指针成环还是解析器自己越界。这对判定"中间设备在篡改 DNS"很关键。另外 `:111-112` 已经能检测 TC 位并返回类型化的 `truncatedResponseError`，但因为 `:122` 这里丢掉了 `err`，两种情况在下游合并成一句。

**建议改用 `%w`**
```go
		offset, err = skipName(packet, offset)
		if err != nil {
			return wireAnswer{}, fmt.Errorf("malformed DNS question name at offset %d: %w", offset, err)
		}
		if offset+4 > len(packet) {
			return wireAnswer{}, fmt.Errorf("malformed DNS question: need 4 bytes at offset %d, have %d", offset, len(packet)-offset)
		}
```

---

### [dns-server] P1-9 NODATA 与解析失败被合并成同一条错误

**位置** `engine/internal/dns/wire.go:171-173`

```go
	if best.Address == "" {
		return wireAnswer{}, fmt.Errorf("DNS response has no requested address record")
	}
```

**触发条件**
NOERROR + `answerCount > 0` 但全是无关类型（例如 CNAME 链断在中途、只有 SOA 而无 A/AAAA 记录），或者 `isFakeIPv4`（`wire.go:152`，过滤 198.18.0.0/15 基准网段）把唯一一条 A 记录滤掉了。

**后果**
这类响应同样是**终态**（再查别的服务器也一样），却和 P0-2 里的 NXDOMAIN 一样被当成传输失败，在 `resolver.go:414-420` 触发 TCP 重试并继续遍历剩余服务器，吃掉整个 `QueryTimeout`。同时它和真正的解析 bug 在错误文本上无法区分。

**为什么没兜住**
和 P0-2 同源：终态语义（NODATA）与瞬态语义（超时/丢包）没有类型区分。

**建议改法**
和 P0-2 一并引入终态错误类型：
```go
	if best.Address == "" {
		return wireAnswer{}, &dnsStatusError{Code: 0, Terminal: true, Detail: "no A/AAAA record in answer"}
	}
```
并在 `resolveLegacy` 的失败分支用 `errors.As` 统一短路。

---

### [dns-server] P1-10 文件完整性校验的错误不带路径和摘要，无法定位是哪个文件

**位置** `engine/internal/fileintegrity/fileintegrity.go:50-51`

```go
	if subtle.ConstantTimeCompare(actualBytes, expectedBytes) != 1 {
		return errors.New("SHA-256 digest mismatch")
	}
```

**触发条件**
`VerifySHA256` 的三个调用点——`engine/internal/tun/supervisor.go:681`（`verify trusted sing-box executable`）、`:687`（`verify requested sing-box config`）、`engine/internal/tun/config_stage_windows.go:75`——收到摘要不匹配的二进制/配置。

**后果**（已逐个读过三个调用方，**确认它们都没有吞错**，都用 `%w` 正确包装并 return）
但最终落到用户面前的错误是 `verify trusted sing-box executable: SHA-256 digest mismatch`——**既不知道是哪个文件（路径被调用方的固定文案遮住了）、也不知道期望值和实际值分别是多少**。安全事件（可执行文件被篡改）复盘时拿不到取证信息。而且 `errors.New` 无格式化参数，`go vet` 也不会提醒。

**为什么没兜住**
`fileintegrity` 明明持有 `path` 和两个摘要，却没有把它们放进错误里。这是一个**信息丢失**而非吞错。

**建议改法**
```go
func VerifySHA256(path string, expected string) error {
	expectedBytes, err := hex.DecodeString(strings.TrimSpace(expected))
	if err != nil {
		return fmt.Errorf("invalid pinned SHA-256 digest for %s: %w", path, err)
	}
	if len(expectedBytes) != sha256.Size {
		return fmt.Errorf("invalid pinned SHA-256 digest for %s: want %d hex chars, got %d", path, sha256.Size*2, len(strings.TrimSpace(expected)))
	}
	actualBytes, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("compute SHA-256 of %s: %w", path, err)
	}
	if subtle.ConstantTimeCompare(actualBytes, expectedBytes) != 1 {
		return fmt.Errorf("SHA-256 digest mismatch for %s: want %x, got %x", path, expectedBytes, actualBytes)
	}
	return nil
}
```

---

### [dns-server] P1-11 `NormalizeConfig` 静默追加硬编码公共 DNS，运维显式配置的"仅用我的服务器"被覆盖

**位置** `engine/internal/dns/config.go:91-95`

```go
		if len(config.LegacyServers) == 0 {
			config.LegacyServers = append([]string(nil), defaultLegacyServers...)
		}
```
`defaultLegacyServers`（`:62-65`）为 `223.5.5.5:53`、`119.29.29.29:53`（阿里公共 DNS / DNSPod）。

**触发条件**
运维通过 `api.MethodDNSStart` 显式配置 `legacy_servers` 为自己的服务器（或出于隐私考虑希望 fallback 只走自建）。

**后果**
配置的 `legacy_servers` 被**保留**，但 `normalizeServerList` 会把它与硬编码公共 DNS 合并（`:135-138` 的 `config.LegacyServers` 组装）。结果是：传统 DNS fallback 会把域名泄露给两家公共服务商，而 UI 上显示的服务器列表与实际行为不符。**没有任何日志或警告**说明追加了什么。

**为什么没兜住**
"未配置"与"配置了"走了同一个空判断分支，追加行为对调用方完全不可见；`dns.New` 只在 `normalizeServerList` 出错时才返回错误（`config.go:124` 附近），追加本身不算错误。

**建议改法**
区分"未配置"和"显式配置"，且只在完全未配置时才用默认值，并把生效服务器列表通过日志暴露出来：
```go
	if len(config.LegacyServers) == 0 && len(config.DohServers) == 0 {
		config.LegacyServers = append([]string(nil), defaultLegacyServers...)
	} else if len(config.LegacyServers) == 0 {
		// 有 DoH 但没有 legacy：不要静默引入公共 DNS，
		// 否则 fallback 会把查询泄露给未获授权的第三方。
		config.LegacyServers = nil
	}
```
并在 `dns.New` 中 `slog.Warn("resolved DNS server set", "legacy", config.LegacyServers, "doh", dohNames)`。

---

## P2 — 代码整洁度

### [dns-server] P2-1 `normalizeRecordType` 的错误被丢弃

**位置** `engine/internal/dns/resolver.go:267` — ``_, wireType, _ := normalizeRecordType(recordType)``
上游是 `engine/internal/server/server.go:243` 校验过的 `dns.QueryType`（1/28/65/AAAA）。丢弃的错误在当前调用链上不可达，但契约上 `normalizeRecordType` 会返回 error（`dns/config.go` 侧），静默丢弃会让未来新增调用点时无声降级到 A 记录。建议：``wireType, err := normalizeRecordType(recordType); if err != nil { return Result{}, 0, err }``。

### [dns-server] P2-2 `context.AfterFunc` 返回的 stop 函数被丢弃

**位置** `engine/internal/dns/resolver.go:138` — ``context.AfterFunc(root, resolver.closeDoHTransports)``
（对比 `engine/internal/dns/doh_transport.go:41-47` 里写对了的 `stop := context.AfterFunc(r.root, cancel)` + `defer stop()`）
`AfterFunc` 返回的 stop 函数会**解除注册**，保留它可以在 root 长期存活但 Resolver 已不再使用时避免回调。建议：``stopClose := context.AfterFunc(root, resolver.closeDoHTransports); _ = stopClose // 或存入 Resolver 供 Close 使用``。

### [dns-server] P2-3 `resolver.go:122` 用 `fmt.Errorf` 拼无格式串的常量

**位置** `engine/internal/dns/resolver.go:122` — ``return nil, fmt.Errorf("DNS dial function is required")``
无占位符应用 `errors.New`，`go vet` 才能检出后续误加参数的 bug。`engine/internal/dns/config.go` 里 `"dial function is required"` 同样问题。

### [dns-server] P2-4 DoH 连接池淘汰只关空闲连接，在途请求的连接被遗留

**位置** `engine/internal/dns/doh_transport.go:88` — ``r.dohTransports[oldest].transport.CloseIdleConnections()``
淘汰发生在 `len(r.dohTransports) >= maxDoHTransports`(64) 时，此时旧 transport 可能正有 in-flight DoH 请求；`CloseIdleConnections` 不影响它们，请求完成后由 transport 自己的空闲定时器回收。行为可接受但会在切换端点时短暂堆积连接。建议改为 `r.dohTransports[oldest].transport.CloseIdleConnections()` 后配合对淘汰项的 `Close`，或把缓存上限调低并在文档里说明取舍。

### [dns-server] P2-5 `wire.go:139` 的负长度检查是死代码

**位置** `engine/internal/dns/wire.go:139` — ``if length < 0 || offset+length > len(packet) {``
`length` 来自 `binary.BigEndian.Uint16(...)[0:2]`，恒为 `0..65535`，`< 0` 永不成立。同样 `int(length)` 在 32/64 位平台上不可能溢出。建议改为 `if int(length) > len(packet)-offset {`（既去掉死分支，又避免 `offset+length` 的潜在整数溢出）。

### [dns-server] P2-6 `wire.go` 的假 IP 过滤只作用于 A 记录，AAAA 不对称

**位置** `engine/internal/dns/wire.go:152-156`
```go
		case recordType == dnsTypeA && length == net.IPv4len:
			ip = net.IP(data)
			if isFakeIPv4(ip) {
				continue
			}
		case recordType == dnsTypeAAAA && length == net.IPv6len:
			ip = net.IP(data)
```
`isFakeIPv4` 过滤 198.18.0.0/15（基准测试网段），但 AAAA 分支**没有对应对应处理**（2001:2::/48 benchmark / 2001:10::/28 ORCHID 也应过滤）。同一次查询若同时拿到假 IPv4 和真 IPv4，会只保留真 IPv4；反之 AAAA 上的同类污染会被原样返回。建议给 AAAA 加 `isBenchmarkIPv6`。

### [dns-server] P2-7 `server.go` 的 JSON 解析错误被替换成固定文案

**位置** `engine/internal/server/server.go:183` — ``return protocol.Failure("", "invalid_json", "request is not valid JSON", nil), false`` 与 `:186` — ``return protocol.Failure(request.ID, "invalid_json", "request contains trailing JSON", nil), false``；同类还有 `:243-244`（DNS 诊断参数错误被换成 `"invalid request"`）
`decoder.Decode` 的真实错误（含字段名/字节偏移）被丢弃，客户端拿到零信息的 `"request is not valid JSON"`。建议把 `err.Error()` 放进 `Details`（见 P2-8 的 Details 用法）。

### [dns-server] P2-8 内部错误的原文被直接返回给远端客户端

**位置** `engine/internal/server/server.go:258-259` — ``fmt.Errorf("dns start failed: %w", err)`` 的 `err.Error()` 被塞进 `Details["message"]`；`:426` — ``"message": err.Error()``（`startProxy` 失败）；`:728` — ``"message": errors.Join(err, tunStopErr, wfpErr, proxyStopErr).Error()``
把内部错误原文直接透给 sidecar/UI 是调试友好，但错误里可能含文件路径、adapter 名称甚至上游地址。建议改为对外给稳定错误码 + 摘要，内部细节走 `slog.Error` 并附带 `request.ID` 便于关联。

### [dns-server] P2-9 `runtime.Transition` 的错误在 9 处调用点被 `_` 丢弃

**位置** `engine/internal/server/server.go:413` — ``_, _ = s.runtime.Transition(StateRunning, ...)``、`:421`（`StateFailed`）、`:463`、`:485`、`:500`、`:512`、`:722`、`:1001`、`:1038`
`engine/internal/runtime/runtime.go:58-83` 的状态机逻辑本身是对的（`:63-65` 同状态 no-op 返回 nil，`:66-68` 非法转换返回带原因的错误）。但调用方同时用 `s.currentState()` 写了状态文案，所以行为不会错。问题在于：`runtime.go:24` 的 `Snapshot()` 结构体**没有 error 字段**，状态机的拒绝原因既进不了状态也无法上报。建议给 `Snapshot` 加 `LastTransitionError string`，或至少在这些点加 `s.log.Warn("runtime transition rejected", ...)`。

### [dns-server] P2-10 `host.exit` 停机路径全链路 `_ =` 丢弃

**位置** `engine/internal/server/server.go:1016` — ``_ = s.tun.Stop(tunCtx)``、`:1019` — ``_ = s.closeDNSExemption()``、`:1030` — ``_ = s.proxy.Stop(ctx)``；同类还有 `:969`/`:977`（`emitEvent`）、`:993`（`proxy.Stop`）、`:1008-1009`（TUN 事件）
这是"关进程路径尽力而为"的合理取舍，但**零日志**意味着：残留 TUN 设备 / WFP 句柄泄漏后完全无从排查。建议至少 `slog.Warn("proxy teardown step failed", "step", "tun.stop", "error", err)`。

### [dns-server] P2-11 DoH 传输的端点 URL 非法时静默丢弃，不报错

**位置** `engine/internal/dns/doh_transport.go` `New` 中构造 `endpoints` 时，解析失败只被跳过；`engine/internal/dns/resolver.go:348` 最终 `errors.Join(failures...)` 在只有一个非法 endpoint 时返回一条 `nil` 元素参与的 join。`engine/internal/dns/config.go` 的 `normalizeDoHEndpoints` 只把整体置空，不指出是哪一个。建议 `New` 返回 `([]dnsDoHEndpoint, error)` 并把坏 URL 一并列出来。

### [dns-server] P2-12 `SteamCDNStatus` 在代理未运行时返回"成功但空配置"

**位置** `engine/internal/server/steam_cdn.go:20-22`
```go
	if s.proxy == nil {
		return protocol.Result(request.ID, proxy.SteamCDNStatus{Entries: []proxy.SteamCDNEntry{}})
	}
```
`configureSteamCDN` 在引擎未运行 / 代理尚未创建时返回 `Accepted` 的**空配置**成功，调用方无法区分"已清空 CDN 配置"和"根本没生效"。建议返回 `protocol.Failure(request.ID, "proxy_unavailable", "proxy is not running", nil)`。

---

## 明确检查过、**未发现**问题的点（避免重复审计）

- **`engine/internal/fileintegrity` 的失败路径没有被吞掉**。三个调用点 `engine/internal/tun/supervisor.go:681`、`:687`、`engine/internal/tun/config_stage_windows.go:75` 都用 `%w` 包装并立即 return，`defer file.Close()` 在 `:20` 正确注册。唯一问题是错误信息缺路径/摘要（P1-10）。
- **`engine/internal/wfp/dns_exemption_windows.go:196-200` 的打开/关闭回滚是正确写法**：`defer func() { if !success { _ = owned.Close() } }()`。`engine/internal/server/server.go:640-658`（`activateTun`）和 `:1043-1056`（`closeDNSExemption`）的组合也正确；`:705-730` 的多步回滚把 `tunStopErr`/`wfpErr`/`proxyStopErr` 全部 `errors.Join` 进来。问题只在 `engine/internal/server/scheduling.go`（P0-3）。
- **`engine/internal/expiry/index.go` 的堆不变量是自洽的**。`Set`/`Delete`/`First`/`PopExpired`（`:46-84`）的 `heap.Fix`/`heap.Remove` 用法正确，`Swap` 正确维护 `e.index`。我核对了 `engine/internal/dns/resolver.go` 中全部 6 处 `cacheExpiry` 访问（`:178`、`:252`、`:625`、`:627-632` 以及 `Status()` 内 `:211` 的间接调用）**全部在 `r.mu` 之内**。包头注释 `engine/internal/expiry/index.go:1` 的 `// Callers provide locking.` 是有效契约，无并发缺陷。仅有的隐患是该类型以值形式（内含 slice）被复制共享，建议加注释或改为指针语义（P2 级，属 API 设计）。
- **`engine/internal/runtime/runtime.go` 状态机本身没有死锁**。`Transition` 在 `:60` 持锁后立即完成状态变更再 `Unlock()`（`:83`），没有在持锁时回调外部。`engine/internal/server/server.go:587` 的 `lifecycleMu` 与 `s.mu` 存在固定获取顺序，未发现反向路径。TUN 的 `handleTunUnexpectedExit` 由 `engine/internal/tun/supervisor.go:657` 的 `onLog` 回调**异步**触发（且 `waitProcess` 跑在 `go s.waitProcess(run)` 上，`supervisor.go:274`），不会与 `activateTun` 持锁期死锁。
- **`engine/internal/protocol/protocol.go` 无实质问题**。`:20-30` 的 `Response` 对 `Result`/`Error` 都用 `omitempty`，但所有 handler 都返回非 nil 的 payload；`:44-53` 的 `Failure` 始终填 `Message`。唯一可提的是 `:41-43` 的 `Notification` 先 `s.eventSeq++`（`engine/internal/server/server.go:1071`）再 `Encode`，编码失败会白白消耗一个序号，造成客户端侧序号空洞。
- **`engine/internal/api/v1/types.go` 的 `DNSStartConfig.ResolverConfig()`（`:206-213`）** 没有暴露 `MaxCacheEntries` 和 `FailureThreshold`，两者永远取默认值（`engine/internal/dns/config.go:41-42`，`FailureThreshold=3`）。这意味着 P0-1 描述的 fallback 通知阈值**无法由运维调整**，加剧了 P0-1 的可观测性损失。建议至少把 `FailureThresholdMS`→次数 暴露出来。
- **`engine/internal/diagnostic` 未发现缓存/ticker 泄漏**。`Diagnostic.Run`（`engine/internal/diagnostic/diagnostic.go:42-131`）每次调用新建 `icmp.Probe`，`close` 在 `:50` 注册，Windows 实现 `diagnostic_windows.go:47` 用 `defer p.Close()`，`closeDoHTransports` 同类模式在 DNS 层也正确。
