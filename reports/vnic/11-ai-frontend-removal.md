# 11 · 前端 AI 助手 / Live2D 皮肤整体移除报告

- 任务：`task-8`（owner `ai-frontend-remover`，Team Lead `lead`）
- 依据契约：`reports/vnic/00-frozen-interface.md` §0 / §5 / §6.1
- 仓库基线：`main` @ `e66016e`（HypoMux v2.7.0），**未执行任何 `git commit` / `git push`**
- 快照时间：第 1 轮 2026-10-03 16:17（+08:00）；第 2 轮修订 2026-10-03（同日，Lead 授权删除 `public/` 资产后）；终局复跑 2026-10-03（同日，Go 侧任务完成后）
- 工作目录：`<repo>`
- 结论一句话：共删除 **39 个 tracked 路径**（`src/components/ai/` 31 个文件 + `platform/ai.ts` + `platform/companionExport.*` + `state/aiAvailability.ts` + `public/live2d/**` 与 `public/skins/**` 4 个资产）；**终局复跑后，验收 §6.1 的 10 个关键词在整个 `desktop/**` 内只剩 3 个依赖声明文件**（`desktop/frontend/package.json`、`pnpm-workspace.yaml`、`pnpm-lock.yaml`，按 Lead 追加硬约束原样保留），`desktop/frontend/src/**` 与 Go 侧均已零命中；5 个 `hypomux:ai-*` 事件与全部悬挂导入已清零。**全部结论为逐行静态自查，未编译验证。**

---

## ① 删除与修改清单（带路径与行号）

### 1.1 删除的文件（共 39 个 tracked 路径）

| # | 路径 | 说明 |
|---|------|------|
| 1 | `desktop/frontend/src/components/ai/`（目录，31 个 tracked 文件） | AI 助手 + 皮肤/衣柜全部实现 |
| 2 | `desktop/frontend/src/platform/ai.ts` | AI 服务前端封装 |
| 3 | `desktop/frontend/src/platform/companionExport.ts` | 精灵导出 |
| 4 | `desktop/frontend/src/platform/companionExport.test.ts` | 其测试 |
| 5 | `desktop/frontend/src/state/aiAvailability.ts` | `useAIEnabled` / `publishAIAvailability` / `AI_AVAILABILITY_EVENT` |
| 6 | `desktop/frontend/public/live2d/live2dcubismcore.min.js`（207,163 B） | Live2D Cubism Core 运行库 —— **第 2 轮，Lead 授权后删除** |
| 7 | `desktop/frontend/public/live2d/NOTICE.md`（1,109 B） | Live2D 归属说明 —— **第 2 轮，Lead 授权后删除** |
| 8 | `desktop/frontend/public/skins/mux-starter.muxskin`（2,177 B） | 入门皮肤包 —— **第 2 轮，Lead 授权后删除** |
| 9 | `desktop/frontend/public/skins/SKIN_SPEC.md`（11,608 B） | `.muxskin` / 皮肤规范 —— **第 2 轮，Lead 授权后删除** |

`components/ai/` 下被删除的 31 个文件（`git status` 权威计数）：

- 根目录 9 个：`AIAssistant.tsx`、`AIAssistant.test.tsx`、`AssistantCompanion.tsx`、`AssistantCompanion.test.tsx`、`AssistantCompanionLoading.test.tsx`、`AssistantMessage.tsx`、`AssistantMessage.test.tsx`、`assistant.css`、`pageContext.ts`
- `components/ai/skins/` 22 个：`DefaultCharacter.tsx`、`LayeredCharacter.tsx`(+`.test.tsx`)、`Live2DCharacter.tsx`(+`.test.tsx`)、`SkinCharacter.tsx`(+`.test.tsx`)、`SkinWardrobe.tsx`(+`.test.tsx`)、`layeredFixture.ts`、`layeredPackage.ts`(+`.test.ts`)、`live2dPackage.ts`(+`.test.ts`)、`package.ts`(+`.test.ts`)、`packageAsync.ts`(+`.test.ts`)、`package.worker.ts`、`skins.css`、`store.ts`(+`.test.tsx`)

删除方式：直接 `Remove-Item`（契约允许），删除后逐个 `Test-Path` 复核全部为 `exists=False`。

**第 2 轮删除（`desktop/frontend/public/` 资产，Lead 于 team-message-7663af03 授权）**：按要求先做全仓消费者扫描（命令与原始结果见 **§2.6**），确认无任何 `.ts/.tsx/.go/.json/.html/.yml/.nsi/.md` 消费者、无构建/安装器/CI 引用后才执行删除。删除后：

- `desktop/frontend/public/` 仅剩 `support/`（`SignPath/SignPath.png`、`icon.ico`、`wei.png`、`zhi.jpg`，均为非 AI 资源，未动）。
- `desktop/frontend/public/live2d/`、`desktop/frontend/public/skins/` 两个目录本身也一并移除（`exists=False`）。
- `git status --short -- desktop/frontend/public` 现为 4 条 `D`。
- **许可证说明**：`public/live2d/NOTICE.md` 是 Live2D Cubism SDK 的归属声明；运行库与其声明现已同时删除，本产品不再分发任何 Live2D 二进制，因此不再需要该归属声明。若日后重新引入 Live2D，必须同时恢复两者。

### 1.2 修改的源文件

| 文件（当前行号） | 修改内容 |
|---|---|
| `desktop/frontend/src/App.tsx` L69-77 | DEV `?page=` 白名单删除 `requested === "assistant" \|\|`（剩余白名单仍是 8 个真实页面） |
| `desktop/frontend/src/App.tsx` L85-95 | `pageOrder: AppPage[]` 删除 `"assistant",` 元素 |
| `desktop/frontend/src/components/shell/AppShell.tsx` L1 | import 去掉 `lazy` / `Suspense` / `useCallback` 及 `../../state/aiAvailability`、`../ai/AIAssistant` |
| 同上 L34-37 | skip link 目标固定为 `document.getElementById("page-content")`（原为 `aiEnabled && page === "assistant" ? "ai-workspace" : "page-content"`，`#ai-workspace` 已不存在） |
| 同上 L39 | `<CompactNavigation page onPageChange />`，不再传 `aiEnabled` |
| 同上 L40 | `#page-content` 去掉 `hidden={aiEnabled === true && page === "assistant"}` |
| 同上 L50-62 | 页面列表过滤去掉 `item !== "assistant"` / `page !== "assistant"`；删除文末 `{aiEnabled && <Suspense fallback={null}><AIAssistant …/></Suspense>}` |
| 同上（整体） | 删除 `const AIAssistant = lazy(...)`、`assistantOpen` state、`useAIEnabled()` 调用、`aiEnabled !== false` 时关闭助手并 `onPageChange("home")` 的 effect、`openWorkspace` useCallback。文件 78 → 66 行 |
| `desktop/frontend/src/components/shell/AppShell.test.tsx` L1-13 | 去掉 `state/aiAvailability`、`../ai/AIAssistant`、`../platform/services` 的 import 与 mock；`CompactNavigation` mock 不再读取 `aiEnabled` |
| 同上 L23-40 | 保留「按需挂载 + 保留草稿/滚动/DOM 身份」测试原样 |
| 同上 L42-46 | 新增 `"moves the skip link onto the page viewport"`（断言 `document.activeElement === #page-content`） |
| 同上（整体） | 删除 4 个 AI 测试。文件 89 → 46 行 |
| `desktop/frontend/src/components/shell/CompactNavigation.tsx` L2-12 | 删除仅 assistant 使用的 `Chat24Regular` import |
| 同上 L16 | `AppPage` 联合类型删除 `"assistant" \|` |
| 同上 L18-24 | 组件签名去掉 `aiEnabled = true` 与 `aiEnabled?: boolean` |
| 同上 L30-37 | `mainItems` 删除 `{ id: "assistant", label: … "AI 助手" …, icon: <Chat24Regular /> }` |
| 同上 L39-61 | `useLayoutEffect` 依赖数组 `[navigationPage, aiEnabled]` → `[navigationPage]` |
| 同上 L82-104 | 删除 `mainItems.filter(item => item.id !== "assistant" \|\| aiEnabled)`，恢复 `mainItems.map(...)`。文件 138 → 134 行 |
| `desktop/frontend/src/components/shell/CompactNavigation.test.tsx` L10-19 | 重写为单个测试：断言无 `"AI assistant"` 按钮，且 `nav_home`/`nav_routing`/`nav_tools`/`Connections`/`Toolbox`/`nav_settings` 六个入口齐全 |
| `desktop/frontend/src/pages/SettingsPage.tsx` L29 / L37（原） | 删除 `import { savePreferences, useSkins } from "../components/ai/skins/store";` 与 `import { publishAIAvailability } from "../state/aiAvailability";` |
| 同上 L43-70 | `emptySettings: CompleteAppSettings` 删除 `ai_enabled: true,`（该字段在门面类型中可选，字面量仍合法） |
| 同上 L242-262 | 删除 `savingCompanion` state 与 `useSkins(!loading && !loadFailed && settings.ai_enabled !== false)` 调用（连带 `companionPreferences` / `companionLoaded` 解构） |
| 同上 L360-390 | 删除 `hypomux:ai-changed` → `setLoadRevision` 的监听 effect；load effect deps 仍为 `[loadRevision, pageActive, saveQueue]`（`setLoadRevision` 仍由 L588 Retry 按钮使用） |
| 同上 L392-414 | `save()` 内删除 `publishAIAvailability(persisted.ai_enabled !== false)` |
| 同上 L546-567 | 迁移分支删除 `publishAIAvailability(restored.ai_enabled !== false)` / `publishAIAvailability(next.ai_enabled !== false)`（原 L410/L416/L557） |
| 同上 L580-589 | 头部 `settings-save-feedback` 删除 `AI 助手设置` 按钮（原 L596，dispatch `hypomux:ai-settings`） |
| 同上 L613-624 | `settings-personalization` 段删除「启用 AI 功能」`SettingRow`（原 L631-635）与「显示 AI 小精灵」`SettingRow`（原 L636-645）；该段现以 `settings_theme` 开头。文件 1065 行 |
| `desktop/frontend/src/pages/SettingsPage.test.tsx` L3 / L9（原） | 去掉 `act` 与 `AI_AVAILABILITY_EVENT` 的 import |
| 同上 L78-85 | 新增负向断言：英文界面下不存在 `AI assistant settings` 按钮 / `Enable AI features` 开关 / `Show AI companion` 开关 |
| 同上 L87-94 | 新增负向断言：中文界面（`mocks.locale = "zh"`）下不存在 `AI 助手设置` / `启用 AI 功能` / `显示 AI 小精灵` |
| 同上 L278-294 | 原名「preserves an AI rule saved after loading the settings page」→「preserves a routing rule written elsewhere after loading the settings page」，注释改为「A field-scoped save must never replay a full payload over the store.」 |
| 同上 L296-304 | 原「refreshes Steam preferences when returning from AI and while already visible」删掉 `hypomux:ai-changed` 分支，改名「refreshes Steam preferences when the toolbox becomes active again」 |
| 同上（整体） | 删除 2 个纯 AI 测试（AI 开关持久化/发布时序、中文 AI 开关）与 2 个被 `hypomux:ai-changed` 驱动的测试（网络草稿刷新、旧响应竞态）。文件 369 → 305 行；`act(` 引用归零 |
| `desktop/frontend/src/pages/RoutingPage.tsx` | 删除原 L162-167（派发 `hypomux:ai-selection` 的 effect）与原 L309-319（监听 `hypomux:ai-changed` → `notify("AI 已更新配置"/"AI updated the configuration")`）。现 L295-301 为 `load/pageActive` effect |
| `desktop/frontend/src/pages/ToolsPage.tsx` | 删除原 L52-56（监听 `hypomux:ai-changed` → `setRevision(v => v+1)`）。`revision` 仍被 L22 state、L50/L61 deps、L73/L92/L136 使用 |
| `desktop/frontend/src/state/useEngineState.ts` | 删除原 `hypomux:ai-changed` → `if (!operationActive.current && !adapterSaveQueue.isPending()) void load();` 的 effect。现存 L343-350（`SYSTEM_PROXY_TAKEOVER_EVENT`）与 L352-359（`ADAPTER_VISIBILITY_EVENT`）均为非 AI 事件；`operationActive`、`adapterSaveQueue` 在 L12/L183/L219/L307-326/L382-396/L457-520 仍有使用 |
| `desktop/frontend/src/app.css` L112-117 | `user-select: text` 选择器列表删除 `.ai-message, .ai-entry pre, .ai-markdown`（AI 助手专用，删除后 `src` 内无任何引用者） |
| `desktop/frontend/src/pages/ConnectionsPage.tsx` L411-412 | 仅注释：`// … commit time: AI/MCP or another editor …` → `// … commit time: another editor …`（无行为变更） |
| `desktop/frontend/src/pages/HealthPage.test.tsx` L29 | 仅注释：`// Home or AI can change mode/strategy …` → `// Home can change mode/strategy …` |

### 1.3 明确保留（未做任何删除）

`desktop/frontend/src/theme/**`、`desktop/frontend/src/components/appearance/**`（含 `AppearancePreview.tsx`）、`desktop/frontend/src/components/material/WallpaperLayer.tsx`、`desktop/frontend/src/pages/AppearanceLab.tsx`、`desktop/frontend/src/state/adapterRuntime.ts` 等 appearance 相关 state、`desktop/frontend/src/components/shell/**`（仅去掉 assistant 入口）、`desktop/frontend/src/platform/services.ts`。

### 1.4 明确未修改（Lead 追加硬约束）

`desktop/frontend/package.json`、`desktop/frontend/pnpm-workspace.yaml`、`desktop/frontend/pnpm-lock.yaml` —— 见 §③。

---

## ② 残留扫描命令与结果

### 2.1 验收 §6.1 关键词全仓扫描

命令（在仓库根执行，`git grep` 覆盖全部 tracked 文件）：

```
git grep -I -c -E 'ai_enabled|AIService|aiService|AIAssistant|pixi-live2d-display|pixi\.js|hypomux:ai-|companionExport|aiAvailability' -- .
```

结果分为两次快照。

**首轮（第 1 轮结束时 2026-10-03 16:17，删除 `public/` 资产之前）**：

```
desktop/frontend/package.json:2
desktop/frontend/pnpm-lock.yaml:7
desktop/frontend/pnpm-workspace.yaml:1
desktop/frontend/public/live2d/NOTICE.md:1
desktop/frontend/src/platform/services.ts:2
desktop/internal/platform/wails/companion_export.go:2
docs/validation/release-readiness-fixes-2026-09-27.md:1
docs/validation/release-review-2026-09-26.md:1
docs/validation/release-review-2026-09-27.md:1
```

**复跑（同日，删除 `public/` 资产之后）**：

```
desktop/frontend/package.json:2
desktop/frontend/pnpm-lock.yaml:7
desktop/frontend/pnpm-workspace.yaml:1
desktop/internal/platform/wails/companion_export.go:2
docs/validation/release-readiness-fixes-2026-09-27.md:1
docs/validation/release-review-2026-09-26.md:1
docs/validation/release-review-2026-09-27.md:1
```

变化：`desktop/frontend/public/live2d/NOTICE.md` 与 `desktop/frontend/src/platform/services.ts` 两条命中消失。

**终局快照（同日，Go 侧 AI 移除任务完成之后再次复跑）**：

```
desktop/frontend/package.json:2
desktop/frontend/pnpm-lock.yaml:7
desktop/frontend/pnpm-workspace.yaml:1
docs/validation/release-readiness-fixes-2026-09-27.md:1
docs/validation/release-review-2026-09-26.md:1
docs/validation/release-review-2026-09-27.md:1
```

变化：`desktop/internal/platform/wails/companion_export.go:2` 一条命中消失 —— Go 侧成员已删除 `companion_export.go` 与 `companion_export_test.go`（`Get-ChildItem desktop/internal/platform/wails -Filter 'companion*' -File` 无输出）。至此**验收 §6.1 的 10 个关键词在 `desktop/**` 内只剩 Lead 硬约束要求保留的 3 个依赖声明文件**（`package.json`、`pnpm-lock.yaml`、`pnpm-workspace.yaml`），其余命中全部落在契约 §6.1 明确排除的 `docs/**`。

同一命令收窄到前端：

```
git grep -n -I -E 'ai_enabled|AIService|aiService|AIAssistant|pixi-live2d-display|pixi\.js|hypomux:ai-|companionExport|aiAvailability' -- desktop/frontend/src
```

```
（无输出）
```

⇒ **`desktop/frontend/src/**` 现为完全零命中。** 首轮此处仅剩 `desktop/frontend/src/platform/services.ts:75-76` 的 `Omit<AppSettings, "ai_enabled">` / `ai_enabled?: boolean` 兼容位（禁改白名单例外）；Lead 已指派 task-10 owner `frontend-vnic-dev` 在同一轮修改中清除，**现已确认清除**，当前该文件为：

```
desktop/frontend/src/platform/services.ts:75: export type CompleteAppSettings = AppSettings & {
```

（`Omit<…>` 与 `ai_enabled?: boolean` 均已不存在；同文件 L350 新增 `virtualAdapter:` 分组。我全程未触碰该文件。）

逐条定性：

| 命中 | 定性 |
|---|---|
| `desktop/frontend/package.json:18-19`、`pnpm-workspace.yaml:4`、`pnpm-lock.yaml` | Lead 追加硬约束要求原样保留，见 §③ |
| `desktop/internal/platform/wails/companion_export.go:14`（+ `companion_export_test.go`） | Go 侧，非本人范围。已确认该包外**无任何调用者**（`git grep -n -I -E 'companionExport\|CompanionExport\|decodeCompanionExport' -- desktop ':!desktop/internal/platform/wails'` 无输出）⇒ 前端 AI/皮肤移除后它成为**死端点**，见 §④ B-3 |
| `desktop/frontend/public/live2d/NOTICE.md` | **已按 Lead 授权删除**（第 2 轮），故复跑后不再命中 |
| `docs/validation/*.md` | 契约 §6.1 明确排除 `docs/**` 与 git 历史 |

### 2.2 悬挂导入与事件残留定向扫描

```
git grep -n -I -E 'platform/ai|state/aiAvailability|from "\./ai"|\.\./ai/|ai-workspace|\.ai-|"assistant"|assistantOpen|useAIEnabled|AIAvailability|AI_AVAILABILITY|Chat24Regular' -- desktop/frontend/src
```

结果仅 2 条，均为本人新增的**负向断言字符串**，非引用：

```
desktop/frontend/src/components/shell/CompactNavigation.test.tsx:12:  expect(screen.queryByRole("button", { name: "AI assistant" })).toBeNull();
desktop/frontend/src/pages/SettingsPage.test.tsx:81:  expect(screen.queryByRole("button", { name: "AI assistant settings" })).toBeNull();
```

→ 已删除模块的导入点、`#ai-workspace` 元素、`.ai-*` CSS 类、`AppPage` 的 `"assistant"` 成员、`useAIEnabled` / `publishAIAvailability` / `AI_AVAILABILITY_EVENT` / `Chat24Regular` 全部清零。

### 2.3 5 个 window 事件清零复核

```
git grep -n -I 'hypomux:' -- desktop/frontend/src
```

```
desktop/frontend/src/theme/appearance.presets.ts:5:  hypomux: "#1677D2",
desktop/frontend/src/state/adapterVisibility.ts:3: export const ADAPTER_VISIBILITY_EVENT = "hypomux:adapter-visibility-changed";
desktop/frontend/src/state/systemProxyTakeover.ts:1: export const SYSTEM_PROXY_TAKEOVER_EVENT = "hypomux:system-proxy-takeover-changed";
```

`hypomux:ai-changed`、`hypomux:ai-selection`、`hypomux:ai-settings`、`hypomux:ask-ai`、`hypomux:ai-availability-changed` 五个事件在 `src` 内 0 处派发、0 处监听。剩余三处 `hypomux:` 分别为颜色 token 值与两个非 AI 事件（虚拟网卡可见性、系统代理接管），不受影响。

### 2.4 Live2D / 皮肤标识残留

```
git grep -n -I -E 'live2d|Live2D|skin|Skin|wardrobe|Wardrobe|companion|Companion' -- desktop/frontend/src
```

仅剩 2 条，均非契约关键词且属必须保留文件：

- `desktop/frontend/src/theme/material.tokens.css:120`：CSS 注释 `/* Companion-inspired wallpapers: CSS-only, … */`（`theme/**` 为契约「保留」清单成员，注释文字本身不在 §6.1 关键词表内，**未修改**）
- `desktop/frontend/src/components/HotspotPanel.tsx:189`、`HotspotPanel.test.tsx:229`：英文单词 `characters` / `character count`，与 AI 无关

### 2.5 残留收口核对

- `git status --short -- desktop/frontend` 中 `components/ai/**`、`platform/ai.ts`、`platform/companionExport.*`、`state/aiAvailability.ts` 均为 `D`（已删除），无 `??` 残留 AI 文件。
- 删除后 `src` 内已无任何文件 import 上述 5 个已删路径。

### 2.6 第 2 轮删除前的消费者扫描证据（`public/live2d/**`、`public/skins/**`）

Lead 要求的命令为：

```
grep -rn "live2d\|Live2D\|muxskin\|SKIN_SPEC\|/skins/\|live2dcubismcore" --include=*.ts --include=*.tsx --include=*.go --include=*.json --include=*.html --include=*.yml --include=*.nsi --include=*.md .
```

本机为 Windows / PowerShell，无独立 `grep` 可执行文件，因此用**等价的 `git grep` 命令族**执行（`git grep` 只遍历 tracked 文件，天然排除 `.git/` 与未跟踪的 `reports/`，正好符合「排除 reports/、node_modules、.git」的要求）。以下为实际执行的命令与原始输出。

**命令 A —— 全仓 tracked 文件，排除被搜文件自身（否则会把 207 KB 的 min.js 正文全部打印出来）**

```
git grep -n -I -iE 'live2d|muxskin|skin_spec|live2dcubismcore|cubism' -- . ':(exclude)desktop/frontend/public/live2d/live2dcubismcore.min.js' ':(exclude)docs' ':(exclude)reports'
```

原始输出（去除重复行后）：

```
.github/release-notes/v2.7.0.en.md:21: ... export `.muxskin`/ZIP packages ...
.github/release-notes/v2.7.0.en.md:22: ... Cubism 3/4 Live2D models ...
.github/release-notes/v2.7.0.md:21: ... `.muxskin`／ZIP 皮肤 ...
.github/release-notes/v2.7.0.md:22: ... Cubism 3/4 Live2D 模型 ...
.gitignore:71:*.muxskin
desktop/frontend/package.json:18:    "pixi-live2d-display": "0.4.0",
desktop/frontend/pnpm-lock.yaml:10:  pixi-live2d-display>gh-pages: ^6.3.0
desktop/frontend/pnpm-lock.yaml:28 / 1875 / 4783: pixi-live2d-display ...
desktop/frontend/pnpm-workspace.yaml:4:  "pixi-live2d-display>gh-pages": "^6.3.0"
desktop/frontend/public/live2d/NOTICE.md:1..18   （被删文件自身）
desktop/frontend/public/skins/SKIN_SPEC.md:12,17,80,82,87,97,101,108,114,120,122,172   （被删文件自身）
desktop/internal/platform/wails/companion_export.go:14:var companionExportName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,100}\.(muxskin|md)$`)
desktop/internal/platform/wails/companion_export_test.go:12,17,20,23,30: ...muxskin / "SKIN_SPEC.md"...
desktop/scripts/create-layered-skin.py:63,65: OUT/'mux-layered.muxskin'
```

**命令 B —— 构建 / 安装器 / CI / 配置专项（Lead 点名要确认的部分）**

```
git grep -n -I -E 'public/live2d|public/skins|live2dcubismcore|NOTICE\.md|mux-starter|muxskin' -- 'desktop/build' 'desktop/Taskfile.yml' 'desktop/frontend/index.html' 'desktop/frontend/vite.config.ts' '.github' 'protocol' 'desktop/scripts' 'desktop/cmd'
```

原始输出：

```
.github/release-notes/v2.7.0.en.md:21: ... `.muxskin`/ZIP ...
.github/release-notes/v2.7.0.md:21: ... `.muxskin`／ZIP ...
desktop/scripts/create-layered-skin.py:5:OUT = Path(__file__).resolve().parents[1] / 'frontend/public/skins'
desktop/scripts/create-layered-skin.py:63:with zipfile.ZipFile(OUT/'mux-layered.muxskin','w',zipfile.ZIP_DEFLATED) as archive:
desktop/scripts/create-layered-skin.py:65:print(OUT/'mux-layered.muxskin')
```

⇒ **`desktop/build/**`（含 `desktop/build/windows/nsis/*.nsi`、`project.nsi`、`Taskfile.yml`）、`desktop/frontend/index.html`、`desktop/frontend/vite.config.ts`、`protocol/**`、`desktop/cmd/**` 全部零命中** —— 没有任何安装器/NSIS/构建脚本把这些文件当安装包资源引用。`public/` 由 Vite 整体复制进产物，无显式清单，删除即从产物中消失。

**命令 C —— 删除后复跑：全仓是否仍引用已删路径**

```
git grep -n -I -E 'public/live2d|public/skins|live2dcubismcore|mux-starter|SKIN_SPEC' -- ':!docs' ':!reports' ':!*create-layered-skin.py'
```

原始输出：

```
.gitignore:72:/desktop/frontend/public/skins/mux-layered-source/
desktop/internal/platform/wails/companion_export_test.go:20:	if _, err := decodeCompanionExport("SKIN_SPEC.md", base64.StdEncoding.EncodeToString(make([]byte, 65537))); err == nil {
```

> 后续复跑变化（同日，Go 侧任务完成之后）：`desktop/internal/platform/wails/companion_export_test.go` 这一条也已消失（该文件连同 `companion_export.go` 被 Go 侧成员删除），此时命令 C 只剩 `.gitignore:72` 一条。见 §2.1 终局快照。

**逐条判定（是否有「真实消费者」）**

| 命中 | 是否消费者 | 判定 |
|---|---|---|
| `desktop/build/**`、NSIS、`index.html`、`vite.config.ts`、CI、`protocol/` | — | **零命中**，无引用 |
| `desktop/scripts/create-layered-skin.py:5,6,7,63,65` | **否（生产者）** | L5 `OUT = …/frontend/public/skins`、L6 `SOURCE = OUT/'mux-layered-source'`、L7 `SOURCE.mkdir(parents=True, exist_ok=True)`、L63 写 `OUT/'mux-layered.muxskin'`。它**只写不读**，不读取 `mux-starter.muxskin` 或 `SKIN_SPEC.md`，且会自建目录 ⇒ 删除不影响；其产物 `mux-layered.muxskin` 与 `mux-layered-source/` 均被 `.gitignore:71-72` 忽略。该脚本也**不被 build/CI 引用**（`git grep -n -I 'create-layered-skin' -- . ':!docs' ':!reports'` 无输出） |
| `desktop/internal/platform/wails/companion_export.go:14`、`companion_export_test.go` | **否** | 正则 `\.(muxskin\|md)$` 是导出接口的**文件名白名单**；测试里的 `"SKIN_SPEC.md"` 只是传给 `decodeCompanionExport(name, data)` 的**字符串参数**（用于 65537 B 超限测试），不读磁盘。与 `public/` 资源无引用关系（Go 残留另见 §④ B-3） |
| `.gitignore:71` `*.muxskin`、`.gitignore:72` `/desktop/frontend/public/skins/mux-layered-source/` | **否** | 删除后成为**陈旧忽略规则**（L72 指向已删目录），无害。`.gitignore` 不在我的写范围，留给 Lead / CI 任务清理 |
| `.github/release-notes/v2.7.0.md:21-22`、`v2.7.0.en.md:21-22` | **否** | v2.7.0 的**历史发布说明**，描述当时已发布的功能，不是消费者。我按「禁改 `.github/**`」未触碰；是否回改历史发布说明由 Lead 决定（见 §④ B-4） |
| `desktop/frontend/package.json`、`pnpm-workspace.yaml`、`pnpm-lock.yaml` | **否** | 依赖声明，按 Lead 硬约束保留，见 §③ |
| `docs/**`、`reports/**` | — | 按契约 §6.1 排除 |

**结论：4 个资产文件（`live2dcubismcore.min.js`、`NOTICE.md`、`mux-starter.muxskin`、`SKIN_SPEC.md`）没有任何真实消费者，无需保留任何一个，已全部删除。** 4 个文件删除前均为 **tracked**（`git ls-files desktop/frontend/public` 列出全部 4 个；`git check-ignore` 对它们无输出，因为已跟踪文件不受 `.gitignore:71` 的 `*.muxskin` 影响），因此删除在 `git status` 中表现为 4 条 `D`，**不会被 `--frozen-lockfile` 或任何构建步骤引用**。

---

## ③ pnpm-lockfile 结论

**结论：锁文件一致性没有被破坏；CI 不会因本任务失败在 `--frozen-lockfile` 上。**

Lead 追加硬约束（优先级高于原任务书「清理 pixi 依赖」）已遵守：

- `.github/workflows/build.yml:137`：`pnpm --dir desktop/frontend install --frozen-lockfile` —— 该步骤只在 `package.json` 与 `pnpm-lock.yaml` 不一致时失败。
- 本任务**未修改** `desktop/frontend/package.json`、`desktop/frontend/pnpm-workspace.yaml`、`desktop/frontend/pnpm-lock.yaml`（`git status --short -- desktop/frontend` 中三者均无记录）。
- 因此 `pixi.js: 6.5.10`（`package.json:19`）与 `pixi-live2d-display: 0.4.0`（`package.json:18`）仍被声明，`pnpm-workspace.yaml:4` 的 `"pixi-live2d-display>gh-pages": "^6.3.0"` override 仍生效，锁文件仍与清单严格一致。

依赖现状说明：

| 依赖 | 现状 | 处理 |
|---|---|---|
| `pixi.js` | 已声明、**无任何 `src` 引用**（唯一使用点 `components/ai/skins/live2dPackage.ts` 已删除） | 保留声明；Vite/Rollup 无引入边 ⇒ 不进产物 |
| `pixi-live2d-display` | 同上 | 同上 |
| `gh-pages`（pixi-live2d-display 的传递 override） | 同上 | 同上 |
| `fflate` | 已声明、**无任何 `src` 引用**（唯一使用点 `platform/companionExport.ts` 已删除） | 保留声明；同属「待清理」清单 |
| `react-markdown` / `remark-gfm` | **仍在使用**：`desktop/frontend/src/pages/AboutPage.tsx:18-19` | 必须保留，不可清理 |

后续跟进（需要在有 pnpm 的环境执行，本地无 pnpm 无法重新生成锁文件）：

1. 在具备 pnpm 的环境执行 `pnpm --dir desktop/frontend install`，重新生成 `pnpm-lock.yaml`；
2. 从 `package.json` 删除 `pixi.js`、`pixi-live2d-display`、**`fflate`** —— 三者均已「已声明但无任何 `src` 引用」；Lead（team-message-7663af03）已确认 `fflate` 与 `pixi*` 同属该清单，等有 pnpm 的环境重生成锁文件后一并移除；
3. 同步删除 `pnpm-workspace.yaml:4` 的 `"pixi-live2d-display>gh-pages"` override；
4. 再执行一次 `pnpm --dir desktop/frontend install` 使锁文件与清单重新一致，最后跑一遍 `.github/workflows/build.yml:146` 的 vitest 与 `:150` 的 `tsc && vite build`。

---

## ④ 未验证项（**全部未编译验证**）

> 本机环境：无 Go、无 pnpm、无 `node_modules`、无 `desktop/frontend/bindings/`。因此 `tsc`、`vitest`、`vite build`、`wails3 generate bindings` 均**一次都没有运行**。

**A. 完全未验证（环境缺失）**

1. **未运行 `tsc`（未编译验证）**：`pnpm --dir desktop/frontend build` = `tsc && vite build` 未执行。`desktop/frontend/tsconfig.json` 开启了 `"noUnusedLocals": true`，因此以下静态自查结论**未经编译器确认**：
   - 已逐文件核对被修改文件的 import 与局部变量仍有使用点：`AppShell.tsx`（删掉 `lazy`/`Suspense`/`useCallback` 后无其它使用者）、`CompactNavigation.tsx`（`t` 仍用于 L31/L36/L120/L124）、`SettingsPage.tsx`（`Switch` L15↔L148、`SettingSwitch` L137↔9 处、`setLoadRevision`↔L588、`text`/`notify`/`patchAndSave` 多处）、`AppShell.test.tsx`、`CompactNavigation.test.tsx`、`SettingsPage.test.tsx`（`act` 已从 import 移除且文件内 `act(` 归零；`ToolsPage`/`PageActivity` 仍被 L296 测试使用）。
   - `RoutingPage.tsx`、`ToolsPage.tsx`、`useEngineState.ts` 删除 effect 后未引入未使用变量。
   - 仍无法排除：`AppPage`/`CompleteAppSettings` 等类型在未生成的 `bindings/` 下的真实形状差异。
2. **未运行 `vitest`（未编译验证）**：`SettingsPage.test.tsx`（305 行，8 个 `it` + 3 个 `describe`）、`AppShell.test.tsx`（2 个 `it`）、`CompactNavigation.test.tsx`（1 个 `it`）均只做静态阅读，未执行。特别地：
   - 新增负向断言依赖 `@testing-library/react` 的角色查询语义（`queryByRole("switch"/"button", { name })`、`queryByText`），未实测；
   - 删除的 5 个事件驱动测试原本覆盖「设置页刷新 `hypomux:ai-changed`」与「旧响应竞态」路径，该路径随监听器一并删除 —— 未验证是否有其它测试隐式依赖同一行为。
3. **未运行 `vite build` / 未验证产物**：无法确认 tree-shaking 后产物中不含 pixi 代码，也无法给出产物体积对比。第 2 轮删除 4 个 `public/` 资产后，产物体积应减少约 222 KB，但**该数字是「被删文件的字节数之和」，不是实测的产物体积差**（`public/` 由 Vite 整体复制，未实测）。
4. **未在浏览器中实测 `public/` 资产删除后的运行时行为**：`live2dcubismcore.min.js`（Live2D Cubism Core 运行库）、`mux-starter.muxskin`、`SKIN_SPEC.md` 已于第 2 轮删除。源码侧经 grep 确认已无任何加载/读取点（`components/ai/skins/**` 整体删除，见 §2.6），因此不存在可暴露的 404/加载失败路径；但**未启动 dev server 或打包产物做端到端验证**。若日后重新引入 Live2D 或皮肤包，必须同时恢复对应文件与 `public/live2d/NOTICE.md`（见 §1.1 许可证说明）。

**B. 范围外 / 待 Lead 裁决项**

1. **`desktop/frontend/public/**` 的 Live2D 与皮肤静态资源：已按 Lead 授权（team-message-7663af03）于第 2 轮全部删除，无残留。**
   - 删除前按要求做了全仓消费者扫描（命令与原始输出见 **§2.6**），确认**无任何真实消费者**：`desktop/build/**`（含 NSIS `.nsi` / `project.nsi` / `Taskfile.yml`）、`desktop/frontend/index.html`、`desktop/frontend/vite.config.ts`、`.github/workflows`、`protocol/**`、`desktop/cmd/**`、`src/**` 全部零命中；`desktop/scripts/create-layered-skin.py` 是**生产者**（只写不读、自建目录、不被 build/CI 引用），`companion_export_test.go:20` 的 `"SKIN_SPEC.md"` 只是传给 `decodeCompanionExport(name, data)` 的字符串参数。
   - 已删除 4 个 **tracked** 文件：`public/live2d/live2dcubismcore.min.js`（207,163 B）、`public/live2d/NOTICE.md`（1,109 B）、`public/skins/mux-starter.muxskin`（2,177 B）、`public/skins/SKIN_SPEC.md`（11,608 B），共约 222 KB；`public/live2d/`、`public/skins/` 目录本身亦移除（`exists=False`）。`desktop/frontend/public/` 现仅剩 `support/**`（非 AI 资源）。
   - 遗留项（**不在我的写范围，需 Lead 指派**）：`.gitignore:71` 的 `*.muxskin` 与 `.gitignore:72` 的 `/desktop/frontend/public/skins/mux-layered-source/` 现为指向已删目录的**陈旧忽略规则**（无害）。
2. **并发写入提示**：本次扫描期间，`desktop/frontend/src/platform/services.ts`、`desktop/frontend/src/pages/HomePage.tsx` 已处于 `M`（被其他成员修改），并新增了未跟踪目录 `desktop/frontend/src/components/vnic/{VirtualAdapterPanel.tsx,VirtualAdapterPanel.test.tsx}`。`HomePage.tsx` 与 `services.ts` 都是我的禁改文件，我未触碰；但本报告的行号是 2026-10-03 16:17 的**快照**，其他成员继续写入 `src/**` 后，行号与本报告 2.1 的 grep 结果可能漂移，最终以 Lead 在冻结版本上的重跑为准。
3. **Go 侧命中：已由 Go 侧成员处理完毕（最新证据）**：`desktop/internal/platform/wails/companion_export.go` 与 `companion_export_test.go` 现已不存在（`Get-ChildItem desktop/internal/platform/wails -Filter 'companion*' -File` 无输出；§2.1 终局快照中该文件原有的 2 条命中已消失）。此前我已确认该包外零调用者（`git grep -n -I -E 'companionExport|CompanionExport|decodeCompanionExport' -- desktop ':!desktop/internal/platform/wails'` 无输出），因此前端 `platform/companionExport.ts` 与全部皮肤导出 UI 删除后它本就是**死端点**。**该 Go 侧删除不在我的写范围，我未参与、也未对其做任何验证。**
4. **`.github/release-notes/v2.7.0.md:21-22` 与 `v2.7.0.en.md:21-22`（历史发布说明）**：正文提到 `.muxskin`／ZIP 皮肤与 Cubism 3/4 Live2D 模型。我按「禁改 `.github/**`」未触碰。**待 Lead 决定**：发布说明是 v2.7.0 已发布版本的历史记录，通常不应回改；若要彻底去词化需由 Lead 或 ci-runner 任务处理。

**C. 已知的行为变更（非缺陷，但需 CI 验证）**

1. `AppShell` 的 skip link 在「助手工作区打开」时原会聚焦 `#ai-workspace`；现在始终聚焦 `#page-content`。已补测试 `AppShell.test.tsx:42-46`。
2. 设置页不再因 `hypomux:ai-changed` 重新拉取配置；页面切换（`pageActive`）与 `loadRevision` 的刷新路径保留。
3. 设置页不再在保存 AI 开关时广播可用性；`settings-save-feedback` 区域只剩状态文本与 Retry 按钮。
4. `theme/material.tokens.css:120` 的 `Companion-inspired` 注释与 §2.4 的两处 `character` 英文单词**有意保留**，如需彻底去词化请由 Lead 决定（属必须保留文件的措辞调整）。

---

## 附：本任务未执行的写操作

- 未执行任何 `git add` / `git commit` / `git push` / `git rm`（文件删除仅作用于工作区）。
- 未修改 `.github/**`、`desktop/build/**`、`nsis/**`、`protocol/v1/manifest.json`、任何 Go 文件、任何 `docs/**` 文件、`.gitignore`。
- 未修改 `desktop/frontend/package.json`、`pnpm-workspace.yaml`、`pnpm-lock.yaml`、`desktop/frontend/src/platform/services.ts`、`desktop/frontend/src/pages/HomePage.tsx`。
- 文件写入仅通过 read/edit/write 工具或 `Remove-Item`（删除），未使用 `Set-Content`/`Out-File`，不引入 BOM/CRLF 变更。
