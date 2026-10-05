# 90 · 「AI 助手移除 + 新增虚拟网卡功能」集成交叉验证报告

- 验证者：teammate `integration-verifier`（task-12，只读取证；本次唯一写入的文件就是本报告）
- 基线：`HEAD = e66016e`，工作树（未提交）改动 `git status --porcelain` = 97 条路径
- 契约唯一来源：`reports/vnic/00-frozen-interface.md`（本报告只把它当"待验证的约定"，所有结论均独立取证）
- 环境：Windows 10.0.19041 / PowerShell 5.1.19041.5129 / Go `go1.26.6`（`%USERPROFILE%\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.6.windows-amd64\bin\go.exe`，与 `engine/go.mod` 的 `toolchain go1.26.6` 一致）/ wails3 `v3.0.0-alpha2.119`（`%USERPROFILE%\go\bin\wails3.exe`，与 CI 的 `WAILS_VERSION` 一致）/ node `v24.21.0` / pnpm `12.8.1`（CI 锁定 `10.34.5`，版本差异已记入 §9）
- 扫描范围假设（很重要）：所有"全仓扫描"结论都限定为 **git 跟踪的文件**，并显式 `:(exclude)reports`；`reports/**` 是本轮任务的报告产物（未跟踪），它本身大量出现被禁关键词（见 F-4）。若验收脚本对整个工作树做 ripgrep，会命中 `reports/**`，请不要据此判定残留。

---

## §0 结论速览

### 0.1 是否阻塞 CI

> **是，阻塞。** 阻塞项只有 1 个（F-1，严重）：`desktop/internal/services/settings.go:84` 的 struct 字段对齐未满足 `gofmt`，CI 的 Go 格式化校验步骤（`.github/workflows/build.yml:168-186`，`$unformatted.Count -gt 0 → exit 1`）会失败。
> **修掉 F-1 之后，本次在本机实际执行的全部 CI 门禁都通过**（见 0.2）：引擎/桌面 `go test ./...`、`go vet ./...`、`go mod verify`、前端 `vitest run`(243 用例)、`tsc`、`vite build --mode production`、`wails3 generate bindings`。CI 中唯一未在本机执行的门禁是 `wails3 task windows:package`（打包，见 §9）。

### 0.2 已执行的门禁（源码相同、命令与 CI 对齐）

| # | 门禁（CI 步骤） | 本机命令 | 结果 |
|---|---|---|---|
| 1 | `go -C engine test ./...`（build.yml:156-157） | `go -C engine test -count=1 ./...` | **exit 0**，14 行结果（13 个包 `ok` + `internal/protocol [no test files]`）：含 `internal/vnic`、`internal/server`、`internal/tun`、`internal/api/v1`、`internal/proxy` 等（单次耗时随运行波动，例如 vnic 0.642s~0.681s、tun 5.3s~8.3s） |
| 2 | `go -C desktop test ./...`（build.yml:158-159） | `go -C desktop test -count=1 ./...` | **exit 0**，10 个包全 `ok`（含根包 `desktop 0.318s`、`internal/services 56.538s`） |
| 3 | `go -C engine vet ./...` | `go -C engine vet ./...` | **exit 0** |
| 4 | `go -C desktop vet ./...` | `go -C desktop vet ./...` | **exit 0** |
| 5 | `go -C {engine,desktop} mod verify` | 同 | **all modules verified**（两者） |
| 6 | Go 格式化校验（build.yml:168-186） | `git ls-files '*.go' \| 过滤 engine/ desktop/ \| Test-Path` → `gofmt -l <files>` | **FAIL：332 个文件中 1 个未格式化 → `desktop/internal/services/settings.go`**（F-1） |
| 7 | `wails3 generate bindings -clean=true -ts -i`（build.yml:139-142，cwd=`desktop`） | 同（把 Go 工具链目录加入 PATH） | **exit 0**：`Processed: 306 Packages, 13 Services, 106 Methods, 0 Enums, 47 Models` |
| 8 | `pnpm --dir desktop/frontend install --frozen-lockfile`（build.yml:137） | `corepack pnpm install --frozen-lockfile --prefer-offline` | **exit 0**（412 包；`pnpm-lock.yaml`/`package.json` 未被修改，已用 `git status --porcelain -- …` 验证为空） |
| 9 | `pnpm --dir desktop/frontend test`（build.yml:144-146） | `node_modules\.bin\vitest.CMD run` | **exit 0**：`Test Files 43 passed (43)`、`Tests 243 passed (243)` |
| 10 | `pnpm --dir desktop/frontend build`（build.yml:148-150） | `node_modules\.bin\tsc.CMD` 然后 `node_modules\.bin\vite.CMD build --mode production` | **两段都 exit 0**：`tsc` 无输出；vite `✓ 2494 modules transformed` / `built in 7.41s` |

### 0.3 不一致清单（按级别）

| 级别 | 编号 | 位置 | 问题 | 是否阻塞 CI | 建议修法 |
|---|---|---|---|---|---|
| 严重 | **F-1** | `desktop/internal/services/settings.go:84`（结构体 `SettingsService` 内 `mu` 字段） | 删除 `aiExecution sync.RWMutex` 后，对齐组变为 `{mu, path, settings, migration, loadErrorPath, setAutostart, autostartEnabled}`，`mu` 未补齐到 17 列 ⇒ `gofmt` 报 1 处 diff | **阻塞** | 单行改为 `mu               sync.RWMutex`（`mu` + 15 空格），或直接 `gofmt -w desktop/internal/services/settings.go` |
| 一般 | F-2 | `engine/internal/server/server.go:882-887` | `vnic.create` 最终失败返回错误码 `"tun_failed"`（"could not create the virtual adapter"）；契约 §2.2 列举的可用码是 `invalid_params / invalid_state / elevation_required / security_policy_rejected / start_failed / stop_failed`，不含 `tun_failed` | 否（`tun_failed` 在 manifest 的 14 码内，contract_test 不校验码集合，fixtures 也未覆盖该分支） | 与契约作者对齐：要么把这个用例写回契约（推荐，因为 `tun_failed` 已是主 TUN 的既有码），要么改用 `start_failed` |
| 一般 | F-3 | `protocol/v1/fixtures/messages.json:41-60`（`engine_hello_response.result.capabilities`） | fixture 的 capabilities 只有 18 项，缺 `engine.scheduling`、`steam_cdn.configure`、`mtu.set`（真实 `Capabilities()` 与 manifest 均为 21 项）；本次 diff 只往该数组加了 3 个 vnic 方法 | 否（`contract_test.go` 只把该 result 反序列化进 `HelloResult`，不做集合比对；方法名/顺序一致性由 `contract_test.go:64-73` 对 manifest ↔ `Capabilities()` 断言，已通过） | 顺手把 3 个缺失项补进 fixture，消除长期漂移（注：漂移在本轮之前就存在，非本轮引入） |
| 提示 | F-4 | `reports/**`（全部文件，未跟踪） | 报告产物中大量出现 `ai_enabled`、`AIEnabled`、`pixi`、`live2d`、`companionExport`、`assistant`、`AIService` 等被禁关键词；`git grep`（只扫跟踪文件）不会命中，但"整个工作树 ripgrep"式验收会命中 | 否 | 验收脚本限定 `git grep`（或 `:(exclude)reports`）；若最终要把报告目录提交，请先确认验收口径 |
| 提示 | F-5 | `.gitignore:70-72` | `*.muxskin` 与 `/desktop/frontend/public/skins/mux-layered-source/` 是皮肤功能（已删除）的遗留忽略规则，已成死规则 | 否 | 删除这 3 行（或保留，无害） |
| 提示 | F-6 | `desktop/frontend/src/theme/material.tokens.css:120`；`desktop/internal/services/hotspot.go:98`；`desktop/internal/services/routing.go:379`；`desktop/internal/services/routing_quick_add_test.go:22,28` | 陈旧命名/字面量：`/* Companion-inspired wallpapers… */`、`// Keep credentials out of telemetry and AI tools.`、`// …overwriting external AI edits.`、`"AI-added.exe"`、`"stale quick add overwrote AI rules"` | 否 | 可选清理；不影响功能与 CI |
| 提示 | F-7 | `desktop/frontend/package.json:17-19`、`desktop/frontend/pnpm-workspace.yaml:4` | `fflate`、`pixi.js`、`pixi-live2d-display` 仍被声明但源码零引用（`desktop/frontend/src` 内 `fflate`/`@pixi`/`pixi-live2d` 导入 grep 零命中） | 否（契约明确要求保留，且 vite 不打包未引用模块） | 按契约保留；如未来要瘦身依赖再单独处理 |
| 提示 | F-8 | `docs/validation/release-review-2026-09-26.md:54` 等；`.github/release-notes/v2.7.0.md`/`.en.md` | 历史验证文档与 **已发布** v2.7.0 的发布说明中保留了 AI 助手 / Live2D / 皮肤的历史记录 | 否 | 保留（历史事实，非残留） |
| 提示 | F-9 | `desktop/frontend/src/pages/SettingsPage.test.tsx:78-93`；`desktop/frontend/src/components/shell/CompactNavigation.test.tsx:10-12` | 测试里出现 `"AI assistant settings"`/`"Enable AI features"`/`"Show AI companion"`/`"AI 助手设置"` 等字符串 | 否（**负向断言**，`expect(...).toBeNull()`，是有意保留的回归护栏） | 保留 |

---

## §1 AI 残留扫描（逐条给文件:行号 + 原文 + 判定）

### 1.1 决定性命令与结果

```powershell
git grep -n -E 'AIService|NewAIService|aiService|AIEnabled|ai_enabled|AIAssistant|aiAvailability|companionExport|companion_export|ExportCompanionFile|create-layered-skin|ask-ai|hypomux:ai-' -- ':(exclude)reports'
```

结果（唯一命中，1 行）：

- `docs/validation/release-review-2026-09-26.md:54`：``- 设置页只在激活/重试时读取配置，没有监听 `hypomux:ai-changed`。…``
  **判定：假命中（历史文档正文）**。它是在描述当时的行为，不是代码引用；该文件属历史验证记录，未在本轮改动。

```powershell
git grep -n -E 'components/ai|platform/ai|state/aiAvailability' -- ':(exclude)reports'
```

结果：**无命中（exit 1）** ⇒ 已删除目录/模块没有任何残留引用。

```powershell
git grep -n -E 'live2d|muxskin|SkinWardrobe|create-layered-skin' -- desktop/frontend/src desktop/internal engine protocol docs README.md README_EN.md .github desktop/frontend/package.json desktop/frontend/pnpm-workspace.yaml
```

结果（6 行，全部为预期保留项）：

| 位置 | 原文（节选） | 判定 |
|---|---|---|
| `.github/release-notes/v2.7.0.en.md:21` | `Added the Mux wardrobe to import, preview, apply, and export \`.muxskin\`/ZIP packages…` | 已发布版本的历史发布说明，允许保留 |
| `.github/release-notes/v2.7.0.md:21` | `新增「小 Mux 衣柜」，可导入、预览、应用和导出 \`.muxskin\`／ZIP 皮肤…` | 同上 |
| `desktop/frontend/package.json:18` | `"pixi-live2d-display": "0.4.0",` | 契约要求保留的依赖（源码零引用，见 F-7） |
| `desktop/frontend/pnpm-workspace.yaml:4` | `"pixi-live2d-display>gh-pages": "^6.3.0"` | 同上（orverride，保持锁文件一致） |
| `docs/validation/release-readiness-fixes-2026-09-27.md:39` | `通过定向 override 更新 \`pixi-live2d-display\` 引入的 \`gh-pages\`…` | 历史验证文档 |
| `docs/validation/release-review-2026-09-27.md:80` | `\| \`pixi-live2d-display → gh-pages@4.0.0\` \| critical 原型污染公告…` | 历史验证文档 |

### 1.2 其余关键词

- `ai_enabled` / `AIService` / `aiService` / `AIAssistant` / `companionExport` / `aiAvailability` / `hypomux:ai-`：在 `desktop/`、`engine/`、`protocol/` 内**零命中**（唯一例外是 1.1 的历史文档行）。
- `AIA`（`desktop/internal/platform/updater_authenticode_windows.go:114` `// offline check above with online CRL/AIA retrieval enabled.`）：**假命中**，AIA = Authority Information Access。
- `README.md`：AI 相关命中仅为作者自述"高频使用 AI 工具辅助重构"（`:230`）与虚拟网卡功能文案，**无 AI 助手功能宣传**。
- `README_EN.md`：`(?i)ai assistant|live2d|pixi|muxskin|companion export|ai_enabled` 仅 1 命中且无关：`:97` `| System Proxy | … | no virtual adapter |`。
- `legacy.messages.json:72` / `:416`：赞助文案里提到"AI 工具"/"AI tools"，**合法**。
- 扫描产物：`desktop/internal/services/ai_*.go`（13 个文件）、`desktop/internal/platform/wails/companion_export{,_test}.go`、`desktop/frontend/src/components/ai/**`（31 个文件）、`platform/{ai,companionExport}.ts(+test)`、`state/aiAvailability.ts`、`docs/AI_{ASSISTANT,FEATURE_SWITCH,PROVIDER_COMPATIBILITY}.md`、`desktop/scripts/create-layered-skin.py`、`desktop/frontend/public/{live2d,skins}/**` 均已在工作树删除（`git status` 中为 ` D`）。

---

## §2 悬挂引用（删掉的每个文件的引用点是否清干净）

| 被删对象 | 检查命令 | 结果 |
|---|---|---|
| `desktop/internal/services/ai_*.go`（`AIService`/`NewAIService`/`AIConfig`/MCP/tools/secrets…） | `git grep -n -E 'AIService\|NewAIService\|aiHotspotSummary'`（排除 reports） | **零命中**（deleted 文件本身不在工作树） |
| `desktop/internal/platform/wails/companion_export{,_test}.go` | `git grep -n -E 'ExportCompanionFile\|companion_export\|CompanionExport'` | **零命中** |
| `desktop/frontend/src/components/ai/**`（31 文件） | `git grep -n -E 'components/ai'` | **零命中** |
| `desktop/frontend/src/platform/ai.ts` | `git grep -n -E 'platform/ai\|aiService\|aiEnabled'` | **零命中** |
| `desktop/frontend/src/platform/companionExport.ts(+test)` | `git grep -n -E 'companionExport'` | **零命中** |
| `desktop/frontend/src/state/aiAvailability.ts` | `git grep -n -E 'state/aiAvailability\|aiAvailability'` | **零命中** |
| `desktop/scripts/create-layered-skin.py` | `git grep -n -E 'create-layered-skin\|public/skins\|live2d'`（源码/构建/NSIS/workflow 面） | 仅 1.1 表格中的 6 行预期保留项 ⇒ 无脚本引用 |
| `desktop/frontend/public/live2d/**`、`public/skins/**` | 同上 + `grep` 于 `desktop/{build,.github,vite.config*,index.html}` 面 | **零引用** ⇒ 删除不会断构建 |

**已修掉的一处真实悬挂引用（正面证据）**：`desktop/internal/services/hotspot_test.go` 的 diff 删除了对 `aiHotspotSummary()` 的调用（该函数随 `ai_tools.go` 一起删除）。若不删，`desktop/internal/services` 无法编译——本机 `go -C desktop test ./...` 已经**编译并通过**该包（56.538s，`ok`），反证清理到位。

---

## §3 方法名四方（实为七方）一致

### 3.1 一致性表

| 层 | 文件:行号 | 证据 | 判定 |
|---|---|---|---|
| 协议清单 | `protocol/v1/manifest.json:75-92` | `{"name":"vnic.create","idempotency":"state_guarded","privilege":"administrator","cancellation":"caller_deadline_and_host_context"}`、`{"name":"vnic.status","idempotency":"read_only","privilege":"none","cancellation":"not_applicable"}`、`{"name":"vnic.remove","idempotency":"safe_retry","privilege":"administrator","cancellation":"caller_deadline_and_host_context"}` | ✓ 与冻结契约 §2.1 逐字一致 |
| 位置/顺序 | `protocol/v1/manifest.json:70`(`tun.deactivate`) → `:75-92`(vnic×3) → `:94`(`dns.resolve`) | 1-based 第 11/12/13 个方法，0-based 10/11/12 | ✓ 符合"紧跟 tun.deactivate、dns.resolve 之前" |
| 引擎常量 | `engine/internal/api/v1/types.go:30-34` | `:30 MethodTunDeactivate`、`:31 MethodVNICCreate = "vnic.create"`、`:32 MethodVNICStatus = "vnic.status"`、`:33 MethodVNICRemove = "vnic.remove"`、`:34 MethodDNSResolve` | ✓ 同名同序 |
| 引擎能力表 | `engine/internal/api/v1/types.go:50-72` | `var capabilities = []string{… MethodTunDeactivate, MethodVNICCreate, MethodVNICStatus, MethodVNICRemove, MethodDNSResolve …}`（21 项） | ✓ 与 manifest 21 项同序（由 `contract_test.go:64-73` 的 `slices.Equal(methods, Capabilities())` 断言，本机测试通过） |
| 引擎派发 | `engine/internal/server/server.go:224-229` | 三个 `case api.MethodVNICCreate → s.createVNIC(ctx, request)` / `MethodVNICStatus → s.statusVNIC(request.ID)` / `MethodVNICRemove → s.removeVNIC(ctx, request.ID)` | ✓ |
| 契约测试 | `engine/internal/api/v1/contract_test.go:214-215`、`:229-230`、`:269-272` | create↔`VNICCreateParams`；status/remove 归入"request 不得带 params"组；create↔`VNICCreateResult`、status/remove↔`VNICStatus` | ✓ |
| 桌面客户端 | `desktop/internal/engineclient/vnic.go:11-13` | `MethodVNICCreate = "vnic.create"` 等三条 + `:52-79` 三个 `c.Request` 包装 | ✓ |
| 桌面服务/前端 | `desktop/internal/services/virtual_adapter.go:55-57`、`desktop/frontend/src/platform/services.ts:350-354` | `virtualAdapter: { create: (interfaceName, address) => VirtualAdapterService.Create(interfaceName, address), status: …, remove: … }` | ✓ |
| **真实生成的绑定** | `desktop/frontend/bindings/.../internal/services/virtualadapterservice.ts:23,30,39,48` | `Create(interfaceName: string, address: string): $CancellablePromise<$models.VirtualAdapterStatus>`、`Remove()`、`Shutdown(): …<void>`、`Status()` | ✓（本机用 CI 同版本 `wails3` 真实生成，非推断） |

### 3.2 error_codes 仍为 14 个（未改动）

测量命令与结果：

```powershell
$m = Get-Content protocol/v1/manifest.json -Raw
[regex]::Match($m,'"error_codes"\s*:\s*\[(.*?)\]','Singleline').Groups[1].Value 中的 "xxx"
# count=14
# invalid_json, unsupported_protocol, invalid_request, method_not_found, invalid_params, invalid_state,
# unsupported_mode, start_failed, stop_failed, dns_failed, tun_failed, security_policy_rejected,
# wfp_unavailable, elevation_required
```

```powershell
git diff --stat -- protocol/
#  protocol/v1/fixtures/messages.json | 96 ++++++++++++++++++++++++++++++++++++++
#  protocol/v1/manifest.json          | 18 +++++++
#  2 files changed, 114 insertions(+)      ← 0 删除 ⇒ error_codes 块未被触碰
```

### 3.3 fixtures 匹配

- 新增 6 个 vnic fixture（`protocol/v1/fixtures/messages.json`，+96 行、0 删除）：`vnic_create_request`（params 含 `executable/config_path/config_sha256/startup_timeout_ms:20000/interface_name:"HypoMux-VNIC"/address:"10.66.0.1"/prefix_length:24/mtu:1420`）、`vnic_create_response`（`accepted:true` + `vnic.state:"present"`、`created_at:"2026-07-30T02:12:05Z"`）、`vnic_status_request`（无 params）、`vnic_status_response`、`vnic_remove_request`（无 params）、`vnic_remove_response`（`state:"absent"`）。
- `engine/internal/server/server.go:937-952` 把 `CreatedAt` 用 `status.CreatedAt.UTC().Format(time.RFC3339)` 输出，与 fixture 的 `"2026-07-30T02:12:05Z"` 形态一致。
- **F-3**：`fixtures/messages.json:41-60` 的 `engine_hello_response.result.capabilities` 只有 18 项，缺 `engine.scheduling`、`steam_cdn.configure`、`mtu.set`（实测：manifest 21 项，fixture 18 项，缺失项由脚本算出并列出）。`contract_test.go` 对该 result 只做反序列化，不做集合比对，故不阻塞 CI。

---

## §4 前端引用完整性（HomePage 新按钮链路逐符号核对）

| 检查项 | 证据 | 判定 |
|---|---|---|
| HomePage 导入面板 | `desktop/frontend/src/pages/HomePage.tsx:26` `import { VirtualAdapterPanel } from "../components/vnic/VirtualAdapterPanel";` | ✓ |
| HomePage 挂载与 props | `HomePage.tsx:249-254`：`locale={locale}` / `text={text}` / `notifySuccess={notifySuccess}` / `notifyError={notifyError}` | ✓ 与面板 props 定义逐项匹配 |
| 面板 props 定义 | `VirtualAdapterPanel.tsx:27-33`：`locale: "zh" \| "en"`、`text: (zh, en) => string`、`notifySuccess: (msg) => void`、`notifyError: (msg, retry?) => void`、`disabled?`、`className?` | ✓ 必填 4 项全给，可选 2 项省略合法 |
| `notifySuccess/notifyError` 来源 | `HomePage.tsx:57` `const notifyError = useCallback((message: string, retry?: () => void) => {…})`、`:67` `const notifySuccess = useCallback((message: string) => {…})` | ✓ 类型签名与 props 一致 |
| `text`/`locale` 来源 | `HomePage.tsx:98` 起的作用域内既有变量（与 HomePage 其它文案同源） | ✓ |
| 服务门面 | `VirtualAdapterPanel.tsx:15` `import { appServices, withServiceTimeout, type VirtualAdapterStatus } from "../../platform/services";`；`services.ts:350-354` `virtualAdapter: { create, status, remove }` | ✓ |
| 超时工具签名 | `services.ts:253` `export async function withServiceTimeout<T>(`（3 参：request/timeoutMs/operation）；面板 `:162-166`、`:187-191` 三参调用 | ✓（`tsc` exit 0 实证） |
| 类型别名 | `services.ts:24` 从 bindings `models` 引入 `VirtualAdapterStatus as GeneratedVirtualAdapterStatus`；`:73` `export type VirtualAdapterStatus = GeneratedVirtualAdapterStatus;` | ✓ 与真实生成文件 `.../services/models.ts:506-515` 对得上 |
| CSS | `VirtualAdapterPanel.tsx:16` `import "./vnic.css";`；`vnic.css` 定义 `virtual-adapter-*` 11 条选择器；面板用到的 `hm-card`(`app.css:143`)、`section-kicker`(`app.css:1072`)、`network-section-heading`(`app.css:1387`)、`network-section-title-row`(`app.css:1400`)、`network-section-actions`(`app.css:1449`) 全部有定义；用到的 token（`--hm-danger`/`--hm-outer-border`/`--hm-radius`/`--hm-row-hover`/`--hm-text-primary`/`--hm-text-tertiary`）在 `app.css` + `theme/*.css` 有定义 | ✓ 无未定义类、无未定义 token |
| 组件测试 | `desktop/frontend/src/components/vnic/VirtualAdapterPanel.test.tsx`（`vi.mock("../../platform/services")` 提供 `virtualAdapter` 与 `withServiceTimeout: (request) => request`）→ 本机 vitest：`✓ src/components/vnic/VirtualAdapterPanel.test.tsx (9 tests) 2583ms` | ✓ |
| 主页回归 | 本机 vitest：`✓ src/pages/HomePage.test.tsx (17 tests) 4301ms` | ✓ |

---

## §5 类型与 json tag 一致（含真实 Wails 生成物）

### 5.1 引擎 DTO（snake_case）

- `engine/internal/api/v1/types.go:289-298` `VNICCreateParams` 的 tag：`executable/config_path/config_sha256,omitempty/startup_timeout_ms/interface_name/address/prefix_length/mtu`；`:300-308` `func (p VNICCreateParams) Config() tun.Config`（额外映射 `InterfaceName`）；`:312-321` `VNICStatus`：`state/interface_name/address/prefix_length/mtu/adapter_guid,omitempty/created_at,omitempty/last_error,omitempty`；`:323-326` `VNICCreateResult{Accepted bool \`json:"accepted"\`; VNIC VNICStatus \`json:"vnic"\`}`。
- 与 `desktop/internal/engineclient/vnic.go:19-47` 的同名结构体 tag **逐字一致**（跨进程 JSON 边界不会丢字段）。
- 与 `protocol/v1/fixtures/messages.json` 的 vnic fixture 字段名一致（§3.3）。

### 5.2 桌面 → 前端（camelCase）

- `desktop/internal/services/virtual_adapter.go:32-41` `VirtualAdapterStatus` 八个字段 tag：`state/interfaceName/address/prefixLength/mtu/adapterGuid/createdAt/lastError`（**无 `omitempty`**，与契约 §3 逐字一致）。
- **本机真实生成验证**（不是推断）：`wails3 generate bindings -clean=true -ts -i`（v3.0.0-alpha2.119）输出 `desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/virtualadapterservice.ts`（50 行，含 `Create/Remove/Shutdown/Status` 四个导出）与 `.../services/models.ts:506-515`：

```ts
export interface VirtualAdapterStatus {
    "state": string;
    "interfaceName": string;
    "address": string;
    "prefixLength": number;
    "mtu": number;
    "adapterGuid": string;
    "createdAt": string;
    "lastError": string;
}
```

- 面板读取的正是这些小驼峰名（`status?.state/interfaceName/address/prefixLength/mtu/createdAt/adapterGuid/lastError`），`services.ts:7` 的导入路径也与生成路径逐字一致 ⇒ **无需手写 bindings**，CI 生成后前端可编译（`tsc` exit 0 实证）。
- Wails 命名规则实证：既有 `TunService → tunservice.ts`，新 `VirtualAdapterService → virtualadapterservice.ts`（本机生成目录中同时存在两者）。

---

## §6 旧 settings.json（含 `"ai_enabled": true`）仍能加载

- `desktop/internal/services/settings.go:491-494`：

```go
var loaded AppSettings
if err := json.Unmarshal(data, &loaded); err != nil {
    return fmt.Errorf("设置文件格式无效：%w", err)
}
```

- `json.Unmarshal` **未使用 `DisallowUnknownFields`**（全仓只有 `desktop/internal/services/updater.go:267` 用了它，与 settings 无关）⇒ 旧配置里遗留的 `"ai_enabled": true` 会被**静默忽略**，文件照常加载，**不存在"旧配置无法启动/设置读取失败"的破坏性兼容问题**。
- `:496-499` 仍然把整份 JSON 解到 `map[string]json.RawMessage`，`:506-508` 保留 `hide_virtual_adapters` 键存在性探测（既有功能未受影响）。
- Go 侧删除项：`AppSettings.AIEnabled` 字段、`aiExecution sync.RWMutex`、`aiEnabledChanged` 回调、`commitLocked` 的 aiChanged 分支、`UpdateFields` 的 `case "ai_enabled"`。
- 前端侧同步：`services.ts` 的 `CompleteAppSettings` 由 `Omit<AppSettings, "ai_enabled"> & { ai_enabled?: boolean }` 改为 `AppSettings & { update_channel?: … }`（不再向服务层传 AI 字段），与 Go 侧删除一致。
- 结论：**兼容策略 = 忽略未知字段**（无需迁移代码）；本机 `go -C desktop test ./...` 通过（`internal/services` 56.5s `ok`）覆盖了 settings 相关既有测试。

---

## §7 死代码 / 死 CSS / 死 i18n

| 检查项 | 命令 | 结果 | 判定 |
|---|---|---|---|
| 死 CSS 类 | `grep -E '\.(ai\|assistant\|chat\|skin\|live2d\|pixi\|companion)[-_a-z0-9]*\s*[,{]'` 于 `desktop/frontend/src` | **零命中** | ✓ 无孤儿样式 |
| `app.css` 删除面 | `git diff --numstat` + 选择器扫描 | 只删除 `ai-entry`、`ai-markdown`、`ai-message` 三个选择器，全量 grep 零引用 | ✓ |
| 死 i18n 键 | `legacy.messages.json` 中用 `"[a-z0-9_.]*(ai\|assistant\|companion\|skin\|live2d\|pixi)[a-z0-9_.]*":` 匹配 | 70 条命中全是子串误命中（`status_load_failed`、`diag_status_available`…），**没有任何 AI/皮肤相关键** | ✓ 无死键；且契约要求不新增 i18n 键，面板使用既有内联 `locale === "en" ? … : …` 写法 |
| 死代码 | 删掉的 13 个 `ai_*.go` 的导出符号、前端 31 个 `components/ai/**` 模块 | §2 全部零引用 | ✓ |
| 死忽略规则 | `.gitignore:70-72` | `*.muxskin`、`/desktop/frontend/public/skins/mux-layered-source/` + 注释行 | **F-5（提示）** |
| 陈旧注释 | `theme/material.tokens.css:120`、`hotspot.go:98`、`routing.go:379`、`routing_quick_add_test.go:22,28` | 见 F-6 | **F-6（提示）** |
| 零引用依赖 | `package.json:17-19`、`pnpm-workspace.yaml:4` | 见 F-7（契约要求保留） | **F-7（提示）** |

---

## §8 文档悬空链接

```powershell
git grep -n -E 'AI_ASSISTANT|AI_FEATURE_SWITCH|AI_PROVIDER_COMPATIBILITY' -- '*.md'
```

结果：24 行命中，**全部落在 `reports/**`**（`reports/vnic/10-ai-backend-removal.md`、`reports/HypoMux-项目分析报告.md`、`reports/parts/05-security.md`、`reports/parts/06-health-docs.md`）。

⇒ `README.md`、`README_EN.md` 以及 `docs/**` 中**没有**任何指向已删除 `docs/AI_*.md` 的链接（`reports/vnic/10-ai-backend-removal.md:114-115` 也记录了 README 中两处悬空链接已被删除，与我的独立 grep 一致）。

其余相关扫描：

- `README.md`：`(?i)ai assistant|live2d|pixi|muxskin|assistant` 无功能宣传命中（仅作者自述行 `:230`）。
- `docs/**`：仅 `docs/validation/release-review-2026-09-26.md:54`（`hypomux:ai-changed`）、`docs/validation/release-review-2026-09-27.md:80`、`docs/validation/release-readiness-fixes-2026-09-27.md:39`、`docs/validation/review-since-v2.6.0-2026-09-23.md:62` 等历史验证文档提到 AI/Live2D/pixi —— 历史记录，保留（F-8）。
- `.github/**`：11 行命中全在 `release-notes/v2.7.0{,.en}.md`（已发布版本历史说明）；workflows 中**没有**任何 AI/皮肤/`create-layered-skin.py`/`public/skins` 引用 ⇒ 删文件不会导致 workflow 断链。

**结论：无悬空文档链接。**

---

## §9 无法静态验证 / 本机不可执行的部分

1. **`wails3 task windows:package`（打包成 NSIS 安装包）**：CI 最后一步（build.yml:188-191）未在本机执行。需要完整打包链（syso/NSIS/签名工具），本机未验证。可覆盖的近似证据：`go -C desktop test ./...` 已经编译了 `desktop` 根包（含 `//go:embed all:frontend/dist`），且 `vite build --mode production` 成功产出 `desktop/frontend/dist/`。
2. **真实提权 + 真实 Wintun 适配器行为**（`Create` 真的建出 `HypoMux-VNIC`、`Remove` 幂等、失败重试、主 TUN 与 VNIC 并存时的路由/MTU 行为）：需要管理员权限与真实网卡，属运行时验证，**无静态替代**。本次只验证到"单测覆盖 + 状态机映射自洽"层面：`engine/internal/vnic/manager_test.go`、`engine/internal/server/server_test.go:1022-1312`（fakeTunController）均在本机通过。
3. **CI 运行环境差异**：本机 pnpm `12.8.1`，CI 锁定 `PNPM_VERSION=10.34.5`；本机 Go `go1.26.6` 与 `engine/go.mod` 的 toolchain 一致；runner 为 `windows-2025`（本机 Windows 10.0.19041）。前端三条门禁在本机全绿，但**不能 100% 排除 pnpm 版本差异带来的行为差异**（例如 `--frozen-lockfile` 的校验细节、脚本执行方式）。
4. **`desktop/internal/services` 中涉及真实网络的测试**（如 NAT 探测/STUN）行为依赖外网，本机是在配置了 `GOPROXY` 镜像的情况下跑通，联网路径与 CI 不同；结果只能说明"测试逻辑通过"。
5. **真实 GUI 交互**：前端验证全部在 vitest + jsdom 下完成（`tsc` 与 `vite build` 只证明可编译/可打包），未做人工界面验证。
6. **GitHub Actions 侧的东西**：缓存命中、secret（`GITHUB_TOKEN`）、`gh workflow run`、release-notes 生成、签名模式等，均未在本机验证。

---

## 附录 A · F-1 的完整复现与修复验证

复现（CI 同口径，只统计"提交后仍存在"的 .go 文件）：

```powershell
Set-Location <repo>
$gofmt = '%USERPROFILE%\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.6.windows-amd64\bin\gofmt.exe'
$files = git ls-files '*.go' | Where-Object { ($_ -like 'engine/*' -or $_ -like 'desktop/*') -and (Test-Path $_) }
"file_count=$($files.Count)"            # 332
$unformatted = & $gofmt -l $files
"unformatted_count=$($unformatted.Count)"  # 1
$unformatted                                # desktop/internal/services/settings.go
```

diff（`gofmt -d` 输出）：

```diff
--- desktop/internal/services/settings.go.orig
+++ desktop/internal/services/settings.go
@@ -81,7 +81,7 @@
 }
 
 type SettingsService struct {
-	mu sync.RWMutex
+	mu               sync.RWMutex
 	path             string
 	settings         AppSettings
 	migration        ConfigMigrationStatus
```

归因（确认是本轮引入，不是历史遗留）：

```powershell
git show HEAD:desktop/internal/services/settings.go > $env:TEMP\settings_head.go   # 用 cmd 重定向做字节安全落盘
& $gofmt -l $env:TEMP\settings_head.go    # 无输出 ⇒ HEAD 版本是 gofmt 干净的
```

修复已做字节级验证（**但修复动作不属于我的写入范围，需由 settings.go 的 owner 或 Lead 执行**）：

```powershell
$p = 'desktop\internal\services\settings.go'
$src = [IO.File]::ReadAllText((Resolve-Path $p))
$src = $src.Replace("`tmu sync.RWMutex", "`tmu               sync.RWMutex")
[IO.File]::WriteAllText((Resolve-Path $p), $src, (New-Object System.Text.UTF8Encoding($false)))
# 之后 $gofmt -l $p → 无输出、exit 0；且不影响中文注释（必须用 .NET 写入，PS 5.1 的 Set-Content 会破坏 UTF-8 中文）
```

**注意**：本机 `gofmt` 直接对工作树跑时退出码是 2，原因是索引里还留着 15 个"已删除但未提交"的 `.go`（`ai_*.go`、`companion_export*.go`）；这些文件在 CI 的干净 checkout 中不存在，所以 CI 的失败点只有那 1 个文件。

---

## 附录 B · 本机执行环境与命令全集（可复现）

```powershell
# 0) 工具链
$go = '%USERPROFILE%\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.6.windows-amd64\bin\go.exe'
$env:GOTOOLCHAIN = 'local'
# proxy.golang.org 在本机不可达（dial tcp 142.251.33.209:443 超时），改用可达镜像：
$env:GOPROXY = 'https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct'
$env:GOSUMDB = 'off'
& $go -C desktop mod download github.com/adrg/xdg github.com/pion/stun/v3 golang.org/x/net golang.org/x/image

# 1) 引擎
& $go -C engine test -count=1 ./...          # exit 0（14 包 ok，含 internal/vnic 0.642s）
& $go -C engine vet ./...                    # exit 0
& $go -C engine mod verify                   # all modules verified

# 2) 桌面
& $go -C desktop test -count=1 ./...         # exit 0（10 包 ok，internal/services 56.538s）
& $go -C desktop vet ./...                   # exit 0
& $go -C desktop mod verify                  # all modules verified

# 3) 绑定生成（cwd=desktop，需把 Go 目录加入 PATH，wails3 会调用 go）
& "$env:USERPROFILE\go\bin\wails3.exe" generate bindings -clean=true -ts -i
# → Processed: 306 Packages, 13 Services, 106 Methods, 0 Enums, 47 Models

# 4) 前端（cwd=desktop/frontend）
corepack pnpm install --frozen-lockfile --prefer-offline --config.minimum-release-age=0 --registry=https://registry.npmmirror.com
node_modules\.bin\vitest.CMD run             # 43 files / 243 tests passed
node_modules\.bin\tsc.CMD                    # exit 0
node_modules\.bin\vite.CMD build --mode production   # 2494 modules transformed / built in 7.41s
```

补充说明（避免后人重试踩坑）：

- 本机 `corepack pnpm install --frozen-lockfile --offline` **会失败**（`ERR_PNPM_NO_OFFLINE_META` 于 `@bramus/specificity`，外加 `.npmrc` 的 `minimum-release-age=10080` 触发 `Lockfile failed supply-chain policy check (442 entries)`）；必须以**联网 + `--config.minimum-release-age=0`** 方式安装。
- 直接 `pnpm test`/`pnpm build` 会因 pnpm 的前置依赖校验去访问 `registry.npmjs.org` 并长时间挂起（实测单请求 56s~566s）⇒ 直接调用 `node_modules\.bin\*` 更稳。
- 任何文本回写都要用 `[IO.File]::ReadAllText/WriteAllText` + `UTF8Encoding($false)`：本机 PS 5.1 的 `Set-Content -Encoding utf8` 会破坏含中文的 Go 源文件（gofmt 会报 `string literal not terminated`）。

## 附录 C · 本次验证在工作树留下的产物（全部被 gitignore，可安全删除）

| 产物 | 由谁产生 | 是否被忽略 | 说明 |
|---|---|---|---|
| `desktop/frontend/bindings/**` | 我（模拟 CI 的 bindings 生成） | 是：`desktop/.gitignore:9 /frontend/bindings/` | CI 每次都会 `-clean=true` 重新生成；保留它可让本地 `go -C desktop test ./...` 正常编译根包 |
| `desktop/frontend/dist/**` | 我（先占位 `index.html` 以解除 `desktop/main.go:25` 的 `//go:embed all:frontend/dist`，随后由 `vite build` 覆盖为真实产物） | 是：`desktop/.gitignore:8 /frontend/dist/`、根 `.gitignore:47-48` | CI 先 build 再跑 Go 测试，故 CI 中本来就存在 |
| `desktop/frontend/node_modules/**`（顶层链接与 `.bin`） | 我（`pnpm install`） | 是：根 `.gitignore:42-43`、`desktop/.gitignore:7` | 安装前该目录只有 `.pnpm` 412 个包、无顶层链接；安装后有 412 个链接 |
| `desktop/{engine,}/…` 源码 | **未改动**（已用 `git status --porcelain` 与各文件 diff 核对） | — | 本次唯一写入是 `reports/vnic/90-verification.md` |
