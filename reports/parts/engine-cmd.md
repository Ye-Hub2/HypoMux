# engine/cmd/hypomux-engine 错误处理审计

## 概览

`engine/cmd/hypomux-engine/` 是引擎进程的全部入口，共 8 个非测试文件、约 1100 行：`main.go`（CLI 分发 + `runServer`）、`pipe_windows.go` / `pipe_file_windows.go` / `pipe_other.go`（engine 作为**客户端**连接 desktop 宿主管道并做 token + 宿主 PID 认证）、`service_windows.go` / `service_policy_windows.go` / `service_session_windows.go` / `service_other.go`（Windows Service 安装/卸载、HKLM 策略键、命名管道服务端、WTS 会话校验）。

整体风格是**"检查每一个错误、句柄在每条失败路径上都关闭"**——`pipe_file_windows.go:24-35` 的 `newPipeFile`、`pipe_windows.go:80-136` 的握手清理都是逐路径显式 `_ = connection.Close()` 写出来的，说明作者有清理意识。问题不在于漏掉单个 `err != nil`，而在于**系统级事务没有边界**：安装路径做了「停服务 → 改配置 → 写注册表 → 设恢复动作 → 启动」五个不可原子化的系统改动，任何一步失败都没有对应的 undo，已经创建的服务、已经停掉的服务、已经写坏的策略键都会留在机器上。

第二个结构性问题在**退出路径**：唯一的网络状态回滚 `Server.stopProxyForHostExit`（`engine/internal/server/server.go:1012-1041`，由 `Run` 的 defer 触发，`server.go:97`）依赖进程内正常返回，而 Service 的 `Execute` 在 15s 超时后会主动让进程退出（`service_windows.go:265-270`），goroutine panic 同样直接杀进程——两条路都会跳过 DNS 豁免关闭、TUN 停止和虚拟网卡删除。

第三，错误**信息**层面的损失集中在三处：`fmt.Errorf("%w: %v", ...)`（`service_windows.go:340`）把真实错误降级成字符串、注册表 ACL 读取失败与「没有可信 DACL」共用一条消息（`service_policy_windows.go:435-437`）、WTS 调用失败时用可能为 0 的 errno 包装（`service_session_windows.go:46`）。

共 31 条发现：**P0 = 6，P1 = 11，P2 = 14**。

---

## P0 — 系统状态不一致 / 静默失效

### P0-1 `writeCoreServicePolicy` 写一半失败会摧毁原本可用的策略，且无回滚

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

### P0-2 `installWindowsService` 改了系统状态后没有 undo：新建服务失败不删除，更新路径失败后服务永远不再启动

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

### P0-3 Service 停机超时直接退出进程，跳过唯一的 DNS/TUN/VNIC 回滚，并回报成功

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

### P0-4 两个后台 goroutine 都没有 `recover()`，panic 会杀掉 LocalSystem 服务进程并跳过全部清理

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

### P0-5 `requireMachineInstallLocation` 把 `libcronet.dll` 的 stat 错误当成「文件不存在」，直接跳过 ACL 校验

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

### P0-6 `removeWindowsService` 的系统状态回滚不对称：卸载路径不留痕，删服务与删注册表键之间没有补偿

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

### P1-1 `fmt.Errorf("%w: %v", ...)` 把真实拒绝原因降级成不可追溯的字符串

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

### P1-2 注册表 ACL 读取失败被伪装成「没有可信 DACL」

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

### P1-3 认证握手 JSON 解析失败时原始错误被丢弃，无法区分「格式错」和「对端不是 HypoMux」

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

### P1-4 `deleteCoreServicePolicy` 用 `!=` 比较错误，破坏错误链

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

### P1-5 WTS 调用失败时用可能为 errno(0) 的 `callErr` 包装，产生「The operation completed successfully」

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

### P1-6 正常 Ctrl+C 退出被判为失败：退出码 1 + "context canceled"

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

### P1-7 `runtimeServerMetadata` 静默降级：路径解析失败时 `TunExecutable` 为空，无任何日志

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

### P1-8 `buildCoreServicePolicy` 的目录布局检查是恒真式，永不生效

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

### P1-9 `requireProtectedCoreACL` 对非普通 allow ACE 直接硬失败，错误信息误导

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

### P1-10 `stopWindowsService` 的错误丢失服务名与阶段信息，卸载路径直接裸传

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

### P1-11 管道连接没有写超时：对端不读时写入永久阻塞，优雅停止被拖到 15s 超时

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

### P2-1 `removeWindowsService` 中停服失败后仍留下已停止的服务
**位置**：`service_windows.go:130`
```go
	if err := stopWindowsService(service, 20*time.Second); err != nil {
		return err
	}
```
`:131` 裸传错误（已在 P1-10 计入）。此处额外记录：函数先 `defer service.Close()`（`:128`）再停服，顺序正确（句柄在服务停止后才释放），但停服失败时**没有报告服务当前仍处于什么状态**——用户无法判断是该重试还是该强杀。P0-6 已涵盖补偿缺失，此处仅补充「状态未知」这一点。

### P2-2 多处 `Close`/`Disconnect` 的返回错误被丢弃
**位置**：`service_windows.go:59` ``defer manager.Disconnect()``、`:96` ``defer service.Close()``、`:78`/`:83`/`:92` 处的 `service.Close()`（返回值完全未使用）；`service_policy_windows.go:96` ``defer key.Close()``、`:132` ``defer key.Close()``、`:261` ``defer file.Close()``、`:239` ``defer windows.CloseHandle(process)``；`pipe_file_windows.go:27` ``_ = windows.CloseHandle(handle)``、`:32` ``_ = file.Close()``。
这些在正常语义下无害，但 `windows.CloseHandle` 在句柄已被意外关闭（双重 close）时返回 `ERROR_INVALID_HANDLE`，恰恰是排查句柄生命周期 bug 的信号。建议在 Windows 构建下至少对关键句柄使用 `errors.Join` 汇总：
```go
defer func() { _ = manager.Disconnect() }() // 或在 run 的错误路径上 errors.Join(err, closeErr)
```
并把 `//nolint:errcheck` 标注在确实可忽略的位置，让后续 review 能区分「有意忽略」和「忘了看」。

### P2-3 `serve-pipe` 路径对同一条管道连接执行两次 Close
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

### P2-4 `pathWithin` 是死代码，唯一的安全不变量「所有受保护路径都在 ProgramData 根下」从未被校验
**位置**：`service_policy_windows.go:468`
```go
func pathWithin(root string, candidate string) bool {
```
全仓库只有 `service_policy_windows_test.go:95,98` 引用它，生产代码从不调用。`requireMachineInstallLocation`（`:302-342`）逐个校验 `paths` 里的绝对路径（reparse + owner + DACL），但从未断言它们**位于同一个根目录**下——函数名和注释（`:303-305`）承诺的「sidecars 与主程序同处一个 machine-owned 目录」只通过 `:318-321` 对**主 exe** 一条路径做了 `EqualFold` 断言，`:330-331` 的 `sing-box.exe`/`wintun.dll` 可以位于任何地方（只要各自 ACL 合法）。要么接上（对每个 `paths` 元素做 `pathWithin(programData根, path)`），要么删除，别留一个让人以为不变量已覆盖的函数。

### P2-5 `connectAuthenticatedPipe` 在 ctx 已取消时仍会先做一次 `CreateFile`
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

### P2-6 `requireFixedNTFSVolume` / `fixedLocalVolumeRoot` 的 `UTF16PtrFromString` 错误裸传
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

### P2-7 `validatePinnedSHA256` 把「非十六进制」和「长度不对」合并成一条消息
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

### P2-8 `interruptServicePipeConnection` / `connectServicePipe` 丢弃全部 Win32 错误
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

### P2-9 `newPipeFile` 的类型断言失败消息不含实际类型
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

### P2-10 `finalPathForHandle` 每次迭代重新分配缓冲区，且失败原因未区分「太长」与「空」
**位置**：`service_policy_windows.go:279`
```go
		buffer := make([]uint16, size)
```
循环上限 32768，最多重复分配 7 次（512→…→32768），每次失败都把已分配的大切片丢掉。建议复用同一个切片并 `slice = slice[:size]`；另外 `:299` 的 `"canonical path exceeds the Windows path limit"` 没有带上 `size`，排查时无从判断是超了 260 还是超了 32768。

### P2-11 `WTSFreeMemory` 的返回状态被忽略，且 `defer` 中调用的是 `Proc.Call`
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

### P2-12 `main.go` 的参数校验失败一律静默返回 2
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

### P2-13 `runWindowsService` 把服务启动失败报成普通退出码，SCM 侧看不出原因
**位置**：`service_windows.go:193-196`
```go
	if err := svc.Run(coreServiceName, host); err != nil {
		fmt.Fprintf(stderr, "run %s: %v\n", coreServiceName, err)
		return 1
	}
```
`svc.Run` 在无法向 SCM 注册处理器时失败。Service 模式下 stderr 往往无人采集，用户只会看到服务启动后立刻停止，事件日志里也没有有用的信息。建议同时把失败原因写进 Windows 事件日志，或至少把消息扩展为「SCM rejected the service host, see Event Viewer」。属于可排查性问题，不影响正确性。

### P2-14 `serveCoreServicePipe` 对被拒客户端无退避，`os.Stderr` 被刷屏
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
