# engine/internal/proxy/ 错误处理审计报告

## proxy/ 概览

`engine/internal/proxy/` 是 HypoMux 的网络数据面：27 个非测试 `.go` 文件，包含一个多协议入口 `server.go`（SOCKS5 + HTTP 双栈 accept loop）、`socks.go`/`http.go` 两个握手与 CONNECT 解析器、`dial.go`/`dial_windows.go` 的带源地址绑定拨号、`udp.go` 的 SOCKS5 UDP ASSOCIATE 中继、10 个 `steam_*.go` 的 Steam CDN 候选发现/探测/观测/计速子系统，以及 `registry.go`/`scheduler.go`/`adaptive.go`/`latency.go`/`health.go` 的连接注册表与健康度调度栈。整体代码风格是**偏防御性的**：上游拨号 `dial.go:50-59` 有正确的 lease 回滚，Steam CDN 探测的每个阶段都有 `c.note(...)` 打点，锁顺序也被仔细维护（`scheduler.mu → performance.mu`、`cdn.mu → session.mu → performance.mu`，未发现真实死锁环）。主要问题集中在**三个断层**：一是 UDP 数据面有三条错误路径完全没有留下任何痕迹（`udp.go:211`、`udp.go:223`、`udp.go:429`），而这些路径恰恰会污染节点健康度统计；二是所有每连接 goroutine **都没有 `recover()`**，单个客户端就能通过畸形 SOCKS/HTTP 报文打崩整个引擎守护进程；三是 UDP ASSOCIATE 在默认配置下必然失败，且失败原因被伪装成"未知 channel"。共 19 条发现：**P0 6 条 / P1 8 条 / P2 5 条**。

---

# P0 — 静默失效 / 可被外部输入打崩

## P0-1 没有任何 per-connection goroutine 装 `recover()`，一个客户端即可打崩整个引擎

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

## P0-2 UDP 数据面创建 flow 的错误被完全吞掉，连日志都没有

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

## P0-3 复用已有 flow 时的发送错误被丢弃，死亡 flow 永不退役

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

## P0-4 `WriteToUDP` 失败与短写都不记录失败，死亡 flow 永不退役

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

## P0-5 acceptLoop 在持续性 Accept 错误下热自旋，打满一个 CPU 核

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

## P0-6 默认配置下 UDP ASSOCIATE 必然失败，且失败原因被伪装成"未知 channel"

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

# P1 — 错误难排查 / 状态不一致

## P1-7 上行方向（客户端 → 上游）的中继错误被丢弃，CDN 试运行永远不会标记失败

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

## P1-8 探测错误被替换成不可区分的通用字符串，再被字符串匹配消费

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

## P1-9 Steam 探测链路的 resolver 构造失败静默返回，完全不打点

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

## P1-10 UDP ASSOCIATE 已建立后，`serve` 的错误被丢弃且不再发 SOCKS 应答

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

## P1-11 UDP 关联的数据报泵在客户端 goroutine 上同步阻塞，单个慢目标即可拖垮整个关联通道

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

## P1-12 `Attach` 在释放 `session.mu` 之后才写入计数器，与 `Finish` 的无锁读构成竞态

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

## P1-13 Steam 发现 goroutine 未纳入 `s.wg`，`Stop` 超时路径还会泄漏一个看门狗 goroutine

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

## P1-14 `newSteamObserver` 与 `expire`/`feed`/`stopLocked` 的锁获取顺序相反，仅靠"发布时机"侥幸避免死锁

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

# P2 — 代码整洁度 / 可维护性

## P2-15 `handleClient` 的返回值被直接丢弃，`_ = adapter` 是死赋值

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

## P2-16 `writeHTTPError` 用 `_, _ =` 抹掉全部 HTTP 失败原因

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

## P2-17 `steam_accounting.go` 的失败窗口表容量上限在计数器已递增之后才判断

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

## P2-18 上下文取消后生产者循环不 `break`，继续空转迭代

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

## P2-19 observer 定时器自重入，唯一停止路径依赖调用方记得调用 `close()`

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