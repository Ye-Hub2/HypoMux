# 14 前端虚拟网卡 UI（task-10）

- 负责人：`frontend-vnic-dev`（teammate）
- 共享任务：`task-10`
- 日期：2026-10-03
- 上游契约：`reports/vnic/00-frozen-interface.md`（§0 / §2.2 / §3 / §4 / §6）
- 写范围（未越界）：`desktop/frontend/src/platform/services.ts`、`desktop/frontend/src/pages/HomePage.tsx`、`desktop/frontend/src/components/vnic/**`
- 未执行任何 `git add/commit/push`（只跑了只读的 `git status` / `git diff`）。

## 0. 结论摘要

用户需求「程序主页新增一个按钮，用来创建虚拟网卡」已落地：主页网络区下方新增「虚拟网卡」面板（`VirtualAdapterPanel`），面板提供**创建虚拟网卡**按钮（默认 `HypoMux-VNIC` / `10.66.0.1`，可在对话框内改名改址），创建/移除期间按钮与对话框进入 loading（上限 60s），已存在时显示状态徽标与**移除虚拟网卡**按钮，成功走既有通知层，失败走既有 `conciseDiagnosticMessage` 摘要路径。桌面服务封装按冻结契约 §4 逐字落在 `platform/services.ts` 的 `appServices.virtualAdapter` 分组。全部为**静态自查**结果，未在本机编译/运行（原因见 §4）。

## 1. 改动 / 新增清单（文件:行号）

### 1.1 修改：`desktop/frontend/src/platform/services.ts`（396 行；diff `+15 / -2`）

| 行号 | 内容 |
| --- | --- |
| :7 | `import * as VirtualAdapterService from "../../bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/virtualadapterservice";`（紧跟 :6 `TunService`，逐字照抄，仅换服务名） |
| :24 | `VirtualAdapterStatus as GeneratedVirtualAdapterStatus,` 加入 `import type { … } from ".../desktop/internal/services/models"`（:10-25 块内，字母序末位） |
| :70-73 | 注释 + `export type VirtualAdapterStatus = GeneratedVirtualAdapterStatus;`（照 `TunService` 的既有别名写法，同时避免 `noUnusedLocals` 报错、给页面提供类型入口） |
| :75 | `export type CompleteAppSettings = AppSettings & {`（**AI 字段清理**：`Omit<AppSettings, "ai_enabled">` → `AppSettings`，并删除 `ai_enabled?: boolean;`，见 §5.1） |
| :350-354 | `appServices` 内新增分组（在 `tun: { latest, preflight }`(:347-349) 之后，`settings` 之前）：`virtualAdapter: { create: (interfaceName, address) => VirtualAdapterService.Create(interfaceName, address), status: () => VirtualAdapterService.Status(), remove: () => VirtualAdapterService.Remove() }` |

### 1.2 修改：`desktop/frontend/src/pages/HomePage.tsx`（366 行；diff `+16`）

| 行号 | 内容 |
| --- | --- |
| :26 | `import { VirtualAdapterPanel } from "../components/vnic/VirtualAdapterPanel";` |
| :67-74 | `const notifySuccess = useCallback((message: string) => { notify({ title: t("infobar_success"), message, intent: "success", dedupeKey: "home:vnic-success" }); }, [notify, t]);`（紧随既有 `notifyError`(:56-66)） |
| :249-254 | `<VirtualAdapterPanel locale={locale} text={text} notifySuccess={notifySuccess} notifyError={notifyError} />`（位于 network `<section>`(:247 `</section>`) 之后、`<RuntimeStatusBar>`(:256) 之前） |

复用而非新增：`text`(:50 `(zh, en) => locale === "en" ? en : zh`)、`notifyError`(:57，`notify({ intent: "error" })`)、i18n 既有键 `infobar_success`（`legacy.messages.json` zh「成功」/ en「Success」）。**未新增任何 i18n 键。**

### 1.3 新增：`desktop/frontend/src/components/vnic/VirtualAdapterPanel.tsx`（397 行）

| 行号 | 内容 |
| --- | --- |
| :1-16 | Fluent UI v9（Badge/Button/Dialog/DialogActions/DialogBody/DialogContent/DialogSurface/DialogTitle/Input/Spinner）、`@fluentui/react-icons`（Add20Regular/Delete20Regular）、React hooks、`{ appServices, withServiceTimeout, type VirtualAdapterStatus }`(:15)、`import "./vnic.css";`(:16) |
| :21 / :22 / :25 | `VIRTUAL_ADAPTER_DEFAULT_NAME = "HypoMux-VNIC"` / `VIRTUAL_ADAPTER_DEFAULT_ADDRESS = "10.66.0.1"` / `VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS = 60_000` |
| :27-34 | `export type VirtualAdapterPanelProps = { locale: "zh" \| "en"; text: (zh, en) => string; notifySuccess: (message: string) => void; notifyError: (message: string, retry?: () => void) => void; disabled?: boolean; className?: string }` |
| :36-49 | `PanelState = "unknown" \| "absent" \| "creating" \| "present" \| "removing" \| "failed"` + `panelState()` 白名单映射（未知值 → `"unknown"`） |
| :51-55 | `export const isValidIPv4`（四段、每段 `/^\d{1,3}$/` 且 ≤255；提交前拦截，避免把明显非法地址送去后端） |
| :57-90 | `stateText`(:57-75) / `stateBadgeColour`(:77-82，present→success、failed→danger、creating\|removing→warning、其余→informative) / `formatCreatedAt`(:84-90) |
| :92-99 | 组件签名（props 解构，`disabled = false`） |
| :100-215 | 状态与行为：`useState<VirtualAdapterStatus \| null>`、`refresh()` 调用 `appServices.virtualAdapter.status()`（失败置 `loadFailed`，**不弹错误通知**）、挂载时只跑一次的 `useEffect`、`create()`、`remove()`、`openCreate`、`operationBusy = busy \|\| state === "creating" \|\| state === "removing"`、`controlsDisabled = disabled \|\| operationBusy` |
| :215-221 | 面板骨架：`<section className={className ?? "virtual-adapter-section"} aria-labelledby="virtual-adapter-title">` + `network-section-heading`(:216) + `section-kicker`(:218) + `network-section-title-row`(:219) + `<h1 id="virtual-adapter-title">`(:220) |
| :223-249 | 右侧动作区：状态徽标 `<Badge appearance="tint" color={stateBadgeColour(state)} aria-live="polite">`(:225-227)、主按钮「创建虚拟网卡 / Create virtual adapter」(:229-237，`appearance="primary"` `icon={<Add20Regular />}` `disabled={controlsDisabled}` `onClick={openCreate}`)、`state === "present"` 时的「移除虚拟网卡 / Remove virtual adapter」(:238-248，`appearance="subtle"`，busy 时 icon 换成 `<Spinner size="tiny" />`) |
| :251-289 | 正文卡片 `<div className="virtual-adapter-body hm-card" aria-busy={loading \|\| operationBusy}>`：加载态 `<Spinner label=… />`(:252-253)、摘要 `<strong>{summary}</strong>`(:256)、说明句、状态不可读说明、`virtual-adapter-details`(:272-281，名称 / `地址/前缀` / MTU / `Created:` 本地化时间 / GUID)、`virtual-adapter-error` + `lastError`(:283)、Retry 按钮(:285-289) |
| :304-361 | 创建对话框：`DialogSurface aria-label`(:304)、`DialogTitle`(:306)、`DialogContent`(:307) 内 **权限提示 `virtual-adapter-intro`(:308-313)**（Lead 追加，见 §5.2）与 `virtual-adapter-fields`(:314)：名称 `Input`(:315-321，`maxLength={128}`)、地址 `Input`(:322-331，`aria-invalid`)、默认值说明 `virtual-adapter-hint`(:332-337)、非法地址 `<span role="alert">`(:338)、创建中 `<span role="status">`(:339-343)；`DialogActions`(:346-358) 取消 / 创建（busy 时 `disabled` + Spinner）；`onOpenChange` 在 busy 时拒绝关闭(:296-302) |
| :363-396 | 移除确认对话框（说明会停止 keeper、连接会中断；取消 / 移除；busy 时拒绝关闭） |

### 1.4 新增：`desktop/frontend/src/components/vnic/VirtualAdapterPanel.test.tsx`（240 行）

| 行号 | 内容 |
| --- | --- |
| :1 | `// @vitest-environment jsdom`（仓库无全局 setupFiles，照 `ConnectionsPage.test.tsx` 做法逐文件声明） |
| :3-8 | `@testing-library/react`、`FluentProvider/webLightTheme`、`vitest`、`AppNotificationProvider/AppNotificationCenter`、`type VirtualAdapterStatus`、`{ isValidIPv4, VirtualAdapterPanel }` |
| :10-29 | `vi.hoisted` mocks `{ create, status, remove }` + `vi.mock("../../platform/services", () => ({ appServices: { virtualAdapter: {...} }, withServiceTimeout: (request) => request }))` + `vi.mock("../../i18n/i18n", () => ({ useI18n: () => ({ locale: "en" }) }))` |
| :31-92 | 夹具与辅助：`text = (_zh, en) => en`、`status(overrides)` 工厂、`present` 夹具（present / HypoMux-VNIC / 10.66.0.1 / 24 / 1420 / GUID / `createdAt: "2026-08-24T01:02:03Z"`）、`renderPanel()`（FluentProvider + AppNotificationProvider 包裹，返回 `notifySuccess/notifyError` 探针）、`prepareViewport()`（body rect + `offsetParent` 双 spy，照 `ConnectionsPage.test.tsx`）、`readyDialogAction(dialog, name)` |
| :107-117 | `isValidIPv4` 7 例（含 `" 192.168.1.254 "`、`10.66.0.256`、空串） |
| :119-240 | 面板 8 例：加载态+创建可用、present 明细（名称/`10.66.0.1/24`/`1420`/GUID/`Created:` 已本地化不含 ISO 串）、状态不可读可重试、**默认值创建成功（新增断言：对话框展示管理员权限提示）**、改名改址 + 失败保留对话框并带 retry、非法 IPv4 拦截不调服务、移除成功后回到未创建、移除失败后动作仍可用 |

### 1.5 新增：`desktop/frontend/src/components/vnic/vnic.css`（13 行）

面板自有 class（不改 `app.css`，理由见 §3.4）：`.virtual-adapter-section`(4)、`.virtual-adapter-body`(5，复用 `.hm-card` 只提供指针光效)、`.virtual-adapter-body > span`(6)、`.virtual-adapter-details` + `strong`(7-8)、`.virtual-adapter-hint`(9)、`.virtual-adapter-intro`(10)、`.virtual-adapter-error`(11，`var(--hm-danger)`)、`.virtual-adapter-fields` + `.fui-Input`(12-13)。全部使用已定义 token：`--hm-outer-border` / `--hm-radius` / `--hm-row-hover` / `--hm-text-tertiary` / `--hm-text-primary` / `--hm-danger`（分别在 `theme/semantic.tokens.css`、`theme/material.tokens.css`）。

## 2. 契约落地对照（对照 `00-frozen-interface.md` §4）

| 契约要求 | 落地位置 | 状态 |
| --- | --- | --- |
| `appServices` 新增 `virtualAdapter` 分组，create/status/remove 三个薄封装 | `services.ts:350-354` | ✅ 逐字 |
| 导入行逐字 `import * as VirtualAdapterService from "../../bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/virtualadapterservice";` | `services.ts:7` | ✅ 逐字（含 `desktop/internal/services` 路径段） |
| 类型从同目录 `models` re-export，照 `TunService` 做法 | `services.ts:24` + `:70-73` | ✅ |
| 方法名与 Go 侧一致（Create/Status/Remove） | `services.ts:351-353` | ✅（按 §3 契约；无法本地核对生成文件） |
| 主页新增按钮 | `HomePage.tsx:249-254` 挂载面板；按钮在面板 `:229-237` | ✅ |
| 面板显示状态（absent/creating/present/removing/failed）+ 名字/地址/前缀/MTU/创建时间/lastError | 徽标 `:225-227`、`panelState` `:36-49`、`stateText` `:57-75`、明细 `:272-281`、`lastError` `:283` | ✅ 覆盖 §2.2 全部 8 个字段 |
| 已存在时变状态 + 「移除虚拟网卡」 | `:238-248`（仅在 `state === "present"` 渲染） | ✅ |
| 点击创建弹对话框，可改名字与地址，默认 `HypoMux-VNIC` / `10.66.0.1` | 对话框 `:304-361`；常量 `:21-22`；`openCreate` `:234` 附近 | ✅ |
| 提交后 loading（最长 60s） | `:25` 60_000 + `withServiceTimeout(..., 60_000, operation)`；按钮 `disabled` + `<Spinner size="tiny" />`；`:339-344` 状态文案；`:251` `aria-busy` | ✅ |
| 成功走既有通知层 | 面板 `notifySuccess` → `HomePage.tsx:67-74` `notify({ intent: "success", title: t("infobar_success") })` | ✅ |
| 失败走既有 `conciseDiagnosticMessage` 摘要路径 | 面板 `notifyError(String(error), retry)` → `HomePage.tsx:56-66` `notify({ intent: "error" })` → `notificationMessage.prepareNotificationMessage` 内部对 error 自动套 `conciseDiagnosticMessage` | ✅（未重复实现摘要逻辑） |
| 新组件放 `components/vnic/` | `VirtualAdapterPanel.tsx` / `.test.tsx` / `vnic.css` | ✅ |
| 不改 `App.tsx` / `CompactNavigation.tsx` / `AppShell.tsx` | 未触碰（`git status` 显示它们由 ai-frontend 成员改动） | ✅ |
| 中英双语沿用内联 locale 模式，不新增 `legacy.messages.json` 键 | 面板接收 `locale`/`text`，所有文案走 `text(zh, en)`；HomePage 只复用既有 `t("infobar_success")` | ✅ |
| 超时/权限/冲突错误以可读摘要展示 | 见上一行；`withServiceTimeout` 的 operation 文案含「超时」/「timed out」，可被 `conciseDiagnosticMessage` 归一为超时摘要 | ✅ |
| `Status()` 在无核心/未创建时返回 `{state:"absent"}` 且不报错 ⇒ 初始状态按「未创建」处理，不当错误 | 面板 `refresh()` 只在调用**抛错/reject** 时置 `loadFailed`；返回 `absent` 走正常摘要「虚拟网卡 · 未创建 / Virtual adapter · Not created」 | ✅（Lead 追加约定 2） |
| 地址用常量默认 `10.66.0.1`（前缀 24、MTU 1420），后端不做同网段探测 | 面板不做任何探测/校验覆盖，仅做 IPv4 格式预校验；前缀 24 来自后端 `prefixLength` 并原样展示 | ✅（Lead 追加约定 2） |
| `Remove()` 幂等，重复移除不报错 | 前端仅在 `present` 时提供移除入口；成功/失败均由后端返回值决定，前端不做「已移除」前置判断 | ✅（Lead 追加约定 2） |
| 对话框加管理员权限/UAC/核心重启提示文案 | 面板 `:308-313`（`virtual-adapter-intro`），中英双语 | ✅（Lead 追加指令 1） |

## 3. 逐行静态自查（因无法编译，逐项人工核对）

1. **import 全部被使用**：面板 16 个导入符号（10 个 Fluent、2 个图标、4 个 hook、`appServices`/`withServiceTimeout`/`VirtualAdapterStatus`、`./vnic.css`）均有使用点；测试文件 19 个导入符号（`cleanup`/`fireEvent`/`render`/`screen`/`waitFor`/`within`/`FluentProvider`/`webLightTheme`/`afterEach`/`beforeEach`/`describe`/`expect`/`it`/`vi`/`AppNotificationCenter`/`AppNotificationProvider`/`VirtualAdapterStatus`/`isValidIPv4`/`VirtualAdapterPanel`）均有使用点。`tsconfig.json` 为 `strict: true` + `noUnusedLocals: true`，故此项是硬约束。
2. **导出符号均有定义**：`VIRTUAL_ADAPTER_DEFAULT_NAME`(:21)、`VIRTUAL_ADAPTER_DEFAULT_ADDRESS`(:22)、`VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS`(:25)、`VirtualAdapterPanelProps`(:27)、`isValidIPv4`(:51)、`VirtualAdapterPanel`(:92)；无拼写漂移、无重复标识符。
3. **类型自洽**：`stateBadgeColour` 显式标注 `"danger" | "warning" | "success" | "informative"`(:77)，与 Fluent `Badge` 的 `color` 取值集合相容；`panelState` 返回 `PanelState` 白名单；`notifyError` 实参类型 `(message: string, retry?: () => void) => void` 与 `HomePage.tsx:57` 的实际签名一致；`withServiceTimeout` 按既有 3 参签名调用（`services.ts:254`）；`VirtualAdapterStatus` 为 `type` 导入（`services.ts` 无 `verbatimModuleSyntax`，但仍按既有风格用 `import type` / `type` 修饰符）。
4. **Fluent UI v9 props 名称与仓库既有用法一致**：`<Badge appearance="tint" color=…>`（先例 `HotspotPanel.tsx:163`、`HomePage.tsx:284`）、`<Spinner size="tiny" />` 作 Button `icon`（先例 `AboutPage.tsx:175/244`）、`<Spinner label=… />`（先例 `MTUDetectionPage.tsx:110`）、`Dialog/DialogSurface(aria-label)/DialogBody/DialogTitle/DialogContent/DialogActions`（先例 `HomePage.tsx` 既有 preflight 对话框）、`Input` 受控 `onChange={(_, data) => data.value}`（先例 `RuleSetsPanel.tsx:193-198`、`NetworkAdapterItem.tsx:143-150`）。
5. **样式不依赖未定义 class**：面板新增的 4 个自有 class 全部由 `vnic.css`（同目录、组件自行 `import`，与 `pages/mtu.css` 被 `MTUDetectionPage.tsx:6` 引入的模式一致）定义；其余 class（`network-section-heading`、`section-kicker`、`network-section-title-row`、`network-section-actions`、`hm-card`）均为 `app.css` 既有全局类，只读复用。
6. **未新增 i18n 键**：`legacy.messages.json` 无改动；仅复用既有 `infobar_success`。
7. **写范围未越界**：`git status --short -- desktop/frontend/src` 中除我的两个修改文件与新增目录 `components/vnic/` 外，其余（`App.tsx`、`app.css`、`components/shell/**`、`components/ai/**` 删除、`platform/ai.ts` 删除等）均为 ai-frontend 成员的改动，我未触碰。
8. **git 只读复核**：`git diff` 逐字确认 `HomePage.tsx`(+16) 与 `services.ts`(+15/-2) 只含 §1.1/§1.2 列出的改动，无夹带。

## 4. 未验证项（诚实清单）

1. **未运行 tsc / vitest**：本机没有 pnpm、没有 `node_modules`、没有 `desktop/frontend/bindings/`，因此 `pnpm test`、`pnpm build`（内含 `tsc`）都无法执行。以上全部结论来自人工逐行核对。
2. **bindings 由 CI 生成**：`desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/virtualadapterservice.ts` 与 `models.VirtualAdapterStatus` 在本地不存在，由 `.github/workflows/build.yml:139-142` 的 `wails3 generate bindings -clean=true -ts -i` 在 CI 现场生成。若 Go 侧类型名/方法名/模型字段与冻结契约 §3/§2.2 不一致，CI 的 `tsc` 会在 `services.ts:7/24/351-353` 直接报错——我无法在本地发现该问题，只能保证与契约文档逐字一致。
3. **未在 Wails 运行时真机验证**：没有真实运行桌面端，因此「点击按钮 → 弹 UAC → 适配器出现」的端到端链路、60s 超时、地址冲突错误摘要、移除后连接中断等行为均未实际观测。
4. **未做视觉/布局验证**：无法启动 Vite 预览或截图，「主页新增第 4 个 grid 行（隐式 auto 行）不被裁切」「对话框在小窗口下的滚动」等仅为 CSS 逻辑推导。
5. **未验证既有测试是否受影响**：`HomePage.test.tsx` 不 mock `../platform/services`，会连带加载真实 `services.ts`（进而 import 尚未生成的 bindings）——风险分析见 §5.3，但未本地运行确认。
6. **测试文件本身未运行**：`VirtualAdapterPanel.test.tsx` 的 8 个用例（含本轮新增的权限提示断言）全部未执行。
7. **浏览器预览假实现未新增**：任务描述里的 `webPreviewServices` 在当前仓库中**不存在**（`services.ts` 只有单一 `appServices` 门面，浏览器预览由各页面的 `import.meta.env.DEV && !isDesktopRuntime()` 夹具各自处理）。为避免引入无人使用的死结构，未新增该分支；两套对象形状一致性要求在当前代码结构下不适用。

## 5. 附注

### 5.1 Lead 追加：`services.ts` 残留 AI 字段清理（已执行，team message `team-message-bdac2998`）

按 Lead 指令由本文件当前写者一并收口：`services.ts:75` 的 `Omit<AppSettings, "ai_enabled">` 改回 `AppSettings`，并删除 `ai_enabled?: boolean;`。清理后全仓前端 `grep -n "ai_enabled|AIEnabled"` 零命中（此前仅 `services.ts:75-76` 两处）。对应 T1 从 Go 侧删除 `AIEnabled` 后重新生成的 `models.AppSettings` 将不再有该字段，故此处是必需的联动改动，否则 CI 的 `tsc` 会因 `Omit<...>` 的键不存在而报错。

### 5.2 Lead 追加：对话框权限提示（已执行，team message `team-message-01bcacc6`）

`VirtualAdapterPanel.tsx:308-313` 在创建对话框说明位置新增一行（无二次确认交互）：

- zh：`创建虚拟网卡需要管理员权限，可能弹出 UAC，并会短暂重启聚合核心（当前连接可能短暂中断）。`
- en：`Creating a virtual adapter needs administrator rights; a UAC prompt may appear and the core will briefly restart (current connections may drop briefly).`

样式类 `.virtual-adapter-intro`（`vnic.css:10`）。测试 `VirtualAdapterPanel.test.tsx:163-164` 新增断言：对话框打开时能看到该提示。

### 5.3 对既有 `HomePage.test.tsx` 的兼容性分析（推断，未运行）

- 该测试只 mock `../state/useEngineState` 与 `../i18n/i18n`，**不 mock** `../platform/services`，因此会加载真实 `services.ts`。旁证：`components/RuleSetsPanel.test.tsx:8` 用 `vi.mock("../platform/services", async (importOriginal) => …)` 已经在 jsdom 中真实加载过 `services.ts`（连带 `@wailsio/runtime` 与全部 bindings），说明该模块在 CI 环境下可被测试进程加载。
- 面板的 `status()` 调用在 `refresh()` 的 `try/catch` 内，同步抛错或 reject 都会被吞掉并只显示内联 Retry，不会让测试崩溃。
- 面板不产生 `article` / `combobox` / `checkbox` 角色，关闭状态不渲染 `dialog`（Fluent `Dialog` 仅在 `open` 时挂载 `DialogSurface`，HomePage 既有 preflight 对话框已验证该行为），因此不会干扰该文件既有断言（`:301` 的 `queryByRole("dialog") === null` 等）。
- 面板文案在 `HomePage.test.tsx` 的 i18n mock 下走 `text()`（真实 `locale`），与既有断言无交集。

## 6. 交接状态

- `task-10`：实现完成，交付物为本文件 + §1 列出的代码改动；等待 `integration-verifier` 在 CI 现场（bindings 生成后）跑 `pnpm --dir desktop/frontend test` 与 `build`。
- 建议验证顺序（CI 现成）：`wails3 generate bindings` → `pnpm test` → `pnpm build`（`build.yml:139-150`）。
- 需要我配合时（例如 bindings 生成后字段名与假设不符、或 `HomePage.test.tsx` 因真实 `services.ts` 报错），请在 task 重新打开或直接派活。
