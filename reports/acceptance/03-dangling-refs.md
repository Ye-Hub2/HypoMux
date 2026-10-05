# 03 · 全仓死引用与孤儿残留审计

- 审计对象：`<repo>`，HEAD = `fc821b16bd55630ac6cba4311179e1a1b6644ded`
- 审计范围：Go / TS / TSX / JSON / CSS / YAML / Markdown / workflows / tools / engine / portable-package / docs / i18n
- 审计方式：只读。未修改任何源码，未 `git add` / `git commit`。
- 基线验证：`go -C desktop build ./...` exit 0；`go -C desktop vet ./...` exit 0；`go -C desktop test ./...` 7 个包全 `ok`；`tsc --noEmit` exit 0；`vitest run` → `Test Files 44 passed (44)` / `Tests 409 passed (409)`。

---

## 验收结论

**有条件通过**

代码层不存在任何断引用。被删除的更新器（`desktop/internal/services/updater*.go`、`desktop/cmd/update-manifest-sign/`、内置 Ed25519 公钥）、关于页（`desktop/frontend/src/pages/AboutPage.tsx`）与内置赞助二维码（`desktop/frontend/public/support/{wei.png,zhi.jpg,SignPath/SignPath.png}`）在 Go 侧、TS/TSX 侧、i18n、CSS、CI 工作流里都已同步收尾，且有反向契约测试持续守护（见 `desktop/installer_layout_test.go:322-355`、`:495-500`）。

**不通过的唯一理由是对外文档**：中文 `README.md` 与 `docs/RELEASE_VERSIONING.md` 已改写为"本 fork 不提供应用内自动更新"，但**英文 `README_EN.md` 完全未被本次改动触碰**，仍对外宣称 stable/preview 更新渠道、更新检查与 Ed25519 更新清单；当前版本 2.7.0 的发布说明也仍在描述已删除的自动更新链路。这是「文档/实现不一致」，对「git clone 后验收」的用户是实打实的误导。

---

## 残留清单

### A. 已清理干净（正常，不算残留）

| 线索 | 出现位置 file:line | 是否问题 | 建议动作 |
| --- | --- | --- | --- |
| `updater` / `Updater` / `UpdateService` / `CheckForUpdate` / `ApplyUpdate` / `installAndQuit` 在 Go 源码中的残留 import 或调用 | `desktop/main.go`、`desktop/internal/services/*`、`desktop/cmd/*` 全部 0 命中；`go build` / `go vet` / `go test` 全绿 | 否 | 无需动作 |
| `updater` / `updaterMethod` / `appServices.updater` / `ReleaseInfo` / `UpdateCheckResult` / `UpdateProgress` / `CompleteAppSettings.update_channel?` 在前端残留 | `desktop/frontend/src/platform/services.ts` 已删除，全仓 0 引用，`tsc --noEmit` exit 0 | 否 | 无需动作 |
| `updater` 单词残留于 TS/TSX | 仅剩同名无关变量：`platform/settingsQueue.ts:24,29`（`attach((updater) => …)`）、`platform/settingsQueue.test.ts:37,45-46`、`pages/SettingsPage.tsx:314`、`pages/RoutingPage.tsx:344,666,675,681,692,705,726`（`updateRule` 局部变量）。与更新器无关 | 否 | 无需动作 |
| `crypto/ed25519` | `engine/internal/proxy/steam_cdn_test.go:7,459` —— Steam CDN 清单验签，与应用内更新器无关 | 否 | 无需动作 |
| `update_channel` 字面量 | 仅 `desktop/internal/services/settings_fields_test.go:11,19,26,28-29` `TestLegacyUpdateChannelFieldIsIgnored` —— 有意保留的回归护栏：老 `settings.json` 里的 `update_channel` 必须被忽略，且设置页不得再写该字段 | 否 | 无需动作（建议保留） |
| CI 中 `update-manifest-sign` / `latest.json` / `latest.json.sig` / `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` / `update_manifest_ed25519_public_key.txt` / `steps.version.outputs.channel` | `.github/workflows/` 4 个 workflow 全仓 grep：除下方注释外 0 命中 | 否 | 无需动作 |
| CI 里残留的 `latest.json` / `update channel` 字样 | `.github/workflows/build.yml:510`、`:535` —— 均为解释"本步骤不产出更新渠道产物"的注释 | 否 | 无需动作 |
| CI 契约测试本身 | `desktop/installer_layout_test.go:322-355`（遍历 `build.yml` / `release-smoke.yml` / `create-release-tag.yml`，禁止 9 个 token）、`:495-500`（只读性 + updater-free）—— 测试通过即证明 CI 引用完整；`create-release-tag.yml` 确实存在 | 否 | 无需动作 |
| `release-version` CLI 输出与 CI 消费对齐 | `desktop/cmd/release-version/main.go:66` 去掉 `channel=%s`；`make_latest` / `prerelease` 仍被 `build.yml:589-595,658-659,669-670` 消费 | 否 | 无需动作 |
| `releaseversion` 孤儿导出 | `desktop/internal/releaseversion/version.go` 删 `Channel()` 后，`:65` 仅剩一条提及旧 updater 的注释（`// Key retains the old updater's two-to-four-part numeric versions…`），无未使用导出 | 否 | 可选：把注释里的 "old updater" 改为 "historical" 以免误读 |
| `HM-E1601` / `HM-E1602` / `"about:"` 错误键前缀 | `desktop/frontend/src/components/notifications/errorCodes.ts` 已删两条规则；`desktop/frontend/src` 全仓 0 处引用 | 否 | 无需动作 |
| `ADAPTER_VISIBILITY_EVENT` 事件总线残留 | 三处同时删除：`state/adapterVisibility.ts`、`state/useEngineState.ts:16`（import）与 `:352-359`（监听 useEffect）、`pages/SettingsPage.tsx`（import + dispatch）。改由 `HomePage.tsx:140-153` `toggleVirtualAdapterVisibility` 乐观更新 + 失败回滚。全仓 0 残留引用 | 否 | 无需动作 |
| `platform/desktop.ts` 删 `openURL` 后的孤儿 import | `desktop/frontend/src/platform/desktop.ts:1` 仅 `import { Call, Window } from "@wailsio/runtime"`，无 `Browser` 残留；`ignoreOutsideWails` 仍被 9 处使用 | 否 | 无需动作 |
| `product.ts` / `TitleBar` 展示串 | `desktop/frontend/src/product.ts` 只剩 `name` / `edition` / `version`；`components/shell/TitleBar.tsx:56` 改为 `` `${productInfo.name} ${productInfo.edition[locale === "en" ? "en" : "zh"]}` ``。无 `website` / `repository` / `releases` / `build` 残留引用 | 否 | 无需动作 |
| `react-markdown` / `remark-gfm` 依赖 | `desktop/frontend/package.json` 已删，`pnpm-lock.yaml` 同步；`tsc --noEmit` + 409 个 vitest 用例全过，无 import 残留 | 否 | 无需动作 |
| About 页 i18n 键残留 | `desktop/frontend/src/i18n/legacy.messages.json`：本次同步删除 30 个键的中英双份（`about_*`、`nav_about`、`settings_about`、`settings_sponsorship_text`、`settings_sponsorship_title`），文件内 0 处 `about_`/`update_`/`signpath` 残留 | 否 | 无需动作 |
| i18n 中英配对 / 悬空引用 | `zh` = 372 键、`en` = 372 键，`zh-only = []`、`en-only = []`；74 个源文件里所有 `t("…")` 引用键 100% 存在于 JSON（missing = 0）。`i18n.tsx:61` 的 `?? key` 兜底虽会静默回退，但本次无键缺失 | 否 | 无需动作 |
| About/update CSS 类残留 | `desktop/frontend/src/app.css` 同步删除 `.about-page`/`.about-layout`/`.about-brand`/`.about-app-icon`/`.about-product`/`.about-copy`/`.about-actions`/`.signpath-section`/`.signpath-logo`/`.sponsor-section`/`.payment-grid`/`.payment-item`/`.update-dialog`/`.update-summary`/`.update-notes`；新增 `.network-virtual-toggle` 被 `pages/HomePage.tsx:199-209` 使用 | 否 | 无需动作 |
| README 赞助图片断链 | `README.md:44,240,245` 与 `README_EN.md:42,238,243` 指向**仓库根** `support/`，`support/wei.png`、`support/zhi.jpg`、`support/SignPath/SignPath.png` 均仍被 git 跟踪且文件存在（`git ls-files -- support` 4 项）。被删的只是 `desktop/frontend/public/support/` 下的应用内副本，不影响 README | 否 | 无需动作 |
| `desktop/frontend/public/support/` 空壳 | 现仅剩 `support/icon.ico`（被应用图标/托盘继续引用），符合预期 | 否 | 无需动作 |
| `docs/RELEASE_VERSIONING.md` 与实现一致性 | `:44-46` 已改写为「本 fork 不提供应用内自动更新」，`:40` 步骤 4、`:23` 同步目标、`:48` 手动升级方式全部对齐 | 否 | 无需动作 |
| `README.md`（中文）与实现一致性 | `:24`（删「支持正式版／预览版更新渠道」）、`:37`（删「更新检查」）、`:50`（改为「本 fork 不包含应用内自动更新」）、`:59`（隐私政策删掉 signed update channel）、`:212`（Smoke Test 描述改为只核对 Tag 与 CNB Release 访问） | 否 | 无需动作 |
| `tools/support/` 脚本 | `Repair-HypoMuxUpgrade.ps1`、`Start-HypoMux-Repair.cmd`、`使用说明.txt` 对 update/about/赞助关键词 0 命中 | 否 | 无需动作 |
| `protocol/v1/` 协议 | 仅 `manifest.json` / `README.md` / `fixtures/messages.json`，无更新相关协议；`protocol/v1/README.md:109` 的 "live updates" 指导引擎调用方升级能力，与应用内更新器无关 | 否 | 无需动作 |
| `website` 目录 | git 中是 submodule（`git ls-files -s website` → mode `160000` gitlink），本地未初始化，不含本仓内容 | 否（不适用） | 无需动作 |
| `portable-package/` 文本文件 | `便携版说明.txt:56-59,72` 的"自动"仅指路由/网卡生命周期，与更新器无关 | 否 | 无需动作 |

### B. 确认为残留（问题）

| 线索 | 出现位置 file:line | 是否问题 | 建议动作 |
| --- | --- | --- | --- |
| **英文 README 全篇未同步**：仍对外宣称 stable/preview 更新渠道 | `README_EN.md:24`（`…bundled sing-box 1.14.2; stable/preview update channels.`） | **是（阻塞）** | 照 `README.md:24` 改写，删去更新渠道从句 |
| 同上：仍宣称"更新检查" | `README_EN.md:37`（`…live connections, support logs, and update checks.`） | **是（阻塞）** | 照 `README.md:37` 改写 |
| 同上：仍宣称 Ed25519 签名更新元数据 | `README_EN.md:50`（`…Automatic update metadata is delivered through a separate signed update channel and verified with Ed25519, followed by installer size, SHA-256, and Windows Authenticode verification.`） | **是（阻塞）** | 照 `README.md:50` 改写为"本 fork 不包含应用内自动更新，升级需自行前往发布页" |
| 同上：隐私政策仍列「检查官方签名更新渠道」 | `README_EN.md:59`（`…forward traffic, check the official signed update channel, download installers, and validate connectivity.`） | **是（阻塞）** | 照 `README.md:59` 改写 |
| 同上：Smoke Test 仍宣称校验更新签名密钥与已有更新渠道 | `README_EN.md:212`（`…can validate the update signing key, synchronized GitHub/CNB tags, CNB Release access, and any existing signed update channel without creating a Release…`） | **是（阻塞）** | 照 `README.md:212` 改写 |
| **当前版本 2.7.0 的发布说明仍描述已删除的自动更新链路**（`desktop/frontend/src/product.ts` 的 `version` 即 `2.7.0`，且 `README.md:26` 直接链接该文件） | `.github/release-notes/v2.7.0.md:106`、`.github/release-notes/v2.7.0.en.md:106`、`.github/release-notes/v2.7.0.md:97`、`.github/release-notes/v2.7.0.en.md:97`（`…自动同步界面、Core、更新器及 Windows 安装包元数据…` / `…the UI, Core, updater, and Windows metadata`） | **是（阻塞）** | 追加一段「自定义版已移除应用内自动更新」的说明，或改写这两条 |
| **文档索引仍指向已不存在的「更新渠道选择」** | `docs/README.md:3`（`- [版本与发布渠道](RELEASE_VERSIONING.md)：正式版、Beta / RC、版本同步命令与更新渠道选择。`）——目标章节已在 `docs/RELEASE_VERSIONING.md:44` 改名 | 是 | 改索引描述为「版本同步命令与手动升级说明」 |
| **迁移档案仍把已删能力记为 Implemented / Wired**（历史记录，非断链，但会误导"当前能力"判断） | `docs/migration/ui-migration-matrix.md:113-120,131`（关于页 5 行 + 更新 Dialog 3 行，标 Implemented/Wired，引用已不存在的 `about_page.py` / `update_checker.py`）；`docs/migration/wails-architecture.md:28,217,230,261`（`type Updater interface`、目录树 `updater.go`）；`docs/migration/feature-inventory.md:188,208,448`；`docs/migration/migration-plan.md:228,230`；`docs/migration/functional-sprint-changelog.md:6,50,54,78,85`；`docs/validation/wails/visual-redesign-audit.md:14`；`docs/validation/wails/visual-foundation.md:6` | 是（低） | 在 `docs/migration/` 与 `docs/validation/wails/` 顶部加一行失效标注：「本文件为历史迁移记录，关于页与应用内自动更新已在自定义版移除」 |
| **本地生成的 Wails 绑定仍带幽灵字段** | `desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/models.ts:39`（`"update_channel"?: string;`） | 否（仓库层面） | 该路径被 `desktop/.gitignore:9`（`/frontend/bindings/`）忽略，`git ls-files -- desktop/frontend/bindings` = 0 项，fresh clone 会重新生成；本地需重跑 bindings 生成后再做类型核对，避免误导 |
| **本次改动之前就存在的孤儿 CSS 类**（`git show HEAD:desktop/frontend/src/app.css` 确认 `.hotspot-qr` 在 HEAD 的 5653-5654 行即已存在） | `desktop/frontend/src/app.css:5411-5412`（`.hotspot-qr` / `.hotspot-qr > button`；组件只用 `hotspot-qr-toggle`（`components/HotspotPanel.tsx:226`）与 `hotspot-qr-content`）；`desktop/frontend/src/components/vnic/vnic.css:4-6`（`.virtual-adapter-section` / `.virtual-adapter-body`，全仓 0 引用；本轮新增的 `.virtual-adapter-detail` 已被 `components/vnic/HyperVAdapterPanel.tsx:563` 正确使用）；`desktop/frontend/src/app.css` 的 `nat-server-*` 7 个类（0 引用） | 是（低，非本次引入） | 后续清理提交删除；不影响本次验收。注意 `nat-result-success/danger/warning` 是 `pages/NATDetectionPage.tsx:525` 模板串动态拼接，属静态扫描误报，不要删 |
| **i18n 孤儿键存量**：`legacy.messages.json` 372 键中约 186 个没有任何 `t("…")` 调用（`window_title`、`diag_*`、`routing_*`、`tun_*`、`settings_theme_color_*` 等） | `desktop/frontend/src/i18n/legacy.messages.json` | 是（低，非本次引入） | 文件本名即 `legacy`；且 186 个孤儿键中无任何 about / update / 赞助键 → 本次删除**未制造新孤儿**。建议另开清理任务 |
| `settings_version`（`当前版本: v{version}`）在 AboutPage 删除后无人调用 | `desktop/frontend/src/i18n/legacy.messages.json`（`settings_version`） | 否 | `AboutPage.tsx` 在 HEAD 下使用的是 `about_update_current` 等键，并未消费 `settings_version` → 该键在删除前就已是孤儿，非本次造成 |

---

## 阻塞问题

1. **`README_EN.md` 五处仍在宣传已删除的自动更新** —— `README_EN.md:24`、`README_EN.md:37`、`README_EN.md:50`、`README_EN.md:59`、`README_EN.md:212`。中文 `README.md` 的对应五行（`:24`/`:37`/`:50`/`:59`/`:212`）已全部改写，英文版被整份漏掉。用户按英文 README 会期待存在"检查更新"入口与 stable/preview 更新渠道，而本 fork 已完全移除。
2. **当前版本 2.7.0 的发布说明仍描述已删除的 Ed25519 自动更新链路** —— `.github/release-notes/v2.7.0.md:106`、`.github/release-notes/v2.7.0.en.md:106`（"自动更新继续校验 Ed25519 清单…" / "Automatic updates retain Ed25519 manifest…"），以及 `:97` 两版（提到 `更新器` / `updater`）。`README.md:26` 直接把用户引向该文件。

两项均为文档问题，不影响编译与运行；但对"验收 git clone 后下载下来的所有改动"的用户构成事实性误导，建议合并前修复。

---

## 风险与建议

**代码层可以放心。** 四条独立验证全绿：`go build` / `go vet` / `go test ./...`（7 包 ok，含 `installer_layout_test.go` 契约测试）、`tsc --noEmit`、`vitest run`（44 文件 409 用例）。`go test` 中 `desktop/internal/services/settings_fields_test.go:11` 明确覆盖了"老配置里的 `update_channel` 必须被忽略"，升级用户不会因旧 `settings.json` 崩溃。

**风险与建议（按优先级）**

1. **CI 防回归能力已具备，建议在 README 修复后补一条同源护栏。** `desktop/installer_layout_test.go:322-355` 已经把 9 个 update-channel token 变成硬断言，但没有对 `README_EN.md` / `README.md` / `.github/release-notes/*` 做文档侧断言 —— 这正是本次漏改英文 README 的根因。可在同文件追加 `TestDocsCarryNoInAppUpdaterClaims`，禁止 `README*.md` 出现 `update channel` / `Ed25519` / `update checks`。
2. **历史文档降噪。** `docs/migration/*`（8 个文件、约 20 处）与 `docs/validation/wails/*` 属迁移档案，提到旧功能属正当历史提及，不构成断链；但在没有失效标注的情况下，后续审计会反复误报。建议统一加一行顶部声明，而不是逐条删除（删除会破坏迁移可追溯性）。
3. **本地 bindings 需要重新生成。** `desktop/frontend/bindings/.../models.ts:39` 仍有 `"update_channel"?: string;`。它被 `desktop/.gitignore:9` 忽略、不在版本控制内，fresh clone 不会出现；但本地开发者若直接读该文件会误以为字段仍受支持。建议执行一次 bindings 生成再复核。
4. **孤儿 CSS 与孤儿 i18n 键是存量债，不是本次回归。** `.hotspot-qr`（`app.css:5411-5412`）、`.virtual-adapter-section` / `.virtual-adapter-body`（`vnic.css:4-6`）、`nat-server-*` 在 HEAD 即已存在；186 个 i18n 孤儿键同理，且本次删除未新增任何孤儿。建议合并后另开一次卫生清理，与本次验收解耦。
5. **`HideVirtualAdapters` 默认值 true→false 是一次行为变更（默认显示虚拟网卡），与删功能无关。** 已在 `DefaultSettings` / `RollbackLegacyMigration` / `reload()` 三处统一到 `defaults.HideVirtualAdapters`，`useEngineState.ts:175,295` 的 `?? true`→`?? false` 也一致，无残留旧默认值。但对老用户而言这是一次可见的界面变化，建议在发布说明中显式提及。

---

## 覆盖缺口

- **未做实机 / 视觉验收。** `README.md` / `README_EN.md` 引用的 `assets/screenshot_*.png` 是否仍展示已删除的"关于页 / 检查更新"界面，无法靠静态扫描判定，需截图复核。
- **未审 `engine/`（Rust）模块内部语义**，仅做关键词扫描。`crypto/ed25519` 在 `engine/internal/proxy/steam_cdn_test.go:7,459` 命中，经确认是 Steam CDN 验签，与应用内更新器无关；但引擎内部是否存在其他形式的"更新"概念未逐行审查。
- **`website` 为未初始化的 submodule**（`git ls-files -s website` → mode `160000`，本地为空目录），其内容不在本仓，未审。
- **未实跑 CI。** `build.yml` / `release-smoke.yml` / `create-release-tag.yml` 的引用完整性只由 `desktop/installer_layout_test.go` 的静态字符串断言覆盖，未实际执行 GitHub Actions / CNB 发布流水线，SignPath 签名链路与双镜像上传未验证。
- **`portable-package/` 内二进制包未核。** 仅审了 `rebuild.ps1` 与 `便携版说明.txt` / `SHA256SUMS.txt`；包内 EXE 的资源、版本信息与是否仍捆绑旧签名元数据未检查。
- **`docs/validation/wails/` 下的截图证据未复核**，其中的 UI 截图可能已过时。
- **孤儿 Go 导出只做了针对性核查**（`releaseversion`、被删的 `services` 包、`hyperv_adapter`），未对全仓所有导出符号做穷举式"无调用者"扫描 —— Go 生态下导出符号可能被 Wails 反射调用，静态判定会产生大量误报。
