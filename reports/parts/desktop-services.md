# desktop/internal/services 错误处理审计

## 概览

`desktop/internal/services/` 是 HypoMux 桌面端的系统状态操作层——装卸 HNS/Hyper-V 虚拟网卡、增删防火墙规则、改 DNS 与系统代理、写 sing-box 配置、改路由表、启停后台进程。整体上**启动路径的回滚设计明显强于停止与配置持久化路径**：`engine.go` 的 `rollback()` 与 `settings.go` 的 legacy 迁移备份是本目录中少数几处真正做了"改前快照 + 失败恢复"的地方，`system_proxy_restore_windows.go` 的 journal 机制（先写 restoring 状态再改注册表）也相当严谨。最集中的问题区域有三个：**一是配置持久层**（`settings.go` / `atomic_file.go` / `support_log.go` / `appearance.go`）反复手写"临时文件 + rename"，其中 `support_log.go` 存在一次启动即清空全部历史日志的静默数据丢失，且**同一个原子写逻辑在目录内被复制了四份**、其中一份甚至漏掉 `Sync()`；**二是系统代理与 TUN 地址分配**在失败路径上只报"启动失败"而不恢复已改动的注册表/占用表；**三是网络状态的跨层传递**——`engine.go` 的 `Stop()` 在核心不可达时直接跳过全部拆除。goroutine 的 `recover` 覆盖率为零：`engine.go:268` 的长驻事件循环一旦 panic 会直接带崩整个桌面端。

**统计：31 条发现（P0 × 6 / P1 × 11 / P2 × 14）。**

---

## P0 — 数据损坏 / 系统状态不一致 / 静默失效

### P0-1 旧配置迁移前的备份被静默跳过，失败后回滚直接吞掉用户全部配置

**位置**
- `desktop/internal/services/settings.go:195` — `	if current, readErr := os.ReadFile(s.path); readErr == nil {`
- `desktop/internal/services/settings.go:202` — `	if err := s.commitLocked(migrated); err != nil {`
- `desktop/internal/services/settings.go:248` — `		restored = DefaultSettings()`

**触发条件**
`os.ReadFile(s.path)` 返回任何非 nil 错误——包括 `ERROR_ACCESS_DENIED`（配置目录权限被改、杀软占用、文件被同步盘锁住）、`ERROR_SHARING_VIOLATION`、路径是目录、磁盘 I/O 错误。只要不是 `ErrNotExist`，`:195` 的 `readErr == nil` 就为 false，备份分支整段被跳过，`backup` 保持 `""`，代码继续走到 `:202` 用迁移结果**直接覆盖 `s.path`**。

**后果**
用户的新版配置被覆盖且**磁盘上不存在任何副本**。`:206` 的 `if backup != "" && !s.migration.Applied` 因 `backup == ""` 而不成立，`:207` 也不会执行。此后若用户点"回滚旧版迁移"，`RollbackLegacyMigration` 走 `:226 if s.migration.BackupPath != ""` 的 false 分支，直接落到 `:248 restored = DefaultSettings()`——**新旧两代配置一起消失，UI 还显示"回滚成功"**。这是不可恢复的用户数据丢失。

**为什么没兜住**
对比同一函数上一段代码 `:186-189`：
```go
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return AppSettings{}, fmt.Errorf("读取旧版配置失败：%w", err)
	}
```
读旧文件失败会**中止整个迁移**，而读新文件失败却**继续迁移**。作者显然把"新配置不存在（首次启动）"和"新配置读不出来（故障）"当成了同一件事。前者是正常路径，后者必须中止。

**建议改法**
区分 `ErrNotExist` 与其它错误，读不出来就中止迁移并明确报错：
```go
	current, readErr := os.ReadFile(s.path)
	switch {
	case readErr == nil:
		backup = filepath.Join(settingsDirectory(), "settings.before-legacy-migration.json")
		if writeErr := os.WriteFile(backup, current, 0o600); writeErr != nil {
			return AppSettings{}, fmt.Errorf("备份新版配置失败：%w", writeErr)
		}
		s.migration.BackupPath = backup
	case errors.Is(readErr, os.ErrNotExist):
		// 首次启动，没有可备份内容，继续迁移。
	default:
		// 读不出来 ≠ 不存在。宁可让用户用默认配置启动，也不能在无备份的情况下覆盖。
		return AppSettings{}, fmt.Errorf("读取当前配置失败，已中止旧配置迁移以免覆盖现有设置：%w", readErr)
	}
```
并把 `:248` 的兜底改为显式报错（`return AppSettings{}, errors.New("没有可用的迁移前备份，无法回滚")`），避免"回滚成功但其实什么都没恢复"。

---

### P0-2 reload 把"旧配置读不出来"当成"没有旧配置"，默认值静默覆盖用户设置

**位置**
- `desktop/internal/services/settings.go:462` — `			return nil`
- `desktop/internal/services/settings.go:466` — `			return nil`
- `desktop/internal/services/settings.go:482` — `	if err != nil {`（对照：主配置读取**正确**返回了错误）

**触发条件**
新版 `s.path` 不存在（用户首次运行、或清理脚本删掉了配置），进入 `:459` 的 `ErrNotExist` 分支；随后 `legacyConfigPath()` 因 `APPDATA` 未设置/不可写返回错误（`:461` pathErr != nil），或旧配置文件因权限/占用/编码问题读不出来（`:465` legacyErr != nil）。

**后果**
`:462`/`:466` 返回 `nil` 表示**迁移成功**，调用方拿到的 `loadErr` 为空，于是 `load()` 继续执行 `commitLocked(DefaultSettings())`。用户的旧版代理端口、分流规则、订阅等**被默认配置静默替换且无任何提示**。用户视角的表现是"重启后我的配置全没了，界面上什么错都没报"。

**为什么没兜住**
与紧邻的 `:482-484` 构成刺眼的不对称：
```go
	if err != nil {
		return fmt.Errorf("读取设置失败：%w", err)
	}
```
主配置读取失败会带 `%w` 上抛，legacy 路径的两个失败分支却都 `return nil`。同一次 reload 里，两类语义完全不同的失败被分别编码成"错误"和"成功"。另外 `:470` 的 `s.loadErrorPath = legacyPath` 表明作者已具备记录失败路径的能力，只是没用在 `:461`/`:465` 这两条分支上。

**建议改法**
把两条 `return nil` 改为带上下文的错误，并在迁移前记录路径：
```go
		legacyPath, pathErr := legacyConfigPath()
		if pathErr != nil {
			s.loadErrorPath = legacyPath
			return fmt.Errorf("定位旧版配置失败，已跳过迁移：%w", pathErr)
		}
		legacyData, legacyErr := os.ReadFile(legacyPath)
		if legacyErr != nil {
			if !errors.Is(legacyErr, os.ErrNotExist) {
				s.loadErrorPath = legacyPath
				return fmt.Errorf("读取旧版配置失败，已跳过迁移（原文件未修改）：%w", legacyErr)
			}
			return nil // 确实没有旧配置，这是正常路径。
		}
```
这样"没有旧配置"和"读不出旧配置"在返回语义上就分开了。

---

### P0-3 enableSystemProxy 逐条改注册表却没有内部回滚，失败即留下半开状态的系统代理

**位置**
- `desktop/internal/services/system_proxy_windows.go:69` — `	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {`
- `desktop/internal/services/system_proxy_windows.go:72` — `	if err := key.SetStringValue("ProxyServer", serverValue); err != nil {`
- `desktop/internal/services/system_proxy_windows.go:75` — `	if err := key.SetStringValue("ProxyOverride", systemProxyBypass); err != nil {`
- `desktop/internal/services/system_proxy_windows.go:78` — `	if err := notifyProxyChanged(); err != nil {`
- `desktop/internal/services/system_proxy_windows.go:86` — `	if err := atomicWriteFile(proxyMarkerPath(), activeData, 0o600); err != nil {`

**触发条件**
三次 `Set*Value` 中任意一次失败。典型触发：注册表被其它进程并发写（杀软实时防护、其他 VPN 客户端、组策略刷新）、权限被组策略收紧、注册表配置单元被写坏。

**后果**
最坏情形：`ProxyEnable` 写成 1 成功，`ProxyServer` 写入失败 → 函数在 `:70` return。此时 **Windows 全局代理开关是打开的，但指向的仍是用户原来的代理地址或空值**，全机所有遵循系统代理的程序（浏览器、WinHTTP、部分更新器）全部断网，且没有任何组件负责恢复。`ProxyOverride` 写失败同理——代理开着但绕过列表是旧的，内网与 localhost 流量可能被错误地送进代理。`notifyProxyChanged()`（`:78`）失败更隐蔽：注册表已全改但 WinINet 未收到广播，Explorer 仍按旧配置工作，直到用户重启 explorer。

**为什么没兜住**
`:66` 已经把恢复点快照写进 marker 文件（State="prepared"），也就是说**回滚所需的信息是齐备的**，但 `:69`-`:88` 之间没有任何 `defer` 或错误分支去调用恢复。整个函数是"改一步查一步、错了就 return"的直线结构。唯一的补偿来自调用方 `engine.go:906` 的 `return rollback(err)`，而 `rollback()` 会调 `restoreSystemProxy()`——但这条补偿路径只有 `engine.Start()` 一处：任何其它直接调用 `enableSystemProxy` 的代码（未来新增的"仅开代理不开 TUN"入口、安装器流程）都没有这层保护，且用户看到的错误信息是"启动失败"，完全看不出"我刚才把系统代理改坏了"。

**建议改法**
在第一次改注册表之前挂 defer，把 marker 的 prepared 快照变成一个真正的承诺：
```go
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("启用系统代理失败：%w", err)
	}
	changed := true
	defer func() {
		if !changed {
			return
		}
		// 恢复点已由 :66 落盘，这里只回滚注册表并通知 WinINet。
		if restoreErr := restoreSystemProxy(); restoreErr != nil {
			systemProxyLogger.Printf("enableSystemProxy 部分失败后的恢复也失败了: %v", restoreErr)
		}
	}()
	if err := key.SetStringValue("ProxyServer", serverValue); err != nil { ... }
	if err := key.SetStringValue("ProxyOverride", systemProxyBypass); err != nil { ... }
	if err := notifyProxyChanged(); err != nil { ... }
	if err := atomicWriteFile(proxyMarkerPath(), activeData, 0o600); err != nil { ... }
	changed = false
	return nil
```
这样 `enableSystemProxy` 自带原子性，调用方是否记得调 `rollback()` 就不再影响正确性。

---

### P0-4 启动时一次瞬时读失败就把全部支持日志清空为零字节

**位置**
- `desktop/internal/services/support_log.go:87` — `	sessions := s.readSessionsLocked()`
- `desktop/internal/services/support_log.go:91` — `	s.rewriteLocked(sessions)`
- `desktop/internal/services/support_log.go:189` — `	if err != nil {`
- `desktop/internal/services/support_log.go:216` — `	content := strings.TrimSpace(strings.Join(sessions, "\n\n"))`
- `desktop/internal/services/support_log.go:221` — `	if os.WriteFile(temporary, []byte(content), 0o600) == nil {`

**触发条件**
`os.ReadFile(s.path)` 返回任何错误——最现实的是杀软实时扫描 / 云同步盘 / 备份工具短暂持有日志文件导致的 `ERROR_SHARING_VIOLATION`，或日志文件被临时重命名。

**后果**
`readSessionsLocked` 在 `:190` 返回 `nil`，这个 `nil` 传到 `:91` 的 `rewriteLocked`，`:216` 把它 join 成空字符串，`:221` 把空内容写进 `.tmp`，`:222` 一次 `os.Rename` 覆盖正式日志——**历史全部诊断日志归零，且无任何错误提示**。而 `Start()` 是每次引擎启动都会调用的入口，用户根本不知道自己刚丢掉了用于排查问题的唯一证据。讽刺的是，日志组件本身就是排查其他故障的工具，它的静默失效会让后续所有"难排查"类问题彻底失去证据。

**为什么没兜住**
`readSessionsLocked` 把"文件不存在"（首次运行，正常）和"文件读不出来"（故障）压缩成了同一个 `nil` 返回值；而 `rewriteLocked` 是破坏性写操作，它没有"输入为空时拒绝执行"的保护。同文件的 `:213` 已经对 `MkdirAll` 失败做了 `return`，说明作者知道要防写失败，但没防"被要求把内容清空"。

**建议改法**
让读取函数区分"没有"和"读不出"，并让破坏性重写在输入为空时拒绝执行：
```go
func (s *SupportLogStore) readSessionsLocked() ([]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // 确实还没有日志，属于正常情况。
		}
		return nil, err // 读不出来必须上抛，不能伪装成"没有内容"。
	}
	...
}

func (s *SupportLogStore) rewriteLocked(sessions []string) error {
	if len(sessions) == 0 {
		return nil // 没有可保留的会话时拒绝重写，避免把日志清空。
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	...
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, s.path)
}
```
`Start()` 相应改为：
```go
	sessions, err := s.readSessionsLocked()
	if err != nil {
		s.rates = map[string]rateLimitState{}
		s.active = true
		return false // 读失败就不做破坏性重写，append 仍可继续写。
	}
```
（`appendLocked` 用 `O_APPEND|O_CREATE`，读失败不影响后续追加，只是保留旧内容。）

---

### P0-5 强制启动时把全部地址冲突检查错误丢弃，可能把 TUN 分配到用户真实局域网网段

**位置**
- `desktop/internal/services/tun_address.go:31` — `	if err != nil && !force {`
- `desktop/internal/services/tun_address.go:49` — `		return nil, fmt.Errorf("检查 TUN 地址冲突失败：%w", err)`
- `desktop/internal/services/tun_address.go:161`（回退池）— `	for _, pool := range []string{"172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"} {`

**触发条件**
用户勾选"强制启动"（`ForceStart`），且 `occupiedTunNetworksForStart` 返回非 nil 错误——该函数对每个网络接口分别检查地址，遇到任何一个接口的检查失败就用 `errors.Join` 汇总后**同时返回部分结果和非 nil 错误**。`net.Interfaces()` 本身失败（`:47-50`）时更极端：`occupied` 直接是 `nil`。

**后果**
`:31` 的条件在 `force == true` 时恒为 false，`err` 被整个丢弃，`occupied` 可能是 `nil` 或残缺的网段列表。随后的 `selectTunIPv4Address` 会基于这张残缺/空表挑地址，并在池子用尽后回退到 `:161` 的 `192.168.0.0/16`——**如果用户的物理局域网恰好在 192.168.x.x，而那个接口的地址检查正好失败了，分配器会选中与真实局域网重叠的 TUN 地址**。TUN 带着 auto_route 起来后会抢走内网路由，用户表现为"一开代理就访问不了路由器/打印机/NAS"，且因为失败被静默丢弃，日志里没有任何线索。

**为什么没兜住**
`:34-35` 的注释把设计意图写得很清楚：
```go
	// Even a forced start respects known address allocations. Only incomplete
	// inspection is bypassed; actual inability to allocate an address is not.
```
但实现没能兑现这句注释——"incomplete inspection is bypassed"被实现成了"error 整个丢掉、`occupied` 是否完整完全不看"。调用方 `tun_config.go:217 availableTunIPv4Address(options.ForceStart)` 与 `tun_config.go:224 availableTunIPv6Address` 都直接把结果交给 `engine.go:930`，中间没有任何一层检查被检查过。

**建议改法**
强制模式下至少保留错误用于诊断，并让回退池的推进基于"确实检查过"的前提：
```go
func inspectedTUNAddress(ipv6, force bool, inspect func(bool) ([]netip.Prefix, error)) (string, error) {
	occupied, err := inspect(ipv6)
	if err != nil {
		if !force {
			return "", err
		}
		// 强制模式：继续分配，但必须留下证据，否则这次分配事后无法解释。
		if tunAddressInspector != nil {
			tunAddressInspector(fmt.Sprintf("TUN 地址占用检查不完整，已忽略部分错误: %v", err))
		}
	}
	if len(occupied) == 0 {
		return "", fmt.Errorf("无法确定 TUN 地址占用情况（%w），拒绝在未知网络上分配地址", err)
	}
	if ipv6 {
		return selectTunIPv6Address(occupied)
	}
	return selectTunIPv4Address(occupied)
}
```
关键改动是新增的 `len(occupied) == 0` 守卫：占用表完全为空时绝不分配，这一条就能挡住最危险的情形。

---

### P0-6 Stop() 在核心不可达时跳过全部拆除，虚拟网卡与路由可能残留

**位置**
- `desktop/internal/services/engine.go:1137` — `	if ensureErr == nil {`
- `desktop/internal/services/engine.go:1179` — `	if ensureErr != nil && firstError == nil {`

**触发条件**
`s.client.Ensure(ctx)` 返回错误——核心进程启动失败、端口被占用、核心二进制损坏、或 75 秒生命周期上下文超时。典型场景：用户点"停止"，此时核心刚好崩溃或正在重启。

**后果**
`:1137` 的 `if ensureErr == nil` 意味着 `tun.deactivate`、`engine.stop` **一次都不尝试**，直接跳到 `:1163` 的 `restoreSystemProxy()`。虚拟网卡、静态路由、DNS 劫持项都不会被拆除。如果 sing-box 子进程作为孤儿存活并持有网卡句柄，用户界面显示"已停止"，实际系统状态却是 TUN 仍在接管流量——这是典型的**界面状态与系统状态不一致**，且要等用户重启电脑或手动清理才可能恢复。

**为什么没兜住**
`:1179` 只是把 `ensureErr` 记为 `firstError` 返回。也就是说函数**正确地告知了调用方"出错了"**，但没有为这个错误提供任何补偿动作。对比启动路径：`:862` 的 `rollback()` 明确为每一步配置了 undo，而停止路径的失败分支只有上报、没有 undo。作者在启动侧建立了"回滚纪律"，却没把它复制到停止侧。

**建议改法**
拆除动作不应依赖核心可达——它们本来就是清理残留，即使核心死了也必须执行：
```go
	hello, ensureErr := s.client.Ensure(ctx)
	var firstError error
	if ensureErr != nil {
		firstError = fmt.Errorf("核心不可达，仍将强制清理网络状态：%w", ensureErr)
	}
	// 无论核心是否可达都要拆除：这些调用清理的是"上一次运行留下的残留"。
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()
	if hello != nil {
		if _, err := s.tun.Deactivate(cleanupCtx); err != nil && !isInvalidStateError(err) {
			firstError = errors.Join(firstError, err)
		}
	} else if err := s.tun.Deactivate(cleanupCtx); err != nil {
		// 核心不可达时的降级路径：至少记录日志，供支持日志收集。
		s.logs.Record(fmt.Sprintf("核心不可达，TUN 拆除结果未知: %v", err), true)
		firstError = errors.Join(firstError, err)
	}
```
关键是把"核心可达"从拆除的前置条件降级为一条附加信息。

---

## P1 — 错误难排查

### P1-1 Stop() 中核心预停止失败被完全丢弃，仍继续 engine.start

**位置**
- `desktop/internal/services/engine.go:818` — `		_ = s.client.Request(ctx, "engine.stop", nil, &ignored)`

**触发条件** `status.Engine.State == "failed"` 且 `engine.stop` 请求失败（核心半死不活、上下文超时）。

**后果** 一个运行在 failed 状态的旧核心实例被留在原地，紧接着 `:825` 又向它发 `engine.start`。旧的残留实例与新实例争抢 TUN 网卡和监听端口，导致后续的 `engine.start` 失败或行为诡异，而真正的失败原因（预停止失败）在这条路径上完全不可见。

**为什么没兜住** 变量被显式命名为 `ignored`，说明是**有意**丢弃的——但"忽略清理错误"和"忽略启动错误"在严重程度上完全不同，后者会让诊断失去唯一线索。建议区分处理：预停止失败应当**中止启动**并把错误上抛，而不是继续。

**建议改法**
```go
	if status.Engine.State == "failed" {
		var ignored any
		if err := s.client.Request(ctx, "engine.stop", nil, &ignored); err != nil {
			return rollback(fmt.Errorf("核心处于 failed 状态且预停止失败，已中止重启以免残留实例争抢网卡：%w", err))
		}
	}
```

---

### P1-2 consumeCoreEvents 长驻 goroutine 没有任何 recover，panic 会直接终止桌面端

**位置**
- `desktop/internal/services/engine.go:268` — `func (s *EngineService) consumeCoreEvents() {`
- `desktop/internal/services/engine.go:282` — `			s.handleDNSFallback(fallback)`
- `desktop/internal/services/engine.go:288` — `		s.handleWFPCompatibility(status)`
- `desktop/internal/services/engine.go:293` — `		s.logs.Record("event="+string(data), true)`

**触发条件** 这三个回调中任意一处 panic：`handleDNSFallback` 与 `handleWFPCompatibility` 内部会调用完整的 `s.Stop()` / `s.Start()`（含 adapter 操作、JSON 持久化、通道索引），是整个服务里最容易 panic 的两条路径。

**后果** Go 中 goroutine 内的 panic **无法被外部捕获，会直接终止整个进程**。用户在升级核心或切换模式的中途看到桌面端闪退，且因为崩溃发生在系统状态已改动、尚未回滚的时刻，TUN/代理/防火墙可能处于中间态——这正是本次审计最关心的"改了一半"场景。

**为什么没兜住** 整个目录里 `recover()` 的使用接近于零，唯独这条长驻循环最需要它。

**建议改法**
```go
func (s *EngineService) consumeCoreEvents() {
	...
	for {
		select {
		case <-s.ctx.Done():
			return
		case event, ok := <-s.events:
			if !ok {
				return
			}
			s.consumeCoreEventGuarded(event)
		}
	}
}

func (s *EngineService) consumeCoreEventGuarded(event engineCoreEvent) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// 带上事件上下文，否则事后无法定位。
			s.logs.Record(fmt.Sprintf(
				"core event handler panic: %v | category=%s event=%s data=%s",
				recovered, event.Category, event.Event, truncate(string(event.Data), 512)), true)
		}
	}()
	// 原有 switch 逻辑
}
```
注意 recover 里**必须把 event 内容带上**，裸 `log.Panicf("recovered")` 等于什么都没记。

---

### P1-3 Shutdown() 直接丢弃 Stop() 的全部错误

**位置**
- `desktop/internal/services/engine.go:1203` — `	_, _ = s.Stop()`

**触发条件** 退出应用时 `restoreSystemProxy()` 或 `tun.deactivate` 失败。

**后果** 用户点了退出，界面直接关闭，`firstError`（含"恢复系统代理失败"的完整错误链）被扔掉。虽然 `system_proxy_windows.go:66` 的 marker 文件已落盘、下次启动 `engine.go:232` 的 `restoreSystemProxyDetailed()` 会兜底，但用户**当场完全不知道自己的系统代理是开着的**——他退出后所有程序继续走代理，直到下次启动 HypoMux。这是典型的静默失效。

**为什么没兜住** `_, _ =` 是显式声明"我不打算处理"。但在退出路径上，至少应该写进支持日志或弹一个通知，让用户有机会当场处理。

**建议改法**
```go
func (s *EngineService) Shutdown() {
	if _, err := s.Stop(); err != nil {
		s.logs.Record(fmt.Sprintf("退出时清理网络状态失败，系统代理或 TUN 可能残留：%v", err), true)
		// 不能只记日志：至少要把恢复点保留提示给用户。
		if strings.Contains(err.Error(), "代理") {
			// 视项目现有通知机制弹一次 toast
		}
	}
}
```

---

### P1-4 support_log 的写入失败全部静默，日志组件自己坏掉时零信号

**位置**
- `desktop/internal/services/support_log.go:234` — `	_, _ = file.WriteString(strings.TrimRight(line, "\r\n") + "\n")`
- `desktop/internal/services/support_log.go:227` — `	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {`
- `desktop/internal/services/support_log.go:231` — `	if err != nil {`（OpenFile 失败）
- `desktop/internal/services/support_log.go:221` — `	if os.WriteFile(temporary, []byte(content), 0o600) == nil {`

**触发条件** 磁盘满、配额超限、日志文件被占用、日志目录权限变更。

**后果** 磁盘写满时 `WriteString` 的字节数和 `err` 双双被丢弃（`:234`），日志**静默停止增长**，而 UI 和用户都不会收到任何提示。等到用户报障并要求导出日志时，拿到的是一份残缺且"看起来正常"的日志文件——比没有日志更具误导性。`:227`/`:231`/`:221` 同理：整个日志存储可以完全不可用，而没有任何一处报错。

**为什么没兜住** `appendLocked` 的签名是 `func (line string)`，没有返回值，结构上就无法把错误传给调用方。`:236` 的 `s.enforceLimitLocked()` 还会继续执行，仿佛一切正常。

**建议改法** 至少记录到进程内的标准错误，并让 `Record` 能感知失败：
```go
func (s *SupportLogStore) appendLocked(line string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("准备日志目录失败：%w", err)
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("打开支持日志失败：%w", err)
	}
	if _, err := file.WriteString(strings.TrimRight(line, "\r\n") + "\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入支持日志失败：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭支持日志失败：%w", err)
	}
	s.enforceLimitLocked()
	return nil
}
```
若不想改签名，最低限度也要把 `file.WriteString` 的 err 交给 `log.Printf`——这个组件是排查其它故障的最后手段，它自己坏了必须是可观测的。

---

### P1-5 代理设置锁在失败时静默降级为无锁，且全程零日志

**位置**
- `desktop/internal/services/system_proxy_lock_windows.go:34`（MkdirAll 失败）→ `return func() {}`
- `desktop/internal/services/system_proxy_lock_windows.go:56` — `			if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) || !time.Now().Before(deadline) {`
- `desktop/internal/services/system_proxy_lock_windows.go:58` — `				return func() {}`

**触发条件** 锁文件目录创建失败（权限）、锁文件打开失败，或 2 秒内未取得锁（`proxySettingsLockTimeout = 2 * time.Second`）——后者在另一进程正在改代理、或某个 `RestoreFrom` 恢复流程耗时超过 2 秒时都会发生。

**后果** 无锁进入意味着 `enableSystemProxy` 与 `restoreSystemProxy` 可能并发读写同一个注册表键，产生经典的 lost update：一方刚写完 `ProxyEnable=1`，另一方立刻恢复成 `ProxyEnable=0`，最终状态取决于竞态。**更麻烦的是这个降级完全不可见**——没有日志、没有返回的 warning，事后排查"为什么代理时开时关"没有任何线索。

**为什么没兜住** `:29-32` 的注释（不锁比拒绝恢复用户代理好）说明了设计取舍，对 **restore** 路径是合理的。但同一个 `acquireProxySettingsLock` 也被 `enableSystemProxy` 复用，而**启用代理时无锁是可以拒绝的**——启动本来就该失败并报错。同时 `errors.Is(err, windows.ERROR_LOCK_VIOLATION)` 的 else 分支把"非锁冲突错误"和"超时"混为一谈：前者（如 `ERROR_ACCESS_DENIED`）是完全不同的故障，却被当作"锁拿不到"处理。

**建议改法** 让降级可见并区分错误类型：
```go
	opened, err := windows.OpenFile(lockPath, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		log.Printf("system proxy lock: 打开锁文件失败，将以无锁方式继续: %v", err)
		return func() {}
	}
	...
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
		_ = file.Close()
		switch {
		case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
			log.Printf("system proxy lock: %v 内未取得锁（被其它进程占用），以无锁方式继续", proxySettingsLockTimeout)
		default:
			log.Printf("system proxy lock: 非锁冲突错误 %v，以无锁方式继续", err)
		}
		return func() {}
	}
```
更好的做法是给锁函数加一个 `required bool` 参数：`restore` 传 false（宁可无锁也要恢复），`enable` 传 true（拿不到锁就返回错误中止启动）。

---

### P1-6 tun_config 在聚合核心缺少 direct 通道时静默改变出站拓扑

**位置**
- `desktop/internal/services/tun_config.go:177` — `	if directPort, directErr := loopbackPort(endpoints, "direct"); directErr == nil {`
- `desktop/internal/services/tun_config.go:178` — `		directOutbound = socksOutbound("direct", directPort)`

**触发条件** 聚合核心返回的通道列表里 `direct` 通道缺失、端口非法，或 `loopbackPort` 解析失败。

**后果** `directOutbound` 保持为 `:176` 的 `map[string]any{"type": "direct", "tag": "direct"}`，即 sing-box 原生的 direct 出站。**所有标记为 `direct` 的分流规则（局域网直连、游戏平台直连、CDN 直连等）从此不再经过用户的上游链路**，流量路径发生实质变化。用户会看到"某些网站变慢了""绕过某些代理后走了另一条线"，而配置生成过程没有任何提示。

**为什么没兜住** 同一个文件里其它三处 `loopbackPort` 调用（`:83-94`、`:191-194` 附近）都是**正常上抛错误**的，只有这一处用 `== nil` 把失败静默转成了"用默认值继续"。同一文件里对同一个函数的三种不同错误策略，说明这是遗漏而非有意为之。

**建议改法** 与同文件其它调用点保持一致，失败即上抛：
```go
	directPort, directErr := loopbackPort(endpoints, "direct")
	if directErr != nil {
		return configArtifact{}, fmt.Errorf("解析 direct 通道端口失败，分流规则的直连路径会失效：%w", directErr)
	}
	directOutbound := socksOutbound("direct", directPort)
```

---

### P1-7 appearance.json 读取错误被吞，损坏时丢失去旧背景文件引用

**位置**
- `desktop/internal/services/appearance.go:70` — `	current, _ := s.readDocumentLocked()`

**触发条件** 现有 `appearance.json` 因写入中断（断电、写盘失败）或被手工编辑破坏导致 JSON 解析失败。

**后果** `current` 退化为零值结构，`current.BackgroundFile` 为空。随后用户设置一张新背景并成功提交（`:44` 的 `atomicWriteFile`），旧背景图片**失去了唯一的引用**但仍留在磁盘上，成为孤儿文件。长期使用会在 `%APPDATA%` 下累积无人认领的图片。

**为什么没兜住** `_` 明确表示作者知道这里会失败，但选择继续。一个损坏的文档不应导致"静默把已知信息清零后再写入"。

**建议改法** 读失败时备份损坏文件并明确告警，而不是当作空文档处理：
```go
	current, readErr := s.readDocumentLocked()
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		// 先保住损坏文件，避免其中记录的背景图路径彻底丢失。
		corrupt := filepath.Join(settingsDirectory(), "appearance", "appearance.corrupt.json")
		_ = os.Rename(appearanceDocumentPath(), corrupt)
		return fmt.Errorf("外观配置已损坏，已另存为 %s 并重置：%w", corrupt, readErr)
	}
```

---

### P1-8 支持背景图时若 stat 因权限失败，会留下一张永不被引用的孤儿文件

**位置**
- `desktop/internal/services/appearance.go:96` — `	_, statErr := os.Stat(backgroundPath)`
- `desktop/internal/services/appearance.go:97` — `	if os.IsNotExist(statErr) {`

**触发条件** `os.Stat` 返回的不是 `ErrNotExist`，而是权限错误或路径过长。

**后果** `os.IsNotExist(statErr)` 为 false → 代码认为"文件已存在"→ 走另一分支。此时新背景文件的去向取决于 JSON 提交是否成功；若提交失败，孤儿文件产生（同 P1-7 的成因）。更麻烦的是：文件**确实存在**但 stat 失败的情况下，代码会认为它存在并跳过删除，而用户看到的背景却不是这张——状态不一致且无提示。

**为什么没兜住** `os.IsNotExist` 只匹配"不存在"，其它 stat 错误被隐式归入了"存在"分支，这是 Go 里非常常见的一类错误处理缺陷。

**建议改法** 显式处理未知错误：
```go
	_, statErr := os.Stat(backgroundPath)
	switch {
	case statErr == nil:
		// 已存在，沿用。
	case errors.Is(statErr, os.ErrNotExist):
		newBackground = backgroundPath
	default:
		// 无法判断：既不登记引用也不承诺保留，交由上层报错。
		return fmt.Errorf("检查背景文件失败，未做任何改动：%w", statErr)
	}
```

---

### P1-9 windowsFirewallEnabled 在注册表读取失败时谎报"防火墙已开启"

**位置**
- `desktop/internal/services/nat_firewall_windows.go:65` — `	return true`

**触发条件** 打开防火墙注册表键失败（`HKLM` 权限受限、企业策略锁定、安全软件干扰）。

**后果** 函数返回 `true`，调用方据此认为防火墙处于开启状态，从而**跳过"需要提醒用户检查防火墙"这类分支**，或认为"不需要额外配置"。实际上防火墙状态未知。用户侧表现为"程序说防火墙没问题，但 NAT 相关功能就是不通"，排查方向被误导。

**为什么没兜住** fail-open 的默认值在这里是错的：无法读取防火墙状态 ≠ 防火墙开启。虽然从"不要打扰用户"的角度 `true` 是保守选择，但从"不要给出错误结论"的角度它是有害的。

**建议改法** 返回三态或至少把错误传出去：
```go
func windowsFirewallEnabled() (bool, error) {
	key, err := registry.OpenKey(...)
	if err != nil {
		return false, fmt.Errorf("读取防火墙配置失败：%w", err)
	}
	...
}
// 调用方：
enabled, err := windowsFirewallEnabled()
if err != nil {
	// 状态未知 ≠ 已开启，显式提示。
	return fmt.Errorf("无法确认 Windows 防火墙状态：%w", err)
}
```

---

### P1-10 legacy 配置各字段的 JSON 解码错误被逐字段静默丢弃

**位置**
- `desktop/internal/services/settings.go:597` — `	decode := func(key string, target any) { if payload := raw[key]; payload != nil { _ = json.Unmarshal(payload, target) } }`

**触发条件** 旧配置中某个字段类型不匹配（例如 `TUNStack` 存成了数字、`BypassMode` 存成了对象）。

**后果** 逐字段静默失败后，目标字段保留 `defaults` 的零值，用户看到的是"这项设置被重置了"而不是"你的配置有格式错误"。多个字段同时损坏时，配置被大面积替换且用户无从得知是哪几项。

**为什么没兜住** 闭包把 `json.Unmarshal` 的 error 直接丢给 `_`，设计上就没打算报告。注意 `:525` 对 `TUNStack` 的处理（`if err == nil {...} else { 用了默认值 }`）至少说明了作者知道解析会失败——但同样没有诊断输出。

**建议改法** 收集失败字段名，一次性报告：
```go
	var failures []string
	decode := func(key string, target any) {
		payload := raw[key]
		if payload == nil {
			return
		}
		if err := json.Unmarshal(payload, target); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", key, err))
		}
	}
	// ... 各 decode 调用 ...
	if len(failures) > 0 {
		log.Printf("旧配置存在无法解析的字段，已使用默认值：%s", strings.Join(failures, "; "))
	}
```

---

### P1-11 autostart 注册表查询失败被吞，UI 开关与真实状态不符

**位置**
- `desktop/internal/services/settings.go:160` — `	if enabled, err := s.autostartEnabled(); err == nil {`

**触发条件** `autostartEnabled()` 查询 `HKCU\...\Run` 失败——注册表配置单元被策略锁定、或该值被删除/损坏。

**后果** `err != nil` 时 `result.Autostart` 保持持久化设置里的旧值。用户看到的开机自启开关**可能与真实注册表状态不符**：显示"已开启"但实际不会自启，或反之。用户据此决策（"反正它会自启"）就会踩坑。

**为什么没兜住** 这个查询是对**外部可变状态**的读取，读不到就沿用陈旧缓存值且不告知用户。设置服务的其它读操作（`:482-484`）都正确处理了错误。

**建议改法**
```go
	enabled, err := s.autostartEnabled()
	if err != nil {
		log.Printf("读取开机自启状态失败，界面将显示持久化值而非真实状态：%v", err)
	}
	result.Autostart = enabled
```
即无论成功与否都用真实查询结果，只在失败时记录告警；若返回值语义允许，最好再加一个 `AutostartUnknown` 标志让 UI 能显示"状态未知"。

---

## P2 — 代码整洁度

### P2-1 atomicWriteFile 的 defer 内丢弃 Close 与 Remove 错误，且临时文件名固定导致并发写互相截断

**位置**
- `desktop/internal/services/atomic_file.go:22` — `			_ = file.Close()`
- `desktop/internal/services/atomic_file.go:25` — `			_ = os.Remove(temporary)`
- `desktop/internal/services/atomic_file.go:9` — `	temporary := path + ".tmp"`

**触发条件** Close 失败（磁盘满时的最后一道关卡）、Remove 失败（杀软占用 `.tmp`）；并发场景下两个调用同时写同一 `path`。

**后果** Close 错误被丢弃意味着 `Sync()` 之后的数据落盘失败完全不可见——这正是配置文件损坏的典型成因。并发写同一路径时，两个调用用**同一个** `.tmp` 名：`B` 的 `O_TRUNC` 会截断 `A` 正在写的文件，随后两个 Rename 竞争，最终内容是 A 的前半段或 B 的内容，**配置静默损坏**。

**为什么没兜住** 目录内已经存在四份几乎一样的原子写实现（见 P2-2），这份是其中最完整的一份，但依然用了固定临时名——Go 标准库 `os.CreateTemp` 的存在就是为了解决这个竞态。

**建议改法**
```go
func atomicWriteFile(path string, data []byte, permission os.FileMode) (err error) {
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return fmt.Errorf("准备目录 %s：%w", filepath.Dir(path), mkErr)
	}
	handle, openErr := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if openErr != nil {
		return fmt.Errorf("创建临时文件：%w", openErr)
	}
	temporary := handle.Name()
	defer func() {
		if err != nil {
			if rmErr := os.Remove(temporary); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				log.Printf("清理临时文件 %s 失败：%v", temporary, rmErr)
			}
		}
	}()
	if _, wErr := handle.Write(data); wErr != nil {
		_ = handle.Close()
		return fmt.Errorf("写入 %s：%w", temporary, wErr)
	}
	if sErr := handle.Sync(); sErr != nil {
		_ = handle.Close()
		return fmt.Errorf("同步 %s：%w", temporary, sErr)
	}
	if cErr := handle.Close(); cErr != nil {
		return fmt.Errorf("关闭 %s：%w", temporary, cErr)
	}
	if rErr := os.Rename(temporary, path); rErr != nil {
		return fmt.Errorf("替换 %s：%w", filepath.Base(path), rErr)
	}
	return nil
}
```
（用命名返回值 `err` 简化 defer 条件。另外建议在 Rename 成功后对父目录做一次 `Sync`，保证目录项本身落盘。）

---

### P2-2 原子写逻辑在目录内被复制了四份，其中一份漏掉 Sync()

**位置**
- `desktop/internal/services/settings.go:760` — `	temporary := path + ".tmp"`（第三份）
- `desktop/internal/services/tun_config.go:285` — 第三/四份，写入路径 `os.WriteFile(temporary, data, 0o600)` + `os.Rename`，**无 `file.Sync()`**

**触发条件** 正常路径。

**后果** `tun_config.go` 那份**没有 `Sync()`**，意味着 sing-box 配置在 rename 后内容可能仍在页缓存里；一旦此刻断电/蓝屏，重启后配置文件存在但内容为空或截断，代理以损坏配置启动。反过来看，四份实现的行为不一致意味着**修复一处 bug（例如 P2-1 的并发问题）不会惠及其它三处**。

**为什么没兜住** 目录里明明已经有 `atomicWriteFile` 这个公共函数，但它只被 4 个调用点使用，而另外几处各自重写了一遍。

**建议改法** 删除 `settings.go:752-788 writeSettingsFile` 和 `tun_config.go:285-292` 的实现，全部改调 `atomicWriteFile(path, data, 0o600)`。收敛到单一实现后，P2-1 提到的 `CreateTemp` + `Sync` 修复只需要做一次。

---

### P2-3 tun_config 在 SetBypass 写盘前就把摘要返回给调用方

**位置**
- `desktop/internal/services/tun_config.go:275` — （`options.ConfigSHA256` 在此处即被赋值）
- `desktop/internal/services/tun_config.go:290` — `			_ = os.Remove(temporary)`

**触发条件** `:278`（Marshal 后校验）或 `:286`（WriteFile）失败。

**后果** 调用方 `engine.go:955` 拿到一个非空摘要却发现配置没落盘，于是 `return rollback(...)`。摘要值已经污染了 `options`，若上层把它记进日志或状态，后续诊断会指向一个从未存在过的配置文件。`:290` 的 `os.Remove` 错误被丢弃，写失败时残留的 `.tmp` 无声堆积。

**为什么没兜住** 摘要的赋值时机早于提交时机，"计算"与"提交"被混在同一个函数里，没有区分。

**建议改法** 把摘要作为返回值而非出参，在文件成功 rename 之后才交给调用方：
```go
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := atomicWriteFile(configPath, data, 0o600); err != nil {
		return "", fmt.Errorf("写入 sing-box 配置失败：%w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil // 只在真正落盘后才返回摘要。
```

---

### P2-4 外观设置提交新 JSON 后才删旧图，且删除失败静默

**位置**
- `desktop/internal/services/appearance.go:121` — `		_ = os.Remove(filepath.Join(settingsDirectory(), "appearance", filepath.Base(current.BackgroundFile)))`
- `desktop/internal/services/appearance.go:76` — `			_ = os.Remove(newBackground)`

**触发条件** 正常切换背景；或 `:76` 处回滚删除新背景时文件仍被占用。

**后果** 删除失败静默 → 孤儿图片累积（磁盘占用 + `appearance.go:180` 的 HTTP 路由可能仍能通过旧路径命中）。`:123` 的 `return s.loadLocked()` 若失败，旧图已被删、JSON 已提交，用户刷新后背景丢失且无回滚机会。

**为什么没兜住** 新旧文件切换的两步操作（提交 JSON + 删除旧文件）没有失败语义：第二步的失败被当成成功。

**建议改法** 至少把删除失败记入日志，并考虑"新配置提交成功才算成功"的定义：
```go
	if old := filepath.Join(settingsDirectory(), "appearance", filepath.Base(current.BackgroundFile)); old != "" {
		if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			// 删除旧背景失败不影响本次切换，但会留下孤儿文件，需要可见。
			log.Printf("删除旧背景文件失败 %s（可能残留孤儿文件）：%v", old, err)
		}
	}
```

---

### P2-5 背景图损坏时返回 404 而不是 500，把数据损坏伪装成"不存在"

**位置**
- `desktop/internal/services/appearance.go:180` — `	if err != nil || document.BackgroundFile == "" {`

**触发条件** `appearance.json` 解析失败，或引用的背景文件确实不存在。

**后果** 两种完全不同的情况（配置损坏 vs 文件缺失）返回同一个 404。前者意味着用户配置数据已损坏，需要修复；后者是正常情况。合并处理让"配置损坏"这个问题永远不会被上报。

**为什么没兜住** 短路 `||` 把两个不同语义的错误合并了，错误来源在写出响应时就已丢失。

**建议改法**
```go
	document, err := s.loadLocked()
	if err != nil {
		http.Error(w, "外观配置读取失败", http.StatusInternalServerError)
		return
	}
	if document.BackgroundFile == "" {
		http.NotFound(w, r)
		return
	}
```

---

### P2-6 WFP 兼容性失败状态的持久化错误全部被丢弃

**位置**
- `desktop/internal/services/engine.go:350` — `	_ = s.settings.RememberWFPCompatibilityFailure(status.LastError)`
- `desktop/internal/services/engine.go:585`、`:589`、`:591`
- `desktop/internal/services/engine.go:1029` — `	_ = s.settings.ClearWFPCompatibilityFailure()`

**触发条件** 配置文件不可写（权限、目录被删、磁盘满）时触发上述任一调用。

**后果** 这些调用把"WFP 不兼容"这一关键诊断状态写入设置文件。写失败意味着**故障已经发生但状态没被记录**，用户下次打开 UI 看不到"上次因为 WFP 不兼容失败"的提示，排查线索丢失。同理 `ClearWFPCompatibilityFailure` 写失败会让已恢复的状态一直显示为失败。

**为什么没兜住** 这些是"记录诊断信息"而非"控制流程"的副作用调用，作者可能认为失败无所谓——但对这个工具而言，WFP 兼容性正是最常见的用户报障原因。

**建议改法**
```go
	if err := s.settings.RememberWFPCompatibilityFailure(status.LastError); err != nil {
		// 记录失败本身也要可见：至少写进支持日志。
		s.logs.Record(fmt.Sprintf("持久化 WFP 兼容性失败状态失败：%v", err), true)
	}
```
四处统一处理。

---

### P2-7 核心事件的 JSON 反序列化失败静默丢弃事件

**位置**
- `desktop/internal/services/engine.go:281` — `			if json.Unmarshal(event.Data, &fallback) == nil {`
- `desktop/internal/services/engine.go:286`、`:292`

**触发条件** 核心发送了不符合预期结构的事件数据（版本不匹配的旧核心、核心 bug）。

**后果** 事件被丢弃，**`handleDNSFallback` 与 `handleWFPCompatibility` 都不会被触发**。DNS 回退配置不会应用、WFP 不兼容状态不会被处理，用户遇到的是"功能不生效"而非报错，且核心事件流里没有任何线索说明"有一条事件本该处理但格式不对"。

**为什么没兜住** `== nil` 内联判断把错误完全消解，只保留了成功路径。

**建议改法** 记录被丢弃的事件内容：
```go
			var fallback coreDNSFallback
			if err := json.Unmarshal(event.Data, &fallback); err != nil {
				s.logs.Record(fmt.Sprintf("解析 core 事件失败，已丢弃: category=%s err=%v data=%s",
					event.Category, err, truncate(string(event.Data), 256)), true)
				break
			}
			s.handleDNSFallback(fallback)
```

---

### P2-8 启动失败回滚后残留已写出的 runtime/sing-box.json

**位置**
- `desktop/internal/services/engine.go:955` — `	if configDigest == "" {`
- `desktop/internal/services/engine.go:970`（IPv4 回退配置写入失败的分支）
- `desktop/internal/services/engine.go:884`（`rollback()` 中收集 `tun.deactivate` 错误处）

**触发条件** 配置已成功落盘、但后续步骤失败（端口探测失败、IPv4 回退配置写失败、TUN 未进入 running 状态）而进入 `rollback()`。

**后果** `rollback()` 正确撤销了 TUN 和系统代理，但**不删除已经写出的 `runtime/sing-box.json`**。残留的配置文件不含密钥（不含订阅内容时）但会让下次诊断时"配置文件存在却没生效"变得费解，且其中的分流规则可能已过时。

**为什么没兜住** `rollback()` 的清理清单覆盖了网络层与代理层，但把配置文件视为"可随时覆盖的派生产物"，没有纳入回滚范围。

**建议改法** 在 `rollback()` 中把配置文件纳入清理（注意不要删用户配置目录，只删 runtime 派生产物）：
```go
	// 清理本次启动写出的派生产物，避免下次诊断时被过期配置误导。
	if runtimeConfig := filepath.Join(configDirectory(), "runtime", "sing-box.json"); runtimeConfig != "" {
		if removeErr := os.Remove(runtimeConfig); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupFailures = append(cleanupFailures, fmt.Errorf("清理残留的运行配置失败：%w", removeErr))
		}
	}
```

---

### P2-9 notifyProxyChanged 错误用 %v 包装，丢失错误链

**位置**
- `desktop/internal/services/system_proxy_windows.go:172` — `		return fmt.Errorf("通知 Windows 刷新代理失败：%v", callErr)`

**触发条件** 任何一次代理变更后的 WinINet 刷新失败。

**后果** 错误链被打断，`errors.Is(err, syscall.Errno(...))` 无法命中。由于这个函数在 P0-3 里被 defer 恢复路径调用，错误链的丢失会让上层**无法区分"权限不足"和"WinINet 忙"**，只能看到一句无信息量的中文提示。

**为什么没兜住** 目录内其它错误包装（`settings.go:188`、`system_proxy_windows.go:64`/`:67`/`:70`）都用的是 `%w`，只有这一处用了 `%v`。

**建议改法** 改成 `%w`：
```go
		return fmt.Errorf("通知 Windows 刷新代理失败：%w", callErr)
```

---

### P2-10 支持日志的 JSON 序列化错误被丢弃，事件上下文可能整体丢失

**位置**
- `desktop/internal/services/support_log.go:110` — `		data, _ := json.Marshal(context)`
- `desktop/internal/services/support_log.go:124` — `	data, _ := json.Marshal(payload)`

**触发条件** `context`/`payload` 中含有 `json` 无法序列化的值。

**后果** `:111`/`:125` 会把 `string(data)`（data 为 nil 时是空串）写进日志，产生一条没有上下文的畸形记录 `session_context=` 或 `event=`，而不是一条报错。这会让人误以为"上下文为空"，实际是序列化失败了。

**为什么没兜住** 未处理 marshal 错误；`engine_throughput.go` 已经用 `elapsed > 0` 守卫排除了 NaN/Inf 这个最常见的触发源，所以实际风险较低，但防御深度不足。

**建议改法**
```go
	if len(context) > 0 {
		data, err := json.Marshal(context)
		if err != nil {
			data = []byte(fmt.Sprintf("<marshal 失败: %v>", err))
		}
		s.appendLocked("session_context=" + sanitizeLogText(string(data)))
	}
```

---

### P2-11 ShellExecute 所需的 UTF-16 指针创建错误被丢弃

**位置**
- `desktop/internal/services/nat_firewall_windows.go:144` — `verb, _ := windows.UTF16PtrFromString("runas")`
- `desktop/internal/services/nat_firewall_windows.go:145` — `file, _ := windows.UTF16PtrFromString("netsh.exe")`

**触发条件** 参数含无法转换为 UTF-16 的字符（当前是常量，实际不会发生）。

**后果** 错误时 `verb`/`file` 为 nil，后续 `ShellExecuteExW` 会收到空指针，可能触发原生层崩溃或返回无法理解的错误码。

**为什么没兜住** 这是典型的"这行不会失败所以 `_` 没问题"的假设，但一旦将来把常量换成变量（例如从配置读取要提升权限的可执行文件路径），就会变成真实的崩溃点。

**建议改法**
```go
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("构造提权动词失败：%w", err)
	}
	file, err := windows.UTF16PtrFromString("netsh.exe")
	if err != nil {
		return fmt.Errorf("构造提权程序路径失败：%w", err)
	}
```

---

### P2-12 路径解析错误在多处静默丢弃，最终只报"未找到"

**位置**
- `desktop/internal/services/tun_config.go:130` — `	if absolute, absoluteErr := filepath.Abs(candidate); absoluteErr == nil {`
- `desktop/internal/services/tun_config.go:541` / `:545` — `os.Executable()` / `os.Getwd()`
- `desktop/internal/services/tun_config.go:556` — `os.Stat`

**触发条件** 当前工作目录被删除（`os.Getwd` 返回 `ENOENT` 是真实存在的场景：用户删掉了终端当前目录）、可执行文件路径过长。

**后果** `:130` 失败时该候选路径被静默跳过——进程 bypass 规则**少一条**，而这种缺失会改变哪些进程走代理。同理 `:541`/`:545` 失败时 sing-box 可执行文件定位失败，最终用户只看到一句"未找到 %s"，看不到真正的错误原因。

**为什么没兜住** 错误被吞后，上层只能看到一个笼统的最终错误，诊断链在中间就断了。

**建议改法** 把错误累积起来与最终错误一起返回：
```go
	var lookupErrs []error
	// ...
	if executable, err := os.Executable(); err != nil {
		lookupErrs = append(lookupErrs, fmt.Errorf("定位当前可执行文件失败：%w", err))
	} else {
		candidates = append(candidates, filepath.Dir(executable))
	}
	// ...
	return "", fmt.Errorf("未找到 %s：%w", name, errors.Join(append([]error{fmt.Errorf("已尝试 %d 个路径", len(candidates))}, lookupErrs...)...))
```

---

### P2-13 非法 TUNStack 值静默改为默认值，无任何诊断

**位置**
- `desktop/internal/services/settings.go:525` — `		if stack, err := normalizeTunStack(loaded.TUNStack); err == nil {`（else 分支设默认值）

**触发条件** `settings.json` 中 `tunStack` 字段值不在允许集合内（手工编辑、从旧版本升级、配置文件被改坏）。

**后果** 配置被静默改写为默认值。用户在设置界面看到的 stack 与自己配置的完全不同，且没有任何提示说明"你的配置无效，已被重置"。

**为什么没兜住** `else` 分支只做赋值，没有记录。

**建议改法**
```go
		} else {
			log.Printf("settings.json 中的 tunStack=%q 不是合法值，已回退到默认 %q：%v", loaded.TUNStack, defaults.TUNStack, err)
			loaded.TUNStack = defaults.TUNStack
		}
```

---

### P2-14 SetAutostart 的回滚错误与主错误用 %w/%v 混拼，可用 errors.Join 合并

**位置**
- `desktop/internal/services/settings.go:369-373`（回滚分支中 `%w` 与 `%v` 并用）

**触发条件** 开启自启失败后回滚也失败。

**后果** 回滚错误用 `%v` 拼入，错误链断开，调用方无法 `errors.Is` 命中回滚失败的底层原因（比如权限不足）。同时一条错误里混装两个独立失败，不利于分类统计。

**为什么没兜住** 作者选择了字符串拼接而非 `errors.Join`。

**建议改法**（与 `engine.go:899` 已有的 `errors.Join` 风格保持一致）
```go
	return AppSettings{}, fmt.Errorf("设置开机自启失败：%w；回滚原状态同样失败：%w", enableErr, rollbackErr)
```
或直接：
```go
	return AppSettings{}, errors.Join(
		fmt.Errorf("设置开机自启失败：%w", enableErr),
		fmt.Errorf("回滚原状态失败：%w", rollbackErr),
	)
```

---

## 正面结论（供报告参考）

- **`engine.go:862-902` 的 `rollback()`** 是本目录里最完整的失败恢复实现：用**新的** 25 秒 `cleanupCtx`（而非复用已超时的启动 ctx）、用 `recordCleanupFailure` 逐条收集 `tun.deactivate` 与 `engine.stop` 的错误、跳过 `invalid_state` 这类可忽略错误、最后用 `fmt.Errorf("%w；启动失败后的网络回滚不完整：%v", cause, errors.Join(cleanupFailures...))` 把原始错误和回滚失败**同时**上抛。
- **`engine.go:784-812` 的模式回滚 defer**：只在 `settings.Mode != mode` 时才持久化，并在 `returnErr != nil` 时恢复旧模式、记录 `mode_restore_failed`。defer 在 `rollback()` 之后执行（LIFO），意味着"先清网络、后改持久化模式"的顺序是正确的。
- **`system_proxy_restore_windows.go:105` 先写 journal（Version=2, State="restoring", RestoreFrom=&current）再改注册表**：即使进程在恢复中途被杀死，下次启动也能依据 journal 继续恢复。`:90-95` 对 active marker 用严格的 `matches = current == from` 比对，`:96-101` 检测到用户手工改过代理就只删 marker 而不覆盖用户设置，`:143` 通知失败时保留 marker 以支持幂等重试——这是本目录中最接近"崩溃安全"的实现。
- **`engine.go:1196` 的 `errors.Join(firstError, hotspotErr)`**、**`system_proxy_recovery.go` 把 `RecoverSystemProxy()` 转发给 `--recover-network` 与安装器**，让"用户已经手动卸载了应用但代理还开着"这一常见场景有独立的修复入口。

---

## 已核查、确认无问题

- **`engine_throughput.go:18-24`**：`telemetrySample.update` 用 `valid := ... && elapsed > 0 && elapsed < 30*time.Second` 守卫后，才在 `:32`/`:33`/`:39`/`:40` 做 `float64(...)/elapsed.Seconds()`。`elapsed > 0` 排除了除零，`elapsed` 为 `time.Duration` 不会产生 NaN/Inf，因此 `support_log.go` 的 `json.Marshal` 不会因 NaN 而失败——相关怀疑已排除。
- **`system_proxy_restore_windows.go` 全文**：恢复路径的 journal、marker 归属校验、用户改动保护、幂等重试均正确，未发现回滚缺失。

## 待补（本轮未覆盖）

以下文件因时间限制未完成逐行审查，若需完整覆盖请安排第二轮：

- `adapters.go`、`adapter_metadata*.go`、`hyperv_adapter.go`（121KB，最大文件）、`hyperv_adapter_windows.go` —— **虚拟网卡安装/卸载的主战场，回滚缺失的高发区，建议优先补**
- `network_routes_windows.go`、`routing.go` —— 路由表增删与持久化
- `nat_servers.go`、`nat_detection.go` —— NAT 服务器 socket 生命周期与监听 goroutine
- `singbox_rules.go`、`blocked_domains.go`、`rule_sets*.go`、`steam_cdn.go` —— 规则集下载/解析与原子写
- `hotspot.go`、`hotspot_preferences.go`、`hotspot_windows.go`、`process_windows.go` —— 进程启停与端口释放
- `tun_startup_dns.go`、`tun_preflight_windows.go`、`tun_connectivity.go`、`tun_dns_egress_windows.go`、`tun_local_network.go`、`tun_network_observer.go` —— DNS 改动回滚、preflight、连通性探测
- `diagnostics*.go`、`diagnostic_probe_windows.go`、`mtu_windows.go`、`wfp_fingerprint_windows.go`、`system_tools_windows.go`、`scheduling.go`

已用于本轮扫描的搜索 pattern（可直接复用）：

```
^\s*(_\s*=\s*|.*, _ :?= |.*, _ = ).*\b(Remove|Close|Stop|Delete|Set|Rename|Release|Deactivate|StopProcess|Kill|Uninstall|restore|Restore|Shutdown|Unlock)\w*\(
```