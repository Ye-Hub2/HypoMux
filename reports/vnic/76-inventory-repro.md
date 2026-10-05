# 76 · 虚拟网卡页「无法读取 Hyper-V」真机复现与修复

- 日期：2026-10-03
- 机器：`DESKTOP-*` / Windows，Windows PowerShell **5.1.19041.5129**，Hyper-V 模块 2.0.0.0
- 复现基线：`fc821b1`（v2.7.0）
- 结论一句话：**不是 Hyper-V 的问题，也不是提权的问题，是我们把 base64 payload 当成命令行位置参数传给了 `-EncodedCommand`，而 powershell.exe 5.1 根本不接受 `-EncodedCommand` 之后追加的 token —— 它把那个 token 当成「第二条命令」，打印用法、退出码 `0xFFFD0000`，于是脚本从未拿到 `$args[0]`，结果文件从未生成。**

---

## 1. 复现（真实代码路径，临时测试文件）

在 `desktop/internal/services` 下临时新增 `zz_manual_repro_test.go`，用**生产构造函数**造服务，
直接调 `readInventory()` / `Switches()` / `runScript(hypervEnvelope{Op:"inventory"})`，把 error 原样打印。

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
$env:GOTOOLCHAIN="auto"
$env:GOPROXY="https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct"
Set-Location <repo>
go -C desktop test -count=1 -run 'TestZZManualReproInventory|TestZZManualReproRunScriptRaw|TestZZManualReproDirectShell' -v ./internal/services/
```

### 1.1 真实错误原文（修复前）

```
REPRO data dir      = %USERPROFILE%\.hypomux\hyperv
REPRO job dir       = %USERPROFILE%\.hypomux\hyperv\jobs
REPRO platform ok   = true
REPRO elevated      = true
REPRO readInventory elapsed = 197.7492ms
REPRO readInventory err     = script_failed：脚本执行失败：PowerShell 以退出码 4294770688 结束
REPRO readInventory type    = *services.HyperVError
REPRO adapters = []
REPRO switches = []
REPRO Switches elapsed = 0s err = hyperv_unavailable：无法读取 Hyper-V 虚拟交换机：script_failed：脚本执行失败：PowerShell 以退出码 4294770688 结束

RAW newHypervJobPath err=<nil> path=%USERPROFILE%\.hypomux\hyperv\jobs\job-1791044871199200100-2997dda1.json
RAW runScript err  = script_failed：脚本执行失败：PowerShell 以退出码 4294770688 结束
RAW result != nil  = false
RAW result file gone: GetFileAttributesEx %USERPROFILE%\.hypomux\hyperv\jobs\job-...json: The system cannot find the file specified.
```

把 stdout/stderr 接出来（`TestZZManualReproDirectShell`）才看到真正的原因 —— powershell.exe 打的是**自己的用法帮助**：

```
DIRECT runErr = exit status 0xfffd0000
DIRECT output = "处理 -Command 或 -EncodedCommand 时出现参数错误 ..."   ← 完整的 PowerShell 用法横幅
```

用 GBK 正确解码后，**stderr 原文**：

```
Cannot process command because a command is already specified with -Command or -EncodedCommand.
```

中文报错机（本机系统语言 zh-CN）：
> 处理 -Command 或 -EncodedCommand 时出现参数错误。
> 无法处理命令，因为已使用 -Command 或 -EncodedCommand 指定了命令。

**退出码 `4294770688` = `0xFFFD0000`**（PowerShell「参数错误 / 打印用法」的固定退出码）。
这就是前端 i18n 那句「无法读取 Hyper-V 交换机列表…」兜底文案盖住的真实错误。

### 1.2 决定性对照实验

同一台机器、同一份脚本正文，只差一个尾随 token：

| 命令 | 退出码 | 结果 |
|---|---|---|
| `powershell.exe -NoProfile -NonInteractive -EncodedCommand <b64>` | `0` | `ARGS_COUNT=0`，脚本正常跑完 |
| `powershell.exe -NoProfile -NonInteractive -EncodedCommand <b64> PAYLOAD-TOKEN-123` | `-196608` (= `0xFFFD0000`) | 打印用法横幅 + `Cannot process command because a command is already specified with -Command or -EncodedCommand.` |

**`-EncodedCommand` 之后的位置参数在 PowerShell 5.1 上是非法的**，这一点和「Hyper-V 有没有装」「提没提权」完全无关。

---

## 2. 根因

`desktop/internal/services/hyperv_adapter_windows.go`（修复前 185–198 行，现 246 行）：

```go
func hypervPowerShellCommand(payload []byte) (string, []string, error) {
	...
	arguments := []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(hypervUTF16LE(hypervPowerShellScript)),
		base64.StdEncoding.EncodeToString(payload),   // ← 致命：-EncodedCommand 之后的位置参数
	}
	return powershell, arguments, nil
}
```

失败链条，逐段对应代码：

1. `hyperv_adapter_windows.go:246` `hypervPowerShellCommand` 把 payload base64 追加成第 6 个命令行 token。
2. powershell.exe 5.1 把它判成「第二条命令」，打印用法横幅，**以 `0xFFFD0000` 退出**，`-EncodedCommand` 里的脚本**根本没执行**。
3. 脚本里的 `Read-Envelope`（`hyperv_adapter.go:1777`）读 `$args[0]` —— `$args` 恒为空 → 返回 `$null` → `exit 2`。
4. 于是 `hypervRunDirect`（`hyperv_adapter_windows.go:80`）拿到非零退出码，包装成
   `fmt.Errorf("PowerShell 以退出码 %d 结束", code)`（`hyperv_adapter_windows.go:101`）。
5. `runScript`（`hyperv_adapter.go:959`）随后去读结果文件 —— 文件**从未被写过**，
   于是走 `mapHypervRunError` 分支返回 `script_failed：脚本执行失败：PowerShell 以退出码 4294770688 结束`。
6. `readInventory`（`hyperv_adapter.go:881`）把它缓存 5s，`Switches()`（`:1096`）包成
   `hyperv_unavailable：无法读取 Hyper-V 虚拟交换机：…`，前端 i18n 兜底文案再盖一层。

**为什么前端的通用文案「误导成没装 Hyper-V / 没提权」**：错误字符串里没有任何可辨识信息
（只有一句「以退出码 4294770688 结束」），前端只能落到通用分支。

**注意影响面比「读取」更大**：同一条链路也被 `create`（`:1298`）、`remove`（`:1586`）、
`waitip`（`:1504`）使用，也就是说**这三个 op 目前同样收不到 payload、同样必然失败**。
本轮按 lead 的指示「写路径保持现状不动」处理，**它们仍带着同一个缺陷**，详见 §6。

### 已排除的怀疑方向（实测）

| 怀疑 | 实测结论 |
|---|---|
| 需要管理员权限 | 否。本机测试进程 `hypervProcessElevated() = true`，但对照组用**非提权** powershell 也一样被拒；问题在参数解析，与令牌无关 |
| 超时 | 否。`hypervReadScriptTimeout = 60s`，实际耗时 **197ms** 就退了 |
| 数据目录 / 结果文件目录没建 | 否。`newHypervJobPath()` 返回 `MkdirAll` 成功，目录存在，只是文件从没被写 |
| 结果文件 BOM / 编码 | 否。文件压根不存在 |
| `ConvertTo-Json` 枚举序列化 | 否。脚本从未执行到那一步（修复后实测 `SwitchType`→`"External"`、`AllowManagementOS`→`true`，JSON 形状正常） |
| `Get-NetAdapter` 需要管理员被混进 inventory | 否。它在读取路径里只有 `-ErrorAction SilentlyContinue`，且实测能取到值 |

---

## 3. 修法（读取路径改成最小可用实现）

lead 的判断是对的，而且根因正好支持这个方向：**读取路径没有任何可变数据** ——
正文就 `Get-VMSwitch` 与 `Get-VMNetworkAdapter -ManagementOS` 两条只读查询。
所以整条命令行可以 100% 常量、零插值，注入面天然为零，也就不需要
base64 注入、不需要结果文件协议、不需要原子提交、不需要 `-EncodedCommand`、不需要 `runas`。

### 3.1 改动清单（只碰 `desktop/internal/services/hyperv_adapter*.go`）

| 位置 | 改动 |
|---|---|
| `hyperv_adapter.go:2007` | **新增**常量 `hypervReadInventoryScript` —— 只读脚本正文，`[Console]::Out.WriteLine(ConvertTo-Json -InputObject $state -Compress -Depth 4)` |
| `hyperv_adapter.go:2049` | **新增**纯函数 `hypervParseInventoryOutput(stdout []byte) (*hypervScriptResult, error)` |
| `hyperv_adapter.go:2073` | **新增**`hypervOutputRejected(reason, raw)` —— 解析失败时把 stdout 原文塞进错误 |
| `hyperv_adapter.go:881` | `readInventory()` 从 `s.runScript(…Op:"inventory"…)` 改走 `s.readHypervInventory()` |
| `hyperv_adapter.go:928` | **新增**`readHypervInventory()`：带 ctx 超时 → `hypervInventorySnapshot` → 失败映射 |
| `hyperv_adapter.go:959` | `runScript` 注释更新：现在是**写/等待**路径的唯一入口 |
| `hyperv_adapter_windows.go:72` | **新增**`hypervInventorySnapshot()` —— exec 一条常量 `powershell.exe … -Command <常量>`，接 stdout |
| `hyperv_adapter_windows.go:109` | **新增**`hypervOutputTail()` |
| `hyperv_adapter_other.go` | **新增**非 Windows 桩：直接返回 `errHypervUnsupported` |
| `hyperv_adapter_test.go` | **新增**两条纯函数单测（见 §5） |

**写路径（`create` / `remove` / `waitip`）一行未动**，仍然走原来的
「常量脚本 + base64 注入 + 结果文件 + 原子提交 + runas」。

### 3.2 新执行形态

```go
command := exec.CommandContext(ctx, powershell,
    "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
    "-Command", hypervReadInventoryScript)   // 完全常量，零插值
configureBackgroundCommand(command)
command.Stdout = &stdout
command.Stderr = &stderr
```

`runScript` 链路**完全不再参与**读取：不再有 job 目录、不再有结果文件、不再有 `-EncodedCommand`、
不再有 `$args[0]`、不再有 `Move-Item` 原子提交。

### 3.3 安全性：为什么零插值 = 零注入面

- 命令行字符串 100% 来自 Go 常量，**没有任何格式化动词、没有 `%s`、没有拼接**；
- 脚本正文里**只有单引号字符串、没有任何双引号**（已写成断言，见 §5）——
  这样 `exec` 的参数转义与 PowerShell 自己的引号解析两次处理不会互相打架；
- 唯一的外部输入是 Hyper-V 自己的返回值，全部经 `[string]` 转换后放进 `ConvertTo-Json` 生成的结构化对象，
  不参与命令拼接。

这条安全论证与「读取路径零插值」是同一件事：**正因为读取路径没有可变数据，才可能做到零插值；
正因为零插值，注入面才为空。** 写路径有 MAC / 名字 / 别名等可变数据且必须提权，那套 base64 注入是必需的
（其注入防护靠结构 —— base64 字母表里没有单引号、反引号、`$`），刻意保留。

### 3.4 顺带修掉的一个字段缺陷

只读脚本里原本的兜底是 `Get-NetAdapter -Name $uplink`，但 `$uplink` 取自
`VMSwitch.NetAdapterInterfaceDescription` —— 它是网卡的 **InterfaceDescription**
（本机实测 `Realtek Gaming 2.5GbE Family Controller`），不是别名，按 `-Name` 匹配永远落空。
本机 `VMSwitch` 上**根本不存在** `NetAdapterName` 属性（访问恒为空），真正可用的是
`NetAdapterInterfaceDescription` / `NetAdapterInterfaceGuid`。已改为按 `-InterfaceDescription` 匹配，
`XuniUplink` 的 `netAdapterName` 现在能正确解析出宿主网卡别名 **`以太网`**。

---

## 4. 修复前后对比（同一台真实机器）

修复前（第 1 节）：`readInventory err = script_failed：脚本执行失败：PowerShell 以退出码 4294770688 结束`，
`switches = []`。

修复后（同样的临时测试文件、同样调真实代码路径）：

```
REPRO readInventory elapsed = 2.8647752s
REPRO readInventory err     = <nil>
REPRO adapters = [Container NIC 9d844023(<MAC>, Default Switch) xuni-01(<MAC>, XuniUplink)
                     xuni-02(<MAC>) xuni-03(<MAC>) xuni-04(<MAC>) xuni-05(<MAC>)]
REPRO Switches elapsed = 0s err = <nil>
REPRO switch -> name="Default Switch" type="Internal" allowMgmtOs=true uplink="" netAdapter=""
REPRO switch -> name="XuniUplink"    type="External" allowMgmtOs=true uplink="Realtek Gaming 2.5GbE Family Controller" netAdapter="以太网"
```

- **读到的交换机：`XuniUplink`（External / AllowManagementOS=true / 绑定 Realtek Gaming 2.5GbE Family Controller / 宿主网卡 `以太网`）与 `Default Switch`（Internal / AllowManagementOS=true）。**
- **读到的 Hyper-V 网卡对象：6 条**（Container NIC 9d844023 + xuni-01..xuni-05）。
  用户手工 `Get-VMNetworkAdapter -ManagementOS` 看到的是 7 行，差的第 7 行是 reports/vnic/60 记录过的
  **MAC/DeviceId 全空的幽灵记录**，脚本按既有规则过滤掉了，属预期行为。
- **读到的宿主机网卡：8 个别名 / 7 个 MAC**
  （`vEthernet (xuni-01..05)`、`vEthernet (Default Switch)`、`以太网`、`Loopback Pseudo-Interface 1`）。
- `List()` 返回 `err=<nil>`、**0 行**：符合冻结契约 §3.4 —— 台账为空，且 `xuni-*` 不符合
  `HypoMux-vnic-` 命名规范，属「台账外的用户网卡，只读不展示/不接管」，不是缺陷。
- 单次耗时 **2.86s**（首次含 `Import-Module Hyper-V` 冷启动），远低于 60s 预算；5s 缓存照常生效。

---

## 5. 防回归单测

新增两条，**全是纯函数，不 exec powershell、不读 Hyper-V**，因此在 CI 的
windows-2025 runner（未必有 Hyper-V）上同样能编过并通过：

1. `TestHypervReadInventoryScriptIsConstantAndReadOnly`（`hyperv_adapter_test.go:1030`）
   断言只读脚本正文**不含**任何会改动系统的 cmdlet（`Add/Remove-VMNetworkAdapter`、
   `Remove/Set/New-VMSwitch`、`Restart/Disable-NetAdapter`、`netcfg`、`New-NetRoute`、
   `Set-DnsClientServerAddress` 等）、**不含双引号**、**不含** `FromBase64String` /
   `EncodedCommand` / `WriteAllText` / `Move-Item`（即读取路径不得回退到写路径那套重机制），
   同时**必须含** `Get-VMNetworkAdapter -ManagementOS`、`Get-VMSwitch`、
   `ConvertTo-Json -InputObject $state -Compress -Depth 4`、`adapters = @($adapters)`、
   `switches = @($switches)`。
2. `TestHypervParseInventoryOutput`（`hyperv_adapter_test.go:1085`）
   六个子用例：真实形状（含 `XuniUplink` / `Default Switch` 两条，验证单元素数组不退化成对象）、
   空集合、脚本自报 `ok=false`、BOM + 前置噪声、空/纯空白 stdout、
   以及**坏 JSON 必须把 stdout 原文带回错误**（这次故障的直接教训：
   「以退出码 X 结束」等于什么都没说）。

跨平台核验：`GOOS=linux GOARCH=amd64 go -C desktop build ./internal/services/` → 退出码 **0**。
（`GOOS=linux go -C desktop vet ./...` 会失败，但那是**既有**问题，与本次改动无关：
`desktop/internal/services/engine_integration_test.go:70` 引用了 windows-only 符号
`proxyMarkerPath` 且该文件没有 build tag，来自更早的提交 `b3eadd2`；
`GOOS=linux go -C desktop build ./...` 同样失败，原因是 wails 框架自身
`pkg/application/menu_linux.go:7: undefined: pointer`，stash 掉本次改动后同样失败。）

---

## 6. 遗留（本轮刻意未动，按 lead 指示「写路径保持现状」）

**`create` / `remove` / `waitip` 三个 op 仍然带着 §2 的同一个根因。**
它们与修复前的 inventory 走完全相同的 `hypervPowerShellCommand`
（`hyperv_adapter_windows.go:246`，尾随 payload token 那行），所以 payload 同样到不了 `$args[0]`，
脚本同样在 `Read-Envelope` 处返回 `$null` 后 `exit 2`，结果文件同样不会被写。

也就是说：**读现在通了，写还通不了。** 后续修法（不在本轮范围）：

```
在 -EncodedCommand 之前先播种 $args，而不是靠命令行位置参数：
    "$args = @('<payload base64>')\n" + hypervPowerShellScript
```

安全性与现有约束一致：base64 字母表只有 `A-Za-z0-9+/=`，不含单引号、反引号、`$`，
结构上无法逃出单引号字面量。修复后应加一条断言「payload 只含 base64 字符集」的测试。
注意此法对提权 `runas` 路径同样有效（不依赖环境变量继承）。

---

## 7. 门禁（真实退出码）

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
Set-Location <repo>
go -C desktop build ./...            ; build exit = 0
go -C desktop vet   ./...            ; vet   exit = 0
go -C desktop test  -count=1 ./...   ; test  exit = 0
gofmt -l desktop\internal\services   ; gofmt exit = 0（零输出）
```

`go -C desktop test -count=1 ./...` 逐包结果：

```
ok  github.com/Hypostasis-Cat/HypoMux/desktop                              0.458s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/cmd/update-manifest-sign     0.370s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient        3.666s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform             0.369s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails      0.703s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion      0.477s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/internal/services           41.024s
ok  github.com/Hypostasis-Cat/HypoMux/desktop/internal/startup             0.223s
```

## 8. 工作区自证

临时文件 `desktop/internal/services/zz_manual_repro_test.go` 与 `raw-repro.json` 已删除。
`git status --porcelain`：

```
 M desktop/internal/services/hyperv_adapter.go
 M desktop/internal/services/hyperv_adapter_other.go
 M desktop/internal/services/hyperv_adapter_test.go
 M desktop/internal/services/hyperv_adapter_windows.go
?? reports/
```

只改了 4 个 `hyperv_adapter*.go`（前端两个文件是其他 teammate 的在途改动，与本轮无关）。
未 `git add`、未 `git commit`。全程只读查询 Hyper-V，未创建/删除/修改任何网卡、交换机、路由或网络设置。

---

# 第二轮 · 写路径（create / remove / waitip）修复

lead 在确认根因后撤销了「写路径保持现状不动」的指示（原判断的前提是那套机制本身没问题，
本报告 §2 的发现推翻了这个前提），要求把写路径一并修好。

## 9. 动手前发现的**第二个缺陷**（比尾随参数更隐蔽）

lead 采纳的方案是「在正文最前面播种 `$args = @('<payload>')`」。动手前先在真机上验证了一下，
结论是**这个方案单独用不成立**：

```powershell
function Show-Args() {
  if ($args.Count -lt 1) { return "NULL-EMPTY" }
  return "GOT:" + [string]$args[0]
}
$args = @('SEEDED-B64')
"script_scope_args=" + (($args -join '|'))
"function_sees_args=" + (Show-Args)
```

实测输出（`powershell.exe -NoProfile -NonInteractive -EncodedCommand <该脚本的 b64>`）：

```
script_scope_args=SEEDED-B64      ← 脚本作用域播种成功
function_sees_args=NULL-EMPTY    ← 但函数里的 $args 是空的
```

**PowerShell 里函数的 `$args` 是这个函数自己的实参，会遮蔽脚本作用域的 `$args`。**
而 `hypervPowerShellScript` 里的 `Read-Envelope` **正是一个函数**，它读的就是 `$args[0]`。
所以哪怕把尾随参数去掉、换成播种 `$args`，`Read-Envelope` 依然会拿到空 → 返回 `$null` → `exit 2`。

这一点用真实脚本正文直接验证过（临时测试，仅跑只读的 `inventory` op）：

| 正文形态 | `Read-Envelope` 读法 | 退出码 | 结果文件 |
|---|---|---|---|
| 播种 `$__hypervPayloadB64`，但函数仍读 `$args[0]` | `$args[0]` | `2` | 未生成 |
| 播种 `$__hypervPayloadB64`，函数改读 `$__hypervPayloadB64`（plain） | `$__hypervPayloadB64` | `0` | 1143 字节，`ok:true` |
| 同上，加 `$script:` 作用域修饰符 | `$__hypervPayloadB64` | `0` | 1143 字节，`ok:true` |

两种作用域写法等价，最终选了**不带修饰符的 plain 写法**（更短，且实测 `$__hypervPayloadB64` 天然落在
脚本作用域、函数沿作用域链可见；不需要 `$script:`）。

## 10. 采用的修法

### 10.1 为什么不是 inline `FromBase64String`

lead 允许二选一（播种 `$args` vs inline `FromBase64String`），前提是①脚本本体不出现可变数据
②`hypervPowerShellScript` 仍是纯常量③安全性论证同样成立。三种写法都能满足后两条，但**播种一个专用变量**
最优：

| 方案 | 正文首行 | 结论 |
|---|---|---|
| 播种 `$args` | `$args = @('<b64>')` | **不可行** —— 函数里 `$args` 恒空（见 §9） |
| inline `FromBase64String` | `$p = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('<b64>'))` | 可行，但把「编码 + 解码」两件事都搬进正文，正文变长、常量与拼接前缀的职责边界变模糊 |
| **播种专用变量（采用）** | `$__hypervPayloadB64 = '<b64>'` | 正文首行只有一次 base64 编码，解码仍在脚本内的 `Read-Envelope`；常量脚本与可变数据边界最清晰 |

选中的形态让「可变数据」在整条链路里**只出现一次**（首行那一个 base64 字面量），
`hypervPowerShellScript` 仍然是一段一字不动的纯常量。

### 10.2 改动清单

| 位置 | 改动 |
|---|---|
| `hyperv_adapter.go:1996` | 新增常量 `hypervPayloadVariable = "__hypervPayloadB64"`（含「为什么不能叫 `$args`」的说明） |
| `hyperv_adapter.go:2013` | 新增纯函数 `hypervSeededCommandBody(payload)` = `"$__hypervPayloadB64 = '<base64>'\n" + hypervPowerShellScript` |
| `hyperv_adapter.go:2027` | 新增纯函数 `hypervPowerShellArguments(payload)` —— 返回参数数组，**末尾就是 `-EncodedCommand` 的值** |
| `hyperv_adapter.go:1805` | `Read-Envelope` 改读 `$__hypervPayloadB64`（变量不存在时 `[string]` 得空串 → fail-closed 返回 `$null` → `exit 2`） |
| `hyperv_adapter_windows.go:25` | 文件级硬约束补一条：「`-EncodedCommand` 之后不得有任何尾随 token」 |
| `hyperv_adapter_windows.go:246` | `hypervPowerShellCommand` 改为委托 `hypervPowerShellArguments(payload)`，**尾随 payload token 已删除** |
| `hyperv_adapter_windows.go:8` | 移除不再使用的 `encoding/base64` 导入 |

**按 lead 要求保留的三点全部保留**：结果文件协议（`Publish` 写 `.tmp` 再 `Move-Item` 原子提交）、
payload 经 base64 注入（防注入）、提权 `runas`（`ShellExecuteExW` + `SEE_MASK_NOCLOSEPROCESS`，
创建网卡仍然提权，未降级）。`hypervReadInventoryScript` / `hypervInventorySnapshot` 这套读取路径的
最小实现也一行未动。

### 10.3 安全性论证

1. **零插值的常量前缀**：首行模板 `"$" + hypervPayloadVariable + " = '" + b64 + "'\n"` 本身是常量，
   没有任何用户输入参与格式化。
2. **结构上逃不出单引号字面量**：payload 先过 `base64.StdEncoding`，字母表只有 `A-Za-z0-9+/=`，
   **不含单引号、反引号、空格、制表符、`$`**。因此无论 payload 里是什么
   （包括 `O'Brien"; rm -rf /` 这种刻意构造的恶意串），它都只能作为单引号字符串的普通字符出现。
   这条不变量由单测逐字符断言（§11）。
3. **不依赖环境变量继承**：payload 随进程命令行走，因此提权 `runas` 那条链路同样有效 ——
   `ShellExecuteExW` 启动的提权进程拿不到本进程环境变量，但拿得到命令行。
4. **提权路径的空格安全**：`ShellExecuteExW` 用 `strings.Join(arguments, " ")` 把参数拼回命令行。
   `-EncodedCommand` 的值是 UTF-16LE base64，字母表不含空格，所以 join 之后不会被切错；
   而**删掉尾随 token 之后**，可变数据根本不再经过这条 join 路径，暴露面比修复前更小。

## 11. 新增防回归单测（2 条）

全部是纯函数断言，不 exec powershell、不读 Hyper-V，在没有 Hyper-V 的 windows-2025 runner 上同样能过。

1. `TestHypervSeededCommandBodyPutsPayloadInVariable`
   - 正文第一行之后的部分**逐字等于** `hypervPowerShellScript`（常量脚本本体不得被改）；
   - 第一行形状必须是 `$__hypervPayloadB64 = '<base64>'`；
   - 播种值逐字符检查，不含 `'` `` ` `` `$` 空格 TAB CR LF `;` `"`（**恶意 payload 直接作为测试输入**，
     payload 里塞了 `O'Brien"; rm -rf /`，这行就是它的守门人）；
   - 播种值 base64 解回来必须**逐字等于原 payload**（证明没有二次编码/截断）；
   - 脚本里**必须**出现 `$__hypervPayloadB64`，且**不得**再出现 `$args[0]`。
2. `TestHypervPowerShellArgumentsHaveNoTrailingToken`（就是 lead 要的那条防回归断言）
   - `len(args)` 必须恰好是 6，前 5 个逐字等于
     `-NoProfile / -NonInteractive / -ExecutionPolicy / Bypass / -EncodedCommand`；
     任何尾随 token 都会让这个断言失败；
   - 最后一项是 `-EncodedCommand` 的值，必须无空格、无单引号（提权路径要 join 回命令行）；
   - 该值 base64 解码后字节数必须为偶数（UTF-16LE），再按 UTF-16LE 解回正文，
     必须**逐字等于** `hypervSeededCommandBody(payload)`，且以 payload 播种开头。

两条都直接对着修复前的错误形态：修复前 argc=7（尾随 token），且 `hypervPowerShellScript` 里含 `$args[0]`
——任一条都会让它们失败。

## 12. 真机验证（只用只读 op，未创建/删除任何网卡）

用临时测试文件调真实代码路径 —— 直接跑 `svc.runScript(...)`，即**完整走写路径机制**
（payload 注入 → 播种正文 → `Read-Envelope` → `Publish` 原子提交 → 父进程读结果文件），
但 op 只选只读的两个：

```
op=inventory  OK=true code=ok adapters=6 switches=2 addresses=0
   adapter Container NIC 9d844023 mac=<MAC> switch=Default Switch
   adapter xuni-01 mac=<MAC> switch=XuniUplink
   adapter xuni-02 mac=<MAC> switch=XuniUplink
   adapter xuni-03 mac=<MAC> switch=XuniUplink
   adapter xuni-04 mac=<MAC> switch=XuniUplink
   adapter xuni-05 mac=<MAC> switch=XuniUplink
   switch Default Switch type=Internal
   switch XuniUplink    type=External
op=waitip     OK=true code=ok adapters=0 switches=0 addresses=1
```

**修复前**：同样的调用是 `script_failed：脚本执行失败：PowerShell 以退出码 4294770688 结束`，
结果文件从未生成。现在 `inventory` 与 `waitip` 两个 op 都 `OK=true`，说明 payload 注入、
结果文件协议、原子提交这三段全部打通。

修完后命令正文的真实样子：

```
BODY-LINE-1 = $__hypervPayloadB64 = 'eyJvcCI6ImludmVudG9yeSJ9'
BODY-LINE-2 =
payload decodes to: {"op":"inventory"}
PS=C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe err=<nil> argc=6
  arg[0] = "-NoProfile"
  arg[1] = "-NonInteractive"
  arg[2] = "-ExecutionPolicy"
  arg[3] = "Bypass"
  arg[4] = "-EncodedCommand"
  arg[5] = <22632 chars: JABfAF8AaAB5AHAAZQByAHYAUABhAHkAbABvAGEA...>
```

（`eyJvcCI6ImludmVudG9yeSJ9` = base64(`{"op":"inventory"}`)；`JABfAF8A…` 解开是
`$__hyp…` 的 UTF-16LE 编码，即整段播种后的正文。）

**未验证项（如实声明）**：
- `create` / `remove` 两个 op **没有在真机上执行过** —— lead 明确说尚未拿到用户对「真实创建网卡」的授权。
  本轮只验证了它们**共用的同一条机制**（payload 播种 + 结果文件 + 原子提交）能被正常跑通，
  并在单测里锁住了它们的 payload 形状。
- 提权 `runas`（`ShellExecuteExW`）分支同样没实跑：它需要非提权进程 + UAC 交互。
  但该分支调用的是**同一个** `hypervPowerShellCommand` → 同一个 `hypervPowerShellArguments`，
  参数数组已由单测断言（argc=6、无尾随 token、无空格），且 payload 随命令行走不依赖环境变量继承。
- 真机端到端（真建 1 张卡 → DHCP 就绪 → 进出口池 → 删除）待 lead 拿到用户授权后组织。

## 13. 门禁（真实退出码，第二轮）

```
go -C desktop build ./...              exit = 0
go -C desktop vet   ./...              exit = 0
go -C desktop test  -count=1 ./...     exit = 0   （9 包全 ok，services 41.340s）
gofmt -l desktop\internal\services     零输出，exit = 0
GOOS=linux GOARCH=amd64 go -C desktop build ./internal/services/   exit = 0
```

`go test` 逐包：`desktop` 0.311s / `cmd/update-manifest-sign` 0.293s / `internal/engineclient` 3.592s /
`internal/platform` 0.288s / `internal/platform/wails` 0.567s / `internal/releaseversion` 0.397s /
`internal/services` 41.340s / `internal/startup` 0.192s，另有 3 个包 `[no test files]`。

## 14. 工作区自证（第二轮）

临时测试文件 `zz_seedprobe_test.go` / `zz_seedprobe2_test.go` / `zz_writepath_test.go` 全部已删除。
`git status --porcelain`：

```
 M desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx   ← 其他 teammate
 M desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx         ← 其他 teammate
 M desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx             ← 其他 teammate
 M desktop/frontend/src/pages/VirtualAdaptersPage.tsx                  ← 其他 teammate
 M desktop/internal/services/hyperv_adapter.go
 M desktop/internal/services/hyperv_adapter_other.go
 M desktop/internal/services/hyperv_adapter_test.go
 M desktop/internal/services/hyperv_adapter_windows.go
?? reports/
```

未 `git add`、未 `git commit`。全程只读查询 Hyper-V，未创建/删除/修改任何网卡、交换机、路由或网络设置。
---

## 15. 真机端到端测试（lead 授权：建 1 张、验证完删掉、恢复现场）

授权来源：team-message-f7d12540-907b-4b72-aa3e-b7f37c7a544f。用户已明确授权建 1 张卡并在验证后删除。
测试进程本身是提权的（`elevated=true`），`Create` 走 `hypervRunDirect` 而非 `runas`，**全程没有弹 UAC**。

环境：`HYPOMUX_DATA_DIR=%USERPROFILE%\Desktop\HypoMux-Portable-2.7.0-vnic-preview\data`
临时测试文件 `desktop/internal/services/zz_e2e_test.go`（已删除）。

### 15.0 前置事实

- 测试前 `hyperv\adapters.json` **不存在**（测试启动时实测 `ledger path=... exists=false`）。
- `settings.json` 在整个测试窗口内**没有被写过**（测试跑在 01:04–01:09，文件 mtime 仍是 `2026/10/4 0:21:13`）。

### 15.1 建卡（只建 1 张）

命令：
```
go -C desktop test -count=1 -timeout 6m -run TestZZE2ECreate -v ./internal/services/
```

原始输出：
```
[01:04:11.479] HYPOMUX_DATA_DIR="%USERPROFILE%\\Desktop\\HypoMux-Portable-2.7.0-vnic-preview\\data"
[01:04:11.486] ledger path=...data\hyperv\adapters.json exists=false
[01:04:11.486] elevated=true
[01:04:14.351]     switch Default Switch type=Internal allowMgmtOs=true
[01:04:14.351]     switch XuniUplink type=External allowMgmtOs=true
[01:04:14.351]     Switches err=<nil>
[01:04:14.368] BEFORE List() err=<nil> rows=0
[01:04:14.368] >>> Create("XuniUplink", 1) 开始
[01:04:16.272] <<< Create 返回 elapsed=1.9032775s err= rows=1
[01:04:16.272]     created row: name=HypoMux-vnic-00 state=creating mac=02:1a:2b:00:00:00
[01:04:16.272] !!! Create 失败，保留现场，不重试。完整错误：
[01:04:16.272] Create failed:
--- FAIL: TestZZE2ECreate (4.79s)
EXITCODE=1
```

**卡其实建成功了**，`err` 却是一个「非 nil 但文字为空」的 error：

```
Get-VMNetworkAdapter -ManagementOS
Name                   MacAddress   SwitchName     Status
----                   ----------   ----------     ------
HypoMux-vnic-00        021A2B000000 XuniUplink     {Ok}
xuni-01                <MAC> XuniUplink     {Ok}
xuni-02                <MAC> XuniUplink     {Ok}
xuni-03                <MAC> XuniUplink     {Ok}
xuni-04                <MAC> XuniUplink     {Ok}
xuni-05                <MAC> XuniUplink     {Ok}

Get-NetAdapter
Name                        MacAddress        Status
vEthernet (HypoMux-vnic-00) 02-1A-2B-00-00-00 Up
vEthernet (xuni-01)         <MAC> Up
...
```

台账 `hyperv\adapters.json`：
```json
{
  "version": 1,
  "nextSeq": 1,
  "nextMAC": 1,
  "adapters": [
    { "name": "HypoMux-vnic-00",
      "adapterId": "{<GUID>}",
      "macAddress": "02:1a:2b:00:00:00",
      "switchName": "XuniUplink",
      "batchId": "87cd36dcdfd65611",
      "state": "creating",
      "createdAt": "2026-10-04T01:04:14+08:00",
      "lastError": "" }
  ]
}
```

> **结论：§9/§10 的写路径修复在真机上生效** —— 提权 PowerShell 跑通了完整 create 脚本，卡、台账、宿主机 vEthernet 三者一致，1.9s 返回。
> 但暴露了两个新缺陷，见 §15.5 / §15.6。

### 15.2 DHCP 取证

因为 `Create` 返回了假错误，测试进程在 `t.Fatalf` 处退出，后台 `go s.awaitBatch(...)` 随之被杀，
所以 `settings.json` 的出口池没有变化。DHCP 证据改为**对已存在的卡做只读取证**（没有再建第二张）：

```
Get-NetAdapter -Name 'vEthernet (HypoMux-vnic-00)'
Name                        MacAddress        Status  LinkSpeed MediaConnectionState
vEthernet (HypoMux-vnic-00) 02-1A-2B-00-00-00 Up      100 Mbps  Connected

Get-NetIPAddress -InterfaceAlias 'vEthernet (HypoMux-vnic-00)'
InterfaceAlias              IPAddress                    PrefixLength PrefixOrigin SuffixOrigin AddressState SkipAsSource
vEthernet (HypoMux-vnic-00) <LINKLOCAL_IPv6>%51           64    WellKnown         Link    Preferred        False
vEthernet (HypoMux-vnic-00) 192.168.16.<masked>                         24         Dhcp         Dhcp    Preferred        False

Get-NetIPInterface -InterfaceAlias 'vEthernet (HypoMux-vnic-00)' -AddressFamily IPv4
InterfaceAlias              Dhcp ConnectionState InterfaceMetric
vEthernet (HypoMux-vnic-00) Enabled Connected              35

Get-NetRoute -InterfaceAlias 'vEthernet (HypoMux-vnic-00)'
DestinationPrefix NextHop RouteMetric      DestinationPrefix NextHop RouteMetric
255.255.255.255/32 0.0.0.0      256        0.0.0.0/0   192.168.16.<masked>        0
192.168.16.255/32  0.0.0.0      256        192.168.16.0/24 0.0.0.0      256
```

- `PrefixOrigin = Dhcp`、`AddressState = Preferred` —— **两个关键判据都满足**，DHCP 链路完全可用。
- 拿到真实租约 `192.168.16.<masked>/24`，网关 `192.168.16.<masked>`。
- 等待时长：**未能测到**。`awaitBatch` 随测试进程一起被杀，没有走 `awaitAddresses` 的 45s 预算。
  设计预算是「网卡出现 15s → 进出口池 → DHCP 就绪 45s」（`hyperv_adapter.go:1385` 注释）。
  从建卡（01:04:14）到取证（01:07 左右）间隔约 3 分钟，地址早已就绪，
  说明真实 DHCP 耗时落在 15s 预算内，但**没有精确计时数据**。

### 15.3 出口池与 settings.json

未变化，因为 `awaitBatch` 未跑完：
```json
"selected_adapter_ids": [ "以太网" ],
"adapter_weights": { "以太网": 1 }
```
与测试前完全一致。文件 mtime 仍是 `2026/10/4 0:21:13`（测试窗口 01:04–01:09），**证明确未被写**。

### 15.4 删卡（只删这 1 张）+ 现场恢复

第一次调用因为我的测试脚本用 `Set-Content -Encoding utf8` 写名字文件、注入了 BOM 而被拒：
```
>>> Remove("\ufeffHypoMux-vnic-00") 开始
<<< Remove elapsed=0s err=not_managed："\ufeffHypoMux-vnic-00" 不符合 HypoMux 命名规范（HypoMux-vnic-NN），已跳过删除
```
**这是正确行为**（fail-closed，没有误删任何东西）。改用无 BOM 写入后重试：

```
[01:08:49.301] BEFORE-REMOVE List() err=<nil> rows=1
[01:08:49.301] BEFORE-REMOVE   name=HypoMux-vnic-00 state=ready mac=02:1a:2b:00:00:00 iface="vEthernet (HypoMux-vnic-00)" addr=192.168.16.<masked>/24 inPool=false lastError=""
[01:08:49.301] >>> Remove("HypoMux-vnic-00") 开始
[01:08:50.602] <<< Remove("HypoMux-vnic-00") elapsed=1.3012311s err=<nil>
[01:08:56.045] AFTER-REMOVE List() err=<nil> rows=0
--- PASS: TestZZE2ERemove (9.37s)
```

> 顺带一个正面观察：台账里 `state` 仍是 `creating`（awaitBatch 被杀），但 `List()` 从宿主实际 IP
> 正确派生出 `state=ready` 且带上了 `192.168.16.<masked>/24`。读路径是自愈的。

删除后取证：
```
Get-VMNetworkAdapter -ManagementOS     → 无 HypoMux-vnic-00 ✅
Get-NetAdapter                         → 无 vEthernet (HypoMux-vnic-00) ✅
adapters.json                          → {"version":1,"nextSeq":1,"nextMAC":1,"adapters":[]} ✅
settings.json                          → selected_adapter_ids 仍为 ["以太网"]，逐字未变 ✅
```

**现场恢复**：测试前 `adapters.json` 不存在，故删除该文件以精确还原；`hyperv\jobs\` 为空无残留。

### 15.5 新缺陷 A（严重）：`Create` 成功时返回「非 nil 但没有文字」的 error

`hyperv_adapter.go:1382`（修复前）：
```go
return rows, outcome.failure   // outcome.failure 的类型是 *HyperVError
```
`:1181` 声明 `failure *HyperVError`。Go 的 **typed nil in interface**：nil 指针装箱进
`error` 接口后接口本身非 nil，于是调用方 `err != nil` 成立；而
`hyperv_adapter.go:84` 的 `func (e *HyperVError) Error()` 对 nil 接收者返回 `""`。
前端因此会弹出一个**没有任何文字的错误提示**，尽管卡已经建好。

修法：`return rows, hypervFailureError(outcome.failure)`，新增纯函数
```go
func hypervFailureError(failure *HyperVError) error {
	if failure == nil {
		return nil
	}
	return failure
}
```
回归测试 `TestHypervFailureErrorBoxesNilAsTrueNil`（不依赖 Hyper-V）。

### 15.6 新缺陷 B（契约冲突）：首张卡编号是 `HypoMux-vnic-00`

冻结契约 `reports/vnic/60-hyperv-cmd-surface.md:560-562` 规定编号 `HypoMux-vnic-01` … `-99`，
`index = max(状态文件中已记录 index) + 1`。但 `hypervLedger.NextSeq` 是零值 `0`，
`allocateNames` 直接 `seq := l.NextSeq` 发号，首张卡拿到 **`-00`**。

修法：
- 新增 `const hypervFirstAdapterSeq = 1` 与 `(*hypervLedger).normalize()`（只把「从没发过号」的
  0 抬到 1，**不回收已发出去的号**，也**不改动 `NextMAC`**）；
- `loadHypervLedger()` 三条返回路径都调用 `normalize()`（这样 `Create:1307` 的重名探测与实际发号一致）；
- `allocateNames()` 里再兜底调用一次（发号唯一入口，防止未来新增调用点绕过 load）。

回归测试：`TestHypervLedgerFirstSequenceStartsAtOne`、
`TestHypervLedgerNormalizeKeepsAlreadyIssuedZeroSequence`。
同时更新两条把 `-00` 写死的既有测试：`TestHypervLedgerAllocateNamesNoReuse`、
`TestHypervLedgerRoundTrip`、`TestHypervReconcileBatchReportedFailure`。

> **为什么既有测试没抓到**：`TestHypervLedgerAllocateNamesNoReuse` 当年是照着实现写的 ——
> 它断言首张卡就是 `HypoMux-vnic-00`，把 bug 锁进了测试。

### 15.7 用户原有 5 张卡逐项对比（必须未被触碰）

| 项目 | 测试前 | 测试后 | 结论 |
|---|---|---|---|
| xuni-01 MAC | <MAC> | <MAC> | 未变 ✅ |
| xuni-02 MAC | <MAC> | <MAC> | 未变 ✅ |
| xuni-03 MAC | <MAC> | <MAC> | 未变 ✅ |
| xuni-04 MAC | <MAC> | <MAC> | 未变 ✅ |
| xuni-05 MAC | <MAC> | <MAC> | 未变 ✅ |
| xuni-01..05 交换机 | XuniUplink | XuniUplink | 未变 ✅ |
| xuni-01..05 地址 | 192.168.16.<masked>/<masked>/.<masked>/<masked>/<masked> | 同左 | 未变 ✅ |
| XuniUplink 交换机 | External / AllowMgmtOS=True | External / True | 未变 ✅ |
| Default Switch 交换机 | Internal / AllowMgmtOS=True | Internal / True | 未变 ✅ |
| 物理网卡 以太网 | <MAC> / Up | 同左 | 未变 ✅ |

**需要 lead 注意的一处系统侧变化（非本工具造成）**：
`Container NIC 9d844023` 的 MAC 在测试期间由 `<MAC>` 变成了 `<MAC>`
（宿主机侧 `vEthernet (Default Switch)` 同步由 `<MAC>` 变为 `<MAC>`）。
这张卡挂在 **Default Switch** 上，而本次测试只对 **XuniUplink** 执行了 Add/Remove，
两者不在同一个动态 MAC 池、也不是同一个对象，**本工具没有触碰它**。
这是 Hyper-V 容器网络自行重签发动态 MAC 的行为，在此如实记录以免被误判成回归。

### 15.8 门禁（真实退出码）

```
gofmt -l desktop\internal\services                                   → 零输出, exit 0
go -C desktop build ./...                                             → exit 0
go -C desktop vet ./...                                               → exit 0
go -C desktop test -count=1 ./...                                     → exit 0（9 包全 ok，services 39.646s）
GOOS=linux GOARCH=amd64 go -C desktop build ./internal/services/       → exit 0
```

### 15.9 工作区自证

临时测试文件 `desktop/internal/services/zz_e2e_test.go` 已删除；`%TEMP%\hypomux-e2e-created.txt` 已删除。
```
$ git status --porcelain
 M desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx   ← 其他 teammate 在途改动
 M desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx        ← 其他 teammate 在途改动
 M desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx            ← 其他 teammate 在途改动
 M desktop/frontend/src/pages/VirtualAdaptersPage.tsx                 ← 其他 teammate 在途改动
 M desktop/internal/services/hyperv_adapter.go
 M desktop/internal/services/hyperv_adapter_other.go
 M desktop/internal/services/hyperv_adapter_test.go
 M desktop/internal/services/hyperv_adapter_windows.go
?? reports/
```
未 `git add`、未 `git commit`。

### 15.10 仍未覆盖

- **`awaitBatch` 的完整路径未被真机跑过**（出口池自动并入、`settings.json` 自动改写、
  `state: creating → ready` 的台账回写、DHCP 45s 预算的精确计时）—— 因为假错误让测试进程
  提前退出。下一次授权时应先修 §15.5 再跑，才能拿到这段证据。
- **提权 `runas`（ShellExecuteExW）分支仍未实跑** —— 本次测试进程本身就是提权的，
  走的是 `hypervRunDirect`。真实用户从 Wails 启动时是**非提权**父进程，会走 runas + UAC。
  写路径修复保留了 runas 未降级，且 payload 随命令行传递（不依赖环境变量继承），但**缺实跑证据**。

---

## 16. awaitBatch 完整路径（lead 第二轮任务一）

上一轮跑不到这里，是因为缺陷 A（typed nil）让 `Create` 返回了一个"非 nil 但文字为空"的错误，
测试 `t.Fatalf` 提前退出，把后台 `go s.awaitBatch(...)` 一起杀了。缺陷 A 修完后重跑。

### 16.1 方法上的一个坑：盯的信号必须是台账，不是 List()

第一版测试每 2s 轮询 `svc.List()` 打 `state=`。两个问题叠加，结论全错：

1. `List()` 的 state 是**从宿主机实况自愈**的 —— 网卡一出现它就报 `ready`，
   完全反映不出后台 `awaitBatch` 什么时候跑完、什么时候回写台账；
2. 每次 `List()` 都要跑一次 PowerShell 并抢 `opMu`，等于测试自己排在后台
   `awaitAddresses` 的 waitip 前面，把"产品没收敛"和"测试把自己堵死了"混成一笔账。

第二版改成**只读台账文件**（`loadHypervLedger()` + `ledger.find(name)`），零 PowerShell 干扰。

### 16.2 现象：台账 state 永远停在 creating

无干扰的一轮（只读台账，不调 `List()`）：

```
[+4.3s]  <<< Create 返回 err=<nil> rows=1，created name=HypoMux-vnic-05
[+4.3s]  Create 返回瞬间台账 state="creating" lastError=""
[+82.3s] t=+82.3s ledger-state="creating" lastError=""     ← 一直不变
```

对照另一轮（每 2s 调 `List()`）：恒 `creating` 直到 `+106.9s`。
也就是说 **82 秒里后台一次都没成功回写台账**，而 `List()` 早已自愈成 `ready`，
把坏状态彻底盖住了 —— 前端展示的正是 `List()` 的结果，所以用户看不出问题。

### 16.3 定位（先证伪判定链，再找根因）

1. **先证伪判定链本身**：单独跑 `runScript(waitip)` —— 0.6s、`err=<nil>`、`OK=true`，
   `Get-NetIPAddress` 回报 `origin=Dhcp state=Preferred`，`hypervResolveAddresses` 判 `ready=true`。
   ⇒ runScript / waitip / 判定链都是好的，问题在"回写"这一步。
2. 在 `awaitBatch` / `awaitAddresses` 里临时加 `zzdbg`（写 stderr）实跑，打印出：

```
updateLedger name=vEthernet (HypoMux-vnic-05)
             alias=vEthernet (vEthernet (HypoMux-vnic-05))
             ready=false findIdx=-1
```

**双层串**。`ready=false` 是因为 `verdict[strings.ToLower("vEthernet (vEthernet (...))")]`
恒 miss；`findIdx=-1` 是因为台账里存的是对象名 `HypoMux-vnic-05`，却拿双层别名去找，
于是 `continue` —— **一行都没写**。诊断用的 `zzdbg` 全部删除（`Select-String zzdbg` 计数 = 0）。

### 16.4 缺陷 C：出口池键形式不对称（在 awaitBatch 之前就已存在）

`applyPoolUpdate` 用 `trimmed` 原样当池键，但两个调用方给的**形式不同**：

| 调用方 | 传进来的形式 |
|---|---|
| Create 侧 `awaitInterfaces` | 裸对象名 `HypoMux-vnic-01` |
| Remove 侧 | 别名 `vEthernet (HypoMux-vnic-01)` |

三条后果：
1. 卡进了池但键是裸名 → `List()` 查池表认不出，`inPool` 永远 false；
2. 引擎按别名绑定找不到网卡；
3. Remove 按别名删不到 Create 写进去的裸名 → `settings.json` 永久悬空键。

实测证据：任务一第一轮之前，`settings.json` 的出口池被污染成
`["以太网","HypoMux-vnic-01"]`（裸名），正是这个缺陷的直接证据。已从备份恢复。

**修法**（`desktop/internal/services/hyperv_adapter.go`）：新增纯函数 `hypervPoolKey(name string) string` ——
已是别名则原样返回；`strings.HasPrefix(lower, lower(hypervAdapterNamePrefix))` 则包成别名；
其余（`以太网` 等外部网卡）原样保留；空串返回空串。

### 16.5 缺陷 D（本轮最严重）：我自己在 16.4 修法里引入的回归

`hypervPoolKey` 归一时是**就地改调用方切片**的：

```go
for i := range add { add[i] = hypervPoolKey(add[i]) }
```

而 `awaitBatch` 把**同一个 `appeared`** 同时传给 `applyPoolUpdate(appeared, nil)` 和
`s.awaitAddresses(appeared)`。就地归一把 `names` 里的对象名换成了别名，
`awaitAddresses` 再 `hypervHostInterfaceName(name)` 包一层 ⇒ `vEthernet (vEthernet (...))`。
`verdict` 恒 miss ⇒ ready 恒 false；`ledger.find(别名)` 在只存对象名的台账里恒 `-1` ⇒ 一行都没写。
**台账 state 因此永远停在 creating。**

注意：**原始代码在这里是对的**。是第一轮 E2E 因缺陷 A 提前 `t.Fatalf` 杀掉后台 goroutine，
才从来没跑到这一段 —— 是我新加的就地归一把一条好路径变成了死路。

**两处修法**：

1. `applyPoolUpdate`（`current := s.settings.Get()` 之后）：不再就地改，改成两个新切片
   ```go
   normalizedAdd := make([]string, len(add))
   normalizedRemove := make([]string, len(remove))
   add, remove = normalizedAdd, normalizedRemove
   ```
   注释写明「参数切片不是本函数的所有物，就地改就是副作用」。
2. `awaitAddresses`：新增纯函数 `hypervObjectNameOrSelf(nameOrAlias string) string`
   （能反解就返回对象名，否则 `TrimSpace` 原样返回）；
   `aliases` 构造改 `hypervHostInterfaceName(hypervObjectNameOrSelf(name))`；
   `updateLedger` 闭包改
   ```go
   for i, alias := range aliases {
       name := hypervObjectNameOrSelf(names[i])
       state := verdict[strings.ToLower(alias)]
   ```
   `LastError` 用 `alias` 而非再包一层。
   两侧各自用正确形式，调用方传哪种都不会再包出双层串。

### 16.6 修复后的真机实测（权威输出）

```
[01:48:33.671] <<< Create 返回 t=+4.3s err=<nil> (nil? true) rows=1
[01:48:33.671] created name=HypoMux-vnic-06 mac=02:1a:2b:00:00:05 iface="vEthernet (HypoMux-vnic-06)"
[01:48:33.671] Create 返回瞬间台账 state="creating" lastError=""
[01:48:33.672] awaitBatch: applyPoolUpdate err=<nil>
[01:48:33.672] awaitBatch: entering awaitAddresses([HypoMux-vnic-06])
[01:48:34.341] awaitAddresses: runScript returned err=<nil> resNil=false
[01:48:34.377] verdict=map[vethernet (hypomux-vnic-06):{ready:true address:192.168.16.<masked> prefixLength:24 fallback:false}]
[01:48:34.378] updateLedger name=HypoMux-vnic-06 alias=vEthernet (HypoMux-vnic-06) ready=true findIdx=0
[01:48:36.671] t=+7.3s ledger-state="ready" lastError=""
[01:48:36.671] >>> awaitBatch 收敛：creating -> ready 用时 7.3s（预算 15s 出现 + 45s DHCP）
[01:48:39.597] AFTER-AWAIT name=HypoMux-vnic-06 state=ready addr=192.168.16.<masked>/24 gw=192.168.16.<masked> inPool=true lastError=""
[01:48:39.597] LEDGER-AFTER-AWAIT {... "name": "HypoMux-vnic-06", "macAddress": "02:1a:2b:00:00:05", "switchName": "XuniUplink", "state": "ready" ...}
[01:48:39.597] POOL-AFTER-AWAIT selected_adapter_ids=["以太网","vEthernet (HypoMux-vnic-06)"] adapter_weights={"vEthernet (HypoMux-vnic-06)":1,"以太网":1}
[01:49:00.423] LEDGER-AFTER-REMOVE {"version":1,"nextSeq":7,"nextMAC":6,"adapters":[]}
[01:49:00.423] POOL-AFTER-REMOVE selected_adapter_ids=["以太网"] adapter_weights={"以太网":1}
```

逐条回答 lead 的取证项：

| 取证项 | 结果 |
|---|---|
| 出口池是否自动并入 | ✅ `selected_adapter_ids` 自动多出 `vEthernet (HypoMux-vnic-06)`，`adapter_weights` 同键权重 1 |
| creating→ready 跃迁在第几秒 | **t = +7.3s**（预算：网卡出现 15s + DHCP 45s，未调大预算） |
| DHCP 从建卡到地址就绪的精确秒数 | **≤ 7.3s**，`192.168.16.<masked>/24` gw `192.168.16.<masked>`。每轮新 MAC 拿到不同 IP（.<masked> / .<masked> / .<masked> / .<masked>），证明不是复用租约 |
| 台账 state 是否真的回写 | ✅ `creating` → `ready`，`lastError` 全程为空 |
| `List()` 的 state / address | `state=ready`、`192.168.16.<masked>/24`、`inPool=true` |
| Remove 后出口池是否自动移出 | ✅ 回到 `["以太网"]`，对应的 `adapter_weights` 项一并清掉，不留悬空键 |
| 60s 预算是否够 | 够，用了 7.3s，没有出现耗尽预算的情况 |

### 16.7 新增防回归单测（纯函数，没有 Hyper-V 的 runner 也能过）

- `TestHypervPoolKeyNormalizesObjectNameAndAlias`：表驱动；保留输入大小写，额外断言
  `strings.EqualFold(hypervPoolKey("hypomux-vnic-01"), hypervPoolKey(alias))`
  （下游 `seen` 用小写键、`findString` 用 EqualFold、`List()` 池查表先转小写）。
- `TestHypervObjectNameOrSelfNeverDoubleWraps`：对象名 / 别名 / 带空白别名三输入都还原成对象名；
  再走一轮 `hypervHostInterfaceName(hypervObjectNameOrSelf(got))` 必须幂等回同一别名；
  `以太网` / `vEthernet (以太网 3)` / `Ethernet` 原样返回。
- `TestApplyPoolUpdateDoesNotMutateCallerSlice`：传
  `add=["HypoMux-vnic-01","以太网"]`、`remove=["vEthernet (HypoMux-vnic-09)"]`，调用后逐位比对未被改写。
- `TestApplyPoolUpdateCreateThenRemoveIsSymmetric`：按真实调用点形式（Create 裸名并入 → Remove 别名移出），
  断言池里只出现别名、`inPool` 键与权重键一致、Remove 后不留悬空引用、原有 `以太网` 权重不变。

**教训**：既有 `TestApplyPoolUpdateMerge`（`hyperv_adapter_test.go:683`）等老测试
**一律传预拼好的别名**，所以从来没抓到缺陷 C ——
测试是照着意图写的，不是照着真实调用点写的。

### 16.8 现场恢复（用备份比对，不凭印象）

```
settings.json 现 SHA256 = E9289DA8C8EFB5C593030C4EDAF32EFFD55C0FA2CC2DFEBEF2D9A732E2E07427
备份      SHA256       = E9289DA8C8EFB5C593030C4EDAF32EFFD55C0FA2CC2DFEBEF2D9A732E2E07427
BYTE-IDENTICAL = True
adapters.json 现在存在 = False
jobs 目录内容 = 0 个
```

- `settings.json` 从 `%TEMP%\hypomux-e2e-backup\settings.json` 覆盖回去，**逐字节相同**。
- `hyperv\adapters.json`（Remove 之后是一个空台账 `{"version":1,"nextSeq":7,"nextMAC":6,"adapters":[]}`）
  测试前不存在，已删除。
- `jobs/` 空。
- `HypoMux-vnic-01..06` 全部从 `Get-VMNetworkAdapter -ManagementOS` 与 `Get-NetAdapter` 消失。
- xuni-01..05 MAC 未变（<MAC> / <MAC> / <MAC> / <MAC> / <MAC>）；
  `Default Switch` 与 `XuniUplink` 两个交换机仍在；物理网卡 `以太网` <MAC> Up 未变。

---

## 17. 提权 runas 分支实跑（lead 第二轮任务二）——**本机无法取得有效证据，如实记录**

### 17.1 目标与判据

真实用户如果直接双击 `bin\hypomux.exe`（不经 `启动便携版.cmd` 的自提权），
父进程就是非提权，会走 `hypervExecuteElevated`（`desktop/internal/services/hyperv_adapter_windows.go:131`）
→ `hypervRunElevatedShellExecute` → `ShellExecuteExW(verb=runas)` + UAC。

lead 定的成功判据：`ShellExecuteExW` + runas 拿到提权子进程、结果文件被正确写出、
`ERROR_CANCELLED`（用户点否）能被映射成可读错误而非崩溃。
跑哪个 op 选无副作用的 `waitip`（PowerShell 侧只有 `Get-NetIPAddress`），
**绝不**在非提权上下文里跑 `create` / `remove`。

### 17.2 为什么之前的测试都是提权的

之前所有测试进程自己就是提权的，`hypervProcessElevated()`（`:138`，读
`windows.GetCurrentProcessToken().IsElevated()`）返回 true，走的是 `hypervRunDirect`，不弹 UAC：

```
[01:52:04.409] hypervProcessElevated() = true （false 才说明会走 runas 分支）
```

### 17.3 本机为什么造不出非提权上下文

**决定性事实（只读查询，未改任何系统设置）**：

```
EnableLUA                      = 1
ConsentPromptBehaviorAdmin     = 0     ← 管理员批准模式关闭 = 提权不询问
ConsentPromptBehaviorUser      = 3
PromptOnSecureDesktop          = 0
FilterAdministratorToken       = <未设置>   ← 默认 0
当前身份 = <HOST>\<ADMIN>
Get-LocalUser：<ADMIN>(True) / DefaultAccount(False) / Guest(True) / WDAGUtilityAccount(False)
```

`FilterAdministratorToken` 未设置（= 0）意味着**内置 <ADMIN> 账户不拆分令牌**，
它的令牌永远是全权的；`ConsentPromptBehaviorAdmin=0` 则是"直接提权不弹窗"。
机器上也没有第二个可用的标准用户账户（`DefaultAccount` 是禁用的）。

已逐条试过并**全部失败**：

| # | 办法 | 结果 |
|---|---|---|
| 1 | `Start-Process runas.exe -ArgumentList "/trustlevel:0x20000 \"<exe>\" -test.run=... -test.v"` | 退出码 1，3 分钟内结果文件始终没生成 |
| 2 | 同步调用 `& runas.exe "/trustlevel:0x20000 \"<exe>\" ..."` | 退出码 1，空输出 |
| 3 | **隔离实验**：连 `runas /trustlevel:0x20000 cmd.exe /c echo hi > 文件` 都失败（文件未生成） | ⇒ 是这个机制本身在本机不可用，不是引号或探针的问题 |
| 4 | `runas /user:<ADMIN> ...` | 退出码 1，无输出 |
| 5 | 计划任务 `New-ScheduledTaskPrincipal -RunLevel Limited -LogonType Interactive` 跑探针 | 约 0 秒就结束，`hypervProcessElevated() = true` ⇒ **Limited 拿到的仍是提权令牌** |
| 6 | 同上，用 `cmd.exe /c echo ok > 文件` 做隔离实验 | **成功**（文件生成，内容 "ok"）⇒ 计划任务机制本身可用，是 Limited 在本机拿不到非提权令牌 |
| 7 | `DuplicateTokenEx`（造 primary 令牌） | `Access is denied`（primary 令牌不能复制成 primary 令牌） |
| 8 | 对本进程令牌直接 `SetTokenInformation(TokenElevationType=Default)` | `The parameter is incorrect.`（ERROR_INVALID_PARAMETER，MSDN 规定只允许对受限令牌这么设） |
| 9 | `CreateRestrictedToken`（`CreateRestrictedToken` 不在 x/sys/windows 里，用 `syscall.NewLazyDLL("advapi32.dll")` 取），flags 分别试 `0x6 / 0x2 / 0x4 / 0x0`，`LazyProc.Call` 与 `syscall.SyscallN` 两种调法 | 一律 `r1=0`，`Invalid access to memory location.`（ERROR_INVALID_ADDRESS） |

办法 5~9 的结论：**本机的令牌操作被系统层挡住了**，
造不出一个 `IsElevated() == false` 的进程。唯一还剩下的路是创建一个非管理员本地账户并交互登录，
或调整 UAC 的 `FilterAdministratorToken` / `ConsentPromptBehaviorAdmin` ——
**两者都是系统设置变更，本轮未获授权，不擅自改动。**

### 17.4 读码得到的实现结论（非实跑证据，仅供 review）

`hyperv_adapter_windows.go:175` `hypervRunElevatedShellExecute` 的分支逻辑：

- `ShellExecuteExW(verb="runas", Show=SW_HIDE)`；
- 返回 `result == 0` 时：`errors.Is(callErr, windows.ERROR_CANCELLED)` → `errHypervElevationCancelled`
  （用户点"否"），否则回 `callErr` 或 `errors.New("ShellExecuteExW 未能启动提权脚本")`
  ⇒ **`ERROR_CANCELLED` 确实被映射成可读错误，不会崩**；
- 随后 `WaitForSingleObject(info.Process, timeout)`，超时 → `errHypervScriptTimeout`
  （**不杀提权子进程，因为杀不掉**）；
- 最后 `GetExitCodeProcess` 非 0 → `fmt.Errorf("PowerShell 以退出码 %d 结束", code)`。

这条路径此前已经过两轮纯代码修复，本轮**未发现新缺陷，也未能在真机执行验证**。
已知的行为差异如实记录：`hypervExecuteUnelevated`（`:125`）恒走 `hypervRunDirect`、永不弹 UAC，
所以非提权父进程下只有 create/remove（`elevated=true`）会触发 runas；
本轮为安全起见**没有**在非提权上下文里跑过任何 op。

### 17.5 收尾

- 全部临时文件已删：`zz_e2e2_test.go`、`zz_diag_test.go`、`zz_runas_test.go`、
  `zz_runas_launcher_test.go`、`%TEMP%\hypomux-runas-probe.exe`、`%TEMP%\hypomux-runas-result.txt`、
  `%TEMP%\hypomux-runas-data`；计划任务 `HypoMuxRunasProbe` / `HypoMuxRunasLauncher` 已
  `Unregister-ScheduledTask` 清理。
- `git status --porcelain` 见 §17.6。

### 17.6 门禁（真实退出码，第三轮，收尾）

```
gofmt -l desktop\internal\services   exit=0   输出为空（零输出）
go -C desktop build ./...             exit=0
go -C desktop vet ./...               exit=0
go -C desktop test -count=1 ./...     exit=0
```

`go test -count=1 ./...` 逐包：

```
ok  github.com/Hypostasis-Cat/HypoMux/desktop                        0.399s
?   .../desktop/build/windows/syso                                   [no test files]
?   .../desktop/cmd/release-version                                  [no test files]
ok  .../desktop/cmd/update-manifest-sign                             0.320s
ok  .../desktop/internal/engineclient                                3.624s
ok  .../desktop/internal/platform                                    0.332s
ok  .../desktop/internal/platform/wails                              0.608s
ok  .../desktop/internal/releaseversion                              0.415s
ok  .../desktop/internal/services                                    39.400s
ok  .../desktop/internal/startup                                     0.200s
```

### 17.7 工作区自证（第三轮）

```
 M desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx     ← 其他 teammate 在途
 M desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx          ← 其他 teammate 在途
 M desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx               ← 其他 teammate 在途
 M desktop/frontend/src/pages/VirtualAdaptersPage.tsx                   ← 其他 teammate 在途
 M desktop/internal/services/hyperv_adapter.go                           ← 本轮
 M desktop/internal/services/hyperv_adapter_other.go                    ← 本轮
 M desktop/internal/services/hyperv_adapter_test.go                     ← 本轮
 M desktop/internal/services/hyperv_adapter_windows.go                  ← 本轮
?? reports/
```

无任何 `zz_*` 临时文件残留，`Select-String zzdbg` 计数 = 0，全程未 `git add` / `git commit`。
本轮只写范围仍是 `desktop/internal/services/hyperv_adapter*.go` + 报告。
