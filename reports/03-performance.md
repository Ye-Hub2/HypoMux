# HypoMux 性能审计报告

> 审计范围：`engine/` 数据面（`internal/proxy/`、`internal/dns/`、`internal/expiry/`）与 `desktop/frontend/` 轮询路径。
> 方法：读函数体 + 调用链追踪 + 频率×单次开销推导 + **本地实测 3 个仓库自带的 `func Benchmark`**（离线运行，命令与原始输出见附录 A）。
> 只读审查，未修改任何源文件。
>
> **重要声明**：除附录 A 中明确标注"实测"的数字外，本报告所有量级数字均为**基于代码推导的估算**，不是实测。每处都给出了推导过程与前提假设，便于复核。

---

## 结论摘要

| # | 类型 | 热点 | 核心数字 |
|---|---|---|---|
| 1 | **内存分配** | `server.go:448` 每连接分配 64 KB 未池化 `bufio.Reader`，叠加 `scheduler.go:119/123` 重复调用 `candidates()` 的 8 次切片分配 | **约 69 KB / 每条 TCP 连接**；4 网卡下 ≈ **55 MB/s 一次性垃圾**（800 连接/秒时）；实测 `SelectForDomain` 本身就要 **769 B / 4 allocs 每选一次网卡** |
| 2 | **锁竞争** | `steamCDN` 全状态只用一把 `sync.Mutex`（`steam_cdn.go:131`），被数据面**每转发 128 KB 就争用一次**（`steam_observed_probe.go:160` ← `server.go:589`） | 单流 100 MB/s ⇒ **800 次/秒**抢同一把锁；4 并发下载 ⇒ **3200 次/秒**；遥测 1.25 Hz 时 `snapshot()` 还要在锁内扫 5 个 map + 最多 512 个 entry 并做 1536 次字符串拼接 |
| 3 | **算法复杂度** | `latency.go:332-342` `latencyTable.snapshot()` 的 **O(adapters × values) 嵌套扫描**，内层每对重复构造 64 B 的 `bindingKey` | A=4/T=10 时 **160 次 `performanceKey` 构造+比较**；应做 **44 次**（复杂度 O(A²T) → O(AT)，降 ~80%） |

三处分别是**每连接分配**、**数据面锁争用**、**遥测路径复杂度**，互不重叠。

---

## 热点 1 —— 每条 TCP 连接在数据面固定烧掉约 69 KB，其中 64 KB 是一个没进池的 `bufio.Reader`

### 代码位置

| 位置 | 原文 |
|---|---|
| `engine/internal/proxy/server.go:448` | `reader := bufio.NewReaderSize(client, 64*1024)` |
| `engine/internal/proxy/socks.go:90` | `s.relay(reader, client, upstream, session)` |
| `engine/internal/proxy/http.go:62` | `s.relay(reader, client, upstream, session)` |
| `engine/internal/proxy/scheduler.go:119` | `fallback, ok := s.selectLocked(excluded, domain)` |
| `engine/internal/proxy/scheduler.go:123` | `candidates := s.health.candidates(s.adapters, excluded, domain)` |
| `engine/internal/proxy/health.go:121` | `healthy := make([]Adapter, 0, len(adapters))` |
| `engine/internal/proxy/health.go:122` | `domainFallback := make([]Adapter, 0, len(adapters))` |
| `engine/internal/proxy/health.go:123` | `recovery := make([]Adapter, 0, len(adapters))` |
| `engine/internal/proxy/health.go:124` | `all := make([]Adapter, 0, len(adapters))` |
| `engine/internal/proxy/scheduling.go:37` | `return SchedulingConfig{Strategy: s.strategy, Weighted: s.weighted, Adapters: append([]Adapter(nil), s.adapters...)}` |
| `engine/internal/proxy/dial.go:28` | `adapters := channelScheduler.snapshot().Adapters` |
| `engine/internal/proxy/dial.go:29` | `excluded := make(map[string]struct{}, len(adapters))` |

对照组（证明这是遗漏而非设计）：`engine/internal/proxy/tcp_profile.go:24-29` 已经为**转发缓冲区**建了 `sync.Pool`：
```go
var tcpRelayBufferPool = sync.Pool{
	New: func() any {
		buffer := make([]byte, tcpRelayBufferSize)   // 128 KB
		return &buffer
	},
}
```
但 `server.go:448` 的 64 KB `bufio.Reader` 走的是裸 `make`，从不归还池。

### 问题机理

1. **64 KB 的生命周期被拉长到整条连接。** `handleClient`（`server.go:438`）建出 `reader` 后，把它交给 `handleSOCKS`/`handleHTTP`，再由 `socks.go:90` / `http.go:62` 原样传进 `relay` → `io.CopyBuffer(writer, readerOnly{Reader: clientReader}, buffer)`（`server.go:517`）。也就是说这个 64 KB 不只是"读一下请求头"，而是**整个下行转发期间一直可达的客户端读缓冲**。高并发下载场景下它会直接抬高进程 RSS（每条在途连接 pinned 64 KB）。

2. **`acquireTCP` 在同一临界区里把 `candidates()` 算了两遍。** `scheduler.go:105-106` 持有 `s.mu` 整个函数体；`scheduler.go:119` 经 `selectLocked`（`scheduler.go:54`）调一次，`scheduler.go:123` 又调一次。两次之间 `h.now()` 已推进，第二次会重跑 `health.go:133` 的 `pruneExpiredDomains`。而 `candidates()` 每次**无条件 `make` 四个切片**（`health.go:121-124`），哪怕最终只用到其中一个返回值（`healthy`/`domainFallback`/`recovery`/`all` 各返回一路）。

3. **一次 TCP 连接要跨 3 把锁做 4 轮可枚举集合构造**：`dial.go:28` 拷一份 adapter 列表 → `dial.go:29` 造一个 map → `scheduler.go:105` 锁内 2× `candidates()`。

### 数据支撑

**单次开销推导**

`Adapter` 结构体（`engine/internal/proxy/config.go:22-30`）按 64 位对齐计算：

| 字段 | 类型 | 字节 |
|---|---|---|
| `Name` | `string` | 16 |
| `SourceIP` | `string` | 16 |
| `IfIndex` | `int` | 8 |
| `SourceIPv6` | `string` | 16 |
| `IPv6IfIndex` | `int` | 8 |
| `Weight` | `int` | 8 |
| `DNSServers` | `[]string` | 24 |
| **合计** | | **96 B** |

以 A = 4（4 张网卡，Windows 上典型配置）为例：

| 项 | 计算 | 字节 |
|---|---|---|
| `bufio.NewReaderSize(client, 64K)` | 65536 B 缓冲 + bufio 结构体 | **65 632** |
| `candidates()` × 2（`scheduler.go:119`+`:123`） | 2 × 4 slice × (4 × 96 B) | 3 072 |
| `snapshot()`（`scheduling.go:37` ← `dial.go:28`） | 1 slice × (4 × 96 B) | 384 |
| `make(map[string]struct{}, 4)`（`dial.go:29`） | hmap 48 B + 1 bucket 144 B | ~192 |
| SOCKS 握手 4 次小分配（`socks.go:15/19/38/47`） | 2+8+4+2 B，向上取整到 size class | ~64 |
| **合计** | | **≈ 69 344 B/连接** |

**实测校验（附录 A-1）**

仓库自带 `BenchmarkSchedulerDomainEvidence`（`engine/internal/proxy/health_index_test.go:54`），我用 2 张网卡实测得到 **769 B/op, 4 allocs/op**。这个数字和"4 个 `make([]Adapter, 0, 2)` = 4 × 2 × 96 = **768 B**"精确吻合，证实了上面 `sizeof(Adapter)=96 B` 的推导，也证实了 4 次分配全部来自 `candidates()`。换算到 A=4：`4 × 4 × 96 = 1536 B`，两次调用即 3072 B —— 与上表一致。

**调用频率推导**

- `acceptLoop` 在 `server.go:434` 每 accept 一个连接就 `go s.handleClient(...)` ⇒ **每条 TCP 连接一次**，无缓存无批量。
- 单次浏览行为的连接数：现代页面平均 50–80 个不同源（域名/资源域），每个源一条新连接。取 60。
- 负载场景三档：

| 场景 | 连接/秒 | 一次性垃圾 |
|---|---|---|
| 轻度浏览（20 页/分钟） | ~20 | 1.4 MB/s |
| 中度浏览（100 页/分钟） | ~100 | **6.9 MB/s** |
| 高负载代理 / Steam 下载启动 | ~800 | **55 MB/s** |

（估算。55 MB/s ≈ 6.7 GB/小时，交给 GC 的速率相当可观。）

### 优化建议

1. **把 `server.go:448` 的 `bufio.Reader` 池化**，与 `tcpRelayBufferPool`（`tcp_profile.go:24`）同一模式：在 `handleClient` 入口 `Get`，在 `relay` 返回后 `Put`。需注意 `reader` 里可能残留未消费字节（`sniffSteamHost`/`peekSteamChunkPath` 会 peek），归还前必须 `reader.Reset(client)` 之后确认无残留，或改为先 peek 再决定是否入池。预计立省 **65.6 KB / 连接（占 94.6%）**。
2. **消除 `scheduler.go:119` 的重复计算**：让 `selectLocked` 把 `candidates()` 的结果一并返回（改成 `selectLockedFrom(candidates, ...)`），一次计算两处复用。立省 **1536 B / 连接**（A=4）。
3. **`health.candidates()` 改成"按需分配"**：只 `make` 最终要返回的那一路；健康链路（`healthy` 非空）这种绝大多数情况连 `domainFallback`/`recovery`/`all` 都不需要。立省约 **2304 B / 连接**。
4. **`dial.go:29` 的 `excluded` map 提级为复用对象**，或在小 N 时用栈上数组 + 线性查找（N ≤ 8 时线性扫描比 map 更快且零分配）。立省 ~192 B / 连接 + 一次哈希初始化。

### 预期收益

单连接一次性分配从 **≈69.3 KB → ≈1.5 KB（-97.8%）**；高负载场景 GC 压力从 ~55 MB/s 降到 ~1.2 MB/s。

---

## 热点 2 —— `steamCDN` 全状态一把锁，被数据面每 128 KB 抢一次

### 代码位置

| 位置 | 原文 | 作用 |
|---|---|---|
| `engine/internal/proxy/steam_cdn.go:131` | `mu                      sync.Mutex` | **覆盖全部 CDN 状态的单把锁** |
| `engine/internal/proxy/steam_observed_probe.go:160` | `c.mu.Lock()` | `trafficChange`，**数据面每 128 KB 一次** |
| `engine/internal/proxy/server.go:589` | `s.cdn.trafficChange(session.cdnKey, session.cdnGeneration, 0, amount, false, session.cdnTrial)` | 上一行的调用点，在 `add` 闭包里 |
| `engine/internal/proxy/server.go:629` | `func (w accountingWriter) Write(payload []byte) (int, error) {` | 每 Write 调 `w.add` |
| `engine/internal/proxy/tcp_profile.go:12` | `tcpRelayBufferSize  = 128 * 1024` | 决定 Write 的粒度 |
| `engine/internal/proxy/steam_http_probe.go:140` | `c.mu.Lock()` | `note()`，锁内线性扫 64 条诊断 |
| `engine/internal/proxy/steam_http_probe.go:147-151` | `for _, previous := range c.diagnostics {` | **持锁 O(64) 扫描** |
| `engine/internal/proxy/steam_cdn.go:266-273` | `c.mu.Lock()` / `c.pruneLocked()` | `snapshot()`，持锁跑全表 prune |
| `engine/internal/proxy/steam_cdn.go:227-264` | `func (c *steamCDN) pruneLocked() {` | 扫 `observed`/`failures`/`entries`/`traffic`/`discovery` **5 个 map** |
| `engine/internal/proxy/steam_observer.go:303` | `o.mu.Lock()` | 每 Write 加 observer 锁 |
| `engine/internal/proxy/steam_observer.go:309` | `c.mu.Lock()` | **每 Write 再加一次全局 CDN 锁** |
| `engine/internal/proxy/steam_observer.go:218-230` | `c.mu.Lock()` … `for _, a := range s.scheduler.snapshot().Adapters {` | **持 CDN 锁去拿 scheduler 的锁** |

### 问题机理

**(a) 把"统计计数"和"决策表"塞进了同一把锁。**
`trafficChange`（`steam_observed_probe.go:159`）做的只是把字节数累加到一个 5 槽滑动窗口（`:183-193`），这是**纯计数器**；但它和 `choose()`（`steam_cdn.go:360`，每连接一次）、`useTrial`（`steam_observed_probe.go:198`）、`note()`（`steam_http_probe.go:139`）、`snapshot()`（`steam_cdn.go:266`）共用 `c.mu`。数据面每 128 KB 的计数更新因此要排在连接建立决策后面。

**(b) `note()` 在锁内做 O(64) 线性扫描。** `steam_http_probe.go:147-151` 遍历最多 64 条 `c.diagnostics` 去重。诊断事件在 Steam 下载期是成串来的（`steam_observer.go:414` 的 `"http_observer_unsupported"`、`:368/:379/:384` 的响应分类等），每次都在全局锁内烧 64 次四字段比较。

**(c) `snapshot()` 在锁内做全表维护 + 排序。** `steam_cdn.go:273` 的 `pruneLocked()` 扫 5 个 map；`steam_cdn.go:296-343` 对每个 entry 调最多 3 次 `steamFailureKey(key)`（`steam_accounting.go:13`：`k.adapter + "/" + k.domain + ":" + k.port`，**每次一次新字符串分配**）和约 5 次 `c.now()`；最后 `steam_cdn.go:344` 的 `sort.Slice`。`c.entries` 的上限是 512（`steam_cdn.go:552`、`:745`）。

**(d) 锁嵌套。** `steam_observer.go:218-230` 在持有 `c.mu` 的情况下调用 `s.scheduler.snapshot()`（`scheduling.go:34`，会取 `scheduler.mu`）。当前全局锁序是 `o.mu → c.mu → s.mu`，一旦将来有人在 `s.mu` 里回调 CDN 代码就会直接死锁。`steam_observer.go:303-311` 进一步叠加成 `o.mu → c.mu`（feed 内部再调 `o.note()` → `c.note()` 再次取 `c.mu`，即**同一函数内可重入两次**）。

### 数据支撑

**调用频率推导（数据面）**

- `accountingWriter.Write`（`server.go:629`）每被调用一次就 `w.add(uint64(written))`。
- `add` 闭包在 `server.go:589` 调 `trafficChange` —— 条件是 `session.cdnKey.domain != ""`（`server.go:588`），即 Steam CDN 流量。
- 每次 Write 的载荷 = `tcpRelayBufferSize = 128 KB`（`tcp_profile.go:12`，`server.go:543`/`:557` 从池里取）。

⇒ **每转发 128 KB 抢一次 `c.mu`。**

| 场景（单流） | 字节/秒 | `c.mu` 争用/秒 |
|---|---|---|
| Steam 下载 10 MB/s | 10 MB | ~80 |
| Steam 下载 100 MB/s | 100 MB | **~800** |
| Steam 下载 500 MB/s | 500 MB | ~4000 |

多流并发（4 条下载同时跑）时这些数字**全部串行到同一把锁**上：100 MB/s × 4 ⇒ **~3200 次/秒**。这是估算，但 `c.mu` 无分区、无分片、无 `RWMutex` 拆分，串行化是结构性的。

**调用频率推导（控制面）**

`Server.Snapshot` ← `engine/internal/server/server.go:531`（`proxyTelemetry`）← 前端 `useEngineState.ts:318` 的 `appServices.engine.snapshot()`，由 `useEngineState.ts:310,321` 的 `startSerialPoll(..., HOME_TELEMETRY_POLL_MS)` 驱动，间隔定义在 `useEngineState.ts:25`：

```
export const HOME_TELEMETRY_POLL_MS = 800;
```

⇒ **1.25 次/秒**。另有 `SteamCDNPanel.tsx:35` 每 2500 ms 一次独立 CDN 轮询（0.4 Hz）。

**单次开销推导**

一次 `cdn.snapshot()` 在 `c.entries` 满 512 时的估算：

- `pruneLocked()`（`steam_cdn.go:227-264`）：扫 5 个 map，上界 ≈ 512 + 512 + 512 + 512 + 512 ≈ 2560 次迭代
- 每 entry：`steamFailureKey` 最多 3 次拼接（`:298`、`:320`，加 `steam_accounting.go:13` 内联那次）→ **512 × 3 = 1536 次字符串分配**，每次约 48 B ⇒ **~74 KB**
- `c.now()` 每 entry 约 5 次（`:276`/`:279`/`:299`/`:302`/`:314`/`:334`）⇒ **~2560 次 `time.Now()`**
- `sort.Slice` O(E log E) ≈ 512 × 9 ≈ 4600 次比较

⇒ **1.25 Hz × 1 次/次 ⇒ 约 1 920 次字符串分配/秒 + 约 3 200 次 `time.Now()`/秒**，全部发生在**持有全局 `c.mu`** 的窗口内。

### 优化建议

1. **把纯计数器拆出锁。** `traffic.slots`（`steam_observed_probe.go:155`）改成每 key 一个 `atomic.Pointer[slotArray]` 或 `sync/atomic` 字段组，`trafficChange` 不再取 `c.mu`。**预期立省数据面 100% 的 `c.mu` 争用**（即上表全部数字）。
2. **`note()` 的去重扫描移出锁**或改索引：把 `c.diagnostics` 的最近 64 条换成一个 `map[diagKey]time.Time`（`diagKey` = adapter/domain/ip/stage），`O(64)` → `O(1)`。
3. **`snapshot()` 改成 copy-on-read**：先在锁内把需要的字段拷进局部结构（`entries` 最多 512 × sizeof ≈ 512 × ~250 B ≈ 128 KB，一次性），**放锁**，再做 `prune`/`steamFailureKey`/`sort`。锁持有时间从 O(entries) 降到 O(entries) 次指针拷贝（几十微秒级），且不再在锁内分配字符串。
4. **打破 `o.mu → c.mu` 嵌套**：把 `steam_observer.go:309-311` 读到的 `active` 标志改成 `atomic.Bool`，避免 feed 路径上二次取全局锁。

### 预期收益

数据面 `c.mu` 争用次数 **从 ~800 次/秒/流降到 0**（拆分计数器后）；遥测窗口对数据面的阻塞 **从 O(2560 次迭代 + 1536 次分配) 降到 O(1)**。

---

## 热点 3 —— `latencyTable.snapshot()` 的 O(adapters × values) 嵌套扫描

### 代码位置

| 位置 | 原文 |
|---|---|
| `engine/internal/proxy/latency.go:332` | `for _, a := range adapters {` |
| `engine/internal/proxy/latency.go:333` | `for k, v := range p.values {` |
| `engine/internal/proxy/latency.go:334` | `if k.binding != performanceKey(a) {` |
| `engine/internal/proxy/adaptive.go:39-41` | `func performanceKey(a Adapter) bindingKey { return bindingKey{a.Name, a.SourceIP, a.SourceIPv6, a.IfIndex, a.IPv6IfIndex} }` |
| `engine/internal/proxy/adaptive.go:34-37` | `type bindingKey struct { name, ip, ipv6 string; index, index6 int }`（64 B） |
| `engine/internal/proxy/latency.go:255` | `result.Latency, result.Decisions = s.latency.snapshot(s.adapters)` |
| `engine/internal/proxy/adaptive.go:244-245` | `s.mu.Lock()` / `defer s.mu.Unlock()`（`performanceSnapshot` 全程持 scheduler 锁） |
| `engine/internal/proxy/adaptive.go:262-263` | `p.mu.Lock()` / `defer p.mu.Unlock()` |
| `engine/internal/proxy/server.go:276` | `result.Scheduling = s.scheduler.performanceSnapshot()` |
| `engine/internal/proxy/latency.go:16` | `var latencyReferences = [...]string{"223.5.5.5", "1.1.1.1"}` |
| `engine/internal/proxy/latency.go:21` | `latencyTargetLimit = 8` |

### 问题机理

`latency.go:332-343` 是一个教科书式的 **map 上做双重线性扫描**：外层遍历网卡，内层遍历整个采样表，**每一对比较都重新构造一次 `performanceKey(a)`**（`adaptive.go:39`，返回 64 B 的五字段结构体）。而 `a` 在内层是常量 —— 这个构造本该提到内层循环外。

更进一步，这个循环的语义是"按 binding 分组采样"，用嵌套扫描实现是 O(A × V)；正确答案是一次性按 `k.binding` 建索引，O(V)。

**同类问题在 `selectAdapter` 里也有**（`latency.go:144-176`）：`sources` 的内层循环在 `:151`、`:165`、`:174`、`:175` 反复调用 `performanceKey(a)` / `performanceKey(best)`，并且全程持 `p.mu`（`latency.go:131`）。`selectAdapter` 每条新 TCP 连接经 `scheduler.go:116` 调用一次。

### 数据支撑

**规模推导**

- `p.values` 的键是 `latencyKey{binding, target}`（`latency.go:34-37`），每条记录对应 (adapter, target) 一对。
- target 上界 = `len(latencyReferences)` = 2（`latency.go:16`）+ `latencyTargetLimit` = 8（`latency.go:21`）= **10**
- ⇒ `V = A × T = A × 10`

| A（网卡数） | T | V = A×T | 循环次数 = A×V | 正确实现 = A+V |
|---|---|---|---|---|
| 2 | 10 | 20 | 40 | 22 |
| **4** | **10** | **40** | **160** | **44** |
| 8 | 10 | 80 | 640 | 88 |

⇒ **A=4 时 160 次 `performanceKey` 构造（每次 64 B 栈写入 + 5 字段比较），正确做法只需 44 次，降幅 72.5%；复杂度从 O(A²T) 降到 O(AT)。**

**调用频率推导**

调用链（全部已核对行号）：

```
useEngineState.ts:310  startSerialPoll(..., HOME_TELEMETRY_POLL_MS)
useEngineState.ts:321  }, HOME_TELEMETRY_POLL_MS)      // 800 ms
useEngineState.ts:318    const next = await appServices.engine.snapshot()
  → engine/internal/server/server.go:531   return protocol.Result(request.ID, s.proxy.Snapshot(params.IncludeConnections))
    → engine/internal/proxy/server.go:274  func (s *Server) Snapshot(includeConnections bool) TelemetrySnapshot {
      → engine/internal/proxy/server.go:276    result.Scheduling = s.scheduler.performanceSnapshot()
        → engine/internal/proxy/adaptive.go:255   result.Latency, result.Decisions = s.latency.snapshot(s.adapters)
          → engine/internal/proxy/latency.go:328   func (p *latencyTable) snapshot(adapters []Adapter) (...)
```

⇒ **1.25 次/秒**（`HOME_TELEMETRY_POLL_MS = 800`，`useEngineState.ts:25`）。

⇒ A=4 时 **每秒 200 次 `performanceKey` 构造**，且全部发生在 `performanceSnapshot` 持有的 `s.mu`（`adaptive.go:244`）+ `p.mu`（`adaptive.go:262`）双锁窗口内 —— 这两把锁同时被 TCP 连接建立路径（`scheduler.go:105`、`dialUpstream`）争用。

**触发条件（必须诚实说明）**

这段代码只在 `strategy == StrategyLatency`（`"latency-first"`）时执行（`adaptive.go:253`）。而**默认策略是 `round-robin`**（`adaptive.go:19-25`：`strategy == "" && !weighted ⇒ StrategyRoundRobin`）。所以：

- 使用默认配置的普通用户：**该热点不触发**。
- 显式选择 `latency-first` 的用户：**触发**，且 `round()`（`latency.go:261`）会每秒额外做 A×T 次真实 ICMP 探测（`latency.go:302`，`Timeout: 500ms`），起 8 个 worker goroutine（`latency.go:294`）。这部分是设计使然，不算浪费。

### 优化建议

1. **最小改动（保住语义，砍掉 72.5%）**：把 `performanceKey(a)` 提到内层循环外：
   ```go
   for _, a := range adapters {
       binding := performanceKey(a)   // 提到循环外
       for k, v := range p.values {
           if k.binding != binding { continue }
           ...
       }
   }
   ```
   循环次数不变（仍是 A×V 次比较），但 `performanceKey` 构造从 A×V 次降到 A 次。
2. **正确做法（砍掉整个 A 因子）**：单趟遍历 `p.values`，按 `k.binding` 归并：
   ```go
   grouped := make(map[bindingKey][]LatencyTelemetry, A)
   for k, v := range p.values { if keep[k.target] { grouped[k.binding] = append(...) } }
   ```
   ⇒ O(V)，A=4 时 **40 次而非 160 次**。
3. 顺带：`selectAdapter`（`latency.go:144-176`）在每个 `source` 的内层重复 `performanceKey`，可预先算好 `map[bindingKey]Adapter` 复用。

### 预期收益

`latency-first` 模式下，`Snapshot()` 中这部分的工作量从 **O(A²T) 降到 O(AT)**；A=4 时 160 → 44 次 key 操作（**-72.5%**），且 `s.mu`+`p.mu` 双锁窗口缩短。

---

## 次要观察（未进前三，按"影响面×开销"排序，均已核对行号）

1. **UDP 每包重算 target 字符串** —— `engine/internal/proxy/udp.go:516`：
   ```go
   target:  net.JoinHostPort(ip.String(), strconv.Itoa(port)),
   ```
   `parseSOCKSUDPPacket` 对**每一个 UDP 数据报**做 `ip.String()` + `Itoa` + `JoinHostPort`（3 次字符串分配）。`udp.go:172` 的 `forward()` 随后 `a.scheduler.watchLatency(packet.target)`（`udp.go:174` → `latency.go:219-221`）**每包取一次 `scheduler.mu`**，而 `target` 对同一目的流是恒定的 —— 可缓存进 `a.flows` 的 flow 结构。频率：每 UDP 包一次（游戏流量下可达数千包/秒）。**估算：1000 包/秒 ⇒ 3000 次字符串分配 + 1000 次全局锁争用/秒。**

2. **UDP 关联固定占 64 KB，且每包一次 `SetReadDeadline`** —— `udp.go:133` `buffer := make([]byte, maxSOCKSUDPDatagramBytes)`（`maxSOCKSUDPDatagramBytes = 65535`，`udp.go:21`）每关联一次；`udp.go:143` `_ = a.relay.SetReadDeadline(time.Now().Add(a.sweepInterval))` 在**每次循环迭代**（即每个包）执行一次 `setsockopt` 系统调用。

3. **`expiry.Index` 走 `container/heap` 的 `any` 装箱** —— `engine/internal/expiry/index.go:23` `Push(value any)` 与 `:28` `Pop() any`。`healthTable` 在 `recordComparativeDomainFailure`（`health.go:230`）、`recordSuccess`（`health.go:193`）、`candidates`（`health.go:133`）里每次 `Set`/`Delete` 都要装箱一次接口 + 一次类型断言。频率：每连接一次（`recordSuccess`）或每失败域一次。改成 `expiry.Index[T]` 泛型即可消除。

4. **遥测 `Snapshot` 里的 `health.snapshot()` 是 O(adapters × domains)** —— `health.go:245-260` 对每个 adapter 遍历其全部 `state.domains`，`quarantines` 切片无预估容量（`:254` append 触发反复扩容），末尾 `health.go:278` 的 `sort.Slice` O(Q log Q)。1.25 Hz。若域隔离积累到 8192 个域（`health_index_test.go:55` 的 benchmark 参数上界就是 8192），单次扫描 8192 项 ⇒ 约 10 240 次迭代/次、12 800 次/秒。注意 `health.go:246-248` 的 `if !h.domainIsolation { break }` 写在循环体**首行**，等于一旦关闭域隔离仍会多跑一次 map 迭代 —— 逻辑等价但位置可疑。

5. **`dns.Resolver` 缓存命中仍有 3 次分配** —— 实测见附录 A-3：`BenchmarkResolverCacheHit`（`engine/internal/dns/cache_index_test.go:65`）= **408.4 ns/op, 64 B/op, 3 allocs/op**。对应 `resolver.go:181` 的 `append([]string(nil), result.Addresses...)`（为防别名而复制）+ 缓存键拼接。频率：每个未被 inflight 去重的查询命中一次。1000 次查询/秒 ⇒ 3000 次分配/秒（绝对量不大，但因缓存命中是最高频路径，值得削）。上游 miss 路径另有 `resolver.go:460` `response := make([]byte, 4096)`（每次 legacy UDP 查询 4 KB）和 `resolver.go:524` 每次 `queryDoH` 重建 endpoint 切片。

6. **HTTP 代理路径的请求头多次全量拷贝** —— `engine/internal/proxy/http.go:179` 每个响应 `bufio.NewReader(io.MultiReader(bytes.NewReader(header), upstreamReader))` 新建 4 KB bufio；`parseProxyRequest` 先 `strings.ReplaceAll(string(header), "\r\n", "\n")`（一次 header 全量拷贝）再 `[]byte(forward.String())`（再一次）。频率：每 HTTP 请求/响应一次，不是每包，但 HTTP 代理是主要入口。

7. **`steamHTTPFramer` 逐字节推进** —— `engine/internal/proxy/steam_observer.go:155` 和 `:188`：
   ```go
   f.header = append(f.header, data[0])
   data = data[1:]
   ```
   header 状态（`default` 分支，`:187-208`）逐字节 append + 每字节一次 `bytes.HasSuffix`，上限 `steamSniffLimit = 16 * 1024`（`steam_sniff.go:15`）。只对 Steam CDN 连接生效，且只作用于 header 字节（body 走 `:128-143` 的批量跳过），所以**实际影响远小于表面**。可用 `bytes.IndexByte(data, '\n')` 做批量推进，属微优化。

---

## 附录 A —— 本地实测的 benchmark（已运行）

三个 benchmark 都是仓库自带的（`grep '^func Benchmark'` 在 `engine/` 下只找到这 3 个）。全部在 `C:\Users\Administrator\Desktop\HypoMux\engine` 下用 `GOPROXY=off GOFLAGS=-mod=mod` 运行，**离线、未联网**。

### A-1 `BenchmarkSchedulerDomainEvidence`（`engine/internal/proxy/health_index_test.go:54`）

测什么：在 2 张网卡上预置 0/64/1024/8192 个带隔离证据的域名，然后反复调 `s.SelectForDomain(nil, "unrelated.example")`（`health_index_test.go:72`）。`b.ReportAllocs()` 已开启。**这条 benchmark 直接测的就是热点 1 的 `candidates()` 路径。**

```
go test -run XXX -bench BenchmarkSchedulerDomainEvidence -benchtime 200x ./internal/proxy/

goos: windows
goarch: amd64
pkg: github.com/Hypostasis-Cat/HypoMux/engine/internal/proxy
cpu: 11th Gen Intel(R) Core(TM) i9-11900KF @ 3.50GHz
BenchmarkSchedulerDomainEvidence/0-16          200    314.0 ns/op    769 B/op    4 allocs/op
BenchmarkSchedulerDomainEvidence/64-16         200    437.5 ns/op    769 B/op    4 allocs/op
BenchmarkSchedulerDomainEvidence/1024-16       200    305.5 ns/op    769 B/op    4 allocs/op
BenchmarkSchedulerDomainEvidence/8192-16       200    413.5 ns/op    769 B/op    4 allocs/op
PASS
ok      github.com/Hypostasis-Cat/HypoMux/engine/internal/proxy   0.621s
```

**结论（实测）**：`SelectForDomain` 每次 **769 B / 4 allocs**，且**与域名证据数量无关**（指数堆 `expiry.Index` 的剪枝是 O(1) 的，设计正确）。769 B ≈ 4 × 2 × 96 B，精确对应 `health.go:121-124` 的四个 `make([]Adapter, 0, 2)`。

### A-2 `BenchmarkUDPReplyPreparation`（`engine/internal/proxy/udp_buffer_test.go:39`）

测什么：对比 SOCKS5 UDP 回包的两种构造方式 —— 每包分配（`packSOCKSUDPReply`，`udp_buffer_test.go:45`）vs 每流缓冲（`newSOCKSUDPReplyBuffer`，`udp_buffer_test.go:50`）。**生产代码已经用后者**（见 `udp.go:551`），这条 benchmark 是"池化收益"的现成标尺。

```
go test -run XXX -bench BenchmarkUDPReplyPreparation -benchtime 2000x ./internal/proxy/

BenchmarkUDPReplyPreparation/per-packet-allocation-16   2000    476.4 ns/op   1280 B/op   1 allocs/op
BenchmarkUDPReplyPreparation/flow-buffer-16              2000     13.35 ns/op      0 B/op   0 allocs/op
PASS
ok      github.com/Hypostasis-Cat/HypoMux/engine/internal/proxy   0.636s
```

**结论（实测）**：池化路径 **0 分配，快 35.7 倍**。这条数字是热点 1 优化建议 1（池化 `server.go:448` 的 reader）的收益参照 —— 热点 1 的 reader 是 64 KB 且贯穿连接全生命周期，比这里的 1280 B 单包更有池化价值。

### A-3 `BenchmarkResolverCacheHit`（`engine/internal/dns/cache_index_test.go:65`）

测什么：DNS 缓存命中路径，预置 1/64/1024/16384 条缓存后反复命中。

```
go test -run XXX -bench BenchmarkResolverCacheHit -benchtime 2000x ./internal/dns/

BenchmarkResolverCacheHit/1-16        2000    408.4 ns/op    64 B/op   3 allocs/op
BenchmarkResolverCacheHit/64-16       2000    403.2 ns/op    64 B/op   3 allocs/op
BenchmarkResolverCacheHit/1024-16     2000    393.5 ns/op    64 B/op   3 allocs/op
BenchmarkResolverCacheHit/16384-16    2000    397.4 ns/op    64 B/op   3 allocs/op
PASS
ok      github.com/Hypostasis-Cat/HypoMux/engine/internal/dns   0.633s
```

**结论（实测）**：缓存命中 **408 ns / 64 B / 3 allocs**，且与缓存容量无关（命中是 O(1) 的，`resolver.go:188-193` 的 inflight 去重设计正确）。对应次要观察 #5。

---

## 审计方法说明（回应"grep 命中数 ≠ 热度"）

本报告的候选排序**没有**用 `fmt.Sprintf` / `make(` 的 grep 命中数当热度证据。实际做法是：

1. 先沿 `acceptLoop`（`server.go:406`）、`udpAssociation.serve`（`udp.go:132`）、`relayTransfers`（`server.go:528`）三个数据面入口追函数体；
2. 对每个候选标注**调用点**与**触发条件**（每连接 / 每包 / 每 Write / 每秒 / 仅 `latency-first`）；
3. 按"频率 × 单次开销"排序。

据此排除或降级的候选：

- `fmt.Sprintf` 类命中确实集中在低频路径（`steam_http_probe.go:108`、`server.go:72` 的启动/错误日志），**未进前三**。
- `latencyTable` 相关热点只在 `latency-first` 下触发，而默认是 `round-robin`（`adaptive.go:19-25`）—— 已在热点 3 中显式标注触发条件，未夸大为"默认即慢"。
- `steamHTTPFramer` 逐字节解析只在 Steam CDN 连接上跑 header 字节 —— 降级到次要观察 #7。
- DNS 缓存命中（实测 3 allocs/次）绝对量不大 —— 降级到次要观察 #5。