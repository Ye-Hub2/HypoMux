# CI / 构建 / 文档 —— 移除应用内自动更新

负责范围：`.github/workflows/**`、`desktop/cmd/update-manifest-sign/**`、`desktop/installer_layout_test.go`、`README.md`、`docs/RELEASE_VERSIONING.md`。

**用户硬性要求已满足：安装包代码签名全部保留。** `build` job 的 SignPath 签名链（step 18–28）、Windows 打包、GitHub Release 上传、CNB Release 上传、`HYPOMUX_SIGNED_INSTALLER_TEST` 验证一字未动。

变更规模：`git diff --stat`（仅我的文件）= **83 insertions, 403 deletions**。

| 文件 | 结果 |
| --- | --- |
| `.github/workflows/build.yml` | 843 → **704** 行；release job 16 → **12** 步 |
| `.github/workflows/release-smoke.yml` | 144 → **91** 行；smoke job 7 → **5** 步 |
| `desktop/cmd/update-manifest-sign/` | 整目录**删除**（`main.go` 80 行 + `main_test.go` 66 行） |
| `desktop/installer_layout_test.go` | 906 → **927** 行；5 个用例改写 + 1 个新增守卫 |
| `README.md` | 5 处改写 |
| `docs/RELEASE_VERSIONING.md` | 更新渠道章节整段替换为事实陈述 |

---

## 1. `.github/workflows/build.yml`

### 删除的步骤（行号为 HEAD 原始行号）

| 原始行号 | 步骤名 | 删除理由 |
| --- | --- | --- |
| 538–570 | `Generate update manifest` | 纯更新渠道产物：`jq -n … > artifacts/latest.json` 组装 `schema_version: 1` / `--rawfile notes` / `installer_sha256` / `urls: [$cnb_url, $github_url]`，随后 `jq empty` 校验并与 `manifest-notes.md` 比对。无安装包签名成分。 |
| 572–581 | `Sign update manifest` | 唯一使用 `secrets.UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 的步骤；调 `go -C desktop run ./cmd/update-manifest-sign` 产出 `artifacts/latest.json.sig`。**注意**：其中的 `-verify-public-key "$(tr -d '\r\n' < desktop/internal/services/update_manifest_ed25519_public_key.txt)"` 行随整个步骤一并删除——该公钥文件已被 backend 删除，保留会指向不存在的文件。 |
| 745–797 | `Publish signed update channel to GitHub and CNB` | `git mktree` / `commit-tree` 把清单提交到 `refs/heads/<channel>` 并 `git push origin` + `git push cnb`。纯更新渠道发布，与 Release 上传无交集。 |
| 799–841 | `Verify both raw update channels and Ed25519 signature` | curl `raw.githubusercontent.com/…/latest.json(.sig)` 与 `cnb.cool/…/-/git/raw/…/latest.json(.sig)`，`cmp` 后 `update-manifest-sign -verify-only`。纯更新渠道校验。 |

### 重命名 / 加注释（步骤体未改）

| 原始行号 | 原名 | 新名 |
| --- | --- | --- |
| 509 | `Resolve release channel` | `Resolve release version metadata` |
| 530 | `Stage legacy-updater-compatible installer name` | `Stage versioned release installer name` |

两处都加了注释说明「本 fork 无应用内自动更新」。`steps.version.outputs.prerelease` 与 `.make_latest` 仍被 CNB / GitHub release 步骤消费，`id: version` 保留，引用链未断。

### 保留的步骤（release job 全部 12 步）

`Checkout` → `Set up Go`（`cmd/release-version` 仍需要）→ `Download build artifacts` → `Resolve release version metadata` → `Validate versioned release notes` → `Stage versioned release installer name` → `Preflight synchronized GitHub and CNB tags` → `Ensure CNB Release and upload installer` → `Check existing GitHub installer` → `Create or update GitHub Release metadata` → `Upload installer to GitHub Release` → `Verify both Release installers are byte-identical`。

`build` job 31 步全部未动，其中 SignPath 链为 step 18–28：`Stage unsigned executables for SignPath` / `Upload desktop executable for SignPath` / `Sign desktop executable` / `Upload Core executable for SignPath` / `Sign Core executable` / `Repackage signed executables` / `Stage unsigned installer for SignPath` / `Upload installer for SignPath` / `Sign installer` / `Use signed installer` / `Verify production installer trust policy`。

### 关键判断：为什么保留 `cp artifacts/hypomux-amd64-installer.exe "artifacts/HypoMux_Setup_${version}.exe"`

**结论：保留。** 它不是更新渠道产物，理由有三，任一条独立成立：

1. **它的输出被四个"必须保留"的步骤消费。** `steps.stage_release.outputs.installer_name` 被 CNB `release asset-upload -f "${installer}"`、GitHub 既有安装包检查、`files: artifacts/${{ steps.stage_release.outputs.installer_name }}`（`action-gh-release`）和 `Verify both Release installers are byte-identical` 引用。删掉它就要重写这四步，属于对保留链路的非必要改动。
2. **它是人工下载的可读文件名。** `README.md:69` 让用户下载 `HypoMux_Setup_*.exe`；产物即 `HypoMux_Setup_2.7.0.exe`。删掉后 Release 上的文件名会退回 `hypomux-amd64-installer.exe`，与 README 指引脱节。
3. **它保证两镜像只有一份安装包。** 现有断言 `strings.Count(workflow, "cp artifacts/hypomux-amd64-installer.exe") == 1` 依赖它——两镜像必须拿到字节完全相同的同一份签名安装包。

该步骤不引用 `latest.json`、不引用 Ed25519 密钥、不引用 channel ref。仅重命名去除 "legacy-updater" 措辞并加注释。

### 清理

- `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 在整个 `.github/` 下**已无任何引用**（原先唯一引用点就是被删的 `Sign update manifest`）。
- 没有步骤跨越删除点互相引用被删步骤的输出；`needs:` 关系未涉及被删内容。
- `create-release-tag.yml` 未改、无更新渠道残留。
- YAML 合法性：`yaml.safe_load` 解析通过，`build` = 31 步、`release` = 12 步。

---

## 2. `.github/workflows/release-smoke.yml` —— **保留文件，剥离更新检查**

**判断依据：不是"整体都是更新链路"。** 原 7 步中：

| 原始行号 | 步骤名 | 处置 |
| --- | --- | --- |
| 21 | `Checkout` | 保留 |
| 24 | `Set up Go` | 保留（`release-version` 需要） |
| 30 | `Resolve release channel` | **保留并改名** `Resolve release version metadata` |
| 37–50 | `Verify manifest signing secret matches embedded public key` | **删除** |
| 52–80 | `Verify GitHub tag is mirrored to CNB at the same commit` | **保留** |
| 82–104 | `Verify CNB token can read the existing Release` | **保留** |
| 106–142 | `Verify existing signed update channel is readable and identical` | **删除** |

- 第 37 步纯粹验证 `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 与 `update_manifest_ed25519_public_key.txt` 内嵌公钥配对——两者都已不存在（公钥文件被 backend 删除）。
- 第 106 步读两个 raw channel 的 `latest.json` 并验签——纯更新渠道。
- 第 52 / 82 步是**与自动更新无关的发布信任检查**：确认 GitHub tag 在 CNB 镜像上指向同一 commit（否则两镜像内容分叉）、确认发布用 CNB token 具备只读 Release 访问权限（否则正式发布会在上传阶段炸）。这正是 `README.md:214` 描述的手动发布前核对，保留它比整文件删除更有价值。
- workflow 顶层 `name: Release Trust Smoke Test` **未改**（README 以该名字引用）；`permissions` 仍为 `contents: read`。
- job 名 `Verify signing key and CNB mirror` → `Verify CNB tag mirror and release access`（原名含 "signing key"，已不成立）。

现 5 步：`Checkout` / `Set up Go` / `Resolve release version metadata` / `Verify GitHub tag is mirrored to CNB at the same commit` / `Verify CNB token can read the existing Release`。

---

## 3. `desktop/cmd/update-manifest-sign/` —— 整目录删除

CLI 专用 Ed25519 清单签名工具，调用者全部消失后无残留价值：

- `main.go`（80 行）：`const privateKeyEnvironment = "UPDATE_MANIFEST_ED25519_PRIVATE_KEY"`，flag `-input` / `-output` / `-verify-public-key` / `-verify-only`。
- `main_test.go`（66 行）。

删除前已核对 `Resolve-Path` 绝对路径确为 `<repo>\desktop\cmd\update-manifest-sign`。`desktop/cmd/` 现只剩 `release-version`（**按 lead 要求保留**，`build.yml` 与 `release-smoke.yml` 仍调用它）。

全仓剩余 `update-manifest-sign` 字样仅出现在 `reports/**`（历史记录，契约禁止改动）与本轮已改写的 `installer_layout_test.go`。

**`desktop/go.mod` 无需改动**：`crypto/ed25519` 属标准库，不引入 module 依赖；`golang.org/x/crypto v0.53.0` 是 `// indirect`，由 wails / pion 的依赖图带入，与本 CLI 无关。`git status` 确认 `go.mod` / `go.sum` 未被本轮改动。未执行 `go mod tidy`（超出写作用域，且会与并发改动冲突）。

---

## 4. `desktop/installer_layout_test.go`

### 语义互斥的一对用例 —— 取舍结论

Lead 指出的冲突属实：`TestReleaseWorkflowsCarryNoUpdateChannelArtifacts` 禁止三个 workflow 出现 `artifacts/latest.json`，而原 `TestReleasePublishesOneSignedInstallerThenUpdatesSignedChannel` 又**要求** build.yml 出现它。两条不可能同时成立。

**取舍：采用「无更新渠道产物」语义。** 具体做法是**改造而非二选一**——把原用例的断言面从「安装器 + 清单双源」收窄为「签名安装器双镜像发布」，使两者不再互斥、各自守住一条不重叠的不变量：

- `TestReleaseWorkflowsCarryNoUpdateChannelArtifacts`（**新增**）守**否定面**：三个 workflow 里不得再出现任何更新渠道机制（`artifacts/latest.json`、`latest.json.sig`、`UPDATE_MANIFEST_ED25519_PRIVATE_KEY`、`update-manifest-sign`、`update_manifest_ed25519_public_key.txt`、`steps.version.outputs.channel`、`git commit-tree`、`raw.githubusercontent.com`、`-verify-only`）。这是防回归的核心守卫——没有它，以后有人顺手加回清单生成不会被任何测试发现。
  - 刻意用 `artifacts/latest.json` 而非裸 `latest.json`，以便 build.yml 里解释性注释中出现的 `latest.json` 不会误触发。
- `TestReleasePublishesOneSignedInstallerToBothMirrors`（**原 `…ThenUpdatesSignedChannel` 改名**）守**肯定面**：签名安装器必须同时发往 GitHub 与 CNB 且字节一致。它不再断言任何 manifest / Ed25519 / channel / raw URL。

用户要求保留安装包签名，所以"肯定面"用例必须留下——它正是保护 SignPath → Release 这条链的测试，不能因为 manifest 断言被删就整条丢掉。

### 逐用例处置

（最终文件 `desktop/installer_layout_test.go`，行号为改写后的位置：294 / 322 / 357 / 403 / 469 / 519）

| 用例（行） | 处置 |
| --- | --- |
| `TestReleasePublishesLegacyUpdaterCompatibleInstallerName` → **`TestReleasePublishesVersionedInstallerNameForManualDownloads`** | 改名 + 强化。保留原有 `version="${GITHUB_REF_NAME#v}"`、`HypoMux_Setup_${version}.exe`、`INSTALLER_PATH`、`cp …` 与 Taskfile 断言；**新增** `files: artifacts/${{ steps.stage_release.outputs.installer_name }}`，把"保留该 staging 步骤"的决定固化成测试。 |
| `TestReleasePublishesOneSignedInstallerThenUpdatesSignedChannel` → **`TestReleasePublishesOneSignedInstallerToBothMirrors`** | 改名 + 收窄。**删除**：`installer_sha256=…`、`schema_version: 1`、`urls: [$cnb_url, $github_url]`、`artifacts/latest.json`、`artifacts/latest.json.sig`、`UPDATE_MANIFEST_ED25519_PRIVATE_KEY`、`go -C desktop run ./cmd/update-manifest-sign`、两条 `git push … "${channel_commit}:${channel_ref}"`、两个 raw channel URL、`-verify-only`、以及"先验证 Release 再发布 channel"的顺序断言。**保留**：git-cnb sha256 digest `c254172bb9d6025733a0e2991b4a99af8c46aeedcadcb468788ef5a0dc00275c`、`secrets.CNB_TOKEN`、`cnb release get/create -t "${tag}"`、`release asset-upload -t "${tag}" -f "${installer}"`、`HYPOMUX_SIGNED_INSTALLER_TEST`、`cp …` 计数 == 1、mutable tag 禁止、`files: artifacts/latest.json` 禁止、`seq 1 20` 禁止。**顺序断言改写**为：`Verify both Release installers are byte-identical` 必须晚于 `- name: Upload installer to GitHub Release` **和** `release asset-upload`。**新增** `files: artifacts/${{ …installer_name }}` 断言。 |
| `TestReleaseNotesAreTheSingleSourceForReleaseBodiesAndManifest` → **`TestReleaseNotesAreTheSingleSourceForReleaseBodies`** | 改名 + 收窄。**保留** lead 要求的"单一来源"约束：路径构造、空文件校验、GitHub body、CNB body、`cmp --silent "${release_notes_path}" artifacts/cnb-release-notes.md`。**删除**：`--rawfile notes "${release_notes_path}"`、`notes: $notes`、`jq -j '.notes' artifacts/latest.json > artifacts/manifest-notes.md`、清单比对、`notes: ""` 禁止。 |
| `TestReleaseTrustSmokeWorkflowIsReadOnly` | 必需要素收窄为 `git ls-remote`、`github_commit`、`cnb_commit`、`release get -t "${RELEASE_TAG}"`、`secrets.CNB_TOKEN`、git-cnb digest；**新增** `go -C desktop run ./cmd/release-version -tag "${RELEASE_TAG}"`（保证被保留的版本步骤不被误删）。**删除**：`UPDATE_MANIFEST_ED25519_PRIVATE_KEY`、`-verify-public-key "${public_key}"`、`refs/heads/${{ steps.version.outputs.channel }}`、两个 raw channel URL、`-verify-only`。**禁止清单新增** `latest.json`、`update-manifest-sign`、`steps.version.outputs.channel`。只读断言（`asset-upload` / `release create` / `upload-artifact` / `action-gh-release` / `git push` / `git commit-tree`）全部保留。 |
| `TestPreviewPublishingIsIsolatedFromStable` | 仅删一行：`channel_ref="refs/heads/${{ steps.version.outputs.channel }}"`（该串已不存在于 build.yml）。其余全留：`prerelease == 'true'` 出现 2 次、`--prerelease=`、`--make-latest=`、`-tag $env:GITHUB_REF_NAME -write -notes`、`group: hypomux-release-publish`，以及"Prepare and validate release version" 必须早于 "Build and package Wails desktop" 的顺序断言。 |
| **`TestReleaseWorkflowsCarryNoUpdateChannelArtifacts`** | **新增**（见上）。 |

**未触碰**：该文件中所有 legacy 恢复脚本（`legacy-v22-recover.ps1`、`RecoverLegacyV22Network`）、NSIS、MSIX、Wails 配置、单实例身份等无关用例，一处未改。注意该文件里 "legacy" 多指 Python 旧版恢复脚本，与 updater 无关——已逐处甄别。

---

## 5. 文档改动

### `README.md`（5 处，均按 lead 指定行号）

| 行 | 改动 |
| --- | --- |
| 26 | `…升级 sing-box 至 1.14.2，支持正式版／预览版更新渠道。` → `…升级 sing-box 至 1.14.2。`（去掉渠道表述） |
| 39 | `连接查看、日志与更新检查` → `连接查看与日志` |
| 52 | **删除**「自动更新元数据通过独立的 signed update channel 分发并使用 Ed25519 验证，客户端随后校验安装包大小、SHA-256 与 Windows Authenticode 签名。」**并替换为事实陈述**「本 fork 不包含应用内自动更新，升级需由用户自行前往上述发布页获取新版本。」**同段 SignPath / 发布者为 SignPath Foundation 的说明完整保留**（用户硬性要求）。 |
| 61 | 从网络通信披露清单中移除「从官方 signed update channel 检查更新、从 GitHub 或 CNB Release 下载安装包」，保留转发流量与连通性验证 |
| 214 | 「只读核对更新清单密钥、GitHub/CNB Tag、CNB Release 访问权限及已有 signed update channel」→ 「只读核对 GitHub/CNB Tag 是否指向同一提交以及 CNB Release 访问权限」 |

> **需要 lead 复核的一点**：`:26` 位于 `## 2.7.0 新版本` 这个版本变更记录块内，属于历史发布记录，而 lead 对 `.github/release-notes/` 的原则是「发布记录保留不动」。此处按 lead 的逐行明确指令执行了删除；若倾向严格保留历史，被删的「支持正式版／预览版更新渠道」在 `.github/release-notes/v2.7.0.md:94` 仍有原文记录，不会丢失信息。

### `docs/RELEASE_VERSIONING.md`

- `:23` 版本同步清单中删去「更新器」。
- `:40` Release Trust Smoke Test 步骤改为「只读核对 GitHub 与 CNB Tag 是否指向同一提交，并确认 CNB Release 可读」。
- `:42` `test` signing mode 的说明由「不是预发布渠道」改为「不能用于正式发布」（原措辞依赖已不存在的渠道概念）。
- `:44–57` 整个 `## 更新渠道` 章节**替换**为 `## 本 fork 不提供应用内自动更新`，内容为事实陈述：不再生成 `latest.json`、不再 Ed25519 签清单、不再维护正式版／预览版渠道、设置中更新渠道选项已移除、客户端不会自行检查或下载；**明确声明安装包代码签名不受影响**（SignPath 签名、Windows 打包、双镜像上传全部保留）；升级方式为手动下载 `HypoMux_Setup_<标签>.exe` 并确认发布者为 SignPath Foundation；Beta/RC 仍标为预发布、不设为 latest，程序不会提示也不会自动降级。

### 未改动

`.github/release-notes/**`（`v2.5.7.md` … `v2.7.0.en.md`）——历史更新日志，按 lead 指示保留。`docs/migration/**` 三处（`ui-migration-matrix.md:117`、`functional-sprint-changelog.md:78`、`feature-inventory.md:448`）、`reports/**` 一律未动。

---

## 6. 收尾 grep

对 `.github/`、`README.md`、`docs/RELEASE_VERSIONING.md` 搜 `latest.json|update channel|更新渠道|auto.?update|自动更新|Ed25519|update-manifest-sign|channel|manifest|mktree|verify-only`，剩余命中**全部为有意保留**：

| 位置 | 性质 |
| --- | --- |
| `.github/workflows/build.yml:509-510` | 我加的注释，陈述"没有自动更新"这一**缺席事实** |
| `.github/workflows/build.yml:535` | 我加的注释，解释 staging 步骤**不是**更新渠道产物 |
| `README.md:52` | 新增的事实陈述「本 fork **不包含**应用内自动更新」 |
| `docs/RELEASE_VERSIONING.md:44,46` | 新的 `## 本 fork 不提供应用内自动更新` 章节正文 |
| `.github/release-notes/**` 共 22 处 | 历史版本更新日志，按契约保留 |

`README.md` 除 `:52` 的否定式陈述外已无任何更新链路表述。`create-release-tag.yml` 与 `go-engine.yml` 零命中。

---

## 7. 验收输出

```
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
cd <repo>\desktop
```

**`gofmt -l .`** — 无输出，exit 0 ✅

**`go vet ./...`** — 无输出，exit 0 ✅

**`go test ./internal/releaseversion/... .`**
```
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion	0.317s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop	0.254s
```
exit 0，**零 FAIL** ✅（`-count=1` 强制未缓存重跑）

**`go test -count=1 ./...`**（全模块复核）
```
ok  	github.com/Hypostasis-Cat/HypoMux/desktop	0.254s
?   	github.com/Hypostasis-Cat/HypoMux/desktop/build/windows/syso	[no test files]
?   	github.com/Hypostasis-Cat/HypoMux/desktop/cmd/release-version	[no test files]
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient	3.534s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform	0.253s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails	0.526s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion	0.317s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/services	56.080s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/startup	0.250s
```
exit 0 ✅ —— backend 对 `internal/services/updater*` 的删除**未引入任何编译错误或测试失败**，`update-manifest-sign` 包已从列表中消失。

**YAML 校验**（bundled python `yaml.safe_load`）：三个 workflow 全部解析通过；build.yml `build`=31 步 / `release`=12 步，release-smoke.yml `smoke`=5 步，create-release-tag.yml `create-tag`=4 步。

### Lead 报告的 3 个 FAIL —— 已全部修复

`TestReleaseNotesAreTheSingleSourceForReleaseBodies:434`、`TestReleaseTrustSmokeWorkflowIsReadOnly:497`、`TestPreviewPublishingIsIsolatedFromStable:545` 均为我改动进行到一半时的中间状态所致（测试已改、workflow 引用尚未同步收尾）。现已全部改完并通过，见上表。

---

## 8. 他人作用域的残留观察（**未修改，仅报告**）

backend 成员在我收工时已自行修复了先前发现的两处：

- ~~`desktop/internal/releaseversion/metadata.go:25`~~ —— 已从同步路径表移除（`git diff` 显示该文件 -1 行）。
- ~~`desktop/internal/releaseversion/version_test.go:53`~~ —— 已同步移除（-2 行）。

当前 `releaseversion` 包内仅剩一处**无害的注释级残留**：

- `desktop/internal/releaseversion/version.go:72`
  ```
  // Key retains the old updater's two-to-four-part numeric versions while adding
  ```
  纯注释，解释 `Key` 的版本段格式为何沿用旧更新器的两到四段数字版本。语义上已无"更新器"实体，但作为历史沿革说明保留并非错误。**属 backend 的 `releaseversion/**` 作用域，我未改。**

其他观察：

- `desktop/frontend/src/pages/SettingsPage.tsx` 已由前端成员修改（`git status` 显示 M），其中更新渠道相关部分按 SCOPE 约定未由我触碰。
- `reports/**` 中大量 `update-manifest-sign`、`latest.json`、`release-smoke.yml:37-51` 的描述（`HypoMux-项目分析报告.md:261,415,429`、`parts/02-desktop-backend.md:432,489,503`、`parts/04-protocol-ci.md:153,156,183,443,480`、`parts/05-security.md:226`、`parts/06-health-docs.md:55,107`、`vnic/*.md` 多处）现与代码不符——按契约属历史记录，一律未改。若 lead 希望后续刷新这些报告，需要单独授权。
- 未执行 `git add` / `git commit`，仅修改工作区文件。