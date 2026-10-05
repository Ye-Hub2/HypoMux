# 修复记录：P0-1 / P0-2 / P1-1 + reports 脱敏

- 日期：2026-10-04（本轮会话）
- 范围：`00-final-verdict.md` 第六节的 3 条 🔴/🟠 功能缺陷 + 第七节第 1/3 条提交卫生项
- 基线：`fc821b16bd55630ac6cba4311179e1a1b6644ded`（工作区未提交改动之上的增量）
- 执行方式：Go 与超时预算由 lead 亲自改；身份键与回归测试分别交给两名成员（写入范围互斥、零重叠），全部改动由 lead 独立复跑门禁复核。

---

## 1. P0-1 单卡删除超时错配（前端先于后端到期）

### 缺陷

`Remove(name)` 单张路径的前端超时预算是 `HYPERV_ADAPTER_WRITE_TIMEOUT_MS` = **60s**，而后端真实预算是 **90s**：

- `desktop/internal/services/hyperv_adapter.go:42` `hypervRemoveScriptTimeout = 90 * time.Second`
- `desktop/internal/services/hyperv_adapter.go:2037-2046` `hypervRemoveBatchTimeout(items)` = `min(90s × items, 180s)`，单张即 90s
- `desktop/internal/services/hyperv_adapter.go:2203` `hypervRemoveBatchTimeout(len(items))`

关键事实：`runScript(envelope, timeout, elevated)`（`hyperv_adapter.go:1199-1206`）对提权与非提权**使用同一个 timeout**，仓库中不存在独立的「提权删除超时」常量。

而 `withServiceTimeout`（`desktop/frontend/src/platform/services.ts:268`）只是 reject 等待者，**不会中止底层脚本**。后果链条：第 60s UI 弹出失败 → 用户以为失败 → 重新发起删除 → 第二次 UAC 弹窗 → 而第一次的脚本其实在第 90s 正常执行完毕，形成「失败却删掉了」的幽灵状态。

### 修改

`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:516` —— 单删回调的预算由 60s 改为 `HYPERV_ADAPTER_BATCH_TIMEOUT_MS`（180s），并补注释说明「单张就是一张的批量，预算按张数给」。批量路径 `:639` 本来就是 180s，未动。

副作用：`HYPERV_ADAPTER_WRITE_TIMEOUT_MS` 变成悬空 import，触发 TS6133；已删除该 import（该常量仍导出于 `services.ts:275`，全仓无其他使用点）。若将来单张删除改走更快的非提权路径，此处需重新核算。

---

## 2. P0-2 同名网卡串行误删（身份键）

### 缺陷

勾选态与删除剪枝都以 `adapter.name` 当身份，但 **Hyper-V 允许两张同名网卡**：

- `managementAdapters.ts` 的 `dropAdapters` 按 `row.name` 过滤 removed 名单 → 删一张抹掉两行
- `VirtualAdaptersPage.tsx` 的 `tickedNames` 用名字存选择态 → 勾一张亮两张
- `HyperVAdapterPanel.tsx` 的 Checkbox `checked` 同样按名字

仓库里**早就有**稳定行键 `adapterRowKey()`（`adapterId || interfaceName || name`），但只被用作 React `key`，从未进入状态语义。

### 为什么不改后端契约

后端 `RemoveAdapters(names []string)` 的入参是名字，返回 `HyperVRemoveResult{name, removed, reason, interface}`，本身没有 adapterId/MAC 字段。若要根治（返回身份字段以区分同名卡）必须扩展冻结契约 §3.2 —— 属于用户拍板事项，不在本次三个缺陷的授权范围内。因此本次采用**前端行键解耦**：后端行为一字未改，前端不再把「名字相同」当作「同一张卡」。

### 修改

- `managementAdapters.ts:169` `adapterRowKey` 位置调整；`:191` 新增导出 `adapterNamesForKeys(adapters, keys)` —— 按表格顺序解析，**key 级 + name 级两级去重**，解析不出的 key 直接丢弃。
- `managementAdapters.ts:241` `dropAdapters(previous, removedNames, submittedKeys?)` 增加可选第三参。**省略或传 null 时行为逐字不变**（原单测零改动即通过，保证向后兼容）；传入时只剪「行键 ∈ submittedKeys」且「名字 ∈ removedNames」的行，空数组则一行不剪。
- `VirtualAdaptersPage.tsx` `tickedNames` → `tickedKeys`；新增 `tickedRows` memo（`:196`）每轮 poll 重算 —— **tick 活不过它命名的行**，行被 poll 换掉后勾选自动失效；`ticked`（`:208`）由 `adapterNamesForKeys` 解析；`toggleSelection` / `selectAllRemovable` / `exitSelection` 行键化；`removeSelected` 在 **await 之前**快照 `submittedKeys`（`:634`），确保并发 poll 不会让剪枝条件漂移。
- `HyperVAdapterPanel.tsx` prop `selectedNames` → `selectedKeys`（`:114`），`selectedRows` 过滤与 Checkbox `checked` 改用 `adapterRowKey`。

### lead 追加的两处同源修补（成员发现、lead 动手）

| 位置 | 问题 | 处理 |
|---|---|---|
| `HyperVAdapterPanel.tsx:743` | 批量确认弹窗 `<li key={row.name}>` —— 同名两行撞 React key，会触发 warning 且重渲染时可能错位 | 改为 `adapterRowKey(row) \|\| row.name` |
| `HyperVAdapterPanel.tsx:539` | Checkbox `aria-label` 只有名字 —— 同名两行读屏播报完全相同的两条选项，用户无法分辨在勾哪张 | 追加 `interfaceName`（同行已印该别名，只作消歧，不引入新词汇） |

### 复核后决定不改的两处

- `HyperVAdapterPanel.tsx:614` `<Option key={item.name}>` 是**交换机**下拉，交换机名唯一，与网卡身份无关。
- `mergeAdapterBatch`（`managementAdapters.ts:74-89`）按 name 合并 —— 它只消费 `Create()` 返回的批次，而那批名字由后端保证唯一（存在 `hypervCodeNameConflict` 冲突码）。改动风险大于收益，保持原样。

---

## 3. P1-1 `Remove()` 漏读 `result.Failures`（后端，潜伏缺陷）

### 缺陷

`removeAdapters(names, requireLedger=true)` 的台账分支**只判 `runErr != nil`，从不读取 `result.Failures`**。而提权脚本把每张卡的异常 catch 进 `$failures` 后仍会 `Publish $true 'ok'`：

- 脚本把失败收进 `$failures`，`runErr` 为 nil
- `requireLedger` 分支不检查 failures → 所有 scripted 行被 `hypervReconcileRemoveRows` 标成 `removed=true`
- 清台账 + 清出口池照常执行

后果：**网卡还在宿主上，但归属记录已被抹掉**。此后 `isManagedAdapter()` 恒为 false，卡变成永远删不掉的「外来卡」。原注释 `:2205-2206` 声称「脚本没报错就一定删掉了」，与实现相反。

严重度说明：UI 的单删走的是批量 API，所以该路径目前**潜伏未爆**（`Remove` 仅作为 Wails 绑定导出，前端零调用方，见 `services.ts:374`）。但它是导出的公开方法，契约 §3.4 下任何人调用都会踩中，因此按「必改」处理。

### 修改

`desktop/internal/services/hyperv_adapter.go:2209` 与 `:2223`：

- `hypervReconcileRemoveRows(rows, result, runErr)` 改为**无条件先执行**（原先在 `else` 分支里）。
- `requireLedger` 分支内：`runErr != nil` 行为不变；否则遍历 scripted 行，任一行 `!row.removed` 即 `return nil, hypervErrorf(hypervCodeRemoveFailed, "%s 删除失败：%s", row.name, row.reason)`。**因为提前 return，清台账与 `applyPoolUpdate` 的代码根本不执行** —— 台账宁可多不可少。

错误码选用已存在的冻结常量 `hypervCodeRemoveFailed`（`hyperv_adapter.go:91`），而非 `not_managed`：卡还在宿主上、归属也没问题，说成「不是我们建的」会把用户引向错误方向。

批量路径原本就是正确的（`:2223` 之后由 `hypervReconcileRemoveRows` 处理部分失败），未改动其语义。

### 新增回归测试（5 个顶层用例，净 +9 PASS）

`desktop/internal/services/hyperv_adapter_test.go`：

| 行号 | 用例 | 钉住的行为 |
|---|---|---|
| 2811 | `TestRemoveReportsScriptFailureAsRemoveFailed` | 脚本失败必须返回 `remove_failed`，不能静默成功 |
| **2852** | `TestRemoveKeepsLedgerAndPoolOnScriptFailure` | **最重要**：逐字节比对 `adapters.json` 不变 + 出口池条目与权重不变 |
| 2910 | `TestRemoveClearsLedgerAndPoolOnScriptSuccess` | 成功时台账与池**必须**被清理（防止把上一条改成永不清） |
| 2950 | `TestRemoveAdaptersPartialFailureKeepsBatchContract` | 批量部分失败的既有契约不变 |
| 3004 | `TestRemoveTimeoutBudgetScalesWithItems` | 表驱动断言 1/1/2/3 张 → 90s/90s/180s/180s |

**首次真正读取了 `fixture.timeouts`** —— 此前该字段只写不读，意味着把 `hyperv_adapter.go:2203` 写死成 90s 全部测试依然全绿（详见 `08-test-audit.md`）。

新增 helper：`removeScriptFailureReason`(:2763)、`newManagedRemoveFixture`(:2769)、`(f *removeFixture) poolState()`(:2107)、`(f *removeFixture) removeBudget(label)`(:2125)。

**反向验证**：仅注释掉 `return nil, hypervErrorf(hypervCodeRemoveFailed, ...)` → 前两个用例立即变红；成员另复刻修复前的原缺陷逻辑，实跑出现真实现场「删除后 `adapters:[]`」，证明场景 2 并非恒真断言。生产文件已按 SHA256 校验还原。

---

## 4. reports/ 脱敏

成员执行、lead 独立复核（未采信自报结论）。写入范围严格限定 `reports/**` 48 个文件。

**验证陷阱（已记录）**：`reports/` 是未跟踪目录，`git grep -- reports` **恒返回 0 条**（git 看不见未跟踪文件），用它验收是假阴性；`git status` 也只会折叠成一行 `?? reports/`，无法证明写入范围。本轮复核因此改用直接遍历文件系统 + 写入范围对照。

| 项 | 结果 |
|---|---|
| 用户目录绝对路径（`C:` 开头含 Users 的两种写法） | **0** |
| 主机名（真实值已脱敏，此处不重复） | **0** |
| 真实内网 IP（`192.168.16.*` 除 `.0`/`.255`/通配符外） | **0** |
| `192.168.11.*` / `172.25.160.*` | **0** |
| 机器 SID `S-1-5-21-…`（排除公开组 SID） | **0** |
| NUL 字节（UTF-16 残留） | **0 / 67 文件** |
| 编码 | **67/67 UTF-8** |
| 行数守恒 | 44 份比对 dry-run 记录，**0 处回归** |
| mojibake（UTF-8 被按 ANSI/GBK 误读的典型残留字符） | **0** |

**lead 复核中发现的 4 处「残留」及裁定**（全部为复核正则过宽导致的误报，非脱敏遗漏）：

| 命中 | 实情 | 裁定 |
|---|---|---|
| 4 处 email | 全是 Go 模块路径 `golang.org/toolchain@v0.0.1-go1.26.8.windows-amd64` 被邮箱正则误捕 | 误报 |
| 1 处 `fe80:` | 已是 `fe80::....%68/64`，地址段已掩码，只留接口索引 | 已处理 |
| 3 处 GUID | 2 处是公开 SignPath 组织 id（CI 中本就公开）、1 处是 team-message ID（内部标识，无 PII） | 保留合理 |
| `192.168.16.15` | 原文是作者自己写的通配符 `192.168.16.15X` | 误报 |

**保留项的裁定理由**（均已复核确认）：

- 22 处 `Administrator` 字面量全部是 Windows 内置角色/协议标识符（`requireAdministrator`、`FilterAdministratorToken`、`IsInRole(Administrator)`、`state_guarded/administrator`…）。报告本身在教人 grep 这些名字，替换成 `<ADMIN_ROLE>` 会让报告说谎 → **保留**。
- `172.24.8.11` 是编造的 UI 示例地址，与点名保留的假 MAC `00-15-5D-01-02-03` 成对出现，掩码会破坏「三项身份字段」示例的含义 → **保留**。
- 产品设计网段 `192.168.16.0/24`、测试夹具 `10.66.*`/`10.77.77.77`、公网 DNS、TUN 自有 `172.19.0.*` → **保留**。

**成员计划外追加发现并处理**：真实个人邮箱（`95-ci-run.md:21`）、DHCP 客户端标识 `0x…`、7 处 IPv6 链路本地地址（已验证不含被掩码 MAC 的 EUI-64，无法反推）、约 38 处机器专属 GUID、Windows 8.3 用户目录短路径（用户名缩写形式）。

### lead 的后续整理

- `07-repo-hygiene-reports.md:75` 脱敏后读作 `%USERPROFILE% ×73、×19、×1`（原文三条统计项是同一个用户目录的不同路径形态，统一映射后语义重复）→ 已合并为「合计 ×93 处」并注明原因。
- `00-final-verdict.md` 与 `07` 的「隐私泄露」结论加注**已闭环**脚注，保留原始判断作为时点记录。
- `_test-audit-scratch.md`（312 行实质审查内容）→ 更名为 `08-test-audit.md` 并纳入编号序列。

---

## 5. 门禁（全部由 lead 亲自复跑，非采信成员自报）

| 闸门 | 命令 | 结果 |
|---|---|---|
| Go 编译 | `go build ./...` | ✅ exit 0 |
| Go vet | `go vet ./...` | ✅ exit 0 |
| Go 格式 | `gofmt -l .` | ✅ 零输出 |
| Go 测试 | `go test -count=1 ./...` | ✅ exit 0，internal/services 37.8s |
| Go 竞态 | `go test -race -count=1 ./internal/services/...` | ✅ exit 0，无 DATA RACE（56.3s） |
| 前端类型 | `tsc --noEmit` | ✅ exit 0 |
| 前端测试 | `vitest run` | ✅ exit 0，**44 文件 / 423 passed**（基线 409，净 +14） |
| 前端构建 | `vite build --mode production` | ✅ exit 0，`✓ built in 509ms` |

---

## 6. 本次未修（用户未授权）

| 编号 | 问题 | 位置 |
|---|---|---|
| P1-2 | 便携 zip 未进 Release —— `download-artifact` 只取 `HypoMux-Windows-*`，便携包被发布者漏掉 | `.github/workflows/build.yml:463` vs `:506` |
| P1-3 | Authenticode 校验静默空跑 —— 所依赖的测试已随 updater 删除，`-run` 匹配不到用例仍 EXITCODE=0 | `build.yml:296-301` |
| P2-1 | 文档漂移 3 处 | `README_EN.md`、`release-notes/v2.7.0(.en).md`、`docs/README.md` |
| P2-2 | 保护名单前后端两份无编译期约束的镜像 | `hyperv_adapter.go:2012-2028` / `managementAdapters.ts:117-150` |
| P2-3 | 后端注释与需求反向 | `hyperv_adapter.go:1329-1335`、`:1348-1351` |
| — | `internal/services` 无任何 CI 守护（非 Windows 上 Hyper-V 用例恒 skip，workflows 只校验 gofmt/build 与 engine 模块），建议加 windows-2025 job | `.github/workflows/` |

## 7. 提交前仍需人工处理

1. `reports/` 全部脱敏完毕，**可以入库**。
2. 5 个文件跨主线混改需 `git add -p` 逐 hunk 拆：`App.tsx`、`i18n/legacy.messages.json`、`app.css`、`settings.go`、`SettingsPage.tsx`。
3. 建议 4 个 commit：vNIC / updater 删除 / About 删除 / fork 重品牌化。
4. 建议把 `desktop/cover_*`、`.DS_Store` 一类测试产物补进 `.gitignore`（`cover_hyperv` 本轮已删除，但未加忽略规则，下次跑测试还会复现）。
