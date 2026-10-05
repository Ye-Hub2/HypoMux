# 95 · 提交与 GitHub CI 执行报告（ci-runner / task-13）

- 执行者：teammate `ci-runner`（task-13；本轮唯一写入的文件就是本报告）
- 工作目录：`<repo>`（Windows 10.0.19041 / PowerShell 5.1）
- 远端：`origin = https://github.com/Ye-Hub2/HypoMux`，分支 `main`
- 推送前基线：`HEAD == origin/main == e66016e129c321557b48935629521e917f17b76e`
- 权威输入：`reports/vnic/00-frozen-interface.md` §7、`reports/vnic/90-verification.md` §0

**状态：✅ CI 全绿（build.yml 全步骤 success，唯一 run 已完成）**

---

## 1. 提交（commit）

| 项 | 值 |
|---|---|
| 提交 SHA | `37571e5a0531c16621cf4ac3e7511ac21135aa00`（短 `37571e5`） |
| 父提交 | `e66016e129c321557b48935629521e917f17b76e`（快进，无 force、无改写历史） |
| 提交信息 | `feat: replace the AI assistant with a virtual adapter action on home`（逐字使用 Lead 给的消息，30 行） |
| 提交方式 | 消息写入 `%TEMP%\hypomux-commit.txt`（`Set-Content -Encoding ascii`，实收 1508 字节 / 无 BOM / 非 ASCII 字节 0），`git commit -F` |
| 提交身份 | `git -c user.name=Hypostasis-Cat -c user.email=<EMAIL>`（沿用仓库既有作者；本机原本未配置 git identity） |
| 变更规模 | **99 个文件**（M 32 / D 58 / A 9），`2686 insertions(+), 6758 deletions(-)` |

### 1.1 提交前的暂存自检

暂存命令（显式排除报告与生成物）：

```
git add -A -- . ':(exclude)reports' ':(exclude)desktop/frontend/bindings' ':(exclude)desktop/frontend/dist' ':(exclude)desktop/frontend/node_modules'
```

自检结果：

- `git diff --cached --name-only | Select-String '^reports/'` → **无输出**（`reports/` 未进暂存区）
- `git status --porcelain` 中 `reports/` 仅以 `?? reports/` 出现（未跟踪），提交后工作树只剩这一条
- `desktop/frontend/bindings`、`desktop/frontend/dist`、`desktop/frontend/node_modules` 被 `.gitignore` 忽略（`git add` 提示 ignored，未加入）
- 提交后 `git show --name-only HEAD | Select-String '^reports/'` → **无输出**

### 1.2 提交前预检：F-1 阻塞项确认已修复

Lead 报告的 F-1（`desktop/internal/services/settings.go:84` gofmt 对齐）已复核：

```
gofmt -l <engine/ 与 desktop/ 下全部 .go，排除 node_modules 共 338 个文件>
→ 零输出（gofmt clean）
```

（对照 `reports/vnic/90-verification.md` §0.2 门禁 #6 的 CI 步骤，332→338 的差异是本轮新增的 6 个未被 git 跟踪过的 `.go` 文件 — 预检覆盖了比 CI 的 `git ls-files` 更宽的范围。）

---

## 2. 推送（push）

```
git push origin main
→ e66016e..37571e5  main -> main   (exit 0)
```

- **未**设置 `GCM_INTERACTIVE=Never` / `credential.interactive=false`（保留 GCM 交互能力，实测凭据管理器直接放行，无凭据提示）
- **未**使用 `--force`，纯快进推送
- 推送后远端校验：`git ls-remote --heads origin refs/heads/main` → `37571e5a0531c16621cf4ac3e7511ac21135aa00	refs/heads/main` ✅ 与本地 HEAD 一致
- `gh auth status`：已登录 `Ye-Hub2`（keyring），token scopes 含 `repo`、`workflow`、`gist`、`read:org` ✅

---

## 3. CI 触发

### 3.1 推送本身**没有**产生 run（需要手动 dispatch）

推送后监控 3 分钟，`gh run list` 与 `gh api repos/Ye-Hub2/HypoMux/actions/runs` 均返回 **`total_count: 0`**（该仓库此前从未有 workflow run 记录）。

取证：

- `gh api repos/Ye-Hub2/HypoMux/actions/workflows/373800289` → `{"name":"Build Desktop","path":".github/workflows/build.yml","state":"active","created_at":"2026-10-03T17:11:43+08:00","updated_at":"2026-10-03T17:11:43+08:00"}`
  即 workflow 的注册时间戳 **正好等于本次推送时刻（提交 17:11:30 + 推送数秒）**。
- `git cat-file -e e66016e:.github/workflows/build.yml` → 存在（exit 0），且该文件有历史提交（`b7a18f2` 等）。
- `.github/workflows/build.yml:3-15` 的 `on` 确实包含 `push: branches: [main]`，且 `workflow_dispatch` 有必填 choice 输入 `signing_mode`。

**判定（非代码问题，属 GitHub 侧事件注册竞态）**：本次推送是该仓库 Actions 工作流的首次注册事件，push 事件在 workflow 被索引之前已求值，因此没有为该 push 生成 run。这是 `workflow_dispatch` 的存在意义，不构成构建失败。

### 3.2 手动触发（按预案执行）

```
gh workflow run build.yml -f signing_mode=none
```

| 项 | 值 |
|---|---|
| run id | **37112297270** |
| URL | https://github.com/Ye-Hub2/HypoMux/actions/runs/37112297270 |
| 事件 | `workflow_dispatch`（`signing_mode=none`，与契约 §7 允许的触发方式一致） |
| workflow | `Build Desktop` |
| **headSha** | `37571e5a0531c16621cf4ac3e7511ac21135aa00` ✅ 与本次提交一致（确认 CI 构建的就是本次代码） |
| 分支 | `main` |
| startedAt | `2026-10-03T09:13:05Z`（UTC）/ `2026-10-03T17:13:05+08:00` |
| job | 1 个：`Validate and package Wails desktop`（`runs-on: windows-2025`） |

---

## 4. CI 最终结论

> **✅ 全绿。`completed` / `success`，无失败步骤、无失败日志可抓。**

| 项 | 值 |
|---|---|
| run id | `37112297270` |
| URL | https://github.com/Ye-Hub2/HypoMux/actions/runs/37112297270 |
| headSha | `37571e5a0531c16621cf4ac3e7511ac21135aa00` ✅ |
| **status** | `completed` |
| **conclusion** | **`success`** |
| 挂钟耗时 | 8m15s（`09:13:05Z` → `09:21:20Z`） |

### 4.1 每个 job 的结论

| job | 结论 |
|---|---|
| `Validate and package Wails desktop`（ID `111172417643`） | **success**（32 个步骤全绿） |
| `Publish release installer`（ID `111173735687`） | `skipped`（预期：仅 `signing_mode=publish`/tag 才跑发布通道；本次 `signing_mode=none`） |

### 4.2 逐步骤结论（含与本地验证门禁的对应关系）

| # | 步骤 | 结论 | 耗时 | 实际执行的命令（`.github/workflows/build.yml`） |
|---|---|---|---|---|
| 1 | Set up job | success | 2s | — |
| 2 | Checkout | success | 7s | — |
| 3 | Validate release options | success | 0s | dispatch 输入校验 |
| 4 | Set up Go | success | 21s | — |
| 5 | Set up Node.js | success | 5s | — |
| 6 | Prepare and validate release version | success | 8s | `go -C desktop run ./cmd/release-version -check`（:75） |
| 7 | Restore pnpm tool cache | success | 0s | — |
| 8 | Install pinned pnpm | success | 7s | — |
| 9 | Configure pnpm | success | 1s | 断言 pnpm == `10.34.5`（:94-102） |
| 10 | Restore frontend dependency cache | success | 1s | — |
| 11 | Install Wails and NSIS | success | 77s | Wails `v3.0.0-alpha2.119` + NSIS |
| 12 | Install frontend dependencies | success | 32s | `pnpm --dir desktop/frontend install --frozen-lockfile`（:137）——**lockfile 与 `package.json` 一致** |
| 13 | Generate Wails frontend bindings | success | 6s | `wails3 generate bindings -clean=true -ts -i`（:142）——**新 `VirtualAdapterService` 绑定生成成功** |
| 14 | Run frontend tests | success | 88s | `pnpm --dir desktop/frontend test`（:146，vitest） |
| 15 | Build frontend assets for Go validation | success | 11s | `pnpm --dir desktop/frontend build`（:150，含 `tsc` + `vite build`） |
| 16 | Validate Go modules | success | 101s | `go -C engine mod verify` → `go -C desktop mod verify` → `go -C engine test ./...` → `go -C desktop test ./...` → `go -C engine vet ./...` → `go -C desktop vet ./...`（:152-166）——**CI 上真实跑了 Go 测试** |
| 17 | Verify Go formatting | success | 0s | `gofmt -l` + `$unformatted.Count -gt 0 → throw`（:168-186）——**此前唯一的阻塞项 F-1 已在 CI 上放行（0s 即时通过，零输出）** |
| 18 | Build and package Wails desktop | success | 83s | `wails3 task windows:package`（:191）——**Wails3 编译 + NSIS 打包成功** |
| 19-29 | SignPath 签名链（Stage/Upload/Sign ×3、Repackage、Use signed installer、Trust policy） | `skipped` | — | 预期：`signing_mode=none`，全部 `if` 条件为假 |
| 30 | Upload build artifacts | success | 3s | 产物：`HypoMux-Windows-37571e5a...-1`，**52,761,696 字节** |
| 56-60 | Post Restore 缓存 ×2 / Post Set up Node.js / Go / Checkout | success | — | 收尾 |
| 61 | Complete job | success | — | — |

### 4.3 失败原文

**无。** 没有失败步骤，因此没有 `gh run view --log-failed` 内容可抓（该命令在无失败步骤时返回空）。本次不存在需要交回 Lead 的 `文件:行号` 级编译/测试错误。

（备注：监控期间有一次 `gh run watch` 因 GitHub API 瞬时 `HTTP 502 ... /actions/runs/37112297270` 退出码 1 —— 这是**网络/API 抖动，不是 CI 失败**；改用带重试的轮询循环后同一 run 正常收敛为 `success`。）

---

## 5. 距离 CI 全绿还差什么

> **不差任何东西。距离 CI 全绿 = 0 项。**

- `37571e5` 这个 commit 上，`.github/workflows/build.yml` 的**全部真实门禁（步骤 12-18）均为 success**，签名相关步骤按 `signing_mode=none` 正确 skip。
- 本地验证（`reports/vnic/90-verification.md` §0.2 的门禁 1-10）与 CI 结果**完全一致**，且 CI 额外覆盖了本地无法执行的两项：`wails3 task windows:package`（NSIS 打包）与产物上传。
- 结论：契约 §7 的提交与 CI 目标已达成，**无需再派修复单**。
- 可选后续（非 CI 阻塞，均来自 90-verification.md 的提示级项，可另行决策）：F-2（`vnic.create` 失败返回 `tun_failed` 而非契约列举码）、F-3（fixture capabilities 缺 3 项的历史漂移）、F-5（`.gitignore` 中皮肤功能死规则）、F-6（陈旧 AI 措辞注释）、F-7（`pixi.js`/`pixi-live2d-display`/`fflate` 仍声明未引用 —— 契约要求保留，`pnpm --frozen-lockfile` 已通过，不影响 CI）。

---

## 6. 工作树与交付物位置提醒（给 Lead）

- 提交后 `git status --porcelain` 仅剩 **`?? reports/`**（未跟踪）—— 符合契约 §0.4「禁止把 `reports/` 加入 git」。
- 提交中 `reports/` 出现次数 = **0**（`git show --name-only HEAD | Select-String '^reports/'` 无输出）。
- `desktop/frontend/bindings`、`desktop/frontend/dist`、`desktop/frontend/node_modules` 仍为 ignored 状态，未误入提交。
- 本轮所有报告交付物位于 `reports/vnic/`（未跟踪，不会随提交到 GitHub）；本报告：`reports/vnic/95-ci-run.md`。
- 本地 `HEAD == origin/main == 37571e5a0531c16621cf4ac3e7511ac21135aa00`，无 force、无历史改写。

