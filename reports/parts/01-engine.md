# engine/ 分析报告（HypoMux Go 聚合内核）

> 分析对象：`engine/`（约 20660 行源码 + 约 8800 行测试）
> 分析者：teammate `engine-analyst`；共享任务 `task-1`；写入范围仅本文件。
>
> **方法与证据声明**
> - 本机**未安装 Go 工具链**，因此**没有编译、没有运行任何 Go 测试**；本文所有结论来自逐文件阅读（read/grep/glob）。
> - 所有结论尽量给出 `相对路径:行号`。无法从 engine 源码单独核实的内容一律标注“**未验证**”。
> - 行号以本次读取到的文件内容为准（例如 `engine/internal/server/server.go` 共 878 行）。

---

## 1. 模块与进程拓扑

### 1.1 源码规模（逐目录，源码行/测试行）

| 目录 | 文件数(源/测) | 源码行 | 测试行 |
|---|---|---|---|
| `engine/cmd/hypomux-engine/` | 8 / 5 | ~1320 | ~525 |
| `engine/internal/api/v1/` | 1 / 3 | 304 | ~356 |
| `engine/internal/diagnostic/` | 3 / 1 | 181（diagnostic.go） | — |
| `engine/internal/dns/` | 4 / 6 | 652（resolver.go）+190（config.go） | ~1164 |
| `engine/internal/expiry/` | 1 / 0 | 84 | 0 |
| `engine/internal/fileintegrity/` | 1 / 0 | 54 | 0 |
| `engine/internal/platform/` | 12 / 2 | ~288 | — |
| `engine/internal/protocol/` | 1 / 0 | 69 | 0 |
| `engine/internal/proxy/` | 27 / 28 | ~6178 | ~6172 |
| `engine/internal/runtime/` | 1 / 0 | 102 | 0 |
| `engine/internal/server/` | 3 / 5 | 878 + 61 + 24 | ~1067 |
| `engine/internal/tun/` | 10 / 4 | ~1223 | ~637 |
| `engine/internal/wfp/` | 3 / 1 | 12 + 423 | — |

最重的单文件：`engine/internal/server/server.go`(878)、`engine/internal/proxy/steam_cdn.go`(811)、`engine/internal/tun/supervisor.go`(714)、`engine/internal/proxy/server.go`(668)、`engine/internal/dns/resolver.go`(652)。

### 1.2 进程形态：三种启动方式

`engine/cmd/hypomux-engine/main.go`（181 行）用子命令 switch（`main.go:42-143`）区分：

| 子命令 | 行号 | 行为 |
|---|---|---|
| `serve` | `main.go:56-57` | `runServer(os.Stdin, os.Stdout, runtimeServerMetadata())`，stdio JSONL 会话 |
| `service` | `main.go:58-59` | `runWindowsService(stderr, baseServerMetadata())` → `svc.Run("HypoMuxCore", host)`（`service_windows.go:177-198`） |
| `serve-pipe --pipe --session-token --host-pid` | `main.go:84-105` | 先 `connectAuthenticatedPipe` 做**双向握手鉴权**，随后 `runServer(connection, connection, stderr)`——**同一命名管道既是 stdin 又是 stdout** |
| `install-service --desktop PATH` / `remove-service` | `main.go:60-83` | 安装/卸载 Windows 服务（`service_windows.go:35-168`） |
| `recover` | `main.go:109-117` | 20s 超时调 `tun.Recover`（`tun/recover.go:8-10` → `cleanupPlatform`） |
| `signal-tun <pid>` | `main.go:43-55` | 以 `CTRL_BREAK` 私有控制台方式中断 sing-box（`tun/process_windows.go:52-100`） |
| `diagnose` / `diagnostic` | `main.go:118-138` | 直接跑 ICMP 诊断并输出 JSON（不经协议层） |
| `version` | `main.go:106-108` | — |

**两种常驻形态**：
1. **Windows 服务（提权）**：`HypoMuxCore` 常驻，命名管道 `\\.\pipe\HypoMux-Core-Service`（`service_windows.go:24-25`），SDDL `D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)`（`service_windows.go:30`），`PIPE_REJECT_REMOTE_CLIENTS` + `maxInstances=1`（`service_windows.go:396-425`）。服务**循环接受客户端**，每个客户端一个 `server.New(...).Run(ctx)`（`service_windows.go:274-316`）。
2. **非提权 stdio Core**：由桌面通过 `serve-pipe` 拉起，先在管道上做 `hypomux.core.authenticate`/`hypomux.host.ready` 握手（`pipe_windows.go:17-23,44-142`）。

**运行时可注入变量**（`main.go:22-30`）：`version`、`commit`、`recoverTUN`、`installServiceCommand`、`removeServiceCommand`。

**对外接口**：engine 自身**不监听 TCP/HTTP**；唯一对外通道是命名管道/stdio 上的 `protocol.Transport = "stdio-jsonl"`（`engine/internal/protocol/protocol.go:5-9`），单条消息上限 `MaxMessageBytes = 1024*1024`（`protocol.go:7`）。真正的网络监听（SOCKS/HTTP）在 `proxy` 包，且强制 loopback（见 §5）。

### 1.3 内部模块依赖方向（由 import 实测，无环）

```
cmd/hypomux-engine
  ├─→ internal/server
  ├─→ internal/tun          (PrepareTrustedConfigStorage / Recover)
  ├─→ internal/diagnostic
  └─→ internal/fileintegrity

internal/server  →  api/v1, diagnostic, dns, platform, protocol, proxy, runtime, tun, wfp
internal/api/v1  →  diagnostic, dns, platform, protocol, proxy, runtime, tun
internal/proxy   →  dns, diagnostic, expiry
internal/dns     →  expiry
internal/tun     →  fileintegrity
internal/wfp / platform / runtime / protocol / expiry / fileintegrity / diagnostic  →  （无内部依赖，叶子）
```
（证据：`engine/internal/server/server.go:16-24`、`engine/internal/api/v1/types.go:11-17`、`engine/internal/proxy/dial.go:9`、`engine/internal/proxy/health.go:11`、`engine/internal/proxy/latency.go:10`、`engine/internal/dns/resolver.go:19`、`engine/internal/tun/supervisor.go:19`。）

**没有任何 internal 包反向 import `server`**（只有 `cmd` 依赖它）——这是本仓库最干净的一条架构边界。

### 1.4 ASCII 数据流图

```
                       ┌──────────────────────── 桌面 UI / host (hypomux.exe) ───────────────────────┐
                       │  \\.\pipe\HypoMux-Core-Service  (JSONL, 单实例, SDDL 限 SYSTEM/BA/IU)          │
                       └───────────────┬───────────────────────────────────────────────────────────────┘
                                       │ 18 个 method / 5 个 event（protocol/v1/manifest.json）
                                       ▼
        ┌──────────────────────── server.Server.Run (server.go:80-161) ────────────────────────────┐
        │ bufio.Scanner ≤1MB → lines chan → s.handle(ctx,line) → writeMessage / emitEvent(writeMu)  │
        │ 单一串行 RPC 循环（lifecycleMu 保护 proxy/tun/wfp 事务）                                     │
        └───┬────────────┬─────────────┬──────────────┬──────────────┬───────────────┬──────────────┘
            │            │             │              │              │               │
       engine.start  tun.activate  engine.scheduling  dns.resolve  mtu.set     wfp.inspect
            ▼            ▼             ▼               ▼           ▼              ▼
     proxy.Server   tun.Supervisor  scheduler.update  dns.Resolver platform.SetMTU platform.InspectWFP
            │            │                                              /InspectSharing
            │            └─ exec: <same-dir>\sing-box.exe run -c <staged config>
            │                 Job Object(KILL_ON_JOB_CLOSE) + CREATE_NEW_CONSOLE + CTRL_BREAK 优雅停止
            ▼
   ┌──────────────── 应用流量路径（mode = tun_tcp_pool） ────────────────┐
   │ 应用 socket → Wintun 网卡 "HypoMux-Tun" → sing-box(TUN/FakeIP/本地DNS) │
   │      ├─ TCP → per-channel SOCKS5 监听 127.0.0.1:<port>  (server.go:195-226)
   │      └─ UDP → SOCKS5 UDP ASSOCIATE（仅 channel 端口提供，socks.go:52-56）
   └───────────────┬────────────────────────────────────────────────────┘
                   ▼
        handleSOCKS (socks.go:14-92) / handleHTTP (http.go:19-67)
                   │  ├─ steam CDN 嗅探（纯 Peek，不消费字节）steam_sniff.go:19-98
                   │  └─ registry.Begin 记账（registry.go:128-169）
                   ▼
     connect（server.go:458-490）→ dialUpstream（dial.go:12-154）／ createFlow（udp.go:235-336）
                   │
        channel == "direct" ?  ── 是 ──→ dialDirectTCP（跟随系统路由，无绑定；server.go:492-512）
                   │ 否
                   ▼
   ┌──────── 调度器（每 channel 一个 scheduler） ────────┐
   │ health.candidates 过滤（health.go:108-159）         │
   │  → latency.selectAdapter（latency.go:130-186）      │
   │  → performance.acquire/adaptiveAllocation           │
   │     （adaptive.go:108-137 + adaptive_allocation.go:51-70）│
   │  → SWRR 加权轮询 / 轮转（scheduler.go:39-102）        │
   └───────────┬──────────────────────────────────────────┘
               ▼
  boundNetworkDialer（dial_windows.go:22-77）
    ├─ LocalAddr = SourceIP / SourceIPv6（显式源地址）
    └─ dialer.Control → IP_UNICAST_IF=31（IPv4 网络字节序）/ IPV6_UNICAST_IF=31（主机字节序）
               ▼
        上游服务器（最多尝试 2 张网卡，dial.go:39-42）
               │
               ▼
  relayTransfers（server.go:528-622）双向 io.CopyBuffer（128KB 池化缓冲）+ 每 5 秒 sample
               │
               ▼
  registry/telemetry（registry.go:267-325）→ engine.telemetry → 桌面 UI

  辅助网络面：
   · DNS：dns.Resolver（源绑定 DoH→传统 DNS 自动回退，resolver.go:261-430）
   · WFP：OpenDNSExemption 只给 engine 进程在指定网卡上放行 UDP/TCP:53（wfp/dns_exemption_windows.go:148-351）
   · 清理：tun cleanupPowerShell 删 0.0.0.0/0、::/0 路由并移除 Wintun 设备（tun/cleanup_windows.go:17-96）
```

---

## 2. 多网卡调度算法

### 2.1 策略集合与归一化

- 四种策略常量：`StrategyRoundRobin="round-robin"`、`StrategyWeighted="weighted"`、`StrategyAdaptive="adaptive-throughput"`（`engine/internal/proxy/adaptive.go:14-16`）、`StrategyLatency="latency-first"`（`engine/internal/proxy/latency.go:13`）。
- `NormalizeStrategy`（`adaptive.go:19-32`）：空串时按 `weighted` 布尔回落，非法值报 `unknown scheduling strategy %q`。
- `ValidateScheduling`（`scheduling.go:14-32`）：`config.Weighted = strategy == StrategyWeighted`；**adapter Weight 必须 1..100**，否则 `adapter %q weight must be between 1 and 100`。
- 请求路径：`engine.scheduling` → `server.updateScheduling`（`engine/internal/server/scheduling.go:11-61`）→ `proxy.ValidateScheduling` → `Server.UpdateScheduling`（`scheduling.go:66-86`）。注释明确“**只改聚合池；显式 NIC channel 与已建立 TCP 流保留绑定**”（`scheduling.go:5-7`）。

### 2.2 统一入口：候选过滤 → 策略选择

`newScheduler`（`scheduler.go:19-37`）为每个 channel 建一个 `scheduler`；`Server.New` 中 `ChannelAggregation` 复用同一 health 并替换 `server.scheduler`（`engine/internal/proxy/server.go:98-107`），**performance 表在所有 scheduler 间共享**（`server.go:109-115`）。

`selectLocked`（`scheduler.go:53-72`）核心几步：

```go
candidates := s.health.candidates(s.adapters, excluded, domain)   // scheduler.go:56
if len(candidates) == 0 { return Adapter{}, false }               // :57-59
if len(candidates) == 1 { return candidates[0], true }            // :60-62 不推进 next
if s.strategy == StrategyLatency && s.latency != nil { … }        // :63-65
if s.weighted { return s.selectWeighted(candidates), true }        // :66-68
chosen := candidates[s.next%len(candidates)]                       // :69
s.next = (s.next + 1) % len(candidates)                            // :70
```
**“恰好 1 个候选直接返回且不推进轮转”**是刻意的（注释 `scheduler.go:60`：受限流不应重置轮转），并有专门测试 `engine/internal/proxy/scheduler_rotation_test.go:5 TestConstrainedFlowsPreservePoolRotation`。

加权：平滑加权轮询 SWRR（`scheduler.go:86-102`）——每轮 `currentWeight[name]+=weight`，取最大者，选中者减总权重；`weight<=0` 视为 1。

`acquireTCP`（`scheduler.go:104-130`）：latency 策略且给了 target 时先由 `latency.selectAdapter` 选人，再交给 `performance.acquire(candidates, false, chosen)`；否则先用 legacy 轮转拿 `fallback`，再用 `performance.acquire(candidates, strategy==StrategyAdaptive, fallback, s.adapters)`。**选择与预留共用 `performanceTable.mu` 一把锁**（`adaptive.go:110-112`）。

### 2.3 adaptive-throughput（自适应速度）

设计声明（`engine/internal/proxy/adaptive_allocation.go:8-11`）：
> 有界反馈只改“分配份额”，绝不用瞬时速率/负载给新连接排名；一半分配预算保持均匀，稳态下每条可用链路至少 `1/(2*N)` 的新连接。

- 状态：`adaptiveAllocation{keys, shares, credit, started, cooldownUntil, state, allocations, windowSamples, windowTotal, reference}`（`adaptive_allocation.go:12-23`）；`reset` 均分并把 state 置 `warming-up`（:25-32）；`choose` 在 `state != "adapting"` 时**直接返回 fallback**（:51-70，即退化为 legacy 轮转）。
- 采样：`performanceTable.sample()` 每秒执行（`adaptive.go:165-211`，由 `run` 的 `time.NewTicker(time.Second)` 驱动，`adaptive.go:212-223`）。
- **背压判定**：`backpressure := time.Duration(blocked-lastBlocked) > dt/2`（`adaptive.go:176`）；仅当 `valid(delta>0) && !backpressure` 才更新速率（注释 `adaptive.go:198`：“Idle/blocked windows are not evidence of a slower network”）。
- 速率 EWMA：`link.rate += 0.2*(rate-link.rate)`（`adaptive.go:206`，α=0.2）。
- 每条链路可测性门槛（`adaptive_allocation.go:79-90`）：`windowTransfers >= 2 && !windowBlocked && windowRate > 0`，否则该链路失败并给 reason（冷却中→`throughput-guard`，否则 `insufficient-demand`）。
- 触发条件（`adaptive_allocation.go`）：
  - `windowSamples < 5` 不评估（:100-102）；
  - 起步 15s 内 → `warming-up` 并记录 reference（:106-110）；
  - **新到达数 `arrivals < max(4, len(keys))` 直接返回**（:112-114，注释：长期固定 TCP 流集合无法检验新分配）；
  - 总吞吐均值 `mean < reference*0.9` → `throughput-guard` 且 `cooldownUntil = now+30s`（:115-120）；
  - 目标份额 `target[i] = 0.5/N + 0.5*rate_i/observed`（:138）；
  - **`maxChange < 0.02` 不再追（state→`balanced`）**（:141-146）；
  - 步长 `step = math.Min(1, 0.05/maxChange)` → **单轮最大份额变化 ≈0.05**（:147-150）。
- 状态由 lease 持有（注释 `adaptive.go:43-44`：“绝不按 adapter 名在释放时查找”，`dial.go:136-140` 把连接包装成 `leasedConn`，`adaptive.go:299-310` Close 时 `lease.finish()`）。
- 遥测：初始 `EffectiveTCPStrategy` 被降级为 `round-robin`、`State="warming-up"`；**`UDPStrategy = StrategyRoundRobin`** 恒定（`adaptive.go:243-297`，尤其 :249）。

### 2.4 latency-first（低延迟优先）

- 参考地址 `latencyReferences = {"223.5.5.5", "1.1.1.1"}`（`latency.go:16`）；常量 `latencyFreshness=8s`、`latencyTargetTTL=30s`、`latencyTargetLimit=8`（`latency.go:18-22`）。
- 采样新鲜度：`Samples >= 3 && now.Sub(Updated) <= 8s`（`latency.go:118-120`）。
- 评分：`RTTMS + 2*JitterMS + 400*Loss`；`LastReply` 为零或 `Failures>=3` → `+Inf`（`latency.go:121-126`）。
- 采样 EWMA α：RTT 0.25 / Jitter 0.25 / Loss 0.25（`latency.go:82-116`）。
- 目标优先级：**host → 223.5.5.5 → 1.1.1.1**（`latency.go:143`）；候选必须**全部**有该参考源的新鲜样本，否则跳下一个源；都无 → `reasons["learning-stable-fallback"]++` 并返回 `candidates[0]`（:184-185）。
- **迟滞（hold-down）**：在位者与新最优差距不足 `math.Max(8, oldScore*0.15)` 或在位不足 10s 则不换；在位者 `score==Inf` 时绕过迟滞（`latency.go:163-176`）。
- IPv6 目标直接 `unsupported-target` 回退（`latency.go:139-142`，因为源绑定诊断只支持 IPv4，见 `latency.go:56-67`）。
- UDP 失效切换门槛：`failedAgainst` 要求 current 与 alternative 都有新鲜样本、current 有历史回复且 `Failures>=3`、alternative `Failures==0` 且 3s 内回过包（`latency.go:190-204`）；UDP 侧要求**外发失败或 `time.Since(lastReply) >= 3s`**（`udp.go:185-190`，注释：外发写成功不是连通性证据，绝不重放已写出的数据报）。
- 探测：`run` 每秒 tick（`latency.go:245-259`），`round` 内 **8 个固定 worker**，每探针 `Count:1, Timeout:500ms`（`latency.go:294-308`）。**注意**：`round` 在 tick 循环里是串行等待的，探针总时长超过 1s 会丢拍（属设计取舍，非 bug）。

### 2.5 health（健康度 + 单网卡受限域名记忆）

- 常量：`domainFailureThreshold=2`、`domainEvidenceTTL=10min`、`domainQuarantineTTL=30min`（`engine/internal/proxy/health.go:14-18`）；**适配器失败退避表 `adapterFailureBackoff = [2s, 5s, 15s, 30s]`**（`health.go:20-25`），由 `index = consecutiveFailures-1` 取，封顶到表长-1（`health.go:161-177`）。
- 候选优先级（`health.go:108-159`）：`healthy`（未冷却且未对该域名隔离）> `domainFallback`（未冷却，忽略域名隔离）> `earliestCooldown(recovery)` > `earliestCooldown(all)`。注释（`health.go:150,154`）：域名隔离不得把目标变成全面断网；全冷却时保留一条恢复路径。
- **单网卡受限域名记忆的写入点**：`dialUpstream` 在**成功**于某个 adapter 拨通后，把同轮其它解析失败的 adapter 记为对比失败——`engine/internal/proxy/dial.go:130-135` 对 `comparativeFailures` 逐个调 `channelScheduler.health.recordComparativeDomainFailure(failedAdapter, domain)`；实现 `health.go:196-231`：仅当 `domainIsolation` 为真时 `evidence++`，`evidence >= 2` 时 `expiresAt = now+30min`（若 `domainIsolationExpiry`）否则 `now+100年`；不足阈值时先给 `now+10min`。
- 失效索引：`domainExpiry expiry.Index[string]`（`health.go:35`），`pruneExpiredDomains` 循环 `PopExpired`（`health.go:301-312`），底层 `engine/internal/expiry/index.go:77-84`（调用方负责加锁，注释 `expiry/index.go:1,37`）。
- 清除条件：`recordSuccess` 清 `consecutiveFailures`/`cooldownUntil` 并 `delete(state.domains, domain)` + `domainExpiry.Delete`（`health.go:179-194`）。
- 开关：`DomainIsolation` / `DomainIsolationExpiry` 默认 `true`（`engine/internal/proxy/config.go:190-199`）；**`server` 请求 DTO 中这两项保持 nil 由 proxy 侧默认**（`engine/internal/api/v1/types.go:210-212` 注释明确“两处重复即会漂移”）。
- 种子隔离：`Config.DomainQuarantines`（`config.go:54-58`），`normalizeConfig` 只保留已知 adapter、非空域名、未过期者；`domainIsolationExpiry=false` 时把过期时间推到 `now+100年`（`config.go:209-217`）；`newHealthTableConfigured` 把种子 `evidence` 直接设为阈值 2（`health.go:73-106`）。
- 仅本地失败才污染健康度：`isLocalConnectFailure` → `syscall.Errno` → `isLocalConnectErrno`（`health.go:336-345`；`health_errno_windows.go:15`）；UDP 侧还额外要求 latency-first（`udp.go:575-584`）。

### 2.6 “新连接选网卡”“源地址绑定 / IP_UNICAST_IF”实现位置

| 关注点 | 位置 |
|---|---|
| 新连接选网卡（TCP） | `engine/internal/proxy/dial.go:57`（`acquireTCP`）→ `scheduler.go:104-130` |
| 新连接选网卡（UDP） | `engine/internal/proxy/udp.go:250-260`（`a.scheduler.selectForTarget`） |
| 每 channel 一个 scheduler | `engine/internal/proxy/server.go:98-107` |
| 源地址绑定 | `engine/internal/proxy/dial_windows.go:22-77`（`LocalAddr` = `SourceIP`/`SourceIPv6`） |
| IP_UNICAST_IF / IPV6_UNICAST_IF | `dial_windows.go:15-16`（常量 31）、`dial_windows.go:56-61`（IPv6 主机字节序）、`dial_windows.go:63-69`（IPv4 `bits.ReverseBytes32` 网络字节序） |
| 直连（不绑定） | `engine/internal/proxy/server.go:492-512`（`dialDirectTCP`） |
| 源绑定 DNS | `dial.go:78-94`（A，失败且有 IPv6 源时再 AAAA）+ `dns.Resolver.Resolve` |

### 2.7 Steam CDN 优选

- 生命周期常量 `cdnLifetime = 10 * time.Minute`（`engine/internal/proxy/steam_cdn.go:16`）；所有学习状态属于一次 engine 运行，`generation` 防止旧探测/旧连接回填（注释 `steam_cdn.go:111-112`）。
- 域名白名单 `steamDownloadHost`（`steam_cdn.go:192-213`）：`.steamcontent.com` 后缀 + 11 个硬编码镜像域名；同时做 label 长度/字符校验。
- 候选 IP 必须是公网单播且排除 CGNAT `100.64/10`、`198.18/15`（`steam_cdn.go:215-225`）。
- **择优（TCP 连接级）**`choose`（`steam_cdn.go:360-390`）：仅 `Validated && !CooldownUntil` 的 entry；`best` = 最高 `DownloadBPS`（同分取 IP 小者），`explore` = `Selections` 最少者；**`if explore.Selections == 0 || selections%8 == 0 { best = explore }`**（:385，1/8 探索配额）。
- **试用准入**`useTrial`（`steam_observed_probe.go:198-282`）：`c.decisions[key]%8 == 0` 为探索轮；`hasPreferred && c.decisions[key]%8 == 7` 时**保留原节点控制流**（:229-231，注释：避免自己的基线被饿死而产生振荡）；准入限制 `group_trial_limit`（同组 trial≥2，:251-254）、`route_paused`（失败窗口内，:242-245）、preferred 时 trial≥4 停（:248-250）；`bootstrap` 条件允许原基线新鲜时立刻试一个未用候选（:261）。
- **观测/测量**：
  - 5 秒评分窗口 `window := now.Unix()/5`（`steam_evaluation.go:105`，实现在 `steam_evaluation.go:89-154`）；连续 2 个窗口占优才 `Preferred=true`（`steam_evaluation.go:135-140`）。
  - 变慢冷却：`slow := comparable && (DownloadBPS < base*0.25 || (EffectiveBytes >= 8<<20 && DownloadBPS < base*0.75))`，需 `slowWindows>=4 && now-slowSince>=15s` → `CooldownUntil=now+1min`（`steam_evaluation.go:118-133`）。
  - 速率 EWMA：`DownloadBPS = DownloadBPS*0.75 + bps*0.25`（`steam_cdn.go:392-441`，α=0.25）；样本门槛 `bytes >= 64KB && elapsed >= 100ms`；样本间隔 ≥10s 视为不连续并重置。
  - 吞吐探测：`steamSpeedProbeSize = 256KB`、`steamSpeedProbeBudget = 4MB`/分钟、每次最多 16 次尝试（`engine/internal/proxy/steam_speed_probe.go:10-31`）；普通 HTTP 探测预算 256KB/分钟、64 次尝试（`steam_http_probe.go:73-91`）。
  - 目录上限：`entries<512`（`steam_cdn.go:507-571`）、`discovery<64`、`probing<2`（`steam_cdn.go:479`）、每 adapter 最多 8 个候选（`steam_candidate_pool.go:11-30`）、`traffic/observed/decisions` 各 512（`steam_cdn.go:392-441`、`steam_observed_probe.go:167-172,207-209`）、观察者 ≤64（`steam_observer.go:220`）。
  - 内容验证：`port 80` 要求候选内容与 baseline **逐字节相等**（`steam_cdn.go:507-571` 的 `probeSteamCandidates`）；`port 443` 用 TLS 握手校验（`steam_cdn.go:694-708`）。
  - 锁：`steamCDN.mu sync.Mutex`（`steam_cdn.go:113-142`）。**锁序风险见 §7-2。**

---

## 3. 并发模型

### 3.1 goroutine 生命周期

| goroutine | 起点 | 回收 |
|---|---|---|
| 请求读取器（scanner） | `engine/internal/server/server.go:92-103` | `ctx` 取消或输入 EOF；`lines` 无缓冲 |
| `acceptLoop` × listeners | `engine/internal/proxy/server.go:189-191`、:220-224 | `s.wg`；listener.Close + ctx |
| `handleClient` | `server.go:434`（`wg.Add(1)` 在 :432，**持 RLock**） | `defer s.wg.Done()`（:439） |
| `latency.run` | `server.go:162-163` | `s.wg`，每秒 tick |
| `performance.run` | `server.go:164-165` | `s.wg`，每秒 tick |
| UDP control 监听 | `engine/internal/proxy/udp.go:108-113`（`wg.Add(1)` 在 :108） | 控制 TCP 关闭/ctx |
| UDP watcher | `udp.go:116-125` | **不计 wg**，但由 `<-watcherDone`（:128）同步回收 |
| UDP `receiveLoop` per flow | `udp.go:228-232`（`wg.Add(1)` 在 :228，**在 go 之前**） | `flow.close()`/空闲 120s |
| Steam 发现/校验 | `steam_cdn.go:490-505`、`steam_observed_probe.go:29-49` | 20s deadline；`probing` 计数 |
| Steam 内容校验 worker | `steam_observed_probe.go:124-132`（2 个，`names.Wait`） | 函数返回 |
| Steam DNS 候选采集 | `steam_cdn.go:579-655`（min(4,jobs) worker，4s 总超时） | 函数返回 |
| delay 观察者到期 | `steam_observer.go:231,265`（`time.AfterFunc` 每 5s 重挂） | `stopLocked` 停表 |
| TUN 日志泵 ×2 + waitProcess | `engine/internal/tun/supervisor.go:206-211` | 子进程退出 |

### 3.2 锁与原子量

| 锁 | 保护对象 | 位置 |
|---|---|---|
| `server.Server.writeMu` | encoder（响应与事件共用） | `server.go:867-878` |
| `server.Server.lifecycleMu` | proxy/tun/wfp/adapters 事务 | 各 handler（如 `server.go:320,409,498,513,567`） |
| `proxy.Server.mu` (RWMutex) | ctx/cancel/listeners/endpoints/running/wg | `server.go:425-433`（RLock 内 Add）、`228-240`（Lock 内停转） |
| `scheduler.mu` | adapters/next/currentWeight | `scheduler.go:7-17` |
| `performanceTable.mu` | links/allocation/decisions | `adaptive.go:61-68` |
| `healthTable.mu` | adapters/domains/domainExpiry | `health.go:43-49` |
| `latencyTable.mu` | values/targets/choices | `latency.go:34-50` |
| `registry.mu` (RWMutex) | connections/adapters 索引 | `registry.go:102-112` |
| `connection.mu` (RWMutex) | upstream/target/remote/adapter | `registry.go:76-100` |
| `steamCDN.mu` | 全部 CDN 学习状态 | `steam_cdn.go:113-142` |
| `steamHTTPObserver.mu` | framer/pending/current | `steam_observer.go:100-113` |
| `udpAssociation.mu` / `udpFlow.sendMu` | flows 表 / 串行化写 | `udp.go:29-56` |
| `tun.Supervisor.mu` + `stopMu` | status / Activate-Stop 串行化 | `supervisor.go:90-108` |
| 原子量 | registry 计数、udp lastActive/lastReply、performance bytes/blocked、dns 统计 | 见各结构体 |

**已确认无 Add/Wait 竞态**的三处（属优点）：
- `acceptLoop`：`s.wg.Add(1)`（`server.go:432`）在 `s.mu.RLock` 内、`go`（:434）之前；而 `Stop` 需先拿写锁把 `running=false`（`server.go:234`）→ 不可能在 Wait 观察到计数为 0 之后再 Add。
- UDP `receiveLoop`：`wg.Add(1)` 在 `go` 之前（`udp.go:228-229`），且其父 `handleClient` 自身已被计数。
- `Start` 的常驻 goroutine：`s.wg.Add(2)` 在两次 `go` 之前（`server.go:189-191`）。

### 3.3 channel 与连接池

- `server.Run` 的 `lines chan []byte`（无缓冲）+ `scanErr chan error`（容量 1）（`server.go:90-91`）。
- `latency.round` 的 `jobs` channel + `close(jobs)` + `wg.Wait()`（`latency.go:294-318`）。
- `raceDoHBatch` 每端点一个 goroutine 写入容量 `len(batch)` 的 channel，首个成功即返回（`resolver.go:351-391`）——容量足够，不会阻塞泄漏。
- TCP relay 缓冲池：`tcpRelayBufferPool = sync.Pool{New: make([]byte, 128KB)}`（`engine/internal/proxy/tcp_profile.go:24-29`），`acquireTCPRelayBuffer/release`（:84-91），并用 `readerOnly`（:96-98）阻止 `io.CopyBuffer` 选中 `WriterTo` 路径而绕过池化缓冲。
- socket 缓冲调优：`SetReadBuffer/SetWriteBuffer = 1MB` + `SetNoDelay`，**全部忽略错误**（`tcp_profile.go:72-82` 与 `dial_windows.go:79-97`）；开关由环境变量 `HYPOMUX_TCP_TUNING`（off/force/auto）与 channel 决定（`tcp_profile.go:31-57`）。
- 连接表：`registry` 以 `map[uint64]*connection` + 自增 ID 记账（`registry.go:102-169`），`Finish` 用 `finished.CompareAndSwap(false,true)` 幂等（`registry.go:233-247`，注释 :234：防重复 Finish 导致活跃计数下溢）。
- UDP 会话表：`udpAssociation.flows map[string]*udpFlow`（按 `target` 字符串索引），上限 256、空闲 120s、扫描 5s（`udp.go:17-22,204-207,400-440`）。

### 3.4 竞态/泄漏可疑点（详见 §7）

1. `steam_observer.go:218-234` 的 **`cdn.mu`→`o.mu` 与 `o.mu`→`cdn.mu` 锁序反转**（§7-2）。
2. `proxy.Stop` 里 `s.wg.Wait()` 的 goroutine 在 ctx 超时后**被泄漏**，且与下次 `Start` 共享同一 WaitGroup（`server.go:249-258`，§7-4）。
3. `scheduling.go:51-57` 只补新 adapter、**不删已移除 adapter 的 health 条目**。
4. `proxy/server.go:455` 的 `_ = adapter` —— handler 返回的“实际选中网卡”被丢弃（死代码，非竞态）。
5. `udp.go:204-207`：flow 数达上限时**静默丢弃**数据报，无日志/遥测（§7-8）。

---

## 4. 错误处理与资源回收

### 4.1 做得好的部分（有明确注释与回滚）

- **命名管道会话**：`server.Run` 的 defer 先 `cancel()` 再 `Close(input)`（`server.go:107-112`），注释解释读取端可能阻塞在 `Scan`；`service_windows.go:318-326` 用 `CancelIoEx + DisconnectNamedPipe + Close` 打断 overlapped 读，`os.File.Close` 单独不可靠（注释 `service_windows.go:301-303`）。
- **proxy 启动回滚**：任一步失败都关闭已开 listener 并 `cancel()`（`server.go:176-182`、`206-213`）。
- **proxy 停止顺序**：`running=false` → `cancel` → 关 listener → `registry.CloseAll()` **先于** `wg.Wait()`（`server.go:234-248`）。
- **TUN 停机**：`terminateRun` 5s 优雅（`InterruptConsoleProcess` 用私有控制台 CTRL_BREAK）→ 关 Job Object 句柄（`KILL_ON_JOB_CLOSE`）→ `Process.Kill()`（`supervisor.go:497-541`，`process_windows.go:102-146`）。
- **`cleanupRun` 先于 `close(run.done)`**（`supervisor.go:477-479` 注释：调用方不能观察到 done 已关而设备仍在移除）。
- **WFP 关闭失败保留句柄以便重试**：`dns_exemption_windows.go:367-385` 与 `server.go:848-861`（注释说明清引用会让句柄在进程余下生命周期内失联）。
- **TUN 配置摘要校验**：校验对象是**已写入的 staged 字节**（`tun/config_stage_windows.go:42-80`，注释 :68-70 关闭了“读文件与算摘要之间被替换”的窗口）；受保护目录拒绝 reparse point 且要求 owner 为 <ADMIN_GROUP>/SYSTEM（`config_stage_windows.go:113-195`）。
- **服务安装的原子性提示**：`writeCoreServicePolicy` 先把 `SchemaVersion=0` 使策略失效，最后再写 1（`service_policy_windows.go:84-121`）。

### 4.2 超时与取消

- 请求级 ctx 进入 handler（`server.go:136`）；`dns.resolve` 在 Core 侧额外夹 30s（`server.go:522-529`，注释直言“**the RPC loop is serial**”）。
- TUN：startup 20s / ready 750ms / config check 10s / cleanup 15s / graceful 5s（`supervisor.go:22-30`）。
- proxy：连接 `ConnectTimeout` 默认 6s、上限 30s（`config.go:180-184`）；stop TUN 20s、stop proxy 5s（`server.go:424-435,464-475`）。
- DNS：`QueryTimeout` 默认 4s、上限 30s；DoH race 每批 2 个端点、按批分预算（`resolver.go:308-349`）；传统 DNS 按服务器 `remaining/(2*(len-index))`（`resolver.go:402-403`）。
- Steam 探测：HTTP 探测 1.5s、拨号 750ms（`steam_http_probe.go:92-101`）、TLS 候选校验 750ms（`steam_cdn.go:694-708`）、候选采集 4s 总超时（`steam_cdn.go:579-655`）、发现 20s（`steam_cdn.go:490-505`）。
- **异常路径**：`diagnostic.run` 没有 Core 侧总预算（见 §7-1）；TUN 子进程用 `context.Background()` 启动，不随 host ctx 取消（§7-5）。

---

## 5. 安全边界

### 5.1 监听地址与端口

- **默认值来源**：`DefaultListenHost = "127.0.0.1"`、`DefaultSOCKSPort = 10800`、`DefaultHTTPPort = 10801`（`engine/internal/proxy/config.go:13-15`）。
- **强制 loopback**：`normalizeConfig` 要求 ListenHost 解析后必须是 loopback，否则 `listen_host must be a loopback IP address`（`config.go:73-79`）。
- `tun_tcp_pool` 模式下禁止同时配 SOCKS/HTTP 端口，改为**每 channel 一个端口**（`config.go:86-95`、`normalizeChannels` `config.go:222-294`），端口非法/被占且 `Port != 0` 时回落到自动分配（`engine/internal/proxy/server.go:203-205`）。**channel 的实际端口由 host 通过 `EngineStartParams.Channels` 提供**（`engine/internal/api/v1/types.go:176-191`），engine 侧没有硬编码——**桌面端实际下发的端口值未验证**（不在 engine 可读范围）。
- 监听器一律 `tcp4`（`server.go:170-176,199-202`）；UDP relay 只绑 loopback 临时端口（`udp.go:76-83`）。

### 5.2 鉴权

- **SOCKS5**：只接受 `method 0x00`（无认证），否则回 `{5,0xff}`（`engine/internal/proxy/socks.go:30-33`）。**无用户名/密码路径**。
- **HTTP 代理**：不校验 `Proxy-Authorization`，且在转发时**删除** `Proxy-Authorization`/`Proxy-Connection`（`http.go:113-127,270-273`）。
- 结论：代理入口的访问控制**完全依赖 loopback 绑定**。本机任意进程（含低权限进程、浏览器扩展）均可作为客户端使用，并会**写入 engine 的调度/健康状态**（domain quarantine、adaptive 份额、Steam 试用配额），从而间接影响其他应用的选路。
- **管道侧鉴权较完整**：服务端校验客户端 PID、可执行文件规范路径与 SHA-256、会话是否 active（`service_policy_windows.go:212-232`、`service_windows.go:427-457`）；客户端校验服务端 PID 必须等于 `--host-pid`（`pipe_windows.go:44-142`）。服务端每轮重新加载策略（`service_windows.go:284-290`），并把策略里的 sing-box 路径与摘要注入会话 metadata（`service_windows.go:288-290`）——**这是 §1 中“`TunExecutableSHA256` 是否被设置”的答案：服务模式下被钉住**；非服务（`serve`/`serve-pipe`）模式下 `main.go:165-181` 未设置该摘要，`authorizeTunConfig` 只做路径比对（`server.go:711-742`）。

### 5.3 输入校验

| 入口 | 校验 | 位置 |
|---|---|---|
| JSONL 请求 | 必须是单个 JSON、不得有尾随 JSON、`protocol==1`、ID/Method 非空 | `server.go:163-185` |
| 消息大小 | scanner 上限 1MB | `server.go:85-86` |
| SOCKS 目标 | ATYP 1/3/4；域名为 0 长度拒绝 | `socks.go:129-158` |
| HTTP 头 | 上限 64KB，超限 `HTTP header exceeds %d bytes`；读超时 10s | `http.go:16-17,76-97` |
| HTTP 主机/端口 | `SplitHostPort` 端口 1..65535；单冒号解析失败报 `invalid explicit host or port`；多冒号非 `[` 开头报 `IPv6 host must use brackets` | `http.go:295-314` |
| UDP ASSOCIATE | TCP peer 必须 IPv4；请求地址非 unspecified 必须等于 peer；本地 listener 必须 IPv4 loopback；**首个数据报锁定客户端端口**，之后端口不符丢弃 | `udp.go:65-79,162-167` |
| SOCKS UDP 报文 | `len>=4` 且前三字节 0；ATYP=1 需 `len>=10`、ATYP=4 需 `len>=22`；端口 0 或 payload 空即拒绝；`::ffff:` 映射地址 `To4()` 归一 | `udp.go:480-519` |
| UDP 报文长度缓冲 | 单缓冲 `maxSOCKSUDPDatagramBytes = 65535`（`udp.go:17-22`）；回复缓冲按 `len(header)+65535` 一次分配 | `udp.go:133,551-559` |
| DNS 响应 | TCP 长度前缀为 0 或 >64KB 报 `invalid DNS TCP response length %d`；DoH body 上限 64KB+1；Content-Type 必须为 `application/dns-message` | `resolver.go:477-522,524-585` |
| MTU 变更 | IfIndex>0、GUID 长度 36 且格式校验、576..65535；PowerShell 脚本里再比对 GUID 且拒绝 `HypoMux-Tun`、要求当前 MTU 等于期望值 | `engine/internal/platform/mtu.go:1-28`、`mtu_windows.go:1-33` |
| TUN 配置 | 必须常规文件、摘要（可选）常量时间比对、startup timeout 100ms..60s | `tun/supervisor.go:628-670`、`fileintegrity/fileintegrity.go:37-54` |
| Steam 探测 URI | 必须相对路径、无 host、且通过 `steamRequestURI` 白名单 profile（拒绝 Cookie/Authorization/Range/未知 query/过期签名） | `steam_http_probe.go:158-165`、`steam_observer.go:19-93` |

未在 engine 发现命令注入面：对外部 PowerShell 的调用（MTU、sharing、TUN 清理）参数均为整数或已校验的十六进制 GUID/固定字符串（`mtu_windows.go`、`sharing_windows.go`、`tun/cleanup_windows.go:17-59`）。

---

## 6. 与 protocol/v1 契约的对应

`protocol/v1/manifest.json`（162 行）声明：`protocol:1`、`transport:"stdio-jsonl"`、`max_message_bytes:1048576`、`compatibility:"additive"`（`manifest.json:2-5`）、6 个 states（:6-13）、18 个 methods（每个含 `idempotency`/`privilege`/`cancellation`，`manifest.json:14-`）、5 个 events、14 个 `error_codes`。

| 契约项 | engine 侧 | 是否一致 |
|---|---|---|
| `protocol` / `transport` / `max_message_bytes` | `protocol.Version=1`、`protocol.Transport="stdio-jsonl"`、`protocol.MaxMessageBytes=1MB`（`engine/internal/protocol/protocol.go:5-9`） | ✅ 由测试强制（`engine/internal/api/v1/contract_test.go:46-107`） |
| 18 个方法名与顺序 | `engine/internal/api/v1/types.go:21-38,47-66` 的 `Capabilities()` | ✅ `slices.Equal(methods, Capabilities())`（`contract_test.go:46-107`） |
| 6 个状态 | `engine/internal/runtime/runtime.go:11-18` | ✅ 顺序一致 |
| 5 个事件与投递语义 | `types.go:40-45` | ✅ 顺序一致，均 `coalescible:false` |
| **error_codes** | 引擎实际发出 18 个码 | ❌ **14 中有 4 个未登记**（见 §7-3） |
| 信封字段（`protocol/id/method/params`、`protocol/id/result\|error`、`protocol/sequence/event/data`） | `protocol.go:12-40` | ⚠️ **manifest 未声明信封 schema**，仅由 `protocol/v1/fixtures/messages.json` + `contract_test.go:109-195` 约束（每个 capability 必须有 request/response fixture，每个事件必须有 event fixture） |

另外两点一致性观察：
- `manifest.json` 中 `wfp.inspect.privilege = "administrator_for_repair"`，与 `server.go:264-267`（非提权且 `repair=true` 时把错误码改成 `elevation_required`）语义吻合。
- `diagnostic.run.cancellation = "host_context_only"`，与 `server.go:218-226` 直接同步调用、不接受调用方取消一致——**这正是 §7-1 的风险来源**。
- `engine.scheduling.idempotency = "safe_retry_state_only_response_is_previous_config"`，与 `Server.UpdateScheduling` 返回 `previous` 一致（`engine/internal/server/scheduling.go:55-60`）。

---

## 7. 风险清单（Top 8）

### [置信度 高] 1. `diagnostic.run` 在串行 RPC 循环中同步执行，最坏可阻塞控制面约 1000 秒
- **文件:行号**：`engine/internal/server/server.go:218-226`（同步调用）+ `engine/internal/diagnostic/diagnostic.go:116-133`（上限 Count≤100、Timeout≤10s）+ `server.go:134-139`（`s.handle` 在唯一主循环里被同步调用）
- **证据**：`result := diagnostic.Run(ctx, params.Config())` 直接在主循环执行；`withDefaults` 允许 `Count=100`、`Timeout=10s`，`Run` 对每次探测最多等待一个完整超时（`diagnostic.go:53-114`），因此不可达目标下最坏 `100 × 10s = 1000s`。同一文件的 `dns.resolve` 明确加了 Core 侧上限，注释写着“**the RPC loop is serial**, so a client-only timeout would still stall the next DNS target”（`server.go:522-529`）——说明作者知道该风险但未对 `diagnostic.run` 施加同类约束。manifest 也把它标为 `host_context_only`（调用方无法取消）。
- **影响**：一次 `diagnostic.run` 期间，`engine.status`/`engine.stop`/`host.shutdown`/`tun.deactivate` 全部无法被处理；桌面端“停止/退出”表现为卡死；若 host 侧超时后放弃，engine 仍继续阻塞。
- **修复方向**：给 `diagnostic.run` 加 Core 侧总预算（例如 `min(Count*Timeout, 10s)` 的 context），或把该 handler 移到独立 goroutine 并用“请求-响应配对”写回，或限制 `Count` 与 `Timeout` 的乘积。

### [置信度 中] 2. Steam 观察者存在锁序反转（`cdn.mu`→`o.mu` vs `o.mu`→`cdn.mu`），可致 `cdn.mu` 永久死锁
- **文件:行号**：`engine/internal/proxy/steam_observer.go:218-234`（持 `c.mu` 时取 `o.mu`）对比 `steam_observer.go:303-311`（持 `o.mu` 时取 `c.mu`）
- **证据**：`newSteamObserver` 在 `c.mu.Lock(); defer c.mu.Unlock()`（:218-219）尚未释放时执行 `o.mu.Lock()`（:232）。而 `feed`（:303）、`expire`（:238）、`stopLocked`（:283）都是 **先 `o.mu` 再 `c.mu`**。若在 :233 `context.AfterFunc(c.ctx, o.close)` 注册的瞬间 `c.ctx` 已取消，`AfterFunc` 会**立即在新 goroutine 调用 `o.close`**，该 goroutine 持 `o.mu` 阻塞在 `c.mu`，而 `newSteamObserver` 持 `c.mu` 阻塞在 `o.mu` → 互相等待。触发窗口：`c.ctx` 是 `s.ctx` 的子 context，`proxy.Server.Stop` 在**不持 `cdn.mu`** 的情况下 `s.cancel()`（`engine/internal/proxy/server.go:235-237`），因此无法被 `cdn.mu` 串行化；而 `active()` 检查（`steam_observer.go:214`）与 :233 之间存在真实时间窗口。
- **影响**：一旦命中，`cdn.mu` 永不释放 → 之后所有 Steam 请求路径（`prepareSteamCDN`、`trafficChange`、`snapshot`）全部卡住；`Stop` 因 `wg.Wait` 超时返回 `stop proxy: context deadline exceeded`，引擎进入 `failed`，需要重启进程。
- **修复方向**：不要持 `c.mu` 时取 `o.mu`（把 `observers++` 与 `AfterFunc` 注册拆到 `c.mu` 释放之后，或统一规定 `o.mu` 先于 `c.mu` 并让 `newSteamObserver` 也遵守）；同时给 `Stop` 的 cancel 路径加 `cdn.mu` 保护或改为 `configure()` 内的取消。

### [置信度 高] 3. `protocol/v1/manifest.json` 与实际错误码漂移：引擎多发出 4 个未登记错误码
- **文件:行号**：`engine/internal/server/server.go:233`（`engine_running`）、`server.go:243`（`mtu_failed`）、`server.go:252`（`hotspot_inspection_failed`）、`engine/internal/server/scheduling.go:57`（`update_failed`）；契约测试 `engine/internal/api/v1/contract_test.go`（错误码断言仅“非空”，见 :100-105）
- **证据**：grep 全部 `protocol.Failure(` 得 49 处命中，其中上述 4 个字符串不出现在 `protocol/v1/manifest.json` 的 `error_codes`（14 个）里；`contract_test.go` 只做 `if len(manifest.ErrorCodes) == 0 { t.Fatal(...) }`，**不校验集合包含关系**。
- **影响**：按 manifest 生成客户端错误处理表的桌面端会把这 4 类错误归到 unknown 分支，`mtu.set` 在引擎运行时的拒绝（`engine_running`）尤其容易被误判为传输故障；`compatibility:"additive"` 的承诺在错误码维度出现破洞。
- **修复方向**：把 4 个码补进 manifest；在 `contract_test.go` 里改为“所有 `protocol.Failure` 使用的码都必须∈manifest，且 manifest 的码都必须被用到”的双向校验（需要一份可枚举的码表）。

### [置信度 中] 4. `proxy.Stop` 的 `wg.Wait()` goroutine 可能永久泄漏，且与下一次 `Start` 共享同一 WaitGroup
- **文件:行号**：`engine/internal/proxy/server.go:249-258`
- **证据**：`go func(){ s.wg.Wait(); close(done) }()` 与 `select { case <-done: case <-ctx.Done(): return fmt.Errorf("stop proxy: %w", …) }`。ctx（调用方给 5s，`engine/internal/server/server.go:430-435`）超时后该 goroutine 仍阻塞在 `wg.Wait()`；若某个被计数 goroutine 不退出（relay 卡在 `io.Copy`、UDP `receiveLoop`、Steam 探测 20s、TUN 日志泵），`s.wg` 永不为零。此后 `Start` 又向**同一个 `s.wg`** `Add`（`server.go:162-165,189,222`），下一次 `Stop` 必然超时。
- **影响**：反复 `engine.start`/`engine.stop` 后停止操作稳定失败（`stop_failed`），进程内残留 goroutine 与 socket；表现为“重启网络服务后无法再正常停止”。
- **修复方向**：每次 `Start` 使用新的 `sync.WaitGroup`（把 wg 放进一个每次 Start 重建的 `runtime` 结构），或让 `Stop` 在超时后继续尝试 `CloseAll` + 强制关闭 TUN/UDP relay socket，保证被计数 goroutine 有确定退出路径。

### [置信度 中] 5. TUN 子进程不绑定调用方 context，只能靠 Stop/job object 终止
- **文件:行号**：`engine/internal/tun/supervisor.go:195-201`
- **证据**：`command := s.command(context.Background(), normalized.Executable, "run", "-c", configPath)` —— 显式用 `context.Background()`，注释仅解释“不得阻塞 Activate”，但代价是 host ctx 取消（如服务 Stop / 管道断开）**不会**终止 sing-box；`waitProcess` 的清理只在进程自己退出或被 `terminateRun` 触发时才跑（`supervisor.go:436-489`）。
- **影响**：若 `server.Run` 因管道异常退出而 `stopProxyForHostExit` 未能覆盖（例如进程被强杀、panic），sing-box 与 Wintun 设备/路由将残留在系统上，只能等下一次 `recover`/安装时清理（`tun/cleanup_windows.go:61-96`）。job object 的 `KILL_ON_JOB_CLOSE` 只保证 engine 进程退出时子进程被杀（`process_windows.go:102-146`）。
- **修复方向**：用 `s.stopCtx`（随 host ctx 取消）派生 `exec.CommandContext`，或在 Run 退出路径上注册 `context.AfterFunc(hostCtx, func(){ supervisor.Stop(...) })`。

### [置信度 中] 6. TUN 恢复/IPv6 回退依赖脆弱的错误字符串匹配
- **文件:行号**：`engine/internal/server/server.go:744-763`（`isIPv6AddressSetupError` / `isStaleTunAdapterError`）+ `engine/internal/tun/cleanup_windows.go:53`（抛出 `stale HypoMux-Tun device still exists: <ids>`）
- **证据**：匹配目标包括 `"set ipv6 address"`、`"element not found"`、`"cannot create a file when that file already exists"`、`"create adapter"+"open existing adapter"+"element not found"`、`"stale hypomux-tun device still exists"`——其中多数是 **Windows PowerShell / netsh / pnputil 的本地化或版本相关文案**，而 `cleanup_windows.go:53` 的字符串是唯一“自家可控”的那个。
- **影响**：系统语言非英文（或 PowerShell 版本差异）时，`isIPv6AddressSetupError` 与 `isStaleTunAdapterError` 静默失效：既不重试也不回退 IPv4，`tun.activate` 直接失败（`tun_failed`），或残留设备无法被识别为“stale”而反复失败。
- **修复方向**：在清理脚本里输出结构化标记（例如固定前缀 `HYPOMUX_ERR_STALE_DEVICE`）并只匹配自家标记；对 IPv6 设置失败改用配置/设备状态探测而非文案。

### [置信度 中] 7. SOCKS5/HTTP 入口完全无鉴权，任何本机进程都能驱动选路与污染调度状态
- **文件:行号**：`engine/internal/proxy/socks.go:30-33`（只支持 method 0x00）、`engine/internal/proxy/http.go:113-127`（无 `Proxy-Authorization` 校验并在转发时删除该头）、`engine/internal/proxy/udp.go:162-167`（仅按源 IP + 首个端口过滤）
- **证据**：`normalizeConfig` 强制 loopback（`engine/internal/proxy/config.go:73-79`）确实把暴露面限制在本机，但**没有任何凭据校验**：本机任意用户/进程都可连接 `127.0.0.1:10800/10801`（或 channel 端口）。它会真实地写入共享状态：`health.recordComparativeDomainFailure`（`engine/internal/proxy/dial.go:130-135`）、`adaptiveAllocation.observe`（`engine/internal/proxy/adaptive.go:165-211`）、Steam 试用配额与 `decisions`（`engine/internal/proxy/steam_observed_probe.go:207-211`）。
- **影响**：低权限本机程序可(a)白用代理；(b)通过制造失败/流量把某个域名或网卡“投毒”进 30 分钟隔离或改变自适应份额，从而影响其他应用的连通性与速度；(c)占用 Steam 试用配额（每 adapter 8 个候选、每 30 秒轮换）。
- **修复方向**：给 SOCKS5 增加用户名/密码（method 0x02）或 `Proxy-Authorization` 校验，并在 `engine.start` 时下发一次性 token；至少要把“协议层可见的客户端身份（本机 PID）”纳入准入判断。

### [置信度 中] 8. UDP flow 达到上限时静默丢包，无日志也无遥测
- **文件:行号**：`engine/internal/proxy/udp.go:204-207`（`if len(a.flows) >= a.flowLimit { a.mu.Unlock(); return }`），常量 `defaultUDPFlowLimit = 256`（`udp.go:17-22`）
- **证据**：超限时直接 `return`，不写日志、不计数、不回 SOCKS 错误（此时已回 REP=0）、也不在 `registry`/telemetry 中体现；`udp.go` 中没有任何与 flowLimit 相关的计数或 `note()` 调用。
- **影响**：TUN 模式下 UDP 流量（游戏、QUIC、DNS over UDP）在目标数超过 256 时表现为**无规律丢包**，用户与日志都无法定位原因；`engine.telemetry` 的 UDP 连接数也只反映已建立的成功 flow。
- **修复方向**：超限时至少打一条限频日志/遥测计数器（可复用 `reportConnectFailure` 的限频模式，`engine/internal/proxy/server.go:56-73`）；或对小包/已建立 flow 做淘汰策略，而不是直接丢弃。

---

## 8. 优势（证据化）

1. **依赖方向严格单向、无环**：`cmd → server → {proxy,dns,tun,wfp,platform,runtime,protocol,api} → 叶子`，且没有任何 internal 包反向依赖 `server`（§1.3 的 import 实测）。这让 `proxy`/`dns` 可以独立测试（proxy 目录 27 源 + 28 测试文件）。
2. **协议层有可执行的契约测试**：`engine/internal/api/v1/contract_test.go:46-107` 强制方法名与顺序、状态集合、事件集合、`max_message_bytes` 与 `protocol.Version` 完全一致，并用 `protocol/v1/fixtures/messages.json` 对每个方法的请求/响应与每个事件做解码验证（`contract_test.go:109-195`）——这是一条真实的“编译产物 ↔ 文档契约”耦合。
3. **可注入的边界明显，利于测试**：`main.go:22-30`（version/commit/recoverTUN/install/remove）、`proxy.Server` 的 `dialTCP/dialUDP/listenTCP/listenUDP`（`proxy/server.go:17-46`）、`tun.Supervisor` 的 command/cleanup/contain/configure/interrupt/stageConfig/startupReady（`tun/supervisor.go:90-123`）、`latencyTable.probe` 默认 `diagnostic.Run`（`latency.go:53`）。
4. **平台差异被 `_windows.go` / `_other.go` 隔离**：`dial_windows.go` / `dial_other.go`、`health_errno_windows.go` / `health_errno_other.go`、`mtu_windows.go`、`identity_windows.go`；非 Windows 下 `enableTCPDialerTuning` 是空实现（`dial_other.go:40`），保证跨平台可编译（**本机无 Go，未实际验证编译**）。
5. **字节序陷阱有显式注释与固定常量**：`IP_UNICAST_IF=31` 与 `bits.ReverseBytes32`（`dial_windows.go:15-16,63-69`），以及 WFP 侧 `binary.BigEndian.Uint32` 加 “**Do NOT switch back to LittleEndian**” 注释（`wfp/dns_exemption_windows.go:284-286`）。这两处是 Windows 网络代码最常见的隐性错误，作者用注释把意图钉死了。
6. **资源回收顺序在关键路径上被刻意设计并注释**：`cleanupRun` 先于 `close(run.done)`（`tun/supervisor.go:477-479`）、WFP Close 失败保留句柄以便重试（`wfp/dns_exemption_windows.go:377-379`、`server.go:854-856`）、`registry.CloseAll()` 先于 `wg.Wait()`（`proxy/server.go:248`）、`Stop` 先 cancel 再 Close input（`server.go:105-112`）。这些注释解释了“为什么是这个顺序”，是本仓库最有价值的隐性知识。
7. **失败分类与限频考虑了真实网络语义**：`socksConnectFailureReply` 对 `errors.Join` 递归并在多码并存时统一返回 1（`socks.go:94-127`，注释：多网卡因不同原因失败时不能宣称目的地拒绝）；`reportConnectFailure` 1 秒限频（`proxy/server.go:56-73`）；`recordFailure` 只在本地失败且 latency-first 时污染健康度（`udp.go:575-584`）。
8. **“不拿瞬时速率排名”的自适应设计是有原则的**：`adaptive_allocation.go:8-11` 明确“只改带宽份额、保留 1/(2N) 均匀下限”，`choose` 在非 adapting 时直接回退到 legacy（`adaptive_allocation.go:51-70`），并对背压/空闲窗口显式排除（`adaptive.go:176,198`）。这比常见的“按最近吞吐排序”实现更难被短时抖动欺骗。
9. **Steam 优化做了内容级验证与自博弈防护**：候选必须内容逐字节匹配 baseline（`steam_cdn.go:507-571`）或通过 TLS 校验（:694-708）；`useTrial` 用 1/8 探索 + 保留原节点控制流（`steam_observed_probe.go:229-231`）避免自身基线饿死；all 探测走独立预算（`steam_speed_probe.go:10-31`）且**先扣费再 I/O**（注释 :27）。
10. **管道是本仓库最强的安全边界**：SDDL 只给 SYSTEM/BA/IU、`PIPE_REJECT_REMOTE_CLIENTS`、单实例、客户端 PID + 可执行文件规范路径 + SHA-256 + 会话 active 四重校验（`service_windows.go:30,396-457`、`service_policy_windows.go:302-466`），且服务每轮重新加载策略并把钉住的 sing-box 摘要注入会话（`service_windows.go:284-290`）。

---

## 附录 A：未验证项（供主理人复核）

1. **未运行任何 Go 测试/编译**：本机无 Go 工具链。所有“测试存在”的结论仅来自读取 `*_test.go` 文件名与函数名，未执行。
2. **桌面端实际下发的端口与参数**：engine 侧默认 `10800/10801`（`proxy/config.go:13-15`），但 `tun_tcp_pool` 模式下端口由 host 通过 `EngineStartParams.Channels` 提供（`api/v1/types.go:176-191`）；**实际运行时端口值、`listen_host` 是否被改、`domain_isolation` 是否被关闭，需要读桌面端代码确认**。
3. **非服务模式下的 `TunExecutableSHA256` 来源**：`main.go:165-181` 未设置；除 `service_windows.go:288-290` 之外**未发现其它设置点**（已 grep 全 engine）。因此 `serve`/`serve-pipe` 形态下 `authorizeTunConfig` 只做路径比对（`server.go:711-742`）——是否存在第三方注入点未验证。
4. **§7-2 的死锁窗口**：结论基于 `context.AfterFunc` 在“ctx 已取消时立即在新 goroutine 调用 f”的语义与该处锁序的静态分析。**未构造实际并发复现**（无 Go 工具链）。
5. **`engine/internal/proxy/steam_cdn.go` 中 `prepareSteamCDN` 的嵌套锁（`cdn.mu` → `session.mu`，:780-795）与 `steam_observer.go`/`steam_evaluation.go` 的其余路径**：已检查 `registry.Attach`（`r.mu`→`session.mu`，`registry.go:171-192`）与 `Snapshot`（先释放 `r.mu` 再取 `session.mu`，`registry.go:267-325`），未发现 `session.mu → cdn.mu` 的路径，但**未做全量调用图证明**。
6. **TUN 侧 sing-box 配置的实际内容**（inbounds/outbounds/FakeIP、是否把 DNS 指向 engine）：配置由 host 生成（engine 只做 `configuredTunIPv4Address` 解析，`tun/supervisor.go:334-361`），**不在 engine 仓库内**。
7. **`protocol/v1/fixtures/messages.json` 的实际内容**：只确认了它的路径与 `contract_test.go` 的使用方式，未逐条读取。
8. **`engine/internal/platform/` 下未逐行阅读的文件**：`identity_other.go`、`mtu_other.go`、`sharing_other.go`、`wfp_other.go`（非 Windows 空实现）与 `mtu_test.go`、`sharing_windows_test.go`。本报告只逐行覆盖 `identity.go`/`identity_windows.go`、`mtu.go`/`mtu_windows.go`、`sharing.go`/`sharing_windows.go`、`wfp.go`/`wfp_windows.go`。
