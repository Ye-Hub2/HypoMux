# 摘除「关于」页 · 总览

日期：2026-10-05 ｜ 分支：main ｜ 任务来源：用户「把关于页以及相关的也去掉吧」
前置：[更新链路整条摘除](../updater-removal/README.md)（上一轮已删掉关于页的检查更新 UI）

## 结论

桌面端**「关于」页已整页移除**，不留空壳路由、不留孤儿文案、不留死样式与死依赖。删除范围覆盖页面本体、路由接线、侧边导航项、专属 CSS、专属 i18n 文案、专属静态资源，以及因此失去全部调用方的平台方法与产品元数据字段。Go 侧与 CI 侧无耦合，未做任何改动。

| 指标 | 任务前 | 任务后 |
| --- | --- | --- |
| 前端测试 | 45 files / 413 tests | **44 files / 409 tests**（删掉 `AboutPage.test.tsx` 的 4 个用例） |
| i18n 键数（zh/en） | 386 / 386 | **372 / 372**（各删 14 键，中英键集合与顺序完全一致） |
| `app.css` | — | 净删 242 行（+17 / −259） |
| 源码改动合计 | — | 8 个文件 +97 / −362，另有 5 个文件删除 |
| `package.json` + 锁文件 | — | 移除 2 个死依赖，锁文件 −904 行（−100 个包），**纯删除、零新增行、零版本漂移** |
| 依赖体积 | — | `react-markdown` + `remark-gfm` 及其整棵 mdast/hast/unist 子树不再安装 |

## 成员分工

| 成员 | 交付 | 详情 |
| --- | --- | --- |
| `about-shell` | 页面文件、路由、导航、`openURL`、`product.ts` | [shell.md](shell.md) |
| `about-assets-css` | i18n 文案、`app.css`、静态资源 | [assets-css-i18n.md](assets-css-i18n.md) |
| lead | `errorCodes.ts`、死依赖清理、总验收 | 本文件 |

范围契约见 [SCOPE.md](SCOPE.md)（禁改清单 + 互斥写作用域）。

## 改动明细

### 1. 页面本体（删除）
- `desktop/frontend/src/pages/AboutPage.tsx`（87 行）
- `desktop/frontend/src/pages/AboutPage.test.tsx`（4 个用例）

### 2. 路由与导航接线（`about-shell`）
- `src/App.tsx`：删 lazy import、DEV 深链白名单里的 `"about"`、`pageOrder` 中的 `"about"`、渲染三元里的 `<AboutPage />` 分支；删除后整条三元链重新对齐为每级两空格（逻辑等价性已逐条核对，末支仍是 `: null`）。`?page=about` 深链现在正确回落到 `home`，不再是死链。
- `src/components/shell/CompactNavigation.tsx`：`AppPage` 联合类型去掉 `| "about"`、`Info24Regular` import、`.nav-bottom` 里整个关于页 Tooltip+Button。
- `src/components/shell/CompactNavigation.test.tsx`：**只追加**一条 `queryByRole("button", { name: "nav_about" })` 为 null 的负向断言防回流，未删任何既有断言。
- `src/platform/desktop.ts`：删 `openURL`，import 收窄为 `{ Call, Window }`；`ignoreOutsideWails` 仍被 9 处使用，保留。
- `src/product.ts`：删 `build` / `website` / `repository` / `releases`，保留 `name` / `edition` / `version`（`TitleBar.tsx:56-57` 在用）。

`.nav-bottom` 容器保留：其样式只有 `display:flex/gap/margin-top:auto`，无 padding/border/min-height，生产构建下该容器内只剩 DEV 专用按钮、渲染高度为 0，不留空隙，因此无需跨作用域改 CSS。

### 3. 文案与样式（`about-assets-css`）
- i18n 中英各删 14 键：`about_intro`、`about_open_github`、`about_notice_title`、`about_notice_text`、`about_signpath_title`、`about_signpath_text`、`about_sponsorship_title`、`about_wechat`、`about_alipay`、`about_qr_missing`、`settings_about`、`settings_sponsorship_title`、`settings_sponsorship_text`、`nav_about`。
  其中 `settings_about` / `settings_sponsorship_title` 在删除前就已零引用（既存死键），`git grep HEAD` 证据见成员报告。
- `src/app.css`：`.about-*` / `.signpath-*` / `.sponsor-section` / `.payment-*` 整条删除；**6 处共享选择器组只摘掉关于页那一个选择器**，同组 `.settings-*` 等规则原样保留；`.hm-card`（23 处）未动；大括号 1143/1143 平衡，无空规则块。

### 4. 静态资源（删除）
- `desktop/frontend/public/support/wei.png`
- `desktop/frontend/public/support/zhi.jpg`
- `desktop/frontend/public/support/SignPath/`（整个目录）
- **保留** `desktop/frontend/public/support/icon.ico`（`ProductMark.tsx:3` 仍在用）
- **保留** 仓库根目录 `support/` 四个文件（`README.md:44,240,245`、`README_EN.md:42,238,243` 直接引用）

### 5. lead 侧
- `src/components/notifications/errorCodes.ts`：删 `{ code: "HM-E1602", matches: (_text, key) => key.startsWith("about:") }`。该规则原本是关于页域的通知兜底码，删页后永不可达；全仓仅此一处出现，无测试断言。
- 死依赖清理：`pnpm remove react-markdown remark-gfm`。这两个包原本是 Release Notes Markdown 渲染的唯一消费方（已随更新链路删除），`AboutPage.tsx:18-19` 是最后一个消费点，本轮删页后彻底归零。

## 验收（lead 统一重跑）

```
cd desktop/frontend
node node_modules\typescript\bin\tsc --noEmit        → 无输出，exit 0
node node_modules\vitest\vitest.mjs run             → 44 files / 409 tests passed，exit 0
```

两项均在成员交付后、以及依赖清理后各跑一遍，结果一致。全前端 grep `AboutPage|about_|openURL|signpath|sponsor-section|payment-|wei\.png|zhi\.jpg|SignPath\.png|HM-E1602|react-markdown|remark-gfm` → **零命中**（唯一例外是 `CompactNavigation.test.tsx` 里新增的反向断言字符串 `nav_about`，是刻意留下的防回归断言）。

Go 侧无需改动：全仓 `*.go` 中 `"about"` / `关于` / `OpenURL` / `SetPage` 零匹配，不存在托盘菜单、深链或协议处理器会跳转到关于页。

## 未做的事（有意保留）

1. **`docs/migration/**`、`docs/validation/**`、`reports/**`** 中大量提到关于页的历史记录（`ui-migration-matrix.md`、`feature-inventory.md`、`functional-sprint-changelog.md`、`reports/parts/03-frontend.md` 等）一律不改写，按既定原则当作历史档案。
2. **`TitleBar.tsx` 与 `desktop/main.go` 的「HypoMux 自定义版」** 窗口标题改动保持不变——删除关于页不影响版本号展示，标题栏本就显示 `v{version}`。
3. **未执行 `git add` / `git commit`**，全部改动只在工作区。