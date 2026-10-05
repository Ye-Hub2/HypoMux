# T7 桌面端（Go）AI 助手模块移除 —— 交付报告

- 任务：共享任务 `task-7`（`ai-backend-remover`）
- 冻结契约：`reports/vnic/00-frozen-interface.md`（§0 全局约束 / §5 AI 移除边界）
- 仓库：`<repo>`（v2.7.0）
- 本机环境：**无 Go 工具链、无 pnpm、无 node_modules、无 `desktop/frontend/bindings/`**
- 验收标准（自我声明）：① `desktop/**` 内不再存在任何 AI 助手 Go 符号/文件/接线；② 旧 `settings.json`（含 `"ai_enabled": true`）仍可正常加载；③ 交互式/契约测试中仅断言"AI 存在于前端"的条目被清理，其余断言结构不变；④ 所有被删符号的引用点均已处理，无悬挂引用；⑤ 交付本报告。

---

## ① 改动 / 删除清单

### 1.1 删除文件（16 个）

`desktop/internal/services/` 下 13 个（含测试）：

| 文件 | 行数（原） |
| --- | --- |
| `desktop/internal/services/ai_service.go` | 437 |
| `desktop/internal/services/ai_tools.go` | 562 |
| `desktop/internal/services/ai_compat.go` | 123 |
| `desktop/internal/services/ai_config.go` | 163 |
| `desktop/internal/services/ai_lifecycle.go` | 79 |
| `desktop/internal/services/ai_mcp.go` | 221 |
| `desktop/internal/services/ai_models.go` | 112 |
| `desktop/internal/services/ai_provider.go` | 202 |
| `desktop/internal/services/ai_secret_windows.go` | 7 |
| `desktop/internal/services/ai_secret_other.go` | 9 |
| `desktop/internal/services/ai_test.go` | 639 |
| `desktop/internal/services/ai_compat_test.go` | 294 |
| `desktop/internal/services/ai_lifecycle_test.go` | 314 |

`docs/` 下 3 个：

- `docs/AI_ASSISTANT.md`（122 行）
- `docs/AI_FEATURE_SWITCH.md`（24 行）
- `docs/AI_PROVIDER_COMPATIBILITY.md`（23 行）

删除方式：`pwsh` 中逐个打印解析后的绝对路径并断言位于 `desktop/internal/services/` / `docs/` 之后才 `Remove-Item`，未使用通配删除。

> 注：`desktop/internal/services/virtual_adapter.go`、`desktop/internal/services/vnic_config.go` **未改动**（另一名成员的在建范围）。

### 1.2 `desktop/main.go`（394 → 388 行，仅删 6 行，无新增）

| 位置（原行号） | 删除内容 |
| --- | --- |
| 原 `:151` | `var aiService *services.AIService` |
| 原 `:152-154` | shutdown 回调内 `if aiService != nil { aiService.Shutdown() }` |
| 原 `:181` | `aiService = services.NewAIService(settingsService, adapterService, engineService, routingService, diagnosticsService, tunService, supportLogs)` |
| 原 `:182` | `app.RegisterService(application.NewService(aiService))` |

未受影响、按原样保留（已逐行复核 `desktop/main.go:130-191`）：

- `--core-service-self-test`（`:32`）、`--recover-network`（`:41`）、`StartupError` 分支（`:124-134`）
- 关闭回调 `desktop/main.go:151-158`：仅剩 `diagnosticsService.Shutdown()`（`:152-153`）与 `engineService.Shutdown()`（`:155`）
- 服务注册块 `desktop/main.go:177-188`：`desktop`/`settingsService`/`adapterService`/`engineService`/`routingService`/`ruleSetService`/`diagnosticsService`/MTU/`tunService`(`:185`)/`blockedDomainService`/`updaterService`/`appearanceService`

### 1.3 `desktop/internal/services/settings.go`（874 → 842 行）

| 位置（原行号） | 删除内容 | 现位置 |
| --- | --- | --- |
| 原 `:24` | `AppSettings.AIEnabled bool \`json:"ai_enabled"\`` 字段 | `AppSettings` 现起于 `:23`，首字段为 `RoutingMatchOrder`（`:24`） |
| 原 `:64` | `DefaultSettings()` 内 `AIEnabled: true,` | `:60` |
| 原 `:87-89` | `aiExecution sync.RWMutex` 及其两行注释 | `SettingsService` 现仅剩 `mu sync.RWMutex`（`:83`） |
| 原 `:97-98` | `aiEnabledChanged func(bool)` 及其注释（变更回调） | — |
| 原 `:184-185` | `MigrateLegacy()` 内 `s.aiExecution.Lock()` / `defer ...Unlock()` | 函数现 `:176` |
| 原 `:200-201` | `// Network migration must not opt a user back into AI.` + `migrated.AIEnabled = s.settings.AIEnabled` | — |
| 原 `:228-229` | `RollbackLegacyMigration()` 内 aiExecution 加锁 | 函数现 `:216` |
| 原 `:260` | `restored.AIEnabled = s.settings.AIEnabled` | — |
| 原 `:272-273` | `Update()` 内 aiExecution 加锁 | 函数现 `:257` |
| 原 `:282-288` | `UpdateFields()` 内针对 `"ai_enabled"` 的预加锁循环 | 函数现 `:265` |
| 原 `:294-295` | `UpdateFields()` 的 `case "ai_enabled": next.AIEnabled = values.AIEnabled` | — |
| 原 `:534-536` | `reload()` 内 `if _, exists := storedFields["ai_enabled"]; !exists { loaded.AIEnabled = defaults.AIEnabled }` | 函数现 `:461` |
| 原 `:784-788` | `commitLocked()` 内 `aiChanged := ...` 与 `s.aiEnabledChanged(...)` 回调调用 | 函数现 `:732` |

### 1.4 测试中的 AI 断言清理

**`desktop/internal/services/hotspot_test.go`（`TestHotspotSessionConfigIsBoundToActiveSession`）**

该测试原本遍历两个 JSON 面（`s.HotspotStatus()` 与已随 `ai_tools.go` 删除的 `aiHotspotSummary(...)`）断言凭据不外泄，是**删除 AI 文件后会直接导致编译失败的悬挂引用**（见 ③ 扫描 6）。已改为只检查幸存的 JSON 面：

```
-	for _, value := range []any{s.HotspotStatus(), aiHotspotSummary(s.HotspotStatus())} {
-		data, err := json.Marshal(value)
-		if err != nil || strings.Contains(string(data), config.Password) || strings.Contains(string(data), "password") {
-			t.Fatal("session credentials leaked into status")
-		}
-	}
+	data, err := json.Marshal(s.HotspotStatus())
+	if err != nil || strings.Contains(string(data), config.Password) || strings.Contains(string(data), "password") {
+		t.Fatal("session credentials leaked into status")
+	}
```

现位置 `desktop/internal/services/hotspot_test.go:183-186`。`json`/`strings` 两个 import 仍被使用。**删除后该测试的凭据外泄断言强度有所下降**（原先还覆盖 AI 工具层的摘要投影），已作为未验证项/风险列出。

**`desktop/installer_layout_test.go`（`TestHomeEngineStatePersistsAcrossPageNavigation`，package main 静态契约测试）**

按 Lead 裁定授权修改，仅删除两条"要求前端存在 AI 接线"的 required 字符串（原 `:635-636`）：

```
-		`page !== persistentPage && page !== "assistant" ? (`,
-		`hidden={aiEnabled === true && page === "assistant"}`,
```

required 列表现值 `desktop/installer_layout_test.go:627-635`，末条为 `hidden={page !== persistentPage}`（`:634`）。该测试的其余断言结构（`:628-633`、`:636-639`、`:640` 起的否定断言）未改动。

### 1.5 README

| 文件:行号 | 改动 |
| --- | --- |
| `README.md:24`（删除） | 删除 "2.7.0 新版本" 小节中 `- **AI 助手与小 Mux**：内置工具调用助手、本机 MCP 连接，以及图片、分层和 Live2D 伴侣皮肤。` |
| `README_EN.md:22`（删除） | 删除 `- **AI assistant and Mux**: An integrated tool-calling assistant, local MCP connections, and image, layered, and Live2D companion skins.` |
| `README.md:60` | "隐私政策" 段删除 `使用可选的内置 AI 助手时…返回连接的 AI 客户端。API 密钥与对话历史在本机加密保存，详见 [AI 助手与外部 MCP](docs/AI_ASSISTANT.md)。`（悬空链接） |
| `README_EN.md:59` | 同段落英文对应句与 `[AI assistant and external MCP](docs/AI_ASSISTANT.md)` 链接一并删除 |

中英两份结构与编号保持一致，README 的后续小节标题/编号未受破坏（两个 hunk 均为单行替换/删除）。

> **判断说明（需 Lead 确认）**：删 `README.md:24` / `README_EN.md:22` 是我的判断，超出了"只清理悬空链接"字面范围。理由：该条挂在当前版本 "2.7.0 新版本" 小节下，是对外声称产品现有的能力，而本次改动后产品已无 AI 助手与小 Mux 皮肤；若 Lead 希望把它当历史记录保留，回滚这 2 行即可（`.github/release-notes/**` 的历史发布说明我没有动）。

### 1.6 交付物

- 本文件 `reports/vnic/10-ai-backend-removal.md`

---

## ② 旧配置兼容策略与证据

**结论：采用"直接删除字段"，不保留 deprecated 字段、不做预过滤。旧的 `settings.json`（含 `"ai_enabled": true`）仍能正常加载，该键在下次写回时自然消失。**

证据链（全部为现存文件的行号）：

1. **读取是宽松解码，未知键被忽略。**
   - `desktop/internal/services/settings.go:492`：`if err := json.Unmarshal(data, &loaded); err != nil {` —— 标准库 `encoding/json` 默认忽略结构体中不存在的键。
   - 全仓 `desktop/**/*.go` 中 `DisallowUnknownFields` **只有一处**：`desktop/internal/services/updater.go:267`（`decoder.DisallowUnknownFields()`，用于校验更新元数据 JSON），与 settings 的加载/迁移路径无关。
   - 因此 `"ai_enabled": true` 不会被当作"未知键错误"，也不会影响其他字段的解析。
2. **删除的"补默认值"只影响 AI 自身。**
   - `reload()` 用 `storedFields map[string]json.RawMessage`（`settings.go:496-497`）做"键是否存在"探测，仅用于 `system_proxy_takeover`（`:503`）与 `hide_virtual_adapters`（`:506`）；原 `ai_enabled` 探测（原 `:534-536`）删除后不影响其他键的探测与补齐。
   - `RollbackLegacyMigration()` 保留同样的探测逻辑（`settings.go:231-238`）。
3. **不会残留脏键。**
   - 下一次 `commitLocked`（`settings.go:732`）→ `writeSettingsFile`（`settings.go:760`）会把整个 `AppSettings` 重新序列化写出，`ai_enabled` 因结构体中已无该字段而自然消失。
4. **迁移/回滚不再强制保留 AI 开关**（原 `migrated.AIEnabled = s.settings.AIEnabled`、`restored.AIEnabled = s.settings.AIEnabled` 已删），这是"可选 AI 功能开关"语义整体消失的一部分，不产生新的兼容问题。

**残留兼容层（前端，非我范围）**：`desktop/frontend/src/platform/services.ts:75-76` 仍写着

```ts
export type CompleteAppSettings = Omit<AppSettings, "ai_enabled"> & {
  ai_enabled?: boolean;
```

这是前端成员为容忍后端旧 JSON 而加的兼容声明；后端字段删除后它已成为无用层（不影响运行，TS 侧类型仍自洽）。未改动，交由前端/T6 决定是否清理。

---

## ③ 残留扫描：命令与结果

工作目录 `<repo>`。

| # | 命令（要点） | 结果 |
| --- | --- | --- |
| 1 | `git grep -n -E 'AI_ASSISTANT\|AI_FEATURE_SWITCH\|AI_PROVIDER_COMPATIBILITY\|AIService\|NewAIService\|AIEnabled\|ai_enabled\|AIConfig' -- .` | 仅 2 行：`desktop/frontend/src/platform/services.ts:75-76`（前端范围，见 ② 末）。Go 侧 **0 命中** |
| 2 | PowerShell `Select-String` 扫描 `desktop/**/*.go`（排除 `frontend/`）匹配 40 个 AI 符号（`AIService`、`aiService`、`NewAIService`、`AIConfig`、`AISnapshot`、`AIEntry`、`AIModel`、`AIMCP*`、`aiHotspotSummary`、`aiManualFeatures`、`redactAIText`、`parseAIArguments`、`executeAITool`、`protectAIData`、`TestAI*` …） | **NO MATCHES** |
| 3 | `Get-ChildItem desktop -Recurse -Filter 'ai_*'` | **NONE**（目录中已无任何 `ai_*` 文件） |
| 4 | `Get-ChildItem docs -Recurse -Filter 'AI_*'` | **NONE** |
| 5 | 全仓（排除 `.git`/`node_modules`/`reports`/`dist`/`build`）匹配 `AI_ASSISTANT\|AI_FEATURE_SWITCH\|AI_PROVIDER_COMPATIBILITY\|ai_enabled` | 仅 `desktop/frontend/src/platform/services.ts:75-76` |
| 6 | **悬挂符号分析**：`git grep -E '^(func\|type\|var\|const) ' HEAD -- 'desktop/internal/services/ai_*.go'` 提取被删文件的全部 **116 个顶层声明名**，再在现存的 **204 个 `desktop/**/*.go`** 中找"现存零声明但有引用"的名字 | 仅 2 个候选，**均为假阳性**：<br>① `Config` → `desktop/internal/services/tun_connectivity.go:346,521` 的 `&tls.Config{`（`crypto/tls` 包选择器）；<br>② `approve` → `desktop/installer_layout_test.go:420` 反引号字符串 `` `git credential approve` ``。<br>⇒ **真实悬挂引用 0 个** |
| 7 | `desktop/**/*_test.go`（88 个，排除 frontend）匹配 `assistant\|aiEnabled\|ai_enabled\|AIService\|hypomux:ai\|pixi\|live2d\|companionExport\|skin` | 仅 `desktop/internal/platform/wails/companion_export_test.go`（见 ④ 跨范围项 1），**无 AI 接线断言残留** |
| 8 | camelCase 标识符扫描 `\b(ai\|Ai)[A-Z]`（`desktop/**/*.go`，排除 frontend） | **NO MATCHES**（修复 `hotspot_test.go` 之前此处有 1 处 `aiHotspotSummary`） |
| 9 | `desktop/**/*.go` 中大小写敏感的 `AI\|ai_` 全量扫描 | 全部为假阳性（`WAILS_INSTALL_SCOPE`、`__WAILS__`、`windows.WAIT_*`、`DOMAIN-SUFFIX` 等）＋ 3 处注释/2 处测试字符串（见 ④ 跨范围项 5） |
| 10 | `desktop/main.go` 静态结构：行数 394→388；`{`=83 / `}`=83；`(`=166 / `)`=166；18 个 import 全部仍被引用（各 ≥1 次）；9 个局部变量仍被引用（`tunService` 2、`supportLogs` 4、`diagnosticsService` 5、`routingService` 2、`ruleSetService` 2、`engineService` 6、`adapterService` 8、`settingsService` 16、`updaterService` 2） | 无未使用变量/import，无括号失衡 |
| 11 | `desktop/internal/services/settings.go` 结构：行数 874→842；10 个 import 全部仍被引用（`json` 10、`errors` 9、`fmt` 31、`net` 1、`os` 23、`filepath` 8、`strings` 4、`sync` 1、`desktopplatform` 2）；函数 `MigrateLegacy` `:176`、`RollbackLegacyMigration` `:216`、`Update` `:257`、`UpdateFields` `:265`、`reload` `:461`、`commitLocked` `:732`、`writeSettingsFile` `:760` 均连续无残缺 | 无未使用 import，无语法残缺 |
| 12 | `git diff --stat` 中属我范围的 6 个文件 | `desktop/main.go` −6；`desktop/internal/services/settings.go` −32；`desktop/installer_layout_test.go` −2；`desktop/internal/services/hotspot_test.go` ±8（净 −2）；`README.md` ±2；`README_EN.md` ±2 —— 全部为纯删除，**无新增逻辑** |

---

## ④ 未验证项与跨范围发现

### 4.1 未验证项（明确声明）

- **本机无 Go 工具链，未编译、未 `go build`、未 `go vet`、未运行任何测试；本机无 pnpm/node_modules/`desktop/frontend/bindings/`，未跑前端构建或 TS 类型检查。** 上述全部结论均来自逐行静态审查与文本扫描，不能替代编译与测试。
- 因此以下内容**未经机器验证**，必须由有 Go 环境的 T6 集成验证员确认：
  1. `desktop/main.go` / `desktop/internal/services/settings.go` 编译通过（我仅做了括号/import/变量 3 项静态检查）。
  2. `go test ./desktop/...` 全绿，尤其 `installer_layout_test.go` 的 `TestHomeEngineStatePersistsAcrossPageNavigation` —— 其**保留**的 required 字符串（`desktop/installer_layout_test.go:628-634`，含 `hidden={page !== persistentPage}`）是否仍出现在 ai-frontend-remover 产出后的 `App.tsx` / `AppShell.tsx` 中，取决于前端最终实现。
  3. `gofmt` 一致性：我未运行 `gofmt`，但所有改动都是整行删除，未改动缩进层级；`git diff` 中未见异常空白（仅纯删除 hunk）。
  4. 旧 `settings.json`（含 `"ai_enabled": true`）真实加载的行为，我依据的是标准库 `encoding/json` 的宽松解码语义（`settings.go:492`，且 `DisallowUnknownFields` 仅出现在 `updater.go:267`），**未做运行时验证**。
  5. `hotspot_test.go` 凭据外泄断言的覆盖范围因删除 AI 摘要投影而收窄（见 1.4），该断言仍能通过，但**语义强度下降**，请确认可接受。
  6. `desktop/installer_layout_test.go` 中是否还有其他"要求 AI 存在"的 required 字符串：我按 Lead 清单用了 `assistant\|aiEnabled\|ai_enabled\|AIService\|hypomux:ai\|pixi` 等关键词扫描（扫描 7），除已删两条外无命中；但该测试大量断言前端源码字符串，若前端成员改了其他相关片段，仍可能出现新的不匹配。

### 4.2 跨范围发现（均未改动，交 Lead / T6 决策）

1. **`desktop/internal/platform/wails/companion_export.go`（+ `companion_export_test.go`，非我的 write scope）**：前端 `desktop/frontend/src/platform/companionExport.ts` 已被前端成员删除，Go 侧的 `decodeCompanionExport`(`:16`) / `ExportCompanion`(`:37`) / `writeCompanionExport`(`:59`) 很可能已无调用方。文件本身仍能编译、测试自洽（只校验路径穿越与 base64/大小上限，不依赖 AI 概念），故我未触碰。若需彻底清理，建议新开一条任务给平台层 owner。
2. **`.github/release-notes/v2.7.0.md:3,5,7,12,102` 与 `.github/release-notes/v2.7.0.en.md:3,5,12`**：v2.7.0 已发布版本的历史发布说明，含 "AI 助手与外部 MCP" 小节。属历史记录，**保留**（`.github/**` 亦不在我的范围）。
3. **`docs/validation/release-review-2026-09-27.md:68`、`docs/validation/review-since-v2.6.0-2026-09-23.md:30`**：历史验证记录中提及 AI 助手/外部 MCP。属历史记录，**保留**。
4. **`desktop/frontend/public/skins/SKIN_SPEC.md:3,114`**：仍写着 "入口：AI 助手 → 外观 → 小 Mux 衣柜" / "Open AI assistant → Appearance"，指向已移除的入口。属前端范围（`desktop/frontend/**`），我未改动。
5. **仅注释/测试字符串提及 "AI"，无符号依赖，我未改（避免无意义 diff）**：
   - `desktop/internal/services/hotspot.go:98`：`// Keep credentials out of telemetry and AI tools.`（该约束对 telemetry 仍成立）
   - `desktop/internal/services/routing.go:379`：`// SaveOrderedChecked prevents a stale desktop draft from overwriting external AI edits.`
   - `desktop/internal/platform/wails/companion_export.go:35`：`// ... is not registered as an AI/MCP tool.`
   - `desktop/internal/services/routing_quick_add_test.go:22,28`：测试夹具字符串 `"AI-added.exe"` / `"stale quick add overwrote AI rules"`（模拟"并发外部写入方"，该场景本身仍然有效，只是命名沿用了 AI）
6. **`docs/migration/**`**：扫描后**无**任何 AI 相关提及（`AI 助手|AI_ASSISTANT|ai_enabled|AIService|AI 功能` 全部 0 命中），无需处理。
7. **前端测试断言 AI 元素不存在**（`desktop/frontend/src/components/shell/CompactNavigation.test.tsx:12`、`desktop/frontend/src/pages/SettingsPage.test.tsx:81,91,92` 用 `queryByRole(...)` 断言为 `null`）：AI 移除后这些断言语义仍成立，属前端范围。

---

## ⑤ 交付状态

- `task-7`：完成（见共享任务板状态）。
- 我的 write scope 内 6 个文件 + 16 个删除文件已完成；`desktop/frontend/**`、`engine/**`、`.github/**`、`protocol/**`、`desktop/internal/services/virtual_adapter.go`、`desktop/internal/services/vnic_config.go` 均未触碰。
- 未执行任何 `git commit` / `git push`（全程仅用只读的 `git status` / `git diff` / `git grep` / `git show` 做核对）。
