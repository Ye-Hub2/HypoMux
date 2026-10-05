# 76 — 故障取证 + 前端真实原因上屏

作者：`vnic-error-surfacing` · 日期：2026-10-04 · 写范围：仅前端 4 个文件
范围外未动：`desktop/internal/services/**`（队友 `hyperv-inventory-repro` 的写范围）、`reports/vnic/76-inventory-repro.md`

---

## 1. 取证结果（只读）

### 1.1 数据目录真实路径

解析逻辑：`desktop/internal/services/settings.go:136-150 settingsDirectory()`

- `HYPOMUX_DATA_DIR` 非空 → `filepath.Abs(os.ExpandEnv(...))`
- 否则 `os.UserHomeDir()+"/.hypomux"`，再兜底 `UserConfigDir/HypoMux`、`TempDir/HypoMux`

Hyper-V 台账复用同一解析：`desktop/internal/services/hyperv_adapter.go:227-237`
`hypervDirectory()` / `hypervLedgerPath()` / `hypervJobDirectory()` = `settingsDirectory()/hyperv`（`adapters.json` + `jobs/`）。
日志：`desktop/internal/services/support_log.go:56` → `filepath.Join(settingsDirectory(), "logs", "app.log")`。

**本机真实数据目录 = `%USERPROFILE%\Desktop\HypoMux-Portable-2.7.0-vnic-preview\data`**
（便携包在桌面根目录，**不是** `dist\portable\`；后者是 CI 构建产物副本，两者 exe 同名，容易混淆。）

目录内容（0:20 首次启动后）：

| 路径 | 状态 |
| --- | --- |
| `settings.json` | 有（`mode=proxy`, `language=zh`, `auto_start_engine=false`, `system_proxy_takeover=false`, socks 10800 / http 10801） |
| `appearance.json`、`proxy-settings.lock` | 有 |
| `hyperv/jobs/` | **空目录** |
| `hyperv/adapters.json` | **无**（首启、未创建过网卡，正常） |
| `logs/` | **无** |

### 1.2 原始错误原文 —— **拿不到，不编造**

这一项**没有取到**，如实报告：

- 本次 `fc821b1` 便携版的 data 目录**根本没有 `logs/` 目录**，即该进程一条 support log 都没写。
- `%USERPROFILE%\.hypomux\logs\app.log`（969 KB / 3632 行）是**上一版 `d6fb799` 便携/安装版**留下的旧日志，最后写入 `2026/10/4 00:02:45`；日志内 `core_commit` 字段为 `d6fb799c...`、`core_version: 2.7.0`。其 `category` 只有 `engine` / `engine_start` / `steam_cdn` / `tun_*`，**完全没有 hyperv / switch / virtual_adapter 记录**；在全文搜 `hyperv_adapter|vnic-hyperv|switches|virtual_adapter` **0 命中**。
- 根因之一（可验证）：后端 Hyper-V 路径**从不写 support log**，所以即使进程跑了很久也留不下证据。

→ 因此**故障时刻的真实 Go 错误文本无法从日志复原**。这也是本次前端修复的直接动机之一：真实原因只存在于一次性的 Promise rejection 里。

### 1.3 进程：路径与提权

`Get-CimInstance Win32_Process`：

| PID | 进程 | Exe |
| --- | --- | --- |
| 7208 | `hypomux.exe` | `%USERPROFILE%\Desktop\HypoMux-Portable-2.7.0-vnic-preview\hypomux.exe` |
| 10476 | `cmd.exe`（父） | `/C "…\启动便携版.cmd"` —— 经启动器启动，脚本把 `HYPOMUX_DATA_DIR` 设为 `%ROOT%\data` |
| 10856 | `hypomux-engine.exe` | `…\bin\hypomux-engine.exe` |

提权判定用 `TokenInformation`/`TokenIntegrityLevel` 读 RID，**不是猜的**：

- PID 7208 → RID `0x3000` = **High integrity**
- PID 10856 → High；父 `cmd.exe` 10476 → High

**结论：App 是提权运行的。** 注意本 pwsh 会话基线同样是 High，所以"没看到 UAC 弹窗"与"进程已提权"并不矛盾：父 `cmd.exe` 本身已提权时，用 `exec` 直启子进程**不会再弹 UAC**。所以"没弹框"不能作为"没提权"的证据。

### 1.4 本机 Hyper-V 只读复核（与文案假设矛盾）

- `Get-VMSwitch` → `Default Switch`(Internal)、`XuniUplink`(External, Realtek Gaming 2.5GbE)
- `powershell.exe` = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`（存在）
- `Get-Module -ListAvailable Hyper-V` → count = 2

即通用文案暗示的"Hyper-V 没装 / 没提权"**两个假设都不成立**——这正是只显示通用文案的代价。

### 1.5 前端在哪里吞掉了真实原因（核心取证结论）

调用链（只读理解，未改后端）：
`(*HyperVAdapterService).Switches()` `desktop/internal/services/hyperv_adapter.go:1096-1108` → 失败时
`return nil, hypervErrorf(hypervCodeUnavailable, "无法读取 Hyper-V 虚拟交换机：%v", err.Error())`
→ `runScript` `hyperv_adapter.go:931-964` → 非提权走 `hypervExecuteUnelevated`（`hyperv_adapter_windows.go:62-64`）→ `hypervRunDirect`（`:80-104`，其中 `if code := exitErr.ExitCode(); code != 0 { return fmt.Errorf("PowerShell 以退出码 %d 结束", code) }`）；命令由 `hypervPowerShellCommand`（`:185-198`）以 `-EncodedCommand`（base64 UTF-16LE）+ payload base64 组装，powershell 路径取自 `hypervPowerShellPath`（`:202-212`）；结果文件 `newHypervJobPath`（`:599-605`）建在 `hyperv/jobs/`，`defer os.Remove`。

**前端丢弃点（修复前）**：

| file:line | 内容 |
| --- | --- |
| `desktop/frontend/src/pages/VirtualAdaptersPage.tsx:105-106` | `const errorText = (error: unknown): string => error instanceof Error ? error.message : String(error);` |
| `desktop/frontend/src/pages/VirtualAdaptersPage.tsx:128`（修复前） | `const [switchesFailed, setSwitchesFailed] = useState(false);` —— **只有 boolean，没有任何 state 保存 `switchResult.reason`** |
| `desktop/frontend/src/pages/VirtualAdaptersPage.tsx:188-195`（修复前） | `else { setSwitches([]); setSwitchesFailed(true); }` —— **`switchResult.reason` 在这里被彻底丢弃**：不存 state、不 notify、不传给面板 |
| `desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx:201-208`（修复前） | `{switchesFailed && (<MessageBar intent="warning"><MessageBarBody>{t("virtual_adapters_switch_failed")}<Button …/></MessageBarBody></MessageBar>)}` —— 只输出泛泛文案 |

对照组（说明这**不是**全项目的通病，而是 switch 分支漏了）：同函数 `listResult` 失败分支 `VirtualAdaptersPage.tsx:205-213` 会
`setUnavailableReason(errorText(listResult.reason))` 并
`notifyOnce("virtual-adapters:error:list", { title: tRef.current("virtual_adapters_state_failed"), message: errorText(listResult.reason), intent: "error" })`。
**switch 分支此前连通知都没有**，所以真实原因在界面和通知中心同时消失。

---

## 2. 修法

### 2.1 页面：`desktop/frontend/src/pages/VirtualAdaptersPage.tsx`

- `:105-122` 新增 `ERROR_DETAIL_MAX = 300`（`:109`）与 `errorDetail(error)`（`:120-123`）：基于既有 `errorText()`，把连续空白折叠成单空格并 trim，超过 300 字符才截尾加 `…`。Go 侧是 `hypervErrorf(..., "无法读取 Hyper-V 虚拟交换机：%v", err.Error())`，**原因在句首**，所以截尾不丢核心原因，也不会把整段堆栈糊上去。
- `:146-148` 新增 state `switchesError: string | null`（与既有 `switchesFailed` boolean 并存）。
- `:208-224` switch 分支：成功时 `setSwitchesError(null)`（`:211`）；失败时 `const detail = errorDetail(switchResult.reason)` → `setSwitchesError(detail)`（`:219`）+ 补一条 error 通知
  `notifyOnce("virtual-adapters:error:switches", { title: tRef.current("virtual_adapters_switch_failed"), message: detail, intent: "error" })`（`:220-224`）
  （沿用既有 `notifyOnce` 去重 latch：每次 mount 只弹一次，不会被 3s 轮询刷屏。）
- `:385` 传 `switchesError={switchesError}` 给面板。

### 2.2 面板：`desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx`

- `:60-66` 新增可选 prop `switchesError?: string | null`（沿用同文件 `unavailableReason?: string | null` 的既有写法）；`:111` 加入解构。
- `:205-219` 在**既有 warning 条内部**、通用文案之后、retry 按钮之前，加
  `{switchesError && <span className="virtual-adapter-error">{switchesError}</span>}`（`:216`）
  通用提示与真实原因**同时出现、互不替代**。

### 2.3 i18n：**没有新增任何 key**

- 通用提示仍走既有 `t("virtual_adapters_switch_failed")`；通知标题复用同一个既有 key（语义即"读取交换机列表失败"）；`virtual_adapters_state_failed` 属于"创建失败"，用于 switch 读取失败语义不对，故未复用。
- 真实原因是 **Go 返回的运行时数据**，不是界面散文，**不经过 `t()`、不翻译、也不硬编码任何中英文本**。这就是为什么用「独立元素直出原文」而不是 `t(key, {reason})` 模板：既避免新增 key，也让"真实原因出现在 DOM 里"可被测试断言。
- 样式复用既有 `.virtual-adapter-error`（`vnic.css:11`，12px / `--hm-danger` / `overflow-wrap: anywhere`），**未改 CSS**。
- `desktop/frontend/src/i18n/legacy.messages.json` **未改动**。

### 2.4 既有行为保持不变（逐条核对）

| 行为 | 状态 |
| --- | --- |
| `switches()` 失败仍禁用「批量创建」 | 保持 —— `createDisabled = busy \|\| preview \|\| !selectable.length \|\| !count.ok`（`HyperVAdapterPanel.tsx:134`），未触碰 |
| 失败时**不**显示 `_no_switch` / `_switch_external_only` | 保持 —— `switchNotice = !loadFailed && !switchesFailed && (…)`（`HyperVAdapterPanel.tsx:161`），未触碰 |
| 读失败时仍渲染网卡表格 | 保持 —— `Promise.allSettled` 分支未改结构 |
| 通知每次 mount 只弹一次 | 保持 —— 复用既有 `notifyOnce` latch |
| 读取恢复后原因随之消失 | 新增 —— `:211` 成功分支 `setSwitchesError(null)` |

### 2.5 补的测试

`desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx`（+38 行）

1. **「shows the switches() cause next to the generic hint, and notifies with it」** —— 断言 `virtual_adapters_switch_failed` 与真实原因文本 `无法读取 Hyper-V 虚拟交换机：PowerShell 以退出码 1 结束` **同时出现在界面**；断言 `notify` 收到 `{ dedupeKey: "virtual-adapters:error:switches", intent: "error", message: <真实原因> }`；并再次钉死"不显示 no_switch / switch_external_only + create 仍 disabled"。
2. 「drops the switches() cause again once the read recovers」—— 失败→retry 成功，原因与提示条一并消失（防陈旧残留）。

`desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx`（+15 行）

3. 「shows the service's cause alongside the generic hint」—— 面板层同样钉死两行共存。
4. 「renders the hint alone when no cause was captured」—— `switchesError` 为 null 时不能崩、提示仍在。

---

## 3. 门禁真实退出码

workdir `desktop\frontend`，2026-10-04：

| 命令 | 退出码 | 结果 |
| --- | --- | --- |
| `.\node_modules\.bin\tsc.cmd --noEmit` | **0** | 无错误 |
| `.\node_modules\.bin\vitest.cmd run` | **0** | **45 files / 305 tests 全绿**（含受影响两套：HyperVAdapterPanel 26、VirtualAdaptersPage 20） |
| `.\node_modules\.bin\vite.cmd build` | **0** | `✓ built in 608ms` |

改动文件（`git diff --stat`，仅前端 4 个，均在写范围内）：

```
 desktop/frontend/src/components/vnic/HyperVAdapterPanel.test.tsx  | 15 +
 desktop/frontend/src/components/vnic/HyperVAdapterPanel.tsx       | 14 +-
 desktop/frontend/src/pages/VirtualAdaptersPage.test.tsx          | 38 +
 desktop/frontend/src/pages/VirtualAdaptersPage.tsx               | 33 +-
 4 files changed, 98 insertions(+), 2 deletions(-)
```

未 `git add` / `git commit`。`desktop/internal/services/**` 的改动是队友的，与本次无关。
全程只读查询系统（`Get-VMSwitch` / `Get-Process` / `Get-CimInstance` / 注册表读取完整性级别），**未创建、删除或修改任何网卡 / 交换机 / 路由 / 系统网络设置**。

---

## 4. 给后端队友的交叉验证要点

1. 本机 Hyper-V 完全正常且 App **已提权**（RID `0x3000`）——通用文案的"没装 / 没提权"两个假设都不是原因，请不要按它们排查。
2. 故障**无日志**：本次进程 data 目录下没有 `logs/`，且 Hyper-V 路径从不写 support log。若要留证据，需要在后端失败分支补写（属后端写范围，本轮未做）。
3. 用户可见的真实原因目前只会以这种形态出现：`无法读取 Hyper-V 虚拟交换机：<内层 err>`；内层最可能是 `hypervRunDirect` 里的 `PowerShell 以退出码 %d 结束`。修复后这条会**原样**显示在警告条和通知里，可直接用来验证修复效果。