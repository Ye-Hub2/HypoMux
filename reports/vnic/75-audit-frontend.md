# 75 · 虚拟网卡页前端验收审计（只读）

审计对象：刚完成的「独立虚拟网卡页」前端实现
冻结契约：`reports/vnic/70-frozen-hyperv-interface.md` §4（与 63 冲突时以 70 为准）
参考设计：`reports/vnic/63-frontend-hyperv-page.md`
实现方报告：`reports/vnic/73-frontend-wiring.md`、`reports/vnic/74-frontend-page.md`

审计员：`vnic-page-audit`（只读，未修改任何源文件，未 git add/commit）
审计日期：2026-01-04

---

## 0. 总评

**不建议直接放行。** 三项门禁全部真实通过，契约 §4 的骨架接线、i18n、状态派生、测试质量都很扎实，**用户的两条硬验收（主页彻底移除虚拟网卡面板、创建时手动填数量）100% 达成**。

但交叉核对 Go 侧与前端渲染逻辑后发现 **4 个必须修的问题**，其中 2 个是 Go↔TS 耦合面被前端单方面违反、1 个会让整页永久卡成只读、1 个会让用户在「交换机查不到」时看到一个没有任何解释的灰按钮。其中 #2（重启提示被吞）意味着**契约 §3.6.4 里最重要的用户动作提示当前根本到不了用户眼前**。

| 严重度 | 数量 | 编号 |
| --- | --- | --- |
| ❌ 必须修 | 4 | M1 M2 M3 M4 |
| ⚠️ 建议改 | 9 | S1–S9 |
| 未验证 | 5 | U1–U5 |

---

## 1. 结论表

### 1.1 用户验收硬指标

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 1 | 主页彻底移除旧虚拟网卡面板 | ✅ | `pages/HomePage.tsx` diff 只删了三处：import（原 `:26`）、`notifySuccess` 调用、`<VirtualAdapterPanel …/>` JSX；无残留 |
| 2 | `notifyError` 未被误删 | ✅ | 仍在 `:82`、`:91` 两处使用，`useCallback`/`notify`/`locale` 全部仍被引用，`noUnusedLocals` 通过 |
| 3 | `HomePage` 回归测试 | ✅ | `pages/HomePage.test.tsx:334-345` 新增 `describe("HomePage virtual NIC cleanup")`，断言页面上不存在「Virtual adapter」文本与「Create virtual adapter」/「Remove virtual adapter」按钮 |
| 4 | 新页是顶级导航项、可点进去 | ✅ | `components/shell/CompactNavigation.tsx:16`（`AppPage` 联合类型加 `"virtual-adapters"`）、`:37`（导航项）；`App.tsx:169-170` renderPage 分支 |
| 5 | 导航文案走 `t()` 非硬编码 | ✅ | `CompactNavigation.tsx:37` `label: t("nav_virtual_adapters")`；zh/en 双写齐备 |
| 6 | **创建时手动填数量** | ✅ | `components/vnic/HyperVAdapterPanel.tsx:108` `countDraft` 初值 `"1"`（**不是**按剩余额度推算）；`:117-121` `openCreate` 每次重置为 `"1"`；`managementAdapters.ts:82-85` 注释明确禁止「fill the gap」；`create_banner{count}` 用的是用户输入值 |

### 1.2 契约 §4 逐条

| # | 冻结条款 | 结论 | 证据 |
| --- | --- | --- | --- |
| 7 | 页面标识 `virtual-adapters` | ✅ | `App.tsx:72,92,169`；`CompactNavigation.tsx:16,37` |
| 8 | 导航位置在 `connections` 之后 | ✅ | `CompactNavigation.tsx:37` 插在 `connections` 与 `tools` 之间 |
| 9 | `App.tsx` 四处接线齐全 | ✅ | `:34` lazy import、`:72` DEV 深链白名单、`:92` pageOrder、`:169-170` renderPage |
| 10 | **`pageOrder` 与导航顺序一致** | ✅ | pageOrder = home→routing→health→connections→**virtual-adapters**→tools→settings→blocked-domains→about→appearance；`CompactNavigation` 顺序与之相符。前进/后退方向不会错 |
| 11 | `services.ts` 分组签名逐字一致 | ✅ | `platform/services.ts:367-372` `virtualAdapters: { list(), create(switchName,count), remove(name) }` + `switches()` |
| 12 | 新增三文件 / 删两文件 / 保留 `vnic.css` | ✅ | `HyperVAdapterPanel.tsx`、`managementAdapters.ts`、`VirtualAdaptersPage.tsx` 新增；`VirtualAdapterPanel.tsx`+测试已 `D`；`vnic.css` 保留并被新面板 `:51` import |
| 13 | 不碰 `app.css` | ✅ | `git diff --stat` 无该文件（5944 行原样） |
| 14 | i18n 走既有 `t()`、zh+en、无硬编码 CJK | ✅ | 见 1.7 |

### 1.3 Go ↔ TS 字段耦合（契约 §3.2）

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 15 | `HyperVAdapterStatus` 14 个 JSON tag 逐字一致 | ✅ | Go `desktop/internal/services/hyperv_adapter.go:120-134` vs 占位绑定 `desktop/frontend/bindings/.../services/models.ts:522-537`，字段名/大小写完全相同 |
| 16 | `HyperVSwitch` 5 个字段一致 | ✅ | Go `:139-144` vs `models.ts:543-549` |
| 17 | 五个方法签名一致 | ✅ | Go `List():1034`、`Switches():1074`、`Create(switchName,count):1093`、`Remove(name):1370`、`Shutdown():1430` vs `hypervadapterservice.ts:18-36` |
| 18 | **`Create()` 返回值的语义被前端误解** | ❌ | Go `:1233-1243` 只返回**本批**行；前端 `pages/VirtualAdaptersPage.tsx:259-262` 把它当成全量 `setAdapters(next)` → **见 M1** |
| 19 | **Go 借 `LastError` 下发的「重启聚合」提示被前端丢弃** | ❌ | Go `:1236-1240` 只在 `row.State != failed` 时写 `LastError`；前端 `components/vnic/HyperVAdapterPanel.tsx:262` 只在 `state === "failed"` 时渲染 `lastError` → 两个条件正好互补，提示**永远不显示** → **见 M2** |

### 1.4 状态派生（契约 §3 / 63 §3）

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 20 | `creating` + 无 address → 等待 DHCP | ✅ | `managementAdapters.ts:35` `address ? "creating" : "dhcp"` |
| 21 | `creating` + 有 address → 正在创建 | ✅ | 同上 |
| 22 | `ready` + 有 address → 已就绪，展示 address/prefix/mac | ✅ | `:37`；面板 `:244-247` 渲染 `formatMac` + `formatAddressPrefix` |
| 23 | `failed` → 失败 + lastError | ✅ | `:39`；面板 `:262-264` `role="alert"` |
| 24 | `absent` / 未知值 → 安全回退「状态未知」，不崩不白屏 | ✅ | `:40-42` default 汇入 `unknown`；`stateLabelKey`/`stateColor` 是 `Record<DerivedAdapterState,…>` 全覆盖（面板 `:74-88`），未来新状态由 TS 强制补全 |
| 25 | `inPool===true` 才显示「已加入出口池」 | ✅ | `managementAdapters.ts:53` `=== true`；面板 `:253-255` 才渲染徽章 |
| 26 | `managed===false` 标注「非本工具创建」且**无删除按钮** | ✅ | 面板 `:256-258` 徽章 + `:266` 删除按钮包在 `managed && (...)` 内；页面层 `:281` 还有一道 `isManagedAdapter` 守卫 |

### 1.5 创建 / 删除对话框

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 27 | 交换机下拉来自 `switches()` | ✅ | `VirtualAdaptersPage.tsx:172-176`；面板 `:112` `externalSwitches(switches)` |
| 28 | 仅 `type==="External"` 可选 | ✅ | `managementAdapters.ts:101-102` lowercase 比较；面板 `:299-308` 只渲染 external |
| 29 | `allowManagementOs===false` 标为不可用 | ✅ | `:105`；面板 `:304` `<Option disabled>`（保留可见而非消失） |
| 30 | 数量 1..16 / 默认 1 / 手动填写 | ✅ | 面板 `:313-315` `type=number min=1 max=16 value="1"`；`validateCreateCount` `:86-98` |
| 31 | `switches()` 为空 ⇒ 警告条 + 禁用创建 | ✅ | 面板 `:151,187-193`；`createDisabled = busy||preview||!selectable.length||!count.ok` `:124` |
| 32 | 删除确认文案说明 IP 配额回收 | ✅ | `virtual_adapters_remove_body` = 「该卡将从宿主机移除，路由器分配给它的 IP 配额会被回收。」面板 `:343` |

### 1.6 轮询与生命周期

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 33 | 门控用 `usePageActive()` | ✅ | `VirtualAdaptersPage.tsx:117,210`。`components/shell/AppShell.tsx:50-53` 确实用 `hidden` + `PageActivity.Provider value={page === item}` 让访问过的页面**永久挂载**，所以门控是必需的，实现正确 |
| 34 | 间隔 3s | ✅ | `managementAdapters.ts:17` `VIRTUAL_ADAPTER_POLL_INTERVAL_MS = 3000` |
| 35 | create 发出后继续轮询到无 `creating` 行 | ✅ | `:196` `if (!next.some(isCreatingRow)) setCreating(false)`；进度 banner 面板 `:202-207` 带 `role="status" aria-live="polite"` |
| 36 | 定时器卸载时清理 | ⚠️ | 轮询本身干净（`platform/serialPoll.ts:33-36` `stop()` 置 `stopped` 并 clearTimeout，in-flight 的 `task()` settle 后 `schedule()` 见 `:19` 直接 return）。但 **`busy` 状态会泄漏 → 见 M3** |
| 37 | 轮询回调无 stale closure | ✅ | `refresh` 依赖 `[notifyOnce, preview]`，`notifyOnce` 依赖 `[notify]`；`notify` 是 `useCallback([locale])`（`components/notifications/AppNotifications.tsx:92`）且 context value 被 `useMemo` 缓存（`:147`）→ 引用稳定，不会导致轮询 effect 反复重启。`t()` 另走 `tRef`（`VirtualAdaptersPage.tsx:141-142`）以免切语言重启轮询 |

### 1.7 i18n

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 38 | zh / en 各 31 个 key，双向 0 缺失 | ✅ | node 脚本核验：`zh count 31 / en count 31`，`missing in en: []`、`missing in zh: []` |
| 39 | 占位符逐 key 一致 | ✅ | `count_hint{max}`、`total_hint{total}{used}`、`summary{ready}{total}`、`create_banner{count}`、`unavailable{reason}` 五组 zh/en 完全对齐 |
| 40 | en 段无中文混入 | ✅ | 对全部 31 个 key 跑 CJK 正则，en 段 0 命中 |
| 41 | 新增源文件无硬编码 CJK | ✅ | 对 `VirtualAdaptersPage.tsx/.css`、`HyperVAdapterPanel.tsx`、`managementAdapters.ts` 全量 grep `[\u4e00-\u9fff]` = 0 命中；CJK 只出现在 `.test.tsx` 的 fixture 里 |
| 42 | 无硬编码英文散文 | ⚠️ | 预览 fixture 全 ASCII（`LAN-External` / `Ethernet` / `CreateVmNetworkAdapter: Access denied (0x80070005)`），对齐 `connectionPreview.ts` 先例。仅 `VirtualAdaptersPage.tsx:314` `section-kicker` 硬编码 `"Hyper-V"`、面板 `:224` 表头硬编码 `"MAC"` —— 两者都是专有名词/协议缩写，非散文，可接受 |

### 1.8 预览模式

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 43 | 渲染 fixture | ✅ | `VirtualAdaptersPage.tsx:121` `preview = import.meta.env.DEV && !isDesktopRuntime()`；`:36-102` 三条卡 + 一个 Internal 交换机 |
| 44 | 禁用一切写操作 | ✅ | 面板 `:124` createDisabled 含 `preview`、`:271`/`:317`/`:351` 行级禁用；页面层 `:239`、`:281` 两道 `if (preview) return` 早退 |
| 45 | 显示预览提示条 | ✅ | 面板 `:177-181` |
| 46 | `isDesktopRuntime` 取自既有导出 | ✅ | `VirtualAdaptersPage.tsx:17` `from "../platform/runtime"`，无新造检测逻辑 |
| 47 | 预览模式完全不碰桌面服务 | ✅ | `:213-215` `preview ? null : startSerialPoll(...)`；`:158-163` refresh 首行短路。测试 `pages/VirtualAdaptersPage.test.tsx:314-315` 断言 `list`/`switches` **一次都没被调用** |

### 1.9 测试质量

见第 3 节门禁实测。**新增 52 个用例绝大多数在断言真实行为，不是「渲染了某个 key」的弱断言。** 覆盖到的关键分支包括：`managed===false` 无删除按钮（`HyperVAdapterPanel.test.tsx:128`、`VirtualAdaptersPage.test.tsx:283`）、`inPool` 仅 true 才显示（`:118-123`）、`allowManagementOs===false` 不可选（`managementAdapters.test.ts:166-169`）、0/17 被拒 16 通过（`HyperVAdapterPanel.test.tsx:214-233`）、未知 state 兜底（`managementAdapters.test.ts:69-75`）、轮询门控 + 卸载停表（`VirtualAdaptersPage.test.tsx:117-141`）、60s DHCP 提示不被 3s 轮询重置（`:288-300`）。

**未覆盖的关键分支（见 S5/S6/M1/M3/M4）**：

- 没有任何用例断言「创建成功后原有卡片仍在列表里」→ M1 逃逸。
- 没有任何用例在写操作进行中翻转 `pageActive` → M3 逃逸。
- `switchesFailed` 用例（`:180-187`）只断言**没有**警告，**没有断言有任何替代解释** → M4 被测试固化了。
- `withServiceTimeout` 在页面测试里被 mock 成透传（`:28`），所以**超时分支完全没跑过**。

### 1.10 越界检查

| # | 检查项 | 结论 | 证据 |
| --- | --- | --- | --- |
| 48 | 未碰 `desktop/frontend/src/app.css` | ✅ | `git diff --stat` 13 文件中无此项 |
| 49 | 未改 `engine/**` | ✅ | 无 |
| 50 | 未改 `protocol/**` | ✅ | 无 |
| 51 | 未改 `.github/**` | ✅ | 无 |
| 52 | 未动 `bindings/`（gitignored 生成物） | ✅ | `git status --porcelain` 无该路径。注：`bindings/.../hypervadapterservice.ts` 是**手写占位**（`$Call.ByID(1001..1005)`），注释已写明 CI 会用 `wails3 generate bindings -clean=true` 重生成。该目录下还有一个已废弃的 `virtualadapterservice.ts` 占位残留，CI `-clean` 会清掉，无害 |
| 53 | 全仓无 `VirtualAdapterPanel` / `virtualAdapterService` 代码引用 | ✅ | 全仓 grep 只剩两条**注释**：`components/vnic/vnic.css:2` 和 `pages/HomePage.test.tsx:336`（均已核实只是文字提及，非引用） |

### 1.11 可用性隐患

| # | 场景 | 结论 | 说明 |
| --- | --- | --- | --- |
| 54 | 首屏 loading 失败 | ⚠️ | 页面存活、给重试按钮 ✅；但红条文案用的是**行状态词**「状态未知」而不是「加载失败」（`HyperVAdapterPanel.tsx:197`）→ **S3** |
| 55 | 无 Hyper-V 环境 | ❌ | `Switches()` 失败时 `switchesFailed=true` → 面板**主动抑制**所有交换机提示（`:151`）。若 `List()` 恰好成功，用户看到的是「空列表 + 一个没有任何解释的灰色批量创建按钮」→ **M4** |
| 56 | 数量填 0 / 17 / 负数 / 非数字 | ⚠️ | 安全（提交按钮禁用、`create()` 在 `VirtualAdaptersPage.tsx:242` 二次校验）✅；但**没有可见的失败原因**，只有 `aria-invalid`（`HyperVAdapterPanel.tsx:319`），`validateCreateCount` 算出的 `too-small/too-large/empty/not-integer` 从未渲染 → **S5** |
| 57 | 批量 16 张是否被告知要等一分钟以上 | ⚠️ | `virtual_adapters_count_hint` 只有「一次最多创建 16 张。」；唯一提到 60s 的 `virtual_adapters_dhcp_slow` 要**等到 60s 之后**才出现 → **S6** |
| 58 | 创建完成后是否被告知要重启聚合引擎 | ❌ | **完全收不到**。Go 专门为此设计的 `LastError` 通道被前端条件互补地丢弃（M2）；前端自己的 `virtual_adapters_restart_hint` 只在**创建对话框里**出现过（面板 `:323`），创建完就消失 |

---

## 2. 必须修（4 项）

### M1 · `create()` 用批次返回值覆盖整表，已存在的网卡会瞬间消失
- 位置：`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:259-262`
- 事实：Go 侧 `desktop/internal/services/hyperv_adapter.go:1233-1243` 的 `rows` 只从 `finished`（**本批**）构造，函数头注释 `:1088-1089` 也写明「返回本批的立即态」。前端却写 `setAdapters(next)`。
- 后果：用户已有 12 张卡，再创建 4 张 → 表格**只剩 4 行**，另外 8 行连同它们的「已加入出口池」徽章一起消失，最多 3 秒后被轮询恢复；顶部徽章从「已就绪 12 / 共 12」闪回「已就绪 0 / 共 4」，「已创建 12 / 32」闪回「已创建 4 / 32」。
- 建议修法（**不要**简单换成 `await refresh()`）：Go 在 `Create()` 返回的行里注入了 `row.LastError = hypervPoolRestartHint`（`hyperv_adapter.go:1238-1240`），且注释 `:1236-1237` 明确说这是「唯一能把『需要重启聚合』送到前端的通道」—— 改成 `refresh()` 会把这条提示一起抹掉（Go `:1229` 起的 `List()` 路径不会再写它）。正确做法是**把 `next` 按 `name` 合并进当前 `adapters`**（已存在则覆盖、新增则追加），再让后续轮询自然收敛。
- 配套测试：加一条「已有 1 张 ready + create 返回 1 张新行 → 两张都在表里」。

### M2 · Go 下发的「重启 HypoMux 聚合后生效」提示被前端条件互补地丢弃
- 位置：`desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:262`（`{state === "failed" && lastError && …}`）
- 事实：`desktop/internal/services/hyperv_adapter.go:1238` 是 `if row.State != hypervStateFailed { row.LastError = hypervPoolRestartHint }` —— 只在**非** failed 时写入。前端只在 **failed** 时渲染。两个条件恰好互补，**该提示在当前实现下永远不会被显示**。
- 后果：用户完成批量创建后，收不到任何「必须重启聚合引擎，出口池才生效」的提醒。这正是契约 §3.6.4 之后最关键的下一步动作。
- 建议修法：`lastError` 非空即渲染，按 `state === "failed"` 区分样式（failed 用 danger + `role="alert"`，非 failed 用 subtle 的提示样式），并补一条「ready 行带 lastError 也会显示」的用例。

### M3 · 写操作进行中切走页面 → 回来后整页永久只读
- 位置：`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:272` 与 `:304`，两处都是 `finally { if (mounted.current) setBusy(false) }`
- 机制：轮询 effect 的 cleanup（`:216-220`）在 `pageActive` 变 false 时把 `mounted.current` 置为 false。创建（最长 180s）或删除（最长 60s）期间用户点导航栏切到别的页是完全可能的 —— `submitCreate`（面板 `:126-130`）是先关对话框再调 `onCreate`，导航栏全程可点。若调用在「离开状态」期间 settle，`finally` 里的守卫会跳过 `setBusy(false)`，**`busy` 永久停在 `true`**。
- 后果：回到该页时 `refresh()` 会正常拉到数据，但所有写控件都是灰的：刷新按钮（面板 `:163` `disabled={loading || busy}`）、批量创建（`:124` `createDisabled` 含 `busy`）、每行删除（`:271` `busyRow = busy || isCreatingRow`）。**必须重启应用才能恢复**。
- 建议修法：`finally` 里无条件 `setBusy(false)`（React 18 对卸载后 setState 静默 no-op，且 AppShell 本就永不真正卸载该页）；或在 `pageActive` 变 true 的 effect 重入时 `setBusy(false)`。**注意不要顺手删掉 `setAdapters(next)` 前的 `mounted` 守卫**——那个守卫防的是过期轮询回写，是必要的。
- 配套测试：加一条「create 未 settle 时把 `pageActive` 翻成 false 再翻回 true → busy 归零、删除按钮恢复可用」。

### M4 · `switches()` 查询失败时页面没有任何解释
- 位置：`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:186`（置 `switchesFailed=true`）+ `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:151`
- 事实：`switchNotice = !loadFailed && !switchesFailed && (...)` —— `switchesFailed` **只用于抑制提示，从不产生提示**。
- 后果：`list()` 成功但 `switches()` 失败（例如交换机枚举被拒、Hyper-V 管理 cmdlet 部分不可用）时，用户看到的是：空列表 + 「还没有虚拟网卡…点击批量创建」+ 一个**没有任何说明的灰色「批量创建」按钮**。这是一个死胡同：无 Hyper-V 的机器、交换机权限不足的机器都会撞上。
- 建议修法：新增 `virtual_adapters_switch_failed`（zh/en 双写，如「无法读取 Hyper-V 交换机列表，批量创建暂不可用。请检查 Hyper-V 安装或权限后重试。」），在 `switchesFailed` 时渲染一条 warning，并让「重试」按钮复用 `onRefresh`（它已经会同时重跑 `list()` + `switches()`）。
- 配套测试：`VirtualAdaptersPage.test.tsx:180-187` 目前断言「无 `no_switch` 提示」且**没有**断言任何替代解释，这条用例需要改成同时断言新提示存在。

---

## 3. 建议改（9 项，不阻塞放行但建议同批修）

| # | 位置 | 问题 | 建议修法 |
| --- | --- | --- | --- |
| S1 | `VirtualAdaptersPage.tsx:147-154` | `notifyOnce` 对**所有** intent 上闩。通知仓库本身已按 `dedupeKey` 去重并累加 `occurrences`（`AppNotifications.tsx:95,116-119`），所以对 error 是冗余的，而对 info/success 是**有害的**：删掉第 2 张及以后的网卡**完全没有成功提示**（去删 5 张只响 1 次），创建第 2 批也没有「正在创建」提示 | 只对 `intent === "error"` 上闩；或把 key 改成带实体（`virtual-adapters:info:remove:${adapter.name}`），让仓库自己计数 |
| S2 | `VirtualAdaptersPage.tsx:246-250` | 创建的通知在 `await` **之前**就发了，所以它只是「开始中」，批次落定（`:196` 清 banner）时没有任何完成通知 | 批次 settle 时补一条 success toast；`create_banner` 保留给进行中 |
| S3 | `HyperVAdapterPanel.tsx:197` 与 `:226` | ① 红条正文用 `t("virtual_adapters_state_unknown")` =「状态未知」，这是**行状态词**被当成页面级失败标题，用户看不出到底什么失败了；② 同一 key 被复用为表格最后一列的 `aria-label`，读屏会把这列念成「状态未知」；③ `virtual_adapters_unavailable{reason}`（`:182-186`）与红条同时出现，而超时 reason 实际是 `withServiceTimeout` 拼出来的 `t("virtual_adapters_loading")` + ` (10s)`，即「虚拟网卡（Hyper-V）不可用：正在加载虚拟网卡… (10s)」 | 新增 `virtual_adapters_load_failed`（zh/en）；给最后一列补 `virtual_adapters_state` 作为 aria-label；`withServiceTimeout` 第三个参数对两个读操作改传「读取虚拟网卡列表」/「读取交换机列表」这类**动作名**而不是 UI label |
| S4 | `HyperVAdapterPanel.tsx:123,160` | 只做 1..16 的单批校验，没有 32 总量的**余量**校验。面板明明已经显示「已创建 30 / 32 张」，用户仍可提交 16 张，然后被 Go 整批拒绝（`hyperv_adapter.go:341-342`） | 把 `summary.total + count > VIRTUAL_ADAPTER_MAX_TOTAL` 折进 `createDisabled`，或在数量输入下方提示「剩余额度 N 张」。注：报告 74 §7 第 5 条只覆盖了「并发创建」，没覆盖这个余量缺口 |
| S5 | `HyperVAdapterPanel.tsx:311-321` + `managementAdapters.ts:80,90-96` | `validateCreateCount` 算出的 `empty/not-integer/too-small/too-large` **从未渲染**，只有 `aria-invalid`（视障用户专用）。填 17 的用户只看到按钮变灰，没有任何原因 | 在 `Field` 里加 `validationMessage` 或自定义 hint，把 reason 映射成 4 条 i18n 文案 |
| S6 | i18n `virtual_adapters_count_hint` | 「一次最多创建 16 张。」没有告诉用户一批可能要等一分钟以上。唯一提到 60s 的 `virtual_adapters_dhcp_slow` 要等超时才出现 | 把 count_hint 扩成「一次最多创建 {max} 张，创建并等待 IP 分配可能需要 1 分钟以上。」（70 §4 未冻结这些文案，可自由改） |
| S7 | `VirtualAdaptersPage.tsx:137-138` | `switchesRef` 只写不读，是死代码。`noUnusedLocals` 抓不到「写了但没人读」的 ref | 删掉 |
| S8 | `VirtualAdaptersPage.tsx:107-114` + `App.tsx:170` | `VirtualAdaptersPageProps.onBack` 从未被传入（`App.tsx:170` 渲染的是裸 `<VirtualAdaptersPage />`），是一个永远为 undefined 的预留 prop | 删掉，或由 shell 侧真的传（报告 74 §6 已主动交代了这个决定，二选一即可） |
| S9 | `managementAdapters.ts:63-64` | `adapterRowKey` 在 name/adapterId/interfaceName 全空时返回 `""`，面板 `:237` 的兜底也是 `adapter.name`（同样是空）→ 两行无名卡会拿到重复 key `""`，触发 React 重复 key 渲染错乱。`managementAdapters.test.ts:112` 正好断言了这个 `""` 返回值 | 用 `\`${adapter.adapterId}\|\|${adapter.interfaceName}\|\|${adapter.name}\|\|${index}\`` 之类的兜底，或在 `adapterRowKey` 末尾补序号。Go 侧永远会写 `name`，所以纯属防御性 |

### 文案偏离（非阻塞）
- en 段统一用 "Virtual NIC(s)"（`nav_virtual_adapters` = "Virtual NICs"），而 `63-frontend-hyperv-page.md` §1.1 **明确警告过** "Virtual NIC" 这个词与已下线的 Wintun 方案撞名、建议改成 "virtual adapter (Hyper-V)"。zh 侧正确用了「虚拟网卡（Hyper-V）」。70 §4 没有冻结具体措辞，故只作提示：若在意术语一致性，en 也应改为 "Virtual adapters (Hyper-V)"。另外 `nav_virtual_adapters` 的 zh 是裸的「虚拟网卡」，而页标题是「虚拟网卡（Hyper-V）」，导航与标题不完全对齐。

---

## 4. 门禁实测（本审计员独立复跑）

工作目录 `<repo>\desktop\frontend`

| 命令 | 真实退出码 | 真实输出 |
| --- | --- | --- |
| `& ".\node_modules\.bin\tsc.cmd" --noEmit` | **0** | 零错误 |
| `& ".\node_modules\.bin\vitest.cmd" run` | **0** | `Test Files 45 passed (45)` / `Tests 287 passed (287)`，Duration 18.43s |
| `& ".\node_modules\.bin\vite.cmd" build` | **0** | `✓ built in 504ms`，2497 modules |

新增用例计数与实现方报告一致：纯函数 18（`managementAdapters.test.ts`）+ 面板 19（`HyperVAdapterPanel.test.tsx`）+ 页面 15（`VirtualAdaptersPage.test.tsx`）= **52**；其余 44 文件 235 用例无回归。

构建产物 `dist/assets/VirtualAdaptersPage-BgnEYQA4.js` 11.33 kB（gzip 3.52 kB）+ `dist/assets/VirtualAdaptersPage-OO3FuE4j.css` 3.66 kB 独立成 chunk，**证明 `App.tsx:34` 的 lazy 路由接线真实生效**。

vitest stderr 中的噪声全部是既有问题，无一来自新文件：Wails 的 `⚠️ Browser Environment Detected`（7 个文件）、`useEngineState.test.tsx` 里 tabster 的 `ReferenceError: NodeFilter is not defined`、`SettingsPage.test.tsx:253` 里故意触发的 `settings save failed: Error: offline`、以及 `SettingsPage.tsx:147` 的 controlled→uncontrolled Switch 警告。

---

## 5. 实现方两份报告未提到的新问题

按「越挖越深」排序，前 4 条即上面的 M1–M4。以下为 74 号报告 §7「未验证项 / 风险」清单里没有的：

1. **M1 `setAdapters(next)` 把批次当全量** —— 74 §4 只说「create() 发出后不阻塞轮询」，完全没提返回值语义；Go 侧 `:1088` 的注释明写了「返回本批」，两边对不上。
2. **M2 重启提示被 `state === "failed"` 吞掉** —— 74 报告里连 `virtual_adapters_restart_hint` 出现在哪都没交代，更没发现它只在**创建前**的对话框里出现、创建后 Go 专门下发的提示反而渲染不出来。这是本次审计最实质的发现。
3. **M3 `busy` 泄漏导致整页永久只读** —— 74 §5 描述了 15 条页面测试，没有一条覆盖「写操作中切页」。
4. **M4 `switchesFailed` 只抑制不提示** —— 74 §4 的原文是「`switches()` 为空或没有可用 External 交换机 → 警告条」，只覆盖了**成功返回空**的情况；**查询失败**这条路被完全遗漏，而 74 的测试用例恰好把这个行为固化了下来。
5. **S1 `notifyOnce` 吞掉第 2 次起的成功提示** —— 74 §6/§7 把 `notifyOnce` 描述成「每次 mount 最多一次」并只讨论了 error 去重，没意识到它同样作用在 info/success 上；也没发现通知仓库本身已经有 `occurrences` 累加机制，这个上闩其实是多余的。
6. **S4 32 总量余量没有前端校验** —— 74 §7 第 5 条只提「并发创建靠 Go 兜底是设计如此」，但**单次就超总量**（30/32 时再填 16）这条完全没被讨论。
7. **`withServiceTimeout` 的操作名传错了** —— `VirtualAdaptersPage.tsx:170,175` 把 `t("virtual_adapters_loading")`（"正在加载虚拟网卡…"）当作超时错误信息的前缀，最终渲染成「不可用：正在加载虚拟网卡… (10s)」。74 只在 §7 第 6 条泛泛讨论了 `Promise.race` 语义，没注意到这个具体的文案错误。
8. **`role="table"` 缺 `role="rowgroup"`** —— 面板 `:221-227` 直接在 `role="table"` 下放 `role="row"`，没有 rowgroup 包裹，读屏的行数播报可能不准。小问题，但属于 a11y 规范偏差。
9. **`vnic.css:2` 的注释还在引用已删除的 `VirtualAdapterPanel.tsx`** —— 纯注释残留，无功能影响，但会误导后来者。
10. **`hyperv_pool_restart_hint` 是 Go 下发的中文字符串** —— 即便修好 M2，en locale 下这条提示也只会显示中文。这是 Go→TS 的**文案泄漏**（不像 `lastError` 那样是编码错误，更像文案所有权没划清）。修 M2 时建议同步决定：要么前端按内容识别（脆弱），要么 Go 侧改发一个稳定的错误码/标记，要么干脆由前端自己根据「本批刚创建完」来渲染重启提示（最干净）。

### 正面确认（值得写进放行意见）
- **超时预算的排序是对的**：前端 CREATE 预算 180s > Go 侧 `hypervCreateScriptTimeout = 150s`（`hyperv_adapter.go:39,1163`），且 `runScript` 用 `context.WithTimeout(base, timeout)` 兜底（`:932`）。同时 `awaitBatch` 是后台 goroutine（`:1229`），`Create` 本身会在 ~15s 就返回。所以 180s 实际上打不到，**「前端超时被当作『操作没发生』的证据」这个经典陷阱在当前预算下被正确规避了** —— 74 §7 第 6 条的担心在数值上是成立的。
- **代码里不存在任何「超时后自动重试 / 回滚 / 清空本地状态」的逻辑**，因此没有重复创建路径。
- **轮询 effect 不会因 `notify` 身份变化而反复重启**（`notify` 有 `useCallback` + `useMemo` 双保险），stale closure 风险已实证排除。

---

## 6. 未验证项

| # | 项 | 原因 |
| --- | --- | --- |
| U1 | 真机 Hyper-V 全链路（UAC 弹窗、`CreateVmNetworkAdapter` 真实返回、DHCP 45s 时序、交换机枚举） | 本次为纯只读代码审计，未接触真实网卡与系统网络 |
| U2 | Wails 真实绑定生成后是否仍编译通过 | `bindings/` 被 gitignore，当前只有手写占位（`$Call.ByID(1001..1005)`）。CI 的 `wails3 generate bindings -clean=true` 会重新生成，生成的 `models.ts` 与 Go struct tag 的一致性**没有在本轮验证过** |
| U3 | 预览模式的浏览器目视 | 只在 jsdom 断言，未跑 `npm run dev` 人工看渲染 |
| U4 | `role="table"` 在真实 NVDA/NVDA 中文下的播报 | 需要实际读屏器验证 |
| U5 | M1–M4 的修复回归 | 本审计员只读，未实施任何修复；修复后需补上述 4 条配套测试并重跑三道门禁 |

---

## 7. 放行建议

先修 **M1 + M2 + M3 + M4**（4 项都在前端单文件内，改动量很小：M1 合并逻辑约 8 行、M2 一处渲染条件、M3 两处 `finally`、M4 一条新 MessageBar + 一组 i18n key），补 4 条用例，重跑 `tsc` / `vitest` / `vite build` 三道门禁后即可放行。

S1–S6 建议同一批一起修（都在同一批文件里，顺手成本极低，且 S1/S4/S6 是用户直接可感知的体验缺口）。S7–S9 与文案偏离可并入后续清理。
