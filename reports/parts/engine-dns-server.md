# 错误处理审计 — engine/internal/{dns,server,runtime,expiry,fileintegrity,diagnostic,protocol,api}

审计范围：上述包下全部非测试 `.go` 文件（17 个），并为定论读取了调用方
`engine/cmd/hypomux-engine/main.go`、`service_policy_windows.go`、`engine/internal/proxy/{server,dial,dial_windows,health,steam_cdn,steam_observed_probe}.go`、
`engine/internal/tun/{supervisor,config_stage_windows}.go`、`engine/internal/wfp/dns_exemption_windows.go`。
纯只读审计，未修改任何源文件，未运行构建或测试。

**总览.** 这次审计最核心的问题集中在 DNS 层：**错误分类被压平**。`engine/internal/dns/wire.go:112` 把所有非零 RCODE 压成一句 `DNS response code %d`，NXDOMAIN（域名不存在）、SERVFAIL（上游故障）和我们自己的解析错误在下游完全无法区分，而下游 `engine/internal/proxy/dial.go:86` 正好用这个 error 做 adapter 健康惩罚，`engine/internal/proxy/health.go:15` 的阈值只有 2 次。第二个系统性问题是**超时被当作"无事发生"**：`engine/internal/dns/resolver.go:282` 和 `:425` 的 `if ctx.Err() != nil` 早返回直接吞掉了刚产生的失败，`dohFailures`/`legacyFailures` 计数器不增长，`EventDNSFallbackRequired` 永远不触发，UI 上的 DNS 面板会说谎。第三个是**缺失回滚**，只有 `engine/internal/server/scheduling.go:44-58` 一处（失败点在 `:57` 的 `if err := s.proxy.UpdateScheduling(next); err != nil { return err }`）：新的 WFP 豁免已经装上、旧的已经拆掉之后才做可能失败的代理更新，没有任何还原。相比之下 `engine/internal/fileintegrity`、`engine/internal/tun`、`engine/internal/wfp` 的失败路径是干净的——我逐个读过调用方，没有发现哈希/签名校验被吞掉的情况，那里的问题只是错误信息不够丰富（P1-10，缺路径与摘要）。`engine/internal/runtime` 的 `Transition` 错误在 9 处调用点被 `_` 丢弃，但因为调用方都自己写了状态文本，属于可观测性问题而非状态不一致。

共 **27** 条发现：**P0 = 4，P1 = 11，P2 = 12**。

---

## P0 — 数据损坏 / 状态不一致 / 静默失效

### P0-1 超时类失败被 `ctx.Err()` 完全吞掉，DNS 遥测与 fallback 事件同时失真

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

### P0-2 RCODE 被压平成无类型字符串，NXDOMAIN / SERVFAIL 与自身故障不可区分，并连带污染 adapter 健康

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

### P0-3 WFP DNS 豁免替换失败后没有回滚，运行时状态与代理实际用的 adapter 池永久错位

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

### P0-4 DoH 竞速批次在 `ctx.Done()` 分支丢弃已经成功的结果，把成功变成失败

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

### P1-1 `SetDeadline` 失败被丢弃，`runLookup` 可能永久挂起并毒化 inflight 缓存键

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

### P1-2 `runLookup` goroutine 无 `recover`，一个 panic 会静默杀死查询并留下永不解开的 inflight 条目

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

### P1-3 DoH 非 200 响应体未排空，连接无法复用

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

### P1-4 主机关闭指令无条件回报成功，停机失败被静默吞掉

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

### P1-5 诊断探针把一次瞬时 bind 失败固化成"不可用"，健康链路被误报

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

### P1-6 ICMP 回显的完整状态码被整个丢弃，`Probe` 无法区分"超时"和"不可达"

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

### P1-7 ICMP 探针的 WinError 码在非 bind 失败场景被静默丢弃

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

### P1-8 `parseResponse` 丢弃 `skipName` 的底层错误，畸形报文与截断不可区分

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

### P1-9 NODATA 与解析失败被合并成同一条错误

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

### P1-10 文件完整性校验的错误不带路径和摘要，无法定位是哪个文件

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

### P1-11 `NormalizeConfig` 静默追加硬编码公共 DNS，运维显式配置的"仅用我的服务器"被覆盖

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

### P2-1 `normalizeRecordType` 的错误被丢弃

**位置** `engine/internal/dns/resolver.go:267` — ``_, wireType, _ := normalizeRecordType(recordType)``
上游是 `engine/internal/server/server.go:243` 校验过的 `dns.QueryType`（1/28/65/AAAA）。丢弃的错误在当前调用链上不可达，但契约上 `normalizeRecordType` 会返回 error（`dns/config.go` 侧），静默丢弃会让未来新增调用点时无声降级到 A 记录。建议：``wireType, err := normalizeRecordType(recordType); if err != nil { return Result{}, 0, err }``。

### P2-2 `context.AfterFunc` 返回的 stop 函数被丢弃

**位置** `engine/internal/dns/resolver.go:138` — ``context.AfterFunc(root, resolver.closeDoHTransports)``
（对比 `engine/internal/dns/doh_transport.go:41-47` 里写对了的 `stop := context.AfterFunc(r.root, cancel)` + `defer stop()`）
`AfterFunc` 返回的 stop 函数会**解除注册**，保留它可以在 root 长期存活但 Resolver 已不再使用时避免回调。建议：``stopClose := context.AfterFunc(root, resolver.closeDoHTransports); _ = stopClose // 或存入 Resolver 供 Close 使用``。

### P2-3 `resolver.go:122` 用 `fmt.Errorf` 拼无格式串的常量

**位置** `engine/internal/dns/resolver.go:122` — ``return nil, fmt.Errorf("DNS dial function is required")``
无占位符应用 `errors.New`，`go vet` 才能检出后续误加参数的 bug。`engine/internal/dns/config.go` 里 `"dial function is required"` 同样问题。

### P2-4 DoH 连接池淘汰只关空闲连接，在途请求的连接被遗留

**位置** `engine/internal/dns/doh_transport.go:88` — ``r.dohTransports[oldest].transport.CloseIdleConnections()``
淘汰发生在 `len(r.dohTransports) >= maxDoHTransports`(64) 时，此时旧 transport 可能正有 in-flight DoH 请求；`CloseIdleConnections` 不影响它们，请求完成后由 transport 自己的空闲定时器回收。行为可接受但会在切换端点时短暂堆积连接。建议改为 `r.dohTransports[oldest].transport.CloseIdleConnections()` 后配合对淘汰项的 `Close`，或把缓存上限调低并在文档里说明取舍。

### P2-5 `wire.go:139` 的负长度检查是死代码

**位置** `engine/internal/dns/wire.go:139` — ``if length < 0 || offset+length > len(packet) {``
`length` 来自 `binary.BigEndian.Uint16(...)[0:2]`，恒为 `0..65535`，`< 0` 永不成立。同样 `int(length)` 在 32/64 位平台上不可能溢出。建议改为 `if int(length) > len(packet)-offset {`（既去掉死分支，又避免 `offset+length` 的潜在整数溢出）。

### P2-6 `wire.go` 的假 IP 过滤只作用于 A 记录，AAAA 不对称

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

### P2-7 `server.go` 的 JSON 解析错误被替换成固定文案

**位置** `engine/internal/server/server.go:183` — ``return protocol.Failure("", "invalid_json", "request is not valid JSON", nil), false`` 与 `:186` — ``return protocol.Failure(request.ID, "invalid_json", "request contains trailing JSON", nil), false``；同类还有 `:243-244`（DNS 诊断参数错误被换成 `"invalid request"`）
`decoder.Decode` 的真实错误（含字段名/字节偏移）被丢弃，客户端拿到零信息的 `"request is not valid JSON"`。建议把 `err.Error()` 放进 `Details`（见 P2-8 的 Details 用法）。

### P2-8 内部错误的原文被直接返回给远端客户端

**位置** `engine/internal/server/server.go:258-259` — ``fmt.Errorf("dns start failed: %w", err)`` 的 `err.Error()` 被塞进 `Details["message"]`；`:426` — ``"message": err.Error()``（`startProxy` 失败）；`:728` — ``"message": errors.Join(err, tunStopErr, wfpErr, proxyStopErr).Error()``
把内部错误原文直接透给 sidecar/UI 是调试友好，但错误里可能含文件路径、adapter 名称甚至上游地址。建议改为对外给稳定错误码 + 摘要，内部细节走 `slog.Error` 并附带 `request.ID` 便于关联。

### P2-9 `runtime.Transition` 的错误在 9 处调用点被 `_` 丢弃

**位置** `engine/internal/server/server.go:413` — ``_, _ = s.runtime.Transition(StateRunning, ...)``、`:421`（`StateFailed`）、`:463`、`:485`、`:500`、`:512`、`:722`、`:1001`、`:1038`
`engine/internal/runtime/runtime.go:58-83` 的状态机逻辑本身是对的（`:63-65` 同状态 no-op 返回 nil，`:66-68` 非法转换返回带原因的错误）。但调用方同时用 `s.currentState()` 写了状态文案，所以行为不会错。问题在于：`runtime.go:24` 的 `Snapshot()` 结构体**没有 error 字段**，状态机的拒绝原因既进不了状态也无法上报。建议给 `Snapshot` 加 `LastTransitionError string`，或至少在这些点加 `s.log.Warn("runtime transition rejected", ...)`。

### P2-10 `host.exit` 停机路径全链路 `_ =` 丢弃

**位置** `engine/internal/server/server.go:1016` — ``_ = s.tun.Stop(tunCtx)``、`:1019` — ``_ = s.closeDNSExemption()``、`:1030` — ``_ = s.proxy.Stop(ctx)``；同类还有 `:969`/`:977`（`emitEvent`）、`:993`（`proxy.Stop`）、`:1008-1009`（TUN 事件）
这是"关进程路径尽力而为"的合理取舍，但**零日志**意味着：残留 TUN 设备 / WFP 句柄泄漏后完全无从排查。建议至少 `slog.Warn("proxy teardown step failed", "step", "tun.stop", "error", err)`。

### P2-11 DoH 传输的端点 URL 非法时静默丢弃，不报错

**位置** `engine/internal/dns/doh_transport.go` `New` 中构造 `endpoints` 时，解析失败只被跳过；`engine/internal/dns/resolver.go:348` 最终 `errors.Join(failures...)` 在只有一个非法 endpoint 时返回一条 `nil` 元素参与的 join。`engine/internal/dns/config.go` 的 `normalizeDoHEndpoints` 只把整体置空，不指出是哪一个。建议 `New` 返回 `([]dnsDoHEndpoint, error)` 并把坏 URL 一并列出来。

### P2-12 `SteamCDNStatus` 在代理未运行时返回"成功但空配置"

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