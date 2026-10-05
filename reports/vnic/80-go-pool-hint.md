# 80 · Go 侧收口：出口池写入失败可见化 + LastError 提示标记化

范围：只改 `desktop/internal/services/hyperv_adapter.go` 与 `hyperv_adapter_test.go`。
对应审计条目：`reports/vnic/78-ux-audit.md` 的 **M2（Go 半边）**、`reports/vnic/79-i18n-tests-audit.md` 的 **§A**。

---

## 1. M2 的 Go 半边：`_ = s.applyPoolUpdate(...)` 吞掉了整个根因

### 问题

`awaitBatch` 的后台 goroutine 里原本是：

```go
if len(appeared) > 0 && s.opened() {
    _ = s.applyPoolUpdate(appeared, nil)   // 错误整个丢掉
}
s.awaitAddresses(appeared)
```

`Create` 在 `:1421`（现 `:1616`）启动这个 goroutine 后**立刻返回**，所以：

- 网卡建好了，`List()` 自愈出 `state=ready`；
- 出口池写入发生在返回之后，失败与否前端此刻一无所知；
- 页面却在 `create()` resolve 后**无条件** `setRestartRequired(true)`（前端侧由 `80-frontend-polish` 一并改掉）。

三者叠加的后果：用户看到的界面是「已就绪 + 已加入出口池 + 重启后生效」，实际重启聚合后新卡**完全不生效**，而全程零根因。

### 改法

新增 `(*HyperVAdapterService).recordPoolHint(names []string, lastError string)`，并把 `awaitBatch` 改成：

```go
if err := s.applyPoolUpdate(appeared, nil); err != nil {
    s.recordPoolHint(appeared, hypervHintWithDetail(hypervHintPoolUpdateFailed, err.Error()))
} else {
    s.recordPoolHint(appeared, "")   // 成功即自愈，清掉上一轮遗留的标记
}
```

`recordPoolHint` 的语义：

| 入参 | 行为 |
| --- | --- |
| `lastError != ""` | 逐字写入这些条目的 `LastError` |
| `lastError == ""` | **只**清掉「池写入失败」标记，绝不碰创建失败 / DHCP 超时等其他来源的 `LastError` |

两条实现细节：

1. **清除路径先只读确认**。`updateLedger` 无条件落盘，若不预检，每次成功创建都会白白重写一次台账文件。预检退化成一次冗余写入是最坏情况，不会写错东西。
2. **台账里没有的名字直接跳过**，不凭空造条目（单测第 4 条钉死）。

写进 `LastError` 而不是新字段，是因为 `HyperVAdapterStatus` 的字段由 §3.2 冻结（CI 每次 `wails3 generate bindings` 重生成），而前端每行已有的 `role="alert"` 就是渲染 `lastError` 的地方。

### 连带修：`awaitAddresses` 会把它抹掉

`awaitAddresses` 在 `creating→ready` 跃迁时原本无条件 `entry.LastError = ""`，而它在 `awaitBatch` 里**排在 `applyPoolUpdate` 之后** —— 直接改会当场把刚写进去的标记清掉，等于没修。

抽出纯函数 `hypervLastErrorAfterReady(lastError string) string`（`:1642`）：只有「池写入失败」标记活下来，其余一律清空。DHCP 失败的分支反过来仍然覆盖成 DHCP 原因 —— 拿不到地址的卡整体就是坏的，「没进出口池」对它没有独立意义。

### 并发安全性（已核对）

`recordPoolHint` 只取 `ledgerMu`，**从不取 `opMu`**，因此与用户同时发起的 `Remove`（先 `opMu` 后 `ledgerMu`）不存在加锁顺序反转；`awaitBatch` 的 goroutine 内部 `recordPoolHint` 与 `awaitAddresses` 是串行调用，也不会自嵌套。

---

## 2. §A：LastError 从「中文原文」改成「机器可读标记」

i18n 审计发现 en 用户在这个页面全程看中文，其中一条路径就是后端把中文文案塞进 `LastError`。

`LastError` 上承载的东西分两类，**必须**用前缀区分，否则前端无从本地化：

| 类别 | 例子 | 前端处理 |
| --- | --- | --- |
| 标记（前缀 `hypomux.hint.`） | 「已加入出口池」「没进出口池」 | 查语言包 |
| 自由文本 | DHCP 超时原因、PowerShell 退出码、驱动报错 | **原样显示**（前端无从翻译） |

### 常量（`:120-129`）

```go
hypervHintCodePrefix       = "hypomux.hint."
hypervHintDetailSeparator  = " | "
hypervHintPoolRestart      = hypervHintCodePrefix + "pool_restart"
hypervHintPoolUpdateFailed = hypervHintCodePrefix + "pool_update_failed"
hypervDHCPTimeoutHint      = "等待 60 秒仍未从交换机拿到可用 IPv4（DHCP 未就绪）；…"   // 纯自由文本
```

原 `hypervPoolRestartHint = "已加入出口池，重启 HypoMux 聚合后生效"` 被 `hypervHintPoolRestart` 取代（`Create` 返回行的 `:1555` 改用它）。

### 切分契约（前端按同一条规则实现）

- `hypervHintWithDetail(code, detail)`：`detail` 空白时只返回 `code`，否则 `code + " | " + detail`。
- `hypervHintCode(lastError)`：不带前缀 ⇒ 返回 `""`（整串都是自由文本）；带前缀 ⇒ 按**第一个** `" | "` 切，返回左半段。

细节里若还有 `" | "`（如 `a | b | c`），整体算细节。这一点两边必须一致，否则 en locale 又开始漏中文 —— 所以逐 case 钉进了单测。

---

## 3. 门禁（真实退出码）

| 命令 | 结果 |
| --- | --- |
| `gofmt -l desktop\internal\services` | 零输出 |
| `go -C desktop build ./...` | exit 0 |
| `go -C desktop vet ./...` | exit 0 |
| `go -C desktop test -count=1 ./...` | exit 0，8 包全 ok（services 43.659s） |

新增测试 3 个：`TestHypervHintCodeAndDetail`（9 个子 case）、`TestHypervLastErrorAfterReady`（5 个子 case）、`TestRecordPoolHintSurfacesAndSelfHeals`（4 个子 case）。

## 4. 回归探针（改坏 → 转红 → 回滚 → 复跑）

| 探针 | 结果 |
| --- | --- |
| `recordPoolHint` 开头加 `return`（模拟回到 `_ =` 吞错） | `--- FAIL: TestRecordPoolHintSurfacesAndSelfHeals`，报「池写入失败的标记没有落到台账：""」 |
| `hypervLastErrorAfterReady` 恒返回 `""`（模拟回到无脑清空） | `--- FAIL: TestHypervLastErrorAfterReady`，报「pool failure kept: 留下 ""; want "hypomux.hint.pool_update_failed \| 磁盘已满"」 |

两处探针已全部回滚，回滚后 `gofmt`/`vet`/全量 `test` 复跑均绿。

## 5. 我自己犯的一个测试错误（已修）

`TestHypervHintCodeAndDetail` 第一版里我写了一条毫无意义的「往返」断言（`hypervHintWithDetail(code, lastError) == lastError`），它对「细节本身含分隔符」的 case 必然不成立，跑出 `blank detail collapses: 往返不一致`。断言写得不对，不是实现有问题。改成直接断言**切分后的细节**（前端真正会用的那个值），并把 `blank` 这个 case 的契约钉成「纯空白不是标记、细节按原文带回，由前端折叠空白并 trim」。
