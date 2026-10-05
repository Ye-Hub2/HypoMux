# 63 · 前端：虚拟网卡独立管理页面（Hyper-V 宿主机 vNIC）设计方案

- 任务来源：Team Lead `lead` 消息 `m00418`；上游用户硬性要求 `m01604`。
- 本轮性质：**只读设计**。本轮未修改 `desktop/**`、`engine/**` 下任何文件，仅新建本报告。
- 验证状态（重要）：本机**无 pnpm / node_modules / Go 工具链 / `desktop/frontend/bindings/`**，无法运行 `tsc`、`vitest` 或任何 Go 构建。本报告所有结论均来自逐字源码阅读，凡涉及「某个 npm 导出是否存在」「运行时行为」的，一律标注 **未验证**。
- 交付物：本文件 `reports/vnic/63-frontend-hyperv-page.md`。

---

## §0 结论摘要（TL;DR）

| 决策项 | 结论 |
| --- | --- |
| 页面标识 | **`"virtual-adapters"`**（不用 `"vnics"`，理由见 §1.1） |
| 需改动的既有文件 | `desktop/frontend/src/components/shell/CompactNavigation.tsx`（2 处）、`desktop/frontend/src/App.tsx`（4 处）、`desktop/frontend/src/pages/HomePage.tsx`（3 段删除）、`desktop/frontend/src/i18n/legacy.messages.json`（新增 key）、`desktop/frontend/src/platform/services.ts`（分组重写 + binding 导入路径） |
| 新增文件 | `pages/VirtualAdaptersPage.tsx`、`components/vnic/HyperVAdapterPanel.tsx`、`components/vnic/managementAdapters.ts`（纯函数）、`pages/virtual-adapters.css`、配套 3 个测试文件 |
| 删除文件 | `components/vnic/VirtualAdapterPanel.tsx`、`components/vnic/VirtualAdapterPanel.test.tsx`（旧 Wintun 方案，与新产品语义不同；`components/vnic/vnic.css` **保留**） |
| `app.css` 是否要改 | **不需要改**（页面骨架走页面自有 CSS，先例 `pages/mtu.css:1`；若 Lead 要求路由页统一骨架，最小改动是往 `app.css:4772-4776` 选择器组追加 1 行） |
| 主页清理净效果 | `HomePage.tsx` 366 行 → 350 行（-16 行：1 行 import、8 行 `notifySuccess`、6 行 JSX、1 行空行） |
| `HomePage.test.tsx` | **无需修改既有断言**（17 个用例均不触碰该面板）；建议新增 1 条「主页不再渲染虚拟网卡」回归断言 |
| 最大外部依赖 | Go 服务的**改名联动**（binding 文件名）+ **能力探测字段**（Hyper-V 不可用降级）+ **批量数量上限**，见 §5.3/§8 |

---

## §1 导航接入

### 1.1 页面标识：选 `"virtual-adapters"`

`AppPage` 是字符串联合（`desktop/frontend/src/components/shell/CompactNavigation.tsx:16`），同时被用作 URL 查询参数（`desktop/frontend/src/App.tsx:70-74`）与 DOM 稳定 key（`desktop/frontend/src/components/shell/AppShell.tsx:51`）。

选 `"virtual-adapters"` 的理由：

1. **与后端命名族一致**：前端服务分组现名 `virtualAdapter`（`desktop/frontend/src/platform/services.ts:350`）、Go 服务类型 `VirtualAdapterService`（`desktop/internal/services/virtual_adapter.go:46`），新方案仍在这条命名族上收敛，页面标识与服务名同源，重命名时一起走。
2. **URL 可读**：`?page=virtual-adapters` 自解释（DEV 深链白名单 `App.tsx:70-74` 会暴露该串）。
3. **避开 `"vnics"` 的语义雷区**：`vnic` 这个缩写在仓库里已经被**旧 Wintun 方案**占用——`engine/internal/vnic/manager.go`、`engine/internal/api/v1/types.go:286 VNICCreateParams`、`engine/internal/tun/supervisor.go:37 VNICInterfaceName`，甚至用户可见文案里都有 `Virtual NIC cannot start yet`（`desktop/frontend/src/pages/HomePage.test.tsx:326` 断言的引擎 issue 标题）。新页面若叫 `vnics`，会与「那个没用的三层设备」共用词汇，后续 issue/日志无法区分新旧。**术语建议**：新方案中文统一叫「虚拟网卡（Hyper-V）」，英文「virtual adapter（Hyper-V）」，避免只写 VNIC。

### 1.2 精确改动清单（file:line）

| # | 文件:行号 | 改动 |
| --- | --- | --- |
| 1 | `desktop/frontend/src/components/shell/CompactNavigation.tsx:16` | 联合类型追加 `\| "virtual-adapters"` |
| 2 | `desktop/frontend/src/components/shell/CompactNavigation.tsx:30-37` | `mainItems` 在 `connections` 项之后、`tools` 项之前插入一项（见 §1.3 文案/§1.4 图标） |
| 3 | `desktop/frontend/src/App.tsx:28-34` | 新增 lazy 页面：`const VirtualAdaptersPage = lazy(() => import("./pages/VirtualAdaptersPage").then((module) => ({ default: module.VirtualAdaptersPage })));` |
| 4 | `desktop/frontend/src/App.tsx:69-73` | DEV 深链白名单追加 `requested === "virtual-adapters"` |
| 5 | `desktop/frontend/src/App.tsx:85-95` | `pageOrder` 数组在 `"connections"` 之后插入 `"virtual-adapters"`（决定 `navigate()` 的前进/后退滑动方向，必须与导航顺序一致——`App.tsx:97-107`） |
| 6 | `desktop/frontend/src/App.tsx:143-170` | `renderPage` 嵌套三元链追加分支 `target === "virtual-adapters" ? <VirtualAdaptersPage /> : ...`（插在 `"connections"` 分支之后保持可读） |
| 7 | `desktop/frontend/src/i18n/legacy.messages.json` | 新增 key（zh 段与 en 段各一份，见 §7） |
| 8 | `desktop/frontend/src/components/shell/CompactNavigation.test.tsx` | 追加 1 条断言（见 §6.3），既有 4 组按名断言不受影响 |

**不需要改**：
- `desktop/frontend/src/components/shell/AppShell.tsx`：`visited`（`:28-30`）与 `pages.filter`（`:51`）对 `AppPage` 完全泛化，新页面自动获得「按需挂载 + 访问后常驻 + `PageActivity` 门控」行为。
- `desktop/frontend/src/components/shell/AppShell.test.tsx`：仅使用 `"routing"`/`"settings"` 两个标识（`:23-43`），不受影响。
- `desktop/frontend/src/App.tsx:130-142` 的 `persistentPage="home"`：主页仍常驻挂载（这是「保留草稿/滚动」的既有机制），**与「主页不再显示虚拟网卡」不冲突**——把面板从 `HomePage` 移除即可。

### 1.3 导航项与文案

`CompactNavigation.tsx:34-35` 显示该文件的既有惯例是**两种通道混用**：`t("nav_*")`（如 `:31 t("nav_home")`）与内联 locale 三元（如 `:34 locale === "en" ? "Connections" : "活动连接"`）。

**本设计统一走 i18n key 通道**（遵守 Lead 的硬约束 ⑦，且导航文案是全局的、更值得进 `legacy.messages.json`）：

```tsx
// desktop/frontend/src/components/shell/CompactNavigation.tsx:30-37 内新增一行
{ id: "virtual-adapters", label: t("nav_virtual_adapters"), icon: <VirtualNetwork24Regular />, activeIcon: <VirtualNetwork24Filled /> },
```

- 导航项类型：`mainItems` 现有元素形如 `{ id, label, icon, activeIcon? }`，`:102` 用 `active && item.activeIcon ? item.activeIcon : item.icon` 选择；新项的 `id` 需要 `as AppPage` 断言（`:99` 处已有 `onPageChange(item.id as AppPage)`，无需额外改类型）。

### 1.4 图标

`desktop/frontend/src/components/shell/CompactNavigation.tsx:2-15` 现有导入：`Home24Regular/Home24Filled/BranchFork24Regular/HeartPulse24Regular/PlugConnected24Regular/Toolbox24Regular/Settings24Regular/Beaker24Regular/Info24Regular`（**这些已确认存在**，可直接照抄写法）。

推荐（按优先级）：

1. `VirtualNetwork24Regular` + `VirtualNetwork24Filled` —— 语义最贴合「虚拟网卡」。
2. 备选 `Server24Regular` / `Server24Filled`（Hyper-V 宿主视角）。
3. 保守备选：复用已确认存在的 `PlugConnected24Regular`（但它在 `:35` 已用于「活动连接」，不建议重复）。

> **未验证**：本机无 `node_modules`，无法确认 `VirtualNetwork24*` / `Server24*` 在 `@fluentui/react-icons`（版本见 `desktop/frontend/package.json`）中确实导出；全仓库 grep `VirtualNetwork|Server24Regular|Desktop24Regular|CloudAdd24Regular` 亦零命中。实施阶段必须在有依赖的环境里先做一次存在性检查（例如 `node -e "console.log(Object.keys(require('@fluentui/react-icons')).filter(k=>/VirtualNetwork|Server24/.test(k)))"`）或直接跑 `pnpm --dir desktop/frontend build`（`tsc` 会报未导出成员）。**图标名错误会让 CI build 直接失败。**

---

## §2 主页清理（精确 diff 描述）

### 2.1 三处删除

`desktop/frontend/src/pages/HomePage.tsx`（现 366 行）：

1. **删除 `:26`**
   `import { VirtualAdapterPanel } from "../components/vnic/VirtualAdapterPanel";`
   —— 该导入在文件中只有这一处出现，删除后无残留引用。

2. **删除 `:67-74`**（8 行，含依赖数组行）
   ```ts
   const notifySuccess = useCallback((message: string) => {
     notify({ title: t("infobar_success"), message, intent: "success", dedupeKey: "home:vnic-success" });
   }, [notify, t]);
   ```
   证据：`notifySuccess` 在 `HomePage.tsx` 内仅出现于 `:67`（定义）与 `:252`（传给面板）；grep 全仓库亦无其它使用点。删除后：
   - `notifyError`（`:57-66`）**必须保留**——它另有 `:91`、`:100` 两处使用（今日免提醒的保存失败、`useEngineState(notifyError, handleTunPreflight)`）。
   - `useCallback` 导入**保留**（`notifyError` 仍在用）。
   - `notify`（来自 `useAppNotifications()`）**保留**（`notifyError` 与其它调用仍在用）。
   - `t("infobar_success")` 这个 legacy key **不能删**：`desktop/frontend/src/pages/SettingsPage.tsx:399` 仍在用（key 定义见 `desktop/frontend/src/i18n/legacy.messages.json:39` zh / `:383` en）。

3. **删除 `:249-254`**（6 行 JSX）
   ```tsx
   <VirtualAdapterPanel
     locale={locale}
     text={text}
     notifySuccess={notifySuccess}
     notifyError={notifyError}
   />
   ```
   位置上下文：network 区块 `</section>`（`:247`）之后、`<RuntimeStatusBar strategy={engine.strategy} … />`（`:256`）之前，且 `:248` 与 `:255` 各是一个空行。删除时应连带删掉其中一个空行，避免连续两个空行（提交前跑一次 prettier/eslint 更稳妥）。

**净效果**：-16 行，`HomePage.tsx` 366 → 350 行。删除后 `HomePage.tsx` 中不再出现任何 `virtual`/`VirtualAdapter` 标识。

### 2.2 是否为面板新增过 state / 样式（核查结论）

- **state**：无。`HomePage.tsx` 未为面板引入 `useState`/`useRef`；面板自身状态（`PanelState`、对话框开关）全在 `VirtualAdapterPanel.tsx` 内部（`:36 PanelState`、`:100-108` 状态）。
- **样式**：无。面板样式全部在 `desktop/frontend/src/components/vnic/vnic.css`（13 行，仅 `.virtual-adapter-*` 类），由组件自身 `import "./vnic.css"`（`VirtualAdapterPanel.tsx:16`）。`app.css` 中不存在 `.virtual-adapter-*` 规则（grep 无命中）。
- **因此主页清理是纯删除，不需要任何补偿改动。**

### 2.3 旧文件的处置建议

| 文件 | 建议 | 理由 |
| --- | --- | --- |
| `desktop/frontend/src/components/vnic/VirtualAdapterPanel.tsx`（397 行） | **删除** | 旧语义 = 单张 Wintun 三层设备 + `create(interfaceName, address)`；新语义 = 批量 Hyper-V `ManagementOS` 卡 + 数量驱动。复用其 UI 骨架得不偿失，且 `Create(interfaceName, address)` 签名在 Go 侧必然改变（§5）。 |
| `desktop/frontend/src/components/vnic/VirtualAdapterPanel.test.tsx`（240 行） | **删除** | 9 个用例中有 7 条断言是 IPv4 校验（`:107`），新产品没有「地址」输入框（IP 由路由器 DHCP 分配）⇒ 只有「状态渲染」类思路可迁移。 |
| `desktop/frontend/src/components/vnic/vnic.css`（13 行） | **保留并复用** | `.virtual-adapter-details`（`:7`）、`.virtual-adapter-hint`（`:9`）、`.virtual-adapter-error`（`:11`）、`.virtual-adapter-fields`（`:12-13`）与新面板仍然匹配；`.virtual-adapter-intro`/`.virtual-adapter-section` 若无引用再删（实施阶段 grep 后决定）。 |

### 2.4 `HomePage.test.tsx` 是否需要调整

**既有 17 个用例（15 个 `it` + 2 个 `it.each` 各展开 2 例，`desktop/frontend/src/pages/HomePage.test.tsx:94-331`）全部不需要改。** 逐条核查：

- 唯一与「弹窗」相关的断言是 `:301 expect(screen.queryByRole("dialog")).toBeNull()`，它属于启动风险提醒用例（`:287-302`），断言的是**提醒对话框在今日已忽略后不再出现**；`VirtualAdapterPanel` 的对话框默认关闭（创建/移除对话框只在点按钮后打开），两者无交集。
- `:326 expect(screen.getByText("Virtual NIC cannot start yet")).toBeTruthy()` 断言的是**引擎 issue 标题**（用例 `:319-331` 注入 `{ code: "missing_core", level: "blocker", title: "Missing core" }` 后由启动提醒渲染的阻断文案），与面板无关。注意：这条 `Virtual NIC` 文案来自引擎，属于 §1.1 提到的术语撞名。
- 该测试文件 `:15-30` 只 mock 了 `../state/useEngineState` 与 `../i18n/i18n`，**不 mock `../platform/services`**，因此页面挂载时 `services.ts` 会被真实加载、`VirtualAdapterPanel` 的 `status()` 会真实发起 Wails 调用（jsdom 下会 reject → 面板进入 `loadFailed`，不弹 dialog）。**删除面板反而消除了一处真实调用**，让该测试更快更稳。

**建议（可选，非必须）**：追加一条回归断言，锁住「主页不再出现虚拟网卡入口」，例如在 `describe("HomePage adapter interactions")`（`:93`）内新增：

```tsx
it("no longer renders the virtual adapter panel on the home page", () => {
  renderPage(<HomePage />);
  expect(screen.queryByText("nav_virtual_adapters")).toBeNull();   // i18n mock 返回 key 本身
  expect(screen.queryByRole("button", { name: /VNIC|虚拟网卡|Virtual adapter/i })).toBeNull();
});
```

注意 `HomePage.test.tsx:19-30` 的 i18n mock **只提供 `locale` 与 `t`**（`t: (key) => map[key] ?? key`，未知 key 原样返回）——新增断言时必须按这个 mock 的形状写期望值，不要依赖 `text(zh, en)`（`HomePage` 自己用 `locale` 构造 `text`，见 `HomePage.tsx:38` 附近）。

---

## §3 新页面与组件设计

### 3.1 文件与职责切分

| 文件 | 角色 | 职责 |
| --- | --- | --- |
| `desktop/frontend/src/pages/VirtualAdaptersPage.tsx`（新，约 260-320 行） | **容器 / 路由页** | `useI18n()`、`useAppNotifications()`、`usePageActive()`、轮询、`appServices.virtualAdapters.*` 调用、快照状态持有、通知与错误处理、浏览器预览夹具、把纯数据 + 回调传给面板 |
| `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx`（新，约 320-400 行） | **展示型面板** | 页头（标题/统计/刷新/批量创建）、降级 MessageBar、列表与行状态呈现、空状态、批量创建对话框、删除确认对话框、DHCP 等待进度提示。**不直接调用 service** |
| `desktop/frontend/src/components/vnic/managementAdapters.ts`（新，约 60-90 行） | **纯函数** | 数量校验、状态标签/徽章映射、就绪计数汇总、MAC/时间格式化。便于单测覆盖（仓库先例：`pages/connectionView.ts`、`pages/routingBatch.ts` 这类纯模块 + 同名 `.test.ts`） |
| `desktop/frontend/src/pages/virtual-adapters.css`（新） | 页面骨架 | 见 §4 |

**为什么容器/展示分离**：与既有 `HealthPage` → `MTUDetectionPage`/`NATDetectionPage` 的切分一致（`HealthPage.tsx:570` 把 `adapters/enginePhase/loading/preview/text` 传进子面板）；好处是面板测试可以直接喂快照，不需要 mock service（`MTUDetectionPage.test.tsx:11` 就是这么做的），而轮询/通知逻辑只在页面测试里 mock `../../platform/services`（先例 `pages/HealthPage.test.tsx:15`、`pages/RoutingPage.test.tsx:8-11`）。

页面根元素：`<main className="virtual-adapters-page">` + `<header className="page-heading">`，完全照抄 `BlockedDomainsPage.tsx:89-98` 的结构（该文件是仓库里最接近的「独立管理页」模板：`pageActive` 门控 + 序号比对 + `startSerialPoll` + 手写表格 + 对话框），并在此基础上加一个 `.virtual-adapters-toolbar`（刷新 / 批量创建按钮）。

### 3.2 props 契约

```ts
// desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx
export type HyperVAdapterState = "creating" | "awaiting-dhcp" | "ready" | "failed" | "removing";

export type HyperVAdapterView = {
  id: string;                 // 稳定标识：建议 Go 侧给 adapter GUID 或唯一 Name（删除/定位用）
  name: string;               // 例：HypoMux-VNIC-03
  mac: string;                // 唯一 MAC（大写冒号分隔，展示用）
  state: HyperVAdapterState;
  address?: string;           // 仅在 ready 时有值（DHCP 分配结果）
  prefix_length?: number;
  switch_name?: string;       // 归属的 Hyper-V 外部交换机名
  created_at: string;         // ISO8601
  last_error?: string;
  in_outbound_pool?: boolean; // 「已自动加入出口池」标记（由 Go 侧回传）
};

export type HyperVAdapterSnapshot = {
  capability: "available" | "unavailable"; // Hyper-V / ManagementOS 是否可用
  reason?: string;                         // capability === "unavailable" 时的原因
  adapters: HyperVAdapterView[];
  max_batch?: number;                      // 单次批量上限（Go 侧提供）
};

export type HyperVAdapterPanelProps = {
  text: (zh: string, en: string) => string;   // 与 MTUDetectionPage 的 text prop 同形
  snapshot: HyperVAdapterSnapshot | null;
  loading: boolean;
  loadFailed: boolean;
  busy: boolean;                              // 任一写操作进行中 → 禁用全部写按钮
  preview: boolean;                           // 浏览器预览：禁用写操作
  pendingCreateCount: number | null;          // 正在创建 N 张时 > 0：驱动页面级进度提示
  onCreate: (request: { count: number; namePrefix: string }) => void;
  onRemove: (adapter: HyperVAdapterView) => void;
  onRefresh: () => void;
};
```

- `onCreate/onRemove` 返回 `void`（页面内部 `void run()`），面板不 await —— 与 `BlockedDomainsPage.tsx:112-129` 的对话框回调风格一致。
- `MAX_BATCH` 默认取 `snapshot.max_batch ?? 32`；**上限值需 Go 侧定夺（未验证）**。
- 数量校验纯函数：`validateAdapterCount(raw: string, max: number): { ok: boolean; value: number; reason?: "empty" | "not-integer" | "too-small" | "too-large" }`。

### 3.3 状态机（行级）

| 状态 | 触发 | 呈现 |
| --- | --- | --- |
| `creating` | 已提交创建，Hyper-V 侧尚未返回/尚未落卡 | 行内 `<Spinner size="tiny" />` + `text("正在创建…", "Creating…")`，删除按钮禁用 |
| `awaiting-dhcp` | 卡已存在（有 `mac`）但尚无 `address` | `Badge appearance="tint" color="warning"` + `text("等待 DHCP 分配 IP", "Waiting for DHCP")`；显示 MAC；提示「路由器按 MAC 分配，通常数秒内完成」 |
| `ready` | `address` 有值 | `Badge appearance="tint" color="success"` + `address/prefix_length`（`font-variant-numeric: tabular-nums`，沿用 `vnic.css:7`）+ MAC；若 `in_outbound_pool` ⇒ 追加 `Badge appearance="tint"` + `text("已加入出口池", "In outbound pool")` |
| `failed` | `last_error` 有值 | `.virtual-adapter-error` 样式显示 `last_error`，提供「删除」与「刷新重试」 |
| `removing` | 已确认删除、等待返回 | 行整体 `aria-busy="true"`，按钮禁用 |

页面级状态：`loading`（首次）→ 骨架/加载文案；`loadFailed` → `MessageBar intent="error"` + 「重试」按钮（照 `BlockedDomainsPage.tsx:44-55` 的序号比对 + `notify` 错误通道，dedupeKey `"virtual-adapters:error:load"`）；`capability === "unavailable"` → `MessageBar intent="warning"` + `reason` 文案 + 禁用「批量创建」但保留「刷新」（降级提示）。

### 3.4 交互流程

**批量创建对话框**（`Dialog`，字段 2 个）：
1. **数量**：`<Input type="number" min={1} max={maxBatch} value={countDraft} onChange={(_, data) => setCountDraft(data.value)} />`。先例：`desktop/frontend/src/pages/SettingsPage.tsx:851-852`（`type="number" min={1} max={65534}` + `String(...)` + `Number(data.value)`）。**数量必须由用户手动填写**（上游 `m01604` 已确认的交互），默认 `1`，**不提供**「按缺额自动推算」。
2. **名称前缀**：默认 `HypoMux-VNIC`，用户可改（`virtualAdapter` 旧方案的默认名常量是 `"HypoMux-VNIC"`；新方案建议把后缀编号交给 Go 侧，`HypoMux-VNIC-01 …`）。

校验与提交：`validateAdapterCount` 不通过 ⇒ 主按钮 `disabled` + 行内 hint；通过 ⇒ `onCreate({ count, namePrefix })`，对话框关闭，页面顶部出现进度条区域：`<Spinner size="tiny" /> + text("正在创建 N 张虚拟网卡…", "Creating N virtual adapters…")`，并按 3s 轮询直到全部行离开 `creating`。

**DHCP 等待进度**：`awaiting-dhcp` 行本身就是进度提示；面板顶部再加一条汇总：`text("已就绪 x / 共 y", "x of y ready")`；若某行 `awaiting-dhcp` 持续超过阈值（建议 60s，与旧面板 `VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS = 60_000` 同量级），行内追加提示 `text("超过 60 秒仍未拿到 IP，请检查路由器 DHCP 地址池是否已满。", "No IP after 60s — check your router's DHCP pool.")`。**不承诺具体耗时**（未验证真实 DHCP 时延）。

**删除确认**：`Dialog` + `DialogTitle/DialogBody/DialogActions`（照 `BlockedDomainsPage.tsx:112-129`），文案需说明「该卡将从系统移除，其占用的 IP 配额会被路由器回收」。单卡删除；批量「全部删除」作为可选增强（建议放到后续迭代，避免一次误删全部配额）。

**空状态**：`adapters.length === 0 && !loading && !loadFailed` ⇒ 面板中央区域（沿用 `.virtual-adapter-body` 的居中网格样式）+ 说明文案 + 主按钮「批量创建」。

**「已自动加入出口池」提示**：仅在 `in_outbound_pool === true` 时显示；`false`/`undefined` 时**不显示任何断言性文案**（出口池接线在 Go 侧，前端不能替后端承诺）——见 §8 风险。

**Hyper-V 不可用降级**：`capability === "unavailable"` ⇒ `MessageBar intent="warning"`，文案由 `reason` 决定（未安装 Hyper-V / 没有外部交换机 / 需要管理员权限）；同时禁用创建，保留刷新与删除（已存在的卡仍可清理）。

**无二次确认**：创建对话框只做字段校验，不弹二次确认（旧面板同样无二次确认；批量语义下用户已显式键入数量）。删除**必须**二次确认。

### 3.5 轮询与页面活动门控（必须做对）

`AppShell` 让访问过的页面**永久挂载**（`desktop/frontend/src/components/shell/AppShell.tsx:50-53`，非当前页只是 `hidden`），所以新页面**必须**用 `usePageActive()` 门控轮询，否则离开页面后仍在每 3 秒打一次 Wails 调用：

```ts
// 结构参考 desktop/frontend/src/pages/BlockedDomainsPage.tsx:29-67
const pageActive = usePageActive();
const requestSequence = useRef(0);
const mounted = useRef(true);
useEffect(() => {
  if (!pageActive) return;
  void refresh();
  const stop = startSerialPoll(refresh, 3000);   // 先例：platform/serialPoll.ts
  return () => { stop(); requestSequence.current += 1; };
}, [refresh, pageActive]);
```

`mounted.current = false` / 序号比对用来丢弃过期响应（`BlockedDomainsPage.tsx:40-55`）。轮询间隔 3000ms 与 `BlockedDomainsPage` 一致。

### 3.6 浏览器预览模式（既有仓库惯例，必须遵守）

`desktop/frontend/src/platform/runtime.ts:22 export const isDesktopRuntime = () => System.IsDesktop() || hasNativeWailsBridge();`

页面按既有惯例取 `const preview = import.meta.env.DEV && !isDesktopRuntime();`（`ConnectionsPage.tsx:204`、`HealthPage.tsx:43`、`RoutingPage.tsx:927`），并：
1. 用本地夹具渲染 3 张卡（一张 ready、一张 awaiting-dhcp、一张 failed）以便视觉验收；
2. 禁用所有写按钮（`disabled={preview || busy || ...}`，先例 `RuleSetsPanel.tsx:226`）；
3. 显示预览说明条：`<MessageBar intent="info">`（先例 `RuleSetsPanel.tsx:184`）或 `Badge appearance="tint" color="warning"`（先例 `ConnectionsPage.tsx:640`）。

这条很重要：`ConnectivityPage`/`HealthPage` 都支持浏览器预览，新页面若在预览下真实调用 Wails 会抛错（`platform/desktop.ts:8` 的 no-op 降级路径不覆盖 service 调用）。

---

## §4 样式归属：结论与理由

### 结论

**新代码继续走「功能局部 CSS」，本设计不需要改动 `app.css`。**

具体分配：

| 文件 | 内容 |
| --- | --- |
| `desktop/frontend/src/pages/virtual-adapters.css`（新） | 页面骨架：`.virtual-adapters-page`、`.virtual-adapters-toolbar`、`.virtual-adapters-summary`、`.virtual-adapters-list`、`.virtual-adapters-row`、`.virtual-adapters-empty`；由 `VirtualAdaptersPage.tsx` `import "./virtual-adapters.css";` |
| `desktop/frontend/src/components/vnic/vnic.css`（改） | 面板内部：沿用 `.virtual-adapter-body/.details/.hint/.error/.fields`，新增行状态/徽章/进度类；由 `HyperVAdapterPanel.tsx` `import "./vnic.css";` |
| `desktop/frontend/src/app.css` | **不动** |

`.virtual-adapters-page` 需自带的骨架值（与 `app.css:4772-4781` 的页面组保持一致，直接照抄数值，避免视觉漂移）：

```css
.virtual-adapters-page {
  width: min(100%, 1180px);
  height: 100%;
  margin: 0 auto;
  padding: calc(18px * var(--hm-density-space));
  overflow: auto;
}
```

### 理由

1. **先例充分**：`desktop/frontend/src/pages/MTUDetectionPage.tsx:6` `import "./mtu.css";`，而 `desktop/frontend/src/pages/mtu.css:1-10` 第 1 条规则就是页面根类 `.mtu-page { display: grid; …; overflow: auto; padding: 2px 2px 16px; }`。即**「页面骨架放页面自有 CSS」在本仓库已经发生过**，不是新发明。
2. **改 `app.css` 的代价高**：`desktop/frontend/src/app.css` 现 **5944 行**（Lead 消息里的 5,147 行已过时），是多人/多任务并行的最高冲突面文件；本轮之前该文件已有其它成员在改。页面组规则在 `app.css:4772-4781`（`.tools-page, .settings-page, .about-page, .blocked-domains-page`），追加选择器只是一行，但会把「新增页面」与「改全局样式表」绑定，增加合并冲突风险。
3. **可选替代方案**（若 Lead 坚持路由页骨架统一）：只往 `app.css:4772-4776` 的选择器组追加 `.virtual-adapters-page,` 一行（+1 行），`pages/virtual-adapters.css` 里则只放工具栏/列表等局部规则。这属于 Lead 的偏好选择，两种方式都能通过 `tsc`/构建。

### 需要确认的既有类

- `.page-heading`（`app.css:4783`）：**复用**，不要自己重定义。
- `.hm-card` / `.glass-surface`：**复用**（`desktop/frontend/src/components/material/GlassSurface.tsx:19` 输出 `glass-surface hm-card`），面板用 `<GlassSurface>` 包一层即可获得卡片外观（先例 `BlockedDomainsPage.tsx:100`）。
- `.page-transition-layer`（`app.css:913-1008`）会对 `> main > header > *` 做入场动画，新页面的 header 结构照抄 `BlockedDomainsPage.tsx:89-98` 即可安全命中。

---

## §5 service 层对接

### 5.1 建议的新分组

`desktop/frontend/src/platform/services.ts:350-354` 现为：

```ts
virtualAdapter: {
  create: (interfaceName: string, address: string) => VirtualAdapterService.Create(interfaceName, address),
  status: () => VirtualAdapterService.Status(),
  remove: () => VirtualAdapterService.Remove(),
},
```

建议改为（复数命名 + 批量语义）：

```ts
virtualAdapters: {
  list: () => VirtualAdapterService.List(),
  create: (count: number, namePrefix: string) => VirtualAdapterService.CreateBulk(count, namePrefix),
  remove: (id: string) => VirtualAdapterService.Remove(id),
},
```

要点：
- `status()`（单卡）→ `list()`（全量快照，含能力/上限）。
- `create()` 从 `(interfaceName, address)`（用户填地址）变为 `(count, namePrefix)`（地址由路由器 DHCP 决定）——**这是与用户交互直接对应的契约变化**。
- `remove()` 从「移除唯一一张」变为按 `id` 移除。
- 超时：继续用 `withServiceTimeout`（`platform/services.ts` 内已有），沿用 `virtualAdapter` 的 60s 量级（旧常量 `VirtualAdapterPanel.tsx:25 VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS = 60_000`）；`list()` 建议 10s（对齐 Go 侧 `vnicStatusTimeout = 10s`，`desktop/internal/services/virtual_adapter.go:20` 附近）。

### 5.2 需要从响应中读的字段（前端影响点）

- 能力/降级：`capability` + `reason`（新字段，见 §5.3）。
- 每卡：`id`、`name`、`mac`、`state`、`address`、`prefix_length`、`switch_name`、`created_at`、`last_error`、`in_outbound_pool`。
- 批量：`max_batch` 或独立常量。

### 5.3 ⚠️ Wails binding 改名联动（必须提示）

生成规则（仓库事实）：CI 先执行 `wails3 generate bindings -clean=true -ts -i`，产物落在 `desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/`，**文件名 = Go 服务类型名的小写形式**：现有导入可见 `adapterservice`、`diagnosticsservice`、`engineservice`、`routingruleservice`、`settingsservice`、`tunservice`、`virtualadapterservice`（`desktop/frontend/src/platform/services.ts:1-7`，对应 Go 类型 `AdapterService`/`VirtualAdapterService` 等）。CI 之后才跑 `pnpm --dir desktop/frontend test` 与 `build`（`.github/workflows/build.yml:139-150`）。

**因此**：Go 侧只要服务类型改名（例如 `VirtualAdapterService` → `HyperVAdapterService`），binding 文件就变成 `.../services/hypervadapterservice.ts`，前端 `platform/services.ts:7` 的 import 路径必须同步改，**否则 tsc/CI 立即失败**。前端受影响点清单：

| # | 位置 | 影响 |
| --- | --- | --- |
| 1 | `desktop/frontend/src/platform/services.ts:7` | `import * as VirtualAdapterService from "…/services/virtualadapterservice";` → 新文件名 |
| 2 | `desktop/frontend/src/platform/services.ts:24` | `VirtualAdapterStatus as GeneratedVirtualAdapterStatus` → 新模型类型名（`bindings/…/services/models` 中的导出） |
| 3 | `desktop/frontend/src/platform/services.ts:70-73` | `export type VirtualAdapterStatus = GeneratedVirtualAdapterStatus;` 别名（旧面板删除后应一并清理或改名） |
| 4 | `desktop/frontend/src/platform/services.ts:350-354` | 分组重写（§5.1） |
| 5 | 旧面板引用点 | `components/vnic/VirtualAdapterPanel.tsx:15` 的 `type VirtualAdapterStatus` 导入随文件删除而消失 |
| 6 | `desktop/frontend/src/pages/HomePage.tsx` | 不再引用该分组（§2） |

> 命名权在 Go 侧（`desktop-vnic-dev`）。前端**不预设**名字，但要求：Go 服务类型名一旦确定，前端按 §5.1 的 `virtualAdapters` 分组落地，并把上面 4 处路径/类型名一次性对齐。**未验证**：无法在本机生成 bindings 验证模型字段名（`desktop/frontend/bindings/` 不存在）。

---

## §6 测试方案（vitest）

前提（仓库事实，影响每个测试文件）：`desktop/frontend/vite.config.ts` 无 `test` 段/setupFiles ⇒ **每个 jsdom 测试文件首行必须写 `// @vitest-environment jsdom`**（先例 `HomePage.test.tsx:1`、`AppShell.test.tsx:1`）；`desktop/frontend/tsconfig.json` 开启 `noUnusedLocals` ⇒ 改完必须清掉未使用的 import/变量（旧面板删除后尤其注意 `HomePage.test.tsx` 无关，但新测试文件容易残留）。

### 6.1 纯函数：`components/vnic/managementAdapters.test.ts`（新）

1. `validateAdapterCount("0", 32)` → `too-small`；`"1"` → ok；`""` → `empty`；`"3.5"` → `not-integer`；`"abc"` → `not-integer`；`"33"`（max 32）→ `too-large`；`"32"` → ok。
2. `summarizeStates(adapters)` → `{ ready: n, awaitingDhcp: m, creating: k, failed: j }`（用于「已就绪 x / 共 y」）。
3. 状态 → 徽章/文案映射覆盖全部 5 个状态（含未知状态的安全回退，参照旧实现 `VirtualAdapterPanel.tsx:38-49` 的白名单收敛思路）。

### 6.2 组件：`components/vnic/HyperVAdapterPanel.test.tsx`（新，展示型，直接喂快照）

1. 空快照（`adapters: []`）渲染空状态与主按钮「批量创建」。
2. `capability: "unavailable"` + `reason` ⇒ 渲染降级 MessageBar，且创建按钮 `disabled`，刷新仍可用。
3. 列表渲染：`ready` 行显示 `address/prefix_length`、`mac`、`name`，并显示「已加入出口池」标记（`in_outbound_pool: true`）。
4. `awaiting-dhcp` 行显示等待提示且**不显示 IP**。
5. `failed` 行显示 `last_error`，并提供删除/重试入口。
6. `loadFailed` ⇒ 错误条 + 「重试」回调被调用。
7. 批量创建对话框：默认数量 `1`；输入 `"0"` ⇒ 提交按钮 `disabled`；输入 `"5"` ⇒ 点提交后 `onCreate` 收到 `{ count: 5, namePrefix: "HypoMux-VNIC" }`（**数量手动填写**的验收点）。
8. 删除：点击行删除 → 出现确认对话框 → 确认后 `onRemove(adapter)` 收到该行；取消则不被调用。
9. `preview: true` ⇒ 所有写按钮 `disabled` 且出现预览提示；`onRefresh` 仍可用。
10. `busy: true` ⇒ 写按钮全部 `disabled`，且行 `aria-busy` 正确。

### 6.3 容器：`pages/VirtualAdaptersPage.test.tsx`（新）

mock `../../platform/services`（`vi.hoisted` + `vi.mock`，先例 `RoutingPage.test.tsx:8-11`）与 i18n；用 `AppNotificationProvider` 包裹（先例 `HomePage.test.tsx:32-36`）。

1. 挂载即调用 `list()` 并渲染返回的卡。
2. 轮询：`vi.useFakeTimers()` 推进 3s ⇒ 第二次 `list()`（模式先例 `platform/serialPoll.test.ts`）。
3. **页面非活动时不轮询**：`<PageActivity.Provider value={false}>` 包裹渲染 ⇒ 无 `list()` 调用（这条是防止「离开页面仍打 Wails」的回归锁）。
4. 创建成功 ⇒ `notify` 成功通道被调用（`infobar_success`）+ 重新 `list()`。
5. 创建失败/超时 ⇒ `notify` 错误通道被调用（带 retry 回调），且页面不崩。
6. 删除需确认：未确认前不调用 `remove()`。
7. `preview`（`vi.mock("../../platform/runtime", () => ({ isDesktopRuntime: () => false }))` + DEV）⇒ 使用夹具且不调用 service 写方法。

### 6.4 迁移 / 删除

- `desktop/frontend/src/components/vnic/VirtualAdapterPanel.test.tsx` 整体删除；其中 `:107 describe("isValidIPv4")`（1 个 it、7 条断言）由 §6.1 的「数量校验」取代；`:119` 的 8 个用例按 §6.2 重写。
- `desktop/frontend/src/components/shell/CompactNavigation.test.tsx`（19 行）：追加 1 条断言，例如 `expect(screen.getByRole("button", { name: "nav_virtual_adapters" })).toBeTruthy();`——注意该文件 mock 的 `t` 返回 key 本身（`:7`），所以按 key 名找按钮，不要写中文/英文文案。
- `desktop/frontend/src/components/shell/AppShell.test.tsx`、`desktop/frontend/src/pages/HomePage.test.tsx`：无需改（§2.4）。
- **本机无法运行**：以上全部用例本轮未被执行（无 pnpm/node_modules/bindings）。

---

## §7 i18n

通道：`desktop/frontend/src/i18n/i18n.tsx:60-63` 的 `t(key, values)`，数据源 `desktop/frontend/src/i18n/legacy.messages.json`（`zh` 段 `:2-345`，`en` 段 `:346-690`）。**无任何测试校验两语言键集合一致**（grep 未见 i18n 测试文件），所以新增 key 必须 zh/en 双写，遗漏只会静默回退到中文（`:60` `messages[locale][key] ?? messages.zh[key] ?? key`）。

新增 key 清单（建议全部进 `legacy.messages.json`，遵守 Lead 约束 ⑦）：

| key | zh | en |
| --- | --- | --- |
| `nav_virtual_adapters` | 虚拟网卡 | Virtual adapters |
| `virtual_adapters_title` | 虚拟网卡管理 | Virtual adapter management |
| `virtual_adapters_hint` | 在 Hyper-V 外部交换机上批量创建宿主机虚拟网卡：每张卡拥有唯一 MAC，路由器会按 MAC 分别分配 IP。 | Create host virtual adapters in bulk on the Hyper-V external switch. Each adapter gets a unique MAC, so your router assigns an IP per adapter. |
| `virtual_adapters_create` | 批量创建 | Create in bulk |
| `virtual_adapters_refresh` | 刷新 | Refresh |
| `virtual_adapters_count` | 数量 | Count |
| `virtual_adapters_count_hint` | 一次最多创建 {max} 张。 | Up to {max} adapters per batch. |
| `virtual_adapters_prefix` | 名称前缀 | Name prefix |
| `virtual_adapters_empty` | 还没有虚拟网卡。点击「批量创建」开始。 | No virtual adapters yet. Use “Create in bulk” to start. |
| `virtual_adapters_state_creating` | 正在创建… | Creating… |
| `virtual_adapters_state_dhcp` | 等待 DHCP 分配 IP | Waiting for DHCP |
| `virtual_adapters_state_ready` | 已就绪 | Ready |
| `virtual_adapters_state_failed` | 创建失败 | Failed |
| `virtual_adapters_state_removing` | 正在移除… | Removing… |
| `virtual_adapters_pool` | 已加入出口池 | In outbound pool |
| `virtual_adapters_dhcp_slow` | 超过 60 秒仍未拿到 IP，请检查路由器 DHCP 地址池是否已满。 | No IP after 60 seconds — check whether your router’s DHCP pool is exhausted. |
| `virtual_adapters_summary` | 已就绪 {ready} / 共 {total} | {ready} of {total} ready |
| `virtual_adapters_remove` | 移除 | Remove |
| `virtual_adapters_remove_title` | 移除此虚拟网卡？ | Remove this virtual adapter? |
| `virtual_adapters_remove_body` | 该卡将从系统中删除，路由器分配给它的 IP 配额会被回收。 | The adapter is removed from Windows and its IP lease is released. |
| `virtual_adapters_unavailable` | Hyper-V 不可用：{reason} | Hyper-V is unavailable: {reason} |
| `virtual_adapters_preview` | 浏览器预览 · 以下为示例数据，未连接桌面服务。 | Browser preview · Sample data only; the desktop service is not connected. |
| `virtual_adapters_retry` | 重试 | Retry |
| `virtual_adapters_loading` | 正在加载… | Loading… |

（`{max}`、`{ready}`、`{total}`、`{reason}` 均落在既有 `formatMessage` 占位符能力内，见 `i18n.tsx:31`。）

**关于 `text(zh, en)` 内联通道**：仓库现存页面大量使用内联双语（`BlockedDomainsPage.tsx:30-31` 等），但 Lead 明确要求「新文案必须走既有 i18n 通道，不要新增硬编码 CJK」。本设计因此**不建议**在新页面新增内联 `text(zh, en)`；面板 props 仍保留 `text` 形参只是为了与 `MTUDetectionPage`/`NATDetectionPage` 的既有 prop 形状一致、便于测试注入，**其实现由页面从 `t()` 派生**（例如页面内 `const text = (zh: string, en: string) => locale === "en" ? en : zh;` 会被替换为 `t(key)` 调用链；若为省事保留该 helper，必须保证其字面量同时也在 JSON 中有 key，否则等于新增硬编码 CJK）。

---

## §8 未验证项与风险

| # | 项 | 说明 |
| --- | --- | --- |
| 1 | **图标导出名** | `VirtualNetwork24Regular/Filled`、`Server24Regular` 是否存在**未验证**（本机无 `node_modules`，全仓库 grep 零命中）。写错会让 `pnpm build`（tsc）失败。 |
| 2 | **Go/引擎契约** | 能力探测字段名（`capability`/`reason`）、批量上限（`max_batch`）、`id` 语义（GUID 还是 Name）、`in_outbound_pool` 是否由后端回传，全部**待 `desktop-vnic-dev` 定夺**（本报告 §3.2/§5.1 给的是前端期望形状）。 |
| 3 | **binding 文件名** | 取决于最终 Go 服务类型名（§5.3）。无法本机生成验证。 |
| 4 | **未运行 tsc/vitest** | 环境缺 pnpm/node_modules/bindings；本报告所有前端改动均未编译验证。 |
| 5 | **「自动加入出口池」措辞** | 该行为在 Go 侧实现；前端只做提示，**不得**在 `in_outbound_pool` 为假时显示任何「已加入」文案（否则是对后端的虚假承诺）。另：仓库术语不统一（grep `出口池\|聚合池` 仅命中 `desktop/frontend/src/pages/HealthPage.tsx:103` 一处 HP 聚合池文案），中英文案需 Lead 统一。 |
| 6 | **术语撞名** | 旧 Wintun 方案的用户可见文案仍含 `Virtual NIC`（如 `desktop/frontend/src/pages/HomePage.test.tsx:326` 断言的引擎 issue 标题）。建议后续把新方案统一称「虚拟网卡（Hyper-V）」并在文档中标注旧称历史。 |
| 7 | **DHCP 时延** | 真实分配耗时未实测（`reports/vnic/30-real-machine-verdict.md` 是旧方案的实测结论）；60s 提示阈值是沿用旧面板常量的建议值，不是实测值。 |
| 8 | **删除旧文件的影响面** | `VirtualAdapterPanel.tsx` 被删后，`appServices.virtualAdapter` 分组在 Go 侧未改名之前仍会保留，属无害死代码；若 Go 侧先改名而前端旧面板还在，则 CI 立刻失败——**实施顺序建议：前端一次提交内同时完成「删旧面板 + 改 services.ts + 加新页面」**，不要分两次。 |

---

## §9 实施阶段建议（给 Lead 排产用）

单次提交的最小闭环（同一分支、同一 PR，避免中间态编译失败）：

1. `desktop/frontend/src/i18n/legacy.messages.json`：新增 §7 的 key（zh+en）。
2. `desktop/frontend/src/components/shell/CompactNavigation.tsx`：`:16` 联合类型 + `:30-37` 导航项。
3. `desktop/frontend/src/App.tsx`：`:28-34` lazy 导入、`:69-73` 深链白名单、`:85-95` `pageOrder`、`:143-170` `renderPage` 分支。
4. `desktop/frontend/src/platform/services.ts`：`:7`/`:24`/`:70-73`/`:350-354` 按 §5 落地（**依赖 Go 侧最终命名**）。
5. 新增 `pages/VirtualAdaptersPage.tsx`、`components/vnic/HyperVAdapterPanel.tsx`、`components/vnic/managementAdapters.ts`、`pages/virtual-adapters.css`。
6. `desktop/frontend/src/pages/HomePage.tsx`：删除 §2.1 三段；删除 `components/vnic/VirtualAdapterPanel.tsx` 与其测试文件。
7. 测试：新增 3 个测试文件，追加 `CompactNavigation.test.tsx` 断言；`HomePage.test.tsx` 可选加 1 条回归断言。
8. 交付前检查（有环境时）：`pnpm --dir desktop/frontend test` → `pnpm --dir desktop/frontend build`（tsc 会抓 `noUnusedLocals`、图标导出名、三元链分支类型）。
9. 验收对照（上游 `m01604`）：**主页不得再出现任何虚拟网卡控件**；独立页面可从导航进入；数量由用户在创建时手动填写。

**写范围建议**（避免多成员冲突）：本任务的前端写范围 = `desktop/frontend/src/{pages,components/vnic,components/shell,platform,i18n}` 的相关文件 + 本报告；`desktop/frontend/src/app.css` **不在写范围内**（§4 结论：无需改动）。

**升级顺序**：`desktop-vnic-dev` 先冻结 Go 契约（服务类型名、能力字段、上限、`id` 语义）→ 前端按 `reports/vnic/00-frozen-interface.md` 的方式登记为冻结接口 → 本报告 §3.2/§5 按冻结结果校正 → 再实施。
