# 真机判定：新构建「TUN 模式无法上网」的根因 / 虚拟网卡能力现状 / 便携包结论

- 判定期：2026-10-03（用户已把机器回滚到官方 2.7.0 之后）
- 判定人：Lead（主会话），证据全部来自本机只读命令与仓库代码
- 相关 HEAD：`37571e5a0531c16621cf4ac3e7511ac21135aa00`（新构建）；已安装回滚版 `d6fb799c08068d2bdea79d3a3521795adf98048d`（官方 2.7.0）
- 唯一日志：`%USERPROFILE%\.hypomux\logs\app.log`（344 行，覆盖 2026-10-03 17:37:59 – 17:55:26，含新构建与官方版两段会话）

---

## 0. 结论摘要

| 问题 | 判定 | 置信度 |
| --- | --- | --- |
| 新构建下「TUN 模式无法上网」 | **不是我们提交的代码引起的回归**。是当时生效的 **DNS 设置组合**（`dns_policy:"off"` + `dns_egress_mode:"adapter"`）在本机网络下**DNS 解析必然失败**，TUN 接管了流量却解析不了域名 ⇒ 上不了网；同一台机器、同一份网络，官方版在 `dns_policy:"auto"`（DoH）下立刻 `verified:true` | 高（日志逐阶段对照） |
| TUN 网卡/路由/WFP 是否被我们的改动弄坏 | **没有**。新旧构建三次会话都打出 `[TUN] sing-box is stable and owns TUN/WFP/routes`，`HypoMux-Tun` 每次都正常建成并接管 | 高 |
| 「创建的虚拟网卡拿不到 DHCP IP」 | 我们的实现**根本不提供 DHCP**：Wintun 是 L3 点对点设备，我们只写静态 `10.66.0.1/24`，配置里唯一出站是 `direct`，`auto_route:false` ⇒ 既不引入流量也不分发地址。要 DHCP/桥接级能力必须换技术路线（见 §4） | 高（配置原文 + 真机探针） |
| 便携包能否在不碰已安装版的前提下验证新功能 | **可以**，但必须满足 3 个硬条件（保留 `bin\` 布局 / **以管理员启动便携版**（停 `HypoMuxCore` 服务为保险）/ 独立 `HYPOMUX_DATA_DIR`），见 §5 | 高（代码依据） |
| 无法 100% 排除的部分 | **没有 settings 备份**（`%USERPROFILE%\.hypomux\` 下不存在），所以「`dns_policy:"off"` 是谁、什么时候写进去的」无法举证。只能证明「故障会话读到的就是 off，恢复会话读到的是 auto」 | — |

---

## 1. 证据链 A：三次会话的 `session_context` 就是判别器

app.log 每次会话开头都会打印一行生效设置（行号 / 时间 / 内容）：

| app.log 行 | 时间 | 引擎 pid | `core_commit` | `dns_policy` | `dns_egress_mode` |
| --- | --- | --- | --- | --- | --- |
| 3 | 17:37:59 | 8632 | `37571e5a`（新构建） | **off** | **adapter** |
| 51 | 17:38:45 | 6460 | `37571e5a`（新构建） | **off** | **adapter** |
| 122 | 17:41:11 | 6920 | 官方 2.7.0（`d6fb799c`，见 §3） | **auto** | **auto** |

三行的 `http_port:10801 / socks_port:10800 / strategy:round-robin / system_proxy_takeover:true` 完全一致 ⇒ 差异被锁定在 DNS 两项上。

## 2. 证据链 B：逐阶段连通性对照（决定性）

`tun_connectivity` 事件每次会跑 4 个阶段：`dns_bootstrap` / `aggregation_data`(http) / `aggregation_data`(https) / `tun_data_path`。

新构建的 5 次检查（行 26/70/96/100/113，17:38:13 – 17:40:43）全部 `connectivity_checked verified:false`，形态完全一致：

- `dns_bootstrap` **ok=false**，`endpoint=114.114.114.114:53`，`outbound=system-direct`，
  `error=read udp4 192.168.16.<masked>:56093->114.114.114.114:53: i/o timeout`
- `aggregation_data` ok=true（http `msftconnecttest` 通、https `baidu` 多数通）⇒ **聚合代理链路是好的**
- `tun_data_path` **ok=false**，`error=http://www.msftconnecttest.com/connecttest.txt: curl: (28) Resolving timed out after 6008 milliseconds; https://www.baidu.com/: curl: (28) Resolving timed out ...`
  ⇒ **走 TUN 的数据通路失败的原因是「域名解析超时」**，不是包走不通

官方版恢复后的 20+ 次检查（行 140 起，17:41:13 – 17:55:26）全部 `verified:true`，同样是 4 阶段，形态：

- `dns_bootstrap` **ok=true**，`endpoint=dns.alidns.com@223.5.5.5:443`（**DoH**），`outbound=system-direct`
- `tun_data_path` **ok=true**（17:43:16、17:46:19 偶发 `baidu https` 用 IP 直连 EOF，属单点抖动，不影响判定）

配套的 `dns_prepared` 事件：

| 会话 | app.log 行 | adapter | policy | server | transport |
| --- | --- | --- | --- | --- | --- |
| 新构建 | 13 / 61 | `以太网` | **off** | `114.114.114.114:53` | `udp` |
| 官方版 | 132 | `vEthernet (xuni-01)` | **auto** | `dns.alidns.com@223.5.5.5:443` | **`doh`** |

`engine started` 事件同样一致：新构建 `"effective_dns_policy":"off"`（行 29/74），官方版 `"effective_dns_policy":"auto"`（行 145）。

**机制**：`dns_policy:"off"` 表示不接管 DNS，系统直连明文 UDP/53 查 `114.114.114.114`；`strict_route:true` 下这条查询在 TUN 环境中失败，且本机默认路由的 metric 让查询从 `192.168.16.<masked>`（`vEthernet (xuni-01)`）出去，而这张卡的上游（`192.168.16.<masked>`）对本机不可达（app.log 里官方会话同样大量 `socks_reply=6 error=vEthernet (xuni-0X) connect: dial tcp4 192.168.16.15X:0->…: i/o timeout`）。`dns_policy:"auto"` 走 DoH（TCP/443 → `223.5.5.5`）就通，TUN 随即恢复。

### 本机网卡地址（用于核对 192.168.16.<masked>）

| 网卡 | IPv4 |
| --- | --- |
| `以太网`（Realtek 2.5GbE，ifIndex 8） | 192.168.16.<masked>/24 |
| `vEthernet (xuni-01 … xuni-11)` | 192.168.16.<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> |
| `vEthernet (Default Switch)` | 172.25.160.<masked>/20 |
| `HypoMux-Tun` | 172.19.0.1/30 |

⇒ 失败探测的源地址 `192.168.16.<masked>` = **xuni-01**，不是应用自己认定的 `以太网`（192.168.16.<masked>）。

## 3. 证据链 C：我们的提交没有碰 TUN / DNS / 路由

- `git show 37571e5 -- desktop/internal/services/settings.go` 的 34 行改动**逐行只与 AI 助手有关**：删 `AIEnabled` 字段、`DefaultSettings()` 里的 `AIEnabled: true`、`aiExecution sync.RWMutex` 及其 4 处 Lock/Unlock 对、`aiEnabledChanged` 回调、`MigrateLegacy`/`RollbackLegacyMigration` 的 AI 保留行、`UpdateFields` 的 `case "ai_enabled":`、`reload()` 的 `ai_enabled` 补默认。**没有一行涉及 `dns_policy` / `dns_egress_mode` / `mode` / `tun_stack` / `strict_route` / `selected_adapter_ids` / 路由 / WFP。**
- 窗口内 TUN 侧改动只有：`engine/internal/tun/{supervisor.go,readiness_windows.go,readiness_other.go,interface_name.go}`（就绪判定从硬编码 `HypoMux-Tun` 改为「期望名优先、配置名兜底」+ 新增 vnic 模块）与 `engine/internal/server/server.go` 新增 vnic RPC。判据：新构建打 `[TUN] expecting adapter HypoMux-Tun with IPv4 address 172.19.0.1`、官方版打 `[TUN] expected IPv4 address: 172.19.0.1`，**两次都紧跟着 `[TUN] sing-box is stable and owns TUN/WFP/routes`**（行 20/68/139）⇒ 就绪判定在两条路径上都成功了。
- `desktop/internal/services/settings.go` 的 `DefaultSettings()` 里 `DNSPolicy: "auto"`（:75）、`DNSEgressMode: DNSEgressAuto`（:76），唯二合法 mode 是 `"proxy"`/`"tun"`（:423）——我们的 diff 未触碰这些默认值。
- 官方版会话里同样存在 xuni 聚合超时，说明该环境噪声与我们的改动无关。

**保留意见**：日志里看不到任何「DNS 策略被降级/被拒绝」的告警，也没有 settings 备份，因此无法确定 `dns_policy:"off"` 是用户手动选的、还是更早某次操作遗留的；也无法解释它为什么在重装官方版后变成 `auto`（可能：用户在排障中改过；或重装流程重建了设置）。可确证的是：**触发故障的是设置状态，而不是我们的二进制。**

## 4. 「虚拟网卡拿不到 DHCP IP」的正面回答

- 相关实现：`desktop/internal/services/vnic_config.go:41-115` 生成的配置只有三段：`log` + 单个 `tun` inbound（`address:["10.66.0.1/24"]`、`auto_route:false`、`strict_route:false`、`stack:"system"`、`mtu:1420`）+ 单个 `direct` outbound。真机落盘原文见 `%USERPROFILE%\.hypomux\vnic\sing-box-vnic.json`（408 B）。
- 含义：**没有 dns 段、没有任何流量被引入这张卡（默认路由零改动）、唯一出站是 direct（零代理能力）、接口 Dhcp=Disabled、也没有任何对端会分发地址**。`10.66.0.1` 是本机自己挂上的静态地址，不是地址池 ⇒ 「拿不到 DHCP IP」是设计使然，不是 bug。
- 技术事实：Wintun/sing-tun 是 **L3 点对点隧道适配器**，它不参与二层广播，因此**不可能**像桥接网卡那样从物理网卡的 DHCP 服务器拿地址。真机已证明它能建出 Windows 认可的真实适配器（`sing-tun Tunnel`、Status Up、真 IPv4、MTU 1420 生效、socket 可 bind、`ping -S 10.66.1.1 8.8.8.8` 有回包）。
- 若用户要的是「像一块真实的、能从上游拿地址的网卡」，可选路线（需产品决策，均未实施）：
  1. **桥接/二层**：改用 Hyper-V/NDIS 桥或用 `Ethernet` 层方案，把虚拟网卡桥进物理网段 → 才能拿到物理网段的 DHCP；代价是内核驱动、签名、兼容性风险大。
  2. **NAT/网关式共享**：虚拟网卡自建网段 + 内置 DHCP/DNS（sing-box 侧另起 DHCP 服务或 Windows ICS），给它发地址并做 NAT；拿到的是「本机分配的地址」，不是物理网段的。
  3. **保持 L3 点对点，但把功能做实**：给它 DNS 段 + `auto_route`（或按域名/按进程分流）+ 真实出站（经聚合/指定网卡），让它成为「可用的分流网卡」。这是成本最低、与现有 `vnic.*` RPC 契约一致的方向。
- 当前实现的**产品定位问题**（需要在文档里说清）：它是一张「有地址、能被绑定、但默认不承载任何流量」的 L3 隧道适配器，适合作为「指定程序/指定路由走这张卡」的载体，不是「替代物理网卡」。

## 5. 便携包（免安装）方案与代码依据

用户要求（m00856）：「你给我构建便携版的包」。设计如下（已派 task-19 组装、task-20 静态验证）：

```
HypoMux-Portable-2.7.0-vnic-preview\
  hypomux.exe                    ← HEAD 37571e5 构建（14,999,552 B）
  bin\hypomux-engine.exe         ← 8,482,304 B
  bin\sing-box.exe               ← 81,947,136 B（官方 1.14.2，SHA256 7bbef1de…）
  bin\wintun.dll                 ← 427,552 B
  bin\libcronet.dll              ← 9,528,832 B
  启动便携版.cmd / 恢复环境.cmd / 便携版说明.txt
  data\                          ← 首次启动由启动器创建，HYPOMUX_DATA_DIR 指向它
```

三个硬条件及其依据：

1. **必须保留 `bin\` 子目录**：`desktop/internal/engineclient/client.go:562-567` 按 `dir(exe)/bin/hypomux-engine.exe` 找引擎；`desktop/internal/engineclient/service_windows.go:73-91` 的 `allowAutomaticCoreFallbackPath()` 在「服务已装但未运行」时用 `os.SameFile` 强制要求回退引擎就是 `dir(exe)/bin/hypomux-engine.exe`，否则直接报「待启动 Core 不是当前 HypoMux 安装目录中的 bin\hypomux-engine.exe」。
2. **停 `HypoMuxCore` 服务（保险措施）+ 以管理员身份启动便携版（决定性前提）**：
   - `client.go:117` 是 `newClient(stdioLauncher{}, newPrivilegedLauncher())`，普通会话（含启动时的自动拉起）固定走 stdio：`launcher.go:127-131` 就是 `exec.Command(path)` **不带参数**，`process_windows.go:10-15` 设 `CREATE_NO_WINDOW`、`command.Dir = dir(引擎)`；而引擎 `engine/cmd/hypomux-engine/main.go:37-42` 在**无参数时 command 默认 = `"serve"`**（`runServer`，main.go:146-155）⇒ app.log 三次会话都是 `"launcher":"stdio"`。
   - 点「创建虚拟网卡」时走 `EnsureElevated`，但 `client.go:203-209` 会**先复用已有会话**：`if active && hello.ProtocolVersion == ProtocolVersion && (!requireElevated || hello.Elevated) { return hello, nil }`。`Hello.Elevated` 源自引擎自己的令牌（`engine/internal/platform/identity_windows.go:15` → `GetCurrentProcessToken().IsElevated()`），而 stdio 引擎是 UI 进程的子进程 ⇒ **只要便携版本身是以管理员启动的，stdio 引擎就已提权，「创建虚拟网卡」无需服务即可工作，根本不碰服务管道。**
   - 只有当便携版**未提权**（或 stdio 会话尚未建立）时才会走到 `privileged_windows.go:89-96` 的 **serviceFirstLauncher（服务优先）**：服务在跑就连已安装的股票版服务引擎（`\\.\pipe\HypoMux-Core-Service`，`service_windows.go:20`），它没有 `vnic.create` ⇒ 桌面层能力探测降级为「不支持」（`virtual_adapter.go` 的 `errVNICUnsupported`），**不会静默失败也不会改系统设置**。服务停止时则回退 `privilegedLauncher`（`privileged_windows.go:102-131`）→ `ShellExecuteExW(runas)`（`:312-336`）用 `File = ResolveExecutable()`（`HYPOMUX_ENGINE_PATH` 指向的便携引擎）启动 `serve-pipe --pipe … --session-token … --host-pid …`，并按需弹 UAC（回退前要过 `service_windows.go:73-91` 的 `os.SameFile` 校验：回退引擎必须是 `<exe 目录>\bin\hypomux-engine.exe`）。
   - 因此：`sc stop HypoMuxCore` 是**保险**（防止未提权场景误连股票服务引擎），跑 `启动便携版.cmd` 时的管理员要求才是让 VNIC 真正可用的决定性条件。
3. **必须隔离数据目录**：`desktop/internal/services/settings.go:136-141` 支持 `HYPOMUX_DATA_DIR`；不设就会用 `%USERPROFILE%\.hypomux`（`settings_directory_test.go:14` 断言的就是这个默认值）。而 `DefaultSettings()` 是 `Mode:"tun"`（`settings.go:63`）+ `SystemProxyTakeover:true`（`:68`）⇒ 共享数据目录 + 默认设置会让便携版一启动就再抢一次 TUN/系统代理。便携包因此预置 `{"mode":"proxy","system_proxy_takeover":false,...}`（合法取值只有 `"proxy"`/`"tun"`，`settings.go:423`）。
4. 附带限制（已写入说明）：`desktop/main.go:69,84-96` 的 `Name:"HypoMux"` + `UniqueID "io.hypomux.desktop"` 单实例机制 ⇒ 便携版**不能**与已安装版并存，必须先退出后者。

## 6. 未验证项（如实列出）

- 便携包**尚未真机运行过**（组装与静态验证在 task-19/task-20；真正启动需要先关掉用户已安装的版本并停服务，属需要用户同意的动作）。
- 未能证明 `dns_policy:"off"` 的来源；无 settings 备份。
- `dns_egress_mode:"adapter"` 的实际绑定行为（应用声明 egress=以太网，实际源地址是 xuni-01）只在日志层面观察到，未做代码级溯源（列为次要发现 F-A 待查）。
- 新构建在 `dns_policy:"auto"` 下能否正常上网：**未实测**（用户已回滚；便携包就是为这个验证准备的）。
- 37571e5 尚未重新过 GitHub CI（m00271）。

## 7. 次要发现（不阻塞，建议排期）

- **F-A（一般）**：`dns_egress_mode:"adapter"` + `egress_source:"explicit_adapter"` 声明走 `以太网`，但 `dns_bootstrap` 实际源地址是 `192.168.16.<masked>`（xuni-01）——`upstream_selected` 的语义与实际 socket 绑定不一致，排障时会被误导。建议：绑定源地址或把事件里的 egress 描述改成"建议值"。
- **F-B（一般·产品）**：TUN 模式 + `dns_policy:"off"` + `strict_route:true` 是**必然断网组合**，但 UI 只显示"无法上网"。建议在 `tun_connectivity` 的 `dns_bootstrap` 失败时给出明确诊断（含"当前 DNS 策略：off，建议改为 auto/DoH"）。
- **F-C（环境·非本项目）**：本机 11 张 `vEthernet (xuni-0X)` 的默认路由 metric=0、`以太网` metric=500，所有直连流量优先从 xuni 出去，而它们的上游不通（大量 `socks_reply=6 i/o timeout`）。这会持续污染任何"直连/DNS"探测结果，建议在排障时先把这些实验网卡禁用。
- **F-D（一般）**：`engine/internal/server/server.go:866-878` 的 stale 自愈只调 `s.vnic.Remove`，而 `engine/internal/vnic/manager.go:196-208` 的 Remove 只停自己跟踪的 keeper、不做 NIC 级删除 ⇒ 外部占用同名适配器时重试必然失败（T11 已复现，`Cannot create a file when that file already exists.`）。建议重试前按名清理并给出可区分错误码。
