# HypoMux 跨平台兼容性审计报告（Windows / Linux / macOS）

> 审计范围：`engine/`（Go 核心守护进程）、`desktop/`（Wails3 桌面应用）、`protocol/v1/`、`.github/workflows/`、`desktop/build/`、`desktop/portable/`、`bin/`。
> 审计方式：只读静态审查，所有结论均来自实际读取的代码与工作流文件，逐条给出 `文件路径:行号` 与原文引用。
> 本报告由三条并行审计线（打包分发 / CI 覆盖 / 路径与文件系统）汇总而成，Lead 复核后整合。

---

## 一、结论摘要

1. **仓库在 Linux/macOS 上不存在任何桌面实现，是"无产物"而非"功能受限"。** 全仓库 `//go:build windows` 文件 85 个、`//go:build !windows` 文件 39 个，两者合计 124 恰好等于对 `//go:build (windows|!windows|darwin|linux)` 的全部命中数 —— 这证明**不存在任何一个 `//go:build linux` 或 `//go:build darwin` 文件**。非 Windows 支持完全靠 39 个 `_other.go` 通配桩文件承载。

2. **CI 从未在非 Windows 上编译过任何应用代码。** `.github/workflows/` 全部 4 个工作流、5 个 job 中，`runs-on:` 共 5 处，无一例外是 `windows-2025`（2 个）或 `ubuntu-latest`（3 个）；`macos|macOS|osx|darwin` 在整个 `.github/` 下**零匹配**。三个 ubuntu job 只各跑一次 `go run ./cmd/release-version`，是全仓库唯一平台无关的可执行包，**应用代码编译/测试覆盖率为零**。因此那 39 个 `_other.go` **从未被编译器读取过一次** —— 连"能不能编译过"都没有验证。

3. **⚠️ 补充实测证据：非 Windows 不是"未验证"，而是"已知编译不通过"。** 仓库自己的验收记录留有实测原文 —— `reports/vnic/76-inventory-repro.md:240-241` 原文 `GOOS=linux go -C desktop build ./... 同样失败，原因是 wails 框架自身 pkg/application/menu_linux.go:7: undefined: pointer，stash 掉本次改动后同样失败`（即与本仓库代码无关，是 wails alpha 版本自身缺陷）。同一文件 `:236` 记录 `GOOS=linux GOARCH=amd64 go -C desktop build ./internal/services/` **退出码 0** —— 说明 HypoMux 自有代码能编过，卡点全在 wails 框架层。此外 `desktop/internal/services/engine_integration_test.go:70` 引用了只在 `//go:build windows` 文件中定义的 `proxyMarkerPath`，该测试文件**没有 build tag**，导致 `GOOS=linux` 下 `go -C desktop test ./...` 与 `go -C desktop vet ./...` **直接编译失败**（见第三章 [严重] 条目）。

4. **打包与分发层 100% Windows 独占。** `desktop/Taskfile.yml:15-17` 只 `includes` 了 `common` 与 `windows`；`desktop/build/` 下不存在 `darwin/`、`linux/`、`macos/` 任何目录；NSIS / MSIX / `.exe` 签名 / 便携 zip 全部只有 Windows 一列。

5. **更严重的是：桌面端在非 Windows 上连启动都做不到。** 平台无关文件里硬编码了 `.exe`，引擎与 sing-box 的可执行文件定位必然失败（`desktop/internal/engineclient/client.go:565`、`desktop/internal/services/tun_config.go:107`）。这是**运行期失败**，不是编译失败，CI 更加发现不了。

6. **有一处安全边界在非 Windows 上被放宽。** `engine/internal/server/server.go:749` 用 `strings.EqualFold` 比较可信可执行文件路径，在区分大小写的文件系统上会把不同文件判为同一；配合全仓 `filepath.EvalSymlinks` 零命中（符号链接从不被解析），软链可直接绕过该检查。

7. **一组跨维度的静默失败（同时是错误处理问题）。** `desktop/internal/services/system_proxy_other.go:11,13` 的 `restoreSystemProxy()` / `restoreSystemProxyDetailed()` 在非 Windows 上直接 `return nil`，即向调用方宣称"已成功恢复系统代理"，实际什么都没做。同族还有 `engine/internal/tun/cleanup_other.go:7`、`config_stage_other.go:5-9`、`process_other.go:12-29` —— 见第六章完整清单。

---

## 二、平台 × 能力矩阵

### 2.1 桌面打包与分发

| 打包目标 / 产物 | Windows | Linux | macOS |
|---|:---:|:---:|:---:|
| `task build`（GUI 编译） | ✅ `desktop/Taskfile.yml:23` | ❌ 无 | ❌ 无 |
| `task package`（分发包） | ✅ `desktop/Taskfile.yml:28` | ❌ 无 | ❌ 无 |
| NSIS 安装包 | ✅ `windows/Taskfile.yml:155` | ❌ 无 | ❌ 无 |
| MSIX 包 | ✅ `windows/Taskfile.yml:177` | ❌ 无 | ❌ 无 |
| `.exe` 签名 | ✅ `windows/Taskfile.yml:207` | ❌ 无 | ❌ 无 |
| 安装包签名 | ✅ `windows/Taskfile.yml:221` | ❌ 无 | ❌ 无 |
| 免安装便携包（zip） | ✅ `build.yml:303`（pwsh 硬编码） | ❌ 无 | ❌ 无 |
| 便携启动器 | ✅ `desktop/portable/*.cmd` | ❌ 无 `.sh` | ❌ 无 `.sh` |
| 运行时资产 `bin/` | ✅ 3 个 Windows 产物 | ❌ 0 个变体 | ❌ 0 个变体 |
| 图标生成 | ✅ `windows/icon.ico` | ❌ | ❌ 无 `.icns` |
| `build:server` / `build:docker`（无 GUI） | ✅ | ✅ `Taskfile.yml:274` | ✅ `Taskfile.yml:274` |

矩阵读法：Windows 列 11/11 全绿；Linux 与 macOS 仅最后一行（headless server / Docker）有产物，其余全部为空格。

### 2.2 CI Runner 覆盖

`.github/workflows/` 恰好 4 个 `.yml`（Glob `**/*.yaml` 返回零），无 composite action、无 `dependabot.yml`、无 `workflow_call` 可复用工作流。

| Runner 平台 | Job 数 | 编译应用 Go | `go test` | `go vet` | `gofmt` | 仅编译 release-version |
|---|---|---|---|---|---|---|
| `windows-2025` | 2（`build.yml:33`、`go-engine.yml:25`） | ✅ 是 | ✅ `build.yml:159,161` / `go-engine.yml:54` | ✅ `build.yml:163,165` / `go-engine.yml:56` | ✅ `build.yml:179` / `go-engine.yml:43` | — |
| `ubuntu-latest` | 3（`build.yml:483`、`release-smoke.yml:18`、`create-release-tag.yml:16`） | ❌ **否** | ❌ | ❌ | ❌ | ✅ `build.yml:515`、`release-smoke.yml:37`、`create-release-tag.yml:36` |
| `macos-*` | **0** | ❌ | ❌ | ❌ | ❌ | — |
| 容器 / self-hosted | **0** | ❌ | ❌ | ❌ | ❌ | — |

### 2.3 运行时能力（`_windows.go` / `_other.go` 配对）

共发现 **80 个平台分派文件、约 40 对**。抽样实测的四对：

| 功能 | Windows 实现 | 非 Windows 实现 | 非 Windows 实际行为 |
|---|---|---|---|
| 系统代理恢复 | `system_proxy_restore_windows.go` | `system_proxy_other.go:11,13` | `return nil` / `return "", nil` —— **静默宣称成功** |
| MTU 检测 | `mtu_windows.go` | `mtu_other.go:11,14` | `errors.New("MTU 检测仅支持 Windows")` |
| TUN 路由预检 | `tun_preflight_windows.go` | `tun_preflight_other.go:12` | `RouteScanError: "...only implemented on Windows"` |
| TUN 平台清理 | `cleanup_windows.go` | `cleanup_other.go:7` | `return nil` —— **静默 no-op** |

---

## 三、构建与 CI 覆盖（最致命的一层）

### [严重] 非 Windows 上 `go test ./...` 会**直接编译失败** —— 测试文件漏标 build tag

- **文件**：`desktop/internal/services/engine_integration_test.go:70`
- **原文**：
  ```go
  	if _, err := os.Stat(proxyMarkerPath()); !os.IsNotExist(err) {
  ```
- **补充原文**：`desktop/internal/services/engine_integration_test.go:1` 直接是 `package services`，**没有任何 `//go:build` 行**；而 `proxyMarkerPath` 定义在 `desktop/internal/services/system_proxy_windows.go:36` `func proxyMarkerPath() string {`，该文件第 1 行是 `//go:build windows`。
- **仓库自测证据**：`reports/vnic/75-audit-go.md:212` 原文 `internal\services\engine_integration_test.go:70:23: undefined: proxyMarkerPath      ← 仓库既有`；`reports/vnic/72-desktop-hyperv.md:350` 进一步写明「该测试文件没有 build tag，却用了只在 `system_proxy_windows.go:36` 定义的函数。**这是仓库既有的历史遗留**」，`reports/vnic/76-inventory-repro.md:239` 补充「来自更早的提交 `b3eadd2`」。
- **影响**：`GOOS=linux` / `GOOS=darwin` 下 `go -C desktop test ./...`（CI 在 `build.yml:161` 跑的那条）与 `go -C desktop vet ./...`（`:165`，vet 也会编译测试文件）**编译失败**。当前被"CI 只有 windows runner"这个结构性缺口掩盖。
- **建议**：给 `engine_integration_test.go` 加 `//go:build windows`，或把该用例拆到独立的 `engine_integration_windows_test.go`。**这是成本最低、且必须最先做的一步** —— 否则新增 Linux runner 会在第一天就把流水线打红。
- **⚠️ 防止引用过时报告**：`reports/vnic/72-desktop-hyperv.md:333` 记录的另外两个未定义符号 `hypervUTF16LE` / `hypervPowerShellScript` **已经修复** —— 它们已被移入跨平台文件 `desktop/internal/services/hyperv_adapter.go:2507` `func hypervUTF16LE(text string) []byte {` 与 `:2526` `const hypervPowerShellScript =`。不要按旧报告把它们算作现存缺口。

### [严重] `wails/v3` 在 Linux 上源码级编译失败（非环境缺库，是框架自身缺陷）

- **文件**：`desktop/go.mod:9`
- **原文**：`	github.com/wailsapp/wails/v3 v3.0.0-alpha2.119`
- **仓库实测原文**（`reports/vnic/76-inventory-repro.md:240-241`）：
  > `GOOS=linux go -C desktop build ./... 同样失败，原因是 wails 框架自身 pkg/application/menu_linux.go:7: undefined: pointer，stash 掉本次改动后同样失败。`

  "stash 掉本次改动后同样失败"这一句是关键 —— 它证明该失败**与本仓库代码无关**，是 wails alpha2 版本自身的 Linux 分支缺陷。
- **对照组**：同一文件 `:236` 记录 `GOOS=linux GOARCH=amd64 go -C desktop build ./internal/services/` **退出码 0**。即 **HypoMux 自有包在 Linux 上能编过，卡点 100% 在 wails 框架层**。
- **影响的 import 点（4 处无平台限定，使 `pkg/application` 在每个 GOOS 都被编译）**：`desktop/main.go:21` `	"github.com/wailsapp/wails/v3/pkg/application"`、`desktop/main.go:22` `	"github.com/wailsapp/wails/v3/pkg/events"`、`desktop/internal/platform/wails/desktop.go:11-12`。另有 3 处已正确限定：`desktop/internal/platform/wails/tray_position_windows.go:8`（文件 `:1` `//go:build windows`）、`tray_position_other.go:5`（`:1` `//go:build !windows`）。
- **缓解事实**：`desktop/go.mod:18` `	github.com/godbus/dbus/v5 v5.2.2 // indirect` 与 `:15` `	github.com/adrg/xdg v0.5.3 // indirect` 说明 wails 的 Linux 代码路径确实存在于依赖图中，不是被裁掉的。同时仓库自带构建路径因 `desktop/build/windows/Taskfile.yml:65` `      GOOS: windows` 从不触及它。
- **建议**：若要支持 Linux，需锁定一个已修复该 `menu_linux.go` 问题的 wails alpha 版本（或自行 patch），再补 `ubuntu-latest` runner 并安装 GTK/WebKitGTK 开发包。若明确只支持 Windows，应在 `desktop/main.go:31` `func main()` 开头加 `runtime.GOOS` 早退并在 README 写明。

### [严重] 全部 Go 编译/测试只发生在 `windows-2025`

- **文件**：`.github/workflows/go-engine.yml:25`
- **原文**：`    runs-on: windows-2025`

全仓库 `runs-on:` 仅 5 处：`build.yml:33`、`build.yml:483`、`go-engine.yml:25`、`release-smoke.yml:18`、`create-release-tag.yml:16`。全仓库仅有的 3 条 `go test`、2 条 `go vet`、2 条 `go build` 全部落在这两个 Windows job 内：

- `go-engine.yml:54` `go test ./...`
- `go-engine.yml:56` `go vet ./...`
- `go-engine.yml:67` `go build -trimpath -o "$env:RUNNER_TEMP\hypomux-engine.exe" .\cmd\hypomux-engine`
- `build.yml:159` `go -C engine test ./...`
- `build.yml:161` `go -C desktop test ./...`
- `build.yml:163` `go -C engine vet ./...`
- `build.yml:165` `go -C desktop vet ./...`

**影响**：`//go:build !windows` 分支的类型错误、签名不匹配、导入缺失可以全部合入 main 而 CI 全绿。`engine/internal/proxy/health_errno_other_test.go:1`（`//go:build !windows`）是唯一的非 Windows 测试文件，但它从未被执行过 —— **非 Windows 路径的测试用例实际执行次数为 0**。
**建议**：新增 `runs-on: ubuntu-latest` 的 matrix job 跑 `go -C engine test ./...` 与 `go -C desktop test ./...`，再新增 `macos-latest` 覆盖 darwin 分支。

### [严重] `.github/` 下不存在任何 macOS runner

- **文件**：`.github/workflows/`（全部 4 个文件）
- **原文**：无匹配行。搜索 pattern `macos|macOS|osx|darwin|Mac OS`（Grep tool，path=`.github`）→ `No matches found`。
- **补充**：搜索 `GOOS|GOARCH|CGO_ENABLED` 在 `.github/` 下无任何环境变量赋值命中。`build.yml:179/181/184` 与 `go-engine.yml:43/45` 是 gofmt，不是交叉编译。
- **影响**：darwin 分支代码零编译。macOS 上会编译失败或静默走 fallback，CI 永远不会提示。
- **建议**：加入 `macos-latest` runner 跑 `go vet` + `go test`。

### [严重] 唯一的构建链把 `GOOS` 硬编码为 windows

- **文件**：`desktop/build/windows/Taskfile.yml:102`
- **原文**：`      GOOS: windows`（同文件 `:64-67` 的 `GOOS: windows`，以及 `:103` `CGO_ENABLED: '0'`）
- **影响**：调用链为 `build.yml:191` `wails3 task windows:package` → `desktop/Taskfile.yml:17` `windows: ./build/windows/Taskfile.yml` → 该文件 `:137` `package:` → `:69` `build:core-runtime` → `:88` `go build ... -o "{{.ROOT_DIR}}/{{.BIN_DIR}}/hypomux-engine.exe"`。因为 env 里 `GOOS` 写死，**即使把这个 job 搬到 ubuntu-latest，产出的仍是 `.exe` 而非 Linux 二进制**。加 Linux runner 只能验证 `_other.go` 分支能否编译，产不出可发布产物。
- **建议**：把 `GOOS`/`GOARCH` 提为 task 变量（默认 `windows`），另加 `linux`/`darwin` 变体 task。

### [高] `-H windowsgui` 链接标志与 Windows 强绑定

- **文件**：`desktop/build/windows/Taskfile.yml:63`
- **原文**：`BUILD_FLAGS: '... -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"{{end}}'`
- **影响**：`-H windowsgui` 是 ld64/COFF 专属，移植到 Linux/macOS 会在链接阶段报 `-H flag not recognized`。
- **建议**：`{{if eq .TARGET_OS "windows"}}-H windowsgui{{end}}`。

### [高] CGO 在 CI 中被强制关闭，cgo 相关路径零覆盖

- **文件**：`desktop/build/windows/Taskfile.yml:103`
- **原文**：`      CGO_ENABLED: '0'`（另见 `:66`，默认值即 0）
- **影响**：`build:docker:` 的摘要是 `Cross-compiles for Windows using Docker with Zig (for CGO builds on non-Windows)`，但 CI 从不调用它。依赖 cgo 的第三方代码在 Linux 目标上能否编译，CI 完全无感知 —— 典型表现为"Windows 全绿、Linux 构建炸在 runtime/cgo"。
- **建议**：至少在 ubuntu job 加一次 `CGO_ENABLED=1 go build ./...` 冒烟。

### [高] 便携包组装步骤是 Windows PowerShell 硬编码

- **文件**：`.github/workflows/build.yml:304`
- **原文**：`        shell: pwsh`
- **影响**：该步骤（`:303-458`）使用 `Add-Type -AssemblyName System.IO.Compression`（`:307-308`）、`[System.IO.File]::ReadAllText`（`:320`）、`[System.IO.Compression.ZipFile]::Open`（`:413`）等 Windows-only .NET API。反斜杠路径 `'desktop\VERSION'`（`:316`）、`'bin\sing-box.exe'`（`:331-333`）、`'bin\hypomux-engine.exe'`（`:377`）在 Linux/macOS 上无法解析。便携 zip 是**唯一**的非安装器发行形态，非 Windows 用户拿不到任何免安装包。
- **建议**：把组装逻辑抽成跨平台脚本（Node 或 Go），或按 `runner.os` 分支提供 bash 与 pwsh 两份实现。

### [高] 便携包内容清单把 Windows 产物写死为唯一来源

- **文件**：`.github/workflows/build.yml:328-334`
- **原文**：
  ```
    $binarySources = @(
      (Join-Path $repoRoot 'desktop\bin\hypomux.exe'),
      (Join-Path $repoRoot 'desktop\bin\hypomux-engine.exe'),
      (Join-Path $repoRoot 'bin\sing-box.exe'),
      (Join-Path $repoRoot 'bin\wintun.dll'),
      (Join-Path $repoRoot 'bin\libcronet.dll')
    )
  ```
- **影响**：`:340-344` 对它们做 fail-fast `Test-Path` 断言，非 Windows 上必然在 `:342` `throw "Portable package source is missing: ..."`。注意 `wintun.dll` 是 Windows 虚拟网卡驱动 —— Linux/macOS 的等价物是内核 TUN / `utun`，不能靠改名解决。
- **建议**：清单按 `runner.os` 分支；macOS 用 `.app` bundle 且无 wintun，Linux 用 AppImage + 内核 TUN。

### [高] 运行时资产 `bin/` 只有 Windows 产物

- **文件**：`desktop/build/windows/Taskfile.yml:77-82`
- **原文**：
  ```
      - sh: 'test -f "{{.ROOT_DIR}}/../bin/sing-box.exe"'
        msg: "Missing official runtime asset: bin/sing-box.exe"
  ```
- **影响**：仓库根 `bin/` 全部内容为 `sing-box.exe`、`wintun.dll`、`libcronet.dll`、`README.md`，**0 个非 Windows 变体**。`bin/README.md:5` 自证：`Version: **1.14.2** (Windows amd64, official SagerNet build).`
- **建议**：改为 `bin/<os>-<arch>/` 结构并按 `OS`/`ARCH` 选择，或构建期下载 + 校验 SHA256（`README.md:8-9` 已记录 SHA-256）。

### [中] `desktop/Taskfile.yml` 不含任何非 Windows 打包任务

- **文件**：`desktop/Taskfile.yml:15-17`
- **原文**：
  ```
  includes:
    common: ./build/Taskfile.yml
    windows: ./build/windows/Taskfile.yml
  ```
- **影响**：`task build` / `task package` / `task run` 全部转发到 `windows:*`（`desktop/Taskfile.yml:23`、`:28`、`:33`）。在 Linux/macOS 上调用 `task package` 会因找不到 `windows:package` 而直接失败，无降级路径。
- **建议**：新增 `desktop/build/darwin/Taskfile.yml`（`.app` + dmg）与 `desktop/build/linux/Taskfile.yml`（AppImage/deb/tar.gz），根 Taskfile 用 `{{OS}}` 分派。

### [中] `desktop/build/` 下不存在任何非 Windows 打包目录

- **文件**：`desktop/build/Taskfile.yml:1-3`
- **原文**：`version: '3'` / 空行 / `tasks:`
- **影响**：Glob `desktop/build/{darwin,linux,macos}/**` 返回 `No files found`。`build:` / `package:` / `run:` / `sign:` 任务只存在于 `windows/Taskfile.yml`。模板自带的 `darwin:build:universal` 引用（`desktop/build/Taskfile.yml:75`）是**悬空注释**，指代的 task 不存在。
- **建议**：补 darwin/linux 目录，同时删除 `:75` 那条误导性注释。

### [中] govulncheck 只覆盖 engine 模块

- **文件**：`.github/workflows/go-engine.yml:62`
- **原文**：`          go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`
- **影响**：该 job 的 `defaults.run.working-directory: engine`（`go-engine.yml:26-28`）。`desktop/` 模块（100+ 依赖，含 Wails v3）完全没有漏洞扫描覆盖。
- **建议**：在 `build.yml` 的 `Validate Go modules` 步骤（`:152-166`）追加 desktop 模块的 `govulncheck ./...`。

### [中] `go-engine.yml` 的 paths 过滤不含 `desktop/**`

- **文件**：`.github/workflows/go-engine.yml:6-10`
- **原文**：`    paths:` 后接 `:7` `'engine/**'`、`:8` `'bin/sing-box.exe'`、`:9` `'protocol/**'`、`:10` `'.github/workflows/go-engine.yml'`
- **影响**：仅改动 `desktop/**` 的 PR 不会跑 engine 的快速 vet/test 门禁，只能靠 `build.yml` 兜底，而后者要跑完整 Wails 打包（`build.yml:191`），重且慢。
- **建议**：把 `desktop/**` 加入 paths，或拆分一个快速 lint job。

### [中] Windows 专属集成测试可能被静默跳过，产生假绿

- **文件**：`desktop/internal/services/tun_preflight_windows_integration_test.go:1`
- **原文**：`//go:build windows`（同类：`desktop/internal/engineclient/service_windows_integration_test.go:1`、`desktop/internal/services/diagnostics_windows_integration_test.go:1`）
- **影响**：这些测试需要 Windows 服务 / WFP / IP Helper 权限。在 GitHub 托管 runner 上若因权限不足而 skip，`go test` 返回 0，CI 显示成功但路径实际未验证。
- **建议**：用 `-v` 断言测试确实执行，或拆分为独立 job。

### [低] `go-engine.yml` 的 validate job 缺少 `name:` 键

- **文件**：`.github/workflows/go-engine.yml:24`
- **原文**：`  validate:`（第 25 行直接是 `    runs-on: windows-2025`）
- **影响**：检查列表显示为裸 job id，纯可读性问题。
- **建议**：补 `name: Validate engine module`。

---

## 四、运行时资产定位（阻塞非 Windows 启动）

### [高] 引擎可执行文件解析在平台无关文件里硬编码 `.exe`

- **文件**：`desktop/internal/engineclient/client.go:565`
- **原文**：
  ```go
  			filepath.Join(root, "bin", "hypomux-engine.exe"),
  ```
- **补充原文**：`client.go:566` `filepath.Join(root, "hypomux-engine.exe")`；`:572-574` `"hypomux-engine.exe"` / `"bin", "hypomux-engine.exe"` / `"dist", "hypomux-engine.exe"`；`:598` `return "", errors.New("未找到 hypomux-engine.exe；可设置 HYPOMUX_ENGINE_PATH")`
- **影响**：`client.go` 无 build tag，`func ResolveExecutable()` 从 `:557` 开始。全部 5 个候选路径都写死 `.exe` 后缀；Linux/macOS 下产物名是 `hypomux-engine`，`os.Stat` 全部落空，直接返回 `:598` 的中文错误。桌面端在非 Windows 上**无法自动定位引擎**，只能靠 `HYPOMUX_ENGINE_PATH` 兜底。**运行期失败，非编译失败。**
- **建议**：提取 `engineExecutableName()` 辅助函数 —— 参照 `engine/cmd/hypomux-engine/main.go:176` 已有的正确写法 `if runtime.GOOS == "windows" { tunName += ".exe" }`，候选列表同时追加带/不带扩展名两个名字。

### [高] TUN 侧车解析同样在平台无关文件里写死 `sing-box.exe`

- **文件**：`desktop/internal/services/tun_config.go:107`
- **原文**：`		singBox, err = resolveRuntimeAsset("sing-box.exe")`
- **补充原文**：`desktop/internal/services/tun_preflight.go:78` `resolveSingBox: func() (string, error) { return resolveRuntimeAsset("sing-box.exe"), },`；`tun_config.go:543` `candidates = append(candidates, filepath.Join(root, "bin", name), filepath.Join(root, name))`（`name` 原样拼接，不追加平台后缀）；`tun_config.go:560` `return "", fmt.Errorf("未找到 %s", name)`
- **影响**：`resolveRuntimeAsset` 本身是平台无关的（`tun_config.go:539`），`name` 由调用方硬编码传入 `.exe`。非 Windows 上候选路径恒为 `.../bin/sing-box.exe` 与 `.../sing-box.exe`，`os.Stat`（`:556`）永远失败 → TUN 配置生成返回"未找到 sing-box.exe"，**TUN 栈整体不可用**。
- **建议**：同上，让 `resolveRuntimeAsset("sing-box")` 内部按 `runtime.GOOS` 决定是否追加 `.exe`。

### [中] 平台无关文件把 Windows 适配器名与 `.exe` 进程名写进 sing-box 配置

- **文件**：`desktop/internal/services/tun_config.go:150`
- **原文**：`			"process_name": []string{"HypoMux.exe", "hypomux-engine.exe", "sing-box.exe"},`
- **影响**：`tun_config.go` 无 build tag。非 Windows 上进程名是 `hypomux`/`sing-box`，这条 `system-direct` 旁路规则永不命中。**缓解事实**：同文件 `:126-133` 用 `os.Args[0]` / `engineExecutableOrEmpty()` / `singBox` 构造了 `processPaths`，`:148` 的 `process_path` 规则在所有平台都能命中，因此实际不会形成代理回环 —— 该行属于非 Windows 上的死配置，而非功能性缺陷。同类：`desktop/internal/services/connections.go:111` `if strings.EqualFold(process, "sing-box.exe") || strings.EqualFold(process, "hypomux-engine.exe") {`
- **建议**：按 `runtime.GOOS` 生成进程名列表，或统一只依赖 `:148` 的 `process_path` 并删除该 `process_name` 分支。

### [中] 兼容性计划在非 Windows 上注入约 40 个永不匹配的 `.exe` 进程名

- **文件**：`desktop/internal/services/compatibility.go:25`
- **原文**：`		"qiyou.exe", "networkdaemon.exe", "qeetm.exe", "injhelper.exe", "injhelper64.exe", "lsphelper64.exe",`
- **影响**：整个 `compatibilityProcessNames` 列表（`:24-34`）写死 `.exe`。关键在于 `compatibility_other.go:5-7` 的非 Windows 降级实现 `return normalizedCompatibilityPlan(nil, nil)` —— 路径列表虽为空，但 `normalizedCompatibilityPlan` 的 `:37` `names := append([]string(nil), compatibilityProcessNames...)` 仍会把全部 `.exe` 名字塞进 `ProcessNames`，最终进入 sing-box 的 route 规则。非 Windows 上静默无效果，污染配置体积与"已检测到的冲突程序"UI 语义。
- **建议**：把 `compatibilityProcessNames` 移进 `compatibility_windows.go`，或按 `runtime.GOOS` 追加无扩展名变体。

---

## 五、安全边界与文件系统语义

### [高] 可信 TUN 可执行文件的路径授权用了大小写不敏感比较

- **文件**：`engine/internal/server/server.go:749`
- **原文**：`		if !strings.EqualFold(filepath.Clean(asserted), filepath.Clean(trusted)) && pinnedDigest == "" {`
- **影响**：这是 `server.go`（平台无关文件）里的**安全边界**：请求方断言的 sing-box 路径若与策略路径不等就拒绝。Windows 上 `EqualFold` 正确（NTFS 不区分大小写），但 Linux/macOS 区分大小写，`EqualFold` 会把 `/opt/Sing-Box` 与 `/opt/sing-box` 判为相同，**绕过该检查**。仅当 `pinnedDigest` 为空时生效，所以这是条件性策略放宽（`config.ExecutableSHA256` 有值时仍走摘要校验）。
- **建议**：改为按平台分支 —— `windows` 用 `EqualFold`，其余用 `==`；更稳妥的是统一改用 `os.SameFile`（需先 `os.Stat` 两侧）。

### [中] 全仓无 `filepath.EvalSymlinks`，路径授权是纯文本比较，软链可绕过

- **文件**：`engine/internal/server/server.go:757`
- **原文**：`	config.Executable = filepath.Clean(trusted)`
- **影响**：Grep `os.Symlink|os.Readlink|filepath.EvalSymlinks` → **全仓 0 命中**。`filepath.Clean` 只做词法清理，不触碰文件系统。因此在 Linux/macOS 上，把自建二进制软链成受信路径、或让受信路径本身是软链，都能通过 `:749` 的文本相等检查。Windows 侧对**配置目录**做了 reparse point 防护（`engine/internal/tun/config_stage_windows.go:146` `if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {`），但对可执行文件本身没有对应检查。
- **建议**：在 `:757` 之前对 `trusted` 与 `asserted` 各做一次 `filepath.EvalSymlinks`，失败则拒绝。

### [中] 原子写使用固定临时文件名，并发写会互相污染

- **文件**：`desktop/internal/services/atomic_file.go:13`
- **原文**：`	temporary := path + ".tmp"`
- **影响**：同一路径的并发写共用同一个临时文件。两个协程/进程同时写，第二个 `os.OpenFile(..., os.O_CREATE|os.O_TRUNC|os.O_WRONLY, ...)`（`:14`）会截断第一个已写入的内容，两次 `file.Write` 的字节交错；随后 `:38` 的 `os.Rename` 把这份交错数据提交为正式文件 → **静默数据损坏**。此模式在 `settings.go:760`、`blocked_domains.go:214`、`nat_servers.go:277`、`tun_config.go:285`、`support_log.go:220` 重复出现（共 6 处）。
- **建议**：改用 `os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")` 取得随机名 —— `mtu.go:86` 与 `config_stage_windows.go:47` 已经是这个正确写法。

### [中] `os.Rename` 无前置清理、无重试，Windows 上被占用即提交失败

- **文件**：`desktop/internal/services/atomic_file.go:38`
- **原文**：`	if err := os.Rename(temporary, path); err != nil {`
- **前提纠正**：Go 的 `os.Rename` 在 Windows 上**确实会覆盖已存在文件**（走 `MoveFileEx(..., MOVEFILE_REPLACE_EXISTING)`）。真正的差异是：目标文件被其他进程以**不带 `FILE_SHARE_DELETE`** 打开时，Windows 返回 `ERROR_ACCESS_DENIED`，而 Linux 的 `rename()` 无视打开状态直接成功；以及目标是目录时 Windows 直接失败。
- **影响**：这 9 个 `os.Rename` 调用点（`atomic_file.go:38`、`settings.go:782`、`blocked_domains.go:218`、`nat_servers.go:281`、`singbox_rules.go:768`、`support_log.go:222`、`mtu.go:102`、`tun_config.go:289`、`system_proxy_windows.go:142`）**无一**在 rename 前对目标做 `os.Remove`，也**无一**有重试。`atomicWriteFile` 的 6 个调用方（`appearance.go:44`、`hotspot_preferences.go:62`、`hyperv_adapter.go:370`、`system_proxy_windows.go:66/86/135`）会把这个错误直接抛给 UI。
- **最危险的一处**：`singbox_rules.go:497` `err = replaceFileAtomically(snapshot.Path, snapshot.Data, snapshot.Mode)` —— 目标 rule-set JSON 可能正被 sing-box 进程打开，此时热加载必然失败。
- **建议**：rename 前 `if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) { … }`，并对 rename 加 3~5 次、50ms 间隔的重试，只对 `ERROR_ACCESS_DENIED`/`ERROR_SHARING_VIOLATION` 重试。

### [中] `replaceFileAtomically` 缺少 `Sync()`，rename 后可能出现空文件

- **文件**：`desktop/internal/services/singbox_rules.go:765`
- **原文**：`	if err := os.WriteFile(temporary, data, mode); err != nil {`
- **补充原文**：`singbox_rules.go:768` `	if err := os.Rename(temporary, path); err != nil {`
- **影响**：`os.WriteFile` 写完即返回，字节仍在页缓存，紧接着 rename。掉电或进程被杀后，目录项已切换但数据块未落盘，正式文件可能长度为 0 或半截。同包的 `atomicWriteFile:31`、`settings.go:775`、`mtu.go:95` 都正确调了 `file.Sync()`，唯独这条路径没有。
- **建议**：改为 `OpenFile` + `Write` + `Sync` + `Close` + `Rename`；更好的做法是直接复用 `atomicWriteFile`。

### [中] 文件权限参数在 Windows 上被静默忽略

- **文件**：`desktop/internal/services/atomic_file.go:14`
- **原文**：`	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, permission)`
- **影响**：Go 在 Windows 上只读取 mode 的写位来设置/清除 `FILE_ATTRIBUTE_READONLY`，其余 ACL 位不落盘。调用方传的 `0o600`（`settings.go:761` 写 `settings.json`、`system_proxy_windows.go:66/86/135` 写代理标记、`hyperv_adapter.go:370` 写 ledger）在 Windows 上等同于"继承父目录默认 ACL 的普通可写文件"。同问题见 `atomic_file.go:10` 的 `os.MkdirAll(filepath.Dir(path), 0o755)`。Linux/macOS 上 `0o600` 生效。这是**平台固有限制**而非 bug，但代码无任何注释说明。
- **建议**：若这些文件含敏感信息，Windows 分支改用显式 ACL（参照 `engine/internal/tun/config_stage_windows.go:182`）；否则至少加注释说明该 mode 在 Windows 上不生效。

### [低] 路径去重用 `strings.ToLower`，在区分大小写的文件系统上会合并不同文件

- **文件**：`desktop/internal/services/compatibility.go:46`
- **原文**：`		key := strings.ToLower(filepath.Clean(absolute))`
- **影响**：非 Windows 上 `/opt/Mihomo` 与 `/opt/mihomo` 是两个不同文件，这里会被判为重复而只保留前者（`:47-50` `if _, exists := seen[key]; exists { continue }`）。同类问题见 `desktop/internal/engineclient/client.go:589` `		key := strings.ToLower(absolute)`。后果是漏掉一个真实存在的冲突代理程序。
- **建议**：用 `runtime.GOOS == "windows"` 分支，或统一改用 `os.SameFile`。

### [低] 路径比较用 `EqualFold`

- **文件**：`engine/internal/vnic/manager.go:310`
- **原文**：`	return strings.EqualFold(m.meta.Executable, meta.Executable) &&`
- **影响**：平台无关的 vNIC 管理器用它判断可执行文件是否变化。非 Windows 上 `/usr/bin/sing-box` 与 `/usr/bin/Sing-Box` 会被判为同一文件，导致复用错误的管理器实例。非 Windows 上实际影响有限（vNIC 本身是 Windows 特性）。
- **建议**：改为平台分支或 `os.SameFile`。

### [低] 日志脱敏用 `%USERPROFILE%` 占位符

- **文件**：`desktop/internal/services/support_log.go:337`
- **原文**：`	value = homePathPattern.ReplaceAllString(value, `%USERPROFILE%`)`
- **补充原文**：`support_log.go:338` `	value = escapedHomePathPattern.ReplaceAllString(value, `%USERPROFILE%`)`
- **影响**：`support_log.go` 是平台无关文件。Linux 上 `/home/alice/.hypomux/settings.json` 会被替换成 `%USERPROFILE%/.hypomux/settings.json` —— 脱敏本身是对的，但产出的是 Windows 风格占位符，附到 Linux bug 报告里会让排障者困惑。
- **建议**：按平台选占位符（`$HOME` / `%USERPROFILE%`）。

### [低] `"HypoMux-Tun"` 字面量散落多处

- **文件**：`desktop/internal/services/adapters.go:136`
- **原文**：`	return strings.EqualFold(strings.TrimSpace(name), "HypoMux-Tun")`
- **影响**：同类命中：`network_routes.go:29`、`tun_address.go:55`、`tun_network_observer.go:63`、`tun_preflight.go:190`。这是 Wintun 的网卡名；`tun_config.go:231` 用同一个名字作为 sing-box 的 `interface_name`，在 Linux 上 sing-box 也会照此创建 TUN 设备，所以多数情况**自洽** —— 风险仅在于若将来 Linux 侧改用其它命名，这些匹配会静默失效。
- **建议**：抽成单一常量 `tunInterfaceName()`。

---

## 六、跨维度的静默失败（同时属于错误处理问题）

### [高] 非 Windows 上"恢复系统代理"静默返回成功

- **文件**：`desktop/internal/services/system_proxy_other.go:11`
- **原文**：`func restoreSystemProxy() error { return nil }`
- **补充原文**：`system_proxy_other.go:13` `func restoreSystemProxyDetailed() (string, error) { return "", nil }`
- **影响**：这是一个"恢复系统代理"的函数，返回 `nil` 等于告诉调用方"已成功恢复"，但实际上什么都没做 —— 且 `restoreSystemProxyDetailed` 连还原描述字符串都返回空。
- **待核查项**：调用方（Grep `restoreSystemProxy`、`restoreSystemProxyDetailed`）若因此在退出时以为已还原而跳过用户提示，或把"未还原"当成"已还原"，则用户在非 Windows 上会残留代理设置而毫无察觉。本项同时是错误处理维度的"静默吞错"，已同步给错误处理审计线。
- **建议**：返回显式的 `errUnsupportedPlatform`，让上层能据此决定是否提示用户。

### [中] 非 Windows 上 TUN 平台清理静默 no-op

- **文件**：`engine/internal/tun/cleanup_other.go:7-9`
- **原文**：`func cleanupPlatform(context.Context) error { return nil }`
- **影响**：清理函数在非 Windows 上什么都不做却报告成功。若上层依赖它的返回值决定是否继续退出流程，会跳过必要的兜底清理。
- **建议**：同上报显式的不支持错误。

### 「静默返回 nil」家族：4 处同构缺陷

这四处共有的问题是：**在非 Windows 上把"没做"报告成"做成了"**，比直接报错危险得多 —— 调用方的 `if err != nil` 防御分支永远不会触发。

| # | 文件:行号 | 原文 | 调用方为什么会被误导 |
|---|---|---|---|
| 1 | `desktop/internal/services/system_proxy_other.go:11,13` | `func restoreSystemProxy() error { return nil }` / `func restoreSystemProxyDetailed() (string, error) { return "", nil }` | 退出/重启流程会认为"环境已恢复干净"。同文件 `:8` 的 `enableSystemProxy` 是正确的 fail-closed（`fmt.Errorf("系统代理模式仅在 Windows 上可用")`），唯独恢复路径放行 |
| 2 | `engine/internal/tun/cleanup_other.go:7-9` | `func cleanupPlatform(context.Context) error { return nil }` | 跳过兜底清理 |
| 3 | `engine/internal/tun/config_stage_other.go:5-9` | `stageTrustedConfig` 返回 `(config.ConfigPath, func() {}, nil)`；`PrepareTrustedConfigStorage() error { return nil }` | **双重伪装**：既返回 nil 错误声称"准备完成"，又返回**空清理函数**让调用方以为"清理动作已注册" |
| 4 | `engine/internal/tun/process_other.go:12-29` | `containProcess` 返回 `noopContainment{}, nil` | 调用方收到 nil 错误，误以为子进程已被容器化隔离 —— **这是一条安全边界被静默放宽**。同文件 `InterruptConsoleProcess` 返回 `fmt.Errorf("Windows console interruption is unavailable on this platform")`，是 fail-closed 的正确写法 |

- **建议**：四处统一返回显式的 `errUnsupportedPlatform`，或在非 Windows 上做实际的 best-effort 实现并把真实错误传回。

### 「不许假装成功」契约的 7 处违例（`_other.go` 桩内部）

上面那 4 处是**跨模块**的 `return nil`。`desktop/` 内部还有 7 处桩违反了同一个契约 —— 它们让调用方以为功能生效了。完整清单：

| # | 文件:行号 | 原文 | 后果 |
|---|---|---|---|
| 1 | `desktop/internal/services/network_routes_other.go:5` | `func readNetworkRoutes() ([]networkRoute, error) { return nil, nil }` | **语义反转**：`tun_network_observer.go:43-47` 的防御注释写的是「Do not replace the last complete baseline with partial observations」，但 `err == nil` 让这段守卫失效，`:48` 把**空路由集**算成合法指纹写进基线，`:53` 的 `previous == fingerprint` 此后恒真 ⇒ 本该记录「检查未完成」的场合被记成「网络环境自始至终毫无变化」 |
| 2 | `desktop/internal/services/network_routes_other.go:7` | `func readAddressNetworkRoutes(ipv6 bool) ([]networkRoute, error) { return nil, nil }` | 击穿 `tun_address.go:81-84` 的 TUN 路由地址冲突检查，`:92` 把空的 `occupiedRoutePrefixes` 当作「无冲突」放行 |
| 3 | `desktop/internal/services/nat_firewall_other.go:10` | `return currentNATFirewallState(), nil` | 假成功。Windows 版 `nat_firewall_windows.go:119,125,129,134,145` 真跑 netsh 并返回真实执行结果；非 Windows 版 error 无条件为 nil。调用链 `diagnostics.go:142-144` → `frontend/src/platform/services.ts:360` → `frontend/src/pages/NATDetectionPage.tsx:313`，前端既不判错也不看 `Supported` |
| 4 | `desktop/internal/services/compatibility_other.go:6` | `return normalizedCompatibilityPlan(nil, nil)` | 恒报「无冲突」，且 `compatibilityPlan` 类型**没有 `Supported` 字段** ⇒ 调用方无法区分「检测过且无冲突」与「根本没检测」。对照 `engine.go:675-677` 把 DNS 探测的 error 当值传给 `resolveTUNDNSEgress` 做优雅降级 —— 同一个文件里两种截然不同的降级质量 |
| 5 | `desktop/internal/services/adapter_metadata_other.go:6` | `return map[int]adapterMetadata{}` | `adapters.go:45` 拿到的 `metadata` 并入 `AdapterView`，网卡类型/隧道类型字段静默变零值；无错误通道，「确实没有」与「没检测」不可区分 |
| 6 | `desktop/internal/services/connection_process_other.go:6` | `return map[uint64]string{}` | `connections.go:73` 活动连接丢失进程归属（`connections.go:80` 之后 Clash API 还能补一部分，故非全丢） |
| 7 | `desktop/internal/services/diagnostic_probe_other.go:18` | `Status: "unavailable", LossRate: 100,` | **复用了已有语义**：`frontend/src/i18n/legacy.messages.json:79` `"diag_desc_unavailable": "链路不可用：100% 丢包或源 IP 绑定失败（可能是网卡被架空/掉线）。"`，而 `frontend/src/state/useEngineState.ts:550-551` 把它映射成健康度 `"failed"` ⇒ 非 Windows 上**每张网卡都渲染成红色故障**，并弹出「网卡被架空/掉线」的误导解释 |

- **对照组 —— 同一仓库里做得对的降级**（这 7 处应该照抄哪几行）：
  - `desktop/internal/services/tun_dns_egress_other.go:8` `return "", errors.New("当前平台不支持读取系统默认路由")`
  - `desktop/internal/services/tun_preflight_other.go:12` `RouteScanError: "TUN route preflight is only implemented on Windows",`
  - `desktop/internal/services/mtu_other.go:11,14`、`startup/wifi_other.go:8`、`hotspot_other.go:10-15`、`processes_other.go:8,12`、`system_proxy_other.go:8` 全部返回显式 `errors.New("...仅支持 Windows")`
  - `desktop/internal/services/nat_firewall_other.go:6` 用结构化字段 `NATFirewallState{Detail: "Host firewall integration is only available on Windows"}` 表达不可用 —— 同文件 `:10` 却没这么做，属于同一文件内的自相矛盾
  - `desktop/internal/platform/wails/tray_position_other.go:8` `return func() error { return tray.PositionWindow(window, 8) }` —— 这**不是桩**，是真实实现，进一步说明 `_other.go` 里的假成功是选择而非必然

### [中] `PrivilegedLaunchSupported()` 能力标志存在，但共享分派路径从不查它

- **文件**：`desktop/internal/engineclient/privileged_other.go:18-19`
- **原文**：`func PrivilegedLaunchSupported() bool {` / `    return false`
- **对照**：`desktop/internal/engineclient/privileged_windows.go:98-100` 返回 `true`。
- **影响**：该导出函数**全仓只有一个调用点**，而且在 Windows-only 文件里 —— `desktop/internal/services/tun_preflight_windows.go:26` `PrivilegeBrokerAvailable: engineclient.PrivilegedLaunchSupported(),`。真正做分派的共享代码 `desktop/internal/engineclient/client.go:220-223` 是：
  ```go
  launcher := c.normalLauncher
  if requireElevated {
      launcher = c.elevatedLauncher
  }
  ```
  **无任何能力探测**。非 Windows 上 `requireElevated == true` 会选中 `privileged_other.go:14-16` 的 `unsupportedPrivilegedLauncher{}`，其 `Launch`（`:22-24`）无条件返回 `errors.New("当前平台不支持独立管理员核心")`；而 `client.go:240-265` 的握手后回退走 `postHandshakeFallbackLauncher` 接口，`unsupportedPrivilegedLauncher` 并未实现该接口 ⇒ **连降级路径都不存在**。
- **建议**：在 `client.go:221` 加能力守卫 `if requireElevated && !PrivilegedLaunchSupported() { return Hello{}, ErrElevationUnsupported }`，并导出该哨兵错误，让上层能区分「平台不支持提权」与「提权被拒绝」。

### [中] `WebView2Available()` 在非 Windows 上恒返回 `true`

- **文件**：`desktop/internal/platform/webview2_other.go:10-14`
- **原文**：`WebView2Available() bool { return true }`（同文件 `ShowWebView2MissingMessage() {}` 为空实现）
- **影响**：非 Windows 上程序一路通过可用性检查，直到 `app.Run()` 才失败，用户拿不到任何早期提示。对照组：同包 `desktop/internal/services/mtu_other.go:11`、`hotspot_other.go:12` 等都返回了明确的 `errors.New("...仅支持 Windows")`，此处是少数几个"用假 true 放行"的桩之一。
- **建议**：返回 `false`，并在 `desktop/main.go:31` `func main()` 开头加 `runtime.GOOS` 早退与清晰提示。

### [低] 仓库自述 Windows-only —— 这是设计意图的自证

- **文件**：`engine/internal/platform/identity_other.go:7-8`
- **原文**：
  ```go
  // CurrentIdentity keeps protocol tests and tooling portable. HypoMux itself is
  // Windows-only, so elevation is meaningful only in identity_windows.go.
  ```
- **补充原文**：同文件 `:9-14` `CurrentIdentity()` 返回 `{ProcessID: os.Getpid(), Elevated: false}`，`Elevated` 硬编码 `false`。
- **影响**：这解释了上面所有静默桩为何存在 —— 它们是**有意的降级，不是疏漏**。但它同时意味着"Linux/macOS 零覆盖"是**已知且未兑现的承诺**：仓库一边声明 Windows-only，一边维护着 39 个 `_other.go` 和整套跨平台依赖图。
- **建议**：必须二选一 —— 要么兑现承诺（加 CI runner + 修 `engine_integration_test.go:70` + 修 wails 的 `menu_linux.go` 问题），要么删掉伪跨平台面并在 README 写明真实支持矩阵。现在这种中间状态是最贵的：既付出了维护成本，又得不到任何非 Windows 的质量保障。

---

## 七、已核查、确认无问题的项（避免重复排查）

| 类别 | 检查 pattern | 结论 |
|---|---|---|
| UNC / 设备路径 | `\\\\[.?]\\` | 0 命中。唯一 UNC 出现在 `engine/cmd/hypomux-engine/service_policy_windows_test.go:104` 的**否定测试**（断言 UNC 被 `requireFixedLocalVolume` 拒绝），属正确用法 |
| 硬编码盘符 `C:\` | `C:\\` | 25 命中，**全部在 `_test.go` 里**（`engine/internal/server/server_test.go` 约 20 处、`desktop/internal/services/connection_process_clash_test.go:21`、`hyperv_adapter_test.go:1733/1780`），均为 JSON fixture 字符串，生产代码零处 |
| 注册表 | `HKLM\|HKCU\|HKEY_\|registry\.` | 74 命中。生产代码**全部**位于 `_windows.go`，且都配有 `_other.go` 降级桩 → 非 Windows 可编译 |
| netsh / powershell | `netsh\|cmd\.exe\|powershell` | 35 命中。生产代码全部在 `_windows.go`（`nat_firewall_windows.go:145`、`cleanup_windows.go:151-155`、`mtu_windows.go:52`、`sharing_windows.go:42`、`hyperv_adapter_windows.go:271`、`system_tools_windows.go:17-21`、`tun_connectivity_windows.go:20`） |
| 硬编码 `.exe` 拼接 | `\.exe` | 213 命中，生产代码仅 `compatibility.go:25-33`、`tun_config.go:150`、`connections.go:111` 三处（均已单列）。`engine/cmd/hypomux-engine/main.go:177` `tunName += ".exe"` 由 `:176` 的 `runtime.GOOS == "windows"` 正确守卫；`desktop/internal/platform/wails/desktop.go:249-256` 的 `explorer.exe`/`open`/`xdg-open` 由 `switch runtime.GOOS` 正确守卫 |
| 符号链接 / 权限修改 | `os\.Symlink\|os\.Readlink\|os\.Chmod\|os\.Lchown` | **0 命中** |
| `O_EXCL` 锁文件 | `O_EXCL` | **0 命中**。唯一跨进程锁是 `desktop/internal/services/system_proxy_lock_windows.go:45` 的 `windows.LockFileEx(... LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY ...)`，整个文件带 `//go:build windows`，无需 `_other` 桩 |
| `os.SameFile` | `os\.SameFile` | 仅 1 处 `desktop/internal/engineclient/service_windows.go:87`，在 `_windows.go` 内且用法正确 |
| `filepath.Join` | `filepath\.Join` | 155 命中，全部为平台无关的标准写法，未发现 Join 后接字面 `\\` 的反模式 |
| `.gitattributes` 换行符 | — | `:12` 已有 `*.sh text eol=lf`，未来新增 POSIX 脚本不会被 CRLF 污染 |
| `{{exeExt}}` 跨平台变量 | — | `desktop/build/Taskfile.yml:261`、`:272` 正确使用 Wails3 的 `{{exeExt}}` 而非硬编码 `.exe`，说明公共 Taskfile 本身平台中立，缺的是 darwin/linux 的 GUI 打包任务 |
| `platforms: [linux, darwin]` 分支 | — | `windows/Taskfile.yml:60-61`、`:86-87`、`:95-100`、`:127-128` 逻辑正确（POSIX host 上用 `mkdir -p`/`cp`/`rm` 替代 PowerShell）。**但它们产出的是 `GOOS: windows` 的 Windows 二进制**，属于"在 POSIX 上交叉编译 Windows"，易被误读为跨平台支持 |
| `desktop/scripts/` | — | 目录不存在（Glob 无结果），无可评估对象；脚本职责实际由 `desktop/portable/*.cmd` 与 `tools/support/` 承担 |

---

## 八、缺口统计与修复优先级

| 严重程度 | 条数 |
|---|---|
| 严重 | 5 |
| 高 | 9 |
| 中 | 15 |
| 低 | 6 |
| 小节级清单（不计入上表，另计 11 条） | 11 |
| **合计条目** | **46** |

> 「小节级清单」指以表格形式集中呈现、已在正文逐条给出 `文件:行号` 的族：第六章「静默返回 nil 家族」4 条 + 第六章「不许假装成功契约的 7 处违例」7 条。

### 修复优先级

**第 0 梯队（成本最低，且必须最先做 —— 否则后面全部白做）**
1. 给 `desktop/internal/services/engine_integration_test.go` 加 `//go:build windows`。这是仓库已知的历史遗留（`reports/vnic/75-audit-go.md:212`），当前被「CI 只有 windows runner」掩盖；一旦加上 Linux runner，它会在第一天就把流水线打红。**1 行改动**。
2. 定下跨平台立场：兑现承诺（修 wails `menu_linux.go:7` 的 `undefined: pointer`、加 CI matrix、补 `darwin/` `linux/` 打包目录），或删掉伪跨平台面并在 README 写明真实支持矩阵。**现在这种"既付维护成本又无任何非 Windows 保障"的中间状态是最贵的**（自证见 `engine/internal/platform/identity_other.go:7-8`「HypoMux itself is Windows-only」）。

**第一梯队（阻塞非 Windows 可用）**
3. `client.go:565` / `tun_config.go:107` 的 `.exe` 硬编码 —— 应用在非 Windows 上连启动都做不到。抽出平台配对函数（范本：`engine/cmd/hypomux-engine/main.go:176` `if runtime.GOOS == "windows" { tunName += ".exe" }`）。
4. CI 加 `ubuntu-latest` + `macos-latest` matrix 跑 `go test ./...` / `go vet ./...` —— 否则这 39 个 `_other.go` 连能否编译都无人验证。
5. `desktop/build/windows/Taskfile.yml:102` 的 `GOOS` 提为变量 —— 否则加了 Linux runner 也产不出 Linux 二进制（它会把交叉编译产物误当跨平台支持）。

**第二梯队（安全与数据正确性）**
6. `server.go:749` 的 `EqualFold` + 全仓补 `filepath.EvalSymlinks`，与 `config_stage_windows.go:146` 已有的 reparse point 防护对齐。
7. 固定临时名（6 处）+ 9 个 `os.Rename` 点无 `os.Remove` 与重试 —— 可一次性改进 `atomicWriteFile` 并让其余 5 处复用。
8. `support_log.go:222` 的 `_ = os.Rename` 与 `singbox_rules.go:765` 缺失的 `Sync()` —— 前者让日志修剪静默失效，后者在掉电后可能产生空 rule-set 文件（这两条详见 [01-error-handling.md](01-error-handling.md)）。

**第三梯队（静默失败显式化 + 补齐打包）**
9. 13 处「假装成功」桩（第六章两张表）改为显式 `errUnsupportedPlatform` 或结构化 `Supported` 字段。**注意其中 3 处有配套的正面范本可抄**：`tun_dns_egress_other.go:8`、`tun_preflight_other.go:12`、`nat_firewall_other.go:6`。
10. `privileged_other.go:18` 的 `PrivilegedLaunchSupported()` 接进 `client.go:220-223` 的共享分派路径。
11. `diagnostic_probe_other.go:18` 的 `"unavailable"` 换独立状态词 —— 否则「平台不支持」在 UI 上会永远长成「网卡被架空」。
12. 新增 `desktop/build/{darwin,linux}/Taskfile.yml` + 跨平台便携包组装脚本 + `bin/` 分平台资产结构。