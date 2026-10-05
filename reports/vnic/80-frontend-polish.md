# 80 — 虚拟网卡（Hyper-V）页前端收口

负责人：vnic-frontend-polish · 工作区：`<repo>`
只改白名单内 7 个文件里的 6 个（`reports/vnic/80-frontend-polish.md` 为交付物）：
`desktop/frontend/src/pages/VirtualAdaptersPage.tsx`、其 `.test.tsx`、
`desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx`、其 `.test.tsx`、
`desktop/frontend/src/components/vnic/managementAdapters.ts`、其 `.test.ts`、
`desktop/frontend/src/i18n/legacy.messages.json`。
**没有碰 Go、没有碰 `.css`、没有 `git add/commit/stash/checkout`、没有新增依赖、没有删除任何现有测试。**

---

## 0. 门禁（真实退出码）

| 阶段 | 命令（workdir = `desktop/frontend`） | 退出码 |
|---|---|---|
| 起始基线 | `& ".\node_modules\.bin\vitest.cmd" run` | **0** — `Test Files 45 passed (45) / Tests 330 passed (330)` |
| 收口后 | `& ".\node_modules\.bin\tsc.cmd" --noEmit` | **0** |
| 收口后 | `& ".\node_modules\.bin\vitest.cmd" run` | **0** — `Test Files 45 passed (45) / Tests 373 passed (373)` |
| 收口后 | `& ".\node_modules\.bin\vite.cmd" build` | **0** — `✓ built in 4.55s` |
| 探针回滚后重跑 | `tsc --noEmit` / `vitest run` / `vite build` | **0 / 0 / 0** |
| 语言包 | `node -e "JSON.parse(...legacy.messages.json…)"` | **0** — `json ok` |

**测试总数：330 → 373，净增 43 条，0 条被删、0 条被放宽。文件数仍是 45。**
本次触及的三个套件：`VirtualAdaptersPage.test.tsx` 32 → 39、`HyperVAdapterPanel.test.tsx` 25 → 46、
`managementAdapters.test.ts` 36 → 52。

---

## 1. `notified` 闩收窄到读路径（create/remove 每次都弹）

**做法**：闩本身保留，但作用域显式缩小到两个被 3 秒轮询重复触发的读路径；五条 action 通知全部改用普通 `notify()`。

- `VirtualAdaptersPage.tsx:198-210` — `notifyOnce` 上方补注释，说明它**只为轮询读保留**。
- 只剩两个调用点走 `notifyOnce`：
  - `:262` `virtual-adapters:error:switches`
  - `:280` `virtual-adapters:error:list`
- 改用 `notify()`（每次都发）的五个 action 点：
  - `:387` `virtual-adapters:info:create`（创建开始）
  - `:417` `virtual-adapters:info:create-done`（创建完成）
  - `:468` `virtual-adapters:info:remove`（删除成功）
  - `:439` `virtual-adapters:error:create`（**创建失败**，原为 `notifyOnce`，这是本次最致命的那一处）
  - `:479` `virtual-adapters:error:remove`（**删除失败**，同上）
- 读路径不刷屏靠**两件事**，不是一件：`notifyOnce` 的 `Set`（每次挂载只发一次）+ `notify` 自身的 `dedupeKey`（`AppNotifications.tsx:92` 用 `input.dedupeKey` 当 id）。action 路径之所以敢每次都发，是因为 `AppNotifications.tsx:116-120` 对同一 id 是**替换 + 计数**（封顶 ×5），不是抑制也不是堆叠——不会刷屏。
- `notified` 仍然只增不清，因为读路径的语义就是「一次挂载只播报一次故障」；清空它反而会让同一个持续故障在每次重新进入页面时重复播报。

**新增测试** `VirtualAdaptersPage.test.tsx:735-770`：
- `notifies every failed create instead of latching the first one` —— 连点两次创建，断言 `virtual-adapters:error:create` 恰好 2 次。
- `notifies every failed remove instead of latching the first one` —— 同上，`virtual-adapters:error:remove` 恰好 2 次。
- `:773-788` `titles each failure for the operation that actually failed` —— 读失败连播两次仍只发一次（闩还在），且标题是新的读失败 key。

---

## 2. 操作标题拆成三个精确 key

`virtual_adapters_state_failed` **保留不动**（它同时是行状态标签「创建失败」，走 `stateLabelKey`）。新增三个操作标题：

| 调用点 | 新 key | zh | en |
|---|---|---|---|
| `VirtualAdaptersPage.tsx:281`（列表读取失败） | `virtual_adapters_read_failed` | 读取虚拟网卡列表失败：列表是旧的，不代表网卡状态已改变。请点「重试」再读一次，暂时不要重复创建或删除。 | Reading the virtual NIC list failed: what you see is stale and does not mean the adapters changed. Press Retry to read it again, and do not create or remove anything in the meantime. |
| `:436`（创建失败） | `virtual_adapters_create_failed` | 创建虚拟网卡失败：宿主机上没有新增网卡。请查看下面的错误原因，修正后重试；已经建成功的网卡不受影响。 | Creating virtual NICs failed: no adapter was added to the host. Read the reason below, fix it and retry — the adapters that did get created are unaffected. |
| `:476`（删除失败） | `virtual_adapters_remove_failed` | 删除虚拟网卡失败：网卡仍留在宿主机上。请查看下面的错误原因后重试，或重启 HypoMux 后再删一次，以免留下孤儿网卡。 | Removing a virtual NIC failed: it is still on the host. Read the reason below and retry, or restart HypoMux and remove it again, so you do not leave an orphaned adapter behind. |

每条都点名了**是谁失败**（读列表 / 创建 / 删除）+ **下一步**（重试前不要写、修正后重试、重启后再删避免孤儿）。

---

## 3. 交换机不可用拆成四个分支

新增纯函数 `managementAdapters.ts:237-246` `switchAvailability()`，返回
`"none" | "external-only" | "management-os-off" | "none-usable" | "ready"`；
面板 `HyperVAdapterPanel.tsx:202` 取值、`:270-280` 渲染。

| 情形 | key |
|---|---|
| 交换机列表为空 | `virtual_adapters_no_switch`（原有，未改） |
| 有交换机但没有外部的 | `virtual_adapters_switch_external_only`（原有，未改） |
| **有外部交换机，但全都没开 AllowManagementOS** | **新增** `virtual_adapters_switch_management_os_off` |
| 混合 / 防御分支 | **新增** `virtual_adapters_switch_none_usable` |

**zh**（`management-os-off`，占位符 `{count}`）：
> 宿主机上有 {count} 个外部交换机，但都没勾选「允许管理操作系统」（AllowManagementOS）。请打开 Hyper-V 管理器 → 左侧「虚拟交换机」→ 选中这些交换机 → 属性 → 管理操作系统 勾选「允许」→ 确定，然后回来重试。

**en**：
> This host has {count} external switch(es), but none of them has "Allow management operating system" (AllowManagementOS) ticked. In Hyper-V Manager, go to Virtual Switches, select these switches, open Properties, tick Allow under "Management operating system", confirm, then come back and retry.

**zh**（`none-usable`）：
> 本机的外部交换机都没有勾选「允许管理操作系统」（AllowManagementOS），且无法自动判定原因。请打开 Hyper-V 管理器 → 「虚拟交换机」→ 属性 → 管理操作系统 勾选「允许」，或新建一个外部交换机。

**en**：
> None of the external switches on this host has "Allow management operating system" (AllowManagementOS) ticked, and the exact reason cannot be determined automatically. In Hyper-V Manager, open Virtual Switches → Properties → Management operating system and tick Allow, or create a new external switch.

措辞**搬运自**既有 `virtual_adapters_switch_hint`（「必须挂在允许管理操作系统（AllowManagementOS）的外部交换机上」）的句式，没有新造第二套语气。
诚实备注：只要 `isSwitchSelectable` 仍是 `allowManagementOs !== false`（`managementAdapters.ts:206`），
`none-usable` 这一支**当前不可达**——它是为「将来把判定改严」预留的兜底，保证那种改动只降级为解释、不会静默漏掉原因。已在函数注释里写明，测试只覆盖可达的四个返回值。

---

## 4. 两个 Retry 按钮加闸门

- `HyperVAdapterPanel.tsx:256-266`（switches 失败横幅）与 `:284-294`（列表读取失败横幅）：
  `disabled={refreshing}` + `icon={refreshing ? <Spinner size="tiny" /> : undefined}` +
  文案 `{refreshing ? t("virtual_adapters_retrying") : t("virtual_adapters_retry")}`。
  标签切换本身就是防抖：一次点击后按钮变灰并换文案，连点无从发生。
- `VirtualAdaptersPage.tsx:219-289` `refresh(options: { manual?: boolean } = {})` 增加 in-flight 闸门
  （`refreshInFlight` ref，`:184`）：
  - `:220-221` 闸门判断**在 `++requestSequence.current` 之前**返回，所以被拒绝的重叠调用**不会顶掉正在跑的那次读**。
  - `:288` / `:307` 在 `finally` 与轮询 effect 的 cleanup 里复位，因此「页面在后台、读请求卡住」也不会把页面永久锁成只读——**不会变成硬死锁**。
  - `sequence !== requestSequence.current` 的 last-writer-wins 陈旧丢弃逻辑一行未动。
- `setRefreshing` **只**由 `manual` 读驱动（`:222-225`）。3 秒轮询不碰它，所以按钮不会每 3 秒闪一次。
- 工具栏「刷新」按钮 `:217-222` 照既有样板加了同款 spinner 图标，标签保持不变（既有断言不受影响）。
- 取舍：一次读在飞行中时，`remove()` 尾部那次 `await refresh()` 会被跳过；下一次 3 秒轮询会补上，用户可见的差异 ≤ 3 秒。这是刻意的——宁可慢一拍，也不要两次读互相作废。

---

## 5. 重启提示改为由真实数据推导

- `managementAdapters.ts:325-326` `needsRestartPrompt(adapter)` = `state === "ready" && isInOutboundPool(adapter)`（直接读原始 `state`，不经过 `deriveAdapterState`，理由写在注释里）。
- `managementAdapters.ts:333` `pendingRestartNames(adapters, createdNames)` —— 只取「本次会话创建的名字」∩「当前仍声称在池里的行」。
- 页面侧：`VirtualAdaptersPage.tsx:172-176` 用 `createdNames` state + `useMemo` 派生 `pendingRestart`；
  `:403-409` 在 `create()` resolve 后把本批网卡名并进 `createdNames`（`[]` 或失败批次不加名）；
  `:526-528` 把 `pendingRestart` 传给面板，`onDismissRestartHint` 清空 `createdNames`。
- 面板侧 prop 由 `restartRequired: boolean` 改成 `pendingRestart: string[]`（`HyperVAdapterPanel.tsx:87`）；
  横幅 `:309-313` 只在非空时渲染，列出具体网卡名，关闭按钮一次清空。**横幅没有被删掉。**
- 每 3 秒轮询重算 `pendingRestart`，所以 `inPool` 翻回 false 的卡会自动退出横幅，收敛不需要额外逻辑。

**语义张力（如实记录）**：`InPool === true` 的含义是「这张卡**已经**在出口池里」（`hyperv_adapter.go:166`），读起来和「需要重启」相反。本实现按任务书给定的判定式实现（`ready && inPool === true`，且 `inPool` 翻 false 时退出），
一致的理解是：后台 goroutine 还没写池时，行数据**可能抢跑**地声称了成员关系。**这一条没有在真机 Hyper-V 上验证过**（见第 8 节）。

---

## 6. 后端标记本地化

**纯函数层**（`managementAdapters.ts`，无 React、无 i18n runtime 依赖）：
- `:267-270` 常量 `HYPOMUX_HINT_PREFIX = "hypomux.hint."`、`HYPOMUX_HINT_POOL_RESTART`、
  `HYPOMUX_HINT_POOL_UPDATE_FAILED`、`HYPOMUX_HINT_SEPARATOR = " | "`。
- `:272-282` `type BackendHint = { code: string; detail: string }`。
- `:284-298` `splitBackendHint(lastError)`：`trim` 后 `""` → `{code:"",detail:""}`；不以 `hypomux.hint.` 开头 → `{code:"",detail:原文}`；
  有前缀无分隔符 → `{code:前缀串,detail:""}`；有分隔符 → 按**第一个** `" | "` 切分（`indexOf`），两侧再 trim。
- `:300-308` `BACKEND_HINT_I18N_KEYS` + `backendHintI18nKey(code)`（`hasOwnProperty` 查表，未知 code 返回 `undefined`，绝不猜翻译；`"toString"` 之类的原型键也不会命中）。

**渲染层** `HyperVAdapterPanel.tsx:353-…`：每行算 `hint = splitBackendHint(lastError)`、`hintKey = backendHintI18nKey(hint.code)`；
已知 code → 语言包文案作主句、`hint.detail` 作次要信息；未知 code / 自由文本 → **整串原样**。
并且**在非 `failed` 行也渲染**（`showLastError = Boolean(lastError) && (state === "failed" || Boolean(hintKey))`）——
因为 `pool_update_failed` 标记是刻意保留在 `ready` 行上的，只在 `failed` 里显示等于永远看不见。

新增两个 key：

**`virtual_adapters_hint_pool_restart`**
- zh：这张卡真正写进出口池要等聚合引擎重启之后。重启聚合引擎，再回来确认这张卡是否显示「已加入出口池」。
- en：This adapter only really joins the outbound pool after you restart the aggregation engine. Restart it, then come back and check whether the pool badge is there.

**`virtual_adapters_hint_pool_update_failed`**
- zh：网卡本身已经创建成功，但没有写进出口池；重启聚合引擎也不会生效。请删除这张卡后重新创建，或检查 settings.json 里的出口池配置。
- en：The adapter itself was created, but it did not make it into the outbound pool, and restarting the aggregation engine will not change that. Delete this adapter and create it again, or check the outbound pool configuration in settings.json.

（en 已验证不含任何 CJK 字符。）

---

## 7. 三处内联文案进语言包

删掉本地 `text = useCallback((zh,en)=>…)` 辅助函数（`VirtualAdaptersPage.tsx:146` 附近），
三处配额文案改成正式 key，占位符名与调用点逐一核对：

| key | 占位符 | 调用点 |
|---|---|---|
| `virtual_adapters_quota_line` | `{used} {total} {remaining}` | `:341` |
| `virtual_adapters_quota_exceeded` | `{used} {total} {remaining} {max}` | `:346` |
| `virtual_adapters_quota_exhausted` | `{used} {total}` | `:352` |

zh/en 原文：
- `quota_line` zh「已创建 {used} / {total} 张虚拟网卡，本轮最多还能创建 {remaining} 张。」/ en「{used} of {total} virtual adapters created — at most {remaining} more in this batch.」
- `quota_exceeded` zh「已创建 {used} / {total} 张虚拟网卡，本轮最多还能创建 {remaining} 张。请把创建数量改成 {max} 以内再提交。」/ en「{used} of {total} virtual adapters created, so only {remaining} more fit in this batch. Enter {max} or fewer and submit again.」
- `quota_exhausted` zh「已创建 {used} / {total} 张虚拟网卡，已达上限，本轮无法再创建。请先删除不再使用的虚拟网卡。」/ en「All {total} virtual adapter slots are taken ({used} created), so nothing more can be created right now. Delete the adapters you no longer use first.」

中文三条与旧的内联文案**逐字一致**，所以原有中文断言一字未改仍然通过；en 侧 `quota_line` 也与旧文案逐字一致。
（`quota_exceeded` / `quota_exhausted` 的英文做了轻微重写，因为它们现在还要在对话框里当 hint 出现。）

---

## 8. 测试补强（三处缺口）

### (1) `busy === true` 锁死 5 个入口（原本整个套件里 `busy` 从未为 `true`）
`HyperVAdapterPanel.test.tsx:349-407`：
- `locks the toolbar entries and refuses their clicks` —— 工具栏「刷新」`disabled` 且点击不回调 `onRefresh`；工具栏「批量创建」`disabled` 且点击**开不出对话框**、不回调 `onCreate`。
- `locks the create dialog that is already open when busy flips` —— 用 `rerender` 在对话框已打开时把 `busy` 翻成 `true`，断言**取消**与**创建**两键都 `disabled`，点击均无回调。
- `locks the remove dialog that is already open when busy flips` —— 同法，删除确认键 `disabled`，点击不回调 `onRemove`。
（`loading === true` 的覆盖是既有的，未重复造。）

### (2) 工具栏「刷新」点击路径（原本零覆盖）
`HyperVAdapterPanel.test.tsx:400-407` `wires the toolbar refresh through to the page's read` —— 点一次，`onRefresh` 恰好 1 次。

### (3) 建卡 / 删除的并发串行化（原本零覆盖）
页面级 `VirtualAdaptersPage.test.tsx:794-…`：
- `refuses a remove while an elevated create is still running` —— create 的 promise 悬着，行内删除键 `disabled`，点击后 `mocks.remove` **从未被调用**、对话框没开；工具栏「批量创建」同样 `disabled`，`mocks.create` 仍只有 1 次调用；等 create 结束后依然没被绕过。
- `refuses a create while a remove is still running` —— 反向对称。
两条都断言**后发起的那条流程在 `busy` 期间打不到服务上**，而不只是「按钮看起来是灰的」。

### 附带新增
- `managementAdapters.test.ts` +16 条：`splitBackendHint` 六种形态（空、空白、自由文本、伪前缀、裸标记、含两个 ` | ` 只切第一个、两端 trim）、`backendHintI18nKey` 的两张映射与未知/原型键、`needsRestartPrompt` 四态、`pendingRestartNames` 四种收敛、`switchAvailability` 四个返回值。
- `HyperVAdapterPanel.test.tsx` 另加：重试闸门 3 条、创建预算 4 条、交换机可用性 3 条、后端标记 5 条。
- `VirtualAdaptersPage.test.tsx` 另加：重启横幅的**反向**两条（`inPool:false` 不弹、失败创建不弹）。

### 既有测试的改动（如实列出）
1. `VirtualAdaptersPage.test.tsx:41-62` i18n mock 从纯 key 回显改为：**只对 `virtual_adapters_quota_` 前缀解析真实语言包**（异步工厂 + `await import("../i18n/legacy.messages.json")` + 手工 `{name}` 插值），其余 key 仍逐字节回显。因为第 7 项把配额文案搬进了语言包，不这么做就没法再钉住中文/英文原文。
2. `prompts for an aggregation restart after a successful create` 的夹具加了 `inPool: true`——横幅现在由 `ready && inPool` 推导，**断言本身一字未改**。
3. `refuses an over-quota submit before it can reach the service` / `refuses every submit once the budget is exhausted` / `frees the budget again as soon as the poll shows a deletion` 三条**按遗留 77 §7 的新行为重写**（见下）。这是审计要求导致的**行为增强**，不是放宽：旧断言里的「弹一次 `virtual-adapters:error:quota` 吐司」现在**根本不可能发生**——对话框在提交口就挡住了，页面侧的闸门成了纯纵深防御。
   - 现在断言的是：提交键 `disabled` + 对话框内给出真实缺口文案 + `mocks.create` 从未被调用 + 连吐司都不需要（理由就在用户刚输入的字段下面）。
   - 额度为 0 时断言的是**入口本身**关闭（工具栏批量创建 `disabled`、点击开不出对话框、`mocks.create` 未被调用、页面横幅给出删除指引）。
4. 新增辅助 `openCreateForm()`：被对话框自身拒绝的提交不会再关闭对话框，Fluent 会暴露两个 `role="dialog"`（模态框 + 内层面板）；取文档顺序最后一个即可拿到表单所在的那个。

---

## 9. 遗留 `reports/vnic/77-create-count-guard.md` §7

- 面板新增可选 prop `quota?: CreateQuota`（`HyperVAdapterPanel.tsx:94`），页面在 `:527` 传入。
- `HyperVAdapterPanel.tsx`：`validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH, quota)`。
  **两参数调用行为一字未变**——`quota === undefined` 时第三个实参就是 `undefined`，`validateCreateCount` 的原签名
  `quota?: CreateQuota` 走的是同一条分支。`HyperVAdapterPanel.test.tsx` 里既有的两参数用例（`keeps the dialog's own 1..16 rules when no quota is passed at all`）仍然通过，另外 `managementAdapters.test.ts` 里钉死两参数语义的那条也还在。
- 额度为 0：`createDisabled`（`= busy || preview || !selectable.length || !count.ok`）已覆盖工具栏「批量创建」入口 → 置灰；对话框根本开不出来。
- 额度不足但对话框可开：提交键 `disabled`，字段下方给一句提示，复用
  `virtual_adapters_quota_exhausted` / `virtual_adapters_quota_exceeded`（`:165-168`，与页面横幅**同一份文案**，不造第二套）。
- Go 口径对齐：`上限 32 = hypervMaxTotalAdapters`、`len(台账行数) + 本次 count <= 32`、`managed=false` 的只读行**不占额度**
  —— 页面传给面板的 `quota` 来自既有的 `createQuota(adapters)`，其 `countQuotaAdapters` 只数 `isManagedAdapter` 的行，
  这三条既有测试（`states the remaining budget…` / `does not charge read-only foreign rows against the budget` / `still creates when the typed count fits the remaining budget`）继续原样通过。

---

## 10. 回归探针（做了、改了什么、谁变红）

三处**同时**改坏（全程用 `edit`，未用 shell 改写源文件），跑 `vitest run src/components/vnic src/pages/VirtualAdaptersPage.test.tsx`：

| # | 注入的缺陷 | 位置 | 转红的用例 |
|---|---|---|---|
| 1 | 创建失败的通知改回 `notifyOnce`（把第 1 项改坏） | `VirtualAdaptersPage.tsx:435` `notify({` → `notifyOnce("virtual-adapters:error:create", {` | `VirtualAdaptersPage create-failure notifications > notifies every failed create instead of latching the first one` |
| 2 | `needsRestartPrompt` 去掉 `inPool` 判定（第 5 项改坏） | `managementAdapters.ts:326` `state === "ready" && isInOutboundPool(adapter)` → `state === "ready"` | `pendingRestartNames > only prompts for a ready adapter that claims pool membership`、`… > converges on its own once a claim disappears from the next poll`、`VirtualAdaptersPage create flow > does not prompt when the created adapters never claim pool membership` |
| 3 | 对话框额度的第三参传 `undefined`（77 §7 改坏） | `HyperVAdapterPanel.tsx` `validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH, quota)` → `…, undefined)` | `HyperVAdapterPanel create budget > shuts the toolbar entry once the ledger is full`、`… > explains the real gap inside the dialog and refuses the batch there`、`VirtualAdaptersPage create quota > refuses an over-quota submit before it can reach the service`、`… > refuses every submit once the budget is exhausted`、`… > frees the budget again as soon as the poll shows a deletion` |

结果：`Tests 9 failed | 128 passed (137)`，**`PROBE_EXIT=1`**。三处缺陷都精确命中了为它们新写的用例，没有出现「坏了却全绿」。
三处**全部回滚**后重跑全量：`tsc --noEmit` = 0、`vitest run` = `45 files / 373 tests passed`、退出码 **0**；`vite build` = **0**；语言包 `json ok`。

---

## 11. 我没能验证的部分（老实说）

1. **没有跑过桌面真机 / 真 Hyper-V。** 全部结论来自单测（jsdom + 打桩 service）。真实 Hyper-V 管理器、真实的 UAC 提权、真实的 DHCP 与出口池写入路径**一次都没执行过**。
2. **没有真点过按钮。** 五个禁用入口、两个 Retry 的闸门、对话框里的额度提示，都只在 testing-library 的合成事件下验证过 `disabled` 属性与回调未发生；没有真实鼠标/键盘操作，也没有真实焦点环与屏幕阅读器播报顺序。
3. **没有读屏器验证。** 新增的 `role="alert"`、`aria-live`、`role="status"` 沿用了页面既有写法，但 NVDA / Narrator / VoiceOver 的实际播报**没有测过**。重启横幅从「一句固定提示」变成了「插网卡名」，读屏下断句是否还顺畅，未知。
4. **第 5 项的判定语义（`ready && inPool === true` → 提示重启）与 `InPool` 的字面含义相反**（`InPool=true` 表示「已在池中」）。这是按任务书给的式子实现的，并且 `inPool` 翻 false 会退出横幅的行为在单测里钉住了；但**它与真机后台 goroutine 的真实时序是否自洽，我无法验证**——需要在真机跑一次「创建 → 不重启聚合 → 观察池徽章与横幅」才能确认。
5. **`.virtual-adapter-detail`（后端细节的次要文字）和新的 quota 提示行没有任何样式。** CSS 文件不在白名单里，所以它们现在继承既有 `.virtual-adapter-error` 的外观；视觉层级是否符合设计稿，没有真人看过。
6. **没有验证 en 语言包在真机上的完整观感。** 我只做了机器校验：12 条新 key 的 en 值**不含任何 CJK 字符**，且全语言包 `{name}` 占位符与调用点一一对齐（脚本核对 0 处不匹配）。
7. **没有验证与 Go 侧的端到端联调。** 第 6 项完全按 `hyperv_adapter.go:121-124` 的既有常量实现；如果 Go 侧之后新增第三个标记，前端会**原样显示该标记串**（不翻译、不吞掉），这属于设计内的降级，但没有真实数据跑过。
8. **没有跑过 `desktop/` 的 Wails 真机构建**，只跑了前端 `vite build`（rolldown，`✓ built in 4.55s`）。
9. **第 3 项的 `none-usable` 分支当前不可达**（详见第 3 节），只测了它的返回值，没有真实数据能触发它。