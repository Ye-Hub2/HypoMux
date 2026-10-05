# 摘除「关于」页 · 范围契约

日期：2026-10-05 ｜ 分支：main ｜ 任务来源：用户 m00307/m00308「把关于页以及相关的也去掉吧」

## 目标

从桌面端**彻底移除「关于」页**及其全部专有引用：页面、路由、导航项、专属样式、专属文案、专属静态资源、失去调用方的产品元数据与平台方法。不留死代码、不留空壳路由。

## 全局禁改（任何成员都不得触碰）

1. 与本任务无关的**未提交 vNIC / 设置页改动**（`VirtualAdaptersPage*`、`hyperv_adapter*`、`services/settings.go` 等）——**不要回退、不要顺手格式化**。
2. 上一轮已完成并验证的更新链路摘除成果（updater 文件已删、`AboutPage` 的更新 UI 已删、CI 更新渠道已删）——**不要回退**。
3. `desktop/frontend/src/components/shell/TitleBar.tsx` 与 `desktop/main.go` 里的 **`HypoMux 自定义版`** 标题改动。⚠️ 但注意：`TitleBar.tsx` **不在本任务任何人的写作用域内**，谁都不许动它。
4. **仓库根目录 `support/`**（`SignPath/`、`icon.ico`、`wei.png`、`zhi.jpg`）——README.md / README_EN.md 直接引用它们，**必须原样保留**。只删前端 bundle 里的 `desktop/frontend/public/support/`。
5. `docs/migration/**`、`docs/validation/**`、`reports/**` 下的历史记录——按既定原则**不动**（即使里面提到关于页）。
6. **不执行 `git add` / `git commit`**，全部改动只留在工作区。
7. `desktop/frontend/bindings/` 被 `.gitignore` 忽略且内容由 wails 重新生成，**本任务不需要也不允许改它**（除非编译报错证明必要，先在报告里说明）。

## 写作用域（互斥，不得越界）

| 成员 | 允许写 | 允许删 |
| --- | --- | --- |
| `about-shell` | `desktop/frontend/src/App.tsx`、`src/components/shell/CompactNavigation.tsx`、`src/components/shell/CompactNavigation.test.tsx`、`src/platform/desktop.ts`、`src/product.ts` | `src/pages/AboutPage.tsx`、`src/pages/AboutPage.test.tsx` |
| `about-assets-css` | `desktop/frontend/src/app.css`、`src/i18n/legacy.messages.json` | `desktop/frontend/public/support/wei.png`、`public/support/zhi.jpg`、`public/support/SignPath/`（整个目录） |

任何人需要写超出自己作用域的文件时，**不要自己动手**——在报告里写明「需要 X 的改动：具体内容」，由 lead 处理。

## 必须删除的东西（清单，节选自侦察）

**about-shell**
- `App.tsx:28` lazy import；`:74` 深链白名单里的 `"about"`；`:96` 页面列表里的 `"about"`；`:150-151` 渲染三元里的 `<AboutPage />` 分支。
- `CompactNavigation.tsx:18` `AppPage` 联合类型里的 `| "about"`；`:122-130` 整个 Tooltip+Button 导航项；`:125` `ref={navigationPage === "about" ? activeButtonRef : undefined}` 的分支。
- `platform/desktop.ts:80` `openURL`（删掉后 `import { Browser, Call, Window }` 里的 `Browser` 变成未使用标识符，**必须一并从 import 里移除**，否则 `tsc --noEmit` 可能因 `noUnusedLocals` 报错）。
- `product.ts` 中删除后**在全前端再无引用**的字段：`build`、`website`、`repository`。`releases` 在本轮开始前就已经是死字段（无人引用），**一并删除**。保留 `name`、`edition`、`version`（TitleBar 在用）。

**about-assets-css**
- i18n：zh 段 `:68 settings_about`、`:72 settings_sponsorship_text`、`:98 nav_about`、以及 `about_intro`/`about_notice_title`/`about_notice_text`/`about_signpath_title`/`about_sponsorship_title`/`about_wechat`/`about_alipay`；en 段对应项（`:456`、`:460`、`:486`）。⚠️ 删前必须逐个 grep 确认**除 AboutPage 外无其他引用**，并在报告中列出证据。
- `app.css`：`4929-4950`、`:5060-5195`、`:5261-5280` 区段的 `.about-*` / `.signpath-*` / `.sponsor-section` / `.payment-*` 规则，以及 `:4786` 的共享选择器组里 `.about-page,` 那一行、`:5072`/`:5076` 的 `.about-page ...` 规则。⚠️ `.hm-card` 被全应用共用，**绝不可删**；共享选择器组只摘掉属于关于页的那一项，保留其余选择器。
- `public/support/wei.png`、`public/support/zhi.jpg`、`public/support/SignPath/`（SignPath logo 只被 AboutPage 引用）。**`public/support/icon.ico` 必须保留**（应用图标源文件）。

## 完成判据

1. `cd desktop/frontend` 后 `node node_modules\typescript\bin\tsc --noEmit` 无输出。
2. `node node_modules\vitest\vitest.mjs run` 全部通过；**用例总数会下降**（AboutPage.test.tsx 整个文件删除），这是预期，报告里写出新总数即可。注意该命令偶发误报 exit 1，用 `"vitest exit=$LASTEXITCODE"` 复核。
3. 全前端 grep `about`/`About`/`openURL`/`support/(wei|zhi|SignPath)` 后，除 `app.css` 的删除痕迹、历史文档与本次报告外**无残留引用**。
4. 中英文 i18n **键集合完全一致**（本仓有既存测试守这条，不要引入新的不一致）。
5. 不新增任何依赖、不改 `package.json`。