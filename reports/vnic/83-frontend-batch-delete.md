# 83 — 虚拟网卡页：批量删除交互（前端）

- 作者：`vnic-batch-delete-ui`
- 分支：`main`（工作区已有前序未提交改动，本次**未**执行任何 `git add/commit/push`）
- 需求来源（用户原话）：「刷新按钮旁边加个删除按钮，用于选择删除那些虚拟网卡，不要管是不是这个软件创建的虚拟网卡，能检测到，能删除就加上移除按钮。删除按钮用于可选删除网卡。移除按钮，每个虚拟网卡都有移除按钮，用于快速移除某个选中的虚拟网卡。」

## 1. 交互设计

**普通模式**（工具栏 `刷新` 旁边）

| 控件 | 位置 | 行为 |
| --- | --- | --- |
| `删除` | `刷新` 右侧、`批量创建` 左侧 | 进入多选模式；当一行都不可删时禁用 |
| `移除` | 每行右侧 | 单张移除；不可删的行**不渲染**这个按钮 |
| 徽章 | 行内 | `非本工具创建`（台账外）保留；Hyper-V 自有对象另加 `系统保留` |

**单张移除的二次确认**（`HyperVAdapterPanel.tsx`）

- 受管卡：沿用原 `virtual_adapters_remove_body`（「该卡将从宿主机移除，路由器分配给它的 IP 配额会被回收。」）。
- 台账外卡：换用 `virtual_adapters_remove_foreign_body`，在 `role="alert"` 区块里明说「这张虚拟网卡不是本工具创建的，删除后无法撤销，也无法由本工具恢复」，并列出 `vEthernet (xuni-01)` / `00-15-5D-01-02-03` / `172.24.8.11/24` 三项身份字段——宿主机上有五张同厂网卡时，只有一句话的确认框无法让用户确认自己删的是哪一张。

**多选模式**

- 每行最左侧出现 Checkbox；不可删的行该格**留空**（不是 disabled 的空框，见下节理由）。
- 工具栏换成 `删除所选（N）` + `取消`。N = 0 时 `删除所选` disabled。
- 确认框列出**全部**已选卡名 + 宿主别名，台账外的卡在名字旁带 `非本工具创建` 标记；若选中项里有台账外卡，另有一段 `role="alert"` 的醒目警示。
- 一次 `删除所选（N）` = 后端一次提权（一次 UAC），不是 N 次单删。

**结果呈现**

| 后端返回 | 前端 |
| --- | --- |
| 全部 `removed:true` | `intent:"success"`，`已删除 N 张虚拟网卡。`，dedupeKey `virtual-adapters:info:remove-selected` |
| 部分失败 | `intent:"warning"`，`已删除 N 张虚拟网卡，其余未删除成功：` + 每张一行 `{name}：{reason}`，dedupeKey `virtual-adapters:error:remove-selected` |
| 顶层 throw | 既有错误通知路径，`intent:"error"`，标题 `virtual_adapters_remove_failed` |

- 部分失败**不是**整体失败：成功的行照样从本地状态剔除，失败的行原样留在屏上。
- `reason` 是后端运行时事实（哪条 cmdlet 被拒、哪个对象被占用），只作为语言包模板的 `{reason}` 占位符穿过，**不过 `t()` 本体**、不翻译、不概括成「失败」。
- 批量失败用独立 dedupeKey，与单张的 `virtual-adapters:error:remove` 分离，避免两次不同事件被折叠成一条 ×2 提示。

**本地状态更新**

删除成功后用函数式 setState 剔除行（`dropAdapters`），**不 `await refresh()`**。原因沿用本项目既有纪律：`List()` 无法复现 Go 写在每行 `lastError` 里的重启提示标记，一次刷新就会把这条通道抹掉。已用测试钉住（`removes a registered adapter by name and prunes the row without re-reading the host` 断言 `mocks.list` 调用次数不变）。

## 2. 保护名单镜像（前端 ↔ Go 必须同步改）

`managementAdapters.ts` 的「Removal policy」段，逐条镜像 Go 侧规则：

| 常量 | 规则 |
| --- | --- |
| `PROTECTED_ADAPTER_NAME_PREFIX = "container nic"` | 名字 `trim().toLowerCase()` 后 `startsWith` ⇒ 不可删（前缀匹配，非相等） |
| `PROTECTED_ADAPTER_INTERFACE = "vEthernet (Default Switch)"` | `interfaceName` `trim().toLowerCase()` **相等** ⇒ 不可删 |

对外只暴露 `isAdapterRemovable(adapter) = !isProtectedAdapter(adapter)` 与 `removableAdapters(rows)`。注释里写明这是后端保护名单的前端镜像、两边必须同时改；测试逐条钉住大小写、前缀与别名三种规则（`managementAdapters.test.ts` 的 `describe("isAdapterRemovable")`）。

**为什么受保护行的多选格是空的、而不是一个 disabled 复选框**：`disabled` 的复选框仍会被读屏器念出来，用户会以为那里有个能操作却操作不了的控件；留空 + 行内 `系统保留` 徽章（「Hyper-V 自己管理的对象」的 tooltip 兼 `aria-label`）语义更准，也避免用户反复点一个永远不生效的框。

## 3. 契约接线

- `services.ts` 新增导出 `HYPERV_ADAPTER_BATCH_TIMEOUT_MS = 180_000`，批量删除走 `withServiceTimeout(...)` 包住 `appServices.virtualAdapters.removeAdapters(names)`。
- 该常量注释写明：后端整批超时上限约 180s，前端**不得小于**它——前端先超时会造成「前端报失败、卡其实已经删了」的错觉。
- 结构性类型 `HyperVRemoveResult` 在 `services.ts` 里定义而非从生成模型再导出，CI 用 `wails3 generate bindings -clean=true -ts -i` 重生成时不会撞车。
- `bindings/**`（gitignore 的生成目录）里手写了 `RemoveAdapters` + `HyperVRemoveResult` 占位，仅为本地 `tsc` 通过，文件内已标注 LOCAL PLACEHOLDER。

## 4. i18n

- 新增 15 个键，zh / en 双写：`_remove_foreign_body`、`_remove_field_interface`、`_remove_field_address`、`_delete`、`_select`、`_remove_selected`、`_remove_selected_title`、`_remove_selected_body`、`_remove_selected_warning`、`_remove_selected_ok`、`_remove_selected_partial`、`_remove_failure_line`、`_remove_reason_unknown`、`_reserved`、`_reserved_hint`。
- node 校验：`virtual_adapters_*` 键 zh 58 / en 58，zh-only 0、en-only 0；全部 401 个键的占位符集合逐一比对，**零错位**；en 段**零 CJK**；JSON 可解析。

## 5. 测试与门禁证据

### 5.1 既有测试零放宽、零删除

| | 改动前（本次会话起点） | 改动后 |
| --- | --- | --- |
| 套件总览 | 45 files / **373** passed, exit 0 | 45 files / **401** passed, exit 0 |
| `managementAdapters.test.ts` | 52 | 63（+11） |
| `HyperVAdapterPanel.test.tsx` | 46 | 56（+10） |
| `VirtualAdaptersPage.test.tsx` | 39 | 46（+7） |

净增 28，与套件增量一致；没有任何既有断言被放宽或删除。被用户要求推翻的旧规则只有 3 处，全部**改写而非删除**，旧标题以注释形式留在原地以便审计：

| 位置 | 旧标题 | 现状 |
| --- | --- | --- |
| `HyperVAdapterPanel.test.tsx:130` | `marks unmanaged adapters and refuses to offer a delete button for them` | `marks adapters the ledger does not own AND still offers them a delete button`（仍断言 `virtual_adapters_foreign` 徽章） |
| `HyperVAdapterPanel.test.tsx:136` | `offers delete only for ledger-registered adapters` | 改名 `offers delete for ledger-registered adapters`，断言一字未动 |
| `VirtualAdaptersPage.test.tsx:692` | `never offers a delete control for an adapter the ledger does not own` | `offers a delete control for an adapter the ledger does not own` |

另外 `VirtualAdaptersPage.test.tsx` 的 `removes a registered adapter by name and refreshes` 改名去掉 "and refreshes"（`refresh()` 已按纪律移除），并加了两条断言：本地行被剔除、`mocks.list` 调用次数不变——原断言（`remove` 入参、notify dedupeKey）保持不变。

### 5.2 新增覆盖

- `managementAdapters.test.ts`：`isAdapterRemovable` 的容器前缀（4 种大小写/空白）、Default Switch 别名（大小写 + 前后空白）、相似但不受保护的名称/别名、混合列表过滤；`dropAdapters` 只剔指定名且保序、对未知名 no-op；`partitionRemoveResults` 全成功 / 部分失败 / `removed` 与 `reason` 自相矛盾 / 空与 null。
- `HyperVAdapterPanel.test.tsx`：受保护行无按钮且有 `系统保留` 徽章；台账外行仍有按钮；台账外确认框用更强文案并给出三项身份、受管卡沿用旧文案；多选进出、计数与 N=0 禁用、只给可删行复选框、批量确认框列出全部卡名并警示台账外卡、取消不调用删除、已变为受保护的勾选被丢弃。
- `VirtualAdaptersPage.test.tsx`：普通 ↔ 多选往返、勾选计数与 0 选禁用（含 un-tick 回落）、确认框列出全部名字且只发一次 `removeAdapters([...])`、全成功提示与两行剔除 + 模式自动关闭、部分失败（2 成功 / 1 失败）以 `warning` 呈现且失败卡名与后端中文 reason 都在提示里、整批 throw 走既有错误路径且不误删任何行。

### 5.3 回归探针（注入缺陷 → 红 → 回滚 → 绿）

用 `edit` 把 `PROTECTED_ADAPTER_NAME_PREFIX` 从 `"container nic"` 改成 `"container nic-"`（模拟有人「简化」保护名单、后端开始接受 Container NIC 的漂移），再回滚：

| 阶段 | 三个文件合计 | 退出码 |
| --- | --- | --- |
| 注入前（基线） | 165 passed / 165 | 0 |
| 注入缺陷 | **8 failed / 157 passed (165)** | 1 |
| 回滚后 | 165 passed / 165 | 0 |

失败的 8 条跨三层，说明保护名单在纯函数、面板、页面三处都是承重的：
`isAdapterRemovable > protects any container NIC by name prefix` / `filters a mixed list…`；`removal policy > gives a protected row no remove button…` / `keeps the delete button…`；`multi-select > offers a checkbox only for removable rows` / `ignores a tick that is no longer removable…`；`remove flow > still refuses a delete control for a Hyper-V owned adapter`；`batch removal > round-trips between normal mode and batch mode`。

回滚用的是定点 `edit`（未用 `Copy-Item`/`Set-Content`），回滚后复查 `managementAdapters.ts` 已无 `"container nic-"` 残留。

### 5.4 门禁真实退出码

回滚后在 `desktop\frontend` 依次执行：

```
.\node_modules\.bin\tsc.cmd --noEmit    → tsc exit=0
.\node_modules\.bin\vitest.cmd run      → 45 files / 401 tests passed, 23.93s, vitest exit=0
.\node_modules\.bin\vite.cmd build      → ✓ built in 582ms, vite exit=0
```

（`vite` 在 stderr 打了一行 `wails-typed-events` 的 `[PLUGIN_TIMINGS]` 提示，是既有插件的耗时提醒，不是错误，退出码为 0。）

## 6. 给 lead 的几点说明 / 偏离与风险

1. **退出一键多选的方式**：需求写「再次点删除回到普通模式」，同时又要求「工具栏变成『删除所选（N）』、『取消』」。工具栏替换后「删除」已不存在，因此实现取**单一出口「取消」**（`onExitSelection` 清模式与勾选），没有在多选态再放第二个「删除」造成两个近义按钮。进出往返有页面级测试钉住。
2. **批量抛错后也清勾选**：与成功/部分失败一致。整批 throw 之后后端对「哪几张还在」没有可信答案，留着勾选等于鼓励盲重发。
3. **提示被 72 字折叠**：`notificationMessage.ts` 会把可见摘要折叠到 72 字、其余进「详情」展开（既有全局行为，本次未改）。批量失败行较多时，一部分失败原因默认在折叠区里，需要展开才能看到全部。若认为不够，可以后续给批量通知单独走不过折叠的通道。
4. **Go 侧契约尚未落地**：本次前端按约定契约写就，仓库里 Go 代码目前还没有 `RemoveAdapters`。等后端成员合入后需要真实联调一次（尤其 `reason` 文案与「部分失败」的实际排列）。
5. **`bindings/` 是手写占位**：CI 会用 `wails3 generate bindings -clean=true -ts -i` 重新生成，届时请以生成结果为准（占位里 `$Call.ByID(0, names)` 的 id 是编造的）。

## 7. 我没有验证的部分（务必不要当成已验证）

- **没跑真机**：没有在真实 Hyper-V 宿主机上执行过任何一次删除，没有真实 `Remove-VMNetworkAdapter` 调用、没有提权。
- **没真点按钮**：全部交互验证来自 jsdom + Testing Library，没有真人鼠标/键盘操作；Fluent v9 的实际观感（徽章宽度、5 张卡时确认框高度、Toast 换行）没有看过。
- **没用读屏器**：aria 关系（Checkbox 的 `aria-label` 含卡名、`role="alert"` 警示块、徽章的 `aria-label`）是按惯例写的，没有 NVDA/JAWS/VoiceOver 实测。受保护行「留空一格而不是 disabled 复选框」是**推断**更可访问，未经验证。
- **UAC 行为未验证**：一次提权删 N 张是否真的只弹一次框、用户在 UAC 上点「否」时后端返回什么，属于未测。
- **部分失败的真实表现未验证**：`reason` 的真实中文文案、同一批里多张同时失败的排列、以及 `removed:true` 与非空 `reason` 并存这种矛盾返回是否真会出现，都只是按契约推演。
- **`vite build` 通过 ≠ 真机可用**：Wails 绑定是占位、真机 Hyper-V 权限路径未跑，桌面端实际启动未验证。