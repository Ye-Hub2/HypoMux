# 79 — HypoMux vNIC 真机端到端回归报告

- 执行者: `hyperv-e2e-retry`
- 工作目录: `<repo>` (分支 main)
- 开始时间: 报告创建时刻（后续各阶段按实际时间戳追加）

## 授权边界（本次执行严格遵守）
- 最多创建 **1 张** HypoMux 网卡，验证后**必须删除**并恢复原状。
- 绝不触碰用户自建的 5 张 vNIC（仅只读用于前后对比）。
- 绝不删除任何不在 HypoMux 台账里的网卡。
- 外部交换机固定用 `XuniUplink`。
- 测试使用一次性临时数据目录 `$env:HYPOMUX_DATA_DIR`，不污染 `HypoMux-Portable-2.7.0-vnic-preview\data`。

## 环境
- `C:\Program Files\Go\bin` 置于 PATH 首位，`GOTOOLCHAIN=auto`，GOPROXY 走阿里云镜像。

## 阶段 0 — 报告初始化
✅ 报告文件已创建。后续阶段逐段追加。

---
## 阶段 1 — 只读基线快照

采样时刻: **2026-10-04 13:04:07 +08:00**（第二段 13:04:23）

### 1.1 交换机（2 个，与预期一致）

| Name | SwitchType | NetAdapterInterfaceDescription |
|---|---|---|
| Default Switch | Internal | (空) |
| XuniUplink | **External** | Realtek Gaming 2.5GbE Family Controller |

### 1.2 `Get-VMNetworkAdapter -ManagementOS` 全量（COUNT = **10**）

| Name | SwitchName | Status | MAC | Id(前8) |
|---|---|---|---|---|
| Container NIC 9d844023 | Default Switch | {Ok} | <MAC> | 9d844023 |
| XuniUplink | XuniUplink | {Ok} | (空) | d7aa1705 |
| xuni-01 | XuniUplink | {Ok} | (空) | 6249ab34 |
| xuni-01 | XuniUplink | {Ok} | <MAC> | 78eab750 |
| xuni-02 | XuniUplink | {Ok} | <MAC> | fdb210c9 |
| xuni-03 | XuniUplink | {Ok} | <MAC> | e49d90df |
| xuni-04 | XuniUplink | {Ok} | <MAC> | a2b867d0 |
| xuni-05 | XuniUplink | {Ok} | <MAC> | ac95c67a |
| **HypoMux-vnic-01** | XuniUplink | {Ok} | **021A2B000000** | 5e637f7e |
| **HypoMux-vnic-02** | XuniUplink | {Ok} | **021A2B000001** | cec70fc5 |

#### ⚠️ 意外发现（重要，后续判据必须据此调整）
宿主机上**已经存在 2 张 HypoMux 网卡**：`HypoMux-vnic-01` / `HypoMux-vnic-02`
（MAC `02-1A-2B-00-00-00` / `02-1A-2B-00-00-01`，均为 Dhcp、192.168.16.<masked> / .<masked>）。
这两张**不是本次创建的**，是上一轮执行遗留。
→ 它们不在本次的临时台账里，按 fail-closed 设计 `Remove()` 必须拒绝删除它们。
→ 因此阶段 3/4 的"网卡总数"判据不能用 10→11→10 来看，而应核对
   **本次新建的那一张是否出现、又是否消失**，其余 10 行逐行不变。

另注：`xuni-01` 在 `Get-VMNetworkAdapter` 层面有 **2 条**记录（一条 MAC 为空、一条
<MAC>），但两条都指向同一个宿主接口 `vEthernet (xuni-01)`，IP 完全相同。
这是宿主机侧的既有现象，仅记录，不处理。

### 1.3 5 张 xuni 卡 IP / PrefixOrigin / AddressState

| 网卡 | IPv4 | /24 | PrefixOrigin | SuffixOrigin | AddressState | IPv6 (link-local) | NetAdapter Status | LinkSpeed |
|---|---|---|---|---|---|---|---|---|
| xuni-01 | 192.168.16.<masked> | 24 | **Dhcp** | Dhcp | **Preferred** | <LINKLOCAL_IPv6>%29 | Up | 100 Mbps |
| xuni-02 | 192.168.16.<masked> | 24 | **Dhcp** | Dhcp | **Preferred** | <LINKLOCAL_IPv6>%33 | Up | 100 Mbps |
| xuni-03 | 192.168.16.<masked> | 24 | **Dhcp** | Dhcp | **Preferred** | <LINKLOCAL_IPv6>%39 | Up | 100 Mbps |
| xuni-04 | 192.168.16.<masked> | 24 | **Dhcp** | Dhcp | **Preferred** | <LINKLOCAL_IPv6>%43 | Up | 100 Mbps |
| xuni-05 | 192.168.16.<masked> | 24 | **Dhcp** | Dhcp | **Preferred** | <LINKLOCAL_IPv6>%47 | Up | 100 Mbps |

MAC 与授权清单逐一核对：
`<MAC>` / `<MAC>` / `<MAC>` / `<MAC>` / `<MAC>`
→ **5/5 全部吻合** ✅

### 1.4 既有 HypoMux 卡（只读，未触碰）

| 网卡 | IPv4 | PrefixOrigin | AddressState | NetAdapter |
|---|---|---|---|---|
| HypoMux-vnic-01 | 192.168.16.<masked> | Dhcp | Preferred | Up / 02-1A-2B-00-00-00 |
| HypoMux-vnic-02 | 192.168.16.<masked> | Dhcp | Preferred | Up / 02-1A-2B-00-00-01 |

### 1.5 settings.go 本次改动核对（只读）

- `settings.go:41` — 字段声明 `HideVirtualAdapters bool \`json:"hide_virtual_adapters"\``
- `settings.go:78` — `DefaultSettings()` 中 **`HideVirtualAdapters: false`**（已由 true 翻成 false）✅ 确认改动在仓库里
- `settings.go:243-245` — 回滚路径：`if _, exists := storedFields["hide_virtual_adapters"]; !exists { restored.HideVirtualAdapters = DefaultSettings().HideVirtualAdapters }` ✅ 已改为跟随 `DefaultSettings()`
- `settings.go:515-517` — Load 路径同构的 `!exists` 兜底

---

## 阶段 2 — 复核「修复引入的回归」（纯代码 + 单测，未动真机）

代码基线: commit `fc821b1`（工作区有未提交改动，见 `git status`）

### 2.1 结论速览

| 复核项 | 判定 | 证据 |
|---|---|---|
| `applyPoolUpdate` 不再就地改写调用方切片 | ✅ 通过 | hyperv_adapter.go:1764-1772 |
| Create 侧与 Remove 侧池键归一后完全一致 | ✅ 通过 | 见 §2.3 手工推演 |
| 台账编号从 0 开始不被回收 | ✅ 通过 | `TestHypervLedgerNormalizeKeepsAlreadyIssuedZeroSequence` PASS |
| typed nil 装箱 | ✅ 通过 | `TestHypervFailureErrorBoxesNilAsTrueNil` PASS |
| 整个 services 包单测 | ✅ **341 PASS / 0 FAIL / 6 SKIP**，exit 0，耗时 48.361s | `_phase2_gotest.txt` |

### 2.2 `applyPoolUpdate` 不再就地改写调用方切片 ✅

`desktop/internal/services/hyperv_adapter.go:1764-1772`：

```go
normalizedAdd := make([]string, len(add))
for i := range add {
    normalizedAdd[i] = hypervPoolKey(add[i])
}
normalizedRemove := make([]string, len(remove))
for i := range remove {
    normalizedRemove[i] = hypervPoolKey(remove[i])
}
add, remove = normalizedAdd, normalizedRemove
```

两处 `make` 都开的是**新切片**，且用 `for i := range add` 写 `normalizedAdd[i]`，
**从不写 `add[i]`**。函数签名的 `add`/`remove` 在 :1772 被**重新绑定到新切片**，
原调用方持有的切片内容一字未动。✅

这段的注释（:1758-1763）明确记录了当初那个回归的机理：
> 「必须写进**新切片**而不是就地改 add/remove：awaitBatch 把 appeared 同时传给了
> 本函数和 awaitAddresses。就地归一会把 names 里的对象名就地换成别名，
> awaitAddresses 再包一层就成了 `vEthernet (vEthernet (HypoMux-vnic-01))`，
> 台账 state 从此永远回写不了（真机端到端实测到，reports/vnic/76 §16）。」

回归测试 `hyperv_adapter_test.go:734-758` — `TestApplyPoolUpdateDoesNotMutateCallerSlice`
对 `add` 与 `remove` 两个入参都做了副本快照比对，**PASS**。

### 2.3 Create / Remove 两侧池键完全一致 ✅

**Create 侧的输入形态**（裸 Hyper-V 对象名）：
- `awaitBatch` hyperv_adapter.go:1457-1460 — `names` 由 `entry.Name` 拼装，即 `HypoMux-vnic-01`
- `awaitInterfaces` :1477-1483 — `appeared = append(appeared, name)`，**追加的是对象名本身**，
  宿主别名只用于 `hosts.byAlias[strings.ToLower(hypervHostInterfaceName(name))]` 的查找键
- :1465 `_ = s.applyPoolUpdate(appeared, nil)`

**Remove 侧的输入形态**（已拼好的宿主别名）：
- :1712 `return s.applyPoolUpdate(nil, []string{hypervHostInterfaceName(target)})`
- `hypervHostInterfaceName` :411-413 → `"vEthernet (HypoMux-vnic-01)"`

**归一函数 `hypervPoolKey`**（:424-436）两个方向的手工推演：

输入 `"HypoMux-vnic-01"`（Create 侧）
1. :429 `hypervObjectNameFromInterface("HypoMux-vnic-01")` → 进函数 :453，
   `strings.HasPrefix(lower, "vethernet (")` 为 **false** → 返回 `("", false)`
2. :432 `strings.HasPrefix("hypomux-vnic-01", "HypoMux-vnic-")`（`hypervAdapterNamePrefix` = :189）→ **true**
3. :433 返回 `hypervHostInterfaceName(...)` = **`"vEthernet (HypoMux-vnic-01)"`**

输入 `"vEthernet (HypoMux-vnic-01)"`（Remove 侧）
1. :429 `hypervObjectNameFromInterface(...)` → :453 前缀 `vethernet (` + 后缀 `)` 均匹配 →
   :456 剥壳得 `HypoMux-vnic-01` → :457 `isHyperVAdapterName` 通过 → 返回 `("HypoMux-vnic-01", true)`
2. :430 命中 `ok` 分支，**原样返回** trimmed = **`"vEthernet (HypoMux-vnic-01)"`**

**两侧最终写入/删除的池键逐字节相同：`vEthernet (HypoMux-vnic-01)`。** ✅

由此推出归一函数对池键是**幂等**的（`hypervPoolKey(hypervPoolKey(x)) == hypervPoolKey(x)`）——
第二遍走 :430 原样返回，不会套第二层 `vEthernet (...)`。这正是当初 `creating` 永不收敛
的根因（键被包成 `vEthernet (vEthernet (HypoMux-vnic-01))`，List() 与引擎都认不出）。

`findString` :1493-1500 用的是 `strings.EqualFold(TrimSpace(item), TrimSpace(value))`，
**大小写不敏感**比较，所以在池里比对键时不分大小写也不会漏删。

回归测试 `TestApplyPoolUpdateCreateThenRemoveIsSymmetric` 与
`TestHypervPoolKeyNormalizesObjectNameAndAlias` 均 **PASS**。

---

## 阶段 3 — 真机建 1 张

### 3.1 测试脚手架

- 临时测试文件：`desktop/internal/services/zz_temp_e2e_vnic_test.go`（`package services`，阶段 5 删除）
- 临时数据目录：`%TEMP%\hypomux-e2e-79-20261004-130825`（一次性，阶段 5 删除）
- 命令：`go test ./internal/services/ -count=1 -v -timeout 480s -run TestZZTempRealMachineE2E`
- `go vet ./internal/services/` 先跑了一遍：**exit 0**，编译无错。
- 当前 shell：`<HOST>\<ADMIN>`，`IsAdmin=True` —— 已提权，
  所以 `hypervExecuteElevated` 走 `hypervRunDirect` 分支，**不会弹 UAC**（这也解释了 5.3s 的返回）。

### 3.2 第一次尝试：**失败**（未创建任何网卡，宿主零影响）

```
[pre-create] 台账 NextSeq=1 NextMAC=0 条目数=0
[pre-create] 出口池 SelectedAdapterIDs=[]string(nil)
[pre-create] 权重表 [] -> map[]
[pre-create] Mode="tun" Strategy=""
---------------- CREATE 开始 ----------------
[create] Create 返回耗时 = 5.309s
[create] err 的接口值 = &services.HyperVError{Code:"create_failed",
        Message:"第 1/1 张创建失败：未知原因", Detail:"未知原因"}
[create] err == nil ? false   <<< typed-nil 装箱判据
[cleanup] 没有创建过任何网卡，跳过
--- FAIL: TestZZTempRealMachineE2E (5.32s)
```

**宿主侧结果：没有创建任何网卡。** `Get-VMNetworkAdapter -ManagementOS` 事后仍是 10 条，
无新增。临时台账里只有一条 `state=failed` 的预留记录。这符合"预留先于提权"的 §3.6.4 设计。

#### 根因定位：MAC 地址与上一轮遗留卡撞车（**新发现的真实缺陷**）

台账（正确按 UTF-8 读取）：
```json
{ "version":1, "nextSeq":4, "nextMAC":1,
  "adapters":[ { "name":"HypoMux-vnic-03",
                 "macAddress":"02:1a:2b:00:00:00",
                 "state":"failed",
                 "lastError":"第 1/1 张创建失败：未知原因" } ] }
```

宿主上占用 `02:1A:2B*` 的网卡：
```
Name            MacAddress   SwitchName Status
HypoMux-vnic-01 021A2B000000 XuniUplink {Ok}
HypoMux-vnic-02 021A2B000001 XuniUplink {Ok}
```

- 新建的临时台账 `nextMAC` 从 **0** 起算 → 发出第一个 MAC `02:1a:2b:00:00:00`
- 这与上一轮遗留的 `HypoMux-vnic-01` 的 MAC **逐字节相同**
- `Add-VMNetworkAdapter -StaticMacAddress`（hyperv_adapter.go:1992）因此失败

**缺陷定性**：Create 的**重名探测**有防撞（`inventory.hasName`，:1360-1367，
实测有效 —— 它正确跳过了 `-01`/`-02`，分配出 `-03`），但 **MAC 分配没有任何撞车探测**。
`nextMAC` 只在**本台账**内单调递增；台账一旦丢失、重置、或换一台机器，
就会重新发出宿主机上已被占用的 MAC。这在"用户手工建过 HypoMux 卡"或"重装应用"后必然复现。

**为什么用户看到的是"未知原因"**：hyperv_adapter.go:1293-1301 —
```go
message := "未知原因"
if runErr != nil { message = runErr.Error() }
for _, item := range failures {
    if strings.EqualFold(item.Name, entry.Name) && strings.TrimSpace(item.Error) != "" {
        message = strings.TrimSpace(item.Error)
    }
}
```
脚本回报的 `failures` 里**没有**与该名字匹配且 `Error` 非空的条目，`runErr` 也是 nil
（进程退出码不代表解释力，见 hyperv_adapter_windows.go:162），
于是文案退化成"未知原因"。**脚本明明报了具体错误，却被吞成"未知原因"**——
这本身就是第二个可报的缺陷：创建失败时用户拿不到任何可行动的信息。

#### 附带记录：一个我自己踩的坑（不是产品缺陷）
用 PowerShell 5.1 的 `Get-Content -Raw` 读 UTF-8 无 BOM 的 `adapters.json`，
中文全变乱码（`绗?1/1 寮犲垱寤哄け璐ワ細...`）。那是**读取端默认 ANSI 代码页**导致的，
不是产品写坏了。后面统一改用 `[System.IO.File]::ReadAllText($p, [Text.Encoding]::UTF8)`。
→ 这一点顺带印证了任务里提到的"BOM 坑"的同类：编码问题在这条链路上极易出岔子。

### 3.3 第二次尝试：调整测试夹具后重跑

见下节。

---
### 3.3 第二次尝试：**成功**（EXIT=0，PASS，墙钟 21.06s）

改动仅限一次性测试夹具：`zzSeedNextMAC` 先用只读 PowerShell 扫宿主的 `021A2B*` MAC，
把临时台账的 `nextMAC` 抬到「已占用最高序号 + 1」。
宿主实测最高序号 = 1（`HypoMux-vnic-02`）⇒ 夹具置 `nextMAC = 2`。
**没有改动任何产品代码，没有触碰宿主上任何既有网卡。**

#### 3.3.1 `List()` 前置快照 —— lead 改动的验收点 ✅

```
[list pre-create] List() 耗时=3.379s err=<nil> 行数=8
  Name="HypoMux-vnic-01"         IF="vEthernet (HypoMux-vnic-01)"   State="ready" Managed=false InPool=false MAC="02:1a:2b:00:00:00" Switch="XuniUplink"        IP="192.168.16.<masked>"
  Name="HypoMux-vnic-02"         IF="vEthernet (HypoMux-vnic-02)"   State="ready" Managed=false InPool=false MAC="02:1a:2b:00:00:01" Switch="XuniUplink"        IP="192.168.16.<masked>"
  Name="Container NIC 9d844023"  IF="vEthernet (Default Switch)"    State="ready" Managed=false InPool=false MAC="<MAC>"    Switch="Default Switch"    IP="172.25.160.<masked>"
  Name="xuni-01"                 IF="vEthernet (xuni-01)"           State="ready" Managed=false InPool=false MAC="<MAC>"   Switch="XuniUplink"        IP="192.168.16.<masked>"
  Name="xuni-02"                 IF="vEthernet (xuni-02)"           State="ready" Managed=false InPool=false MAC="<MAC>"   Switch="XuniUplink"        IP="192.168.16.<masked>"
  Name="xuni-03"                 IF="vEthernet (xuni-03)"           State="ready" Managed=false InPool=false MAC="<MAC>"   Switch="XuniUplink"        IP="192.168.16.<masked>"
  Name="xuni-04"                 IF="vEthernet (xuni-04)"           State="ready" Managed=false InPool=false MAC="<MAC>"   Switch="XuniUplink"        IP="192.168.16.<masked>"
  Name="xuni-05"                 IF="vEthernet (xuni-05)"           State="ready" Managed=false InPool=false MAC="<MAC>"   Switch="XuniUplink"        IP="192.168.16.<masked>"
Managed=false 的只读行 = 8 行
```

**验收结论 ✅**：5 张 xuni 卡 + `vEthernet (Default Switch)` 全部以 `Managed=false`
的未纳管只读行出现，正是 lead 这次改动的目标形态。附带确认：`xuni-01` 只出现 **1 行**
（幽灵那条无 MAC 的记录被 `AdapterID`/MAC 去重折叠掉了），没有重复行。
临时台账此时为空（`条目数=0`），故 8 行全部 `Managed=false`，无一行 `InPool=true`。

#### 3.3.2 Create 返回值

```
[create] Create 返回耗时 = 2.292s
[create] err 的接口值 = <nil>
[create] err == nil ? true   <<< typed-nil 装箱判据
[create] 返回行数 = 1
[create] row: Name="HypoMux-vnic-03"
[create]      InterfaceName="vEthernet (HypoMux-vnic-03)"
[create]      MacAddress="02:1a:2b:00:00:02" SwitchName="XuniUplink"
[create]      AdapterID="{<GUID>}"
[create]      State="creating" Managed=true InPool=false
[create]      BatchID="70e238558d9b635d" CreatedAt="2026-10-04T13:10:53+08:00"
[create]      LastError="已加入出口池，重启 HypoMux 聚合后生效"
```

- **`err == nil` 为 true** ✅ —— typed nil 装箱缺陷**未复现**。
  `hypervFailureError`（:1443-1448）这次确实返回了真正的 nil 接口。
- 返回行状态 `creating`（**不是** ready），符合"Create 立即返回、后台等待"的 §3.5 设计。
- 名字探测生效：跳过宿主已存在的 `-01`/`-02`，发出 `-03` ✅
- MAC `02:1a:2b:00:00:02` 与宿主既有两张无冲突 ✅

#### 3.3.3 池键对称性真机复核 ✅

```
hypervPoolKey("HypoMux-vnic-03")               = "vEthernet (HypoMux-vnic-03)"
hypervPoolKey("vEthernet (HypoMux-vnic-03)")   = "vEthernet (HypoMux-vnic-03)"
两侧一致 ? true
```
与 §2.3 的纯代码推演**完全吻合**，真机复现验证通过。

#### 3.3.4 状态机真实耗时（直读台账，绕开 `List()` 自愈）

```
[state] t=0s      "" -> "creating"
[state] t=910ms   "creating" -> "ready"
[pool]  t=910ms   出口池已含 "vEthernet (HypoMux-vnic-03)"，权重=1
[state] 观测结束，总耗时 910ms
```

> **关键耗时数字：`creating` → `ready` = 910ms**（远小于 15s 出现预算 / 45s DHCP 预算）。

完整迁移序列：`[t=0s  -> creating,  t=910ms creating -> ready]` —— **没有二次抖动、没有 failed 中间态**。
这直接证明阶段 2 复核的"池键就地改写"回归**确实已修复**：
当初双层别名会让台账 state 永远回写不了，而这里一次就收敛了。

台账落盘（ready 时）：
```
[post-ready] 台账 NextSeq=4 NextMAC=3 条目数=1
  entry Name="HypoMux-vnic-03" State="ready" MAC="02:1a:2b:00:00:02" Switch="XuniUplink"
        AdapterID="{<GUID>}" LastError=""
```
`LastError` 在 ready 后被清空 —— 创建时那句 `hypervPoolRestartHint`
（"已加入出口池，重启 HypoMux 聚合后生效"）如 :1430-1433 的注释所述只在 Create
返回那一刻出现，后续轮询自然消失。✅

#### 3.3.5 出口池（键名与创建侧一致）✅

```
[post-ready] SelectedAdapterIDs=[]string{"vEthernet (HypoMux-vnic-03)"}
[post-ready] 权重表 [vEthernet (HypoMux-vnic-03)] -> map[vEthernet (HypoMux-vnic-03):1]
[post-ready] Mode="tun" Strategy="round-robin"
```
池键是**宿主别名**，与 Create 侧归一结果一致，不是裸对象名 ✅
权重按 `AdapterWeightDefault = 1` 初始化 ✅

#### 3.3.6 宿主机侧状态 ✅

```
NETADAPTER status=Up mac=02-1A-2B-00-00-02 ifIndex=59 linkSpeed=100 Mbps
IP af=IPv6 addr=<LINKLOCAL_IPv6>%59/64 PrefixOrigin=WellKnown SuffixOrigin=Link AddressState=Preferred
IP af=IPv4 addr=192.168.16.<masked>/24              PrefixOrigin=Dhcp     SuffixOrigin=Dhcp     AddressState=Preferred
```
- 网卡 **Up** ✅
- 拿到 **DHCP** 地址 `192.168.16.<masked>/24` ✅
- **`PrefixOrigin=Dhcp` + `AddressState=Preferred`** ✅ 完全符合 §3.5 判据

---

## 阶段 4 — 删除并前后比对

### 4.1 Remove

```
[remove] Remove 返回耗时 = 6.514s
[remove] err = <nil> ; err == nil ? true
```
**`err == nil` 为 true** ✅，耗时 6.514s（提权删除脚本执行时间）。

### 4.2 台账与出口池清空 ✅

```
[post-remove] 台账 NextSeq=4 NextMAC=3 条目数=0
[post-remove] 出口池 SelectedAdapterIDs=[]string(nil)
[post-remove] 权重表 [] -> map[]
[post-remove] Mode="tun" Strategy="round-robin"
```
- 台账条目数 **0** ✅
- 出口池 **空** ✅（不只是移出 selected，连**权重项也一并清掉** ✅）
- `NextSeq=4 / NextMAC=3` **不回收** ✅ —— 与 :216-218 注释一致
  （避免路由器侧 MAC↔IP 租约记忆错位）。删除后重建会拿到 `-04` 和新 MAC，不会复用。

### 4.3 宿主侧查无此卡 ✅

```
[host vEthernet (HypoMux-vnic-03)] NETADAPTER <absent>
[host vEthernet (HypoMux-vnic-03)] IP <none>
```
两处（`Get-NetAdapter` 与 `Get-NetIPAddress`）**都查无此卡** ✅

### 4.4 `List()` 删后状态（按 lead 改动后的新语义解读）

```
[list post-remove] List() 耗时=3.242s err=<nil> 行数=8
Managed=false 的只读行 = 8 行
  [HypoMux-vnic-01 HypoMux-vnic-02 Container NIC 9d844023 xuni-01 xuni-02 xuni-03 xuni-04 xuni-05]
[List() 返回行数 = 8]
```

⚠️ **这里需要明确说明**：任务书原话是「`List()` 回到 0 行」。
实测是 **8 行**，但这**不是回归** —— lead 在我测试期间把 `List()` 改成了
返回宿主上所有 Hyper-V 虚拟网卡作为 `Managed=false` 只读行（见 §3.3.1）。
因此正确判据是：

| 判据 | 期望 | 实测 | 结论 |
|---|---|---|---|
| `HypoMux-vnic-03` 出现在 List() 里吗 | 否 | **否** | ✅ |
| 台账内受管行（`Managed=true`）数 | **0** | **0**（8 行全 false） | ✅ |
| 出口池残留 | 无 | `[]string(nil)` | ✅ |
| 宿主接口残留 | 无 | `<absent>` / `<none>` | ✅ |

即「本次创建的那一张在台账、出口池、宿主三处**全部消失**」这一核心判据全部满足。

### 4.5 前后比对表（5 张 xuni 卡 + 交换机）

| 项目 | 阶段 1 基线 | 阶段 4 收尾 | 判定 |
|---|---|---|---|
| xuni-01 MAC | <MAC> | <MAC> | ✅ 不变 |
| xuni-01 IP / Origin / State | 192.168.16.<masked> / Dhcp / Preferred | 192.168.16.<masked> / — / — | ✅ 不变 |
| xuni-02 MAC | <MAC> | <MAC> | ✅ 不变 |
| xuni-02 IP | 192.168.16.<masked> / Dhcp / Preferred | 192.168.16.<masked> | ✅ 不变 |
| xuni-03 MAC | <MAC> | <MAC> | ✅ 不变 |
| xuni-03 IP | 192.168.16.<masked> / Dhcp / Preferred | 192.168.16.<masked> | ✅ 不变 |
| xuni-04 MAC | <MAC> | <MAC> | ✅ 不变 |
| xuni-04 IP | 192.168.16.<masked> / Dhcp / Preferred | 192.168.16.<masked> | ✅ 不变 |
| xuni-05 MAC | <MAC> | <MAC> | ✅ 不变 |
| xuni-05 IP | 192.168.16.<masked> / Dhcp / Preferred | 192.168.16.<masked> | ✅ 不变 |
| 交换机 XuniUplink | External / Realtek 2.5GbE | （见 §4.6 独立复查） | ✅ 仍在 |
| 交换机 Default Switch | Internal | （见 §4.6 独立复查） | ✅ 仍在 |
| HypoMux-vnic-01 / -02（上一轮遗留） | 192.168.16.<masked> / .<masked>，Dhcp | 192.168.16.<masked> / .<masked> | ✅ 未被误删 |

**5 张 xuni 卡逐项完全未变，两个交换机仍在，本次创建的卡已彻底清除。**

#### ⚠️ 一处需要如实记录的观察
`Container NIC 9d844023`（挂在 **Default Switch** 上）的 MAC 在测试期间发生了变化：

| 时刻 | MAC |
|---|---|
| 阶段 1 基线 | `<MAC>` |
| Create 前 List() | `<MAC>` |
| Remove 后 List() | `<MAC>` |

该网卡属于 Hyper-V **动态 MAC 池**（`00:15:5D:xx:xx:xx`，与代码 :198 注释一致），
本次测试全程**没有任何操作碰过它**（我们只创建/删除了 `HypoMux-vnic-03`，
且它挂在 `XuniUplink` 上）。更可能是 Hyper-V/宿主侧动态 MAC 的周期性轮换
（动态 MAC 本来就会老化换址）。**如实记录，不作定论，也不把它算作本次的副作用。**
其 IP `172.25.160.<masked>` 与 `Default Switch` 归属均未变。

---
### 4.6 独立宿主复查（13:12:17，独立于 Go 测试的第二次只读采样）

不依赖刚才那个测试进程，单独再查一遍：

```
=== 交换机复查 ===
Name           SwitchType NetAdapterInterfaceDescription
Default Switch   Internal
XuniUplink       External Realtek Gaming 2.5GbE Family Controller      ✅ 两个都还在

=== ManagementOS 网卡全量 ===   COUNT = 10   （与阶段 1 基线完全一致）
Container NIC 9d844023  Default Switch  {Ok}  <MAC>
XuniUplink              XuniUplink      {Ok}
xuni-01                 XuniUplink      {Ok}              ← 幽灵记录仍在（既有现象）
xuni-01                 XuniUplink      {Ok}  <MAC>
xuni-02                 XuniUplink      {Ok}  <MAC>
xuni-03                 XuniUplink      {Ok}  <MAC>
xuni-04                 XuniUplink      {Ok}  <MAC>
xuni-05                 XuniUplink      {Ok}  <MAC>
HypoMux-vnic-01         XuniUplink      {Ok}  021A2B000000
HypoMux-vnic-02         XuniUplink      {Ok}  021A2B000001

=== HypoMux-vnic-03 残留检查（三处） ===
VMNetworkAdapter: <absent> OK
Get-NetAdapter:   <absent> OK
Get-NetIPAddress: <absent> OK
```

**`COUNT = 10`，与阶段 1 基线逐行相同。宿主已完全恢复原状。**

5 张 xuni 卡终态（独立复查，与 §4.5 表一致）：
| 网卡 | MAC | Status | IP/前缀 | PrefixOrigin | SuffixOrigin | AddressState |
|---|---|---|---|---|---|---|
| xuni-01 | <MAC> | Up | 192.168.16.<masked>/24 | **Dhcp** | Dhcp | **Preferred** |
| xuni-02 | <MAC> | Up | 192.168.16.<masked>/24 | **Dhcp** | Dhcp | **Preferred** |
| xuni-03 | <MAC> | Up | 192.168.16.<masked>/24 | **Dhcp** | Dhcp | **Preferred** |
| xuni-04 | <MAC> | Up | 192.168.16.<masked>/24 | **Dhcp** | Dhcp | **Preferred** |
| xuni-05 | <MAC> | Up | 192.168.16.<masked>/24 | **Dhcp** | Dhcp | **Preferred** |

上一轮遗留的两张卡也**完好无损**（192.168.16.<masked> / .<masked>，Dhcp / Preferred）——
证明 fail-closed 生效：它们不在本次临时台账里，`Remove()` 确实没碰它们。

### 4.7 正式数据目录未被污染 ✅

```
%USERPROFILE%\Desktop\HypoMux-Portable-2.7.0-vnic-preview\data\hyperv\adapters.json
    LastWrite = 2026/10/4 13:01:42
```
该文件的 `LastWriteTime` 是 **13:01:42**，而我的第一次基线采样是 **13:04:07**、
第一次 Create 尝试是 **13:08:26**。时间戳**早于我全部活动**，且在我整个会话期间未变
⇒ 我没有写入过它。全程 `HYPOMUX_DATA_DIR` 均显式指向一次性临时目录，测试日志里
打印过解析后的绝对路径可佐证。

---

## 阶段 5 — 清理

| 清理项 | 结果 |
|---|---|
| 临时测试文件 `desktop/internal/services/zz_temp_e2e_vnic_test.go` | ✅ **已删除**（复查 `Test-Path` = False） |
| 临时数据目录 `%TEMP%\hypomux-e2e-79-20261004-130825`（第 1 次失败尝试） | ✅ **已删除** |
| 临时数据目录 `%TEMP%\hypomux-e2e-79-run2-20261004-131046`（第 2 次成功尝试） | ✅ **已删除** |
| 临时目录残留复查 | ✅ `无残留临时目录` |
| 编译复查 `go vet ./internal/services/` | ✅ **exit 0**，没留下任何孤儿代码 |
| `git status --porcelain` | ✅ 工作区只剩 lead 的未提交改动 + `?? reports/`，**没有 `zz_temp_*`** |

⚠️ 清理过程中的一次自我修正（如实记录）：第一次 `Remove-Item` 前我写的路径守卫用了
`$env:TEMP\hypomux-e2e-79*` 做 `-like` 匹配，但本机 `$env:TEMP` 是 8.3 短路径
`%LOCALAPPDATA%\Temp`，而 `Get-ChildItem` 返回的是长路径
`%LOCALAPPDATA%\Temp\...`，两者字符串不等 → 守卫**正确地拒绝了删除**。
我没有放宽守卫，而是先 `Resolve-Path` 打印出两个目录的**解析绝对路径**和**完整内容清单**
（确认里面只有本次测试产生的 `adapters.json` / `settings.json`），再按已核验的长路径删除。
这与本报告主题一致：**fail-closed 是对的，不要为了让流程跑通而放宽。**

---

## 总结

### 通过的判据（全部有真实输出佐证）

| # | 判据 | 结果 |
|---|---|---|
| 1 | `applyPoolUpdate` 不就地改写调用方切片 | ✅ 纯代码 + 单测 + 真机三重佐证 |
| 2 | Create/Remove 池键归一后**完全一致** | ✅ 真机两侧同得 `vEthernet (HypoMux-vnic-03)` |
| 3 | `Create` 返回 `err == nil`（typed nil 未装箱） | ✅ 返回 `<nil>` |
| 4 | 返回行状态为 `creating`（非 ready） | ✅ |
| 5 | 台账 `creating` → `ready` 实际用时 | ✅ **910ms**，序列无抖动 |
| 6 | 出口池出现该卡且池键 = 宿主别名 | ✅ 权重 1 |
| 7 | 宿主 Up + DHCP + `PrefixOrigin=Dhcp` + `AddressState=Preferred` | ✅ 192.168.16.<masked>/24 |
| 8 | `Remove` 返回 `err == nil` | ✅ 6.514s |
| 9 | 台账 / 出口池 / 权重 / 宿主接口**四处**均已清除 | ✅ 宿主三处查询全 `<absent>` |
| 10 | 5 张 xuni 卡逐项未变 | ✅ MAC/IP/Origin/State 全一致 |
| 11 | 两个交换机仍在 | ✅ |
| 12 | 正式 data 目录未被污染 | ✅ mtime 早于我全部活动 |
| 13 | `HideVirtualAdapters` 默认值确为 `false` | ✅ settings.go:78 |
| 14 | services 包单测 | ✅ **341 PASS / 0 FAIL / 6 SKIP** |
| 15 | lead 的 `List()` 改动验收点 | ✅ 5 张 xuni + Default Switch 全部 `Managed=false` |

### 发现的真实缺陷（建议修复）

**D1（高）MAC 分配没有撞车探测。**
`nextMAC` 只在**本台账内**单调递增，`Create` 对**名字**有重名探测
（:1360-1367 `inventory.hasName`，实测有效），对 **MAC 却没有**。
全新/重置/丢失的台账会从 `02:1a:2b:00:00:00` 重新开始发号，
与宿主机上任何已存在的 HypoMux 卡撞车，`Add-VMNetworkAdapter -StaticMacAddress` 直接失败。
本次第一次真机尝试就是**实测撞上了**（详见 §3.2）。
复现路径极常见：换机、重装应用、手工清台账。
建议：`Create` 的预留循环里像处理名字一样，把已占用的 MAC 后缀也一并跳过。

**D2（中）创建失败的具体原因被吞成"未知原因"。**
`hypervReconcileBatch`（:1293-1301）只在 `failures` 里找到**名字匹配且 `Error` 非空**的条目
才用它的文案；脚本明明回报了失败、却因任何原因没匹配上时，文案退化成 `未知原因`，
用户拿不到任何可行动信息。本次实测就是这条路径（§3.2）。
建议：找不到匹配项时，把 `result.Failures` 原文（或前 N 条）兜进 `Detail`。

**D3（低，观察项）Default Switch 的 Container NIC 动态 MAC 在测试窗口内轮换。**
`<MAC>` → `<MAC>`（§4.5）。全程未被本次测试操作触及，
IP 与交换机归属不变。可能是 Hyper-V 动态 MAC 老化。如实记录，不作定论。

### 没能做到 / 需要如实说明的部分

1. **主流程是在调整了测试夹具之后才跑通的。** 全新临时台账的 `nextMAC=0`
   直接发出与宿主既有卡撞车的 MAC，第一次真实 `Create` **失败**了。
   第二次我只改了**一次性测试夹具**（把临时台账的 `nextMAC` 抬到宿主已占用值之后），
   **没有改动任何产品代码**。因此「默认配置下首次安装能成功建卡」这条路径，
   本报告**没有跑通**，且我判定它有真实缺陷（D1）。这不是我放宽授权边界换来的结果 ——
   第一次尝试同样只建了 1 张卡，且实际一张都没建成。
2. **`List()` 删后返回 8 行而非任务书写的 0 行。** 原因已查明：lead 在测试期间把
   `List()` 改成返回所有宿主 Hyper-V 虚拟网卡作为 `Managed=false` 只读行。
   我按新语义核对（受管行 0 台账行、本次卡在三处全消失），不是回归。详见 §4.4。
3. **xuni-01 的幽灵记录**（`Get-VMNetworkAdapter` 里两条同名、一条 MAC 为空）未处理。
   这是宿主侧既有现象，不在授权范围内，仅记录。
4. **上一轮遗留的 `HypoMux-vnic-01` / `-02` 仍留在宿主上**，我没有删除
   （不在本次授权内、也不在本次临时台账里）。如需清理请另行授权。
5. `Docker`/CI 环境无 Hyper-V，未覆盖；本报告全部结论均来自本机真机。

### 证据文件

| 文件 | 内容 |
|---|---|
| `_phase2_gotest.txt` | services 包完整 `go test -v` 输出（341 PASS / 0 FAIL） |
| `_phase3_e2e.txt` | 真机 E2E 完整 `go test -v` 输出（第 2 次成功的全部日志） |
| `_phase3_datadir.txt` | 第 2 次运行的临时数据目录路径（目录本身已删除） |

（第 1 次失败尝试的原始输出已在上文 §3.2 原样引用；该次运行的输出文件被第 2 次覆盖。）

---
*报告完*
