# 60 · Hyper-V 宿主 vNIC 命令面侦察（只读）

- 任务来源：Team Lead 消息 m00259 + 更正/重做消息 m00324
- 侦察对象：本机 Hyper-V 宿主侧（ManagementOS）虚拟网卡的命令面、对象模型、权限与降级路径
- 目标产品行为：在既有**外部交换机** `XuniUplink` 上批量创建宿主 vNIC，每张一个唯一（本地管理）MAC，由局域网路由器按 MAC 分别 DHCP 派发 IP
- 报告性质：**设计/侦察**。本报告不修改任何代码，不执行任何写操作
- 证据类型标记：
  - **【实测】** 本机只读命令的真实输出（可复现命令见附录 A）
  - **【参考真机】** 参考项目 `xuni-network` 的脚本与其真机运行记录（`reports/task-43-remove-nic-ghost-fix.md`）
  - **【推断】** 由实测事实推导，或来自本机未能读取到的官方文档语义
  - **【未验证】** 本环境无法验证（无公网、无 Go、禁止写操作）

> **本轮执行的命令边界（合规声明）**：全部为 `Get-*` / `Get-Help` / `Get-Command` / `Get-CimInstance` / `Get-PnpDevice` 与 `Add-VMNetworkAdapter -WhatIf`。
> **未执行**任何 `Add-VMNetworkAdapter`（无 -WhatIf）、`Remove-VMNetworkAdapter`、`New-/Remove-VMSwitch`、`Enable-WindowsOptionalFeature`、服务启停、路由/代理/网卡配置改动。未触碰 `以太网`（ifIndex 8）、`HypoMux-Tun`（ifIndex 51）、系统代理与路由表。
> `Add-VMNetworkAdapter -WhatIf` 的前后计数校验：`(Get-VMNetworkAdapter -ManagementOS).Count` 均为 **14**，无 `HypoMux-probe-01` 之类对象产生。

---

## 0. 结论速览

| # | 问题 | 结论 | 证据 |
|---|---|---|---|
| 1 | 创建命令 | **`Add-VMNetworkAdapter -ManagementOS -SwitchName -Name -StaticMacAddress -PassThru`**；不存在 `New-VMNetworkAdapter` | 【实测】`Get-Command New-VMNetworkAdapter` 解析失败、【参考真机】`New-XuniNic.ps1:226-227` |
| 2 | `-StaticMacAddress` 格式 | 接受 MAC 字符串；真机可用形式为**小写冒号** `02:1a:2b:00:00:01`（参考项目），Hyper-V **读回**形式为**裸 12 位大写十六进制** `<MAC>` | 【参考真机】+【实测】读回值 |
| 3 | 格式/重名校验 | **`-WhatIf` 不做 MAC 格式、MAC 冲突、重名校验**（9 个用例零报错）→ 不能用它做参数校验测试 | 【实测】 |
| 4 | 持久化参数 | `Add-VMNetworkAdapter` **没有 `-Persistent`**（也没有 `-SwitchType`） | 【实测】`Get-Command` |
| 5 | 稳定唯一键 | 主键用 **`DeviceId`**（GUID，唯一且等于主机侧 `Get-NetAdapter.InterfaceGuid`）；**`Name` 与 `Name+SwitchName` 都不可靠**（实测同名两条） | 【实测】 |
| 6 | 幽灵/残留记录 | `MacAddress`+`DeviceId`+`AdapterId` **三空**但 `Status={Ok}` 的记录真实存在（`xuni-01` 幽灵、`XuniUplink` 管理端口），**按名字删会同时命中** | 【实测】+【参考真机】`task-43:22-28` |
| 7 | 删除正确姿势 | 必须把 **`DeviceId` 非空的实体对象管道给 `Remove-VMNetworkAdapter`**（`ResourceObject` 参数集），**不能** `-ManagementOS -Name` | 【实测】参数集 +【参考真机】 |
| 8 | 权限 | **本地 <ADMIN_GROUP>（高完整性）足够**；本机 `Hyper-V <ADMIN_GROUP>` 组**为空**而全部 Hyper-V cmdlet 可用 | 【实测】 |
| 9 | DHCP 等待 | 参考实现**只等网卡出现、不等 IP**；HypoMux 需补一轮 `Get-NetIPAddress` 轮询（建议 500 ms / 上限 45 s，整体 60 s） | 【参考真机】`New-XuniNic.ps1:254-266` |
| 10 | 降级 | 无 Hyper-V 模块 / 功能未启用 / 无外部交换机 / 上行非 Up→Connected 四道前置检查，全部只读可测 | 【实测】+【推断】 |
| 11 | 提权路径 | 建议把 Hyper-V 操作放在**已提权的 Core（engine）**内执行（桌面进程 `asInvoker`），复用既有 `vnic.*` RPC，不新增 UAC 通道 | 【实测】manifest:18 + `privileged_windows.go` 现网机制 |
| 12 | 默认路由副作用 | **每新建一张 vNIC 就多一条 `0.0.0.0/0`（RouteMetric 0、InterfaceMetric 35）**，而物理网卡 RouteMetric 500 → 会改变主机出口 | 【实测】 |

---

## 1. 本机环境与基线

### 1.1 环境事实【实测】

| 项 | 值 |
|---|---|
| OS | Microsoft Windows 10 专业工作站版 10.0.19045（64 位） |
| Hyper-V 功能 | `Microsoft-Hyper-V-All` = **Enabled** |
| 服务 | `vmms` = Running / Automatic（`C:\Windows\system32\vmms.exe`）；`Dhcp` = Running / Automatic（显示名 DHCP Client）；`nvagent` = Running / Manual |
| PowerShell | 5.1.19041.5129（`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`） |
| Hyper-V 模块 | 2.0.0.0 |
| 会话权限 | `Mandatory Label\High Mandatory Level`，`IsInRole(Administrator)=True`，身份 `<HOST>\<ADMIN>` |
| 组成员 | 只有 `BUILTIN\<ADMIN_GROUP>`（S-1-5-32-544）；`Get-LocalGroupMember -Group 'Hyper-V <ADMIN_GROUP>'`（S-1-5-32-578）**返回空** |

复现：
```powershell
$PSVersionTable.PSVersion; (Get-Module -ListAvailable Hyper-V | Select -First 1).Version
Get-Service Dhcp,vmms,nvagent | Select Name,Status,StartType
Get-LocalGroupMember -Group 'Hyper-V <ADMIN_GROUP>'
```

### 1.2 虚拟交换机（全量 2 个）【实测】

| Name | SwitchType | AllowManagementOS | 上行 | Id |
|---|---|---|---|---|
| `Default Switch` | Internal | True | 无（`NetAdapterInterfaceGuid.Count=0`） | `<GUID>` |
| `XuniUplink` | **External** | True | `Realtek Gaming 2.5GbE Family Controller`，GUID `{<GUID>}` | `<GUID>` |

`XuniUplink` 的其它字段：`Extensions = {Microsoft Windows 筛选平台(Filter,Enabled=False), Microsoft Azure VFP Switch Extension(Forwarding,False), Microsoft NDIS 捕获(Monitoring,True)}`、`IovSupport=False`、`BandwidthReservationMode=Absolute`、`DefaultFlowMinimumBandwidthAbsolute=250000000`、`BandwidthPercentage=10`、`EmbeddedTeamingEnabled=False`。

> **只有 External 交换机才能承载上行**。`Default Switch` 是 Internal（NAT 型），不能用来拿局域网 IP。【推断/文档语义】

### 1.3 `Get-VMNetworkAdapter -ManagementOS` 全景（14 条）【实测】

| Name | MacAddress | DeviceId | 形态 |
|---|---|---|---|
| `Container NIC 9d844023` | `<MAC>` | `{<GUID>}` | Default Switch 的正常实体 |
| `XuniUplink`（交换机管理端口） | *空* | *空* | **ghost**（见 1.5） |
| `xuni-01` | *空* | *空* | **ghost**（见 1.5） |
| `xuni-01` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-02` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-03` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-04` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-05` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-06` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-07` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-08` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-09` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-10` | `<MAC>` | `{<GUID>}` | 实体 |
| `xuni-11` | `<MAC>` | `{<GUID>}` | 实体 |

计数：`ManagementOS` 合计 **14**，其中挂在 `XuniUplink` 上的 **13**；主机侧 `vEthernet (…)` 适配器 **12** 个（= Default Switch 1 + xuni-01..11）。

MAC 位分析【实测】：11 张实体 MAC 的首字节 `& 0x01 = 0`（单播）且 `& 0x02 = 0x02`（**本地管理地址 LAA**），即符合随机 LAA 规范；且全部**不在** Hyper-V 动态池内（见 1.4）。

### 1.4 MAC 池与"静态 MAC"【实测】

- `Get-VMHost`：`MacAddressMinimum=00155D109000`、`MacAddressMaximum=00155D1090FF`（仅 256 个，`00:15:5D:10:90:xx`）。
- 注册表 `HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization`：`MinimumMacAddress`/`MaximumMacAddress`（二进制）、`CurrentVmVersion=9.2`、`CompatibleVmVersion=4.0`、`Version=10.0.19041.1`。
- 11 张 xuni 的 MAC 无一落在该池中 → 它们是 **`-StaticMacAddress` 写入**的，不是 Hyper-V 动态分配。【推断，证据充分】

> **结论**：批量创建必须显式给 `-StaticMacAddress`，不要依赖动态池（256 个上限、`00:15:5D` 前缀固定、且会与其它 VM 抢池）。

### 1.5 幽灵记录（ghost）：本机两类实例【实测】

**ghost 定义**：`MacAddress`、`DeviceId`、`AdapterId` **三个字段全空**，但 `Status={Ok}`、`IsDeleted=False`、`IsManagementOs=True`，其余字段看起来完全正常。【参考真机】`task-43:16-20`

本机实测到两条：

1. `Name='xuni-01'`（幽灵）—— 与真实的 `xuni-01` **同名**。
2. `Name='XuniUplink'`（外部交换机的宿主管理端口）—— 与交换机同名。

关键佐证：
- `Get-PnpDevice -Class Net` 中 `ROOT\VMS_MP\0000..0011` 共 **12** 个实例、全部 `Status=OK` → **幽灵没有任何 PnP 设备**（Windows 网络栈中不存在对应网卡）。
- 主机侧 `Get-NetAdapter` 里既无幽灵 `xuni-01` 的第二张卡，也**没有 `vEthernet (XuniUplink)`**（详见 1.6）。
- 但 WMI `Msvm_EthernetPortAllocationSettingData` 中**存在**两条 `ElementName='xuni-01'` 的分配记录（`InstanceID` 分别为 `Microsoft:D7AA1705-…\<GUID>` 与 `…\<GUID>`）→ 幽灵是"**有配置/端口分配、无设备实体**"的残留。

**为什么会产生**：无法在本机复现（禁止写操作）。参考项目记录：删除/创建异常路径会留下这类记录，且**按名字删除会同时命中真卡与残留**，整条命令报「删除虚拟以太网交换机连接时失败。」，回滚删不掉，留下永远清不掉的半成品。【参考真机】`task-43:22-28`

> **对 HypoMux 的硬性含义**：
> 1. 任何按 `Name` 定位/删除的动作都不可用。
> 2. 归属判定与删除必须以 **`DeviceId` 非空**为"这是真卡"的前置条件。
> 3. 幽灵**没有清理路径**（彻底清需重建交换机或重启 VMMS，超出应用范围）—— HypoMux 必须能"忽略幽灵并继续工作"。

### 1.6 上行链路解析，以及"为什么没有 `vEthernet (XuniUplink)`"【实测 + 推断】

恢复出正确上行的方法（**注意 GUID 数组陷阱**）：
```powershell
$sw = Get-VMSwitch -Name 'XuniUplink'
$g  = [guid]($sw.NetAdapterInterfaceGuid | Select-Object -First 1)   # 关键：先取第一个元素
Get-NetAdapter | Where-Object { [guid]$_.InterfaceGuid -eq $g }
```
- `NetAdapterInterfaceGuid` 是 **`System.Guid[]`（长度 1）**，而 `Get-NetAdapter.InterfaceGuid` 是**带花括号的字符串**。直接 `[guid]$_.InterfaceGuid -eq [guid]$sw.NetAdapterInterfaceGuid`（后者是数组）**匹配不到任何东西** → 静默得到"上行不存在"的假结论。
- 正确解析结果：上行 = `以太网`，`InterfaceGuid={<GUID>}`、`ifIndex=8`、`Status=Up`、`MediaConnectionState=Connected`、`LinkSpeed=100 Mbps`、驱动 `rt640x64.sys`、IP `192.168.16.<masked>/24`（`PrefixOrigin=Dhcp`、`AddressState=Preferred`）。
- `Get-NetAdapterBinding -Name '以太网'`：`vms_pp`（Hyper-V 可扩展虚拟交换机）**Enabled=True**，`ms_tcpip` **Enabled=True** → 物理网卡既绑在 vSwitch 上、又仍持有 IPv4。

**`vEthernet (XuniUplink)` 不存在的解释【推断，多重实测支撑】**：
- `Get-NetAdapter -IncludeHidden | ? Name -like '*XuniUplink*'` 只有 1 个 `vSwitch (XuniUplink)`（ifIndex 34、`vmswitch.sys`、MAC 空），**没有任何 `vEthernet (XuniUplink)`**。
- 该交换机的 ManagementOS 管理端口记录就是 1.5 描述的第二条 **ghost**（三字段全空）。
- 因此合理解释是：该外部交换机的宿主管理端口**设备实体不存在**（残留配置形态），主机出网目前实际仍走物理 `以太网` 自带的 IP。这与"物理网卡保留 IP"的实测一致。
- **但这不影响在 `XuniUplink` 上新建 vNIC**：11 张 xuni 卡正是经该交换机从 `192.168.16.<masked>` 拿到 DHCP 租约（见 1.7），**说明该交换机的上行转发链路是有效的**。

**上行有效性的可靠前置检查（建议实现）**：
```powershell
$sw = Get-VMSwitch -Name $switchName -ErrorAction Stop
if ($sw.SwitchType -ne 'External') { <SWITCH_NOT_SUPPORTED> }
if (-not $sw.AllowManagementOS)    { <SWITCH_NOT_SUPPORTED> }
$g = [guid]($sw.NetAdapterInterfaceGuid | Select-Object -First 1)
$up = Get-NetAdapter -IncludeHidden | Where-Object { [guid]$_.InterfaceGuid -eq $g } | Select-Object -First 1
if (-not $up) { <UPLINK_MISSING> }
if ($up.Status -ne 'Up' -or $up.MediaConnectionState -ne 'Connected') { <UPLINK_DOWN> }
```
补充建议：**不要**要求 `vEthernet (<交换机名>)` 存在（本机就没有），**不要**要求交换机管理端口记录非 ghost。

### 1.7 DHCP / 路由 / 租约现状【实测】

- 11 张 `vEthernet (xuni-0x)` 全部：`Get-NetIPInterface -AddressFamily IPv4` → `Dhcp=Enabled`、`ConnectionState=Connected`、`InterfaceMetric=35`（AutomaticMetric）、`NlMtu=1500`。
- `Get-NetIPAddress` → `PrefixOrigin=Dhcp`、`SuffixOrigin=Dhcp`、`AddressState=Preferred`、`/24`；IP = `192.168.16.<masked>/<masked>/<masked>/<masked>/<masked>/<masked>/<masked>/<masked>/<masked>/<masked>/<masked>`；网关 `192.168.16.<masked>`；DHCP 下发 DNS = `192.168.16.<masked>, 8.8.8.8, 114.114.114.114`。
- **无任何 169.254 APIPA 地址，无 `AddressState ≠ Preferred` 的地址**。
- `Win32_NetworkAdapterConfiguration`：`DHCPEnabled=True`、`DHCPServer=192.168.16.<masked>`、`IPConnectionMetric=35`，租约**恰好 1 小时**（9 张 20:08:54→21:08:54，xuni-10/11 20:28:45→21:28:45）。
- **默认路由副作用**：11 张 vEthernet **各有一条** `0.0.0.0/0 → 192.168.16.<masked>`（`RouteMetric=0`、`InterfaceMetric=35`、`Protocol=NetMgmt`），而物理 `以太网` 的 `RouteMetric=500`。
- 主机侧 `vEthernet (xuni-0x)` 均为 `Status=Up`、`Hidden=False`、驱动 `VmsProxyHNic.sys`、`LinkSpeed=100 Mbps`。
- DHCP 客户端日志：`Microsoft-Windows-Dhcp-Client/Operational` **默认未启用**（`IsEnabled=False`）；`/Admin` 只有 1 条历史事件（Id=1002、Level=错误）：`网络地址是 0x<HWID> 的网卡的 IP 地址租约 192.168.11.<masked> 被 DHCP 服务器 192.168.11.<masked> 拒绝(DHCP 服务器发送了 DHCPNACK 消息)。`

### 1.8 注册表与 PnP 残留【实测】

- `HKLM:\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces` 共 **18** 个键，其中 14 个带 DHCP 信息（每张 xuni 的 `DeviceId` GUID 都对应一个键）。
- **3 个孤儿键**：`<GUID>`、`<GUID>`（`sing-tun`，属 HypoMux TUN 时代），以及 `<GUID>`（网卡已消失，`EnableDHCP=1`、`DhcpIPAddress=192.168.11.<masked>`、`DhcpServer=192.168.11.<masked>`）→ **删除网卡不会清掉它的 TCP/IP 接口键与陈旧租约**。
- `Get-PnpDevice -Class Net`：`ROOT\VMS_MP\0000..0011` 共 12 个、全 OK；非 OK 的 Net 设备只有 2 个 `sing-tun Tunnel`（`SWD\WINTUN\…`）与 1 个 `Remote NDIS`（Unknown），与 vNIC 无关。
- PnP 实例号与创建时间（用于估计创建耗时）：`0001` 14:00:19、`0002` 14:00:20、`0003` install 14:00:20/arrival 14:00:23、`0004` 14:00:23→14:00:26、`0005` 14:00:26→14:00:46（+20 s）、`0006/0007` 14:04:35、`0008` 14:04:36→14:04:39、`0009` 14:05:28、`0010/0011` 16:58:46 → **热路径单张约 3 s，个别到 20 s**。

### 1.9 交互键总表【实测】

| 键 | 作用域 | 唯一性 | 与主机网络栈的关系 |
|---|---|---|---|
| `Name` | Hyper-V 侧 | **不唯一**（实测同名 2 条） | 主机侧别名是 `vEthernet (<Name>)`，不是 `<Name>` |
| `Name` + `SwitchName` | Hyper-V 侧 | **仍不唯一**（实测 `-Name xuni-01 -SwitchName XuniUplink` 返回 2 条） | — |
| **`DeviceId`** | Hyper-V 侧 | 唯一 GUID（ghost 为空） | **等于 `Get-NetAdapter.InterfaceGuid`**（`{…}` 花括号形式） |
| `AdapterId` | Hyper-V 侧 | 唯一 GUID（ghost 为空） | 主机网络栈**不暴露** |
| `MacAddress` | 两侧可见 | 真卡唯一；ghost 为空 | `Get-NetAdapter.MacAddress` 为 `XX-XX-XX-XX-XX-XX`（大写短横） |
| `Id` / `InstanceID` | Hyper-V/WMI | 唯一 | `Microsoft:<SwitchId>\<AllocationGuid>`，与 `Msvm_EthernetPortAllocationSettingData.InstanceID` 一致 |
| `SwitchId` | Hyper-V 侧 | 交换机 Id（小写无花括号） | `Get-VMSwitch.Id` |
| `Status` | Hyper-V 侧 | **`{Ok}` 不代表可用**（ghost 也报 `{Ok}`） | — |
| `IsDeleted` | Hyper-V 侧 | ghost 亦为 `False` | — |

`Get-NetAdapter.InterfaceGuid` ↔ Hyper-V `DeviceId` 的实例（实测）：`vEthernet (xuni-01)` = `{<GUID>}` = xuni-01 实体的 `DeviceId`；PnP 侧 `ROOT\VMS_MP\0001`；`vEthernet (Default Switch)` = `{A696632E-…}` = `Container NIC` 的 `DeviceId`，`ROOT\VMS_MP\0000`。

---

## 2. 创建：`Add-VMNetworkAdapter`

### 2.1 参数集与完整参数面【实测】

`Get-Command Add-VMNetworkAdapter` 三套参数集：

| 参数集 | 必需参数 | 用途 |
|---|---|---|
| `VMName` | `-VMName` | 给某台 VM 加网卡 |
| **`ManagementOS`** | **无必需参数** | **给宿主管理操作系统加 vNIC ← 本任务用** |
| `VMObject` | `-VM` | 通过 VM 对象管道 |

`ManagementOS` 参数集可用参数（全量）：

```
CimSession, ComputerName, Confirm, Credential, Debug, DeviceNaming, DynamicMacAddress,
ErrorAction, ErrorVariable, InformationAction, InformationVariable, IsLegacy, ManagementOS,
Name, NumaAwarePlacement, OutBuffer, OutVariable, Passthru, PipelineVariable, ResourcePoolName,
StaticMacAddress, SwitchName, Verbose, VM, VMName, WarningAction, WarningVariable, WhatIf
```

关键类型与语义【实测】：

| 参数 | 类型 | 语义/约束 |
|---|---|---|
| `-ManagementOS` | SwitchParameter | 标记"创建到宿主管理操作系统"；本参数集下**不强制**其它参数 |
| `-SwitchName` | String（ValidateNotNullOrEmpty） | 目标虚拟交换机名；不传的后果**未验证**（推断：创建为未连接适配器/默认交换机） |
| `-Name` | String（ValidateNotNullOrEmpty，有别名） | vNIC 名；**允许与既有记录重名**（实测幽灵先例） |
| **`-StaticMacAddress`** | **String**（ValidateNotNullOrEmpty） | 静态 MAC；格式见 2.2 |
| `-DynamicMacAddress` | SwitchParameter | 与 `-StaticMacAddress` 互斥的语义选项（由 Hyper-V 从 1.4 的池分配） |
| `-Passthru` | SwitchParameter | **返回适配器对象**；否则无输出 → 必须加，否则拿不到 `DeviceId` |
| `-DeviceNaming` | `OnOffState` | 设备命名（来宾侧网卡命名，本任务无关） |
| `-IsLegacy` / `-NumaAwarePlacement` / `-ResourcePoolName` | — | 存在，本任务无关 |
| `-WhatIf` / `-Confirm` | SupportsShouldProcess | 支持，但**不做语义校验**（2.4） |

**没有 `-Persistent`、没有 `-SwitchType`、没有 `MacAddressSpoofing/DhcpGuard/RouterGuard`**（后三者属于 `Set-VMNetworkAdapter`）。【实测】

`Set-VMNetworkAdapter` 补充：有 `StaticMacAddress`、`DynamicMacAddress`、`MacAddressSpoofing`、`DeviceNaming`、`DhcpGuard`、`RouterGuard`、`Name`、`ManagementOS`；**没有 `SwitchName`，也没有 `NewName`**（改名用 `Rename-VMNetworkAdapter`）。【实测】

推荐调用形式（参考项目真机验证过）：
```powershell
$adapter = Add-VMNetworkAdapter -ManagementOS `
    -SwitchName $switchName -Name $name `
    -StaticMacAddress $mac -Passthru -ErrorAction Stop
```
【参考真机】`xuni-network/artifacts/payload/resources/scripts/New-XuniNic.ps1:226-227`

### 2.2 `-StaticMacAddress` 的格式约束

| 事实 | 证据 |
|---|---|
| 参数类型是 `System.String`，仅校验非空 | 【实测】`Get-Command` |
| **真机可用形式：小写冒号 `02:1a:2b:00:00:01`**（参考实现固定用此形式） | 【参考真机】`New-XuniNic.ps1:226-227` + `lib\XVNic.Common.psm1:1908-1924 Format-XVMacRaw` |
| **Hyper-V 读回形式：裸 12 位大写十六进制**（如 `<MAC>`、`021A2B000011`） | 【实测】+【参考真机】`task-43:30-36` |
| `XX-XX-XX-XX-XX-XX` 短横形式 | 【推断】官方文档语义；**本机未验证** |
| 纯 `XXXXXXXXXXXX` / `XX:XX:…` / `XX-XX-…` / 点分形式 | 【参考真机】`MacAddress.psm1` 的解析器全部接受（`ConvertTo-XVNicMacBytes`）；其中**冒号形式已真机验证**，其余**本机未验证** |
| **大小写**：不敏感（读回恒为大写） | 【推断，基于读回值】 |
| **位约束**：首字节 `bit0=0`（单播）、`bit1=1`（本地管理）；全 0、全 F 非法 | 【参考真机】`MacAddress.psm1:134-136,164-212`（参考项目对 MAC 做前置校验，因为 Hyper-V 侧不报错） |
| Hyper-V 是否自身强校验格式/冲突 | **未验证**（`-WhatIf` 不校验，见 2.4） |

**HypoMux 侧建议**：
1. 自己实现解析/规范化，**在调用前**用与参考项目同构的规则校验（可解析 6 字节、单播、本地管理、非全 0/全 F、组内唯一、与系统现有 MAC 不冲突）。
2. 传给 `-StaticMacAddress` 时统一使用**短横大写** `XX-XX-XX-XX-XX-XX`（与 Hyper-V 读回格式一致，便于用户比对）；如遇真机拒绝，退回"小写冒号"（参考项目已证明可用）。
3. **创建后必须回读核对**：`(Get-VMNetworkAdapter -ManagementOS | ? DeviceId -eq $dev).MacAddress` 必须等于期望值（去掉分隔符、大写比较），不等则回滚并报 `ADAPTER_MAC_MISMATCH`。这与参考实现 `New-XuniNic.ps1:280-293` 的做法一致。

### 2.3 随机 / 动态 MAC

- `-DynamicMacAddress`：由 Hyper-V 从 1.4 的池 `00:15:5D:10:90:00–FF`（**仅 256 个**）分配 → **不适用于本任务**（多开即耗尽、前缀固定、路由器侧策略不可控）。【实测池 + 推断】
- 不指定任何 MAC：**未验证**本机行为（推断为等价于动态分配）。`-WhatIf` 用例中"不指定 MAC"零报错，不能作为证据。
- 本机 11 张卡均为静态 LAA（1.4/1.3 实测）→ 证明"静态 LAA + 外部交换机 + 路由器 DHCP"这条链路真机可用。

### 2.4 `-WhatIf` 的真实行为（重要陷阱）【实测】

9 个 `Add-VMNetworkAdapter -ManagementOS … -WhatIf` 用例：
`02-11-22-33-44-55` / `021122334455` / `02:11:22:33:44:55` / `02-aa-bb-cc-dd-ee` / `ZZ`（非法） / `01-11-22-33-44-55`（组播位） / 已被占用的 `<MAC>` / 不指定 MAC / 重名 `xuni-02`

→ **全部零输出、零报错**；前后 `(Get-VMNetworkAdapter -ManagementOS).Count` 均为 14；无 `HypoMux-probe-01` 对象。

结论：
- `-WhatIf` **被遵守**（不产生对象），可以安全用于"调用路径"预检；
- 但它在**参数解析完成后即短路**，**不做** MAC 格式校验、MAC 冲突校验、重名校验；
- `"What if"` 文本走 PowerShell **host 流**：`2>&1`、`3>&1`、`4>&1`、`6>&1`、`Out-String` 都抓不到（只有真实调用时由 host 打印，如 `task-43:22-28` 的重复两行）。
- **含义**：不能用 `-WhatIf` 写"参数校验测试"；MAC/重名规则必须由我们自己的代码保证。

### 2.5 创建耗时与并发

- 单张创建热路径约 **3 s**，个别到 **20 s**（1.8 PnP 时间线）。【实测】
- 参考实现创建后只等 **10 s / 500 ms** 网卡出现（`New-XuniNic.ps1:254-266`），主要成本在 `Get-NetAdapter` 轮询。【参考真机】
- 建议 HypoMux：单张超时 30 s；整体 `vnic.create` RPC 60 s（与 `00-frozen-interface.md` §3 一致）。
- 并发：Hyper-V 侧无并发保护，且我们的状态文件需要读改写 → **必须串行化**（见 3.4）。参考实现实测过 5 个并发 runspace 只剩 1 条记录的锁事故（`XVNic.Common.psm1:805-874` 注释）。

---

## 3. 归属判定与唯一标识

### 3.1 唯一键评估

| 候选键 | 可靠性 | 判据 |
|---|---|---|
| `Name` | ✗ | 实测幽灵与真卡同名 |
| `Name`+`SwitchName` | ✗ | 实测仍返回 2 条 |
| `MacAddress` | 真卡唯一，ghost 为空 | 可作**归属校验**，不能作主键 |
| **`DeviceId`** | ✓ 唯一 | 真卡必有；等于主机侧 `InterfaceGuid`；可跨 Hyper-V / NDIS 关联 |
| `AdapterId` | ✓ 唯一 | 真卡必有，但主机网络栈不暴露，需额外映射 |
| `Id`/`InstanceID` | ✓ 唯一 | `Microsoft:<SwitchId>\<Guid>`，WMI 层一致 |
| `Status={Ok}` | ✗ | ghost 也报 `{Ok}` |

### 3.2 推荐判定算法

```
真卡判定   : DeviceId 非空 且 AdapterId 非空 且 MacAddress 非空          → entity
信息不全   : DeviceId 空 且 MacAddress 非空                              → degraded（跳过+告警）
幽灵       : DeviceId 空 且 MacAddress 空                                → ghost（忽略，绝不当目标）
HypoMux 卡 : 满足 entity 且 Name 以 'HypoMux-vnic-' 开头 且 (Name, MAC) 出现在本应用状态文件中
删除目标   : 仅"本应用状态文件中记录过"的卡（对象管道传入 Remove-VMNetworkAdapter）
```
主机侧关联：`Get-NetAdapter | ? { [guid]$_.InterfaceGuid -eq [guid]$deviceId }`（注意 `DeviceId` 带花括号、大小写不敏感）。

### 3.3 绝不误删用户 `xuni-*` 的安全设计（对齐参考项目）

参考实现的安全策略（`Remove-XuniNic.ps1:22-27,138-145`）：
- **只有出现在 `state.json` 里的名字/MAC 才可删**；不在记录里的 → 输出 `stage=skipped, reason=not-managed-by-xuninet` 并 **exit 0**；
- 系统上已查不到 → `stage=already-gone`，仅清登记；
- 删除失败 → **不清登记**（否则重试退化成"空转 exit 0"的假成功）。

HypoMux 应逐条对齐。

### 3.4 状态文件设计（HypoMux 侧）

- **位置**：`%HYPOMUX_DATA_DIR%\vnic-hyperv.json`。`HYPOMUX_DATA_DIR` 已存在（`desktop/internal/services/settings.go:137`，未设时 `%USERPROFILE%\.hypomux`），并且测试普遍用 `t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())` → 天然可测。
- **不要塞进 `settings.json`**：settings 有迁移/备份/写放大逻辑（`settings.go:194,761-790`），混入运行时设备记录会污染用户配置与迁移路径。
- **落盘原语**：复用 `desktop/internal/services/atomic_file.go:9 atomicWriteFile(path, data, permission)`（MkdirAll + 临时文件 + `os.Rename`），语义与 `blocked_domains.go:207-218`、`nat_servers.go:270-281`、`tun_config.go:278-289`、`vnic_config.go:94-102` 一致。
- **建议 schema**：
```json
{
  "schema": 1,
  "switch_name": "XuniUplink",
  "name_prefix": "HypoMux-vnic-",
  "mac_prefix": "02-1A-2B",
  "seed": 1234567890,
  "next_index": 3,
  "updated_at_utc": "2026-10-03T20:30:00Z",
  "adapters": [
    {"index":1,"name":"HypoMux-vnic-01","mac_address":"02-1A-2B-3C-4D-5E",
     "device_id":"{...}","adapter_id":"...","interface_guid":"{...}",
     "if_index":29,"state":"present","created_at_utc":"...","last_error":""}
  ]
}
```
- **并发**：应用内**单写者**（engine 侧把 vnic 操作串行化）+ Windows **命名互斥**（`Global\HypoMux.VNIC.State`，回退 `HypoMux.VNIC.State`）。参考项目教训：**等待超时后绝不要换个名字重试**（否则两个持有者都以为拿到锁）；只有"创建互斥对象本身抛异常"才降级（`XVNic.Common.psm1:805-874`）。
- **漂移自愈**：`vnic.status` 每次都要以系统实况为准（Hyper-V 枚举 + 主机侧网卡 + IP），与状态文件双向核对；系统侧缺失 → 标 `missing`（不自动删记录）；状态文件缺失但系统存在同名同 MAC 的 entity → **认领**（避免重装后误伤）。
- **重启后可识别**：依赖 `DeviceId`（持久）+ `Name`+`MAC` 三键；`if_index` 可能变化，不能作主键。

---

## 4. DHCP 等待设计

### 4.1 参考实现的缺口

参考项目创建后**只等网卡出现 + MAC 一致**（10 s / 500 ms），**不等 IP**（`New-XuniNic.ps1:254-278`）。因此 HypoMux 需要额外的一轮等待，否则 UI 会在拿到 IP 前显示"已创建但无地址"。

### 4.2 推荐算法

```powershell
# 阶段 1：等主机侧网卡出现（NDIS 别名固定为 vEthernet (<Name>)）
$deadline = (Get-Date).AddSeconds(15)
do {
  $a = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue |
       Where-Object { $_.Name -eq "vEthernet ($name)" } | Select-Object -First 1
  if ($a) { break }
  Start-Sleep -Milliseconds 500
} while ((Get-Date) -lt $deadline)
if (-not $a) { <ADAPTER_NOT_VISIBLE> }      # 对齐参考实现：回滚 + exit 12/13

# 阶段 2：等 DHCP 地址
$deadline = (Get-Date).AddSeconds(45)
do {
  $ip = Get-NetIPAddress -InterfaceIndex $a.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.PrefixOrigin -eq 'Dhcp' -and $_.AddressState -eq 'Preferred' `
                       -and $_.IPAddress -notlike '169.254.*' } | Select-Object -First 1
  if ($ip) { break }
  Start-Sleep -Milliseconds 500
} while ((Get-Date) -lt $deadline)
```

参数依据【实测】：网卡出现（PnP 到达）**3 s 典型 / 20 s 极端**；租约获取在参考机器上约在网卡出现后数秒内完成；本机 11 张卡目前在 <2 s 内即为 `Preferred`（已是稳定态，非首配耗时）→ **建议轮询间隔 500 ms、网卡超时 15 s、IP 超时 45 s、整体 RPC 60 s**。

### 4.3 可观测的成功/失败特征

| 现象 | 含义 | 处置 |
|---|---|---|
| `PrefixOrigin=Dhcp` + `AddressState=Preferred` | **成功** | 记录 IP |
| `AddressState=Tentative` | DAD 进行中 | 继续等 |
| `AddressState=Duplicate` | 地址冲突（极少见，DHCP 场景） | 失败 |
| `169.254.x.x` / `PrefixOrigin=WellKnown` | APIPA，DHCP 失败 | 超时后失败，提示检查路由器 DHCP 池/隔离 |
| 一直无 IPv4 直到超时 | DHCP 无响应/被拒 | 同 APIPA |
| 事件 `Microsoft-Windows-Dhcp-Client/Admin` **Id 1002** | **服务器发 DHCPNACK**（明确拒绝） | 立即失败，文案带 NACK | 
| `Dhcp=Enabled`、`ConnectionState=Connected` | 接口已就绪（必要条件，非充分） | 继续等 |

【实测】本机 1002 事件实例文案：`网络地址是 0x… 的网卡的 IP 地址租约 192.168.11.<masked> 被 DHCP 服务器 192.168.11.<masked> 拒绝(DHCP 服务器发送了 DHCPNACK 消息)。`
【实测】`Microsoft-Windows-Dhcp-Client/Operational` 默认**未启用** → 不能依赖它；只用 `/Admin`。

### 4.4 超时的处置（建议）

**不回滚、不静默**：卡已建好且占用 MAC/编号，回滚会改变编号并使租约与 MAC 的对应关系抖动。
- `vnic.status` 返回 `state=present`、`ip_address=""`、`dhcp_pending=true`（新增字段）；
- UI 文案：`虚拟网卡已创建，但尚未取得 IP 地址。请确认路由器 DHCP 可用，或稍后重试。`；
- 保持可删除（用户可移除后重建）。

---

## 5. 枚举

### 5.1 Hyper-V 侧【实测】

`Get-VMNetworkAdapter -ManagementOS [-SwitchName <sw>]` 返回 `Microsoft.HyperV.PowerShell.VMInternalNetworkAdapter`，可用属性见附录 C.1。

**没有 `IPAddresses` 属性** → IP 必须通过 `DeviceId ↔ Get-NetAdapter.InterfaceGuid` 关联后查 `Get-NetIPAddress`。【实测】

建议枚举过滤：
```powershell
Get-VMNetworkAdapter -ManagementOS -SwitchName $sw -ErrorAction Stop |
  Where-Object { $_.IsManagementOs -and -not $_.IsDeleted -and $_.DeviceId -and $_.MacAddress }
```
（`SwitchName` 过滤不能替代 `DeviceId` 校验，只是收窄。）

### 5.2 主机侧【实测】

- 别名规则：**`vEthernet (<Name>)`**（`Get-NetAdapter -Name '<Name>'` 永远查不到）。
- `Hidden=False`（11 张 vNIC 都可见），`DriverFileName=VmsProxyHNic.sys`，`LinkSpeed=100 Mbps`（交换机侧固定 100 Mbps 呈现）。
- 交换机自身的 `vSwitch (<SwitchName>)`（ifIndex 34、`vmswitch.sys`、MAC 空、`Hidden=True`）**不是 vNIC**，必须排除。

### 5.3 建议枚举算法

```
1) Hyper-V: 枚举 SwitchName 上的 ManagementOS 适配器，按 DeviceId 过滤 entity，按名字前缀筛 HypoMux
2) 主机侧 : Get-NetAdapter -IncludeHidden，用 InterfaceGuid == DeviceId 关联，拿 Name/ifIndex/MAC/Status
3) IP     : Get-NetIPAddress -InterfaceIndex <idx> -AddressFamily IPv4，取 Dhcp+Preferred
4) 状态   : 与 vnic-hyperv.json 双向核对，输出 present/missing/orphan/ghost-ignored
```

---

## 6. 删除

### 6.1 参数集与正确姿势【实测】

`Get-Command Remove-VMNetworkAdapter` 四套参数集：

| 参数集 | 必需 | 主要参数 |
|---|---|---|
| `VMName` | `-VMName` | `-Name` |
| **`ResourceObject`** | **`-VMNetworkAdapter`（`ValueFromPipeline`）** | 仅对象参数 + 通用参数；**没有 `-Name`、没有 `-ManagementOS`** |
| `ManagementOS` | `-ManagementOS` | `-Name`、`-SwitchName` |
| `VMObject` | `-VM` | `-Name` |

**正确姿势**（把实体对象管道进去，走 `ResourceObject` 集）：
```powershell
$targets = Get-VMNetworkAdapter -ManagementOS -SwitchName $sw |
  Where-Object { $_.Name -eq $name -and $_.DeviceId -and $_.MacAddress -and
                 ((Format-Mac $_.MacAddress) -eq $expectedMac) }
if (-not $targets) { <NOT_FOUND_OR_GHOST_ONLY> }
$targets | Remove-VMNetworkAdapter -Confirm:$false -ErrorAction Stop
```
【参考真机】`XVNic.Common.psm1:3154-3156`：`$e.Object | Remove-VMNetworkAdapter -Confirm:$false -ErrorAction Stop`（**不能同时传 `-ManagementOS`**）

### 6.2 `-ManagementOS -Name` 为什么失败【实测 + 参考真机】

- 【实测】`Remove-VMNetworkAdapter -ManagementOS -Name 'xuni-01' -WhatIf` 的 WhatIf 行**打印两次**（同时命中幽灵与真卡）。
- 【参考真机】`task-43:22-28` 记录：真机上该命令整条报「**删除虚拟以太网交换机连接时失败。**」，导致回滚删不掉，留下永远清不掉的半成品。
- 【参考真机】`Remove-XuniNic.ps1:30-37`：删除**必须**走对象管道；删除失败**绝不清登记**。

### 6.3 删除的连带影响

| 项 | 结论 | 证据 |
|---|---|---|
| DHCP 租约（路由器侧） | **不会主动释放**；租约按 1 小时自然过期 | 【推断】，依据【实测】1 小时租期 |
| 本机 TCP/IP 接口注册表键 | **不清理**（实测孤儿键 `b8f05015-…` 仍带 `EnableDHCP=1`/`DhcpIPAddress`/`DhcpServer`） | 【实测】 |
| PnP 设备 | 会移除（宿主侧 vEthernet 消失） | 【参考真机】`task-43:389` |
| 主机侧 `Get-NetAdapter` 消失耗时 | 本机删除均在 **2 s 内**完成（复查循环 400 ms） | 【参考真机】`task-43:389`、`XVNic.Common.psm1:3163-3181` |
| 默认路由 | 随接口消失而消失 | 【推断】 |
| Hyper-V 侧残留 | 删除成功后**不应**残留；但异常路径会产生 ghost（本机已有 2 条先例） | 【实测】1.5 |

**复查循环建议**：400 ms 轮询，条件 = "Hyper-V 侧无同名同 MAC 的 entity" **且** "主机侧 `vEthernet (<Name>)` 为 `$null`"；超时上限 **30 s**（`task-43:389` 建议慢机从 15 s 提到 30 s）。

### 6.4 残留与幽灵无法清理

- **ghost 记录没有可用删除路径**：`DeviceId` 空 → 无法走 `ResourceObject`；按名字走 `ManagementOS` 会连带命中真卡并把整条命令搞失败。彻底清理需重建交换机或重启 `vmms`，**超出应用范围**。【参考真机】`task-43:381`
- `degraded`（`DeviceId` 空但 MAC 非空）分支在参考项目里**只有代码推理、无真机读数**（`task-43:383`）→ HypoMux 需自行防御：遇到 `degraded` 一律**跳过 + 告警**，不删。
- **HypoMux 的安全底线**：只删自己状态文件记录过、且 `DeviceId` 非空、MAC 匹配的对象；其余（含幽灵）一律只在 UI 中标注"存在系统残留，无法由应用清理"。

---

## 7. 权限与提权路径

### 7.1 必需权限【实测 + 推断】

- 【实测】当前会话**只在 `BUILTIN\<ADMIN_GROUP>`**、`Hyper-V <ADMIN_GROUP>` 组**为空**，而 `Get-VMNetworkAdapter`/`Get-VMSwitch`/`Add-VMNetworkAdapter -WhatIf` 全部可用 → **本地管理员权限足够**。
- 【推断/文档语义】`Hyper-V <ADMIN_GROUP>` 是给**非管理员**用户管理 Hyper-V 的替代路径；`#Requires -RunAsAdministrator`（参考项目 `New-XuniNic.ps1:1-2`）是"必须提权"的强断言，而不是"HVA 组足够"的反证。两者可并存（管理员 or HVA 成员）。
- 【未验证】非管理员会话下调用 Hyper-V cmdlet 的确切报错文案（无法在提权会话中降权测试）。

### 7.2 HypoMux 当前进程模型【实测】

- `desktop/build/windows/wails.exe.manifest:18`：`<requestedExecutionLevel level="asInvoker" uiAccess="false"/>` → **桌面/Wails 进程不提权**。
- `desktop/build/windows/nsis/project.nsi:743` 注释：`Core. The Wails/WebView2 executable remains asInvoker.` → 提权的是 **Core**。
- Core 的启动方式（`desktop/internal/services/engine.go:550` 注释）：**已安装的 Windows 服务优先，其次才是经过认证的 runas Core**。两条路径都带管理员/系统权限。

### 7.3 仓库现有的三种"提权/执行外部命令"机制

| # | 机制 | 位置 | 能力 |
|---|---|---|---|
| A | runas + **命名管道 + 一次性 token** 启动 Go Core | `desktop/internal/engineclient/privileged_windows.go:102-131,139-162,312-355` | ShellExecuteExW(`runas`,`SEE_MASK_NOCLOSEPROCESS`) → 随机管道名 `\\.\pipe\HypoMux-Core-<token24>` + SDDL 限流 + `GetNamedPipeClientProcessId` 校验 + 会话 token 认证；`ERROR_CANCELLED → ErrElevationCancelled("用户取消了管理员权限请求")`（`:41,:341-343`） |
| B | runas 调 `netsh.exe`（防火墙） | `desktop/internal/services/nat_firewall_windows.go:143-185` | ShellExecuteExW(`runas`)，`WaitForSingleObject(INFINITE)` + `GetExitCodeProcess`；**只有退出码，没有输出通道**；`ERROR_CANCELLED` 映射为 `user cancelled the administrator permission request`（`:161-163`） |
| C | 非提权 `powershell.exe -Command` + `ConvertTo-Json -Compress` 读结果 | `desktop/internal/services/mtu_windows.go:51-64`；可执行文件解析见 `desktop/internal/services/system_tools_windows.go:17-21`；TUN 预检 `desktop/internal/services/tun_preflight_windows.go:204` | 展示了"PowerShell 内联脚本 + JSON 结果"的现网范式（只读查询） |

### 7.4 方案对比与建议

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| **A（推荐）在已提权的 Core 内执行** | engine 实现 `vnic.create/status/remove`：内部 `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <scripts>\hyperv-vnic.ps1 -ParamsJson '<json>'`，以**退出码 + stdout 单行 JSON** 回传 | ① 不新增 UAC 通道（Core 本就提权/服务）；② 与 `desktop/internal/engineclient/vnic.go` 既有 RPC 完全一致；③ 桌面进程保持 `asInvoker`；④ 结果结构可测（stdout JSON） | 需随包分发脚本（`desktop/build/windows/Taskfile.yml` 的 resources/scripts），且 engine 首次承担"跑 PowerShell"的职责 |
| B 桌面 runas + 临时结果文件 | ShellExecuteExW(`runas`, `powershell.exe -File … -OutFile %TEMP%\<rand>.json`)，父进程读文件 | 不需要动 engine；复用 B 机制 | 每次操作一次 UAC；无 stdout 通道需落盘；取消/超时清理复杂 |
| C WMI/COM 直连 `root\virtualization\v2` | 直接调用 `Msvm_*`（`Msvm_VirtualSystemManagementService.AddResourceSettings` 等） | 不起子进程、无脚本分发 | Go 侧需 COM/WMI 绑定与 `Msvm_*` 手工调用（现仓库无此依赖），实现量/风险最高；**首版不推荐** |
| D 直接把桌面应用提权 | 把 manifest 改成 `requireAdministrator` | 最简单 | 破坏现网模型（`:18` 明确 asInvoker）、全应用常驻 UAC 提示，**不推荐** |

**结论**：走 **方案 A**。落点就是已冻结的 `vnic.create/status/remove`（`desktop/internal/engineclient/vnic.go:10-14,52-79`），引擎侧新增 Hyper-V 实现，桌面/前端无需新的提权交互。若再要求"零脚本分发"，则退化到方案 C，但应作为后续迭代。

**调用契约（建议与参考项目逐条对齐）**：
```
powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass \
  -File "<安装目录>\resources\scripts\hyperv-vnic.ps1" -ParamsJson '<JSON>'
```
- stdout：每行一个紧凑 JSON；成功末行含 `"success":true,"exitCode":0`；
- stderr：人类日志，失败末行 `{"__error":{"code","message","detail"}}`；
- 结果判定以**退出码 + stderr 末行**为准（stdout 可能为空）。
【参考真机】`xuni-network/artifacts/payload/resources/scripts/CONTRACT.md:26-52`

**提权失败 UX**：沿用现网文案风格（`privileged_windows.go:41`）：`用户取消了管理员权限请求`；服务模式不可用且 runas 被拒时给出可操作指引（安装服务 / 以管理员运行）。

---

## 8. Hyper-V 不可用 / 无外部交换机：检测与降级

### 8.1 前置检查清单（全部只读，可静态执行）

| 序 | 检查 | 命令 | 失败语义 |
|---|---|---|---|
| 1 | Hyper-V 模块 | `Get-Module -ListAvailable Hyper-V` | `HYPERV_NOT_AVAILABLE` |
| 2 | 功能启用 | `Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All`（`State -eq 'Enabled'`；`-like '*Pending*'` ⇒ 需重启） | `HYPERV_NOT_AVAILABLE` / `NEEDS_REBOOT` |
| 3 | 管理服务 | `Get-Service vmms`（参考项目用 `vmmsvc`→`Vmms`） | `HYPERV_NOT_AVAILABLE` |
| 4 | 必需 cmdlet | `Get-Command Add-/Remove-/Get-VMNetworkAdapter, Get-/New-/Remove-/Set-VMSwitch, Connect-VMNetworkAdapter` | `HYPERV_NOT_AVAILABLE`（缺哪个报哪个） |
| 5 | 交换机存在 | `Get-VMSwitch -Name <sw>` | `SWITCH_NOT_FOUND` |
| 6 | 交换机类型 | `SwitchType -eq 'External'` 且 `AllowManagementOS` | `SWITCH_NOT_SUPPORTED` |
| 7 | 上行有效 | 1.6 的 GUID 解析 + `Status -eq 'Up' -and MediaConnectionState -eq 'Connected'` | `UPLINK_DOWN` |

【参考真机】`XVNic.Common.psm1:261-310,1591-1720`（capability 探测）、`New-XuniNic.ps1:75-81,113-127`
【实测】Hyper-V 功能在本机 `Enabled`、`vmms` Running、模块 2.0.0.0、`XuniUplink` External 且上行 Up/Connected → 全部通过。

### 8.2 错误文案

- **实测**（不存在的交换机名）：
  `Get-VMNetworkAdapter` / `Get-VMSwitch` 抛 `Microsoft.HyperV.PowerShell.VirtualizationException`，`FullyQualifiedErrorId = InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVMNetworkAdapter`，消息：
  `Hyper-V 找不到名为"NoSuchSwitch-HypoMuxProbe"的虚拟交换机。`
  → 本机为中文环境；**实现必须按异常类型/FQID 判断，不能字符串匹配中文**。
- 【推断/参考真机】其他文案（`XVNic.Common.psm1:363-445` 的映射表，供降级兜底）：`不是内部或外部命令`→Hyper-V 未启用；`拒绝访问`→需管理员；`已存在具有相同名称`→换前缀；RPC/通信错误→查 `vmcompute`/`hns`。
- 【未验证】Hyper-V 服务未运行时 `Get-VMNetworkAdapter` 的**确切报错文案**（严禁停服务）。参考项目对该场景的文案是：`本机未启用 Hyper-V 组件，无法<动作>。请在"启用或关闭 Windows 功能"中勾选 Hyper-V（或 Hyper-V 虚拟机平台）后重启本机。`（`XVNic.Common.psm1:1313-1408`）
- **家庭版/未启用**：Windows Home 无 Hyper-V cmdlet/功能 → 命中检查 1/2；此时 HypoMux 必须**优雅降级**（隐藏/禁用该功能，保留原有出口网卡选择），绝不阻塞主流程。参考项目在枚举脚本里明确"Hyper-V 未启用**不是失败**"（`CONTRACT.md:197`，`available=false` 且 exit 0）——**建议 HypoMux 的 `vnic.status` 同样返回 `state=absent` + `hyperv_available=false` 而不是报错**。

---

## 9. 命名与 MAC 生成规范（HypoMux 侧建议）

### 9.1 命名

- 前缀 `HypoMux-vnic-`，编号 2 位十进制：`HypoMux-vnic-01` … `HypoMux-vnic-99`。
- **与用户既有 11 张 `xuni-*` 天然不冲突**（前缀不同）；且 1.5 已证明"同名重名"是真实风险 → 命名空间隔离是必需的。
- 编号分配：`index = max(状态文件中已记录 index) + 1`；生成前再检查系统侧是否已有同名 **entity**（ghost 不算占用）→ 冲突则 +1。
- 上限：建议 64 张（参考项目 `count ≤ 64`，`New-XuniNic.ps1:163-177`），超过则拒绝并给出明确文案。
- 主机侧别名固定为 `vEthernet (HypoMux-vnic-01)`；**任何按裸名字 `Get-NetAdapter -Name HypoMux-vnic-01` 的查找必然失败**。

### 9.2 MAC 生成

要求：
1. **首字节 `bit0=0`**（单播）、**`bit1=1`**（本地管理）→ 第二个十六进制字符 ∈ {2,6,A,E}；
2. 非全 0、非全 F；
3. 与系统现有所有网卡 MAC（`Get-NetAdapter -IncludeHidden`）不冲突；
4. 与本应用状态文件内既有 MAC 不冲突；
5. **确定性可复现**（重启/重装后能给同一 index 生成同一 MAC，避免租约与编号漂移）；
6. 不使用 Hyper-V 动态池 `00:15:5D:10:90:xx`。

推荐算法（等价于参考项目 `MacAddress.psm1:343-433`，已真机使用）：
- 固定语义前缀 `02:1A:2B`（三字节；`02` 满足 LAA + 单播），其余 3 字节由 `FNV-1a32(magic="XVNic", seed, prefix, index, attempt)` 派生；
- `seed` 由 `crypto/rand` 生成一次、写入状态文件（`vnic-hyperv.json.seed`）；
- 冲突时 `attempt++` 重散列（上限 4096）；
- 输出与传入形式：短横大写 `02-1A-2B-3C-4D-5E`（见 2.2）。
- **不要用 `Get-Random`**（不可复现）【参考真机】`MacAddress.psm1:343-433`。

**关于"随机 vs 顺序 vs 与路由器租约冲突"**：
- 顺序（`…-00-00-01`）会导致多台用户机器上的 MAC 重复 → 同一网段内冲突 → **不推荐**；
- 本方案 = "固定前缀 + 每安装随机 seed + index 派生"，同机可复现、跨机不重复【推断】；
- 路由器侧租约按 MAC 绑定：删除后重建同 index 卡会拿到**同一 IP**（租约 1 小时内）——这是**优点**（稳定映射），但也要在 UI 说明"删除后短时间内重建可能沿用一个 IP"。【推断】

### 9.3 并发创建

- 应用侧：单一 writer（engine 串行化请求 + 命名互斥），一次只建一张，逐张 `index+1` 并**立即落盘**（先落盘再建卡，或建卡成功立刻落盘，失败则回滚+清记录）。
- 参考实现一次性传入 `entries[]` 批量创建（`New-XuniNic.ps1:226`），HypoMux 建议**逐张提交**，便于 UI 呈现进度与失败定位。

---

## 10. 幂等语义

对齐参考实现（`New-XuniNic.ps1:205-220`）：创建前单向快照 `Get-NetAdapter`，
- **同名 entity 已存在** → `stage=nic-skipped, reason=name-exists`，**exit 0**；
- **同 MAC entity 已存在** → `stage=nic-skipped, reason=mac-exists`，**exit 0**；
- 已存在但形态异常（degraded/ghost）→ 不算命中，**新建**（避免继承坏记录）。
- 删除（`vnic.remove`）：目标不存在 → `state=absent`、**成功**（`Remove-XuniNic.ps1:24,105-113`）；非本应用纳管 → 跳过 + `exit 0`。
【参考真机】+【实测】同名/同 MAC 的判定依赖 `DeviceId` 非空。

---

## 11. 错误码契约对齐（建议）

参考项目退出码表【参考真机】`CONTRACT.md:54-70`：

| 码 | 名称 | HypoMux 用法建议 |
|---|---|---|
| 0 | 成功 | 含 skipped / already-gone 的幂等成功 |
| 2 | `INVALID_PARAMS` | 参数/MAC 格式/组内重复 |
| 3 | `PERMISSION_DENIED` | 未提权（方案 A 下应极少出现） |
| 4 | `HYPERV_NOT_AVAILABLE` | 模块/功能/cmdlet 缺失 |
| 5 | `ADAPTER_NOT_FOUND` | 删除/查询目标不存在（可降级为成功） |
| 6 | `ADAPTER_NOT_SUPPORTED` | 交换机非 External / 适配器形态不支持 |
| 7 | `SWITCH_NOT_FOUND` / `SWITCH_EXISTS` / `SWITCH_BUSY` | 交换机侧 |
| 9 | `NETWORK_IMPACT_REQUIRED` | 需要用户确认网络影响（如会改默认路由） |
| 10 | `SWITCH_CREATE_FAILED` / `VM_SWITCH_FAILED` | HypoMux **不应**创建交换机；保留位 |
| 11 | `ADAPTER_EXISTS` | 与幂等语义冲突时可选 |
| 12 | `ADAPTER_CREATE_FAILED` / `ADAPTER_REMOVE_FAILED` | 创建/删除失败（已回滚） |
| 13 | `ROLLBACK_FAILED` | **必须提示"机器可能处于半成品状态"**并给修复入口 |
| 70 | `INTERNAL_ERROR` / `STATE_FILE_ERROR` / `OPERATION_TIMEOUT` | 兜底 |

映射到 HypoMux RPC 错误码时：`protocol/v1/manifest.json` 目前只有 14 个错误码且**不新增**（`00-frozen-interface.md` §2 的既有约定）；建议把这些退出码折叠到既有的 `invalid_params` / `invalid_state` / `elevation_required` / `start_failed` / `stop_failed`（注意 Lead 更正：`vnic.create` 的最终启动失败实际返回 `tun_failed`，`engine/internal/server/server.go:882-887`），并在 `error.message` 里携带原始退出码与错误码名（供 UI 与日志）。

---

## 12. 与现有冻结接口的差异（最小改造清单）

`reports/vnic/00-frozen-interface.md` 的 `vnic.*` 契约是为 **Wintun/TUN** 设计的，与 Hyper-V 方向的字段不匹配：

| 现有字段 | 问题 | 建议 |
|---|---|---|
| `executable` / `config_path` / `config_sha256` | 仅 TUN keeper 有意义 | Hyper-V 路径下忽略（或删除） |
| `interface_name` | 语义是 TUN 适配器名 | 复用为 **vNIC 名**（`HypoMux-vnic-01`） |
| `address` / `prefix_length` / `mtu` | TUN 是静态地址模型 | **Hyper-V 路径由 DHCP 决定**，改为输出字段 `ip_address` / `prefix_length` / `gateway` |
| `startup_timeout_ms` | TUN 启动超时 | 复用为整体等待预算（建议 60 s） |
| （缺） | 交换机名、MAC、编号 | 新增 `switch_name`、`mac_address`、`index` |
| `VNICStatus.adapter_guid` | 语义模糊 | 明确为 Hyper-V **`DeviceId`**（GUID 字符串，带花括号） |
| `VNICStatus.state` | `absent/creating/present/removing/failed` | 建议增加 `dhcp_pending`/`missing`/`orphan`（或用 `last_error` 携带） |
| `host.shutdown` 必须先停 vnic keeper | TUN keeper 概念 | Hyper-V 路径无 keeper：**shutdown 不需要删卡**（卡片应在重启后继续存在并自动获取 DHCP） |
| `tun.Recover()` 误杀风险 | TUN 专用 | Hyper-V 路径应**完全绕开** `engine/internal/vnic/manager.go` 的 keeper 逻辑 |

**关键结论**：本方向下 `engine/internal/vnic/*`（keeper/supervisor）**不再适用**；`vnic.create` 的实现应替换为"Hyper-V 适配器管理"，且 `engine/internal/server/server.go:866-878` 的 stale 自愈逻辑（只停 keeper、不做 NIC 级删除）在新方向下**必须同步改造**，否则"外部已存在同名适配器 → 重试必失败"（`Cannot create a file when that file already exists.`，`30-real-machine-verdict.md` §7 F-D）这个缺陷会原样保留。

---

## 13. 风险清单（Top 10）与未验证项

### 13.1 风险

| # | 风险 | 证据 | 影响 | 置信 |
|---|---|---|---|---|
| 1 | **幽灵记录不可清理**，按名删会连带命中真卡并整条失败 | 【实测】1.5 +【参考真机】`task-43` | 删除/回滚失效，留下半成品 | 高 |
| 2 | **每张 vNIC 增加一条 `0.0.0.0/0`（RouteMetric 0 < 物理 500）** | 【实测】1.7 | 改变主机默认出口，可能影响 HypoMux 自身的直连/DNS 探测 | 高 |
| 3 | 依赖 Hyper-V：**家庭版/未启用即不可用** | 【推断】+【参考真机】capability 探测 | 功能对部分用户不可用，必须优雅降级 | 高 |
| 4 | `-WhatIf` **不校验** MAC/重名 | 【实测】2.4 | 测试会给出假绿灯；必须自建校验 | 高 |
| 5 | 交换机管理端口处于 ghost 形态 | 【实测】1.6 | 不能依赖 `vEthernet (XuniUplink)` 出网；前置检查不能要求它存在 | 高 |
| 6 | 提权依赖 Core（服务或 runas） | 【实测】manifest:18 / `engine.go:550` | runas 被取消或服务不可用时功能失效（需明确 UX） | 中 |
| 7 | 脚本需随包分发并被审计 | 【参考真机】+`Taskfile.yml` | 版本漂移/被篡改风险；需与签名流程一致 | 中 |
| 8 | 状态文件与系统漂移（用户手删/改名/重装） | 【推断】 | 记录错乱、误判 | 中 |
| 9 | 依赖本地化错误文本 | 【实测】8.2 中文文案 | 非中文系统会错判 → 必须用异常类型/FQID | 中 |
| 10 | 路由器按 MAC 的策略/租约 1 小时 | 【实测】1.7 | 限速策略不一定生效；删除后重建短期沿用一个 IP | 中 |

### 13.2 未验证项（明确列出，勿当结论）

1. Hyper-V 服务停止时 `Get-VMNetworkAdapter` 的**确切**报错文案（严禁停服务）。
2. 重启后 vNIC 是否自动存在并重新获取 DHCP（本机未重启验证；仅验证了跨数小时持久）。
3. `-StaticMacAddress` 短横/裸 12 位/大小写三种形式的**本机**接受度（`-WhatIf` 不校验）。
4. 删除一张实体后 Hyper-V 侧是否**必然**无残留（本机禁止删除）。
5. `Remove-VMSwitch` 是否连带删除其上的 ManagementOS vNIC（推断"是"，未验证）。
6. 无交换机名参数的 `Add-VMNetworkAdapter -ManagementOS` 行为。
7. Windows 家庭版/未启用 Hyper-V 上的 cmdlet 可用性（本机为专业工作站版）。
8. 非管理员会话（仅 HVA 组成员 / 普通用户）下的确切拒绝文案。
9. PnP 实例号在删除后是否复用（本机编号连续 `0000..0011`，未删除过）。
10. `Microsoft-Windows-Dhcp-Client/Operational` 启用后的字段（默认关闭，未启用以免改动系统）。
11. 官方文档语义（本环境**无法访问公网**：`web_fetch https://learn.microsoft.com/…` 返回 `URL hostname "learn.microsoft.com" resolves to a non-public IP address`）→ 所有"文档说"类结论均为【推断】。

---

## 附录 A · 复现命令全集（全部只读）

```powershell
# 环境 / 权限
$PSVersionTable.PSVersion; (Get-Module -ListAvailable Hyper-V | Select -First 1).Version
Get-Service Dhcp,vmms,nvagent | Select Name,Status,StartType
Get-LocalGroupMember -Group 'Hyper-V <ADMIN_GROUP>'

# 交换机 / 适配器
Get-VMSwitch | Format-List *
Get-VMNetworkAdapter -ManagementOS | Format-List *          # 属性全集见附录 C.1
Get-VMNetworkAdapter -ManagementOS -SwitchName 'XuniUplink' | Select Name,MacAddress,DeviceId,AdapterId,Status
Get-VMNetworkAdapter -ManagementOS -Name 'xuni-01'          # 演示同名两条

# 上行解析（注意 Guid[] 陷阱）
$sw = Get-VMSwitch -Name 'XuniUplink'
$g  = [guid]($sw.NetAdapterInterfaceGuid | Select-Object -First 1)
Get-NetAdapter -IncludeHidden | Where-Object { [guid]$_.InterfaceGuid -eq $g } | Format-List *
Get-NetAdapterBinding -Name '以太网'
Get-NetAdapter -IncludeHidden | Where-Object { $_.Name -like '*XuniUplink*' }

# 主机侧网卡 / IP / 路由
Get-NetAdapter | Where-Object { $_.Name -like 'vEthernet (*' } | Select Name,ifIndex,Status,Hidden,MacAddress,DriverFileName
Get-NetIPAddress -InterfaceAlias 'vEthernet (xuni-01)' -AddressFamily IPv4 | Format-List *
Get-NetIPInterface -InterfaceAlias 'vEthernet (xuni-01)' -AddressFamily IPv4 | Format-List *
Get-NetRoute -AddressFamily IPv4 -DestinationPrefix 0.0.0.0/0
Get-CimInstance Win32_NetworkAdapterConfiguration -Filter "IPEnabled=True" | Select Description,DHCPEnabled,DHCPServer,DHCPLeaseObtained,DHCPLeaseExpires,IPConnectionMetric

# 残留
Get-PnpDevice -Class Net | Where-Object { $_.InstanceId -like 'ROOT\VMS_MP\*' } | Select InstanceId,FriendlyName,Status | Sort InstanceId
Get-ChildItem 'HKLM:\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces'
Get-CimInstance -Namespace root\virtualization\v2 -ClassName Msvm_EthernetPortAllocationSettingData | Select ElementName,InstanceID,HostResource

# DHCP 事件
Get-WinEvent -LogName 'Microsoft-Windows-Dhcp-Client/Admin' -MaxEvents 20

# 参数面（安全）
Get-Command Add-VMNetworkAdapter,Remove-VMNetworkAdapter,Set-VMNetworkAdapter | ForEach-Object { $_.Name; $_.ParameterSets | ForEach-Object { $_.Name + ' :: ' + (($_.Parameters | ForEach-Object Name) -join ',') } }
Add-VMNetworkAdapter -ManagementOS -SwitchName 'XuniUplink' -Name 'HypoMux-probe-01' -StaticMacAddress '02-11-22-33-44-55' -WhatIf
(Get-VMNetworkAdapter -ManagementOS).Count      # 前后应相等（14）
```

## 附录 B · 参考实现关键行号索引

根目录：`%USERPROFILE%\Desktop\xuni-network\artifacts\payload\resources\scripts\`

| 主题 | 位置 |
|---|---|
| 创建适配器（正确命令） | `New-XuniNic.ps1:226-227` |
| 创建失败回滚 / 退出码 | `New-XuniNic.ps1:229-252`、`:329-341` |
| 创建后校验（网卡出现 + MAC 一致，10 s/500 ms） | `New-XuniNic.ps1:254-278`、`:280-293` |
| 幂等（同名/同 MAC 跳过，exit 0） | `New-XuniNic.ps1:205-220` |
| MAC 生成/校验约束 | `New-XuniNic.ps1:139-196`；`lib\MacAddress.psm1:134-136,164-212,321-328,343-433` |
| 状态文件读写/锁 | `New-XuniNic.ps1:109-111,302-320`；`lib\XVNic.Common.psm1:84-94,805-874,876-898` |
| 删除（对象管道 + 复查） | `lib\XVNic.Common.psm1:3025-3209`（删动作 `:3154-3156`，复查 `:3163-3181`） |
| ghost/degraded 分类 | `lib\XVNic.Common.psm1:2925-3023` |
| 只删纳管卡的安全策略 | `Remove-XuniNic.ps1:22-27,138-145,240-253` |
| 命令可用性 / 功能探测 | `lib\XVNic.Common.psm1:261-310,312-357,1313-1408,1591-1720` |
| 友好错误映射 | `lib\XVNic.Common.psm1:363-445` |
| 主机侧别名候选 / 交换机绑定映射 | `lib\XVNic.Common.psm1:1858-1906,1984-2010` |
| 调用契约与退出码表 | `CONTRACT.md:26-52,54-83,197` |
| 幽灵记录真机记录 | `xuni-network\reports\task-43-remove-nic-ghost-fix.md:16-51,381-389` |

## 附录 C · 本机实测原始数据

### C.1 `Get-VMNetworkAdapter -ManagementOS` 可用属性（全量，按字母）【实测】

`AclList, AdapterId, AllowTeaming, BandwidthPercentage, BandwidthSetting, CimSession, ComputerName, CurrentIsolationMode, DeviceId, DhcpGuard, DynamicIPAddressLimit, ExtendedAclList, FixSpeed10G, Id, IeeePriorityTag, IPsecOffloadMaxSA, IPsecOffloadSAUsage, IsDeleted, IsExternalAdapter, IsManagementOs, IsolationSetting, IsTemplate, MacAddress, MacAddressSpoofing, Name, PortMirroringMode, RouterGuard, RoutingDomainList, Status, StatusDescription, StormLimit, SwitchId, SwitchName, VFDataPathActive, VirtualSubnetId, VlanSetting, VMCheckpointId, VMCheckpointName, VMId, Vmmq*, VMName, VMQueue, VmqUsage, VMQWeight, VMSnapshotId, VMSnapshotName, Vrss*`

（`*` 为同名族属性；类型全名 `Microsoft.HyperV.PowerShell.VMInternalNetworkAdapter`；**无 `IPAddresses`**。）

### C.2 完整交换机清单【实测】

```
Name           SwitchType AllowManagementOS NetAdapterInterfaceDescription
Default Switch   Internal              True
XuniUplink       External              True Realtek Gaming 2.5GbE Family Controller
```

### C.3 `XuniUplink` 上的 13 条管理适配器（含幽灵与交换机自身端口）【实测】

```
Name       MacAddress   DeviceId                               Status
XuniUplink                                                     {Ok}     ← 交换机管理端口（ghost）
xuni-01                                                        {Ok}     ← ghost
xuni-01    <MAC> {<GUID>} {Ok}
xuni-02    <MAC> {<GUID>} {Ok}
xuni-03    <MAC> {<GUID>} {Ok}
xuni-04    <MAC> {<GUID>} {Ok}
xuni-05    <MAC> {<GUID>} {Ok}
xuni-06    <MAC> {<GUID>} {Ok}
xuni-07    <MAC> {<GUID>} {Ok}
xuni-08    <MAC> {<GUID>} {Ok}
xuni-09    <MAC> {<GUID>} {Ok}
xuni-10    <MAC> {<GUID>} {Ok}
xuni-11    <MAC> {<GUID>} {Ok}
```

### C.4 主机侧 `vEthernet` 适配器【实测】

```
Name                       ifIndex Status Hidden MacAddress
vEthernet (xuni-08)             60 Up      False <MAC>
vEthernet (xuni-09)             64 Up      False <MAC>
vEthernet (xuni-01)             29 Up      False <MAC>
vEthernet (xuni-06)             52 Up      False <MAC>
vEthernet (Default Switch)      27 Up      False <MAC>
vEthernet (xuni-11)             75 Up      False <MAC>
vEthernet (xuni-03)             39 Up      False <MAC>
vEthernet (xuni-07)             56 Up      False <MAC>
vEthernet (xuni-02)             33 Up      False <MAC>
vEthernet (xuni-04)             43 Up      False <MAC>
vEthernet (xuni-10)             71 Up      False <MAC>
vEthernet (xuni-05)             47 Up      False <MAC>
```
（全部 `DriverFileName=VmsProxyHNic.sys`；**没有 `vEthernet (XuniUplink)`**；`vSwitch (XuniUplink)` ifIndex 34、`vmswitch.sys`、Hidden=True、MAC 空。）

---

## 附录 D · 参考脚本骨架（设计建议，本轮未执行、未落盘）

> 仅作为落地时的骨架，标注**未执行**。`$ParamsJson` 契约与参考项目一致。

```powershell
#Requires -Version 5.1
# 由已提权的 Core 调用，因此不需要 #Requires -RunAsAdministrator
param([Parameter(Mandatory=$true)][string]$ParamsJson)

$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# 1) 能力探测（只读）：模块 / 功能 / vmms / cmdlet
# 2) 交换机检查：存在 + SwitchType=External + AllowManagementOS
# 3) 上行检查：NetAdapterInterfaceGuid 取首元素 → Get-NetAdapter → Up & Connected
# 4) 幂等：同名/同 MAC 的 entity → stage=skipped, exit 0
# 5) MAC：自校验（6 字节 / 单播 / LAA / 非 0 / 非 F / 组内唯一 / 与系统不冲突）
# 6) 创建：Add-VMNetworkAdapter -ManagementOS -SwitchName -Name -StaticMacAddress -Passthru
# 7) 校验：轮询 vEthernet (<Name>) 出现（15 s/500 ms）→ 回读 MacAddress 比对（不等则回滚）
# 8) 等 DHCP：Get-NetIPAddress PrefixOrigin=Dhcp & AddressState=Preferred（45 s/500 ms）
# 9) 落盘：state JSON（原子写、命名互斥）→ stdout 单行 JSON
# 10) 任何失败：逆序回滚已建对象（对象管道删除 + 400 ms/30 s 复查），exit 12 / 回滚失败 exit 13
```

---

*报告结束。本轮未修改任何源码、未执行任何写操作、未做任何 git 操作（工作树仅新增未跟踪的 `reports/`）。*
