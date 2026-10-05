# Hyper-V 虚拟网卡 Go 后端深度代码审查

- 审查人：`hyperv-go-review`
- 审查对象：`desktop/internal/services/hyperv_adapter.go`（2868 行）、`hyperv_adapter_windows.go`（276 行）、`hyperv_adapter_other.go`（30 行）、`hyperv_adapter_test.go`（2717 行），以及与之配套的前端保护名单镜像
- 仓库：`<repo>`，HEAD `fc821b16bd55630ac6cba4311179e1a1b6644ded`
- 性质：**只读审查**。未修改任何源码，未 `git add` / `git commit`
- 参照报告：`reports/vnic/70-frozen-hyperv-interface.md`、`61`、`62`、`72`、`75`、`80`、`82`、`30`（只作意图参照，结论以代码现状为准）

---

## 验收结论

**有条件通过**

六条需求全部落地，批量删除方案与用户拍板的一致，未发现命令注入面，跨平台构建与全量测试通过。但存在 **1 个可复现的逻辑缺陷**（单张删除路径吞掉脚本回报的逐条失败，会造成"台账被清、网卡还在、此后永远删不掉"的不可恢复状态）与 **2 处代码注释与实现相反**（会误导后续维护者）。三个问题都不影响当前 UI 主流程（前端一律走批量 API），因此不判不通过。

---

## 需求逐条核对

### 需求 1：每张能检测到的虚拟网卡都有移除按钮（不管是不是本软件创建的）

**满足**（附带一处遗留能力不一致）

| 证据 | 位置 |
| --- | --- |
| `isAdapterRemovable = !isProtectedAdapter`，注释明确写"不检查 `managed`，归属只是行的描述，可删除性是宿主属性" | `desktop/frontend/src/components/vnic/managementAdapters.ts:141-150` |
| 单行删除按钮由 `removable` 决定渲染 | `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:471-472` |
| 后端批量路径**没有任何归属闸门**：`row.managed = index >= 0` 之后直接进 `items`，台账外的卡照删 | `desktop/internal/services/hyperv_adapter.go:2148-2164`、`:2194-2200` |
| 台账外的卡不进台账清理（无记录可清），但仍从出口池移除 | `hyperv_adapter.go:2237-2238` |
| 未纳管卡解析：必须在实况里存在且 MAC 非空 | `hyperv_adapter.go:2258-2300`（`hypervRemoveRow.resolve`） |
| 测试覆盖"删台账外的卡且台账保持不变" | `hyperv_adapter_test.go:2249` `TestRemoveAdaptersDeletesUnmanagedCard`、`:2280` `...KeepsLedgerForUnmanagedCard` |

**不一致点**：Wails 仍导出旧方法 `Remove()`，它保留 `isHyperVAdapterName` 命名闸门 + 台账归属闸门，对未纳管卡返回 `not_managed`（`hyperv_adapter.go:2104-2119`、`:2157-2159`）。当前前端无任何调用点（`VirtualAdaptersPage.tsx:486` 与 `:594` 都调 `removeAdapters`），但方法在 `frontend/bindings/.../hypervadapterservice.ts` 里仍可被任意 JS 调用。即"能不能删"取决于调用哪个 API——见阻塞项 P1。

### 需求 2：刷新按钮旁批量多选删除（多选 + 一键删）

**满足**

| 证据 | 位置 |
| --- | --- |
| 删除/多选/全选/刷新/新建按钮同处一个 header 行（`</div></div>` 收尾） | `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:280-351` |
| 非选择态显示"删除"进入多选模式；无任何可删行时按钮禁用 | `HyperVAdapterPanel.tsx:331-340` |
| 选择态显示"全选 + 删除所选 (N) + 退出" | `HyperVAdapterPanel.tsx:295-327` |
| 全选只勾可删行，受保护行永不进批量 | `HyperVAdapterPanel.tsx:190-197`（`removableRows`）、`:193-196` 注释 |
| 选择项在提交前再剪一次（受保护行、3s 轮询期间消失的行都剔掉） | `HyperVAdapterPanel.tsx:176-189` + `managementAdapters.ts:152-155` |
| 页面侧同步剪枝 + 归一化 | `VirtualAdaptersPage.tsx:184-189`、`:551`、`:564-570` |

### 需求 3：唯一硬排除 `Container NIC *` 与 `vEthernet (Default Switch)`，其余一律允许删

**满足**（前后端双实现，后端为准）

| 证据 | 位置 |
| --- | --- |
| `hypervProtectedReason` 是纯函数，同时覆盖**对象名形态**与**宿主别名形态**：`container nic` 前缀（对 trimmed 与 unwrap 后的名字各判一次）、`Default Switch` 的别名形态与裸形态 | `hyperv_adapter.go:2012-2028` |
| `hypervUnwrapHostInterface` 剥掉 `vEthernet (...)` 外壳；`hypervHostInterfaceName` 反向合成 | `hyperv_adapter.go:1989-2001`、`:485-487` |
| **保护名单是第一道，且在读台账 / 读实况之前**——受保护目标连脚本都进不去 | `hyperv_adapter.go:2150-2155` |
| 整批都被拦下时不弹 PowerShell、不读实况 | `hyperv_adapter.go:2165-2168` |
| 前端镜像：名字前缀 `container nic` + `interfaceName` 精确等值 `vEthernet (Default Switch)` | `managementAdapters.ts:117-139` |
| 选择 / 对话框 / 计数三处都用 `removableAdapters` 过滤，界面不出现受保护行的复选框 | `HyperVAdapterPanel.tsx:182-197`、`:515`、`553-554` |
| 测试：10 条正向（含空串、纯空白、大小写、两形态）+ 6 条反向（`xuni-01 container`、`my container nic 1`、`vEthernet (WSL)`） | `hyperv_adapter_test.go:2152-2187` `TestHypervProtectedAdapter` |
| 测试：断言受保护目标**不进入 envelope Items**，而不只是结果里标 `Removed=false` | `hyperv_adapter_test.go:2197-2248` `TestRemoveAdaptersProtectedNeverEntersScript` |
| 测试：即便在台账里也仍然受保护 | `hyperv_adapter_test.go:2368` `TestRemoveAdaptersProtectedEvenWhenInLedger` |

**边界评估（有未验证前提）**：`vEthernet (WSL)` 不在保护名单上。`hyperv_adapter_test.go:2180` 的注释把它放进"普通卡绝不能被误判进保护名单"的列表，并断言它「压根不会出现在 `List()` 的输入域里」——**但这个前提没有任何测试或实测支撑**。输入域是 `Get-VMNetworkAdapter -ManagementOS`（`hypervReadInventoryScript` `hyperv_adapter.go:2806`），WSL2 的 `vEthernet (WSL)` 是否落在该枚举里取决于 WSL 交换机 `WSL` 的 `AllowManagementOS` 取值，本次审查**无法从代码判定，必须真机核对**。

- 若**不**出现（预期）：按需求 3 字面执行正确，无影响。
- 若**出现**：HypoMux 会把它当普通 vNIC 展示**并给出移除按钮**，而它与 `vEthernet (Default Switch)` 属于同一类 WSL/Docker 基础设施，删了同样断 WSL/Docker 网络——却不在"唯一硬排除"里，与需求意图相悖。

建议：真机跑一次 `Get-VMNetworkAdapter -ManagementOS | Select-Object Name, SwitchName` 确认；在确认之前，`hyperv_adapter_test.go:2180` 的注释应从断言降级为"未验证，见 P4"。

**镜像漂移风险**见阻塞项 P3（后端保护 4 种形态，前端只覆盖 2 种；真机输出形态下不触发）。

### 需求 4：合成一次提权脚本删 N 张（不是线程并发）

**满足，完全按用户选定的方案实现**

| 证据 | 位置 |
| --- | --- |
| 所有通过判定的行打进**同一个** envelope，`runScript` 只调一次 | `hyperv_adapter.go:2194-2203` |
| 超时按张数放大并封顶：`min(90s × N, 180s)`，N=0 兜底按 1 张算 | `hyperv_adapter.go:2037-2046` `hypervRemoveBatchTimeout` |
| `opMu` 在整个提权调用期间持有，**先跑脚本、确认成功之后再清台账与出口池**（反序残留不可自愈，正序残留是"台账多一条指向已删网卡的记录"，`List()` 显示 absent，重删一次幂等收干净） | `hyperv_adapter.go:2137-2138`、`:2220-2227` |
| 读实况只做一次，N 张共用 | `hyperv_adapter.go:2177` |
| 台账清理合并成**一次** `updateLedger`，出口池合并成**一次** `applyPoolUpdate` | `hyperv_adapter.go:2240-2249`、`:2251` |
| 测试：断言提权脚本只被调 1 次、Items 条数正确、`Op=="remove"`、`elevated[0]==true` | `hyperv_adapter_test.go:2390-2427` `TestRemoveAdaptersUsesOneScriptForMany` |
| 测试：精确断言 1→90s、0→90s、2→180s、3→180s、16→180s | `hyperv_adapter_test.go:2430-2449` |

**实现满足需求，但"而非并发"与"超时按张数放大"这两条只有一半被断言**（详见 P4、P5）：`test:2407` 只断言了"脚本被调 1 次"，脚本内的串行证据（`hyperv_adapter.go:2664` 的 `foreach ($item in $items)`）没有任何测试读过；而 `fixture.timeouts` 这个采集槽位**全文只写不读**，所以批量路径实际传入的超时值从未被验证。

### 需求 5：台账 `data/hyperv/adapters.json`，删除后重写保持与实况一致

**满足**

| 证据 | 位置 |
| --- | --- |
| 台账落盘走 `atomicWriteFile(..., 0o600)`（临时文件 + rename） | `hyperv_adapter.go:361-374` `hypervLedger.save` |
| 读-改-写整体包在 `ledgerMu` 内，并发下不会写出半截 JSON | `hyperv_adapter.go:1065-1082` `readLedger` / `updateLedger` |
| 删除成功后按**逐张成败**摘台账（失败那张保留） | `hyperv_adapter.go:2228-2249`；测试 `hyperv_adapter_test.go:2486-2489` |
| 摘出口池同理：失败那张的 id 与权重必须保留 | 测试 `hyperv_adapter_test.go:2490-2502` |
| 删除后 `invalidateInventory()`，下一次 `List()` 强制重读实况 | `hyperv_adapter.go:2250` |
| 台账外的卡**不动台账** | `hyperv_adapter.go:2237` |

**需要说明的取舍**：`removeAdapters` 内**不**在删除后重读实况，只清缓存。实况读取发生在删除前一次（`:2177`）。这满足"台账与实况一致"，但语义是"信任脚本回报 + 让下一次 `List()` 复核"，不是"当场复核"。依据脚本按 `DeviceId`（纳管）/ `MAC`（未纳管）定位并回报，方向是安全的。

### 需求 6：真机实测底账对照

**不适用**。本轮为纯代码审查，未接触实机，未执行任何 Hyper-V 变更。真机结论仍以 `reports/vnic/30-real-machine-verdict.md` 为准。

---

## 阻塞问题

按严重度排序。P1 是唯一需要在合入前决策的项；P2–P4 不阻塞合入但需要修注释/补测试。

### P1（高）单张删除路径吞掉脚本逐条失败，会造成"永远删不掉"的卡

**位置**：`desktop/internal/services/hyperv_adapter.go:2204-2214`

```go
if requireLedger {
    // 单张路径：脚本层面的失败原样上抛，与改动前的 Remove 完全一致；脚本一旦
    // 没报错，这张卡就一定删掉了（脚本按 DeviceId 定位并回报 removed/skipped）。
    if runErr != nil {
        return nil, runErr
    }
    for index := range rows {
        if rows[index].scripted {
            rows[index].removed = true
        }
    }
} else {
    hypervReconcileRemoveRows(rows, result, runErr)
}
```

**问题**：单张分支**从不读取 `result.Failures`**。

**复现/推理依据**（三跳都可静态确认）：

1. PowerShell 的 remove 分支对每张卡 `try{ Remove-VMNetworkAdapter -VMNetworkAdapter $target } catch { $failures += ... }`，循环结束后仍然 `Publish $true 'ok' ''` —— 只要脚本整体跑完，`ok` 恒为 `true`，单张失败只体现在 `failures` 里。见 `hyperv_adapter.go:2660-2689`。
2. `runScript` 只在 `!result.OK` 或 `runErr != nil` 时返回 error；`result.OK==true` 时返回 `(result, nil)`，`Failures` 原样挂在 `result` 上。见 `hyperv_adapter.go:1216-1229`。
3. 于是 `runErr == nil`，`hyperv_adapter.go:2210-2214` 把这张卡标成 `removed=true`，随后 `:2240-2249` 删台账记录、`:2251` 把别名移出出口池。

**后果**：Hyper-V 里网卡还在，台账没了，出口池也摘了。`List()` 会把它显示成 `Managed=false` 的未纳管行；UI 的批量删除能删掉它，但**后端 `Remove()` 此后再也删不掉**（`:2157-2159` 永远返回 `not_managed`）。这与 `hyperv_adapter.go:2170-2176` 自己写下的"用户在 UI 里彻底删不掉这张卡"是同一种不可恢复状态。

**注释与实现相反**：`:2205-2206` 声称"脚本一旦没报错，这张卡就一定删掉了（脚本按 DeviceId 定位并回报 removed/skipped）"。代码既没有读 `skipped`，也没有读 `failures`。

**不是本次回归，但本次重构是它该被修的地方**：`git show HEAD:desktop/internal/services/hyperv_adapter.go` 的旧 `Remove()` 只有 `if runErr != nil` 一个判断，同样忽略逐条失败。批量路径这次做对了（`hypervReconcileRemoveRows` `:2309-2332` 正确处理了"脚本回报 OK 但带无主失败 → 谁都不算成功"，见 `hyperv_adapter_test.go:2538`），单张路径没跟上。文档 `:2102-2103` 宣称"两条路径共用判定逻辑，只在『怎么表达失败』这一处分叉"，与事实不符。

**可达性**：`Remove` 仍在 Wails 绑定里导出（`desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/hypervadapterservice.ts` 有 `export function Remove`），任何 JS 调用方都能命中。当前前端 0 调用点，所以是**潜伏缺陷**而非线上故障。

**建议修法**（二选一，推荐前者）：

- 让单张路径复用 `hypervReconcileRemoveRows(rows, result, runErr)`，然后把"失败"翻译成 error 返回（文案复用 `rows[i].reason`，错误码用 `hypervCodeRemoveFailed`）。这样两条路径真的一致，文档注释也就成立了。
- 或最小改动，在 `:2207` 后追加 `if len(result.Failures) > 0 { return nil, hypervErrorf(hypervCodeRemoveFailed, ...) }`。但这样会把"5 张里 3 张成功"的批量语义带进单张路径，只是不再丢数据。

**配套测试**（当前缺失）：`Remove()` + 脚本回报 `Failures` 一条 ⇒ 断言返回非 nil error、且台账与出口池**未**被清理。现有的 `TestRemoveRejectsForeignAdapters`、`TestRemoveKeepsLedgerWhenInventoryQueryFails` 都没覆盖这条。

### P2（中）三处注释与实现相反，把"归属闸门"描述成仍然存在

**位置**：

- `hyperv_adapter.go:1329-1330`：「台账外的卡：只读展示（Managed=false ⇒ Remove 直接拒绝）。这是绝误删用户自己网卡的最后一道闸门，也是 §3.4『未登记的一律跳过』的可视化。」
- `hyperv_adapter.go:1334-1335`：「两道闸门都不依赖这个过滤」（指 `:1650` 命名规范 + `:1665` 台账归属）
- `hyperv_adapter.go:1348-1351`：「Remove() 已经有两道独立的闸门（:1650 命名规范、:1665 台账归属）……本函数只回答『可见性』，不回答『可删除性』」

**问题**：这三条描述的是**改动前**的设计。按用户拍板的需求 1，批量路径已完全放开归属闸门，`hypervShowUnmanagedAdapter` 去掉命名过滤的正确性论证（"Remove() 还有两道闸门兜底"）**已经不成立**——`RemoveAdapters` 一道都没有，这正是需求要的效果。现在的注释是纯反向误导。

**风险**：后续维护者读到"这是绝误删用户自己网卡的最后一道闸门"，可能误以为这里漏了一道而补回去（直接违背需求 1），或反过来在别处放心地再加一道。

**建议修法**：把这三段改写为"这里只决定可见性；可删除性由前端 `isAdapterRemovable` + 后端 `hypervProtectedReason` 双端把守，归属不设闸门是产品明确决策"。并显式记一句"覆盖 §3.4『未登记的一律跳过』，依据 2025 年用户需求变更"——目前代码里看不到任何"需求变更覆盖旧契约"的说明（见 P5 末段）。

### P3（中）保护名单是两份无编译期约束的镜像，后端覆盖 4 种形态、前端只覆盖 2 种

**位置**：后端 `hyperv_adapter.go:2012-2028` vs 前端 `desktop/frontend/src/components/vnic/managementAdapters.ts:117-150`

| 形态 | 后端 | 前端 |
| --- | --- | --- |
| `Container NIC {guid}` / `container nic 2`（对象名前缀） | ✅ | ✅ |
| `vEthernet (Container NIC {guid})`（别名 + 前缀） | ✅ | ❌ |
| `vEthernet (Default Switch)`（别名精确） | ✅ | ✅ |
| `Default Switch`（裸对象名） | ✅ | ❌（只查 `interfaceName`，不查 `name`） |

**问题**：`HyperVAdapterStatus`（`hyperv_adapter.go:178-193`）没有 `removable` / `protected` / `deletable` 字段，前端只能自己复刻规则。`managementAdapters.ts:107-116` 的注释自己就写明了："This is a MIRROR, not the authority … There is no build-time link between the two sides … they must be changed in BOTH places"。也就是说这个漂移风险**被记录了，但没有被消解**。

**触发条件**：`buildUnmanagedHyperVStatus`（`hyperv_adapter.go:985-1012`）用 `hypervHostInterfaceName(adapter.Name)` 合成 `interfaceName`。若 Hyper-V 返回的 `Name` 本身已带 `vEthernet (...)` 外壳，就会套出 `vEthernet (vEthernet (...))`，前端两条规则都匹配不上 → **给受保护行画删除按钮**。当前真机形态下（`Get-VMNetworkAdapter` 返回裸对象名 `Default Switch` / `Container NIC {...}`）不会发生，所以不是现行故障。

**后果**：不删错卡（后端 `:2151` 仍会拒），但用户点下去必失败，且是在确认弹窗 + UAC 之后才失败——正是 `managementAdapters.ts:113-115` 自己担心的那个"far worse"分支。

**建议修法**（按侵入性从低到高）：

1. 最小：把前端 `isProtectedAdapterInterface` 改成"先 unwrap 再比"，并让 `isProtectedAdapter` 同时对 `name` 和 `interfaceName` 都跑一遍 `isProtectedAdapterName`。约 5 行。
2. 更好：给 `HyperVAdapterStatus` 加 `protected bool`（或 `removable bool`），由后端 `buildHyperVStatus` / `buildUnmanagedHyperVStatus` 用**同一个** `hypervProtectedReason` 填。前端只读不判。这是对冻结契约 §3.2 的**加法扩展**（不删不改已有 13 个字段），不破坏 `bindings/models.ts` 里已生成的接口，但需要重新生成 bindings。需 lead 确认是否接受这一改动范围。

### P4（中）批量路径的"按张数放大超时"与"非并发"两项承诺，测试实际没守住

**位置**：`desktop/internal/services/hyperv_adapter.go:2203`（`hypervRemoveBatchTimeout(len(items))`）、`hyperv_adapter_test.go:2049` / `2062` / `2078`（`fixture.timeouts` 采集）

**问题**：`fixture.timeouts` 在三处被 append，**全文再无一处读取**（grep `\.timeouts` 只有这 3 处赋值、0 处读取）。于是 `TestRemoveAdaptersTimeoutScalesAndCaps`（`:2430-2449`）只测了 `hypervRemoveBatchTimeout` 这个**纯函数自己**，从未验证批量路径真的把它接上了。

**复现/推理依据**：把 `hyperv_adapter.go:2203` 的 `hypervRemoveBatchTimeout(len(items))` 改成写死的 `hypervRemoveScriptTimeout`（90s），或反过来改成 `hypervRemoveBatchTimeoutMax`，**整套测试依然全绿**。同理，`hypervRemoveBatchTimeoutMax = 180s` 这个字面值也没被钉死——`test:2438-2439` 用的是常量本身（`{3, hypervRemoveBatchTimeoutMax}`），改成 1 小时照样通过；真正字面量钉住的只有 `{1:90s}` `{0:90s}` `{2:180s}`。

用户拍定批量方案的理由之一正是"每张约 2.86s 的 PowerShell 读 + N 次重写"，超时放大是这条方案的配套保护。保护本身写对了（`:2037-2046` 逻辑正确），但**没有回归防护**。

**同类悬空采集**：`fixture.elevated` 在 16 个用例里都被采集，只有 `TestRemoveAdaptersUsesOneScriptForMany`（`test:2416`）读了 `[0]`。"批量必须走提权"目前只在一条用例里被守住。

**"而非并发"零断言**：用户明确否掉了线程并发方案，理由是 `Remove()` 被 `opMu` 包着会退化成串行。但测试只断言了"只调一次脚本"，没有断言脚本内部**不含并发原语**：

- 串行证据 `hyperv_adapter.go:2664` 的 `foreach ($item in $items)` 是一条**没有任何测试读过的文本**。
- `TestHypervPowerShellScriptIsConstantAndSafe` 的禁用名单（`test:1221-1232`）缺 `Start-Job`、`Start-ThreadJob`、`ForEach-Object -Parallel`、`Start-Process`、`Invoke-Expression`。
- 其余脚本断言全是 `strings.Contains`（辅助函数 `containsHypervScript` `test:1255-1266`），把 `$targets | Remove-VMNetworkAdapter` 写成 `$targets | Remove-VMNetworkAdapter; Start-Job { ... }` **依然通过**。

**建议修法**：

1. 在 `test:1221-1232` 的禁用名单补 `Start-Job`、`Start-ThreadJob`、`-Parallel`、`Start-Process`、`Invoke-Expression`，并在 `required` 里显式要求 `foreach ($item in $items)`。
2. 在 `TestRemoveAdaptersUsesOneScriptForMany` 里加一行 `if got := fixture.timeouts[0]; got != hypervRemoveBatchTimeout(len(names)) { ... }`，把"接上了"钉死。
3. `test:2438-2439` 的 `{3, hypervRemoveBatchTimeoutMax}` 改成 `{3, 180 * time.Second}` / `{16, 180 * time.Second}` 字面量。

### P5（低）一条失效断言：把运行时文案和格式串模板比较

**位置**：`desktop/internal/services/hyperv_adapter_test.go:2354-2357`

```go
if results[0].Reason != "%s 的 MAC（%s）与台账记录（%s）不一致，已跳过删除" &&
    !strings.Contains(results[0].Reason, "不一致") {
```

**问题**：`results[0].Reason` 是 `hypervMACMismatchReason()` **代入实参后**的运行时字符串，第一个条件拿它和**未代入**的格式串模板比较，`!=` 恒为真，整个 `if` 退化成 `!strings.Contains(reason, "不一致")`。意图里的"**逐字一致**"完全没有生效。

**佐证**：实现侧专门抽了 `hypervMACMismatchReason()`（`hyperv_adapter.go:2048-2053`），注释里明确要求两条路径必须逐字一致——而这个 helper **在整个测试文件里一次都没被引用过**（grep 零命中）。`test:2354` 自己的注释"原因应与单张路径同源"因此是落空的。

**建议修法**：改为精确比对

```go
want := hypervMACMismatchReason("HypoMux-vnic-01", "AA:BB:CC:DD:EE:FF", "02:1a:2b:00:00:01")
if results[0].Reason != want { t.Fatalf(...) }
```

顺带补一条单删路径用同一 helper 的断言，把"逐字一致"这条契约真正锁住。

### P6（低）混合失败列表会丢弃"无主"失败

**位置**：`hyperv_adapter.go:2319`

```go
anonymous := len(failures) > 0 && !hypervFailuresNamed(failures)
```

**问题**：只要失败列表里**有一条**带名字，`anonymous` 就是 false，于是那条不带名字的失败被静默忽略——对应的卡会被记成 `removed=true`，进而被清台账、清出口池。而 `hypervFailureMessage`（`:1494-1516`）本身是能正确处理 `unassigned`（无主失败）的，说明判定条件与 helper 的语义不一致。

**可达性**：理论上不可达。脚本侧 `name = [string]$item.name`，而 `normalizeHypervRemoveNames`（`:2075-2082`）会丢掉空名，所以脚本回报的每条失败必然带名字。属于防御性判定写窄了。

**建议修法**：`anonymous` 改为"存在任何一条无主失败"，即遍历 `failures` 检查是否有 `strings.TrimSpace(item.Name) == ""`。

### P5（低）删除判定基于最多 5s 旧的实况缓存

**位置**：`hyperv_adapter.go:2177`（`readInventory`，`hypervInventoryTTL = 5s`）与 `:2137`（`opMu.Lock()`）之间没有版本校验，属 TOCTOU。

**缓解**：脚本在真正执行时按 `DeviceId`（纳管）/ `MAC`（未纳管）重新定位，不是按名字盲删，所以"缓存里存在但实况已消失"最多退化成一次幂等成功（`:2263-2264` 明确处理了这条），爆炸半径有限。列为观察项而非缺陷。

---

## 风险与建议

### 已确认做对的地方（供 lead 在总报告中引用）

1. **无命令注入面**。`-EncodedCommand` 传 Base64(UTF16LE) 脚本（`hyperv_adapter.go:2770-2777`）；参数 payload 以 Base64 **StdEncoding** 字母表（`A-Za-z0-9+/=`）塞进单引号常量（`:2757-2761`），结构上无法闭合引号、无法插入反引号或换行。读路径脚本 `hypervReadInventoryScript`（`:2796-2829`）**零插值**，且全用单引号（PowerShell 单引号串内不展开任何变量）。**网卡名 / MAC / GUID 从未以明文拼进任何脚本字符串**。
2. **没有 `-Command` 拼接**。提权走 `ShellExecuteExW` + `runas`（`hyperv_adapter_windows.go:175-243`），命令行为 `powershell.exe` + `-NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand <b64>`，`-EncodedCommand` 之后**没有任何尾随 token**（PowerShell 5.1 的经典坑，`hyperv_adapter_test.go:1779` 专门锁死）。`-NoProfile` 齐全。
3. **结果文件不泄漏**。`newHypervJobPath`（`:808-814`）建 `~/.hypomux/hyperv/jobs` 目录，权限 `0o700`，文件名 `job-<UnixNano>-<8位随机>.json`；`runScript` `defer os.Remove(resultPath)`（`:1187`）；`Shutdown` 调 `removeHypervJobDirectory()` 清空目录（`:2349-2365`）；台账 `0o600`。写入用 `WriteAllText` + `Move-Item -Force` 原子提交；读取有 1 MiB 上限（`hypervMaxResultBytes`）与 300 字节截断（`hypervOutputRejected` `:2862-2868`）。
4. **命令白名单**。只允许 `Import-Module Hyper-V` / `Get-VMSwitch` / `Get-VMNetworkAdapter -ManagementOs` / `Add-VMNetworkAdapter -ManagementOs` / `Remove-VMNetworkAdapter`（对象管道）。显式禁止 `Remove-VMSwitch`、`Set-VMSwitch`、`New-VMSwitch`、`Restart-NetAdapter`、`Disable-WindowsOptionalFeature`、`netcfg`。删除走对象管道而非 `-ManagementOs -Name`，正是为了避开幽灵记录。
5. **失败安全方向正确**。`readInventory` 失败 ⇒ **整批中止**，台账与出口池原样保留（`:2177-2181`），文案"已中止删除，台账与出口池保持不变"。定位卡用 `DeviceId` 优先、名字兜底（`:2260-2261`），MAC 逐字节相等（`hypervMACEqual`）。**台账宁可多，不可少**。
6. **部分成功语义**。单张失败不冒泡成整批 error，成败下沉到每张卡（`HyperVRemoveResult`），只有"连脚本都没跑起来"这类致命问题才返回 error（`hyperv_adapter.go:2334-2345`）。全失败也返回 nil error。
7. **错误码可辨识且文案面向用户**。`mapHypervRunError`（`:1244-1259`）区分 UAC 取消 / 超时 / 退出中 / 平台不支持；超时文案"Hyper-V 操作可能仍在后台进行，请刷新列表确认"。符合冻结契约 §3.7。
8. **锁序无死锁**。`opMu`（独占提权操作）→ `ledgerMu`（台账读改写）→ `mu`（inventory 缓存 / closed 标志 / 测试钩子），三把锁无嵌套环；`List()` 只取 `mu`；`settings.Get()` / `updateHomeStrategy` 不回调 Hyper-V（全仓只有 `desktop/main.go:151` 构造该服务）。`opMu` 用 `defer` 释放，批量脚本失败、超时、提前 return 全部正确解锁。
9. **跨平台通过**。`hyperv_adapter_other.go:1` 的 `//go:build !windows` 正确，降级实现（`hypervPlatformSupported()` 返回 false，四个平台函数直接返回 `errHypervUnsupported` / `nil, errHypervUnsupported`）与 `hyperv_adapter_windows.go` 的符号一一对应。实测 `GOOS=linux` / `GOOS=darwin`、`CGO_ENABLED=0` 下 `go build ./internal/services/` 均 **OK**。
10. **测试可信度高于预期**。`hyperv_adapter_test.go` 共 78 个 `func Test`，0 个 `t.Skip`。批量删除有 14 个专门用例，覆盖单次提权、逐张结果、台账摘除粒度、出口池权重粒度、超时放大封顶、保护名单不进脚本、无主失败阻断、幂等、全空输入、实况查询失败中止。断言是结构化的（envelope 条数 / Items 内容 / timeout 值 / elevated 标志 / 台账与 settings 的精确内容），**不是**弱 `strings.Contains`。

### 构建与测试实测（本次亲自执行）

| 命令 | 结果 |
| --- | --- |
| `go vet ./internal/services/` | 通过，无输出 |
| `go vet ./...`（windows） | 通过，无输出 |
| `GOOS=linux CGO_ENABLED=0 go build ./internal/services/` | OK |
| `GOOS=darwin CGO_ENABLED=0 go build ./internal/services/` | OK |
| `go test ./internal/services/ -run HyperV -count=1` | `ok 0.224s`，7 PASS / 0 SKIP |
| `HYPOMUX_DATA_DIR=<temp> go test ./internal/services/ -count=1` | `ok 70.642s`，全量通过 |

> 注：整模块跨平台 `go build ./...` 会失败，原因是 wails v3 alpha 依赖需要 cgo/webkit（`pkg/application/menu_linux.go:7:12: undefined: pointer`），**与本次改动无关**。

### 建议

1. **合入前**：修 P1（单张路径复用 `hypervReconcileRemoveRows`）并补那条缺失的测试。这是唯一一个会让系统进入不可恢复状态的缺陷。
2. **紧随其后**：改写 P2 的三段注释。同样的注释在契约变更后没人清理，下一个人会按它做出错误决定。
3. **下一轮**：按 P3 建议 1（前端加 unwrap，约 5 行）消除镜像漂移的现行隐患；P3 建议 2（后端下发 `protected` 字段）是根治，但会动冻结契约 §3.2 与生成的 bindings，**需要 lead 拍板是否纳入本轮范围**。
4. **产品侧留档**：代码里没有任何地方写明"§3.4『未登记的一律跳过』已被 2025 年用户需求变更覆盖"。建议在 `hyperv_adapter.go` 顶部注释块或 `reports/vnic/` 补一份变更说明，否则冻结契约与实现会长期处于"看起来矛盾"的状态，后续任何契约核对都会重复这个问题。
5. **CI 提示（低）**：CI 用的是 `go test ./...`（`.github/workflows/go-engine.yml:54`），全量跑，没有问题。但本次审查按任务书给的 `-run HyperV` 只命中 7 个测试——`TestRemoveAdapters*`、`TestRemove*` 等 14 个批量删除用例**名字里不含 HyperV**，会被这个过滤器全部漏掉。如果有人用 `-run HyperV` 做本地回归，等于没测批量删除。建议本地回归改用 `-run 'HyperV|RemoveAdapters'`。

---

## 覆盖缺口

### 测试覆盖缺口

1. **P1 的路径完全无覆盖**：`Remove()` 收到带 `Failures` 的结果时会发生什么，没有任何测试断言。78 个用例里有 `TestRemoveRejectsForeignAdapters`（归属闸门）和 `TestRemoveKeepsLedgerWhenInventoryQueryFails`（实况查询失败），但没有"脚本跑完并回报单张失败"。
2. **失败安全的组合路径无覆盖**：`readInventory` 失败 + 一批里混有受保护行的组合（预期：整批中止，保护行也不返回结果）未见断言。
3. **无主失败的混合形态无覆盖**：`TestRemoveAdaptersUnattributedFailureBlocksSuccess`（`:2538`）只测了"全部无主"，没测"1 有名 + 1 无名"（即 P4）。
4. **真机形态无覆盖**：全部 inventory 数据都是手工构造的 `hypervScriptAdapter` 字面量，没有用 `Get-VMNetworkAdapter -ManagementOS` 的真实输出做 fixture。因此"Name 是裸对象名、`InterfaceDescription` 形如 `Hyper-V Virtual Ethernet Adapter`"这个**识别「虚拟网卡」的关键前提**没有被测试锁死——代码依赖它（`buildUnmanagedHyperVStatus` 靠它算 `InterfaceName`），但一旦 Hyper-V 改了输出形态，测试全绿而线上错。建议至少加一条以真实输出片段为常量 fixture 的解析测试。
5. **`isHyperVAdapterName` 与 `hypervProtectedAdapter` 的交叉无覆盖**：命名规范（`HypoMux-vnic-NN`）本身就保证撞不上保护名单，但没有测试锁死"这两套谓词永不相交"。
6. **并发无覆盖**：没有 `-race` 的并发用例（两个 `RemoveAdapters` 同时打进来、或 `Create` 与 `RemoveAdapters` 交错）。锁序我静态核对过没问题，但没有实测背书。**建议在 CI 的 Go 作业上加 `-race`**（本次审查未加跑，属建议）。

### 本次审查未覆盖（明确声明）

- **未做真机验证**：没有在装有 Hyper-V 的机器上跑过 `Create` / `RemoveAdapters`。所有结论来自静态代码 + `HYPOMUX_DATA_DIR` 指向临时目录的进程内测试（测试通过 `scriptHook` 注入假执行器，**从不真的调 PowerShell**）。
- **未验证提权链路**：`hypervRunElevatedShellExecute`（`hyperv_adapter_windows.go:175-243`）的 `ShellExecuteExW` + `runas` 行为、UAC 取消分支、`SEE_MASK_NOCLOSEPROCESS` + `SW_HIDE` 的组合，仅做了代码审查。
- **未验证脚本在真机上的 PowerShell 兼容性**：`hypervPowerShellScript`（`:2660-2700` 附近）与 `hypervReadInventoryScript`（`:2796-2829`）在 PS 5.1 上的真实执行结果未复跑。
- **需求 6 的真机底账**（XuniUplink / xuni-01..05 / 5 个 MAC / 192.168.16.<masked>~<masked>）本轮仅作为"代码是否与之兼容"的对照阅读，未实机核对。顺带记录一处观察：代码对 `vEthernet (Default Switch)` 的判定走 `interfaceName` 精确等值 + 裸名等值双路，与实测底账里"它不是 ready 的 vNIC、只是 172.25.160.<masked>/20 的手工地址"这一事实**不冲突**——保护名单与 ready 状态是正交的两件事。