# engine/internal/{tun,wfp,vnic,platform} — 错误处理审计

> **本文件即 `engine-core` 分节**，覆盖：`engine/internal/tun/`、`engine/internal/wfp/`、
> `engine/internal/vnic/`、`engine/internal/platform/` 下的全部非测试 `.go` 文件。
> 同批次的 `engine/cmd/`（`engine-cmd.md`）、`engine/internal/proxy/`、`engine/internal/{dns,server,runtime,...}`
> 由并列的审计员覆盖，见姊妹文件；四份最终合并为 `reports/parts/engine.md`。

## 本节概览

这几个包的整体错误处理水平**明显高于仓库平均水平**：`tun.Supervisor` 有真正的两阶段状态机（`Activate` / `failStartedRun` / `terminateRun` / `cleanupRun`），回滚用 `errors.Join` 汇总，`cleanupRun` 用 `sync.Once` 保证幂等，`wfp.OpenDNSExemption` 用 WFP 事务 + dynamic session 让内核侧对象随句柄关闭自动消失。作者显然想过回滚问题。

问题高度集中在**三处系统状态改动点上**：(1) TUN sidecar 启动后、回滚机制接管前的那段"空窗期"；(2) **整个 `engine/` 没有任何一处 `recover()`**——对一个装 WFP 规则和虚拟网卡的特权守护进程，任何一处 panic 都会直接留下系统状态残留；(3) 用 PowerShell 子进程做系统清理的三个文件（`cleanup_windows.go`、`sharing_windows.go`、`mtu_windows.go`）**全部把子进程输出拼进 `%s` 而不是 `%w`**，且全部不区分"ctx 超时被 kill"和"脚本真的失败"。此外 `hasOwnedTunDevice` 的枚举错误被 `continue` 吞掉后**反而跳过了完整清理路径**，与其注释声明的 fail-safe 意图正好相反——这是本节最值得修的一处。

---

## P0

### P0-1 · `Activate` 在 `containProcess` 失败时跳过网络清理，留下 Wintun 网卡与默认路由

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

### P0-2 · `hasOwnedTunDevice` 把枚举错误吞成"没有残留设备"，导致完整清理路径被跳过

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

### P0-3 · `OpenDNSExemption` 失败路径丢弃 `Close()` 错误，泄漏 WFP 引擎句柄与子层

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

### P1-1 · 整个 `engine/` 没有任何 `recover()`，特权守护进程一次 panic 就留下系统状态残留

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

### P1-2 · `cleanupRun` 被 `ctx` 超时跳过时，`Stop` 返回的清理失败信息彻底丢失

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

### P1-3 · PowerShell 清理 / MTU / 共享检测把 `ctx` 超时报告成"脚本失败"，丢失全部因果

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

### P1-4 · `vnic.Manager.Create` 丢弃回滚错误，并对仍存活的虚拟网卡谎报 `failed`

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

### P1-5 · `vnic.Manager` 在最长 60 秒的阻塞调用期间持有 `m.mu`

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

### P1-6 · `FwpmGetAppIdFromFileName0` 返回 NULL 时未校验，把空指针交给内核

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

### P1-7 · WFP 所有 Win32 调用的错误用 `%s` 包装，`errors.Is` 永久失效

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

### P1-8 · TUN 就绪探测把 Win32 错误吞成"还没就绪"，20 秒后报一个不相关的原因

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

### P1-9 · 暂存配置的删除错误被吞，凭据文件可能留在 ProgramData

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

### P1-10 · `Stop` 无条件把状态重置为 `StatusStopped`，抹掉全部失败细节

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

### P2-1 · `wfp.call` 之外的 `UTF16PtrFromString` 错误全部丢弃

**位置**：`engine/internal/wfp/dns_exemption_windows.go:173-175`、`:206-209`、`:317-322`、`:323-325`

```go
	sessionName, _ := windows.UTF16PtrFromString("HypoMux temporary DNS egress exemption")
```

**说明**：这些都是硬编码 ASCII 字面量，`UTF16PtrFromString` 只在字符串含 NUL 时失败，实际不可能触发。`:317`、`:323` 的字符串含 `fmt.Sprintf` 的格式化结果，理论上如果 adapter 名含 NUL 会失败，但 adapter 名来自 JSON。

**改法**：可以统一改成包级 `var` + `init()` 里 panic（字面量，安全），或加注释说明为何可忽略。低优先级。

---

### P2-2 · `InspectWFP` 探测路径丢弃句柄关闭错误

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

### P2-3 · `containProcess` 的三条错误路径丢弃 `CloseHandle`

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

### P2-4 · `GetConsoleProcessList` 成功时把 `nil` 错误用 `%v` 印出来

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

### P2-5 · `fmt.Errorf` 无格式动词，应为 `errors.New`

**位置**：`engine/internal/platform/mtu.go:13`、`:19`、`:24`

```go
		return fmt.Errorf("invalid IPv4 MTU change")
```

**说明**：`go vet` 不报错，但 `staticcheck` 的 S1039 会标记，且多一次无用的格式解析。

**改法**：改为 `errors.New("invalid IPv4 MTU change")` 并在 `mtu.go` 引入 `errors` 包。

---

### P2-6 · `buildRules` 静默丢弃无法解析的适配器，依赖跨包的隐式校验

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

### P2-7 · `OpenDNSExemption` 丢弃 `os.Stat` 错误，路径错误与"是目录"不可区分

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