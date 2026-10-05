# Go 端更新链路摘除 · 后端交付（updater-backend）

范围契约：`reports/updater-removal/SCOPE.md`。本文件只覆盖 `updater-backend` 的写作用域。
未执行任何 `git add` / 提交，只改工作区文件。

---

## 1. 删除的文件（9 个，全部在 `desktop/internal/services/`）

| 文件 | 说明 |
| --- | --- |
| `desktop/internal/services/updater.go` | 更新服务主体（`UpdaterService`、`UpdateProgress`、`ReleaseInfo`、清单拉取/验签/下载/安装） |
| `desktop/internal/services/updater_windows.go` | Windows 分支实现 |
| `desktop/internal/services/updater_other.go` | 非 Windows 桩实现 |
| `desktop/internal/services/updater_authenticode_windows.go` | Authenticode 安装包校验（Windows） |
| `desktop/internal/services/updater_authenticode_other.go` | Authenticode 桩实现 |
| `desktop/internal/services/updater_test.go` | 主体测试 |
| `desktop/internal/services/updater_preview_test.go` | preview/stable 渠道测试 |
| `desktop/internal/services/updater_windows_test.go` | Windows 分支测试 |
| `desktop/internal/services/update_manifest_ed25519_public_key.txt` | `//go:embed` 的 Ed25519 公钥（46 B） |

**删除前确认**：`desktop/internal/services/` 下 `updat*` / `update_manifest*` 过滤后正好就是上面这 9 个，没有漏网的。
**公钥引用确认**：全仓（排除 `reports/**`、`docs/migration/**`、`node_modules/**`、构建产物）对
`update_manifest_ed25519_public_key` 的引用只有 `updater.go:49` 的 `//go:embed` 一处，
加上 `desktop/installer_layout_test.go`（CI 成员文件）与 workflow 文本断言，均在他人作用域内。删除安全。

`git status` 对应：`D desktop/internal/services/updater*.go`、`D .../update_manifest_ed25519_public_key.txt`。

---

## 2. `desktop/main.go`

只删了两行，**没有碰窗口标题那处未提交改动**（`Title: "HypoMux 自定义版",` 及其上方中文注释仍然在）。

- 删掉 `updaterService := services.NewUpdaterServiceWithSettings(settingsService, desktop.Quit)`（原 :164）
- 删掉 `app.RegisterService(application.NewService(updaterService))`（原 :193）

删完后 `grep -n 'updater|Updater' desktop/main.go` → **No matches found**。
`desktop` 局部变量仍被 `desktop.HideToTray()` / `diagnosticsService` 闭包等大量使用，没有产生未使用变量。
`go vet ./...` 无输出即证明没有多余 import / 变量。

---

## 3. `desktop/internal/services/settings.go` —— 摘除 `UpdateChannel`

删了 6 处分支（文件当前行号是改动后的）：

| 位置 | 删掉的内容 |
| --- | --- |
| `AppSettings` 结构体（原 :28） | `UpdateChannel string \`json:"update_channel,omitempty"\`` |
| `DefaultSettings()`（原 :65） | `UpdateChannel: "stable",` |
| `UpdateFields()` switch（原 :276-277） | `case "update_channel": next.UpdateChannel = values.UpdateChannel` |
| `updateLocked()` 开头（原 :316-321） | “空值则保留旧值、再兜底 stable” 的整个 if 块 |
| 加载校验（原 :534-535） | `if loaded.UpdateChannel != "stable" && != "preview" { loaded.UpdateChannel = "stable" }` |
| `validateSettings()`（原 :664-666） | `if value.UpdateChannel != "" && ... return fmt.Errorf("不支持的更新渠道：%s", ...)` |

`updateLocked()` 里紧邻的 `DNSEgressMode` 兜底分支原样保留（那是另一个兼容性兜底）。
`UpdateFields` 的 `default:` 分支保持不变，所以前端若仍传 `update_channel` 会走到
`不支持通过设置页修改字段：update_channel` —— 这正是我们想要的显式拒绝。

### 兼容性（SCOPE §兼容性要求）

`settings.go` 的加载路径只有裸 `json.Unmarshal`（现 :486 `load` 分支、:591 `migrateLegacySettings` 分支），
全文件 **没有 `DisallowUnknownFields`**（已 grep 确认，`git diff` 前也确认）。
`encoding/json` 默认忽略未知键，因此旧 `settings.json` 里的 `"update_channel": "preview"` 会被静默丢弃，
其余字段照常加载。已用新测试把这个行为钉住。

### 测试改动

- `desktop/internal/services/settings_fields_test.go`
  - 删除 `TestUpdateChannelPersistsAndDefaultsSafely`。
  - 新增 `TestLegacyUpdateChannelFieldIsIgnored`：写一份带 `"update_channel":"preview"` 的旧 `settings.json`，
    断言 `mode` / `socks_port` 正常加载，并断言 `UpdateFields(..., []string{"update_channel"})` 现在必须被拒绝。
    （该测试继续用到 `os` / `filepath`，所以 import 无需删。）
  - 文件里其余三个 `TestSettingsFieldUpdate*` 用例一字未动。
- `desktop/internal/services/settings_migration_test.go`：**无需改动**。
  任务书里写的「约 :19 和 :39」与实际文件不符——该文件从来就没有 `update_channel` / `UpdateChannel`
  引用（`migrateLegacySettings` 也不解码这个键），工作区版本与 HEAD 一致。三个用例
  （`TestMigrateLegacySettingsPreservesSemantics`、`TestLegacyMigrationRollbackRestoresNewSettings`、
  `TestSettingsUpdateDefaultsMissingDNSEgressMode`）在改动后仍全部 PASS，见 §5。

---

## 4. `desktop/internal/releaseversion` —— 只删两行

- `metadata.go`：`edits` 列表里删掉
  `{"internal/services/updater.go", `(CurrentVersion\s*= ")[^"]+`, display, 1},`
- `version_test.go:53`：`TestMetadataRoundTripInIsolatedCheckout` 的隔离 checkout 路径清单里删掉
  `"internal/services/updater.go"`。

其余 10 个路径项（`VERSION`、`Taskfile.yml`、`build/config.yml`、`frontend/package.json`、
`frontend/src/product.ts`、`build/windows/nsis/wails_tools.nsh`、`build/windows/nsis/version.nsh`、
`build/windows/info.json`、`build/windows/wails.exe.manifest`、`build/windows/msix/template.xml`、
`build/windows/msix/app_manifest.xml`）**一个都没动**。
`TestMetadataRoundTripInIsolatedCheckout` 通过（它同时验证 `-check` 模式能发现过期版本、写回后能对齐、
以及 `wails.exe.manifest` 里的 `version="6.0.0.0"` 依赖身份不被改动）。

---

## 5. 验收输出

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
cd <repo>\desktop
```

### `gofmt -l .`
```
########## gofmt -l . ##########
[gofmt exit=0 / no output above == PASS]
```
**无输出 ✅**

### `go vet ./...`
```
########## go vet ./... ##########
[go vet exit=0 / no output above == PASS]
```
**无输出 ✅**

### `go test ./...`

我负责的包（`-count=1` 强制不走缓存）**全绿**：
```
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/services	44.498s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion	0.278s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient	(cached)
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform	(cached)
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails	(cached)
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/startup	(cached)
```

关键用例逐条验证：
```
--- PASS: TestLegacyUpdateChannelFieldIsIgnored (0.00s)
--- PASS: TestSettingsFieldUpdatePreservesConcurrentOwners (0.00s)
--- PASS: TestSettingsFieldUpdatesMergeUnderLock (0.00s)
--- PASS: TestSettingsFieldUpdateRejectsInvalidOrForeignFieldsAtomically (0.00s)
--- PASS: TestMigrateLegacySettingsPreservesSemantics (0.00s)
--- PASS: TestLegacyMigrationRollbackRestoresNewSettings (0.01s)
--- PASS: TestSettingsUpdateDefaultsMissingDNSEgressMode (0.00s)
--- PASS: TestReleaseVersionOrderingAndWindowsMapping (0.00s)
--- PASS: TestMetadataRoundTripInIsolatedCheckout (0.05s)
```

**⚠️ 全仓 `go test ./...` 目前有 3 个 FAIL，全部落在别人作用域里、与我的改动无关：**

```
--- FAIL: TestReleaseNotesAreTheSingleSourceForReleaseBodies (0.00s)
    installer_layout_test.go:434: release notes are not wired to every release consumer: missing "--rawfile notes \"${release_notes_path}\""
--- FAIL: TestReleaseTrustSmokeWorkflowIsReadOnly (0.00s)
    installer_layout_test.go:497: release trust smoke workflow is missing "UPDATE_MANIFEST_ED25519_PRIVATE_KEY"
--- FAIL: TestPreviewPublishingIsIsolatedFromStable (0.00s)
    installer_layout_test.go:545: missing release isolation guard: channel_ref="refs/heads/${{ steps.version.outputs.channel }}"
FAIL	github.com/Hypostasis-Cat/HypoMux/desktop	0.215s
```

判定依据：
1. 三条断言的失败点全在 `desktop/installer_layout_test.go`（`updater-ci` 的写作用域，本任务书第 5 步明确说**不要动**）。
2. 我第一次跑 `go test ./...` 时（早于 CI 成员完成其编辑）该包是 `ok`；两次运行之间
   `git status --short -- desktop` 里 `D desktop/installer_layout_test.go → M`（该文件被 CI 成员改成了新版本），
   失败是随之出现的。
3. 失败原因都是「workflow 里已经没有 `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` / `channel_ref` / `--rawfile notes` 这些
   更新渠道产物了」，正是在删 CI 更新链路，属于 CI 成员收尾范围。

**归属：updater-ci。** 请让 TA 知道 `go test ./...` 现在是红的，红在 `installer_layout_test.go`。

---

## 6. 残留引用清单（grep 结果 + 归属）

以 `UpdaterService|updater_method|update_channel|UpdateChannel|update_manifest_ed25519|update-manifest-sign`
为 pattern，排除 `.git/`、`reports/**`、`docs/migration/**`、`node_modules/**`、
`portable-package/**`、`dist/**` 与二进制产物后的全仓文本扫描结果：

| 位置 | 归属 | 状态 |
| --- | --- | --- |
| `desktop/installer_layout_test.go:333,334,452`（`UPDATE_MANIFEST_ED25519_PRIVATE_KEY`、`update-manifest-sign`） | **updater-ci** | TA 正在改，见 §5 |
| `desktop/frontend/bindings/.../services/index.ts:15,28` — `import * as UpdaterService from "./updaterservice.js"` | **updater-frontend** | 生成的 Wails binding，`updaterservice.ts` **文件仍在**，所以 `tsc --noEmit` 不会因此挂；只是陈旧产物，需要重新生成 bindings 后自然消失 |
| `desktop/frontend/bindings/.../services/models.ts:39` — `"update_channel"?: string;` | **updater-frontend** | 同上，陈旧生成产物 |
| `desktop/frontend/src/platform/services.ts:100` — `update_channel?: "stable" \| "preview";` | **updater-frontend** | `AppSettings` 补的兼容可选字段，**建议保留**：老 settings.json 里的键还会被前端读到，删了要连带处理类型面 |
| `desktop/frontend/src/platform/services.ts:267` — `...desktop.internal.services.UpdaterService.${method}` | **updater-frontend** | 该调用器现在指向已不存在的服务，是**死引用**（运行时只会报错，不会编译错）。TA 删掉对应 UI 调用即可 |
| `desktop/internal/services/settings_fields_test.go` 的 4 处 | 我 | 有意保留，是兼容性回归测试 |

已被其他成员在我工作期间处理掉的（无需再管）：
- `desktop/cmd/update-manifest-sign/main.go`、`main_test.go` —— **已删除**（`git status` 显示 `D`），
  不是我删的。删完之后 `desktop/cmd/` 下只剩 `release-version`，很好。
- `.github/workflows/build.yml:510,535` 只剩注释里提到 “latest.json / update channel” 字样，
  实际步骤已移除，workflow 无任何可执行引用。
- `.github/release-notes/*.md` 是历史发布说明，不动。

`desktop/frontend/src/pages/SettingsPage.tsx` / `SettingsPage.test.tsx` 的更新渠道部分按 SCOPE §3 我没碰。

---

## 7. 我判断有风险的地方

1. **`installer_layout_test.go` 里存在一对自相矛盾的断言（他人文件，我只报告不动）。**
   同一文件里 `TestReleaseWorkflowsCarryNoUpdateChannelArtifacts` 要求
   `build.yml` / `release-smoke.yml` / `create-release-tag.yml` **不得**出现
   `artifacts/latest.json`、`update-manifest-sign`、`steps.version.outputs.channel` 等 token；
   而 `TestReleasePublishesOneSignedInstallerThenUpdatesSignedChannel` 又**要求** `build.yml`
   出现 `artifacts/latest.json`。这两条不可能同时通过。目前 FAIL 的是后者相关的那一组
   （`TestReleaseNotesAreTheSingleSourceForReleaseBodies` / `TestReleaseTrustSmokeWorkflowIsReadOnly` /
   `TestPreviewPublishingIsIsolatedFromStable`），但前面那条“禁止”组一旦 workflow 彻底清干净也会翻转成
   `TestReleasePublishesOneSignedInstallerThenUpdatesSignedChannel` 失败。请 updater-ci 确认到底要留哪一套语义。
   ⚠️ 这不是我能修的：`installer_layout_test.go` 不在我的写作用域内。

2. **`desktop.Quit` 语义现在只被诊断服务之外的地方间接依赖。**
   updater 以前是唯一「装完更新器前必须走正常生命周期退出」的持有者（`NewUpdaterServiceWithSettings(settingsService, desktop.Quit)`）。
   现在 `wails.NewDesktopHost` 的退出钩子里只剩 `diagnosticsService.Shutdown()` → `hypervAdapterService.Shutdown()` → `engineService.Shutdown()`。
   这对普通退出没问题；但「外部触发重启/替换二进制」的能力随 updater 一起消失了。
   如果产品上还有「装完更新器后自动重启」这类需求，它已经无处可寻——目前 SCOPE 没有要求保留，我没做任何替代实现。

3. **`UpdateFields` 的 `update_channel` 现在走 `default:` 返回错误。**
   这是刻意的显式拒绝（前端若还在发这个字段会报错而不是静默忽略）。
   风险点：前端成员如果**还没**删掉「更新渠道」下拉的调用，运行时会弹错误提示。
   `SettingsPage.tsx` 按 SCOPE 已经删掉了下拉，所以目前安全；但请前端成员确认没有别处（比如首屏 bootstrap
   的全量 `Update`）还在带 `update_channel`。注意 `Update`（全量替换）路径不受影响——未知字段直接被 json 忽略。

4. **旧 `settings.json` 的兼容只做了「读」方向的保证，没做「写」方向。**
   老配置里的 `"update_channel": "preview"` 加载时被丢弃；下一次应用设置写盘时，
   `settings.json` 会被整体重写且不再含这个键。也就是说**用户的 preview 偏好会被静默清空**。
   因为更新链路已整体删除，这是期望行为，但如果将来要加「本地自更新」，需要另做迁移保留。

5. **`releaseversion` 与 `VERSION` 的一致性不再覆盖 updater.go。**
   之前 `SyncMetadata` 会把 `updater.go` 里的 `CurrentVersion` 一起改掉；现在少了一个同步点。
   `frontend/src/product.ts` 仍在清单里，前端展示的版本号来源没变——**不影响用户可见的版本号**。
   唯一影响是 `cmd/release-version -check` 的覆盖面变窄了，`go -C desktop run ./cmd/release-version -check`
   依然通过（已在 `TestMetadataRoundTripInIsolatedCheckout` 中验证）。

6. **并发编辑风险（已发生）。**
   我跑验收期间，`updater-ci` 和 `updater-frontend` 同时在改文件，`desktop` 包的测试结果在两次运行之间
   从 `ok` 变成了 3 个 FAIL。建议最终由 lead 在**所有人收工后**统一重跑一次三条验收命令再判定。

---

## 附：完整改动文件列表（我动的，共 14 个）

删除 9 个（见 §1），修改 5 个：

- `desktop/main.go`（2 行）
- `desktop/internal/services/settings.go`（6 处）
- `desktop/internal/services/settings_fields_test.go`（换 1 个用例）
- `desktop/internal/releaseversion/metadata.go`（1 行）
- `desktop/internal/releaseversion/version_test.go`（1 行）