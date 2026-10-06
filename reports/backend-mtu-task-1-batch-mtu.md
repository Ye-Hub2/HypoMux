# task-1 实现报告：批量修改网卡 MTU 后端能力

- 实现者：backend-mtu（Go 后端）
- 契约来源：`task-1` 冻结描述
- 写范围：`desktop/internal/services/`（`mtu.go`、新增 `mtu_batch_test.go`）；bindings 为生成产物
- 仓库 HEAD：`717f5fd`

## 1. 交付内容

`desktop/internal/services/mtu.go`（319 → 517 行，+199/-1）新增：

- 类型 `MTUBatchItem`，json tag 与契约逐字一致：`adapter_id` / `guid` / `address` / `before` / `after` / `original` / `changed` / `error,omitempty`。
- 常量：`mtuBatchMax = 32`、`mtuSetRPC = "mtu.set"`、`errMTUOutOfRange`、`errMTUNoAdapter`、`errMTUTooMany`、`errMTUNoOriginal`。
- 方法：`SetBatch(ids []string, value int) ([]MTUBatchItem, error)`、`RestoreBatch(ids []string) ([]MTUBatchItem, error)`，以及内部 `applyMTU` / `dedupeIDs` / `validateBatch` / `batch` / `requireSetCapability` / `run`。
- 唯一被改动的既有代码各 1 行：`read()` 由直接调用 `readMTU` 改为 `s.readCurrent(ctx, id)`；`readCurrent` 在 seam 为 nil 时仍调用同一个 `readMTU`。单卡 `Apply` / `Restore` / `Detect` / `Cancel` / `Current` 的签名与行为未变。

引擎侧零改动：逐卡调用现有 `mtu.set` RPC，参数 `{if_index, guid, expected=修改前的当前值, value=目标值}`（与 `change()` 一致）。

## 2. 契约逐条落点

| 契约 | 实现位置 |
| --- | --- |
| value 576–65535，否则「请输入 576–65535 之间的 MTU 值」 | `mtu.go` `SetBatch` |
| ids 去重 + 丢弃空串；空选「请至少选择一张网卡」；>32「一次最多修改 32 张网卡」 | `dedupeIDs` + `validateBatch` |
| 整批前置：`s.cancel != nil` →「请等待 MTU 检测完成」 | `batch` 持 `s.mu` 后首先判断 |
| 整批前置：引擎须 stopped/failed + 提权 + `mtu.set` capability，否则「请更新 Core 后再修改 MTU」 | `stopped(ctx)`（既有）→ `requireSetCapability`，均在读任何网卡之前 |
| 逐卡串行、失败互相隔离 | `run` 循环，每卡失败只写该 item 的 `Error` 并 `continue` |
| 读失败 → item.Error | `run` 首段 `s.read` |
| 已是目标值 → 跳过且 `changed=false` | `run` 中 `info.Current == value` 分支 |
| Restore 无原值 →「没有已保存的原值」 | `run` 中 `info.Original <= 0` 分支 |
| 保存原值失败 → item 错误「保存原 MTU 失败，未执行修改：…」且不写网卡 | `run` 中 `saveOriginal` 失败分支（`continue`，未调用 `applyMTU`） |
| 写成功后回读校验 GUID 未变且 `Current == target` | `run` 尾部 `s.read` + 双条件判断 |
| 成功 → `changed=true`、`after=目标值`；Restore 成功 → `clearOriginal` | `run` 尾部 |
| 逐卡失败一律返回 `(items, nil)` | `run` 只返回 `items, nil`；`error` 仅保留给整批前置失败 |
| 超时 = 20s + 15s×len(ids)，上限 300s | `batch` |
| 与 Detect/change 互斥、不重入 `s.mu` | `batch` 全程持 `s.mu`（与 `change` 同构），`run` 内不再次加锁 |
| 不改 `s.result` 语义 | 批量路径不读写 `s.result` |

一处实现取舍：`saveOriginal` 失败的错误用 `fmt.Sprintf("…：%v", err)` 拼装 item 文案（契约写的是 `%w`）。二者对用户可见文案完全一致；item 的 text 字段无法做错误包装，因此按文案契约为准，未引入 `%w`。

## 3. 测试

新增 `desktop/internal/services/mtu_batch_test.go`（349 行），全部通过注入 seam 完成，**没有**调用真实 `Set-NetIPInterface` / powershell / 真实网卡。harness 以 `preflightSet=true` + no-op `preflight` + no-op `checkEngine` 替换引擎闸门，`readInfo` 读一份内存态、`setMTU` 记录并更新该内存态（因此回读校验真实生效）。

11 个新用例：

1. `TestMTUBatchRejectsInvalidValue`
2. `TestMTUBatchRejectsEmptySelection`
3. `TestMTUBatchDeduplicatesAndCapsAt32`
4. `TestMTUBatchContinuesPastPerAdapterFailure`
5. `TestMTUBatchSkipsAdaptersAlreadyAtTarget`
6. `TestMTUBatchRestoreSkipsAdaptersWithoutOriginal`
7. `TestMTUBatchSetSavesAndRestoreClearsOriginal`
8. `TestMTUBatchReportsFailedApply`
9. `TestMTUBatchReportsUnpersistableOriginal`
10. `TestMTUBatchRejectsCoreWithoutCapability`
11. `TestMTUBatchPrecheckFailsWithNoChanges`

（共 11 个新用例；其中第 11 个刻意走真实路径：用 `EngineService{settings: NewSettingsService(), client: engineclient.New(), lifecycleGate: make(chan struct{}, 1)}` 触发真实 `stopped()` + `EnsureElevated` 失败，并断言零读零写。）

### 实测命令与退出码

```
cd C:\Users\Administrator\Desktop\HypoMux\desktop
go vet ./internal/services/            → exit 0
go test ./internal/services/ -run TestMTU -v -count=1
  → PASS，ok github.com/Hypostasis-Cat/HypoMux/desktop/internal/services
  → exit 0（输出计 15 × `--- PASS`、1 × `--- SKIP`、0 × `--- FAIL`）
  → 完整日志见 `reports/backend-mtu-task-1-go-test.log`
go test ./internal/services/ -count=1   → ok，exit 0（45.6s，全包无回归）
go build ./...                          → exit 0
```

`TestMTUWindowsReadOnlySmoke` 按设计 SKIP（未设 `HYPOMUX_MTU_SMOKE_ADAPTER`）。

## 4. bindings 结论

```
cd C:\Users\Administrator\Desktop\HypoMux\desktop
C:\Users\Administrator\go\bin\wails3.exe generate bindings -clean=true -ts -i
  → exit 0；Processed: 316 Packages, 12 Services, 106 Methods, 0 Enums, 47 Models
  → Output directory: desktop/frontend/bindings
```

- 已确认生成物包含新契约：`frontend/bindings/.../internal/services/models.ts:292` 的 `MTUBatchItem`，以及 `mtuservice.ts:32` `RestoreBatch` / `mtuservice.ts:40` `SetBatch`；`index.ts` 也导出了 `MTUBatchItem`。
- 该目录在 `desktop/.gitignore:9` 被 `/frontend/bindings/` 忽略，`git ls-files -- frontend/bindings` 为 0，因此 `git status -- frontend/bindings` 干净、`git diff HEAD -- frontend/bindings` 为空——不存在需要回退的无关重写。
- **结论：保留生成物**（内容正确且被 ignore，不会污染提交）。前端仍可走 `Call.ByName("...MTUService.SetBatch")`，CI 也会重新生成。

## 5. 未触碰的文件

- `engine/**`：零改动（复用既有 `mtu.set`）。
- `desktop/frontend/src/**`：零改动（`MTUDetectionPage.tsx`、`services.ts`、`mtu.css` 的改动来自 frontend-mtu，不属于本任务）。
- `docs/**`：零改动。
- 其它既有 Go 服务与测试：仅 `internal/services/mtu.go` 的 `read()` 改动 1 行；`mtu_test.go` 等已有测试文件未改。
- 未执行 `git checkout` / `git reset` / `git clean`，未使用任何 shell 写文件绕过 write/edit 保护。

## 6. 阶段二修复（F1 / F2）

Lead 根据 verify-mtu 的独立验证解冻 task-1 写范围，要求仅改 `desktop/internal/services/mtu.go` 与 `desktop/internal/services/mtu_batch_test.go`。两处偏离均已修，只动这两个文件。

### F1：restore 成功后清理恢复记录失败被静默吞掉

- 位置：`desktop/internal/services/mtu.go:509-517`（原 509-513）。
- 旧代码 `if err = s.clearOriginal(info.GUID); err == nil { item.Original = 0 }` 在失败时既不置 `item.Error` 也不保留原值语义，界面显示成功而 `mtu-originals.json` 残留。
- 新代码：
  ```go
  item.Changed = true
  item.After = target
  if restore {
      // The MTU is already restored, so a failed cleanup keeps the record
      // (and the original value) and only adds a warning.
      if err = s.clearOriginal(info.GUID); err != nil {
          item.Error = fmt.Sprintf("MTU 已恢复，但清理恢复记录失败：%v", err)
      } else {
          item.Original = 0
      }
  }
  ```
- 语义：`Changed=true` / `After=target` 保留（MTU 确实已恢复）；`clearOriginal` 失败时 `item.Original` 保持调用方读到的原值不变，并把文案与单卡路径 `mtu.go:344-347` 完全一致的警告写入 `item.Error`。

### F2：批量 apply 失败未沿用单卡引导文案

- 位置：`desktop/internal/services/mtu.go:491-495`。
- 旧代码 `item.Error = err.Error()`（实测形如 `mtu_failed: access denied`）→ 新代码：
  ```go
  item.Error = fmt.Sprintf("修改未确认，请刷新当前值；原值已保留，可重试恢复：%v", err)
  ```
- 与单卡 `mtu.go:335` 同一文案，逐字一致，`Changed` 仍为 `false`。

### 测试改动（`desktop/internal/services/mtu_batch_test.go`）

- 新增 `TestMTUBatchReportsUnclearedOriginal`（第 297 行）：走真实 `RestoreBatch` 恢复路径，把恢复记录写成只读（`os.Chmod(harness.service.path, 0o444)`）使 `writeOriginals` 的 temp-file + `os.Rename` 覆盖只读目标失败（`mtu.go:104-129`）；断言 `Changed==true`、`After==1500`、`Original==1500`（未被清成 0）、`item.Error` 前缀为「MTU 已恢复，但清理恢复记录失败：」、且 `mtu.set` 确实被调用过一次 value=1500。**F1 因此可以真的失败，未新增任何生产代码 seam**（复用的 `readInfo` / `setMTU` / `preflightSet` / `checkEngine` 均为 task-1 已有）。
- 扩展 `TestMTUBatchReportsFailedApply`（第 274 行）：`setMTU` 返回 `errors.New("mtu_failed: access denied")`，断言 `Changed==false`、`strings.HasPrefix(item.Error, "修改未确认，请刷新当前值；原值已保留，可重试恢复：")` 且仍包含底层 `mtu_failed: access denied`；同批健康卡仍 `Changed=true` / `After=1400`。
- 同步修正 `TestMTUBatchContinuesPastPerAdapterFailure`（第 143 行）中 eth-b 的期望文案，使其等于 F2 的新引导文案。
- 契约与其余行为未变：去重 / 32 上限 / 前置零改动 / 超时 / 互斥 / `(items, nil)` 保持原样；`Apply` / `Restore` / `Detect` / `Cancel` 签名与行为不变。

### 验收证据（工作目录 `C:\Users\Administrator\Desktop\HypoMux\desktop`）

```
gofmt -l ./internal/services/                       → 无输出，exit 0
go vet ./internal/services/                         → VET_EXIT=0
go test ./internal/services/ -run TestMTU -v -count=1 → EXIT=0
  → 16 × --- PASS，1 × --- SKIP（TestMTUWindowsReadOnlySmoke，未设 HYPOMUX_MTU_SMOKE_ADAPTER），0 × --- FAIL
  → ok github.com/Hypostasis-Cat/HypoMux/desktop/internal/services 0.218s
go test ./internal/services/ -count=1                → ok，PKG_EXIT=0（42.227s，全包无回归）
go build ./...                                      → BUILD_EXIT=0
```

`git status --porcelain -- internal/services/` 只有本任务的两个文件：` M desktop/internal/services/mtu.go`、`?? desktop/internal/services/mtu_batch_test.go`（后者是 task-1 新增的未跟踪文件）。

