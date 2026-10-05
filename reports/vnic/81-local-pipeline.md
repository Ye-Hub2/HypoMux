# 81 本地复刻 CI 全流程报告（T8）

> 执行者：`local-pipeline-verifier`（共享任务 `task-14`）
> 目的：在**没有 GitHub Actions 环境**的本地机器上，按 `.github/workflows/*.yml` 的**真实次序与真实命令**逐步复刻，把「只有 CI 才能发现」的问题（尤其 gofmt / 前端类型检查 / Go 门禁 / Wails 打包）提前暴露。
> 被测提交：`37571e5a0531c16621cf4ac3e7511ac21135aa00`（`feat: replace the AI assistant with a virtual adapter action on home`）。所有命令均在**该提交的工作树内容**上执行；提交落盘后用同一命令复核过 gofmt。

---

## 0. 结论摘要（TL;DR）

| 结论 | 内容 |
| --- | --- |
| 本地可执行的门禁 | **全部通过**（gofmt、engine/desktop 的 mod verify+test+vet、vitest、tsc+vite build、bindings 生成、engine/desktop 生产构建、govulncheck、**NSIS 打包**）。CI 要求的 **3 个必需产物已全部在本地产出**：`hypomux.exe`、`hypomux-engine.exe`、`hypomux-amd64-installer.exe` |
| 本地已复现的 CI 失败 | **1 处，且已在被测提交中修复**：`37571e5` 之前的工作树中 `desktop/internal/services/settings.go`（结构体字段对齐，约 :84）未通过 `gofmt -l`，会让 `build.yml:184` 直接 `exit 1`；该文件在 `37571e5` 中已随提交修正，当前 HEAD 上 gofmt 干净（338 个受管 `.go` 文件，`gofmt -l` 输出为空）。 |
| 当前 HEAD 上预计会挂 CI 的点 | **无本地可复现的必挂点**。未被本地覆盖的只剩：`choco install nsis` 这一**安装方式**本身（打包编译已用等价的 NSIS 3.11 工具链验证通过）、SignPath 签名（:193-302）、`upload-artifact`（:303-311）、以及需要 secrets 的 `create-release-tag.yml` / `release-smoke.yml`（见 §6） |
| 最大不确定项 | `wails3 task windows:package`（`build.yml:188-191`）内部的**裸 `pnpm install`**（`common:install:frontend:deps:pnpm`）在本地无 TTY 时报 `ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY`；**已按 CI 条件（`CI=true`）补跑验证：exit 0，75s 完成，412 包重建，`pnpm-lock.yaml` 哈希不变**，故 CI 中该步骤预期通过（见 §5.3） |
| 与 CI 的替代差异 | NSIS 未用 `choco install nsis`，改用**官方 NSIS 3.11 便携版**（`makensis` v3.11）执行同一条 `makensis` 命令；编译结果 exit 0（见 §2.1 与 §8） |

---

## 1. 环境与准备

| 项 | CI（GitHub Actions） | 本机实测 | 差异评估 |
| --- | --- | --- | --- |
| Runner OS | `windows-2025` | Windows（本机） | 可接受 |
| Node | 22（`actions/setup-node@v4`） | `v24.21.0`（npm `11.19.0`） | **版本不同**，frontend 门禁结论以本机为准（vite 8 / vitest 4 均支持 24） |
| pnpm | 10.34.5（`PNPM_VERSION`） | 10.34.5（`npm install --global --prefix "$env:TEMP\hypomux-pnpm" pnpm@10.34.5`） | **完全一致** |
| Go | `actions/setup-go` + `go-version-file: engine/go.mod`（toolchain 自动） | 便携版 `go1.22.5`，`GOTOOLCHAIN=auto` 自动拉取 `go1.26.6`（engine）/ `go1.25.0`（desktop） | 与 CI 同为「按 go.mod 自动取 toolchain」 |
| Wails CLI | `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.119` | 同命令，`wails3 version` → `v3.0.0-alpha2.119` | **完全一致** |
| NSIS | `choco install nsis --yes --no-progress` | **未用 choco**（本机无 choco/管理员权限）；改用**官方 NSIS 3.11 便携发行版**（`nsis-3.11.zip`，`makensis` 报 `v3.11`），并成功编译 `project.nsi` | 安装方式不同、**编译行为等价**（见 §8） |
| 模块代理 | 默认 proxy.golang.org | `GOPROXY=https://goproxy.cn,direct` | 可接受（仅换源，版本解析仍以 go.mod/go.sum 为准） |
| npm registry | 默认 registry.npmjs.org | `desktop/frontend/.npmrc` 仅含 `minimum-release-age=10080`（无镜像配置）→ 实际走 registry.npmjs.org | **完全一致** |

工作树约束遵守情况：
- 未修改任何源码/配置/工作流；未执行 `git add`/`commit`/`push`。
- 唯一由工具改写过的受管文件是 `desktop/go.mod`（`go mod tidy` 造成的行尾差异），**已 `git checkout -- desktop/go.mod` 还原**，详见 §5.4。
- 允许产生的构建产物保留：`desktop/frontend/node_modules/`、`desktop/frontend/dist/`、`desktop/frontend/bindings/`、`desktop/bin/`。
- 说明：`desktop/frontend/bindings/` 与 `desktop/bin/` **都被 gitignore**（`desktop/.gitignore:9 /frontend/bindings/`、`desktop/.gitignore:3 /bin/`），因此构建产物**不会**污染 `git status`（实测 `git status --porcelain` 中无 bindings/dist/bin 条目）。

---

## 2. `build.yml`（主构建工作流）逐步复刻总表

`build.yml` 的 build job 共 3 个工作流分支，此处覆盖其**主路径**（`signing_mode = none` 的默认分支）。

| # | CI 步骤（build.yml 行号） | 本地执行的命令 | 结果 | 是否等同 CI |
| --- | --- | --- | --- | --- |
| 1 | Checkout | 工作树即为 `37571e5` | ✔ | 等同 |
| 2 | Validate release options（:59 附近） | — | 未执行 | 仅 `workflow_dispatch` 输入校验，PR/push 不跑 |
| 3 | setup-go / setup-node 22 | 见 §1 | ✔ | 部分等同（node 24 vs 22） |
| 4 | **Prepare and validate release version**（:69-77） | `go -C desktop run ./cmd/release-version -check` | **exit 0**，输出 `version=2.7.0 / windows_version=2.7.0.65535 / prerelease=false / make_latest=true / channel=update-channel` | 等同 |
| 5 | Restore pnpm tool cache | — | 未执行 | 缓存步骤，无失败风险 |
| 6 | **Install pinned pnpm**（:86-92） | `npm install --global --prefix "$env:TEMP\hypomux-pnpm" "pnpm@10.34.5" --no-audit --no-fund` | **exit 0**；`pnpm --version` → `10.34.5` | 等同 |
| 7 | Configure pnpm（校验版本 == 10.34.5） | `pnpm --version` | 通过 | 等同 |
| 8 | Restore frontend dependency cache | — | 未执行 | 缓存步骤 |
| 9 | **Install Wails and NSIS**（:117-133） | ① `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.119`；② 官方 NSIS 3.11 便携版（代替 `choco install nsis`） | ① **首次失败、重试成功**（见 §5.1）；② `makensis` **v3.11 可用**（SourceForge 与 chocolatey 源均被 403/404 挡住，改用官方 NSIS 3.11 发行包，见 §8）；CI 对 `${env:ProgramFiles(x86)}\NSIS\makensis.exe` 的路径校验在本机为 `False`（因未用 choco 安装，符合预期） | 部分等同（安装方式不同，编译能力等价） |
| 10 | **Install frontend dependencies**（:135-137） | `pnpm --dir desktop/frontend install --frozen-lockfile` | **exit 0**，412 个包全部安装（耗时 **26m05.9s**）；无锁文件不同步报错 | 等同 |
| 11 | **Generate Wails frontend bindings**（:139-142，cwd=`desktop`） | `wails3 generate bindings -clean=true -ts -i` | **exit 0**；`Processed: 300 Packages, 13 Services, 106 Methods, 0 Enums, 47 Models, 0 Events in 20.4s`；输出到 `desktop/frontend/bindings` | 等同 |
| 12 | **Run frontend tests**（:144-146） | `pnpm --dir desktop/frontend test`（= `vitest run`） | **exit 0**：**43 个测试文件 / 243 个测试全部通过**（40.66s） | 等同 |
| 13 | **Build frontend assets for Go validation**（:148-150） | `pnpm --dir desktop/frontend build`（= `tsc && vite build --mode production`） | **exit 0**；`tsc` 未因 `noUnusedLocals: true` 报错；vite `✓ 2494 modules transformed`、`✓ built in 6.46s`；产出 `desktop/frontend/dist/{index.html,assets,support}` | 等同 |
| 14 | **Validate Go modules**（:152-166，6 条命令） | `go -C engine mod verify` / `go -C desktop mod verify` / `go -C engine test -count=1 ./...` / `go -C desktop test -count=1 ./...` / `go -C engine vet ./...` / `go -C desktop vet ./...` | **全部 exit 0**（明细见 §3.1 / §3.2） | 等同（本地加 `-count=1` 关缓存，比 CI 更严格） |
| 15 | **Verify Go formatting**（:168-186） | 原样复刻：`$goFiles = @(git ls-files -- '*.go' \| Where-Object { $_ -like 'engine/*' -or $_ -like 'desktop/*' }); gofmt -l @goFiles` | **exit 0，未格式化 0 个**（338 个文件，全部存在）。⚠️ 提交前的工作树为 1 个失败（见 §5.2） | 等同 |
| 16 | **Build and package Wails desktop**（:188-191，cwd=`desktop`） | `wails3 task windows:package` → 本地按 Taskfile 定义**分步等价执行**（含最终 `makensis project.nsi`） | **通过**：`windows:build` 全部子环节 exit 0，`wails3 generate webview2bootstrapper` exit 0，`makensis` **exit 0（29s）**，产出 `desktop/bin/hypomux-amd64-installer.exe`（43,240,066 B，FileDescription=`HypoMux Installer`，FileVersion=`2.7.0`）。⚠️ 整条 `wails3 task windows:package` 命令本身未跑通（§5.3 的无 TTY 中止），故改分步等价执行 | 等同（分步等价） |
| 17 | SignPath 签名/重打包（:193-302） | — | 未执行 | 需 `workflow_dispatch` + SignPath 组织/证书密钥 |
| 18 | **Upload build artifacts**（:303-311，`if-no-files-found: error`） | 手工核对 3 个必需产物 | **3/3 全部存在**：`desktop/bin/hypomux.exe` ✔（14,999,552 B，`HypoMux 2.7.0`）、`desktop/bin/hypomux-engine.exe` ✔（8,482,304 B）、`desktop/bin/hypomux-amd64-installer.exe` ✔（43,240,066 B，`HypoMux Installer 2.7.0`） | 产物等价（仅未走 `actions/upload-artifact` 上传动作） |

### 2.1 步骤 16 的分步等价执行明细

`wails3 task windows:package` → `create:nsis:installer`（deps: `build`）→ `windows:build` → `build:native`，
其 deps/cmds 与本地执行结果：

| 子步骤（Taskfile 位置） | Taskfile 中的命令 | 本地结果 |
| --- | --- | --- |
| `windows:build` 首条 | `go -C desktop run ./cmd/release-version -check` | exit 0（同 #4） |
| `common:go:mod:tidy` | `go mod tidy`（desktop） | 执行成功；仅造成 `desktop/go.mod` **行尾**差异，已还原（§5.4） |
| `build:core-runtime` preconditions | 要求 `bin/sing-box.exe`、`bin/wintun.dll`、`bin/libcronet.dll` 存在 | **三步前置校验均通过**：三个资产均存在且**已被 git 跟踪**（`bin/sing-box.exe` 81,947,136 B、`bin/wintun.dll` 427,552 B、`bin/libcronet.dll` 9,528,832 B） |
| `build:core-runtime` cmds | `go build -trimpath -buildvcs=false -ldflags="-w -s -X main.version=2.7.0 -X main.commit=<HEAD>" -o desktop/bin/hypomux-engine.exe ./cmd/hypomux-engine` + 拷贝 3 个运行时资产 | **exit 0**；产出 `desktop/bin/hypomux-engine.exe`（8,482,304 B）；3 条 `Copy-Item` 全部成功（sing-box.exe / wintun.dll / libcronet.dll 已就位于 `desktop/bin/`） |
| `common:build:frontend` | `pnpm run build`（frontend） | 等同 #13，exit 0 |
| `generate:syso` | `wails3 generate syso -arch amd64 -icon windows/icon.ico -manifest windows/wails.exe.manifest -info windows/info.json -out ../wails_windows_amd64.syso`（cwd=`desktop/build`） | **exit 0**，`desktop/wails_windows_amd64.syso` 生成成功 |
| `build:native` 构建 | `go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui" -o bin/hypomux.exe` | **exit 0**，产出 `desktop/bin/hypomux.exe`（14,999,552 B） |
| `create:nsis:installer` 之一 | `wails3 generate webview2bootstrapper -dir desktop/build/windows/nsis` | **exit 0**（离线即可）：`Generated WebView2 bootstrapper at ...\MicrosoftEdgeWebview2Setup.exe`（1,793,816 B，工具内置，无需联网；该文件被 `desktop/.gitignore:14` 忽略，不会污染工作树） |
| `create:nsis:installer` 之二 | `makensis -DARG_WAILS_AMD64_BINARY="...\desktop\bin\hypomux.exe" project.nsi`（cwd=`desktop/build/windows/nsis`） | **exit 0，耗时 29s** → `Output: "...\desktop\bin\hypomux-amd64-installer.exe"`；`Processed 1 file, writing output (x86-unicode)`；`Total size: 43240066 / 217802428 bytes (19.8%)`（217,802,428 = 未压缩数据，含 81.9 MB 的 sing-box.exe；`Datablock optimizer saved 34667 KiB (~45.0%)`） |

> 注：`wails3 task windows:package`（以及其内的 `windows:build`）作为**整条命令**在本地**未能跑完**——中止于 `common:install:frontend:deps:pnpm` 的裸 `pnpm install`（无 TTY，§5.3）。上表是按其 Taskfile 定义的**等价命令分步执行**的结果，命令文本与 `desktop/build/windows/Taskfile.yml` 一致；其中 `makensis` 使用官方 NSIS 3.11 便携版（§8）。

---

## 3. 其他 3 个工作流的覆盖情况

### 3.1 `go-engine.yml`（engine 专项，`windows-2025`，working-directory: engine）

| CI 步骤（行号） | 本地命令 | 结果 | 等同 CI |
| --- | --- | --- | --- |
| Verify formatting（:40-46） | `gofmt -l .`（cwd=`engine`） | **exit 0，0 个未格式化** | 等同 |
| Test and vet（:49-57） | `go mod verify` → `go test ./...` → `go vet ./...` | 全部 exit 0（`all modules verified`；测试明细见 §3.3） | 等同（本地 `-count=1`） |
| Scan known vulnerabilities（:59-62） | `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` | **exit 0：`No vulnerabilities found.`**（过程：`golang.org/x/vuln@v1.6.0 requires go >= 1.25.0; switching to go1.26.8`，自动切 toolchain） | 等同 |
| Verify release build（:64-67） | `go build -trimpath -o "$env:TEMP\hypomux-engine.exe" ./cmd/hypomux-engine` | **exit 0** | 等同 |

### 3.2 `create-release-tag.yml` / `release-smoke.yml`（均为 `ubuntu-latest`）

| CI 步骤 | 本地命令 | 结果 | 等同 CI |
| --- | --- | --- | --- |
| `create-release-tag.yml:32-36`「Validate release version and notes」 | `go -C desktop run ./cmd/release-version -tag v2.7.0 -notes` | **exit 0**，输出与 `-check` 一致的版本四元组 | 等同 |
| `create-release-tag.yml:38+`「Validate and synchronize release tag」 | — | 未执行 | 需 `CNB_TOKEN` 秘密、CNB 远端与真实 tag 状态 |
| `release-smoke.yml:30-35`「Resolve release channel」 | 同上（`release-version -tag <tag>`） | exit 0（用 `v2.7.0` 代替输入 tag） | 部分等同 |
| `release-smoke.yml:37-50`「manifest 签名密钥与内嵌公钥一致性」 | — | 未执行 | 需 `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 秘密 |
| `release-smoke.yml:52+`「GitHub tag 与 CNB tag 同 commit」/「CNB token 可读 Release」 | — | 未执行 | 需 CNB 凭据与已发布 tag |

### 3.3 门禁测试明细（全部 exit 0）

**engine（`go -C engine test -count=1 ./...`）**
```
ok  .../cmd/hypomux-engine 1.112s      ok  .../internal/dns 2.803s
ok  .../internal/api/v1 0.566s         ok  .../internal/expiry 0.375s
ok  .../internal/diagnostic 0.417s     ok  .../internal/fileintegrity 0.410s
ok  .../internal/platform 0.832s       ?   .../internal/protocol [no test files]
ok  .../internal/proxy 10.508s         ok  .../internal/runtime 0.407s
ok  .../internal/server 0.984s         ok  .../internal/tun 5.135s
ok  .../internal/vnic 0.644s           ok  .../internal/wfp 0.391s
```
另有 `go -C engine mod verify` → `all modules verified`；`go -C engine vet ./...` → 无输出。

**desktop（`go -C desktop test -count=1 ./...`）**
```
ok  .../desktop 0.475s                 ?   .../desktop/build/windows/syso [no test files]
?   .../desktop/cmd/release-version [no test files]
ok  .../desktop/cmd/update-manifest-sign 0.369s
ok  .../desktop/internal/engineclient 3.687s
ok  .../desktop/internal/platform 0.337s
ok  .../desktop/internal/platform/wails 0.666s
ok  .../desktop/internal/releaseversion 0.452s
ok  .../desktop/internal/services 51.690s
ok  .../desktop/internal/startup 0.289s
```
另有 `go -C desktop mod verify` → `all modules verified`；`go -C desktop vet ./...` → exit 0；`go -C desktop build ./...` → exit 0。**无因缺少管理员权限而被跳过的失败。**

---

## 4. 新增代码的契约核验（bindings ↔ Go ↔ 前端）

| 检查点 | 结果 |
| --- | --- |
| 生成物存在 | `desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/virtualadapterservice.ts` ✔ |
| 导出签名 | `Create(interfaceName: string, address: string)`、`Remove()`、`Shutdown()`、`Status()`（均走 `$Call.ByID(...)`）✔ |
| 类型一致性 | bindings `services/models.ts:506` 的 `VirtualAdapterStatus` 字段 `state/interfaceName/address/prefixLength/mtu/adapterGuid/createdAt/lastError`，与 `desktop/internal/services/virtual_adapter.go:32-41` 的 json tag **完全一致** ✔ |
| 消费侧接线 | `desktop/frontend/src/platform/services.ts:7` 导入 `virtualadapterservice`、`:24` 导入 `VirtualAdapterStatus`、`:73` 别名 re-export、`:350-354` 暴露 `virtualAdapter.{create,status,remove}` ✔ |
| 相关前端测试 | `VirtualAdapterPanel.test.tsx`（9 例）+ `HomePage.test.tsx`（17 例）单跑 **2 files / 26 tests passed**；全量 43 files / 243 tests passed ✔ |
| 删除 AI 模块后残留引用 | `src` 内已无 `components/ai`、`AIAssistant`、`AssistantCompanion`、`SkinWardrobe`、`live2d`、`pageContext` 引用 → `tsc`（`noUnusedLocals: true`）通过 ✔ |

---

## 5. 本地复刻过程中发现的问题

### 5.1 `go install wails3` 首次失败（网络抖动，非仓库缺陷）
- 命令：`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.119`
- 首次 **exit 1**，原文：
  ```
  verifying module: ... Get "https://goproxy.cn/sumdb/sum.golang.org/lookup/...": read tcp ...: wsarecv: An existing connection was forcibly closed by the remote host.
  read "https://goproxy.cn/.../@v/*.zip": ... (同类错误)
  ```
- 重试 1 次即成功，`wails3 version` → `v3.0.0-alpha2.119`。
- 判定：本机到 goproxy.cn 的连接抖动；CI 使用默认 `proxy.golang.org`，**不构成本仓库的 CI 失败点**。

### 5.2 gofmt：唯一真实门禁失败，且已在被测提交中修复
- **提交前工作树**（当时尚未 commit）复刻 `build.yml:168-186` 得到 **1 个未格式化文件**：
  - 文件：`desktop/internal/services/settings.go`
  - `gofmt -d` 原文：
    ```
    @@ -81,7 +81,7 @@ type SettingsService struct {
    -	mu sync.RWMutex
    +	mu               sync.RWMutex
    ```
  - 影响：`build.yml:183-185` 会 `Write-Error "Not gofmt-formatted: ..."` 并 **`exit 1`**，即该步骤**必然挂掉**（PR/push 都会跑）。
  - 该工作树下的另外 6 个新增 `.go` 文件（`desktop/internal/engineclient/vnic.go`、`desktop/internal/services/virtual_adapter.go`、`desktop/internal/services/vnic_config.go`、`engine/internal/tun/interface_name.go`、`engine/internal/vnic/manager.go`、`engine/internal/vnic/manager_test.go`）gofmt 全部干净。
- **提交 `37571e5` 之后复核（决定性结论）**：`desktop/internal/services/settings.go:84` 现为 `mu               sync.RWMutex`（已对齐），`git show --stat HEAD` 显示该文件随提交改动 34 行。
  - `gofmt -l`（`build.yml` 形式，338 个受管文件）→ **未格式化 0 个，exit 0**
  - `gofmt -l .`（`go-engine.yml` 形式，cwd=`engine`）→ **未格式化 0 个，exit 0**
  - `gofmt -l .`（cwd=`desktop`）→ **未格式化 0 个，exit 0**
- 判定：**gofmt 门禁当前已通过**；该问题是「提交前未格式化」，已由提交修正，必须在 CI 前保持不回归。

### 5.3 `wails3 task windows:build` 在裸 `pnpm install` 处中止（本地无 TTY 造成的假失败）
- 命令：`wails3 task windows:build`（cwd=`desktop`），实测 **exit 1**。
- 中止点：`task: [windows:common:install:frontend:deps:pnpm] "pnpm" install`，原文：
  ```
   ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY  Aborted removal of modules directory due to no TTY
  If you are running pnpm in CI, set the CI environment variable to "true", or set "confirmModulesPurge" to "false".
    ERROR   task: Failed to run task "windows:build": exit status 1
  ```
- 关键背景：该子任务执行的是**不带 `--frozen-lockfile` 的 `pnpm install`**（`desktop/build/Taskfile.yml` 的 `install:frontend:deps:pnpm`），而 `build.yml:135-137` 单独执行的是**带 `--frozen-lockfile` 的安装**（本地已成功，见 §2 #10）。
- **补充验证（决定性）**：按 CI 的真实条件补设 `$env:CI="true"` 后，在 `desktop/frontend` 原样复跑 `pnpm install`：
  - **exit 0，耗时 75s**（`Done in 1m 15.2s using pnpm v10.34.5`）
  - 首行日志：`Recreating C:\...\desktop\frontend\node_modules`，紧随 `Lockfile is up to date, resolution step is skipped`，随后 `Packages: +412`，412 个包全部 added
  - `pnpm-lock.yaml` SHA256 前后一致：`76D3E00DEDFD01576C9865CE22415EE9FB6E47F4C4830E4F541402678E6E18F8`（**未被改写**）
  - 执行后 `git status --porcelain` 仅 `?? reports/`（工作树干净）
- 判定：**该步骤在 CI 中预期通过**。GitHub Actions 默认设置 `CI=true`，pnpm 会自动确认删除并重建 `node_modules`，重建仅耗时 75s（缓存命中 store）。本地之所以报错，纯属「无 TTY + 未设置 `CI`」。
- 遗留观察点（非失败风险）：pnpm 在此处会**重建** `node_modules`（即便 `build.yml:135-137` 刚刚用同一 pnpm 安装过）。原因未深究（与 `.npmrc` 的 `minimum-release-age=10080` 或 modules 元数据有关），代价小（75s）。若希望连这点重复开销也消除，可让 `desktop/build/Taskfile.yml` 的 `install:frontend:deps:pnpm` 使用 `pnpm install --frozen-lockfile`。

### 5.4 `go mod tidy` 对 `desktop/go.mod` 的改写（已还原）
- 触发：`build:native` 的 dep `common:go:mod:tidy` 会执行 `go mod tidy`（desktop）。
- 现象：执行后 `git status` 出现 ` M desktop/go.mod`，但 `git diff -- desktop/go.mod` **内容为空**（仅行尾 CRLF↔LF 差异，git 提示 `LF will be replaced by CRLF the next time Git touches it`）。
- 处理：已执行 `git checkout -- desktop/go.mod` **还原**，还原后 `git status --porcelain` 只剩 `?? reports/`（我的报告目录）。
- 判定：**未被真实改写，无内容差异**；此项仅作为「工具会触碰 go.mod」的记录。

### 5.5 任务书中的两处事实更正
1. `desktop/frontend/bindings/` **是被忽略的**（`git check-ignore -v` → `desktop/.gitignore:9: /frontend/bindings/`），不会出现在 `git status`。
2. `desktop/bin/` 同样被忽略（`desktop/.gitignore:3: /bin/`，另有根 `.gitignore:59 /desktop/bin/`）。因此 bindings/bin/dist/node_modules 都不会污染工作树状态。

---

## 6. CI 预计会失败的点（清单）

> 被测提交 `37571e5`，按**本地可复现**与**无法本地复现**两类分开列。

### 6.1 本地无法复现的步骤（剩余，均无「必挂」证据）
1. **`choco install nsis` 这一安装方式本身**（`build.yml:117-133`）
   - 本地情况：本机无 `choco`、无管理员权限。SourceForge（`downloads.sourceforge.net` / 各 `*.dl.sourceforge.net` 镜像）返回 **HTTP 403**（取回的是 127,844 字节 HTML 而非 zip）；`community.chocolatey.org/api/v2/package/nsis` 只返回 3,222 字节错误页。
   - 影响：**仅影响「NSIS 安装」这一步**。其下游的**打包编译已用官方 NSIS 3.11 便携版等价验证通过**（§2.1），因此这条路径不再属于高风险项。
   - CI 侧唯一未验证假设：windows-2025 runner 的 `choco install nsis` 能成功把 `makensis.exe` 放到 `${env:ProgramFiles(x86)}\NSIS\`（`build.yml:133` 的存在性校验）。这是标准 runner 上的标准包，风险低。
2. **`wails3 task windows:package` 作为「单条命令」的执行形态**
   - 本地该整条命令在 `common:install:frontend:deps:pnpm` 处因无 TTY 中止（§5.3）；已按 Taskfile 定义分步等价执行并**全部通过**，且用 `CI=true` 补跑证实裸 `pnpm install` 本身成功（75s）。
   - 结论：CI 侧预期通过；建议首次 CI 运行时确认该行日志为 `Recreating ... node_modules` 而非报错。
3. **SignPath 签名与重打包**（`build.yml:193-302`，条件 `workflow_dispatch && signing_mode != 'none'`）
   - 本地原因：需 SignPath 组织/证书密钥与 dispatch 输入。仅在手动触发且选择签名模式时执行；`signing_mode = none` 的 push/PR 路径不受影响。
4. **`upload-artifact`**（`build.yml:303-311`）
   - 本地原因：需 `actions/upload-artifact@v4` 运行环境。
   - 已本地生成的 3 个必需产物：`desktop/bin/hypomux.exe` ✔、`desktop/bin/hypomux-engine.exe` ✔、`desktop/bin/hypomux-amd64-installer.exe` ✔（`if-no-files-found: error` 在 CI 中不应触发）。
5. **`create-release-tag.yml` / `release-smoke.yml`**：运行于 `ubuntu-latest`，需 `CNB_TOKEN` / `UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 等秘密与真实远端 tag 状态，本地不可复刻（其中的 `release-version` 校验部分已通过，见 §3.2）。

### 6.2 本地可复现且**全部通过**的门禁（即当前 HEAD 上不应再挂）
- `build.yml`：release-version `-check` → pnpm 安装/版本校验 → `--frozen-lockfile` 安装 → bindings 生成 → vitest（243 用例）→ `tsc && vite build` → engine/desktop 的 `mod verify`/`test`/`vet` → **gofmt（338 文件，0 未格式化）** → core-runtime 构建 → `generate:syso` → 生产 `go build`（`-tags production -trimpath -ldflags="-w -s -H windowsgui"`）→ WebView2 bootstrapper → **`makensis project.nsi`（exit 0，产出 installer）**。
- `go-engine.yml`：`gofmt -l .`（0）→ `go mod verify` → `go test ./...` → `go vet ./...` → **govulncheck（No vulnerabilities found）** → `go build -trimpath ./cmd/hypomux-engine`。
- `create-release-tag.yml` / `release-smoke.yml` 的 `release-version` 校验步骤。

---

## 7. 建议的后续动作（供 Lead 决策）

1. **首次 CI 运行**时重点盯两段日志：`Install Wails and NSIS`（:117-133，本地只能用便携版 NSIS 替代）与 `Build and package Wails desktop`（:188-191，本地用分步等价命令 + `CI=true` 补跑通过）。
2. 若希望本地能**一条命令**跑通打包（`wails3 task windows:package`），可在开发机执行一次 `choco install nsis`（需管理员），并把 `CI` 环境变量设为 `true`（或在 `desktop/frontend/.npmrc` 加 `confirmModulesPurge=false`）以消除 §5.3 的无 TTY 中止。
3. 保持 `gofmt` 门禁不回归：提交前跑 `gofmt -l $(git ls-files -- '*.go' | ...)`（本报告 §2 #15 的命令）。
4. 可考虑为 `desktop/build/Taskfile.yml` 的 `install:frontend:deps:pnpm` 增加 `--frozen-lockfile`（与 `build.yml:135-137` 一致），以消除 §5.3 中「重复安装 / 需删除 modules 目录」的不确定性。

---

## 8. NSIS 打包的复刻方式与产物验证（重要说明）

CI 用 `choco install nsis` 安装 NSIS；本机无 choco/无管理员权限，且 SourceForge 与 chocolatey 源均不可达（403/404/错误页）。为**不放弃最后一段打包链路**，改用**官方 NSIS 3.11 便携发行版**（`nsis-3.11.zip`，`makensis` 自报 `v3.11`）执行与 Taskfile **完全相同的命令**：

```powershell
# CI: "{{.MAKENSIS}}" -DARG_WAILS_AMD64_BINARY="<ROOT>\desktop\bin\hypomux.exe" project.nsi
#     cwd = desktop/build/windows/nsis
& "$env:TEMP\nsis311\nsis-3.11\makensis.exe" '-DARG_WAILS_AMD64_BINARY=<repo>\desktop\bin\hypomux.exe' project.nsi
```

结果（`makensis` 关键输出）：

```
Processing script file: "project.nsi" (ACP)
Processed 1 file, writing output (x86-unicode):
Output: "<repo>\desktop\bin\hypomux-amd64-installer.exe"
Install: 4 pages (256 bytes), 1 section (1 required) (2072 bytes), 1130 instructions (31640 bytes), 431 strings (21014 bytes), 2 language tables (780 bytes).
Datablock optimizer saved 34667 KiB (~45.0%).
Total size:  43240066 / 217802428 bytes (19.8%)
```

产物核验：

| 产物 | 大小 | 版本信息 | CI 是否必需 |
| --- | --- | --- | --- |
| `desktop/bin/hypomux.exe` | 14,999,552 B | `HypoMux` / FileVersion `2.7.0` | 必需（upload-artifact） |
| `desktop/bin/hypomux-engine.exe` | 8,482,304 B | — | 必需 |
| `desktop/bin/hypomux-amd64-installer.exe` | 43,240,066 B | `HypoMux Installer` / FileVersion `2.7.0` | 必需 |
| `desktop/bin/{sing-box.exe, wintun.dll, libcronet.dll}` | 81,947,136 / 427,552 / 9,528,832 B | — | `build:core-runtime` 会拷贝进 `bin/` |

差异声明（诚实记录）：
- **NSIS 来源与版本**：CI 为 `choco install nsis`（写入 `${env:ProgramFiles(x86)}\NSIS`），本地为官方 NSIS 3.11 便携版；`makensis` 主版本一致（3.11）。`build.yml:133` 对 choco 安装路径的 `Test-Path` 校验在本机为 `False`（未用 choco 安装，属预期）。
- `desktop/build/windows/nsis/MicrosoftEdgeWebview2Setup.exe`（1,793,816 B）由 `wails3 generate webview2bootstrapper` 生成于仓库内，但被 `desktop/.gitignore:14` 忽略，**未污染 `git status`**；保留不影响仓库。
- 最终工作树状态：`git status --porcelain` 仅 `?? reports/`（本报告目录）；`gofmt` 复核 338 个文件、0 个未格式化、exit 0。

---

## 附：原始命令清单（可直接复制复现）

```powershell
# 0) 环境
$env:GOROOT="$env:TEMP\go-full\go"; $env:Path="$env:GOROOT\bin;$env:USERPROFILE\go\bin;$env:Path"
$env:GOPROXY="https://goproxy.cn,direct"

# 1) 固定版本 pnpm
npm install --global --prefix "$env:TEMP\hypomux-pnpm" "pnpm@10.34.5" --no-audit --no-fund
$env:Path="$env:TEMP\hypomux-pnpm;$env:Path"; pnpm --version

# 2) Wails CLI
go install "github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.119"

# 3) 版本校验
go -C desktop run ./cmd/release-version -check

# 4) 前端
pnpm --dir desktop/frontend install --frozen-lockfile
(Set-Location desktop; wails3 generate bindings -clean=true -ts -i)
pnpm --dir desktop/frontend test
pnpm --dir desktop/frontend build

# 5) Go 门禁
go -C engine mod verify;  go -C desktop mod verify
go -C engine test -count=1 ./...; go -C desktop test -count=1 ./...
go -C engine vet ./...;   go -C desktop vet ./...

# 6) gofmt（build.yml:168-186 原样式）
$goFiles = @(git ls-files -- '*.go' | Where-Object { $_ -like 'engine/*' -or $_ -like 'desktop/*' })
$unformatted = @(gofmt -l @goFiles)

# 7) （go-engine.yml 形式）
(Set-Location engine; gofmt -l .; go mod verify; go test ./...; go vet ./...)
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...   # cwd=engine
go build -trimpath -o "$env:TEMP\hypomux-engine.exe" .\cmd\hypomux-engine

# 8) Wails 打包链（等价分步，windows:build）
(Set-Location desktop\build; wails3 generate syso -arch amd64 -icon windows/icon.ico -manifest windows/wails.exe.manifest -info windows/info.json -out ../wails_windows_amd64.syso)
(Set-Location desktop; $env:GOOS="windows"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
 go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui" -o bin/hypomux.exe
 Remove-Item *.syso)

# 9) core-runtime 运行时资产（CI: build:core-runtime）
Copy-Item -LiteralPath .\bin\sing-box.exe  -Destination .\desktop\bin\sing-box.exe  -Force
Copy-Item -LiteralPath .\bin\wintun.dll    -Destination .\desktop\bin\wintun.dll    -Force
Copy-Item -LiteralPath .\bin\libcronet.dll -Destination .\desktop\bin\libcronet.dll -Force

# 10) NSIS 打包（CI: choco install nsis + makensis project.nsi）
$mk = "<NSIS 目录>\makensis.exe"   # 本机用官方 NSIS 3.11 便携版
(Set-Location desktop; wails3 generate webview2bootstrapper -dir "$PWD\build\windows\nsis")
(Set-Location desktop\build\windows\nsis
 & $mk '-DARG_WAILS_AMD64_BINARY=<repo>\desktop\bin\hypomux.exe' project.nsi)
# 产出 desktop\bin\hypomux-amd64-installer.exe（43,240,066 B, HypoMux Installer 2.7.0）

# 11) 裸 pnpm install 的 CI 条件补跑（windows:package 内部步骤）
$env:CI="true"; (Set-Location desktop\frontend; pnpm install)   # exit 0，75s，lockfile 哈希不变
```
