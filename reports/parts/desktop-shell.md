# 桌面外壳与平台层 · 错误处理审计

## 桌面外壳层概览

整体健康度**中等偏下**：这一层的错误处理模式高度不统一——`engineclient` 的启动/握手路径写得相当细致（有 `coreTerminateWait`、`sessionAuthTimeout`、`sameFile` 校验、PID 身份校验、`sync.Once` 幂等关闭），但这些细致几乎全部止步于"把错误算出来"，**在最后一步把算出来的错误丢掉**：`_ = process.Kill()`、`_ = session.process.Kill()`、`default:` 丢弃事件、`if err != nil { return nil }` 覆盖安全检查，在本层反复出现。最集中的问题区域是 **`desktop/internal/engineclient/` 的失败清理路径**（提权核心启动失败、会话被丢弃、传输读循环退出），因为这些路径恰恰是"已经修改了系统状态（拉起了管理员进程 / 建立了命名管道）之后失败"，却全部用 `_ =` 丢弃回滚结果，**孤儿提权进程和泄漏的管道句柄是最现实的后果**。其次是 **`desktop/internal/platform/` 的"探测型"函数**（`WebView2Available`、托盘定位、DWM 属性、`SetAutostart`），它们把"打不开 / 读不到"一律当成"不存在"，把权限问题、策略问题、竞态问题统统伪装成一条误导性的用户可见结论。最后是 `startup/wifi_windows.go` 的 `connect()`：它先发出 `WlanConnect` 请求、**之后**才做策略诊断，并在诊断命中时返回一条与真实 API 结果无关的错误，把真实失败原因覆盖掉。

- 审查范围：`desktop/main.go`、`desktop/cmd/`、`desktop/internal/engineclient/`、`desktop/internal/startup/`、`desktop/internal/platform/`、`desktop/internal/releaseversion/` 的**非测试** `.go` 文件。
- 为确认调用方语义，另行读取了（未审计，仅作依据）：`desktop/internal/services/engine.go:262-316, 393-414`、`desktop/internal/platform/wails/desktop.go`（在范围内，作为 D-11/D-23 的证据）。
- **发现总数：27 条** —— P0 × 5、P1 × 10、P2 × 12。

---

# P0 —— 用户数据损坏 / 系统状态不一致 / 静默失效

## D-01 引擎事件通道打满时静默丢弃事件，导致 TUN 失败与 DNS 回退被静默吞没

**位置**

`desktop/internal/engineclient/client.go:492-498`

```go
		select {
		case c.events <- Event{
			Name: message.Event, Sequence: message.Sequence,
			Data: append(json.RawMessage(nil), message.Data...),
		}:
		default:
		}
```

**触发条件**

`events` 通道在 `client.go:127` 以 `make(chan Event, 64)` 定容。引擎在启动/切模式/连接风暴时会连续推送大量 `log.record`（引擎每条连接日志一条，见 `desktop/internal/services/engine.go:290-294`），消费者 `EngineService.consumeCoreEvents`（同文件 `268-297`）是单 goroutine 串行处理。缓冲区一旦填满，`default:` 分支把事件**无声丢弃**——没有计数、没有日志、没有指标。

**后果**

被丢弃的包括 `tun.state_changed`（`engine.go:284-289`：`failed` + WFP 兼容错误时触发 `handleWFPCompatibility` 自动重启修复）和 `dns.fallback_required`（`engine.go:279-283`：触发引擎重启）。结果是：**虚拟网卡已经进入 `failed` 状态，但桌面端永远不知道**，不会触发兼容重启，用户看到的是一个"看起来在运行、实际不通"隧道，且没有任何错误提示。同时序列号 `Sequence` 出现空洞，事件流也不再可靠。

**为什么当前的错误处理没兜住**

`default:` 是最彻底的吞错形态——它连"发生过丢弃"这一事实都没有记录。整个 `readLoop` 没有任何"事件丢弃"的可观测点；`LaunchReport`（`client.go:79-82`）只记录启动阶段的事件，与运行期无关。

**建议改法**

不要静默丢弃，改为"有界阻塞 + 可观测丢弃"。最小改动：

```go
// client.go:127 扩大缓冲，并记录丢弃
events: make(chan Event, 256),

// client.go:492-498 替换 default 分支
select {
case c.events <- Event{...}:
default:
    c.recordDroppedEvent(message.Event)   // 原子计数 + 最近一次丢弃的日志
}
```

并把 `tun.state_changed` / `dns.fallback_required` 走一条**永不阻塞**的独立高优先通道（容量 4，`default` 只丢 `log.record`），保证控制面事件不被日志洪峰挤掉：

```go
if isCriticalEvent(message.Event) {
    select { case c.criticalEvents <- ev: default: c.recordDroppedEvent(...) }
    continue
}
```

---

## D-02 NAT 类型检测的安全前置检查被静默绕过：`Snapshot()` 的错误被当成"引擎已停止"

**位置**

`desktop/main.go:166-170`

```go
		func() error {
			snapshot, err := engineService.Snapshot()
			if err != nil {
				return nil
			}
```

**触发条件**

`engineService.Snapshot()` 会真实失败。读 `desktop/internal/services/engine.go:393-410`（范围外，为确认依据）：它在没有过渡态时调用 `s.client.Ensure(ctx)`（8 秒超时，见 `engine.go:401-406`）并 `Request(ctx, "engine.status", ...)`（`engine.go:408-410`）。只要提权弹窗被取消、核心进程崩溃、管道断开、握手超时（`client.go:196-201` 的 startGate 排队超时），这里就返回非 nil error。而这恰恰是**引擎最可能仍在代理流量**的时刻——上一次的核心进程还活着，只是对 `Ensure` 无响应。

**后果**

前置检查的作用是拦截"聚合运行时进行 NAT 检测"（`main.go:173`）。`Snapshot()` 一失败就 `return nil` = 放行。用户会在引擎实际仍在代理的状态下启动 NAT 检测，检测流量被代理转发，得到的 NAT 类型是错的；更糟的是检测过程会临时改防火墙/端口状态，与引擎持有的 WFP 状态互相干扰。整个过程**没有任何提示**，UI 会显示一个错误的检测结果。

**为什么当前的错误处理没兜住**

这是一个**失败开放（fail-open）的安全守卫**：`err != nil` 与"引擎已停止"被合并成同一个结论。它和 `client.go:501-503` 那种"继续"的吞错不同——这里直接把错误解释成了相反的语义。

**建议改法**

失败时必须拒绝，并区分两种失败：

```go
func() error {
    snapshot, err := engineService.Snapshot()
    if err != nil {
        // 无法确认引擎状态 = 不能证明它没在跑，fail-closed
        return fmt.Errorf("无法确认聚合引擎状态，已取消 NAT 类型检测：%w", err)
    }
    switch snapshot.Phase {
    case "starting", "running", "degraded", "stopping":
        return fmt.Errorf("请先停止聚合再进行 NAT 类型检测；聚合运行时无法保证检测流量直连所选物理网卡")
    default:
        return nil
    }
}
```

若产品上确实要"引擎未启动时也能检测"，需要改用一个**只读、永不启动核心**的状态查询（例如直接读 `EngineService` 已缓存的 snapshot，而非触发 `Ensure`），而不是吞掉错误。

---

## D-03 提权核心启动失败后，`Kill()` 与 `waitTimeout()` 的错误被丢弃 → 孤儿管理员进程

**位置**

`desktop/internal/engineclient/privileged_windows.go:113-122`

```go
	connection, err := pipe.accept(ctx, process.pid)
	if err != nil {
		// The desktop cannot terminate an elevated process (ACCESS_DENIED) and
		// a wedged core may never exit on its own. Never wait indefinitely
		// here: the startGate slot would stay occupied and every future Ensure
		// call would time out until the desktop restarts.
		_ = process.Kill()
		_ = process.waitTimeout(coreTerminateWait)
		return nil, fmt.Errorf("验证高权限核心通信失败：%w", err)
	}
```

**触发条件**

`privilegedLauncher.Launch` 已经通过 `ShellExecuteExW` + `runas`（`privileged_windows.go:312-355`）拉起了一个**真正的管理员权限**引擎进程，随后管道等待/身份校验/一次性令牌认证失败（`accept` 的任一分支：`connectAuthenticatedPipeServer:200-241`、`clientPID` 不匹配 `184-186`、`authenticateCore:243-301`）。

**后果**

`windowsCoreProcess.Kill()`（`privileged_windows.go:390-404`）对管理员进程**必然返回 `ERROR_ACCESS_DENIED`** —— 注释 `397-400` 自己就写明了这一点。因此第 119 行在提权路径上是一个必然失败的调用，而它的错误被 `_ =` 丢掉。结果：用户取消 UAC 之后又点了几次"启动"、或令牌不匹配，都会留下一个**管理员权限的孤儿引擎进程**在后台运行。这个进程持有 TUN 虚拟网卡和 WFP 状态，桌面端却认为"什么都没发生"，只会向用户显示"验证高权限核心通信失败"。多个孤儿叠加后互相争抢网卡/代理，是最难排查的一类故障。

**为什么当前的错误处理没兜住**

清理失败是**已知的、代码注释里已经写明的**失败模式，但仍然用 `_ =` 丢弃。既没有升级为返回值，也没有追加进 `LaunchReport`（`client.go:67-82`），用户和日志都看不到"有一个核心进程没被杀掉"。唯一指望的兜底是引擎自己的 `--host-pid` 自杀机制（`privileged_windows.go:320`），但桌面进程退出前这个孤儿已经会持续占用系统资源。

**建议改法**

把清理结果并入返回错误，并用 `errors.Join` 保留两条原因（Go 1.20+）：

```go
	connection, err := pipe.accept(ctx, process.pid)
	if err != nil {
		killErr := process.Kill()
		waitErr := process.waitTimeout(coreTerminateWait)
		// Wait releases the process handle even on timeout, so report the
		// orphan explicitly instead of pretending cleanup succeeded.
		if killErr != nil || waitErr != nil {
			return nil, errors.Join(
				fmt.Errorf("验证高权限核心通信失败：%w", err),
				fmt.Errorf("清理失败的管理员核心进程（PID %d）：%w；%w",
					process.PID(), killErr, waitErr),
			)
		}
		return nil, fmt.Errorf("验证高权限核心通信失败：%w", err)
	}
```

同时在 `client.go:226-229` 的 `recordLaunchAttempt` 里补一条 `cleanup_failed` 记录，让托盘/诊断页能看到残留 PID。

---

## D-04 会话被丢弃时 `Kill()` 的错误被丢弃 → 引擎进程可能残留

**位置**

`desktop/internal/engineclient/client.go:283-292`

```go
		c.mu.Unlock()
		_ = session.closeTransport()
		_ = session.process.Kill()
		return Hello{}, errors.New("聚合核心客户端已关闭")
	}
	if c.session != nil {
		c.mu.Unlock()
		_ = session.closeTransport()
		_ = session.process.Kill()
		return Hello{}, errors.New("聚合核心已由另一启动请求连接")
	}
```

**触发条件**

`negotiateSession` 的这两个早退分支：(a) `Close()` 与启动竞态（`client.go:280-287`）；(b) 另一个 `Ensure` 已经抢先注册了会话（`client.go:288-293`）。两者都是并发窗口，在"快速切换代理模式""启动中用户点停止""窗口关闭时仍有启动在飞"等场景下很容易命中。

**后果**

`_ = session.process.Kill()` 丢弃错误。对 `stdioLauncher`（`launcher.go:164-169`，`p.command.Process.Kill()`）来说，若 `Process` 为 nil 则 `Kill()` **静默返回 nil** 而什么都没做（`launcher.go:165-167`），若权限不足/进程已进入不可中断状态则返回真实错误——两种情况都被丢弃，调用方一律看到 `"聚合核心已由另一启动请求连接"`。残留的引擎进程会继续持有系统代理和 TUN 虚拟网卡设置，桌面端的状态机却认为"没有会话"，用户在 UI 上也看不到任何异常。

**为什么当前的错误处理没兜住**

`killCurrent`（`client.go:467-481`）处理得相对好——它至少把 `reason` 传给 `failReplies`。但 `negotiateSession` 的这两条路径自己做了清理，**没有走 `killCurrent`**，因而既没有统一清理入口，也没有任何记录。

**建议改法**

统一走一条带记录的清理路径，并让清理失败可见：

```go
// client.go 新增
func (c *Client) discardSession(session *coreSession, reason string) {
	closeErr := session.closeTransport()
	killErr := session.process.Kill()
	if closeErr != nil || killErr != nil {
		c.recordLaunchAttempt(LaunchAttempt{
			Source: string(session.source), Stage: "cleanup", Result: "failed",
			PID:  session.process.PID(), ServicePID: session.details.ServicePID,
			Fallback: session.fallback,
			Message: fmt.Sprintf("%s；关闭传输：%v；终止进程：%v", reason, closeErr, killErr),
		})
	}
}

// 两处早退改为
	c.mu.Unlock()
	c.discardSession(session, "客户端在启动竞态中已关闭")
	return Hello{}, errors.New("聚合核心客户端已关闭")
```

另外 `execCoreProcess.Kill()`（`launcher.go:164-169`）在 `Process == nil` 时返回 nil 是误导性的，应该返回一个哨兵错误，让调用方知道"没有进程可杀"而不是"杀成功了"。

---

## D-05 `app.Run()` 返回错误时 `log.Fatal` 跳过全部服务清理 → 引擎进程 + 虚拟网卡 + 系统代理残留

**位置**

`desktop/main.go:239-241`

```go
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
```

**触发条件**

`app.Run()` 返回非 nil：`application.New` 已注册单实例锁（`main.go:84-96`），单实例竞争失败；WebView2 环境创建失败（`main.go:59-62` 的预检查只查注册表，运行期创建仍可能失败）；窗口创建失败。

**触发时已发生的系统状态修改**

`main.go:140-150` 已经构造了 `engineService`，`main.go:136-151` 构造了 `tunService` / `hypervAdapterService`。这些服务在本进程存活期间可能已启动引擎进程、创建虚拟网卡、改写系统代理。而真正的清理回调注册在 `main.go:153-163`：

```go
	desktop := wails.NewDesktopHost(app, mainWindow, startSilent, func() {
		...
		hypervAdapterService.Shutdown()
		engineService.Shutdown()
	}, ...)
```

这个 `onQuit` **只在** `DesktopHost.Quit()`（`desktop/internal/platform/wails/desktop.go:203-207`）里被调用。`app.Run()` 直接返回错误时，`Quit()` 从未被调用，`onQuit` 从未执行。

**后果**

`log.Fatal` → `os.Exit(1)`：进程立即死亡，跳过所有 `defer`。引擎进程成为孤儿（还在代理 / 还持有 TUN 网卡）、系统代理停留在 127.0.0.1、虚拟网卡留在设备列表里。用户重启 HypoMux 后看到一堆残留状态，且**没有任何日志说明发生过什么**（`log.Fatal` 只打印 `app.Run()` 的那一条）。

**为什么当前的错误处理没兜住**

清理闭包只挂在 UI 退出路径上，没有挂在 `main()` 的退出路径上；`log.Fatal` 明确绕过了 defer 链。

**建议改法**

让清理在所有退出路径上可达，并禁止 `log.Fatal` 跳过 defer：

```go
// main.go:31 顶部
func main() {
	// Catch panics on background goroutines' behalf is out of scope here, but
	// main() itself must never os.Exit past a registered cleanup.
	code := run()
	os.Exit(code)
}

// main.go:239
func run() error {
	...
	if err := app.Run(); err != nil {
		log.Printf("HypoMux 桌面运行失败: %v", err)
		desktop.Quit()   // 触发 main.go:153 注册的 onQuit 清理链
		return err
	}
	return nil
}
```

其中 `desktop.Quit()`（`desktop.go:199-209`）内部有 `quitting.CompareAndSwap` + `cleanupOnce` 幂等保护，即使 `app.Run()` 正常返回后再调用也不会重复清理。

---

# P1 —— 错误难排查

## D-06 `readLoop` 从不检查 `scanner.Err()`：真实传输错误被替换成"输出已关闭"

**位置**

`desktop/internal/engineclient/client.go:486-490` 与 `client.go:516`

```go
	for scanner.Scan() {
		var message response
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
```
```go
	c.failSession(session, errors.New("聚合核心输出已关闭"))
```

**触发条件**

`bufio.Scanner` 退出循环有三种原因，`Scan()` 返回 false 时必须读 `Err()` 才能区分：EOF、**底层读错误**、**token 超长**。这里第 488 行对 `json.Unmarshal` 的错误直接 `continue` 吞掉，第 516 行无条件用一个固定字符串收尾。`scanner.Err()` 在整个 `engineclient` 包中**从未被调用**（已全仓库 grep 确认）。

**后果**

- 引擎发出一条 >1 MiB 的帧（`client.go:485` 设置了 `MaxMessageBytes` 上限）→ `bufio.ErrTooLong` → 用户看到"聚合核心输出已关闭"。
- 命名管道被服务单方面关闭 / 句柄失效 → `ERROR_BROKEN_PIPE` → 同样显示"输出已关闭"。

`failSession` 会用这个 `reason` 去 `failReplies`（`client.go:519-533` → `541-545`），所以这个**错误一路传到前端**：`failReplies` 构造 `RemoteError{Code: "disconnected", Message: reason.Error()}`，前端拿到的永远是同一句话。运维看到的 support log 里也没有任何原始错误。这正是本审计要抓的"跨层错误载荷丢失"。

**为什么当前的错误处理没兜住**

`scanner.Err()` 是 `bufio` 唯一的错误出口，跳过它等于丢弃了整条传输层的诊断信息；而失败原因被硬编码成一个常量字符串，没有任何注入点。

**建议改法**

```go
	// client.go:516
	if scanErr := scanner.Err(); scanErr != nil && !errors.Is(scanErr, io.EOF) {
		c.failSession(session, fmt.Errorf("聚合核心传输中断：%w", scanErr))
		return
	}
	c.failSession(session, errors.New("聚合核心输出已关闭"))
```

第 488 行同理，至少计数并保留最后一条坏帧：

```go
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			c.recordBadFrame(err)   // 计数 + 截断后的原文片段
			continue
		}
```

---

## D-07 `command.Start()` 失败时三个管道句柄全部泄漏

**位置**

`desktop/internal/engineclient/launcher.go:132-146`

```go
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("连接聚合核心输入失败：%w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("连接聚合核心输出失败：%w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("连接聚合核心错误输出失败：%w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动 hypomux-engine.exe 失败：%w", err)
	}
```

**触发条件**

`command.Start()` 失败（引擎文件被占用/被杀软拦截/依赖 DLL 缺失/权限不足）。第 137 行和第 141 行的错误分支也一样：拿到 `stdout` 之后 `StderrPipe()` 失败，`stdout` 直接泄漏。

**后果**

每次 `Start()` 失败泄漏 **2-3 个 OS 句柄**。`exec.Cmd` 的文档明确说明 `StdinPipe` 创建的管道"will be closed automatically **after Wait sees the command exit**"，而 `Start` 失败时 `Wait` 永远不会被调用（`execCoreProcess.Wait` 是唯一的调用点，`launcher.go:160-162`，只有会话注册成功后才在 `client.go:302` 起 goroutine 调它）。UI 上的"重试连接"每点一次漏一次，短时间内就能耗尽进程的句柄上限（默认 32K，但 Wails/WebView2 本身已占用大量）。

**为什么当前的错误处理没兜住**

`exec.Cmd` 的自动关闭语义依赖 `Wait`，而这里的失败路径恰好在 `Start` 处返回，落在语义之外，且没有任何 `defer` 兜底。

**建议改法**

在 `Start` 之前挂上清理，成功后解除：

```go
	stdin, err := command.StdinPipe()
	// ... stdout / stderr 同理
	started := false
	defer func() {
		if !started {
			_ = stdin.Close()
			_ = stdout.Close()
			_ = stderr.Close()
		}
	}()

	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动 hypomux-engine.exe 失败：%w", err)
	}
	started = true
```

另外第 137/141 行的中间失败分支也要显式关闭已经拿到的管道。

---

## D-08 SCM 状态把 `START_PENDING` / `STOP_PENDING` 当成"未运行"，触发多余的 UAC 与竞争核心

**位置**

`desktop/internal/engineclient/service_windows.go:142-144`

```go
	if status.CurrentState != windows.SERVICE_RUNNING || status.ProcessId == 0 {
		return 0, true, nil
	}
```

**触发条件**

服务已安装、刚被启动或正在停止。此时 `QueryServiceStatusEx`（第 132-138 行）返回的 `CurrentState` 是 `SERVICE_START_PENDING (2)` 或 `SERVICE_STOP_PENDING (3)`，两者都会命中这个 `if`，返回 `pid == 0`。

**后果**

`windowsServiceLauncher.Launch`（`service_windows.go:39-41`）据此返回 `ErrCoreServiceNotRunning`，`serviceFirstLauncher.Launch`（`launcher.go:81-96`）随即走 fallback：弹出 UAC，用 `ShellExecuteExW` 拉起**第二个**管理员引擎进程。系统里同时存在"正在启动的服务实例"和"UAC 拉起的独立实例"，两个进程争抢同一个虚拟网卡与同一套系统代理设置——这是典型的系统状态不一致。而且 `status.ProcessId == 0` 这个条件对 `START_PENDING` 是**正确的**（PID 还没分配），但对已经 `RUNNING` 只是响应稍慢的场景同样误判。

**为什么当前的错误处理没兜住**

`queryCoreService` 只返回 `(pid, installed)`，**把 SCM 的原始状态整个丢掉了**，调用方无法区分"没启动"和"正在启动"。`connectCoreServicePipe` 第 154-177 行已经有一套 40ms 轮询重试机制可以吸收这个窗口，但重试发生在状态判定**之后**，形不成保护。

**建议改法**

把状态透传给调用方，让 pending 走已有的重试而不是走 UAC fallback：

```go
// service_windows.go
func queryCoreService() (pid int, installed bool, state uint32, err error) {
	...
	return int(status.ProcessId), true, status.CurrentState, nil
}

// service_windows.go:31-41
	pid, installed, state, err := queryCoreService()
	if err != nil { return nil, err }
	if !installed { return nil, ErrCoreServiceUnavailable }
	if pid == 0 {
		switch state {
		case windows.SERVICE_START_PENDING:
			// 服务正在启动：给它一点时间，绝不退化成 UAC 拉起第二个核心
			return windowsServiceLauncher{}.awaitRunning(ctx)
		default:
			return nil, ErrCoreServiceNotRunning
		}
	}
```

`awaitRunning` 复用 `connectCoreServicePipe` 已有的 40ms 节流循环即可（`service_windows.go:154-177`）。

---

## D-09 `SetReadDeadline` 错误被丢弃 → 认证读取 goroutine 可能永久滞留

**位置**

`desktop/internal/engineclient/privileged_windows.go:253`

```go
	_ = connection.SetReadDeadline(deadline)
```

**触发条件**

`pipeFile.SetReadDeadline` 返回错误（`pipe_file_windows.go:14-18` 的接口，经 `winio.NewOpenFile` 的类型断言而来）。该文件的注释 `244-248` 自己就承认了这一点："a Close that fails strands it for the lifetime of the process"。

**后果**

第 256-288 行起的 goroutine 在 `reader.ReadBytes('\n')`（第 258 行）上**没有任何超时保护**——它唯一的出路是第 297-299 行 `case <-ctx.Done()` 里的 `_ = connection.Close()`。如果 `Close()` 也失败，goroutine 永久阻塞。第 294 行的 `return connection.SetReadDeadline(time.Time{})` 虽然返回了错误，但它把"清除 30 秒认证期限"失败**升级成了整个启动失败**，随后 `Launch`（`privileged_windows.go:119-121`）会去 Kill 一个其实完全正常的管理员核心——因 D-03 而变成孤儿进程。两个缺陷叠加。

**为什么当前的错误处理没兜住**

`_ =` 同时吃掉了"设置失败"和（通过返回值链）制造了一个误导性的失败原因。goroutine 本身也没有生命周期跟踪（没有 `context`、没有 `done` 通道），泄漏是不可观测的。

**建议改法**

让 deadline 设置失败成为硬错误（早失败优于 goroutine 泄漏），并给 goroutine 加显式生命周期：

```go
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(sessionAuthTimeout)
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("为核心身份校验设置读超时失败：%w", err)
	}

	result := make(chan error, 1)
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				result <- fmt.Errorf("核心身份校验 panic: %v", r)
			}
		}()
		reader := bufio.NewReaderSize(connection, maxSessionMessageBytes)
		// ...
	}()

	select {
	case err := <-result:
		if err == nil {
			if resetErr := connection.SetReadDeadline(time.Time{}); resetErr != nil {
				_ = connection.Close()
				return fmt.Errorf("清除核心身份校验读超时失败：%w", resetErr)
			}
		}
		return err
	case <-ctx.Done():
		if closeErr := connection.Close(); closeErr != nil {
			return errors.Join(ctx.Err(), fmt.Errorf("关闭核心管道失败，认证 goroutine 可能滞留: %w", closeErr))
		}
		return ctx.Err()
	}
```

`finished` 通道可用于将来让 goroutine 自我退出；当前至少 `recover` 补上（见 D-24 同类问题）。

---

## D-10 `ShellExecuteExW` 的失败原因取自 `syscall.Errno`，错误文案可能完全错误

**位置**

`desktop/internal/engineclient/privileged_windows.go:339-345`

```go
	result, _, callErr := procedure.Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) || errors.Is(callErr, syscall.Errno(windows.ERROR_CANCELLED)) {
			return nil, ErrElevationCancelled
		}
		return nil, fmt.Errorf("启动管理员核心失败：%w", callErr)
	}
```

**触发条件**

`ShellExecuteExW` 返回 FALSE 的绝大多数情况（文件找不到、路径非法、关联程序缺失、磁盘错误）。

**后果**

`syscall.LazyProc.Call` 在返回 0 时把 `GetLastError()` 包成 `syscall.Errno`，但 `ShellExecuteExW` **不是**通过 `SetLastError` 报告失败的——它的失败原因在 `SHELLEXECUTEINFO.hInstApp` 字段里。因此第 344 行包装出来的 `callErr` 经常是 `0`（"操作成功"）或一个完全无关的陈旧值。用户看到的是 `启动管理员核心失败：The operation completed successfully.`。而且第 341 行的 `ERROR_CANCELLED (1223)` 判断同样不可靠——用户点"否"时能否正确映射到 `ErrElevationCancelled` 取决于 `hInstApp`，而代码根本没读它。这直接破坏 `allowPostHandshakeFallback`（`service_windows.go:93-108`）和 `Client.ensure`（`client.go:240-265`）对"用户取消提权"这一语义的依赖。

**为什么当前的错误处理没兜住**

`shellExecuteInfo` 结构体（`privileged_windows.go:64-80`）**已经定义了 `Instance windows.Handle` 字段**（第 73 行，对应 `hInstApp`），但整个文件从未读取它。错误被"包装"了，但包装的是错的东西——比不包装更糟，因为它看起来有诊断信息。

**建议改法**

读 `info.Instance`（`hInstApp`），并保留 `callErr` 作为兜底：

```go
	result, _, callErr := procedure.Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		// ShellExecuteExW reports its failure through hInstApp, not
		// GetLastError; callErr is frequently a stale/zero value.
		switch hInstApp := int32(info.Instance); hInstApp {
		case 0:
			// out-of-memory or pre-Shell32 failure
			return nil, fmt.Errorf("启动管理员核心失败（ShellExecuteExW 未启动进程）: %w", callErr)
		case 32: // ERROR_SHELLAPI_CANCELLED
			return nil, ErrElevationCancelled
		default:
			return nil, fmt.Errorf("启动管理员核心失败（ShellExecuteExW hInstApp=0x%X）", uint32(hInstApp))
		}
	}
```

---

## D-11 托盘弹窗定位失败被静默丢弃，用户看到"位置不对"却没有任何说明

**位置**

`desktop/internal/platform/wails/desktop.go:85-92`

```go
	if d.trayWindow != nil {
		d.positionTray = newTrayPositioner(d.trayWindow, d.tray)
		if err := d.positionTray(); err == nil {
			d.trayWindow.Show().Focus()
			return
		}
	}
	d.tray.ShowMenu()
```

以及 `desktop/internal/platform/wails/desktop.go:102-104`

```go
	if d.positionTray != nil && d.trayWindow.IsVisible() {
		_ = d.positionTray()
	}
```

**触发条件**

`positionTray()` 返回错误——它内部走 `application.InvokeSyncWithError`（`tray_position_windows.go:40-50`），在 UI 线程被占用、窗口尚未创建、或 `window.Bounds()` 在销毁竞态中失败时都会返回非 nil。

**后果**

用户右键托盘后看到的是**原生菜单**而不是自定义弹窗，或者弹窗停在错误位置（多显示器/负坐标场景）。UI 没有任何提示、`supportLogs` 没有任何记录。`ResizeTray`（第 96-105 行，翻译文本变化时调用）里的失败更是完全无声——弹窗尺寸没跟上，但没人知道为什么。注意 `ConfigureTray` 第 56 行的注释 `// Retain a native fallback if positioning the popup fails.` 表明这是有意设计，但"有意 fallback"不等于"应该丢掉错误"。

**为什么当前的错误处理没兜住**

`if err := ...; err == nil {}` 是把错误**绑定在控制流上**的吞错形态——错误值确实被接收了，但立刻离开作用域，既不上报也不记录。

**建议改法**

保留 fallback 行为，但让失败可观测：

```go
	if d.trayWindow != nil {
		d.positionTray = newTrayPositioner(d.trayWindow, d.tray)
		if err := d.positionTray(); err == nil {
			d.trayWindow.Show().Focus()
			return
		} else {
			d.reportTrayPositionFailure(err)
		}
	}
	d.tray.ShowMenu()
```

```go
// desktop.go 新增
func (d *DesktopHost) reportTrayPositionFailure(err error) {
	if d.onError != nil {
		d.onError(fmt.Errorf("托盘弹窗定位失败，已回退到原生菜单：%w", err))
		return
	}
}
```

`ResizeTray` 的第 103 行同理，至少计数并在连续失败时降级一次原生菜单。

---

## D-12 `wifi connect()` 先发请求再诊断策略，且策略错误会覆盖真实的 WlanConnect 失败原因

**位置**

`desktop/internal/startup/wifi_windows.go:142-154`

```go
	code, _, _ := wlanConnect.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&params)), 0)
	// Read-only diagnosis: an organization/user policy must not be overwritten.
	key, policyErr := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\WcmSvc\GroupPolicy`, registry.QUERY_VALUE)
	if policyErr == nil {
		value, _, readErr := key.GetIntegerValue("fMinimizeConnections")
		key.Close()
		if readErr == nil && value == 3 {
			return fmt.Errorf("Windows 同时连接策略为 3，有线已连接时禁止 WLAN；请在 Windows 连接管理器策略中检查，受管理设备请联系管理员")
		}
	}
	if code != 0 {
		return wifiAPIError("请求连接已保存的无线网络", code)
	}
	return nil
```

**触发条件**

同时满足两个条件：策略 `fMinimizeConnections == 3`，且 `WlanConnect` 返回非 0（真实的 WLAN 失败，例如配置文件加密方式不支持、无线电关闭、AP 不可见）。

**后果**

`WlanConnect` 的真实失败码 `code` 被**整个丢弃**，用户只看到策略错误。两者都被返回成 error，`wifi.go:112-114` 只会把这一条包进 `failures`，`main.go:377` 再显示为"最近的 Wi-Fi 连接提示"。于是"网卡没连上"被诊断成"公司策略不让连"——用户会去改组策略，而真正的原因（配置文件不兼容）永远查不到。

反过来，`policyErr != nil`（非管理员进程读不到 `HKLM`）时策略检查被静默跳过（`policyErr` 从未被使用，只在 `== nil` 时进入分支），错误同样不体现。`readErr`（第 146 行）被丢弃也是同一模式。

**为什么当前的错误处理没兜住**

诊断逻辑被写成了一条"抢先 return"的路径，抢在真正的错误传播之前。策略诊断本身是只读的、正确的，但它被放在了**会遮蔽真实错误的位置**。

**建议改法**

把诊断降级为附加上下文，绝不替代真实错误：

```go
	code, _, _ := wlanConnect.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&params)), 0)

	// Diagnosis only; must never mask the real WlanConnect failure below.
	var policyNote string
	if key, policyErr := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\WcmSvc\GroupPolicy`, registry.QUERY_VALUE); policyErr == nil {
		value, _, readErr := key.GetIntegerValue("fMinimizeConnections")
		key.Close()
		if readErr == nil && value == 3 {
			policyNote = "Windows 同时连接策略为 3（有线已连接时禁止 WLAN）"
		}
	} else if !errors.Is(policyErr, windows.ERROR_FILE_NOT_FOUND) {
		policyNote = "（无法读取连接管理器策略：" + policyErr.Error() + "）"
	}

	if code != 0 {
		if policyNote != "" {
			return fmt.Errorf("%w；注意：%s", wifiAPIError("请求连接已保存的无线网络", code), policyNote)
		}
		return wifiAPIError("请求连接已保存的无线网络", code)
	}
	return nil
```

---

## D-13 WebView2 探测把"读不到注册表"一律当成"未安装"，产生误导性的用户可见结论

**位置**

`desktop/internal/platform/webview2_windows.go:23-34`

```go
	for _, check := range checks {
		key, err := registry.OpenKey(check.root, check.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		version, _, readErr := key.GetStringValue("pv")
		_ = key.Close()
		if readErr == nil && version != "" {
			return true
		}
	}
	return false
```

**触发条件**

`HKLM\SOFTWARE\...` 打开失败——组策略限制非管理员读取 EdgeUpdate 键、沙箱/加固镜像、企业环境 ACL 拒绝访问，或 `pv` 值缺失/为空（企业版 Edge 分发的注册表布局不同）。

**后果**

第 25-27 行 `continue` 与第 30 行 `readErr == nil &&` 把**权限不足**和**组件缺失**折叠成同一个结论 `false`。`main.go:59-62` 随即弹出：

```
未检测到 Microsoft Edge WebView2 Runtime，HypoMux 无法显示桌面界面。
请重新运行 HypoMux 安装程序；安装程序会自动安装 WebView2 Runtime。安装完成后再启动 HypoMux。
```

用户会去重装安装程序、重装 WebView2，全都无效——因为 WebView2 一直都在，只是这个用户读不到那三个注册表路径。这是一条**会直接把用户引向错误方向**的诊断信息。此外这是全应用最外层的检查：一旦误判，`main()` 直接 `return`，UI 完全不出现，而日志里没有任何东西（`WebView2Available()` 返回 bool，不返回 error，调用方也无法区分）。

**为什么当前的错误处理没兜住**

函数签名 `func WebView2Available() bool` 根本不具备承载错误的能力；`contracts.go` 也没有为它定义契约。所有失败模式都被压成两个布尔值。

**建议改法**

区分"确定缺失"与"无法确认"，并让调用方可以据此继续：

```go
// webview2_windows.go：改为三态
func webView2Probe() (available bool, indeterminate bool) {
	for _, check := range checks {
		key, err := registry.OpenKey(check.root, check.path, registry.QUERY_VALUE)
		if err != nil {
			if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				indeterminate = true   // 权限/策略问题，不能据此断言"未安装"
			}
			continue
		}
		version, _, readErr := key.GetStringValue("pv")
		_ = key.Close()
		if readErr == nil && version != "" {
			return true, false
		}
		indeterminate = true
	}
	return false, indeterminate
}
```

```go
// main.go:59
	available, indeterminate := desktopplatform.WebView2Probe()
	if !available && !indeterminate {
		desktopplatform.ShowWebView2MissingMessage()
		return
	}
	// indeterminate 时继续启动，让 Wails 自己报真实错误，而不是替它下结论
```

---

## D-14 `SyncMetadata` 顺序写 11+ 个文件，中途失败无回滚、不原子

**位置**

`desktop/internal/releaseversion/metadata.go:65-69`

```go
	for path, data := range contents {
		if err := os.WriteFile(filepath.Join(root, path), data, 0644); err != nil {
			return err
		}
	}
```

**触发条件**

第 3 个文件写入失败——只读文件、杀毒软件锁、`wails.exe.manifest` 被 Git 索引占用、磁盘满。`contents` 有 12 个条目（`metadata.go:20-31` 的 10 个 edit 涉及 9 个不同路径，加上 `:48` 的 `VERSION` 和 `:49` 的 `version.nsh`）。

**后果**

`os.WriteFile` = `OpenFile(O_TRUNC)` + `Write`，**失败时目标文件已被截断**。结果是一个半同步的仓库：`Taskfile.yml` 已经是新版本、`frontend/package.json` 还是旧版本、`wails.exe.manifest` 可能变成 0 字节。第 33-47 行做了完整的"所有输入先校验"（这一点很好），但写阶段完全没有对应的原子性或回滚。CI 里 `cmd/release-version/main.go:15-18` 会以 exit 1 退出，所以构建不会继续——但工作区已经脏了，而且损坏的文件（尤其是 NSIS/manifest）要靠 `git checkout` 才能恢复。

**为什么当前的错误处理没兜住**

"先全量校验、再全量写入"这个模式只解决了*读*侧的一致性，写侧没有任何事务边界。`errors` 已 import 但从未使用（`metadata.go:4`），说明这里原本考虑过回滚。

**建议改法**

两阶段提交：先全部写临时文件并 rename（rename 在同一卷内是原子的），任一步失败就清理已写的临时文件：

```go
	temporaries := make([]string, 0, len(contents))
	defer func() {
		for _, t := range temporaries {
			_ = os.Remove(t)   // 失败路径的清理
		}
	}()

	written := make([]string, 0, len(contents))
	paths := slices.Sorted(maps.Keys(contents))   // 固定顺序，报错可复现
	for _, path := range paths {
		final := filepath.Join(root, path)
		tmp := final + ".releaseversion.tmp"
		if err := os.WriteFile(tmp, contents[path], 0o644); err != nil {
			return errors.Join(err, rollbackFiles(written, root))
		}
		if err := os.Rename(tmp, final); err != nil {
			_ = os.Remove(tmp)
			return errors.Join(err, rollbackFiles(written, root))
		}
		temporaries = append(temporaries, tmp)
		written = append(written, final)
	}
	return nil
```

```go
func rollbackFiles(written []string, root string) error {
	var errs []error
	for _, f := range written {
		if data, err := os.ReadFile(f); err == nil {
			errs = append(errs, os.WriteFile(f, data, 0o644))
		}
	}
	return errors.Join(errs...)
}
```

---

## D-15 `repairLegacyAutostartTask` 把**任何**查询失败当成"任务不存在"，幽灵自启动永不修复

**位置**

`desktop/internal/startup/privilege_windows.go:415-420`

```go
func repairLegacyAutostartTask() error {
	query := exec.Command("schtasks.exe", "/Query", "/TN", legacyAutostartTaskName)
	hideWindow(query)
	if err := query.Run(); err != nil {
		return nil
	}
```

**触发条件**

`schtasks.exe /Query` 返回非 0 的**任何**原因：旧版任务确实不存在（预期）、`schtasks.exe` 不在 PATH（精简 Windows / 组策略禁用）、任务文件损坏、当前用户无权查询该任务（它在管理员上下文注册，而此处的提权 bootstrap 在某些路径下并非管理员）。

**后果**

第 418-420 行把这三种情况全部映射成"成功，任务不存在"。真正的旧版自启动任务 `\HypoMuxAutoStart`（`privilege_windows.go:17`）就此**静默残留**：每次开机它都会以旧方式拉起 HypoMux，与新的 `HKCU\...\Run`（`platform/autostart_windows.go:16-19`）**双重启动**。而 `PrepareDesktopLaunch` 第 45-47 行只会在 `err != nil` 时记录 `LegacyTaskRepairNote`——这里 `err` 恒为 nil，所以连一句提示都不会出现在 `main.go:53-57` 的日志里。用户在任务计划程序里看到了一个"删不掉"的任务，却不知道为什么。

**为什么当前的错误处理没兜住**

`err != nil` 被当作"不存在"的充分条件，而**存在性判断需要区分 `exit code != 0` 里的具体原因**（`schtasks` 对"任务不存在"和"查询失败"返回不同的退出码/输出）。

**建议改法**

解析输出区分两种失败，未知情况上报而不是假装成功：

```go
func repairLegacyAutostartTask() error {
	query := exec.Command("schtasks.exe", "/Query", "/TN", legacyAutostartTaskName)
	hideWindow(query)
	output, err := query.CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(output))
		// schtasks 对"任务不存在"返回 ERROR_FILE_NOT_FOUND(2) 并输出
		// "ERROR: The system cannot find the file specified."
		if strings.Contains(text, "cannot find") || strings.Contains(text, "系统找不到") ||
			strings.Contains(text, "no tasks") {
			return nil
		}
		return fmt.Errorf("查询旧版自启动任务失败（任务可能仍残留）：%s: %w",
			strings.TrimSpace(string(output)), err)
	}
	remove := exec.Command("schtasks.exe", "/Delete", "/TN", legacyAutostartTaskName, "/F")
	hideWindow(remove)
	out, err := remove.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return errors.New(detail)
	}
	return nil
}
```

---

# P2 —— 代码整洁度 / 潜在隐患

## D-16 多处用 `%v` 拼接两个错误，破坏 `errors.Is` / `errors.As` 链

**位置**

| 文件:行 | 原文 |
|---|---|
| `desktop/internal/engineclient/client.go:251` | `return Hello{}, fmt.Errorf("%v；UAC 兼容启动失败：%w", negotiateErr, fallbackErr)` |
| `desktop/internal/engineclient/client.go:259` | `return Hello{}, fmt.Errorf("%v；UAC 兼容核心协议协商失败：%w", negotiateErr, fallbackNegotiateErr)` |
| `desktop/internal/engineclient/launcher.go:83` | `return nil, fmt.Errorf("%w；自动兼容启动已取消：%v", err, trustErr)` |
| `desktop/main.go:374` | `return fmt.Errorf("等待开机网卡就绪失败：%v：%w", lastListErr, ctx.Err())` |
| `desktop/main.go:377` | `return fmt.Errorf("等待开机网卡就绪超时（缺少：%s）；最近的 Wi-Fi 连接提示：%v：%w", strings.Join(missing, "、"), lastWiFiErr, ctx.Err())` |
| `desktop/internal/startup/privilege_windows.go:294` | `return fmt.Errorf("CreateProcessWithTokenW: %v; CreateProcessAsUserW: %w", tokenErr, asUserErr)` |

**触发条件 / 后果**

`client.go:251` 尤其关键：调用方（`services/engine.go`）依赖 `errors.Is(err, ErrElevationCancelled)` 之类的判断来决定是否提示"请以管理员身份重试"。`negotiateErr` 用 `%v` 后就永远无法被匹配。`main.go:374/377` 同理——`ctx.Err()` 用 `%w` 保住了 `DeadlineExceeded`，但 `lastListErr`/`lastWiFiErr` 永久丢失。这类错误会一路传到前端和 support log，排查时只能靠字符串匹配。

**为什么当前的错误处理没兜住**

作者手工拼装了 `A；B` 的文案，但没有意识到这等价于丢弃了 A 的错误身份。仓库里已有正确范式——`startup/wifi.go:116` 的 `errors.Join(failures...)`。

**建议改法**

用 `errors.Join`（消息本身已可读，无需再加分隔符）：

```go
// client.go:251
return Hello{}, errors.Join(negotiateErr, fmt.Errorf("UAC 兼容启动失败：%w", fallbackErr))
// client.go:259
return Hello{}, errors.Join(negotiateErr, fmt.Errorf("UAC 兼容核心协议协商失败：%w", fallbackNegotiateErr))
// launcher.go:83
return nil, errors.Join(err, fmt.Errorf("自动兼容启动已取消：%w", trustErr))
// main.go:374
return fmt.Errorf("等待开机网卡就绪失败：%w", errors.Join(lastListErr, ctx.Err()))
// privilege_windows.go:294
return errors.Join(fmt.Errorf("CreateProcessWithTokenW: %w", tokenErr),
                   fmt.Errorf("CreateProcessAsUserW: %w", asUserErr))
```

---

## D-17 `readLoop` 在会话过期时静默 `return`，不记录也不排空传输

**位置**

`desktop/internal/engineclient/client.go:504-508`

```go
		c.mu.Lock()
		if c.session != session {
			c.mu.Unlock()
			return
		}
```

**触发条件**

会话已被 `killCurrent` / `failSession` 换掉（`client.go:467-481` / `519-533`），旧 `readLoop` 读到残留帧。

**后果**

这个 `return` 是**正确的**（否则会污染新会话的 pending 表），但它是一个零可观测性的静默退出点，与第 488 行的 `continue` 一起，构成"传输层所有异常路径都无法诊断"的模式。另外此处直接 `return` 而不关闭 `session.reader`——正常情况下已被 `killCurrent` 关过，但如果是 `failSession` 因别的原因先关的，这里存在一个未关闭的读端。

**为什么当前的错误处理没兜住**

没有任何计数器/日志能区分"正常退休"与"异常中止"。

**建议改法**

```go
		c.mu.Lock()
		stale := c.session != session
		c.mu.Unlock()
		if stale {
			c.recordReadLoopRetirement(session)   // 计数；并确保读端已关闭
			return
		}
```

---

## D-18 `setDWMAttribute` 打印的 `callError` 对 HRESULT 型 API 无意义

**位置**

`desktop/internal/platform/wails/appearance_windows.go:34-37`

```go
	if result != 0 {
		return fmt.Errorf("DwmSetWindowAttribute returned HRESULT 0x%X: %v", result, callError)
	}
```

**触发条件 / 后果**

`DwmSetWindowAttribute` 通过返回值（HRESULT）报告失败。第 28 行的 `dwmSetWindowAttribute.Call(...)` 在返回非 0 时把 `GetLastError()` 包成 `callError`，但 DWM API 不使用 `SetLastError`。`nativeResult`（`appearance_windows.go:40-45`）把这条消息原样送到前端 `NativeAppearanceResult.Reason`（`contracts.go:23-27`），于是 UI 上出现 `DwmSetWindowAttribute returned HRESULT 0x80070057: The operation completed successfully.` —— 与 D-10 是同一类错误。

**为什么当前的错误处理没兜住**

HRESULT 已经是完整的诊断信息，额外拼接一个不可靠的 `callError` 只会污染它。

**建议改法**

```go
	if result != 0 {
		return fmt.Errorf("DwmSetWindowAttribute(0x%X) 返回 HRESULT 0x%X", attribute, uint32(result))
	}
```

---

## D-19 WLAN API 的返回码在多处被丢弃

**位置**

- `desktop/internal/startup/wifi_windows.go:62`：`func (s *nativeWiFiSession) close() { wlanClose.Call(uintptr(s.handle), 0) }`
- `desktop/internal/startup/wifi_windows.go:127`：`wlanFree.Call(uintptr(unsafe.Pointer(payload)))`
- `desktop/internal/startup/wifi_windows.go:146`：`value, _, readErr := key.GetIntegerValue("fMinimizeConnections")`
- `desktop/internal/platform/wails/tray_position_windows.go:29` / `:33` / `:36`：`if ok, _, _ := trayGetCursorPos.Call(...)` 等

**触发条件 / 后果**

`WlanCloseHandle` 失败（句柄已失效）→ `wifi.go:66` 的 `defer session.close()` 永远无法上报，会话句柄泄漏且不可见。`WlanFreeMemory` 失败 → 每次 `profiles()` 调用泄漏一块非分页池内存；`wifi.go:92` 在 2 分钟开机等待循环里按 2 秒轮询调用（`main.go:271` `autoStartAdapterPollInterval`），一次开机可能调用 60 次。`tray_position_windows.go` 的三处失败都走同一条"退回到 `PositionWindow`"的路径（`:30` / `:37`），行为正确但同样零记录。

**为什么当前的错误处理没兜住**

`wifiSession` 接口（`wifi.go:19-24`）的 `close()` **没有返回值**，从类型上就不允许上报。

**建议改法**

把 `close()` 改为 `close() error`，并在 `wifi.go:66` 汇总：

```go
// wifi.go:23
	close() error
// wifi.go:66
defer func() {
    if closeErr := session.close(); closeErr != nil {
        // 至少计数；开机等待循环里每 2 秒一次，不能打日志刷屏
        wifiSessionCloseFailures.Add(1)
    }
}()
```

```go
// wifi_windows.go:62
func (s *nativeWiFiSession) close() error {
	code, _, _ := wlanClose.Call(uintptr(s.handle), 0)
	if code != 0 {
		return wifiAPIError("关闭 WLAN 会话", code)
	}
	return nil
}
```

`profiles()` 内的 `wlanFree` 失败同理，可在返回前统计并通过 `wifiProfile` 之外的诊断通道上报。

---

## D-20 无线配置文件 XML 解析失败被静默降级为"没有可用网络"

**位置**

`desktop/internal/startup/wifi.go:49`

```go
	return xml.Unmarshal([]byte(payload), &profile) == nil && profile.Mode == "auto" && profile.Type == "ESS"
```

**触发条件**

`wlanProfile` 返回的 XML 因以下原因无法解析：配置文件由较新/较旧版本的 WlanSvc 导出、包含本代码未预期的命名空间、被第三方工具改写。

**后果**

解析失败与"配置不是自动连接类型"被折叠为同一个 `false`，该 profile 被 `wifi.go:99-101` 跳过。若所有 profile 都如此，`wifi.go:104` 返回：

```
Wi-Fi 网卡 %s 没有允许自动连接的已保存网络，请先在 Windows 中连接 Wi-Fi 并勾选自动连接
```

这条建议**完全误导**——网络确实保存了、也确实勾选了自动连接，只是 XML 解析失败。用户按提示操作后问题依旧，而 `main.go:377` 显示给用户的原因也是这条。

**为什么当前的错误处理没兜住**

同样是一个 `== nil` 布尔折叠，和 D-13 是同一形态。

**建议改法**

区分"解析失败"与"确实不是自动连接"：

```go
func automaticWiFiProfile(payload string) (bool, error) {
	var profile struct {
		Mode string `xml:"connectionMode"`
		Type string `xml:"connectionType"`
	}
	if err := xml.Unmarshal([]byte(payload), &profile); err != nil {
		return false, fmt.Errorf("解析 Wi-Fi 配置失败：%w", err)
	}
	return profile.Mode == "auto" && profile.Type == "ESS", nil
}
```

调用侧（`wifi.go:99`）把解析错误收集进 `failures`，让用户看到真实原因。

---

## D-21 提权重启失败清理中的 `TerminateProcess` 错误全部丢弃

**位置**

`desktop/internal/startup/privilege_windows.go:301`、`:381`、`:390`、`:397`、`:401`、`:405`、`:409`

```go
	_ = windows.TerminateProcess(processInfo.Process, 1)
	return fmt.Errorf("verify replacement UI token: %w", err)
```

（`:390`、`:397`、`:401`、`:405`、`:409` 形态相同）

**触发条件**

替换 UI 的令牌校验失败（SID 不匹配 / 意外获得提权令牌 / `ResumeThread` 失败），走 `verifyAndResumeReplacement`（`privilege_windows.go:387-413`）的清理分支。

**后果**

`CreateProcess` 用的是 `CREATE_SUSPENDED`（`privilege_windows.go:254`、`:359`），所以被"终止"的进程若 `TerminateProcess` 失败，会停在**挂起态**——不运行、也不退出、不占 CPU，但进程对象和它的主线程句柄一直存在，任务管理器里可见一个"挂起"进程。而且这些失败只会返回上面那条 `verify replacement UI token` 错误，用户完全不知道自己留下了一个挂起进程。

**为什么当前的错误处理没兜住**

这是**六处一模一样的 `_ =`**，没有任何一处把清理结果并入返回错误。

**建议改法**

在 `verifyAndResumeReplacement` 里统一处理，并 `errors.Join`：

```go
// privilege_windows.go:387
func verifyAndResumeReplacement(processInfo windows.ProcessInformation, expectedSID string) error {
	terminate := func(reason error) error {
		if termErr := windows.TerminateProcess(processInfo.Process, 1); termErr != nil {
			return errors.Join(reason,
				fmt.Errorf("清理失败：替换 UI 进程（PID 可能已挂起）终止失败：%w", termErr))
		}
		return reason
	}

	var childToken windows.Token
	if err := windows.OpenProcessToken(processInfo.Process, windows.TOKEN_QUERY, &childToken); err != nil {
		return terminate(fmt.Errorf("verify replacement UI token: %w", err))
	}
	// ... 其余分支同样改为 return terminate(fmt.Errorf(...))
}
```

`privilege_windows.go:299-303` 与 `:379-383` 两处同理。

---

## D-22 `SetAutostart(true)` 先清审批记录再写 Run 值，写失败不回滚

**位置**

`desktop/internal/platform/autostart_windows.go:59-69`

```go
	if err := clearStartupApproval(); err != nil {
		return fmt.Errorf("开启开机自启失败：%w", err)
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRegistryKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("开启开机自启失败：%w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(autostartValueName, autostartCommand(executable)); err != nil {
		return fmt.Errorf("开启开机自启失败：%w", err)
	}
```

**触发条件**

第 59 行成功、第 64 行或第 67 行失败（`HKCU` 被组策略重定向、磁盘满、注册表句柄耗尽、杀毒软件拦截）。

**后果**

`StartupApproved\Run\HypoMux` 的审批记录已被删除，但 Run 值没写（或写了旧值）。这是一个**半完成的系统状态变更**。实际影响有限——`AutostartEnabled()`（`:112-127`）在没有审批记录时返回 `true`（`:113-115`、`:121-123`），所以下次读取会认为已启用，与磁盘上的事实不一致。真正的风险在于：用户原本在任务管理器里**显式禁用**过 HypoMux，此时重试 `SetAutostart(true)` 会把这个用户选择永久抹掉且无法恢复。

**为什么当前的错误处理没兜住**

两个独立的注册表写操作之间没有事务边界，第 59 行的破坏性写没有对应的 undo。

**建议改法**

把顺序反过来（先写 Run 值、成功后再清审批），失败自然不留半状态：

```go
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRegistryKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("开启开机自启失败：%w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(autostartValueName, autostartCommand(executable)); err != nil {
		return fmt.Errorf("开启开机自启失败：%w", err)
	}
	// Re-enabling in HypoMux is an explicit user action. Remove a stale Task
	// Manager approval record last, so a failure above leaves it untouched.
	if err := clearStartupApproval(); err != nil {
		return fmt.Errorf("开机自启已写入，但未能清除任务管理器审批记录：%w", err)
	}
	return nil
```

---

## D-23 `ShowErrorMessage` 丢弃字符串转换错误，可能弹出空标题的对话框

**位置**

`desktop/internal/platform/webview2_windows.go:46-50`

```go
	title, _ := windows.UTF16PtrFromString(titleText)
	message, _ := windows.UTF16PtrFromString(messageText)
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	_, _, _ = messageBox.Call(
```

**触发条件**

`UTF16PtrFromString` 只在字符串含 NUL 字节（`\x00`）时返回错误。`titleText`/`messageText` 来自调用方：`main.go:126-133` 的 `fmt.Sprintf(...settingsService.StartupErrorPath()..., err)` 和 `main.go:59-61`。**如果配置文件路径或引擎返回的错误消息里含嵌入 NUL，转换就会失败**（例如从注册表读出的畸形值）。

**后果**

转换失败时指针为 `nil`，`uintptr(unsafe.Pointer(message))` 变成 0，`MessageBoxW(0, NULL, NULL, MB_ICONERROR)` 弹出一个**完全空白的错误框**——而这恰恰是"配置加载失败、已停止启动"这个最需要解释的场景。用户看到一个没有文字的对话框。同时第 50 行的 `_, _, _ =` 让 `MessageBoxW` 自身失败（会话已断开、桌面不存在）也完全不可见。

**为什么当前的错误处理没兜住**

两条路径都假设转换必然成功，没有兜底文本。

**建议改法**

```go
func ShowErrorMessage(titleText string, messageText string) {
	titlePtr, err := windows.UTF16PtrFromString(titleText)
	if err != nil || titlePtr == nil {
		titlePtr, _ = windows.UTF16PtrFromString("HypoMux")   // 兜底，绝不传 nil
	}
	messagePtr, err := windows.UTF16PtrFromString(messageText)
	if err != nil || messagePtr == nil {
		messagePtr, _ = windows.UTF16PtrFromString(
			"（错误信息包含无法显示的字符，详见日志：" + titleText + "）")
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	if result, _, callErr := messageBox.Call(
		0,
		uintptr(unsafe.Pointer(messagePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		0x00000010,
	); result == 0 {
		// 没有 GUI 会话时至少保证信息不丢
		_, _ = fmt.Fprintf(os.Stderr, "%s: %s\n", titleText, messageText)
		_ = callErr
	}
}
```

---

## D-24 后台 goroutine 缺少 `recover`

**位置**

| 文件:行 | 内容 |
|---|---|
| `desktop/internal/engineclient/client.go:301-304` | `go func() { _ = session.process.Wait(); c.failSession(...) }()` |
| `desktop/internal/engineclient/launcher.go:147-149` | `go func() { _, _ = io.Copy(io.Discard, stderr) }()` |
| `desktop/internal/engineclient/privileged_windows.go:256-288` | 身份认证读取 goroutine（内部 `reader.ReadBytes`） |
| `desktop/main.go:198-228` | 开机自动加速 goroutine |

**触发条件**

任何 goroutine 内未捕获的 panic。`privileged_windows.go:257-286` 涉及 `json.Unmarshal` 与 `bufio.Reader`；`main.go:198-228` 涉及设置读取与 WiFi 操作。

**后果**

Go 的 panic 语义是**整个进程崩溃**，不是只杀 goroutine。一次配置解码 panic 就会带走在跑的引擎管理和用户界面。第 301-304 行的 goroutine 还持有 `c.mu` 的调用上下文——虽然它自身不持锁，但 `failSession`（`client.go:519`）会拿锁。

**为什么当前的错误处理没兜住**

整个引擎客户端层没有任何 `recover`。值得注意的是，测试文件 `privileged_windows_test.go` 里有手写的 recover 逻辑，说明这个模式在项目里是被认识的，只是生产代码没有统一采用。

**建议改法**

至少在这四个启动/会话关键 goroutine 上加统一的 recover，并记录后降级：

```go
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.failSession(session, fmt.Errorf("聚合核心会话监视器 panic: %v\n%s", r, debug.Stack()))
			}
		}()
		_ = session.process.Wait()
		c.failSession(session, errors.New("聚合核心进程已退出"))
	}()
```

更彻底的做法是加一个包级 helper：

```go
// desktop/internal/engineclient/guard.go
func guardGoroutine(what string, onPanic func(error), fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				onPanic(fmt.Errorf("%s panic: %v\n%s", what, r, debug.Stack()))
			}
		}()
		fn()
	}()
}
```

---

## D-25 `releaseversion.Key()` 的 legacy 分支不校验 65535 上限，与自身文档矛盾

**位置**

`desktop/internal/releaseversion/version.go:65-93`

```go
// Key retains the old updater's two-to-four-part numeric versions while adding
// the strictly validated beta/rc formats. Invalid versions never compare newer.
func Key(value string) []int {
```

```go
	key := []int{0, 0, 0, 0, 2, 0}
	for i, part := range strings.Split(value, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		key[i] = n
	}
```

**触发条件**

传入 `"99999.0.0"` 或 `"1.70000.0"` 这类超大分量的 legacy 版本串。

**后果**

`Parse`（`version.go:28`、`:35`）严格限制分量 ≤ 65535，并发版元数据也限制在 65535（`version.go:55` 的 `revision := 65535`）。但 `Key` 的 legacy 分支只检查 `Atoi` 是否失败，**"`99999` 比 `65535` 新"被判定为更新**——恰好违反第 66 行 "Invalid versions never compare newer" 的承诺。一个畸形版本串能让更新检查误判为"有新版"。

**为什么当前的错误处理没兜住**

两条解析路径（`Parse` 与 legacy 正则）规则不一致，而 `Key` 负责兜住 legacy 输入——兜底本身没有兜底规则。

**建议改法**

```go
	for i, part := range strings.Split(value, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n > 65535 {
			return nil          // 与 Parse 保持同一上限
		}
		key[i] = n
	}
```

---

## D-26 `releaseversion` 的其余整洁度问题

**a) `--check` 的报错文件顺序不确定**

`desktop/internal/releaseversion/metadata.go:50`：`for path, desired := range contents {` —— Go 的 map 遍历顺序随机。`--check` 失败时（`:59`）报出的第一个失配文件每次运行都可能不同，CI 日志无法稳定复现。

```go
// metadata.go:50 与 :65 两处都改成确定顺序
paths := make([]string, 0, len(contents))
for path := range contents { paths = append(paths, path) }
sort.Strings(paths)
for _, path := range paths {
	desired := contents[path]
	// ...
}
```

**b) `Parse` 的错误信息不含出错的具体值**

`desktop/internal/releaseversion/version.go:29`：`return Version{}, fmt.Errorf("version components must be between 0 and 65535")` —— 用户不知道是哪个分量的哪个值。`match[i+1]` 就在手边。

```go
// version.go:28-30
if err != nil || n > 65535 {
	return Version{}, fmt.Errorf("version component %q exceeds the 0-65535 range", match[i+1])
}
```

**c) `os.WriteFile` 硬编码 0644 权限**

`desktop/internal/releaseversion/metadata.go:66`：已存在的文件会被改写为 0644，丢失原有的 ACL/只读属性。建议先 `os.Stat` 保留原 mode。

**d) `cmd/release-version/main.go:72` 未校验写入字节数**

```go
_, err = file.WriteString(metadata)
```
`os.File.Write` 本身会返回 `io.ErrShortWrite`，所以实际安全，但丢弃 `n` 让意图不明确。紧随其后的 `closeErr` 检查（`:73-79`）是本仓库中**写得最好的错误处理范式**——建议作为其他 `_ =` 位置的参考模板。

---

## 汇总

| 编号 | 严重度 | 文件 | 一句话 |
|---|---|---|---|
| D-01 | P0 | engineclient/client.go:492-498 | 事件通道 `default:` 静默丢弃，TUN/DNS 失败事件被吞 |
| D-02 | P0 | main.go:167-170 | `Snapshot()` 错误被当成"引擎已停止"，绕过检测守卫 |
| D-03 | P0 | engineclient/privileged_windows.go:119-120 | 提权核心清理失败被丢弃 → 孤儿管理员进程 |
| D-04 | P0 | engineclient/client.go:284-285, 290-291 | 会话丢弃时 `Kill()` 错误被丢弃 → 引擎进程残留 |
| D-05 | P0 | main.go:239-241 | `log.Fatal` 跳过全部服务清理 → 网卡/代理/进程残留 |
| D-06 | P1 | engineclient/client.go:486-516 | `scanner.Err()` 从不检查，真实传输错误被替换成常量文案 |
| D-07 | P1 | engineclient/launcher.go:132-146 | `Start()` 失败泄漏 3 个管道句柄 |
| D-08 | P1 | engineclient/service_windows.go:142-144 | `START_PENDING` 被当成未运行 → 多余 UAC + 竞争核心 |
| D-09 | P1 | engineclient/privileged_windows.go:253 | `SetReadDeadline` 错误丢弃 → 认证 goroutine 可能永久滞留 |
| D-10 | P1 | engineclient/privileged_windows.go:339-345 | `ShellExecuteExW` 失败原因取自错误的字段 |
| D-11 | P1 | platform/wails/desktop.go:85-92, 102-104 | 托盘定位失败被丢弃，用户只看到"位置不对" |
| D-12 | P1 | startup/wifi_windows.go:142-154 | 策略诊断遮蔽真实的 `WlanConnect` 失败码 |
| D-13 | P1 | platform/webview2_windows.go:23-34 | 权限错误被压成"WebView2 未安装"，误导用户 |
| D-14 | P1 | releaseversion/metadata.go:65-69 | 12 个文件顺序写入，中途失败无回滚、不原子 |
| D-15 | P1 | startup/privilege_windows.go:415-420 | 任何查询失败都被当成"任务不存在" → 幽灵自启动 |
| D-16 | P2 | client.go:251,259 / launcher.go:83 / main.go:374,377 / privilege_windows.go:294 | `%v` 拼接两个错误，破坏 `errors.Is` 链 |
| D-17 | P2 | engineclient/client.go:504-508 | `readLoop` 静默 `return`，零可观测性 |
| D-18 | P2 | platform/wails/appearance_windows.go:34-37 | DWM HRESULT 后拼接无意义的 `callError` |
| D-19 | P2 | startup/wifi_windows.go:62,127,146 / wails/tray_position_windows.go:29,33,36 | WLAN 与 Win32 返回码多处丢弃 |
| D-20 | P2 | startup/wifi.go:49 | XML 解析失败被误报为"没有可用网络" |
| D-21 | P2 | startup/privilege_windows.go:301,381,390,397,401,405,409 | 六处 `TerminateProcess` 错误丢弃 → 挂起进程残留 |
| D-22 | P2 | platform/autostart_windows.go:59-69 | 先清审批再写 Run 值，写失败无回滚 |
| D-23 | P2 | platform/webview2_windows.go:46-50 | 字符串转换失败可弹出全空白错误框 |
| D-24 | P2 | client.go:301 / launcher.go:147 / privileged_windows.go:256 / main.go:198 | 后台 goroutine 无 `recover` |
| D-25 | P2 | releaseversion/version.go:82-93 | legacy 分支不校验 65535，违反自身文档承诺 |
| D-26 | P2 | releaseversion/metadata.go:50,66 / version.go:29 / cmd/release-version/main.go:72 | 报错顺序不确定、硬编码权限、错误信息缺上下文 |