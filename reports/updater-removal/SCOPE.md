# 摘除更新链路 · 范围契约（团队共用）

## 目标

把 HypoMux 的**应用内自动更新链路整条摘掉**：程序永远不会再检查、下载或安装上游官方版本；同时把 CI 里**只服务于自动更新的那部分**（`latest.json` 清单生成 / Ed25519 签名 / 更新渠道发布 / release-smoke 校验）一并删除。

**用户明确要求：安装包的代码签名必须保留**（SignPath + `HYPOMUX_SIGNED_INSTALLER_TEST` 等签名路径不动）。Windows 打包任务、GitHub Release 上传、CNB Release 上传都保留。

本仓库是上游开源项目的 fork，自行构建分发，因此不能让客户端去拉上游签名清单里的官方版本。

## 硬性禁止

1. **不要回退或改动与本任务无关的在途修改**。当前工作区有大量未提交的 vNIC/设置页改动（`git status` 里的 M 文件，含 `desktop/frontend/src/pages/VirtualAdaptersPage.tsx`、`desktop/internal/services/hyperv_adapter*.go`、`desktop/frontend/src/pages/HomePage.tsx`、`desktop/frontend/src/components/vnic/*` 等）。只做本任务必要的最小改动，不要顺手重构、不要格式化无关文件。
2. `desktop/main.go` 已有未提交改动：窗口标题改成 `HypoMux 自定义版`（带一行中文注释）。**保留它**。
3. `desktop/frontend/src/pages/SettingsPage.tsx` 已删掉「更新渠道」下拉，`SettingsPage.test.tsx` 对应用例已改写。**不要再动这两个文件的更新渠道部分**（后端字段清理属于 backend 任务）。
4. 历史文档 `docs/migration/**` 与 `reports/**` 是**当时状态的记录**，不要改写。只改仍在生效的文档。

## 写作用域（互斥）

| 成员 | 允许修改 |
| --- | --- |
| updater-backend | `desktop/internal/services/**`（**不含** `desktop/installer_layout_test.go`）、`desktop/main.go`、`desktop/internal/releaseversion/**` |
| updater-frontend | `desktop/frontend/**` |
| updater-ci | `.github/workflows/**`、`.github/release-notes/**`（若有）、`desktop/installer_layout_test.go`、`README.md`、`docs/RELEASE_VERSIONING.md` |

`git add`/提交一律**不要执行**，只改工作区文件。

## 验收命令

Go（必须先前置 PATH，Go 不在会话默认 PATH 里）：

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
cd <repo>\desktop
gofmt -l .          # 必须无输出
go vet ./...        # 必须无输出
go test ./...       # 必须全绿
```

前端：

```powershell
cd <repo>\desktop\frontend
node node_modules\typescript\bin\tsc --noEmit          # 必须无输出
node node_modules\vitest\vitest.mjs run               # 必须全绿
```

注意：`go test ./...` 在本机会输出 `FF`（TUN/管理员相关）与网络相关跳过项属正常，只要**没有 FAIL 行**即可；把关键行记录下来。

## 兼容性要求

旧用户的 `settings.json` 里可能仍有 `"update_channel": "preview"` 等字段。删除 Go 字段后必须**仍能正常加载**（`encoding/json` 默认忽略未知字段）。settings 相关测试不得因此新增失败。