# 73 — 前端接线层交接（T-C1 / `frontend-vnic-wiring`）

范围：把冻结契约 `reports/vnic/70-frozen-hyperv-interface.md` §4 的前端接线落到
`services.ts` / 导航 / `App.tsx` / i18n / 导航测试。页面与面板（task-3）不在本文件范围内。

写范围（本任务唯一改动文件）：

- `desktop/frontend/src/platform/services.ts`
- `desktop/frontend/src/components/shell/CompactNavigation.tsx`
- `desktop/frontend/src/components/shell/CompactNavigation.test.tsx`
- `desktop/frontend/src/App.tsx`
- `desktop/frontend/src/i18n/legacy.messages.json`
- 本报告

**未改动**：`app.css`（契约 §4 明确不改）、`pages/**`、`components/vnic/**`、任何 Go 文件、
`bindings/**`（gitignored，CI 用 `wails3 generate bindings -clean=true -ts -i` 重新生成）。

---

## 1. 改动清单

### 1.1 `platform/services.ts`

| 位置 | 改动 |
| --- | --- |
| `:4` | 新增 `import * as HyperVAdapterService from "../../bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/hypervadapterservice";`（按字母序放在 engineservice 之后） |
| `:15-16` | 生成类型改为 `HyperVAdapterStatus as GeneratedHyperVAdapterStatus`、`HyperVSwitch as GeneratedHyperVSwitch`；**删除** `VirtualAdapterStatus as GeneratedVirtualAdapterStatus` |
| `:76-77` | **删除** `export type VirtualAdapterStatus = GeneratedVirtualAdapterStatus;`，改为 `export type HyperVAdapterStatus = GeneratedHyperVAdapterStatus;` + `export type HyperVSwitch = GeneratedHyperVSwitch;`（注释指向 70 §3.2 字段名） |
| `:364-372` | **删除**旧 `virtualAdapter` 分组与旧 `virtualadapterservice` import，替换为契约 §4 的逐字分组 |
| `:281-283` | 新增三个超时预算常量导出（理由见 §2） |

新分组逐字照抄 70 §4：

```ts
virtualAdapters: {
  list: () => HyperVAdapterService.List(),
  switches: () => HyperVAdapterService.Switches(),
  create: (switchName: string, count: number) => HyperVAdapterService.Create(switchName, count),
  remove: (name: string) => HyperVAdapterService.Remove(name),
},
```

### 1.2 `components/shell/CompactNavigation.tsx`

| 位置 | 改动 |
| --- | --- |
| import 区 | 新增 `VirtualNetwork24Filled` / `VirtualNetwork24Regular`（`@fluentui/react-icons`，按字母序插入） |
| `:16` | `AppPage` 联合类型追加 `\| "virtual-adapters"`（位于 `"connections"` 与 `"settings"` 之间） |
| `:37` | `mainItems` 在 `connections`（`:36`）之后、`tools`（`:38`）之前插入 |

```tsx
{ id: "virtual-adapters", label: t("nav_virtual_adapters"), icon: <VirtualNetwork24Regular />, activeIcon: <VirtualNetwork24Filled /> },
```

图标导出已**实证**（非凭记忆）：`desktop/frontend/node_modules/@fluentui/react-icons/lib/atoms/fonts/virtual-network.d.ts`
中确有 `VirtualNetwork24Regular` / `VirtualNetwork24Filled` 两个 `FluentFontIcon` 常量。
注意真实路径是 `lib/atoms/fonts/`，不是 `lib/fonts/`。

### 1.3 `App.tsx`（按 63 §1.2 的 4 处）

| 位置 | 改动 |
| --- | --- |
| `:34` | lazy 导入，命名导出映射：`const VirtualAdaptersPage = lazy(() => import("./pages/VirtualAdaptersPage").then((module) => ({ default: module.VirtualAdaptersPage })));`（置于 ToolsPage 与 SettingsPage 之间） |
| `:72` | DEV 深链白名单追加 `requested === "virtual-adapters" \|\|` |
| `:92` | `pageOrder` 在 `"connections"`（`:91`）之后插入 `"virtual-adapters",` |
| `:169-170` | `renderPage` 嵌套三元链追加 `: target === "virtual-adapters" ? <VirtualAdaptersPage />`，放在 `target === "routing"` 分支之前 |

**顺序一致性已核对**：`mainItems`（CompactNavigation `:33-39`）与 `pageOrder`（App `:88-97`）的相对顺序
逐项一致 —— home, routing, health, connections, virtual-adapters, tools, settings。`pageOrder` 决定
前进/后退的滑动方向，导航与它不一致会导致翻页动画方向错误。

### 1.4 `i18n/legacy.messages.json`

zh 段 `:100-130`、en 段 `:475-505`，两段各 31 个 key，均插在 `nav_blocked_domains` 之后。
key 集合与分配给我的冻结清单**逐字一致**，无增无减。

已用脚本校验：zh 31 / en 31；zh 有 en 无 = 空；en 有 zh 无 = 空；缺失 = 空；多余 = 空；
**zh/en 占位符集合逐 key 相同**。（占位符不一致会让某一语言把 `{max}` 原样渲染给用户 —— `i18n.tsx:31` 的
`formatMessage` 正则 `/\{(\w+)(?::[^}]+)?\}/g` 只替换消息里实际存在的占位符。）

带占位符的 5 个 key（按分配清单）：`count_hint`→`{max}`；`total_hint`→`{used}` `{total}`；
`summary`→`{ready}` `{total}`；`create_banner`→`{count}`；`unavailable`→`{reason}`。

文案基调已按 70 的 Hyper-V 语义校准（而非 63 草案里的 Wintun 措辞）：中文统一写「虚拟网卡（Hyper-V）」，
`switch_hint` 说明必须选 AllowManagementOS 的外部交换机，`dhcp_slow` 说明 60s 软失败语义，
`foreign` 区分非本工具创建的网卡，`restart_hint` 说明创建后需重启聚合引擎。

### 1.5 `components/shell/CompactNavigation.test.tsx`

`:17` 追加 1 条断言（该文件 mock 的 `t` 返回 key 本身，故按 key 名查找）：

```tsx
expect(screen.getByRole("button", { name: "nav_virtual_adapters" })).toBeTruthy();
```

---

## 2. 超时取值理由

### 先读了既有实现的语义

`withServiceTimeout`（`services.ts:257-273`）是裸的 `Promise.race([request, timeout])`：
它**只让等待方 reject，不会取消底层 Wails 调用**（也没有 `$Call.Cancel()`）。这是选值的关键前提。

### 取值

| 常量 | 值 | 用途 |
| --- | --- | --- |
| `HYPERV_ADAPTER_READ_TIMEOUT_MS` | `10_000` | `list()` / `switches()` |
| `HYPERV_ADAPTER_WRITE_TIMEOUT_MS` | `60_000` | `remove()` |
| `HYPERV_ADAPTER_CREATE_TIMEOUT_MS` | `180_000` | `create()` |

### 为什么 create 用 180s 而不是 60s

1. **DHCP 预算本身是 60s**（70 §3.5：适配器出现 15s → IP 就绪 45s → **整体 60s**）。批量建卡时这个等待是
   **串行累加**在提权子进程里的，60s 只会刚好卡在边界上。
2. **提权链路的时间不可控**：70 §3.3 要求 runas 拉起一次性 PowerShell 子进程，**UAC 弹窗要等用户点**，
   PowerShell 冷启动 + `Hyper-V` 模块加载也是无上界的。这两项在 60s 之外。
3. **超时不会中止工作，只会误报**。因为 `withServiceTimeout` 不取消底层调用，短超时会让 UI 报"失败"，
   而提权子进程**仍在继续建卡**。用户据此重试 → 撞上 70 §3.7 的**每批 16 张 / 总计 32 张**上限，
   把一个慢操作变成一个撞配额的问题。这是最坏情形。
4. 反方向的风险可忽略：预算过长最多是错误提示晚一点出现，不会产生错误状态。

180s = 60s DHCP + UAC/模块加载余量。

### 为什么是导出常量，而不是在 facade 里直接包一层

契约 §4 把 `virtualAdapters` 分组**逐字**钉死为裸透传，所以超时不能写进分组里。另外，
仓库既有惯例也是把 `withServiceTimeout` 放在**页面调用点**而非 facade 内
（`pages/ConnectionsPage.tsx:223`、`pages/HealthPage.tsx:151,168`、`state/useEngineState.ts:499,524`、
旧 `VirtualAdapterPanel.tsx:162,187`）。因此导出常量、由页面按操作选值，既贴合契约又不破坏既有惯例，
并且给页面成员保留了传本地化操作名的能力（旧面板是这么做的）。

> 交接提醒（task-3）：请在页面里显式用这三个常量包 `withServiceTimeout`，并给 `create()` 用
> `HYPERV_ADAPTER_CREATE_TIMEOUT_MS`。create 期间需要撑住横幅（`create_banner`）。

---

## 3. 验证结果

在本机 `desktop/frontend` 下跑的（node v24，无 pnpm，直接用 `node_modules\.bin`）：

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 类型检查 | `.\node_modules\.bin\tsc.cmd` | **我负责的 5 个文件零错误**；剩余错误全部在 task-3 的文件里（见下） |
| 测试 | `.\node_modules\.bin\vitest.cmd` run | **43 个文件 / 243 个用例全部通过，exit 0** |
| i18n 校验 | node 脚本读 JSON | 31/31 双语齐备，无多余 key，占位符 zh/en 一致 |
| 构建 | `.\node_modules\.bin\vite.cmd` build | **失败，但唯一原因是 task-3 未完成的文件**（见 §4） |

第一次跑 vitest 时看到的 `exit code 1` 是 PowerShell 管道（`| Select-Object -Last`）造成的假象；
不带管道重跑得到干净的 `EXITCODE=0`。

---

## 4. 未验证项 / 阻塞他人（请 lead 关注）

以下**都不在我的写范围内**，我没有去动它们。

1. **`src/pages/VirtualAdaptersPage.tsx` 依赖的 `./VirtualAdaptersPage.css` 不存在**
   ⇒ `vite build` 唯一失败原因：
   `[UNRESOLVED_IMPORT] Could not resolve './VirtualAdaptersPage.css' in src/pages/VirtualAdaptersPage.tsx`。
   （该文件在我开工时还不存在，期间 task-3 建好了，页面导出命名 `VirtualAdaptersPage`，与我的 lazy 导入匹配，
   `App.tsx` 早先的 TS2307 已随之消失。）

2. **旧 Wintun 面板仍在引用我删除的导出**，需由 task-3 删除这两个文件（契约 §4 要求删）：
   - `src/components/vnic/VirtualAdapterPanel.tsx` —— `TS2305: no exported member 'VirtualAdapterStatus'`（`:15`）、
     `TS2551: Property 'virtualAdapter' does not exist ... Did you mean 'virtualAdapters'?`（`:121` `:163` `:188`）、
     `TS2339: Property 'interfaceName' does not exist on type '{}'`（`:172`）
   - `src/components/vnic/VirtualAdapterPanel.test.tsx` —— `TS2305`（`:7`）

   这些报错是**我按契约删除旧 scheme 的预期后果**，不是回归。删掉这两个文件后即消失。
   `components/vnic/vnic.css` 按契约**保留**。

3. **`src/components/vnic/HyperVAdapterPanel.tsx:292` 的类型错误属于 task-3**：
   `TS2322: Type 'HyperVSwitch[]' is not assignable to type 'string[]'`。
   按 70 §3.2，`Switches()` 返回的是 `HyperVSwitch[]`（含 `name`/`type`/`allowManagementOs`/`uplink`/`netAdapterName`），
   不是 `string[]`；若面板把下拉选项当字符串数组用，需要改成对象数组或取 `name` 映射。

4. **超时预算未做真机验证**：180s / 60s / 10s 是按契约推算的，实机（真 UAC + 真 DHCP）耗时需要
   task-3 或 task-1 在真实 Windows + Hyper-V 环境下复核一次。

5. **i18n 文案未做双语母语审校**，是按 63 §7 的基调 + 70 语义写的；若产品侧对措辞有偏好需另行调整。

6. **binding 占位文件未重新生成**：本地用的是仓库里现成的占位 bindings（method id 1001-1005），
   真实绑定要等 CI / 本地 `wails3 generate bindings -clean=true -ts -i` 生成后，
   `HyperVAdapterService.Create/List/Remove/Switches` 的签名才最终可信。本任务所有代码只依赖这 4 个方法名与 2 个模型名。