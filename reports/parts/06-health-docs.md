# HypoMux 工程健康度分析（测试 / 依赖 / 文档 / CI）

> 分析对象：仓库根目录 `<repo>`，分支 `main`（干净树）。
> 分析日期：本次会话。方法：静态阅读 + 文件枚举 + 交叉比对；**无 Go 工具链**，未编译、未运行任何 Go 测试，所有"通过/失败"结论均来自代码与 CI 配置的静态推断。
> 证据约定：每条结论附 `相对路径:行号`。无法静态核实的写"未验证"。
> 交付物：本文件（`reports/parts/06-health-docs.md`），是主理人汇总用的材料，非最终报告。

---

## 0. 结论速览

| 维度 | 分档 | 一句话依据 |
| --- | --- | --- |
| 测试 | **B** | Go 侧 149 个测试文件 / 22,931 行，算法级强断言；但真实协议与网络生命周期测试在 CI 中**永久跳过**，前端仅 56 文件 / 4,469 行且无 lint 门 |
| 文档 | **C+** | 49 篇 Markdown、多数带日期与提交号，但历史迁移文档未清理，硬编码统计数字普遍陈旧，无 doc↔code 自动校验 |
| CI | **B** | 3 个工作流覆盖 gofmt/vet/mod verify/engine govulncheck/前端测试/NSIS 打包/签名校验；桌面模块无漏洞扫描、无前端 lint、无覆盖率、集成测试长期空转 |
| 依赖 | **B-** | 直接依赖总量克制（engine 3 / desktop 6 / 前端 11+10）；但 Wails 为 Alpha、`pixi.js` 锁死 v6、engine 与 desktop 存在 `x/net`/`x/text` 版本偏斜、仓库无依赖更新机器人配置 |

综合成熟度：**B（工程实践扎实的中小项目）**。最大风险不是"零测试"，而是**"CI 全绿 ≠ 真实链路可用"**——最关键的两条真实链路（真实引擎握手、真实网络启停）在 CI 里是 skip 而非通过。

---

## 1. 测试实况统计

### 1.1 规模与分布

| 指标 | 数值 | 证据 |
| --- | --- | --- |
| Go 测试文件总数 | **149** 个 `*_test.go` | 全仓枚举（`engine`, `desktop` 递归） |
| Go 源文件总数 | 347 个 `.go`（非测试 198） | 全仓枚举 |
| Go 测试行数 / 非测试行数 | 22,931 / 31,153（≈0.74 : 1） | 全仓行数统计 |
| desktop 非测试 `.go` 文件 | 123 | `347 − 149 − 75(engine)` |
| engine 非测试 `.go` 文件 | 75 | 全仓枚举 |
| 前端测试文件 | **56** 个（`*.test.ts` / `*.test.tsx`） | `desktop/frontend/src` 递归枚举 |
| 前端测试行数 / 源行数 | 4,469 / 11,826（≈0.38 : 1） | 全仓行数统计 |
| 前端测试文件形态 | 约 29 个 `.test.tsx`（组件）+ 27 个 `.test.ts`（纯逻辑） | 文件名枚举 |
| Go 测试函数总数 | **686** 个 `func Test` | 全仓 `Select-String 'func Test'` |

Go 测试文件按模块分布（files 为 `*_test.go` 计数）：

| 目录 | 文件数 | 模块 |
| --- | --- | --- |
| `desktop/internal/services` | 72 | desktop |
| `engine/internal/proxy` | 28 | engine |
| `engine/internal/dns` | 6 | engine |
| `desktop/internal/engineclient` | 6 | desktop |
| `engine/cmd/hypomux-engine` | 5 | engine |
| `engine/internal/server` | 5 | engine |
| `engine/internal/tun` | 4 | engine |
| `desktop/internal/platform/wails` | 4 | desktop |
| `engine/internal/api/v1` | 3 | engine |
| `desktop`（根包） | 3 | desktop |
| `desktop/internal/startup` | 3 | desktop |
| `engine/internal/platform` | 2 | engine |
| `desktop/cmd/update-manifest-sign` | 1 | desktop |
| `engine/internal/runtime` | 1 | engine |
| `engine/internal/wfp` | 1 | engine |
| `engine/internal/diagnostic` | 1 | engine |
| `engine/internal/fileintegrity` | 1 | engine |
| `engine/internal/expiry` | 1 | engine |
| `desktop/internal/platform` | 1 | desktop |
| `desktop/internal/releaseversion` | 1 | desktop |
| **engine 合计** | **58** | |
| **desktop 合计** | **91** | |

单文件规模前列（行数）：`engine/internal/server/server_test.go` 943、`desktop/installer_layout_test.go` 871、`engine/internal/proxy/server_test.go` 735、`desktop/internal/services/singbox_rules_test.go` 698、`desktop/internal/services/ai_test.go` 628、`engine/internal/proxy/udp_test.go` 627、`engine/internal/proxy/steam_cdn_test.go` 599、`desktop/internal/services/updater_test.go` 552。
说明：存量测试确实很厚，但**大量测试集中在 `desktop/internal/services`（91 个中的 72 个，占 79%）**，`engine` 侧只有 58 个文件，其中 `proxy` 一个包占 28 个。

前端测试文件按目录分布：

| 目录 | 测试文件数 | 说明 |
| --- | --- | --- |
| `desktop/frontend/src/components`（本层） | 8 | HotspotPanel、HotspotQuickControl、RuleSetsPanel、SteamCDNPanel、steamCDNView、VirtualRows 等 |
| `desktop/frontend/src/components/ai` | 4 | AIAssistant、AssistantCompanion、AssistantMessage 等 |
| `desktop/frontend/src/components/ai/skins` | 9 | Live2D / Layered / 皮肤包与 store（数量最多，说明这块逻辑最复杂） |
| `desktop/frontend/src/components/home` | 2 | NetworkAdapterItem、ThroughputDisplay |
| `desktop/frontend/src/components/notifications` | 3 | AppNotifications、errorCodes、notificationMessage |
| `desktop/frontend/src/components/shell` | 3 | AppShell、CompactNavigation、windowTouchDrag |
| `desktop/frontend/src/components/tray` | 1 | TrayMenu |
| `desktop/frontend/src/pages` | 16 | 页面级测试集中区 |
| `desktop/frontend/src/platform` | 5 | adapterSaveQueue、companionExport、latestSaveQueue、serialPoll、settingsQueue（纯队列逻辑） |
| `desktop/frontend/src/state` | 4 | adapterFeedback、adapterVisibility、startupWarningReminder、useEngineState |
| `desktop/frontend/src/theme` | 1 | appearance.store |
| **合计** | **56** | 30 个 `.test.tsx` + 26 个 `.test.ts` |

### 1.1b 包级测试覆盖矩阵（"哪些包一个测试都没有"）

以 Go 包为单位统计非测试源文件数与测试文件数：

| 模块 | 包 | 源文件 | 测试文件 |
| --- | --- | --- | --- |
| engine | `engine/internal/proxy` | 27 | **28** |
| engine | `engine/internal/dns` | 4 | 6 |
| engine | `engine/internal/server` | 3 | 5 |
| engine | `engine/cmd/hypomux-engine` | 8 | 5 |
| engine | `engine/internal/tun` | 10 | 4 |
| engine | `engine/internal/api/v1` | 1 | 3 |
| engine | `engine/internal/platform` | 12 | 2 |
| engine | `engine/internal/diagnostic` / `expiry` / `fileintegrity` / `runtime` / `wfp` | 各 1–3 | 各 1 |
| engine | `engine/internal/protocol` | 1 | **0** |
| desktop | `desktop/internal/services` | **89** | **72** |
| desktop | `desktop/internal/engineclient` | 8 | 6 |
| desktop | `desktop/internal/platform/wails` | 8 | 4 |
| desktop | `desktop/internal/startup` | 7 | 3 |
| desktop | `desktop/internal/platform` | 5 | 1 |
| desktop | `desktop/internal/releaseversion` | 2 | 1 |
| desktop | `desktop/cmd/update-manifest-sign` | 1 | 1 |
| desktop | `desktop/cmd/release-version` | 1 | **0** |
| desktop | `desktop`（根包，含 `main.go`/安装器脚本解析） | — | 3（含 `desktop/installer_layout_test.go` 871 行） |

判读：

- **22 个含源文件的 Go 包中，19 个至少有一个 `*_test.go`**，覆盖率（按包）约 86%，这是相当健康的比例。
- 零测试的三个包分别为 `engine/internal/protocol`（1 个源文件，协议常量/类型，被 `engine/internal/api/v1` 的 3 个测试间接覆盖）、`desktop/cmd/release-version`（1 个源文件，发布版本改写工具）、`desktop/build/windows/syso`（1 个源文件，PE 资源声明，无行为可测）。**实际未覆盖的业务逻辑包为 0 个**。
- 结构失衡点：`desktop/internal/services` 同时是"最大源目录（89）"和"最大测试目录（72）"；`engine/internal/platform`（12 个源文件）只有 2 个测试文件，`desktop/internal/platform`（5 源）只有 1 个，这两处是相对薄弱区。


### 1.2 Windows 专用测试与构建标签

- `//go:build` 行全仓共 **129** 处：`//go:build windows` **88** 处、`//go:build !windows` **41** 处。也就是说平台分叉是"实现+测试成对"的，非常彻底。
- `*_windows_test.go` 共 **34** 个（engine 10 / desktop 24）。
  例外：`desktop/installer_directory_windows_test.go:1` 是 `package main`，**没有 `//go:build windows` 行**——其 Windows 约束来自文件名后缀 `_windows_test.go` 的隐式 GOOS 约束。这是唯一一处风格不一致。
- `*_other_test.go` 仅 **1** 个：`engine/internal/proxy/health_errno_other_test.go`（与 `engine/internal/proxy/health_errno_windows_test.go` 配对）。
- **全仓不存在 `integration` 构建标签**（`//go:build .*integration` 零命中）。集成测试的隔离完全靠**文件名约定 + 环境变量**，见 §1.4。

### 1.3 CI 中实际执行了哪些测试

三个工作流：

**A. `.github/workflows/build.yml`（"Build Desktop"，677 行，`push main` / `PR main` / `workflow_dispatch`）**

| 步骤 | 行号 | 实际执行的测试 |
| --- | --- | --- |
| Run frontend tests | `.github/workflows/build.yml:144-146` | `pnpm --dir desktop/frontend test`（即 `vitest run`，见 `desktop/frontend/package.json:11`）→ **全部 56 个前端测试文件** |
| Validate Go modules | `.github/workflows/build.yml:152-166` | `go -C engine mod verify`（:155）、`go -C desktop mod verify`（:157）、`go -C engine test ./...`（:159）、`go -C desktop test ./...`（:161）、两个 `go vet`（:163、:165） |
| Verify Go formatting | `.github/workflows/build.yml:168-186` | 对 git 跟踪的 `engine`/`desktop` `*.go` 跑 `gofmt -l` |
| Build and package Wails desktop | `.github/workflows/build.yml:188-191` | `wails3 task windows:package` |
| 仅 production/publish | `.github/workflows/build.yml:296-301` | 只跑 1 个测试：`go -C desktop test ./internal/services -run TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller -count=1 -v` |

Runner：`windows-2025`（`.github/workflows/build.yml:33`）→ **34 个 `*_windows_test.go` 全部具备编译与执行条件**（不是被构建标签排除）。
Go 工具链：`actions/setup-go@v7` + `go-version-file: engine/go.mod`（`.github/workflows/build.yml:57-59`）→ Go 1.26.0。
关键：`build.yml` 的测试步骤在打包步骤（:188）**之前**，且过程中**没有任何 `go build` 生成 `hypomux-engine.exe`**（全文件只有 :199、:254、:309 三处提到该文件，均在测试之后）。
NSIS：`.github/workflows/build.yml:117-133` 用 `choco install nsis` 安装，并把 `MAKENSIS=...makensis.exe` 写进 `GITHUB_ENV`（:133）——**这一步在测试之前**，所以 `desktop/installer_directory_windows_test.go:38` 的 NSIS 用例在 CI 中是会真实执行的。

**B. `.github/workflows/go-engine.yml`（"Validate Go Engine"，67 行，按 `engine/**` 等路径过滤）**

- runner `windows-2025`（`.github/workflows/go-engine.yml:25`），`working-directory: engine`。
- `gofmt -l .`（:43）、`go mod verify`（:52）、`go test ./...`（:54）、`go vet ./...`（:56）、`govulncheck@v1.6.0 ./...`（:59-62）、`go build -trimpath -o $RUNNER_TEMP\hypomux-engine.exe .\cmd\hypomux-engine`（:64-67）。

**C. `.github/workflows/release-smoke.yml`（"Release Trust Smoke Test"，144 行，仅 `workflow_dispatch`）**

- runner `ubuntu-latest`，**不跑任何 Go 单元测试**；只验证 Ed25519 清单签名与内嵌公钥一致（:37-50）、GitHub↔CNB tag 提交一致（:52-80）、CNB Release 可读（:82-104）、signed update channel 字节一致 + 签名（:106-144）。
- 另有 `.github/workflows/create-release-tag.yml` 存在（未展开分析）。

**净结论**：两个 Go 模块的**全部**测试都会在 `windows-2025` 上被编译并运行；前端全部测试都会在 `build.yml:144` 运行。**但"被运行"不等于"有断言"——大量用例在 CI 环境里第一行就 `t.Skip`。**

### 1.4 "存在但未在 CI 执行"的测试（重点）

全仓共 **36 处 `t.Skip`**，分布在 21 个文件。CI 中**从不设置任何 `HYPOMUX_*` 环境变量**（唯一例外见下），因此下列用例在 CI 中是空转：

**(a) 环境变量门控 → CI 永久跳过（10 处）**

| 位置 | 跳过条件 | 覆盖的真实能力 |
| --- | --- | --- |
| `desktop/internal/engineclient/service_windows_integration_test.go:16` | `HYPOMUX_RUN_SERVICE_TEST=1` | 与已安装 Windows 服务的真实命名管道握手 |
| `desktop/internal/services/diagnostics_windows_integration_test.go:13` | `HYPOMUX_RUN_DIAGNOSTIC_TEST=1` | 真实网卡诊断 |
| `desktop/internal/services/engine_integration_test.go:11` | `HYPOMUX_RUN_NETWORK_TEST=1` | 真实引擎启停 + 系统网络恢复 |
| `desktop/internal/services/tun_preflight_windows_integration_test.go:12` | `HYPOMUX_RUN_TUN_PREFLIGHT_TEST=1` | 真实 TUN 预检 |
| `desktop/internal/services/network_routes_windows_test.go:40` | `HYPOMUX_RUN_TUN_PREFLIGHT_TEST=1` | 原生路由表只读检查 |
| `desktop/internal/services/mtu_windows_test.go:17` | `HYPOMUX_MTU_SMOKE_ADAPTER=...` | 真实网卡 MTU 探测 |
| `desktop/internal/startup/wifi_windows_test.go:14` | opt-in 原生 WLAN | 真实 WLAN 快照 |
| `desktop/internal/services/updater_windows_test.go:72` | `HYPOMUX_SIGNED_INSTALLER_TEST=...` | 官方签名安装包验签 |
| `desktop/internal/engineclient/client_test.go:14` | 仓库根存在 `hypomux-engine.exe` | **真实协议握手 + `engine.status`** |
| `desktop/internal/services/engine_integration_test.go:22` | 同上（找不到真实引擎二进制） | 同上 |

其中 `updater_windows_test.go:72` 更微妙：`.github/workflows/build.yml:300` 确实设置了 `HYPOMUX_SIGNED_INSTALLER_TEST`，但紧接着 `:301` 用 `-run TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller` 只放行**一个**用例，所以该文件里其余依赖该变量的用例仍然跳过。

`client_test.go:11-15`（`TestRealEngineHandshakeAndStoppedStatus`）是最值得警惕的一条：它找的是**仓库根**的 `hypomux-engine.exe`（`filepath.Join("..","..","..","..","hypomux-engine.exe")`），而 CI 里引擎产物落在 `desktop/bin/hypomux-engine.exe`（`.github/workflows/build.yml:254`、:309），且在测试步骤之后才生成 → **CI 上前端与引擎之间的真实协议契约从来没有被端到端验证过。**

**(b) 平台门控 → 反向结论：CI 上其实会执行（5 处）**

`desktop/internal/services/ai_test.go:126`、`desktop/internal/services/ai_compat_test.go:128`、`:202`、`:273`、`desktop/internal/services/startup_feedback_test.go:13` 的跳过条件是 `runtime.GOOS != "windows"`（例如 `startup_feedback_test.go:12-14`）。在 `windows-2025` runner 上这些分支不成立，**用例会真实执行**（DPAPI 密钥存储、用户目录迁移）。这一点容易被误判为"死测试"，特此澄清。

**(c) 运行时资源门控 → 视 runner 而定**

- IPv6 回环类：`engine/internal/proxy/ipv6_loopback_test.go:16,22,34,39,45,50,53,56`（8 处 `t.Skipf`）、`engine/internal/proxy/server_test.go:701`、`engine/internal/proxy/udp_test.go:510` —— 优雅降级（等价于 `docs/steam-cdn-v3-plan.md:164` 中"本机 IPv6 环境失败"的历史描述）。
- 活动网卡类：`desktop/internal/services/engine_integration_test.go:40`、`desktop/internal/services/diagnostics_windows_integration_test.go:23`、`desktop/internal/services/tun_preflight_windows_integration_test.go:22`（"no active IPv4 adapter"）。
- 端口占用：`engine/internal/server/server_test.go:26`（本地 DNS 测试端口）。
- 活网毁伤保护：`desktop/internal/services/hotspot_windows_test.go:409`（"active TUN; do not mutate live networking"）、`:414`。
- CI runner 有活动网卡，所以 (c) 类多数会执行；但这类"环境决定是否运行"的写法使覆盖率不可复现。

**(d) 结构性/自述性跳过（4 处）**

- `engine/cmd/hypomux-engine/service_pipe_windows_test.go:119`：`t.Skip("test token is not an interactive user allowed by the production pipe ACL")`。
- `engine/internal/wfp/dns_exemption_windows_test.go:13`：`t.Skip("SDK layout assertions currently cover the Windows amd64 release target")`。
- `engine/internal/dns/resolver_test.go:389`：`t.Skip("PolicyAuto no longer provides at least three endpoints")`。
- `desktop/installer_directory_windows_test.go:40`（非 Windows 才跳过；CI 是 Windows 故不触发）、`:47`（无 makensis；CI 在 `.github/workflows/build.yml:133` 设置了 `MAKENSIS` 故不触发）。

**一句话风险**：`desktop/internal/engineclient` 与 `desktop/internal/services` 下 4 个 `*_integration_test.go` 文件（`service_windows_integration_test.go`、`diagnostics_windows_integration_test.go`、`engine_integration_test.go`、`tun_preflight_windows_integration_test.go`）在 CI 中的实际断言数为 **0**。

### 1.5 断言密度与测试风格（前后端对比）

| 指标 | Go | 前端 |
| --- | --- | --- |
| 用例数 | 686 `func Test` | 265 `it(` |
| 子测试 | **65** 处 `t.Run(` | 31 个文件有 `describe(` |
| 致命断言 | **995** 处 `t.Fatalf` | — |
| 非致命断言 | **46** 处 `t.Errorf` | — |
| 断言总数 | — | **933** 处 `expect(`（≈3.5/用例） |
| 渲染 | — | 180 处 `render(` |
| 异步等待 | — | 125 处 `waitFor(` |
| DOM 查询 | — | 445 处 `screen.getBy*` |
| jest-dom 匹配器 | — | **0** 处 `toBeInTheDocument` |
| 测试替身 | **0** 处 `testify`（纯标准库） | 64 处 `vi.mock(`，覆盖 26/56 个文件；21 处 `fetch`/`stubGlobal` 打桩 |
| 真实 HTTP | 46 处 `httptest.` | — |

解读：

1. **Go 侧零第三方断言库**（`stretchr/testify` 零命中），全部手写 `if ... t.Fatal`。好处：依赖干净、行为可读；代价：`t.Fatalf`:`t.Errorf` ≈ 22:1，**一次失败只暴露第一个问题**，且 686 个用例只有 65 处子测试，意味着绝大多数失败无法定位到具体输入组合。
2. **前端断言靠 `expect(...).not.toBeNull()` 等原生判断**（`desktop/frontend/src/pages/ConnectionsPage.test.tsx:507`），因为 `desktop/frontend/package.json` 的 devDependencies 里**没有 `@testing-library/jest-dom`、也没有 `@testing-library/user-event`**。这解释了 `toBeInTheDocument` 零命中——不是风格选择，而是缺依赖。
3. 前端 mock 面偏宽（见 §2.3），Go 侧则偏好**真实文件系统 + tempdir 注入**，几乎没有 interface mock。

### 1.6 测试类别 × CI 执行路径总览

| 测试类别 | 数量 | CI 是否执行 | 证据 |
| --- | --- | --- | --- |
| Go 两个模块的全部单元测试 | 149 文件 / 686 用例 | ✅ 执行（`windows-2025`） | `.github/workflows/build.yml:159,161`；`.github/workflows/go-engine.yml:54` |
| 前端全部测试 | 56 文件 / 265 用例 | ✅ 执行 | `.github/workflows/build.yml:144-146` |
| `*_windows_test.go`（平台专用） | 34 文件 | ✅ 编译并执行 | runner 为 `windows-2025`（`.github/workflows/build.yml:33`） |
| `runtime.GOOS != "windows"` 门控用例 | 5 处 | ✅ 执行（条件不成立） | `desktop/internal/services/ai_test.go:126`、`ai_compat_test.go:128,202,273`、`startup_feedback_test.go:13` |
| NSIS 安装器目录解析 | 1 用例 | ✅ 执行（CI 装了 NSIS） | `desktop/installer_directory_windows_test.go:38` + `.github/workflows/build.yml:117-133` |
| 真实引擎握手 / 协议契约 | 1 用例 | ❌ **永久跳过** | `desktop/internal/engineclient/client_test.go:11-15`；`build.yml` 测试步骤（:161）前无引擎构建 |
| 真实网络生命周期 / 诊断 / TUN 预检 / 服务 IPC | 4 文件 | ❌ **永久跳过**（env 未设置） | `desktop/internal/services/{engine_integration_test.go:11, diagnostics_windows_integration_test.go:13, tun_preflight_windows_integration_test.go:12}`、`desktop/internal/engineclient/service_windows_integration_test.go:16` |
| 真实签名安装包验签 | 1 用例 | ⚠️ 仅 production/publish 执行 | `.github/workflows/build.yml:296-301` |
| 依赖漏洞扫描 | — | ⚠️ 仅 engine | `.github/workflows/go-engine.yml:59-62`；desktop 无 |
| 覆盖率 | — | ❌ 无 | `.github/workflows/` 零命中 `-cover`/`codecov` |
| 前端 lint / format | — | ❌ 无 | `desktop/frontend/package.json` 零命中 `eslint|prettier|lint` |

这张表是本节的核心结论：**绿色的 CI 覆盖了"代码能编译、格式正确、单元逻辑正确"，但没有覆盖"真实引擎/网络在本机可用"。** 后者恰好是这个项目最容易出问题、也最难手工复现的部分。


---

## 2. 测试质量抽样（3 个代表性文件）

### 2.1 引擎：`engine/internal/proxy/adaptive_allocation_test.go`（329 行 / 5 个 Test）——**评级：强**

证据：

- 用可注入时钟把时间变成输入而非环境：`engine/internal/proxy/adaptive_allocation_test.go:19` `p.now = func() time.Time { return now }`；随后逐次 `now = now.Add(time.Millisecond)`（`:50`）。测试因此**确定性、无 sleep、无 flake 源**。
- 断言的是**不变量**而不是返回值：`:29-31` 断言 `p.allocation.matches(pool)`，即"受限拨号不得用被过滤的子集初始化学习"；`:44-46` 断言绝不被选中的网卡真的没被选中；`:53-54` 用 `reflect.DeepEqual` 断言状态机与份额**在 300 次拨号后一字未变**。
- 有真正的对照组论证，而非只测自己：`:311-336` `TestAdaptiveDownloadModelAgainstRoundRobin` 同时跑 `rr`/`old`/`adaptive` 三种策略（`:322-324`），断言 `adaptive.bytes >= rr.bytes*0.98`（`:326`），并**反向断言旧实现确实会回归**（`:329-331` `old.bytes >= rr.bytes*0.9` 则失败）。这是"测试能捕获回归"的自我证明，价值远高于普通 happy-path。
- 表驱动 + 子测试：`:312-321` 三个场景（`startup-long-streams` / `symmetric-chunks` / `asymmetric-chunks`），`:332-334` 还针对非对称场景断言"自适应确实被激活过"（`adaptive.adapting != 0`），避免"没跑起来也算过"。
- 无 mock、无外部依赖：只依赖同包的 `newPerformanceTable`、`newScheduler`、`Adapter` 结构。

不足：它是**模型级仿真**，不建立真实 socket，所以"份额控制逻辑正确"不等于"真实链路带宽分配正确"；真实带宽行为只能靠 §1.4 中被跳过的网络集成测试。

### 2.2 桌面服务：`desktop/internal/services/singbox_rules_test.go`（698 行 / 18 个 Test）——**评级：强（但受全局状态约束）**

证据：

- 用真实临时文件系统替代 mock：`:15` `t.Setenv("HYPOMUX_DATA_DIR", t.TempDir())`，然后直接 `os.ReadFile` 校验产物（`:36`），断言的是真实落盘 JSON 的字段与优先级（`:29-40`）。
- 覆盖**失败路径与回滚**，这是最能体现测试深度的信号：
  - `:75` `TestPublishRuleSetFilesRollsBackEveryReplacementOnFailure`——每个替换失败都要回滚；
  - `:116` `TestRefreshRuleSetsRestoresFilesWhenSettingsCommitFails`；
  - `:136` `TestRoutingSaveRestoresSettingsAndRuleSetsAfterPartialPublish`（部分发布后恢复）。
- 断言"不存在"用 `errors.Is(err, os.ErrNotExist)`（`:695`），而不是只判断 error 非空——精确。
- 语义边界清晰：`:666` `TestCleanupKeepsDisabledSubscriptionPayloads` 区分"禁用（保留缓存）"与"删除（清理文件）"（注释见 `:681-682`、`:687`），这正是容易写错的业务规则。
- 命名即规格：18 个用例名全部是完整句子（如 `:484` `TestRuleSetRestartRequirementDetectsNewlyLiveExternalRuleSet`）。

不足：测试通过包级全局（`HYPOMUX_DATA_DIR`、`ruleSetDirectory()`）间接注入，因此**无法 `t.Parallel()`**（全文件无一处并行）；同时断言大量依赖字符串/JSON 形状，配置结构调整会带来成片修改。

### 2.3 前端：`desktop/frontend/src/pages/ConnectionsPage.test.tsx`（450 行 / 18 个 it）——**评级：中上**

证据：

- 不是 smoke：断言交互后的**状态与可见性**，例如分组展开在刷新后保持（`:201`）、清空筛选不误清搜索词（`:242`）、排序点击后行序变化（`:295`）、只对存在的单网卡连接提供路由（`:324`）。
- 覆盖并发冲突这一硬骨头：`:450` `keeps the draft open when a concurrent writer changes the revision`、`:465` `requires reviewing a newly discovered conflict before replacing it`；结尾 `:510` 断言 `mocks.saveRules` **未被调用**——即"拒绝覆盖"被真正验证，而不是只断言弹窗出现。
- 覆盖可达性：`:497-509` 连续 3 次打开/取消上下文菜单后仍能拿到 dialog，并断言退出动画期间内容仍在（`:507`）。
- 依赖注入方式明确：`vi.hoisted` + `vi.mock("../platform/services")`（`:17-34`），并用 `withServiceTimeout` 透传实现（`:33`）降低超时噪声；i18n 也被替换为固定 locale（`:36-38`），避免文案抖动。

不足（也是全前端通病）：

- **替换了整个服务模块**（`:24-34` 一次性 mock `engine.connections`、`routing.snapshot/previewBatch/save`），Wails 绑定层的真实形状、序列化与错误语义**完全没有被覆盖**——前端测试通过不能说明 `platform/services` 契约正确。
- 断言缺少语义匹配器，只能 `not.toBeNull()`（`:507`），失败信息可读性差。
- 没有 `user-event`，用 `fireEvent`（`:4`）模拟，对键盘/焦点这类真实交互的保真度较低。

---

## 3. 依赖与风险

### 3.1 `engine/go.mod`（13 行）——直接依赖 **3** 个

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| `github.com/Microsoft/go-winio` | v0.6.2 | Windows 命名管道 / 句柄传递，引擎与桌面 Core 服务通信 |
| `golang.org/x/net` | v0.57.0 | HTTP/2、DNS、SOCKS 等网络原语 |
| `golang.org/x/sys` | v0.47.0 | Win32 syscall 绑定（WFP、路由、进程） |

- `go 1.26.0` + `toolchain go1.26.6`（`engine/go.mod:3-4`）。
- 间接依赖仅 1 个：`golang.org/x/text v0.40.0`。
- 这是一个**非常克制**的清单，符合引擎"低依赖、可静态发布"的定位。

### 3.2 `desktop/go.mod`（30 行）——直接依赖 **6** 个

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| `github.com/Microsoft/go-winio` | v0.6.2 | 命名管道客户端（连接 Core 服务） |
| `github.com/pion/stun/v3` | v3.1.6 | NAT 类型检测的 STUN 交互 |
| `github.com/tc-hib/winres` | v0.3.1 | 生成 Windows PE 资源与清单（版本信息注入） |
| `github.com/wailsapp/wails/v3` | v3.0.0-alpha2.119 | 桌面框架（Wails v3，Alpha） |
| `golang.org/x/net` | v0.56.0 | 网络原语 |
| `golang.org/x/sys` | v0.47.0 | Win32 syscall 绑定 |

间接依赖 15 个：

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| `github.com/adrg/xdg` | v0.5.3 | XDG 目录解析（跨平台路径） |
| `github.com/coder/websocket` | v1.8.14 | Wails 资产/事件 WebSocket |
| `github.com/go-ole/go-ole` | v1.3.0 | COM/自动化接口 |
| `github.com/godbus/dbus/v5` | v5.2.2 | Linux 桌面通知（跨平台构建残留） |
| `github.com/jchv/go-winloader` | v0.0.0-20250406163304-c1995be93bd1 | WebView2 运行库加载 |
| `github.com/mattn/go-colorable` | v0.1.14 | 控制台颜色（Windows） |
| `github.com/mattn/go-isatty` | v0.0.20 | TTY 判定 |
| `github.com/nfnt/resize` | v0.0.0-20180221191011-83c6a9932646 | 图标缩放（**2018 年伪版本，已无维护**） |
| `github.com/pion/dtls/v3` | v3.1.4 | STUN 链路上的 DTLS |
| `github.com/pion/logging` | v0.2.4 | pion 日志 |
| `github.com/pion/transport/v4` | v4.0.2 | pion 传输抽象 |
| `github.com/wlynxg/anet` | v0.0.5 | 网卡枚举辅助 |
| `golang.org/x/crypto` | v0.53.0 | 加密原语 |
| `golang.org/x/image` | v0.45.0 | 图像处理（托盘/皮肤） |
| `golang.org/x/text` | v0.41.0 | 文本编码 |

`go 1.25.0` 且**没有 `toolchain` 行**（`desktop/go.mod:3`）。

### 3.3 依赖风险点

1. **跨模块版本偏斜（已在仓库内实际存在）**
   - `engine/go.mod:7` 要求 `golang.org/x/net v0.57.0`、`:10` 间接 `x/text v0.40.0`；
   - `desktop/go.mod:11` 要求 `golang.org/x/net v0.56.0`、间接 `x/text v0.41.0`。
   - 后果：同一安全公告需要**双份、分别**升级；`x/text` 在 desktop 更高、在 engine 更低，说明升级是各升各的，没有统一策略。
2. **桌面模块完全没有漏洞扫描**。`govulncheck@v1.6.0` 只出现在 `.github/workflows/go-engine.yml:59-62`（工作目录 `engine`）。桌面侧持有 Wails Alpha、`coder/websocket`、`pion/*`、`go-ole` 这些攻击面更大的依赖，却没有任何自动漏洞门。
3. **Wails 为 Alpha 且是框架级依赖**：`desktop/go.mod:9` `v3.0.0-alpha2.119`，`.github/workflows/build.yml:26` `WAILS_VERSION=v3.0.0-alpha2.119` 与之对齐（这点做得好：`docs/migration/wails-architecture.md:434` 明确要求 pin 精确 Alpha 版本）。但 Alpha 框架意味着升级即破坏性变更，且无 LTS 承诺。
4. **`nfnt/resize`（2018 年伪版本）**：`desktop/go.mod` 间接依赖，上游已停更，属于纯技术债（用于图标缩放，替换成本低）。
5. **两模块 Go 版本不一致**：engine `go 1.26.0` + `toolchain go1.26.6`；desktop `go 1.25.0` 无 toolchain。CI 通过 `go-version-file: engine/go.mod` 拿到 1.26（`.github/workflows/build.yml:57-59`）后用同一工具链构建 desktop，所以**CI 是 1.26 编译 1.25 模块**；开发者本地若只有 1.25，构建 desktop 可以成功但语义/标准库行为与 CI 不同。
6. **仓库无依赖更新自动化**：`.github/` 下只有 `release-notes/` 与 `workflows/`（枚举确认），**没有 `dependabot.yml` 或 Renovate 配置**。

### 3.4 前端 `desktop/frontend/package.json`（38 行）

`dependencies` **11** 个：

| 依赖 | 版本 | 备注 |
| --- | --- | --- |
| `@fluentui/react-components` | **9.74.4** | 精确锁定；与 README 徽章"Fluent UI"一致 |
| `@fluentui/react-icons` | **2.0.334** | 精确锁定 |
| `@wailsio/runtime` | **3.0.0-alpha2.119** | 精确锁定，与 Go 侧 Wails 版本对齐 |
| `fflate` | **0.8.3** | 精确锁定；皮肤 ZIP 解压 |
| `pixi-live2d-display` | **0.4.0** | 精确锁定；**仅支持 Pixi v6** |
| `pixi.js` | **6.5.10** | 精确锁定；Pixi 主线已到 v8 |
| `qrcode.react` | **4.2.0** | 精确锁定；Wi-Fi 二维码 |
| `react` | ^18.2.0 | caret |
| `react-dom` | ^18.2.0 | caret |
| `react-markdown` | ^10.1.0 | caret；AI 回复渲染 |
| `remark-gfm` | ^4.0.1 | caret |

`devDependencies` **10** 个：

| 依赖 | 版本 | 备注 |
| --- | --- | --- |
| `@testing-library/react` | ^16.3.2 | 组件测试 |
| `@types/node` | 22.20.4 | 与 CI `node-version: 22`（`.github/workflows/build.yml:67`）一致 |
| `@types/react` | ^18.2.43 | |
| `@types/react-dom` | ^18.2.17 | |
| `@vitejs/plugin-react` | ^6.0.0 | |
| `fake-indexeddb` | 6.0.0 | 精确锁定 |
| `jsdom` | ^30.0.1 | `// @vitest-environment jsdom` 依赖 |
| `typescript` | ^5.2.2 | |
| `vite` | ^8.0.5 | 较新主版本 |
| `vitest` | ^4.1.11 | 较新主版本 |

风险点：

1. **缺测试基础设施依赖**：无 `@testing-library/jest-dom`、无 `@testing-library/user-event`。这直接导致 §1.5 中 `toBeInTheDocument` 零命中、断言只能用 `not.toBeNull()`——**这是可低成本修复的明确缺陷**。
2. **无 lint / format 工具**：`package.json` 中零命中 `eslint|prettier|biome|lint`，CI 也没有 lint 步骤 → 前端没有任何静态质量门（唯一门是 `pnpm build` 内的 `tsc`，见 `desktop/frontend/package.json:9`）。
3. **无 `engines` / `packageManager` 字段**（零命中）→ Node/pnpm 版本只靠 CI 变量（`.github/workflows/build.yml:27` `PNPM_VERSION=10.34.5`、`:67` node 22）约束，本地开发无强制。
4. **锁文件与文档不一致**：目录内只有 `pnpm-lock.yaml`（261,423 B）与 `pnpm-workspace.yaml`，**没有 `package-lock.json`**；但 `docs/AI_ASSISTANT.md:80-81` 给出的开发命令是 `npm --prefix desktop/frontend test` 与 `npm run build`。用 npm 会绕过 pnpm 锁文件，产出与 CI 不同。
5. **Pixi 生态锁定**：`pixi.js` 6.5.10 + `pixi-live2d-display` 0.4.0 是硬绑定组合（后者仅支持 Pixi v6），升级到 Pixi v8 需要换库，属于长期不可升级点。
6. **`vite ^8` + `vitest ^4` + `jsdom ^30` 属于较激进的主版本组合**，caret 允许 minor 升级，但三者耦合（vitest 与 vite 版本兼容性）会带来偶发破坏。

---

## 4. 文档漂移抽查（5 篇 × 2–3 条可验证声明）

### 4.1 `docs/architecture/adaptive-scheduling-implementation.md`（198 行）

| # | 文档声明 | 代码证据 | 判定 |
| --- | --- | --- | --- |
| 1 | `docs/architecture/adaptive-scheduling-implementation.md:7` 有界份额控制器位于 `adaptive_allocation.go` | `engine/internal/proxy/adaptive_allocation.go` 存在 | **一致** |
| 2 | 目标份额公式 `0.5/N + 0.5*rate_i/sum(rate)`（`:13`） | `engine/internal/proxy/adaptive_allocation.go:138` `target[i] = 0.5/float64(len(a.keys)) + 0.5*links[key].rate/observed` | **一致** |
| 3 | 每窗口份额变化 ≤5 个百分点（`:13`） | `engine/internal/proxy/adaptive_allocation.go:147` `step := math.Min(1, 0.05/maxChange)` | **一致** |
| 4 | 遥测状态枚举含 `warming-up / balanced / insufficient-demand / adapting / throughput-guard`（`:15`） | `engine/internal/proxy/adaptive_allocation.go:26,107`（`warming-up`）、`:85,94,116`（`throughput-guard`）、`:87`（`insufficient-demand`）命中；`balanced` / `adapting` 在该文件未命中 | **部分一致**：3/5 可直接核实，`balanced`/`adapting` **未验证**（可能在测试或其它文件拼装） |
| 5 | 该文引用的 8 个实现文件（`:70-83` 表格）全部存在 | `engine/internal/proxy/{scheduler,dial,registry,server,health,udp,scheduling}.go`、`engine/internal/server/scheduling.go`、`desktop/frontend/src/components/home/schedulingStrategies.ts` 均存在 | **一致** |
| 6 | `:141-142` 的 `performance.go` / `lease.go` 与 `Acquire/lease.Attach/Fail/Finish` 接口 | 两文件**不存在**（独立 worker 实现在 `adaptive.go` / `adaptive_allocation.go`） | **一致但不友好**：文档已自述为"草案、不是现有 API"，判定不构成错误，但同页混排"已实现"与"草案"易误读 |

结论：**该文件整体与代码吻合度最高**，是"实现说明类文档"的正面样本。

### 4.2 `docs/architecture/managed-tun-lifecycle-migration.md`（121 行）

| # | 文档声明 | 代码证据 | 判定 |
| --- | --- | --- | --- |
| 1 | 自称历史记录、托管 Go 生命周期已生产化（`:3`、`:5`） | 与仓库现状相符（存在 `engine/internal/tun/` 及 4 个测试文件） | **一致** |
| 2 | 以 **Qt** 作为事务协调者、**Python** 路径作为 Phase-12 回滚（`:11-18`、`:30-31`） | 全仓 `.go/.ts/.tsx/.json/.cs` 中 `Qt|PySide|PyQt|WPF` **零命中**；全仓唯一 `.py` 文件是 `desktop/scripts/create-layered-skin.py` | **不一致（历史残留）**：叙述的是已删除的组件，仅靠 `:3` 一句带过 |
| 3 | 环境变量 `HYPOMUX_NETWORK_BACKEND=python\|go\|auto` 选择后端（`:113-118`） | 该字符串**只出现在 docs**（4 篇文档共 6 处），**零源码引用** | **明确漂移**：文档描述了一个不存在的开关 |
| 4 | 协议 v1 新增 `tun.activate/status/deactivate`、`tun.state_changed`、`log.record`、错误码 `tun_failed`（`:95-101`） | `protocol/v1/manifest.json:58`（activate）、`:64`（status）、`:70`（deactivate）、`:131`（state_changed）、`:136`（log.record）、`:157`（tun_failed） | **一致** |
| 5 | 同名 feature `managed_tun_lifecycle` | 仅存在于 `engine/internal/api/v1/types.go:122`（及 `engine/internal/server/server_test.go:96`、`protocol/v1/README.md:78`、`protocol/v1/fixtures/messages.json:76`），**`protocol/v1/manifest.json` 中不存在** | **轻微不一致**：feature 名的"真源"与协议清单不同源 |

结论：**典型的历史迁移文档**——核心机制描述可信，但保留了已删除技术栈与从未存在的环境变量，读者若据此排障会被误导。

### 4.3 `docs/migration/wails-architecture.md`（487 行）

| # | 文档声明 | 代码证据 | 判定 |
| --- | --- | --- | --- |
| 1 | 技术栈 = Wails v3 + React + TypeScript + Vite + Fluent UI React v9（`:3`） | `desktop/go.mod:9`（wails v3）、`desktop/frontend/package.json`（React 18 + `@fluentui/react-components` 9.74.4 + Vite） | **一致** |
| 2 | 界面永不常驻管理员、以 `asInvoker` 启动（`:12`、`:52`、`:89`） | `desktop/build/windows/wails.exe.manifest:18` `<requestedExecutionLevel level="asInvoker" uiAccess="false"/>` | **一致** |
| 3 | 必须 pin 精确 Alpha 版本而非浮动 `latest`（`:434`） | `desktop/go.mod:9` `v3.0.0-alpha2.119`；`.github/workflows/build.yml:26` `WAILS_VERSION=v3.0.0-alpha2.119` | **一致（落地到位）** |
| 4 | 旧 WPF 与旧 Python 前端已在完整迁移后从仓库移除（`:286`） | 全仓零 `WPF`/`Qt`/Python 前端残留 | **一致** |
| 5 | 固定管道路径 `\\.\pipe\HypoMux-Core-Service`（`:53`） | 未逐字比对桌面侧常量 | **未验证** |
| 6 | `desktop/cmd/hypomux-desktop` + `internal/app|coreclient|settings|platform/wails|model` 目录布局（`:244-284`） | 实际存在 `desktop/internal/platform/wails`、`desktop/internal/services`、`desktop/internal/engineclient`；文档是"建议目录"（`:244` 明示） | **不构成漂移**（自述为建议），但与现状命名不同，易被误当成事实 |

结论：**架构级声明基本可信**，唯一需要注意的是"建议目录"章节与现状命名不一致（`coreclient` vs 实际 `engineclient`）。

### 4.4 `docs/steam-cdn-v3-plan.md`（199 行）

| # | 文档声明 | 代码证据 | 判定 |
| --- | --- | --- | --- |
| 1 | 实现由 `steam_observer.go` / `steam_observed_probe.go` / `steam_http_probe.go` / `steam_cdn.go` 承担（`:162`） | 四个文件全部存在于 `engine/internal/proxy/` | **一致** |
| 2 | 全局最多 64 个观察者（`:149`） | `engine/internal/proxy/steam_observer.go:220` `if session.cdnGeneration != c.generation \|\| c.observers >= 64` | **一致** |
| 3 | 每连接 ≤16 未决请求（`:149`） | 未逐字核实该常量 | **未验证** |
| 4 | 验收记录"前端 25 文件 / 105 项测试"（`:164`） | 当前前端测试文件 **56** 个（Go 侧 149） | **不一致（陈旧数字）**：至少落后一个数量级的发展 |
| 5 | 同期指出的本机 IPv6 环境失败（`:164` 括注） | 现已改为优雅跳过：`engine/internal/proxy/ipv6_loopback_test.go:16-56`、`server_test.go:701`、`udp_test.go:510` | **一致（且已改善）** |

结论：**实现描述准确，验收数字严重陈旧**。这类"写进文档的统计快照"如果没有校验机制，会持续腐化。

### 4.5 `docs/AI_ASSISTANT.md`（122 行）

| # | 文档声明 | 代码证据 | 判定 |
| --- | --- | --- | --- |
| 1 | 外部 MCP 地址 `http://127.0.0.1:17863/mcp`（`:52`） | `desktop/frontend/src/components/ai/AIAssistant.tsx:60` `useState("17863")`；`desktop/internal/services/ai_mcp.go:55` `net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))` | **一致** |
| 2 | 协商 MCP 2025-03-26 / 2025-06-18 / 2025-11-25（`:59`） | `desktop/internal/services/ai_mcp.go:110`、`:179-180`（仅回显 03-26/06-18，否则落到 11-25） | **一致** |
| 3 | 最多 2 个并发 MCP 请求、调用超时 3 分钟（`:63`） | `desktop/internal/services/ai_mcp.go:60` `slots: make(chan struct{}, 2)`；`:208` `context.WithTimeout(r.Context(), 3*time.Minute)` | **一致** |
| 4 | API Key 与本机历史用当前用户 DPAPI 加密，存于 `ai/provider.bin`、`ai/history.bin`（`:70`） | `desktop/internal/services/ai_config.go:127,137`（provider.bin）、`ai_service.go:65,116`（history.bin）、`desktop/internal/services/ai_secret_windows.go`（DPAPI） | **一致** |
| 5 | 连接查询最多 100 条、规则查询最多 200 条（`:37`） | `desktop/internal/services/ai_tools.go:315`（100）、`:302-305`（200 + `truncated`） | **一致** |
| 6 | 诊断摘要含最近会话最多 12 条错误摘要（`:38`） | `desktop/internal/services/ai_tools.go:341`（`最多 12 条错误摘要`） | **一致** |
| 7 | 后端位于 `desktop/internal/services/ai_*.go`、前端 `components/ai/`、经 `platform/ai.ts` 调用（`:76`） | 13 个 `ai_*.go` 存在；`desktop/frontend/src/platform/ai.ts` 存在 | **一致** |
| 8 | 开发命令用 `npm --prefix desktop/frontend test` / `npm run build`（`:80-81`） | 仓库唯一锁文件是 `desktop/frontend/pnpm-lock.yaml`；CI 用 `pnpm`（`.github/workflows/build.yml:144`）、"pnpm 10"（`README.md:193`） | **不一致**：与仓库包管理器约定冲突 |
| 9 | 皮肤规范与示例包位于 `desktop/frontend/public/skins/SKIN_SPEC.md`、`mux-starter.muxskin`（`:27`） | 两文件均存在 | **一致** |
| 10 | 最近 100 条记录、每条约 16 KiB（`:71`） | 未逐字核实常量 | **未验证** |

结论：**这篇文档与代码一致度出乎意料地高**（10 条中 8 条完全一致、2 条未验证），唯一缺陷是包管理器命令（唯一一处"文档教会了错误流程"）。

### 4.6 漂移共性

- **陈旧统计数字是最高频漂移类型**：`docs/steam-cdn-v3-plan.md:164`（25 文件/105 测试）与 `docs/architecture/adaptive-scheduling-implementation.md:57`（32 文件/159 测试）、`:22`（17 项首页测试）都属于同一个问题——文档把当时的测试快照硬编码进正文。当前实测是 56 个前端测试文件、149 个 Go 测试文件。
- **历史迁移文档缺少"已删除组件"显式标注**（Qt/Python：`docs/architecture/managed-tun-lifecycle-migration.md:11-18`）。
- **文档中出现零引用的开关**（`HYPOMUX_NETWORK_BACKEND`：4 篇文档 6 处，源码 0 处）。
- **无任何 doc↔code 校验**：`.github/workflows/` 三个文件中零命中文档链接检查、零命中示例命令执行。

### 4.7 文档全景与本节点的抽查覆盖度

`docs/` 下共 **49** 篇 Markdown（不含 `.github/release-notes/` 的 10 篇发布说明）：

| 分区 | 篇数 | 本次抽查 |
| --- | --- | --- |
| `docs/`（根） | 15 | 1 篇（`docs/AI_ASSISTANT.md`） |
| `docs/architecture/` | 12 | 2 篇（`adaptive-scheduling-implementation.md`、`managed-tun-lifecycle-migration.md`） |
| `docs/migration/` | 5 | 1 篇（`wails-architecture.md`） |
| `docs/validation/` | 17（含 `validation/wails/` 7 篇） | 0 篇（另有 `docs/validation/engine/phase14/` 下 29 个 JSON 证据文件未逐一解析） |
| `steam-cdn-*` 与 `steam-*` 计划类 | 6 | 1 篇（`steam-cdn-v3-plan.md`） |

本节点**只深挖了 5 篇**（覆盖率约 10%），因此 §4 的判定是"抽样结论"而非全量审计。已读 5 篇的行数：`docs/architecture/adaptive-scheduling-implementation.md` 198、`docs/architecture/managed-tun-lifecycle-migration.md` 121、`docs/migration/wails-architecture.md` 487、`docs/steam-cdn-v3-plan.md` 199、`docs/AI_ASSISTANT.md` 122。

给主理人的提示：`docs/validation/`（17 篇）与 `steam-cdn*` 计划类（6 篇）**未做漂移核对**，而它们正是硬编码统计数字最可能出现的地方（本次已在 `steam-cdn-v3-plan.md:164` 中证实一次）。若需要更完整的文档漂移结论，建议追加一次针对 `docs/validation/**` 的专项抽查。


---

## 5. README 一致性核对

`README.md` 315 行、`README_EN.md` 307 行，逐条核对架构声明：

| # | README 声明 | 代码/清单证据 | 判定 |
| --- | --- | --- | --- |
| 1 | 版本徽章 2.7.0（`README.md:9`、`README_EN.md:9`） | `desktop/frontend/package.json:4` `"version": "2.7.0"`；`.github/release-notes/v2.7.0.md` 与 `v2.7.0.en.md` 均存在 | **一致** |
| 2 | 栈 = Go + Wails v3 + React + Fluent UI（`README.md:11-12`、`:33`） | `desktop/go.mod:9`（wails v3.0.0-alpha2.119）；`desktop/frontend/package.json`（react ^18.2.0、`@fluentui/react-components` 9.74.4） | **一致** |
| 3 | 2.5.0 完成从 Python/Qt 与过渡期 WPF 的迁移，`移除旧版 Python、Qt、asyncio 与 .NET/WPF 运行时依赖`（`README.md:31`、`:33`、`:35`；`README_EN.md:29`、`:31`、`:33`） | 全仓零 `Qt|PySide|PyQt|WPF` 源码引用；唯一 `.py` 为 `desktop/scripts/create-layered-skin.py`（构建期工具，非运行时） | **一致** |
| 4 | 最小权限：`界面不以管理员身份常驻`，高权限操作交给独立 Core 服务（`README.md:94`、`:33`） | `desktop/build/windows/wails.exe.manifest:18` `level="asInvoker" uiAccess="false"` | **一致** |
| 5 | 端口：系统代理 HTTP/HTTPS 10801 · SOCKS5 10800（`README.md:141`；`README_EN.md:139`） | `engine/internal/proxy/config.go:14` `DefaultSOCKSPort = 10800`、`:15` `DefaultHTTPPort = 10801`；`desktop/internal/services/settings.go:68-69`；`desktop/frontend/src/state/useEngineState.ts:173`；`desktop/frontend/src/pages/SettingsPage.tsx:51-52` | **一致（五处同源）** |
| 6 | 支持平台：Windows 10 / 11（`README.md:13`；`README_EN.md:13`） | 产品定位为 Windows；代码有 `*_other.go` / `!windows` 分支（41 处）但无其它平台发行物声明 | **一致** |
| 7 | 推荐环境：Go 1.26、Node.js 22、pnpm 10、Wails v3 CLI `v3.0.0-alpha2.119`（`README.md:193`；`README_EN.md:191`） | `engine/go.mod:3` `go 1.26.0`；`.github/workflows/build.yml:67` `node-version: 22`；`:27` `PNPM_VERSION=10.34.5`；`:26` `WAILS_VERSION=v3.0.0-alpha2.119` | **一致** |
| 8 | （同上，Go 版本细节） | `desktop/go.mod:3` `go 1.25.0`，**桌面模块最低要求是 1.25 而非 1.26** | **轻微不一致**：README 只给单一"Go 1.26"，未反映双模块最低版本差异 |
| 9 | 随附 sing-box **1.14.2**（`README.md:195`、`:27`；`README_EN.md:193`、`:25`） | `bin/README.md:5` `Version: **1.14.2** (Windows amd64, official SagerNet build)`，含 SHA-256 与上游 revision（`:8-10`） | **一致（且有校验）** |
| 10 | `bin/` 必须包含 `sing-box.exe`、`wintun.dll`、`libcronet.dll`（`README.md:193`） | `bin/` 实际包含 `sing-box.exe`、`wintun.dll`、`libcronet.dll` 与 `README.md` | **一致** |
| 11 | 更新通道用 Ed25519 校验、安装包再验大小/SHA-256/Authenticode（`README.md:53`；`README_EN.md:51`） | `.github/workflows/release-smoke.yml:37-50`（Ed25519 公钥比对）、`:106-144`（signed update channel 字节一致 + 签名）、`.github/workflows/build.yml:301`（真实签名安装包验签用例） | **一致** |
| 12 | v2.7.0 更新日志路径（`README.md:29`） | `.github/release-notes/v2.7.0.md` 存在；`docs/RELEASE_VERSIONING.md` 存在（57 行） | **一致** |
| 13 | 2.5.3 兼容旁路段（`README.md:127` ↔ `README_EN.md:125`） | 中英一致 | **一致** |

**README 层面未发现实质性架构漂移。** 只有两处轻微问题：Go 版本表述未覆盖双模块差异（第 8 条），以及 README 全文未说明 `engine/` 与 `desktop/` 是两个独立 Go module（这会导致贡献者误用一个 `go test ./...`）。

---

## 6. 长期技术债清单（按"影响交付速度 / 稳定性 / 安全"排序）

| # | 技术债 | 影响面 | 证据 | 建议动作 |
| --- | --- | --- | --- | --- |
| 1 | 桌面模块无任何漏洞扫描，却持有 Wails Alpha、`coder/websocket`、`pion/*`、`go-ole` | 安全 | `.github/workflows/go-engine.yml:59-62` 只扫 `engine`；`desktop/go.mod` 全部依赖未扫 | 在 `build.yml` 增加 `govulncheck ./...`（工作目录 `desktop`），与 engine 同级 |
| 2 | 真实链路端到端测试在 CI 中**永久跳过**（引擎握手 + 网络启停） | 稳定性 | `desktop/internal/engineclient/client_test.go:11-15`；`desktop/internal/services/engine_integration_test.go:11,22`；`:300` 之前无 `go build`（`.github/workflows/build.yml:159-166` 早于 `:188`） | CI 中先 `go -C engine build -o hypomux-engine.exe ./cmd/hypomux-engine` 再跑测试；至少让 `client_test.go` 的握手用例真实执行 |
| 3 | 4 个 `*_integration_test.go` 靠 env + 文件名隔离，全仓零 `integration` 构建标签 | 速度 + 稳定性 | `desktop/internal/services/{engine_integration_test.go,diagnostics_windows_integration_test.go,tun_preflight_windows_integration_test.go}`、`desktop/internal/engineclient/service_windows_integration_test.go`；`//go:build .*integration` 零命中 | 加 `//go:build integration` 标签并把"带标签跑一遍"做成一个夜间/手动 job，避免集成用例伪装成普通测试 |
| 4 | 前端无 lint / format / 静态门，且缺 jest-dom、user-event | 速度 + 质量 | `desktop/frontend/package.json` 零 `eslint|prettier|lint`；`toBeInTheDocument` 零命中；CI 仅 `vitest`（`.github/workflows/build.yml:144`） | 引入 eslint + prettier 并加 CI 步骤；补 `@testing-library/jest-dom` 与 `user-event`（低风险高收益） |
| 5 | 文档硬编码统计数字持续腐化，且无 doc↔code 校验 | 速度（信任成本） | `docs/steam-cdn-v3-plan.md:164`（25 文件/105 测试）、`docs/architecture/adaptive-scheduling-implementation.md:57`（32 文件/159 测试）vs 实测 56/149；`.github/workflows/` 零文档校验 | 统计数字改为"当时快照 + 提交号"并加 CI 校验（至少校验文档中的相对链接与代码路径存在性） |
| 6 | `x/net`、`x/text` 在 engine 与 desktop 间版本偏斜 | 安全 + 维护 | `engine/go.mod:7,10`（x/net v0.57.0、x/text v0.40.0）vs `desktop/go.mod:11` 及间接（x/net v0.56.0、x/text v0.41.0） | 统一两模块的间接依赖版本（例如在 desktop 显式提级到 v0.57.0），并加 CI 一致性检查 |
| 7 | 两模块 Go 版本与 toolchain 声明不一致 | 速度 + 稳定性 | `engine/go.mod:3-4`（1.26.0 + toolchain 1.26.6）vs `desktop/go.mod:3`（1.25.0，无 toolchain） | 统一 `go` 指令与 toolchain 行，或明确在 README 说明"仅 engine 决定工具链" |
| 8 | 仓库无依赖更新自动化 | 安全 | `.github/` 仅含 `release-notes/` 与 `workflows/`，无 `dependabot.yml`/Renovate | 启用 Dependabot（gomod ×2 + npm），对 Wails/pion 设人工审核策略 |
| 9 | 前端包管理器约定冲突（文档教 npm，仓库用 pnpm） | 速度 | `docs/AI_ASSISTANT.md:80-81` 用 `npm --prefix`；唯一锁文件 `desktop/frontend/pnpm-lock.yaml`；`README.md:193` 与 `.github/workflows/build.yml:27` 用 pnpm 10 | 修正文档命令；在 `package.json` 加 `packageManager` + `engines` 固定 pnpm/Node |
| 10 | Pixi 生态锁死（`pixi.js` 6.5.10 + `pixi-live2d-display` 0.4.0）与 `nfnt/resize` 2018 伪版本 | 维护 | `desktop/frontend/package.json`（两者精确锁定）；`desktop/go.mod` 间接 `nfnt/resize v0.0.0-20180221191011-83c6a9932646` | 评估 Live2D 渲染替代方案并记录升级路径；`nfnt/resize` 换成标准库/`x/image/draw` 缩放 |
| 11 | 断言风格使失败定位成本高：686 用例仅 65 处 `t.Run`、995 `t.Fatalf` vs 46 `t.Errorf` | 速度 | 全仓统计（§1.5） | 对多输入场景推广表驱动 + 子测试；把可恢复校验改为 `t.Errorf` 让一次运行暴露多个问题 |
| 12 | 无覆盖率指标、无覆盖率门 | 稳定性 | `.github/workflows/` 零命中 `-cover|codecov|coverage` | 先产出 `go test -cover` 报告（不设门），识别 `desktop/internal/engineclient`、`engine/internal/tun` 等低覆盖包 |

---

## 7. 工程成熟度评估

### 7.1 测试维度：**B**

- **加分**：149 个 Go 测试文件 / 22,931 行测试代码，测试:实现行数 ≈ 0.74:1；零第三方断言库（`testify` 零命中），`httptest` 46 处，真实文件系统 + `t.TempDir()` 为主；存在像 `engine/internal/proxy/adaptive_allocation_test.go:311-336` 这样"带对照组且能自证回归捕获能力"的高质量测试；`desktop/internal/services/singbox_rules_test.go:75,116,136` 覆盖回滚与部分失败，属于成熟度较高的失败路径测试。
- **扣分**：CI 上 10 处关键用例永久 `t.Skip`（§1.4a），真实协议握手零覆盖；`t.Run` 采用率仅 65/686；前端测试量只有 Go 的 1/5（4,469 vs 22,931 行），组件测试依赖整体 mock 服务模块（`desktop/frontend/src/pages/ConnectionsPage.test.tsx:24-34`），Wails 绑定契约无测试；无覆盖率数据。
- **判据**：后端接近 A-，前端 C+，加权为 **B**。

### 7.2 文档维度：**C+**

- **加分**：49 篇结构化文档，`docs/architecture/` 12 篇、`docs/validation/` 17 篇（含 `docs/validation/engine/phase14/` 29 个 JSON 证据文件，保留 `phase14-dev-19e672f` / `phase14-dev-9a591c7` 两个提交级证据集），`docs/migration/wails-architecture.md` 487 行的架构决策文档质量高；`docs/AI_ASSISTANT.md` 10 条抽查中 8 条与代码完全一致；`bin/README.md:8-10` 甚至记录了归档 SHA-256 与上游 revision。
- **扣分**：历史迁移文档保留已删除技术栈（`docs/architecture/managed-tun-lifecycle-migration.md:11-18` 的 Qt/Python）与零引用开关（同文件 `:113-118` `HYPOMUX_NETWORK_BACKEND`）；多处硬编码测试快照陈旧（`docs/steam-cdn-v3-plan.md:164`、`docs/architecture/adaptive-scheduling-implementation.md:57`）；`docs/AI_ASSISTANT.md:80-81` 的包管理器命令与仓库约定冲突；无 doc↔code 校验。
- **判据**：**C+**（数量与结构达标，一致性维护机制缺位）。

### 7.3 CI 维度：**B**

- **加分**：三个工作流分工清晰；`go mod verify` + `go vet` + `gofmt -l`（`.github/workflows/build.yml:152-186`、`.github/workflows/go-engine.yml:40-56`）构成完整基础门；engine 侧有 `govulncheck`（`.github/workflows/go-engine.yml:59-62`）；`release-smoke.yml` 覆盖签名密钥一致性、tag 镜像一致、更新通道字节一致——**发布信任链的自动化程度高于一般中小项目**；NSIS 在测试前安装（`.github/workflows/build.yml:117-133`），使安装器目录解析用例真实执行；生产模式只放行一个真实签名安装包验签用例（`:296-301`），意图明确。
- **扣分**：桌面模块无漏洞扫描；无前端 lint；无覆盖率；无跨平台/arm64 构建；集成测试长期空转（§1.4a）；`build.yml:159` 与 `go-engine.yml:54` 重复跑 engine 测试（成本冗余）。
- **判据**：**B**。

### 7.4 依赖维度：**B-**

- **加分**：直接依赖极度克制（engine 3 / desktop 6），无臃肿传递树；关键版本三方对齐（Wails Go 侧 `desktop/go.mod:9` ↔ 前端 `@wailsio/runtime 3.0.0-alpha2.119` ↔ CI `WAILS_VERSION`，`.github/workflows/build.yml:26`）；`bin/` 运行时资产有版本 + SHA-256 记录（`bin/README.md:5-10`）；精确锁定高风险 Alpha 依赖。
- **扣分**：Wails Alpha、`pixi.js` v6 锁死、`nfnt/resize` 2018 伪版本、engine/desktop 版本偏斜、无 Dependabot、前端无 lint、缺测试基础设施依赖。
- **判据**：**B-**。

### 7.5 总体与优先行动

**总体：B。** 这是一个"后端与发布链路投入明显高于前端与依赖治理"的项目。工程基础（格式化、vet、模块校验、跨平台构建标签、发布信任链）扎实；主要风险集中在两类：

1. **"CI 绿 ≠ 可用"**（§1.4a 的 10 处永久跳过，尤其是 `desktop/internal/engineclient/client_test.go:11` 的真实协议握手）——这是唯一的**高优先级**项。
2. **前端与依赖治理**（无 lint、无 jest-dom/user-event、无覆盖率、无 Dependabot、桌面无漏洞扫描）——**中优先级、低修复成本**。

建议次序：债 #2 → #1 → #4 → #3 → #5，其余按版本节奏处理。

---

## 8. 未验证项与本次分析的限制

1. **没有 Go 工具链**：未编译、未运行任何 Go 测试，未运行 `go vet`、`govulncheck`、`gofmt`。所有"会执行/会跳过"的结论均由代码条件与 CI 配置静态推断。
2. **未安装 node_modules**（`desktop/frontend/node_modules` 不存在），未运行 `vitest`；前端断言统计来自源码文本匹配，非运行结果。
3. **未联网**：`pnpm-lock.yaml` 未解析，因此**未核实**锁文件内实际解析版本、传递依赖数量、是否存在已公开 CVE；`desktop/go.mod` 各版本是否为最新也未核实。
4. **未逐字核实的代码常量**（已在正文标注"未验证"）：
   - `engine/internal/proxy/adaptive_allocation.go` 中 `balanced` / `adapting` 状态字符串的实际位置；
   - `engine/internal/proxy/steam_observer.go` 中"每连接 ≤16 未决请求"的常量；
   - `docs/migration/wails-architecture.md:53` 的管道路径 `\\.\pipe\HypoMux-Core-Service` 与桌面侧常量的逐字一致性；
   - `docs/AI_ASSISTANT.md:71` 的"100 条 / 每条 16 KiB"历史记录上限常量。
5. **`.github/workflows/create-release-tag.yml` 未展开分析**（仅确认存在，并含 `actions/setup-go@v7` + `go-version-file: desktop/go.mod`，`.github/workflows/create-release-tag.yml:27-29`）。
6. **未评估覆盖率**：没有 `-cover` 数据，也没有基于调用关系的覆盖推断，文中"覆盖/未覆盖"均为"是否存在用例且是否在 CI 中会执行"的口径。
7. 前端测试文件按目录分布见 §1.1 表，来自文件枚举，未逐一阅读内容。
