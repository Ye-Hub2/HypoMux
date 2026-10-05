# 75 · T-E 只读审计：桌面层 Hyper-V 服务（Go）

- 审计员：`hyperv-contract-audit`（只读；未修改任何源文件，未 `git add` / `git commit`）
- 被审计对象：`desktop/internal/services/hyperv_adapter.go`（1525）、`hyperv_adapter_windows.go`（448）、`hyperv_adapter_other.go`（24）、`hyperv_adapter_test.go`（911）、`desktop/main.go`
- 冻结契约：`reports/vnic/70-frozen-hyperv-interface.md` §3.1–§3.9（逐条读完，非摘要）
- 实现方报告：`reports/vnic/72-desktop-hyperv.md`（全文读完，用于区分「新问题」）
- 环境：`go version go1.27.0 windows/amd64`，`GOTOOLCHAIN=auto`，`GOPROXY=https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct`，workdir = 仓库根

**总体结论：有条件放行。** 契约骨架、提权方案、归属三重判定、`List()` 约束、main.go 接线、删除面全部达标，门禁全绿。但发现 **4 个必须修**的问题，其中 M1（调度策略被静默清零）是本次审计最重要的发现，实现方报告完全未提，且**不是 Hyper-V 专属**——它会污染任何用户的出口池操作。

---

## 1. 结论表

| # | 审计项 | 结论 | 证据 | 一句话 |
|---|---|---|---|---|
| 1 | §3.1 签名逐字一致 | ✅ | `hyperv_adapter.go`：`NewHyperVAdapterService(settings *SettingsService, adapters *AdapterService) *HyperVAdapterService`、`List() ([]HyperVAdapterStatus, error)`、`Switches() ([]HyperVSwitch, error)`、`Create(switchName string, count int) ([]HyperVAdapterStatus, error)`、`Remove(name string) error`、`Shutdown()` | 6 个导出面与冻结契约逐字相同 |
| 1 | §3.2 模型字段/类型/JSON tag | ✅ | `hyperv_adapter.go:120-135` `HyperVAdapterStatus` 14 字段、`:139-145` `HyperVSwitch` 5 字段 | tag 全 camelCase，与 `bindings/.../models.ts:522-549` 逐字段一致 |
| 1 | §3.2 `state` 枚举 | ⚠️ | `hyperv_adapter.go` consts `absent/creating/ready/failed` 与契约相符，但另有 `hypervStateSkipped` | `skipped` 只在**提权脚本内部**的 result 行使用，不出现在 Go 模型，符合契约；仅需注意前端不要等它 |
| 2 | §3.3 脚本是**常量**、无插值 | ✅ | `hyperv_adapter_windows.go:235-448` `const hypervPowerShellScript`，全文件零拼接 | 逐字核对为真正的 `const` 原始字符串 |
| 2 | §3.3 可变数据 base64 注入 | ✅ | `hyperv_adapter_windows.go:186-199` 参数表末尾追加 `<b64 envelope JSON>`；脚本内 `$args[0]` → b64 → UTF8 → `ConvertFrom-Json` | base64 无空格/引号，`strings.Join` 进 `Parameters` 安全 |
| 2 | §3.3 `-EncodedCommand` UTF-16LE | ✅ | `hyperv_adapter_windows.go:216` `hypervUTF16LE`；`hyperv_adapter_test.go:852-860` 断言字节序 `A 00 d 00` | 有测试锁死 |
| 2 | §3.3 必须是 `powershell.exe` | ✅ | `hyperv_adapter_windows.go:203` 解析 `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`，回退 `resolveWindowsPowerShellExecutable()` | 绝对路径（`ShellExecuteExW` 的 cwd 是 System32），不是 `pwsh` |
| 2 | §3.3 无 BOM UTF-8 + `.tmp`/`Move-Item` 原子提交 | ✅ | 脚本内 `[IO.File]::WriteAllText($tmp,$json,(New-Object Text.UTF8Encoding($false)))` + `Move-Item -LiteralPath $tmp -Destination $resultPath -Force` | 有测试断言这两个串必须出现 |
| 2 | §3.3 不复用 `EnsureElevated` | ✅ | 全文件无 `EnsureElevated` 引用 | — |
| 2 | §3.3 不新增 exe / helper 子命令 | ✅ | 产物只有三个 `.go` 文件，无新二进制、无新子命令 | — |
| 2 | §3.3 无 shell 注入面 | ✅ | 脚本内无 `Invoke-Expression`、无字符串拼命令；`TestHypervPowerShellScriptIsConstantAndSafe`（`:864-898`）逐条断言 10 个危险 cmdlet 不出现 | 注入安全性靠结构而非转义 ✅ |
| 2 | §3.3 **超时只报错不改状态** | ❌ | `hyperv_adapter.go:1164-1166` + `:1214-1222` | 提权脚本超时时把**全部**预留条目从台账抹掉，而父进程杀不掉提权子进程 → 见 **M3** |
| 3 | §3.4 台账 + 命名 + MAC 三重判定 | ✅ | `hyperv_adapter.go:351-369` `isHyperVAdapterName`（前缀 EqualFold + 1–4 位纯数字）、`:469` `hypervMACEqual`（任一侧非法即 false）、`:1385-1402` | 删用户的 `xuni-*` / `以太网` / WSL 卡在 Go 侧就被挡住 |
| 3 | §3.4 未登记 ⇒ `skipped` | ⚠️ | 脚本 remove 分支查不到 → 写 `status='skipped'` 且不删；但 `Remove()` 在 Go 侧对未登记直接返回 `not_managed`（`:1391`），**根本不进脚本** | 对「台账外同名卡」够用；但台账被误抹的孤儿卡会永久卡死在这（见 **M2/M3**） |
| 3 | §3.4 大小写/空格差异 | ✅ | `:1374` `TrimSpace`；`:565` `hypervInventory.find` 名字兜底走 EqualFold；`hypervMACEqual` 归一化 `:409` `normalizeHypervMAC`（接受 `: - . 空格`，统一小写 12 hex） | 构造的攻击场景均被挡住 |
| 4 | §3.5 500ms / 15s / 45s / 60s | ✅ | `hypervPollInterval=500ms`、`hypervInterfaceTimeout=15s`、`hypervAddressTimeout=45s`、`hypervOverallTimeout=60s`；脚本 `waitip` 分支 `Start-Sleep -Milliseconds 500` + `$deadline` 自带界 | 脚本自带界 ✅ |
| 4 | §3.5 成功判据 `Dhcp` + `Preferred` | ❌ | `hyperv_adapter.go:1327`（脚本侧正确）**但** `:1344-1350` 的宿主扫描兜底只要 `host.hasIPv4` 就判 ready | 兜底分支在脚本**成功返回**时也会跑，主动推翻契约判据 → 见 **M4** |
| 4 | §3.5 超时是软失败（不删卡） | ✅（契约层面） | `:1355-1361` 标 failed + `hypervDHCPTimeoutHint`，不调用任何删除；`TestDeriveHyperVState:337-338` 锁死 | 但 failed **不可自愈**，见 N3 |
| 5 | §3.6 走 `UpdateHome`，禁 `UpdateFields` | ✅ | 全文件 `UpdateFields` 只出现在 `:1451` 的注释里；唯一调用点 `:1521` | `settings.go:265-310` 白名单确认**不含** `selected_adapter_ids`/`adapter_weights`/`mode`，契约判断准确 |
| 5 | §3.6 传当前 `Mode` | ✅ | `:1495-1498` 读 `settings.Get()` 并校验 proxy/tun，非法回落 `DefaultSettings().Mode` | — |
| 5 | §3.6 顺序 提权→台账→UpdateHome | ⚠️ | `:1131-1152` 台账**预留**写在提权**之前**；`:1158` 提权；`:1210` 台账回写；`:1261`（异步）UpdateHome | 顺序是「预留→提权→回写→池」，偏离字面顺序，但 `:1091-1092` 明确声明为刻意取舍且不变量更强（系统状态 ⊆ 台账）。**判定为可接受偏差** |
| 5 | §3.6 不持锁跨越提权 | ✅ | `opMu` 只与 Create/Remove 互斥；`List()`/读路径走 `mu`/`ledgerMu` | 符合 §3.6.4 |
| 5 | §3.6 创建后提示重启聚合 | ✅ | `:1238-1240` 非 failed 行 `LastError = hypervPoolRestartHint` | 契约缺口已在 72 §7.5 提请裁决，本审计认可这是当前最优解 |
| 5 | §3.6 **保留用户其它策略设置** | ❌ | `hyperv_adapter.go:1521` + `settings.go:413` + `scheduling.go:162-168` | `UpdateHome` 硬编码传 `""` → `normalizeSchedulingStrategy` 把 `Strategy` 归一成 `round-robin`/`weighted` → 见 **M1** |
| 6 | §3.7 16/批、32/总 | ✅ | `:183-220` `hypervMaxBatchSize=16`、`hypervMaxTotalAdapters=32`；`:337-345` `hypervCheckBatchCapacity`；`TestHypervCheckBatchCapacity` 8 行边界 | 断言是真的（含 0、-1、17、32+1） |
| 6 | §3.7 MAC `02:` 本地管理位 | ✅ | `:hypervMACOUI="021A2B"`（0x02 = LAA + 单播）；`TestHypervMACValueLocalAdministration:156-168` | 静态 `-StaticMacAddress`，不用 256 个的动态池 ✅ |
| 6 | §3.7 序号不回收 | ✅ | `allocateNames:311` 单调递增，删除只 `removeEntry`；`TestHypervLedgerNoReuse:172-198` | 名字与 MAC 都不复用 |
| 6 | §3.7 `count<=0` 校验 | ✅ | `:1101-1103`；`TestCreateRejectsInvalidArguments:579-581` | 校验在平台检查**之前**，跨平台可测 |
| 6 | §3.7 部分成功呈现 | ✅ | `:1174-1208` 首个硬失败标「第 k/共 n 张」，`failure` 携带可区分错误码，成功项保留 | — |
| 7 | §3.8 `List()` 不触发 UAC | ✅ | `readInventory:878-905` 恒 `elevated=false`；`hypervExecuteUnelevated` → `hypervRunDirect`（普通 `exec`），从不碰 `ShellExecuteExW` | — |
| 7 | §3.8 不用 `AdapterService.List()` | ✅ | `adapters` 字段仅「预留依赖」；`scanHypervHostInterfaces:651` 自扫 `net.Interfaces()` | `adapters.go:80-82` 的 `if ipv4 == "" { continue }` 已亲自核实，契约理由成立 |
| 7 | §3.8 不弹 UAC | ✅ | `List()` 路径无提权调用 | — |
| 8 | §3.9 Shutdown 顺序 | ✅ | `main.go:158 hypervAdapterService.Shutdown()` → `:159 engineService.Shutdown()` | 已用 UTF-8 显式读取核实（`Get-Content` 的乱码是控制台代码页假象） |
| 8 | §3.9 binding 名 / import 路径 | ✅ | `bindings/.../services/hypervadapterservice.ts`（类型名小写，符合推导规则）；`services.ts:4` import 路径正确 | ⚠️ 该文件是**手写占位**（`$Call.ByID(1001..1005)`），真实 Wails 绑定映射本审计无法验证 |
| 9 | 删除面无残留 | ⚠️ | `VirtualAdapterService` / `vnic.create` / `vnic.status` / `vnic.remove` 全部 0 命中 | 3 处陈旧文案仍在：`desktop/portable/launch-portable.cmd:23,24,31`、`desktop/frontend/src/components/vnic/vnic.css:1-2`。**不在我写范围，仅报告** |
| 10 | 测试覆盖关键分支 | ✅（但有盲区） | §3.4 三重判定、§3.7 边界均有真断言；提权路径明确不覆盖（`hyperv_adapter_test.go:14-19` 自述） | 「测试通过 ≠ 正确」的实例见 M1 与 4 号测试盲区 |
| 11 | 独立门禁 | ✅ | 见 §4 | 全部真实通过 |
| 11 | 跨平台降级 | ✅（新增 ⚠️） | `GOOS=linux` / `GOOS=darwin` 的 `go build ./internal/services/` 均 exit 0 | 但 `go test` 在非 Windows 上**编译失败**，见 N5 |

> 注：`rg` 未安装在本机 PATH 上（`CommandNotFoundException`），全部检索改用 `grep` 工具完成，结论不受影响。

---

## 2. 必须修

### M1 ★ 出口池写操作会静默把用户的调度策略清零（影响面最大）

**性质**：§3.6 合规性 + 静默用户数据丢失。

**链路**（全部亲自核实）：

- `hyperv_adapter.go:1521` — `s.settings.UpdateHome(mode, current.Weighted, selected, weights)`
- `settings.go:413` — `return s.updateHomeStrategy(mode, weighted, selectedIDs, weights, "")` ← **第 5 个参数硬编码空串**
- `scheduling.go:162-168` —
  ```go
  func normalizeSchedulingStrategy(strategy string, weighted bool) (string, error) {
      if strategy == "" {
          if weighted { return "weighted", nil }
          return "round-robin", nil
      }
      ...
  }
  ```
- `settings.go:417` — `strategy, err := normalizeSchedulingStrategy(strategy, weighted)` → `next.Strategy` 被覆盖

**后果**：选了「延迟优先」(`latency-first`) 或「自适应吞吐」(`adaptive-throughput`) 的用户，只要**创建或删除一张 Hyper-V 虚拟网卡**（这正是本功能的核心操作），`settings.json` 里的 `Strategy` 就被静默改写成 `round-robin`/`weighted`。合法取值见 `scheduling.go:170`；`engine.go:773` 与 `scheduling.go:90` 都按这两个值分支。重启后引擎会按错误的策略协商。用户在 UI 上**看不到任何提示**。

**为什么单测没抓到**（这正是审计项 10 要找的「水分」）：`hyperv_adapter_test.go:645` 用 `UpdateHome("tun", true, ...)` 预置，本身就把 `Strategy` 设成了 `weighted`；`:664` 的断言只检查 `Mode`/`Weighted`，**全文件没有任何一处断言 `Strategy`**。测试通过，但断言的东西不是出问题的东西。

**修法（仓库内已有正确范例）**：同包的 `adaptive_scheduling_test.go:38-39` 就是标准写法——

```go
// hyperv_adapter.go:1521，替换
- if _, err := s.settings.UpdateHome(mode, current.Weighted, selected, weights); err != nil {
+ if _, err := s.settings.updateHomeStrategy(mode, current.Weighted, selected, weights, current.Strategy); err != nil {
```

`updateHomeStrategy` 与 `UpdateHome` 在同一个包，走完全相同的校验与 `commitLocked` 原子提交，满足 §3.6.1「走 SettingsService 私有写入路径而非 UpdateFields」的实质。`engine.go:787` 也是同样用法（传 `effectiveSchedulingStrategy(settings)`）。

**建议同时补一条测试**：预置 `Strategy="latency-first"` → `applyPoolUpdate` → 断言 `Strategy` 不变。

---

### M2 ★ `Remove()` 在 inventory 查询失败时会「删台账但不删卡」，产生 UI 无法清理的孤儿网卡

**证据**（`hyperv_adapter.go:1394-1425`，逐行核对）：

```go
1394:  inventory, _ := s.readInventory()          // ← 错误被丢弃
1395:  if live, found := inventory.find(entry.AdapterID, entry.Name); found {
1398:      if !hypervMACEqual(entry.MACAddress, live.MAC) { return ... }
1403:      if _, err := s.runScript(... "remove" ..., true); err != nil { return err }
1414:  }
1416:  // 注释：幂等：系统里已经查不到（用户手工删过、或上一轮脚本成功但进程被杀）时也照常清台账
1418:  if err := s.updateLedger(func(current *hypervLedger) error { current.removeEntry(target); return nil }); ...
1424:  s.invalidateInventory()
1425:  return s.applyPoolUpdate(nil, []string{hypervHostInterfaceName(target)})
```

`readInventory()` 会失败的真实场景不少：策略禁止非提权读 `Get-VMNetworkAdapter`（72 §7.2 自己就把这条列为**未验证风险**）、`powershell.exe` 被策略拦截、结果文件缺失/超过 1MiB 上限（`hypervMaxResultBytes`）。此时 `inventory` 是零值，`find` 必然 `found=false`，于是 `:1403-1413` 的提权删除**整段被跳过**，但 `:1418-1425` 照样清台账 + 移出池。

**后果**：Hyper-V 对象和宿主 `vEthernet (HypoMux-vnic-NN)` 都还在，台账里却没有了。此后 `List()` 把它当 `Managed=false` 的只读行展示，而 `Remove()` 会在 `:1391` 永远返回 `not_managed`「不在 HypoMux 台账中」。**用户在 UI 里没有任何办法删掉它**，只能自己去 PowerShell 跑 `Remove-VMNetworkAdapter`。

`:1416-1417` 的注释把「查询失败」和「网卡真的不存在」当成了同一件事——这是本 bug 的根因。

**安全性说明**：方向上是**失败向安全侧倒的**（不会误删用户的卡），所以不是数据安全问题，而是可用性/一致性事故。但它落在归属系统上，必须修。

**修法**：

```go
- inventory, _ := s.readInventory()
+ inventory, invErr := s.readInventory()
+ if invErr != nil {
+     return hypervErrorf(hypervCodeUnavailable,
+         "查询 Hyper-V 现有网卡失败：%v；已中止删除，台账保持不变（不会留下删不掉的孤儿网卡）", invErr.Error())
+ }
  if live, found := inventory.find(...); found { ... }
```

只保留「脚本成功但进程被杀」这一条幂等路径即可——那种情况 `runScript` 是返回成功的，`found=false` 走的仍是无害分支。若要更保守，可在 `found=false` 时**保留台账行并标 `absent`**，交给用户显式确认删除。

---

### M3 ★ `Create()` 提权脚本超时会把全部预留条目从台账抹掉，违反 §3.3「超时只报错不改状态」

**证据**（`hyperv_adapter.go:1158-1226`）：

- `:1158-1163` `runScript(create, hypervCreateScriptTimeout=150s, elevated=true)`；父进程超时 → 返回 `(result=nil, runErr=errHypervScriptTimeout)`。**关键：父进程杀不掉提权子进程**（契约 §3.3 引 `engineclient/privileged_windows.go:390-404`），`hypervRunElevatedShellExecute` 在 `WAIT_TIMEOUT` 时只返回 `errHypervScriptTimeout`，**不终止子进程**。
- `:1164-1166` `if result == nil { result = &hypervScriptResult{} }` → `created` 映射为空。
- `:1179-1208` 第一条 `ok=false` → 标 failed、置 `failure`；其后每条在 `:1192-1194` `if failure != nil { continue }` 被跳过，**不进入 `finished`**。
- `:1214-1222` 对每个既不在 `created` 也不在 `finished` 的预留条目执行 `ledger.removeEntry(entry.Name)` → **N 条全部从台账消失**。

**后果**：提权子进程仍在跑，完全可能把 N 张卡全建出来。最终系统里有 N 张孤儿网卡 + N 个宿主接口，台账为空 ⇒ 同 M2，UI 永远删不掉。这一条是字面违反 §3.3。

**触发条件不是纯理论**：`Add-VMNetworkAdapter -ManagementOS` 在交换机 down / 虚拟网卡驱动卡死 / uplink 无响应时，Hyper-V 自身的长超时完全可能超过 150s。

**修法**：区分「脚本**明确回报**了失败」和「父进程**放弃等待**」两种情况——

```go
// 在 :1174 之前计算
timedOut := runErr != nil && errors.Is(runErr, errHypervScriptTimeout)

// 在 :1210 的 updateLedger 里，对 timedOut 走另一条路：
//   - 不 removeEntry 任何条目（保留 reserved 原样）
//   - 把整批标成 failed + LastError「提权脚本超时，结果未知；请刷新列表核对后再操作」
//   - 不启动 awaitBatch（status 未知的卡不能自动进池）
```

保留台账行的理由和 M2 一致：**台账宁可多，不可少**。多出来的行用户能删，少掉的行用户永远删不掉。

---

### M4 ★ `awaitAddresses` 的宿主扫描兜底推翻了 §3.5 的成功判据

**证据**（`hyperv_adapter.go:1315-1350`）：

```go
1315:  if err != nil { return }              // 脚本失败 → 直接返回，不进兜底
...
1327:  if TrimSpace(row.PrefixOrigin) != "Dhcp" || !EqualFold(row.AddressState, "Preferred") { continue }
...
1338:  // 补一轮宿主扫描：接口回来了但脚本没抓到…
1340:  hosts := scanHypervHostInterfaces()
1344:  if !ready[key] {
1345:      if host, ok := hosts.byAlias[key]; ok && host.hasIPv4 {   // ← 只要有 IPv4 就算 ready
1346:          ready[key] = true
```

因为 `:1315` 在脚本出错时已经 `return`，兜底分支**只在脚本成功返回时执行**。也就是说：脚本明确回报了 `PrefixOrigin=Static` 或 `AddressState=Duplicate`，Go 侧 `:1327` 正确地拒绝，然后 `:1345` 又用「有个非 APIPA 的 IPv4」把它翻回 `ready`。

`host.hasIPv4` 确实排除了 169.254（APIPA），但**不排除静态地址、其他来源的地址、或 Windows 自行分配的非 DHCP 地址**。§3.5 的判据是「必须同时满足 `PrefixOrigin=Dhcp` 且 `AddressState=Preferred`」，这是硬判据。

**后果**：一张从未拿到 DHCP 租约的网卡被标成 `ready`、进入出口池、重启后被引擎拉去聚合，表现为聚合静默失效。这与本需求的初衷（多张独立 MAC 稳定聚合）直接冲突。

**修法**：兜底只能用来**补齐**脚本没返回的行，不能用来**推翻**脚本的否定判定。改成：

```go
1344:  if _, reported := scriptReported[key]; !reported {   // 脚本没提到这个 alias 才允许兜底
1345:      if host, ok := hosts.byAlias[key]; ok && host.hasIPv4 {
1346:          ready[key] = true; ...
```

即：先从 `result.Addresses` 收集「脚本见过的 alias 集合」，只有脚本完全没见过的 alias 才允许走宿主扫描兜底。若 lead 认为兜底整体不必要（它只是应对脚本竞态的缓解），直接删掉 `:1338-1350` 也是合规且更简单的选择。

---

## 3. 建议改（不阻塞放行）

1. **N1 · 出口池的键名不跟随网卡改名**（`hyperv_adapter.go:1261` / `:749` / `:761`）。`awaitBatch` 把 `vEthernet (HypoMux-vnic-01)` 写进 `SelectedAdapterIDs`；若用户在 `ncpa.cpl` 把网卡改名成 `以太网 3`，`buildHyperVStatus:749` 会用 MAC 认领并把 `status.InterfaceName` 改成 `以太网 3`，于是 `:761` 的 `pool[strings.ToLower("以太网 3")]` 查不到 ⇒ **`InPool` 显示 false，尽管卡确实在池里**。更糟的是 `settings.json` 里那个 `vEthernet (...)` 键变成悬空引用，引擎重启后会去绑定一个不存在的别名，没有任何自动修复。修法：在 `buildHyperVStatus` 里当「MAC 认领到的实际别名 ≠ `vEthernet (...)` 形式」时告警，或在 `List()` 里顺带做一次别名对账写回。
2. **N2 · `derivedState` 的 failed 是永久性的，没有任何 reconcile**。`:717` 的粘性是有意的（注释 `:715-716` 解释得很清楚，`TestDeriveHyperVState:341` 也锁死了），契约层面合规。但实际后果是：一张在 t+50s 才拿到 DHCP 的卡会**永远**显示红色 `failed` + DHCP 超时文案，而它其实完全可用、也已经在池里。72 §7.3 假设了「前几秒显示 failed、随后自愈」，代码并不自愈。用户的唯一出路是删除重建——而序号不回收（§3.7）、MAC 不复用，每来一次就往 32 张总上限逼近一格。建议在 `deriveHyperVState` 加一条：条目是 `failed` **且**宿主网卡现在确有可用 IPv4 **且**条目从未被人工重试过 ⇒ 回到 `ready` 并清 `LastError`。
3. **N3 · `Remove()` 的幂等分支缺一次 inventory 语义澄清**。修 M2 后这条自动消解。
4. **N4 · 脚本 `create` 分支复用同 MAC 对象时回报 `$hit.Name`**（`hyperv_adapter_windows.go` create 分支）。若目标 MAC 已存在但名字不同（例如上次超时留下的、`name_conflict` 场景），脚本报回的是真名，父进程 `:1180` 按 `entry.Name` 查不到 ⇒ 该条目被标 failed，但卡是存在的且 MAC 已写进台账。建议回报时把请求名也一并带回，或让父进程按 MAC 兜底匹配。
5. **N5 · `hyperv_adapter_test.go` 让 services 包在非 Windows 上无法编译测试**。实测：
   ```
   GOOS=linux go -C desktop test -run XXXNONE ./internal/services/
   internal\services\engine_integration_test.go:70:23: undefined: proxyMarkerPath      ← 仓库既有
   internal\services\hyperv_adapter_test.go:853:13: undefined: hypervUTF16LE          ← 本轮新增
   internal\services\hyperv_adapter_test.go:865:12: undefined: hypervPowerShellScript  ← 本轮新增
   FAIL  github.com/Hypostasis-Cat/HypoMux/desktop/internal/services [build failed]
   ```
   72 §6 提到了 `engine_integration_test.go` 那条既有问题，但**没发现本轮又新增了 2 个未定义符号**。因为 CI 的 Go 作业全在 `windows-2025`（72 §7.6 已核实），当前不阻塞。修法二选一：把 `hypervPowerShellScript` 和 `hypervUTF16LE` 移进跨平台的 `hyperv_adapter.go`（它们是纯数据/编码，无系统调用，移过去后测试可在全平台跑，符合测试文件 `:566` 自述的「跨平台 CI 也能覆盖」意图）；或给这两个 windows-only 用例单独拆一个带 `//go:build windows` 的文件。
6. **N6 · 台账预留条目的 MAC 与 `awaitBatch` 的匹配**。`:1229` `s.awaitBatch(reserved)` 传的是**预留时的副本**，其 `AdapterID` 为空；`awaitInterfaces:1277` 只按 alias 匹配，所以 MAC 认领这一路在后台等待里其实没被用上。不算 bug（alias 匹配已足够），但若将来 NIC 别名被改，`awaitInterfaces` 会误判「没出现」而提前退出。建议改传 `finished`。
7. **N7 · 陈旧文案（报告不修改，仅提请转交）**：`desktop/portable/launch-portable.cmd:23,24,31` 仍在描述旧的 Wintun `vnic.create` / `virtual_adapter.go:85-87` / `errVNICUnsupported` / 「自带 vnic.create 的管理员引擎」；`desktop/frontend/src/components/vnic/vnic.css:1-2` 注释仍写着已删除的 `VirtualAdapterPanel.tsx`。前者属 infra-analyst 的 T-D 写范围，我未改动。

---

## 4. 门禁真实输出（本审计亲自复跑，非转述 72）

```
PS> go version
go version go1.27.0 windows/amd64

PS> go -C desktop build ./...
(零输出)
BUILD_EXIT=0

PS> go -C desktop vet ./...
(零输出)
VET_EXIT=0

PS> gofmt -l desktop\internal\services desktop\main.go
[]
GOFMT_EXIT=0

PS> go -C desktop test -count=1 ./...
ok  	github.com/Hypostasis-Cat/HypoMux/desktop	0.223s
?   	github.com/Hypostasis-Cat/HypoMux/desktop/build/windows/syso	[no test files]
?   	github.com/Hypostasis-Cat/HypoMux/desktop/cmd/release-version	[no test files]
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/cmd/update-manifest-sign	0.201s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient	3.477s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform	0.189s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails	0.468s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion	0.285s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/services	34.537s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/startup	0.213s
TEST_EXIT=0

PS> $env:GOOS="linux";  go -C desktop build ./internal/services/
(零输出)  EXIT=0
PS> $env:GOOS="darwin"; go -C desktop build ./internal/services/
(零输出)  EXIT=0
PS> $env:GOOS="windows"; go -C desktop build ./internal/services/
(零输出)  EXIT=0
```

与 72 §6 的数字一致（耗时抖动在正常范围内）。**CI 覆盖不到的降级路径我补跑了，且额外发现 `go test` 在非 Windows 上编译失败**（见 N5）——72 只跑了 `go build`，没跑 `go vet`/`go test` 的交叉编译，因为那会撞上 `engine_integration_test.go` 的既有问题。

---

## 5. 实现方报告未提及的新问题（本次审计的主要增量）

| 编号 | 问题 | 72 是否提及 | 我的判断 |
|---|---|---|---|
| **M1** | `applyPoolUpdate` → `UpdateHome` → `normalizeSchedulingStrategy("")` 静默清零用户的 `latency-first`/`adaptive-throughput` | ❌ **完全未提** | 必须修，一行改动。这是唯一会影响**非 Hyper-V 用户既有设置**的问题 |
| **M2** | `Remove()` 在 `readInventory()` 失败时清台账不清卡 → 孤儿网卡 UI 无法删除 | ❌ 未提 | 必须修。注意方向是失败安全的（不会误删用户网卡），但归属系统自洽性被破坏 |
| **M3** | `Create()` 提权超时 purge 全部预留条目，字面违反 §3.3「超时只报错不改状态」 | ⚠️ 部分提及（72 §2.8 只说「硬失败停本批」是**正常**语义，没区分「脚本回报失败」与「父进程放弃等待」） | 必须修。这是契约明文条款 |
| **M4** | `awaitAddresses` 的宿主兜底推翻 `PrefixOrigin=Dhcp AND AddressState=Preferred` 判据 | ❌ 未提（72 §2.6 表格声称判据已落地） | 必须修 |
| N1 | 出口池键名不跟随网卡改名 ⇒ `InPool` 误报 false + `settings.json` 悬空引用 | ❌ 未提 | 建议改 |
| N2 | `failed` 状态无自愈、无重试入口；72 §7.3 假设的「随后自愈」与代码不符 | ❌ 未提，且 72 §7.3 的假设是错的 | 建议改 |
| N5 | 本轮新增 2 个跨平台未定义符号（`hypervUTF16LE`/`hypervPowerShellScript`） | ⚠️ 只提了既有的 `proxyMarkerPath`，没发现增量 | 建议改，不阻塞 |
| N4 | 脚本 create 分支复用同 MAC 对象时回报真名，父进程按名匹配失败 | ❌ 未提 | 建议改 |

---

## 6. 无法验证 / 需 CI 或真机把关

以下项**我无法判断**，与 72 §7 的未验证项一致或更严格：

1. **整段提权链路从未在真机执行过**。`ShellExecuteExW + runas`、UAC 弹窗、`ERROR_CANCELLED` 映射、`SEE_MASK_NOCLOSEPROCESS` + `WaitForSingleObject`、`Move-Item -LiteralPath` 的原子性、`$halted` 控制流、PowerShell 5.1 语法 —— 全部只经过编译与单测。**这是最大风险点**，我同意 72 §7.1 的判断。
2. **非提权能否只读 `Get-VMSwitch` / `Get-VMNetworkAdapter` 未知**。若本机策略要求管理员，`List()` 会全程返回 `hyperv_unavailable`；M2 的修复恰好也会在这条路径上返回更明确的错误。建议真机用**非管理员** PowerShell 单独验一次这两个 cmdlet。
3. **真实 Wails binding 映射未验证**。`bindings/` 整个目录被 gitignore，当前是手写占位（`$Call.ByID(1001..1005)`），只有 TS 形状与 §3.1/§3.2 对得上。CI 的 `wails3 generate bindings -clean=true -ts -i`（`build.yml:139-142`）能否生成与占位一致的代码，需要 CI 实跑。
4. **M3 的超时路径、M2 的 inventory 失败路径，在单测里都没有覆盖**（`hyperv_adapter_test.go:14-19` 自述提权路径不测；`Remove` 的测试只覆盖了「未登记就拒绝」的早期闸门 `:597-621`，没有走到 `:1394` 之后的分支）。修 M2/M3 时应补针对这两条路径的注入式测试（把 `readInventory`/`runScript` 换成可注入的函数指针，或至少把「不改台账」这个不变量抽成纯函数来测）。
5. **`Get-NetIPAddress` 的 `PrefixOrigin=Dhcp` + `AddressState=Preferred` 判据在真机的实际取值**，以及 Windows DAD 期间是否会先返回 `Tentative`（72 §7.3 已列）。

---

## 7. 放行建议

**建议：有条件放行。**

- **M1 应当立刻修**（一行，零风险，收益覆盖所有用户）。
- **M2 / M3 建议在真机验证前修完**——它们都只在异常路径触发，但一旦触发就产生**用户无法自救的孤儿网卡**，正好卡在这个需求最核心的「多张独立 MAC 聚合」上。
- **M4 影响每一次创建**（不是异常路径），建议同批修。
- 其余为建议改，可排入后续迭代。

真机验证仍应按 72 §7.1 的最小序列执行，并**务必用专用外部交换机，不要在生产网卡的交换机上试**。