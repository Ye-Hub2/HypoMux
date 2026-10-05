# 78 — 虚拟网卡模块 UX 复审（只读审计）

- 审计者：`vnic-ux-audit`（只读；本次唯一写入即本文件）
- 基线：`main` @ `fc821b1`，**工作区有未提交改动**，且审计期间 `VirtualAdaptersPage.tsx`（402→466 行）、`managementAdapters.ts`、`hyperv_adapter.go` 正被同事并发修改
- 证据口径：以下行号取自审计时工作区快照（`hyperv_adapter.go` 在 13:10 后整体偏移约 +5 行，已二次核对）。若同事后续继续改动，请以函数名 + 文案原文为准。
- 复审标准：**一个普通用户能不能不求助地把「读列表 → 建卡 → 等 DHCP → 进出口池 → 聚合 → 删卡」走完**，以及走错时能不能自己看出来。

---

## 1. 结论

**不能算顺畅。** 骨架是对的（轮询、并发闸门、DHCP 慢提示、`creating` 期间禁用删除都做了），但存在 **5 处会让用户「点了没反应 / 被告知了假消息 / 看不到根因」的硬伤**，其中最致命的是：**第 2 次及以后的创建/删除失败完全静默**（提示被一个永不解锁的闩吃掉），以及**页面在任何情况下都断言「已加入出口池」，而真正写出口池的那一步把错误直接丢掉了**。用户按提示重启聚合引擎后新网卡依然不生效，且全局没有任何根因——这正是「白干」的典型形态。

好消息：这些**几乎都是前端文案/状态的小修**，不涉及契约（§3/§4）、不涉及 Go 事务逻辑，改动面很小。唯一需要 Go 配合的是「出口池写失败要留痕」。

---

## 2. 必须修清单

### M1. 第二次及以后的创建 / 删除失败**完全没有提示**（去重闩被永久闩死）

**现象**
用户第 1 次创建失败 → 弹错误提示（正常）。第 2 次创建失败 → **什么都不弹**。进度横幅在 `catch` 里瞬间消失，页面回到原样，用户既看不到横幅也看不到通知，等于「点了没反应」。删除流程完全相同：第 1 次删除失败有提示，之后静默；而且**成功提示也只有第一次删除才有**。

**证据**
- `desktop/frontend/src/pages/VirtualAdaptersPage.tsx:184-191` — `notifyOnce` 依赖 `notified.current`（`:173` 声明的 `useRef(new Set<string>())`）做「每个 key 终身只提示一次」闩锁。
- 全文件对该 Set 只有两处读写：`grep notified.current` 命中 **仅 `:186` 与 `:187`**（读 + 写），**没有任何地方清除它**。注释 `:181-183` 写明设计意图是「Contract: one toast per mount」。
- 这个闩被套用在了 **4 个每次操作都应重新提示的 key** 上：
  - `:335` `virtual-adapters:info:create`（创建开始提示）
  - `:371` `virtual-adapters:error:create`（**创建失败**）
  - `:399` `virtual-adapters:info:remove`（**删除成功**）
  - `:407` `virtual-adapters:error:remove`（**删除失败**）
- 横幅瞬间消失：`:369` `setCreating(false)` 位于 `catch` 内、`mounted` 守卫之前，UI 立即复原。

**为什么这条闩对 read 类是对的、对 action 类是错的**：`:229`（switches 读失败）、`:247`（list 读失败）是**每 3 秒复发一次的持续故障**，闩锁正好防止刷屏；而 create/remove 是**每次点击都是独立事件**，第 2 次失败和第 1 次失败对用户是两条不同的新信息。用同一把闩处理两类语义，是静默的根因。

**为什么必须修**：契约 §3.7 明确「硬失败中断批次但保留已成功的卡」。也就是说**创建失败时宿主机上可能真的多出了几张卡**，而用户此时收不到任何通知 → 要么盲目重试（再弹一次 UAC、再等一轮），要么以为什么都没发生就走了。留着宿主机上的孤儿卡，正是契约注释里反复警告要避免的后果。

**建议修法**
`notified` 闩只保留给 `:229` / `:247` 两个读路径。create/remove 的四个 key 改用普通 `notify()`（**不需要额外去重**：`AppNotifications.tsx:116-120` 对同 id 的通知是「替换 + `occurrences++`」，不会堆叠，重试点击也只会刷新计数）。若坚持保留闩，则在每次 `create`/`remove` 开头 `notified.current.delete(key)`。

---

### M2. 页面无条件断言「已加入出口池」，而真正写出口池的错误被丢弃

**现象**
建卡成功后页面立刻弹出「已加入出口池，重启 HypoMux 聚合后生效」。用户照做、重启 HypoMux，结果新网卡**根本不参与聚合**，流量没走新卡。宿主机上网卡是好的、状态是「已就绪」、出口池徽章也在，但全程**没有任何一行文字说明真正的原因**。

**证据（这是一个跨层问题，两端都要看）**
- **Go 端丢弃错误**：`desktop/internal/services/hyperv_adapter.go:1470`
  ```go
  _ = s.applyPoolUpdate(appeared, nil)
  ```
  位于 `awaitBatch`（`:1458`）的 goroutine 内（`:1466-1473`），返回值被显式赋给 `_`。`applyPoolUpdate` 的实现见 `:1753`，内部会写 settings（`updateHomeStrategy`），这条路径**完全可能失败**。
- **时序**：出口池写入发生在后台 goroutine 里（等网卡 15s 上宿主后才写），而 `Create` 在 `:1421` 启动该 goroutine 后**立即返回**。
- **前端断言**：页面 `:353` `setRestartRequired(true)` 在 `create()` 一 resolve 就无条件置真，**不检查任何行是否真的 `inPool`**。
- **Go 侧文案通道**：`:1432-1440` 把 `hypervPoolRestartHint`（`:116` = "已加入出口池，重启 HypoMux 聚合后生效"）盖到所有非 failed 行上——同样是在池写入**之前**。

**为什么必须修**：这是本轮最典型的「静默丢失 + 误导用户」。用户为一条**不成立的承诺**付出了重启的成本，而且没有任何可排查的线索。注意这一条**不是**已接受清单里的「出口池键名不跟随改名」，是两回事。

**建议修法**
1. Go 端：`:1470` 不要丢错。把失败写回台账行的 `LastError`（这样 `buildHyperVStatus` `:792` 会带出来，复用前端已有的每行错误渲染 `HyperVAdapterPanel.tsx:311-313` 的 `role="alert"`），或至少落到服务级状态里。
2. 前端端：`restartRequired` 改为**由数据推导**而非乐观断言——只有当轮询回来的行里出现 `inPool === true` 时才置真；或在 M1 修完后改为由 `applyPoolUpdate` 的成功回执驱动。

---

### M3. 列表读失败、删除失败，弹窗标题都写「创建失败」

**现象**
用户点删除、删除失败 → 错误弹窗标题是 **「创建失败」**。列表读取失败（根本没在创建任何东西）→ 标题同样是 **「创建失败」**。用户在排查一个从未发生过的操作。

**证据**
- 文案：`desktop/frontend/src/i18n/legacy.messages.json:118` `"virtual_adapters_state_failed": "创建失败"`，英文 `:494` `"Failed"`。
- 这个 key 被当成**操作专属标题**用在三处语义完全不同的调用点：
  - `:247-251` **list 读失败**（读）
  - `:371-374` create 失败（创建）
  - `:407-411` **remove 失败**（删除）
- 对比：同文件 `:131` 有专门的 `virtual_adapters_switch_failed`，`:126` 有 `virtual_adapters_remove`——说明「按操作分 key」是本模块既有约定，唯独 `state_failed` 没遵守。

**为什么必须修**：它是失败通知里**最显眼的那一个词**，2/3 的调用点是错的。用户在半夜排查时会被直接引向错误的操作。修复成本极低（拆 3 个 key），收益直接。

**建议修法**：拆成 `virtual_adapters_create_failed` / `virtual_adapters_remove_failed` / `virtual_adapters_list_failed`（中英各一条），三个调用点各用各的。英文侧同理，别再复用泛化的 `"Failed"`。

---

### M4. 「有外部交换机、但都没开 AllowManagementOS」被折叠成一句无法行动的话

**现象**
这是 Hyper-V 里**最常见的真实配置错误**：用户早就建好了外部交换机，只是建的时候**没勾「允许管理操作系统」**。此时页面横幅显示：

> **仅外部交换机（可绑定宿主机网卡）**

用户看着一个根本不存在的「找不到交换机」问题，去 Hyper-V 管理器里再建一个外部交换机——**白做**。真实原因（没开 AllowManagementOS，宿主机拿不到这张卡的 IP）在这个分支里**从来没出现过**。

**证据**
- `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:169` 判定：
  `switchNotice = !loadFailed && !switchesFailed && (switches.length === 0 || noExternalSwitch || noUsableSwitch)`
- `:221-227` 三种根因塌缩成两个 key：
  `noExternalSwitch || noUsableSwitch ? t("virtual_adapters_switch_external_only") : t("virtual_adapters_no_switch")`
  即 **`noExternalSwitch` 和 `noUsableSwitch` 共用同一个 key**。
- 文案：`legacy.messages.json:111` `"virtual_adapters_switch_external_only": "仅外部交换机（可绑定宿主机网卡）"`——这是一个**要求描述片段**，不是问题说明，没有任何行动指引。
- **Go 层本来有精确文案，但前端把它丢了**：`hyperv_adapter.go:1342-1343`（当前偏移后）`交换机 %q 未开启「允许管理操作系统」，宿主机拿不到这张卡的 IP`。
- **前端其实已经有正确的句子，只是没用在正确的分支**：`:109` `virtual_adapters_switch_hint` = 「宿主机虚拟网卡必须挂在允许管理操作系统（AllowManagementOS）的外部交换机上。」——它只出现在创建对话框的 Field hint 里（`:338`），而不出现在用户真正卡住的那个横幅里。

**为什么必须修**：命中最高频的真实配置错误，且当前文案**主动把用户引向错误的修复动作**。三个分支各自都有现成或近乎现成的正确文案。

**建议修法**：`:221-227` 改为三分支——`switches.length === 0` → `no_switch`；`noExternalSwitch` → `switch_external_only`；`noUsableSwitch` → 改用 `switch_hint`（或新增一条 `virtual_adapters_switch_allow_mgmt_os`，点名 AllowManagementOS）。同理，创建对话框里被灰掉的 Option（`:348-357`，`isSwitchSelectable === false`）目前也**没有任何原因说明**，应补同样的行内提示。

---

### M5. 失败横幅的 Retry 按钮没有 `disabled`，且 `refresh` 没有 in-flight 闸门

**现象**
列表/交换机读失败时，横幅上的 Retry **点了没有任何视觉反馈**（不像工具栏的刷新按钮会置灰），用户会连点。而每次点击都会**再发起一轮 `list()` + `switches()`**，在恢复期恰好把机器压得更满。

**证据**
- `HyperVAdapterPanel.tsx:212-220`（switches 失败横幅的 Retry）与 `:228-235`（loadFailed 横幅的 Retry）**都没有 `disabled` 绑定**。
- 对比：工具栏刷新按钮**有**正确绑定 `:181` `disabled={loading || busy}`，且 Create 按钮 `:184-191` 走 `createDisabled`。两个 Retry 是仅有的漏网之鱼。
- `VirtualAdaptersPage.tsx:193-254` 的 `refresh` **没有 in-flight 闸门**：`serialPoll.ts` 只保证*轮询*之间不重叠，**不阻止手动 refresh 与轮询/manual 之间重叠**。每次 `refresh` 在 `:203-214` 并发发起 2 个 Wails 调用。
- 放大效应：Go 端 `readInventory`（`:936`）只有 5 秒 TTL 缓存（`:941`、`:47-49`），缓存未命中就拉起一个 PowerShell 进程；`List()`（`:1139`）和 `Switches()`（`:1184`）各自独立走这条路径，**并发冷读会各自拉起一个进程**。

**诚实的定性**：这**不是**硬死锁——`requestSequence`（页面 `:194`、`:215`）保证了「后发者胜」，结果不会被写坏，用户重试也确实能恢复。所以它排在最后一位。但它落在「用户最慌、最容易连点」的时刻，且两个 Retry 按钮**一个都没接 `loading`**，用户主观就是「点了没反应」——正是本轮要求排查的那一类。

**建议修法**
- `HyperVAdapterPanel.tsx:212-220` 与 `:228-235` 的 Retry 补 `disabled={loading || busy}`（与 `:181` 保持一致）。
- `VirtualAdaptersPage.tsx` 的 `refresh` 加一个 in-flight ref 闸门：已有请求在飞时直接返回本次调用，避免手动与轮询叠加。

---

## 2.5 与并行报告 79 的边界（避免重复劳动）

本报告与同批 `reports/vnic/79-i18n-tests-audit.md`（`i18n-audit-helper`）有交集，**以下内容以 79 为准，不要在本报告里重复修**：

- **en 用户全程看中文**（79 严重项 1）：服务层回传的运行时字符串绕过语言包，含 Go 的 `hypervPoolRestartHint`(`hyperv_adapter.go:116`)、`hypervDHCPTimeoutHint`(`:115`)。这与我的 M2 **是两回事**：M2 说的是「出口池写失败时错误被丢弃、页面仍在断言成功」（正确性问题），79 说的是「同一个成功/失败文案在 en 下仍是中文」（本地化问题）。修 M2 时顺手带 en 文案即可，但两条要分开记。
- **配额文案未进语言包**（79 低危项 2）：页面 `:296-307` 的三处 `text(zh,en)` 内联属有意选择（`:142-145` 有注释记述），**不属我第 4 节的问题**，我也未将其列为缺陷。

**反向声明（79 未覆盖、仍归本报告）**：79 的测试缺口结论是「**未覆盖**」，我的 M5 是「**代码本身缺 disabled**」——两者互补，不是同一件事：

| | 79 的结论 | 本报告的结论 |
|---|---|---|
| 提权建卡 180s 期间 UI 是否锁住 | 没有**任何测试**断言（`busy===true` 在面板测试里从未出现） | 读码确认逻辑**是对的**，共享 `busy` 确实串行化了创建/删除，无重复提交漏洞 |
| 失败横幅 Retry 按钮 | 未提及 | **代码缺陷**：无 `disabled`、无点击反馈（M5） |
| 工具栏刷新按钮 | **零覆盖**（无任何用例找到 `virtual_adapters_refresh`） | 读码确认 `:181` 的 `disabled={loading\|\|busy}` **是正确的**，可作为补测试的参照样板 |

> **合并建议**：M5 的两个 Retry 按钮应当照着工具栏刷新按钮（`:181`）的写法修——那既是现成的正确样板，也正好补上 79 指出的「刷新按钮零覆盖」所缺的断言。

---

## 3. 可选优化清单

| # | 问题 | 证据 | 为什么只是「可选」 |
|---|---|---|---|
| O1 | 表格缺 `role="rowgroup"`：`<div role="table">`（`HyperVAdapterPanel.tsx:270`）直接包 `<div role="row">`（`:271`/`:283`）和 `role="cell"`（`:289`…），中间没有 rowgroup 层。 | `HyperVAdapterPanel.tsx:270-288` | ARIA 结构不完整，部分读屏器在浏览模式下无法把列头关联到单元格，屏幕阅读用户听到的是一串无列名的单元格。能用但费力；加一层 `role="rowgroup"` 即可。 |
| O2 | 状态列的列头既**视觉为空**、`aria-label` 又写成 `t("virtual_adapters_state_unknown")`——读屏器把这一列念作「状态未知 / Unknown」。 | `HyperVAdapterPanel.tsx:275` | 单元格内容本身可读（Badge 有文字），不构成阻断。但显然应换成真正的「状态 / Status」列头（当前没有这个 key，需新增）。 |
| O3 | 重启提示横幅的关闭按钮 `aria-label={t("routing_dialog_cancel")}`，无障碍名就是「取消 / Cancel」。 | `HyperVAdapterPanel.tsx:252` | 视觉正常，只是读屏用户按「关闭」找不到它、按下后还以为是取消操作。建议加 `virtual_adapters_dismiss_restart_hint`。 |
| O4 | 正常 DHCP 等待期（契约 §3.5 约 45s）内**无法删除刚建的卡**，且整批没有「放弃」手势。 | `HyperVAdapterPanel.tsx:279` `const busyRow = busy \|\| isCreatingRow(adapter)` → `:320` `disabled={busyRow \|\| preview}` | 契约 §3.5 本来就要求**不自动删除** DHCP 超时的卡，且 Go 侧 `hypervEntryExpired`（`:759`）+ `deriveHyperVState`（`:772`）会在 60s 后把它收敛成 `failed`，按钮随即恢复。属于合理的保护，不是死锁。 |
| O5 | 前端创建超时 180s < Go 最坏路径（`readInventory` 60s + `hypervCreateScriptTimeout` 150s ≈ 210s），且 `withServiceTimeout` **只拒绝 awaiter、不取消底层调用**。 | `services.ts:257-273`、`:281-283`；`hyperv_adapter.go:1349`（readInventory）、`:1366`（opMu） | **诚实降级**：因为前端每 3s 轮询会顺带把 `readInventory` 的 5s 缓存焐热，点击创建时那次读通常是热的，实际远达不到 210s。仅在缓存因切页/轮询失败而冷掉时才可能触发。可选：超时文案补一句「宿主侧可能仍在创建」，或把预算提到 240s。 |
| O6 | 删除成功 toast 复用了**确认对话框的将来时文案**作正文。 | `VirtualAdaptersPage.tsx:399-403` → `legacy.messages.json:128`「该卡将**从宿主机移除**…」 | 语义上读作「刚刚做了什么」的说明尚可接受，只是时态别扭。真正的伤害来自 M1（只有第一次删除有这个 toast）。 |
| O7 | 过期注释引用，会误导后续审计。 | `VirtualAdaptersPage.tsx:165-166` 指向 `hyperv_adapter.go:1238-1240`（实际约 `:1432-1440`）；`hyperv_adapter.go:47` 注释称「前端按 1s 轮询 List()」，实际 `VIRTUAL_ADAPTER_POLL_INTERVAL_MS = 3000` | 纯维护性问题，不影响用户。 |
| O8 | 创建对话框里有一条游离的 `virtual_adapters_restart_hint` 文案（`:372`），未包在 `Field` 里。 | `HyperVAdapterPanel.tsx:372` | 文案本身（`:125`「创建完成后需要重启聚合引擎…」）作为提前告知是有益的，只是位置突兀。 |

---

## 4. 我实际验证了什么、没验证什么

### 实际验证（读代码 + 交叉核对，均为本次实际执行）

- 通读并交叉比对了：冻结契约 `reports/vnic/70-frozen-hyperv-interface.md` §3/§4、`VirtualAdaptersPage.tsx`、`HyperVAdapterPanel.tsx`、`managementAdapters.ts`、`platform/services.ts`、`platform/serialPoll.ts`、`components/shell/PageActivity.tsx`、`components/notifications/AppNotifications.tsx`、`internal/services/hyperv_adapter.go`、`i18n/legacy.messages.json`。
- **M1**：`grep notified\.current VirtualAdaptersPage.tsx` 实测只命中 `:186`/`:187` 两行，确认闩锁无清除路径；四个被套用的 key 逐一定位到行号；并读了 `AppNotifications.tsx:92-123` 确认 `dedupeKey` 本身只是「替换 + 计数」而非抑制，从而证明「改用普通 `notify()` 不会刷屏」这一修法成立。
- **M2**：`grep` 实测确认 `readInventory`(`:1349`) → `opMu.Lock`(`:1366`) → `hypervCheckBatchCapacity`(`:1373`) 的真实顺序；实读 `:1458-1474` 确认 `_ = s.applyPoolUpdate(...)` 丢弃错误；实读 `:1325-1437` 确认 `Create` 在启动 goroutine 后即返回、且 `:1432-1440` 覆盖写 `LastError`。
- **M3**：`grep` 定位 `legacy.messages.json:118`(zh)/`:494`(en) 的实际取值，并在页面里定位三处错误复用点。
- **M4**：`grep` 取到 `switch_hint`(`:109`) 与 `switch_external_only`(`:111`) 的原文，证明「正确文案已存在但用错分支」；并确认 Go `:1342-1343` 有对应的精确错误文案。
- **M5**：`grep` 确认两个 Retry 无 `disabled`、而工具栏刷新 `:181` 有；确认 `readInventory` 5s TTL 与 `List`/`Switches` 各自独立调用。
- **并发闸门核查**：确认 `busy` 单标志正确阻断了「创建中再删除」「删除中再创建」（`HyperVAdapterPanel.tsx:142/184/265/320/376/400`），**未**发现重复提交漏洞——这条我查了，结论是没问题，所以没写进报告。
- **并发编辑处置**：审计中 4 个被审计文件被同事并发修改（`VirtualAdaptersPage.tsx` 402→466 行、`.test.tsx`、`managementAdapters.ts`、`hyperv_adapter.go`）。我对 Go 文件**二次 grep 重新锚定**了行号；对页面则**整段重读**后引用。M1/M2/M3/M5 在改后依然成立。
- **配额改动避让**：确认同事正在页面 `:284-311`、`:318`、`:430-442` 加入 `createQuota` / `over-quota` 校验，**按指示未触碰、未重复上报**。
- **与 79 号报告做了边界对齐**：确认两份报告的行号基线一致（均以 `VirtualAdaptersPage.tsx` 467 行版为准），并已在 §2.5 显式切分「本地化问题（79）」与「正确性问题（本报告）」，避免 lead 重复派工。

### 明确「没有验证」的部分 —— 以下均为**静态推断**，请勿当作已实测

1. **我完全没有运行过应用**：没有启动 GUI、没有点过一次按钮、没有跑过一次真实建卡/删卡。所有 UI 结论均来自代码阅读与既有测试文件的存在性判断。
2. **我没有跑 `npm test` / `tsc` / 构建**。因此**不能声称当前代码树能编译或测试通过**——考虑到审计期间同事正在并发编辑，当时很可能处于中间态。M1–M5 都是我读出来的，**未经运行确认**。
3. **M2 的失败路径未在真机复现**：我验证的是「`applyPoolUpdate` 的错误被丢弃、因此**无法被观测**」这个代码事实；`applyPoolUpdate` 在真实 Hyper-V 主机上**究竟会不会失败、以多高概率失败，我没有取证**。这是「错误不可观测」缺陷，不是「已复现写入失败」。
4. **`applyPoolUpdate` 内部写 settings 的具体失败条件未逐条枚举**（我只确认了它有返回值且被丢弃）。
5. **提权 / UAC 分支未取证**：与已知情况一致，`runas` 分支在本机不可用，M2 中「提权脚本 → 台账 → UpdateHome」的端到端时序我**没有实测**，只有 Go 代码顺序。
6. **读屏器行为未实测**：O1/O2/O3 是按 ARIA 规范和代码结构推断的**风险**，我没有用 NVDA / Narrator 实测播报文本。
7. **Fluent UI 组件键盘行为未实测**：`Dropdown`(`:339-358`)、`Dialog`(`:333`/`:384`) 的具体键盘交互我依赖组件库默认行为，未验证。
8. **通知自动消失时机未实测**：`AppNotifications.tsx:112` 的 timeout（success 3200ms / info 4200ms / error 0）我读了常量，但**没有跑起来观察**用户实际能看到多久——M1 的「静默」结论不依赖这一点（那是 0 次提示，而非提示太短）。
9. **`Scan` 竞态未穷举**：`refresh` 的 `requestSequence`（`:194`/`:215`）我判断「后发者胜、不会写坏数据」是基于单次赋值的推理，**没有做高频点击的竞态压测**。
10. **契约符合性我只做了抽样核对**（§3.5 DHCP、§3.6 顺序、§3.7 上限、§3.8 List 不用 AdapterService、§4 usePageActive），**没有逐条走完全部条款**。
11. **`.test.tsx` / `.test.ts` 我只查了签名一致性**（`createQuota` 存在于 `managementAdapters.ts:155`、三参 `validateCreateCount` 存在于 `:185`/`:197`，与页面 `:318` 的调用一致），**没有读测试内容、没有验证覆盖是否跟上新配额逻辑**。

### 给下一位接手者的建议顺序

先修 **M1 + M3**（纯前端、十几行、零风险，直接消灭「静默失败」和「指错方向」）；再修 **M4**（纯文案分支拆分，同样零风险）；然后 **M5**（两个 `disabled` + 一个 ref 闸门）；**M2** 需要 Go 与前端各改一处，是唯一有设计取舍的，建议由 lead 拍板「错误留痕写哪」再动。
