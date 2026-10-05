# HypoMux 错误处理审计报告

> **审计问题**：哪些错误被静默吞掉？哪些失败路径没有回滚？
> **范围**：`engine/`（Go 核心守护进程）、`desktop/`（Wails3 桌面应用）、`protocol/v1/`、`desktop/frontend/`、`desktop/portable/`、`.github/`
> **方法**：1 名主审全仓交叉核验 + 6 条专项子线并行深挖（互不重叠的文件范围）。所有结论均来自实际读取的代码，每条给出精确到行号的原文引用。

---

## 结论摘要

1. **主干的"业务层"错误处理有纪律，"系统级副作用"的收尾一律薄弱。** 规则集保存回滚、启动回滚闭包、原子写入等处明显经过打磨，甚至留有整改注释；但凡涉及**系统状态变更的收尾**（WFP 子层、虚拟网卡、TUN、系统代理、注册表策略键、Windows Service），错误几乎一律 `_ =`，丢弃后没有重试、没有落盘、没有上报。

2. **最集中的问题区域是「安装 / 退出 / 启动失败 / 协议契约」四条路径**，它们共享同一个结构：先做了若干**不可原子化**的改动，随后某一步失败，而失败分支里没有 undo。例如 `installWindowsService` 做了「停服务 → 改配置 → 写注册表 → 设恢复动作 → 启动」五步，失败时会在机器上留下一个开机自启、却永远起不来的服务。

3. **最严重的单点是 WFP 子层永生** —— `engine/internal/wfp/dns_exemption_windows.go` 在事务外创建了一个权重 `0xFFFF`（最高）的子层，而 `Close()` 只关闭引擎句柄、从不删除子层或过滤器，且删除用的 Win32 API 在全仓从未被加载（全仓检索 `SubLayerDelete|FilterDelete` 命中数为 **0**）。这是本次审计唯一一处**跨进程、无法自愈**的系统状态污染。

4. **"把『查不出来』当成『否定』"是最危险的吞错形态，本报告共找到 7 处。** 最严重的两处都在安全路径上：`service_policy_windows.go:333` 把 `libcronet.dll` 的 stat 错误当成"文件不存在"，从而**跳过 ACL 校验**（本地攻击者可获得 SYSTEM 代码执行）；`main.go:166-170` 把 `Snapshot()` 的错误当成"引擎已停止"，**失败开放地绕过 NAT 检测守卫**。

5. **全仓 `recover()` 命中数为 0**（含测试文件），而生产代码中至少存在 23 处非测试 `go func()`。任何 goroutine 内的一次 panic 会直接杀死整个桌面进程或 LocalSystem 服务进程，且现场没有任何 panic 上下文落盘。更严重的是 `engine/internal/proxy/steam_observer.go` 在持有 `observer.mu` 时再取 `cdn.mu`，**panic 即死锁**，回收路径被自己锁死。

6. **错误契约在第三跳丢失。** engine 的 `protocol.Error{Code,Message,Details}` 与 engineclient 的 `RemoteError` 两跳保真，但 `client.go:30-49` 把 Code 拼进 `Error()` 字符串后，Wails 只传字符串——全量检索证明协议冻结的 14 个错误码在 `desktop/frontend/src` 的 118 个文件中**出现次数为 0**，前端完全靠中文/英文子串匹配反推。同时 engine 侧还发射了 4 个**契约外**错误码。

7. **值得肯定**：CI 层是全仓最干净的部分——4 个 workflow 无 `continue-on-error`、无被注释掉的检查，`package.json:9` 的 `tsc && vite build` 确保类型错误会 fail CI。3 处 `|| true` 分别在 `trap cleanup` 与被下游 `exit 1` 接管的 curl/ls-remote 中，属正确用法。

---

## 发现分布

| 严重度 | 条数 | 说明 |
|---|---|---|
| **P0** | **31** | 数据损坏 / 系统状态不一致 / 静默失效 / 安全绕过 |
| **P1** | **61** | 错误链断裂、难排查、并发与资源泄漏 |
| **P2** | **77** | 整洁度、误报风险、死代码、错误信息质量 |

**合计 169 条。**

### 覆盖情况与已知缺口

本轮并行审计了 7 个专项分片，**7 个已全部合流**：`engine-cmd.md`(31 条)、`engine-core.md`(20 条)、`engine-dns-server.md`(27 条)、`engine-proxy.md`(19 条)、`desktop-shell.md`(27 条)、`frontend-protocol.md`(20 条)、`desktop-services.md`(31 条)。

但**分片交付不等于目录全覆盖**，仍存在已知的扫描缺口，如实列出：

1. **`desktop/internal/services/hyperv_adapter.go`（121 KB，本目录最大文件）+ `adapters.go` + `hyperv_adapter_windows.go` 未被系统覆盖** —— 这是**虚拟网卡安装/卸载的主战场、回滚缺失的高发区**，建议作为下一轮首要补扫对象。
2. `desktop/internal/services/network_routes_windows.go` / `routing.go`（路由表）、`hotspot*.go` / `process_windows.go`（进程与端口）、`nat_servers.go`（socket 生命周期）、`tun_startup_dns.go`（DNS 回滚）—— 均未覆盖。
3. 任务描述里提到的 `tun_cache*.go`、`tun_routing*.go` **实际不存在**（只有对应的 `_test.go`），非漏扫。

注：本报告正文已顺带命中该目录的若干关键问题（`system_proxy_other.go:11,13` 假成功、`atomic_file.go:13` 固定临时名、`settings.go` / `blocked_domains.go:214` / `nat_servers.go:277` / `tun_config.go:285` / `support_log.go:220` 的同类原子写、`singbox_rules.go:765,768` 缺失 `Sync()`），并在 [02-platform-compat.md](02-platform-compat.md) 第六章有完整清单。

### 附带的深度合并产物

`reports/parts/engine.md`（3525 行，97 条：**P0 19 / P1 40 / P2 38**）是 `engine/` 模块四条分片的深度合并版，含一节**跨分片交叉结论**做去重与同根因归并（典型：「零 `recover()`」是四个分片独立报告的同一根因；「Service 停机超时退进程」+「panic 杀进程」共同跳过 `server.go:1012` 的 `stopProxyForHostExit`，解释了为什么下列单点问题在生产里会放大成机器彻底断网）。本报告的分级口径与它一致，但更精简、更适合按优先级施工。

---

## P0 清单速查表（31 条）

### A. 系统状态泄漏 / 不可自愈

| # | 位置 | 一句话 |
|---|---|---|
| A1 | `engine/internal/wfp/dns_exemption_windows.go:213,216,242,375,383` | WFP 子层与已提交过滤器**永不删除**，跨进程永久残留 |
| A2 | `engine/internal/server/scheduling.go:44-58` | WFP DNS 豁免替换无回滚：先装新、`:50` 拆旧，`:57` 才做可能失败的 `UpdateScheduling` |
| A3 | `engine/internal/tun/supervisor.go:241-247` | `containProcess` 失败只 Kill+Wait，不调 `s.cleanup` → Wintun 网卡/默认路由/WFP 规则残留 |
| A4 | `engine/internal/tun/cleanup_windows.go:118-120,121-127` | `if enumErr != nil { continue }` 把硬枚举错误降级成「无残留设备」，跳过整段清理 |
| A5 | `engine/internal/vnic/manager.go:172,174`（对照 `:226`） | vNIC 回滚绕过 `waitForKeeperStop`，留下孤儿 sidecar |
| A6 | `engine/cmd/hypomux-engine/service_windows.go:63,98-110` | 安装改了系统状态后**没有任何 undo** |
| A7 | `engine/cmd/hypomux-engine/service_windows.go:130-136` | 卸载路径回滚不对称：删服务与删注册表键之间无补偿 |
| A8 | `desktop/internal/engineclient/privileged_windows.go:119-120` | 提权核心 `Kill()` 必然失败且错误被丢弃 → 孤儿管理员进程 |
| A9 | `desktop/internal/engineclient/client.go:284-285,290-291` | 会话被丢弃时 `Kill()` 错误被丢弃 → 引擎进程残留 |
| A10 | `desktop/main.go:239-241` | `log.Fatal` 跳过全部服务清理 → 网卡/代理/进程残留 |

### B. 退出路径跳过清理

| # | 位置 | 一句话 |
|---|---|---|
| B1 | `desktop/internal/services/engine.go:1203` | `Shutdown()` 丢弃 `Stop()` 收集的全部停止错误，随即拆除 IPC |
| B2 | `engine/internal/server/server.go:97,1012-1041` | 引擎退出清理全链路吞错，进程随即退出无重试机会 |
| B3 | `engine/cmd/hypomux-engine/service_windows.go:265` | Service 停机超时直接退出进程，跳过唯一的 DNS/TUN/VNIC 回滚，还回报"成功" |
| B4 | `engine/internal/tun/supervisor.go` P1-2（`cleanupRun` 被 ctx 超时跳过） | `Stop` 返回的清理失败信息彻底丢失 |

### C. 静默失效 / 安全绕过

| # | 位置 | 一句话 |
|---|---|---|
| C1 | `engine/cmd/hypomux-engine/service_policy_windows.go:333` | `libcronet.dll` 的 stat 错误被当成"不存在" → **跳过 ACL 校验** |
| C2 | `engine/cmd/hypomux-engine/service_policy_windows.go:100,112-119` | 策略键写一半失败，摧毁原本可用的策略且无回滚 |
| C3 | `desktop/main.go:166-170` | `Snapshot()` 错误被当成"引擎已停止"，**失败开放**，绕过 NAT 检测守卫 |
| C4 | `desktop/internal/engineclient/client.go:492-498` | 事件通道 `select { default: }` 静默丢弃，TUN 失败与 DNS 回退事件被吞 |
| C5 | `engine/internal/dns/resolver.go:282,425` | DNS 超时被当成"无事发生" → fallback 事件在最常见故障模式上永不响 |
| C6 | `engine/internal/dns/wire.go:112` | RCODE 被压平成无类型字符串 → 语义正确的 NXDOMAIN 两次就能隔离域名 |
| C7 | `engine/internal/proxy/udp.go:88,99` | 默认配置下 UDP ASSOCIATE **必然失败**，且原因被伪装成 `unknown UDP channel ""` |
| C8 | `engine/internal/dns/resolver.go` DoH 竞速批次 | `ctx.Done()` 分支丢弃**已经成功的结果**，把成功变成失败 |
| C9 | `engine/internal/proxy/server.go:418` | `acceptLoop` 遇持续性 Accept 错误热自旋，打满一个 CPU 核 |

### F. 桌面 services —— 数据损坏 / 用户配置丢失

| # | 位置 | 一句话 |
|---|---|---|
| F1 | `desktop/internal/services/settings.go:195` | 旧配置迁移前的备份被静默跳过，`:202` 仍用迁移结果覆盖配置 → 回滚时新旧两代配置一起消失，UI 却显示成功 |
| F2 | `desktop/internal/services/support_log.go:189` + `:91` | 一次瞬时读失败就让 `sessions` 为 nil，`:222` rename 空内容覆盖正式日志 → **每次引擎启动执行一次，历史诊断日志归零** |
| F3 | `desktop/internal/services/system_proxy_windows.go:69-88` | `enableSystemProxy` 逐条改注册表却**无内部回滚** → `ProxyEnable=1` 成功、`ProxyServer` 失败即全机断网 |
| F4 | `desktop/internal/services/settings.go`（reload 路径） | reload 把"旧配置读不出来"当成"没有旧配置"，默认值静默覆盖用户设置 |
| F5 | `desktop/internal/services/tun_address.go:31,49,161` | 强制启动时丢弃全部地址冲突检查错误 → 可能把 TUN 分配到用户**真实局域网网段** |
| F6 | `desktop/internal/services/engine.go:1137,1179` | `Stop()` 在核心不可达时**跳过全部拆除**，虚拟网卡与路由残留，UI 显示"已停止" |

### D. 契约违规 / 数据损坏

| # | 位置 | 一句话 |
|---|---|---|
| D1 | `engine/internal/server/server.go:255,265,274`、`scheduling.go:57` | 发射 4 个**契约外**错误码，测试结构上无法发现 |
| D2 | `desktop/portable/launch-portable.cmd:225-241` | 种子 `settings.json` 部分写入后仍报"已写入"，旧值被截断不可恢复 |

### E. 崩溃面

| # | 位置 | 一句话 |
|---|---|---|
| E1 | 全仓（`recover()` 命中数 = 0） | 全仓零 panic 边界 + ≥23 处非测试 goroutine |
| E2 | `engine/cmd/hypomux-engine/service_windows.go:214,293` | 两个承载整个引擎栈的后台 goroutine 无 recover |
| E3 | `engine/internal/proxy/server.go:434` | 所有每连接 goroutine 无 recover，单个畸形报文可打崩整个守护进程 |
| E4 | `engine/internal/proxy/steam_observer.go:238,291,303` | 持 `observer.mu` 时再取 `cdn.mu`，**panic 即死锁** |

---

## P0 详述

### A1　WFP 子层与已提交过滤器永不删除（跨进程永久泄漏）

**代码**

- `engine/internal/wfp/dns_exemption_windows.go:213` — `		Weight:      0xFFFF,`
- `engine/internal/wfp/dns_exemption_windows.go:216` — `		wfp.subLayerAdd,`（所属调用块起于 `:215` `	if err := wfp.call(`）
- `engine/internal/wfp/dns_exemption_windows.go:242` — `	if err := wfp.call(wfp.transactionBegin, "FwpmTransactionBegin0", uintptr(engine), 0); err != nil {`
- `engine/internal/wfp/dns_exemption_windows.go:375` — `	status, _, _ := wfp.engineClose.Call(uintptr(session.engine))`
- `engine/internal/wfp/dns_exemption_windows.go:383` — `	session.filterIDs = nil`
- 失败路径：`:196-200` — `defer { if !success { _ = owned.Close() } }`（同模式另见 `server.go:650`、`scheduling.go:50`）

**触发条件**　打开 DNS egress 豁免的流程先在 `:215–223` 调用 `FwpmSubLayerAdd0` 建子层，**再**在 `:242` 开启事务。**子层添加发生在事务开启之前**——WFP 中事务外添加的对象立即生效并持久化，不受后续 `transactionAbort` 保护。无论事务最终提交还是中止，子层都留在 BFE 里。

**后果**　名为 "HypoMux DNS egress exemption"、权重 `0xFFFF`（WFP 内最高匹配优先级）的子层**在用户机器上永久残留**。已提交的过滤器同样不会被删：`Close()`（`:367–385`）全函数体只调用了 `engineClose`（`:375`），随后 `:383` 直接 `session.filterIDs = nil` 清空记录。删除所需的 `FwpmSubLayerDeleteByKey0` / `FwpmFilterDeleteById0` **在 `newAPI()`（`:387–400`）中从未被加载**。

**佐证**　全仓检索 `SubLayerDelete|FilterDelete|filterDelete|subLayerDelete` → **No matches found**。整个代码库不存在任何一处删除这些 WFP 对象的实现。

**调用链**　`engine/internal/server/server.go:658` `		s.dnsExemption = exemption`；`server.go:1044` `	current := s.dnsExemption`；`engine/internal/server/scheduling.go:53` `		s.dnsExemption = exemption`。三条路径最终都走到这个不做删除的 `Close()`。

**为什么没兜住**　`:377–379` 的注释写着 "Keep the handle and filter IDs so a later Close can retry; clearing them here would strand both for the remaining lifetime of the process."——它**假设 `Close()` 会执行删除**，为"重试删除"保留了状态，却没有实现被重试的那个动作。同样，`:196-200` 的 deferred `owned.Close()` 丢弃了 Close 的错误，而这个 Close 正是"故意失败后保留句柄等稍后重试"的那个——**later Close 永不存在**。

**建议改法**

1. 在 `newAPI()`（`:387–400`）加载 `FwpmSubLayerDeleteByKey0` 与 `FwpmFilterDeleteById0`。
2. 重写 `Close()`：逐个 `FwpmFilterDeleteById0`，**失败项不清除 ID**（保留重试），成功项移除；再 `FwpmSubLayerDeleteByKey0`；仅当二者都成功或子层已不存在时才 `engineClose` 并置 `filterIDs = nil`。
3. 删除动作包在独立事务中（begin → 删除 → commit，失败 abort），与创建路径对称。
4. 子层创建（`:215`）应移入事务内；若确需事务外创建（部分 WFP 场景要求先有子层才能加过滤器），必须在 `:215` 之前把子层 key 持久化到运行时状态文件，供下次启动执行孤儿清理。
5. `:196-200` 的 deferred Close 应改为 `errors.Join` 记录 Close 失败，使"清理未完成"可观测。
6. 增加启动时的孤儿子层扫描。

---

### A2　WFP DNS 豁免替换无回滚：内核规则与代理 adapter 池永久错位

**代码**　`engine/internal/server/scheduling.go:44-58`——顺序为：先装新句柄（`:53` `		s.dnsExemption = exemption`），`:50` 拆掉旧句柄，`:57` 才做**可能失败的** `UpdateScheduling(next)`，且失败时**无任何补偿动作**。

**触发条件**　适配器集合变化触发重新调度，且 `UpdateScheduling(next)` 失败（管道断开、引擎正在退出）。

**后果**　内核里的 WFP 豁免规则反映的是**新**的 adapter 池，而代理实际使用的却是**旧**池——两者永久错位。这个不一致只能通过重启进程修复，用户在此期间的 DNS 行为不可预期。

**为什么没兜住**　`scheduling.go:44` 的条件判断（`s.dnsExemption != nil && len(bindings) != len(s.adapters)`）只覆盖"是否需要重建"，没有覆盖"重建失败怎么办"。三步操作没有事务边界，也没有 `defer` 形式的补偿。

**建议改法**　把三步包成可回滚序列：保留旧句柄引用 → `UpdateScheduling(next)` **先成功** → 再原子替换 `s.dnsExemption` 并释放旧句柄。若必须先释放旧句柄，则 `UpdateScheduling` 失败时应立即用旧 bindings 重新调用一次以恢复一致状态，并把失败写入 `errors.Join` 汇总上报。

---

### A3　`containProcess` 失败跳过网络清理

**代码**　`engine/internal/tun/supervisor.go:241-247`——失败分支只做 Kill + Wait + `failStart`，**不调用 `s.cleanup`**。

**触发条件**　`Activate` 过程中 `containProcess` 失败（sing-box 进程启动后无法被收编进 Job Object）。

**后果**　进程虽已终止，但它**已经建好的 Wintun 适配器、默认路由、WFP 规则全部残留**。这是回滚链上唯一的缺口——同文件的 `failStartedRun:313-341` 路径处理是正确的。

**为什么没兜住**　两条失败路径的清理逻辑没有统一，`containProcess` 这条遗漏了 `s.cleanup`。

**建议改法**　让两条路径共用同一个失败收尾函数，确保 `failStart` 之前一定执行过 `s.cleanup`；用 defer 驱动的"已创建资源栈"替代手写分支，避免遗漏。

---

### A4　硬枚举错误被降级成「无残留设备」，整段清理被跳过

**代码**　`engine/internal/tun/cleanup_windows.go:118-120` 与 `:121-127` 的 `	if enumErr != nil { continue }`

**触发条件**　WFP 设备枚举返回硬错误（权限、句柄耗尽、驱动未响应）。

**后果**　枚举错误被当成"枚举成功且无残留设备"，导致 `cleanupPlatform:68` 整段 PowerShell 路由 / PnP 清理**被跳过**。这与该函数 `:65-67` 注释声明的 fail-safe 意图**正好相反**——注释说要 fail-safe，代码却 fail-open。

**为什么没兜住**　`continue` 把"查不出来"与"确实没有"折叠成同一结论。

**建议改法**　区分两种返回：枚举失败时**必须**继续执行后续清理（因为无法证明没有残留），并把 `enumErr` 收集进 `errors.Join` 上报。只有枚举成功且结果为空时，才能认为"无残留"。

---

### A5　vNIC 回滚绕过「确认真的停了」校验

**代码**

- `engine/internal/vnic/manager.go:172` — `		_, _ = m.controller.Stop(stopCtx)`（位于 `Create` 的 Activate 失败回滚分支 `:170–178`）
- `engine/internal/vnic/manager.go:174` — `		m.deployed = false`
- **同文件内的正确对照**：`engine/internal/vnic/manager.go:226` — `	if waitErr := m.waitForKeeperStop(stopCtx); waitErr != nil {`（在 `stopLocked`，`:216–240`）；`waitForKeeperStop` 定义于 `:246–268`

**触发条件**　`Create()` 成功部署虚拟网卡并 Activate，后续步骤失败进入回滚，此时 `m.controller.Stop(stopCtx)` 因 keeper 进程未响应而超时或返回错误——错误被 `_, _ =` 丢弃。

**后果**　紧接着 `:174` **无条件**执行 `m.deployed = false`，内存状态断言"当前没有网卡"，但系统中可能仍持有一个持有 Wintun 句柄的孤儿 sidecar 进程。任何依赖 `deployed` 的后续逻辑（跳过创建、认为环境干净）都基于错误前提。

**为什么没兜住**　**同文件里已存在正确实现却未被回滚路径使用**——`stopLocked` 已经会调用 `waitForKeeperStop` 等待并确认 keeper 真的退出，失败时返回错误而非掩盖。这是最不该出现的一类遗漏。

**建议改法**　① 删除 `:172` 的 `_, _ =`，改为直接复用 `stopLocked` 的停止+等待逻辑；② `m.deployed = false`（`:174`）改为**条件置位**：只有确认 keeper 已停止后才置 false，确认失败则保留 `deployed = true` 并记录"待清理"标记供下次启动重试；③ 把 sidecar PID、网卡名写入启动孤儿扫描清单。

---

### A6　`installWindowsService` 改了系统状态后没有 undo

**代码**　`engine/cmd/hypomux-engine/service_windows.go:63` — `		service, err = manager.CreateService(coreServiceName, executable, mgr.Config{`；以及 `:98-110` 的 `writeCoreServicePolicy` / `service.SetRecoveryActions(...)` / `service.Start()` 三段各自 `return fmt.Errorf(...)`。

**触发条件**　（新装）`CreateService` 成功后 `:98`/`:101`/`:108` 任一失败（如 `SetRecoveryActions` 被组策略限制、`Start()` 返回 `ERROR_DEMAND_START_PAUSED`）；（升级）`:77` 已停掉旧服务，随后 `:91` `UpdateConfig` 成功但 `:101` 或 `:108` 失败。

**后果**　新装路径留下一个 `StartType: Automatic` 的已注册服务，每次开机被 SCM 拉起、读不到策略立刻退出，事件日志反复报错，而安装器已返回退出码 1、桌面侧不会再清理。用户重试安装时走 `:61` 的 else 分支，变成升级路径。升级路径更糟：**旧服务停在 Stopped 且再也不被启动**，机器上是新二进制、新注册表路径，但没有任何进程提供服务，用户网络完全中断。

**为什么没兜住**　直线流程，只有 `defer manager.Disconnect()`（`:59`）和 `defer service.Close()`（`:96`）两个纯资源清理，**没有一个 defer 描述"如果走到这里还没成功，就把系统改回去"**。`stopWindowsService` 的结果 `stopErr` 在 `:77-80` 只用于提前返回，同样无 restart 兜底。

**建议改法**

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

`created` 在 `:63` 成功后置 true；`restartPrevious` 在走 else 分支且 `:77` 停服成功后置 true；所有 return 点改为 `errors.Join` 汇总后再返回；成功路径最后置 `committed = true`。另外 `main.go:71-74` 只打印 `%v`，建议把回滚结果一并输出。

---

### A7　`removeWindowsService` 的系统状态回滚不对称

**代码**　`engine/cmd/hypomux-engine/service_windows.go:130-136`

```go
	if err := stopWindowsService(service, 20*time.Second); err != nil {
		return err
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete service: %w", err)
	}
	return deleteCoreServicePolicy()
```

**触发条件**　(a) 停服 20s 超时（`:164`）；(b) `service.Delete()` 失败；(c) `deleteCoreServicePolicy()` 失败（`registry.DeleteKey` 因键下还有子键或被占用而失败）。

**后果**　(a) 服务仍以 SYSTEM 驻留，报失败但什么都没变；(b) 服务已停止但仍注册，注册表里留着指向已删除二进制的完整策略；(c) 服务已删除但注册表键残留，`HKLM\SOFTWARE\HypoMux\CoreServicePolicy`（含 desktop/tun 绝对路径和 SHA-256）成为孤儿。另外安装路径 `:51` 调用的 `tun.PrepareTrustedConfigStorage()`（`engine/internal/tun/config_stage_windows.go:85-94`）会创建 `%ProgramData%\HypoMuxCoreRuntime`，卸载**完全没有对应删除**（NSIS 脚本 `desktop/build/windows/nsis/project.nsi:480-481` 删的是 `$APPDATA\HypoMuxCoreRuntime`，路径不同）。

**建议改法**　三步包成 `errors.Join`，每步都尽力执行并明确报告哪一步残留，并新增 `removeTrustedConfigStorage()` 删除 `%ProgramData%\HypoMuxCoreRuntime`。

---

### A8　提权核心启动失败后 `Kill()` 错误被丢弃 → 孤儿管理员进程

**代码**　`desktop/internal/engineclient/privileged_windows.go:119-120`

```go
		_ = process.Kill()
		_ = process.waitTimeout(coreTerminateWait)
```

**触发条件**　`privilegedLauncher.Launch` 已通过 `ShellExecuteExW` + `runas`（`:312-355`）拉起**管理员权限**引擎进程，随后管道等待/身份校验/一次性令牌认证失败。

**后果**　`windowsCoreProcess.Kill()`（`privileged_windows.go:390-404`）对管理员进程**必然返回 `ERROR_ACCESS_DENIED`**——注释 `:397-400` 自己就写明了这点。因此 `:119` 在提权路径上是一个**必然失败**的调用，而错误被 `_ =` 丢掉。结果：用户取消 UAC 后又点几次"启动"，会留下一个**管理员权限的孤儿引擎进程**持有 TUN 虚拟网卡和 WFP 状态，桌面端却认为"什么都没发生"。多个孤儿叠加后互相争抢网卡/代理。

**为什么没兜住**　清理失败是**代码注释里已写明的已知失败模式**，却仍用 `_ =` 丢弃，也没追加进 `LaunchReport`（`client.go:67-82`）。

**建议改法**

```go
killErr := process.Kill()
waitErr := process.waitTimeout(coreTerminateWait)
// Wait releases the process handle even on timeout, so report the
// orphan explicitly instead of pretending cleanup succeeded.
if killErr != nil || waitErr != nil {
    return nil, errors.Join(
        fmt.Errorf("验证高权限核心通信失败：%w", err),
        fmt.Errorf("清理失败的管理员核心进程（PID %d）：%w；%w", process.PID(), killErr, waitErr),
    )
}
```

同时在 `client.go:226-229` 的 `recordLaunchAttempt` 补一条 `cleanup_failed` 记录。

---

### A9 / A10　引擎进程残留的两条路径

**A9**　`desktop/internal/engineclient/client.go:284-285` 与 `:290-291`——`_ = session.process.Kill()` 丢弃错误。两个并发窗口（`Close()` 与启动竞态 `:280-287`；另一个 `Ensure` 抢先注册会话 `:288-293`）命中时，对 `stdioLauncher`（`launcher.go:164-169`）而言 `Process` 为 nil 则 `Kill()` **静默返回 nil 而什么都没做**，权限不足则返回真实错误——两种都被丢弃，调用方一律看到 `"聚合核心已由另一启动请求连接"`。残留进程继续持有系统代理和 TUN 设置。

> 对照：`killCurrent`（`client.go:467-481`）处理得相对好，至少把 `reason` 传给 `failReplies`。`negotiateSession` 这两条路径自己做了清理、**没走 `killCurrent`**，因而既无统一入口也无记录。建议改为复用 `killCurrent`，或至少把 `Kill()` 错误并入 `errors.Join`。

**A10**　`desktop/main.go:239-241` 的 `log.Fatal` 在 `app.Run()` 返回错误时调用 `os.Exit(1)`，**绕过 Wails 的退出回调**（即 `main.go:153-163` 那一组 `diagnosticsService.Shutdown()` / `hypervAdapterService.Shutdown()` / `engineService.Shutdown()`）。结果：引擎进程残留、虚拟网卡留在系统里、系统代理仍指向即将消失的端口。

> **建议**　把 `log.Fatal` 换成：先显式调用同一个清理序列（提取为 `shutdownServices()` 供两处复用），再 `os.Exit(1)`；或改用带返回值退出的结构让 `OnShutdown` 得以执行。

---

### B1　`EngineService.Shutdown()` 丢弃 `Stop()` 收集的全部停止错误

**代码**

- `desktop/internal/services/engine.go:1203` — `	_, _ = s.Stop()`
- 错误实际产生处：`:1150` — `				firstError = fmt.Errorf("停止 TUN 失败：%w", err)`；`:1158` — `					firstError = fmt.Errorf("停止聚合核心失败：%w", err)`；`:1163` — `	if err := restoreSystemProxy(); err != nil && firstError == nil {`；`:1164` — `		firstError = err`
- 最终返回：`:1196` — `	return snapshot, errors.Join(firstError, hotspotErr)`
- 紧随其后拆除 IPC：`:1206` — `	s.client.Shutdown(ctx)`；`:1207` — `	s.client.Close()`
- 调用方：desktop/main.go:160 — `		engineService.Shutdown()`

**触发条件**　用户退出桌面端，且此时满足：TUN 拆除失败、引擎 `engine.stop` 失败、**或系统代理恢复失败**。三者都在 `Stop()` 内被逐个收集，并在 `:1193` 通过 `s.logs.RecordEvent("engine", reason, fields)` 写入支持日志。

**后果**　`Shutdown()` 用 `_, _ =` 把它扔掉，紧接着拆除 IPC 通道（已核对签名：`desktop/internal/engineclient/client.go:457` `func (c *Client) Shutdown(ctx context.Context)` 与 `client.go:146` `func (c *Client) Close()` 均无返回值——这两处本身不是丢弃点，但它们的执行让**重试所需的通道彻底消失**）。

最坏情况：**用户退出后，Windows 系统代理仍指向已死亡进程的 `127.0.0.1:<port>`**，用户随后完全无法上网，而应用已消失、UI 无提示。

**为什么没兜住**　整条链路上**没有任何上层消费者接收 `Stop()` 的返回值**——`desktop/main.go:160` 同样是无返回值直接调用。没有人问过"清理成功了吗"。

**建议改法**　① 改签名为 `func (s *EngineService) Shutdown() error`；② `main.go:160` 接收返回值并落盘（此时 UI 不可用，落盘是唯一出路）；③ **关键顺序调整**：若 `Stop()` 返回错误，**不要**立即执行 `:1206`/`:1207`，先做有限次数重试（3 次指数退避）并落盘后再拆 IPC；④ 对 `restoreSystemProxy()` 失败这一最高危分支，在失败路径上启动独立的、带超时的补救协程专门重试代理恢复，结果写入"待处理遗留问题"标记文件供下次启动读取。

---

### B2　引擎进程退出时的清理全链路吞错

**代码**

- `engine/internal/server/server.go:97` — `	defer s.stopProxyForHostExit()`
- `engine/internal/server/server.go:1012` — 函数定义处
- `server.go:1016` — `	_, _ = s.tun.Stop(tunCtx)`
- `server.go:1019` — `	_ = s.closeDNSExemption()`
- `server.go:1025` — `		_, _ = s.vnic.Remove(vnicCtx)`
- `server.go:1030` — `		_ = s.proxy.Stop(ctx)`
- `server.go:1038` — `		_, _ = s.runtime.Transition(engineRuntime.StateStopping, "host exiting")`

**后果**　TUN 未拆、DNS 豁免未关、虚拟网卡未移除、SOCKS 未停。与 B1 不同，这里 `defer` 位于 `func (s *Server) Run` 内，**函数返回后进程立即消失**，没有任何重试机会。这些错误**没有任何一条被写入日志或状态文件**——不是记录得不够详细，而是完全静默，连诊断包都不会带上。

**建议改法**　① 用 `errors.Join` 汇总全部子步骤错误（同文件 `server.go:705–730` 已有正确范式：`errors.Join(err, tunStopErr, wfpErr, proxyStopErr)`，此处只是没沿用）；② 返回前写入退出诊断文件 `last-exit.json`；③ 桌面端下次启动主动读取并提示"上次退出时以下系统状态未能恢复"，提供一键修复；④ 该遗留清单直接作为启动时孤儿清理（A1/A5）的输入。

---

### B3　Service 停机超时直接退出进程，跳过唯一的网络状态回滚

**代码**　`engine/cmd/hypomux-engine/service_windows.go:265` — `		return false, 0`（其上 `:262-264` 为超时分支注释，`:30` `coreServiceShutdownTimeout` = 15s）

**触发条件**　`svc.Stop`/`svc.Shutdown` 到达后 15s 内 `serve` 未返回（管道操作卡死、WFP 关闭阻塞、sing-box 子进程拒绝退出）。

**后果**　`Execute` 返回 → `svc.Run` 返回 → 进程退出。此时 `stopProxyForHostExit`（B2）**完全没跑**：DNS 豁免残留、WFP 引擎句柄残留、虚拟网卡留在系统里、sing-box keeper 成为孤儿。而 `return false, 0` 告诉 SCM"干净停止"，事件日志无失败记录，只有一行 stderr——Service 模式下 stderr 通常不落盘。

**建议改法**　超时分支改为"尽力恢复后再退出"，并用非零退出码让 SCM 记录异常：

```go
case <-timer.C:
    recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
    if err := tun.Recover(recoverCtx); err != nil {
        fmt.Fprintf(service.stderr, "post-timeout network recovery failed: %v\n", err)
    }
    recoverCancel()
    return false, 1
```

（`tun` 包在 `service_windows.go:17` 已导入。）

---

### C1　`libcronet.dll` 的 stat 错误被当成「文件不存在」→ 跳过 ACL 校验

**代码**　`engine/cmd/hypomux-engine/service_policy_windows.go:333` — `	if _, statErr := os.Stat(filepath.Join(filepath.Dir(engine), "libcronet.dll")); statErr == nil {`

**触发条件**　AV/EDR 拦截、`ERROR_SHARING_VIOLATION`（文件正被占用）、权限不足、路径过长——`os.Stat` **任何非 `ErrNotExist` 的失败**都会走到这里，`statErr` 被整个丢弃。

**后果**　`libcronet.dll` 不进入 `paths`，因此**完全跳过 `requireProtectedCorePath`**（`:395-408` 的 reparse 检查 + `requireProtectedCoreACL` 的 owner/DACL 校验）。该 DLL 与 `sing-box.exe`（`:331` 有校验）位于**同一目录、由同一个 LocalSystem 服务进程加载**。结果是：本地攻击者只要能让该 DLL 对普通用户可写，就获得了以 SYSTEM 加载任意代码的能力——而安装器在一条 `stat` 错误之后就宣称校验通过。

**建议改法**

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

### C2　策略键写一半失败会摧毁原本可用的策略

**代码**　`engine/cmd/hypomux-engine/service_policy_windows.go:100` — `	if err := key.SetDWordValue(policyValueSchemaVersion, 0); err != nil {`；以及 `:112-119` 的 `for _, value := range values { if err := key.SetStringValue(value.name, value.value); err != nil { return fmt.Errorf("write Core Service policy value %s: %w", value.name, err) } }`

**触发条件**　升级安装时 4 个 `SetStringValue` 中任一失败（磁盘满、AV 拦截注册表写入、进程被杀），或最后的 commit（`:117`）失败。而 `SchemaVersion=0` 已经在 `:100` 写入。

**后果**　注册表里原本完好的策略被降级成 `SchemaVersion=0` + 部分字段。`loadCoreServicePolicy`（`:137-139`）返回 `unsupported Core Service policy version 0`，服务第一次循环就退出（`service_windows.go:276-279`）。叠加 A6（旧服务此时已被停掉）：**机器上留下一个开机自启、却永远起不来的服务 + 一份损坏的策略键**。

**建议改法**　写之前快照旧值，失败时还原，commit（`:117`）失败时同样走还原：`restoreCoreServicePolicy(key, previous)`，并用 `errors.Join` 同时报告写入错误与还原错误。让"先置无效"变成真正的写事务边界。

---

### C3　NAT 类型检测的安全前置检查被静默绕过（失败开放）

**代码**　`desktop/main.go:166-170`

```go
		func() error {
			snapshot, err := engineService.Snapshot()
			if err != nil {
				return nil
			}
```

**触发条件**　`engineService.Snapshot()` 会真实失败。读 `desktop/internal/services/engine.go:393-410` 确认：它在没有过渡态时调用 `s.client.Ensure(ctx)`（8s 超时，`:401-406`）并 `Request(ctx, "engine.status", ...)`（`:408-410`）。提权弹窗被取消、核心进程崩溃、管道断开、握手超时（`client.go:196-201` 的 startGate 排队超时）都会返回非 nil error——而这恰恰是**引擎最可能仍在代理流量**的时刻。

**后果**　前置检查用于拦截"聚合运行时进行 NAT 检测"（`main.go:173`）。`Snapshot()` 一失败就 `return nil` = 放行。用户会在引擎实际仍在代理的状态下启动 NAT 检测，检测流量被代理转发，得到的 NAT 类型是错的；更糟的是检测过程会临时改防火墙/端口状态，与引擎持有的 WFP 状态互相干扰。

**为什么没兜住**　这是**失败开放（fail-open）的安全守卫**：`err != nil` 与"引擎已停止"被合并成同一个结论。它不同于普通的"吞错后继续"——这里直接把错误解释成了**相反的语义**。

**建议改法**　失败必须拒绝（fail-closed），并区分两种失败：

```go
snapshot, err := engineService.Snapshot()
if err != nil {
    return fmt.Errorf("无法确认聚合引擎状态，已取消 NAT 类型检测：%w", err)
}
switch snapshot.Phase {
case "starting", "running", "degraded", "stopping":
    return fmt.Errorf("请先停止聚合再进行 NAT 类型检测；聚合运行时无法保证检测流量直连所选物理网卡")
default:
    return nil
}
```

若产品上确实要"引擎未启动时也能检测"，应改用**只读、永不启动核心**的状态查询，而非吞掉错误。

---

### C4　事件通道打满时静默丢弃事件（违反契约声明）

**代码**　`desktop/internal/engineclient/client.go:492-498`

```go
		select {
		case c.events <- Event{
			Name: message.Event, Sequence: message.Sequence,
			Data: append(json.RawMessage(nil), message.Data...),
		}:
		default:
		}
```

**触发条件**　`events` 通道在 `client.go:127` 以 `make(chan Event, 64)` 定容。引擎在启动/切模式/连接风暴时连续推送大量 `log.record`（每条连接一条，见 `desktop/internal/services/engine.go:290-294`），消费者 `EngineService.consumeCoreEvents`（`engine.go:268-297`）是单 goroutine 串行处理。

**后果**　被丢弃的包括 `tun.state_changed`（`engine.go:284-289`：`failed` + WFP 兼容错误时触发 `handleWFPCompatibility` 自动重启修复）和 `dns.fallback_required`（`engine.go:279-283`：触发引擎重启）。结果：**虚拟网卡已进入 `failed` 状态但桌面端永远知道不了**。同时序列号 `Sequence` 出现空洞，事件流不再可靠。

**契约冲突（重要）**　`protocol/v1/manifest.json:137-163` 把 `tun.state_changed` / `engine.state_changed` / `host.exiting` 明确声明为 `lossless_ordered, coalescible: false`——**协议要求这些事件不可丢**，而实现用非阻塞 `select` 丢弃。这是一个**实现直接违反冻结契约**的问题，而不只是工程质量问题。

**建议改法**　① 扩大缓冲（`make(chan Event, 256)`）并把 `default:` 改为 `c.recordDroppedEvent(...)`；② 把三个 lossless 事件走**永不阻塞的独立高优先通道**（容量 4），保证控制面事件不被日志洪峰挤掉：

```go
if isCriticalEvent(message.Event) {
    select { case c.criticalEvents <- ev: default: c.recordDroppedEvent(...) }
    continue
}
```

---

### C5　DNS 超时被当成「无事发生」→ fallback 事件永不响

**代码**　`engine/internal/dns/resolver.go:282` 与 `:425` 的 `	if ctx.Err() != nil` 早返回分支。

**触发条件**　查询超时——而超时恰是 `resolver.go:269-270` 注释里自己写明的主要 DoH 失败模式。

**后果**　早返回把**刚产生的失败整个扔掉**：`dohFailures` / `legacyFailures` 不增长、`recordStrictFailure` 不触发。于是 `EventDNSFallbackRequired` 在**最常见的故障模式上永不响起**，而 `Status()` 还会报 `legacy_failures: 0` 的假数据——用户看到的遥测是"一切正常"，实际 DNS 已经降级。

**为什么没兜住**　超时与"查询成功但结果为空"共用了一条早返回路径，而前者是必须计入失败统计的。

**建议改法**　在早返回前先调用与普通失败路径相同的失败处理（递增计数 + `recordStrictFailure`），再用原始 ctx 错误构造带 `%w` 的错误返回。

---

### C6　RCODE 被压平成无类型字符串 → 语义正确的 NXDOMAIN 两次就能隔离域名

**代码**　`engine/internal/dns/wire.go:112` — `fmt.Errorf("DNS response code %d", code)`

**触发条件**　上游返回 NXDOMAIN 或 SERVFAIL。

**后果**　无类型的错误字符串让调用方**无法区分"域名确实不存在"与"我们自己的故障"**。于是 `resolver.go:412/420` 会继续 TCP 重试并遍历所有服务器，**烧光 `QueryTimeout` 的 fallback 预算**；`engine/internal/proxy/dial.go:86` 把它记成 adapter 比较失败；而 `engine/internal/proxy/health.go:15` 的阈值仅 2 次、`domainQuarantineTTL` 30 分钟——结果是**一个语义完全正确的 NXDOMAIN 两次就能把该域名隔离 30 分钟**。这是典型的"错误分类丢失导致误伤"。

**建议改法**　定义带类型的错误：`type dnsResponseError struct{ Code int }`，实现 `Error() string`；调用方用 `errors.As` 判断 NXDOMAIN 时直接返回不重试、不计入失败，只对 SERVFAIL/REFUSED 等真正的传输故障计入健康度。

---

### C7　默认配置下 UDP ASSOCIATE 必然失败，原因还被伪装

**代码**　`engine/internal/proxy/udp.go:88` — `		scheduler: s.schedulers[session.channel],`；`engine/internal/proxy/udp.go:99` — 报 `unknown UDP channel ""`

**触发条件**　`schedulers` 只由 `Channels` 填充（`server.go:94-108`），而普通 SOCKS 端口的 channel 为 `""`（`server.go:190`）。因此取到 **nil**。

**后果**　默认配置下 UDP ASSOCIATE **必然失败**，且原因被伪装成"未知 UDP 通道"——用户和运维都会朝错误方向排查。TCP 路径在 `server.go:469` 有 fallback 处理，**UDP 路径漏了**。

**建议改法**　UDP 路径补上与 `server.go:469` 相同的 fallback（回落到默认 scheduler），并把 `unknown UDP channel` 的错误改为包含 channel 名与可用列表的诊断信息。

---

### C8　DoH 竞速批次在 `ctx.Done()` 分支丢弃已经成功的结果

**代码**　`engine/internal/dns/resolver.go` 的 DoH 竞速批次（`[dns-server] P0-4`，见 `reports/parts/engine.md:3004`）

**触发条件**　多个 DoH 服务器并发竞速查询，其中一个**已经成功返回**，但此时 ctx 恰好被取消（或竞速窗口到期）。

**后果**　`ctx.Done()` 分支直接丢弃已经成功的结果，**把成功变成失败**。结果是：一次本应成功的 DNS 查询被上报为失败，计入 C5 所述的失败计数，挤占 fallback 预算，并在极端情况下触发本不该触发的 `EventDNSFallbackRequired`。

**为什么没兜住**　竞速循环的"谁先返回就用谁"逻辑把"已成功的结果"和"ctx 取消"当成同一分支处理，没有区分"没有任何结果"与"已有可用结果但被取消"。

**建议改法**　在进入 `ctx.Done()` 分支前先检查是否已有成功结果（`best != nil` / 首个成功响应），有则直接返回该结果，把 ctx 取消仅作为"一个都没成功"时的失败原因。

---

### C9　`acceptLoop` 遇持续性 Accept 错误热自旋，打满一个 CPU 核

**代码**　`engine/internal/proxy/server.go:418` — `				continue`（上下文 `acceptLoop`，`:406–436`）

**触发条件**　Accept 返回非 `net.ErrClosed` 的错误——最典型的是文件描述符耗尽（`EMFILE`/`ENFILE`）。

**后果**　代码只区分了"预期内的关闭错误"与"其他错误"，对其他错误一律 `continue`，**无退避、无日志、无计数**。持续性错误下形成紧循环**忙等**，CPU 占用飙升而代理功能实际已不可用；因为没有日志，运维完全看不出原因。这是"资源耗尽型错误 + 无退避"的经典组合，比单纯丢日志严重得多——它把一个可恢复的瞬时错误放大成持续性 CPU 消耗。

**为什么没兜住**　缺少对错误**分类**的处理：可恢复（EMFILE 暂态）与不可恢复（监听器本身损坏）走同一条 `continue`。

**建议改法**　① 非 `ErrClosed` 错误采用指数退避（10ms 起，上限 1s）；② 连续失败超阈值（如 10 次）时记录 `Error` 级日志并考虑 fail-fast 关闭监听器，让上层感知而非静默烧 CPU；③ 区分 `EMFILE`/`ENFILE`（暂态，退避重试）与其它（可能致命，关闭监听器）。

---

### D1　引擎发射 4 个契约外错误码，测试结构上无法发现

**代码**　`engine/internal/server/server.go:255`（`engine_running`）、`server.go:265`（`mtu_failed`）、`server.go:274`（`hotspot_inspection_failed`）、`engine/internal/server/scheduling.go:57`（`update_failed`）

**后果**　这 4 个码**都不在** `protocol/v1/manifest.json:164-179` 冻结的 14 个码内。下游按契约做穷举匹配时会落入 default 分支，无法为这些真实且重要的失败提供针对性提示。

**为什么没兜住**　`protocol/v1/contract_test.go:104-106` 只断言清单非空、`:157` 只断言 Code 非空——**结构上无法发现**"引擎发了清单外的码"。这是一处测试设计的缺口，不是单点 bug。

**建议改法**　① 要么把这 4 个码补进 `manifest.json`（需走契约变更流程），要么改用现有码；② 补一条**发射侧**的测试：静态扫描 engine 源码中所有 `protocol.Error{Code: "..."}` 字面量，断言全部 ∈ 清单。后者能防住整类问题。

---

### D2　portable 启动脚本的种子配置可能部分写入

**代码**　`desktop/portable/launch-portable.cmd:225-241` 用 `> file (` 写种子 `settings.json`，写完只检查 `if not exist` 就报"已写入"。

**后果**　部分写入 / 磁盘满时，文件**仍然存在**，脚本 `exit 0` 但配置已损坏，且**旧值被截断无法恢复**。

**建议改法**　改为先写临时文件、成功后再 `move` 覆盖（与 `desktop/internal/services/atomic_file.go` 的原子写入思路一致），并校验写入长度。

---

### E1　全仓零 `recover()` + 至少 23 处非测试 goroutine

**全仓佐证**　检索 `recover\(\)`，范围全部 `*.go`（含测试文件）→ **No matches found**。

**非测试 `go func()` 分布（按文件计数）**：server.go 6、udp.go 3、tun_connectivity.go 2、service_windows.go 2、steam_observed_probe.go 2、steam_cdn.go 2、client.go 2、privileged_windows.go 1、launcher.go 1、main.go 1、hotspot.go 1、hyperv_adapter.go 1、latency.go 1、resolver.go 1。

| 层 | 位置 |
|---|---|
| engine | `engine/internal/dns/resolver.go:366`；`engine/internal/proxy/latency.go:296`；`engine/cmd/hypomux-engine/service_windows.go:214`、`:293`（E2）；`engine/internal/server/server.go:108`；`engine/internal/proxy/server.go:250`、`:434`（E3）、`:541`、`:555`；`engine/internal/proxy/steam_cdn.go:487`、`:600`；`engine/internal/proxy/steam_observed_probe.go:29`、`:126`；`engine/internal/proxy/udp.go:109`、`:116`、`:229` |
| desktop | `desktop/internal/services/hyperv_adapter.go:1678`；`desktop/internal/services/hotspot.go:172`；`desktop/internal/services/tun_connectivity.go:181`；`desktop/internal/engineclient/service_windows.go`（2 处）；`privileged_windows.go`（1）；`launcher.go`（1）；`desktop/main.go`（1）。另见 desktop-shell D-24：`client.go:301` / `launcher.go:147` / `privileged_windows.go:256` / `main.go:198` |

**后果**　桌面端整个应用瞬间消失、用户工作丢失、没有任何错误提示；引擎端守护进程退出、用户网络中断。**现场不会留下任何 panic 上下文**。

**建议**　① 引入统一 panic 守卫 `guardPanic(component string, fn func())`，内部 `defer func(){ if r := recover(); r != nil { logPanic(component, r, debug.Stack()) } }()`；② 至少记录 component、goroutine 用途、recover 值、完整 `debug.Stack()`，写入支持日志并通过现有事件通道上报；③ 对后台循环 recover 后应**重建循环**而非直接返回，否则变成"悄悄停止工作"的静默失效；④ CI 中加静态检查，禁用无 recover 的裸 `go func()`。

---

### E2 / E3 / E4　崩溃面的三个具体落点

**E2**　`engine/cmd/hypomux-engine/service_windows.go:214` 与 `:293-295` 的 `go func() { done <- serve(ctx, service.metadata) }()` / `go func() { sessionDone <- engineServer.Run(ctx) }()` 承载整个引擎栈。任何 panic 直接杀进程，`stopProxyForHostExit` 不会运行（后果同 B2/B3），依赖 `SetRecoveryActions`（`:101-107`）3 秒后重启，变成"崩溃 → 带着脏网络状态重启"的循环。不可信输入驱动的解引用点：`service_session_windows.go:55` 的 `*(*uint32)(unsafe.Pointer(buffer))`、`service_policy_windows.go:460` 的 `(*windows.SID)(unsafe.Pointer(&ace.SidStart))`。建议把 panic 转成 error 送入现有错误通道（加一个 `PanicError` 类型即可复用 `errors.Is(err, context.Canceled)` 分支）。

**E3**　`engine/internal/proxy/server.go:434` — `	go s.handleClient(protocol, client, session)`：**所有每连接 goroutine 全无 recover**，单个客户端的畸形报文即可打崩整个引擎守护进程。

**E4**　`engine/internal/proxy/steam_observer.go:238`、`:291`、`:303` 在持有 `observer.mu` 时另取 `cdn.mu`——这不仅是"panic 无 recover"（E1），还是**锁顺序问题**：panic 时 defer 释放 `observer.mu` 之前若已进入 `cdn.mu`，其它 goroutine 就可能与之互锁，回收路径被自己锁死。建议统一锁顺序并做静态锁序检查。

---

## P1 详述（61 条）

### 一、DNS / server / runtime / expiry / fileintegrity / diagnostic / api（11 条）

| # | 位置 | 一句话 |
|---|---|---|
| 1 | `engine/internal/tun/supervisor.go` P1-2 | `cleanupRun` 被 ctx 超时跳过时，`Stop` 返回的清理失败信息彻底丢失 |
| 2 | `engine/internal/tun/supervisor.go` P1-3 | PowerShell 清理 / MTU / 共享检测把 ctx 超时报告成"脚本失败"，丢失全部因果 |
| 3 | `engine/internal/tun/supervisor.go` P1-8 | TUN 就绪探测把 Win32 错误吞成"还没就绪"，20 秒后报一个不相关的原因 |
| 4 | `engine/internal/tun/supervisor.go` P1-9 | 暂存配置的删除错误被吞，凭据文件可能留在 ProgramData |
| 5 | `engine/internal/tun/supervisor.go` P1-10 | `Stop` 无条件把状态重置为 `StatusStopped`，抹掉全部失败细节 |
| 6 | `engine/internal/vnic/manager.go` P1-5 | `vnic.Manager` 在最长 60 秒的阻塞调用期间持有 `m.mu` |
| 7 | `engine/internal/wfp/dns_exemption_windows.go` P1-6 | `FwpmGetAppIdFromFileName0` 返回 NULL 时未校验，把空指针交给内核 |
| 8 | `engine/internal/wfp/dns_exemption_windows.go` P1-7 | WFP **所有** Win32 调用的错误用 `%s` 包装，`errors.Is` 永久失效 |
| 9 | `engine/internal/proxy/server.go:549` | 上行中继错误被 `_ = upload(...)` 丢弃，CDN 试运行永不会判失败（对照 `:584` 下行是保留的） |
| 10 | `engine/internal/proxy/steam_http_probe.go:112` | `errors.New("http_probe_failed")` 丢掉 `%w`，配合 `:46` 的 `switch err.Error()` 字符串匹配把拒绝/DNS/超时压成同一遥测值 |
| 11 | `engine/internal/proxy/server.go:1008-1009` 等 | TUN 意外退出事件发送失败被丢弃（同 desktop-shell D-01 的引擎侧对偶） |

### 二、proxy 数据面（8 条）

| # | 位置 | 一句话 |
|---|---|---|
| 12 | `engine/internal/proxy/udp.go:211` + `:212` | `createFlow` 失败被完全丢弃：不发 SOCKS 错误回复、不记失败、无日志 |
| 13 | `engine/internal/proxy/udp.go:223` | `_ = existing.send(...)` 与同函数 `:192`（`recordFailure` + `close`）处理方式自相矛盾 |
| 14 | `engine/internal/proxy/udp.go:429,432` | `WriteToUDP` 失败与短写均不调 `recordFailure` → 坏适配器健康度不被标记，调度器持续选它 |
| 15 | `engine/internal/proxy/server.go:178`、`:208` | 监听器部分失败回滚中忽略 Close 错误 |
| 16 | `engine/internal/proxy/server.go:218` 附近 | 回滚不重置 `s.cdn`（赋值于 `:161`），也不等待 `s.wg` 中 `:162-165` 的两个 goroutine |
| 17 | `engine/internal/proxy/server.go:418` | `acceptLoop` 遇非 `net.ErrClosed` 的 Accept 错误直接 `continue`，无退避、无日志 → 忙等烧 CPU |
| 18 | `engine/internal/proxy/steam_observer.go:238,291,303` | 锁顺序 observer.mu → cdn.mu（与 E4 同源，数据面侧影响） |
| 19 | `engine/internal/proxy/latency.go:296` 等 | 后台探测 goroutine 无 recover |

**P1-16 详述**　综合后果：**下一次 `engine.start` 失败于 `listen SOCKS: ... bind: address already in use`**，而这类错误对用户表现为"引擎启动不了"却查不到根因。**建议**用 defer 驱动的"已创建资源栈"统一回滚，避免手写分支遗漏字段。

### 三、engine cmd / service / policy（11 条）

| # | 位置 | 一句话 |
|---|---|---|
| 20 | `service_windows.go:340` | `fmt.Errorf("%w: %v", ...)` 把真实拒绝原因降级成不可追溯字符串（见下详述） |
| 21 | `service_policy_windows.go:435` | 注册表 ACL **读取失败**被伪装成"没有可信 DACL"，会诱导用户手动放开权限 |
| 22 | `pipe_windows.go:126-129` | 认证握手 JSON 解析失败时原始错误被丢弃，无法区分"格式错"与"对端不是 HypoMux" |
| 23 | `service_policy_windows.go:177` | `deleteCoreServicePolicy` 用 `!=` 比较 sentinel 而非 `errors.Is`，破坏错误链 |
| 24 | `service_session_windows.go:45-47` | WTS 调用失败时用可能为 `Errno(0)` 的 `callErr` 包装，产生 "The operation completed successfully" |
| 25 | `main.go:150-153` | 正常 Ctrl+C 退出被判为失败：退出码 1 + "context canceled" |
| 26 | `main.go:167-173` | `runtimeServerMetadata` 静默降级：`TunExecutable` 为空，无任何日志 |
| 27 | `service_policy_windows.go:64-67` | `buildCoreServicePolicy` 的目录布局检查是**恒真式**，永不生效（死代码） |
| 28 | `service_policy_windows.go:454-456` | `requireProtectedCoreACL` 对 mandatory label 等无害 ACE 直接硬失败，误报"ACL 有问题" |
| 29 | `service_windows.go:154`、`:164`、`:131` | `stopWindowsService` 错误丢失服务名与阶段信息，卸载路径裸传 |
| 30 | `pipe_file_windows.go:14-18` | 管道连接**没有写超时**：对端不读时写入永久阻塞，优雅停止被拖到 15s 超时 |

**第 20 条详述**　`engine/cmd/hypomux-engine/service_windows.go:340` — `		return nil, fmt.Errorf("%w: %v", errServiceClientRejected, err)`。只有 sentinel 进了错误链，`err` 携带的真正原因（尤其 `service_policy_windows.go:229` 的**桌面二进制被篡改**这类安全事件）变成纯文本，事后取证拿不到机器可读原因码。Go 1.20+ 支持多个 `%w`，改 `fmt.Errorf("%w: %w", ...)` 即可。

**第 30 条详述**　`pipeFile` 接口只声明了 `SetReadDeadline`，**写方向从未设过 deadline**（`go-winio@v0.6.2/file.go:269` 的 `SetWriteDeadline` 就在那里没被用）。Service 端 `server.writeMessage`（`server.go:1062-1066`）在 64 KiB 缓冲填满后 `Write` 永久阻塞 → `Execute.stop` 等满 15s → 走 B3 强退路径 → 触发全部网络残留。

### 四、桌面外壳 / engineclient / startup / platform（10 条）

| # | 位置 | 一句话 |
|---|---|---|
| 31 | `engineclient/client.go:486-516` | `readLoop` **从不检查 `scanner.Err()`**，真实传输错误被替换成常量文案"输出已关闭" |
| 32 | `engineclient/launcher.go:132-146` | `command.Start()` 失败时三个管道句柄全部泄漏 |
| 33 | `engineclient/service_windows.go:142-144` | SCM 状态把 `START_PENDING`/`STOP_PENDING` 当成"未运行"，触发多余的 UAC 与竞争核心 |
| 34 | `engineclient/privileged_windows.go:253` | `SetReadDeadline` 错误被丢弃 → 认证读取 goroutine 可能永久滞留 |
| 35 | `engineclient/privileged_windows.go:339-345` | `ShellExecuteExW` 的失败原因取自 `syscall.Errno`，错误文案可能完全错误 |
| 36 | `platform/wails/desktop.go:85-92, 102-104` | 托盘弹窗定位失败被静默丢弃，用户只看到"位置不对" |
| 37 | `startup/wifi_windows.go:142-154` | `wifi connect()` 的策略错误会**覆盖**真实的 `WlanConnect` 失败原因 |
| 38 | `platform/webview2_windows.go:23-34` | WebView2 探测把"读不到注册表"一律当成"未安装"，产生误导性结论 |
| 39 | `releaseversion/metadata.go:65-69` | `SyncMetadata` 顺序写 12 个文件，中途失败无回滚、不原子 |
| 40 | `startup/privilege_windows.go:415-420` | `repairLegacyAutostartTask` 把**任何**查询失败当成"任务不存在"，幽灵自启动永不修复 |

### 五、协议 / 前端 / 跨层（9 条）

| # | 位置 | 一句话 |
|---|---|---|
| 41 | `engineclient/client.go:30-49` | 错误码在**第三跳丢失**：Code 被拼进 `Error()` 字符串后，Wails 只传字符串 |
| 42 | `desktop/frontend/src`（118 文件） | 协议冻结的 14 个错误码在**前端出现次数为 0**，全靠中文/英文子串匹配反推 |
| 43 | `desktop/frontend/src/.../errorCodes.ts:14-33` | 子串匹配是唯一的错误识别机制；唯一命中 `SettingsPage.tsx:438` 还是 `settings_autostart_failed` 的假阳性 |
| 44 | `protocol/v1/contract_test.go:104-106, 157` | 只断言清单非空、Code 非空，**结构上无法发现**契约外错误码 |
| 45-49 | 见 `reports/parts/frontend-protocol.md` | 前端各页面的错误呈现与吞错（VirtualAdaptersPage / ToolsPage / BlockedDomainsPage / RuleSetsPanel 等） |

**第 41-43 条是本次审计中最具系统性的发现**　engine 侧 `protocol.Error{Code, Message, Details}` 完整，`engineclient` 的 `RemoteError` 也保真，但 `client.go:30-49` 把 Code 拼进 `Error()` 字符串，而 **Wails 绑定层只传字符串**——于是结构化错误码在最后一跳退化为文本。**建议**在 `RemoteError` 上显式暴露 `Code()` 方法，让 Wails 生成的结构体携带独立字段；前端改为按码分支处理，子串匹配仅作兜底。

### 六、桌面 services（本次主审交叉核验，2 条 + 后续合并）

| # | 位置 | 一句话 |
|---|---|---|
| 50 | `desktop/internal/services/engine.go:507` | `_ = s.blockedDomains.ReplaceRuntime(runtimeEntries)`：遥测循环中运行态同步失败完全静默 |
| 51 | `desktop/internal/services/rule_sets_service.go:253` | `_ = s.settings.saveRuleSets(updated)`：摄取失败记录本身保存失败 → 用户看到"健康"的陈旧规则集 |
| 52 | `desktop/internal/services/atomic_file.go:13` | `	temporary := path + ".tmp"` 固定临时名，并发写会互相 truncate/rename → **配置文件损坏** |

---

## P2 概览（77 条，分类汇总）

### P2-A　`%v` / `%s` 切断错误链（22 处）

来源为全仓 `fmt.Errorf\(.*%v` 与 `%s` 包装检索，逐行核对：

**引擎侧**　`engine/cmd/hypomux-engine/service_windows.go:340`；`engine/internal/tun/process_windows.go:73` — `		return fmt.Errorf("TUN console is not isolated (members=%d): %v", count, err)`；`engine/internal/wfp/*`（全部 Win32 调用用 `%s`，见 P1 第 8 条）；`engine/internal/proxy/steam_http_probe.go:112`。

**桌面侧 · services**　`mtu_windows.go:70` — `		return false, fmt.Errorf("创建 ICMP 探针失败：%v", err)`；`mtu_windows.go:95` — `			return false, fmt.Errorf("ICMP 调用失败：%v", callErr)`；`system_proxy_windows.go:172` — `			return fmt.Errorf("通知 Windows 刷新代理失败：%v", callErr)`；`system_proxy_windows.go:143` — `		return "", fmt.Errorf("代理恢复点损坏且无法隔离：%w（原始错误：%v）", err, parseErr)`；`tun_preflight_windows.go:145`；`hyperv_adapter_windows.go:105`；`settings.go:371`；`singbox_rules.go:453`、`:483`；`scheduling.go:123`。

**桌面侧 · 启动与客户端**　`startup/privilege_windows.go:294`、`:406`；`engineclient/client.go:251`、`:259`；`engineclient/privileged_windows.go:425`；`engineclient/launcher.go:83`；`platform/wails/appearance_windows.go:35`；`main.go:374`、`:377`。

**最值得优先修复**　`desktop/internal/services/engine.go:899` — `			cause = fmt.Errorf("%w；启动失败后的网络回滚不完整：%v", cause, errors.Join(cleanupFailures...))`。这一处把 **`errors.Join(cleanupFailures...)` 聚合出的全部回滚失败降级成不可 unwrap 的字符串**——而回滚失败恰恰是排查"为什么启动失败"时最有价值的信息（它是失败链条的最后一环、也是修复系统状态的依据）。

**通用建议**　`%v` / `%s` 一律改 `%w`；需同时保留主错误与附加错误时用 `fmt.Errorf("%w（回滚失败：%w）", cause, errors.Join(cleanupFailures...))`（Go 1.20+ 支持多个 `%w`）。

### P2-B　启动期静默与其他吞错

- `desktop/internal/services/blocked_domains.go:44` — `	_ = service.load()`；`desktop/internal/services/nat_servers.go:57` — `	_ = store.load()`。服务以默认空配置静默启动（域名过滤列表为空 = 功能完全不起作用）。**建议**区分"文件不存在"（正常首启）与"解析失败"（必须报错）。
- `desktop/internal/services/system_proxy_other.go:11` — `func restoreSystemProxy() error { return nil }`；`:13` — `func restoreSystemProxyDetailed() (string, error) { return "", nil }`。非 Windows 桩（同文件 `:7-9` 的 `enableSystemProxy` 正确返回错误）。**风险**　签名让上层无法区分"恢复成功"与"平台不支持恢复"，与 B1 依赖的返回值直接相关。**建议**返回 `errors.ErrUnsupported` 或哨兵错误。
- `desktop/internal/services/hotspot.go:119,154,162,163,190,241,254` — 大量 `_ = input.Close()` / `_ = output.Close()`。
- `engine/cmd/hypomux-engine/service_windows.go:59,96,78,83,92`；`service_policy_windows.go:96,132,261,239`；`pipe_file_windows.go:27,32` — `Close`/`Disconnect` 返回错误被丢弃。正常语义无害，但 `windows.CloseHandle` 在双重 close 时返回 `ERROR_INVALID_HANDLE`，恰是排查句柄生命周期 bug 的信号。**建议**对关键句柄用 `errors.Join` 汇总，在确实可忽略处加 `//nolint:errcheck` 以区分"有意忽略"和"忘了看"。
- `engine/cmd/hypomux-engine/service_windows.go:323-325`（`_ = windows.CancelIoEx(...)` 等）、`:378-380`（`_ = windows.GetOverlappedResult(handle, &overlapped, &transferred, true)`）—— 这两处是 B3 的**补救动作本身**，丢掉错误会让"补救为什么没生效"永远查不出来；且 `GetOverlappedResult(..., true)` 是**阻塞**等待，若卡住又回到 15s 超时。
- `startup/privilege_windows.go:301,381,390,397,401,405,409` — 六处 `TerminateProcess` 错误丢弃 → 挂起进程残留。

### P2-C　死代码 / 恒真检查 / 整洁度

- `engine/cmd/hypomux-engine/service_policy_windows.go:64-67` — 恒真式目录检查（见 P1 第 27 条）。
- `engine/cmd/hypomux-engine/service_policy_windows.go:468` — `func pathWithin(root string, candidate string) bool {` 是**死代码**，全仓只有 `service_policy_windows_test.go:95,98` 引用。函数名和注释（`:303-305`）承诺的"sidecars 与主程序同处一个 machine-owned 目录"从未被校验。要么接上，要么删除。
- `engine/cmd/hypomux-engine/main.go:104` — `		defer connection.Close()` 与 `Server.Run` 的 defer（`engine/internal/server/server.go:123-128`）重复关闭；`serve` 模式的 `os.Stdin` 同理。建议二选一并注释约定。
- `engine/cmd/hypomux-engine/main.go:44-46`、`:48-50` — `signal-tun` 参数校验失败一律静默 `return 2`（同文件其它分支会打印用法说明）。**建议**至少打印一行（`main_test.go:52-59` 只断言退出码 2，加输出不破坏测试）。
- `engine/cmd/hypomux-engine/main.go:71-74` — 安装失败只打印 `%v`，未含回滚结果。
- `engine/cmd/hypomux-engine/service_windows.go:193-196` — `runWindowsService` 把启动失败报成普通退出码，SCM 侧看不出原因。
- `engine/cmd/hypomux-engine/service_windows.go:282-285` — `serveCoreServicePipe` 对被拒客户端无退避。管道 ACL 是 `(A;;GRGW;;;IU)`（`:29`），任何交互式用户都能连接，循环每次迭代都做一次注册表读 + 新建管道实例。本地用户用循环 connect/disconnect 就能把 stderr 刷屏，**使 P0/P1 级问题无法被发现**。**建议**加退避 + 速率限制。
- `engine/cmd/hypomux-engine/pipe_windows.go:58-79` — `ctx.Done()` 检查在循环体末尾（`:74-78`），第一次 `CreateFile` 无条件执行；`:77` 的 `case <-time.After(40 * time.Millisecond):` 每轮新建 timer，40ms×20s 最多 500 个。
- `engine/cmd/hypomux-engine/service_policy_windows.go:206-207` — `	if err != nil || len(digest) != sha256.Size {` 把"非十六进制"与"长度不对"合并成一条消息。
- `engine/cmd/hypomux-engine/service_policy_windows.go:279`、`:299` — 缓冲区每次迭代重新分配；错误消息未带 `size`。
- `engine/cmd/hypomux-engine/service_session_windows.go:51` — `defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buffer)))`：`defer` 一个返回三值的 `Proc.Call` 在 `go vet` 下会被标记；且未做 `Find()` 检查。
- `engine/cmd/hypomux-engine/pipe_file_windows.go:31-34` — 类型断言失败消息不含实际类型（补 `%T` 即可）。
- `service_policy_windows.go:349-352,385-388,396-399`、`pipe_windows.go:53-56`、`service_windows.go:407-410` — `UTF16PtrFromString` 错误裸传，用户只看到 `invalid argument`。
- engine tun/wfp/vnic：`wfp.call` 之外的 `UTF16PtrFromString` 错误丢弃；`InspectWFP` 探测路径丢弃句柄关闭错误；`containProcess` 的三条错误路径丢弃 `CloseHandle`；`GetConsoleProcessList` 成功时把 `nil` 错误用 `%v` 印出来；`fmt.Errorf` 无格式动词应为 `errors.New`；`buildRules` 静默丢弃无法解析的适配器；`OpenDNSExemption` 丢弃 `os.Stat` 错误导致路径错误与"是目录"不可区分。
- 桌面侧：`platform/wails/appearance_windows.go:34-37`（DWM HRESULT 后拼接无意义的 `callError`）；`startup/wifi_windows.go:62,127,146` 与 `wails/tray_position_windows.go:29,33,36`（WLAN 与 Win32 返回码多处丢弃）；`startup/wifi.go:49`（XML 解析失败被误报为"没有可用网络"）；`platform/autostart_windows.go:59-69`（先清审批记录再写 Run 值，写失败无回滚）；`platform/webview2_windows.go:46-50`（字符串转换失败可弹出全空白错误框）；`engineclient/client.go:504-508`（`readLoop` 静默 `return`，零可观测性）；`releaseversion/version.go:82-93`（legacy 分支不校验 65535 上限，与自身文档矛盾）；`releaseversion/metadata.go:50,66`、`version.go:29`、`cmd/release-version/main.go:72`（报错顺序不确定、硬编码权限、错误信息缺上下文）。
- **Win32 HRESULT 丢弃（成规模）**　`engine/internal/tun/cleanup_windows.go`、`engine/internal/platform/wfp_windows.go` 等存在大量 `status, _, _ := proc.Call(...)`。这是 Win32 Go 绑定的**常规惯用写法**（`x/sys/windows` 等库均如此），本身不构成缺陷，列出仅作完整性并作为 P0/P1 中"HRESULT 未检查"的对照背景。**建议**仅在**系统状态变更类**调用（装/删网卡、改防火墙、DNS、注册表写入）上要求检查 HRESULT。

---

## 已核查、确认无问题（附搜索 pattern 证明确实查过）

| 检查项 | 使用的 pattern / 方法 | 结论 |
|---|---|---|
| WFP 对象是否有删除路径 | `SubLayerDelete\|FilterDelete\|filterDelete\|subLayerDelete`（全仓 `*.go`） | **No matches found** —— 确证 A1，不是"漏看" |
| 全仓 panic 边界 | `recover\(\)`（全仓 `*.go`，含测试文件） | **No matches found** —— 确证 E1 |
| 协议错误码在前端的可达性 | 全量检索协议 14 码在 `desktop/frontend/src`（118 文件）中的出现 | **0 次** —— 确证"错误码在第三跳丢失" |
| CI 是否吞失败 | 4 个 workflow 全文检查：`continue-on-error`、被注释的检查、`\|\| true` | **未发现吞失败**。3 处 `|| true` 分别在 `trap cleanup`（`release-smoke.yml:79`、`create-release-tag.yml:49`）与被下游 `exit 1` 接管的 curl/ls-remote（`build.yml:555,605,634`），属正确用法；`package.json:9` 的 `tsc && vite build` 确保类型错误 fail CI |
| 前端异步队列是否有未处理拒绝 | 读 `latestSaveQueue` / `settingsQueue` 实现 | **无 unhandled rejection，回滚语义正确** |
| VirtualAdaptersPage 部分失败处理 | 逐行论证 | **部分失败与 busy 滞留均有正确处理** |
| ToolsPage / BlockedDomainsPage / RuleSetsPanel | 逐行检查 | **未发现问题** |
| `fileintegrity` 哈希校验是否被吞 | 读 `tun/supervisor.go:681`、`:687`、`tun/config_stage_windows.go:75` 三个调用点 | **全部 `%w` 正确包装并 return**，未发现问题 |
| WFP 失败路径的 deferred Close | 读 `engine/internal/wfp/dns_exemption_windows.go:196-200` | 结构本身是**正确的回滚范式**（问题在于 Close 的语义与错误丢弃，见 A1） |
| `expiry/index.go` 堆不变量与并发 | 全文检查 | 堆不变量自洽，6 处访问全在 `r.mu` 内 |
| `runtime.Transition` 是否会死锁 | 全文检查 | **无死锁**；且确实会对非法转换返回 `fmt.Errorf("invalid engine state transition %q -> %q", previous, next)`（`:67`） |
| 引擎状态转换丢弃 error 是否为缺陷 | 逐行比对 `engine/internal/runtime/runtime.go:85-102` 的 `canTransition`，逐一核对 9 处 `_` 丢弃的调用点 | **不是缺陷**：`engine/internal/server/server.go:722`（Running→Failed）、`:421`（Starting→Failed）、`:463`（Failed→Stopped）、`:485`/`:500`/`:512` 等状态前置条件**均合法**，被丢弃的 error 实际为 nil。合并分片 `reports/parts/engine.md` 的 `[dns-server] P2-9` 将其列为整洁度项而非缺陷，与此结论一致 |
| WTS 会话状态是否过严 | 读 `engine/cmd/hypomux-engine/service_session_windows_test.go:12-31` | 测试明确断言**只允许 `Active`**，故不把"拒绝 `Connected`"列为缺陷 |
| 启动回滚闭包 | 读 `desktop/internal/services/engine.go:862-902` | **设计优秀，全仓最成熟的范式**：独立 `cleanupCtx`（`:867`，含为何不能复用已超时 ctx 的注释）、`recordCleanupFailure`（`:870-879`）正确过滤 `invalid_state`、`errors.Join` 汇总；`:918/932/953/956/971/974` 均正确调用 |
| 引擎 TUN 激活失败路径 | 读 `engine/internal/server/server.go:705-730` | `errors.Join(err, tunStopErr, wfpErr, proxyStopErr)` **正确汇总**，建议 B2 直接沿用 |
| `closeDNSExemption` 的重试设计 | 读 `engine/internal/server/server.go:1043-1056` | 失败时**保留引用**供下次重试，设计**正确**（仅因 B2 的进程立即退出而失效） |
| 设置/规则集保存失败的回滚 | 读 `settings.go:371`、`singbox_rules.go:453`/`:483`、`scheduling.go:123` | 均实现了"保存失败 → 恢复运行配置"，**方向完全正确** |
| 原子写入实现 | 读 `desktop/internal/services/atomic_file.go` 全文 | `:10` MkdirAll、`:31` Sync、`:38` Rename、`:20–27` deferred cleanup **全部正确**，唯一问题在 `:13` 的固定临时文件名 |
| 团队是否已识别同类问题 | 读 `desktop/internal/services/hyperv_adapter.go:1683` | 保留整改注释"原来这里是 `_ = s.applyPoolUpdate(...)`，错误整个被丢掉（审计 M2）。后果："（`hyperv_adapter_test.go:1939` 亦有）——**团队具备自检能力**，本报告 P0/P1 可复用同一套整改流程 |
| vNIC 幂等语义是否有文档 | 读 `engine/internal/vnic/manager.go:133-135`、`:210-215` | `Create` 幂等语义与 `stopLocked` 设计意图均有注释说明，问题仅在回滚路径未复用（A5） |
| `desktop/scripts/` 目录 | 目录检索 | **不存在**，只有 `desktop/portable/` 3 个文件 |

---

## 跨分片同源问题去重

engine/ 的四个专项分片合并时识别出若干**同一根因、多处表现**的问题。为避免整改时被当成多个独立 bug 重复投入，在此单列：

1. **零 `recover()` 是全局单一根因，被四个分片从四个方向独立报告** —— `[core] P1-1`（整个 `engine/` 无 recover，特权守护进程一次 panic 即留下系统状态残留）、`[cmd] P0-4`（两个承载引擎栈的后台 goroutine 无 recover）、`[proxy] P0-1`（每连接 goroutine 无 recover）、`[dns-server] P1-2`（`runLookup` goroutine 无 recover，留永不解开的 inflight 条目）。**建议按一次性整改处理，而不是分四处打补丁。**

2. **WFP DNS 豁免泄漏：一个根因、两个 manifestation** —— `[core] P0-3` 是**契约缺陷本身**（`dns_exemption_windows.go:196-200` 丢 `Close()` 错误，而 `Close()` 失败时故意保留句柄"等稍后重试"，但局部变量让这个 later Close 永不存在）；`[dns-server] P0-3` 是它在 server 侧的**后果**（豁免替换无回滚，运行时状态与代理实际用的 adapter 池永久错位）。两处调用点 `server.go:650`、`scheduling.go:50`。**修 `Close()` 契约 + 给替换加回滚，可一并解决。**

3. **vNIC 回滚丢错** —— `[core] P1-4` 定位 `engine/internal/vnic/manager.go:170-179`，与本报告 A5 同源，保留在 core 分片详述。

4. **多条"退出即残留"路径共享同一放大器** —— `[cmd] P0-3`（Service 停机超时直接退进程）与 `[core] P1-1`（panic 直接杀进程）合起来解释了为什么 B2/B3 在生产里会放大成"机器彻底断网"：**两条路都会跳过 `server.go:1012-1041` 的 `stopProxyForHostExit`**。

---

## 分片索引

各专项分片的**逐条全文**保留在 `reports/parts/` 下，本报告为汇总视图。已列条目均为已逐行验证的确认结论。

| 子线 | 分片文件 | 条数 | P0 / P1 / P2 |
|---|---|---|---|
| **engine/ 全域**（合并产物，含跨分片交叉结论） | `reports/parts/engine.md` | **97** | 19 / 40 / 38 |
| ├ engine cmd / service / policy | `reports/parts/engine-cmd.md` | 31 | 6 / 11 / 14 |
| ├ engine core（tun / wfp / vnic / platform） | `reports/parts/engine-core.md` | 20 | 3 / 10 / 7 |
| ├ engine proxy | `reports/parts/engine-proxy.md` | 19 | 6 / 8 / 5 |
| └ engine dns / server / runtime / expiry | `reports/parts/engine-dns-server.md` | 27 | 4 / 11 / 12 |
| desktop shell / engineclient / startup / platform | `reports/parts/desktop-shell.md` | 27 | 5 / 10 / 12 |
| frontend / protocol / CI | `reports/parts/frontend-protocol.md` | 20 | 4 / 9 / 7 |
| desktop services（settings / support_log / 系统代理 / 快照落盘） | `reports/parts/desktop-services.md` | 31 | 6 / 11 / 14 |
| 主审交叉核验 | 本报告正文 | 17 | 5 / 9 / 3 |

> 注：`engine-core.md` 与 `engine-tun-wfp.md` 曾为同一份审计的两个副本（逐条一致，仅行号偏移 1 行），已核对后保留 `engine-core.md` 并删除重复件。

---

*本报告为只读审计，未修改仓库中任何源文件。*
