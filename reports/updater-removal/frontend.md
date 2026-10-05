# 摘除更新链路 · 前端（updater-frontend）

写作用域：`desktop/frontend/**`。未执行任何 `git add` / 提交，只改工作区文件。

## 一、改动清单

### 1. `src/pages/AboutPage.tsx`（253 行 → 87 行）

整页从「产品信息 + 更新入口」变成纯静态信息页。删除内容：

| 位置（原行号） | 删除项 |
| --- | --- |
| :1-12 | Fluent `Dialog` / `DialogActions` / `DialogBody` / `DialogContent` / `DialogSurface` / `DialogTitle` / `Spinner` 导入 |
| :13-17 | `ArrowSync20Regular` 图标导入 |
| :18-19 | `react-markdown` / `remark-gfm` 导入 |
| :21 | `useAppNotifications` 导入 |
| :23 | `import { appServices, type UpdateCheckResult } from "../platform/services"` —— **整条服务绑定依赖断掉** |
| :25 | `useLayoutEffect` / `useRef` / `useState` / `RefObject` 导入（本文件已无任何 hook） |
| :27 | `startSerialPoll` 导入（下载进度轮询） |
| :29-62 | `ReleaseNotes` 组件（Markdown 渲染 release notes + 外链代理） |
| :67-71 | `checking` / `downloading` / `downloadPercent` / `update` / `updateDialogOpen` 五个 state |
| :72-74 | 三个 dialog/notes ref |
| :77-94 | `useLayoutEffect`：dialog 打开时锚定焦点、重置滚动容器的逻辑 |
| :96-98 | `notify()` 包装（唯一调用方是更新流程） |
| :100-121 | `checkForUpdates()`（含 `appServices.updater.check()`） |
| :123-145 | `installUpdate()`（含 `appServices.updater.progress/download/installAndQuit`） |
| :175-177 | 「检查更新」按钮（含 checking 态 Spinner） |
| :224-250 | 「发现新版本」`Dialog` 全块（标题、版本对比、release notes、立即更新/稍后、下载百分比按钮） |

**保留的能力**（`AboutPage.tsx:12-84`）：

- 页头 kicker / 标题 / 简介（`:14-20`）
- 品牌区：图标、`productInfo.name`、`当前版本: v{version}`、`productInfo.build`（`:23-31`）
- 简介段落（`:32`）
- 两个外链按钮：官方网站 `productInfo.website`、GitHub `productInfo.repository`（`:33-40`）
- 网络与合规声明卡片（`:43-46`）
- **SignPath 代码签名说明卡片**（`:48-66`）——按范围契约保留，且描述的是「从 GitHub/CNB Release 手动下载并验证签名」，不涉及应用内更新
- 赞助区（微信/支付宝二维码，`:68-83`）

页面自洽：改动后文件内没有任何未使用的 state / 变量 / import（`tsc` 的 `noUnusedLocals: true` 已验证）。

### 2. `src/pages/AboutPage.test.tsx`（84 行 → 88 行，1 个 `it.each`×2 → 4 个用例）

- **删除** `vi.mock("../platform/services", () => ({ appServices: { updater: { check: mocks.check } } }))`。原因：AboutPage 已不再 import 该模块，留着一个只暴露 `updater.check` 的假模块会让 `vi.mock` 的形状看起来像 `../platform/services` 的真实契约（真实模块还有 settings / routing / ruleSets 等十几组绑定），也容易被后来者误当成模块接口。现在该文件**完全不 mock 服务层**，mock 形状不可能与其他测试文件冲突。
- 删除只为更新服务的 mock：`mocks.check`、`useAppNotifications` mock、以及 `.update-notes` 滚动复现用的 `HTMLElement.prototype.focus` spy。
- 保留 jsdom 视口相关的两个 spy（`getBoundingClientRect` / `offsetParent`），Fluent 的 Tabster 焦点管理仍需要。
- 新增 4 个用例：
  1. 产品标识与当前版本渲染（`about_intro` 在页头与产品卡各出现一次，断言 `getAllByText` 长度为 2）。
  2. 静态外链可用：点 Website / GitHub / SignPath.io / SignPath Foundation 均调用 `desktopPlatform.openURL` 且 URL 正确（Fluent `Link` 无 `href` 时不带 `link` 角色，用 `getByText` 匹配）。
  3. 合规声明、签名、赞助三个区块标题存在。
  4. **负向断言**：更新按钮 / `about_check_update` / `about_checking_update` / `about_update_now` / `about_update_later` / `about_update_downloading` / `about_update_available_title` 均不再出现；无 `role="dialog"`；`.update-dialog` / `.update-notes` / `.update-summary` 均不存在；`.about-actions` 里恰好只剩 2 个按钮。

### 3. `src/platform/services.ts`（449 行 → 371 行）

| 原行号 | 删除项 |
| --- | --- |
| :100 | `update_channel?: "stable" \| "preview";`（现 `CompleteAppSettings` 从 :99 开始） |
| :152-174 | `ReleaseInfo` / `UpdateCheckResult` / `UpdateProgress` 三个 `export type` |
| :266-267 | `const updaterMethod = ...`（现 `engineMethod` 在 :241） |
| :434-441 | `appServices.updater = { check, download, installAndQuit, progress }`（现 `blockedDomains` 在 :401、`ruleSets` 在 :407 直接相邻） |

删除前已 grep 全前端确认：这三个类型除 `AboutPage.tsx` 外无任何引用；`updaterMethod` / `appServices.updater` 无其他调用点。

### 4. `bindings/`（Wails 生成物）

- **删除文件** `bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/updaterservice.ts`（整文件，含 `Check` / `Download` / `InstallAndQuit` / `Progress` 四个 `$Call.ByID`）。
- `bindings/.../services/index.ts`：删掉 `:15` 的 `import * as UpdaterService from "./updaterservice.js"`、`:28` 的 `UpdaterService` 导出项，以及类型再导出中的 `ReleaseInfo`（:58）、`UpdateCheckResult`（:76）、`UpdateProgress`（:77）。文件 80 行 → 75 行。
- `bindings/.../services/models.ts`：删掉 `ReleaseInfo`（原 :365-374）、`UpdateCheckResult`（原 :570-574）、`UpdateProgress`（原 :576-581）三个 `export interface`（`UpdateCheckResult` 引用 `ReleaseInfo`，两者一并清干净）。文件 596 行 → 578 行。

> ⚠️ **需要更正 lead 的一个前提**：`desktop/frontend/bindings/` 并**没有被提交进仓库**——`desktop/.gitignore:9` 有 `/frontend/bindings/`，`git ls-files -- desktop/frontend/bindings` 返回 0 行。所以这里的改动不会进入任何 commit，也不会出现在 `git status` / diff 里；它只是本地工作区里 `wails3 generate` 的产物，**下次 `wails3 generate` 会按新的 Go 服务列表重建**，重建结果与本次手改一致（Go 侧 `updater.go` 已删 ⇒ 不会再生成 `updaterservice.ts`）。前端 `tsconfig.json:24` 的 `include` 覆盖 `bindings`，所以清掉悬空导出让 `tsc --noEmit` 干净是必要的，但不影响构建产物。

### 5. `src/i18n/legacy.messages.json`（810 行 → 778 行）

zh 段与 en 段各删 16 个 key，**共 32 行**（`git diff` 实测 `deleted about_* keys: 32`）：

`about_check_update`、`about_checking_update`、`about_update_current`、`about_update_check_failed`、`about_update_download_failed`、`about_update_install_failed`、`about_update_unknown_error`、`about_update_available_title`、`about_update_available_summary`、`about_update_notes_label`、`about_update_download_hint`、`about_update_notes_empty`、`about_update_now`、`about_update_later`、`about_update_downloading`、`about_update_installing`

**保留的例外（重要）**：

1. **`about_release_notes` 在本仓库里根本不存在**（全仓 grep 无任何命中），无需处理。
2. **`about_open_github` 保留** —— 与更新链路无关的静态链接文案。
3. **`rulesets_update_now`（值「立即更新」/ "Update now"）保留** —— 这是**规则集订阅的手动刷新**，不是应用更新；`src/components/RuleSetsPanel.tsx:290` 及其测试 `RuleSetsPanel.test.tsx:98,99,160` 仍在用。中文值「立即更新」与被删的 `about_update_now` 字面相同，纯文本搜索容易误伤，特此标注。
4. `virtual_adapters_hint_pool_update_failed` 同样保留（虚拟网卡出口池写入失败）。

结构校验（Node 直接解析）：

```
zh keys 386 en keys 386
zh-only []   en-only []
order-equal true
update-keys-left: virtual_adapters_hint_pool_update_failed, rulesets_update_success,
                  rulesets_update_failed, rulesets_update_now   （均与更新链路无关）
```

### 6. `src/app.css`（删 108 行死样式）

删除整块 `.update-dialog` / `.update-summary` / `.update-notes` 系列规则（原 :5259-5366）：

- `.update-dialog`、`.update-summary, .update-dialog p`
- `.update-notes` 及其 `p` / `p + p` / `h1`–`h6` / `h1:first-child` 等 / `ul, ol` / `li + li` / `blockquote` / `code` / `pre` / `pre code` / `table` / `th, td` / `th` 共 18 条规则

删除后紧接原有的 `@media (max-width: 880px)` 块，分隔空行保留。`.about-*` / `.signpath-*` / `.sponsor-section` / `.payment-*` 等仍被使用的样式一律未动。

### 7. `src/components/notifications/errorCodes.ts`（额外发现，删 1 行）

```ts
{ code: "HM-E1601", matches: (text, key) => key.startsWith("about:") && includesAny(text, ["检查更新", "update check", "check for update"]) },
```

这条规则的唯一触发条件就是 AboutPage 推送的 `about:success:检查更新失败` 一类通知。AboutPage 的 `notify()`（`dedupeKey: \`about:${intent}:${title}\``）已随更新链路删除，全前端 grep 确认**再无任何代码产生 `about:` 前缀的 dedupeKey**，故该规则永不可达，属更新链路死代码，已删除。`errorCodes.test.ts` 无 `HM-E1601` 用例，无需改测试。

**保留** 紧邻的 `HM-E1602`（`key.startsWith("about:")` 的页面域兜底码）：它不是更新专用，而是错误码词表里 About 页面域的稳定槽位，且已写进 `reports/HypoMux-项目分析报告.md:316` 与 `reports/parts/03-frontend.md:206`（按范围契约第 4 条，历史报告不改写）。当前不可达，但保留它不影响任何行为。若 lead 认为该一并摘除，删掉这一行即可，无需改测试。

### 8. `SettingsPage.tsx` — 无需处理

按契约第 3 条**没有动**「更新渠道」相关的任何内容。补充确认：全前端 grep `update_channel` **零命中**，用户上一轮的改动已经把该字段的前端引用清干净了，`CompleteAppSettings.update_channel` 删除后不会造成任何 `tsc` 报错。`SettingsPage.tsx:314` 的 `saveQueue.attach((updater) => ...)` 里的 `updater` 只是回调形参名，与更新服务无关。

## 二、验收输出

```powershell
cd <repo>\desktop\frontend
node node_modules\typescript\bin\tsc --noEmit
node node_modules\vitest\vitest.mjs run
```

```
tsc exit=0                      # 无任何输出

 Test Files  45 passed (45)
      Tests  413 passed (413)
   Duration  22.51s (transform 37.77s, setup 0ms, import 181.73s, tests 52.38s, environment 46.19s)
vitest exit=0
```

- 测试文件数与基线一致：**45 files**。
- 用例数 **411 → 413**：原 `AboutPage.test.tsx` 是 1 个 `it.each([true, false])`（计 2 个用例），改写为 4 个独立用例，`411 - 2 + 4 = 413`。
- 无 FAIL、无 skip 新增。`SettingsPage.test.tsx` 的 `settings save failed: Error: offline` 是既有的 stderr 噪声（用例断言失败路径），非失败。

## 三、遗留与风险

1. **`react-markdown` / `remark-gfm` 依赖已无人使用。**
   `AboutPage.tsx` 是这两个包在全前端的唯一消费方，删除后 `package.json:23-24` 的依赖声明成为死依赖。
   **本次刻意不动 `package.json` 和 `pnpm-lock.yaml`**：只改 `package.json` 而不同步 lockfile 会让 `pnpm install --frozen-lockfile` 直接失败，风险大于收益。建议后续用 `pnpm remove react-markdown remark-gfm` 让 lockfile 一起更新。留着也不影响 `tsc` / `vitest` / 打包。

2. **`bindings/` 是 gitignore 的生成物**（详见上）。手改不入库，靠 `wails3 generate` 自愈；因为 `tsconfig.json` 的 `include` 覆盖 `bindings`，本地不清理就会让 `tsc` 报悬空导出，因此清掉是必要的卫生动作，但**不应被理解为源码改动**。

3. **`productInfo.releases`（`src/product.ts:10`）现在是无引用字段**，值指向 `https://github.com/Hypostasis-Cat/HypoMux/releases/latest`。它只是静态 URL 常量、不产生任何网络请求，且属于「官网/仓库链接」这一要求保留的静态信息范畴，按最小改动原则**保留**。若 lead 希望一并清掉，删 `src/product.ts:10` 一行即可（`tsc` 不会因未使用的对象属性报错，TitleBar 只用 `name/edition/version/build`）。

4. **`HM-E1601` 是对外可见的支持码。** 该错误码此前会在「检查更新失败」时展示给用户并可能出现在用户提交的诊断信息里。删除后同类故障不再有专属码（更新链路已不存在，故障本身消失）；历史工单里若出现 `HM-E1601`，它仍是一个有意义的「旧的更新检查故障」标签，无需为兼容保留前端规则。

5. **`AboutPage` 不再有任何异步动作**，整页纯同步渲染 + 外链跳转。原来那段「dialog 打开时重置滚动、锚定焦点」的复杂 `useLayoutEffect` 及其配套的 `.update-notes` 滚动复现逻辑一并消失——这减少了 Tabster 焦点管理的一个已知脆弱点。

6. **兼容性**：前端已不再读写 `update_channel`。旧 `settings.json` 里残留的 `"update_channel": "preview"` 由 Go 侧 `encoding/json` 默认忽略未知字段处理（范围契约「兼容性要求」），前端不需要任何迁移逻辑。
