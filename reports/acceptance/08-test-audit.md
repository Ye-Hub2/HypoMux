# Hyper-V 适配器测试可信度审查（只读审查，未修改任何源码）

- 被审文件：`desktop/internal/services/hyperv_adapter_test.go`（**2717 行**，非 2868）
- 对照实现：`desktop/internal/services/hyperv_adapter.go`（2868 行）、`hyperv_adapter_windows.go`（276 行）、`hyperv_adapter_other.go`（30 行）
- 环境：Windows，`go test ./internal/services/ -count=1` → **483 PASS / 0 FAIL / 5 SKIP**（那 5 个 SKIP 全部来自其他文件）；整包覆盖率 **61.7%**
- 审查日期：2026 年本次会话

---

## 0. 复现命令与一个重要提醒

```
cd <repo>\desktop
$env:Path="C:\Program Files\Go\bin;"+$env:Path
go test ./internal/services/ -run HyperV -count=1     # ⚠️ 只跑到 7 个测试！
go test ./internal/services/ -count=1 -v               # ✅ 78/78
```

**⚠️ `-run HyperV` 是大小写敏感的通配**：本文件里 `HyperV` 与 `Hyperv` 两种拼法混用，
`-run HyperV` 实际只命中 7 个测试：

```
TestIsHyperVAdapterName / TestDeriveHyperVState / TestBuildHyperVStatus /
TestBuildUnmanagedHyperVStatus / TestHyperVErrorMessageCarriesCode /
TestToHyperVSwitchesSorted / TestSortHyperVStatuses
```

覆盖率因此只有 **1.4%**。审 Hyper-V 测试必须用不带 `-run` 的全量跑，或 `-run 'Hyper[Vv]'`。

---

## 1. 测试总数与跳过清单

### 1.1 数量统计

| 指标 | 数值 | 证据 |
|---|---|---|
| 顶层 `func TestXxx` | **78** | `hyperv_adapter_test.go` 全文正则计数 |
| `t.Run(` 调用点 | **11** | 行 560/602/613/707/1283/1442/1464/1474/1484/1494/1503 |
| 具名子测试实例 | **32** | 7+8+1+6+4+6×1 |
| PASS 结果行（含子测试） | **110** | 全量 `-v` 实测 |
| 表驱动内联 case（无子测试、直接 `t.Fatalf`） | 约 100+ | 多数用例是 `map[string]struct` 内联循环 |
| 文件行数 | 2717 | — |

### 1.2 跳过清单

| 位置 | 跳过语句 | 触发条件 | 是否合理 |
|---|---|---|---|
| `hyperv_adapter_test.go:756` | `t.Skip("当前环境没有网卡")` | `len(net.Interfaces()) == 0` | ✅ 合理。唯一的环境相关跳过，且本机未触发 |

**全文件没有**：`t.Skipf`、`runtime.GOOS != "windows"` 守卫、admin/UAC 权限检测、`testing.Short()`。

> 结论：**跳过面极小且理由成立**。整个文件没有"管理员才跑"的环境门控——这正是它能在
> CI 上稳定跑的原因，但也同时说明它**根本没有触碰真实 Hyper-V**（见 §4）。

> ⚠️ 但包内其余文件有 13 处 `t.Skip`，其中 8 处是 `HYPOMUX_RUN_*=1` 环境变量门控
> （`engine_integration_test.go:11`、`network_routes_windows_test.go:40`、
> `diagnostics_windows_integration_test.go:13` 等）。**Hyper-V 侧没有任何等价物**：
> `reports/vnic/60` 记录的真机 `Get-VMNetworkAdapter` 读路径（策略是否允许非提权读）
> 在本套测试里**零覆盖**。

### 1.3 文件头注释已过期

`hyperv_adapter_test.go:20-22` 写着：

> 「凡是需要提权脚本的路径（Create/Remove 的提权段）不在单测覆盖内」

但 `:2014-2685` 的批量删除整节（16 个测试）恰恰覆盖了 `RemoveAdapters` 的提权段编排
（只是通过 `scriptHook` 注入）。注释与现状不符，会误导后续审查者低估覆盖面。

---

## 2. 六条用户需求的覆盖矩阵

图例：✅ 有直接且强度足够的断言 ｜ ⚠️ 部分覆盖/只测了纯函数，缺集成点 ｜ ❌ 无覆盖

| # | 需求 | 结论 | 证据 |
|---|---|---|---|
| (a) | 每张可检测到的虚拟网卡都有移除按钮（不论是否本软件创建） | ⚠️ | `buildUnmanagedHyperVStatus` → `test:519-537`；`hypervShowUnmanagedAdapter` → `test:544-567`（函数覆盖 100%）；前端镜像 `isAdapterRemovable` → `managementAdapters.test.ts:500-544`。**但 `List()` 只有 6.1% 覆盖**（见缺陷 D1） |
| (b) | 批量多选删除 `RemoveAdapters` 端到端主流程 | ⚠️ | 服务层 E2E 有：`test:2390-2427`（5 张 → 1 次脚本）。**但进程层 E2E 完全没有**：`hyperv_adapter_windows.go` 全文件 **0.0%**（见 D2） |
| (c) | 唯一硬排除 `Container NIC *` / `vEthernet (Default Switch)` | ⚠️ | 单删 ❌、批量 ✅、列表 ⚠️。批量：`test:2197-2245`、`test:2368-2387`（100% 覆盖 `hypervProtectedReason`）。见下方专节 |
| (d) | 批量提权脚本一次删 N 张（而非并发），断言"只调一次" | ⚠️ | `test:2407-2409` 断言 `len(fixture.envelopes) == 1` ✅。**"而非并发"零断言**（见 D3） |
| (e) | 台账 `data/hyperv/adapters.json` 删除后重写 | ⚠️ | 删除后重写 ✅（`fixture.ledgerNames()` 走 `loadHypervLedger` 重新读盘：`test:2289/2361/2487/2583/2679`）。原子性 ⚠️ 仅事后无 `.tmp` 残留（`test:282-291`）。**`invalidateInventory()` 零覆盖**（见 D4） |
| (f) | 识别虚拟网卡的判据（InterfaceDescription / MAC 前缀 / 名称） | ⚠️ | 名称正则 ✅ `test:85-111`；MAC 逐字节 ✅ `test:63-82`；MAC 生成前缀 `0x02` ✅ `test:159-171`。**InterfaceDescription 判据零覆盖**；**跨语言一致性零覆盖**（见 D5） |

### (c) 硬排除的三条路径逐条核对

| 路径 | 覆盖 | 说明 |
|---|---|---|
| **列表 List()** | ❌ | 实现 `hyperv_adapter.go:1336-1341` **不过滤保护名单**——`Container NIC xxx` 与 `Default Switch` 会作为普通 `managed=false` 行出现在列表里。「不给移除按钮」完全由**前端镜像** `isProtectedAdapter`（`managementAdapters.ts:125-150`）独自决定，Go 侧无测试、无标记字段 |
| **单删 Remove()** | ❌ | 保护名单在单删路径上**不可达**：`Remove` 先过 `isHyperVAdapterName`（`:2109`），而 `test:818-842` 的拒绝表里**没有**任何保护名单名字。安全但未被钉死 |
| **批量 RemoveAdapters()** | ✅ | `test:2197-2245` 断言受保护目标 `Removed=false` + 有原因 + **不进 Items** + `Interface` 还回别名；`test:2368-2387` 断言"台账里也保护" |

> **结论 (c) = ⚠️**：批量这条最危险的路径钉得很死（好），但另外两条路径一个靠前端、
> 一个靠上游闸门，Go 侧都没有测试。

---

## 3. 断言强度审查

### 3.1 弱断言清单

| 位置 | 断言方式 | 问题 |
|---|---|---|
| `test:1219-1253` | `containsHypervScript`（手写 `strings.Contains`，见 `test:1255-1266`） | 写路径脚本的**全部**安全契约只靠子串包含。禁用名单（`:1221-1232`）里**没有** `Start-Job`、`Start-ThreadJob`、`ForEach-Object -Parallel`、`Start-Process`、`Invoke-Expression`、`&`。塞进任何并行原语都不会让这条测试变红 |
| `test:1395-1435` | 同上 | 只读脚本同样只有子串断言。`InterfaceDescription` / `Get-NetAdapter -InterfaceDescription` 这段**根本不在 required 列表里**，改坏了没人为止 |
| `test:1243` | `required` 列表含 `"$targets | Remove-VMNetworkAdapter"` | 子串匹配。写成 `$targets | Remove-VMNetworkAdapter -ErrorAction Stop; Start-Job { ... }` 依然通过 |
| `test:2230` / `test:2232` | `strings.TrimSpace(row.Reason) == ""` | 只断言"原因非空"，不校验文案内容 |
| `test:2327` | `results[0].Reason == ""` | 同上 |
| `test:2476` | `strings.Contains(failed.Reason, "正在被使用中")` | 只断言包含子串，脚本原文透传若被截断/改写不会被发现 |
| `test:2354-2357` | **失效断言**，见下方 D6 | — |

### 3.2 反例断言（做对了的部分）

这点必须给分——文件里有**三类**真正的反例/注入断言，不是清一色 `Contains`：

1. **恶意 payload 注入守门人**：`test:1733` 把 `O'Brien"; rm -rf /` 塞进 payload，
   `test:1756-1760` 断言 base64 后的播种值**不含** `' \` $ 空白 ; "`，
   `test:1761-1767` 再解回来逐字节比对。这是真正的"注入应当被中和"断言。
2. **尾随 token 反例**：`test:1779-1823` 断言 `-EncodedCommand` 之后**不得有任何 token**
   （参数个数 `len(want)+1`），并解码 UTF-16LE 反向验证正文。锁定的是 reports/vnic/76 的原始故障。
3. **结构化反例**：`test:1631` 断言失败文案含 `"第 2/3 张"`；`test:1859` `errors.Is` 保持指针身份；
   `test:1055` 用 `ModTime()` 断言空操作不落盘。

**判定**：断言强度是**两极分化**的——纯逻辑/错误映射那半边很强（表驱动 + 精确等值 + 身份断言），
脚本正文那半边偏弱（纯子串）。

---

## 4. 测试辅助函数的滥用情况

`removeFixture`（`test:2025-2130`）提供两个注入钩子：`inventoryHook` + `scriptHook`。
生产侧对应 `hyperv_adapter.go:1125-1137`（`inventoryOverride` / `scriptOverride`）。

**没有滥用——恰恰相反，钩子本身写得很克制**：

- 两个 override 只在 `runScript`（`:1177`）/`readInventory`（`:1091`）最外层短路，
  **保留了整个上层编排**（归一化 → 保护名单 → 台账 → inventory → 批量打包 → reconcile → 清台账 → 清池）。
- `removeAdapters` 本身 **85.5% 覆盖**、`RemoveAdapters` **100% 覆盖**——证明主流程确实跑到了。
- 钩子带 `t.Helper()`、每个 fixture 用 `t.TempDir()` + `t.Setenv` 隔离，无跨用例状态泄漏。

**但"主流程完全没覆盖"这个判断，在进程层是真的成立**：

```
hyperv_adapter.go:1176   runScript                        4.8%
hyperv_adapter.go:1304   List                             6.1%
hyperv_adapter.go:1145   readHypervInventory              0.0%
hyperv_adapter.go:1362   Switches                         0.0%
hyperv_adapter.go:1670   awaitBatch                       0.0%
hyperv_adapter.go:1769   awaitInterfaces                  0.0%
hyperv_adapter.go:1887   awaitAddresses                   0.0%
hyperv_adapter_windows.go （全 9 个函数）                    0.0%
  └─ hypervPowerShellPath / hypervPowerShellCommand /
     hypervRunDirect / hypervRunElevatedShellExecute /
     hypervExecuteElevated / hypervExecuteUnelevated /
     hypervInventorySnapshot / hypervOutputTail /
     hypervProcessElevated
```

**从没被任何测试执行过的真实链路**：
真 PowerShell 定位与拉起、`-EncodedCommand` 的真实编码往返、`ShellExecuteEx` runas 的 UAC 流程、
父进程超时与"子进程杀不掉"的放弃等待、`context` 取消、结果文件读回、
以及 `readHypervInventory`（**0.0%**，即 UI 每秒轮询的那条读路径）。

> 这是**刻意的**（文件头 `test:20-22` 明说不碰真机），不算缺陷；但它意味着
> reports/vnic/60 记录的真机约束（"非提权读 `Get-VMNetworkAdapter` 是否被策略允许"）
> **在测试层面从未被验证过**，只能靠人工真机跑。

---

## 5. 测试自相矛盾核查

`go test` 全绿，**没有测试断言与实现行为直接矛盾的情况**。但发现 4 处"断言与注释不符"
或"断言实际失效"的情况，按严重度列在 D 节。

另有一处**未经验证即写成事实**的注释，见 D7。

---

## 6. 缺陷清单（按严重度）

### 🔴 D1 — `List()` 只有 6.1% 覆盖：需求 (a) 的集成点完全没测

- `hyperv_adapter.go:1304-1344` 的合并循环（台账行 → `isHyperVAdapterName` 过滤 →
  `claimed` 去重 → 台账外行 → `sortHyperVStatuses`）**没有任何测试**。
- 全文件唯一一次 `service.List()` 在 `test:850`，且只断言 `Shutdown` 后返回错误。
- **`inventoryHook` 恰好能被 `List()` 用上**（`readInventory` 第一件事就是查 override，`:1091`），
  写这条测试的成本几乎为零，却没写。
- 风险：`test:544` 的注释自称要"防止有人把命名过滤加回来"，但它守的是
  `hypervShowUnmanagedAdapter`；**真正存在的生产过滤** `hyperv_adapter.go:1323`
  （台账侧的 `isHyperVAdapterName`）没有任何测试守护。有人把它收紧回严格模式，
  台账里名字不合规的旧记录会从 UI 消失，**全绿**。
- 同类问题：`Switches()`（`:1362`）**0.0%**，交换机列表（含 External + AllowManagementOS
  的准入判定 `:1366`）也零覆盖。

### 🔴 D2 — `runScript` 4.8%：超时 / 结果文件 / UAC 取消的真实语义零覆盖

`scriptHook` 短路在 `:1177`，把 `runScript` 的全部实体逻辑挡在测试视野外：
超时放弃等待（`:1170-1175` 注释明说"提权子进程杀不掉"）、结果文件缺失时 `result == nil`
的不确定态、进程退出码映射。这些恰好是 reports/vnic/72 列的"未验证项"。
可接受（需真机），但应显式记为**已知未覆盖**，而不是让 61.7% 的整包覆盖率掩盖了它。

### 🟠 D3 — 需求 (d) 的"而非并发"零断言

- ✅ 有：`test:2407` 断言 `len(fixture.envelopes) == 1`。
- ❌ 缺：脚本内的**串行性**与**无并发原语**没有任何断言。
  - 实现侧串行证据在 `hyperv_adapter.go:2664`（`foreach ($item in $items)`），
    是一条**没有任何测试读过的文本**。
  - `test:1221-1232` 的禁用名单缺 `Start-Job` / `Start-ThreadJob` /
    `ForEach-Object -Parallel` / `Start-Process` / `Invoke-Expression`。
  - 子串断言 `$targets | Remove-VMNetworkAdapter`（`test:1243`）挡不住在后面追加并发语句。

### 🟠 D4 — 需求 (e)：批量路径的超时值被记录但从未断言

- `fixture.timeouts` 在 `test:2049 / 2062 / 2078` 被采集，**全文再无一处读取**
  （grep `\.timeouts` 只有这 3 处赋值）。
- 后果：把 `hyperv_adapter.go:2203` 的
  `hypervRemoveBatchTimeout(len(items))` 改成 `hypervRemoveScriptTimeout`（90s 写死），
  **整套测试依然全绿**——因为 `test:2430-2449` 只测了那个纯函数本身，从没验证批量路径真的用它。
- 同类悬空采集：`fixture.elevated` 只在 `test:2416` 读了一次（`[0]`），
  其余 16 个测试采集了 `elevated` 却从不检查——"批量必须走提权"只在
  `TestRemoveAdaptersUsesOneScriptForMany` 一条里被守住。
- 附带：`hypervRemoveBatchTimeoutMax = 180s`（`hyperv_adapter.go:65`）的**字面值从未被钉住**
  ——`test:2438-2439` 用的是常量本身（`{3, hypervRemoveBatchTimeoutMax}`），把 180 改成 1 小时照样绿。
  只有 `{1:90s} {0:90s} {2:180s}` 是字面量钉死的。

### 🟠 D5 — 需求 (f)：`InterfaceDescription` 判据与跨语言一致性零覆盖

- Go 侧 `InterfaceDescription` 只出现在**交换机上行口解析**（`hyperv_adapter.go:2601`
  与只读脚本 `:2819` 的 `Get-NetAdapter -InterfaceDescription $uplink`），
  虚拟网卡本身**不靠它识别**（靠 `Get-VMNetworkAdapter -ManagementOS` 全量 + 名字/MAC 判定）。
- 需求点名的三个判据的真实覆盖：名称 ✅、MAC ✅、**InterfaceDescription ❌**。
- 更要紧的是**跨语言漂移**：前端 `managementAdapters.ts:125-150` 与 Go
  `hypervProtectedReason`（`:2012-2028`）是两份**手工复制的同一份规则**，
  源码注释 `:107-116` 自己承认"There is no build-time link between the two sides"。
  两侧各有单测（`managementAdapters.test.ts:500-544` 与 `test:2152-2187`），
  **但没有任何测试比对两边会漂移**。保护名单一旦单边改动，后果是"按钮还在、后端拒绝，
  用户已经点完确认、UAC 已经弹了"——正是注释 `:113-115` 自己写的那个 worse 场景。

### 🟡 D6 — 失效断言：把运行时文案和格式串模板比较

`test:2354-2357`：

```go
if results[0].Reason != "%s 的 MAC（%s）与台账记录（%s）不一致，已跳过删除" &&
    !strings.Contains(results[0].Reason, "不一致") {
```

第一个条件拿**已代入实参的运行时字符串**去和**未代入的格式串模板**比，`!=` 恒为真，
整个条件退化成 `!strings.Contains(..., "不一致")`。

实现侧 `hyperv_adapter.go:2048-2053` 专门写了 `hypervMACMismatchReason()`，
并在注释里要求两条路径"必须**逐字一致**"。测试**完全没用这个 helper**，
所以"逐字一致"这个契约没被验证——`test:2354` 的注释自吹"原因应与单张路径同源"是落空的。

正确写法应是 `results[0].Reason != hypervMACMismatchReason("HypoMux-vnic-01", "AA:BB:CC:DD:EE:FF", "02:1a:2b:00:00:01")`。

### 🟡 D7 — 未经验证即写成事实的注释：`vEthernet (WSL)`

`test:2180` 把 `"vEthernet (WSL)"` 放进"普通卡绝不能被误判进保护名单"的列表，
注释写：「不在名单上：**它压根不会出现在 List() 的输入域里**」。

这个断言**没有任何测试或实测支撑**：
`Get-VMNetworkAdapter -ManagementOS` 返回的是挂在 Management OS 上的全部 Hyper-V vNIC，
WSL2 的 `vEthernet (WSL)` 正是 WSL 内部交换机的宿主侧接口（`AllowManagementOS` 为真），
**极可能就在 List() 的输入域里**。

如果属实，这带来两个后果：
1. WSL / Docker 网络接口出现在"虚拟网卡"页，且**有移除按钮**；
2. 它与 `vEthernet (Default Switch)` 是**同一类基础设施**——删了同样断 WSL/Docker 网络，
   却不在"唯一硬排除"里，与需求的意图相悖。

建议：要么把 `vEthernet (WSL)` / WSL 交换机纳入保护名单，要么至少把注释改成
「尚未确认，列为未验证项」。

### 🟡 D8 — 整节 RemoveAdapters 测试是 Windows-only，但既无 build tag 也无 GOOS 守卫

- `hypervPlatformSupported()` 在 `hyperv_adapter_other.go:16`（`//go:build !windows`）返回 `false`。
- `removeAdapters` 在 `:2129` 第一步就 `return nil, hypervUnsupportedError()`。
- `removeFixture`（`test:2035-2055`）**没有**平台 override 钩子
  （全包 grep `platformHook|platformOverride` 零命中）。
- ⇒ 在非 Windows 上，`test:2197-2685` 的 **16 个测试全部会 FAIL（不是 SKIP）**。
- `test:1388` 的注释宣称"因此在没有 Hyper-V 的机器上（含 CI 的 windows runner）同样能编过并通过"
  ——对只读脚本那节成立，对后来新增的批量删除整节**不成立**。
- 缓解：CI 目前只在 `windows-2025` 上跑 `go -C desktop test ./...`（`.github/workflows/build.yml:161`），
  所以**不会真的炸**；且该包在 Linux 上本就编不过（`engine_integration_test.go:70: undefined: proxyMarkerPath`，
  `GOOS=linux go vet` 已实测）。因此这是**潜在**问题，不是活跃 CI 故障。

### 🟡 D9 — 单删 `Remove()` 的两条错误路径未被钉死

- MAC 闸门在单删路径上的报错（`hyperv_adapter.go:2186-2188` → `not_managed` + MAC 文案）零覆盖。
  `test:818-842` 只覆盖命名闸门。
- 另有一处**单删/批量的行为不对称**未被记录：
  `Remove("vEthernet (HypoMux-vnic-01)")` 会在 `:2109` 被 `isHyperVAdapterName` 拒绝
  （别名形态不合规），而 `RemoveAdapters` 走 `hypervUnwrapHostInterface` 能正常处理
  （`test:2595` 明确覆盖了 `"vEthernet (xuni-02)"`）。
  前端已绕开（`VirtualAdaptersPage.tsx:480-486` 注释：单卡也必须走 `removeAdapters`），
  但 Wails binding 上 `Remove` 仍可达，这个不对称没有任何测试或注释记录。

---

## 7. 一句话结论

**逻辑层测试质量高**（表驱动 + 精确等值 + 真实注入反例，批量删除编排 85.5%~100% 覆盖），
**集成层为零**（`List()` 6.1%、`runScript` 4.8%、`hyperv_adapter_windows.go` 全文件 0.0%）。

六条需求里 **(b)(d)(e) 基本达标**（服务层白盒断言到位，含"一次脚本""不进 Items""重读实况"），
**(a)(c)(f) 只在纯函数层达标，集成点与跨语言一致性没有守住**。
最值得立刻修的三件事：
① 给 `List()` 补一条 happy-path 测试（`inventoryHook` 已就绪，几乎零成本）；
② 把 D6 那条失效断言换成 `hypervMACMismatchReason(...)` 精确比对；
③ 把 `Start-Job|Start-ThreadJob|-Parallel|Invoke-Expression` 加进 `test:1221` 的禁用名单。