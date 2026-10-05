# 82 · Go 侧批量删除宿主 Hyper-V 虚拟网卡

> 状态：**已完成**。门禁全绿（见 §4），未验证项见 §5。
> 写范围：`desktop/internal/services/hyperv_adapter.go`、`hyperv_adapter_windows.go`、
> `hyperv_adapter_other.go`、`hyperv_adapter_test.go`

## 0. 基线（改动前，本机真实退出码）

```
gofmt -l internal\services   -> 空输出，exit 0
go vet ./...                  -> exit 0
go test -count=1 ./...        -> exit 0（services 包 45.9s）
```

## 1. 问题

`HyperVAdapterService.Remove(name)` 有三重闸门，只删得了本工具自己建的卡：

| 闸门 | 位置（改动前） | 文案 |
|---|---|---|
| 命名规范 | `hyperv_adapter.go:1889` | `%q 不符合 HypoMux 命名规范（%sNN），已跳过删除` |
| 台账命中 | `hyperv_adapter.go:1905` | `%s 不在 HypoMux 台账中，已跳过删除` |
| MAC 逐字节一致 | `hyperv_adapter.go:1924` | `%s 的 MAC（%s）与台账记录（%s）不一致，已跳过删除` |

用户宿主机上手工建了 5 张 `xuni-01..05`，页面上看得见（`List()` 的台账外只读行），
`Remove` 却永远返回 `not_managed`。用户要求：**页面上能检测到的每张 Hyper-V 虚拟网卡都要有删除能力**。

## 2. 落地的东西

新增导出契约（冻结，未改设计）：

```go
type HyperVRemoveResult struct {
	Name      string `json:"name"`
	Removed   bool   `json:"removed"`
	Reason    string `json:"reason"`
	Interface string `json:"interface"`
}
func (s *HyperVAdapterService) RemoveAdapters(names []string) ([]HyperVRemoveResult, error)
```

内部结构：一条共享私有核心

```go
func (s *HyperVAdapterService) removeAdapters(names []string, requireLedger bool) ([]HyperVRemoveResult, error)
```

- `Remove(name)` → `removeAdapters([]string{name}, true)`
- `RemoveAdapters(names)` → `removeAdapters(names, false)`

`requireLedger=true` 时两种「跳过」会翻译成**错误**而不是结果行：台账未命中 →
原 `not_managed` 文案；`resolve()` 判出的原因（含 MAC 闸门）→ `not_managed` + 该原因串。
批量模式（`false`）下同样的原因只是结果行的 `Reason`，整批仍返回 nil error。

### 2.1 `Remove` 的字节级保留

`Remove` 保留了它自己的 `checkOpen()` + `isHyperVAdapterName` + `hypervPlatformSupported`
前导段，一行没动 —— 包括**闸门顺序**（命名规范在前、平台检查在后）与错误文案里用
**原始入参 `name`** 而非归一化后的 `target`（`%q` 的引号内容因此不变）。只有最后那句
`updateLedger / invalidateInventory / applyPoolUpdate` 被核心接管。

这次拆分没有造成 `Remove` 的错误文案/返回形态变化，因此不需要退回「独立实现 + 允许重复」。
唯一的行为等价性前提是 `normalizeHypervRemoveNames` 对单个 `HypoMux-vnic-NN` 是恒等的：
它会 trim、剥 `vEthernet (...)` 前缀、按小写去重，这三步对符合命名规范的卡都是空操作。

### 2.2 输入归一化

`normalizeHypervRemoveNames`：trim → 剥通用 `vEthernet (...)` 宿主别名 → 丢空 → 按小写去重 → **保序**。

这里新写了一个 `hypervUnwrapHostInterface`，而不是复用既有的 `hypervObjectNameFromInterface`：
后者只认 `HypoMux-vnic-NN` 前缀（见其实现），对 `vEthernet (xuni-01)` 这类**台账外卡**的别名
完全无效。前端把宿主别名传过来是常态，必须能还原成对象名才能去 inventory 里核。

归一化后为空 ⇒ 返回空切片 + nil error（幂等，不报错、不弹 UAC）。

### 2.3 保护名单：为什么只有这三项、为什么是硬排除

实现为纯函数 `hypervProtectedAdapter(name) bool` / `hypervProtectedReason(name) string`：

| 判据 | 为什么硬排除 |
|---|---|
| 名字（小写）以 `container nic` 开头 | Hyper-V 为 Windows 容器 / Docker 合成的对象。删掉直接断容器网络，且宿主上未必有开关能重建回原状。 |
| `vEthernet (Default Switch)`（大小写不敏感） | Hyper-V 自管 Internal 交换机在宿主上的接口。删它会断开所有未绑定交换机的虚拟机网络。 |
| 空白名 | 没有名字就没有可定位对象，脚本只能瞎猜。 |

**硬排除 = 压根不进提权脚本的 `Items`**，不是「进了脚本但标个 false」。
差别很实：一旦进了 `Items`，脚本就会真的去 `Remove-VMNetworkAdapter`；保护名单必须
是「这道目标从没跨过 UAC 那道门」。对应测试 `TestRemoveAdaptersProtectedNeverEntersScript`
直接断言 `Items` 里没有它。

名单是**按 Hyper-V 对象语义**判的（`Container NIC …`、`Default Switch`），
不是按本工具的台账判的 —— 所以即使有人手工往 `settings.json` 台账里塞了
`Container NIC {…}`，照样删不掉（`TestRemoveAdaptersProtectedEvenWhenInLedger`）。

保护名单优先于台账、优先于 inventory，位置在归一化之后、任何 I/O 之前。

### 2.4 台账外卡：跳过闸门 ≠ 不核存在

前两道闸门推翻了，但「能删」不等于「照单全收」。台账外的卡在 `inventory` 里必须
**此刻确实存在**：DeviceId 优先、名称兜底（沿用 `hypervInventory.find`）。
inventory 里 MAC 与 DeviceId 全空的**幽灵记录**（`reports/vnic/60` 在真机上实测到过）
不可删除 —— 脚本按 MAC 定位，MAC 空就是无法定位。

这条路径上 `hypervEnvelopeItem.AdapterID` **故意留空**：台账外卡没有权威 DeviceId，
本轮读到的 DeviceId 只是某次 Get-VMNetworkAdapter 的输出，拿它当权威反而可能删错对象。
脚本侧 `adapterId` 为空时按归一化 MAC 匹配（既有逻辑，未改）。

用户可见原因分了两档，避免误导排查方向：真有一条同名但 MAC 为空的记录 ⇒ 报「幽灵记录」；
压根查无此名 ⇒ 报「找不到，可能已被手工删除」。

### 2.5 第三重闸门保留

受管卡（台账命中）仍走 `hypervMACEqual`。台账 MAC 与实况不一致就跳过并沿用原
文案（`hypervMACMismatchReason`，与原字面量逐字节同源）。前两道闸门被推翻的理由
是「这张卡不是我们建的」；第三道挡的是**另一个东西**：台账记录已经过期 —— 这张卡
被删掉重建、或复用过。按旧记录去删就是在删一个已经不属于我们的对象。

### 2.6 一次提权删 N 张

通过校验的 item 打进**同一个** `hypervEnvelope{Op:"remove", Items: items}`，只调**一次**
`runScript`。提权脚本正文一个字没改 —— `remove` 分支本来就在 `foreach ($item in $items)`
里逐张处理并把失败收集进 `$failures`，这正是 `Create` 批量创建在用的同一套机制。

超时 `hypervRemoveBatchTimeout(n) = min(hypervRemoveScriptTimeout * n, hypervRemoveBatchTimeoutMax)`，
上限 `180s`。为什么不能沿用单卡的 90s：脚本是**串行**逐张删的，N 张的墙钟时间天然是 N 倍，
沿用 90s 会在第 3 张还在正常删除时就把等待掐掉。为什么必须封顶：`runScript` 的超时语义
是「放弃等待、什么都不改」，它**杀不掉**那个已提权的子进程，于是「卡住」会退化成
「宿主上挂着一个跑不完的 powershell.exe」。180s 是「够 2 张慢卡慢慢删完」与
「不至于让人以为程序死了」之间的折中。

### 2.7 部分失败不中止整批

`hypervReconcileRemoveRows` 的判定分三层：

1. `certain := runErr == nil && result != nil && result.OK` —— 脚本整体是否可信。
   不可信 ⇒ 一张都不算成功（无从确知脚本到底做了什么，绝不能清台账）。
2. `named` —— `failures` 里按名字能对上这张卡的失败 ⇒ 这张没删掉。
3. `anonymous` —— `failures` 非空但**一条都对不上任何名字** ⇒ 无从证明剩下几张删掉了，
   一律不算成功。

第 3 条是这轮补的（`TestRemoveAdaptersUnattributedFailureBlocksSuccess` 守着）。
代价是「脚本整体成功但有一条无主失败」时会把整批判失败 —— 但这个方向是保守的：
误报失败的代价是用户再点一次删除，误报成功的代价是台账和出口池被清掉而卡还在。

单张失败**只出现在结果里**，绝不冒泡成整批 error。

### 2.8 同步顺序：先跑脚本，成功之后才清台账与出口池

顺序写在 `removeAdapters` 里：**`runScript` → 清台账 → `invalidateInventory()` → `applyPoolUpdate`**。

反过来的顺序（先摘池、后删卡）失败窗口是「永久」的：脚本失败时用户看到的是
「卡还在、池子里没了」，而池子是我们唯一记着这张卡的地方 —— 用户还得手工把它加回来，
中间出口池一直是错的。正序的失败窗口只有「一次 `Remove-VMNetworkAdapter` 动作的时长」
（毫秒级），而且这段时间里卡可能已经被删了、池子还挂着它；引擎侧有 watchdog 会自愈，
下一次选择器就会发现这张卡不在系统里了。

换句话说：错方向的不一致需要人介入才能收敛，对方向的不一致机器自己会收敛。

### 2.9 清理：台账 + 出口池 + 权重

对每个 `Removed=true`：

- 受管卡 ⇒ `ledger.removeEntry(name)`；台账外卡 ⇒ **不动台账**（本来就没有它）。
- 出口池 `selected_adapter_ids` 移除它的宿主别名。
- **`adapter_weights` 删掉同名的权重键。**

最后一条确认了是 `applyPoolUpdate` 的真实缺口并已补：原本的 `delete(weights, trimmed)`
写在「遍历 selected」的循环体内，只有当这个键**当时还在 `selected_adapter_ids` 里**
才会执行。于是「有权重、没被选中」的悬空权重键删网卡时活了下来 —— 调度器之后会按一张
已经不存在的网卡参与权重分配。修法是在补默认权重之前**无条件**遍历 `remove` 删一遍
（放前面是为了万一某键同时出现在 add/remove 里，「新增」赢）。

同一函数里还补了第二个缺口：变更检测只遍历**新**权重表，纯删除在「按新表找差异」时
不可见，会被误判成「没变」直接 `return nil` 而不落盘 —— 而删网卡走的恰恰是这条路径。
因此在遍历前先比一次 `len(weights) != len(current.AdapterWeights)`。

幂等路径：受管卡在 inventory 里已经找不到时，不弹 UAC，但照常清台账与出口池
（`TestRemoveAdaptersIdempotentWhenAlreadyGone`）。用户手工删过再点一次删除，不该报错。

## 3. 测试清单

全部靠两个注入钩子跑，**不弹 UAC、不碰真实 Hyper-V**：
`inventoryHook`（既有）伪造系统实况；`scriptHook`（本轮新加）伪造提权脚本，
把「一次批量只调一次」「Items 里究竟有什么」断言成白盒 —— 这两条性质在真机上看不出来。

### 3.1 新增用例

| 用例 | 钉住的性质 |
|---|---|
| `TestHypervProtectedAdapter` | 保护名单 10 项（含大小写、空白名、别名/裸名两态）+ 反面 6 项（`my container nic 1`、`xuni-01 container` 之类不得误判）；被保护必须带可展示的原因 |
| `TestRemoveAdaptersProtectedNeverEntersScript` | 4 个目标里 3 个受保护：`Removed=false` + 有原因 + **不在 Items**；结果 `Interface` 把宿主别名还给前端 |
| `TestRemoveAdaptersDeletesUnmanagedCard` | 台账外卡可删；`Item.AdapterID == ""` 且 MAC 非空 |
| `TestRemoveAdaptersKeepsLedgerForUnmanagedCard` | 台账外卡删后台账**不变** |
| `TestRemoveAdaptersSkipsGhostRecord` | MAC/DeviceId 全空 ⇒ 不可删、原因点明「幽灵记录」、不进脚本 |
| `TestRemoveAdaptersSkipsMissingUnmanagedCard` | 查无此名 ⇒ 跳过且**不**误报成幽灵记录 |
| `TestRemoveAdaptersKeepsMACGateForManagedCard` | 第三重闸门仍在：跳过 + 同源文案 + 台账原样保留 |
| `TestRemoveAdaptersProtectedEvenWhenInLedger` | 保护名单优先于台账 |
| `TestRemoveAdaptersUsesOneScriptForMany` | 5 张卡 ⇒ `runScript` **恰好 1 次**、`Items` 长度 5、`Op=="remove"`、走提权 |
| `TestRemoveAdaptersTimeoutScalesAndCaps` | 超时曲线 `{1:90s, 0:90s, 2:180s, 3:180s, 16:180s}` 且上限 > 单张 |
| `TestRemoveAdaptersPartialFailure` | 3 张里第 2 张失败：整批 nil error；失败的带脚本原文原因；成功的照常清台账 + 清池 + **清权重**，失败那张的权重原封不动 |
| `TestRemoveAdaptersAllFailedReturnsNilError` | 全失败仍 nil error，逐行带原因 |
| `TestRemoveAdaptersRunErrorMarksAllFailed` | 提权脚本整体失败（UAC 取消）⇒ 一张都不算成功、台账原样保留 |
| `TestRemoveAdaptersUnattributedFailureBlocksSuccess` | 脚本 `OK=true` 但带无主失败 ⇒ 谁都不算成功 |
| `TestRemoveAdaptersNormalizesInput` | trim/丢空/小写去重/**保序**；同一张卡不进 Items 两次 |
| `TestRemoveAdaptersEmptyInputIsIdempotent` | nil / 空 / 全空白 ⇒ 空切片 + nil error + 不触发脚本 |
| `TestRemoveAdaptersInventoryFailureAbortsBatch` | inventory 读失败是整批级错误（`unavailable`）+ 无结果 + 不触发脚本 |
| `TestRemoveAdaptersIdempotentWhenAlreadyGone` | 系统里已不存在 ⇒ 不弹 UAC，但台账与权重照清 |
| `TestApplyPoolUpdateRemovesDanglingWeight` | 「有权重、没被选中」的悬空权重键必须清掉（`applyPoolUpdate` 两个修复的专项守卫） |

### 3.2 既有用例保持通过（未改任何行为断言）

`TestRemoveRejectsForeignAdapters`、`TestRemoveAfterShutdown`、
`TestRemoveKeepsLedgerWhenInventoryQueryFails`、
`TestApplyPoolUpdateCreateThenRemoveIsSymmetric`、`TestApplyPoolUpdateMerge`、
`TestApplyPoolUpdateDoesNotMutateCallerSlice`、`TestApplyPoolUpdatePreservesSchedulingStrategy`。

其中 `TestRemoveRejectsForeignAdapters` 仍是单张路径的守门人：它把 `以太网` / `xuni-01` /
`vEthernet (WSL)` / `HypoMux-Tun` / `HypoMux-vnic-77` 全喂给 `Remove`，要求一律
`not_managed` —— **`Remove` 的第一道闸门必须原样保留**，新能力只从 `RemoveAdapters` 出。

### 3.3 回归探针（注入缺陷 → 转红 → 回滚 → 复跑绿）

全程只用定点 `edit`，没有用 PowerShell 改写源文件。

| 探针 | 注入的缺陷 | 转红证据 | 回滚后 |
|---|---|---|---|
| A | `applyPoolUpdate` 里无条件删权重那个循环加上 `&& false` | `TestApplyPoolUpdateRemovesDanglingWeight`：`悬空权重键残留：map[vEthernet (xuni-01):9]`，exit 1 | exit 0 |
| B | `hypervReconcileRemoveRows` 的 `anonymous` 守卫改成 `anonymous := false` | `TestRemoveAdaptersUnattributedFailureBlocksSuccess`：`存在无主失败时 xuni-01 不得记为已删除：{Name:xuni-01 Removed:true …}`，exit 1 | exit 0 |

探针 B 第一次尝试时写成了删掉 `|| anonymous`，结果编译期就炸
（`hyperv_adapter.go:2319:2: declared and not used: anonymous`），也算一种转红，
但真正想要的是**行为**转红，所以改成 `anonymous := false` 重做了一遍。

## 4. 门禁真实退出码（最终状态，全部我自己跑的）

```
gofmt -l internal\services   -> 空输出，GOFMT_EXIT=0
go vet ./...                  -> VET_EXIT=0
go test -count=1 ./...        -> TEST_EXIT=0（services 包 45.230s，8 个包全 ok）
GOOS=linux   go build ./internal/services/   -> LINUX_BUILD_EXIT=0
GOOS=darwin  go build ./internal/services/   -> DARWIN_BUILD_EXIT=0
GOOS=windows go build ./internal/services/   -> WIN_BUILD_EXIT=0
```

`GOOS=linux`/`darwin` 单独跑是因为 CI 只跑 windows，`:1884` 附近的 `!windows` 分支
覆盖不到 —— 现在能编译，但**行为仍未在非 Windows 上验证过**（见 §5）。
仓库既有的 `engine_integration_test.go:70` darwin 下 `undefined: proxyMarkerPath`
不是本轮引入的，按约定没去动。

## 5. 我**没有**验证的部分（请当作已知风险）

1. **真机 UAC 全流程。** 全部新用例都走注入钩子，`runScript` 的真实提权、结果文件
   落地、`OK/Code/Error` 的真实取值一次都没跑过。批量一次弹一次 UAC 是「Items 打包在
   一个信封里」这个结构推出来的，不是看出来的。
2. **部分失败时宿主的真实状态。** `Failures` 的逐项归因是按脚本 `$failures[].name`
   与我提交的 `Name` 匹配推的。真机上 PowerShell 报回来的 name 形态（大小写？带 GUID？
   带 `{}`？）我只对齐了「脚本按我给的名字原样回填」，没有实测样本。
   一旦名字对不上，会退到 `hypervFailureMessage` 的「单条无主失败」兜底 —— 那个兜底是
   保守方向（整批判失败），但会让**本该成功的卡也显示失败**。
3. **权重清理是否真生效。** 只验了 `settings.json` 里的 `adapter_weights` 键确实被删。
   调度器那边拿到这份配置后实际行为如何（是否还会读到残留、是否有缓存）没有跟。
4. **`Default Switch` 的判定边界。** 真机上 `Get-VMNetworkAdapter` 报的是对象名
   `Default Switch`，前端传的可能是宿主别名 `vEthernet (Default Switch)`，两种形态我都
   判了；但**自建 Internal 交换机**（名字不叫 Default Switch）的宿主接口不在保护名单里，
   会被当普通卡删掉。删掉它宿主上会不会留下悬空的交换机连接，我没法在当前环境验证 ——
   这条要么实测，要么保守扩名单。
5. **并发。** `opMu` 保证删除与创建不交错，但 `RemoveAdapters` 的「读台账 → 读 inventory
   → 跑脚本 → 清台账」这一整段都在锁内；真机上 N 张卡串行删除期间卡住 180s 的极端情况
   （比如某张卡的虚拟交换机正在关机）没有实测数据。
6. **非 Windows 编译。** 只保证 `GOOS=linux/darwin` 能编过，没在那些平台上跑过删除路径。