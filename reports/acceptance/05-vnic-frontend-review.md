# 05 · 虚拟网卡前端页面与前端契约深度审查

审查范围：`desktop/frontend/` 下虚拟网卡页与其依赖链。基线 HEAD = `fc821b16bd55630ac6cba4311179e1a1b6644ded`。
只审查，未修改任何源码，未 `git add` / `commit`。

验证手段（均已实跑）：

- `pnpm.mjs exec vitest run src/pages/VirtualAdaptersPage.test.tsx src/components/vnic/` → **3 files / 175 tests 全绿**
  （`managementAdapters.test.ts` 63、`HyperVAdapterPanel.test.tsx` 63、`VirtualAdaptersPage.test.tsx` 49，10.92s）
- `pnpm.mjs exec tsc --noEmit -p tsconfig.json` → **0 error**
- i18n 键集合脚本比对：zh / en 各 372 键，**完全一致，无 missing / extra**
- 生产代码（排除 `*.test.*`）孤儿键扫描：新增的全部 `virtual_adapters_*` 键**均被引用**，无孤儿

---

## 验收结论

**有条件通过。**

4 条用户拍板的需求在代码层全部落地，前后端契约签名一致、类型检查干净、175 个测试全绿，没有发现「加了字段没接线」「加了按钮没 handler」这类半成品。但存在 **2 个应在合并前处理的阻塞项**（单卡删除超时早于后端导致误报失败；选择/剪枝用 `name` 当身份导致重名网卡连坐）和 **2 个测试可信度问题**（部分失败用例造的失败行不在列表里、断言指向了已废弃的 mock）。

---

## 需求逐条核对

### 需求 ① 每张能检测到的虚拟网卡都要有移除按钮 —— **满足**

- 删除资格只看保护名单，不再看 `managed`：
  `desktop/frontend/src/components/vnic/managementAdapters.ts:150` `isAdapterRemovable = (adapter) => !isProtectedAdapter(adapter)`。
- 行内按钮：`desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:571-581` `{removable && !selectionMode && (<Button onClick={() => setRemoveTarget(adapter)}>{t("virtual_adapters_remove")}</Button>)}`。
- 唯一被挡掉的是需求 ③ 的两类保护网卡，且不是静默消失——同页渲染 `virtual_adapters_reserved` 徽章 + `virtual_adapters_reserved_hint` title：`HyperVAdapterPanel.tsx:552-556`。
- 台账外（用户自建）网卡同样有按钮，并有独立文案与身份确认：面板单删对话框按 `removeForeign` 分叉（`HyperVAdapterPanel.tsx:198-201`），测试 `HyperVAdapterPanel.test.tsx:194-201`、`VirtualAdaptersPage.test.tsx:711-740`。
- 浏览器预览态下按钮存在但 disabled：`VirtualAdaptersPage.test.tsx:1007-1009`。

### 需求 ② 刷新按钮旁批量多选删除 —— **满足**

- 工具栏：`HyperVAdapterPanel.tsx:272-352`。Refresh 旁在非批量态渲染「批量删除」（`:331-340`），进入批量态换成 全选 / 删除所选（N） / 取消。
- 多选状态机（`desktop/frontend/src/pages/VirtualAdaptersPage.tsx`）：`startSelection:539`、`exitSelection:545-548`、`toggleSelection:550-557`、`selectAllRemovable:562-574`。
- 行即热区（不是只有 16px 复选框）：`HyperVAdapterPanel.tsx:490,502`，且复选框点击 `stopPropagation`（`:511`）避免双触发——测试 `HyperVAdapterPanel.test.tsx:267-283` 两条都覆盖。
- 选择态跨轮询存活：`tickedNames` 为 name 数组，`ticked = useMemo`（`VirtualAdaptersPage.tsx:187-190`）每次刷新按「仍 removable」二次过滤。
- 行为测试扎实：`HyperVAdapterPanel.test.tsx:228-398` 13 条（模式切换、计数同步、空选不提交、取消保留勾选、全选只勾 removable 且全选后禁用、无可删时禁用、保护行点击无效、非批量态行不可点、忽略已不可删的勾选）。

### 需求 ③ 硬排除只有两类，前端不给删除入口 —— **满足**

- 两条规则都在前端镜像：`managementAdapters.ts` `PROTECTED_ADAPTER_NAME_PREFIX = "container nic"`（前缀，trim+lowercase）、`PROTECTED_ADAPTER_INTERFACE = "vEthernet (Default Switch)"`（宿主别名全等），`isProtectedAdapter = name 规则 || interface 规则`。
- 前端不给入口的三处：单删按钮（`HyperVAdapterPanel.tsx:571` 的 `removable &&`）、批量复选框（`:519` protected 行不渲染 Checkbox）、整行点击热区（`rowSelectable` 含 `removable`）。
- `toggleSelection` 本身也二次设防：`VirtualAdaptersPage.tsx:550-557` 受 `isAdapterRemovable` 门控；`remove`/`removeSelected` 提交前再过一道（`:470-473`、`:588` 依赖 `ticked` 的剪枝结果）。即前端的保护名单是三道闸门，不依赖后端拒绝。
- 与后端一致：Go `desktop/internal/services/hyperv_adapter.go:2012-2028` `hypervProtectedReason` 判定同一组名称，且在读台账/读 inventory **之前**就判定（`:2151`），全被保护时直接返回（`:2166-2168`）。两侧规则同构。
- 测试：`HyperVAdapterPanel.test.tsx:184-192`（两种保护形态各一）、`managementAdapters.test.ts:500-547`（大小写、相似名不算保护、混合列表过滤）、`VirtualAdaptersPage.test.tsx:742-754`。

### 需求 ④ 合成一次提权脚本删 N 张，前端一次性把 N 传给后端 —— **满足**

- 前端单次调用、传数组：`VirtualAdaptersPage.tsx:588-657` `removeSelected` → `appServices.virtualAdapters.removeAdapters(names)`，`names` 来自整个 `ticked`，**没有任何 for 循环逐张调用**。
- 服务层直通绑定：`desktop/frontend/src/platform/services.ts:377-378` `removeAdapters: (names: string[]) => HyperVAdapterService.RemoveAdapters(names)`。
- 生成绑定：`desktop/frontend/bindings/.../services/hypervadapterservice.ts:68` `RemoveAdapters(names: string[] | null)`。
- 后端确实合成一次提权：`hyperv_adapter.go:2194-2203` 把所有 ready 行打进**同一个** `hypervEnvelope{Op:"remove", Items: items}`，只调一次 `runScript(..., true)`（true = 提权）= 一次 UAC；逐张成败在 `hypervReconcileRemoveRows`（`:2309`）里定，单张失败只写 `Result.Reason`，不冒泡 error。
- 测试钉死了这一点：`VirtualAdaptersPage.test.tsx:885-886` `expect(mocks.removeAdapters).toHaveBeenCalledTimes(1)` + `toHaveBeenCalledWith(["HypoMux vNIC 1","xuni-01"])`；`:854` 同上。这正是需求 ④ 的回归锁。
- 确认弹窗列出全部选中项：`HyperVAdapterPanel.tsx:709-757`，其中 `selectedForeign > 0` 时追加「其中 N 张不是本工具创建的」警告（`:729-731`）。取消只关对话框、**保留勾选**（`:740`）。

---

## 前后端契约对照表

前端入口统一在 `desktop/frontend/src/platform/services.ts:369-379`。

| # | 前端方法（services.ts） | 前端签名 | 后端导出 | 后端签名 | 一致性 |
|---|---|---|---|---|---|
| 1 | `virtualAdapters.list` `:370` | `() => Promise<HyperVAdapterStatus[]>` | `HyperVAdapterService.List` | `() ([]HyperVAdapterStatus, error)` | ✅ |
| 2 | `virtualAdapters.switches` `:371` | `() => Promise<HyperVSwitch[]>` | `HyperVAdapterService.Switches` | `() ([]HyperVSwitch, error)` | ✅ |
| 3 | `virtualAdapters.create` `:372` | `(switchName: string, count: number)` | `HyperVAdapterService.Create` | `(switchName string, count int)` | ✅ 参数个数与类型一致 |
| 4 | `virtualAdapters.remove` `:373` | `(name: string) => Promise<void>` | `HyperVAdapterService.Remove` `hyperv_adapter.go:2104` | `(name string) error` | ⚠️ **前端已无任何调用方**（`virtualAdapters\.remove` 全仓 0 命中）。Go 侧仍保留旧的 `isHyperVAdapterName` 命名门（`:2109-2111`）。导出面保留但已成死代码，行为与新批量入口不同——建议确认是否要下线 |
| 5 | `virtualAdapters.removeAdapters` `:377` | `(names: string[]) => Promise<HyperVRemoveResult[] \| null>` | `HyperVAdapterService.RemoveAdapters` `hyperv_adapter.go:2093` | `(names []string) ([]HyperVRemoveResult, error)` | ✅ 方法名、参数、N 一次性传入全部一致 |
| 6 | 返回结构 `HyperVRemoveResult` | `services.ts` 本地 `export type { name; removed; reason; interface }` | `hyperv_adapter.go:209-214` + `models.ts` `interface HyperVRemoveResult` | 四字段同名同 json tag | ✅ 字段逐一对应。注意：前端**自己声明**了一份结构类型，没从生成的 `$models` 导入 —— 两侧不会编译期联动，后端加字段时前端不会报错（见风险 R4） |

**批量是否真的一次传 N**：✅ 前后端两侧都确认。前端 `:377` 传整个数组；Go `:2202-2203` 一次 `runScript`。不是循环单删。

**前端是否调用了后端不存在的方法 / 漏传参数**：未发现。`tsc --noEmit` 通过，且本地 bindings 已重新生成并包含 `RemoveAdapters`（`desktop/frontend/bindings/` 被 `desktop/.gitignore:9` 忽略，不入 git，属构建期产物）。

**时间预算契约（前端 vs 后端）**：⚠️ **不一致**，见阻塞项 P0-1。

| 场景 | 前端超时 | 后端实际预算 |
|---|---|---|
| 读（list/switches） | `HYPERV_ADAPTER_READ_TIMEOUT_MS` 10s | — |
| 创建 N 张 | `HYPERV_ADAPTER_CREATE_TIMEOUT_MS` 180s | `hypervCreateScriptTimeout` 150s（`hyperv_adapter.go:41`）✅ 富余 |
| **单张删除** | `HYPERV_ADAPTER_WRITE_TIMEOUT_MS` **60s**（`VirtualAdaptersPage.tsx:485-489`） | `hypervRemoveBatchTimeout(1)` = **90s**（`:42` + `:2037-2046` + `:2203`）❌ 前端更短 |
| 批量删除 N 张 | `HYPERV_ADAPTER_BATCH_TIMEOUT_MS` 180s | `min(90s×N, 180s)`（`:2037-2046`）✅ |

**其他契约面**：

- `hide_virtual_adapters` 默认值翻转（`useEngineState.ts:175,295` 由 `true`→`false`，`SettingsPage.tsx:56` 同步）已与 Go `desktop/internal/services/settings.go:500-506` 对齐（注释明写「旧配置文件拿当前默认值，显式持久化的值永远优先」）。设置行从 SettingsPage 移到 HomePage 开关（`HomePage.tsx:136-153,199-209`），`ADAPTER_VISIBILITY_EVENT` 连同监听/派发两侧一并删除（`adapterVisibility.ts:3`、`SettingsPage.tsx:323-328`、`useEngineState.ts:352-360`），**无残留悬空引用**，`tsc` 通过。
- `update_channel` 前端类型与设置行已删净（`settings_fields_test.go:28-29` 断言 Go 侧也拒绝该字段），`productInfo` 删掉 `website/repository/releases/build` 后无残留引用。

---

## 阻塞问题

按严重度排序。

### P0-1 单卡删除的前端超时（60s）短于后端脚本预算（90s）→ 误报「删除失败」，而卡其实正在被删

- 前端：`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:485-489`
  `withServiceTimeout(appServices.virtualAdapters.removeAdapters([adapter.name]), HYPERV_ADAPTER_WRITE_TIMEOUT_MS, ...)`，而 `HYPERV_ADAPTER_WRITE_TIMEOUT_MS = 60_000`（`desktop/frontend/src/platform/services.ts:275`）。
- 后端：同一路径的预算是 `hypervRemoveBatchTimeout(1) = hypervRemoveScriptTimeout = 90 * time.Second`（`desktop/internal/services/hyperv_adapter.go:42`、`:2037-2046`、`:2202-2203`）。
- 放大器：`withServiceTimeout` 用 `Promise.race`，注释明写「只 reject awaiter，底层 Wails 调用继续跑」（`platform/services.ts:268`）。所以 60s 到点后 UI 弹错误、行仍在列表上、`busy` 释放，用户点「重试」→ **第二次 UAC**，而后端第一遍还在删。
- 改动前这条路径就已经是 `removeAdapters([name])`，但改动前只有本工具创建的卡走得到；本次让**每一张**可删行（含用户自建卡、含 MAC 不一致等慢路径）都走这条 60s 通道，问题从边角变成常态。
- **建议修法（纯前端）**：单卡删除改用 `HYPERV_ADAPTER_BATCH_TIMEOUT_MS`，或新增 `HYPERV_ADAPTER_REMOVE_TIMEOUT_MS = 90_000` 并在 `platform/services.ts:279-286` 的注释区补一句「批量预算不得低于 Go 的 `hypervRemoveBatchTimeout(1)`」。同时在超时文案里说明「操作可能仍在进行，请先刷新确认」。
- **需后端确认**：Go 是否可能把单张脚本预算下调到 60s 以下？若会，前端应改为「前端预算 = 后端预算 + 余量」而不是各自拍常数。

### P0-2 选择态与本地剪枝都用 `name` 当身份 → 同名网卡被连坐

- 选择：`desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:176` `const selected = useMemo(() => new Set(selectedNames), [selectedNames])`，`:183` `adapters.filter((a) => isAdapterRemovable(a) && selected.has(a.name))`，`:521` `checked={selected.has(adapter.name)}`；页面侧 `tickedNames` 也是 `string[]`（`VirtualAdaptersPage.tsx:550-574`）。
- 剪枝：`desktop/frontend/src/components/vnic/managementAdapters.ts:166-174` `dropAdapters` 最后一行 `previous.filter((row) => !gone.has(row.name))`。
- 仓库里已经有稳定身份函数 `adapterRowKey`（`managementAdapters.ts:227-228`，优先 `adapterId`，再 `interfaceName`，再 `name`），但**只用在 React `key`**（`HyperVAdapterPanel.tsx:495`）上，身份语义分裂成了两套。
- 触发条件：同一台宿主出现两张同名虚拟网卡（Hyper-V 允许，命名冲突时会出现）。此时：
  1. 勾任意一张 → 两张一起亮，且**无法单独取消**（点哪张都是同一个 name）；
  2. 删一张成功 → `dropAdapters` 把**两行**一起从本地列表抹掉，直到 3s 轮询补回，期间列表与宿主不一致；
  3. 确认对话框 `selectedRows.map` 会把同一个名字列两遍（`HyperVAdapterPanel.tsx:720`）。
- **建议修法（纯前端能做完 1、2、3 的一半）**：`tickedNames`、`selectedNames`、`dropAdapters` 全部改用 `adapterRowKey(adapter)` 作为身份。
- **需后端确认（关键）**：`HyperVRemoveResult`（`hyperv_adapter.go:209-214`、产出点 `:2334-2347`）目前**只有 `name`**，没有 `adapterId` / `MAC`。同名卡即使前端改了身份，回填「哪一张真的删掉了」仍然只能按名字猜。建议后端在 `HyperVRemoveResult` 补一个稳定标识（`adapterId` 或 `mac`），否则前端只能做「一次性剪掉所有同名行」这种乐观假设。

### P1-3 「部分失败」测试造的失败行根本不在列表里，核心场景实际未被验证

- `desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx:917-952`：mock 返回 `[removed("HypoMux vNIC 1"), removed("xuni-01"), failed("xuni-02", "适配器正被虚拟交换机占用")]`，但 `rows`（`:770`）只有 `HypoMux vNIC 1` / `xuni-01` / `Container NIC 9d844023` —— **`xuni-02` 从来没渲染过，也从没被勾选过**。
- 断言 `:949-951` 检查的是「两张已删的消失 + 从未参与批量的受保护行还在」，而 `:947-948` 的注释却声称验证了「失败的那张仍留在屏幕上」。**注释与断言不符**。
- 也就是说：「用户勾了 3 张、第 3 张失败 → 它必须留在列表里、可再次勾选重试」这条最关键的 UX 契约，**零覆盖**。
- **建议修法**：把 `xuni-02` 加进 `rows` 并在 `enterAndTick([0,1,2])` 中勾选它，mock 返回它 failed，然后断言 `expect(screen.getByText("xuni-02")).toBeTruthy()`。

### P1-4 写序列化测试断言指向了已废弃的 mock，恒真

- `VirtualAdaptersPage.test.tsx:1108` 与 `:1120`：`expect(mocks.remove).not.toHaveBeenCalled()`。页面早已不走 `remove()`（全仓 0 调用方，见契约表第 4 行），所以这两条断言**无论门闩是否生效都会通过**。
- 真正承重的是 `:1105` 的 `expect(remove.hasAttribute("disabled")).toBe(true)`，那条是有效的。但两条并排会让人误以为「后端没被调到」也被测了。
- **建议修法**：改成 `expect(mocks.removeAdapters).not.toHaveBeenCalled()`。

---

## 风险与建议

| 级别 | 位置 | 问题 | 建议 |
|---|---|---|---|
| 中 | `VirtualAdaptersPage.tsx:504-512` + `src/components/notifications/notificationMessage.ts:47-62` | 单删失败用 `intent:"error"`，message 是 `{name}：{reason}`。`conciseDiagnosticMessage` 先做关键词替换：reason 里只要含「网络 / 联网 / resolve / network」就整条换成「网络暂时不可用，请检查连接后重试。」——而后端对容器网卡的保护原因本身就含「容器网络」。即便不含关键词，首行 >56 字或含 2 个以上冒号也会被整条替换成「操作未完成，请重试；如仍失败，请导出支持日志。」 | 失败原因改走行内错误徽标（每行一条短句），toast 只报「N 张未删除，详见列表」；或保证每行 ≤56 字再交给 error intent |
| 中 | `VirtualAdaptersPage.tsx:623-634` + `notificationMessage.ts:73-77` | 批量部分失败用 `intent:"warning"`，把 N 行 `"{name}：{reason}"` 用 `\n` join；`normalizeMessage` 把换行折成空格，再 `slice(0,69)+"…"`。N>2 时**默认可见文本通常只含第 1 条失败**。好消息是 `AppNotifications.tsx` 有 Details 展开，`detail` 保留原文 | 同上；或改成「列表内逐行红字 + toast 只报计数」，别把可读信息塞进 72 字摘要 |
| 中 | `src/i18n/legacy.messages.json` zh `virtual_adapters_remove_selected_body` | 正文是「以下网卡会在一次提权操作中一并删除，只弹一次权限提示。删除后无法撤销。」——**既没写「将删除 N 张」，也没有任何断网风险提示**。N 只体现在按钮 `{count}` 和列表里。对照：创建侧有 `virtual_adapters_create_banner`「正在创建 {count} 张虚拟网卡，请勿关闭窗口或断网。」，删除侧没有对等物 | 补一句「将删除 {count} 张虚拟网卡；若其中有正在承载流量的网卡，网络会短暂中断」。单删文案 `virtual_adapters_remove_body` 同步补断网提示 |
| 低 | `HyperVAdapterPanel.tsx:435` | dismiss restart hint 按钮的 `aria-label` 复用了 `routing_dialog_cancel`（读作「取消」），语义不对，屏幕阅读器会念错 | 补一个 `virtual_adapters_dismiss_hint` 之类的专键 |
| 低 | `HyperVAdapterPanel.tsx:495` | `key={adapterRowKey(adapter) \|\| adapter.name}`：三个身份字段皆空时退化为空串，多行共享同一 key | `\|\| index` 或保证后端 `adapterId` 非空 |
| 低 | `HyperVAdapterPanel.tsx:217,630-632` vs `VirtualAdaptersPage.tsx:379-468` | 配额上限在一处用 `quota.perSubmitMax`、另一处用 `quota.remaining`。当前两者恒等（over-quota 只在 `remaining < value ≤ 16` 时触发，此时 `perSubmitMax = remaining`），所以不是 bug，但是两处独立计算、靠巧合相等 | 统一由 `createQuota` 暴露一个 `blockedMax` 字段，消除巧合依赖 |
| 低 | `platform/services.ts` `export type HyperVRemoveResult` | 前端自己手写了一份后端结构类型，没从生成的 `$models.HyperVRemoveResult` 导入。后端加/改字段时前端**编译期不报错**，只在运行期出现 `undefined` | 改为 `import type { HyperVRemoveResult } from "../../bindings/.../models"`，或加一条与 Go 结构体对齐的契约测试 |
| 低 | `services.ts:373` | `virtualAdapters.remove` 已无调用方，但 Go 侧 `Remove` 仍保留旧的 HypoMux 命名门（`hyperv_adapter.go:2109-2111`）。两条删除入口行为不同 | 确认是否要下线该导出，避免日后有人误用旧语义 |
| 低 | `HyperVAdapterPanel.tsx` / `managementAdapters.ts` 顶部常量 | `VIRTUAL_ADAPTER_MAX_TOTAL=32`、`VIRTUAL_ADAPTER_MAX_BATCH=16` 是 Go 常量的**手工镜像**（`hyperv_adapter.go:204-205`），无编译期链接（代码注释里已自陈） | 保持注释即可；若将来 Go 调参，建议加一条断言镜像值的测试 |

**渲染正确性（已核对，无问题）**：

- 无 setState-in-render；所有副作用在 `useCallback` / `useEffect` 内。
- 轮询与定时器无泄漏：轮询 effect（`VirtualAdaptersPage.tsx:311-328`）cleanup 清 `mounted` / 请求序号 / `refreshInFlight` 门闩 / `stop()`；DHCP 提示的 60s 计时器挂在 `[waitingForDhcp]` 布尔量上（`:365-374`），轮询刷新不会重置它——`VirtualAdaptersPage.test.tsx:978-990` 专门用 `tick(30_000)` 两次验证了这点。
- 依赖数组没有 stale closure：会变的值一律走 `tRef` / `switchesRef` / `quotaRef` / `quotaNoticeRef`，`refresh`/`create`/`remove` 的 deps 分别是 `[notifyOnce, preview]` / `[preview]` / `[preview]`，闭包里不捕获易变 state。
- `memo` 依赖正确：`ticked`（`:187-190`）、面板 `selected` / `selectedRows` / `selectedForeign`（`HyperVAdapterPanel.tsx:176-189`）deps 齐。
- `removeSelected` 依赖整个 `ticked`（每次轮询会换新数组），导致回调每 3s 重建一次——只被 `onClick` 使用，无正确性影响，仅多余的重建。

**i18n**：

- zh / en 键集合完全一致（372 = 372，无 missing / extra），新增 30+ 个 `virtual_adapters_*` 键中英成对。
- 生产代码孤儿键扫描：新增键**无一孤儿**；扫描出的孤儿全部是改动前就存在、且多数通过动态插值（如 `col_${x}`）引用的旧键，与本次改动无关。
- 被删的 About / 更新相关键（`settings_about`、`about_*`、`nav_about`、`settings_sponsorship_*`）与页面一起删除，`CompactNavigation.test.tsx:20` 还留了一条 `expect(queryByRole("button", {name:"nav_about"})).toBeNull()` 的回归锁——**无残留孤儿键、无残留导入**（`tsc` 通过 + 全仓 grep 只命中无关注释词）。

**03 号分工范围内顺带记一笔（未深入，仅前端可确认的部分）**：About 页与自动更新在前端侧的清理是干净的——`App.tsx` 移除 lazy import、路由分支、`pageOrder` 项；`errorCodes.ts:26-27` 移除 `HM-E1601/E1602`；`SettingsPage.tsx` 移除更新渠道行；`desktop.ts` 移除 `openURL` 与 `Browser` 导入；`product.ts` 移除 website/repository/releases/build。`grep -i "about|updater|checkForUpdate|sponsor|donate|收款"` 全仓只剩注释与测试名，无代码级残留。

---

## 覆盖缺口

1. **「勾选的卡失败了，它必须留在列表里可重试」——零覆盖。** 见 P1-3：`VirtualAdaptersPage.test.tsx:917-952` 的 `xuni-02` 不在 `rows` 里，实际断言的是另外两张。建议补一条：`rows` 加 `xuni-02` → 勾 3 张 → mock 让它 failed → 断言 `getByText("xuni-02")` 仍在。
2. **同名网卡的整条链路——零覆盖。** 没有任何一个用例构造两张 `name` 相同、`adapterId` 不同的行。P0-2 的连坐、确认对话框重名、剪枝误删都测不出来。建议在 `HyperVAdapterPanel.test.tsx` multi-select 里加一条。
3. **超时行为——零覆盖。** 测试把 `withServiceTimeout` mock 成恒等透传（`VirtualAdaptersPage.test.tsx:34`），所以 P0-1 这类「前端预算 < 后端预算」的问题在单测里永远测不出来。建议至少加一条断言「单卡删除走的是哪个超时常量」的结构性测试（例如 spy `withServiceTimeout` 并核对第三个实参）。
4. **`partitionRemoveResults` 的 `removed:true` + `reason` 非空（矛盾行）——有覆盖但只在单元层。** `managementAdapters.test.ts:604-613` 覆盖了拆分规则，但没有页面级用例验证矛盾行时 UI 的呈现（`dropAdapters` 会同时剪掉并报错）。
5. **多选态下的 3s 轮询竞态——只覆盖了「不再是 removable 的勾选被忽略」（`HyperVAdapterPanel.test.tsx:383`），没覆盖「轮询把某张已删卡带回来后，已提交批次的 tickedNames 是否被正确清理」。**
6. **`HyperVRemoveResult` 与 Go 结构体的契约测试——不存在。** 前端手写了一份类型（见风险表最后第 3 行），没有防漂移的护栏。
7. **单删 / 批量的错误文案在真实 `prepareNotificationMessage` 折叠后的可读性——无覆盖。** `notificationMessage.test.ts` 只测了折叠函数本身，没有测「后端 reason 原文经折叠后用户看到什么」。