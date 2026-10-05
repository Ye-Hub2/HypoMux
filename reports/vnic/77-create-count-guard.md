# 77 · 创建对话框的 vNIC 数量余量校验

- 范围：`desktop/frontend`（`main` 分支，未提交）
- 写入文件（全部在授权白名单内）：
  - `desktop/frontend/src/components/vnic/managementAdapters.ts`
  - `desktop/frontend/src/components/vnic/managementAdapters.test.ts`
  - `desktop/frontend/src/pages/VirtualAdaptersPage.tsx`
  - `desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx`
- 未触碰：Go 代码、`SettingsPage`、`HomePage`、`HyperVAdapterPanel.tsx`、i18n JSON、`app.css`。全程没有执行任何 `git add/commit/checkout/stash`。

---

## 1. Go 侧 32 的真实口径与位置

### 1.1 常量

`desktop/internal/services/hyperv_adapter.go:191-193`

```go
const (
    hypervMaxBatchSize     = 16 // 单次提交上限
    hypervMaxTotalAdapters  = 32 // 总量上限
)
```

注释是 `// §3.7 的硬上限。`

### 1.2 判定逻辑

`desktop/internal/services/hyperv_adapter.go:362-370`

```go
func hypervCheckBatchCapacity(existing int, count int) error {
    if count < 1 || count > hypervMaxBatchSize {
        return hypervErrorf(hypervCodeBatchTooLarge, "单次创建张数必须在 1–%d 之间（收到 %d）", hypervMaxBatchSize, count)
    }
    if existing+count > hypervMaxTotalAdapters {
        return hypervErrorf(hypervCodeTotalLimitReached, "当前已有 %d 张，再创建 %d 张会超过 %d 张上限", existing, count, hypervMaxTotalAdapters)
    }
    return nil
}
```

口径确认：**是「总数 ≤ 32」，而且 `existing` 精确等于 `len(ledger.Adapters)` —— 只数我们自己账本里的行，不数 Hyper-V 里现存的适配器对象。**

这一点有两个非常容易踩错的后果：

1. **不是「系统里 vNIC 总数」。** 用户自己在 Hyper-V 管理器里手工建的卡、或系统自带的 vEthernet，**不占这 32 张**。所以不能用任何"HypoMux 相关的 vNIC 数量"来近似它。
2. **`List()` 返回的行 ≠ 账本行数。** `hyperv_adapter.go:1139-1175` 的 `List()` 会把账本行翻译成 `HyperVAdapterStatus`，另外还会通过 `buildUnmanagedHyperVStatus`（`hyperv_adapter.go:1166-1172`）补上**命名符合约定但不在账本里的**对象，这些行 `Managed=false`。前端若直接 `adapters.length` 当已用数，会把这类只读行也算进额度，从而**拒绝 Go 本来会接受的创建**。所以前端镜像必须数 `managed === true` 的行（对应纯函数 `countQuotaAdapters`）。

反向的偏差也存在但偏安全：账本里名字不符合约定的行会被 `List()` 跳过（`hyperv_adapter.go:1158`），所以前端看到的 `used` 可能略小于 Go 的 `existing`。这只会让用户多提交一次然后吃 Go 的整批拒绝，不会让本该成功的创建被挡掉。

### 1.3 在哪一步拒绝整批

`Create(switchName string, count int)`（`hyperv_adapter.go:1316`）：

- `hyperv_adapter.go:1324` 先做一次批量形状校验（1–16），
- `hyperv_adapter.go:1355` 在 `s.updateLedger(...)` 事务里做容量校验：

```go
if err := s.updateLedger(func(ledger *hypervLedger) error {
    if err := hypervCheckBatchCapacity(len(ledger.Adapters), count); err != nil {
        return err // 整批拒绝，账本不变
    }
    ...
```

- 而真正提权的脚本在 `hyperv_adapter.go:1381` `s.runScript(...)`。

关键结论：**容量判定发生在提权脚本之前**，所以超配额时不会有任何一张卡被创建，是干净的 fail-closed 整批拒绝（不是部分成功）。错误码 `hypervCodeTotalLimitReached = "total_limit_reached"`（`hyperv_adapter.go:63`）。

用户侧的观感就是缺陷本身：先弹 UAC，然后回来一个笼统的 `virtual_adapters_create_failed`，既不知道"已经 30 张了"，也不知道"最多还能建 2 张"。

Go 侧已有覆盖：`desktop/internal/services/hyperv_adapter_test.go:341-346`（容量表驱动用例）。

---

## 2. 前端校验的设计

### 2.1 纯函数层 `managementAdapters.ts`（零 React、零服务）

文件顶部原有的 `VIRTUAL_ADAPTER_MAX_BATCH = 16` / `VIRTUAL_ADAPTER_MAX_TOTAL = 32` 已存在且已经是 `export`。我**没有引入第二个常量源**，只补齐了它们的溯源注释：

```ts
/**
 * 单次提交张数上限。镜像 Go 的 hypervMaxBatchSize
 * (desktop/internal/services/hyperv_adapter.go:191)。Go 从不读取本文件 —
 * 纯前端镜像，改动必须与 Go 侧同步。
 */
export const VIRTUAL_ADAPTER_MAX_BATCH = 16;

/**
 * 总张数上限。镜像 Go 的 hypervMaxTotalAdapters
 * (desktop/internal/services/hyperv_adapter.go:192)。改这里必须同时改
 * hypervMaxTotalAdapters，否则用户会看到 Go 从不接受、且永远不会变的额度。
 */
export const VIRTUAL_ADAPTER_MAX_TOTAL = 32;
```

新增：

```ts
export const countQuotaAdapters = (adapters: readonly HyperVAdapterStatus[]): number =>
  adapters.filter(isManagedAdapter).length;

export type CreateQuota = {
  used: number;
  total: number;
  remaining: number;
  perSubmitMax: number;
  exhausted: boolean;
};

export const createQuota = (
  adapters: readonly HyperVAdapterStatus[],
  total: number = VIRTUAL_ADAPTER_MAX_TOTAL,
): CreateQuota => {
  const used = countQuotaAdapters(adapters);
  const remaining = Math.max(0, total - used);
  return {
    used, total, remaining,
    perSubmitMax: Math.min(VIRTUAL_ADAPTER_MAX_BATCH, remaining),
    exhausted: remaining === 0,
  };
};
```

以及 `CreateCountValidation` 的 `reason` 增加 `"over-quota"`；`validateCreateCount(raw, max = VIRTUAL_ADAPTER_MAX_BATCH, quota?: CreateQuota)` 在**形状校验之后**追加：

```ts
if (quota && value > quota.remaining) return { ok: false, reason: "over-quota" };
```

**顺序是刻意的**：Go 先查 `count < 1 || count > 16`（`:1324`），再查 `existing+count > 32`（`:1355`）。所以满机器上填 17 仍然是"批量形状错"，不是"额度不足"；前端保持同样的判定顺序，两边的错误分类才对得上。失败返回体仍然只有 `{ ok, reason }` 两个字段（既有测试用 `toEqual` 断言），保持等价。

第三个参数是**可选**的，这是硬性约束的直接结果：`HyperVAdapterPanel.tsx:141` 用两参数调用 `validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH)`，而该文件不在我的写入白名单里。可选参数让那个调用点一行不改、行为逐字不变（有专门用例钉住）。

### 2.2 页面 `VirtualAdaptersPage.tsx`

- `const quota = useMemo(() => createQuota(adapters), [adapters])` —— **额度永远是按当前列表实算的**，没有任何写死的"当前已用"，每次 3 秒轮询 `refresh` 带来的新 `adapters` 都会让它重算。列表读失败（`loadFailed`）时不显示任何额度文案，避免把"读不到"说成"还能建 N 张"。
- 常驻额度条（放在 `HyperVAdapterPanel` 之上），内容即要求的文案：
  - `已创建 {used} / {total} 张虚拟网卡，本轮最多还能创建 {remaining} 张。` / `{used} of {total} virtual adapters created — at most {remaining} more in this batch.`
  - 剩余为 0 时 `intent="error"` 并追加原因：`已创建 32 / 32 张虚拟网卡，已达上限，本轮无法再创建。请先删除不再使用的虚拟网卡。` / `All {total} adapter slots are taken ({used} created). Delete adapters you no longer need before creating more.`
- 创建门口二次校验（"唯一的事实来源是当前列表"的那道闸）：`create()` 里用 `quotaRef.current` 重新跑一遍 `validateCreateCount`，`reason === "over-quota"` 时弹 error toast（`dedupeKey: "virtual-adapters:error:quota"`）并 **return**，**不调用服务**——所以没有 UAC、不存在"点了才失败"、更不会有半成品。形状类错误（0 / 空 / 非整数 / >16）保持静默 return，与改动前完全一致（对话框本来就拦住了这些，不该由页面重复提示）。
- 双语文案走项目既有约定：页面内 `const text = (zh, en) => locale === "en" ? en : zh`（与 `ConnectionsPage.tsx`、`HealthPage.tsx` 同款），**没有新增任何 i18n key**。

### 2.3 两个刻意选择的取舍

**(a) 钳制 vs 拒绝：选了拒绝 + 解释。** 需求里写的是"校验/钳制"，同时又要求"提交按钮禁用（不要让用户点了才失败）"。静默钳制会把 16 悄悄改成 2，用户看到的数量和他输入的不一致，而且横幅只说了额度、没有解释为什么数字被改了。现在是：横幅常驻说明额度，超额提交被明确拒绝并告知改成几以内。唯一不拦截的窗口是"列表还没刷新出来就已经填了数"，那种情况下宁可放过让 Go 拒，也不擅自改用户输入。

**(b) 额度只能新鲜到上一次轮询。** 前端永远可能落后 Go 一拍（别人刚建、轮询还没回来）。所以 Go 侧判定保持不变、依然是最终权威；前端这一层的作用是"把绝大部分超配额提交挡在 UAC 之前并说清楚原因"，不是（也不可能）取代 Go 的事务内校验。

---

## 3. 测试清单

`managementAdapters.test.ts`（纯函数，36 tests）：

- `createQuota`：空账本 → `remaining 32 / perSubmitMax 16`；30 行 → `remaining 2 / perSubmitMax 2`；32 行 → `exhausted`；35 行 → `remaining` 夹到 0；**只读外来行（`managed: false`）不计入**；`managed: undefined` 按非托管处理；显式 `total` 覆盖。
- `validateCreateCount`：额度刚好够（30 行填 2 / 1）通过；超额（30 行填 3、填 16）→ `over-quota`；额度耗尽（32 行填 1）→ `over-quota`；**判定顺序**（满机器填 17 → `too-large`、0 → `too-small`、`""` → `empty`、`1.5` → `not-integer`）；两参数调用路径行为逐字不变。
- 新增 `managedRows(n)` 帮助函数：按 `List()` 的真实形状造数据（`adapterId` 互不相同、`managed: true`、名字 `HypoMux-vnic-NN`），这样"额度断言"不可能在测一个服务根本产不出的列表。

`VirtualAdaptersPage.test.tsx`（新增 `describe("VirtualAdaptersPage create quota")`，12 个用例；文件共 32 tests）：

| 用例 | 咬住的行为 |
| --- | --- |
| 余量充足 | 4 张时横幅是「已创建 4 / 32 …最多还能创建 28 张」 |
| 余量被别处占用 | 30 张时横幅收窄到「最多还能创建 2 张」 |
| 英文 | `locale = "en"` 时是 `30 of 32 virtual adapters created — at most 2 more in this batch.`，且中文文案不出现 |
| 余量恰好为 0 | 32 张时横幅文案 + 独立的"已达上限"原因文案同时出现 |
| 动态重算 | 首次 4 张 → 轮询一次后变 30 张，横幅跟着变，旧文案消失（证明不是挂载时算一次的常量） |
| 列表读失败 | 不出现任何额度文案（`queryByText` 为 null） |
| 只读外来行 | 30 托管 + 2 只读 → 仍按 30 算，不误伤 Go 会接受的创建 |
| 额度够时照常创建 | 30 张填 2 → `create` 收到 `("外网交换机", 2)`，且没有 error toast |
| 超额提交被禁用/拒绝 | 30 张填 3 → `mocks.create` **一次都没被调用**，`notify` 恰好一次且为 `intent: "error"` + `dedupeKey: "virtual-adapters:error:quota"` + 精确文案「…请把创建数量改成 2 以内再提交。」 |
| 额度为 0 时任何提交都被拒 | 32 张填 1 → 不调服务，toast 为耗尽文案 |
| 对话框自身的 1–16 拦截不变 | 填 17 → 对话框内创建按钮 `disabled`，且页面不弹任何 toast（形状错仍然静默） |
| 删除后额度回升 | 30 张填 3 被拒 → 轮询显示 20 张后再填 3 → 正常提交 |

覆盖对照需求：余量充足 ✅、余量刚好为 0 ✅、余量不足 ✅、超额提交被禁用 ✅（页面门口拒绝 + 对话框对 >16 的既有 disabled）、提示文案正确 ✅（中文 + 英文逐字断言）。

---

## 4. 门禁真实退出码

全部在 `<repo>\desktop\frontend` 下执行。

**修复后（最终态）：**

```
> & ".\node_modules\.bin\tsc.cmd" --noEmit
EXITCODE=0

> & ".\node_modules\.bin\vitest.cmd" run
 Test Files  45 passed (45)
      Tests  330 passed (330)
   Duration  69.28s
EXITCODE=0
```

**两个被改文件的定向运行：**

```
> & ".\node_modules\.bin\vitest.cmd" run src/components/vnic/managementAdapters.test.ts src/pages/VirtualAdaptersPage.test.tsx
 ✓ src/components/vnic/managementAdapters.test.ts (36 tests)
 ✓ src/pages/VirtualAdaptersPage.test.tsx (32 tests)
 Test Files  2 passed (2)
      Tests  68 passed (68)
EXITCODE=0
```

stderr 里仅有既存噪音：`SettingsPage.tsx` 的受控/非受控输入告警、Wails 的 "Browser Environment Detected" 提示，与本次改动无关。

`git status --short` 确认：**没有任何暂存/新增条目**（全部是未暂存的 ` M`，加一个未跟踪的 `reports/`）。

---

## 5. 回归探针：把校验改坏，确认用例真的会红

在三处各打一个"故意弄坏"（都是类型安全的改动，所以 `tsc` 不会提前报错、探针测的确实是行为而不是编译）：

1. `managementAdapters.ts`：`if (quota && value > quota.remaining)` → `if (quota && false)`（额度判定失效）
2. `VirtualAdaptersPage.tsx`：`create()` 里 `validateCreateCount(..., quotaRef.current)` → `validateCreateCount(..., undefined)`（页面门口的闸失效 = 相当于"去掉禁用逻辑"）
3. `VirtualAdaptersPage.tsx`：额度条渲染条件 `{!loadFailed && (` → `{false && !loadFailed && (`（额度提示消失）

探针运行结果：

```
TSC_EXIT=0
 × rejects a count past the remaining quota as over-quota, not too-large
 × rejects every positive count once the quota is exhausted
 × leaves the dialog's own 1..16 validation untouched when no quota is passed
 × states the remaining budget while the ledger is nowhere near the ceiling
 × narrows the claim to the real gap once other batches filled the ledger
 × says the same thing in English when the locale is en
 × explains at zero remaining why create is unavailable
 × recomputes the budget from each poll instead of freezing it at mount
 × does not charge read-only foreign rows against the budget
 × refuses an over-quota submit before it can reach the service
 × refuses every submit once the budget is exhausted
 × frees the budget again as soon as the poll shows a deletion
 Test Files  2 failed (2)
      Tests  12 failed | 56 passed (68)
VITEST_EXIT=1
```

- 12 个用例转红，其中 9 个来自页面层（3 个纯函数用例因探针 1 而红）。
- `× leaves the dialog's own 1..16 validation untouched when no quota is passed` 之所以也红，是因为该用例是两段断言：先钉两参数旧路径，再钉"有额度且耗尽时 → over-quota"，红的是第二段（`managementAdapters.test.ts:208-211`）。
- 关键的"超额提交被拦"用例确实咬住了服务调用：`refuses an over-quota submit before it can reach the service` 在闸门失效后失败（`create` 被真的调用了）。

三处改坏全部回滚，回滚后复跑全量：

```
TSC_EXIT=0
 Test Files  45 passed (45)
      Tests  330 passed (330)
VITEST_EXIT=0
```

**当前落盘的文件就是回滚后的最终态，探针改动没有残留。**

---

## 6. 我没有验证到的部分（务必知悉）

1. **对话框内的那条提示和"禁用提交按钮"本身没做——因为做不了。** 创建对话框是 `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx` 里的 `HyperVAdapterCreateDialog`，它 `import` 了本文件，**不在我的写入白名单**。它的提交禁用条件是 `const createDisabled = busy || preview || !selectable.length || !count.ok;`（`HyperVAdapterPanel.tsx:142`），而它两参数调用 `validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH)`（`:141`），所以在不碰那个文件的前提下，父级无法把额度传进去（可选第三参只能让**旧调用点保持旧行为**，不能改变它的返回值）。
   因此本轮的实际形态是：**页面常驻额度条 + 创建门口的硬拦截 + 超额时的精确错误 toast**，用户点"创建"会立刻看到「本轮最多还能创建 2 张，请把数量改成 2 以内」，且**不会**触发 UAC、不会产生半成品，也**不会**再看到笼统的 `virtual_adapters_create_failed`。但按钮**不会**提前变灰。
2. **"创建入口明确不可用"只在页面层做到，工具栏按钮未禁用。** 面板是 `HyperVAdapterPanel`（同样不在白名单）。额度为 0 时页面横幅变成红色错误条并说明"已达上限，请先删除"，提交会被拒；但用户仍能点开对话框。这条需求**未完全满足**，原因同上（属白名单外）。
3. **没跑桌面端真机。** 只跑了 `tsc --noEmit` 与 vitest（jsdom）。没有起 Wails 应用、没有真的在 Hyper-V 上创建 32 张卡、没有观察真实 UAC 流程与真实 `List()` 往返。Go 侧判定我**只读代码、没有改、也没有跑 `go test`**。
4. **没有验证 `managed` 字段的运行时取值分布。** "只读外来行不计入额度"这条依赖 `List()` 的 `managed` 语义，我是从 `hyperv_adapter.go:1166-1172` 的构造逻辑推断的，没有在真机上抓过一份含外来行的真实 `List()` 响应来交叉确认。
5. **没验证并发窗口。** 两个页面/批次同时提交时，前端额度会双花；这条路径完全依赖 Go 的事务内校验（行为未变），我没有构造并发用例。
6. **工作区本来就不干净。** `git status` 显示 vNIC 迁移本身还有 21 个文件处于未暂存修改状态（含 `hyperv_adapter.go`、`HyperVAdapterPanel.tsx`、`SettingsPage.tsx` 等）。这些**不是我改的**；我只写了白名单里那 4 个文件。建议合入前用 `git diff -- desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx` 等逐个核对来源。

## 7. 建议的下一步（若要满足剩余两条需求）

改 `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx`，只动三行、把额度透传给对话框：

```tsx
// props 增加：quota?: CreateQuota
const count = validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH, quota);
const createDisabled = busy || preview || !selectable.length || !count.ok || Boolean(quota?.exhausted);
// 表单底部（沿用既有 .virtual-adapter-hint 类）追加：
{count.reason === "over-quota" && <span className="virtual-adapter-error">{quotaHint}</span>}
```

`VirtualAdaptersPage.tsx` 已经持有 `quota`，把它当 prop 传给面板即可；两侧 `.virtual-adapter-error` / `.virtual-adapter-hint` 在页面 CSS 与 `vnic.css` 里都已存在，不需要碰样式文件。
