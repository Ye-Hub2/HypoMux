# 79 · i18n 与测试覆盖审计（只读）

- 审计者：`i18n-audit-helper`，受 `vnic-ux-audit` 委派
- 范围：`VirtualAdaptersPage.tsx`、`HyperVAdapterPanel.tsx`、`managementAdapters.ts` 及其测试、`i18n/` 语言包、`CompactNavigation.tsx` / `AppShell.tsx` 导航接线
- 约束：只读。未修改任何源文件，未执行 git 写操作。
- ⚠️ **审计期间文件正在被改动**：`VirtualAdaptersPage.tsx` 首次读取时约 380 行，审计结束时已 **467 行**（新增 create 配额功能）。本报告所有行号以 **467 行版本** 为准；若该文件再次变动，行号需重新核对。

---

## A. i18n findings

### A1 · 后端注入的中文字符串被原样渲染（严重，en 用户全程可见中文）

`i18n.tsx:60-63` 的 `t()` 只翻译 key 本身；**服务层回传的运行时字符串一律绕过语言包**。以下四条路径把 Go 侧的中文直接送进 DOM：

| 位置（前端） | 位置（后端，佐证） | 用户可见症状 |
|---|---|---|
| `VirtualAdaptersPage.tsx:225` `const detail = errorDetail(switchResult.reason)` → `:228 setSwitchesError(detail)` → `:450 switchesError={switchesError}` → `HyperVAdapterPanel.tsx:216` `{switchesError}` 原样渲染 | `hyperv_adapter.go:1000` `"读取 Hyper-V 状态失败：%s"`；`:1059` `"脚本返回失败：%s"` | locale=en 时，交换机读取失败横幅下半行是中文 |
| `VirtualAdaptersPage.tsx:246` `setUnavailableReason(errorText(listResult.reason))` → `:458` → `HyperVAdapterPanel.tsx:202` 插入 `t("virtual_adapters_unavailable", { reason })` 的 `{reason}` | `hyperv_adapter.go:1000`、`:1096` 一系 | locale=en 时，不可用横幅尾部的真实原因是中文 |
| `HyperVAdapterPanel.tsx:281` `const lastError = formatLastError(adapter)` → `:312` 原样渲染；`managementAdapters.ts:117-118` `formatLastError` 直接返回 `lastError` | `hyperv_adapter.go:116` `hypervPoolRestartHint = "已加入出口池，重启 HypoMux 聚合后生效"` | 建卡成功后该行 `lastError` 非空时，失败行显示中文 |
| 同上 | `hyperv_adapter.go:115` `hypervDHCPTimeoutHint = "等待 60 秒仍未从交换机拿到可用 IPv4（DHCP 未就绪）；网卡已保留，可稍后重试或直接删除"` | locale=en 时，DHCP 超时行显示中文 |

注：`VirtualAdaptersPage.tsx:114-122` 的注释明确记录了这是**有意为之**（"surface that cause verbatim"），且注释里已经引用了 Go 的中文格式串 `无法读取 Hyper-V 虚拟交换机：%v`。即"显示真实原因"这个决定是对的，但**没有考虑 en locale**。

### A2 · 三处硬编码 CJK 绕过语言包（低；但属明确的契约违反）

| 位置 | 问题 | 用户可见症状 |
|---|---|---|
| `VirtualAdaptersPage.tsx:296-298` `quotaLineText` | 文案以本地 `text(zh, en)` 内联拼接，未进 `legacy.messages.json` | **zh/en 无可见缺陷**；破坏任何第三语言，也不在语言包审校流程覆盖范围内 |
| `VirtualAdaptersPage.tsx:300-302` `quotaExceededText` | 同上 | 同上 |
| `VirtualAdaptersPage.tsx:304-307` `quotaExhaustedText` | 同上 | 同上 |

`text` 的定义在 `VirtualAdaptersPage.tsx:146`，`legacy.messages.json` 中**没有**对应 key。`VirtualAdaptersPage.tsx:142-145` 的注释把它记为有意选择并给出先例（`ConnectionsPage.tsx:172`、`HealthPage.tsx:98`），理由是"插值实时数字，静态 key 需要额外的占位符契约"。这是**有文档的偏离**，不是疏忽，但确实不满足本次审计契约里"i18n 必须走 t()"的要求。

### A3 · 无缺失 key ✅

代码引用的 32 个 key 全部同时存在于 zh 与 en 语言包，逐一比对无 MISSING：
`virtual_adapters_loading` / `_switch` / `_switch_failed` / `_state_failed` / `_state_creating` / `_state_dhcp` / `_state_ready` / `_state_unknown` / `_create` / `_create_banner` / `_summary` / `_total_hint` / `_refresh` / `_preview` / `_unavailable` / `_retry` / `_switch_external_only` / `_no_switch` / `_switch_hint` / `_empty` / `_pool` / `_foreign` / `_dhcp_slow` / `_hint` / `_title` / `_count` / `_count_hint` / `_remove` / `_remove_title` / `_remove_body` / `_restart_hint` / `routing_dialog_cancel`（末位为复用的既有 key）。

### A4 · 无同值 / 半翻译 key ✅

- 32 个 key 中 zh 与 en 取值**无一相同**。
- `en` 段**零 CJK 残留**（全语言包扫描）。
- `zh` 段纯 ASCII 且含字母的取值仅 9 条，全部是技术性标签，非本模块且非半翻译：`settings_language_en`(English)、`settings_http_label`(HTTP:)、`settings_socks_label`(SOCKS5:)、`diag_card_line`(`{name} · {ip}`)、`home_engine_ports`、`home_card_speed`、`home_metric_latency_value`、`routing_match_ip`、`settings_doh_google`(Google DNS)、`about_open_github`(GitHub)。

### A5 · 占位符全部对齐 ✅

仅 5 个 key 带占位符，调用点全部正确供给，且模板无多余占位符（zh/en 各验一次）：

| key | 模板占位符 | 调用点供给 | 结论 |
|---|---|---|---|
| `virtual_adapters_summary` | `{ready} {total}` | `HyperVAdapterPanel.tsx:176` | OK |
| `virtual_adapters_total_hint` | `{used} {total}` | `HyperVAdapterPanel.tsx:178` | OK |
| `virtual_adapters_create_banner` | `{count}` | `VirtualAdaptersPage.tsx:337`、`HyperVAdapterPanel.tsx:239` | OK |
| `virtual_adapters_unavailable` | `{reason}` | `HyperVAdapterPanel.tsx:202` | OK |
| `virtual_adapters_count_hint` | `{max}` | `HyperVAdapterPanel.tsx:360` | OK |

无"locale 有占位符但代码不供给"的反向情况。

### A6 · 占位符缺失时的失败形态（机制记录，当前未触发）

`i18n.tsx:29-34`：
- `:30` `if (!values) return template` —— 完全不传 `values` 时，模板里的 `{count}` **原样显示在屏幕上**；
- `:31-33` 正则 `\{(\w+)(?::[^}]+)?\}`，未命中供给值时回调返回 `placeholder` 本身 —— 即再次**原样显示 `{name}`**（单花括号，不是 `{{count}}`）。

当前 A5 已证明无触发路径；但这是"一旦 key 与调用点不同步就会直接把占位符漏到 UI 上"的机制，值得留档。

### A7 · 导航接线 ✅

- `CompactNavigation.tsx:18` `AppPage` 联合含 `"virtual-adapters"`；
- `CompactNavigation.tsx:34` 导航项 `{ id: "virtual-adapters", label: t("nav_virtual_adapters"), icon: <VirtualNetwork24Regular />, activeIcon: <VirtualNetwork24Filled /> }`，走 `t()` 而非内联；
- `nav_virtual_adapters` 在两语言包均存在：zh `虚拟网卡` / en `Virtual NICs`；
- `AppShell.tsx:28-30,51` 对 `AppPage` 完全泛化（`visited` + `hidden`），新页面无需额外接线即获得"按需挂载 + 活动门控"。

---

## B. 未被覆盖的用户可见行为

以下均为**逐一读过后确认不存在**的用例。

### B1 · `busy === true` 全局从未被触发（最严重缺口）

`HyperVAdapterPanel.test.tsx:63` 里 `busy` 只作为默认夹具的 `false` 出现；**整个测试文件没有任何一处 `busy: true`**（面板测试全量 grep 仅命中该行）。

因此以下全部**未被验证**：

| 未覆盖控件 | 源码位置 |
|---|---|
| 刷新按钮 `disabled={loading \|\| busy}` | `HyperVAdapterPanel.tsx:181` |
| 创建提交 `disabled={busy \|\| preview \|\| selectable.length === 0}` | `:343` |
| 创建对话框取消 `disabled={busy \|\| preview}` | `:366` |
| 创建对话框确认 `disabled={createDisabled}`（间接依赖 busy） | `:376` |
| 删除确认 `disabled={busy \|\| preview}` | `:400` |

用户可见影响：一次提权建卡最长可达 **180 秒**，这段时间内全部写操作都应锁定；目前没有任何测试断言"UI 在这个窗口里真的锁住了"。一旦 `busy` 未被置位或被提前清掉，用户可以在建卡途中再提交一次。

（对照：`loading === true` 是**有覆盖**的 —— `HyperVAdapterPanel.test.tsx:193` `rerender(<HyperVAdapterPanel {...props} loading />)`。所以 B1 精确地只针对 `busy`，不代表加载态也没测。）

### B2 · 刷新按钮本身零覆盖

全量 grep `HyperVAdapterPanel.test.tsx` 中 `refresh` 只命中 `onRefresh` 夹具（`:71`）与两处 **Retry** 按钮的点击（`:166`、`:247`）。

没有任何用例：
- 通过可访问名 `virtual_adapters_refresh` 找到刷新按钮；
- 点击它并断言 `onRefresh` 被调用；
- 断言它在 `loading` / `busy` 下的 `disabled` 状态。

### B3 · 建卡与删除并发未覆盖

- 删除流程测试：`VirtualAdaptersPage.test.tsx:573`（正常删除）、`:587`（非托管卡不给删除按钮）—— 均为**孤立**场景。
- 建卡流程测试：`:261`–`:405` 全部为**孤立**场景。

没有任何用例驱动"删除尚未 settle 时用户又点批量创建"（或反向）。当前实现靠共享的 `busy` 状态串行化，但该串行化路径**无测试**——与 B1 是同一根因。

### 已确认**有**覆盖的行为（避免重复审计）

| 行为 | 覆盖位置 |
|---|---|
| 无可用交换机时禁用创建 | `HyperVAdapterPanel.test.tsx:170` |
| 数量非法禁用提交（0 / 17 拒绝，16 通过） | `HyperVAdapterPanel.test.tsx:283`、`:304`；`managementAdapters.test.ts:147`/`:153`/`:158` |
| 预览模式禁用全部写操作 | `HyperVAdapterPanel.test.tsx:197`、`:251` |
| 删除确认框打开 / 确认 / 取消 | `HyperVAdapterPanel.test.tsx:315`、`:326`；`VirtualAdaptersPage.test.tsx:573` |
| 非托管（foreign）卡**不出现**删除按钮 | `HyperVAdapterPanel.test.tsx:128`、`:134`；`VirtualAdaptersPage.test.tsx:587` |
| 建卡后出现"重启聚合"提示且可关闭 | `HyperVAdapterPanel.test.tsx:210`、`:215`、`:230`；`VirtualAdaptersPage.test.tsx:338` |
| DHCP 慢提示 | `HyperVAdapterPanel.test.tsx:140`；`VirtualAdaptersPage.test.tsx:597` |
| loadFailed 横幅 + 重试 | `HyperVAdapterPanel.test.tsx:163`；`VirtualAdaptersPage.test.tsx:159` |
| switchesFailed 横幅 + 重试 + 真实原因并排 | `HyperVAdapterPanel.test.tsx:240`、`:256`、`:265`；`VirtualAdaptersPage.test.tsx:199`、`:224` |
| 轮询按 pageActive 门控 | `VirtualAdaptersPage.test.tsx:121`、`:129`、`:147` |
| 数量边界 1 / 16 / 17 / 0 / 空 / 非数字 | `HyperVAdapterPanel.test.tsx:283`；`managementAdapters.test.ts:147`、`:153`、`:158` |
| 空态与加载态分别渲染 | `HyperVAdapterPanel.test.tsx:190` |
| 批次合并而非整表替换 | `managementAdapters.test.ts:328-372`；`VirtualAdaptersPage.test.tsx:313` |

---

## Not verified（明确声明未核实的部分）

1. **未运行任何测试或构建**。本次为纯静态只读审计，`vitest` / `tsc` / `vite build` 均未执行；"有覆盖 / 无覆盖"的结论来自逐行阅读测试源码与用例名，不是来自覆盖率工具。某行为可能在某个未被我识别出名字的用例中间接被断言过。
2. **未做运行时验证 A1**。中文泄漏的判定基于后端字符串常量与前端渲染路径的静态连线，未在 en locale 下实际启动应用观察。
3. **未审计其余页面**。`SettingsPage.tsx`、`HomePage.tsx` 等处的 `text(zh, en)` 内联模式未纳入本次范围，故"全仓内联双语"的完整清单未知。
4. **`VirtualAdaptersPage.tsx` 在审计期间处于变动中**（380 → 467 行）。新增的配额功能部分我没有做行为审计，仅核对了它的 i18n 形式（A2）与既有 key 是否仍完整（A3）。
5. **`HyperVSwitch` 的 `uplink` / `netAdapterName` 展示路径**未单独核对其本地化形态，仅确认其渲染走 `t()` 或既有描述函数。