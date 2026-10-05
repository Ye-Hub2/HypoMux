# HypoMux 前端分析报告（desktop/frontend）

- 分析对象：`desktop/frontend/**`（React 18 + TypeScript + Fluent UI v9 + Wails v3 绑定）
- 版本基线：`desktop/frontend/package.json` version `"2.7.0"`，分支 main（干净）
- 行号口径：**引用行号**（`文件:行号`）全部为当前 checkout 的物理行号，由 `Select-String` 输出，可直接用编辑器跳转核对。
- **"行数"口径警告**：本报告表格中的文件"行数"沿用任务书的统计口径 = **非空行数**（等价 `Get-Content | Measure-Object -Line`，空行不计）。它与编辑器/`wc -l` 显示的**物理总行数**不同，差异见下表；两者都实测过，引用行号以物理行为准。

| 关键文件 | 非空行（本报告口径） | 物理总行数（含空行） |
|---|---|---|
| `src/app.css` | 5147 | **5947** |
| `src/pages/RoutingPage.tsx` | 1126 | **1170** |
| `src/pages/SettingsPage.tsx` | 1057 | **1096** |
| `src/pages/ConnectionsPage.tsx` | 843 | **882** |
| `src/pages/HealthPage.tsx` | 566 | **587** |
| `src/pages/NATDetectionPage.tsx` | 546 | **575** |
| `src/App.tsx` | 178 | **187** |
| `src/i18n/i18n.tsx` | 61 | **73** |

（换算参考：`app.css` 空行 800、`RoutingPage.tsx` 空行 44、`SettingsPage.tsx` 空行 39、`App.tsx` 空行 9。）
- 分析限制：本机未安装 Go、未安装 pnpm、`desktop/frontend/node_modules` **不存在**，因此 **tsc / vitest / npm 脚本均未执行**；所有"运行时行为"结论均为**静态阅读源码**得出，标注为"静态推断"。
- 证据规则：每条结论附 `相对路径:行号`；无法核实的一律写入 §9，不编造。

---

## 1. 目录与页面拓扑

### 1.1 规模总览（实测行数）

| 区域 | 文件数/行数 | 证据 |
|---|---|---|
| 入口 | `App.tsx` 178 行、`main.tsx` 14 行、`product.ts` 8 行（非空行口径） | 实测 |
| 页面 `pages/` | **21** 个文件 = 11 个页面 tsx + 9 个纯逻辑 ts + 1 个 `mtu.css` | 见 1.4 / 1.6 |
| 组件 `components/` | **47** 个文件 = 45 个 ts/tsx + 2 个 css（`ai/assistant.css`、`ai/skins/skins.css`） | 见 1.5 / 1.6 |
| 样式 | `app.css` **5147** 行（物理 5947）+ `theme/*.css` 5 个文件 + 组件级 3 个 css | 实测 |
| 国际化 | `i18n/i18n.tsx` 61 行（物理 73）+ `i18n/legacy.messages.json` **690** 行 | 实测 |
| 源码总量 | `src/**` 非测试文件 **102** 个 | 实测 |

对账：`src/**` 全部文件 **158** 个（102 非测试 + 56 测试），与任务书的"157 文件"相差 1（口径差异，可能是 `vite-env.d.ts` 或某个纯声明文件是否计入；**未逐一比对**）。
| 平台层 `platform/` | 8 个源码文件 + 5 个测试 | 见 §2 |
| 状态 `state/` | 8 个文件（`useEngineState.ts` 571 行最大） | 见 §3 |
| 测试 | **56** 个 `*.test.ts(x)`（任务书估计 54，实测 56） | 见 §5 |

### 1.2 应用入口与"路由"

项目**没有 router 库**（无 react-router 依赖），路由是 `AppPage` 字符串联合 + `useState` 手工实现。

- `desktop/frontend/src/components/shell/CompactNavigation.tsx:17` — `export type AppPage = "assistant" | "tools" | "home" | "routing" | "health" | "connections" | "settings" | "blocked-domains" | "about" | "appearance"`
- `desktop/frontend/src/App.tsx:85-96` — `pageOrder` 数组决定前进/后退方向：`[home, assistant, routing, health, connections, tools, settings, blocked-domains, about, appearance]`
- `desktop/frontend/src/App.tsx:98-108` — `navigate(nextPage, adapterName?)`：同页直接返回；否则比较 `pageOrder` 索引设置 `pageDirection`，`setNavigationRevision(r => r+1)` 并 `setPage`
- `desktop/frontend/src/App.tsx:67-77` — 初始页来自查询串 `?page=`，**仅 `import.meta.env.DEV` 生效**，白名单为上列 10 个 page，否则回落 `"home"`
- `desktop/frontend/src/App.tsx:27-34` — 除 `HomePage` 外**所有页面都是 `lazy(() => import(...))`**：AppearanceLab / AboutPage / BlockedDomainsPage / HealthPage / ConnectionsPage / RoutingPage / ToolsPage / SettingsPage
- `desktop/frontend/src/App.tsx:178-187` — 根组件：`?tray=1` 时直接渲染 `<TrayMenu/>`（独立托盘窗口），否则 `LanguageProvider > AppearanceProvider > HypoMuxWindow`
- `desktop/frontend/src/main.tsx:5` — 依据 `?tray=1` 给 `documentElement` 加 `tray-document` 类；`:9-11` 全局 `contextmenu` preventDefault；`:13-17` `createRoot(...).render(<StrictMode>)`

**子页面高亮父项**：`desktop/frontend/src/components/shell/CompactNavigation.tsx:32` — `navigationPage = page === "blocked-domains" ? "settings" : page`（`blocked-domains` 页面挂靠在设置项下）。

### 1.3 外壳（components/shell/**）

- `desktop/frontend/src/components/shell/AppShell.tsx:49-51` — 渲染 `<TitleBar/>` + `<CompactNavigation/>` + `<div id="page-content" className="page-viewport" tabIndex={-1}>`
- `desktop/frontend/src/components/shell/AppShell.tsx:38-40` — `visited` 集合**只增不减**：访问过的页面常驻挂载，切换只用 `hidden` 隐藏（保状态、避免重挂载）
- `desktop/frontend/src/components/shell/AppShell.tsx:52-60` — `persistentPage`（= `"home"`）+ `PageActivity.Provider value={page===persistentPage}`，让首页知道自己是否在前台
- `desktop/frontend/src/components/shell/AppShell.tsx:61-64` — 每个页面独立 `.page-transition-layer`，带 `data-direction` 做进出场动画
- `desktop/frontend/src/components/shell/AppShell.tsx:45-48` — 无障碍跳过链接 `a.skip-to-content` → `#page-content` / `#ai-workspace`
- `desktop/frontend/src/components/shell/AppShell.tsx:32-37` — `aiEnabled` 变 false 时关闭助手并把当前 assistant 页重定向首页
- `desktop/frontend/src/components/shell/AppShell.tsx:75` — `<Suspense fallback={null}>` 包裹懒加载的 `AIAssistant`（**失败/加载中无视觉反馈**）
- `desktop/frontend/src/components/shell/TitleBar.tsx:21-23` — `attachWindowTouchDrag(titlebar.current, desktopPlatform)`（触摸/笔拖动，鼠标走 Wails 原生 drag handler）
- `desktop/frontend/src/components/shell/TitleBar.tsx:36-44` — `Events.On("common:WindowMaximise" / "common:WindowUnMaximise")` 同步最大化态
- `desktop/frontend/src/components/shell/TitleBar.tsx:53-59` — `.titlebar` / `.titlebar-identity`（`ProductMark` + "HypoMux" + `` `v${productInfo.version} · Desktop` ``）/ `.titlebar-drag`
- `desktop/frontend/src/components/shell/ProductMark.tsx`（5 行）— 极小的品牌图形组件；`components/shell/PageActivity.tsx`（10 行）— `usePageActive()` 上下文，**是后台页面抑制轮询的唯一开关**

### 1.4 页面清单与职责

| 页面 | 行数 | 职责（证据） |
|---|---|---|
| `pages/HomePage.tsx` | 344 | 首页：适配器聚合选择 + TUN 预检对话框。`HomePage.tsx:135-240` 结构：`EngineHero` → `section.network-section`（`:156`，`aria-labelledby="network-section-title"`）→ `NetworkAdapterItem` 列表（`:223`）→ `RuntimeStatusBar`（`:240`）；`:250-345` `tun-preflight-dialog`（预检证据/问题列表/今日不再提醒复选框）；`:66-90` `handleTunPreflight` 用 Promise 化的模态确认 |
| `pages/ConnectionsPage.tsx` | 843 | 活动连接：表格 + 分组 + 搜索 + 排序 + 右键菜单 + 快速建规则。`:83/:92` 导出 `connectionRuleCandidates`/`preferredConnectionRuleOutbound`；`:141-159` 单元级计时器；`:244-251` 首次加载 + 串行轮询；`:762` 大列表走 `VirtualRows` |
| `pages/RoutingPage.tsx` | 1126 | 路由规则编辑器（最大单文件）。`:141` `ruleView: "manual" \| "sets"`；`:53` 引入 `LatestSaveQueue`；`:73` `makeDrafts`、`:84` `reconcileSavedDrafts`（后端归一化后保持行身份稳定）；`:202-204` 保存队列；`:683` 注释说明导入走同一校验与保存队列；`:835-836` 选择态需在防抖前跨导航存活 |
| `pages/SettingsPage.tsx` | 1057 | 设置中心（7+ 个 section）：`:79 SettingRow`、`:110 SettingDropdown`、`:140 SettingSwitch`、`:161 SettingSlider`、`:191 SettingInput`、`:215 SettingTabs`、`:238 SettingsPage`；`:247` `usePageActive()`；`:249-257` 网络项本地草稿 + `beforeunload` 拦截；`:318` `SettingsSaveQueue`；`:283-312` 设置索引浮层用 `IntersectionObserver` + `ResizeObserver` |
| `pages/HealthPage.tsx` | 566 | 网络体检容器，含三视图 `healthView: "link" \| "nat" \| "mtu"`（`:128`），`:385-419` 用 Fluent `TabList` 切换；`:328` 体检中 400ms 串行轮询进度；`:314-321` 体检前 `adapterSaveQueue.flush()` |
| `pages/NATDetectionPage.tsx` | 546 | NAT 类型检测视图（父页传 `adapters/enginePhase/loading/preview/text/notify`）；`:164` `startSerialPoll` |
| `pages/MTUDetectionPage.tsx` | 139 | MTU 探测视图（同 HealthPage 子视图） |
| `pages/BlockedDomainsPage.tsx` | 154 | 屏蔽域名管理；`:28` 接收 `onBack`（作为设置子页）；`:57-67` 3000ms 轮询 + `requestSequence` 防陈旧响应 |
| `pages/ToolsPage.tsx` | 152 | 工具箱；`:36-50` 自写 `setTimeout` 链轮询 Steam CDN 状态（5000ms，`document.hidden` 时暂停） |
| `pages/AboutPage.tsx` | 240 | 关于/版本/发布说明；`:29 ReleaseNotes`、`:64 AboutPage`；`:127` 轮询更新下载进度 |
| `pages/AppearanceLab.tsx` | 190 | 外观实验台（`?page=appearance` + DEV 专属入口 `CompactNavigation.tsx:111-123`）；`:24 RangeControl`、`:47 AppearanceLab` |
| `pages/healthNotice.ts`（2 行）、`pages/natDetectionPolicy.ts`（4 行）、`pages/routingBatch.ts`（13）、`pages/routingEffect.ts`（15）、`pages/connectionNavigation.ts`（11）、`pages/connectionView.ts`（30）、`pages/connectionSort.ts`（37）、`pages/connectionGroups.ts`（62）、`pages/connectionPreview.ts`（23） | — | 从页面中抽出的纯逻辑（多数有单测，见 §5） |

### 1.5 组件分层

三层：**shell（外壳）→ 页面 → 领域组件**，另有横切组件与材质层。

- 外壳层：`components/shell/{AppShell,CompactNavigation,TitleBar,ProductMark,PageActivity,windowTouchDrag}`
- 领域组件：
  - AI 助手：`components/ai/{AIAssistant(275),AssistantCompanion(82),AssistantMessage(24),pageContext(17)}`
  - 皮肤系统：`components/ai/skins/{SkinWardrobe(171),SkinCharacter(72),LayeredCharacter(72),Live2DCharacter(146),DefaultCharacter(14),store(99),package(160),packageAsync(26),package.worker(14),live2dPackage(66),layeredPackage(47)}`
  - 首页：`components/home/{EngineHero(162),NetworkAdapterItem(192),RuntimeStatusBar(77),ThroughputDisplay(108),NetworkHealthBadge(38),schedulingStrategies(42)}`
  - 热点/多网卡：`components/{HotspotPanel(244),HotspotQuickControl(76),hotspotAccess,hotspotDraft,hotspotOperation}`
  - 规则订阅：`components/RuleSetsPanel.tsx(293)`、Steam：`components/{SteamCDNPanel(159),steamCDNView(29)}`
  - 通知：`components/notifications/{AppNotifications(280),errorCodes(32),notificationMessage(76)}`
  - 托盘：`components/tray/TrayMenu.tsx(148)`
- 材质/外观横切层：`components/material/{WallpaperLayer(14),GlassSurface(21),useCardGlowField(124)}`、`components/appearance/AppearancePreview(91)`
- 通用列表：`components/VirtualRows.tsx`（65 行）
- 组件级样式（随组件放置，非全局 `app.css`）：`components/ai/assistant.css`（257 行）、`components/ai/skins/skins.css`（129 行）——这是"组件自带样式"的例外，其余样式集中在 `app.css`

---

### 1.6 前端源码文件总清单（实测行数，`desktop/frontend/src/**`，不含测试）

口径：行数 = **非空行**（见 §1.1 说明）。文件数实测：**102 个非测试文件**（pages 21、components 47、platform 8、state 8、theme 12、i18n 2、根 4）；另有 **56 个测试文件**（§5）。

**根与入口**

| 文件 | 行 |
|---|---|
| `src/app.css` | 5147 |
| `src/App.tsx` | 178 |
| `src/i18n/i18n.tsx` | 61 |
| `src/i18n/legacy.messages.json` | 690 |
| `src/main.tsx` | 14 |
| `src/product.ts` | 8 |
| `src/vite-env.d.ts` | 1 |

**`src/pages/`（20 个文件）**

| 文件 | 行 | 文件 | 行 |
|---|---|---|---|
| `RoutingPage.tsx` | 1126 | `connectionGroups.ts` | 62 |
| `SettingsPage.tsx` | 1057 | `connectionSort.ts` | 37 |
| `ConnectionsPage.tsx` | 843 | `connectionView.ts` | 30 |
| `HealthPage.tsx` | 566 | `connectionPreview.ts` | 23 |
| `NATDetectionPage.tsx` | 546 | `routingEffect.ts` | 15 |
| `HomePage.tsx` | 344 | `routingBatch.ts` | 13 |
| `AboutPage.tsx` | 240 | `connectionNavigation.ts` | 11 |
| `AppearanceLab.tsx` | 190 | `natDetectionPolicy.ts` | 4 |
| `BlockedDomainsPage.tsx` | 154 | `healthNotice.ts` | 2 |
| `ToolsPage.tsx` | 152 | `MTUDetectionPage.tsx` | 139 |

页面目录另含 1 个样式文件：`mtu.css` 53 行（NAT/MTU 体检子视图专用，`pages/mtu.css`）。

**`src/components/`（44 个源文件）**

| 文件 | 行 | 文件 | 行 |
|---|---|---|---|
| `RuleSetsPanel.tsx` | 293 | `material/useCardGlowField.ts` | 124 |
| `notifications/AppNotifications.tsx` | 280 | `material/GlassSurface.tsx` | 21 |
| `ai/AIAssistant.tsx` | 275 | `material/WallpaperLayer.tsx` | 14 |
| `HotspotPanel.tsx` | 244 | `home/EngineHero.tsx` | 162 |
| `home/NetworkAdapterItem.tsx` | 192 | `home/ThroughputDisplay.tsx` | 108 |
| `ai/skins/SkinWardrobe.tsx` | 171 | `home/RuntimeStatusBar.tsx` | 77 |
| `SteamCDNPanel.tsx` | 159 | `home/schedulingStrategies.ts` | 42 |
| `ai/skins/package.ts` | 160 | `home/NetworkHealthBadge.tsx` | 38 |
| `tray/TrayMenu.tsx` | 148 | `appearance/AppearancePreview.tsx` | 91 |
| `ai/skins/Live2DCharacter.tsx` | 146 | `ai/AssistantCompanion.tsx` | 82 |
| `shell/CompactNavigation.tsx` | 132 | `ai/skins/SkinCharacter.tsx` | 72 |
| `shell/windowTouchDrag.ts` | 125 | `ai/skins/LayeredCharacter.tsx` | 72 |
| `shell/TitleBar.tsx` | 94 | `ai/skins/store.ts` | 99 |
| `shell/AppShell.tsx` | 75 | `ai/skins/live2dPackage.ts` | 66 |
| `HotspotQuickControl.tsx` | 76 | `ai/skins/layeredPackage.ts` | 47 |
| `VirtualRows.tsx` | 65 | `ai/skins/packageAsync.ts` | 26 |
| `notifications/notificationMessage.ts` | 76 | `ai/skins/layeredFixture.ts` | 15 |
| `notifications/errorCodes.ts` | 32 | `ai/skins/package.worker.ts` | 14 |
| `ai/AssistantMessage.tsx` | 24 | `ai/skins/DefaultCharacter.tsx` | 14 |
| `ai/pageContext.ts` | 17 | `hotspotAccess.ts` | 15 |
| `hotspotOperation.ts` | 14 | `shell/PageActivity.tsx` | 10 |
| `steamCDNView.ts` | 29 | `shell/ProductMark.tsx` | 5 |
| `hotspotDraft.ts` | 3 | | |

组件目录另含 2 个样式文件：`ai/assistant.css` 257 行、`ai/skins/skins.css` 129 行（组件自带样式，其余样式集中在 `src/app.css`）。

**`src/platform/`（8 个源文件）**：`services.ts` 358、`settingsQueue.ts` 97、`desktop.ts` 102、`latestSaveQueue.ts` 75、`serialPoll.ts` 32、`adapterSaveQueue.ts` 28、`ai.ts` 25、`runtime.ts` 19

**`src/state/`（8 个源文件）**：`useEngineState.ts` 571、`aiAvailability.ts` 32、`startupWarningReminder.ts` 23、`adapterRuntime.ts` 18、`adapterVisibility.ts` 6、`systemProxyTakeover.ts` 1

**`src/theme/`**：`controls.css` 217、`appearance.store.tsx` 203、`material.tokens.css` 161、`design.tokens.css` 135、`background.service.ts` 110、`createFluentTheme.ts` 93、`appearance.presets.ts` 74、`appearance.types.ts` 55、`motion.tokens.css` 56、`semantic.tokens.css` 35、`wallpaper.ts` 29、`typography.tokens.css` 10

**规模分布特征**：≥500 行的 6 个文件全部是页面/状态容器（`RoutingPage`/`SettingsPage`/`ConnectionsPage`/`useEngineState`/`HealthPage`/`NATDetectionPage`），而 ≤30 行的文件有 14 个（多为 context、常量、纯策略）→ **复杂性高度集中在少数几个巨型文件，外围模块都很小**。这一分布说明重构的着力点明确（§7 问题 3）。

---

## 2. 前后端边界（Wails v3 绑定）

### 2.1 绑定目录当前不存在（重要）

- `desktop/frontend/src/platform/services.ts:1` 起 `import * as bindings from "../../bindings/..."`，而仓库中 **`desktop/frontend/bindings/` 不存在**（全仓库递归搜索"bindings"目录零结果）。
- `desktop/frontend/vite.config.ts:1-13` — `plugins: [react(), wails("./bindings")]`，绑定路径**硬编码** `./bindings`
- `desktop/frontend/tsconfig.json` — `"include": ["src", "bindings"]`
- 结论：绑定由 Go 侧 `wails` 生成（构建时/开发时），**当前 checkout 不含生成物**，所以前端目前无法独立 typecheck / build。这是流程事实而非缺陷，但会让 CI/CI 之外的本地验证依赖 Go 工具链（本机无 Go）。
- 未忽略生成物：根 `.gitignore` 的 "Wails generated output." 段只忽略 `/desktop/.task/`、`/desktop/bin/`、`/desktop/build/tray-review/` 与 nsis 的 `MicrosoftEdgeWebview2Setup.exe`，**没有忽略 `bindings/`**（即生成物本应入库或实际未提交 → 见 §9）。

### 2.2 唯一门面：`appServices`

- `desktop/frontend/src/platform/services.ts:12-28` — `isDesktopRuntime()` 判据是 Wails 注入的 `window.go.main.App`
- `desktop/frontend/src/platform/services.ts:36-48` — `withServiceTimeout(promise, ms, action)`：`Promise.race` + `setTimeout`，超时抛 `` Error(`${action} timed out after ${ms}ms`) ``
- `desktop/frontend/src/platform/services.ts:50-104` — `webPreviewServices`（浏览器假数据）
- `desktop/frontend/src/platform/services.ts:106-186` — `desktopServices`（每个方法薄封装 `bindings.*`，如 `:136-140` `diagnostics.latest()`）
- `desktop/frontend/src/platform/services.ts:188-201` — `export const appServices = isDesktopRuntime() ? desktopServices : webPreviewServices`
- 页面**一律通过 `appServices.*` 调用**（`App.tsx:47`、`SettingsPage.tsx:371-409`、`ConnectionsPage.tsx:224`…），不直接 import `bindings`；**唯一在组件层直接 import `@wailsio/runtime` 的是 `components/shell/TitleBar.tsx:10`**（窗口事件）。
- 窗口控制集中在 `desktop/frontend/src/platform/desktop.ts`（102 行，minimise/maximise/unmaximise/toggleMaximise/close/isMaximised/showStartup/setWindowAppearance）与 `platform/runtime.ts`（19 行）。
- 浏览器预览还有页面级 fixture 开关：`ConnectionsPage.tsx:204-205`（`?fixture=connections` → `await import("./connectionPreview")`）、`HealthPage.tsx:43/53-88`。

### 2.3 错误如何在 UI 侧呈现

三层链路：**后端错误 → 摘要化 → 错误码 → 通知卡片**。

1. `components/notifications/notificationMessage.ts:65-83` — `prepareNotificationMessage(message, intent, locale)`：error 走 `conciseDiagnosticMessage`（`:9-63`，按关键词归类为固定中英文案：超时/取消/服务不可用/权限/网络），非 error 超 72 字符截断为 `slice(0,69).trimEnd()+"…"`，返回 `{ summary, detail }`。→ **UI 不直接展示后端原文**，保留可展开 detail。
2. `components/notifications/errorCodes.ts`（32 行）— 关键词/`dedupeKey` 前缀 → 错误码（HM-E1001 超时、E1002 命名管道/拒绝连接、E1003 权限、E1004 网络/DNS，以及 HM-E1201/1301/1401/1501/1601/1701/1101 等页面域码），`:32-35` 兜底 `HM-E1900`。
3. `components/notifications/AppNotifications.tsx:95` — 无 `dedupeKey` 时以 `` `${intent}:${title}:${summary}` `` 作为 id；`:119` — `[next, ...current.filter(n => n.id !== id)].slice(0, 5)`（**去重 + 上限 5 条**）；`:112` 默认超时：success 3200ms / info 4200ms / error·带 action 不自动消失；`:214-216` `role={error?"alert":"status"}`、`aria-live={error?"assertive":"polite"}`、`aria-atomic="true"`；`:200` 出场动画 240ms 后才卸载（避免塌陷）；`:261-262` `aria-expanded`/`aria-controls`。
4. 页面侧示例：`HomePage.tsx:56-65` `notifyError` 区分"提示/错误"并带"重试" action；`SettingsPage.tsx:347-349` 通知 dedupeKey 为 `` `settings:${intent}:${title}` ``；`ConnectionsPage.tsx:231-236` dedupeKey `connections:load-error`。

---

### 2.4 跨模块通信通道（`window` 事件 + localStorage）

前端**没有事件总线库**，模块间横向通信全部走 `window` 自定义事件。实测清单：

| 事件名 | 定义/派发 | 监听 |
|---|---|---|
| `hypomux:ai-changed` | `components/ai/AIAssistant.tsx:86` | `pages/RoutingPage.tsx:317`、`pages/SettingsPage.tsx:400`、`pages/ToolsPage.tsx:54`、`state/useEngineState.ts:356` |
| `hypomux:ai-selection` | `pages/RoutingPage.tsx:165`（带 detail `{page, selection}`；卸载时 `:166` 清空） | `components/ai/AIAssistant.tsx:74` |
| `hypomux:ai-settings` | `pages/SettingsPage.tsx:596`（`new Event(...)`，**无 detail**） | `components/ai/AIAssistant.tsx:130` |
| `hypomux:ask-ai` | **全 `src/**` 未找到派发点** | `components/ai/AIAssistant.tsx:128` |
| `hypomux:system-proxy-takeover-changed` | 常量 `state/systemProxyTakeover.ts:1`；派发 `pages/SettingsPage.tsx:325` | 见常量消费者 |
| `hypomux:adapter-visibility-changed` | 常量 `state/adapterVisibility.ts:3`；派发 `pages/SettingsPage.tsx:331` | 见常量消费者 |
| `hypomux:ai-availability-changed` | 常量 `state/aiAvailability.ts:4`；派发 `state/aiAvailability.ts:8`（detail = enabled） | 见常量消费者 |

localStorage 键（全部实测，共 3 个）：

| 键 | 定义位置 | 用途 |
|---|---|---|
| `hypomux.language` | `i18n/i18n.tsx:22`（读写 `:27`、`:41`） | 界面语言 |
| `hypomux.startup-warnings.dismissed-date.v1` | `state/startupWarningReminder.ts:3`（读写 `:14`、`:23`） | "今日不再提醒" |
| `hypomux.appearance.v1` | `theme/background.service.ts:5`（读 `:31`、写 `:43`、删 `:23`） | 外观/背景文档；由 `theme/appearance.store.tsx:3`（`appearancePersistence`、`loadLegacyBrowserAppearance`）+ `:49` 的 `appearancePersistence` 统一读写 |

评价：事件通道**数量少（7 个）、命名有统一前缀、常量集中定义在 `state/` 三个小文件里**，比散落的字符串好维护；但代价是**类型不安全**（detail 形状无编译期校验，`SettingsPage.tsx:596` 甚至用无 detail 的 `Event` 而监听方按带 detail 处理）且**无法追踪引用**（`hypomux:ask-ai` 只有监听没有派发，就很可能是死代码；**未验证**它是否由未纳入本次扫描的代码派发）。

---

## 3. 状态管理与数据轮询

### 3.1 store 方案：**全部自研，零状态库**

`package.json` dependencies 中不存在 redux / zustand / jotai / mobx / react-query / swr。实际方案是：

- React 内建 `useState` + `useRef` + Context；典型"大页面把状态放在自己身上"（`RoutingPage.tsx:141-191` 一个组件内 **30+ 个 `useState`**）。
- 两个自研 Context store：
  - `state/useEngineState.ts`（571 行）— 引擎/适配器全局状态：`:25 HOME_TELEMETRY_POLL_MS = 800`、`:27 shouldPollEngineSnapshot`、`:138 phaseText`、`:157 useEngineState`；`:310` 快照轮询 + `:322` 适配器轮询（两条独立串行轮询）。
  - `theme/appearance.store.tsx`（203 行）— 外观 store：`:14-24` 上下文接口；`:40-73` 归一化/迁移/初始值；`:75-103` `applyDocumentTokens` 写 `documentElement.dataset` 与约 20 个 CSS 变量；`:114-140` 加载失败时**刻意不置 hydrated**（注释说明否则默认值会覆盖用户外观文档）；`:150-177` 300ms 防抖保存，`pagehide`/`visibilitychange(hidden)`/卸载时 flush；`:178-185` 同步原生窗口外观。
- 跨模块通信靠 **window 自定义事件**：`SettingsPage.tsx:324-334` 派发 `SYSTEM_PROXY_TAKEOVER_EVENT`、`ADAPTER_VISIBILITY_EVENT`；`SettingsPage.tsx:398-402` 监听 `hypomux:ai-changed` 触发重载。

### 3.2 三个保存队列 + 一个轮询器（串行化机制）

| 原语 | 文件:行 | 机制 |
|---|---|---|
| 轮询 | `platform/serialPoll.ts`（32 行），测试 `platform/serialPoll.test.ts` | `startSerialPoll(task, intervalMs=1500)` 返回 stop；内部是 **`setTimeout` 链**而非 `setInterval`，等待上一轮 `await` 完成后才排下一轮 → 天然不重入；task 抛错被吞不打断链；stop 取消 pending timer |
| 最新值队列 | `platform/latestSaveQueue.ts`（75 行），测试 51 行 | `LatestSaveQueue<TInput,TOutput>`：在飞时**只保留最后一个**待执行输入（丢弃中间值），在飞 promise 复用，`flush()` 等当前完成；失败后队列恢复可用 |
| 网卡写入单例 | `platform/adapterSaveQueue.ts`（28 行），测试 26 行 | `export const adapterSaveQueue = new LatestSaveQueue<AdapterMutation, AdapterView[] \| null>(...)`；`HealthPage.tsx:314-321` 体检前 `await adapterSaveQueue.flush()`，失败即提前返回 |
| 设置保存队列 | `platform/settingsQueue.ts`（97 行），**测试 195 行（最厚）** | `SettingsSaveQueue<TState>`：`attach(updater)` / `enqueue(operation, fields)` / `mergeAuthoritative(authoritative, current)`；按**字段所有权**串行化，操作返回 `SaveOutcome`；`SettingsPage.tsx:313-317` 注释明确目标是"并发保存会让较早响应覆盖较新乐观值" |
| 页面专用队列 | `RoutingPage.tsx:202-204` | `LatestSaveQueue<{rules, order}, RoutingSnapshot>`，`:835-836` 注释说明队列在卸载后仍存活，以保留离散选择 |

`SettingsPage.tsx:336-343` 的 `enqueueSave` 统一包装：`settingsRevision.current++` + 吞掉队列 reject（避免 React 事件处理器产生未处理拒绝）；`:381` 加载响应仅在 `revision === settingsRevision.current` 时合并（`:382` 走 `mergeAuthoritative`）。

### 3.3 轮询点位全表（静态推断：均为串行链，非 setInterval 风暴）

| 位置 | 间隔 | 抑制条件 |
|---|---|---|
| `state/useEngineState.ts:310` 引擎快照 | `HOME_TELEMETRY_POLL_MS = 800`（`:25`） | `shouldPollEngineSnapshot`（`:27`） |
| `state/useEngineState.ts:322` 适配器列表 | 由 `startSerialPoll` 默认 1500 | 同上 |
| `ConnectionsPage.tsx:250` | 1500 | `live` 开关 + `pageActive` + `document.hidden` |
| `ConnectionsPage.tsx:155` 计时单元格 | 1000 | `IntersectionObserver` 可见性 + `document.hidden` + `pageActive` |
| `BlockedDomainsPage.tsx:61` | 3000 | `pageActive`；卸载 stop + `requestSequence++` |
| `HealthPage.tsx:328` 体检进度 | 400 | 仅体检进行中；`runEpoch` 防陈旧 |
| `NATDetectionPage.tsx:164` | 默认 1500 | 页面级 |
| `AboutPage.tsx:127` 更新进度 | — | 下载中 |
| `ToolsPage.tsx:36-50` Steam CDN | 5000 | `document.hidden`（自写 setTimeout 链，**未用 `startSerialPoll`**） |

### 3.4 竞态 / 重复请求 / 无上限轮询评估

- **竞态防护是系统性的**（优势，非问题）：`requestActive` 互斥（`ConnectionsPage.tsx:197/217-218`）、`requestSequence`（`BlockedDomainsPage.tsx:40-41`）、`runEpoch`（`HealthPage.tsx:326`）、`cancelled` 标志（`SettingsPage.tsx:368/380/395`）、`adapterListKey` 变化检测（`SettingsPage.tsx:359-361`、`HealthPage.tsx:131`）、`settingsRevision`（`SettingsPage.tsx:248`）。
- **重复请求**：无全局去重/缓存层；同一时刻多个页面各自调 `appServices.*` 是可能的（首页常驻 + 当前页）。因为页面在 `visited` 后**常驻挂载**（`AppShell.tsx:38-40`），抑制后台页面轮询**完全依赖每个页面自觉调用 `usePageActive()`**（`components/shell/PageActivity.tsx`）。`ToolsPage.tsx:36-50` 即未走 `appServices` 的统一约定、自己写循环（但确实读了 `document.hidden`）→ 这是**易漏模式**而非已确认 bug。
- **无上限轮询**：`startSerialPoll` 是 setTimeout 链，不会堆积；但 `useEngineState.ts:25` 的 **800ms** 是全程节奏，加上页面级 400/800/1000/1500/3000/5000ms 多点轮询，**没有自适应退避（backoff）**：窗口最小化只靠 `document.hidden` 检查，没有暂停机制。
- **未发现明确的重复提交竞态**：所有写路径都过队列（settings / adapter / routing）+ 页面互斥标志。
- **未限制的并发域**：`SettingsPage.tsx:371-378` 用 `Promise.all` 并发 4 个请求（settings/configPath/migrationStatus/adapters），这是有意的首屏加速，无超时包装（其余地方普遍用 `withServiceTimeout`，如 `ConnectionsPage.tsx:223` 8s）→ **此处缺少超时会挂住 loading**（`:394` finally 才会 `setLoading(false)`）。

---

## 4. i18n 现状（结论：基础设施真实可用，覆盖率很低）

**基础设施（真实可用）**
- `i18n/i18n.tsx:13` `Locale = "zh" | "en"`；`:16-20` 上下文 `{locale, setLocale, t}`；`:22` 持久化键 `hypomux.language`；`:44-46` 同步 `<html lang>`（`en` / `zh-CN`）；`:48-58` 用后端 `settings.language` 覆盖本地值。
- `i18n/i18n.tsx:60-63` — `t = (key, values) => messages[locale][key] ?? messages.zh[key] ?? key`：**缺失键回退中文，再回退键名**。
- `i18n/legacy.messages.json`（690 行）含 **zh 与 en 两套目录，键集一致**，每套约 250 键（`window_title`、`status_*`、`col_*`、`settings_*`、`routing_*`、`rulesets_*`、`diag_*`、`nav_*`、`home_*`、`tun_*`、`tools_*`、`about_*`、`tray_*`、`blocked_*`、`mode_*`、`infobar_*`）。
- 测试覆盖：**无 `i18n` 相关测试文件**（§5 清单中不存在）。

**目录质量实测（`legacy.messages.json`）**

| 指标 | 数值 | 说明 |
|---|---|---|
| 每语言键数 | **342**（zh 342 / en 342） | `Object.keys` 计数 |
| en 相对 zh 缺键 | **0** | 两套目录完全对齐 |
| zh 相对 en 缺键 | **0** | 无单向遗漏 |
| 中英文案完全相同 | **11** 键 | 允许（如语言名、SOCKS5:） |
| 带格式说明符（`{x:.2f}`）的键 | **5**：`status_running_live`、`up_format`、`home_up_conn`、`home_card_speed`、`home_row_traffic` | 见下"零引用"结论 |
| 带普通占位符（`{value}`）的键 | **39** | `formatMessage` 能正确处理这类 |
| 被 `t()` 实际引用的键 | **158** | 共 185 个 `t()` 调用点 |
| **从未被引用的"死键"** | **184（54%）** | 含全部 `window_title`/`subtitle`/`status_*`/`col_*`/`error_*` 系列 |
| 传入 `t()` 但目录中不存在的键 | **0** | 无拼写错误导致的回退到键名 |

键前缀分布（zh）：`settings` 97、`routing` 51、`home` 37、`about` 26、`rulesets` 25、`diag` 21、`blocked` 16、`tun` 14、`status` 10、`nav` 6、`col` 5、`warn` 5、`tools` 5、`infobar` 4、`mode` 3、`error` 3、`tray` 3、其余零散。
→ 结论修正：**目录不只是"抽取不足"，还是"抽取了一半就停了"**：`window_title`/`subtitle`/`status_*`/`col_*`（v2.0 时代的首页表格）整段废弃但保留；`settings`/`routing`/`rulesets` 三段是仍在服役的部分。

**覆盖率证据（真实可用性判据）**
- `t()` 调用（非测试文件）：**共 185 处、11 个文件** — `SettingsPage.tsx` 79、`RuleSetsPanel.tsx` 30、`RoutingPage.tsx` 22、`AboutPage.tsx` 19、`BlockedDomainsPage.tsx` 14、`CompactNavigation.tsx` 6、`NetworkAdapterItem.tsx` 5、`HomePage.tsx` 5、`EngineHero.tsx` 3、`RuntimeStatusBar.tsx` 1、`ThroughputDisplay.tsx` 1。
- 源码硬编码中文：**75 个文件、18195 个 CJK 字符**。Top：`AIAssistant.tsx` 1873、`SteamCDNPanel.tsx` 1615、`SettingsPage.tsx` 1476、`NATDetectionPage.tsx` 1448、`RoutingPage.tsx` 1420、`HotspotPanel.tsx` 1248、`ConnectionsPage.tsx` 956、`HealthPage.tsx` 759、`SkinWardrobe.tsx` 743、`HomePage.tsx` 570、`MTUDetectionPage.tsx` 561、`steamCDNView.ts` 461、`ToolsPage.tsx` 417、`ai/skins/package.ts` 398、`useEngineState.ts` 317、`ai/pageContext.ts` 314。
- 主导模式是**内联双语对**：`const text = (zh, en) => locale === "en" ? en : zh;`（`ToolsPage.tsx:16`、`BlockedDomainsPage.tsx:31`、`HealthPage.tsx:98`、`ConnectionsPage.tsx:172`、`HomePage.tsx:49`、`SettingsPage.tsx:273`），以及大量 `locale === "en" ? ... : ...` 三元（`CompactNavigation.tsx:35/38/39`、`TitleBar.tsx:28-30/46-50`、`AppShell.tsx:48`、`App.tsx:144-171`、`AppNotifications.tsx:282`）。
- **"import 了 i18n 却不用 `t()`"**（只用 `locale` 做三元）：`SkinWardrobe.tsx`、`AIAssistant.tsx`、`AssistantCompanion.tsx`、`NetworkHealthBadge.tsx`、`AppNotifications.tsx`、`AppShell.tsx`、`TitleBar.tsx`、`HotspotPanel.tsx`、`HotspotQuickControl.tsx`、`SteamCDNPanel.tsx`、`ConnectionsPage.tsx`、`HealthPage.tsx`、`ToolsPage.tsx`、`App.tsx`。
- 完全无本地化通道的位置：`index.html:27`（`role="status"` 的 "正在启动…"）、`main.tsx`、全部 CSS 内容、后端错误原文（经 `notificationMessage.ts` 摘要但仍保留原文 detail）。

**判定**：国际化**不是假的、也非"仅部分抽取"能概括**——渲染期语言切换（zh/en）对绝大多数界面**确实生效**（因为都写了 `locale === "en"` 分支），但 **`t()`/消息目录这条正式通道几乎被绕过**（185 处 vs 18195 个 CJK 字符），后果是：文案不可集中审计/翻译、新增语言需要改 75 个文件、`legacy.messages.json` 与内联文案双份维护、极易漂移。

**格式说明符缺陷（已验证为"潜伏缺陷"而非现行 bug）**：`i18n/i18n.tsx:29-34` 的 `formatMessage` 用 `/\{(\w+)(?::[^}]+)?\}/g` **丢弃** `:...` 说明符且不做数值格式化。实测：目录中带格式说明符的 5 个键（`status_running_live`、`up_format`、`home_up_conn`、`home_card_speed`、`home_row_traffic`，含 `{down:.2f}` / `{value:.2f}` / `{up:.2f}` / `{speed:.2f}`）**在 `src/**` 中零引用**（grep 无匹配）→ 当前不会产生错误渲染，但该函数一旦被这 5 个键（或将来任何带格式的键）使用就会输出未格式化数字；同时 `:29-34` 的正则也会静默吞掉说明符而不报错，属于**无声陷阱**。相关数值格式化目前散落在页面里各写一份：`HomePage.tsx:32-37` 与 `ConnectionsPage.tsx:121-126` 各有一份 `formatBytes` 实现（实现细节不同：一个用 `1024**3`、一个用 `1024*1024*1024`）。

---

## 5. 前端测试实况

### 5.1 框架与脚本

- 框架：**vitest 4.1.11 + jsdom 30 + @testing-library/react 16.3.2 + fake-indexeddb 6.0.0**（`package.json` devDependencies）；**没有 jest**。
- 脚本：`package.json` scripts → `"test": "vitest run"`（无 `--coverage`、无阈值配置；`vite.config.ts:1-13` 只有 server 与 plugins，**无 test 配置块**，故 jsdom 环境依赖 vitest 默认或隐式配置）。
- 构建：`"build": "tsc && vite build --mode production"`、`"build:dev": "tsc && vite build --minify false --mode development"` → **`tsc` 是构建前置门槛**，而 bindings 缺失使本地无法通过（§2.1）。
- 无 ESLint / Prettier / Stylelint 依赖与配置文件（全仓库无相关配置）。

### 5.2 测试文件分布（**56 个**，实测；任务书估计 54）

| 目录 | 数量 | 文件（行数） |
|---|---|---|
| `components/ai/` | 4 | AIAssistant(296)、AssistantCompanion(21)、AssistantCompanionLoading(25)、AssistantMessage(19) |
| `components/ai/skins/` | 9 | LayeredCharacter(47)、layeredPackage(35)、Live2DCharacter(105)、live2dPackage(34)、package(61)、packageAsync(38)、SkinCharacter(63)、SkinWardrobe(76)、store(77) |
| `components/home/` | 2 | NetworkAdapterItem(86)、ThroughputDisplay(17) |
| `components/notifications/` | 3 | AppNotifications(135)、errorCodes(21)、notificationMessage(23) |
| `components/shell/` | 3 | AppShell(82)、CompactNavigation(19)、windowTouchDrag(136) |
| `components/`（根） | 8 | hotspotAccess(11)、hotspotOperation(26)、HotspotPanel(234)、HotspotQuickControl(42)、RuleSetsPanel(200)、SteamCDNPanel(105)、steamCDNView(15)、VirtualRows(30) |
| `components/tray/` | 1 | TrayMenu(81) |
| `pages/` | 16 | AboutPage(78)、connectionGroups(39)、connectionNavigation(16)、connectionSort(71)、ConnectionsPage(**450**)、connectionView(46)、healthNotice(23)、HealthPage(32)、HomePage(296)、MTUDetectionPage(69)、NATDetectionPage(39)、natDetectionPolicy(12)、routingBatch(16)、routingEffect(16)、RoutingPage(210)、SettingsPage(347) |
| `platform/` | 5 | adapterSaveQueue(26)、companionExport(21)、latestSaveQueue(51)、serialPoll(31)、settingsQueue(**195**) |
| `state/` | 4 | adapterFeedback(78)、adapterVisibility(23)、startupWarningReminder(29)、useEngineState(52) |
| `theme/` | 1 | appearance.store(43) |

（合计：components 30 + pages 16 + platform 5 + state 4 + theme 1 = **56**。任务书给出的"54 个"与实际清单不符，以实测 56 为准。）

特征：**纯逻辑模块覆盖好，UI 渲染覆盖薄**。同一组件常有"逻辑测试 + 渲染测试"配对（如 `layeredPackage.test.ts` + `LayeredCharacter.test.tsx`），`platform/**` 5/5 全测，`pages` 的 9 个抽出的纯逻辑模块**全部**有单测。

### 5.3 无测试缺口（对照源码清单）

| 类别 | 无测试文件 | 行数 |
|---|---|---|
| **页面（3 个）** | `pages/AppearanceLab.tsx`、`pages/ToolsPage.tsx`、`pages/BlockedDomainsPage.tsx` | 190 + 152 + 154 = 496 |
| 页面辅助 | `pages/connectionPreview.ts`（仅 DEV fixture，影响小） | 23 |
| 外壳 | `components/shell/TitleBar.tsx`（含事件订阅与 4 个窗口按钮）、`ProductMark.tsx`、`PageActivity.tsx` | 94 + 5 + 10 |
| 首页组件 | `components/home/EngineHero.tsx`、`RuntimeStatusBar.tsx`、`NetworkHealthBadge.tsx`、`schedulingStrategies.ts` | 162 + 77 + 38 + 42 |
| 材质/外观 | `components/material/GlassSurface.tsx`、`WallpaperLayer.tsx`、`useCardGlowField.ts`、`components/appearance/AppearancePreview.tsx` | 21 + 14 + 124 + 91 |
| AI/皮肤 | `components/ai/pageContext.ts`、`skins/DefaultCharacter.tsx`、`skins/layeredFixture.ts`、`skins/package.worker.ts` | 17 + 14 + 15 + 14 |
| 热点 | `components/hotspotDraft.ts` | 3 |
| 平台/状态 | `platform/services.ts`(358)、`platform/desktop.ts`(102)、`platform/runtime.ts`(19)、`platform/ai.ts`(25)、`state/adapterRuntime.ts`(18)、`state/aiAvailability.ts`(32)、`state/systemProxyTakeover.ts`(1) | — |
| 主题 | `theme/appearance.presets.ts`(74)、`appearance.types.ts`(55)、`background.service.ts`(110)、`createFluentTheme.ts`(93)、`wallpaper.ts`(29) | — |
| 国际化/入口 | `i18n/i18n.tsx`、`App.tsx`、`main.tsx`、`product.ts` | — |

**最值得补的三个**：`TitleBar.tsx`（窗口控制 + 事件同步，无测试）、`platform/services.ts`（**唯一前后端门面、`withServiceTimeout` 与 `isDesktopRuntime` 全在此**，无测试）、`theme/background.service.ts`（110 行背景图处理，无测试）。

---

## 6. UI 工程质量

### 6.1 `app.css` 单文件 5147 行的构成（物理 5947 行，见 §1.1 口径说明）

实测：`!important` **93** 处、`@media` **25** 块、`@keyframes` **18** 个、`[data-appearance|material|panel-material|background-source|density|motion]` 主题选择器 **43** 处。

按注释横幅分节（行号＝节起点）：

| 行号 | 节 |
|---|---|
| 1-34 | 全局 reset / 字体 / 滚动条 |
| 35 | "Remove outline for mouse users (focus-visible handles keyboard)" — 无障碍焦点策略 |
| 143 | "Pointer-driven edge lighting" — 指针跟随玻璃高光 |
| 214 | "CSS fallback for environments where pointer field setup is unavailable" |
| 379 / 382 / 408 | 标题栏拖动：鼠标走 Wails 原生、触摸/笔走 `windowTouchDrag`；WebView2 兼容鼠标事件 |
| 935 / 971 | 页面过渡（"A nested tool view already travels with the entering page" / "Shared interaction motion"） |
| 1231 | 调度策略切片"预留两行高度防跳动" |
| 1519 | 操作反馈绘制在既有卡片边界内 |
| 1851 | Routing rules |
| 2183 | "English labels are longer than their Chinese counterparts" — 英文变长的布局预留 |
| 2802 | Network diagnostics |
| 3274 | NAT behavior diagnostics |
| 4029 | Active connections |
| 4679 | Appearance Lab |
| 5614-5784 | 热点/玻璃表面/装饰光辉（"Keep the decorative glow inside the scroll container" `:5767`） |
| 5784 | Steam：总览、空态、可搜索节点清单 |
| 5850 / 5868 / 5880 / 5882 | 壁纸贯穿 chrome 与内容；实色主题下导航不透明；窗口 chrome 可拖、内容可复制；规则订阅有独立视图 |
| 5929 | "Match the settings page's hierarchy and shared material tokens" |

配套 token 文件：`theme/design.tokens.css`(135)、`theme/material.tokens.css`(161)、`theme/controls.css`(217)、`theme/motion.tokens.css`(56)、`theme/semantic.tokens.css`(35)、`theme/typography.tokens.css`(10)。

评价：**分区是有意识的、带解释性注释**（注释密度高、说明了"为什么"），但**单文件 5147 行 + 全部类名全局 + 93 处 `!important`** 是可维护性风险；与 `AppearanceLab`（`:4679` 起，约 1000 行）说明外观实验台占了全站样式的近 1/5。

### 6.2 体积 Top 10

（行数＝非空行口径；物理总行数：RoutingPage 1170、SettingsPage 1096、ConnectionsPage 882、useEngineState 604、services 385、RuleSetsPanel 308、AppNotifications 304、AIAssistant 281、HotspotPanel 246、appearance.store 220。）

页面（含状态容器）：

| # | 文件 | 行数 |
|---|---|---|
| 1 | `pages/RoutingPage.tsx` | 1126 |
| 2 | `pages/SettingsPage.tsx` | 1057 |
| 3 | `pages/ConnectionsPage.tsx` | 843 |
| 4 | `state/useEngineState.ts` | 571 |
| 5 | `pages/HealthPage.tsx` | 566 |
| 6 | `pages/NATDetectionPage.tsx` | 546 |
| 7 | `platform/services.ts` | 358 |
| 8 | `pages/HomePage.tsx` | 344 |
| 9 | `components/RuleSetsPanel.tsx` | 293 |
| 10 | `components/notifications/AppNotifications.tsx` | 280 |

组件（不含 pages/state/platform）：

| # | 文件 | 行数 |
|---|---|---|
| 1 | `components/RuleSetsPanel.tsx` | 293 |
| 2 | `components/notifications/AppNotifications.tsx` | 280 |
| 3 | `components/ai/AIAssistant.tsx` | 275 |
| 4 | `components/HotspotPanel.tsx` | 244 |
| 5 | `components/home/NetworkAdapterItem.tsx` | 192 |
| 6 | `components/ai/skins/SkinWardrobe.tsx` | 171 |
| 7 | `components/home/EngineHero.tsx` | 162 |
| 8 | `components/ai/skins/package.ts` | 160 |
| 9 | `components/SteamCDNPanel.tsx` | 159 |
| 10 | `components/tray/TrayMenu.tsx` | 148 |

关键架构信号：**页面文件承载了状态机**（`RoutingPage.tsx:141-191` 30+ `useState`、`SettingsPage.tsx:245-281` 约 20 个 state/ref），而非抽成 reducer/hook。仅 9 个纯逻辑模块被抽出（§1.4 末行）。

### 6.3 内联样式与重复样式

- 内联 `style={{...}}` **一共只有 18 处左右**，集中在 `AssistantCompanion.tsx`(5)、`VirtualRows.tsx`(2)、`SettingsPage.tsx`(2)、`SkinWardrobe.tsx`(2)、`Live2DCharacter.tsx`(2)、`LayeredCharacter.tsx`(2)、`ConnectionsPage.tsx`(1)、`WallpaperLayer.tsx`(1)、`AppearanceLab.tsx`(1)、`CompactNavigation.tsx`(1)、`SkinCharacter.tsx`(1)。→ **样式基本全在 CSS 类里，内联样式不是问题**（这是优点）。
- 但**用 CSS 自定义属性做命令式布局**：`CompactNavigation.tsx:43-65` 用 ResizeObserver 计算并写 `translate3d`；`SettingsPage.tsx:290-293` 写 `--hm-settings-index-center-shift`；`appearance.store.tsx:75-103` 写约 20 个 `--hm-*` 变量。这是有意的（避免 React 重渲染），代价是布局逻辑散落在 JS 与 CSS 两侧。
- 重复样式风险集中在 `app.css` 93 处 `!important` 与 43 处 `data-*` 主题选择器（精确重复选择器统计未做，见 §9）。

### 6.4 无障碍（实测 `aria-*` 出现次数）

Top：`SettingsPage.tsx` 29、`AIAssistant.tsx` 24、`RoutingPage.tsx` 15、`ConnectionsPage.tsx` 13、`NetworkAdapterItem.tsx` 12、`SkinWardrobe.tsx` 12、`HotspotPanel.tsx` 11、`RuleSetsPanel.tsx` 9、`ToolsPage.tsx` 9、`CompactNavigation.tsx` 8、`NATDetectionPage.tsx` 8、`AppNotifications.tsx` 7、`SteamCDNPanel.tsx` 7。

优点：
- 全局跳过链接 `AppShell.tsx:45-48`；`#page-content` 带 `tabIndex={-1}`（`:51`）
- 通知语义正确：`AppNotifications.tsx:214-216` `role="alert"/"status"` + `aria-live="assertive"/"polite"` + `aria-atomic`
- 导航键盘漫游：`CompactNavigation.tsx:68-78` 自实现 roving focus（ArrowDown/ArrowUp/Home/End + 取模循环），`:95-105` `aria-label` + `aria-current="page"`
- 焦点策略有注释与实现：`app.css:35`（鼠标去除 outline、`focus-visible` 保留键盘焦点）
- 装饰元素正确标记：`AppNotifications.tsx:230/232` `aria-hidden="true"`；`CompactNavigation.tsx:79-84` 指示条 `aria-hidden`；`VirtualRows.tsx:66/68` 上下占位 `aria-hidden="true"`
- 对话框标题/描述通过 `aria-labelledby`（如 `HomePage.tsx:156`、`HomePage.tsx:286` `aria-label="预检详情"`）

缺口：
- 无 `@testing-library/jest-dom` 之外的无障碍断言；**没有任何 axe / 无障碍专用测试**（56 个测试文件中无 a11y 测试）
- 高亮/低对比外观（`AppearanceLab` 允许用户自由调 accent、亮度、透明度）**没有对比度守卫**（无对比度计算代码）
- `VirtualRows.tsx` 的虚拟化窗口在键盘 Tab 到屏幕外行时可能引发滚动跳变（**静态推断，未实测**）

### 6.5 性能

**已做的**：
- 路由级代码分割：`App.tsx:27-34` 8 个页面 lazy；`AppShell.tsx:9` AI 助手 lazy；`SkinCharacter.tsx:6` `lazy(() => import("./Live2DCharacter"))`；`AIAssistant.tsx:9` 衣橱 `loadWardrobe` 动态 import。
- **重资源按需加载**：`components/ai/skins/Live2DCharacter.tsx:97` `await Promise.all([import("pixi.js"), import("pixi-live2d-display/cubism4")])` → pixi.js 6.5.10 与 live2d 显示库**不进主包**，只有真正切到 Live2D 皮肤时才拉取。
- **Worker 化**：`components/ai/skins/packageAsync.ts:6` 每个皮肤解析/导出任务**独立 `new Worker(new URL("./package.worker.ts", import.meta.url), {type:"module"})`**，`:7` 60s 超时终止，`:8` 完成即 `terminate()`（注释：每个任务独占 worker，失败不阻塞其他操作、不驻留资源）；`:18/:22-24` 无 Worker 环境（DOM 测试）回落主线程并注释说明生产不回落以免重新冻结 UI。
- **长列表虚拟化**：`components/VirtualRows.tsx:10` `const virtual = rows.length > 80;`（≤80 行不虚拟化，避免小列表开销）；`:16-20` 累积偏移；`:24-28` 上下各 300px 过扫描；`:40-43` ResizeObserver + `scroll` passive + `requestAnimationFrame` 节流；`:46-62` 用 `borderBoxSize` 实测行高（支持换行标签与展开分组）；`:11-15` 自动清理已消失行的缓存。仅 `ConnectionsPage.tsx:762` 使用。
- **细粒度计时**：`ConnectionsPage.tsx:140-159` 注释"只有这个小格每秒重建，滚动与遥测不再每秒重建整页"，并用 `IntersectionObserver` + `document.hidden` 暂停。
- `document.hidden` 检查出现在连接轮询（`ConnectionsPage.tsx:250`）、计时器（`:148`）、Steam 轮询（`ToolsPage.tsx:36-50`）。
- 启动防白闪：`index.html:9-18` 在 React 渲染前用内联脚本写 `documentElement.dataset`（appearance/material/panelMaterial/backgroundSource）；`:21-26` 内联浅/深底色 + `.boot-placeholder` 骨架；`App.tsx:110-122` `showStartup()` 后双 `requestAnimationFrame` 才揭示（`startup-reveal`）。

**未做的 / 风险**：
- `useEngineState.ts:25` **800ms** 遥测节奏 + 多页面常驻挂载（`AppShell.tsx:38-40`）→ 长时间运行的后台开销依赖 `usePageActive` 正确使用；无退避、无"窗口最小化即暂停"的集中机制。
- `ConnectionsPage.tsx:761`（`:762` 三元另一支）在 ≤80 行时直接全量渲染（合理）。
- 无 `React.memo`/`useMemo` 计数的系统性审计（**未做**）；`RoutingPage`/`SettingsPage` 的巨型组件在每次 keystroke 上重建（`RoutingPage.tsx:835-836` 的防抖注释侧面印证这点）。
- 无 bundle 体积基线/预算（`vite.config.ts` 无 `build.rollupOptions.manualChunks`、无体积告警）→ **未验证**实际产物体积。
- 无 `ErrorBoundary`（App.tsx / AppShell.tsx 全文未见），懒加载 chunk 失败或渲染异常会白屏；`AppShell.tsx:75` 的 `Suspense fallback={null}` 在 AI 助手加载期间无反馈。

---

## 7. 问题清单（前 8 条）

1. **[置信度高] 当前 checkout 无法 typecheck/build（bindings 缺失）** — `desktop/frontend/src/platform/services.ts:1`（`import ... "../../bindings/..."`）、`desktop/frontend/vite.config.ts:1-13`（`wails("./bindings")` 硬编码）、`desktop/frontend/tsconfig.json`（`include: ["src","bindings"]`）— 证据：全仓库递归搜索 `bindings` 目录零结果，而 `package.json` 的 `build` 脚本首步就是 `tsc` — 影响：任何不经 Go/wails 生成步骤的环境（含本机）无法做类型检查与构建，前端问题只能靠读代码发现；客户端 CI 会强依赖 Wails 工具链 — 修复方向：确认 `bindings/` 是否应入库（`.gitignore` 未忽略它），或在 CI/文档中明确"前端验证必须先 `wails generate module`"，并给前端加 `tsc --noEmit` 的独立门槛。

2. **[置信度高] i18n 名存实亡：正式通道 `t()` 只覆盖 11 个文件 185 处，其余 75 个文件 18195 个 CJK 字符硬编码/内联三元** — `desktop/frontend/src/i18n/i18n.tsx:60-63`、`desktop/frontend/src/pages/ToolsPage.tsx:16`、`desktop/frontend/src/pages/SettingsPage.tsx:273`、`desktop/frontend/src/components/shell/CompactNavigation.tsx:35/38/39`、`desktop/frontend/src/components/shell/TitleBar.tsx:28-30/46-50`、`desktop/frontend/src/index.html:27` — 影响：消息目录与内联文案双份维护必漂移；新增第三种语言要改 75 个文件；UI 文案无法集中审计（这也是 `legacy.messages.json` 名字的由来） — 修复方向：把 `text(zh,en)` 内联对机械替换为 `t(key)`，先做"提取清单 + 半自动迁移脚本"，并保留 `text()` 仅为过渡期的 DEV 断言。

3. **[置信度高] 巨型页面组件：单文件 1126/1057/843 行 + 单组件 30 余个 `useState`** — `desktop/frontend/src/pages/RoutingPage.tsx:141-191`（`ruleView`…`replaceBatchConflicts` 连续 30+ state）、`desktop/frontend/src/pages/SettingsPage.tsx:245-281`、`desktop/frontend/src/pages/ConnectionsPage.tsx:173-201` — 影响：任何改动都要在千行文件中定位；渲染粒度粗（每次 keystroke 重建整页，`RoutingPage.tsx:835-836` 注释已承认需要防抖）；测试只能整页渲染（`SettingsPage.test.tsx` 347 行、`ConnectionsPage.test.tsx` 450 行） — 修复方向：按"视图/表单/保存队列"三块拆子组件 + `useReducer` 收敛状态，先把 `RoutingPage` 的批量导入与进程选择器抽成独立模块。

4. **[置信度中] `formatMessage` 丢弃格式说明符且不做数值格式化** — `desktop/frontend/src/i18n/i18n.tsx:29-34`（正则 `/\{(\w+)(?::[^}]+)?\}/` 直接丢弃 `:...`，替换值只 `String(values[name])`）— 证据：`i18n/legacy.messages.json` 中存在 `{value:.2f}` / `{down:.2f}` 类占位符 — 影响：一旦这些键被渲染，字节/速率会显示为未格式化数字（如 `1234567.891234`），且 `locale` 不同无法用 `Intl` 适配 — 修复方向：改为解析 `{name:spec}` 并把 spec 交给 `Intl.NumberFormat`；同时清理 184 个死键（含这 5 个），并把两份 `formatBytes`（`HomePage.tsx:32-37`、`ConnectionsPage.tsx:121-126`）合并为一处。（**已验证：这 5 个键当前零引用，故现在无可见故障**；缺陷是潜伏的。）

5. **[置信度中] `errorCodes.ts` 规则顺序会让页面域错误被误判为通用错误** — `desktop/frontend/src/components/notifications/errorCodes.ts:6-30`：通用 HM-E1001（`timeout`/`超时`）等规则排在 `health:` / `routing:` / `settings:` 前缀规则之前 — 影响：体检或路由页的超时错误会拿到 HM-E1001 而非 HM-E1401/HM-E1301，弱化按域排障与后续统计 — 修复方向：先匹配 `dedupeKey` 前缀（页面域），再退化到关键词规则；或把通用规则降为兜底档。

6. **[置信度中] 轮询点位分散、无集中调度与退避，且后台抑制完全依赖手工 `usePageActive()`** — `desktop/frontend/src/state/useEngineState.ts:25/310/322`（800ms + 两条串行轮询）、`ConnectionsPage.tsx:250`（1500）、`ConnectionsPage.tsx:155`（1000）、`BlockedDomainsPage.tsx:61`（3000）、`HealthPage.tsx:328`（400）、`ToolsPage.tsx:36-50`（5000，自写 setTimeout 链、未走 `startSerialPoll`）、`components/shell/PageActivity.tsx`（唯一开关）；页面访问后常驻挂载见 `components/shell/AppShell.tsx:38-40` — 影响：新增页面若忘记 `usePageActive()`，后台页面会持续请求；节奏不可统一调节；`ToolsPage` 已出现"绕开公共原语"的苗头（重复实现） — 修复方向：把 `startSerialPoll` 与 `usePageActive` 合并成一个 `useActivePoll` hook，并在 `ToolsPage.tsx:36` 收敛自写循环。（**未观察到实际重复请求堆积**：`serialPoll` 是 setTimeout 链，不重入。）

7. **[置信度中] `SettingsPage` 首屏 4 个并发请求未加超时，`Promise.all` 任一挂起会卡住 loading** — `desktop/frontend/src/pages/SettingsPage.tsx:371-378`（`settings.get()` / `configPath()` / `migrationStatus()` / `adapters.list()`）、`:369` `setLoading(true)`、`:394` finally 才 `setLoading(false)`；对比 `ConnectionsPage.tsx:223-227` 用了 `withServiceTimeout(..., 8_000, ...)`（定义在 `platform/services.ts:36-48`） — 影响：后端某个服务不响应时设置页永久停在骨架/空态 — 修复方向：给每个调用套 `withServiceTimeout` 或用 `Promise.allSettled` 并逐项降级。

8. **[置信度中] 测试覆盖偏纯逻辑，UI/边界层存在明确空洞；且三个页面零测试** — 无测试：`pages/AppearanceLab.tsx`(190)、`pages/ToolsPage.tsx`(152)、`pages/BlockedDomainsPage.tsx`(154)、`components/shell/TitleBar.tsx`(94，含 `Events.On` 订阅与 4 个窗口按钮)、`platform/services.ts`(358，**唯一前后端门面**、含 `isDesktopRuntime`/`withServiceTimeout`)、`theme/background.service.ts`(110)、`i18n/i18n.tsx`、`App.tsx`；另有 16 个 `components/**` 文件与 4 个 `pages/**` 文件无对应测试（明细见 §5.3） — 影响：窗口控制、运行环境判定、超时语义、i18n 回退链这些"最易回归"的路径没有护栏；`package.json` 的 `test` 无覆盖率门槛，回归不可度量 — 修复方向：优先补 `platform/services.ts`、`TitleBar.tsx`、`i18n.tsx` 三处（都能纯 jsdom 测），并给 `vitest run --coverage` 设定最低行覆盖阈值。

---

## 8. 优势（证据化）

1. **一套自研的串行化并发原语，且带测试**：`platform/serialPoll.ts`（setTimeout 链防重入）、`platform/latestSaveQueue.ts`（在飞时只留最后一个输入）、`platform/adapterSaveQueue.ts`（全局单例）、`platform/settingsQueue.ts`（按字段所有权串行 + 权威值合并）；对应测试 `platform/serialPoll.test.ts`、`latestSaveQueue.test.ts`、`adapterSaveQueue.test.ts`、**`settingsQueue.test.ts`（195 行，比被测代码 97 行还长）**。写路径全部过队列，读到的是可复用的并发纪律，而不是零散的防抖。
2. **前后端边界干净**：所有 Wails 调用收敛在 `platform/**`（`services.ts:188-201` `appServices`），页面只依赖 `appServices.*`；唯一例外是 `components/shell/TitleBar.tsx:10` 的窗口事件，边界清晰可审计。
3. **浏览器预览独立可用**：`platform/services.ts:50-104` `webPreviewServices` + 页面级 fixture（`ConnectionsPage.tsx:204-205`、`HealthPage.tsx:43/53-88`）+ `?page=`/`?tray=`/`?preset=`/`?mode=` 调试开关（`App.tsx:67-77`、`theme/appearance.store.tsx:57-73`）→ 无 Windows/无 Go 环境也能做 UI 迭代（前提是补上 bindings 空实现，见问题 1）。
4. **重资源加载策略专业**：pixi.js + pixi-live2d-display **运行时动态 import**（`components/ai/skins/Live2DCharacter.tsx:97`）、皮肤解析/导出**每任务独立 Worker + 60s 超时 + 立即 terminate**（`components/ai/skins/packageAsync.ts:4-14`，注释明确"生产不在主线程重试以免重新冻结 UI"）、页面与助手全部 `lazy`（`App.tsx:27-34`、`AppShell.tsx:9`）。
5. **列表与计时器做了真实的性能工程**：`components/VirtualRows.tsx:10/24-28/46-62`（阈值 80、过扫描 300px、`borderBoxSize` 实测行高、rAF 节流、passive scroll、缓存回收）；`ConnectionsPage.tsx:140-159` 把 1s 心跳缩到单个单元格并加可见性暂停；`document.hidden` 检查出现在三个轮询点。
6. **无障碍是对待过的，不是补丁**：`AppShell.tsx:45-48` 跳过链接、`CompactNavigation.tsx:68-78` 自实现键盘漫游 + `aria-current`、`AppNotifications.tsx:214-216` 正确的 `role`/`aria-live`/`aria-atomic`、`app.css:35` 明确"鼠标去 outline、键盘保留 focus-visible"、装饰元素统一 `aria-hidden`（`VirtualRows.tsx:66/68`、`AppNotifications.tsx:230/232`）。全站 `aria-*` 使用集中且语义正确。
7. **通知层设计成熟**：错误原文→可读摘要（`notifications/notificationMessage.ts:9-63`）、稳定错误码（`notifications/errorCodes.ts`）、`dedupeKey` 去重 + **上限 5 条**（`AppNotifications.tsx:95/119`）、成功 3.2s / 提示 4.2s / 错误与带 action 的通知不自动消失（`:112`）、出场动画后才卸载避免塌陷（`:200`）。
8. **样式纪律**：内联 `style={{}}` 仅约 18 处，样式几乎全在 CSS 类与 token 文件（`theme/*.tokens.css` 6 个）；`app.css` 每节都带解释性注释（§6.1 表格），说明"为什么要这样写"（如英文标签更长的预留 `:2183`、策略切片固定两行防跳动 `:1231`）。
9. **纯逻辑抽出率高且配套单测齐全**：`pages/` 下 9 个纯逻辑模块（`connectionSort`/`connectionGroups`/`connectionView`/`connectionNavigation`/`routingBatch`/`routingEffect`/`natDetectionPolicy`/`healthNotice`/`connectionPreview`）中 **8/9 有单测**；`platform/` 5/5 有单测；`components/shell/windowTouchDrag.test.ts`（136 行，比实现 125 行还长）覆盖触摸拖动这一最易退化的交互。

---

## 9. 未验证 / 无法核实（明确列出）

1. **未运行任何构建或测试**：`desktop/frontend/node_modules` 不存在，本机无 pnpm；`npm run test`（vitest）、`npm run build`（tsc + vite）**均未执行**。§5 的"测试通过/覆盖情况"仅基于文件与脚本声明，**实际是否全绿未验证**。
2. **真实绑定 API 形状未验证**：`desktop/frontend/bindings/` 不存在，`platform/services.ts` 中每个 `bindings.X.Y()` 的签名、返回类型、错误形态都**只看到调用点**，未与 Go 侧实现对照。
3. **`app.css` 精确重复选择器统计未做**：只统计了 `!important`(93)、`@media`(25)、`@keyframes`(18)、`data-*` 主题选择器(43)；未做选择器去重/冲突分析。
4. **`legacy.messages.json` 的 184 个死键与 5 个格式说明符键**：带 `{x:.2f}` 的 5 个键（`status_running_live`、`up_format`、`home_up_conn`、`home_card_speed`、`home_row_traffic`）**在 `src/**` 中零引用已实测确认**；但另外 39 个带普通占位符的键中有部分在服役，其 `{value}` 替换是否都传了 `values` 参数未逐个核对（**未验证**）。
5. **运行期性能未测量**：无 bundle 体积基线（`vite.config.ts` 无 `manualChunks`/体积告警）、无 React Profiler 数据、无 Lighthouse/axe 结果。§6.5 的"性能工程"评估均为源码级静态判断。
6. **虚拟化在键盘 Tab 到屏外行时的滚动行为**、以及 `visited` 页面常驻挂载的**实际内存占用**，未实测。
7. **Wails v3（`@wailsio/runtime` 3.0.0-alpha2.119）与 `pixi-live2d-display` 0.4.0 + pixi 6.5.10 的兼容性**未验证；`pnpm-workspace.yaml` 的 overrides（`pixi-live2d-display>gh-pages → ^6.3.0` 等）是否仍生效未验证。
8. **测试环境配置来源未验证**：`vite.config.ts` 无 `test` 配置块，jsdom/环境与 setup 文件（是否存在 `vitest.setup`）未在本次分析中定位；测试如何获得 DOM 与 Fluent Provider 包裹未逐文件核对。
9. **覆盖率数字未验证**：仓库无 `coverage` 配置与阈值，未产出覆盖率报告。
10. 本报告未覆盖 `desktop/frontend/public/**`（`live2d`、`skins`、`support/SignPath` 资源）与 `index.html` 之外的静态资源体积；`desktop/frontend/pnpm-lock.yaml`（261423 B）与 `package.json` 的版本一致性未校验。
