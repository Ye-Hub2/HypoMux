# 正式版、Beta 与 RC 发布

## 版本格式

- 正式版：`2.7.0`，Git 标签为 `v2.7.0`。
- Beta：`2.8.0-beta.1`、`2.8.0-beta.2`。
- RC：`2.8.0-rc.1`、`2.8.0-rc.2`。
- 同一基础版本按 `beta.2 < beta.10 < rc.1 < 正式版` 排序。

使用小写 `beta.N` / `rc.N`，序号从 1 开始，不接受 `beta1`、`bata1`、前导零或构建元数据后缀。三个基础版本段为 0–65535，预发布序号为 1–29999，以便映射 Windows 版本资源。

## 统一设置版本

在仓库根目录执行（只修改本地文件，不创建标签或发布）：

```powershell
go -C desktop run ./cmd/release-version -version 2.7.0 -write
# 后续预发布版本示例
go -C desktop run ./cmd/release-version -version 2.8.0-beta.1 -write
go -C desktop run ./cmd/release-version -check
```

`desktop/VERSION` 是本地版本源。命令同步前端展示、package.json、Core Taskfile、Wails 配置、Windows EXE / NSIS / MSIX 元数据。README 中面向公众的最新正式版标识由正式发布时维护，不随 Beta / RC 改动。

Windows 数字版本和产品展示版本分别生成。例如：

| 展示版本 | Windows 数字版本 |
| --- | --- |
| 2.8.0-beta.1 | 2.8.0.1 |
| 2.8.0-rc.1 | 2.8.0.30001 |
| 2.8.0 | 2.8.0.65535 |

这样安装包和 EXE 的版本资源保持纯数字，正式版排在同一基础版本的所有预发布版之后。不要手动修改生成的版本字段；重新生成 Wails build assets 后再执行版本同步命令。

## 发布步骤

1. 准备 `.github/release-notes/<完整标签>.md`，例如 `v2.8.0-beta.1.md`，内容不能为空；把代码和说明合入 main。
2. 在 main 上运行 **Create Release Tag**，填入完整标签。工作流先验证格式和发布说明，再同步 GitHub / CNB 标签。
3. 运行 **Build Desktop**，选择该标签，`signing_mode` 选择 `publish`。标签版本会在构建、测试、签名前注入所有产品版本字段，避免安装包文件名与内部版本不一致。
4. 可用 **Release Trust Smoke Test** 指定同一标签，只读核对 GitHub 与 CNB Tag 是否指向同一提交，并确认 CNB Release 可读。

普通 main / PR 构建会校验 `VERSION` 和生成字段是否一致。发布 Beta / RC 仍使用正式代码签名；`test` signing mode 只是签名测试，不能用于正式发布。

## 本 fork 不提供应用内自动更新

本 fork 已移除应用内自动更新链路。发布流程中不再生成更新清单（`latest.json`），不再用 Ed25519 对清单签名，也不再维护正式版／预览版更新渠道；设置中的更新渠道选项随之移除。因此客户端不会自行检查或下载新版本。

安装包代码签名不受影响：`build.yml` 的 SignPath 签名、Windows 打包、GitHub Release 与 CNB Release 上传全部保留，两处仍分发同一份 SignPath 签名安装包。

升级方式为用户手动获取：前往 [GitHub Releases](https://github.com/Hypostasis-Cat/HypoMux/releases/latest) 或 [腾讯 CNB Release](https://cnb.cool/Hypostasis-Cat/HypoMux/-/releases/latest) 下载 `HypoMux_Setup_<完整标签去掉 v>.exe`，确认 Windows 属性中的发布者为 **SignPath Foundation** 后安装覆盖。

Beta / RC 的 GitHub 与 CNB Release 仍标为预发布、不设置为 latest；由于没有更新器，是否升级完全由用户决定，程序不会提示也不会自动降级。

发布操作须使用包含这些工作流改动的新提交；旧标签仍会使用旧工作流。
