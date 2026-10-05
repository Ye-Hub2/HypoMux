# 07 · 未跟踪文件 / 提交卫生 / 变更台账核验

- 核验人：`repo-hygiene`（验收团队）
- 核验时间：2026-10-04
- 仓库：`<repo>`
- HEAD：`fc821b16bd55630ac6cba4311179e1a1b6644ded`
- 分支：`main`，与 `origin/main` 同步（无 ahead / behind）
- 范围约束：**只读核验**。全程未执行 `git add / commit / reset / checkout / clean / stash`，未修改任何被跟踪文件。

---

## 验收结论

**有条件通过。**

代码侧质量已实测达标，可交付：

| 验证项 | 命令 | 实测结果 |
|---|---|---|
| Go 单元测试 | `go test ./...`（desktop/） | **exit 0**，耗时 65.23s；`internal/services 48.211s` 为最慢包 |
| Go 格式化 | `gofmt -l`（engine + desktop 全部 329 个 .go） | **0 个未格式化**（报告 F-1 已修复） |
| 前端单测 | `node node_modules/vitest/vitest.mjs run` | **exit 0**，`Test Files 44 passed (44)`、`Tests 409 passed (409)`，37.06s |
| 前端类型 | `node node_modules/typescript/bin/tsc --noEmit` | **exit 0**，无输出 |

交付侧存在 **5 项必须先处理的问题**（见「阻塞问题」），全部属于提交卫生与信息泄露，**不涉及代码正确性**。处理完即可提交。

**关于未跟踪文件的结论修正**：任务初稿称「全部未跟踪文件都在 `reports/` 下」，经复核**该结论不成立** —— 另有 1 个未跟踪文件在源码树内（`desktop/cover_hyperv`），详见 B-1。

---

## 未跟踪文件盘点

### 权威口径

```
git -c core.quotepath=false ls-files --others --exclude-standard   →  60 项
```

> ⚠️ **不要用 `git check-ignore -v reports/` 判断是否被忽略。** 该命令对一个**完全不存在**的目录 `zzz_nonexistent_dir_xyz/` 同样返回 `.gitignore:72:` + TAB + 路径名且 **rc=0**（`.gitignore` 第 72 行实为空行，且全文检索 `report` 零命中）。这是该 Git 版本 `-v` 输出的伪匹配。用它做判断会得出「reports/ 已被忽略」的错误结论。唯一权威口径是 `--others --exclude-standard`（逐文件走 `--exclude-standard`）与 `git check-ignore -v -- <具体文件>`（`reports/vnic/90-verification.md` 实测 **rc=1，未被忽略**）。

### 汇总

| 分组 | 数量 | 性质 | 含敏感信息 | 建议 |
|---|---:|---|---|---|
| `reports/vnic/*` | 41 | 验收/验证报告（38 .md） | ⚠️ 有（4 个文件含本机路径/主机名） | **提交**，提交前脱敏 |
| `reports/vnic/_phase*.txt` | 3 | 临时日志（go test / data dir / e2e 原始输出） | 否 | **删除**（详见下表） |
| `reports/updater-removal/*` | 6 | 验收报告与 go test 输出 | 否 | **提交**（1 个需转 UTF-8） |
| `reports/parts/*` | 6 | 分阶段实施报告 | 否 | **提交** |
| `reports/about-removal/*` | 4 | 验收报告（README / SCOPE / assets-css-i18n） | 否 | **提交** |
| `reports/acceptance/06-ci-release-docs.md` | 1 | 同批次验收组产出 | 否 | **提交** |
| `reports/HypoMux-项目分析报告.md` | 1 | 项目分析报告 | 否 | **提交** |
| **`desktop/cover_hyperv`** | **1** | **Go 覆盖率剖析文件（构建残留）** | 否 | **删除或加入 .gitignore** — B-1 |

合计 **60** 个未跟踪项（59 份 reports/ 文档 + 1 个构建残留）。

### 明细：需要处置的文件

| 路径 | 性质 | 是否含敏感信息 | 建议 |
|---|---|---|---|
| `desktop/cover_hyperv` | Go coverage profile（首行 `mode: set`，4920 行，459,303 B，mtime 2026-10-04 21:58） | 否 | **删除**。未被任何 workflow/脚本引用（检索 `.github/workflows/*.yml`、`*.ps1`、`*.md` 零命中），也未被 `.gitignore` 覆盖（`check-ignore` rc=1），会被 `git add -A` 扫入 |
| `reports/vnic/_phase2_gotest.txt` | 临时 go test 阶段日志 | 否 | **删除**（UTF-16LE+BOM，Git 会当二进制原样存储） |
| `reports/vnic/_phase3_datadir.txt` | 临时 data dir 阶段日志 | 否 | **删除**（UTF-8-BOM + CRLF） |
| `reports/vnic/_phase3_e2e.txt` | 临时 e2e 阶段日志 | 否 | **删除**（UTF-16LE+BOM） |
| `reports/vnic/21-engine-e2e.md:55, 264-271` | 验证报告 | ⚠️ **是** — 含主机名 `<HOST>`、本机用户目录、vEthernet 地址 `192.168.16.<masked>` | 提交前脱敏 |
| `reports/vnic/60-hyperv-cmd-surface.md:49` | 验证报告 | ⚠️ **是** — 含主机名 + `<ADMIN>` | 提交前脱敏 |
| `reports/vnic/76-inventory-repro.md:964` | 验证报告 | ⚠️ **是** — 含主机名 + `<ADMIN>`；另为 CRLF/LF 混排行尾 | 脱敏；行尾由 `.gitattributes` `*.md text eol=lf` 自动归一 |
| `reports/vnic/79-e2e-regression.md:179` | 验证报告 | ⚠️ **是** — 含主机名 + `<ADMIN>`；另为 CRLF/LF 混排行尾 | 脱敏；同上 |
| `reports/updater-removal/backend-gotest.txt` | go test 输出快照 | 否 | **提交前转 UTF-8**：现为 UTF-16LE+BOM（1,514 B / 756 个 NUL），Git 二进制探测会原样存储，GitHub 与 diff 均显示乱码，`read` 工具直接拒绝 |

### 敏感信息扫描汇总

- **无密钥/令牌**：对全部 reports/*.md|*.txt 扫描 `ghp_` `gho_` `github_pat_` `sk-` `AKIA` `AIza` `xox[baprs]-` `-----BEGIN PRIVATE KEY-----`，**零命中**。
- 被删除的 `desktop/internal/services/update_manifest_ed25519_public_key.txt` 内容为 `cADpocBrcdxl7Ihmu2SkOZdXy9D8Hpcf5B5FjEYJNys=`，是**公钥**，报告中未逐字引用（仅 `reports/updater-removal/02-desktop-backend.md:428-432` 描述性提及），不构成泄露。
- **需脱敏的三类信息**：
  1. 本机用户目录（绝对路径）：合计 **×93 处**，分布于 **34 个**报告文件。（原文按三种路径形态分别计数，脱敏后三者统一映射为 `%USERPROFILE%`，逐条计数已失去区分意义，故合并为总数。）
  2. 主机名：`<HOST>`（4 处，见上表）。
  3. **内网真实 IP**：`Get-NetIPAddress` 显示本机物理网卡地址为 `192.168.16.<masked> 以太网` —— 即 `192.168.16.0/24` 是**真实物理局域网**，报告泄露了对端主机地址 `.<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked> / .<masked>`（分别 8/7/8/7/7/7/4 次命中）。
     对比：测试自建的 `10.66.0.1` `10.66.1.1` `10.67.0.1` `10.68.0.1` `172.25.160.<masked>` 属测试网段，无风险。

> **状态更新（本轮已闭环）**：上表与本节所述的脱敏项已全部处理完毕。本机路径 → `<repo>` / `%USERPROFILE%` / `%LOCALAPPDATA%`，主机名 → `<HOST>`，用户名 → `<ADMIN>`，真实内网 IP → `192.168.16.<masked>`，MAC → `<MAC>`，机器 GUID → `<GUID>`；并额外发现并掩码了计划外的个人邮箱、DHCP 客户端标识与 IPv6 链路本地地址。3 个 UTF-16LE txt 已转 UTF-8 无 BOM，全目录 67/67 文件编码一致、行数零回归。保留项均为产品设计网段、测试夹具与协议标识符（复核记录见 `00-final-verdict.md` 第七节）。

### 源码是否遗漏 `git add` —— 无遗漏

- 全部**新增源码均落在已跟踪文件内**，无「新建源码忘记 add」的情况。
- 唯一看起来像漏加的 `desktop/frontend/bindings/**` 下 `.ts` 文件为 Wails 自动生成物，已被 `desktop/.gitignore:9:/frontend/bindings/` 正确忽略，属预期行为。

---

## 报告与代码一致性抽查

| 报告 | 声称的结论 | 实际是否成立 | 依据 |
|---|---|---|---|
| `reports/about-removal/README.md:60-66` | 前端验收：`vitest run` 通过，`tsc --noEmit` 通过 | ✅ **完全复现** | 实测 exit 0；`Test Files 44 passed (44)`、`Tests 409 passed (409)`、`Duration 37.06s` 与报告**逐字一致**；`tsc` 无输出、`TSC_EXIT=0` |
| `reports/about-removal/README.md:14` | `app.css` +17 / −259 | ⚠️ **数字成立，归因错误** | `git diff HEAD --numstat` 确为 `17 / 259`；但这 17 行新增含 `.network-virtual-toggle { margin-right: 7px; color: var(--hm-text-tertiary); font-size: 12px; }` 及其 `.fui-Switch__label` 子选择器，属 **vNIC** 工作，非关于页 |
| `reports/about-removal/README.md`（锁文件） | 「锁文件 −904 行、纯删除、零新增行、零版本漂移」 | ✅ **成立** | numstat `0 / 904`；HEAD blob 261,423 B → 工作区 229,704 B；worktree 全文检索 `react-markdown`/`remark-gfm` **0 命中**；704 行删除为 mdast/hast/unist 子树（`react-markdown`、`remark-gfm`、`bail`、`ccount`、`micromark*`、`mdast-util-*`、`hast-util-*`、`devlop`、`longest-streak`、`markdown-table`、`zwitch` 等），对应 `package.json` 移除 `react-markdown ^10.1.0` 与 `remark-gfm ^4.0.1` |
| `reports/updater-removal/backend-gotest.txt` | `go test ./...` 全部 ok | ✅ **结构一致**（1 行已过时） | 实测 9 个包全 ok；逐行比对仅差 `cmd/update-manifest-sign 0.313s` —— 该包本轮已删除，故此行为**删除前**的历史输出。耗时漂移正常：`internal/services` 报告 46.398s vs 实测 48.211s |
| `reports/vnic/82-go-batch-delete.md` | 批量删除已实现（含受保护网卡不入脚本） | ✅ **成立** | `desktop/internal/services/hyperv_adapter.go:2093` `func (s *HyperVAdapterService) RemoveAdapters(names []string) ([]HyperVRemoveResult, error)`、`:2125` `removeAdapters(names []string, requireLedger bool)`、`:1968` `normalizeHypervRemoveNames`、`:1989` `hypervUnwrapHostInterface`、`:2012` `hypervProtectedReason`、`:2031` `hypervProtectedAdapter`；`desktop/internal/services/hyperv_adapter_test.go:2197` `TestRemoveAdaptersProtectedNeverEntersScript`，另含 17 个批量用例（`:2249` `:2280` `:2296` `:2321` `:2341` `:2368` `:2390` `:2430` `:2452` `:2506` `:2538` `:2569` `:2589` `:2614` `:2637` `:2663`） |
| `reports/vnic/90-verification.md` 门禁 6 | F-1：`settings.go` 未 gofmt 格式化，判定 **FAIL** | ✅ **缺陷已修复**（报告已过期） | 现 `gofmt -l` 覆盖 329 个 .go 文件，**0 个未格式化**。文件数由报告的 332 降为 329，因本轮删除了 8 个 .go（`updater*.go` ×6 + `update-manifest-sign/*.go` ×2），差值自洽 |
| `reports/vnic/90-verification.md` F-2 | `tun_failed` 未处理 | ⚠️ **仍开放** | `engine/internal/server/server.go:725`、`:799`、`:884`（报告引 882-887，因增删行位移） |
| `reports/vnic/90-verification.md` F-5 | `.gitignore` 漏配本地皮肤产物 | ⚠️ **仍开放** | 现 `.gitignore:74` `*.muxskin`、`.gitignore:75` `/desktop/frontend/public/skins/mux-layered-source/`（报告引 70-72，行号已位移） |
| `reports/vnic/90-verification.md` F-7 | 依赖表存在未使用项 | ⚠️ **仍开放** | `desktop/frontend/package.json:17` `"fflate": "0.8.3"`、`:18` `"pixi-live2d-display": "0.4.0"`、`:19` `"pixi.js": "6.5.10"` 仍在 |
| `reports/vnic/95-ci-run.md` | CI run `37112297270`（windows-2025，`headSha=37571e5`）`success`；§6「本地 HEAD == origin/main == 37571e5」「`git status` 仅剩 `?? reports/`」 | ⚠️ **历史属实，当前已过期** | 两个 SHA 均存在且 `git merge-base --is-ancestor 37571e5 HEAD` rc=0。提交链：`e66016e` → `37571e5` → `46680e0` → `018712d` → `fc821b1`（reflog 显示末次为 `commit (amend)`，`git diff --stat 018712d HEAD` 为空 ⇒ 改写内容中性）。当前 HEAD 为 `fc821b1`，已在其后两次提交，且工作区另有 59 个改动 → §6 两项描述均被超越，**非编造**。CI 运行记录离线无法复验（本机无 gh 认证） |
| `reports/vnic/30-real-machine-verdict.md` | 真机验证结论 | ✅ **与代码状态自洽** | 结论所依赖的批量删除、保护判定、单脚本批量执行等实现均在位（见上）；真机 Hyper-V 操作本身无法在本次核验中离线复现 |
| `reports/about-removal/README.md` 基线 | 「任务前 45 files / 413 tests」 | ❌ **基线不成立** | HEAD 的前端测试文件顶层 `it(`/`test(` 声明仅 **268** 个，工作区 **378** 个（+110）。413 这个基线不可能对应 HEAD，只能是在已含 vNIC 测试的中间树上测得。「任务后 44 files / 409 tests」则**可精确复现** |

---

## 阻塞问题

### B-1 · `desktop/cover_hyperv` 是未忽略的覆盖率产物，`git add -A` 会将其扫入提交

- 事实：未跟踪的 Go coverage profile，4920 行 / 459,303 B，首行 `mode: set`。
- 未被任何 workflow 或脚本引用（检索 `.github/workflows/*.yml`、`*.ps1`、`*.md` 零命中）。
- `.gitignore` 未覆盖（`git check-ignore -v desktop/cover_hyperv` → **rc=1**）。
- 处理：**删除该文件**，或向 `.gitignore` 增补覆盖率产物规则。

### B-2 · `reports/` 没有任何 `.gitignore` 保护，一次 `git add -A` 就会提交全部 59 份报告

- 事实：`.gitignore` 全文检索 `report` **零命中**；`git ls-files --others --exclude-standard` 将 59 份 reports 文件全部列为「未跟踪且未被忽略」。
- 冲突点：`reports/vnic/95-ci-run.md` §6 声称「符合契约 §0.4『禁止把 reports/ 加入 git』」，但该契约**仅靠当时那一条 `git add -A -- . ':(exclude)reports'` 的显式 pathspec 排除来维持**，仓库层面无任何强制机制。任何后续的 `git add -A` / `git add .` 都会一次性提交全部报告。
- 附带陷阱：如「未跟踪文件盘点」所述，`git check-ignore -v reports/` 会返回 **rc=0** 的伪匹配，无法用作判据。
- 处理：二选一 —— 在 `.gitignore` 增补 `/reports/`；或明确决定「报告入库」并撤销该契约。

### B-3 · 报告含本机身份信息与真实内网 IP

- `%USERPROFILE%`（73 处）+ `%USERPROFILE%`（19 处），涉及 **34 个**文件。
- 主机名 `<HOST>`：`reports/vnic/21-engine-e2e.md:55`、`reports/vnic/60-hyperv-cmd-surface.md:49`、`reports/vnic/76-inventory-repro.md:964`、`reports/vnic/79-e2e-regression.md:179`。
- 真实物理局域网对端地址 `192.168.16.<masked>/.<masked>/.<masked>/.<masked>/.<masked>/.<masked>/.<masked>`（本机 `192.168.16.<masked>` 位于同段物理网卡）。
- 处理：提交前统一脱敏（路径 → `%USERPROFILE%`，主机名/IP → 占位符）。仓库为**公开 fork**（`origin = https://github.com/Ye-Hub2/HypoMux`），泄露后无法撤回。

### B-4 · 3 个 UTF-16LE 文件会被 Git 当二进制原样存储

- `reports/updater-removal/backend-gotest.txt`、`reports/vnic/_phase2_gotest.txt`、`reports/vnic/_phase3_e2e.txt` 均为 UTF-16LE + BOM。
- 后果：Git 二进制探测 → 原样存储，GitHub 页面、diff、任何文本工具均显示乱码（本次核验中 `read` 工具直接判定为 binary file 而拒绝读取）。
- 处理：转存为 UTF-8 无 BOM；若判定为临时日志则直接删除（推荐）。

### B-5 · 实际是 **4 条** 改动主线，其中 4 个文件跨主线混改，且第 4 条主线无任何报告覆盖

- **此前被识别为 3 条**：vNIC / updater-removal / about-removal。
- **新发现的第 4 条 —— fork 重品牌化（自定义版）**，涉及：
  - `desktop/main.go`：`Title: "HypoMux"` → `"HypoMux 自定义版"`
  - `desktop/frontend/src/product.ts`：**新增** `edition: { zh: "自定义版", en: "Custom Edition" }`
  - `desktop/frontend/src/components/shell/TitleBar.tsx`：`HypoMux` → `${productInfo.name} ${productInfo.edition[locale === "en" ? "en" : "zh"]}`
  - 注：`product.ts` 的 +3 / −4 中，**+3 属本主线**，−4（`build`/`website`/`repository`/`releases` 外链）属 about-removal —— 该文件同样是**混改**文件。
- **跨主线混改文件（共 4 个）**：
  1. `desktop/frontend/src/App.tsx`（+17/−21）—— about-removal（AboutPage 懒加载、`"about"` 从 DEV 白名单 / pageOrder / 渲染三元中移除）+ vNIC（pageOrder 将 `"virtual-adapters"` 从 `"connections"` 之后前移到 `"home"` 之后）
  2. `desktop/frontend/src/i18n/legacy.messages.json`（+56/−60）—— 删除的 60 行中 58 行属 about/signpath/sponsor/payment/nav_about；**新增的 56 行全部**为 `virtual_adapters_*`
  3. `desktop/frontend/src/app.css`（+17/−259）—— +17 全为 vNIC 的 `.network-virtual-toggle`；−259 属 about-removal
  4. `desktop/internal/services/settings.go`（+11/−19）—— updater-removal（`UpdateChannel` 字段及其校验分支）+ vNIC（`HideVirtualAdapters` 默认值 `true` → `false`）
  5. （附带）`desktop/frontend/src/pages/SettingsPage.tsx`（+1/−30）—— updater-removal（`update_channel: "stable"` 与「更新渠道」下拉行）+ vNIC（删 `hide_virtual_adapters` 开关、删 `ADAPTER_VISIBILITY_EVENT` effect，默认翻转）
- 后果：无法用 `git add <目录或文件>` 直接切分这几条主线；强行整体提交会让提交信息失真，未来 `git bisect` / revert 粒度不可用。
- 处理：见「建议的提交拆分」的 `git add -p` 拆分方案。

### 非阻塞观察（供负责人知悉，本报告不建议本轮处理）

- **`.github/` 下 2 个 workflow 变更属 updater-removal 且带守卫测试**：`build.yml`（+8/−147）删除 `latest.json` 清单生成与签名步骤，`release-smoke.yml`（+4/−57）删除 Ed25519 私钥一致性校验；新增守卫测试 `TestReleaseWorkflowsCarryNoUpdateChannelArtifacts`（`desktop/installer_layout_test.go`）会扫描三个 workflow 断言无更新渠道产物。逻辑自洽。
- **`engine/` 与 `protocol/` 零未提交改动**：报告 10/12/13/14 声称的引擎侧 vNIC 工作**已随 fc821b1 提交**，不属本轮工作区改动。核对无误，非缺陷。
- **`bin/` 下约 92 MB 二进制为历史入库**（`bin/libcronet.dll` 9,528,832 B、`bin/sing-box.exe` 81,947,136 B、`bin/wintun.dll` 427,552 B、`bin/README.md`），本轮未改动，最近提交为 `850e53c`/`6f35566`/`5d3d56b`。与本次验收无关，仅提示仓库体积。
- **`.gitattributes` 与本轮删除无冲突**：`binary` 覆盖 `*.png`/`*.jpg`，删除 3 张图片不触发行尾/LFS 争议；`desktop/portable/*.txt text eol=crlf` 与被删的 `update_manifest_ed25519_public_key.txt` 路径无关。
- **`origin` 指向个人 fork** `https://github.com/Ye-Hub2/HypoMux`，而规范模块路径为 `github.com/Hypostasis-Cat/HypoMux/{desktop,engine}`、`.gitmodules` 中 website 指向 `Hypostasis-Cat/hypomux-web`。属于有意 fork，仅提示推送目标。

---

## 建议的提交拆分

### 总体原则

四条主线**必须拆开**。第 1–3 条可按文件直接 `git add`；**5 个混改文件需 `git add -p` 逐 hunk 选择**。第 4 条（重品牌化）虽与 vNIC 无耦合，但会与第 2 条共处 `main.go` 与 `product.ts`，需 patch 级拆分。

### 提交 1 — `feat(vnic): 新增 Hyper-V 虚拟网卡独立管理页`（主体，最大）

纯 vNIC、无混改，可整目录/整文件提交：

```
desktop/frontend/src/pages/VirtualAdaptersPage.tsx              +423  -77
desktop/frontend/src/pages/VirtualAdaptersPage.css              +116   -0
desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx         +747  -17
desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx      +391  -35
desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx +505  -32
desktop/frontend/src/components/vnic/managementAdapters.ts       +312   -2
desktop/frontend/src/components/vnic/managementAdapters.test.ts  +382   -1
desktop/frontend/src/components/vnic/vnic.css                      +4    0
desktop/frontend/src/pages/HomePage.tsx                           +37   -3
desktop/frontend/src/pages/HomePage.test.tsx                      +41   -1
desktop/frontend/src/pages/SettingsPage.test.tsx                  +9  -25
desktop/frontend/src/state/useEngineState.ts                       +5  -12
desktop/frontend/src/state/adapterVisibility.ts                     0   -2
desktop/frontend/src/platform/services.ts                         +35  -35
desktop/internal/services/hyperv_adapter.go                       +976  -68
desktop/internal/services/hyperv_adapter_test.go                 +1494  -10
desktop/internal/services/hyperv_adapter_windows.go               +76  -12
desktop/internal/services/hyperv_adapter_other.go                  +6    0
desktop/internal/services/adapter_visibility_test.go              +33   -7
```

### 提交 2 — `refactor(updater): 移除应用内自动更新链路`

```
desktop/internal/services/updater.go                       D   -691
desktop/internal/services/updater_authenticode_windows.go  D   -238
desktop/internal/services/updater_test.go                  D   -602
desktop/internal/services/updater_preview_test.go          D   -136
desktop/internal/services/updater_windows_test.go          D   -132
desktop/internal/services/updater_windows.go               D    -86
desktop/internal/services/updater_authenticode_other.go    D     -9
desktop/internal/services/updater_other.go                 D     -9
desktop/internal/services/update_manifest_ed25519_public_key.txt  D  -1
desktop/cmd/update-manifest-sign/main.go                   D    -80
desktop/cmd/update-manifest-sign/main_test.go              D    -66
desktop/internal/releaseversion/version.go                 0    -7
desktop/internal/releaseversion/metadata.go                0    -1
desktop/internal/releaseversion/version_test.go             +1   -4
desktop/cmd/release-version/main.go                        +2   -2
.github/workflows/build.yml                                +8  -147
.github/workflows/release-smoke.yml                        +4   -57
desktop/installer_layout_test.go                            +57  -36
docs/RELEASE_VERSIONING.md                                  +9  -12
README.md                                                   +5   -5
```

### 提交 3 — `refactor(about): 移除关于页与外部链接`

```
desktop/frontend/src/pages/AboutPage.tsx                     D  -253
desktop/frontend/src/pages/AboutPage.test.tsx                D   -84
desktop/frontend/public/support/SignPath/SignPath.png        D  (binary)
desktop/frontend/public/support/SignPath/wei.png             D  (binary)
desktop/frontend/public/support/SignPath/zhi.jpg             D  (binary)
desktop/frontend/src/components/shell/CompactNavigation.tsx       +2 -14
desktop/frontend/src/components/shell/CompactNavigation.test.tsx  +1   0
desktop/frontend/src/components/notifications/errorCodes.ts        0  -2
desktop/frontend/src/platform/desktop.ts                         +1  -2
desktop/frontend/package.json                                   +1  -3
desktop/frontend/pnpm-lock.yaml                                  0 -904
```

### 提交 4 — `chore(branding): 标记为自定义版 fork`

```
desktop/main.go                                              +2  -3   ← 仅取 Title 那一处
desktop/frontend/src/product.ts                               +3  -4   ← 仅取 edition 那一处
desktop/frontend/src/components/shell/TitleBar.tsx           +1  -1
```

### 需要 `git add -p` 逐 hunk 拆分的 5 个混改文件

| 文件 | 归属提交 1（vNIC） | 归属提交 2（updater） | 归属提交 3（about） | 归属提交 4（branding） |
|---|:---:|:---:|:---:|:---:|
| `desktop/frontend/src/App.tsx` | pageOrder 中 `"virtual-adapters"` 前移 | — | AboutPage 懒加载、`"about"` 三处移除 | — |
| `desktop/frontend/src/i18n/legacy.messages.json` | 全部 56 行新增 `virtual_adapters_*` | — | 58 行 about/signpath/sponsor/payment/nav_about 删除 | — |
| `desktop/frontend/src/app.css` | 全部 17 行新增 `.network-virtual-toggle` | — | 259 行删除 | — |
| `desktop/internal/services/settings.go` | `HideVirtualAdapters` 默认翻转 + 注释 | `UpdateChannel` 字段及校验分支 | — | — |
| `desktop/frontend/src/pages/SettingsPage.tsx` | 删 `hide_virtual_adapters` 行、删 `ADAPTER_VISIBILITY_EVENT` effect 与 import、默认值翻转 | 删 `update_channel: "stable"` 与「更新渠道」行 | — | — |
| `desktop/main.go` | — | 删 `updaterService` 注册两行 | — | `Title` 改为「HypoMux 自定义版」 |
| `desktop/frontend/src/product.ts` | — | — | −4（`build`/`website`/`repository`/`releases`） | +3（`edition`） |

> 实操建议：`product.ts`、`main.go`、`TitleBar.tsx` 三者构成一次原子改名，**可整体归入提交 4**，仅 `product.ts` 需 `-p` 拆出 −4 部分。

### 提交 5 — `docs(acceptance): 归档验收报告`

`reports/` 下 59 份文档 —— **但必须先完成 B-2 的取舍决定与 B-3 的脱敏**。

---

## 覆盖缺口

本次核验**未能覆盖**的部分，需负责人知悉并在最终验收报告中标注：

1. **CI 运行记录无法离线复验**。`reports/vnic/95-ci-run.md` 引用的 run `37112297270`（artifact 52,761,696 B、32 steps、gofmt step 0s）需要 `gh` 认证访问 GitHub 才能确认，本次仅验证了两个 SHA 存在于本地对象库且祖先关系成立，**未验证 CI 端结论本身**。
2. **真机 Hyper-V 行为无法复现**。`reports/vnic/30-real-machine-verdict.md` 的结论依赖真实 Hyper-V 主机操作，本次仅能确认其所依赖的代码实现到位，**未执行任何 Hyper-V 相关操作**。
3. **「任务前 45 files / 413 tests」基线无法追溯**。HEAD 仅有 268 个顶层测试声明，该基线必测自中间态树，**无法复现或否证**。同理 `AboutPage.test.tsx` 的用例全部嵌套在 `describe` 内（顶层 `it(` 计数为 0），`413 → 409` 的 −4 增量**无法用静态计数独立确认**。
4. **`go test` 未覆盖 `engine/`**。本次在 `desktop/` 下执行 `go test ./...`（engine 是独立 module）。`engine/internal/server/server.go` 的 `tun_failed`（F-2）**未通过测试验证**，仅做了文本定位。
5. **未执行 lint / 静态分析**。`golangci-lint`、ESLint、prettier 均未在本次核验范围内运行。
6. **`git check-ignore` 的 rc=0 伪匹配未查明根因**。已用「不存在的目录同样返回 rc=0」证明其为该 Git 版本的输出行为而非真实规则，但对 `git check-ignore -v` 在「目录路径 + 空匹配行」组合下的具体成因**未做源码级确认**。建议全组后续统一以 `--others --exclude-standard` 为准。
7. **二进制删除未做内容级确认**。3 张图片（`SignPath.png` / `wei.png` / `zhi.jpg`）确为「删除」状态且其对应的关于页组件亦已删除，但**未确认这些图片是否仍被其他代码或文档引用**（仅确认 `desktop/frontend/public/support/icon.ico` 与根 `support/` 下 4 个文件保留）。
8. **未审计 `docs/` 下 116 个已跟踪文档与 `assets/` 12 个资产**是否引用了本轮删除的 AboutPage / 更新渠道能力。本轮仅核对了 `README.md` 与 `docs/RELEASE_VERSIONING.md` 两处的文案同步。
9. **子模块 `website/` 未核验**。`git diff HEAD` 中无子模块指针变更，但未 `git submodule status` 确认其工作区是否脏。
10. **`tools/support/\344\275\277\347\224\250\350\257\264\346\230\216.txt`**（用说明.txt）在 `git ls-files` 中以引号转义形式存在，为历史遗留，非本轮引入，**未核查其内容与编码**。