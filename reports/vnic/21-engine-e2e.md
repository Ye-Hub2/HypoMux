# 21 · 引擎层虚拟网卡真机端到端验证（T10 / task-16）

- 任务：`task-16`（T10 真机引擎层端到端）
- 执行者：teammate `integration-verifier`
- 目标环境：本机物理机（Windows，<ADMIN>，High Mandatory Level）
- 唯一写入文件：本文件
- 结论一句话：**在真机上创建出来的 `HypoMux-VNIC-E2E` 是一个"真实可用"的虚拟网卡**（真实 Wintun 驱动、系统可见、IP/MTU 生效、可 ping、能真正承载出网 IP 流量、可干净移除）；**但它默认不接管任何用户流量**（`auto_route:false`，契约要求的安全属性），要"用它上网"仍需产品层显式把流量导进去。

> **中止说明（重要）**：执行期间 Lead 下达「用户报告新构建 TUN 模式无法上网，立即暂停并清理」。收到该指令时，本次 E2E 的**两轮完整流程已经跑完**（create → 观测 → 流量实测 → status → remove → 引擎退出 → 残留核验），因此**没有任何一步因中止而缺失**。唯一因中止未执行的是「重复创建 / 并发创建 / 创建中击杀 keeper」这类可选压力用例（见 §13）。收到指令后我只执行了只读检查与清理核验，未再创建任何适配器或进程，也未执行任何改动系统网络的命令。

---

## §0 结论速览

| # | 检查项 | 结果 | 关键证据 |
|---|--------|------|----------|
| 1 | 引擎 `serve` 可握手、进程已提权 | ✅ PASS | `hello.result.elevated = true`，pid 12600 / 20816 |
| 2 | `vnic.create` 被接受并进入 present | ✅ PASS | `{"accepted":true,"vnic":{"state":"present",...}}` |
| 3 | 真机出现适配器且由 Wintun 驱动承载 | ✅ PASS | `DriverProvider: WireGuard LLC` / `DriverDescription: Wintun Userspace Tunnel` / `DriverFileName: wintun.sys` / `DriverVersion: 0.14.0.0` |
| 4 | 地址 `10.66.1.1/24` 生效 | ✅ PASS | `Get-NetIPAddress`：`10.66.1.1 PrefixLength 24 PrefixOrigin Manual AddressState Preferred` |
| 5 | MTU 1420 真正生效 | ✅ PASS | `Get-NetIPInterface` IPv4 `NlMtu = 1420`；`netsh` → `70  5  1420  connected  HypoMux-VNIC-E2E` |
| 6 | 适配器自身可达 | ✅ PASS | `Test-Connection` 2/2；`ping 10.66.1.1` 2/2 `0% loss` |
| 7 | **真实承载流量**（源绑定出网并回包） | ✅ PASS | `ping -S 10.66.1.1 -n 2 8.8.8.8` → `Reply from 8.8.8.8: bytes=32 time<1ms TTL=62`，2/2、`0% loss` |
| 8 | `auto_route:false` 安全性：默认路由零变化 | ✅ PASS | 全文 `0.0.0.0/0` 仅存在于 vEthernet×11 与 以太网(8)，**不含** VNIC |
| 9 | `vnic.status` 与实际状态一致 | ✅ PASS | `{"state":"present","interface_name":"HypoMux-VNIC-E2E",...}` |
| 10 | `vnic.remove` 后零残留 | ✅ PASS | 适配器消失、`ifIndex` 路由 0 条、无 keeper 残留、引擎 `exit_code=0` |
| 11 | `adapter_guid` 有值 | ❌ 降级 | 线上 JSON 被 `omitempty` 整体省略 ⇒ 引擎未填充（已知降级，非阻塞） |
| 12 | 适配器自带 IPv4 DNS | ⚠️ 观察 | IPv4 `{}`（无），仅 Windows IPv6 占位 `fec0:0:0:ffff::{1,2,3}` |
| 13 | 收尾后整机干净 | ✅ PASS | 见 §9：无我的适配器/路由/进程/配置拷贝；`git status` 仅 `?? reports/` |

**是否阻塞"真实可用"结论：不阻塞。** 唯一未达标项是 `adapter_guid` 为空（功能降级，§10 D1），不影响创建、持有、流量与移除。

---

## §1 任务、隔离与硬约束

| 项目 | 值 |
|------|-----|
| 隔离适配器名 | `HypoMux-VNIC-E2E` |
| 隔离地址 | `10.66.1.1/24`，MTU 1420 |
| 配置目录 | `%TEMP%\vnic-e2e\`（`%LOCALAPPDATA%\Temp\vnic-e2e\`） |
| 严禁触碰 | `HypoMux-Tun`（ifIndex 51，用户的主 TUN）、`HypoMux-VNIC`（Lead）、`HypoMux-VNIC-Probe`（T9）、`HypoMux-VNIC-Edge`（T11） |
| 写入范围 | 仅本文件 `reports/vnic/21-engine-e2e.md` |
| 禁止操作 | `Remove-NetRoute`、`Set-DnsClientServerAddress`、任何写入型 `netsh`、`Restart-NetAdapter`、改系统代理 |

**声明**：全过程未执行上述任何被禁命令。所有观测命令均为 `Get-*` / `ping` / `tracert` / `netsh ... show`（只读）与引擎自身协议调用。

---

## §2 环境基线

### 2.1 权限与二进制

```
elevated = True            （<HOST>\<ADMIN>，High Mandatory Level）
$bin  = <repo>\desktop\bin
hypomux-engine.exe          8,482,304 B
sing-box.exe               81,947,136 B   （sing-box version 1.14.2 / Revision af6e64c3b69e6132ebaee0e1a3d24e93903f6709）
wintun.dll                    427,552 B
hypomux.exe / libcronet.dll / hypomux-amd64-installer.exe
```

### 2.2 基线进程与适配器（**全部不可触碰**）

创建前的只读快照（`e2e-transcript.txt:16-23`）：

```
ProcessId ParentProcessId Name               CommandLine
    13308             688 hypomux-engine.exe C:\ProgramData\HypoMux\Core\bin\hypomux-engine.exe service
     6240           13156 hypomux.exe        "C:\Program Files\HypoMux\hypomux.exe"
     6920            6240 hypomux-engine.exe "C:\Program Files\HypoMux\bin\hypomux-engine.exe"
    19216            6920 sing-box.exe       "C:\Program Files\HypoMux\bin\sing-box.exe" run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-254382414.json
     5444            4728 sing-box.exe       "...\desktop\bin\sing-box.exe" run -c ...\vnic-probe\sing-box-vnic-probe-gvisor.json
```

基线适配器：`HypoMux-VNIC-Probe`（ifIndex 68，T9 的探针）+ `HypoMux-Tun`（ifIndex 51，用户主 TUN）。

> **观测噪声说明**：T9 的探针适配器在两轮 E2E 之间被其 owner 创建/销毁，这解释了 §6.7 中 `ifIndex 68 ↔ 69` 成对出现的路由/DNS 差异（**不是**我的残留）。详见 §9。

---

## §3 方法与踩坑

### 3.1 调用方式

引擎以 `hypomux-engine.exe serve` 启动，走 **stdin/stdout JSONL 协议**（日志走 stderr）：

- 每行一个 JSON 请求，**无需先握手**（`handle()` 无 hello 门禁）。
- stdout 会混入非请求事件 `{"event":"log.record",...}`，**读响应必须按 `id` 字段过滤**（脚本用带超时的轮询队列 + `ConvertFrom-Json` 过滤）。
- **观测必须在引擎存活期间完成**：`engine/internal/server/server.go:97` 的 `defer s.stopProxyForHostExit()` 会在引擎退出时移除适配器。因此脚本在 create 之后**不关 stdin**，先跑完全部观测，再发 `vnic.remove`。

脚本（`%TEMP%\vnic-e2e\`）：`run-e2e.ps1`（第 1 轮）、`run-e2e2.ps1`（第 2 轮）；原始输出 `e2e-transcript.txt`（14,599 B）、`e2e-transcript-2.txt`（6,986 B）。

### 3.2 配置（与桌面层 `desktop/internal/services/vnic_config.go:64-87` 同形状）

```
cfg_path   = %LOCALAPPDATA%\Temp\vnic-e2e\sing-box-vnic.json
cfg_bytes  = 262
cfg_sha256 = 2de3d33aec7351b4fcf1473fd5ae0b60ff21dc4910f9bd220966b2e396debc2b
cfg_text   = {"log":{"level":"warn","timestamp":true},"inbounds":[{"type":"tun","tag":"tun-in","interface_name":"HypoMux-VNIC-E2E","address":["10.66.1.1/24"],"mtu":1420,"auto_route":false,"strict_route":false,"stack":"system"}],"outbounds":[{"type":"direct","tag":"direct"}]}
first3_bytes = 123,34,108   (无 BOM)
```

### 3.3 踩坑记录（写给后续复现者）

1. **PS 5.1 会按 ANSI 解码无 BOM 的 UTF-8 `.ps1`** ⇒ 含中文的脚本报 `字符串缺少终止符` / `表达式或语句中包含意外的标记` 等解析错误。修法：写脚本时用 `[IO.File]::WriteAllText($p,$txt,(New-Object System.Text.UTF8Encoding($true)))` 加 BOM，或脚本保持纯 ASCII（第 2 轮脚本即纯 ASCII）。
2. PowerShell 双引号字符串里 `\"` **不是**转义（反引号才是），早期一行 `'\"'` 造成 24 处级联解析错误。
3. 观测必须在引擎存活期间做（见 3.1）；否则会把"引擎退出即移除"误判为"适配器自己掉了"。

---

## §4 `engine.hello`（引擎提权与能力清单）

请求/响应原文（`e2e-transcript.txt:29-31`）：

```
REQ| {"protocol":1,"id":"r1","method":"engine.hello"}
OUT| {"protocol":1,"id":"r1","result":{"engine":"hypomux-engine","engine_version":"2.7.0",
     "commit":"37571e5a0531c16621cf4ac3e7511ac21135aa00","protocol_version":1,
     "transport":"stdio-jsonl",
     "capabilities":["engine.hello","engine.status","engine.start","engine.scheduling","engine.stop",
       "engine.telemetry","steam_cdn.configure","tun.activate","tun.status","tun.deactivate",
       "vnic.create","vnic.status","vnic.remove","dns.resolve","dns.status","health.check",
       "diagnostic.run","mtu.set","wfp.inspect","hotspot.inspect","host.shutdown"],
     "modes":["proxy","tun_tcp_pool"],
     "mode_features":{"proxy":[...],"tun_tcp_pool":["tcp_connect","udp_associate","ipv6_egress",
       "adaptive_health","managed_tun_lifecycle","dynamic_nic_channels"]},
     "os":"windows","arch":"amd64","pid":12600,"elevated":true,
     "started_at":"2026-10-03T09:46:42.2818616Z"}}
hello.result.elevated = True
```

要点：`elevated=true`（否则 `vnic.create` 必返 `elevation_required`，见 `engine/internal/server/server.go:820`）；能力清单 21 项且含全部三个 `vnic.*`。

---

## §5 `vnic.create`（请求原文 + 响应 + keeper 日志）

请求（`e2e-transcript.txt:35`，脚本序列化后字段顺序无关）：

```json
{"protocol":1,"id":"r2","method":"vnic.create","params":{
  "executable":"<repo>\\desktop\\bin\\sing-box.exe",
  "config_path":"%LOCALAPPDATA%\\Temp\\vnic-e2e\\sing-box-vnic.json",
  "config_sha256":"2de3d33aec7351b4fcf1473fd5ae0b60ff21dc4910f9bd220966b2e396debc2b",
  "startup_timeout_ms":20000,"interface_name":"HypoMux-VNIC-E2E",
  "address":"10.66.1.1","prefix_length":24,"mtu":1420}}
```

stdout 原文（含 keeper 日志与最终响应，`e2e-transcript.txt:35`）：

```
OUT| {"protocol":1,"sequence":1,"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] sing-box configuration check passed"}}
OUT| {"protocol":1,"sequence":2,"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] expecting adapter HypoMux-VNIC-E2E with IPv4 address 10.66.1.1"}}
OUT| {"protocol":1,"sequence":3,"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] sing-box process started (PID=13296), waiting for stable takeover"}}
OUT| {"protocol":1,"sequence":4,"event":"log.record","data":{"component":"vnic-keeper","message":"[TUN] sing-box is stable and owns TUN/WFP/routes"}}
OUT| {"protocol":1,"id":"r2","result":{"accepted":true,"vnic":{"state":"present","interface_name":"HypoMux-VNIC-E2E","address":"10.66.1.1","prefix_length":24,"mtu":1420,"created_at":"2026-10-03T09:46:43Z"}}}
```

- `create.result.accepted = True`，`create.result.vnic.state = present`
- **`adapter_guid` 与 `last_error` 在线上 JSON 中整体不出现** ⇒ 两者为空串（Go `omitempty`，见 `desktop/internal/engineclient/vnic.go:38,40`）
- `created_at` 存在：`2026-10-03T09:46:43Z`
- 第 2 轮 wall time：`create_wall_ms = 2295`（含 keeper 启动与稳定接管，未触及 60s 上限）

---

## §6 真机观测（引擎存活期间）

### 6.1 适配器（第 1 轮 ifIndex 69 / 第 2 轮 ifIndex 70）

第 1 轮（`e2e-transcript.txt:51-58`）：

```
Name                 : HypoMux-VNIC-E2E
InterfaceDescription : sing-tun Tunnel
Status               : Up
ifIndex              : 69
MacAddress           :
LinkSpeed            : 100 Gbps
AdminStatus          : Up
MediaConnectionState : Connected
```

第 2 轮补充驱动层证据（`e2e-transcript-2.txt`）：

```
InterfaceName        : iftype53_32771        ← Windows 内部接口名，正常
DriverProvider       : WireGuard LLC
DriverDescription    : Wintun Userspace Tunnel
DriverVersion        : 0.14.0.0
DriverDate           : 2021-10-13
DriverFileName       : wintun.sys
NdisVersion          : 6.83
Virtual              : True
```

⇒ **不是** loopback / 假接口，而是由已在仓库中钉哈希的 `wintun.dll` 安装的真实 Wintun 适配器。

### 6.2 地址（`e2e-transcript.txt:61-76`）

```
IPAddress      : 10.66.1.1
PrefixLength   : 24
AddressFamily  : IPv4
InterfaceIndex : 69
SuffixOrigin   : Manual
PrefixOrigin   : Manual
AddressState   : Preferred

IPAddress      : <LINKLOCAL_IPv6>%69   （IPv6 link-local，系统自动）
```

### 6.3 自身可达（`e2e-transcript.txt:78-92`）

```
Test-Connection 10.66.1.1 -Count 2  →  10.66.1.1  0ms  StatusCode 0（2/2）
ping -n 2 10.66.1.1                 →  Reply from 10.66.1.1: bytes=32 time<1ms TTL=64 ×2
                                       Packets: Sent = 2, Received = 2, Lost = 0 (0% loss)
```

### 6.4 MTU / 度量 / 状态（第 2 轮，`Get-NetIPInterface` + `netsh`）

```
ifIndex AddressFamily NlMtu InterfaceMetric ConnectionState     Dhcp     DadTransmits
     70          IPv6 65535               5       Connected Disabled            1
     70          IPv4  1420               5       Connected Disabled            0

netsh interface ipv4 show interfaces：
 Idx     Met         MTU          State                Name
 ---  ----------  ----------  ------------  ---------------------------
  51           0        1492  connected     HypoMux-Tun
  70           5        1420  connected     HypoMux-VNIC-E2E
```

⇒ 请求的 `mtu:1420` 真正落到网卡（不是只回显在 JSON 里）。

### 6.5 适配器自带路由（5 条接口作用域路由，无默认路由）

```
ifIndex DestinationPrefix  NextHop RouteMetric InterfaceAlias
     70 255.255.255.255/32 0.0.0.0         256 HypoMux-VNIC-E2E
     70 224.0.0.0/4        0.0.0.0         256 HypoMux-VNIC-E2E
     70 10.66.1.255/32     0.0.0.0         256 HypoMux-VNIC-E2E
     70 10.66.1.1/32       0.0.0.0         256 HypoMux-VNIC-E2E
     70 10.66.1.0/24       0.0.0.0         256 HypoMux-VNIC-E2E
```

### 6.6 适配器 DNS（`e2e-transcript.txt:104-107`）

```
InterfaceIndex InterfaceAlias   AddressFamily ServerAddresses
            69 HypoMux-VNIC-E2E             2 {}
            69 HypoMux-VNIC-E2E            23 {fec0:0:0:ffff::1, fec0:0:0:ffff::2, fec0:0:0:ffff::3}
```

⇒ 无 IPv4 DNS（`direct` outbound 不代理解析），仅系统给 IPv6 的占位值。见 §10 D2。

### 6.7 `auto_route:false` 安全性：默认路由零变化（`e2e-transcript.txt:142-156`）

创建后 `0.0.0.0/0` 的持有者：

```
ifIndex InterfaceAlias      NextHop      RouteMetric PolicyStore
     75 vEthernet (xuni-11) 192.168.16.<masked>           0
     71 vEthernet (xuni-10) 192.168.16.<masked>           0
     64 vEthernet (xuni-09) 192.168.16.<masked>           0
     60 vEthernet (xuni-08) 192.168.16.<masked>           0
     56 vEthernet (xuni-07) 192.168.16.<masked>           0
     52 vEthernet (xuni-06) 192.168.16.<masked>           0
     47 vEthernet (xuni-05) 192.168.16.<masked>           0
     43 vEthernet (xuni-04) 192.168.16.<masked>           0
     39 vEthernet (xuni-03) 192.168.16.<masked>           0
     33 vEthernet (xuni-02) 192.168.16.<masked>           0
     29 vEthernet (xuni-01) 192.168.16.<masked>           0
      8 以太网                 192.168.16.<masked>         500
```

**新建的 VNIC 不在其中** ⇒ `auto_route:false` 被真正遵守：创建虚拟网卡**不会**改变本机默认路由（安全属性成立，这是设计目标而非缺陷）。

### 6.8 keeper 进程（`e2e-transcript.txt:110-113` / 第 2 轮）

```
ProcessId ParentProcessId CommandLine
    13296           12600 <desktop\bin>\sing-box.exe run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-1422628591.json   （第 1 轮）
    18808           20816 <desktop\bin>\sing-box.exe run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-1483536318.json   （第 2 轮）
```

- keeper 是**独立子进程**，父进程 = 我起的那次引擎，命令行确认它跑的正是本次投递的配置副本。
- 注意：**引擎会把请求里的配置复制到 `C:\ProgramData\HypoMuxCoreRuntime\tun-config-<随机后缀>.json` 再交给 keeper**（所以命令行看不到我写的 `%TEMP%` 路径）；两份副本在 remove 后已被引擎清理（见 §9.4）。

---

## §7 流量实测（第 2 轮，决定性证据）

```
-- E1 ping -S 10.66.1.1 -n 2 8.8.8.8  （源绑定到虚拟网卡）--
Pinging 8.8.8.8 from 10.66.1.1 with 32 bytes of data:
Reply from 8.8.8.8: bytes=32 time<1ms TTL=62
Reply from 8.8.8.8: bytes=32 time<1ms TTL=62
    Packets: Sent = 2, Received = 2, Lost = 0 (0% loss),

-- E2 ping -S 10.66.1.1 -n 2 192.168.16.<masked>  （源绑定到 LAN 网关）--
Request timed out. ×2      → Packets: Sent = 2, Received = 0, Lost = 2 (100% loss),

-- E3 ping -n 2 8.8.8.8  （不绑定源，对照组）--
Reply from 8.8.8.8: bytes=32 time<1ms TTL=64
    Packets: Sent = 2, Received = 2, Lost = 0 (0% loss),

-- E4 tracert -d -h 3 -w 500 -S 10.66.1.1 8.8.8.8 --
10.66.1.1 is not a valid address.
```

判读：

| 用例 | 结果 | 判读 |
|------|------|------|
| E1 | 2/2 回包，TTL=62（比对照的 64 少 2 跳） | **流量确实经由 `10.66.1.1` 进入 TUN → keeper 的 sing-box → `direct` 出站 → 公网并原路返回**。TTL 差 2 与"多经一次用户态转发/一跳"一致，是真实承载的旁证。 |
| E2 | 100% loss | 预期：LAN 网关对 `10.66.1.1` 无回程路由。**不是缺陷**。 |
| E3 | 2/2，TTL=64 | 未绑定源的正常路径**未受虚拟网卡影响**（也说明 VNIC 没有偷偷改写默认路径）。 |
| E4 | 工具直接拒绝该地址 | `tracert -S` 不接受非本机主地址作为源（工具限制），**不是产品缺陷**。 |

---

## §8 `vnic.status` / `vnic.remove`（原文 + 与桌面端形状的一致性）

```
REQ| {"protocol":1,"id":"r3","method":"vnic.status"}
OUT| {"protocol":1,"id":"r3","result":{"state":"present","interface_name":"HypoMux-VNIC-E2E","address":"10.66.1.1","prefix_length":24,"mtu":1420,"created_at":"2026-10-03T09:47:23Z"}}

REQ| {"protocol":1,"id":"r4","method":"vnic.remove"}
OUT| {"protocol":1,"id":"r4","result":{"state":"absent","interface_name":"","address":"","prefix_length":0,"mtu":0}}
```

**形状差异（已核对，属设计一致，非缺陷）**：

- `vnic.create` 的结果是 `{"accepted":bool,"vnic":{...状态...}}`（外层包一层）；
- `vnic.status` / `vnic.remove` 的结果是**扁平状态对象**本身。

这与桌面端代码逐字一致：`desktop/internal/engineclient/vnic.go:43-47` 定义 `VNICCreateResult{Accepted bool \`json:"accepted"\`; VNIC VNICStatus \`json:"vnic"\`}`，而 `:63-66`（Status）与 `:74-77`（Remove）直接解码为 `VNICStatus`。字段标签（`state/interface_name/address/prefix_length/mtu/adapter_guid,omitempty/created_at,omitempty/last_error,omitempty`）与线上 JSON 完全对应。

> 第 1 轮脚本里打印的 `status.result.vnic.state = (空)` 是我脚本取了错误的路径（`result.vnic.state`）；原始 JSON 证明 `vnic.status` 返回 `state=present`，无需修复产品代码。

---

## §9 清理与残留核验

### 9.1 remove 之后（第 1 轮 `e2e-transcript.txt:170-180`）

```
9.1 Get-NetAdapter -Name HypoMux-VNIC-E2E      → 未找到（适配器已消失）✓
9.2 Get-NetIPAddress -InterfaceAlias ...       → 无 IP（接口已不存在）✓
9.3 残留 sing-box 进程                          → 只剩 19216（用户 app 的主 TUN keeper）
9.4 路由/DNS 是否回到基线                       → False（原因见 9.2）
```

`route_snapshot_back_to_baseline=False` 的**真实原因**（差异原文，`e2e-transcript.txt:119-137`）：进入 `=>` 的是 `69|…`（我的 VNIC，+5 条），离开 `<=` 的是 `68|10.67.0.0/24 …`（**T9 的 `HypoMux-VNIC-Probe`，-5 条**），DNS 差异同样是 `69|HypoMux-VNIC-E2E =>` 对 `68|HypoMux-VNIC-Probe <=`。路由总数 `164 → 164`。⇒ 属队友并发探针造成的成对抵消，**不是我的残留**。

### 9.2 remove 之后（第 2 轮，逐条断言）

```
J1 adapter gone = True
J2 routes left on ifIndex 70 = 0
J3 sing-box processes referencing e2e or tun-config =（空）
J4 all sing-box now: 只剩 19216（app 主 TUN）+ 5360（T9 探针 keeper）
```

### 9.3 引擎退出

```
engine_exited = True   exit_code = 0
elapsed_ms = 9   （第 1 轮） / 7   （第 2 轮）
engine stderr = 空
```

### 9.4 收到「立即清理」指令后的最终只读核验（`m00545`）

```
Q1 Get-NetAdapter | Name,Status,ifIndex
     以太网(8,Up) vEthernet×11(27..75,Up) HypoMux-Tun(51,Up)     ← 14 项，无任何 VNIC
Q2 MINE_GONE=True（无名为 HypoMux-VNIC-E2E 的适配器）
Q3 routes_on_69_70 = 0
Q4 sing-box.exe：仅 19216（"C:\Program Files\HypoMux\bin\sing-box.exe" run -c ...\tun-config-254382414.json）
Q5 hypomux-engine.exe：仅 13308（...\Core\bin\hypomux-engine.exe service）与 6920（"C:\Program Files\HypoMux\bin\hypomux-engine.exe"）
Q6 git status --porcelain → ?? reports/
```

运行时配置目录 `C:\ProgramData\HypoMuxCoreRuntime` 现状：仅 `tun-config-1388291783.json`(14:12) 与 `tun-config-254382414.json`(17:41，正被 19216 使用)——**我两轮产生的 `…-1422628591.json` / `…-1483536318.json` 已被引擎自行清理**，无需我处理。

**结论：我这两轮 E2E 没有留下任何适配器、路由、DNS 项、进程或配置副本。** 我没有执行任何改动系统网络的命令（未调用 `Remove-NetRoute` / `Set-DnsClientServerAddress` / 写型 `netsh` / `Restart-NetAdapter` / 代理设置）。

---

## §10 已知降级与边界（逐条定性）

| # | 现象 | 定性 | 说明 |
|---|------|------|------|
| D1 | `adapter_guid` 恒空（线上 JSON 直接省略该键） | **一般（功能降级）** | 引擎无"按接口名取 GUID"的 helper ⇒ `AdapterGUID` 恒为 `""`。影响：UI/日志无法显示或持久化适配器 GUID，无法按 GUID 精确定位适配器。不阻塞创建/持有/流量/移除。 |
| D2 | 适配器无 IPv4 DNS | 提示 | `direct` outbound 不做代理解析；VNIC 不是"给用户上网的网卡"，名称解析仍走主适配器。 |
| D3 | 默认路由零变化 | **设计如此（安全属性）** | `auto_route:false` + `strict_route:false` 是冻结契约要求；实测成立（§6.7）。 |
| D4 | IPv6 `NlMtu = 65535` | 提示 | 请求只约束 IPv4 MTU 1420；未设置 IPv6 MTU。 |
| D5 | `InterfaceName = iftype53_32771` | 正常 | Windows 内部接口名；`Name`/`InterfaceAlias` 才是 `HypoMux-VNIC-E2E`。 |
| D6 | `tracert -S 10.66.1.1` 报 `not a valid address` | 工具限制 | 非产品缺陷。 |
| D7 | 源绑定到 LAN 网关 100% loss | 预期 | 网关对 `10.66.1.1` 无回程。 |
| D8 | `LinkSpeed = 100 Gbps`、`MacAddress` 为空 | 正常 | TUN/Wintun 虚拟适配器的典型值。 |
| D9 | 适配器 DNS 出现 `fec0:0:0:ffff::{1,2,3}` | 正常 | Windows 对无 DNS 接口的 IPv6 占位值。 |

---

## §11 判定：这个适配器算不算「真实可用」

**答：算，但仅限"创建/持有/承载/移除"这条链路；它不是"插上就能上网"的网卡。**

判定依据（全部为真机原始输出，非推断）：

1. **真实存在**：Windows 看到的是一张 `Wintun Userspace Tunnel` 适配器（`wintun.sys` 0.14.0.0），`Status=Up`、`Virtual=True`，不是 loopback/虚拟占位。
2. **参数真实生效**：`10.66.1.1/24`（Manual）与 MTU 1420 在 `Get-NetIPAddress`、`Get-NetIPInterface` 与 `netsh` 三处一致。
3. **真的能承载 IP 流量**：源绑定 `10.66.1.1` 的 ICMP 到 `8.8.8.8` 收到回包（TTL 62，与对照路径 TTL 64 相差 2），说明报文确实进了 TUN、经 keeper 的 sing-box `direct` 出站并原路返回；不绑定源时普通路径不受影响。
4. **生命周期真实可控**：`vnic.status` 反映真实状态；`vnic.remove` 后适配器、5 条接口路由、keeper 进程一并消失，引擎 `exit_code=0`。
5. **安全边界被遵守**：创建/持有期间默认路由与系统 DNS 零变化。

**它"还差什么"（要成为用户可用的虚拟网卡）：**

- 默认**没有**任何流量经过它（`auto_route:false`，无默认路由）⇒ 需要产品层显式使用：把应用/进程绑定到该接口（如源地址绑定、按进程路由、或作为出口绑定），或由上层按需添加路由。目前未见该"使用侧"闭环（本轮只验证引擎层，未验证 UI/桌面如何把它用于实际业务）。
- 无 DNS、无 `adapter_guid`：前者要求上层自己做域名解析路径，后者限制可观测性与精确寻址。
- 本轮**未**验证 IPv6 经该适配器的可达性、也未验证 TCP/HTTPS 等真实业务流量（只做了 ICMP），见 §13。

---

## §12 与用户「TUN 模式无法上网」的关系（只读观察，供 Lead 定位）

以下均为故障发生期间的**只读**快照，不含推断性结论；两点假设已明确标注，**请勿当作已验证事实**。

**已观测事实**

1. 主 TUN 适配器 `HypoMux-Tun`（ifIndex 51）**存在且 Up**；其 keeper `sing-box.exe`(19216，父 6920) 正在运行 `C:\Program Files\HypoMux\bin\sing-box.exe run -c C:\ProgramData\HypoMuxCoreRuntime\tun-config-254382414.json`。
2. 系统当前 `0.0.0.0/0` 仅由 `vEthernet×11`（metric 0）与 `以太网`(8，metric 500) 持有；**`HypoMux-Tun`(51) 不持有任何默认路由**（§6.7 原始表）。
3. 我两轮 E2E 期间"不绑定源"的 `ping 8.8.8.8` 正常（TTL=64），即当时**普通路径**是通的。
4. 我的 E2E 产物已全部回滚（§9），因此**不可能**是我遗留的适配器/路由/进程造成该故障。

**待 Lead 判定的假设（我无法静态判定，未执行任何验证动作）**

- 假设 A（若本产品 TUN 模式依赖**路由接管**）：主 TUN 不持有 `0.0.0.0/0` 即为可疑点——用户态流量不会被送进 TUN。
- 假设 B（若依赖 **WFP 重定向**）：`engine.hello` 的 `tun_tcp_pool` 能力列表含 `managed_tun_lifecycle`、`dynamic_nic_channels`，说明产品可能是 WFP/驱动层接管而非路由接管，则"无默认路由"属正常设计，本观察与故障无关。
- 区分 A/B 的方法（本轮**未**执行，建议由有权改网络环境的 owner 进行）：调用只读的 `wfp.inspect` 能力，或在故障时对比 `Get-NetRoute`/WFP 过滤器状态；也可查看 `tun-config-254382414.json` 中是否含 `auto_route:true`。

---

## §13 未执行 / 无法静态验证项

1. **因中止未执行**：重复创建、并发 create、`creating` 中间态的 `vnic.status` 观测、创建中击杀 keeper 的恢复行为、压力下的 remove。
2. **未执行**：`wfp.inspect`（只读但会与引擎交互，故障期间为保持机器安静而跳过）。
3. **未验证**：UI 按钮 → 桌面服务 → 引擎的整链路（本轮只到引擎层；前端/桌面层为 task-12 的静态与单测验证）。
4. **未验证**：IPv6 经该适配器的出网可达性；TCP/HTTPS 等真实业务流量（仅 ICMP）。
5. **未验证**：重启/异常退出后的持久性（契约设计为不持久化，本轮只验证了正常 remove）。
6. **未执行**：`wails3 task windows:package`（本轮范围外，属 CI 打包步骤）。

---

## 附录 A · 原始产物（均在 `%TEMP%`，不在仓库内）

| 路径 | 内容 |
|------|------|
| `%LOCALAPPDATA%\Temp\vnic-e2e\e2e-transcript.txt`（14,599 B） | 第 1 轮完整原始输出（基线、hello、create、adapters/IP/ping/路由/DNS、status、remove、收尾） |
| `%LOCALAPPDATA%\Temp\vnic-e2e\e2e-transcript-2.txt`（6,986 B） | 第 2 轮完整原始输出（驱动信息、MTU/netsh、E1–E4 流量、J1–J4 残留核验） |
| `...\vnic-e2e\run-e2e.ps1` / `run-e2e2.ps1` | 两轮驱动脚本 |
| `...\vnic-e2e\sing-box-vnic.json`（262 B，sha256 `2de3d33a…debc2b`） | 投递给引擎的 VNIC 配置 |

## 附录 B · 可复现步骤（命令级纲要）

```powershell
$bin='<repo>\desktop\bin'
# 1) 写配置（UTF-8 无 BOM，内容见 §3.2）到 $env:TEMP\vnic-e2e\sing-box-vnic.json 并算 sha256
# 2) 进程重定向启动：$psi.FileName="$bin\hypomux-engine.exe"; $psi.Arguments='serve'; RedirectStdIn/Out/Err=$true
# 3) 逐行写 stdin，按 "id" 过滤读 stdout（Get-NetAdapter/Get-NetIPAddress/Get-NetRoute/Get-DnsClientServerAddress/
#    Get-NetIPInterface/netsh ... show/ping -S/Test-Connection 均在引擎存活时执行）
# 4) 请求序列：engine.hello → vnic.create →（观测）→ vnic.status → vnic.remove →（残留核验）→ 关闭 stdin
```

报告结束。
