# 批量修改网卡 MTU —— 独立验证报告

- 验证者：verify-mtu（独立于 backend-mtu / frontend-mtu / docs-mtu）
- 目标契约来源：`task-4` 冻结描述 + `docs/MTU_DETECTION.md`（docs-mtu 的批量段落）
- 写范围：仅本文件（`reports/`）。**未修改任何源码或文档**；自建的临时测试文件用完已删除（见 §6）。
- 报告日期：2026-10-06（F1/F2 修复复测：2026-10-06 21:40）
- 当前阶段：**全部完成** —— 阶段一清单（§0/§1）、阶段二判定（§1/§2/§3）、Lead 裁决与 F1/F2 修复复测（§4.1）、总体判定与残余风险（§5）

## 被验证的字节（钉住 revision）

| 文件 | 行数 | SHA256(前 16) |
| --- | --- | --- |
| `desktop/internal/services/mtu.go` | **521**（复测版；修复前 517） | **51DE56112CA24F7A**（修复前 A4EA5B94895794C8） |
| `desktop/internal/services/mtu_batch_test.go`（新增） | **388**（复测版；修复前 354） | **0717F725849724A6**（修复前 3756FA5FB817E12F） |
| `desktop/frontend/src/platform/services.ts` | 424 | ED951BC2731639E3 |
| `desktop/frontend/src/pages/MTUDetectionPage.tsx` | 224 | BBC4AD31BCC0DD84 |
| `desktop/frontend/src/pages/MTUDetectionPage.test.tsx` | 142 | E6F210166A287D84 |
| `desktop/frontend/src/pages/mtu.css` | 74 | 377691E6D26A0792 |
| `docs/MTU_DETECTION.md` | 17 | DF3CA0980EE90E98 |

> 上表为**最终复测快照**：F1/F2 修复只改动两个 Go 文件（521 / 388 行）；前端 4 个文件与 `docs/MTU_DETECTION.md` 的行数与哈希在修复前后完全一致，确认未变动。
> 与 Lead 初始描述的两点出入（以文件字节为准）：修复前 `mtu.go` 517 行一致；`mtu_batch_test.go` 实为 **354 行、11 个 `TestMTUBatch*` 用例**（Lead 记为 309 行 / 8 个）。

---

## 0. 阶段一基线快照（记录于实现落地前，历史对照，不作为失败判定）

| 文件 | 阶段一实测状态 |
| --- | --- |
| `desktop/internal/services/mtu.go` | 319 行；无 `MTUBatchItem`/`SetBatch`/`RestoreBatch` |
| `desktop/internal/services/mtu_windows.go` | 108 行；`readMTU`/`probeMTU` 未变 |
| `desktop/internal/services/mtu_test.go` | 74 行；4 个单卡/搜索测试 |
| `desktop/frontend/src/platform/services.ts` | `mtu` 命名空间（:333-339）无 `setBatch`/`restoreBatch` |
| `desktop/frontend/src/pages/MTUDetectionPage.tsx` | 143 行；只有单卡流程 |
| `desktop/frontend/src/pages/MTUDetectionPage.test.tsx` | 72 行 / 6 个单卡用例 |
| 复用基础设施 | `MTUService.mu`；`EngineService.acquireLifecycle/releaseLifecycle`（`engine.go:1090-1101`）；Core 侧 `platform.MTUChange.Validate`（`engine/internal/platform/mtu.go:12-27`） |

---

## 1. 逐条判定（C1–C25）

判定口径：**通过** ＝ 实测命令退出码 0 且行为与契约一致；**通过（偏差）** ＝ 契约主语义成立、附一条已记录的轻微偏差；**发现缺陷** ＝ 可复现的行为偏离；**无法动态验证** ＝ 受环境限制，已给出替代验证与残余风险。

### A. Go 侧类型与签名

| # | 契约 | 判定 | 证据 |
| --- | --- | --- | --- |
| C1 | `MTUBatchItem` 8 字段 | **通过** | `mtu.go:31-40`：`adapter_id`/`guid`/`address`/`before`/`after`/`original`/`changed`/`error,omitempty` 与契约字段名一一对应。偏差记录 F3/F4（`error` 带 `omitempty`，与 TS `error?: string`（`services.ts:48`）自洽） |
| C2 | `SetBatch(ids []string, value int)` / `RestoreBatch(ids []string)` | **通过** | `mtu.go:393` `func (s *MTUService) SetBatch(ids []string, value int) ([]MTUBatchItem, error)`；`mtu.go:404` `func (s *MTUService) RestoreBatch(ids []string) ([]MTUBatchItem, error)`。`go vet` exit 0（§2 命令①） |
| C3 | 可从 Wails 绑定调用 | **通过（静态）** | 接收者 `*MTUService` 且方法导出；`desktop/main.go:188` `app.RegisterService(application.NewService(services.NewMTUService(...)))`（反射式绑定，无需白名单）；前端名字符串 `services.ts:345-348` 逐字为 `...services.MTUService.SetBatch` / `.RestoreBatch`。**未做真实 Wails runtime 端到端调用**（无桌面实机 UI 环境）→ 残余风险 R1 |

### B. 入参校验与边界

| # | 契约 | 判定 | 证据 |
| --- | --- | --- | --- |
| C4 | 值范围 576–65535 | **通过** | `mtu.go:398-400` `if value < 576 \|\| value > 65535 → errMTUOutOfRange`（"请输入 576–65535 之间的 MTU 值"，`mtu.go:45`）；实现者 `TestMTUBatchRejectsInvalidValue` 覆盖 `0/575/65536/-1`，断言 `len(readIDs)==0 && len(applied)==0`（PASS）。576/65535 落在合法侧（静态） |
| C5 | ids 去重 | **通过** | `dedupeIDs`（`mtu.go:365-379`）丢空串、按首次出现顺序保序去重。实测（探针 Edge 5）：`[32 ids + 重复 + 空串]` → `items=32`，每张卡只出现一次，`reads` 成对、`applies` 32 条 |
| C6 | 空集合报错 | **通过** | `validateBatch`（`mtu.go:381-389`）`len==0 → errMTUNoAdapter`（"请至少选择一张网卡"）。实现者 `TestMTUBatchRejectsEmptySelection` 覆盖 `SetBatch(["" ""])` 与 `RestoreBatch(nil)`，断言零触碰（PASS） |
| C7 | 最多 32 张 | **通过（已记录实现选择）** | `SetBatch:394-397`：**先去重、再判长度**，>32 → `errMTUTooMany`（"一次最多修改 32 张网卡"，含上限）。实测：33 个唯一 id 报错且零触碰；32 唯一 id 通过；"33 个含重复、去重恰 32" 通过（`items=32`）。先判长度再去重才会产生绕过路径，当前顺序以"真实网卡数"为准，无绕过。32 与 33 的两个边界均由实现者 `TestMTUBatchDeduplicatesAndCapsAt32` 与我的探针 Edge 5 双重覆盖 |
| C8 | 前置失败零改动报错 | **通过** | 顺序：`batch():413-417` 检测在跑 → `:424-437` `stopped()`（`acquireLifecycle` → `closing` → `engine.status` 必须 stopped/failed，`mtu.go:157-181`）→ `run():458` `requireSetCapability`（`EnsureElevated` + `Capabilities` 含 `mtu.set`，`mtu.go:442-454`）→ **之后**才进入逐卡循环。实测探针 Edge 9（真实 `stopped()` 路径）：① `closing=true` → "HypoMux 正在退出" ② 无提权 Core → "聚合核心协议协商失败：disconnected…" ③ `cancel!=nil` → "请等待 MTU 检测完成"；三者 `touched()==0` 且 `mtu-originals.json` 不存在、`lifecycleGate` 槽位归还。engine.status 分支与单卡共享同一 `stopped()`，且 Core 侧二次校验（`engine/internal/server/server.go:253-256` `engine_running`） |

### C. 逐卡执行语义

| # | 契约 | 判定 | 证据 |
| --- | --- | --- | --- |
| C9 | 逐卡失败隔离 | **通过** | `run():462-515` 每个 `continue` 只写当前 item。实测探针 Edge 1（全失败）：3 张全 `mtu_failed` → `items` 3 条、均 `Changed:false`、`Error` 非空、`reads=[eth-a eth-b eth-c]`、`applies` 三条、后续卡继续；实现者 `TestMTUBatchContinuesPastPerAdapterFailure` 覆盖"第 1 张读失败 + 第 2 张写失败 + 第 3/4 张成功"的顺序继续（PASS） |
| C10 | 部分失败仍返回 `(items, nil)` | **通过** | 实测 Edge 1：`err == nil` 且 3 条 item 均带 error；实现者 `TestMTUBatchReportsFailedApply`（1 失败 1 成功，`err==nil`）PASS。顶层 error 只用于前置/入参失败 |
| C11 | SetBatch 跳过 current==value | **通过（偏差 F3）** | `mtu.go:478-483`：跳过且 `Error:""`；实测探针 Edge 5：`Before:1400 Changed:false Error:""`，`applies==0`，**且 `mtu-originals.json` 未被创建**。偏差：`After` 保持 0 而非 `current` |
| C12 | RestoreBatch 无原值跳过 | **通过（偏差 F4）** | `mtu.go:470-476`：`info.Original <= 0` → 跳过 + `Error=errMTUNoOriginal`（"没有已保存的原值"），不下发 RPC。实测 Edge 3 后半（原值 `-5`）与 Edge 4（记录属于旧 GUID `guid-old`、当前卡为 `guid-new`）均 `Changed:false`、`applies==0`、原记录 `guid-old:1500` 未被删除。偏差：以 item.error 表达"跳过"，与 SetBatch 跳过分支（空串）语义不一致 |
| C13 | save → RPC → 回读 →（仅恢复）clear | **已修复并复测通过**（原判「通过（偏差 F1）」，见 §4.1） | ① 顺序实测（探针 Edge 6）：在 `setMTU` seam 内部读盘已看到 `map[guid-a:1500]`，证明 **saveOriginal 先于写入**；② Set 失败 → 记录仍在（探针 Edge 1：全失败后 `record=map[guid-a:1500 guid-b:1500 guid-c:1500]`，与单卡"原值已保留，可重试恢复"设计一致）；③ Restore 成功 → 键被删除（探针 Edge 6 末段 `record[guid-a]==0`；实现者 `TestMTUBatchSetSavesAndRestoreClearsOriginal` PASS）；④ Restore 的 RPC 失败 → 键仍在（静态：`clearOriginal` 只在 `:509` 成功回读之后调用）；⑤ `current==original>0` 时不跳过、仍下发同值 RPC（静态观察 F6）。**偏差 F1（已修复）**：修复前 `:509-513` 清理失败被吞；修复后 `:509-517` 在 `clearOriginal` 失败时写入 `item.Error = fmt.Sprintf("MTU 已恢复，但清理恢复记录失败：%v", err)` 且不清零 `Original` —— 独立探针复测通过（§4.1） |

### D. 前置/后置一致性与零改动

| # | 契约 | 判定 | 证据 |
| --- | --- | --- | --- |
| C14 | 前置失败零改动（文件系统层面） | **通过** | 探针 Edge 9 三个用例：失败后 `touched()==0`（0 读 0 写）且 `os.Stat(mtu-originals.json)` 为 `IsNotExist`；`lifecycleGate` 未泄漏 |
| C15 | 回读验证（GUID / current 不符） | **通过** | `mtu.go:502-506`。探针 Edge 2（回读返回换卡后的 GUID）：`item.Error == "修改后配置发生变化，请刷新并检查网卡；恢复记录已保留"`、`Changed:false`、记录保留 `map[guid-a:1500]`；与单卡 `mtu.go:341-343` 文案一致。`current != target` 分支为同一 `if` 的另一析取（静态） |
| C16 | 越界 original | **通过（方案 B 成立；缺口 F7）** | 实测探针 Edge 3：写入 `{"guid-a":100}` 后 `RestoreBatch` → `items=[{Before:1500 After:100 Original:0 Changed:true}]`、`applies=[eth-a=100]` —— **桌面侧确实把越界值下发**（无本地守卫）；Core 侧 `engine/internal/platform/mtu.go:13` 要求 `Value ∈ [576,65535]`，`engine/internal/server/server.go:261-263` 以 `invalid_params` 拒绝，因此不会产生越界写入，该卡以 `item.Error` 呈现、记录保留。`<=0` 的原值按"无原值"跳过（实测 `-5` → `errMTUNoOriginal`）。**未在真实管理员 Core 上实测**（无法启停本机 Core）→ 残余风险 R2 |
| C17 | 超时上限 | **通过（残余风险 R3）** | `mtu.go:418-421` `timeout := 20s + 15s×len(ids)`，上限 **300s**（32 张时 20+15×32=500s → 截到 300s）；1 张时预算 35s。实测探针 Edge 10（1 张、`setMTU` 阻塞到 ctx 结束）：`elapsed=35s`、`err==nil`、`items=[{Changed:false Error:"context deadline exceeded"}]`、`record=map[guid-a:1500]` —— 超时降级为逐卡 error、批 error 仍为 nil、恢复记录保留。注：单张批量（35s）**小于**单卡 `change()` 的 45s（`mtu.go:288`），因为批量预算是 20+15×n；1 张时比单卡更紧，但每张只做一次 read/save/set/read，35s 与 45s 同属安全的量级 |
| C18 | 并发互斥与死锁 | **通过** | 锁序 `s.mu → engine.lifecycleGate`，与单卡 `change()`（`mtu.go:283-294`）一致；`run()` 用 `s.read`（不取锁）而非 `s.Current`（`mtu.go:148-154` 取锁），**无自死锁**。实测探针 Edge 8：2 个 goroutine 各提交一个批量 + 1 个 `Cancel()`，`-race` 下 3 个调用均在 40s 看门狗内返回，`record=map[guid-a:1500 guid-b:1500]`，读/写成对串行（`reads=[eth-b eth-b eth-a eth-a]`）。实现者 11 个批量用例 + 4 个既有用例在 `-count=1 -race` 下全绿（§2 命令③）。残余：`Detect` 的 defer 顺序（`mtu.go:196` 先注册、`:201` 后注册 → LIFO 先 `release()` 再清 `s.cancel`）存在极窄窗口，落在窗口内的批量会被误拒"请等待 MTU 检测完成"（可重试、无损坏）；该窗口为既有代码，非本次改动引入 → R4 |
| C19 | 错误文案一致性 | **已修复并复测通过**（原判「发现缺陷（轻微，F2）」，见 §4.1） | 一致：`:416` "请等待 MTU 检测完成"、`:451` "请更新 Core 后再修改 MTU"、`:486` "保存原 MTU 失败，未执行修改：%v"、`:503` "修改后配置发生变化，请刷新并检查网卡；恢复记录已保留"、共享 `stopped()` 的 "请先停止网络服务，再检测或修改 MTU"。**不一致 ①（已修复）**：修复前 `:491-495` 失败时 `item.Error = err.Error()`，未沿用单卡 `:335` 的引导文案（实测 error 为 "mtu_failed: access denied" / "context deadline exceeded"）；修复后 `:492` 逐字使用同一前缀 + `%v`，复测完全相等（§4.1）。**不一致 ②（已修复）**：修复前 `:509-513` 吞掉 `clearOriginal` 失败，单卡 `:344-347` 会报 "MTU 已恢复，但清理恢复记录失败：%w"；修复后 `:513` 与单卡同一文案（§4.1） |

### E. 前端契约

| # | 契约 | 判定 | 证据 |
| --- | --- | --- | --- |
| C20 | `services.ts` 暴露 `mtu.setBatch/restoreBatch` | **通过** | `services.ts:48` `export type MTUBatchItem`（8 字段、snake_case、`error?`）；`:345-346` `setBatch: (ids: string[], value: number) => Call.ByName("...MTUService.SetBatch", ids, value) as Promise<MTUBatchItem[]>`；`:347-348` `restoreBatch`（单参数）。参数顺序与 Go 签名一致；`npx tsc --noEmit` exit 0（§2 命令⑤） |
| C21 | UI 多选 Dialog | **通过（UX 提示 F5）** | `MTUDetectionPage.tsx:181-222` 批量 Dialog；多选受控于 `batchIds:21` + `:188-194`；值校验 `:38-39` `batchValid`（整数且 576–65535）；提交按钮 `:219` 在 `!batchIds.length \|\| !batchValid \|\| batchBusy \|\| batchLocked` 时 disabled。实测前端探针 2：`575/65536/1400.5/""` → 按钮 disabled；`576/65535/1400` → enabled；未点"批量应用"前 `setBatch` 未被调用。**F5**：`:98-104` 无 32 张前置上限，探针 1 实测"33 张选中 → `setBatch(ids(33), 1400)`"（33 条 id 全下发），只能靠后端 `errMTUTooMany` 以 `:207` 的 alert 呈现 |
| C22 | blocked 下禁用 | **通过** | `blocked`（`:29`）与既有单卡语义相同；批量入口 `:146` disabled；Dialog 内 `batchLocked`（`:40`）同时禁用复选框 `:191`、数值输入 `:203`、两个提交按钮 `:218-219`；`runBatch:98` 二次早退。实测：实现者用例 10（`enginePhase="running"`）断言入口 disabled、Dialog 不出现、`setBatch/restoreBatch` 均未调用（PASS）。Dialog 内三处 disabled 为静态读码（无对应用例） |
| C23 | preview 下禁用 | **通过** | 同一 `batchLocked = blocked \|\| preview`（`:40`）；入口 `:146` 含 `preview`；`:186` 在 Dialog 内显示 preview 说明。实测：实现者用例 11（`preview`）断言入口 disabled、无 Dialog、无调用（PASS） |
| C24 | 关闭 Dialog 后刷新 | **通过** | `closeBatch:90-96`：仅当 `batchResults.some(item => item.changed)` 才 `setRevision(v => v+1)`；`revision` 是读盘 effect（`:56`）的依赖 → 重新 `appServices.mtu.current(adapter)`。实测：实现者用例 9 断言"批量成功期间不刷新（`api.current` 调用数不变）→ 关闭后调用数增加且 Dialog 消失"（PASS）。批量未改动时不刷新，符合"无乐观 UI"要求 |

### F. 回归

| # | 契约 | 判定 | 证据 |
| --- | --- | --- | --- |
| C25 | 既有单卡流程不回归 | **通过** | `go vet ./internal/services/` exit 0；`go test ./internal/services/ -run TestMTU -count=1 -race -v` → 15 PASS + 1 SKIP（`TestMTUWindowsReadOnlySmoke`，需 `HYPOMUX_MTU_SMOKE_ADAPTER`）exit 0；`npx vitest run src/pages/MTUDetectionPage.test.tsx` → 11 passed exit 0（原有 6 个单卡用例 + 新增 5 个批量用例）；`npx tsc --noEmit` exit 0；`go test ./... -run TestMTU` exit 0（无其他包回归）。详见 §2 |

---

## 2. 阶段二复跑命令与真实结果

| # | 命令（工作目录） | 退出码 | 关键输出 |
| --- | --- | --- | --- |
| ① | `go vet ./internal/services/`（`desktop`） | **0** | 无输出 |
| ② | `go test ./internal/services/ -run TestMTU -v` | **0** | 15 PASS + `--- SKIP: TestMTUWindowsReadOnlySmoke`，`ok ... (cached)`（故补跑 ③ 强制非缓存） |
| ③ | `go test ./internal/services/ -run TestMTU -count=1 -race -v` | **0** | 全部 PASS（含 11 个 `TestMTUBatch*`），`ok github.com/Hypostasis-Cat/HypoMux/desktop/internal/services 2.094s` |
| ④ | `go test ./... -run TestMTU`（`desktop`） | **0** | 各包 `ok … [no tests to run]` / `internal/services ok 0.253s` |
| ⑤ | `npx vitest run src/pages/MTUDetectionPage.test.tsx`（`desktop/frontend`） | **0** | `✓ src/pages/MTUDetectionPage.test.tsx (11 tests)`，`Test Files 1 passed (1)` |
| ⑥ | `npx tsc --noEmit`（`desktop/frontend`） | **0** | 无输出（仅 npm config 警告） |

`TestMTUWindowsReadOnlySmoke` 的 SKIP 是环境门控（未设 `HYPOMUX_MTU_SMOKE_ADAPTER`），非失败；它是只读烟测，不覆盖真实改 MTU。

### 2.1 F1/F2 修复复测（2026-10-06 21:40 —— 最终快照：`mtu.go` 521 行 / `mtu_batch_test.go` 388 行）

| # | 命令（工作目录） | 退出码 | 关键输出 |
| --- | --- | --- | --- |
| ⑦ | `go test ./internal/services/ -run "TestVerifyF1\|TestVerifyF2" -count=1 -v`（**我自己的独立探针**，非实现者的用例，见 §3.1） | **0** | `--- PASS: TestVerifyF1RestoreCleanupWarning (0.00s)`、`--- PASS: TestVerifyF2ApplyFailureGuidance (0.01s)`（含 `rpc` / `deadline` 子用例），`ok ... 0.101s` |
| ⑧ | 同上加 `-race` | **0** | 两个探针均 PASS，`ok ... 1.834s` |
| ⑨ | `go test ./internal/services/ -run TestMTU -count=1 -race -v`（全量回归） | **0** | `PASS_COUNT=16`、`SKIP_COUNT=1`、`FAIL_COUNT=0`，`ok github.com/Hypostasis-Cat/HypoMux/desktop/internal/services 1.955s`。16 个 PASS = 12 个 `TestMTUBatch*` + `TestMTUSearchBoundaries`、`TestMTUSearchDoesNotInterpretLossAsFragmentation`、`TestMTUSearchCancellationAndVerification`、`TestMTUOriginalSurvivesRestartAndRepeatedChanges`；唯一 SKIP 仍是 `TestMTUWindowsReadOnlySmoke` |
| ⑩ | `npx vitest run src/pages/MTUDetectionPage.test.tsx`（`desktop/frontend`；前端本次未变动，仅回归确认） | **0** | `✓ src/pages/MTUDetectionPage.test.tsx (11 tests) 976ms`、`Test Files 1 passed (1)`、`Tests 11 passed (11)` |
| ⑪ | `npx tsc --noEmit`（`desktop/frontend`） | **0** | 无输出（仅 npm config 警告） |

---

## 3. 我自建的对抗性探针（实现者未覆盖的边界）

临时文件（用完已删除，见 §6）：`desktop/internal/services/zz_verifymtu_tmp_test.go`（Go，10 个探针）与 `desktop/frontend/src/pages/zz.verifymtu.tmp.test.tsx`（3 个探针）。
命令：`go test ./internal/services/ -run TestVerify -count=1 -race -short -v` → **exit 0**（9 个 fast 探针）；`... -run "TestVerifyAllFailBatch|TestVerifyBatchDeadline" -count=1 -race -v` → **exit 0**（`ok … 36.926s`）；`npx vitest run src/pages/zz.verifymtu.tmp.test.tsx` → **exit 0**（3 passed）。

| 探针 | 目的 | 实测结果 |
| --- | --- | --- |
| Edge 1 全失败批次 | 全部 `mtu.set` 失败 / 全部读失败 | `err==nil`；3 条 item 均 `Changed:false, Error:"mtu_failed: access denied", After:0`；`reads=3, applies=3`；`record=map[guid-a:1500 guid-b:1500 guid-c:1500]`。恢复方向全失败：2 条 error、`applies=[]` |
| Edge 2 GUID 中途变化 | 回读时网卡被替换 | `Changed:false`、`Error:"修改后配置发生变化，请刷新并检查网卡；恢复记录已保留"`、`record=map[guid-a:1500]` |
| Edge 3 越界/非法 original | `{guid:100}` 与 `{guid:-5}` | 100：`applies=[eth-a=100]`、`After:100`、`Changed:true` → 桌面侧无守卫（F7）；-5：`errMTUNoOriginal`、`applies=[]` |
| Edge 4 恢复时 GUID 不一致 | 记录归属旧 GUID | `Changed:false`、`errMTUNoOriginal`、`applies=[]`、旧记录 `guid-old:1500` 保留 |
| Edge 5 跳过与拒绝零触碰 | skip 分支 / 33 唯一 id / 含重复去重到 32 | skip：`Changed:false, Error:""`、`mtu-originals.json` **不存在**；33 唯一：`errMTUTooMany`、0 读 0 写；含重复去重到 32：`items=32`、每卡一次 |
| Edge 6 记录写入顺序 | saveOriginal 必须先于写入、restore 期间记录必须在 | 写入 seam 内读到 `map[guid-a:1500]`；restore 写入 seam 内仍读到 `map[guid-a:1500]`，成功后 `record[guid-a]==0` |
| Edge 7 恢复后清理失败 | 记录替换被拒（Windows 只读文件 rename 失败） | `Changed:true, Error:"", Original:1500`，磁盘仍 `map[guid-a:1500]` → **F1 静默吞掉**（修复前行为；修复后复测见 §3.1） |
| Edge 8 并发 + Cancel | 互斥与死锁（`-race`） | 3 个并发调用全部返回（<40s 看门狗），`record=map[guid-a:1500 guid-b:1500]`，读写成对串行 |
| Edge 9 前置门 | `closing` / 无提权 Core / 检测中（真实 `stopped()`） | 分别 "HypoMux 正在退出" / "聚合核心协议协商失败：disconnected: 聚合核心输出已关闭" / "请等待 MTU 检测完成"；三者 0 读 0 写、无记录文件、`lifecycleGate` 未泄漏 |
| Edge 10 超时预算 | 1 张卡的批量 deadline | `elapsed=35s`、`err==nil`、`Error:"context deadline exceeded"`、`record=map[guid-a:1500]` |
| FE 探针 1 | 32 张上限是否有前端前置拦截 | `PROBE selected=33 sent=33 value=1400` → 无前端拦截（F5） |
| FE 探针 2 | 数值边界 | `575/65536/1400.5/""` → 提交按钮 disabled；`576/65535/1400` → enabled；未调用 API |
| FE 探针 3 | 无原值的恢复结果呈现 | 渲染为普通结果行 `eth-0：没有已保存的原值`（非 alert，无失败计数） |

### 3.1 F1/F2 修复复测探针（我的独立探针，真实输出）

临时文件 `desktop/internal/services/zz_verifymtu_f1f2_test.go`（本次复测专用，跑完已删除，见 §6）：自建 harness（`preflightSet:true` + 假 `readInfo`/`setMTU` + `t.TempDir()` 记录路径），**不复用** `mtu_batch_test.go` 的 harness 与断言，独立构造失败路径；用 `os.Chmod(path, 0o444)` 让 `mtu-originals.json` 的替换（rename）被 Windows 拒绝来触发真实清理失败。

`go test ./internal/services/ -run "TestVerifyF1|TestVerifyF2" -count=1 -v` 真实输出：

```text
=== RUN   TestVerifyF1RestoreCleanupWarning
    zz_verifymtu_f1f2_test.go:88: F1 items=[{AdapterID:eth-a GUID:guid-eth-a Address:10.0.0.1 Before:1400 After:1500 Original:1500 Changed:true Error:MTU 已恢复，但清理恢复记录失败：rename C:\Users\ADMINI~1\AppData\Local\Temp\TestVerifyF1RestoreCleanupWarning2409422876\001\mtu-2286664164.tmp C:\Users\ADMINI~1\AppData\Local\Temp\TestVerifyF1RestoreCleanupWarning2409422876\001\mtu-originals.json: Access is denied.}] err=<nil> applied=[1500]
--- PASS: TestVerifyF1RestoreCleanupWarning (0.00s)
=== RUN   TestVerifyF2ApplyFailureGuidance
=== RUN   TestVerifyF2ApplyFailureGuidance/rpc
    zz_verifymtu_f1f2_test.go:139: F2 items=[{AdapterID:eth-a GUID:guid-eth-a Address:10.0.0.1 Before:1500 After:0 Original:0 Changed:false Error:修改未确认，请刷新当前值；原值已保留，可重试恢复：mtu_failed: access denied} {AdapterID:eth-b GUID:guid-eth-b Address:10.0.0.1 Before:1500 After:1400 Original:0 Changed:true Error:}] err=<nil>
=== RUN   TestVerifyF2ApplyFailureGuidance/deadline
    zz_verifymtu_f1f2_test.go:139: F2 items=[{AdapterID:eth-a GUID:guid-eth-a Address:10.0.0.1 Before:1500 After:0 Original:0 Changed:false Error:修改未确认，请刷新当前值；原值已保留，可重试恢复：context deadline exceeded} {AdapterID:eth-b GUID:guid-eth-b Address:10.0.0.1 Before:1500 After:1400 Original:0 Changed:true Error:}] err=<nil>
--- PASS: TestVerifyF2ApplyFailureGuidance (0.01s)
    --- PASS: TestVerifyF2ApplyFailureGuidance/rpc (0.01s)
    --- PASS: TestVerifyF2ApplyFailureGuidance/deadline (0.01s)
PASS
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/services	0.101s
```

复测断言（全部通过）：
- **F1**：`Changed=true`、`After=1500`、`Original=1500`（**未被清零**）、`Error` 以「MTU 已恢复，但清理恢复记录失败：」开头且底层 rename 错误文本保留、磁盘记录仍为 `guid-eth-a:1500`（探针直接 `os.ReadFile` + `json.Unmarshal` 校验，非复用实现的 `originals()`）、`mtu.set` 恰好一次且 value=1500。
- **F2**：`item.Error` 与 `fmt.Sprintf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%v", err)` **完全相等**（前缀一致、底层 err 未被吞），失败卡 `Changed=false`，同批健康卡仍 `Changed=true/After=1400`，两卡原值记录均在。

源码交叉核对（`grep`，确认与单卡逐字一致、仅 `%w`→`%v`，因 `item.Error` 是 string）：

```text
desktop/internal/services/mtu.go
Line 335: 		return info, fmt.Errorf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%w", err)
Line 346: 			return updated, fmt.Errorf("MTU 已恢复，但清理恢复记录失败：%w", err)
Line 492: 			item.Error = fmt.Sprintf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%v", err)
Line 513: 				item.Error = fmt.Sprintf("MTU 已恢复，但清理恢复记录失败：%v", err)
```

---

## 4. 发现汇总（按严重度）

| ID | 严重度 | 位置 | 期望 vs 实际 | 影响 | 建议 |
| --- | --- | --- | --- | --- | --- |
| **F1** | 轻微 → **已修复并复测通过**（§3.1 / §4.1） | `mtu.go:509-517`（修复前 :509-513） | 期望：清理失败要体现出来（单卡 `:344-347` 报 "MTU 已恢复，但清理恢复记录失败：%w"）。实际：`if err = s.clearOriginal(...); err == nil { item.Original = 0 }`，err 被丢弃，item 仍是 `Changed:true, Error:""` | 界面显示成功但 `mtu-originals.json` 残留旧原值；后续对同一卡 restore 会再下发一次同值写入并再试清理；不会误改 MTU、不会丢记录 | `if err := s.clearOriginal(info.GUID); err != nil { item.Error = fmt.Sprintf("MTU 已恢复，但清理恢复记录失败：%v", err) } else { item.Original = 0 }` |
| **F2** | 轻微 → **已修复并复测通过**（§3.1 / §4.1） | `mtu.go:491-495`（修复后 :492 文案已补） | 期望：沿用单卡 `:335` 的 "修改未确认，请刷新当前值；原值已保留，可重试恢复：%w"。实际：`item.Error = err.Error()`（如 "mtu_failed: access denied"、"context deadline exceeded"） | 用户看不到"原值已保留、可重试恢复"的引导；文案与单卡不一致 | 对 `applyMTU` 的错误加同一前缀后写入 item |
| F3 | 信息 | `mtu.go:468, 508, 471-475` | 期望（阶段一 C11 字面）：跳过条目 `before==after==current`。实际：`after` 只在成功时（`:508`）赋值，跳过与失败条目 `after==0`；`error` 带 `omitempty`（`:39`） | 前端只在 `changed` 时渲染 `after`（`MTUDetectionPage.tsx:210-212`），无用户可见影响；其它消费者（日志/导出）可能误读 0 | 跳过条目令 `After = Before` |
| F4 | 信息 | `mtu.go:471-475` vs `:478-483` | 两个"跳过"分支对 `error` 的处理不一致：SetBatch 跳过 → `""`；RestoreBatch 无原值 → `"没有已保存的原值"` | 前端把后者渲染成普通结果行（FE 探针 3），无失败计数，影响可忽略；API 语义不一致 | 二者统一（保留提示文案或改用 `changed:false + 说明性字段`） |
| F5 | 信息 | `MTUDetectionPage.tsx:98-104`、`:219` | 期望（UX）：>32 张在提交前拦截或提示。实际：无长度校验，33 张直接下发，由后端 `errMTUTooMany` 以 `:207` 的 alert 呈现 | 多一次无用的批量往返；文案仍清晰。契约只要求后端限制 32，故非违约 | 复选超过 32 时禁用提交并就地提示 |
| F6 | 信息 | `mtu.go:470-483` | Restore 分支只在 `Original <= 0` 跳过；当 `Original == Current > 0` 时不跳过，仍下发同值 `mtu.set` 再清理；单卡 `:310-317` 在 `value==current` 时直接清理、不发 RPC | 一次多余的提权写入（同值），无正确性影响（静态观察，未动态构造） | 恢复分支也判 `Original == Current` 直接清理 |
| F7 | 信息 | `mtu.go:471` | 桌面侧只判 `<= 0`，不校验 original ∈ [576,65535]；探测证明越界值确被下发（`applies=[eth-a=100]`） | 依赖 Core 拒绝（`engine/internal/platform/mtu.go:13` + `server.go:261-263`），不会产生越界写入，该卡报错并保留记录 → 纵深防御缺口 | 增加本地范围判断并给明确文案 |

**无高/中危缺陷**：MTU 正确性、整批零改动保证、恢复记录原子性与"失败不丢记录"均未被破坏。

**Lead 裁决（收到缺陷预警后）**：F1、F2 → **应修**（已解冻 task-1，backend-mtu 做最小修复：`restore` 清理失败写入 `item.Error`；`apply` 失败沿用单卡引导文案），**两者已于 2026-10-06 21:40 由我独立复测通过（见 §4.1）**；F3、F4、F5、F7 → **记录不改**（理由见 §5）；F6 → 未列入本轮裁决，维持信息级 follow-up 建议。

### 4.1 F1/F2 修复复测结论：**已修复并复测通过**

| 项 | 修复前 | 修复后（当前字节） | 我的独立复测（真实输出见 §2.1 / §3.1） | 判定 |
| --- | --- | --- | --- | --- |
| F1 | `mtu.go:509-513` 只写 `if err = s.clearOriginal(...); err == nil { item.Original = 0 }`，清理失败被静默吞掉 | `mtu.go:509-517`：失败 → `item.Error = fmt.Sprintf("MTU 已恢复，但清理恢复记录失败：%v", err)`，成功才 `Original = 0`；`Changed=true` / `After=1500` 保留 | 探针 `TestVerifyF1RestoreCleanupWarning` PASS（`-race` 亦 PASS）：`Changed:true After:1500 Original:1500 Error:"MTU 已恢复，但清理恢复记录失败：rename … Access is denied."`，磁盘记录仍 `guid-eth-a:1500`（探针自行读盘校验），`mtu.set` 恰好一次 value=1500 | **已修复并复测通过** |
| F2 | `mtu.go:491-495` `item.Error = err.Error()`（"mtu_failed: access denied" / "context deadline exceeded"） | `mtu.go:492`：`item.Error = fmt.Sprintf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%v", err)`，与单卡 `:335` 逐字一致 | 探针 `TestVerifyF2ApplyFailureGuidance`（`rpc` + `deadline` 子用例）PASS（`-race` 亦 PASS）：`item.Error` 与期望 `fmt.Sprintf(...)` **完全相等**；失败卡 `Changed:false`，同批健康卡 `Changed:true/After:1400`；两卡原值记录保留 | **已修复并复测通过** |

- 回归：`go test ./internal/services/ -run TestMTU -count=1 -race -v` → **16 PASS / 1 SKIP / 0 FAIL，exit 0**（新增的 `TestMTUBatchReportsUnclearedOriginal`、更新后的 `TestMTUBatchReportsFailedApply` 与 `TestMTUBatchContinuesPastPerAdapterFailure` 均 PASS）；`npx vitest run src/pages/MTUDetectionPage.test.tsx` → 11 passed，exit 0；`npx tsc --noEmit` → exit 0。
- 因此 **C13、C19 已改判为「已修复并复测通过」**，总体判定从「通过（附 1 条轻微缺陷）」收紧为 **通过（无缺陷、无回归，仅余信息级 follow-up）**。

---

## 5. 总体判定与残余风险

**总体判定：通过（无缺陷、无回归）** —— F1/F2 修复后我已独立复测通过（§4.1），清单 25 条全部通过。C1–C25 逐条统计：**16 条"通过"**（C1、C2、C4、C5、C6、C8、C9、C10、C14、C15、C18、C20、C22、C23、C24、C25）、**8 条"通过（附实现选择/环境限制）"**（C3 静态核对、C7 实现选择已记录、C11→F3、C12→F4、C13→F1（**已修复复测**）、C16→F7、C17→R3、C21→F5）、**1 条原判缺陷**（C19→F2 文案一致性，**已修复并复测通过**）。F1–F7 全部为轻微/信息级，不影响契约的行为主语义（整批零改动、逐卡失败隔离、原值原子写入与失败保留、并发互斥均已实测成立）。

Lead 裁决（收到我的缺陷预警后）：**F1、F2 判定为「应修」**，已解冻 task-1 并派 backend-mtu 做最小修复（`mtu.go` 517 → **521 行**、`mtu_batch_test.go` 354 → **388 行**）；**修复已落地，我只复测了 F1/F2 与全量回归，结论为「已修复并复测通过」（§2.1 命令⑦–⑪、§3.1 真实输出、§4.1 改判），无回归**。**F3、F4、F5、F7 判定为「记录不改」**（F3 属契约未定义字段语义；F4 保留为信息级建议；F5 由后端 `errMTUTooMany` 拒绝并在 UI 展示即可；F7 本地记录被外部篡改才可能出现，Core 以 `invalid_params` 拒绝）。F6 未列入裁决，维持信息级 follow-up。

残余风险（未覆盖 / 无法验证）：
- **R1**：未做真实 Wails runtime 端到端调用（`Call.ByName` → Go 反射绑定 → 返回 `[]MTUBatchItem`），仅静态核对方法名/参数顺序 + TS 类型 + `tsc`。若绑定层对 `[]struct` 返回值有特殊处理，需在桌面实机点一次批量才能完全确认。
- **R2**：未在真实管理员 Core + 真实网卡上执行批量改 MTU（本机无法停 Core / 无可用可中断网卡）。越界 original 的"Core 拒绝"结论来自 `engine/internal/platform/mtu.go:13` 与 `engine/internal/server/server.go:261-263` 源码 + fake seam 实测，未做 Core 端到端。`TestMTUWindowsReadOnlySmoke` 因未设 `HYPOMUX_MTU_SMOKE_ADAPTER` 被 SKIP。
- **R3**：32 张批量上限 300s（`mtu.go:418-421`）。单卡端到端若 >9.4s，尾部网卡会以 `context deadline exceeded` 失败（失败语义已验证：逐卡 error、批 error 为 nil、记录保留）。300s 是否够用取决于 Core 侧 `SetMTU` 的 PowerShell 耗时，未实测。
- **R4**：`Detect` 结束后释放生命周期门与清除 `s.cancel` 之间存在极窄窗口（`mtu.go:196` / `:201` 的 defer LIFO 顺序），落在窗口内的批量会被误拒"请等待 MTU 检测完成"；可重试、无损坏，且为既有代码。
- **R5**：只读烟测 `TestMTUWindowsReadOnlySmoke` 与真实 `readMTU`（PowerShell `Get-NetAdapter`/`Get-NetIPInterface`）路径未在本次验证中执行；批量复用同一 `readMTU`，其行为未变（`mtu_windows.go` 未在本次改动中修改）。
- **R6**：验证快照已更新为**最终复测版**（§0 上方表格）：`mtu.go` **521 行 / 51DE56112CA24F7A**、`mtu_batch_test.go` **388 行 / 0717F725849724A6**，前端 4 个文件与 `docs/MTU_DETECTION.md` 哈希与修复前完全一致（未变动）。若这些文件在本次复测（2026-10-06 21:40）之后再次改动，F1/F2 复测结论需要重跑。

---

## 6. 写范围与清理证明

- 本次验证仅新建/修改本文件（`reports/batch-mtu-verification-20261006.md`）；**未触碰任何源码或文档**。
- 自建临时测试文件已在报告定稿前删除：
  - `REMOVING C:\Users\Administrator\Desktop\HypoMux\desktop\internal\services\zz_verifymtu_tmp_test.go`
  - `REMOVING C:\Users\Administrator\Desktop\HypoMux\desktop\frontend\src\pages\zz.verifymtu.tmp.test.tsx`
  - 复查 `Get-ChildItem -Recurse -Filter "*verifymtu*"`（`desktop`）→ 无残留。
- F1/F2 复测用的第二个临时探针也已删除（先打印解析后的绝对路径确认目标，再删除）：
  - `RESOLVED: C:\Users\Administrator\Desktop\HypoMux\desktop\internal\services\zz_verifymtu_f1f2_test.go` → `EXISTS_AFTER: False` → `LEFTOVER: none` → `SCAN_DONE`（`Get-ChildItem -Recurse -Filter "*verifymtu*"` 全树复查无残留）。
- 清理后 `git status --porcelain`（工作树）仅含本次功能与验证的产物，无任何临时文件：`M desktop/frontend/src/pages/MTUDetectionPage.test.tsx`、`M desktop/frontend/src/pages/MTUDetectionPage.tsx`、`M desktop/frontend/src/pages/mtu.css`、`M desktop/frontend/src/platform/services.ts`、`M desktop/internal/services/mtu.go`、`M docs/MTU_DETECTION.md`、`?? desktop/internal/services/mtu_batch_test.go`、`?? reports/backend-mtu-task-1-batch-mtu.md`、`?? reports/batch-mtu-verification-20261006.md`（另有一批与本次无关的 `reports/deepseek-v4*` 产物，非我创建）。
