# 20 — keeper 层虚拟网卡真机探测报告

**任务**：验证 keeper 层配置能否在真机创建**真实可用**的虚拟网卡（用户原话：「我要的是真实可用的虚拟网卡」）。
**探测标识**：`HypoMux-VNIC-Probe` / `10.67.0.1/24`（资源隔离，未使用 `HypoMux-VNIC`/`10.66.0.1`，未触碰 `HypoMux-Tun` ifIndex 51）。
**执行时间**：2026-10-03 17:4x（本机时区 +0800）。
**状态**：⚠️ **实验因用户环境故障（新构建 TUN 模式无法上网）被 Lead 紧急中止**——见 §7。已采集的证据按原样落盘，未跑的步骤如实标注"未执行"。
**结论一句话**：keeper 层**能**创建出 Windows 真实认到的 Wintun L3 适配器（真 IPv4、真 MTU、可绑定、kill 即消失），但**当前 keeper 配置产出的不是"真实可用的虚拟网卡"**——它是一个不改路由、不做 DNS、不转发任何流量的空壳 TUN。

---

## 0. 结论先行（What we can and cannot claim）

| 判定项 | 结果 | 证据 |
|---|---|---|
| sing-box 接受 keeper 生成的配置 | ✅ 是（`check` EXIT=0） | §2 |
| 真机创建出适配器，Windows 认到 | ✅ 是（`Get-NetAdapter` = `sing-tun Tunnel`, Up） | §3.1 |
| 拿到配置的 IPv4 地址 | ✅ 是（`10.67.0.1/24`, Manual, Preferred） | §3.1 |
| 配置的 MTU 生效到系统 | ✅ 是（NlMtu=1420；1500 变体也 1:1 生效） | §3.2 |
| 地址被本机 IP 栈**真实持有** | ✅ 是（socket 可 bind；无适配器时 bind 失败） | §4.2 |
| 生命周期随 keeper 进程干净结束 | ✅ 是（kill 后 1s 内适配器消失、路由回滚） | §5 |
| **是否有流量被引入这张网卡** | ❌ **否**（默认路由 0 改动；`auto_route:false`） | §4.3 |
| **是否能域名解析** | ❌ **否**（配置无 `dns` 段，接口无 DNS 服务器） | §4.4 |
| **是否具备代理转发能力** | ❌ **否**（唯一出站是 `direct`，只是把流量交还系统） | §1.2 |
| **对端能否拿到 IP（DHCP）** | ❌ **否**（无 DHCP 服务、无对端地址分发，地址是静态 Manual） | §6 |
| 快速重启是否可靠 | ❌ **否**（kill 后数秒内重启会 FATAL 退出，可复现） | §5.3 |
| 本机 `ping` 能否作为可用性证据 | ❌ **完全不能**（对照实验证伪） | §4.1 |

**因此**：可以诚实地说"keeper 层能创建真实的虚拟网络接口设备"，**不能**说"keeper 层已交付真实可用的虚拟网卡"。

---

## 1. 被验证的源码事实（只读，未修改任何源码）

### 1.1 keeper 生成的 stack 实际值
`desktop/internal/services/tun_config.go:378-388` `normalizeTunStack(value)`：
- `"" → "system"`；允许 `system|mixed|gvisor`；其它值报错 `不支持的 TUN 协议栈：%s（可选 system、mixed、gvisor）`。
- ⇒ keeper 路径下最终写入配置的 `stack` = **`"system"`**（不是 gvisor）。

### 1.2 keeper 生成的配置形状
`desktop/internal/services/vnic_config.go:15-23` 常量：`vnicDefaultInterfaceName = "HypoMux-VNIC"`、`vnicDefaultAddress = "10.66.0.1"`、`vnicPrefixLength = 24`、`vnicMTU = 1420`。

`desktop/internal/services/vnic_config.go:41-115` `writeVNICSingBoxConfig(...)` 产出**只有三段**：
1. `log { level: "warn", timestamp: true }`
2. `inbounds[1]`：`{ type: "tun", tag: "tun-in", interface_name: <name>, address: ["<addr>/24"], mtu: <mtu>, auto_route: false, strict_route: false, stack: <stack> }`
3. `outbounds[1]`：`{ type: "direct", tag: "direct" }`

**没有**：`dns` 段、`route` 段、`route_exclude_address`、`dns_mode`、`route_address`、`experimental`/`clash_api`。**唯一出站是 `direct`**。

### 1.3 与主 TUN 路径的对比
`desktop/internal/services/tun_config.go:230-238` 主路径：`interface_name:"HypoMux-Tun"`、`mtu:1492`、`auto_route:true`、`strict_route`、`route_exclude_address`、`dns_mode`，并带完整 `dns`/`route`/`experimental` 段。

⇒ **VNIC 路径生成的是一个"降级到只剩 direct 出站"的配置**，这就是"能建网卡但不能上网"的配置级根因。

---

## 2. 实验 1–3：配置接受性

```powershell
& <repo>\desktop\bin\sing-box.exe check --help
# -c, --config stringArray    → 参数合法
& ...\sing-box.exe check -c %TEMP%\vnic-probe\sing-box-vnic-probe.json
# EXIT=0，无任何输出
```

**结果**：`EXIT=0`，零输出 ⇒ 配置被 sing-box 1.14.2 真实接受。
**注意**：`check` 只做配置校验，**不创建适配器**，不能作为"网卡可用"的证据。
证据文件：`%TEMP%\vnic-probe\02-check.txt`。

探测配置（严格复刻 keeper 形状，UTF-8 无 BOM）：
```json
{
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [{ "type": "tun", "tag": "tun-in", "interface_name": "HypoMux-VNIC-Probe",
                 "address": ["10.67.0.1/24"], "mtu": 1420, "auto_route": false,
                 "strict_route": false, "stack": "system" }],
  "outbounds": [{ "type": "direct", "tag": "direct" }]
}
```

---

## 3. 实验 4：真机创建与地址/MTU 验证

启动：`Start-Process sing-box.exe -ArgumentList 'run','-c',<cfg>`（首实例 **PID 17980**）。

### 3.1 适配器与地址（`04-runtime-checks.txt`）
**适配器在启动后 1 秒内出现**：

```
Name                 : HypoMux-VNIC-Probe
InterfaceDescription : sing-tun Tunnel
Status               : Up
ifIndex              : 68
MacAddress           : (空 — Wintun 无 MAC)
MtuSize              : 65535        ← 见 §3.2，此值不可信
MediaConnectionState : Connected
LinkSpeed            : 100 Gbps
```

```
Get-NetIPAddress -InterfaceIndex 68:
10.67.0.1/24   PrefixOrigin=Manual   AddressState=Preferred
fe80::....%68/64
```

sing-box stdout/stderr **全空**（`log level = warn`，成功时无输出）→ `03-run-stdout.txt`/`03-run-stderr.txt` 均 0 字节。

### 3.2 MTU 真相（重要工具陷阱）
- `Get-NetAdapter.MtuSize` 恒为 **65535**（Wintun/NDIS 汇报的假值，**不可用于判定**）。
- 真实生效值要这样看：
  ```
  netsh interface ipv4 show subinterfaces
  → 1420  1  0  17939  HypoMux-VNIC-Probe

  Get-NetIPInterface -InterfaceIndex 68 -AddressFamily IPv4
  → NlMtu=1420  InterfaceMetric=5  AutomaticMetric=Enabled  Dhcp=Disabled
  ```
⇒ **配置的 1420 真的写进了 Windows IP 栈**。变体 `mtu=1500` 同样 1:1 生效（netsh 行 = 1500，NlMtu=1500，见 `09-variants.txt`）。

---

## 4. 实验 5：**"真实可用"的判别证据**（本任务的核心）

### 4.1 ❌ ping 是无效证据 —— 对照实验证伪
表面结果（看起来"可用"）：
```
ping -n 3 10.67.0.1   → 3/3 Reply, TTL=64, EXIT=0
Test-NetConnection 10.67.0.1 → PingSucceeded=True, InterfaceAlias=HypoMux-VNIC-Probe
```

**对照实验**（`11-control-ping.txt`）：把探测适配器**完全删除**后，再次 ping：
```
10.67.0.1   → Reply TTL=64 <1ms     ← 适配器已不存在，依然"通"
10.67.0.99  → Reply TTL=64 <1ms     ← 从未分配
10.66.0.1   → Reply TTL=64 <1ms     ← Lead 的保留段
10.77.77.77 → Reply TTL=64 <1ms     ← 从未分配
tracert 10.67.0.1 → 1 跳直达
```
**根因**：既存的 `HypoMux-Tun`（用户主 TUN 路径）持有 `0.0.0.0/1` 与 `128.0.0.0/1`（metric 0，经 172.19.0.2），把**全部 IPv4**吞进 sing-box，由 sing-box 应答 ICMP。
⇒ **在这台机器上 ping 任何地址都通**。任何用 ping 证明虚拟网卡可用的结论都是伪证（**教训：ping 在本机零判别力**）。

### 4.2 ✅ 有效证据 —— 本机 socket 绑定
```powershell
$l = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Parse("10.67.0.1"), 0)
$l.Start()
# BIND_OK  local_endpoint = 10.67.0.1:60038
# 客户端 Connect("10.67.0.1",60038) → CONNECT_OK=True
# 服务端 AcceptTcpClient()          → ACCEPT_OK=True
```
**对照 A**（探测适配器不存在时执行同样代码）：
```
使用"0"个参数调用"Start"时发生异常:
"The requested address is not valid in its context"
```
⇒ **`10.67.0.1` 确实被本机 IP 栈真实持有**（且仅在 keeper 进程存活期间）。这是本次探测中**唯一**能区分"真网卡"与"空壳/ping 幻觉"的本机判据。

### 4.3 ❌ 路由：没有任何流量被引入
| 时刻 | ROUTE_COUNT | DEFAULT_ROUTE_COUNT |
|---|---|---|
| 基线 | 159 | 12 |
| 探测实例运行中 | **164** | **12** |
| kill 之后 | 159 | 12 |

运行期新增的恰好 **5 条**（`05-after-run-routes.csv` 与 `06-after-kill-routes.csv` 差集），全部 `ifIndex=68`、`NextHop=0.0.0.0`、`RouteMetric=256`、`Protocol=Local`、`Store=ActiveStore`：
```
10.67.0.0/24        10.67.0.1/32        10.67.0.255/32
224.0.0.0/4         255.255.255.255/32
```
**默认路由与既有路由 0 改动**。⇒ `auto_route:false` 被忠实执行——这张网卡只是本机的一个"孤岛"，没有任何流量被导进去。

### 4.4 ❌ DNS：接口无任何解析能力
DNS 集合对比（`05-after-run-dns.csv` vs `06-after-kill-dns.csv`）：只多出一条 `ifIndex=68` 的**空 `ServerAddresses`** 记录；既有各接口的 DNS 服务器**完全未变**。探测接口 `Get-DnsClientServerAddress` 无任何服务器。配置里本来也没有 `dns` 段。

### 4.5 ❌ 出站：没有代理能力
`outbounds` 只有 `direct`。即使有流量被引入，sing-box 也只是把它交还系统网络栈——**不存在任何代理转发语义**（无 socks/http/vmess 等出站）。

---

## 5. 实验 6/7：生命周期、变体与启动竞态

### 5.1 ✅ kill 后干净回滚（无需任何手工清理命令）
`Stop-Process -Id 17980 -Force` →
- 进程消失；
- **适配器 1 秒内自动消失**；
- ROUTE_COUNT 回到 159、DEFAULT 回到 12，无残留路由；
- **全程从未需要 `Remove-NetAdapter`**（本次探测自始至终没有执行过 `Remove-NetAdapter`/`Remove-NetRoute`/`netsh` 修改类命令）。

⇒ 适配器由 Wintun 驱动在进程句柄关闭时自动销毁，生命周期是干净的。

### 5.2 变体对比
| 变体 | 结果 |
|---|---|
| `stack=system, mtu=1420` | 1s 出现，Up，10.67.0.1/24，MTU 1420 生效 |
| `stack=gvisor, mtu=1420` | **t=3s** 出现（更慢），Up，10.67.0.1/24，连续 25s 稳定（`13-gvisor-timeline.txt`） |
| `stack=system, mtu=1500` | 可用，MTU 1:1 生效（NlMtu=1500） |

### 5.3 ❌ 可复现缺陷：kill 后快速重启 → FATAL 退出
在上一实例被 kill 后数秒内再次启动（`system` 与 `gvisor` 均会复现），sing-box 硬失败（`12-gvisor-stderr.txt`、`14-iter1-stderr.txt`、`15-a-err.txt`，三者均 308 字节）：

```
+0800 2026-10-03 17:47:41 WARN inbound/tun[tun-in]: open interface take too much time to finish!
FATAL[0015] start service: start inbound/tun[tun-in]: configure tun interface:
  (create adapter: Cannot create a file when that file already exists. | open existing adapter: Element not found.)
```

失败窗口内的现象（`10-gvisor-detail.txt`）：`Get-NetAdapter` 可能短暂出现"半成品" `HypoMux-VNIC-Probe`（`Status` 空、`MediaConnectionState=Unknown`），**没有任何 IPv4**（10.67 地址计数 = 0；此时 socket bind 报 "The requested address is not valid in its context"），随后进程退出、僵尸适配器自行消失。

复现率（`14-repeatability.txt`，3 次连续 start/kill）：**iteration 1 失败（FATAL，耗时 15s），iteration 2/3 ready=1s 成功**。

⇒ **建议修复**：同名适配器的创建/销毁需要串行化——启动前等待旧适配器消失（轮询 + 超时）、失败重试、"create adapter / open existing adapter" 两条错误路径都要兜住；或改用唯一名称。

---

## 6. 直接回答用户："为什么虚拟网卡拿不到 DHCP IP？"

因为**这份配置里根本没有任何东西会发 IP**：
1. **没有 DHCP 服务**：sing-box 的 `tun` inbound 不实现 DHCP 服务器；keeper 配置里也没有任何 DHCP 组件。
2. **10.67.0.1（线上是 10.66.0.1）是"本机自己"的地址**（`PrefixOrigin=Manual`），不是分发给对端的地址池。
3. `Get-NetIPInterface` 显示该接口 **`Dhcp=Disabled`**，接口上也不存在任何 DHCP 客户端租约流程。
4. 对端要拿到 IP，必须有：静态地址配置，或由另一个组件（如 dnsmasq/内置 DHCP 服务器）提供 DHCP，或由上层协议（如 ipv4 地址池/隧道协商）分配。**当前 keeper 层三样都没有。**

此外，即便对端手工配好同网段静态地址，也仍然"上不了网"：`auto_route:false` 不改默认路由（§4.3）、无 `dns` 段（§4.4）、唯一出站 `direct`（§4.5）。

---

## 7. ⚠️ 中止说明（如实记录）

用户随后报告：**安装新构建后 TUN 模式无法上网**（系统代理模式正常），并要求用**干净机器**做根因定位。Lead 下达了立即中止 + 清理指令（team message，2026-10-03）。遵照执行：

- **被中断的步骤**：原计划的"重启竞态 exit-code 捕获"phase2/3 被中断（`15-*` 系列只留下部分文件，未跑完）。
- **故意未执行的实验**：`auto_route:true` 对比实验**主动放弃**——它会在本机安装 `0.0.0.0/1` + `128.0.0.0/1` 默认路由，可能黑洞化整机并破坏 Lead 的并行实验与用户正在依赖的主 TUN 路径。这是**安全考量下的主动取舍，不是失败**。
- **本次探测内所有观测数据均为中止前已落盘的真实输出**，未做任何推测性补写。

---

## 8. 清理与最终机器状态（只读核查）

清理动作：本次探测**没有**执行任何 `Remove-NetAdapter` / `Remove-NetRoute` / `Set-DnsClientServerAddress` / `netsh` / `Restart-NetAdapter` 等改动系统网络的命令。探测实例的 sing-box 进程结束（或被 `Stop-Process` 结束）后，适配器由驱动自动销毁，路由自动回滚。

核查结果（all clean）：

```
Get-NetAdapter | ft Name,Status,ifIndex
  ... HypoMux-Tun   sing-tun Tunnel   Up   51      ← 用户主路径，未受影响
  (无 HypoMux-VNIC-Probe)

HypoMux-VNIC-Probe 适配器存在      = False
10.67.* 路由条数                   = 0
10.67.* 地址条数                   = 0
ROUTE_COUNT = 159 / DEFAULT_ROUTE_COUNT = 12      ← 与基线一致

Get-CimInstance Win32_Process -Filter "Name='sing-box.exe'" | select ProcessId,CommandLine
  19216  "C:\Program Files\HypoMux\bin\sing-box.exe" run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-254382414.json
  ← 这是用户的主 TUN 路径进程，绝不可杀；本次探测的进程已全部不存在
其它用户进程（未触碰）：hypomux-engine.exe 13308 / 6920，hypomux.exe 6240

git status --porcelain
  ?? reports/        ← 未修改任何源码/配置/锁文件，未 git add / 未 commit
```

仓库内本次唯一写入的文件即本报告 `reports/vnic/20-keeper-probe.md`；其余全部产物在 `%TEMP%\vnic-probe\`。

---

## 9. 证据文件索引（`%TEMP%\vnic-probe\`）

| 文件 | 内容 |
|---|---|
| `01-baseline.txt` | 基线：14 个适配器、路由 159/12、DNS 快照、IP 地址表 |
| `02-check.txt` | `sing-box check` EXIT=0 |
| `03-run-stdout.txt` / `03-run-stderr.txt` | 首次 run 输出（均 0 字节 = 无错误） |
| `03-run.pid` | 首实例 PID（17980） |
| `04-runtime-checks.txt` | 适配器/地址/MTU/接口指标的完整运行期快照 |
| `05-after-run.txt` + `05-after-run-routes.csv` + `05-after-run-dns.csv` | 运行期路由 164 与 DNS 快照 |
| `06-after-kill.txt` + `06-after-kill-routes.csv` + `06-after-kill-dns.csv` | kill 后路由回滚 159 与 DNS 快照（用于差集） |
| `09-variants.txt` | mtu=1500 变体的 netsh/接口证据 |
| `10-gvisor-detail.txt` | gvisor 失败窗口：半成品适配器、0 个 IPv4、bind 失败 |
| `11-control-ping.txt` | **对照实验：适配器不存在时 ping 任意地址仍全通** |
| `12-gvisor-longwait.txt` / `12-gvisor-stderr.txt` | gvisor 重启 FATAL 原文 |
| `13-gvisor-timeline.txt` | gvisor 干净状态时间线（t=3s 出现，25s 稳定） |
| `14-repeatability.txt` + `14-iter{1,2,3}-{stdout,stderr}.txt` | 3 次连续 start/kill 复现率 |
| `15-a-err.txt` | 紧接着 kill 后启动的 FATAL 原文（中止前最后一次观测） |
| `sing-box-vnic-probe{,-gvisor,-mtu1500}.json` | 三份探测配置（严格复刻 keeper 形状） |

---

## 10. 要让 keeper 层虚拟网卡"真实可用"所需的最小改动（建议）

1. **流量引入**：`auto_route:true`，或用 `route_address` 限定引流范围 + 手工路由（避免全局黑洞风险，需在 UI 层给出明确提示）。当前 `auto_route:false` ⇒ 网卡是孤岛。
2. **真实出站**：给 VNIC inbound 配一个真正的代理出站（socks/http/vmess 等），而不是只有 `direct`。没有这一步，"网卡通"也不等于"能上网"。
3. **DNS**：加 `dns` 段（必要时配合 `route` 里的 hijack-dns），否则接口无解析能力。
4. **对端地址分发**：明确方案——静态地址约定，或提供 DHCP 组件；sing-box `tun` 自身不提供 DHCP。
5. **启动竞态**：修掉"同名适配器 kill 后快速重启 → FATAL（Cannot create a file when that file already exists / Element not found）"的竞态（等待旧适配器消失 + 重试 + 兜住两条错误路径，或唯一命名 + 清理）。
6. **判据修正**：任何验收测试**不得**用 `ping` 判定虚拟网卡可用性（本机零判别力，见 §4.1）；应使用 socket bind 判据（§4.2）+ 路由差集（§4.3）+ DNS 检查（§4.4）。
