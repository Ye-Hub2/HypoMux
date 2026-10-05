# 01 — Go 后端编译与测试验收

- 验收人：go-checks
- 日期：2026-10-04
- HEAD：`fc821b16bd55630ac6cba4311179e1a1b6644ded`
- 范围：`desktop/` Go 模块（工作区未提交改动，58 个已跟踪文件变更）
- 工具链：`go version go1.27.0 windows/amd64`（`desktop/go.mod:3` 声明 `go 1.25.0`）
- 本轮**只读审计**：未修改任何源码，未执行 git add/commit/reset/checkout。

---

## 验收结论

**通过。** `go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`go test -race` 全部退出码 0，570 个断言事件全绿、0 失败；`go.mod`/`go.sum` 依赖无残留（归一化行尾后与 `go mod tidy` 结果逐字节一致）。存在 1 个中风险卫生问题（Windows 克隆上行尾导致的 `go mod tidy` 假阳性），不影响本轮验收结论。

---

## 阻塞问题

**无。** 本轮范围内未发现阻塞项。

---

## 风险与建议

### R1（中）`go mod tidy` 在 Windows 克隆上把 `go.mod`/`go.sum` 从 CRLF 改写成 LF，产生全文件假 diff

- 位置：`desktop/go.mod:1`、`desktop/go.sum:1`、`.gitattributes:1`
- 复现（在 `desktop/` 下）：

  ```powershell
  $env:Path="C:\Program Files\Go\bin;"+$env:Path
  go mod tidy -diff
  ```

- 退出码：`1`
- 实际输出原文（开头与结尾，中间为同样形态的 59 行）：

  ```
  diff current/go.sum tidy/go.sum
  --- current/go.sum
  +++ tidy/go.sum
  @@ -1,59 +1,59 @@
  -github.com/Microsoft/go-winio v0.6.2 h1:F2VQgta7ecxGYO8k3ZZz3RS8fVIXVxONVUPlNERoyfY=
  -github.com/Microsoft/go-winio v0.6.2/go.mod h1:yd8OoFMLzJbo9gZq8j5qaps8bJ9aShtEA8Ipt1oGCvU=
  -github.com/adrg/xdg v0.5.3 h1:xRnxJXne7+oWDatRhR1JLnvuccuIeCoBu2rtuLqQB78=
  -github.com/adrg/xdg v0.5.3/go.mod h1:nlTsY+NNiCBGCK2tpm09vRqfVzrc2fLmXGpBLF0zlTQ=
  ...（中间 51 行同形态省略）
  -golang.org/x/text v0.41.0/go.mod h1:jvf1O8ajNzZqhSrQBPbutR/EB83Cc0CFrezNQIwbb5M=
  -gopkg.in/yaml.v3 v3.0.1 h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA=
  -gopkg.in/yaml.v3 v3.0.1/go.mod h1:K4uyk7z7BCEPqu6E+C64Yfv1cQ7kz7rIZviUmN+EgEM=
  +github.com/Microsoft/go-winio v0.6.2 h1:F2VQgta7ecxGYO8k3ZZz3RS8fVIXVxONVUPlNERoyfY=
  +github.com/Microsoft/go-winio v0.6.2/go.mod h1:yd8OoFMLzJbo9gZq8j5qaps8bJ9aShtEA8Ipt1oGCvU=
  +github.com/adrg/xdg v0.5.3 h1:xRnxJXne7+oWDatRhR1JLnvuccuIeCoBu2rtuLqQB78=
  +github.com/adrg/xdg v0.5.3/go.mod h1:nlTsY+NNiCBGCK2tpm09vRqfVzrc2fLmXGpBLF0zlTQ=
  ...（中间 51 行同形态省略）
  +golang.org/x/text v0.41.0/go.mod h1:jvf1O8ajNzZqhSrQBPbutR/EB83Cc0CFrezNQIwbb5M=
  +gopkg.in/yaml.v3 v3.0.1 h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA=
  +gopkg.in/yaml.v3 v3.0.1/go.mod h1:K4uyk7z7BCEPqu6E+C64Yfv1cQ7kz7rIZviUmN+EgEM=
  ```

  共 59 删 59 增：**删除侧与新增侧逐行内容完全一致，只有行尾不同**（工作区 CRLF vs tidy 期望 LF）。完整输出长度 10585 字符。

- **判定：假阳性，不是依赖问题。** 取证：

  ```powershell
  $b=[IO.File]::ReadAllBytes("$PWD\go.mod")   # bytes=1045 LF=30 CRLF=30
  $b=[IO.File]::ReadAllBytes("$PWD\go.sum")   # bytes=5188 LF=59 CRLF=59
  git config --get core.autocrlf              # true
  git show HEAD:desktop/go.sum                 # 提交态 blob 内 CRLF 计数 = 0
  git diff --numstat -- desktop/go.sum        # 空（工作区与索引在归一化后一致）
  ```

  根因：`.gitattributes:1` 只有 `* text=auto`，**没有**给 `*.mod`/`*.sum` 钉 `eol=lf`；本机 `core.autocrlf=true` 导致检出后变成 CRLF，而 `go mod tidy` 永远写 LF。

  在**临时副本**（`%TEMP%\hypomux-tidy-check`，含全部 193 个 `.go`，已包含 `build/windows/syso/main.go`）里跑 `go mod tidy` → 退出码 0；把两侧 `\r\n` 归一化为 `\n` 后 `Compare-Object` 结果：

  ```
  ########## go.mod ##########
    <NO DRIFT>
  ########## go.sum ##########
    <NO DRIFT>
  ```

  → **没有任何依赖残留**，删除 updater 系列文件后无需 `go mod tidy`。

- 影响与建议：
  - 真实危害是**污染 source control**：`wails3 task`（或任何人手动 `go mod tidy`）在 Windows 上会把 `go.mod`/`go.sum` 改写成 LF，git status 凭空多出约 30 行改动，若误提交就是一次无意义的全文件行尾变更。
  - 建议在 `.gitattributes` 追加：

    ```gitattributes
    go.mod text eol=lf
    go.sum text eol=lf
    ```

  - **本轮非回归**：`git status` 显示 `desktop/go.mod`、`desktop/go.sum` 均未被修改，即该 CRLF 状态与 HEAD 相同，属仓库既有属性，不是这轮改动引入的。

### R2（低）`isVirtualAdapter` 的关键字表没有 `hypomux`，改名后的自有 vNIC 会被当成物理网卡

- 位置：`desktop/internal/services/adapters.go:142-156`
- 现状：判定表依赖 `"vethernet"`/`"hyper-v"`/`"vmware"` 等驱动特征串。HypoMux 自建 vNIC 在 Windows 上默认叫 `vEthernet (HypoMux-vnic-NN)`，能靠 `vethernet` 命中，所以**当前默认路径没问题**。
- 缺口：`adapter_visibility_test.go:10-36` 的分类表里没有任何 `HypoMux-vnic-*` 条目。若用户把自有 vNIC 重命名为普通名字（文件上方 `adapters.go:139-141` 的注释明确承认用户会改 VMware/Hyper-V 网卡名），它会漏判成物理网卡并出现在适配器列表里，进而污染聚合选源与 TUN 路由选择。
- 建议：在 `adapters.go:144` 的 marker 表里加 `"hypomux-vnic"`，并在 `adapter_visibility_test.go` 表中补一条纯名字用例。

### R3（低）新增测试强，可信度高

审计结论（详见「执行记录 · 测试可信度审计」）：**未发现被 Skip 掩盖、`//nolint` 压制、注释掉或断言过弱的新测试**。新增的 78 个 `hyperv_adapter_test.go` 测试函数（+1494/−10 行）以表驱动 + 精确文案比对为主，例如 `hyperv_adapter_test.go:1843-1849` 显式断言 `Error() != ""`，`adapter_visibility_test.go:51-53` 用注释说明了「为何不能断言 false」（默认值翻转会让该断言恒真）。`installer_layout_test.go:336-354` 是一组高质量的**反向守卫**，任何 CI workflow 重新出现 `artifacts/latest.json`、`latest.json.sig`、`UPDATE_MANIFEST_ED25519_PRIVATE_KEY`、`update-manifest-sign`、`update_manifest_ed25519_public_key.txt`、`steps.version.outputs.channel`、`git commit-tree`、`raw.githubusercontent.com`、`-verify-only` 都会让测试直接失败——这正面锁死了「自动更新删除不彻底」的回归。

### R4（信息）`desktop/cover_hyperv` 是工作区里的游离产物

- 位置：`desktop/cover_hyperv`（未跟踪，459303 字节，内容以 `mode:` 开头 = Go 覆盖率 profile）
- 不是本腿产物（本腿未执行任何 `-coverprofile` / `-o`），应为并行验收腿或此前运行残留。提交前请删除或加进 `.gitignore`。

---

## 执行记录

所有命令的工作目录均为 `<repo>\desktop`，且每条都前置 `$env:Path="C:\Program Files\Go\bin;"+$env:Path`。**未使用 `wails3 task`。**

| # | 命令 | 退出码 | 耗时 | 结果 |
|---|---|---|---|---|
| 1 | `go build ./...` | 0 | 1.155 s | 通过，无输出 |
| 2 | `go vet ./...` | 0 | 0.644 s | 通过，无输出 |
| 3 | `go test -count=1 ./...` | 0 | 51.635 s | 通过，9 个包全部 ok |
| 4 | `go test -count=1 -v ./internal/services/ ./internal/releaseversion/ . ./internal/startup/` | 0 | 60.046 s | PASS 536 / FAIL 0 / SKIP 7 |
| 5 | `go test -count=1 -v ./internal/engineclient/ ./internal/platform/ ./internal/platform/wails/ ./build/windows/syso/ ./cmd/release-version/` | 0 | — | PASS 34 / FAIL 0 / SKIP 2 |
| 6 | `go mod tidy -diff` | 1 | 0.238 s | 假阳性，见 R1 |
| 7 | `%TEMP%` 副本内 `go mod tidy` | 0 | — | 归一化后 go.mod/go.sum 均无漂移 |
| 8 | `go test -race -count=1 ./internal/services/ ./internal/releaseversion/ .` | 0 | 92.184 s | 通过，无 DATA RACE |
| 9 | `gofmt -l .`（排除 frontend） | 0 | — | 无输出，全部已格式化 |
| 10 | `GOOS=linux` + `GOOS=darwin` 下 `go build ./internal/services/ ./internal/releaseversion/ ./internal/startup/ ./internal/platform/` | 0 | — | 双平台通过 |
| 11 | `go test -count=1 -v -run 'Hyperv\|HyperV\|VNIC\|Vnic\|Adapter\|adapter' ./internal/services/` | 0 | 1.062 s | PASS 112 / FAIL 0 / SKIP 1 |

### 3. `go test -count=1 ./...` 原始输出

```
ok  	github.com/Hypostasis-Cat/HypoMux/desktop	0.274s
?   	github.com/Hypostasis-Cat/HypoMux/desktop/build/windows/syso	[no test files]
?   	github.com/Hypostasis-Cat/HypoMux/desktop/cmd/release-version	[no test files]
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient	3.599s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform	0.280s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails	0.636s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion	0.387s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/services	50.129s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/startup	0.297s

EXITCODE=0
```

**全量测试统计：PASS 570（含子测试） / FAIL 0 / SKIP 9。**

### 失败测试名

无。（0 个失败。）

### 跳过的 9 个测试（逐个列出，均为环境/显式开关门控，非本轮新增掩盖）

| 测试 | 位置 | 跳过原因 |
|---|---|---|
| `TestRealWindowsAdapterDiagnostic` | `desktop/internal/services/diagnostics_windows_integration_test.go:11` | 需 `HYPOMUX_RUN_DIAGNOSTIC_TEST=1` |
| `TestRealProxyStartStopAndNetworkRestore` | `desktop/internal/services/engine_integration_test.go:9` | 需 `HYPOMUX_RUN_NETWORK_TEST=1` + 真实 `hypomux-engine.exe` |
| `TestMTUWindowsReadOnlySmoke` | `desktop/internal/services/mtu_windows_test.go:14` | 需 `HYPOMUX_MTU_SMOKE_ADAPTER` |
| `TestReadOnlyNetworkRouteSnapshot` | `desktop/internal/services/network_routes_windows_test.go:38` | 需 `HYPOMUX_RUN_TUN_PREFLIGHT_TEST=1` |
| `TestRealWindowsTunPreflightIsReadOnly` | `desktop/internal/services/tun_preflight_windows_integration_test.go:10` | 需 `HYPOMUX_RUN_TUN_PREFLIGHT_TEST=1` |
| `TestInstallerDirectoryResolutionNSIS` | `desktop/installer_directory_windows_test.go:38` | 需 `MAKENSIS` 执行 NSIS 集成 |
| `TestNativeWiFiReadOnly` | `desktop/internal/startup/wifi_windows_test.go:12` | opt-in 原生 WLAN 冒烟 |
| `TestRealEngineHandshakeAndStoppedStatus` | `desktop/internal/engineclient/client_test.go:11` | 无真实 `hypomux-engine.exe` |
| `TestInstalledDesktopExercisesTrustedServiceClientPath` | `desktop/internal/engineclient/service_windows_integration_test.go:14` | 需 `HYPOMUX_RUN_SERVICE_TEST=1` 且已安装当前构建 |

> 其中 3 个属于本轮**被删或被改**的包（`engineclient` 两个、`services` 五个），其余为既有门控。**没有任何一个被本轮改动的 Hyper-V 新测试跳过**——`hyperv_adapter_test.go:756` 的 `t.Skip("当前环境没有网卡")` 是真实环境守卫，本机有网卡，测试实际执行并通过。

### 测试可信度审计

- **`t.Skip` 全量扫描**：全部 `.go` 命中 18 处 Skip 关键字，分布在 15 个文件，逐条核对后**全部**是显式环境变量门控或硬件前置条件，无一用于掩盖本次改动的缺陷。
- **`//nolint` / `lint:ignore` / `TODO` / `FIXME` / `XXX`**：在 `desktop/` 全部 `.go` 中 **零命中**。
- **断言强度**：重点文件 `hyperv_adapter_test.go`（2334 行 / 78 个测试函数）、`adapter_visibility_test.go`（88 行 / 3 个）、`settings_fields_test.go`（113 行 / 4 个）、`installer_layout_test.go`（927 行 / 24 个）、`version_test.go`（90 行 / 3 个）共扫描约 180 处 `err != nil` / `err == nil`，**全部**是两类：① 测试准备阶段的 `if err != nil { t.Fatal(err) }` 前置检查（正确用法）；② 断言阶段且均配有说明性 `t.Fatalf` 文案。无「只断言 err != nil 就算过」的糊弄断言。
- **已删除 updater 符号的测试引用**：全仓 `.go` 扫描 `Updater|updater|UpdateManifest|update_manifest|update-manifest`（忽略大小写）命中 12 处，**无一是编译期符号引用**，全部为：
  - `desktop/internal/releaseversion/version.go:65` —— 注释文字；
  - `desktop/installer_layout_test.go:336-354, 496-500` —— 反向守卫的黑名单字符串（期望 workflow **不含**这些 token）；
  - 其余 7 处是命名巧合（`TestApplyPoolUpdateRemovesDanglingWeight`、`TestRuleSetUpdate*`、`TestSettingsFieldUpdate*` 中的 "Update" 指配置更新）。
- **格式**：`gofmt -l` 干净。
- **`go vet`**：零告警。

---

## 覆盖缺口

1. **真实 Hyper-V 主机路径未验**。`desktop/internal/services/hyperv_adapter_windows.go` 中调用 `powershell.exe` 的实机链路（创建/删除 vNIC、挂载到 VM、`New-VMSwitch`）在本机与 CI 都无法执行——全部 570 个测试覆盖的是解析、账本（ledger）、对账（reconcile）、权重合并、错误归因等纯逻辑。**建议**：至少在带 Hyper-V 的 Windows 机器上手工走一遍新建页面的「创建 → 应用 → 删除」全流程，或加一个 `HYPOMUX_RUN_HYPERV_TEST=1` 门控的真机冒烟。
2. **9 个环境门控测试未执行**（见上表），其中 `TestInstalledDesktopExercisesTrustedServiceClientPath`、`TestRealEngineHandshakeAndStoppedStatus` 覆盖的是引擎与提权服务链路，与本轮改动无直接关系但同属「未跑过」。
3. **非 Windows 的 `go build ./...` 未能验证**。从 Windows 主机交叉编译失败，**失败点在第三方依赖**而非 HypoMux 代码：

   ```
   GOOS=linux  EXITCODE=1
   # github.com/wailsapp/wails/v3/pkg/application
   ...\wails\v3@v3.0.0-alpha2.119\pkg\application\menu_linux.go:7:12: undefined: pointer
   ...\wails\v3@v3.0.0-alpha2.119\pkg\application\webview_window_linux.go:31:16: undefined: pointer
   ...\menuitem_linux.go:12:13: undefined: pointer
   ...\menuitem_linux.go:14:13: undefined: pointer
   ...\webview_window_linux.go:32:16: undefined: pointer
   ...\webview_window_linux.go:33:16: undefined: pointer
   ...\webview_window_linux.go:35:16: undefined: pointer
   ...\webview_window_linux.go:36:16: undefined: pointer
   ...\webview_window_linux.go:37:16: undefined: pointer
   ...\webview_window_linux.go:42:16: undefined: pointer
   ...\webview_window_linux.go:42:16: too many errors

   GOOS=darwin EXITCODE=1
   # package github.com/Hypostasis-Cat/HypoMux/desktop
   	imports github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails
   	imports github.com/wailsapp/wails/v3/pkg/application
   	imports github.com/wailsapp/wails/v3/pkg/mac: build constraints exclude all Go files in ...\wails\v3@v3.0.0-alpha2.119\pkg\mac
   ```

   这是「从 Windows 交叉编译需要目标平台的原生 cgo 工具链」导致的，**不能据此断言 CI 的非 Windows 作业会失败**。好消息是：**HypoMux 自有包（含 `desktop/internal/services/hyperv_adapter_other.go`，`//go:build !windows` 的降级实现）在 `GOOS=linux` 与 `GOOS=darwin` 下均编译通过**（退出码 0），说明本轮对 `hyperv_adapter_other.go` 的改动没有破坏跨平台编译。
4. **前端与 CI/docs 未验**（不属于本腿范围），由对应验收腿覆盖。
5. **本腿未修改任何文件**，验收前后 `git status` 的已跟踪变更集合完全一致（58 项），`desktop/go.mod`、`desktop/go.sum` 保持 clean。