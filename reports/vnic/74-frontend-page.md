# 74 · T-C2 前端页面交接（虚拟网卡 Hyper-V）

作者：`frontend-vnic-page` · 日期：2026-01-01 · 上游契约：`reports/vnic/70-frozen-hyperv-interface.md`
详细设计：`reports/vnic/63-frontend-hyperv-page.md` · 路由/服务由 `frontend-vnic-wiring` 落地

---

## 1. 结论

页面 **UI 已完成并全部门禁通过**。三层拆分：

| 层 | 文件 | 职责 |
| --- | --- | --- |
| 数据 | `components/vnic/managementAdapters.ts` | 纯函数：状态派生、计数校验、交换机过滤、格式化。零 React、零服务依赖 |
| 展示 | `components/vnic/HyperVAdapterPanel.tsx` | 纯展示组件。不调服务、不持轮询、不发通知 |
| 编排 | `pages/VirtualAdaptersPage.tsx` | 唯一的副作用源：轮询、超时、通知、预览 |

这个切分让 §3 的状态映射表和 §6 的测试矩阵都能在**不 mock 任何服务**的情况下直接断言 —— 18 个纯函数测试 + 19 个面板测试完全离线。

## 2. 交付文件

**新增**
- `desktop/frontend/src/pages/VirtualAdaptersPage.tsx`（命名导出 + default 导出）
- `desktop/frontend/src/pages/VirtualAdaptersPage.css`
- `desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx`（15 tests）
- `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx`
- `desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx`（19 tests）
- `desktop/frontend/src/components/vnic/managementAdapters.ts`
- `desktop/frontend/src/components/vnic/managementAdapters.test.ts`（18 tests）

**删除**
- `desktop/frontend/src/components/vnic/VirtualAdapterPanel.tsx`（旧 Wintun 面板，已被 `HyperVAdapterPanel` 取代）
- `desktop/frontend/src/components/vnic/VirtualAdapterPanel.test.tsx`

**保留并复用**
- `desktop/frontend/src/components/vnic/vnic.css` —— 未改、未删，由新面板 import

**修改**
- `desktop/frontend/src/pages/HomePage.tsx` —— 删 `:26` 旧 import、`notifySuccess` 块、旧面板 JSX；**保留** `notifyError`（`:91`/`:100` 在用）与 `virtualAdapterService` import（Go 侧构造参数仍需要）
- `desktop/frontend/src/pages/HomePage.test.tsx` —— 追加 `describe("HomePage virtual NIC cleanup")`，断言旧卡片/两个旧按钮均不再渲染，防回归

## 3. 契约逐条对照

### §3.2 状态派生（`deriveAdapterState`）

| 输入 | 派生状态 | UI |
| --- | --- | --- |
| `state==="creating"` 且 `address` 为空 | `dhcp` | `virtual_adapters_state_dhcp`，dhcp 超 60s 追加 `virtual_adapters_dhcp_slow` |
| `state==="creating"` 且有 `address` | `creating` | `virtual_adapters_state_creating` |
| `state==="ready"` 且有 `address` | `ready` | `virtual_adapters_state_ready` + `address`/`prefixLength`/`macAddress` |
| `state==="ready"` 但 `address` 为空 | `unknown` | **不谎报「已就绪」** |
| `state==="failed"` | `failed` | `virtual_adapters_state_failed` + `lastError` |
| `state==="absent"` 或任何未知值 | `unknown` | `virtual_adapters_state_unknown` |

`unknown` 是**兜底分支而非异常分支**：所有非枚举值都落进来，不抛错。测试用 `"totally-bogus-state"` 覆盖过。

### §3.4 删除安全
删除按钮**仅在 `managed === true` 时渲染**；`onRemove` 里再做一次 `if (!isManagedAdapter(adapter)) return;`。未登记的卡（`managed===false`）标 `virtual_adapters_foreign`「非本工具创建」，UI 上无任何删除入口。

### §3.5 DHCP 时序
`state==="creating" && !address` 即进入「等待 DHCP」。`virtual_adapters_dhcp_slow` 在 60s 后出现 —— 实现上刻意依赖**布尔量** `waitingForDhcp` 而非 adapters 数组身份：

```tsx
const waitingForDhcp = useMemo(() => adapters.some(a => deriveAdapterState(a) === "dhcp"), [adapters]);
useEffect(() => { if (!waitingForDhcp) return; const timer = setTimeout(() => setDhcpSlow(true), VIRTUAL_ADAPTER_DHCP_SLOW_MS); return () => clearTimeout(timer); }, [waitingForDhcp]);
```

若直接依赖 `adapters`，3s 轮询每次返回新数组都会重置定时器，60s 提示永远不会出现 —— 这是本轮最容易踩的一个坑。

### §3.7 容量上限
`VIRTUAL_ADAPTER_MAX_BATCH = 16`、`VIRTUAL_ADAPTER_MAX_TOTAL = 32` 常量写入 `managementAdapters.ts`，并作为 `{max}` / `{total}` 注入 `virtual_adapters_count_hint`、`virtual_adapters_total_hint`、`virtual_adapters_summary`。**前端不自行截断**：超限由 Go 侧拒绝，前端只做提示。

### §4 轮询门控（关键）
`AppShell` 永久挂载访问过的页面，所以不门控就会永久轮询：

```tsx
useEffect(() => {
  if (!pageActive) return;
  mounted.current = true;
  void refresh();
  const stop = preview ? null : startSerialPoll(refresh, VIRTUAL_ADAPTER_POLL_INTERVAL_MS);
  return () => { mounted.current = false; requestSequence.current += 1; stop?.(); };
}, [pageActive, preview, refresh]);
```

`refresh` 的身份稳定性是这里的关键：`t` 走 `tRef.current` 读取，`useEffect` 的 deps 里**不含 `t`**。若直接依赖 `t`，测试里每次渲染返回新 `t` 的 mock 会让 `refresh` 每次渲染都是新引用 → 轮询定时器每帧重建 → 永不推进。`requestSequence` 用于丢弃过期响应，避免快速切换页面时旧结果覆盖新结果。

### §4 超时（lead 追加的硬性要求）
`withServiceTimeout` 是裸 `Promise.race`，**只 reject 等待方、不取消底层 Wails 调用**。给短超时会「误报失败、但提权子进程还在建卡」，用户重试就撞 16/32 硬上限。因此按操作分别取 wiring 导出的常量：

| 操作 | 常量 | 值 |
| --- | --- | --- |
| `list()` / `switches()` | `HYPERV_ADAPTER_READ_TIMEOUT_MS` | 10s |
| `remove()` | `HYPERV_ADAPTER_WRITE_TIMEOUT_MS` | 60s |
| `create()` | `HYPERV_ADAPTER_CREATE_TIMEOUT_MS` | **180s** |

`list()` 与 `switches()` 用 `Promise.allSettled` 并行 —— 任一失败都不阻断另一项。

### §4 创建流程
`create()` 发出后**不阻塞轮询**：`setCreating(true)` 后立刻返回，`startSerialPoll` 独立继续每 3s 拉 `list()`，直到某次返回里没有 `state==="creating"` 的行才 `setCreating(false)`。期间显示 `virtual_adapters_create_banner{count}` 进度条（带 Spinner，`role="status" aria-live="polite"`）。

数量输入 **1..16，默认 1，必须手动填**（不按剩余额度自动推算）。`validateCreateCount` 用 `/^\d+$/` 判整数，拒绝 `""`/`0`/`17`/`-1`/`1.5`/`" 3 "`。提交前在编排层再校验一次，不信任 UI。

`switches()` 为空或没有可用 External 交换机 → `virtual_adapters_no_switch` MessageBar warning + 创建按钮禁用。`allowManagementOs === false` 的交换机在 Dropdown 里保留但 `disabled`，避免「消失=看不见为什么不能选」。

### 通知
统一走 `useAppNotifications`。`notifyOnce(dedupeKey, input)` 用 per-mount `Set` ref 实现「每次 mount 最多一次」，四个 dedupeKey：

| dedupeKey | 时机 | intent |
| --- | --- | --- |
| `virtual-adapters:error:list` | `list()` 抛错 | error |
| `virtual-adapters:info:create` | `create()` 发出 | info |
| `virtual-adapters:error:create` | `create()` 失败 | error |
| `virtual-adapters:info:remove` | `remove()` 成功 | info |
| `virtual-adapters:error:remove` | `remove()` 失败 | error |

所有文案走 `t()`。**唯一例外**见第 6 节（`routing_dialog_cancel`）。

### 预览模式
`import.meta.env.DEV && !isDesktopRuntime()` → 渲染 3 条 fixture（2 ready + 1 failed）+ 1 个内部交换机，**所有写路径硬禁用**（`busy` 恒 true、按钮 `disabled`、`onCreate`/`onRemove` 直接 return），并显示 `virtual_adapters_preview` 信息条。fixture 值用纯 ASCII（`LAN-External` / `Ethernet`），对齐 `src/pages/connectionPreview.ts` 先例，避免 zh locale 预览里出现硬编码英文散文。

## 4. 门禁结果（本地实跑）

| 命令 | 结果 |
| --- | --- |
| `.\node_modules\.bin\tsc.cmd --noEmit` | **exit 0**，零错误 |
| `.\node_modules\.bin\vitest.cmd run` | **exit 0**，45 files / **287 tests passed** |
| `.\node_modules\.bin\vite.cmd build` | **exit 0**，`✓ built in 690ms` |

新增 52 个测试：纯函数 18 + 面板 19 + 页面 15。既有 44 个文件 235 个测试**全部无回归**。

构建产物 `dist/assets/VirtualAdaptersPage-BgnEYQA4.js`（11.33 kB │ gzip 3.52 kB）独立成 chunk，证明 `App.tsx:34` 的 lazy 路由接线生效。

## 5. 测试要点

**`managementAdapters.test.ts`（18）** —— 状态派生全枚举、计数边界、交换机过滤、格式化、unknown 兜底。

**`HyperVAdapterPanel.test.tsx`（19）** —— 三种状态映射、foreign 无删除按钮、`inPool` 仅 true 时显示、删除确认文案含配额回收、创建对话框 count 0/17 被拒 16 通过、`switches()` 为空时创建禁用。
> 踩坑：`beforeAll` 里必须桩 `ResizeObserver`，否则 FluentUI MessageBar/Dialog 静默不渲染。工具栏与空态各有一个 `virtual_adapters_create` 按钮，断言必须用 `within(document.querySelector(".virtual-adapters-toolbar"))` 限定范围。

**`VirtualAdaptersPage.test.tsx`（15）** —— 轮询门控（`usePageActive=false` 时零调用；切回 true 后恢复）、`list()` 抛错 → 错误条 + 重试、创建期间持续轮询、删除确认、DHCP 60s 提示时序、预览模式 fixture + 写操作禁用。
> 踩坑：fake timers 下 `waitFor` **永不触发**（它内部靠 `setInterval` 轮询，而这个被 fake 了），表现为 `Test timed out in 5000ms`。改用 `await act(async () => { await vi.advanceTimersByTimeAsync(ms); })`。`vi.useFakeTimers` 的 `toFake` 必须排除 `queueMicrotask`/`nextTick`，否则 React 调度器不刷新、UI 不更新。

## 6. 需要 lead 知道的决策

### `routing_dialog_cancel` 复用
简报明令「不得新增 key」，但允许集合里**没有通用的 cancel key**。两个对话框的取消按钮复用了既有的 `routing_dialog_cancel`（zh `:151`「取消」/ en `:495`「Cancel」，`BlockedDomainsPage` 已在用）。两个 locale 都已有该 key、都是明文，语义完全吻合。**如果 lead 认为该加 `virtual_adapters_cancel`，请让 wiring 成员双写后我替换** —— 只改两处 `t()` 调用。

### i18n key 使用面
字面调用 29 个 key，全在允许集合内；`virtual_adapters_state_creating` / `_state_dhcp` / `_state_ready` 通过 `stateLabelKey` 映射间接使用。**未硬编码任何 CJK/英文用户可见文案**，未新增 key。

### 主页返回按钮
`VirtualAdaptersPageProps = {onBack?: () => void}` 存在但**刻意不渲染**返回按钮 —— 该页是顶级导航项（`CompactNavigation.tsx:37` 已加导航项），没有可返回的父页面。若 lead 希望加，由 shell 侧传 `onBack` 即可，组件已预留。

## 7. 未验证项 / 风险

| # | 项 | 说明 |
| --- | --- | --- |
| 1 | **真机 Hyper-V 行为未跑过** | 本轮只写代码，未碰真实网卡或系统网络设置。UAC 弹窗、`CreateVmNetworkAdapter` 真实返回、DHCP 45s 时序均只在 fixture 与单测中验证 |
| 2 | **Go 侧字段假设** | 假定 `HyperVAdapterStatus.state` 取值落在 `creating`/`ready`/`failed`/`absent` 内。任何其它值安全落到「状态未知」。若 Go 侧实际用 `pending`/`up` 之类命名，需同步 `deriveAdapterState` |
| 3 | **`list()` 不要求提权** | 按 §3.8 实现为静默失败 → 错误条。若 Go 侧实际会返回错误而非空列表，文案需微调 |
| 4 | **预览模式未经浏览器目视** | 只在 jsdom 里断言了 fixture 与禁用态；`npm run dev` 的真实渲染效果未人工检查 |
| 5 | **并发 create 的上限保护在 Go 侧** | 前端只做 1..16 的输入校验。两次并发创建可能都通过前端校验，靠 Go 侧 §3.7 兜底 —— 这是设计如此，不是缺口 |
| 6 | **`withServiceTimeout` 的语义缺陷** | `Promise.race` 不取消底层调用（lead 已指出）。前端用 180s 大超时规避，但**任何前端超时都不能作为「操作没发生」的证据**。若将来调小该值，需同时确认 Go 侧有对应的取消/幂等设计 |

## 8. 复现命令

```powershell
cd <repo>\desktop\frontend
& ".\node_modules\.bin\tsc.cmd" --noEmit      # 期望 exit 0
& ".\node_modules\.bin\vitest.cmd" run       # 期望 45 files / 287 tests
& ".\node_modules\.bin\vite.cmd" build       # 期望 built in <1s
```

未做 git add / commit。`reports/**` 按惯例不入库。

---

# 修复轮 · 独立审计 M1–M4

来源：`frontend-vnic-audit` 的 `reports/vnic/75-audit-frontend.md`（审计总评不建议直接放行，4 个必须修项）。
本轮由 `lead` 在 m00298 派单，**全部四项一次改完**。写范围严格限定在：
`pages/VirtualAdaptersPage.tsx`(+`.test.tsx`)、`components/vnic/HyperVAdapterPanel.tsx`(+`.test.tsx`)、
`components/vnic/managementAdapters.ts`(+`.test.tsx`)、`pages/VirtualAdaptersPage.css`、`i18n/legacy.messages.json`。
**未碰** Go 文件、`app.css`、`services.ts`、`CompactNavigation.tsx`、`App.tsx`、`HomePage.tsx`、`bindings/`。

## M1 · `create()` 把批次返回值当全量写回

**根因**：Go `desktop/internal/services/hyperv_adapter.go:1233-1243` 的返回值只含**本批**行（`:1088` 注释「返回本批的立即态」）。
原 `setAdapters(next)` ⇒ 已有 12 张卡再创建 4 张，表格只剩 4 行，其余连同「已加入出口池」徽章一起消失最多 3 秒，顶部徽章从「已就绪 12/12」闪回「0/4」。

**为什么不改成 `await refresh()`**：Go 在 `:1238-1240` 专门往 Create 返回行的 `LastError` 注入 `hypervPoolRestartHint`，`:1236-1237` 注释说这是「唯一能把需要重启聚合送到前端的通道」。`List()` 路径从不写这个字段，所以 `refresh()` 会把它一并抹掉 —— 等于用 M1 换掉 M2。

**修法**：`managementAdapters.ts` 新增 `mergeAdapterBatch(previous, batch)`，按 `name` 合并 —— batch 报告的同名行以 batch 为准（更新鲜的快照）、batch 未提及的旧行原样保留、batch 里表格没见过的行追加到末尾（保持既有顺序，避免新卡在表里跳来跳去）。`batch.length === 0` 返回 `[...previous]` 而非原引用，保证 React 仍会重渲染。页面侧用**函数式** `setAdapters((previous) => mergeAdapterBatch(previous, next))`，顺带消除 stale closure。

## M2 · Go 的「重启聚合后生效」提示被条件互补地丢弃（审计最实质的发现）

**根因**：面板只在 `state === "failed"` 时渲染 `lastError`；Go 恰好只在 `row.State != failed` 时才写它。两个条件**精确互补** ⇒ 该提示在原实现下**永远不会显示**。而 §3.6.4 的「必须重启聚合引擎」是用户创建完成后唯一需要执行的下一步动作 —— 提示到不了用户眼前，契约第 4 条用户硬验收实际不成立。前端自己的 `virtual_adapters_restart_hint` 只在创建对话框里出现，创建完即随对话框消失。

**修法**（未去动 Go，也未放宽 failed 条件让 ready 行渲染 `lastError` —— 那既脆弱又语义错）：由**页面**在 `create()` 成功后置 `restartRequired`，传给面板，在横幅区渲染既有 key `virtual_adapters_restart_hint` + 一个 `Dismiss20Regular` 关闭按钮。创建对话框里那处保留。下次成功创建会重新置位，用户可手动关闭。

顺带补上审计提的「创建完成无通知」：`notify({ title: t("virtual_adapters_state_ready"), message: t("virtual_adapters_restart_hint"), intent: "info", dedupeKey: "virtual-adapters:info:create-done" })`。按 lead 要求**未**改成 `notifyOnce` 语义 —— 每次成功创建都应提示，而 `notifyOnce` 的 per-mount 上闩会让第二次起完全静默（这正是 lead 记下的 S1，此处用显式 `notify` 规避）。

`setRestartRequired` / `notify` / `setCreating(false)` 三者**刻意放在 `if (!mounted.current) return;` 之前**：批次要么发生要么没发生，toast 与重启提示都是全局的；只有行写入可能过期，才需要守卫。

## M3 · 写操作进行中切走页面 → 回来后整页永久只读

**根因**：`finally { if (mounted.current) setBusy(false) }` 依赖 `mounted`，而轮询 effect 的 cleanup（`:216-220`）在 `pageActive` 变 false 时就把 `mounted.current` 置 false。180s 创建 / 60s 删除若在「离开状态」settle，`busy` 永久停在 true ⇒ 刷新按钮、批量创建、每行删除全禁用，**必须重启应用才能恢复**。

**修法**：`create` 与 `remove` 的 `finally` 均改为**无条件** `setBusy(false)`。`setAdapters` 前的 `mounted` 守卫按 lead 要求**保留**（那个防的是过期轮询回写，与本项无关）。`create` 的 `catch` 里 `setCreating(false)` 同样移到守卫之前 —— 否则离开页面后失败会留下一个永不消失的进度 banner，属同一类泄漏。

## M4 · `switches()` 查询失败时页面没有任何解释

**根因**：`switchesFailed` 之前只被用来**抑制**提示、从不产生提示。若 `list()` 成功而 `switches()` 失败（交换机枚举被拒 / Hyper-V 管理 cmdlet 部分不可用 / 无 Hyper-V 机器），用户看到空列表 + 一个**没有任何说明的灰色「批量创建」按钮** —— 死胡同。原测试 `VirtualAdaptersPage.test.tsx:180-187` 恰好把这个死胡同行为固化了。

**修法**：`lead` 批准新增**第 32 个** i18n key `virtual_adapters_switch_failed`（zh/en 双写、无占位符）：
`switchesFailed` 时渲染 `intent="warning"` 的 MessageBar + 复用 `onRefresh` 的重试按钮；同时明确断言**不**显示 `virtual_adapters_no_switch` / `_switch_external_only`（「读失败」不等于「这台机器没有外部交换机」两种不同断言）。测试已按此改写。

## 新增/改写的回归用例（14 条）

| 文件 | 条数 | 覆盖 |
| --- | --- | --- |
| `managementAdapters.test.ts` | +6（24 总） | `mergeAdapterBatch`：保留未提及行、batch 同名优先、保留 `LastError` 通道、不产生重复 name、空 batch 返回新引用、空表全盘接收 |
| `HyperVAdapterPanel.test.tsx` | +5（24 总） | 重启提示未创建时隐藏 / 出现在横幅区而非对话框 / 与进行中批次并存 / dismiss 回调；`switchesFailed` 警告文案 + 重试回调 + 创建仍禁用 |
| `VirtualAdaptersPage.test.tsx` | +3、改写 1（18 总） | M1 合并后两行共存且出口池徽章不丢；M2 创建成功后提示落在 `.virtual-adapters-restart` 且带 `create-done` 通知、可关闭；M3 离开页面后 settle 再回来刷新/创建/删除均可用；M4 改写为断言新文案 + 重试真的重发 `switches()` |

**回归探针（已实测）**：临时把 `mergeAdapterBatch` 退回 `setAdapters(next)`、把 `finally` 退回 `if (mounted.current) setBusy(false)`，重跑得到 **恰好 2 条失败**：

```
× merges the created batch instead of replacing the table
× stays writable when a write settles while the page is in the background
Tests  2 failed | 16 passed (18)
```

证明两条用例真的咬住了 M1/M3，不是陪跑。探针随后已完全还原（`TEMP-REGRESSION-PROBE` 标记 0 处，已 grep 确认）。

## 修复轮门禁结果（workdir `desktop\frontend`，真实退出码）

| 命令 | 退出码 | 结果 |
| --- | --- | --- |
| `& ".\node_modules\.bin\tsc.cmd" --noEmit` | **0** | 零错误 |
| `& ".\node_modules\.bin\vitest.cmd" run` | **0** | **45 files / 301 tests passed**（修复前 287，净增 14；既有套件无回归） |
| `& ".\node_modules\.bin\vite.cmd" build` | **0** | `✓ built in 5.48s`；产出 `VirtualAdaptersPage-hOcgKW4K.js (12.27 kB │ gzip 3.75 kB)` + `VirtualAdaptersPage-CQiRbZx9.css (4.07 kB)` |

## 修复轮风险与遗留

1. **`mergeAdapterBatch` 只在内存里合并** —— 真正的权威仍是下一次 3s 轮询的 `list()`。若轮询失败，表格会停留在「已合并但可能已过期」的状态（与 M4 的解释条并存，可读性可接受）。
2. **`restartRequired` 不跨会话持久化** —— 重启应用后提示消失。这是刻意的：聚合引擎已随应用重启过一次，再提示反而是噪声。
3. **重启提示仍只说「需要重启」，没说在哪重启** —— 文案 key 是既有的 `virtual_adapters_restart_hint`（lead 批准范围外不得新增），不覆盖重启入口引导。如需引导按钮请 `lead` 决定是否新增 key。
4. **审计的 9 项建议（S1–S9）本轮未动** —— 含 lead 已记下的 S1（`notifyOnce` 对 info/success 上闩导致删第 2 张起无成功提示）与 S4（32 总量无前端余量校验）。M2 的完成通知我已用显式 `notify` 规避 S1 的同类问题，但 S1/S4 本身仍待后续轮次。
5. **Go 下发的 `hypervPoolRestartHint` 仍是中文字符串**（审计指出的文案泄漏）—— 本轮改为前端自有提示后，该通道在前端**已无消费者**，实际不再显示；若将来 Go 侧改走 i18n，这条泄漏自然消失。Go 侧改动不在我范围内。
6. **`withServiceTimeout` 的操作名传错**（审计建议项）—— 前端传的第三个参数是给用户看的本地化提示串、非操作名；当前实现只用作 reject message，不影响行为，未改。