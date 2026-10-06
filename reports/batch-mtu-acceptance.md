# 批量修改网卡 MTU —— Lead 最终验收记录

日期：2026-10-06
任务：在「网络检测 → MTU 检测」页交付「多选网卡 + 统一 MTU 值」的批量修改与批量恢复原值功能（用户确认形态）。

## 1. 交付行为

- 入口：MTU 页「01 · 检测路径」卡片新增「批量修改 MTU / Batch change MTU」按钮（`busy`、`blocked`、`preview` 或无网卡时禁用）。
- Dialog：勾选网卡（显示名称 + 地址）→ 填一个统一目标 MTU（576–65535）→「批量应用」，或「批量恢复原值」按同一勾选逐张还原。
- 结果：执行中 Spinner 并禁用全部按钮；结果在同一 Dialog 内逐卡展示（`名称：before → after`，失败/跳过显示原因）；关闭 Dialog 时若任一网卡 `changed=true`，复用页面既有 `revision` 机制重读当前值。
- 后端：`MTUService.SetBatch(ids, value)` / `RestoreBatch(ids)` 逐卡顺序执行，单卡失败只影响该张；整批前置条件（无进行中检测、引擎 stopped/failed、Core 已提权且 capabilities 含 `mtu.set`）任一失败则**零改动**报错；值 576–65535；ids 去重丢空串；空集合与超 32 张报错；超过则返回 `(items, nil)`，error 只表达整批前置失败。
- 原值安全：修改前按 GUID 原子写入 `mtu-originals.json`（已有记录不覆盖），写入失败则该卡不修改；恢复成功且清理成功才清除该卡记录，清理失败时保留记录并在该卡结果上给出 warning。
- 超时：`20s + 15s × len(ids)`，上限 300s；批量全程持 `s.mu`，与 Detect/单卡 change 互斥且不重入。

## 2. 改动文件

| 文件 | 说明 |
| --- | --- |
| `desktop/internal/services/mtu.go` | 319 → 521 行。`MTUBatchItem`(:31-40)、常量(:42-49)、测试用 seam 字段(:57-62)、`applyMTU`(:355-361)、`dedupeIDs`(:365-379)、`validateBatch`(:381-389)、`SetBatch`(:392-402)、`RestoreBatch`(:404-410)、`batch`(:412-438)、`requireSetCapability`(:442-454)、`run`(:457-521)；`read()` 内 1 行改为 `s.readCurrent`（seam 为 nil 时仍走原 `readMTU`）。单卡 `Apply`/`Restore`/`Detect`/`Cancel` 签名与行为未变。 |
| `desktop/internal/services/mtu_batch_test.go` | 新增 388 行，12 个 `TestMTUBatch*` 用例，全部注入 fake，不触碰真实网卡。 |
| `desktop/frontend/src/platform/services.ts` | 新增 `MTUBatchItem` 类型与 `mtu.setBatch` / `mtu.restoreBatch`（沿用 `Call.ByName` 风格）。 |
| `desktop/frontend/src/pages/MTUDetectionPage.tsx` | +85 行：入口按钮 + 批量 Dialog（多选、统一值、批量应用/恢复、Spinner、逐卡结果、blocked/preview 禁用、关闭后刷新）。 |
| `desktop/frontend/src/pages/mtu.css` | +21 行：`.mtu-batch-*` 样式，复用 `--hm-*` 变量。 |
| `desktop/frontend/src/pages/MTUDetectionPage.test.tsx` | +74 行：新增 5 个用例（共 11 个）。 |
| `docs/MTU_DETECTION.md` | 13 → 17 行：补批量说明。 |
| `desktop/frontend/bindings/**` | `wails3 generate bindings` 重新生成（该目录被 `desktop/.gitignore` 忽略，含 `MTUBatchItem`、`SetBatch`、`RestoreBatch`）。 |

engine 侧零改动：批量逐卡复用现有 `mtu.set` RPC，参数与单卡 `change()` 一致（`if_index` / `guid` / `expected=改前值` / `value=目标值`）。

## 3. 验收证据（Lead 独立复跑 + 独立验证成员复跑）

| 命令（工作目录） | 结果 |
| --- | --- |
| `go vet ./internal/services/`（desktop） | exit 0 |
| `go test ./internal/services/ -run TestMTU -v -count=1`（desktop） | exit 0：16 PASS / 1 SKIP / 0 FAIL（SKIP = 未设 `HYPOMUX_MTU_SMOKE_ADAPTER` 的只读烟测） |
| `go test ./internal/services/ -count=1`（desktop） | exit 0（含既有用例，无回归） |
| `go test ./internal/services/ -run TestMTU -count=1 -race -v`（verify-mtu） | exit 0：16 PASS / 1 SKIP / 0 FAIL |
| `go build ./...`（desktop） | exit 0 |
| `npx vitest run src/pages/MTUDetectionPage.test.tsx`（desktop/frontend） | exit 0：11 passed |
| `npx tsc --noEmit`（desktop/frontend） | exit 0 |
| `npx vite build`（desktop/frontend） | exit 0，built in 4.85s |

独立验证报告：`reports/batch-mtu-verification-20261006.md`（C1–C25 清单、13 个自建边界探针、逐条判定与残余风险）。

## 4. 验证中发现并已修复

- F1：`RestoreBatch` 恢复成功后 `clearOriginal` 失败被静默吞掉（界面显示成功但记录残留）→ 现在写入 `item.Error = "MTU 已恢复，但清理恢复记录失败：<err>"`，`Changed=true`/`After` 保留，`Original` 不被清零；新增 `TestMTUBatchReportsUnclearedOriginal`（真实触发 rename 失败），verify 用独立探针复现。
- F2：批量 apply 失败未沿用单卡引导文案 → 现在与单卡 `mtu.go:335` 一致：`修改未确认，请刷新当前值；原值已保留，可重试恢复：<err>`，并补断言。

判定为记录不改的信息级项：skip/失败条目 `after` 恒为 0；两个 skip 分支 error 语义不一致；前端无 32 张前置校验（后端会报错并展示）；桌面侧无 `original` 越界守卫（Core 以 invalid_params 拒绝）。

## 5. 残余风险

- 未做真实 Wails runtime 端到端，也未在真实管理员 Core/网卡上执行（越界拒绝为源码 + fake seam 证明）；建议实机验证一次。
- 32 张 × 300s 上限：单卡端到端耗时 > 约 9.4s 时，尾部网卡会以超时失败结束（失败语义正确，但用户可能看到尾部失败）。
- `Detect` 释放 lifecycle gate 与清除 `s.cancel` 之间存在极窄窗口，可能出现一次误拒（既有代码逻辑，非本次引入）。
- 结论绑定修复后的文件快照：`mtu.go` 521 行、`mtu_batch_test.go` 388 行；再改动需重跑上述命令。
