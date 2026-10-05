# 关于页摘除 · 成员 `about-assets-css` 交付报告

日期：2026-10-05 ｜ 分支：main ｜ 契约：`reports/about-removal/SCOPE.md`
写作用域：`desktop/frontend/src/app.css`、`src/i18n/legacy.messages.json`（删）+ `public/support/{wei.png,zhi.jpg,SignPath/}`（删）
**未越界**：未碰 `App.tsx`、`CompactNavigation*`、`platform/desktop.ts`、`product.ts`、`AboutPage*`、`bindings/**`、`desktop/main.go`、`TitleBar.tsx`、仓库根 `support/`、`package.json`；未 `git add`/`git commit`。

---

## 1. 结论速览

| 项 | 结果 |
| --- | --- |
| i18n 删除 | zh/en 各 **14** 个键 → 全库 **372 键**，中英键集合与顺序完全一致 |
| `app.css` | 净删 242 行（`17 insertions / 259 deletions`），花括号 1143/1143 平衡，无空规则块 |
| 静态资源 | 删 `wei.png`、`zhi.jpg`、`SignPath/`（整个目录）；**保留 `icon.ico`** |
| `tsc --noEmit` | 无输出，exit 0 |
| `vitest run` | **44 files / 409 tests 全通过**，exit 0 |

---

## 2. 删除前的零引用证据（契约要求项）

用 `git grep … HEAD -- desktop/frontend/src` 搜**提交态**（比工作区更有说服力：能同时证明「历史遗留死键」）：

| 键 | zh / en（工作区行号） | HEAD 里的代码引用 | 判定 |
| --- | --- | --- | --- |
| `about_intro` | 327 / 703 | `AboutPage.tsx:153,167` | 随 AboutPage 一起消失 ✅ |
| `about_notice_title` | 345 / 721 | `AboutPage.tsx:182` | 同上 ✅ |
| `about_notice_text` | 346 / 722 | `AboutPage.tsx:183` | 同上 ✅ |
| `about_signpath_title` | 347 / 723 | `AboutPage.tsx:189` | 同上 ✅ |
| `about_sponsorship_title` | 349 / 725 | `AboutPage.tsx:207` | 同上 ✅ |
| `about_wechat` | 350 / 726 | `AboutPage.tsx:211` | 同上 ✅ |
| `about_alipay` | 351 / 727 | `AboutPage.tsx:216` | 同上 ✅ |
| `settings_sponsorship_text` | 72 / 448 | `AboutPage.tsx:208` | 同上 ✅ |
| **`about_open_github`** | 356 / 744 | **零引用**（HEAD 也是零） | 追加删除项，lead m00318 指定 ✅ |
| **`about_signpath_text`** | 360 / 748 | **零引用**（AboutPage 用的是内联文案） | 追加删除项 ✅ |
| **`about_qr_missing`** | 364 / 752 | **零引用** | 追加删除项 ✅ |
| `nav_about` | 98 / 474 | `CompactNavigation.tsx:123,127` | **about-shell 作用域**，其删除导航项后失效 ✅ |
| `settings_about` | 68 / 444 | **零引用**（HEAD 也是零） | 契约指定项 ✅ |
| **`settings_sponsorship_title`** | 70 / 445 | **零引用**（HEAD 也是零） | ⚠️ 见 §5 越界申报 |

结论：**除 AboutPage（本次整体删除）与 CompactNavigation（about-shell 正在删除）外，没有任何第三方引用**；`settings_about` / `settings_sponsorship_title` / 3 个追加 `about_*` 键在删之前就已经是零引用的死键。

---

## 3. `app.css` 改动明细

**整条删除**（均为关于页专属）：`.about-layout`（含 2 处 `grid-template-columns`）、`.about-page .page-heading .section-kicker` / `p`、`.about-layout h2`、`.about-brand` 及 `.about-app-icon` / `> div` / `h2` / `strong` / `span`、`.about-product > p`、`.about-product .about-actions` 两条、`.signpath-section` 3 条、`.signpath-logo`、`.signpath-section, .sponsor-section`、`.payment-grid`、`.payment-item` 4 条。

**共享选择器组只摘掉属于关于页的那一项**（保留其余选择器，避免影响他人页面）：

| 位置 | 改前 | 改后 |
| --- | --- | --- |
| `app.css:4784` | `.tools-page, .settings-page, .about-page, .blocked-domains-page` | `.tools-page, .settings-page, .blocked-domains-page` |
| `app.css:4928` | `.settings-layout, .about-layout` | `.settings-layout` |
| `app.css:4934` | `.settings-section, .about-product, .about-copy, .signpath-section, .sponsor-section` | `.settings-section` |
| `app.css:4946` | `.settings-section > h2, .about-layout h2` | `.settings-section > h2` |
| `app.css:5059` | `.settings-actions, .about-actions` | `.settings-actions` |
| `app.css:5122` `@media (max-width: 880px)` | `.setting-row, .about-layout` / `.signpath-section` / `.payment-grid` | 仅 `.setting-row`；`.setting-control` 原样保留 |

**`.hm-card` 未动**（契约第 38 行红线）：`app.css:143-220` 及 `:5523` 共 23 处 `hm-card` 规则全在，`ProductMark`/各页面卡片样式不受影响。

**未触碰他人未提交改动**：`git diff app.css` 的新增行里 `.network-virtual-toggle`（`:143` 附近那 9 行）是本会话之前就存在的 vNIC 改动，不是我加的。

**结构性校验**：`{`=1143、`}`=1143 平衡；正则扫 `/\{\s*\}/` 空规则块 **0** 个；全文件 `grep about|signpath|sponsor|\.payment` **零命中**。

---

## 4. 静态资源

```
desktop/frontend/public/support/
├── icon.ico   16609 B   ← 保留（ProductMark.tsx:3 <img src="/support/icon.ico">，应用图标源文件）
（wei.png 102482 B、zhi.jpg 108517 B、SignPath/SignPath.png 3956 B 已删）
```

删除前先用 `Resolve-Path` 校验三个目标的绝对路径都落在 `public\support\` 之内（含 `SignPath` 目录整体），再 `Remove-Item`；删后 `Get-ChildItem public -Recurse` 结果仅剩 `support\icon.ico`。

**仓库根 `support/` 原样保留**（契约第 14 行红线）：`support/{icon.ico, wei.png, zhi.jpg, SignPath/SignPath.png}` 四个文件均未被触碰——README.md / README_EN.md 仍能引用到。

---

## 5. 需要 lead 处理 / 复核的项

1. **⚠️ 超出契约清单的一次删除，请复核**：`settings_sponsorship_title`（zh `:70` / en `:445`）不在契约第 37 行的清单里，也不在 lead m00318 的追加 3 个键里。我一并删了，理由：`git grep HEAD` 证明它**代码零引用**（本仓 `settings_about` 同为死键），语义上属赞助/关于页残留，AboutPage 删除后不可能再有调用方。若你认为应保留，请告知，我可在 1 分钟内恢复两行。
2. **残留（非我作用域，仅报告）**：`src/components/shell/CompactNavigation.test.tsx:20` 仍出现字符串 `nav_about`——那是 about-shell 用来断言「关于按钮已不存在」的**反向断言**，属预期保留，不是漏网引用。
3. `settings_sponsorship_title` 删除后 `settings_*` 段只剩 `settings_version` / `settings_lang_saved` 等仍在用的键，中英仍严格一致（见 §6 脚本输出）。

---

## 6. 验证记录（本次改动后重跑）

```
$ node node_modules\typescript\bin\tsc --noEmit
tsc exit=0                       # 无任何输出

$ node node_modules\vitest\vitest.mjs run
Test Files  44 passed (44)
     Tests  409 passed (409)
vitest exit=0
```

用例数由上一轮的 45 files / 411 tests 变为 44 files / 409 tests：`AboutPage.test.tsx`（4 例）随页面整体删除而减少，与契约第 44 行的预期一致；未新增任何用例，未改 `package.json`，未引入依赖。

> 附带观察：vitest 输出里有一行 `ReferenceError: NodeFilter is not defined`，出自某个三方库（非本仓 `src`，全仓 grep 零命中），属**既存噪音**，不导致任何用例失败，与本任务无关。

i18n 一致性脚本（每次改完都跑）：

```
$ node -e "…JSON.parse(legacy.messages.json)…"
zh 372 en 372 onlyZh [] onlyEn [] orderEqual true
```

残留扫描（全前端 `src`）：

```
openURL / About / about  → 仅 vNIC 文件里的英文散文注释 "…warns about…"、
                            以及 CompactNavigation.test.tsx:20 的反向断言
support/                  → 仅 ProductMark.tsx:3 的 /support/icon.ico（必须保留）
public/                   → 仅 support\icon.ico
```