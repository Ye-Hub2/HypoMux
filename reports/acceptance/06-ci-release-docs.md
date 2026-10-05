# 06 · CI 工作流 + 发布链 + 版本元数据 + 文档一致性 验收报告

- 验收对象：`<repo>` 工作区未提交改动
- 基线 HEAD：`fc821b16bd55630ac6cba4311179e1a1b6644ded`
- 验收范围：`.github/workflows/build.yml`、`.github/workflows/release-smoke.yml`、`docs/RELEASE_VERSIONING.md`、`README.md`、`desktop/internal/releaseversion/*`、`desktop/cmd/release-version/main.go`、`desktop/installer_layout_test.go`，以及与本次删除相关的文档/工具/打包资产
- 验收方式：只读（未修改源码、未 `git add`、未 `wails3 task`）

---

## 验收结论

**有条件通过**

CI 改动方向正确、YAML 合法、删除面干净：三个工作流中已不存在任何指向 `cmd/update-manifest-sign`、`update_manifest_ed25519_public_key.txt`、`latest.json`、`steps.version.outputs.channel` 的引用；`go -C desktop run ./cmd/release-version` 已不再输出 `channel`，而 build.yml 剩余消费者只用 `prerelease` / `make_latest`，链路自洽；`desktop` 根包 40 项测试与 `internal/releaseversion` 全部通过。

但有 **2 个产物错误级问题**：便携 zip 根本没有进入 Release 产物；以及删除 updater 后 CI 的「生产安装包信任校验」步骤已退化为静默空跑。另有 `README_EN.md` 五处仍在宣传已删除的自动更新/更新通道（`README.md` 已同步改，中英文对不上）。

---

## CI 步骤核对表

### build.yml — job `build`（windows-2025）

| 步骤（file:line） | 引用文件是否存在 | 是否引用已删除物 | 结论 |
| --- | --- | --- | --- |
| Checkout `build.yml:36` (`actions/checkout@v5`) | — | 否 | ✅ |
| Validate release options `build.yml:39` | — | 否 | ✅ |
| Set up Go `build.yml:56`（`go-version-file: engine/go.mod`，缓存 `engine/go.sum`/`desktop/go.sum`） | `engine/go.mod`、`engine/go.sum`、`desktop/go.sum` 均存在 | 否 | ✅ |
| Set up Node.js `build.yml:64` | — | 否 | ✅ |
| Prepare and validate release version `build.yml:69` | `desktop/cmd/release-version` 存在 | 否 | ✅ 实测 `go run ./cmd/release-version -check` 输出 `version=2.7.0 / windows_version=2.7.0.65535 / prerelease=false / make_latest=true` |
| Restore pnpm tool cache `build.yml:79` | — | 否 | ✅ |
| Install pinned pnpm `build.yml:86` | — | 否 | ✅ |
| Configure pnpm `build.yml:94`（缓存键含 `desktop/frontend/pnpm-lock.yaml`） | 存在 | 否 | ✅ |
| Install Wails and NSIS `build.yml:117` | — | 否 | ✅ |
| Install frontend dependencies `build.yml:135` | — | 否 | ✅ |
| Generate Wails frontend bindings `build.yml:139` | — | 否 | ✅ |
| Run frontend tests / Build frontend assets `build.yml:144,148` | — | 否 | ✅ |
| Validate Go modules `build.yml:152` | — | 否 | ✅ |
| Verify Go formatting `build.yml:168` | — | 否 | ✅ |
| Build and package Wails desktop `build.yml:188` | — | 否 | ✅ |
| Stage unsigned executables for SignPath `build.yml:193`（`desktop\bin\hypomux.exe`、`hypomux-engine.exe`） | 由上一产出 | 否 | ✅ |
| Upload/Sign desktop exe `build.yml:201,210` | — | 否 | ✅ |
| Upload/Sign Core exe `build.yml:225,234` | — | 否 | ✅ |
| Repackage signed executables `build.yml:249`（`desktop\build\windows\nsis\project.nsi`） | `desktop/build/windows/nsis/project.nsi` 存在 | 否 | ✅ |
| Stage/Upload/Sign installer `build.yml:262,267,276` | `$INSTALLER_PATH = desktop/bin/hypomux-amd64-installer.exe` | 否 | ✅ |
| Use signed installer `build.yml:291` | `signed-installer/HypoMux-amd64-installer-unsigned.exe` | 否 | ✅ |
| **Verify production installer trust policy** `build.yml:296` | 目标测试已被删除 | 引用了**已删除物**（`updater_windows_test.go` 中的 `TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller`） | ❌ 静默空跑，见「产物错误级 B1」 |
| Assemble portable package `build.yml:303` | `bin/sing-box.exe`、`bin/wintun.dll`、`bin/libcronet.dll`、`desktop/portable/{launch-portable.cmd,restore-environment.cmd,PORTABLE-README.txt}` 全部存在且已被 git 跟踪 | 否 | ✅ zip 逐条 `CreateEntry` 写入（`build.yml:413-428`）+ 反斜杠条目校验（`build.yml:430-441`），符合 ZIP 规范要求 |
| Upload portable package `build.yml:460` | — | 否 | ⚠️ 产物无人消费，见「产物错误级 A1」 |
| Upload build artifacts `build.yml:469` | — | 否 | ✅ |

### build.yml — job `release`（ubuntu-latest）

| 步骤（file:line） | 引用文件是否存在 | 是否引用已删除物 | 结论 |
| --- | --- | --- | --- |
| Checkout `build.yml:492`（fetch-depth 0） | — | 否 | ✅ `git rev-parse HEAD^{commit}` 依赖完整历史，成立 |
| Set up Go `build.yml:497` | `desktop/go.mod` 存在 | 否 | ✅ |
| Download build artifacts `build.yml:503` | 产物名 `HypoMux-Windows-<sha>-<attempt>` 与 `build.yml:472` 一致 | 否 | ✅ 但只下载了安装包/EXE，未下载便携 zip |
| Resolve release version metadata `build.yml:512` | — | 否 | ✅ 实测 `-tag v2.7.0 -github-output` 只输出 `version/windows_version/prerelease/make_latest`，无 `channel` |
| Validate versioned release notes `build.yml:517` | `.github/release-notes/` 存在 | 否 | ✅ |
| Stage versioned release installer name `build.yml:536` | `artifacts/hypomux-amd64-installer.exe` 由上一产出 | 否 | ✅ |
| Preflight synchronized GitHub and CNB tags `build.yml:544` | — | 否 | ✅ |
| Ensure CNB Release and upload installer `build.yml:570` | `docker.cnb.cool/looc/git-cnb@sha256:c254…`（未改动）；`jq` 在 ubuntu-latest 可用 | 否 | ✅ |
| Check existing GitHub installer `build.yml:626` | — | 否 | ✅ |
| Create or update GitHub Release metadata `build.yml:652` | — | 否 | ✅ |
| Upload installer to GitHub Release `build.yml:663` | `files: artifacts/${{ steps.stage_release.outputs.installer_name }}` 与 `build.yml:542` 输出一致 | 否 | ✅ |
| Verify both Release installers are byte-identical `build.yml:676` | — | 否 | ✅ 顺序正确：上传（618 / 663）在校验（676）之前，满足 `installer_layout_test.go:388-401` 的断言 |

### release-smoke.yml — job `smoke`（ubuntu-latest）

| 步骤（file:line） | 引用文件是否存在 | 是否引用已删除物 | 结论 |
| --- | --- | --- | --- |
| Checkout `release-smoke.yml:21` | — | 否 | ✅ |
| Set up Go `release-smoke.yml:24` | `desktop/go.mod` 存在 | 否 | ✅ |
| Resolve release version metadata `release-smoke.yml:32` | `desktop/cmd/release-version` 存在 | 否 | ✅ 输出未被消费，但 `-github-output` 仍写入 GITHUB_OUTPUT，无害 |
| Verify GitHub tag is mirrored to CNB `release-smoke.yml:39` | — | 否 | ✅ |
| Verify CNB token can read the existing Release `release-smoke.yml:69` | — | 否 | ✅ |

### create-release-tag.yml

未在本次 diff 中改动；`installer_layout_test.go:322` 新增的禁用词检查覆盖了该文件，实测通过（`TestReleaseWorkflowsCarryNoUpdateChannelArtifacts` PASS）。

---

## 阻塞问题

### 一、发布失败级

**无。** 本次工作流改动没有发现任何会在 GitHub Actions 上直接失败的问题：

- 4 个工作流 YAML 全部可被 `yaml.safe_load` 解析（build.yml 2 个 job、create-release-tag.yml 1 个、go-engine.yml 1 个、release-smoke.yml 1 个）。
- 三个工作流中已无任何指向删除物的引用（`git grep` 确认 `update-manifest-sign` / `latest.json` / `ed25519` 公钥 / `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 在 `.github/workflows/` 下零命中）。
- 保留的版本输出 `prerelease` / `make_latest` 均仍由 `desktop/cmd/release-version/main.go:63` 产出。

### 二、产物错误级

**A1 · 便携 zip 从未进入 Release 产物（安装包有、便携包没有）**

- `.github/workflows/build.yml:460-467` 把 `portable-package/*.zip` 上传为 artifact `HypoMux-Portable-Windows-${{ github.sha }}-${{ github.run_attempt }}`。
- `.github/workflows/build.yml:503-507` 的 `release` job 是**全仓库唯一**一处 `download-artifact`（`grep download-artifact .github/workflows/*.yml` 仅命中此行），且只下载 `HypoMux-Windows-…`，路径 `artifacts`。
- `.github/workflows/build.yml:671` 的 `files: artifacts/${{ steps.stage_release.outputs.installer_name }}` 与 `.github/workflows/build.yml:618` 的 `release asset-upload -f "${installer}"` 都只处理安装包。
- 结果：GitHub Release / CNB Release 只有 `HypoMux_Setup_<version>.exe`，**便携 zip 只停留在 Actions artifact 里，用户下载页拿不到**。删除更新发布链并不会导致这个问题，但它使「Release 产物 = 安装包 + 便携 zip」这一预期不成立。
- 来源：该便携包步骤来自已提交的 `46680e0`，不属于本次未提交 diff；但落在本次验收范围内，按产物完整性要求记为阻塞。
- 证据补充：`portable-package/` 已被 `.gitignore:64` 整体忽略（`git ls-files portable-package` 为空），因此本地产物不会污染提交。

**B1 · 「Verify production installer trust policy」已退化为静默空跑**

- `.github/workflows/build.yml:296-301` 在 `signing_mode == production|publish` 时设置 `HYPOMUX_SIGNED_INSTALLER_TEST` 并执行
  `go -C desktop test ./internal/services -run TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller -count=1 -v`。
- 该测试原本位于已删除的 `desktop/internal/services/updater_windows_test.go`（`git show HEAD:desktop/internal/services/updater_windows_test.go` 可见其定义）。
- 现在仓库中该测试名零命中；本地实测输出 `testing: warning: no tests to run` + `PASS`，`EXITCODE=0`。
- 后果：`production` / `publish` 发布**不再校验 SignPath 签名安装包的真实 Authenticode 签名**，步骤却显示为绿色通过；`desktop/installer_layout_test.go:370` 仍在断言工作流保留 `HYPOMUX_SIGNED_INSTALLER_TEST`，等于把一个空跑固化成了契约。
- 该步骤本身不在本次 diff 中，但因本次删除 `updater_windows_test.go` 而失效，属本次改动引发的直接后果。

### 三、文案陈旧级

**C1 · `README_EN.md` 与 `README.md` 不同步（5 处仍在宣传已删除的自动更新）**

| 位置 | 现存文案（已失效） | `README.md` 对应位置已改为 |
| --- | --- | --- |
| `README_EN.md:24` | `…bundled sing-box 1.14.2; stable/preview update channels.` | 删除了 `支持正式版／预览版更新渠道` 短句 |
| `README_EN.md:37` | `…adapter health checks, live connections, support logs, and update checks.` | 改为 `…连接查看与日志`（去掉「更新检查」） |
| `README_EN.md:50` | `Automatic update metadata is delivered through a separate signed update channel and verified with Ed25519, followed by installer size, SHA-256, and Windows Authenticode verification.` | 改为「本 fork 不包含应用内自动更新，升级需由用户自行前往上述发布页获取新版本」 |
| `README_EN.md:59` | `…to forward traffic, check the official signed update channel, download installers, and validate connectivity.` | 改为 `转发用户选择的网络流量，以及进行网络连通性验证` |
| `README_EN.md:212` | `…validate the update signing key, synchronized GitHub/CNB tags, CNB Release access, and any existing signed update channel without creating a Release, uploading assets, or modifying the channel.` | 改为「只读核对 GitHub/CNB Tag 是否指向同一提交以及 CNB Release 访问权限」 |

补充：`docs/RELEASE_VERSIONING.md:44-54` 已完整改写为「本 fork 不提供应用内自动更新」，与代码一致（`git grep update_channel` 在 `desktop/` 下仅命中 `settings_fields_test.go:11` 的**回归测试** `TestLegacyUpdateChannelFieldIsIgnored`，确认旧配置文件仍可加载且字段已不可写）——文档这一侧是对的。

**C2 · 新增功能未写进 README（中英均缺）**

- 便携包（`build.yml:303-458` 打出的 `HypoMux-Portable-<version>-vnic-preview.zip`）在 `README.md` / `README_EN.md` 的「下载」章节（`README.md:69`、`README_EN.md:67`）只提到 `HypoMux_Setup_*.exe`，未提及便携包及其 `SHA256SUMS.txt`。
- 独立的虚拟网卡 / Hyper-V 管理页（HEAD `fc821b1`）在两份 README 中均无对应说明。

**C3 · `docs/migration/` 历史矩阵仍描述已删除的关于页与 Updater**

- `docs/migration/ui-migration-matrix.md:117-120` 仍把 `关于 → 检查更新 / Release Notes / 下载与进度 / 清理后安装` 标为 `Wired`，并引用 `UpdaterService.Check`。
- `docs/migration/wails-architecture.md:217,230,261` 仍保留 `Updater` 接口与 `updater.go` 目录示意。
- 判定：这些是 Python→Wails 迁移的历史归档，记录当时状态，可不改；但 `ui-migration-matrix.md` 被当作「迁移完成度矩阵」时会造成误读，建议加一行「自动更新已于本 fork 移除」的脚注。

**C4 · README 声称 Go 1.26，`desktop/go.mod` 要求 1.25.0**

- `README.md:192` / `README_EN.md:190` 写 `Go 1.26`；`engine/go.mod:3` 为 `go 1.26.0`（+ `toolchain go1.26.6`），`desktop/go.mod:3` 为 `go 1.25.0`。CI 用 `go-version-file: engine/go.mod`，实际不受影响，仅文档表述不精确。

---

## 风险与建议

1. **便携包发布（对应 A1）**：在 `release` job 增加一次 `actions/download-artifact` 拉取 `HypoMux-Portable-Windows-${{ github.sha }}-${{ github.run_attempt }}`，并把 zip 一并 `asset-upload` / `files:` 到 CNB 与 GitHub Release；同时给 `installer_layout_test.go` 加一条「便携 zip 必须先于校验步骤上传」的顺序断言，与现有安装包断言保持同样风格。
2. **签名信任校验（对应 B1）**：要么恢复一个不依赖 updater 的最小 Authenticode 校验测试（例如在 `desktop/internal/services` 新增只读校验函数 + 对 `HYPOMUX_SIGNED_INSTALLER_TEST` 的测试），要么删除 `build.yml:296-301` 并同步移除 `installer_layout_test.go:370` 的 `HYPOMUX_SIGNED_INSTALLER_TEST` 断言，避免绿色空跑。
3. **`README_EN.md`（对应 C1）**：按 `README.md` 的 5 处改写逐条对齐，中英文版本应视为同一份事实来源，建议加一条 CI 检查断言两文件的更新通道措辞同步。
4. **禁用词检查的精度**：`desktop/installer_layout_test.go:338` 禁用的是精确串 `artifacts/latest.json`，而 `.github/workflows/build.yml:510` 的**注释**里仍写着裸的 `latest.json`。目前因为前缀不同而通过；建议要么把注释里的 `latest.json` 改写为不含该精确子串的措辞，要么在测试中忽略注释行，避免将来误报/漏报。
5. **便携 zip 条目数硬断言**：`.github/workflows/build.yml:436` 断言 `$zipCheck.Entries.Count -eq $required.Count + 1`（9）。`$required` 在 `build.yml:375-384` 硬编码 8 项，任何新增/删除打包文件都会让 CI 失败——这是有意的契约保护，但改动 `portable-package` 布局时必须同步该列表。
6. **`*.cmd` / `*.txt` 行尾**：`.gitattributes:16-17` 已为 `*.cmd` 与 `desktop/portable/*.txt` 固定 CRLF，且 `desktop/portable/*.cmd` 是便携包启动器，直接影响 `build.yml:336-338` 打出的产物，无需额外同步；`.gitignore:64` 已忽略 `/portable-package/`。
7. **`tools/support/` 无需同步**：对 `latest.json` / `update-manifest-sign` / `HypoMux_Setup` / `About` 的扫描零命中，修复脚本不依赖被删除的更新链路。
8. **`website/` 为空目录**（0 个条目），本次改动无需同步。
9. **`engine/` 无需同步**：`engine/internal/proxy/steam_cdn_test.go:7` 的 `crypto/ed25519` 是 Steam CDN 证书校验，与更新清单签名无关，属误报。

---

## 执行记录

| # | 命令 / 动作 | 结果 |
| --- | --- | --- |
| 1 | `git log --oneline -5` / `git status --porcelain` | HEAD `fc821b1`；工作区 10 个修改 + 22 个删除 + `reports/` 未跟踪 |
| 2 | `git diff -- .github/workflows/build.yml` | 删除 4 个步骤（Generate update manifest、Sign update manifest、Publish signed update channel、Verify both raw update channels），重命名 2 个步骤并加注释 |
| 3 | `git diff -- .github/workflows/release-smoke.yml` | 删除 2 个步骤（Verify manifest signing secret、Verify existing signed update channel），job 名改为 `Verify CNB tag mirror and release access` |
| 4 | `git grep "update-manifest-sign" / "latest.json" / "ed25519" / "update_manifest_ed25519_public_key" / "UPDATE_MANIFEST_ED25519_PRIVATE_KEY"` | 仅命中 `desktop/installer_layout_test.go`（禁用词常量）与 `docs/RELEASE_VERSIONING.md:46` / README_EN.md（文案）；`.github/workflows/` 下零命中 |
| 5 | `python -c "yaml.safe_load(...)"` 对 4 个 workflow | 全部 `OK`，job 列表符合预期 |
| 6 | `Test-Path` 逐一检查 CI 引用的 20 个路径 | 全部存在；`bin/{sing-box.exe,wintun.dll,libcronet.dll}`、`desktop/portable/*`、`support/*` 均已被 `git ls-files` 跟踪（保证 fresh clone 可用） |
| 7 | `Select-String 'download-artifact' .github/workflows/*.yml` | 仅 1 处命中（`build.yml:504`），确认便携 artifact 无消费者 |
| 8 | `go test ./ -run Installer -count=1 -v`（`desktop/`） | 9 项：8 PASS + 1 SKIP（`TestInstallerDirectoryResolutionNSIS`，需 `MAKENSIS`）；`ok github.com/Hypostasis-Cat/HypoMux/desktop 0.167s` |
| 9 | `go test ./ -count=1 -v`（`desktop/` 全包） | 40 项全部 PASS（含 `TestReleaseWorkflowsCarryNoUpdateChannelArtifacts`、`TestReleasePublishesOneSignedInstallerToBothMirrors`、`TestReleaseTrustSmokeWorkflowIsReadOnly`、`TestPreviewPublishingIsIsolatedFromStable`、`TestVersionMetadataIsConsistent`） |
| 10 | `go build ./...`（`desktop/`） | 无输出（成功）；删除 updater 后无残留引用 |
| 11 | `go test ./internal/releaseversion/... -count=1` | `ok`（`Channel()` 断言移除后仍通过） |
| 12 | `go run ./cmd/release-version -check` | `version=2.7.0 / windows_version=2.7.0.65535 / prerelease=false / make_latest=true` |
| 13 | `go run ./cmd/release-version -tag v2.7.0 -github-output <tmp>` | 输出 4 行，**无 `channel=`** —— 证实 `build.yml` 剩余消费者不会读到空输出 |
| 14 | 复现 `build.yml:301` 的信任校验命令 | `testing: warning: no tests to run` / `PASS` / `EXITCODE=0` —— 证实 B1 空跑 |
| 15 | `git show HEAD:desktop/internal/services/updater_windows_test.go` | 确认该测试确实位于被删除文件中 |
| 16 | `git show d6fb799 -- desktop/installer_layout_test.go` | d6fb799 只改了 `TestWindowsTaskManagerUsesProductName`：改为用 `releaseversion.Parse(VERSION)` 解析后逐字段比对 `build/windows/info.json` 的 `0000` / `0409` 版本表。该测试本次仍 PASS，逻辑未被本次改动破坏 |
| 17 | `git diff -- desktop/installer_layout_test.go` | 重命名 3 个测试、新增 `TestReleaseWorkflowsCarryNoUpdateChannelArtifacts` 禁用词检查、把安装包上传/校验顺序断言从「渠道发布晚于校验」改为「两处上传都晚于校验之前」 |
| 18 | `git diff -- desktop/internal/releaseversion/ desktop/cmd/release-version/main.go docs/RELEASE_VERSIONING.md` | `Channel()` 方法删除、`channel=` 输出删除、`metadata.go` 移除对 `internal/services/updater.go` 的同步项、`version_test.go` 移除 `Channel()` 断言 —— 与 `docs/RELEASE_VERSIONING.md:44-54` 完全一致 |
| 19 | `git diff -- README.md` | 4 处更新（短句、架构短句、发布说明段、隐私段）+ 发布流程段的 smoke 描述 |
| 20 | `Select-String` 扫描 README/README_EN/docs/website 的更新通道与赞助关键词 | README.md 已清；README_EN.md 残留 5 处；`docs/` 根目录文件仅 RELEASE_VERSIONING.md 命中（且是正确表述）；`website/` 为空 |
| 21 | `Get-ChildItem support -Recurse` / `desktop/frontend/public` | 根 `support/` 4 张图仍在，README 图片链接有效；前端仅剩 `support/icon.ico`，`ProductMark.tsx:3` 引用有效 |
| 22 | `Get-Content .gitattributes` / `tools/` / `portable-package/` / `.gitignore` | 行尾规则覆盖便携启动器；`tools/support/` 3 个文件无过期引用；`/portable-package/` 已忽略且未跟踪 |
| 23 | `Select-String '^go |^toolchain '` on `engine/go.mod`、`desktop/go.mod` | 1.26.0 / toolchain 1.26.6 与 1.25.0 |

**环境说明**：Go 通过 `$env:Path="C:\Program Files\Go\bin;"+$env:Path` 取得（go1.27.0 windows/amd64）；系统 `python` 为 WindowsApps 占位符，改用 `%USERPROFILE%\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe`（含 PyYAML）。全程未使用 `wails3 task`，未触碰 `desktop/go.mod`。

---

## 覆盖缺口

以下判断依赖 GitHub 实际运行或外部服务，本地无法确认，**未臆断为通过**：

1. **SignPath 全链路**：`signpath/github-action-submit-signing-request@v2` 的上传、审批轮询（`wait-for-completion-timeout-in-seconds: 3600`）、产物回传，未实跑。
2. **CNB 发布 API**：`docker.cnb.cool/looc/git-cnb@sha256:c254172bb9d6025733a0e2991b4a99af8c46aeedcadcb468788ef5a0dc00275c` 镜像可达性、`release create` / `asset-upload` 行为、`secrets.CNB_TOKEN` 有效性。
3. **`softprops/action-gh-release@v3` 上传**：`build.yml:671` 的 `files:` 与 `fail_on_unmatched_files: true` 在真实 artifact 布局下是否命中（本地靠 `installer_layout_test.go` 的字符串断言间接覆盖，非运行时验证）。
4. **跨 job artifact 传递**：`actions/upload-artifact@v7` → `actions/download-artifact@v8` 的名称/路径匹配只做了静态核对。
5. **Actions 版本可用性**：`actions/upload-artifact@v7`、`actions/download-artifact@v8`、`actions/setup-go@v7`、`actions/setup-node@v6`、`actions/cache@v4`、`actions/checkout@v5`、`softprops/action-gh-release@v3` —— 这些 tag 在本次 diff 中**未被改动**（与 HEAD 逐行一致，见执行记录 #2/#3 的 `uses:` 比对），是否在 Marketplace 真实存在需实跑确认，本报告不对其存在性下结论。
6. **release-smoke.yml 的网络步骤**：`git ls-remote` 对 GitHub / cnb.cool 的标签比对、`release get` 鉴权，均需网络与真实 token。
7. **`wails3 task windows:package`**：Windows runner 上的实际打包、NSIS 安装与 `project.nsi` 编译未执行（且按要求不得用 `wails3 task`）。
8. **便携包运行时行为**：zip 内容仅做了静态核对（条目名规范、正斜杠校验逻辑存在于 `build.yml:413-441`），未在 CI 外实际生成并解压验证。
9. **前端测试**：`pnpm --dir desktop/frontend test` 未执行（属于前端验收腿的范围）。
10. **`d6fb799` 版本表校验的深度**：`TestWindowsTaskManagerUsesProductName` 在本地 PASS，但它读取的是工作区当前的 `build/windows/info.json`；在 tag 构建时 `-write` 注入后的最终状态未实跑验证。