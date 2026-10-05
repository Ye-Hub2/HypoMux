# 62 · Hyper-V 桌面层服务设计（HypoMux-vnic-NN 批量宿主 vNIC）

> 任务来源：Lead `m00432`。阶段：**设计，只读**。本文件是本轮**唯一**写入产物。
> 结论均已带 `文件路径:行号`；凡本机未实测的行为一律标「**未验证**」。

---

## 0 摘要（先给结论）

| 问题 | 结论 |
|---|---|
| 提权方式 | **推荐 (b) 变体**：常量 PowerShell 脚本（`-NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand <UTF-16LE base64>`）+ **结果文件**回传；提权走**独立一次性 runas 子进程**（复用仓库既有的 `ShellExecuteExW + verb runas` 机制），**不复用 `EnsureElevated` 核心提权通道**（它会重启聚合核心、中断用户连接）。不引入新依赖。 |
| 与旧 `virtual_adapter.go` 的关系 | **完全替换**：新服务独立实现，旧三文件 + 引擎侧 `vnic.*` + 前端面板全部删除（清单见 §10）。唯一继承的是 *Win32 提权机制* 这一写法。 |
| 归属判定 | **三重**：① `settingsDirectory()/hyperv/adapters.json` 台账（权威）② 命名规范 `HypoMux-vnic-NN` ③ 删除时 **MAC 必须与台账一致**。三者全中才允许删；全仓既有 `xuni-*` / `Container NIC *` / 交换机自身适配器一律不受影响。 |
| 状态机 | `creating`（已下发，等 DHCP）→ `ready`（拿到非 APIPA 的 IPv4）/ `failed`（超时或脚本报错）；`absent` 表示台账有、系统里已无。轮询 1s、总预算 60s，**超时不自动删除**。 |
| 失败回滚 | **停批 + 保留已成功项 + 记 `failed` + 提供显式「撤销本次」（按 `batchID`）**；软失败（DHCP 超时）不阻断后续。 |
| 出口池 | 复用 `SettingsService.UpdateHome`（`settings.go:407-414`）→ `updateHomeStrategy`（`:416-443`），**必须传 `settings.Get().Mode`**；绝不走 `UpdateFields`（白名单里没有这两个字段，`settings.go:265-307`）。创建后**必须重启聚合**才生效（`engine.go:828`/`:854` 启动期快照）。 |
| 引擎/契约改动 | **不需要**（这是与旧方案最大的区别）。引擎侧只做**删除**清理。 |
| 新增文件 | `desktop/internal/services/hyperv_adapter.go`、`hyperv_adapter_windows.go`、`elevated_script_windows.go`、`hyperv_ledger.go`（+ 测试）；前端 `components/hyperv/*`。 |

---

## 1 背景与方向反转：必须回应 `00-frozen-interface.md` 的四条反对理由

`reports/vnic/00-frozen-interface.md:24-25` 当初**明确否决**了 Hyper-V `Add-VMNetworkAdapter -ManagementOS` 路线，理由四条。现在方向反转，逐条回应（真机事实见 §2）：

1. **「需要启用 Hyper-V + 预先建好虚拟交换机」** → 本机**已经**处于该状态：`XuniUplink` 是 External 交换机、绑定物理 `Realtek Gaming 2.5GbE Family Controller`、`AllowManagementOS=True`（实测）。产品不应**主动启用** Hyper-V，只应**检测**；检测不到就整块功能隐藏（见 §4 `Available()`）。
2. **「Windows 家庭版不可用」** → 同意，仍成立。这是**能力缺失**而非失败：`Get-VMSwitch` 失败 / `Hyper-V` 模块不存在 ⇒ 返回 `hyperv_unavailable`，UI 不显示该面板（与 `connections.go:45` 的降级思路一致）。
3. **「需要随包分发经审计的 .ps1」** → **本方案消解**：脚本常量**内嵌在 Go 二进制里**（`encoding/base64` + `unicode/utf16` 现场编码），**不落地任何 .ps1 文件**，`-ExecutionPolicy Bypass` 只对本次 `-EncodedCommand` 生效。仓库自此仍无 `.ps1` 资产。
4. **「本仓库无先例」** → **已有两处先例**：`desktop/internal/services/nat_firewall_windows.go:143-185`（`runElevatedFirewallCommand`，`ShellExecuteExW`+`runas`+等待+退出码）与 `desktop/internal/engineclient/privileged_windows.go:312-355`（`launchElevatedCore`，同款 + 认证命名管道）。本设计复用其**机制**并修补其不足（只吃字符串参数、不看输出——见 §9）。

---

## 2 事实基线

### 2.1 真机只读实测（Hyper-V，本轮未做任何修改）

- 交换机：`Default Switch`（Internal，`AllowManagementOS=True`）、**`XuniUplink`（External，物理网卡 `Realtek Gaming 2.5GbE Family Controller`，`AllowManagementOS=True`）**。
- `Get-VMNetworkAdapter -ManagementOS` 返回 13 条：`Container NIC 9d844023`（Default Switch，MAC `<MAC>`）、`XuniUplink`（**交换机自身适配器，MAC 为空**）、`xuni-01..11`（MAC 全部是**本地管理地址 LAA**，首字节第二位置 1，例如 `xuni-01=<MAC>`、`xuni-02=<MAC>` … `xuni-11=<MAC>`）；其中 **`xuni-01` 出现了两条**，一条 MAC 为空——**说明「按名字唯一定位」不可靠**，必须用 `Name` + 非空 `MacAddress` 双条件。
- **`Get-VMNetworkAdapter ... IPAddresses` 全部为空**（宿主机 ManagementOS vNIC 不通过 KVP 上报）⇒ 状态判定**必须**走 `Get-NetIPAddress` / `net.Interfaces()`，不能读 Hyper-V 对象的 `IPAddresses`。
- `Get-NetAdapter` 侧名字为 **`vEthernet (xuni-01)`**，`InterfaceDescription = Hyper-V Virtual Ethernet Adapter #N`，`LinkSpeed 100 Mbps`，MAC 与 Hyper-V 侧一致；`vEthernet (Default Switch)` ifIndex 27；物理 `以太网` ifIndex 8。
- `Add-VMNetworkAdapter` 参数表**实测存在**：`ManagementOS,SwitchName,Name,DynamicMacAddress,StaticMacAddress,DeviceNaming,Passthru,ResourcePoolName,IsLegacy`；**没有 notes/description 参数**（`Set-VMNetworkAdapter` 也没有）⇒ **归属标记不能依赖描述字段**。`Set-VMNetworkAdapter` 另有 `StaticMacAddress,DynamicMacAddress,MacAddressSpoofing,DhcpGuard,RouterGuard` 等。
- `powershell.exe` 版本 **5.1.19041.5129**，`Hyper-V` 模块 **2.0.0.0** 可用；当前会话 `IsInRole(Administrator)=True`（但产品进程是 asInvoker，见 §3）。
- 本机 Go 1.26.6 可用（只读编译检查用）。

### 2.2 仓库事实

- 产品清单：`desktop/build/windows/wails.exe.manifest:18` `<requestedExecutionLevel level="asInvoker" uiAccess="false"/>` ⇒ **任何 Hyper-V cmdlet 都要自己提权**。
- 自命令分发先例：`desktop/main.go:32`（`--core-service-self-test`）、`:41`（`--recover-network`），均在 GUI 起来之前 `return`；`:48` `startup.PrepareDesktopLaunch(os.Args[1:])`，`:52-57` 已提权时的降级日志。⇒ 需要 helper 子命令时**不需要新增二进制**，加一个 `--hyperv-helper` 分支即可（见 §3.5）。
- 进程内提权判定先例：`desktop/internal/services/tun_preflight_windows.go:25` `windows.GetCurrentProcessToken().IsElevated()`；`desktop/internal/startup/privilege_windows.go:197-213 tokenElevation()`（`GetTokenInformation(TokenElevation)`）。
- 适配器读取：`desktop/internal/services/adapters.go:39-133` 用 `net.Interfaces()` + `adapterPlatformMetadata()`（`adapter_metadata_windows.go:19`）拿到 `Address/PrefixLength/Gateway/DNSServers/Metric`；**`id := item.Name`（`:83`）即 Windows 接口别名**，`SelectedAdapterIDs` 用它做**大小写敏感的精确匹配**。
  - ⚠️ 关键限制：`adapters.go:80-82` **没有 IPv4 的接口整体丢弃** ⇒ 刚创建、DHCP 未到的 vNIC **不会**出现在 `AdapterService.List()` 里。因此新服务**不能**只依赖 `AdapterService.List()`，必须自己做 `net.Interfaces()` 扫描（§4.4）。
- 隐藏逻辑：`isVirtualAdapter()`（`adapters.go:142-156`，标记词含 `vethernet`，见 `:145`）把 vEthernet 判为虚拟网卡；`settings.go:41 HideVirtualAdapters`、`:73`（默认 `true`）；前端 `state/adapterVisibility.ts:6` 在 `hideVirtual` 时把虚拟网卡**从列表里过滤掉**，`state/useEngineState.ts:584` 会显示 `hiddenSelectedCount`（选中但被隐藏的张数）。⇒ **UX 冲突**，见 §8.4。
- 出口池消费面：`engine.go:1217-1227 engineAdapters()`、`:1238-1266 engineChannels()`——同一 kind 的适配器进 `nic_ethernet`/`nic_wifi`/`aggregation` 的 `adapter_names` 列表（`:1255-1257`），并且**每张适配器另建一个 `nic_<接口名>` 通道**（`:1260-1264`）。这正好给出「按 vNIC 粒度分流」的机制：路由器按 MAC 限速 ↔ 每张 vNIC 一个出口。
- 启动期快照：`engine.go:828 "adapters": engineAdapters(selected)`、`:854 "channels": engineChannels(selected)` 只在 `engine.start` 时计算一次；`:787/:804` 会用 `updateHomeStrategy` 把 mode+selection 一起写回。⇒ **创建 vNIC 后必须重启聚合**。
- 空选择合法：`scheduling_test.go:82-83` 把 `SelectedAdapterIDs = nil` 视为可接受 ⇒ 删除最后一张 vNIC 时可以传空列表（但引擎启动自身仍要求 ≥1 张可用网卡）。
- 域名冲突风险（历史）：`docs/validation/tun-startup-dns-address-fix.md:6`（issue #75）记录过 Hyper-V 接口 `172.19.0.1/20` 与固定 TUN 地址冲突。本方案 vNIC **没有固定地址**（由局域网路由器 DHCP），天然规避。

---

## 3 Q1 提权执行方式

### 3.1 备选方案评估表

| 方案 | 可行性 | 结论 |
|---|---|---|
| **(a) 复用 engineclient 的 runas 模式** | 机制可行，**代码不可直接复用** | `privileged_windows.go:312-355 launchElevatedCore` 硬编码 `serve-pipe --pipe <pipe> --session-token <token> --host-pid <pid>` 参数 + `:133-198` 认证管道握手，是**核心专用通道**；且 `:390-404` 的 `TerminateProcess` 在未提权 UI 侧会 `ERROR_ACCESS_DENIED`。⇒ **只借机制，不借代码**。 |
| **(a2) 走 `EnsureElevated()` 让引擎代劳** | **否决** | ① `services/engine.go:763` `EnsureElevated` 会**重启聚合核心**，`client.negotiateSession` 要求重新握手 ⇒ 中断用户当前连接；② 需要新引擎 RPC + `protocol/v1/manifest.json` + `api/v1` 契约改动（上一轮 `vnic.*` 的教训）；③ 只有「已安装的 Core 服务在跑」时才有提权代理，便携版静默不可用；④ 引擎终究还是要 `exec powershell.exe`，问题只是搬到了引擎侧。 |
| **(b) `powershell.exe -EncodedCommand` 子进程** | **可行 · 推荐** | Hyper-V 管理面只有 PowerShell 模块；脚本可**内嵌常量**；结果可回传（文件或管道）。三个必须记住的坑：① **必须 `powershell.exe`（5.1），不能 `pwsh`**——`Hyper-V` 是 Windows PowerShell 模块；② `-EncodedCommand` 必须是 **UTF-16LE 的 base64**；③ **`ShellExecuteExW + runas` 无法把子进程 stdout 管道给父进程**（`nat_firewall_windows.go:143-185` 正是因此只看退出码）⇒ 结果**必须走文件**（或命名管道）。 |
| **(c) 直接 WMI/COM（`root\virtualization\v2`）** | **本轮否决** | `desktop/go.mod` 无 COM/OLE 依赖；需要 `Msvm_VirtualSystemManagementService.AddResourceSettings` + `Msvm_EthernetPortAllocationSettingData` 的 embedded-instance XML 编组；破坏性调用在本机**无法安全验证**。收益（省一次 PowerShell 进程）远小于风险。列为**未来可选**（签名集中在一个 `hypervBackend` 接口后，将来可换实现）。 |
| **(d) 在已提权的常驻 Core 服务里执行（新增引擎 RPC）** | 可行但不推荐（与 a2 同因） | 优点：无 UAC、结果类型化。缺点：契约面变更 + 便携版无提权代理 + 引擎侧依旧要起 PowerShell。若未来 Core 服务成为强制形态，可作为 v2 演进方向。 |

### 3.2 推荐架构（一句话）

> **桌面层新增一个通用「一次性提权脚本运行器」**（`ShellExecuteExW + runas`，或本进程已提权时直接 `exec`），执行**内嵌常量脚本**，可变数据全部经 **base64** 注入、结果经**结果文件**回传；**全程不碰聚合核心**。

与旧 `virtual_adapter.go` 的关系：**替换**。旧实现把「创建网卡」委托给引擎的 Wintun keeper（`engineclient.VNICCreate` → `vnic.*` RPC），新实现是**纯桌面层**，引擎侧不需要任何新代码。

### 3.3 不需要 helper 二进制

提权子进程是 `powershell.exe`，**不是** `hypomux.exe` 自身 ⇒ 不需要新 exe、不需要打包改动。仅当将来要「父进程已提权 ⇒ 直连 stdout」时才考虑 `--hyperv-helper` 子命令（`main.go:32/41` 是现成模板）。

### 3.4 提权运行器接口（新文件 `desktop/internal/services/elevated_script_windows.go`）

```go
//go:build windows

// elevatedJob / elevatedResult 是脚本的输入输出契约（JSON，camelCase）。
type elevatedJob struct {
    Action     string   `json:"action"`      // "create" | "remove" | "renew"
    SwitchName string   `json:"switchName"`
    Count      int      `json:"count"`
    Names      []string `json:"names"`       // remove 用
    Prefix     string   `json:"prefix"`      // "HypoMux-vnic-"
    StartIndex int      `json:"startIndex"`
    JobID      string   `json:"jobId"`
}

type elevatedAdapter struct {
    Name       string `json:"name"`
    SwitchName string `json:"switchName"`
    MacAddress string `json:"macAddress"`
}

type elevatedResult struct {
    OK       bool              `json:"ok"`
    Code     string            `json:"code,omitempty"`
    Error    string            `json:"error,omitempty"`
    Adapters []elevatedAdapter `json:"adapters,omitempty"`
}

// runElevatedScript 执行内嵌常量脚本；要求 job 与 resultPath 绝对路径。
// timeout 是父进程的逻辑截止时间；到点即返回超时错误（不保证杀死子进程，见 §9.4）。
func runElevatedScript(ctx context.Context, scriptID string, job elevatedJob, timeout time.Duration) (elevatedResult, error)
```

关键实现点（逐条可落地）：

1. **脚本常量**：`const hypervScriptCreate/Remove = ...`（Go 原始字符串），脚本里**只允许**出现两个字面量占位 `{{REQUEST_B64}}`、`{{RESULT_B64}}`——两者都是 **base64 字母表**（`A-Za-z0-9+/=`），**不可能**逃出 PowerShell 单引号字符串 ⇒ 结构上免疫注入（满足 §9.1）。
2. **编码**：`b64(script)` → `utf16.Encode([]rune(...))` → 逐 rune 写 `uint16`（小端）→ `base64.StdEncoding`。不要用 `windows.UTF16FromString`（会带结尾 NUL，且语义不同）。
3. **结果文件**：父进程生成 `settingsDirectory()/hyperv/jobs/<jobID>/result.json`（目录 `0o700`），路径 base64 注入脚本；子进程**原子写**（先 `result.json.tmp` 再 `Move-Item -Force`），且用
   `[IO.File]::WriteAllText($path, $json, (New-Object Text.UTF8Encoding($false)))`
   ——**必须显式无 BOM**，否则 PowerShell 5.1 的 `Set-Content -Encoding UTF8` 会写 BOM，Go 侧 `json.Unmarshal` 直接失败。
4. **启动**：
   - `windows.GetCurrentProcessToken().IsElevated() == true`（`tun_preflight_windows.go:25` 先例）⇒ `exec.CommandContext(ctx, powershellPath, "-NoProfile","-NonInteractive","-ExecutionPolicy","Bypass","-EncodedCommand", b64)`，`SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}`。
   - 否则 ⇒ `ShellExecuteExW`（`shell32.dll` 懒加载，同 `nat_firewall_windows.go:143-185`）：`Verb="runas"`、`File=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`（**绝对路径**，避免 PATH 劫持与 EDR 误判）、`Parameters` = 上面那串固定参数（**不含任何用户数据**）、`Mask=0x40`（`SEE_MASK_NOCLOSEPROCESS`）、`Show=0`；`ERROR_CANCELLED` → 统一错误「用户取消了管理员权限请求」（沿用 `nat_firewall_windows.go:161-163` 的文案语义）。
5. **等待与结果**：`WaitForSingleObject(handle, timeoutMs)` + 轮询结果文件（100ms 一次，双保险），读之前 `os.Stat` 限制 ≤ 1 MiB；读不到文件但退出码非 0 ⇒ `script_failed` + 退出码。
6. **清理**：结束后 `os.RemoveAll(jobDir)`（路径由我们自己生成，**绝不**使用任何用户输入拼路径），失败只记日志。
7. **单飞**：整个运行器由一个 `sync.Mutex`（服务级）保护，避免两次 Create/Remove 交错触发两个 UAC（§9.5）。

### 3.5 内嵌脚本章程（设计稿，非最终实现）

```powershell
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$job = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('{{REQUEST_B64}}')) | ConvertFrom-Json
$resultPath = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('{{RESULT_B64}}'))
function Write-Result([hashtable]$payload) {
  $json = $payload | ConvertTo-Json -Depth 6 -Compress
  $tmp = $resultPath + '.tmp'
  [IO.File]::WriteAllText($tmp, $json, (New-Object Text.UTF8Encoding($false)))
  Move-Item -LiteralPath $tmp -Destination $resultPath -Force
}
try {
  Import-Module Hyper-V -ErrorAction Stop
  $switch = Get-VMSwitch -Name $job.switchName -ErrorAction Stop      # 只接受显式传入且存在的交换机
  if ($switch.SwitchType -ne 'External') { throw 'switch_not_external' }
  if ($job.action -eq 'create') {
    $created = @()
    for ($i = $job.startIndex; $i -lt ($job.startIndex + $job.count); $i++) {
      $name = '{0}{1:d2}' -f $job.prefix, $i
      $existing = Get-VMNetworkAdapter -ManagementOS -Name $name -ErrorAction SilentlyContinue |
                  Where-Object { $_.MacAddress }                        # 名字可能重复，必须非空 MAC
      if ($existing) {                                              # 幂等：同名同交换机直接复用
        if ($existing.SwitchName -ne $switch.Name) { throw 'name_conflict' }
        $created += @{ name = $name; switchName = $existing.SwitchName; macAddress = $existing.MacAddress }
        continue
      }
      Add-VMNetworkAdapter -ManagementOS -Name $name -SwitchName $switch.Name -ErrorAction Stop | Out-Null
      $adapter = Get-VMNetworkAdapter -ManagementOS -Name $name | Where-Object { $_.MacAddress } | Select-Object -First 1
      $created += @{ name = $name; switchName = $adapter.SwitchName; macAddress = $adapter.MacAddress }
    }
    Write-Result @{ ok = $true; adapters = $created }
  } elseif ($job.action -eq 'remove') {
    $removed = @()
    foreach ($name in $job.names) {
      $adapter = Get-VMNetworkAdapter -ManagementOS -Name $name -ErrorAction SilentlyContinue |
                 Where-Object { $_.MacAddress } | Select-Object -First 1
      if (-not $adapter) { $removed += @{ name = $name; switchName = ''; macAddress = '' }; continue }
      if ($adapter.SwitchName -ne $job.switchName) { throw 'switch_mismatch' }   # 二次确认
      Remove-VMNetworkAdapter -ManagementOS -Name $name -ErrorAction Stop
      $removed += @{ name = $name; switchName = $job.switchName; macAddress = $adapter.MacAddress }
    }
    Write-Result @{ ok = $true; adapters = $removed }
  } else { throw 'unknown_action' }
} catch {
  Write-Result @{ ok = $false; code = 'script_failed'; error = $_.Exception.Message }
}
```

脚本**只**调用：`Import-Module Hyper-V`、`Get-VMSwitch`、`Get-VMNetworkAdapter -ManagementOS`、`Add-VMNetworkAdapter -ManagementOS`、`Remove-VMNetworkAdapter -ManagementOS`。
**绝不**调用：`Remove-VMSwitch` / `Set-VMSwitch` / `New-VMSwitch` / `Restart-NetAdapter` / `Disable-WindowsOptionalFeature` / `netcfg` / 任何物理网卡操作。

---

## 4 Q2 服务 API 签名

新文件：`desktop/internal/services/hyperv_adapter.go`（与平台无关的编排）+ `hyperv_adapter_windows.go`（`//go:build windows` 的实现，含 ledger 与 runner 调用）+ `hyperv_ledger.go`（台账读写，可跨平台编译以便单测）。

### 4.1 类型（JSON tag 用 **camelCase**）

```go
// 与前端约定一致：旧的 VirtualAdapterStatus（virtual_adapter.go:29-38）就是 camelCase；
// 注意仓库里 AdapterView 是 snake_case（adapters.go:10-28）——两套并存，新类型统一取 camelCase。
type HyperVAdapterStatus struct {
    Name         string `json:"name"`                 // HypoMux-vnic-01
    AdapterID    string `json:"adapterId"`            // Windows 接口别名：vEthernet (HypoMux-vnic-01)
    MacAddress   string `json:"macAddress"`           // AA-BB-CC-DD-EE-FF（与 Get-NetAdapter 同格式）
    SwitchName   string `json:"switchName"`           // XuniUplink
    State        string `json:"state"`                // absent|creating|ready|failed
    Address      string `json:"address,omitempty"`    // 192.168.16.x
    PrefixLength int    `json:"prefixLength,omitempty"`
    Gateway      string `json:"gateway,omitempty"`
    Managed      bool   `json:"managed"`              // 台账命中（true 才允许删除）
    BatchID      string `json:"batchId,omitempty"`    // 「撤销本次创建」用
    CreatedAt    string `json:"createdAt,omitempty"`  // RFC3339
    LastError    string `json:"lastError,omitempty"`
}

type HyperVSwitch struct {
    Name                string `json:"name"`
    SwitchType          string `json:"switchType"`          // External|Internal|Private
    InterfaceDescription string `json:"interfaceDescription,omitempty"`
    AllowManagementOS   bool   `json:"allowManagementOs"`
    Usable              bool   `json:"usable"`              // External && AllowManagementOS
}

type HyperVAdapterSummary struct {                // 可选：给 UI 一次性拿全量
    Available  bool                `json:"available"`   // Hyper-V 可用且至少一个可用交换机
    Detail     string              `json:"detail,omitempty"`
    Switches   []HyperVSwitch      `json:"switches"`
    Adapters   []HyperVAdapterStatus `json:"adapters"`
    MaxBatch   int                 `json:"maxBatch"`     // 16
    TotalLimit int                 `json:"totalLimit"`   // 32（见 §4.3）
}
```

### 4.2 方法（Lead 给的四条 + 建议补充两条）

```go
func NewHyperVAdapterService(settings *SettingsService, adapters *AdapterService) *HyperVAdapterService

// List 返回「台账 ∪ 系统实况」的合集；不需要提权，不阻塞（net.Interfaces + 台账文件）。
func (s *HyperVAdapterService) List() ([]HyperVAdapterStatus, error)

// Create 批量创建 count 张（1..MaxBatch），命名从台账最大序号 +1 开始；
// 返回**立即态**（通常 state=creating），DHCP 由前端轮询 List() 等待。
func (s *HyperVAdapterService) Create(switchName string, count int) ([]HyperVAdapterStatus, error)

// Remove 删除单张；要求 台账命中 + 命名规范 + MAC 一致 + 交换机一致。
func (s *HyperVAdapterService) Remove(name string) error

// —— 建议补充（提高可用性，非契约强制） ——
func (s *HyperVAdapterService) Summary() (HyperVAdapterSummary, error) // List + Switches + 可用性
func (s *HyperVAdapterService) Switches() ([]HyperVSwitch, error)     // 供 UI 下拉，且 Create 的白名单来源
func (s *HyperVAdapterService) Shutdown()                             // 仅清理 job 目录与内存态，无副作用
```

`NewHyperVAdapterService` 的依赖选择理由：`settings` 用于台账目录（`settings.go:136`）与出口池写入（`:407`）；`adapters` 不是必须（因为 `adapters.go:80-82` 会丢弃无 IPv4 的接口），保留它只为拿 `Gateway/DNSServers`，实现里也**可以直接调同包的 `adapterPlatformMetadata()`**（`adapter_metadata_windows.go:19`）。

### 4.3 错误码（前端可映射）

```go
const (
    CodeHyperVUnavailable  = "hyperv_unavailable"   // Hyper-V 缺失 / 无可用交换机
    CodeSwitchNotFound     = "switch_not_found"     // 交换机不存在或不是 External
    CodeSwitchNotAllowed   = "switch_not_allowed"   // 不是可用交换机（白名单校验失败）
    CodeElevationCancelled = "elevation_cancelled"  // 用户在 UAC 点取消
    CodeBatchTooLarge      = "batch_too_large"      // count < 1 || count > 16
    CodeTotalLimitReached  = "total_limit_reached"  // 台账已满 32
    CodeNameConflict       = "name_conflict"        // 目标名已被非本程序创建的适配器占用
    CodeCreateFailed       = "create_failed"        // 脚本部分/全部失败，Detail 带第 k 张
    CodeCreateTimeout      = "create_timeout"       // 脚本超时（§9.4）
    CodeNotManaged         = "not_managed"          // 删除时三重校验未全中
    CodeRemoveFailed       = "remove_failed"
    CodeRemoveTimeout      = "remove_timeout"
    CodeDHCPTimeout        = "dhcp_timeout"         // 状态侧：60s 未拿到 IPv4（不删卡）
)

type HyperVError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
    Detail  string `json:"detail,omitempty"`
}
func (e *HyperVError) Error() string { return e.Message }
```

（沿用 `engineclient.RemoteError`（`client.go:24-30`，指针接收者）的既有风格；Wails 侧 `error` 会被序列化成字符串，前端可用 `code` 前缀匹配做分支——**若要结构化错误，需额外返回 `HyperVResult{ok,code,message,adapters}` 而不是 `error`**，这一条留给 Lead 决策，见 §12 决策项 D2。）

### 4.4 `List()` 的实现要点（明确不可复用 `AdapterService.List()`）

1. `net.Interfaces()` 全量扫描（**不用** `AdapterService.List()`，因为 `adapters.go:80-82` 会把「还没有 IPv4」的 vNIC 丢掉，导致 `creating`/`failed` 状态永远不可见）。
2. 对每个接口取 `HardwareAddr`、`Addrs()`（跳过 `169.254.0.0/16`，与 `adapters.go:66-69` 同规则）、`Index`；Gateway/DNS 用 `adapterPlatformMetadata()[index]`。
3. 与台账按 **接口别名**（`vEthernet (<name>)`）+ **MAC** 双键匹配：
   - 台账有 + 系统有 + 有 IPv4 → `ready`；无 IPv4 → `creating`（或在 `createdAt` 超过 60s 后 → `failed` + `dhcp_timeout`）。
   - 台账有 + 系统无 → `absent`（`Managed=true`，UI 给「清理记录」）。
   - 系统有 + 台账无 + 名字匹配规范 → `Managed=false`（**只展示，绝不允许删**）。
4. 排序：按名字序号。

### 4.5 `Create()` 编排（伪代码）

```
Create(switchName, count):
  if closed: return 「HypoMux 正在退出」                      # 与 mtu.go:135 / engine.go:562 同款文案
  if count < 1 || count > 16:                       -> HyperVError{CodeBatchTooLarge}
  s.mu.Lock(); defer s.mu.Unlock()                          # 单飞（§9.5）
  if !availability.ok:                              -> HyperVError{CodeHyperVUnavailable}
  if switchName 不在 Switches() 的可用列表:          -> HyperVError{CodeSwitchNotAllowed}
  ledger := loadLedger()                                     # hyperv/adapters.json
  if len(ledger.Adapters) + count > 32:             -> HyperVError{CodeTotalLimitReached}
  检查 runas 会话可行性（可选：先跑一次 no-op 探测，避免"创建一半才弹 UAC"）
  startIndex := 台账最大序号 + 1
  job := elevatedJob{Action:"create", SwitchName:switchName, Count:count,
                     Prefix:"HypoMux-vnic-", StartIndex:startIndex, JobID:newGUID()}
  result, err := runElevatedScript(ctx(90s), "hyperv-create", job)
  # 无论成败，先把脚本回报的适配器写进台账（保证"系统里有的卡一定在台账里"）
  for a := range result.Adapters: ledger.upsert(entry{Name:a.Name, Mac:a.MacAddress,
        SwitchName:a.SwitchName, BatchID:job.JobID, CreatedAt:now, State:"creating"})
  saveLedger(ledger)                                        # 原子写（见 §5.2）
  if err != nil || !result.OK:
      标记本批已创建的为 failed（lastError=err）
      -> 返回**部分结果 + HyperVError{Code:CreateFailed, Detail:"第 k/共 count 张失败"}**
  合并出口池：ids = 现存 selected + 新 <接口别名= vEthernet (<name>)>；weights 补 1
  settings.UpdateHome(settings.Get().Mode, settings.Get().Weighted, ids, weights)   # §8
  return List()                                             # 立即态（creating）
```

**顺序很重要**：脚本 → 台账 → 出口池。任何一步失败都保证「系统状态 ⊆ 台账」，绝不出现「系统里有卡但台账不知道」（那会导致永远删不掉或误判为 unmanaged）。

---

## 5 Q3 「是否由 HypoMux 创建」的判定

### 5.1 为什么不能用单一判据

- **只靠名字**：用户改名 / 别的软件也用 `HypoMux-vnic-*` ⇒ 会误删；且本机已实测 `xuni-01` **重名两条**（一条 MAC 为空）⇒ Hyper-V 的 `Name` 本身就不保证唯一。
- **只靠 MAC 前缀**：本机现有 11 张 `xuni-*` 的 MAC 全是 LAA（`F6/3E/DE/5A/16/C6/A2/56/06/82/76` 开头），**没有可用前缀空间**指认归属；且用户可能自己指定过 MAC。
- **只靠描述字段**：`Add-VMNetworkAdapter`/`Set-VMNetworkAdapter` **没有** notes/description 参数（§2.1 实测）⇒ 物理上没这个字段可用。

### 5.2 三重判定（权威 = 台账）

```go
type hyperVLedger struct {
    Version  int               `json:"version"`  // 1
    Adapters []hyperVLedgerEntry `json:"adapters"`
}
type hyperVLedgerEntry struct {
    Name       string `json:"name"`        // HypoMux-vnic-01
    MacAddress string `json:"macAddress"`  // AA-BB-CC-DD-EE-FF（创建后回写）
    SwitchName string `json:"switchName"`
    BatchID    string `json:"batchId"`
    CreatedAt  string `json:"createdAt"`
    State      string `json:"state"`
    LastError  string `json:"lastError,omitempty"`
}
// 落盘：settingsDirectory()/hyperv/adapters.json
//      settingsDirectory()/hyperv/adapters.json.tmp -> os.Rename（0o600；MkdirAll(dir,0o755)）
// 与 vnic_config.go:285-292 相同的原子提交写法（临时文件 + Rename + 失败 os.Remove）
```

删除**必须**同时满足（`Remove()` 内的校验顺序）：

1. 名字匹配规范 `^HypoMux-vnic-\d{2}$`（`strings.EqualFold` 比较，避免大小写差异）；
2. **台账命中**该名字（否则 `not_managed`，**即使名字看起来是我们的**）；
3. 系统实况 MAC（`net.InterfaceByName(adapterID).HardwareAddr`）与台账 MAC **逐字节相等**（否则 `not_managed`：说明卡被重建/复用，不能按我们的记录删）；
4. 台账 `switchName` 与 `Get-VMNetworkAdapter` 回报的 `SwitchName` 一致（脚本内 `throw 'switch_mismatch'`，§3.5）。

**重启后仍能识别**：台账是文件（`~/.hypomux/hyperv/adapters.json` 或 `HYPOMUX_DATA_DIR` 下，`settings.go:136-141`），跨进程/跨重启持久；`List()` 每次都从系统实况重算状态，台账只提供「归属 + 历史 MAC」。
**绝不误删用户卡**：`xuni-*`、`Container NIC *`、交换机自身条目（MAC 为空——所以脚本里所有匹配都带 `Where-Object { $_.MacAddress }`）、物理 `以太网`、以及任何「名字像我们的但台账没有」的卡，全部只读展示。

### 5.3 命名与序号分配

- 规范：`HypoMux-vnic-NN`（`NN` 两位十进制，`01..99`）；对应 Windows 别名 `vEthernet (HypoMux-vnic-NN)`（`selected_adapter_ids` 必须存这个**别名**，`adapters.go:83`）。
- 序号：`startIndex = 台账最大已用序号 + 1`；**不回收空洞**（避免与路由器侧 DHCP 租约的 MAC↔IP 记忆打架）；删除后序号若成为空洞，下次创建时优先复用最小空洞（可配置，默认不回收）。
- 与 `HypoMux-Tun`（引擎 TUN，`adapters.go:136` 只硬过滤这一个名字）无冲突。
- **MAC 策略**：默认让 Hyper-V **动态分配**（`Add-VMNetworkAdapter` 不传 `-StaticMacAddress`），创建后回读并写入台账。理由：静态 MAC 有「与局域网内既有 MAC 冲突」的风险（那正是本功能要绕开的按 MAC 限速场景的镜像风险），且动态池由 Hyper-V 保证唯一；台账记录 MAC 后归属判定同样成立。若要给路由器白名单一个稳定前缀，可作为 v2 可选项（`-StaticMacAddress 02:48:4D:..`，LAA 前缀；**本轮未验证** Hyper-V 是否接受任意 LAA）。**未验证**动态 MAC 池容量（默认池范围）与 32 张上限的关系。

---

## 6 Q4 DHCP 等待与状态机

### 6.1 状态取值与迁移

| 状态 | 含义 | 判定依据 | UI |
|---|---|---|---|
| `absent` | 台账有记录，系统里已无该适配器 | `net.Interfaces()` 找不到该别名 | 灰显 + 「清理记录」按钮 |
| `creating` | 已创建，等 DHCP | 适配器在、**无非 APIPA 的 IPv4** | 「等待路由器分配 IP（最多 60 秒）…」+ 进度条 |
| `ready` | 拿到 IP | 存在 IPv4 且非 `169.254.0.0/16` | 显示 IP/前缀/网关 + 「已加入出口池」 |
| `failed` | 超时或脚本报错 | `creating` 持续 > 60s，或台账 `lastError != ""` | 红字原因 + 「重试获取 IP」+「删除」 |

迁移：`(无) --Create--> creating --有IP--> ready`；`creating --60s--> failed`；`failed --重试成功--> ready`；任意 `--Remove--> 删除并出池`。

### 6.2 轮询与超时

- **谁轮询**：前端（`Create` 立即返回 `creating` 后，`setInterval` 每 **1s** 调 `List()`，最长 **60s**，随后停止并标 `failed`）。理由：① Wails 调用阻塞 UI，不能把 45s 等待塞进 `Create`；② `List()` 是纯本地（`net.Interfaces()` + 一个小 JSON），1s 一次无压力；③ 现有面板已是「轮询 status」的写法（`VirtualAdapterPanel.tsx:121`）。
- **超时值 60s**：DHCP 正常 < 5s，慢的交换机/路由器 10–20s；60s 是「比最坏正常情况宽 3 倍、又不至于让用户等到怀疑人生」的折中。可配置（`hyperv.config.json`），默认 60。
- **超时后策略：保留适配器，不自动删除**。理由：① 路由器可能稍后才分配（MAC 首次出现、地址池回收）；② 失败项往往需要用户拿 MAC 去路由器加白名单，删了就没得看；③ 删除需要**再一次 UAC**，在「什么都没做成」的情况下强制弹窗体验极差。UI 明确给两个出口：`重试`（可触发一次提权的 `ipconfig /renew "<别名>"`，见 §6.4）与 `删除`。
- **创建脚本自身的超时**：`Create` 的提权脚本预算 **90s**（16 张 × 单张 < 2s + UAC 等待余量）；父进程逻辑超时后按 §9.4 处理。

### 6.3 为什么不能用 `Get-VMNetworkAdapter ... IPAddresses`

实测**全为空**（§2.1）⇒ 必须走 `net.Interfaces()`（或 `Get-NetIPAddress`）。这条与「不需要提权就能看到 IP」正好契合：**状态读取全在未提权侧**，只有创建/删除需要 UAC。

### 6.4 可选的「重新获取 IP」

`ipconfig /renew "<别名>"` 通过同一个提权运行器执行（`elevatedJob{Action:"renew"}`，脚本里 `ipconfig /renew <quote>` 用**参数数组**语义的 `& ipconfig.exe /renew $alias`——`$alias` 来自 base64 JSON，不做字符串拼接）。**未验证** `ipconfig /renew` 对 Hyper-V 适配器是否需要提权/是否需要 `Restart-NetAdapter`。

---

## 7 Q5 失败回滚

### 7.1 策略：**停批 + 保留已成功 + 标记失败 + 显式撤销**

- **硬失败**（交换机不存在/非 External、MAC 池耗尽、cmdlet 抛错、UAC 取消）：**立即停止本批**，不再尝试后续张数（后一张同理必败，继续只会刷更多错误与更久 UAC）。
- **已成功的卡全部保留**（不静默删除）。理由：
  1. 目标是「拿到更多 IP 配额」，成功 k 张就实得 k 个配额，删除是净损失；
  2. 回滚本身要**再一次提权**，可能被拒或超时，把「部分成功」变成「部分成功 + 一次失败的删除」；
  3. 路由器侧已按 MAC 建立 DHCP 租约，删卡会让 MAC↔IP 记忆失效，下次重建可能拿到不同 IP（用户若已在路由器做白名单会更麻烦）。
- **软失败**（`dhcp_timeout`）**不阻断**后续创建。
- 失败项写入台账 `state=failed` + `lastError`（含第 k/共 n 张），UI 明确展示。
- **显式回滚**：`Remove(name)` 逐张；另提供「撤销本次创建」（按 `batchId` 过滤台账 → 逐张走同一个三重校验的删除路径）。这是**用户点出来的**破坏性动作，符合「提权 = 用户有意识」的原则。

### 7.2 边界情形

- **第一张就失败** ⇒ 什么都没创建，只返回错误（不写台账、不动出口池）。
- **UAC 被取消** ⇒ `elevation_cancelled`，无任何系统变更（脚本根本没跑）。
- **脚本超时但后台仍在跑** ⇒ 见 §9.4：返回 `create_timeout`，**不猜测**结果；UI 提示「创建可能仍在后台继续，请稍后刷新」，台账保持「已回报的」条目，下次 `List()` 以系统实况为准（**不自动补挂台账**：系统有、台账无 → `Managed=false`，用户可手动「接管为受管」——可选动作，默认不做）。
- **部分成功但出口池写入失败** ⇒ 台账里已有全部卡；返回「已创建但未加入出口池：<err>」，UI 给「重试加入出口池」按钮（重试只调 `UpdateHome`，不再提权）。

---

## 8 Q6 自动加入出口池

### 8.1 唯一正确入口

```go
current := s.settings.Get()                       // settings.go:152（RLock 内克隆）
ids    := mergeUnique(current.SelectedAdapterIDs, newAdapterIDs)   // 新 ID 是 vEthernet (HypoMux-vnic-NN)
weights := mergeWeights(current.AdapterWeights, newAdapterIDs, AdapterWeightDefault) // =1，settings.go:20
_, err := s.settings.UpdateHome(current.Mode, current.Weighted, ids, weights)        // settings.go:407-414
```

- `UpdateHome` → `updateHomeStrategy`（`settings.go:416-443`）会**整体覆盖** `Mode/Strategy/Weighted/SelectedAdapterIDs/AdapterWeights` ⇒ **`mode` 必须传 `current.Mode`**，否则会把用户的 `tun` 模式改掉。
- `updateHomeStrategy` 对每个 weight 校验 `[AdapterWeightMin, AdapterWeightMax] = [1,100]`（`settings.go:17-21`、`:426-431`）⇒ 新卡权重给 `AdapterWeightDefault=1` 最稳。
- `SelectedAdapterIDs` 经 `uniqueNonEmpty`（`settings.go:437`、`:828`）去重去空 ⇒ 幂等，重复调用安全。
- **禁止**走 `UpdateFields`：其白名单（`settings.go:265-310`，`default` 分支 `:305-307`）**不含** `selected_adapter_ids`/`adapter_weights`，会直接报「不支持通过设置页修改字段」。

### 8.2 锁与并发

- `SettingsService.mu sync.RWMutex`（`settings.go:84`）只保护内存态与落盘；`Get()`（`:152`）内部取 RLock 并 `cloneSettings`（`:798`），`UpdateHome` 内部取 Lock。
- 关键纪律：**不要持锁跨提权调用**。编排顺序固定为
  `① 提权创建（秒级，无锁） → ② 台账原子落盘 → ③ UpdateHome（毫秒级）`。
- 服务自身再用 `s.mu`（单飞，§9.5）串行化 Create/Remove，避免两次 UAC 叠加与台账竞写。
- 前端侧：`useEngineState.ts:123` 会把 UI 选择整体写回 `selected_adapter_ids`；因此在「创建后写池」与「用户手动改选择」之间**天然存在竞态**。设计上以「**以 `settings.Get()` 最新值为基线做合并**」为准（读-改-写，毫秒窗口），并在说明中提示：面板创建时应先禁用「选择网卡」交互。
- **落盘格式**：`writeSettingsFile`（`settings.go:760`）既有写法；任何新增字段都不需要（本设计**不新增 `AppSettings` 字段**）。

### 8.3 生效条件：必须重启聚合

`engineAdapters`/`engineChannels` 只在 `engine.start` 时计算（`engine.go:828`/`:854`；见 §2.2）⇒ 创建 vNIC 后**不会**自动进入运行中的聚合。设计：

1. 面板在创建成功后显示「已加入出口池（N 张），**重启聚合后生效**」+ 一个按钮（调既有 `engine.stop` + `engine.start`，前端已有能力）；
2. 若用户跳过重启，`List()` 里这些卡仍是 `ready`，但 `engine` 的 `Snapshot` 不会包含它们——**不要**伪造「已生效」；
3. 引擎重启用到的 `connect(ctx, current.SelectedAdapterIDs)`（`main.go:313`）会带上新卡。
- 每张 vNIC 会额外得到一个 `nic_vEthernet (HypoMux-vnic-NN)` 通道（`engine.go:1260-1264`）⇒ 路由规则可以把流量**指定到某一张 vNIC**（这就是「绕过按 MAC 限速拿多个 IP 配额」落到数据面的方式）。**未验证** 20+ 适配器时 `engineChannels` 与 `tun_config.go:180 socksOutbound`/`loopbackPort` 的端口分配是否仍成立。

### 8.4 「隐藏虚拟网卡」UX 冲突（必须决策，见 §12 决策项 D1）

- 新卡是 `vEthernet (...)`，`isVirtualAdapter()`（`adapters.go:142-156`，`:145` 标记 `vethernet`）⇒ `is_virtual=true`。
- `settings.go:73` 默认 `hide_virtual_adapters=true`，前端 `adapterVisibility.ts:6` 会把它们**从设置页/首页的网卡列表里过滤掉**，但 `useEngineState.ts:584` 会统计 `hiddenSelectedCount`。
- 两种处理（推荐 A）：
  - **A（推荐）**：新增的 Hyper-V 面板**自己管理**这批卡（列表、状态、删除），设置页的网卡列表继续隐藏它们；`hiddenSelectedCount` 的提示文案顺带说明「隐藏的虚拟网卡仍参与聚合」。
  - B：创建成功后**提示并建议**关闭 `hide_virtual_adapters`（走 `UpdateFields` 白名单里的 `hide_virtual_adapters`，`settings.go:293-294`），让它们在设置页可见可选。

---

## 9 Q7 安全与边界

### 9.1 无注入（结构性，而非"小心拼接"）

- 脚本文本是**常量**；所有可变数据（交换机名、张数、序号、名字列表、结果路径）经 **base64** 注入（字母表 `A-Za-z0-9+/=`，无法构成引号/反引号/`$`/分号/换行）⇒ 不可能逃出 PowerShell 单引号字面量。
- **明确不要**照抄 `nat_firewall_windows.go:131-132` 的字符串拼接写法（那里 `executable` 是我们自己算出的、也补了引号，但作为模板很危险）。
- 脚本内对适配器名/交换机名一律当**数据**用（`$job.switchName`、`$job.names`），不漏进 `Invoke-Expression`、不拼 `cmd /c`。
- 文件访问一律 `-LiteralPath` / `[IO.File]`（`ConvertTo-Json` 输出到我们生成的绝对路径）。

### 9.2 白名单与最小权限面

- 交换机名必须来自 `Switches()`（`Get-VMSwitch` 枚举结果）且 `SwitchType -eq 'External' && AllowManagementOS` ⇒ 「只允许操作显式传入的交换机名」在内核语义上变成「只允许操作枚举出来的那个对象」。
- 脚本只调用 §3.5 列出的 5 个 cmdlet（`Import-Module`/`Get-VMSwitch`/`Get-VMNetworkAdapter`/`Add-VMNetworkAdapter`/`Remove-VMNetworkAdapter`），**不允许** `Remove-VMSwitch`、`Set-VMSwitch`、`Restart-NetAdapter`、`Disable-*`、`netcfg`、`netsh`。
- `powershell.exe` 用**绝对路径**（`%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`）；`-NoProfile -NonInteractive` 防止 profile 注入；`-ExecutionPolicy Bypass` 只作用本次 `-EncodedCommand`。

### 9.3 删除二次确认

`Remove(name)` 的四重校验见 §5.2（名字规范 → 台账命中 → MAC 逐字节相等 → 交换机一致），其中「MAC 相等」由**父进程用系统实况**先判一次、脚本再判一次交换机；二者任一不符即 `not_managed`，**不执行删除**。

### 9.4 超长/半行输出与超时强杀（必须写明的限制）

- 结果**不走 stdout**（`ShellExecuteExW+runas` 拿不到 stdout，见 §3.1）⇒ 半行/超长解析问题结构性消失。
- 结果文件读取：`os.Stat` 限制 **≤ 1 MiB**，超限即 `script_failed` 且不解析；`json.Unmarshal` 到结构体（未知字段忽略、必填缺失校验）；文件不存在/为空 ⇒ 失败。
- 超时：父进程逻辑截止（create 90s / remove 30s）+ `WaitForSingleObject(timeoutMs)`；到点返回 `create_timeout`/`remove_timeout`。
- **⚠️ 已知限制（必须在 UI 文案里体现）**：未提权的父进程**可能杀不掉**已提权的子进程（`privileged_windows.go:390-404` 记录了 `ERROR_ACCESS_DENIED` 这一现象）⇒ 脚本必须**自带界**（每张 cmdlet `-ErrorAction Stop`，循环最多 16 张，最坏几秒）且**幂等**（同名同交换机直接复用，§3.5 的 `if ($existing)` 分支）；父进程超时后**只报不改**，由用户刷新。
- 若将来要「可杀」：唯一可靠途径是把执行方搬到常驻服务（§3.1 方案 d），或给提权子进程一个带超时的 `Start-Process -Wait` 包装——**本轮均未验证**。

### 9.5 并发与资源

- 服务级 `s.mu`：Create/Remove/撤销 三者串行（UAC 弹窗 + 台账写不并发）。
- 每次操作一个 `hyperv/jobs/<jobID>/` 目录，结束时 `os.RemoveAll`（路径自造，不含用户输入）。
- `Shutdown()` 只清内存态与 job 目录；**不做**任何提权动作（避免退出时弹 UAC）⇒ 与 `main.go:152-157` 的关闭闭包天然兼容。

---

## 10 Q8 与旧实现的替换/删除清单

### 10.1 删除（旧 Wintun 虚拟网卡方案，共 3 个桌面文件 + 1 个引擎包）

| 文件 | 行数 | 说明 |
|---|---|---|
| `desktop/internal/services/virtual_adapter.go` | 201 | 整文件删（旧 `VirtualAdapterService`） |
| `desktop/internal/services/vnic_config.go` | 128 | 整文件删（sing-box/Wintun 配置生成） |
| `desktop/internal/engineclient/vnic.go` | 80 | 整文件删（`VNICCreate/Status/Remove`） |
| `engine/internal/vnic/manager.go` | 343 | 整个包删 |
| `engine/internal/vnic/manager_test.go` | 370 | 同上 |

### 10.2 引擎侧改动（删除，无新增）

| 位置 | 内容 |
|---|---|
| `engine/internal/api/v1/types.go:31-33` | `MethodVNICCreate/Status/Remove` 常量删除 |
| `engine/internal/api/v1/types.go:61-63` | `Capabilities()` 里的三项删除（`contract_test.go:71` 会断言集合一致） |
| `engine/internal/api/v1/types.go:286-325` | `VNICCreateParams/VNICStatus/VNICCreateResult`（含 `Config()`）删除 |
| `engine/internal/api/v1/contract_test.go:214-272` | 三个 case 分支删除 |
| `engine/internal/server/server.go:25` | `vnic` import 删除 |
| `engine/internal/server/server.go:44-50` | `vnicManager` 接口删除 |
| `engine/internal/server/server.go:61` | `vnic vnicManager` 字段删除 |
| `engine/internal/server/server.go:89` | `vnic.NewManager(...)` 删除 |
| `engine/internal/server/server.go:224-229` | 三路 dispatch 删除 |
| `engine/internal/server/server.go:814-976` | `createVNIC`/`statusVNIC`/`removeVNIC`/`validateVNICCreate`/`vnicStatus`/`isStaleVNICAdapterError`/`handleVnicLog` 删除 |
| `engine/internal/server/server.go:1023-1026` | `host.shutdown` 里的 vnic keeper 停止段删除（**注意**：`host.shutdown` 仍要停 TUN 等其它资源） |
| `engine/internal/server/server_test.go:1022-1312` | vnic 生命周期测试段删除 |
| `engine/internal/tun/supervisor.go:32-37` | 只删 `VNICInterfaceName` 常量（`supervisor` 本体保留） |
| `protocol/v1/manifest.json:76,82,88` | `vnic.create`/`vnic.status`/`vnic.remove` 三块删除（**error_codes 数量不变**，与 `00-frozen-interface.md` 一致） |

### 10.3 桌面侧改动（替换）

| 位置 | 动作 |
|---|---|
| `desktop/main.go:150` | `virtualAdapterService := services.NewVirtualAdapterService(engineService)` → `hypervService := services.NewHyperVAdapterService(settingsService, adapterService)` |
| `desktop/main.go:156` | `virtualAdapterService.Shutdown()` → `hypervService.Shutdown()`；**必须仍在 `engineService.Shutdown()`（`:157`）之前**（虽然新服务不依赖引擎，但保持既有顺序以免将来回退） |
| `desktop/main.go:188` | `app.RegisterService(application.NewService(virtualAdapterService))` → `...NewService(hypervService)`；绑定的 TS 文件名随之变为 `hypervadapterservice.ts` |
| `desktop/main.go:211`/`:307`/`:313` | **不动**（它们用的是 `SelectedAdapterIDs`，新方案天然复用） |

### 10.4 前端改动（替换）

| 位置 | 动作 |
|---|---|
| `desktop/frontend/src/components/vnic/VirtualAdapterPanel.tsx` | 397 行整文件替换（新面板：交换机选择、张数、批量创建、状态轮询、逐张删除、撤销本次） |
| `desktop/frontend/src/components/vnic/VirtualAdapterPanel.test.tsx` | 240 行整文件替换（mock 形状改为 `HyperVAdapterStatus`） |
| `desktop/frontend/src/components/vnic/vnic.css` | 13 行，可复用或随目录改名 |
| `desktop/frontend/src/platform/services.ts:7` | import 改用 `hypervadapterservice` |
| `desktop/frontend/src/platform/services.ts:24` | 类型 import `VirtualAdapterStatus as GeneratedVirtualAdapterStatus` → `HyperVAdapterStatus` |
| `desktop/frontend/src/platform/services.ts:350-354` | `virtualAdapter:{create,status,remove}` → `hypervAdapter:{list,create,remove,switches,summary}` |
| `desktop/frontend/src/pages/HomePage.tsx:26` | `import { VirtualAdapterPanel } from "../components/vnic/VirtualAdapterPanel"` → 新组件路径 |
| `desktop/frontend/src/pages/HomePage.tsx:249` | 挂载点 `<VirtualAdapterPanel ... />` → 新组件 |
| `desktop/frontend/bindings/**/virtualadapterservice.ts` + `models.ts` | **生成物**：删除旧绑定、重新 `wails3 generate bindings` 产出 `hypervadapterservice.ts` |

### 10.5 文档/报告

| 位置 | 动作 |
|---|---|
| `reports/vnic/00-frozen-interface.md:1-146` | 顶部加「**已作废**（方向反转，见 62-）」标注（`reports/` 不入 git，仅内部留痕） |
| `reports/vnic/13-desktop-vnic.md` | 同上标注（我上一轮的 T4 交付） |

### 10.6 落地顺序（一个 PR 内）

`新增 desktop 服务 + 面板`（可编译可自测）→ `删除引擎 vnic 包/RPC + manifest`（`engine` 与 `desktop` 同批，否则运行期 `method_not_found`）→ `删旧桌面三文件 + main.go 三行替换` → 生成绑定 → `go test ./...` + `go vet` + `gofmt -l`（CI 门：`.github/workflows/build.yml:168-186`，`gofmt` 列出即失败）。

---

## 11 未验证项与风险（必须真机验证）

1. **未实际创建/删除任何适配器**（本轮只读硬约束）⇒ `Add-VMNetworkAdapter -ManagementOS -Name ... -SwitchName ...` 的真实行为、创建耗时、MAC 分配方式**全部未验证**。
2. **DHCP 时延分布未验证**：60s 预算是估计值；`ipconfig /renew` 是否需要提权、是否对 Hyper-V 适配器有效**未验证**。
3. **提权子进程写、未提权父进程读结果文件的 ACL 未验证**（同一个用户 SID，理论可行；`ShellExecuteExW+runas` 的默认工作目录是 `System32`，所以脚本内**全用绝对路径**已经规避）。
4. **父进程能否 `TerminateProcess` 已提权子进程未验证**（`privileged_windows.go:390-404` 的现象说明很可能不行）⇒ 设计上以「脚本自带界 + 幂等 + 超时只报不改」兜底。
5. **Hyper-V 动态 MAC 池容量**、以及 32 张总上限是否安全**未验证**。
6. **未提权能否只读 `Get-VMSwitch`/`Get-VMNetworkAdapter`**：本机当前会话是提权态，无法区分。故 `List()` 设计成完全不依赖它们（只 `net.Interfaces()` + 台账）；`Switches()` 需要提权/模块，应在未提权时**优雅降级**（返回空 + `available=false`）。
7. **20+ 适配器时引擎侧承载**：`engineChannels`（`engine.go:1238-1266`）会为每张卡建一个 `nic_<名>` 通道，`tun_config.go:180` 的 `socksOutbound` + `loopbackPort` 端口分配上限**未验证**。
8. **与 `HypoMux-Tun` 的路由/优先级共存**：issue #75（`docs/validation/tun-startup-dns-address-fix.md:6`）记录过 Hyper-V 接口与 TUN 固定地址的冲突；本方案 vNIC 无固定地址，但**路由跃点/自动跃点**与 TUN 的交互需真机验证。
9. **前端 Wails 绑定生成**：本机无 `node_modules`/bindings 生成链路，`hypervadapterservice.ts` 的形状未验证（依赖 Wails 对 `[]T` 返回值的序列化）。
10. **`xuni-01` 重名两条**这一既有脏数据提示：**任何**按名字定位的实现都不可靠；本设计的 `Where-Object { $_.MacAddress }` 只是缓解，未验证 `Get-VMNetworkAdapter -Name` 在重名时的返回顺序。

---

## 12 决策项（请 Lead 定案）

| 编号 | 决策项 | 我的建议 |
|---|---|---|
| **D1** | `hide_virtual_adapters` 默认为 `true`（`settings.go:73`），新 vNIC 在设置页不可见（`adapterVisibility.ts:6`） | 取 **A**：新面板自管列表；设置页保持隐藏；`hiddenSelectedCount` 文案补一句「隐藏的虚拟网卡仍参与聚合」 |
| **D2** | 错误返回形态：`error`（Wails 序列化成字符串，前端靠 code 前缀匹配） vs 返回 `HyperVResult{ok,code,message,adapters}` | 取 **返回结构化 `HyperVResult`**（前端分支更稳，也便于"部分成功"表达）；`Remove` 可保留 `error` |
| **D3** | 总上限（32）与单批上限（16） | 采纳；数值可调，写进 `hyperv` 配置 |
| **D4** | 是否需要「撤销本次创建（按 batchID）」 | 建议**要有**：本功能天然会一次建很多卡，逐张删体验差 |
| **D5** | MAC 静态 vs 动态 | 取**动态 + 台账记录**（§5.3）；静态作为 v2 可选 |
| **D6** | 删除后序号是否回收复用 | 默认**不回收**（避免路由器侧 MAC↔IP 记忆错位），可在设置里开 |
| **D7** | 是否本轮就准备 helper 子命令（`--hyperv-helper`） | **不需要**（提权目标是 `powershell.exe`）；将来要做「可杀/直读 stdout」时再加 |

---

## 13 附：关键函数签名速查（供实现者直接誊抄）

```go
// desktop/internal/services/hyperv_adapter.go
type HyperVAdapterService struct {
    settings *SettingsService
    adapters *AdapterService
    mu       sync.Mutex   // 单飞：Create/Remove/撤销 串行
    closed   bool
}
func NewHyperVAdapterService(settings *SettingsService, adapters *AdapterService) *HyperVAdapterService
func (s *HyperVAdapterService) List() ([]HyperVAdapterStatus, error)
func (s *HyperVAdapterService) Create(switchName string, count int) ([]HyperVAdapterStatus, error)
func (s *HyperVAdapterService) Remove(name string) error
func (s *HyperVAdapterService) Switches() ([]HyperVSwitch, error)
func (s *HyperVAdapterService) Summary() (HyperVAdapterSummary, error)
func (s *HyperVAdapterService) Shutdown()

// 内部
func (s *HyperVAdapterService) scanHostAdapters() ([]hostAdapter, error)   // net.Interfaces + HardwareAddr + Addrs
func (s *HyperVAdapterService) loadLedger() (hyperVLedger, error)
func (s *HyperVAdapterService) saveLedger(l hyperVLedger) error            // tmp + os.Rename，0o600
func (s *HyperVAdapterService) applyPool(names []string, add bool) error   // UpdateHome(…)
func hyperVAdapterName(index int) string                                   // "HypoMux-vnic-%02d"
func hyperVAdapterAlias(name string) string                                // "vEthernet (" + name + ")"
func isManagedHyperVName(name string) bool                                 // ^HypoMux-vnic-\d{2}$

// desktop/internal/services/elevated_script_windows.go
func runElevatedScript(ctx context.Context, kind string, job elevatedJob, timeout time.Duration) (elevatedResult, error)
func powershellPath() string                       // %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe
func encodePowerShellCommand(script string) string // UTF-16LE + base64
func currentProcessElevated() bool                 // windows.GetCurrentProcessToken().IsElevated()

// desktop/internal/services/hyperv_ledger.go
func hyperVLedgerPath() string                     // filepath.Join(settingsDirectory(), "hyperv", "adapters.json")
func hyperVJobDirectory(jobID string) string
```
