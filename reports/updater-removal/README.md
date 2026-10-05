# 摘除应用内自动更新链路 · 总览

日期：2026-10-04 ｜ 分支：main ｜ 未提交（仅工作区改动）

## 目标与边界

本 fork 是上游 HypoMux 的二次分发版本，因此**程序不再检查、下载或安装上游官方版本**；CI 里只服务于自动更新的部分（更新清单生成 / Ed25519 签名 / 更新渠道发布）一并删除。

**用户明确要求：Windows 安装包的代码签名（SignPath）必须保留** —— 打包、双 Release 镜像上传、签名校验、发布者比对全部原样保留。

## 最终验证（全员收工后由 lead 统一重跑）

| 检查 | 结果 |
| --- | --- |
| `gofmt -l .`（desktop） | 无输出 ✅ |
| `go vet ./...` | 无输出 ✅ |
| `go test -count=1 ./...` | 全 ok，零 FAIL ✅ |
| `tsc --noEmit` | 无输出 ✅ |
| `vitest run` | 45 files / 413 tests 全通过 ✅ |

## 分工与交付

| 成员 | 范围 | 报告 |
| --- | --- | --- |
| updater-backend | Go 端 updater 服务、`update_channel` 设置字段、版本同步表 | [backend.md](backend.md) |
| updater-frontend | 关于页更新 UI、前端服务绑定、i18n、死样式 | [frontend.md](frontend.md) |
| updater-ci | CI 更新渠道步骤、release-smoke、构建契约测试、文档 | [ci.md](ci.md) |
| lead | 范围契约、`releaseversion.Channel()` 残留清理、最终统一验证 | 本文 |

范围契约见 [SCOPE.md](SCOPE.md)。

## 改动概览

**Go（删除为主）**

- 删 `desktop/internal/services/` 下 9 个文件：`updater.go`(691) / `updater_windows.go` / `updater_other.go` / `updater_authenticode_windows.go`(238) / `updater_authenticode_other.go` / `updater_test.go`(602) / `updater_preview_test.go` / `updater_windows_test.go` / `update_manifest_ed25519_public_key.txt`
- 删 `desktop/cmd/update-manifest-sign/`（main.go + main_test.go）
- `desktop/main.go`：去掉 updater 服务的构造与注册（窗口标题改动保留）
- `settings.go`：摘除 `UpdateChannel` 的 6 处分支；新增回归测试确认旧 `settings.json` 里的 `update_channel` 被安全忽略
- `releaseversion`：从版本同步清单移除 `internal/services/updater.go`；删除已无消费者的 `Channel()`

**前端**

- `AboutPage.tsx` 253 → 87 行：删除检查更新按钮、下载/安装状态机、更新说明渲染与「发现新版本」弹窗；保留版本号、官网/GitHub 链接、SignPath 说明与赞助区
- `platform/services.ts`：删除 `appServices.updater`、`updaterMethod`、三个更新相关类型、`update_channel`
- i18n：zh/en 各删 16 个 `about_update_*` key
- `app.css` 删 108 行 `.update-*` 死样式；`errorCodes.ts` 删仅服务于更新提示的 `HM-E1601`

**CI / 文档**

- `build.yml` 删 4 步（清单生成、清单签名、发布更新渠道、校验双渠道），release job 16 → 12 步
- `release-smoke.yml` 保留，7 → 5 步（只留 tag 镜像与 Release 访问检查）
- `installer_layout_test.go`：新增「workflow 不得再出现更新渠道产物」守卫；原「发布后更新渠道」用例收窄为「签名安装器双镜像一致」
- README 5 处、`docs/RELEASE_VERSIONING.md` 的更新渠道章节改为事实陈述

## 有意保留

- SignPath 代码签名、Windows 打包、GitHub/CNB Release 上传、`HypoMux_Setup_<版本>.exe` 版本化命名（人工下载名）
- Release notes 单一来源约束（Release body 与镜像 body 的逐字节一致性）
- `.github/release-notes/**`、`docs/migration/**`、`reports/**` 中的历史记录

## 遗留项（可选后续处理）

1. `react-markdown` / `remark-gfm` 已成为死依赖，需用 `pnpm remove` 一并处理以免破坏 `--frozen-lockfile`。
2. `productInfo.releases` 成为无引用的静态常量（`product.ts`）。
3. 老配置里的 `update_channel` 偏好会在下次写盘时被静默清空（预期行为）。
