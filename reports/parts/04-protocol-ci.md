# 04 · 协议契约与发布/CI 工程分析（HypoMux）

- 分析对象：仓库 `<repo>`，HEAD `e66016e`（`fix: resolve HTTP forwarding and desktop recovery audit findings`），工作树干净。
- 分析方式：**只读**。用 read/grep/glob + pwsh 只读命令（git log、git ls-files、字节/哈希核对、Select-String）静态取证。
- 环境限制：**本机未安装 Go**，因此 `go test` / `go build` / `gofmt` / `govulncheck` 均**未实际执行**；所有 Go 侧结论来自源码与 workflow 的静态阅读，已尽量用可复核的行号与交叉引用固证。
- 证据规则：每条重要结论标注 `相对路径:行号`。无法核实的项目集中在第 8 节"未验证项"。

---

## 0. 摘要（TL;DR）

1. **`protocol/v1` 契约是"单端门禁"**：唯一的契约消费者是引擎侧 `engine/internal/api/v1/contract_test.go`（读 `manifest.json` 与 `fixtures/messages.json`）。桌面端（Wails/Go）对协议方法名全部使用**散落的字符串字面量**，没有任何编译期或测试期绑定到 manifest，属于真实的一致性缺口。
2. **发布链路设计完善且证据充分**：tag → `build.yml`（Windows 打包 + SignPath 三段签名）→ 生成 `latest.json` → Ed25519 签名（私钥**只在 GitHub Secret**，仓库内只有公钥）→ GitHub Release + 腾讯 CNB 镜像 → 双源字节一致校验 → 更新渠道分支 → 客户端四层校验（Ed25519 / 大小 / SHA-256 / Authenticode）。这条链路上的幂等与 fail-closed 逻辑有对应测试把 workflow 文本本身当契约断言。
3. **最大的供应链缺口是第三方二进制**：`bin/sing-box.exe`（81,947,136 B）、`bin/wintun.dll`（427,552 B）、`bin/libcronet.dll`（9,528,832 B）直接入库；`bin/README.md` 只记录了 sing-box 的版本/来源/哈希，**wintun 与 libcronet 无版本、无来源 URL、无哈希**；且这两个已文档化的哈希在全仓库中**只出现在 `bin/README.md`**，代码、CI、NSIS、测试都不做任何比对——**无出处校验、无哈希门禁**。
4. **CI 门禁的最关键结构性缺口**：签名、安装包信任校验、发布说明校验、清单生成、CNB 同步、双源一致性校验**全部只在 `workflow_dispatch` 且 `signing_mode != none` 时执行**（`build.yml:201-301`、`build.yml:313-323`）；PR 与 main push 上这些步骤一律跳过，即"关键校验只在发布时执行"。
5. **版本一致性门禁只覆盖 12 个受管文件**，README 徽章/章节与 `.en.md` 发布说明**完全不在同步与校验范围内**；tag 构建还使用 `-write` 静默改写元数据（不带 `-check`），因此"tag 提交树自身是否自洽"在发布时并不校验。

---

## 1. protocol/v1 契约

### 1.1 `manifest.json` 定义了什么

文件：`protocol/v1/manifest.json`（3999 B，162 行，实测首 4 字节 `7b 0a 20 20` = `{\n  `，**无 BOM**）。

| 字段 | 行号 | 值 |
| --- | --- | --- |
| `protocol` | manifest.json:2 | `1` |
| `transport` | manifest.json:3 | `stdio-jsonl` |
| `max_message_bytes` | manifest.json:4 | `1048576` |
| `compatibility` | manifest.json:5 | `additive` |
| `states` | manifest.json:6-13 | `stopped / starting / running / degraded / stopping / failed`（6 个） |
| `methods` | manifest.json:14-118 | 18 个方法，每个带 `idempotency` / `privilege` / `cancellation` 三元语义 |
| `events` | manifest.json:119-145 | 5 个事件，每个带 `delivery` / `coalescible` |
| `error_codes` | manifest.json:146-161 | 14 个结构化错误码 |

方法清单（`manifest.json:16-117`）：`engine.hello`、`engine.status`、`engine.start`、`engine.scheduling`、`engine.stop`、`engine.telemetry`、`steam_cdn.configure`、`tun.activate`、`tun.status`、`tun.deactivate`、`dns.resolve`、`dns.status`、`health.check`、`diagnostic.run`、`mtu.set`(:99)、`wfp.inspect`、`hotspot.inspect`、`host.shutdown`。

要点：manifest **只有方法/事件/状态/错误码级别的语义清单，没有字段级 schema**。字段级契约实际由 `fixtures/messages.json` + Go DTO 承担，见 1.3。

### 1.2 版本兼容与演进策略

- 文档承诺：`protocol/v1/README.md:58-61` —— "Protocol v1 is additive. Existing fields keep their name, JSON type, and meaning for the lifetime of v1. New optional fields, methods, events, and error codes may be added. Removing a field, changing its type or meaning, or changing lifecycle ordering requires a newly negotiated protocol version." 同义的机器可读标记是 `manifest.json:5` 的 `compatibility: "additive"`。
- 但**该字段没有任何测试校验**：`contract_test.go:20-37` 的 `contractManifest` 只声明 `protocol/transport/max_message_bytes/states/methods/events/error_codes`，不含 `compatibility`；Go 的 `json.Unmarshal` 默认忽略未知字段，因此 `compatibility` 改错、写错甚至删掉都不会被 CI 发现。
- 其他语义承诺与出处：时间戳 UTC RFC3339、时长整数毫秒（README.md:63-64）；客户端必须用 `engine.hello.capabilities` 而不是假定所有方法存在（README.md:64-65）；`modes` 仅 `proxy` / `tun_tcp_pool`（README.md:67-76）；`managed_tun_lifecycle` 两阶段事务（README.md:78-83）；特权 Core 只认安装策略钉住的 sing-box 路径，并校验 `config_sha256` / `ipv4_fallback_sha256`（README.md:85-91）；`dns.resolve` / `dns.status` 的可用条件（README.md:93-96）；`engine.scheduling` 1–64 个适配器、权重 1–100、返回"上一次配置"用于回滚（README.md:100-109）。
- 运行时的**实际**兼容策略是"硬相等"，不是区间：`desktop/internal/engineclient/client.go:18` `ProtocolVersion = 1`，`:313-316` `if negotiated.ProtocolVersion != ProtocolVersion { c.killCurrent(...) }`。即 negotiated 版本必须精确等于 1，否则终止 Core 进程并返回 `ErrCoreProtocolIncompatible`。`client.go:207` 的快速路径同样用 `hello.ProtocolVersion == ProtocolVersion`；`desktop/internal/services/scheduling.go:71` 与 `steam_cdn.go:122` 把 `ProtocolVersion == 0` 当作"hello 尚未完成"。
- 结论：additive 策略在文档与 manifest 层面是承诺，在代码层面是"同版本号硬校验"。对 v1 内新增可选字段是安全的；对"新协商版本"只有进程终止这一种退化路径，没有版本区间兼容表。

### 1.3 fixtures 如何被使用（唯一的引用点）

`fixtures/messages.json`（22048 B，实测首 4 字节 `5b 0a 20 20` = `[\n  `，无 BOM）在整个仓库中**只有一个代码消费者**：

- `engine/internal/api/v1/contract_test.go:109-195` `TestCanonicalFixturesDecodeIntoTransportDTOs`
- 加载实现：`contract_test.go:311-325` `readContractJSON`，用 `runtime.Caller(0)` 定位源码目录后拼 `../../../../protocol/v1/<relative>`（`contract_test.go:317`），即 `engine/internal/api/v1` → 仓库根 `protocol/v1`。

该测试强制的内容：

- 每个 fixture 的 envelope `protocol` 必须等于 `protocol.Version`（contract_test.go:127-129）。
- `kind == "request"`：必须反序列化成 `protocol.Request`，`request.Method` 必须等于 fixture 元数据 `method`（:137-139），参数必须能反序列化成对应 DTO（`decodeRequestParams`，:197-241）；**除以下方法外，其余方法必须不带 params**（:222-234）。
- `kind == "response"`：必须是成功响应（`Error == nil && Result != nil`，:147-149），结果必须能反序列化（`decodeResult`，:243-287）。
- `kind == "error"`：`Error.Code` 与 `Error.Message` 必须非空（:157-159）。
- `kind == "event"`：`Sequence != 0` 且 `Event != ""`（:165-167），data 必须能反序列化（`decodeEventData`，:289-309）。
- **覆盖率强制**：`Capabilities()` 中每个方法都必须有 request fixture 与 response fixture（:176-183）；5 个事件都必须有 fixture（:184-194）。

fixtures 实测构成（用 pwsh `ConvertFrom-Json` 统计）：共 **48 条** = request 19 / response 20 / error 4 / event 5；request 中 `engine.start` 有 2 条（`engine_start_request`、`tun_tcp_pool_start_request`），正好补齐 19 条对 18 个方法。

已存在的 error fixture 与其 code（实测）：`unsupported_protocol_error → unsupported_protocol`、`method_not_found_error → method_not_found`、`invalid_state_error → invalid_state`、`dns_failed_error → dns_failed`。

### 1.4 引擎与桌面两端实现与 fixtures 的一致性校验：**不存在桌面侧校验**

- manifest 与编译产物的一致性：`contract_test.go:46-107` `TestManifestMatchesCompiledProtocol`
  - `manifest.Protocol == protocol.Version`（:50-52；`engine/internal/protocol/protocol.go:6` `Version = 1`）
  - `manifest.Transport == protocol.Transport`（:53-55；`protocol.go:7` `Transport = "stdio-jsonl"`）
  - `manifest.MaxMessageBytes == protocol.MaxMessageBytes`（:56-62；`protocol.go:8` `MaxMessageBytes = 1024 * 1024`）
  - 方法名列表必须与 `Capabilities()` **顺序敏感完全相等**（:71-73；`engine/internal/api/v1/types.go:70` `func Capabilities() []string`）
  - states 必须等于 `engineRuntime` 的 6 个状态常量（:75-85）
  - events 必须等于 5 个事件常量（:87-103）
  - **error_codes 只要求 `len != 0`（:104-106）**——既不与编译期错误码集合比对，也不校验 fixtures 中的 code 是否属于该列表
  - 每个 method 必须带齐 `idempotency/privilege/cancellation`（:67-69），每个 event 必须带 `delivery`（:97-99）
- 桌面端：**没有**任何测试或生成步骤读取 `protocol/v1`。全仓库 `*.go` 中 `protocol/v1|manifest.json|messages.json` 的命中只有 `engine/internal/api/v1/contract_test.go:48` 与 `:111` 两处（grep 实测）。
- 桌面端方法名以字符串字面量散落在 ~12 个文件（实测清单，节选）：`desktop/internal/engineclient/client.go:309` `"engine.hello"`、`:459` `"host.shutdown"`；`desktop/internal/services/engine.go:858` `"engine.start"`、`:819/:888/:1154` `"engine.stop"`、`:436` `"engine.telemetry"`、`:448/:776/:1138` `"steam_cdn.configure"`、`:994` `"tun.activate"`、`:884/:1147` `"tun.deactivate"`、`:584` `"wfp.inspect"`；`desktop/internal/services/scheduling.go:87/113/122` `"engine.scheduling"`；`desktop/internal/services/hotspot.go:313/332` `"hotspot.inspect"`、`:307` `"tun.status"`；`desktop/internal/services/mtu.go:291/300` `"mtu.set"`；`desktop/internal/services/tun_connectivity.go:288` `"dns.resolve"`、`tun_startup_dns.go:64` `"dns.status"`；`desktop/internal/services/main.go:259` `"engine.status"`/`"health.check"`；`desktop/internal/services/engine.go:279` 与 `desktop/internal/engineclient/client_event_test.go:46` `"dns.fallback_required"`、`engine.go:284` `"tun.state_changed"`。
- 这些字面量**与 manifest 无任何机械联系**：manifest 中重命名/删除一个方法，引擎侧 `contract_test.go:71-73` 会失败（正确拦截），但桌面侧的字面量不会被任何编译期或测试期检查发现，只会在运行时以 `method_not_found` 形式暴露。
- 桌面侧的测试（`desktop/internal/engineclient/client_event_test.go`、`desktop/internal/services/*_test.go`）使用的是自建的假 Core/假响应，不读 fixtures，因此**不构成契约一致性校验**。

### 1.5 文档漂移

`engine/README.md:43` 声称："Go contract tests and the C# real process smoke client validate the same contract."，但仓库中 `git ls-files '*.cs'` 返回**空**（实测），即**不存在 C# 客户端或 C# 冒烟程序**。该句已过时（旧 WinUI 客户端已被 Wails 取代，参见 `docs/migration/`）。影响：读者会误以为存在第二语言实现的一致性校验。

---

## 2. 发布全链路

### 2.1 触发与准入

- `build.yml:3-15`：`on: push(branches: [main])` / `pull_request(branches: [main])` / `workflow_dispatch`，其中 `workflow_dispatch.inputs.signing_mode` 是 `choice`，取值 `none|test|production|publish`，默认 `none`。
- `build.yml:17-19`：并发组 `desktop-${{ github.event.pull_request.number || github.ref }}`，仅 PR 取消进行中构建（tag 发布不会被后一次触发打断）。
- `build.yml:21-23`：`permissions: actions: read, contents: read`（最小权限；发布 job 单独提权到 `contents: write`，见 `build.yml:313-323`）。
- `build.yml:25-28` 环境常量：`WAILS_VERSION: v3.0.0-alpha2.119`、`PNPM_VERSION: 10.34.5`、`INSTALLER_PATH: desktop/bin/hypomux-amd64-installer.exe`。
- 准入校验步骤 `Validate release options`（`build.yml:39-54`）：`production` 必须来自 `main`；`publish` 必须来自 `refs/tags/v*`；`test` 不允许在 tag 上。

### 2.2 job `build`（`build.yml:31-311`，"Validate and package Wails desktop"，`runs-on: windows-2025`）

| 步骤 | 行号 | 作用 / 产物 |
| --- | --- | --- |
| Checkout | 36-37 | `actions/checkout@v5` |
| Validate release options | 39-54 | 上述准入规则 |
| Set up Go | 56-62 | `actions/setup-go@v7`，`go-version-file: engine/go.mod`，缓存 `engine/go.sum` + `desktop/go.sum` |
| Set up Node.js | 64-67 | `actions/setup-node@v6`，node 22 |
| Prepare and validate release version | 69-77 | tag 时 `go -C desktop run ./cmd/release-version -tag $env:GITHUB_REF_NAME -write -notes`；否则 `-check` |
| Restore pnpm tool cache / Install pinned pnpm / Configure pnpm | 79-107 | `actions/cache@v4`（key 含 `PNPM_VERSION`）+ `npm install --global ... pnpm@10.34.5 --no-audit --no-fund`（`build.yml:91`） |
| Restore frontend dependency cache | 109-115 | `actions/cache@v4`，key 含 `hashFiles('desktop/frontend/pnpm-lock.yaml')` |
| Install Wails and NSIS | 117-133 | `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.119`；`choco install nsis --yes`；断言 `%ProgramFiles(x86)%\NSIS\makensis.exe` 存在 |
| Install frontend dependencies | 135-137 | `pnpm --dir desktop/frontend install --frozen-lockfile` |
| Generate Wails frontend bindings | 139-142 | `wails3 generate bindings -clean=true -ts -i` |
| Run frontend tests | 144-146 | `pnpm --dir desktop/frontend test` |
| Build frontend assets for Go validation | 148-150 | `pnpm --dir desktop/frontend build` |
| Validate Go modules | 152-166 | 对 `engine` 与 `desktop` 分别 `go mod verify` + `go test ./...` + `go vet ./...` |
| Verify Go formatting | 168-186 | `git ls-files '*.go'` 过滤 `engine/`、`desktop/` 后 `gofmt -l` |
| Build and package Wails desktop | 188-191 | `wails3 task windows:package`（working-directory: desktop）→ `desktop/bin/*` |
| Stage unsigned executables for SignPath | 193-199 | 复制出 `signing-input/hypomux-unsigned.exe`、`hypomux-engine-unsigned.exe` |
| Upload desktop executable for SignPath | 201-208 | `actions/upload-artifact@v7`（`archive: false`，`if-no-files-found: error`） |
| Sign desktop executable | 210-223 | `signpath/github-action-submit-signing-request@v2` → `signed-desktop` |
| Upload Core executable for SignPath | 225-232 | 同上 |
| Sign Core executable | 234-247 | → `signed-core` |
| Repackage signed executables | 249-260 | 已签名 EXE 复制回 `desktop\bin`，再手工 `& $env:MAKENSIS "-DARG_WAILS_AMD64_BINARY=..\..\..\bin\hypomux.exe" project.nsi`（`desktop/build/windows/nsis`） |
| Stage unsigned installer for SignPath | 262-265 | 暂存安装器 |
| Upload installer for SignPath | 267-274 | `upload-artifact@v7` |
| Sign installer | 276-289 | → `signed-installer` |
| Use signed installer | 291-294 | 落回 `env.INSTALLER_PATH` |
| Verify production installer trust policy | 296-301 | 仅 production/publish：设 `HYPOMUX_SIGNED_INSTALLER_TEST`（:300）并跑 `go -C desktop test ./internal/services -run TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller -count=1 -v` |
| Upload build artifacts | 303-311 | artifact 名 `HypoMux-Windows-${{ github.sha }}-${{ github.run_attempt }}`（:306） |

产物：`desktop/bin/hypomux.exe`（桌面）、`desktop/bin/hypomux-engine.exe`（Core）、`desktop/bin/hypomux-amd64-installer.exe`（NSIS 安装器）。三者中只有安装器被发布。

### 2.3 SignPath 代码签名

- 三个 submit 步骤：`build.yml:210-223`（桌面 EXE）、`build.yml:234-247`（Core EXE）、`build.yml:276-289`（安装器）；对应 upload 步骤 `build.yml:201-208 / 225-232 / 267-274`。
- 固定参数：`organization-id: 4463263d-e740-4262-ae16-7eac788453ea`、`project-slug: HypoMux`、`api-token: ${{ secrets.SIGNPATH_API_TOKEN }}`、`wait-for-completion: true`、`timeout: 3600`、`skip-decompress: true`。
- 策略选择：`signing-policy-slug` = `inputs.signing_mode == 'test' && 'test-signing' || 'release-signing'`，即 `test` 模式走测试证书（`docs/architecture/strict-go-windows-qualification.md:46-57` 说明测试证书使用不受信任的测试根，仅可用"精确指纹钉住"的方式用于物理矩阵），`production`/`publish` 走正式签名。
- 关键条件：**三个签名步骤的门槛都是 `github.event_name == 'workflow_dispatch' && inputs.signing_mode != 'none'`**。因此 PR 与 main push 上的构建产物**完全未签名**，`Verify production installer trust policy`（`build.yml:296-301`）也随之跳过。`docs/validation/release-review-2026-09-27.md:64` 在历史评审中已明确指出这一点："成功的未签名打包不能证明最终 SignPath 安装包已经通过发布验收"。
- 签名后必须重新打包安装器（`build.yml:249-260`），因为 NSIS 安装器内嵌被签名的 EXE；这一步是用 `makensis` 手工重跑 `project.nsi`，而非重跑 `wails3 task`。

### 2.4 更新清单生成与 Ed25519 签名（密钥来源）

- 生成：`build.yml:372-404` `Generate update manifest`，用 `jq` 产出 `artifacts/latest.json`：
  - `schema_version: 1`（`:386` 附近断言）、`version`、`name: "HypoMux ${version}"`、`notes`
  - `installer: { name, size, sha256, urls: [$cnb_url, $github_url] }`（`:396-398`）
  - `installer_sha256="$(sha256sum ...)"`（`build.yml:379`）
  - `cnb_url = https://cnb.cool/Hypostasis-Cat/HypoMux/-/releases/download/<tag>/<file>`，`github_url = https://github.com/Hypostasis-Cat/HypoMux/releases/download/<tag>/<file>`
  - 自检：`jq empty` 校验 JSON；`jq -j '.notes'` 与源发布说明 `cmp --silent`（保证 Release body 与清单 notes 逐字节一致）
- 签名：`build.yml:406-415` `Sign update manifest`
  - 私钥注入：`env: UPDATE_MANIFEST_ED25519_PRIVATE_KEY: ${{ secrets.UPDATE_MANIFEST_ED25519_PRIVATE_KEY }}`
  - 执行：`go -C desktop run ./cmd/update-manifest-sign -input ../artifacts/latest.json -output ../artifacts/latest.json.sig -verify-public-key "$(tr -d '\r\n' < desktop/internal/services/update_manifest_ed25519_public_key.txt)"`
  - 断言签名文件恰为 64 字节（Ed25519 签名长度）
- **密钥来源结论：私钥来自 GitHub Secret，不是仓库内文件。** 证据：
  - `desktop/cmd/update-manifest-sign/main.go:13` `const privateKeyEnvironment = "UPDATE_MANIFEST_ED25519_PRIVATE_KEY"`，`:32` 只从 `os.Getenv(...)` 读取；`:48-51` 要求 base64 解码后长度恰为 `ed25519.PrivateKeySize`
  - 仓库内唯一的相关密钥文件是**公钥** `desktop/internal/services/update_manifest_ed25519_public_key.txt`，由 `desktop/internal/services/updater.go:49-50` 用 `//go:embed` 编进客户端；其内容为 `cADpocBrcdxl7Ihmu2SkOZdXy9D8Hpcf5B5FjEYJNys=`（base64，32 字节）
  - `git ls-files` 中与密钥/证书相关的跟踪文件只有该公钥 `.txt` 与两个无关的 `ai_secret_*.go`；全仓库（除 `.git`、`node_modules`）grep `BEGIN (RSA |EC )?PRIVATE KEY` **零命中**；无 `.pem/.pfx/.key/.p12/.snk` 被跟踪
  - `release-smoke.yml:37-51` `Verify manifest signing secret matches embedded public key`：用 Secret 对 `{"schema_version":1,"smoke_test":true}` 签名，断言 64 字节——即 CI 能主动验证"Secret 与内嵌公钥配对"，可提前发现密钥轮换遗漏
- 客户端验签：`desktop/internal/services/updater.go:260-277`（公钥长度必须 32、签名长度必须 64、`ed25519.Verify` 绑定精确清单字节）；`:278-284` 公钥加载失败即失败关闭（`mustUpdateManifestPublicKey`）。

### 2.5 job `release`（`build.yml:313-677`，"Publish release installer"）

条件（`build.yml:313-323`）：`needs: build`，`if: workflow_dispatch && inputs.signing_mode == 'publish' && startsWith(github.ref, 'refs/tags/v')`，`runs-on: ubuntu-latest`，并发组 `hypomux-release-publish`（`cancel-in-progress: false`），`permissions: contents: write`。

| 步骤 | 行号 | 作用 |
| --- | --- | --- |
| Checkout | 326-329 | `fetch-depth: 0` |
| Set up Go | 331-335 | `actions/setup-go@v7`（关闭缓存） |
| Download build artifacts | 337-341 | `actions/download-artifact@v8` → `artifacts/` |
| Resolve release channel | 343-346 | `go -C desktop run ./cmd/release-version -tag "${GITHUB_REF_NAME}" -notes -github-output "${GITHUB_OUTPUT}"`，产出 `version/windows_version/prerelease/make_latest/channel` |
| Validate versioned release notes | 348-362 | 要求 `.github/release-notes/${GITHUB_REF_NAME}.md` 存在（`-f`）且非空（`-s`）；**只校验 `.md`，不校验 `.en.md`** |
| Stage legacy-updater-compatible installer name | 364-370 | `cp artifacts/hypomux-amd64-installer.exe "artifacts/HypoMux_Setup_${version}.exe"` |
| Generate update manifest | 372-404 | 见 2.4 |
| Sign update manifest | 406-415 | 见 2.4 |
| Preflight synchronized GitHub and CNB tags | 417-441 | `git ls-remote` 解析 `refs/tags/<tag>` 与 peeled `^{}`，要求 GitHub == CNB == HEAD |
| Ensure CNB Release and upload installer | 443-497 | 固定 digest 镜像 `CNB_IMAGE: docker.cnb.cool/looc/git-cnb@sha256:c254172bb9d6025733a0e2991b4a99af8c46aeedcadcb468788ef5a0dc00275c`（`:447`）；`cnb release get/create --make-latest --prerelease`；Release body 与笔记一致；同名资产已存在则按字节比对，仅 404 时 `asset-upload` |
| Check existing GitHub installer | 499-523 | `curl` 200 → `cmp`；404 → 置 `upload=true` |
| Create or update GitHub Release metadata | 525-534 | `softprops/action-gh-release@v3`（`upload == false` 分支，`body_path`、`prerelease`、`make_latest`） |
| Upload installer to GitHub Release | 536-547 | 同一 action（`upload == true`，`files` + `fail_on_unmatched_files`） |
| Verify both Release installers are byte-identical | 549-577 | 两源各 12 次重试下载后 `cmp` |
| Publish signed update channel to GitHub and CNB | 579-631 | 用 `git hash-object -w` + `git mktree` + `git commit-tree` 直接构造提交，push 到 `refs/heads/${{ steps.version.outputs.channel }}`（`origin` 与 `cnb`），再 `ls-remote` 复核；凭据走临时 `GIT_CREDENTIAL` store 文件 |
| Verify both raw update channels and Ed25519 signature | 633-677 | 分别 curl `raw.githubusercontent.com/.../<channel>/latest.json(.sig)` 与 `cnb.cool/.../-/git/raw/<channel>/latest.json(.sig)`，`cmp` + `update-manifest-sign -verify-only` |

发布渠道命名：正式版 `update-channel`，预发布（beta/rc）`update-channel-preview`（`desktop/internal/releaseversion/version.go:53-58` `Channel()`；`build.yml:579-631` 使用该输出作为分支名）。`prerelease` 在 workflow 中出现两次（GitHub 与 CNB，`installer_layout_test.go:497-522` 强制计数为 2）。

### 2.6 GitHub Releases 与腾讯 CNB 镜像

- 双写：GitHub Release（`build.yml:525-547`）+ CNB Release（`build.yml:443-497`）。
- 双源一致性是**强制**的：tag 三方一致（`:417-441`）、安装包两源字节一致（`:549-577`）、更新渠道两源字节一致且签名可验（`:633-677`）。
- CNB 侧写操作的兜底逻辑：同 tag 下已存在同名资产时，不是重传而是**按字节比对**（`:443-497`），从而让"重复发布同一 tag"变成幂等操作而不是覆盖。
- tag 的创建/同步由独立工作流负责：`create-release-tag.yml:13-19`（`if: github.ref == 'refs/heads/main'`，`runs-on: ubuntu-latest`），步骤 `Check out main`（`:21-22`）、`Set up Go`（`:26-27`）、`Validate release version and notes`（`:32`）、`Validate and synchronize release tag`（`:38-43` 起，含 CNB_TOKEN、`git credential` 临时文件、优先解 peeled 的 `resolve_remote_tag_commit`、GitHub 无 tag 则注释 tag 并 push、已存在但指向不同 commit 则拒绝移动、CNB 无 tag 则 push cnb、最终三方 commit 必须一致）。

### 2.7 客户端自动更新的元数据下载地址

常量集中在 `desktop/internal/services/updater.go:29-42`：

- `githubRepositoryURL = https://github.com/Hypostasis-Cat/HypoMux`
- `releaseDownloadURL = githubRepositoryURL + "/releases/download/"`
- `githubLatestManifestURL = https://raw.githubusercontent.com/Hypostasis-Cat/HypoMux/update-channel/latest.json`
- `cnbRepositoryURL = https://cnb.cool/Hypostasis-Cat/HypoMux`
- `cnbDownloadURL = cnbRepositoryURL + "/-/releases/download/"`
- `cnbLatestManifestURL = cnbRepositoryURL + "/-/git/raw/update-channel/latest.json"`
- 预览渠道：`githubPreviewManifestURL = .../update-channel-preview/latest.json`、`cnbPreviewManifestURL = .../-/git/raw/update-channel-preview/latest.json`
- 边界：`maxUpdateManifestSize = 2 << 20`、`maxManifestSignatureSize = 1024`、`maxInstallerMirrorCount = 4`、`updateMetadataTimeout = 10s`、`installerDownloadTimeout = 15min`
- 正则：`installerNamePattern = (?i)^HypoMux_Setup_[A-Za-z0-9][A-Za-z0-9._+\-]*\.exe$`、`sha256Pattern = ^[a-f0-9]{64}$`
- 渠道选取：`updater.go:153-155`，正式渠道 = `[cnbLatestManifestURL, githubLatestManifestURL]`，预览渠道附加两个 preview URL。即**默认同时读两个镜像**，而非只读 GitHub。

---

## 3. CI 门禁实况

### 3.1 四份 workflow 的触发矩阵

| 工作流 | 触发 | 关键路径过滤 | 门槛 |
| --- | --- | --- | --- |
| `build.yml`（677 行，`name: Build Desktop`，`build.yml:1`） | `push(main)`、`pull_request(main)`、`workflow_dispatch`（`build.yml:3-15`） | **无** | 全量桌面构建；签名/发布需 dispatch |
| `go-engine.yml`（67 行，`name: Validate Go Engine`，`:1`） | `push(main)`、`pull_request(main)` 带 `paths:` = `engine/**`、`bin/sing-box.exe`、`protocol/**`、`.github/workflows/go-engine.yml`（`:3-17`），加 `workflow_dispatch`（`:18`） | 有 | 仅引擎 |
| `create-release-tag.yml`（125 行，`name: Create Release Tag`，`:1`） | **仅** `workflow_dispatch`（`:3`），输入完整 tag | — | 必须在 main（`:15`） |
| `release-smoke.yml`（144 行，`name: Release Trust Smoke Test`，`:1`） | **仅** `workflow_dispatch`（`:3`，输入 tag，默认 `v2.5.6`） | — | 只读（见 3.4） |

### 3.2 PR / push 上真正运行的门禁

`build.yml` job `build` 在每次 PR / main push 上运行（无 paths 过滤），门禁项：

1. 发布选项合法性（`build.yml:39-54`，push/PR 下基本空转）
2. **版本元数据一致性** `release-version -check`（`build.yml:69-77`）
3. 前端：`pnpm --frozen-lockfile install`（`:135-137`）→ 生成 bindings（`:139-142`）→ `pnpm test`（`:144-146`）→ `pnpm build`（`:148-150`）
4. Go：`mod verify` + `test ./...` + `vet ./...`，engine 与 desktop 两个模块（`:152-166`）
5. `gofmt -l` 全量 Go 文件（`:168-186`）
6. 完整 Windows 打包（`:188-191`）——但**未签名**

`go-engine.yml` 仅当 `engine/**`、`bin/sing-box.exe`、`protocol/**` 变化时运行，门禁：`Verify formatting`（`:40-48`，`gofmt -l .`）、`Test and vet`（`:49-58`，`go mod verify`/`test`/`vet`）、`Scan known vulnerabilities`（`:59-63`，`govulncheck@v1.6.0`，`:62`）、`Verify release build`（`:64-67`）。

### 3.3 "关键校验只在发布时执行"的缺口清单（结构化）

| 只在 dispatch 时执行的校验 | 行号 | 影响 |
| --- | --- | --- |
| 三段 SignPath 签名 | `build.yml:201-289` | PR/main 产物全部未签名；签名配置（organization/policy/token）失效要到发布日才暴露 |
| 签名后重新打包安装器 | `build.yml:249-260` | 该路径（含 `makensis` 手工调用）在 PR 上从不执行 |
| `Verify production installer trust policy` | `build.yml:296-301` | 唯一"真实签名安装包能通过 Authenticode 信任检查"的门禁，只在 production/publish 跑 |
| 发布说明存在性与非空校验 | `build.yml:348-362` | 缺失/空的中文发布说明只在发布时暴露 |
| 更新清单生成 / Ed25519 签名 | `build.yml:372-415` | 清单结构与 Secret 可用性只在发布时验证（`release-smoke.yml:37-51` 可部分补偿，但需人工 dispatch） |
| GitHub/CNB tag 预检、资产上传/字节比对 | `build.yml:417-523` | 双源漂移只在发布时发现 |
| 双源安装包一致、更新渠道发布与验签 | `build.yml:549-677` | 同上 |

**"本地能过、CI 不过"的候选点（据证据推断，未实测）**：
- `gofmt`（`build.yml:168-186`、`go-engine.yml:40-48`）与 `SyncMetadata` 的换行处理：`desktop/internal/releaseversion/metadata.go:56` 显式把 CRLF 归一化成 LF 再比较，说明作者已注意到 Windows 本地 CRLF 差异；`.gitattributes:1-12` 也对 `*.go/*.md/*.yml/*.json/...` 设了 `eol=lf`，因此这条风险基本被结构性消除。
- 前端 `pnpm test` 在没有 `--frozen-lockfile` 的本地环境可能通过，而 CI 会用锁文件；本地漏跑测试则 CI 才拦。属常规差异，非结构性缺陷。
- 元测试依赖**仓库相对路径**（例如 `engine/internal/api/v1/contract_test.go:317` 的四级 `..`，`desktop/installer_layout_test.go:294-409` 读 `../.github/workflows/build.yml`、`../.github/release-notes/v2.5.8.md`）：任何模块目录搬迁或子目录化都会让这些门禁**静默失效或直接报错**，需要在重构时特别小心。

### 3.4 `go-engine.yml` 与 `build.yml` 的重复/冲突

- **重复（同一事实被两处检查，非缺陷）**：`gofmt`（`build.yml:168-186` vs `go-engine.yml:40-48`）、`go mod verify`/`go test`/`go vet`（`build.yml:152-166` vs `go-engine.yml:49-58`）、引擎构建（`build.yml:188-191` 间接 vs `go-engine.yml:64-67`）。
- **非对称（真实缺口）**：
  - `govulncheck` **只在 `go-engine.yml:59-63`（engine 模块）**运行；`desktop` 模块（Wails 桌面 + `desktop/internal/services` 更新器，恰恰是承载凭据/更新信任链的模块）**没有任何漏洞扫描**。
  - `desktop/**` 的改动不会触发 `go-engine.yml`（`:6-17` 的 paths 过滤），只会触发 `build.yml`。
  - `bin/wintun.dll`、`bin/libcronet.dll` 不在 `go-engine.yml:6-17` 的 paths 列表中（只有 `bin/sing-box.exe`），单独替换这两个文件不会触发引擎校验（而校验本身也不做哈希比对，见 5.2）。
- **冲突**：未发现。两个工作流在不同 runner job 中独立构建，无共享产物、无并发写同一资源；`build.yml:17-19` 与 `build.yml:313-323` 的 concurrency 分组也不会互相覆盖。

### 3.5 仓库级治理配置缺失

- `.github/` 下只有 `release-notes/` 与 `workflows/`（实测目录列举）：**无 `dependabot.yml`、无 `CODEOWNERS`、无 `dependency-review` 工作流、无 `pnpm audit`/`npm audit` 步骤**（grep `audit|dependency-review|dependabot` 在 `.github/workflows/*.yml` 中只命中 `build.yml:91` 的 `--no-audit`，即**明确关闭**了 pnpm 安装时的审计）。
- 因此：GitHub 侧的分支保护 / required checks / 谁有权批准 SignPath 请求（README.md:58 与 README_EN.md:56 声称由 `Hypostasis-Cat` 在 SignPath UI 中人工批准）**无法从仓库内核实**（见第 8 节）。

---

## 4. 版本一致性

### 4.1 `release-version` 工具做什么校验

工具：`desktop/cmd/release-version/main.go`（83 行）；解析与同步逻辑：`desktop/internal/releaseversion/version.go`、`metadata.go`。

- 参数（`main.go:22-29`）：`-version` / `-tag`（互斥，`main.go:30`）/ `-root`（默认 `.`，即 desktop 目录）/ `-write` / `-check`（互斥）/ `-notes` / `-github-output`。
- tag 必须以 `v` 开头（`main.go:35-37`）；无显式版本时读 `desktop/VERSION`（`main.go:40-46`）。
- `-notes` 校验：`.github/release-notes/v<version>.md` 必须存在且 `TrimSpace` 后非空（`main.go:51-60`）——**只校验中文 `.md`**。
- `-check`/`-write` 走 `releaseversion.SyncMetadata`（`main.go:61-65`）。
- 输出 5 个键供 GitHub Actions 使用（`main.go:66`）：`version`、`windows_version`、`prerelease`、`make_latest`、`channel`。
- 版本格式与 Windows 数字版本映射（`version.go`）：
  - 正则 `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(beta|rc)\.([1-9][0-9]*))?$`（`version.go:11`）：拒绝前导零、拒绝 `beta1`、拒绝构建元数据后缀，与 `docs/RELEASE_VERSIONING.md:10` 一致。
  - 三段各 0–65535（`version.go:28-31`），beta/rc 序号 1–29999（`version.go:33-39`）。
  - `Windows()`（`version.go:61-70`）：正式版 revision=65535；beta.N → N；rc.N → 30000+N。数值上保证 **beta.N(1..29999) < rc.N(30001..59999) < 正式版(65535)**，与 `docs/RELEASE_VERSIONING.md:27-31` 的表格完全一致。
  - `Channel()`（`version.go:53-58`）：预发布 → `update-channel-preview`，否则 `update-channel`。
  - `Key()`（`version.go:74-101`）兼容旧更新器的 2–4 段纯数字版本，并给 beta/rc 加 rank（beta=0 < rc=1 < 正式=2）；无法解析的版本"永不比较为新"，属失败关闭。

### 4.2 受管文件清单（共 12 个路径）

`SyncMetadata`（`metadata.go:14-71`）逐文件用**正则+精确计数**替换，写前先全部读取并在 `check` 模式下逐文件比对（`metadata.go:51-65`）：

| # | 路径 | 字段 | 模式 / 计数 |
| --- | --- | --- | --- |
| 1 | `Taskfile.yml` | 展示版本 | `(APP_VERSION: ")[^"]+`，1（metadata.go:21） |
| 2 | `build/config.yml` | 展示版本 | `(?m)(^  version: ")[^"]+`，1（:22） |
| 3 | `frontend/package.json` | 展示版本 | `("version": ")[^"]+`，1（:23） |
| 4 | `frontend/src/product.ts` | 展示版本 | `(version: ")[^"]+`，1（:24） |
| 5 | `internal/services/updater.go` | `CurrentVersion` | `(CurrentVersion\s*= ")[^"]+`，1（:25） |
| 6 | `build/windows/nsis/wails_tools.nsh` | `INFO_PRODUCTVERSION` | 1（:26） |
| 7 | `build/windows/info.json` | `file_version`/`product_version` | **数字版本**，计数 2（:27） |
| 8 | `build/windows/info.json` | `FileVersion`/`ProductVersion` | **展示版本**，计数 4（:28） |
| 9 | `build/windows/wails.exe.manifest` | `version=` | 数字版本，1（:29） |
| 10 | `build/windows/msix/template.xml` | `Version=` | 数字版本，1（:30） |
| 11 | `build/windows/msix/app_manifest.xml` | `Version=` | 数字版本，1（:31） |
| 12 | `VERSION` + 生成的 `build/windows/nsis/version.nsh` | 展示/数字 | 直接整文件写入（:49-50） |

任何一处字段数量不符即报错 `unexpected version fields in <path>`（`metadata.go:44-46`）；`-check` 下不一致报 `version metadata out of sync: <path>; run go -C desktop run ./cmd/release-version -write`（`metadata.go:59-61`）。

### 4.3 门禁本体与 d6fb799

- 版本一致性的**测试本体**是 `desktop/installer_layout_test.go:483-495` `TestVersionMetadataIsConsistent`：读 `VERSION` → `releaseversion.Parse` → `releaseversion.SyncMetadata(".", v, true)`。它随 `go -C desktop test ./...`（`build.yml:161`）在每次 PR/main push 上执行。
- `d6fb799 fix(ci): validate Windows version tables against release version` 的改动面（`git show --stat`）：**只改了 `desktop/installer_layout_test.go`，+23 / −5**。新增的是 `TestWindowsTaskManagerUsesProductName`（`installer_layout_test.go:760-806`）对 `build/windows/info.json` 的断言：语言表 `0000` 与 `0409` 都必须存在，且 **`FileVersion`/`ProductVersion` 必须等于 `VERSION` 的字符串值**。这正好与 `metadata.go:27-28` 的两条编辑（数字版本 2 处 + 展示版本 4 处）互为约束，即编号 7/8 两行现在受测试保护。
- 该提交也解释了 CI 中 `-check` 的实际覆盖面：`metadata.go` 的 12 个路径 + `installer_layout_test.go:760-806` 的 info.json 语言表。

### 4.4 README 徽章与代码版本常量的一致性（当前快照：全部一致）

| 位置 | 内容 | 结论 |
| --- | --- | --- |
| `desktop/VERSION` | `2.7.0` | 版本源 |
| `desktop/Taskfile.yml:5` | `APP_VERSION: "2.7.0"` | 一致 |
| `desktop/internal/services/updater.go:28` | `CurrentVersion = "2.7.0"` | 一致 |
| `README.md:9` | 徽章 `Version-2.7.0` | 一致（**但不受门禁保护**） |
| `README_EN.md:9` | 徽章 `Version-2.7.0` | 一致（**但不受门禁保护**） |
| `README.md:22` | `## 2.7.0 新版本` | 一致（**但不受门禁保护**） |
| `README_EN.md:20` | `## What's new in 2.7.0` | 一致（**但不受门禁保护**） |
| `desktop/build/windows/Taskfile.yml:88` | `-ldflags "... -X main.version={{.APP_VERSION}} ..."` 注入引擎版本 | 构建期由 `APP_VERSION` 决定 |
| `engine/cmd/hypomux-engine/main.go:23` | `version = "dev"` | 未被注入时的默认值（本地直跑） |

**缺口**：README 的 4 个版本标识**不在 `SyncMetadata` 的 12 个路径内**（`metadata.go:20-31`），也不被任何测试或 workflow 校验（grep `README|\.en\.md` 在 `desktop/**/*.go` 中的命中只有 `installer_layout_test.go:373/388/399/400` 与 `cmd/release-version/main.go:52`，全部与 README 无关；`.github`/`desktop`/`tools` 下 grep `en\.md` **零命中**）。`docs/RELEASE_VERSIONING.md:23` 更是明确写明："README 中面向公众的最新正式版标识由正式发布时维护，不随 Beta / RC 改动。" —— 即这是**有意的设计**，但代价是：**正式版发布时没有任何机器校验能保证 README 徽章与实际发布版本一致**。

**叠加风险**：tag 构建路径使用 `-write` 而非 `-check`（`build.yml:69-77`）：`go -C desktop run ./cmd/release-version -tag $env:GITHUB_REF_NAME -write -notes`。也就是说，如果某个 tag 指向的提交树上这 12 个文件与 tag 版本不一致，构建**不会失败**，而是静默把它们改写后再打包。产物本身是自洽的（版本来自 tag），但"tag 提交树是否自洽"在发布时**不被校验**——`-check` 只在 PR/main push 路径（非 tag）执行。

---

## 5. 供应链风险

### 5.1 GitHub Actions 的版本固定方式

`uses:` 全量清单（grep 实测，`文件名:行号`）：

| 行号 | Action |
| --- | --- |
| build.yml:37 / 327 | `actions/checkout@v5` |
| build.yml:57 / 332、create-release-tag.yml:27、go-engine.yml:35、release-smoke.yml:25 | `actions/setup-go@v7` |
| build.yml:65 | `actions/setup-node@v6` |
| build.yml:81 / 110 | `actions/cache@v4` |
| build.yml:204 / 228 / 270 / 304 | `actions/upload-artifact@v7` |
| build.yml:212 / 236 / 278 | `signpath/github-action-submit-signing-request@v2` |
| build.yml:338 | `actions/download-artifact@v8` |
| build.yml:527 / 538 | `softprops/action-gh-release@v3` |
| create-release-tag.yml:22、go-engine.yml:32、release-smoke.yml:22 | `actions/checkout@v5` |

**结论：全部使用可变 major tag，零个 SHA 固定。** 风险点按严重度排序：

1. `signpath/github-action-submit-signing-request@v2`（3 处）持有 `secrets.SIGNPATH_API_TOKEN`，且其产物会被最终用户信任（Authenticode）。第三方 action 的 major tag 被上游重打（或上游账号被接管）即可影响签名请求内容。
2. `softprops/action-gh-release@v3`（2 处）运行在 `permissions: contents: write` 的发布 job 中，可直接改写 Release 资产。
3. 其余为 GitHub 官方 action（`actions/*`），风险相对低，但同样未固定 SHA。

### 5.2 第三方二进制（sing-box / wintun / libcronet）的来源与校验

事实（实测字节数、哈希与跟踪状态）：

| 文件 | 大小 | 实测 SHA-256 | 仓库内溯源记录 |
| --- | --- | --- | --- |
| `bin/sing-box.exe` | 81,947,136 B | `7bbef1dea9189ee12799ae834ea4b4658355da25c47a21ad8804904c0ccd9410` | `bin/README.md:5-10`：版本 1.14.2、release URL、archive 名、archive SHA-256 `c2d8bfff…`、binary SHA-256、upstream revision `af6e64c3…` |
| `bin/wintun.dll` | 427,552 B | `e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce` | **无**（`bin/README.md` 全文未提 wintun） |
| `bin/libcronet.dll` | 9,528,832 B | `257f966119ffca91d7a2ce110a4b668b865d88bf2ed5339fd06a5644b0d02823` | **无** |

- 三文件均由 git 跟踪（`git ls-files bin` → `bin/README.md`、`bin/libcronet.dll`、`bin/sing-box.exe`、`bin/wintun.dll`），未使用 Git LFS（`.gitattributes` 只有 `*.dll binary` / `*.exe binary`，无 `filter=lfs`）。
- sing-box 的文档化哈希**在全仓库只出现于 `bin/README.md`**（对 `7bbef1de…` 与 `c2d8bfff…` 各做一次全仓文本搜索，除 `bin/README.md:9` / `:8` 外**零命中**）。代码、CI、NSIS 脚本、测试**均不比对**：
  - CI 中 grep `Get-FileHash|sha256|signature` 在 `.github/` 的命中只有 `release-smoke.yml:86` 与 `build.yml:379/386/398/447/577`，全部是**安装包与更新清单**的摘要，与第三方二进制无关。
  - NSIS 侧 grep `Authenticode|sha256|SHA256|Verify` 在 `desktop/build/windows/nsis/*.nsi|*.ps1|*.nsh` 中**零命中**，即安装器不校验随包的第三方二进制。
- 唯一的"来源控制"是**构建期存在性检查**：`desktop/build/windows/Taskfile.yml:69-104`（`build:core-runtime`）在复制前断言 `bin/sing-box.exe`、`bin/wintun.dll`、`bin/libcronet.dll` 存在（`:76-82`），然后复制进 `desktop/bin`（`:89-100`）。**没有任何哈希断言**。
- 安装/服务注册期有"信任首次安装"（TOFU）式的完整性钉扎：`engine/cmd/hypomux-engine/service_policy_windows.go:57-81` 计算 `hypomux.exe` 与 `bin\sing-box.exe` 的 SHA-256（`fileintegrity.SHA256`），写入 HKLM 策略（`:84-121`，先写 schema=0 失效再逐值替换再提交）。这能阻止**安装后的替换**，但**不能证明**被钉扎的 sing-box 就是官方 1.14.2 构建——它信任的是"安装那一刻磁盘上的字节"。
- 备注：`docs/architecture/adaptive-scheduling-implementation.md:80` 提到"补策略字段契约与新旧 hello fixture"，`docs/migration/feature-inventory.md:24-26` 把 manifest + fixtures 列为协议清单，说明协议资产被当作契约维护，但如上所述第三方二进制不在其中。

### 5.3 `.gitmodules` 子模块

`.gitmodules:1-3` 只有一个子模块：`[submodule "website"] path = website, url = https://github.com/Hypostasis-Cat/hypomux-web.git`。

- 实测 `git submodule status` → `-df1ec57a3cbfaf5a554ca1f99efdda1ae1ca6132 website`（前导 `-` = **未初始化**），本地 `website/` 目录为空。
- 四个 workflow 的 `actions/checkout` 步骤（`build.yml:37/327`、`create-release-tag.yml:22`、`go-engine.yml:32`、`release-smoke.yml:22`）**均未使用 `submodules: true`**，因此 CI 完全不检出、不构建网站。
- 风险等级：低（网站不参与发布产物），但意味着"网站内容与发布版本"之间没有任何自动化一致性；且子模块固定到具体 commit 才具备可复现性，当前 CI 不消费它。

### 5.4 npm 依赖锁文件

- `desktop/frontend/pnpm-lock.yaml` 存在且被跟踪（`git ls-files`），`lockfileVersion: '9.0'`；CI 使用 `pnpm --dir desktop/frontend install --frozen-lockfile`（`build.yml:135-137`），缓存 key 含 `hashFiles('desktop/frontend/pnpm-lock.yaml')`（`build.yml:109-115`）→ 锁文件变更会失效缓存，属正向设计。
- 锁文件内含安全型 `overrides`（实测）：`undici@>=8.0.0 <8.10.2: 8.10.2`、`nanoid@<3.3.18: 3.3.18`，以及 `pixi-live2d-display>gh-pages: ^6.3.0`。
- 缺失：**无 `pnpm audit`、无 `dependency-review-action`、无 Dependabot**（`.github/` 下无 `dependabot.yml`）。`build.yml:91` 还显式使用 `--no-audit`。即前端依赖漏洞只能靠人工升级锁文件的 `overrides`。

### 5.5 Go 模块与漏洞扫描

- 两个模块（`engine`、`desktop`）各有 `go.sum`；CI 两处执行 `go mod verify`（`build.yml:152-166`、`go-engine.yml:49-58`）。
- `govulncheck@v1.6.0` **只在 `go-engine.yml:59-63`（`:62`）**对 `engine` 运行；`desktop` 模块无等价扫描。

### 5.6 安装脚本（NSIS）签名与升级修复脚本的风险点

- 安装器形态：**NSIS**（非 Inno）。CI 用 `choco install nsis --yes`（`build.yml:117-133`），签名由 SignPath 完成（`build.yml:267-289`），签名后用 `makensis` 重新打包（`build.yml:249-260`）。
- NSIS 脚本自身不做签名/哈希校验（见 5.2 的 grep 结果）。信任边界因此落在客户端：`desktop/internal/services/updater_windows.go:16-23` 在启动安装器前调用 `verifyDownloadedInstallerAuthenticity`；该函数的实现在 `desktop/internal/services/updater_authenticode_windows.go:44-111`：
  - 主校验为**离线**（`WTD_CACHE_ONLY_URL_RETRIEVAL`，`:60`），失败关闭（`:76-78`）；
  - 从 WinVerifyTrust 状态里取主签名者证书，比较 `certificateSimpleDisplayName(certificate) != trustedInstallerPublisher`（`:103-109`），其中 `trustedInstallerPublisher = "SignPath Foundation"`（`:15`）——**按证书 CN 的简单名比对，不比对指纹**；
  - 之后再跑一次在线吊销检查（`checkInstallerRevocation`，`:122-125` 起），该检查**明确 fail-open**（`:113-121` 注释：吊销服务器不可达不算吊销证据，只有 `TRUST_E_REVOKED`/`CRYPT_E_REVOKED` 这类确定性结果才硬失败，`:17-27`）。
- 凭据/令牌处理：`CNB_TOKEN` 通过临时 `git credential` store 文件使用（`build.yml:443-497`、`build.yml:579-631`、`create-release-tag.yml:38` 起），且 `installer_layout_test.go:411-443` 明确断言**禁止把 token 内嵌进 URL**；实测仓库内无明文 token。git-cnb 容器使用固定 digest（`build.yml:447`、`release-smoke.yml:86`），且 `installer_layout_test.go:321-370` 禁止回退到浮动 tag（`docker.cnb.cool/looc/git-cnb:1.2.0`）。
- 升级修复脚本 `tools/support/Repair-HypoMuxUpgrade.ps1`（269 行，配套 `Start-HypoMux-Repair.cmd`、`使用说明.txt`）：
  - 强护栏（正向）：`-ExpectedUserSid` 防止提权后换账户（`:22-23`、`:31-36`）；`Assert-LocalPath` 拒绝相对路径、含引号路径，并沿父链拒绝重解析点/junction（`:69-88`）；`Assert-SignedRecovery` 要求 Authenticode 状态 `Valid`（`:90-97`）；进程只能按**已核实路径**终止（`:99-120`，路径不符即抛错，不按进程名杀进程）；明确不下载、不安装、不删配置/缓存、不重置网络、不改 ACL、不关安全软件（`:5-13`、`:217`）。
  - 风险点（需要留意）：
    1. **签名比对同样只看组织名**：`Assert-SignedRecovery` 的条件是 `SignerCertificate.Subject -notmatch '(^|,\s*)O=SignPath Foundation(,|$)'`（`:94`）——任何由 SignPath Foundation 签发、Authenticode 有效的可执行文件都能通过该断言，未比对指纹/产品名/版本。
    2. **脚本自身未签名，却被要求以 `-ExecutionPolicy Bypass` 提权重启**（`:58-64`）：用户若从非官方渠道获取该脚本，等于以管理员身份运行任意代码。脚本内的 `Read-Host` 确认（`:54`、`:218`）是最后的社交防线。
    3. 脚本只按 `ProductName == 'HypoMux'` 与版本 ≥2.5 判定安装目录（`:179-182`），而安装目录来自注册表 `Uninstall\HypoMux` 的 `InstallLocation`（`:155-160`）或用户手输（`:164-167`）——注册表被写坏时可能指向任意目录（但仍受 `Assert-LocalPath` + 文件存在性检查约束）。

---

## 6. 风险清单（前 8 条）

1. **[置信度 高] 第三方二进制无出处、无哈希门禁** — `bin/README.md:5-10`、`desktop/build/windows/Taskfile.yml:69-104`、`.github/` 全量 grep — 证据：`bin/sing-box.exe`(81.9 MB)/`bin/wintun.dll`(427 KB)/`bin/libcronet.dll`(9.5 MB) 直接入库；`bin/README.md` 只记录 sing-box 的版本/URL/哈希，wintun 与 libcronet **无任何来源记录**；两个已文档化哈希在全仓只出现于该 README，CI 只做"文件存在"断言（Taskfile.yml:76-82）而不比对哈希。 — 影响：任何人只要能在 PR/提交中替换这三者之一（尤其是替换为体积相近的恶意 sing-box.exe 或 libcronet.dll），全部 CI 门禁仍然通过；该二进制会随签名安装包分发给所有用户，并被 Core Service 在安装时"钉扎"为可信。 — 修复方向：把三个文件的期望 SHA-256 写进版本控制的清单文件（或 workflow 的 env），在 `build.yml` 的"Validate Go modules"之前增加一步逐文件 `Get-FileHash` 断言；同时补齐 wintun/libcronet 的来源 URL、版本与 upstream 哈希（含归档哈希），并把校验失败设为硬失败。

2. **[置信度 高] 所有 GitHub Actions 使用可变 major tag，无 SHA 固定；签名与发布 action 直接持有 secrets** — `build.yml:212/236/278`（signpath）、`build.yml:527/538`（softprops）、`build.yml:37/57/65/81/110/204/228/270/304/327/332/338`、`create-release-tag.yml:22/27`、`go-engine.yml:32/35`、`release-smoke.yml:22/25` — 证据：22 处 `uses:` 全部形如 `@vN`，无一为 40 位 commit SHA。 — 影响：上游 tag 被重打或上游账号失陷时，可在持有 `SIGNPATH_API_TOKEN`、`CNB_TOKEN`、`UPDATE_MANIFEST_ED25519_PRIVATE_KEY` 的 job 中执行任意代码，直接威胁代码签名与更新渠道私钥；`softprops/action-gh-release@v3` 还运行在 `contents: write` 的发布 job 内。 — 修复方向：对第三方 action 固定 commit SHA（可用 Dependabot 的 `github-actions` 生态自动更新 SHA 注释），至少把 signpath 与 softprops 两个 action 钉到 SHA；并考虑给发布 job 增加 environment 保护规则与人工审批。

3. **[置信度 中] 协议一致性只有引擎单端门禁，桌面端方法名为裸字符串字面量；错误码契约无交叉校验** — `engine/internal/api/v1/contract_test.go:71-73`、`:104-106`、`:157-159`；`desktop/internal/engineclient/client.go:309`、`desktop/internal/services/engine.go:858/994`、`desktop/internal/services/mtu.go:291` 等 12 个文件 — 证据：`manifest.json` 的 `methods` 只与 Go 端 `Capabilities()`（`engine/internal/api/v1/types.go:70`）比对；桌面端所有协议方法名都是字面量，无任何生成/校验环节读 `protocol/v1`（全仓 `*.go` grep 仅 `contract_test.go:48/111`）；`error_codes`（manifest.json:146-161，14 个）在测试里只被要求 `len != 0`，fixtures 里仅 4 条 error 用例且其 `code` 不被校验是否属于该列表。 — 影响：manifest 重命名方法时桌面端只能在运行时以 `method_not_found` 暴露；错误码可凭空增删而 CI 全绿；`compatibility` 字段（manifest.json:5）同理无人校验。 — 修复方向：由 `manifest.json` 生成桌面端常量（或把 manifest 嵌入并在 `init()` 中校验桌面用到的字面量集合是 manifest 的子集）；把 `error_codes` 与 `fixtures` 中的 code 做双向集合比对；把 `compatibility` 纳入 `contractManifest` 并断言为 `additive`。

4. **[置信度 中] README 版本标识不在同步范围，tag 构建用 `-write` 静默改写元数据，发布时不校验提交树自洽** — `desktop/internal/releaseversion/metadata.go:20-31`、`docs/RELEASE_VERSIONING.md:23`、`build.yml:69-77` — 证据：受管 12 个路径不含 `README.md`/`README_EN.md`；`grep 'README|\.en\.md'` 在 `desktop/**/*.go` 中零命中；tag 分支命令为 `-write`（`:73`）而非 `-check`，`-check` 只在非 tag 路径执行。 — 影响：正式版发布时 README 徽章与"新版本"章节可能停留在旧版本（`README.md:9/22`、`README_EN.md:9/20`），用户看到与实际下载版本不符的说明；tag 提交树上 12 个受管文件若不自洽也会被静默修复而不报警，削弱"tag = 发布内容"的可审计性。 — 修复方向：把 README 徽章与"新版本"章节标题纳入 `SyncMetadata`（或新增一个轻量 `-check-readme` 断言），并在 tag 路径先跑 `-check` 再跑 `-write`，对"原本不自洽"的情况发出显式警告或失败。

5. **[置信度 中] 英文发布说明从不被校验，但仍随 Release 分发** — `desktop/cmd/release-version/main.go:51-60`、`build.yml:348-362`、`desktop/installer_layout_test.go:373` — 证据：`-notes` 只读 `.github/release-notes/v<version>.md`；发布 job 只断言 `${GITHUB_REF_NAME}.md` 存在且非空；测试硬编码 `../.github/release-notes/v2.5.8.md`；`.github`/`desktop`/`tools` 全文 grep `en\.md` **零命中**。 — 影响：`v2.7.0.en.md`(16939 B) 之类的英文说明可缺失、可为空、可与中文版不同步（甚至可描述错误的版本内容），而发布与校验全绿；英文用户看到的 Release 说明可能与实际版本不符。 — 修复方向：在 `-notes` 与发布 job 中同时校验 `.md` 与 `.en.md` 的存在与非空；进一步可断言两者"新版本"章节的版本号一致。

6. **[置信度 中] `desktop` 模块无漏洞扫描，仓库无依赖审计与自动更新机制** — `go-engine.yml:59-63`（govulncheck 只对 engine）、`.github/` 目录仅 `release-notes/` 与 `workflows/`、`build.yml:91`（`--no-audit`） — 证据：`grep 'govulncheck|audit|dependency-review|dependabot'` 在 workflows 中只命中 `go-engine.yml:62` 与 `build.yml:91`；无 `dependabot.yml`、无 `CODEOWNERS`。 — 影响：恰恰承载更新信任链与凭据处理的 `desktop` 模块（`desktop/internal/services/updater*.go`、Authenticode/WinTrust 调用）没有已知漏洞扫描；前端依赖（`pnpm-lock.yaml` 含 undici/nanoid override）也没有审计步骤，供应链漏洞只能靠人工发现。 — 修复方向：把 `govulncheck` 提升为对 `engine` 与 `desktop` 两个模块都执行；新增 `pnpm audit --prod --audit-level=high` 或 `dependency-review-action`（仅 PR），并启用 Dependabot（gomod × 2 + npm + github-actions）。

7. **[置信度 中] 签名与安装包信任的关键校验完全依赖人工 dispatch，PR/main 一律跳过** — `build.yml:201-289`、`build.yml:296-301`、`build.yml:313-323` — 证据：三个 SignPath 步骤与信任校验的条件都是 `github.event_name == 'workflow_dispatch' && inputs.signing_mode != 'none'`；`docs/validation/release-review-2026-09-27.md:64` 已明确记录"成功的未签名打包不能证明最终 SignPath 安装包已经通过发布验收"。 — 影响：签名配置漂移（organization-id/policy slug/token 失效）、签名后重打包路径（`build.yml:249-260`）与"真实签名安装包能被 WinVerifyTrust 接受"这三类问题都只会在真正发布那一刻暴露，回滚成本高；`release-smoke.yml` 虽可只读验证密钥与渠道，但仍需人工触发。 — 修复方向：在 PR/main 上增加"签名 dry-run"（`signing_mode=test`）的定期（schedule）运行；把 `release-smoke.yml` 挂到 `schedule`（如每周）以持续验证签名密钥配对与渠道可读性；给 tag 发布增加预检（tag 存在即可先跑只读冒烟）。

8. **[置信度 中] 安装包发布者只按证书 CN 简单名比对，吊销检查 fail-open；本地签名任务与 CI 签名路径相互独立且不被校验** — `desktop/internal/services/updater_authenticode_windows.go:15/103-109/113-121`、`tools/support/Repair-HypoMuxUpgrade.ps1:94`、`desktop/build/windows/Taskfile.yml:207-236` — 证据：`trustedInstallerPublisher = "SignPath Foundation"` 与 `certificateSimpleDisplayName` 做字符串相等比较（不比对指纹/序列号）；在线吊销检查注释明确说明不可达时放行；`Assert-SignedRecovery` 只用 `O=SignPath Foundation` 正则；本地 `sign` / `sign:installer` 任务使用开发者本机 `wails3 setup` 证书或 `SIGN_CERTIFICATE`/`SIGN_THUMBPRINT`（`desktop/build/windows/Taskfile.yml:211-213`、`:225-227`），与 CI 的 SignPath 流程无关且 CI 从不校验本地签出的产物。 — 影响：1) 同一 CA/组织签发的其他项目可执行文件在"发布者"维度上等价，仅靠 Ed25519 清单 + SHA-256 + 大小做最终区分（纵深仍在，但单层强度低于指纹钉扎）；2) 签名证书被吊销时离线/受限网络机器仍会接受安装包；3) 开发者本地用个人证书签出的安装器在形态上与官方产物无法区分，若被误分发会以另一个发布者身份出现。 — 修复方向：把发布者校验从 CN 简单名升级为"CN + 指纹钉扎（可从 `latest.json`/内嵌常量取）"；吊销检查失败时至少提示用户而非完全静默放行；在 `desktop/build/windows/Taskfile.yml` 的 `sign`/`sign:installer` 中显式标注"仅本地调试、不得分发"，并在 CI 中对 `Desktop` 产物的签名者做一致性断言。

**次级观察（未进入前 8，但建议记录）**：
- `website` 子模块未初始化且 CI 不检出（`.gitmodules:1-3`，`git submodule status` 前导 `-`）——网站内容与发布无自动化一致性。
- `bin/sing-box.exe` 81.9 MB 无 LFS（`.gitattributes` 无 `filter=lfs`）——仓库与 CI 克隆成本、历史膨胀。
- `bin/wintun.dll`/`bin/libcronet.dll` 不在 `go-engine.yml:6-17` 的 paths 过滤中——单独替换这两个文件甚至不会触发引擎校验工作流。
- `engine/README.md:43` 提到的 "C# real process smoke client" 在本仓库不存在（`git ls-files '*.cs'` 为空）——文档漂移，可能误导读者相信存在跨语言一致性校验。

---

## 7. 优势（证据化）

1. **更新信任链是多层且顺序严格**：Ed25519 清单验签（`updater.go:260-277`，公钥 `//go:embed` 于 `updater.go:49-50`）→ 清单大小/签名大小上限（`updater.go:38-39`）→ 安装包名正则与 sha256 正则（`updater.go:47`）→ 下载前大小校验（`updater.go:462-467`）→ 落盘时 `io.LimitReader` + 实算 SHA-256（`updater.go:472-491`，大小不符即 `大小校验失败`）→ Authenticode 离线验签（`updater_authenticode_windows.go:44-111`）→ 发布者比对（`:107-109`）。每一层都有对应测试（`updater_test.go:450-509` 的镜像摘要不一致/缺摘要/大小不符/不可信发布者等用例）。
2. **镜像双源冲突是 fail-closed**：`updater.go:342-380`（`selectLatestRelease` / `sameReleaseMetadata`），并有测试 `TestUpdaterFailsClosedWhenSignedChannelsConflictForSameVersion`（`updater_test.go:287`）、`TestUpdaterIgnoresTamperedChannelWhenOtherChannelIsTrusted`（`:312`）、`TestUpdaterFailsWhenBothChannelSignaturesAreInvalid`（`:340`）、`TestUpdaterRejectsMissingTamperedOrWrongManifestSignature`（`:361`）。镜像失败还设计了双向回退（`:395-436`）。
3. **更新清单的"单一真源"被强制**：Release body、GitHub Release、CNB Release body 与 `latest.json` 的 `notes` 必须逐字节一致（`build.yml:372-404` 的 `cmp`、`build.yml:525-547` 的 `body_path`、`:443-497` 的 CNB body 比对），并且 `installer_layout_test.go:372-409` 把这条规则**写成对 workflow 文本的断言**（含 `--rawfile notes`、`cmp --silent`、禁止 `notes: ""`）。
4. **发布幂等性被显式设计与测试**：tag 已存在且指向不同 commit 时拒绝移动（`create-release-tag.yml:38` 起，测试 `installer_layout_test.go:411-443` 断言四方 commit 比较）；GitHub/CNB 同名资产已存在时按字节比对而非重传（`build.yml:443-523`）；测试禁止"异步轮询等 tag"式写法（`installer_layout_test.go:321-370` 禁止 `for attempt in $(seq 1 20)`，并要求"两源安装包一致"步骤必须先于"发布更新渠道"）。
5. **私钥卫生良好**：更新清单私钥仅存于 GitHub Secret 且工具只从环境变量读取（`update-manifest-sign/main.go:13/32/48-51`）；仓库内只有公钥（`update_manifest_ed25519_public_key.txt`），全仓无任何私钥材料（grep `BEGIN ... PRIVATE KEY` 零命中）；`release-smoke.yml:37-51` 能主动验证 Secret 与内嵌公钥配对，提前发现密钥轮换事故。
6. **只读冒烟工作流由测试强制保持只读**：`release-smoke.yml` 仅 `contents: read`（`:12-13`），且 `installer_layout_test.go:445-481` **断言**它不得含 `asset-upload`、`release create`、`actions/upload-artifact`、`action-gh-release`、`git push`、`git commit-tree`。这是少见的"用测试约束 CI 权限面"的做法。
7. **CI/发布/安装脚本本身被当作被测契约**：`desktop/installer_layout_test.go`（908 行，package main）大量读 `build/windows/nsis/project.nsi`、`*.ps1`、`../.github/workflows/build.yml`、`../.github/release-notes/v2.5.8.md`，逐条断言安装布局迁移顺序、保护目录顺序、进程关闭屏障、版本表、发布步骤顺序等（如 `:12-91`、`:143-190`、`:233-274`、`:294-319`、`:760-806`）。这种"元测试"把发布纪律固化进 PR 门禁。
8. **版本号数学正确且自洽**：`version.go:11`（格式正则）、`:28-39`（范围）、`:61-70`（Windows 数字版本映射）保证 beta.N < rc.N < 正式版，且 4 段 16 位不溢出；与 `docs/RELEASE_VERSIONING.md:25-31` 的表格逐项一致。`d6fb799` 又把 `build/windows/info.json` 的语言表（`0000`/`0409`）与其 `FileVersion`/`ProductVersion` 纳入断言（`installer_layout_test.go:760-806`）。
9. **权限最小化**：`build.yml:21-23`、`go-engine.yml:20-21`、`release-smoke.yml:12-13` 均为 `contents: read`；只有真正需要写的发布 job 提升为 `contents: write`（`build.yml:313-323`）。发布 job 还有独立并发组且不取消进行中构建（`build.yml:317-320`）。
10. **升级修复工具的作用域很窄且有护栏**：`tools/support/Repair-HypoMuxUpgrade.ps1` 拒绝换账户执行（`:31-36`）、拒绝相对/含引号路径并沿父链拒绝重解析点（`:69-88`）、只按已核实路径终止进程（`:99-120`）、要求签名有效（`:90-97`）、明确不下载不安装不删配置不改 ACL（`:5-13`、`:217`）。相比"让用户手动删目录/关杀软"的常见做法，这是显著更好的工程实践。
11. **OCI 镜像与容器按 digest 固定**：`build.yml:447` 与 `release-smoke.yml:86` 使用 `docker.cnb.cool/looc/git-cnb@sha256:c254172b…`，且测试明确禁止回退到浮动 tag（`installer_layout_test.go:321-370`）。这与第 6 节风险 2（Actions 未固定 SHA）形成对比，说明团队并非不知道固定版本的价值，只是尚未覆盖 Actions。

---

## 8. 未验证项

以下内容**未经验证**，不应作为结论使用：

1. **所有 Go 侧动态行为均未实测**：本机无 Go，`go test ./...`、`go build`、`gofmt -l`、`govulncheck` 全部未执行。`contract_test.go` 与 `installer_layout_test.go` 的断言"是否真的通过"仅由源码阅读推断（从 HEAD 干净且这些测试位于 PR 门禁路径推断应当通过，但**未运行验证**）。
2. **SignPath 侧配置不可核实**：`organization-id 4463263d-e740-4262-ae16-7eac788453ea`、`project-slug HypoMux`、`test-signing`/`release-signing` 策略是否存在、`SIGNPATH_API_TOKEN` 是否有效、以及 README.md:58 / README_EN.md:56 声称的"每次生产签名需 `Hypostasis-Cat` 人工批准"——均无仓库内可验证的证据。
3. **第三方二进制的真实出处未验证**：`bin/wintun.dll` 与 `bin/libcronet.dll` 的版本、来源 URL、是否与官方发布一致**完全没有记录**，我无法比对（无上游哈希可依）。`bin/sing-box.exe` 的哈希与 `bin/README.md:9` 一致（此项已实测），但**是否等于 SagerNet 官方 1.14.2 构建未经独立验证**（未下载上游归档核对）。
4. **GitHub 仓库设置不可核实**：分支保护规则、required status checks、secrets 是否存在与轮换策略、SignPath 集成配置、`update-channel` / `update-channel-preview` 分支的当前内容与保护规则——本地无 API/网络访问，均未验证。
5. **CNB 侧现状未验证**：`cnb.cool/Hypostasis-Cat/HypoMux` 的 Release、镜像 tag、`CNB_TOKEN` 权限范围未联网核实。
6. **`build.yml` 的 shell 脚本逐字复核有限**：job/步骤名称、行号、条件表达式与 `uses:` 行已通过结构化抓取核对；但 677 行中的 shell 细节（例如 `jq` 表达式、`git mktree` 的精确用法、重试循环实现）未逐行逐字复核，结论以行号定位为准。
7. **`engine/README.md:43` 的 C# 客户端**：本仓库无 `*.cs`（已实测）；但该客户端可能存在于仓库之外，**未验证**。
8. **前端测试的实际覆盖**：`pnpm --dir desktop/frontend test`（`build.yml:144-146`）跑的是哪些用例、是否覆盖更新/协议 UI 路径，未查看 `desktop/frontend` 的测试配置与用例清单，**未验证**。
9. **`docs/validation/*` 中引用的历史评审结论**：本文引用 `docs/validation/release-review-2026-09-27.md:64` 作为"未签名打包不足以验收"的佐证，该文档本身的结论未被独立复核。

---

## 附录 A · 关键文件与行号索引

| 关注点 | 位置 |
| --- | --- |
| 协议清单 | `protocol/v1/manifest.json:1-161`；文档 `protocol/v1/README.md:47-109` |
| 协议 fixtures | `protocol/v1/fixtures/messages.json`（48 条）；唯一消费者 `engine/internal/api/v1/contract_test.go:109-195`、加载器 `:311-325` |
| 编译期协议常量 | `engine/internal/protocol/protocol.go:6-8`；能力清单 `engine/internal/api/v1/types.go:70` |
| 协议版本协商 | `desktop/internal/engineclient/client.go:18`、`:207`、`:309-316` |
| 桌面端方法名字面量 | `desktop/internal/engineclient/client.go:309/459`；`desktop/internal/services/engine.go:279/284/436/448/584/776/819/858/884/888/994/1138/1147/1154`；`scheduling.go:87/113/122`；`hotspot.go:307/313/332`；`mtu.go:291/300`；`tun_connectivity.go:288`；`tun_startup_dns.go:64`；`main.go:259` |
| 构建主流程 | `build.yml:31-311`（job build）、`:313-677`（job release） |
| SignPath | `build.yml:201-289`、信任校验 `:296-301` |
| 更新清单与签名 | `build.yml:372-415`；工具 `desktop/cmd/update-manifest-sign/main.go:13/32/44-80`；公钥文件 `desktop/internal/services/update_manifest_ed25519_public_key.txt` |
| 客户端更新常量与验签 | `desktop/internal/services/updater.go:28-50`、`:153-155`、`:210-284`、`:382-506`；Authenticode `updater_authenticode_windows.go:15/44-125` |
| 版本同步 | `desktop/internal/releaseversion/metadata.go:14-71`；格式与映射 `version.go:11/28-39/53-70/74-101`；CLI `desktop/cmd/release-version/main.go` |
| 版本一致性测试 | `desktop/installer_layout_test.go:483-495`、`:760-806`（d6fb799） |
| 发布纪律元测试 | `desktop/installer_layout_test.go:12-91`、`:93-141`、`:143-190`、`:233-274`、`:294-319`、`:321-370`、`:372-409`、`:411-443`、`:445-481`、`:497-522` |
| 引擎工作流 | `go-engine.yml:3-18`（触发与 paths）、`:20-21`（权限）、`:23-28`（job）、`:31-67`（步骤，govulncheck `:59-63`） |
| tag 工作流 | `create-release-tag.yml:3`（触发）、`:13-19`（job）、`:21-125`（步骤） |
| 冒烟工作流 | `release-smoke.yml:3`（触发）、`:12-13`（权限）、`:17-19`（job）、`:21-144`（步骤） |
| 第三方资产 | `bin/README.md:5-10`、`:27-29`；构建 `desktop/build/windows/Taskfile.yml:69-104`、`:88`（ldflags） |
| 安装期完整性钉扎 | `engine/cmd/hypomux-engine/service_policy_windows.go:57-81`、`:84-121`、`:330-334` |
| NSIS 与本地签名 | `desktop/build/windows/Taskfile.yml:180-201`（MSIX，CI 不用）、`:207-236`（sign / sign:installer） |
| 升级修复脚本 | `tools/support/Repair-HypoMuxUpgrade.ps1:22-36`、`:69-88`、`:90-97`、`:99-120`、`:122-141`、`:203-254` |
| 子模块 | `.gitmodules:1-3`；`git submodule status`（未初始化） |
| 前端锁文件 | `desktop/frontend/pnpm-lock.yaml`（`lockfileVersion: '9.0'`，含 overrides） |
