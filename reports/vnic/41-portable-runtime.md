# 便携运行路径验证报告（T-P2 / task-20）

- **任务**：task-20「T-P2 便携运行路径静态+只读验证」——配合 T-P1 便携包，代码级/只读核查「便携版真的会用我们自己的引擎、真的不碰已安装版与系统状态」
- **执行者**：`integration-verifier`（teammate）
- **对应源码**：`37571e5`（`feat: replace the AI assistant with a virtual adapter action on home`）
- **方法**：只读代码审阅 + `git grep` + 只读 PowerShell（`Get-Service` / `Get-PnpDevice` / `Get-NetAdapter` / `Get-ItemProperty` 读注册表 / `Test-Path`）。**未启动便携版、未改服务/代理/路由/适配器**；唯一写过的文件是本报告。
- **验证日期**：2026-10-03（本机 = 已安装官方 2.7.0 + 1 块在用的 `HypoMux-Tun` 的机器）
- **证据基准**：`reports/vnic/00-frozen-interface.md`（接口冻结）+ 本报告逐条 `相对路径:行号`

> 本报告**不引用任何队友结论作为证据**；队友产物只用于对齐包布局（`reports/vnic/40-portable-package.md`，已在文末标注为「仅对齐」）。

---

## §0 结论速览

### 0.1 用户最关心的一句话

**便携包能验证「创建虚拟网卡」，但必须同时满足三个前提**，缺一条就会失败或根本看不到便携版窗口：

1. **用 `启动便携版.cmd` 以管理员身份启动**（`HYPOMUX_DATA_DIR` / `HYPOMUX_ENGINE_PATH` 由脚本设置，`启动便携版.cmd:45-46`）；
2. **`HypoMuxCore` 服务已停止**（脚本会 `sc stop`，`启动便携版.cmd:161-191`）——**这一条是必需项，不是"保险"**（理由见 §1.2 情形 C）；
3. **已安装的 2.7.0 已完全退出**（托盘退出）——否则便携版进程会**静默退出**，用户看到的是安装版窗口（§3）。

### 0.2 Q1–Q7 判定表

| 问题 | 判定 | 关键证据 |
|---|---|---|
| Q1 引擎选择 | 服务 RUNNING 且**当前无既有提权会话**时，`EnsureElevated` 必然连到**股票版引擎** ⇒ `vnic.create` 不可用；服务停止/未安装时回退到便携引擎，`SameFile` 校验在既定布局下**放行** | `launcher.go:73-98`、`client.go:203-224`、`service_windows.go:73-91` |
| Q2 提权路径 | 非提权 + 服务已停：`runas` 弹 UAC，启动的是 **`HYPOMUX_ENGINE_PATH` 指向的便携引擎**（**无任何 Program Files 硬编码**），参数 `serve-pipe --pipe … --session-token … --host-pid …` | `privileged_windows.go:89-96,312-355`、`client.go:557-598` |
| Q3 单实例 | **会阻止并存**：同 `UniqueID` 的第二个实例 `os.Exit(0)` 静默退出，并把参数 WM_COPYDATA 发给先启动者 | `main.go:84-96`、wails `application.go:182-196` |
| Q4 数据隔离 | `settingsDirectory()` 派生的**全部**读写点都落在 `HYPOMUX_DATA_DIR` 内；data 目录外仅有 4 类写入（ProgramData 临时配置、注册表系统代理、HKCU Run、网络栈本身） | §4 表 |
| Q5 种子配置 | 9 键种子**可被正常加载、不会被拒、不会被回写、不触发 legacy 迁移**；`selected_adapter_ids` 启动期**不会被覆盖** | `settings.go:461-524`、`:352-358/:437/:407-414` |
| Q6 副作用面 | `system_proxy_takeover=false` 时启动**不碰**系统代理；**启动期不写 HKCU Run**；但用户在便携版 UI 里动「开机自启」**会改写/删除安装版的 Run 值** | `engine.go:195,610,905,909`、`autostart_windows.go:36-71` |
| Q7 端口/TUN 冲突 | proxy 模式并存 ⇒ `bind` 失败，UI 只显示原始英文 errno（**无友好预检**）；TUN 模式并存 ⇒ 同名适配器争用，且清理逻辑在本机条件下形同虚设 | `engine.go:848-860`、`supervisor.go:36-41`、`cleanup_windows.go:68,105-140` |

### 0.3 风险分级

| 级别 | 事项 |
|---|---|
| **严重** | ① 便携版与安装版**同名单实例**，用户极易误以为在用便携版，实际点的是安装版的按钮（§3.3）；② 服务在跑且无既有提权会话时，`vnic.create` 打到股票引擎 ⇒ 只报「不支持」（§1.2-C） |
| **一般** | ③ 便携版 UI 的「开机自启」开关会覆盖/删除安装版的 `HKCU\...\Run\HypoMux`（§6.3）；④ `%ProgramData%\HypoMuxCoreRuntime` 临时配置由便携版每次 TUN/VNIC 启动写入（§4.3）；⑤ TUN 模式两版并存必冲突（§7.3） |
| **提示** | ⑥ 端口冲突无友好文案（§7.1）；⑦ TUN 清理快路径在本机条件下恒为「无自有设备」（§7.3）；⑧ 更新器会把官方安装包装回系统（显式动作，§6.4） |

---

## §1 Q1 引擎选择优先级

### 1.1 代码事实

- `desktop/internal/engineclient/launcher.go:73-98`（`serviceFirstLauncher.Launch`）：
  - `:74-77` 先试 `launcher.service.Launch`，**成功即返回**；
  - `:78-80` 错误不是 `ErrCoreServiceUnavailable`（`launcher.go:13-14`「HypoMux Core Service 未安装」）也不是 `ErrCoreServiceNotRunning`（`:15-16`「已安装但未运行」）⇒ 直接失败返回；
  - `:81-85` **只有** `ErrCoreServiceNotRunning` 才会跑 `allowAutomaticFallbackPath(path)` 路径守卫，失败即 `"%w；自动兼容启动已取消：%v"`；
  - `:86-96` 回退 `launcher.fallback.Launch`，并给会话打 `fallback = "service_unavailable"`（服务未安装）或 `"service_not_running"`。
  - **注意非对称**：服务**未安装**时**不做**任何路径校验就直接回退（`:81` 的条件只在 NotRunning 成立）。
- `desktop/internal/engineclient/privileged_windows.go:89-96` `newPrivilegedLauncher()` = `serviceFirstLauncher{service: windowsServiceLauncher{}, fallback: privilegedLauncher{}, allowAutomaticFallbackPath: allowAutomaticCoreFallbackPath, allowPostHandshakeFallback: allowServicePostHandshakeFallback}`。
- `desktop/internal/engineclient/service_windows.go:73-91` `allowAutomaticCoreFallbackPath(path)`：`expected := filepath.Join(filepath.Dir(os.Executable()), "bin", "hypomux-engine.exe")`，`expected` 与 `path` 都必须存在且为常规文件且 `os.SameFile` 为真；否则 `待启动 Core 不是当前 HypoMux 安装目录中的 bin\hypomux-engine.exe`；`os.Stat(expected)` 失败 ⇒ `正式安装目录中的 Core 不可用：%w`；`os.Stat(path)` 失败 ⇒ `待启动 Core 不可用：%w`。
- `desktop/internal/engineclient/client.go:116-118`：`New()` = `newClient(stdioLauncher{}, newPrivilegedLauncher())` ⇒ **`normalLauncher` 就是 stdioLauncher（永不连服务）**，只有 `requireElevated` 才用服务优先的 `elevatedLauncher`。
- `desktop/internal/engineclient/client.go:203-209`：已有会话且 `hello.ProtocolVersion == ProtocolVersion && (!requireElevated || hello.Elevated)` ⇒ **直接复用**（不连服务管道）；`:210-212` 否则 `killCurrent(errors.New("正在切换聚合核心权限级别"))`。
- `desktop/internal/services/engine.go:760-766`：`if mode == "tun" { s.client.EnsureElevated(ctx) } else { s.client.Ensure(ctx) }`。
- `desktop/internal/services/virtual_adapter.go:62,107-108`：`Create` 用 `readyClient()`（**只返回已存在的 client，不建立会话**，`:166-177`），`Status`/`Remove` 用 `connectedClient()`（无会话即降级 absent，`:180-188`）⇒ **主页挂载时的 `status()` 轮询不会拉起任何核心**。
- `:75-77`：仅当**已有会话**（`hello.ProtocolVersion != 0`）却缺 `vnic.create` 能力时，**先**返回 `errVNICUnsupported`（不重启核心）；`:81` 才 `EnsureElevated`；`:85-87` 提权后仍缺能力 ⇒ 同一错误。
- `desktop/internal/services/virtual_adapter.go:27` `errVNICUnsupported = errors.New("当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试")`。

### 1.2 三种情形（确定结论）

前提记法：便携布局 `<portable>\hypomux.exe` + `<portable>\bin\hypomux-engine.exe`，脚本设 `HYPOMUX_ENGINE_PATH=<portable>\bin\hypomux-engine.exe`（`启动便携版.cmd:46`）；`ResolveExecutable()` 优先取该环境变量（`client.go:557-598`）。

**A. 服务已安装且 RUNNING，且**（管理员启动的便携版）**当前已有提权的 stdio 会话**
⇒ `EnsureElevated` 命中 `client.go:203-209` 提前返回 ⇒ **用便携引擎，不连服务**。会话来源：用户先启动过 **proxy 模式**（`engine.go:763` 走 `Ensure` → `normalLauncher = stdioLauncher`）；由于便携版整体是管理员进程树的子进程，该 stdio 子进程的令牌也是提权的（`engine/internal/platform/identity_windows.go:12-17` 读的正是本进程令牌；`desktop/build/windows/wails.exe.manifest:18` 的 `asInvoker` 只影响"是否自动请求提权"，不影响继承）。

**B. 服务已安装且 RUNNING，且当前无既有会话**（典型：管理员启动便携版后**第一件事**就点「创建虚拟网卡」）
⇒ `Create` → `Hello().ProtocolVersion == 0` ⇒ 跳过 `:75-77` ⇒ `EnsureElevated` → `ensure(requireElevated=true)` → 无会话 ⇒ `elevatedLauncher` = `newPrivilegedLauncher()` ⇒ `serviceFirstLauncher` → **服务 RUNNING ⇒ `windowsServiceLauncher` 连 `\\.\pipe\HypoMux-Core-Service`（`service_windows.go:19-20,41-71`）⇒ 用的是股票版引擎** ⇒ `:85-87` 返回 `errVNICUnsupported`。
⇒ **所以"服务在跑时是否 100% 连股票版"的准确答案是：不是 100%（A 情形不会），但在"刚到主页就点按钮"的默认状态下会。** UI 表现见 §5.5。
⇒ 这正是 `启动便携版.cmd:108-191` 的 `sc stop HypoMuxCore` **必须是必需步骤**的原因（脚本注释把它写成"保险措施"，措辞偏轻）。

**C. 服务已安装但已 STOPPED**
⇒ `windowsServiceLauncher.Launch` 返回 `ErrCoreServiceNotRunning`（`service_windows.go:38-45`：`pid == 0`）⇒ `:81-85` 跑 `allowAutomaticCoreFallbackPath(path)`：`os.Executable()` = `<portable>\hypomux.exe`，`expected` = `<portable>\bin\hypomux-engine.exe`，`path` = `HYPOMUX_ENGINE_PATH` = 同一文件 ⇒ **`os.SameFile` 通过 ⇒ 放行** ⇒ 走 `privilegedLauncher`（Q2）。

**D. 服务完全未安装**
⇒ `ErrCoreServiceUnavailable`（`service_windows.go:32-37`）⇒ `launcher.go:81` 的条件不成立 ⇒ **不做路径校验直接回退** `privilegedLauncher` ⇒ 同样启动便携引擎（**比 C 更宽松**）。

> 反例提醒（守卫的真实作用）：若把 `HYPOMUX_ENGINE_PATH` 指到**别的目录**（例如 `C:\Program Files\HypoMux\bin\hypomux-engine.exe`），在**情形 C** 下会被这条守卫拒绝并报 `待启动 Core 不是当前 HypoMux 安装目录中的 bin\hypomux-engine.exe`；在情形 D 下则**不会**被拒。

---

## §2 Q2 提权路径（非提权启动 + 服务已停）

1. 面板 `create()` → `appServices.virtualAdapter.create()`（`desktop/frontend/src/platform/services.ts:350-353`）→ `VirtualAdapterService.Create`。
2. `virtual_adapter.go:69` 先写 vnic keeper 配置到 `<data>\vnic\`（`vnic_config.go:93`），`plan.Executable` = 引擎可执行路径。
3. `virtual_adapter.go:81 client.EnsureElevated(ctx)` → `client.go:188-190 ensure(ctx, true)`。
4. `client.go:215 ResolveExecutable()` ⇒ 候选顺序（`client.go:557-598`）：① `os.ExpandEnv(os.Getenv("HYPOMUX_ENGINE_PATH"))` ② `filepath.Dir(os.Executable())/bin/hypomux-engine.exe` ③ `Dir(exe)/hypomux-engine.exe` ④ 当前目录起的 6 层 `{exe, bin\, dist\}`；取第一个存在的常规文件；全失败 ⇒「未找到 hypomux-engine.exe；可设置 HYPOMUX_ENGINE_PATH」。
5. `client.go:220-223` `requireElevated` ⇒ `newPrivilegedLauncher()`（`privileged_windows.go:89-96`）⇒ 服务已停 ⇒ `privilegedLauncher.Launch`（`:102-131`）。
6. `privileged_windows.go:133-173 createAuthenticatedPipe`：32 字节随机 token；管道名 `\\.\pipe\HypoMux-Core-<token[:24]>`；SDDL `D:P(A;;GA;;;<当前用户SID>)(A;;GA;;;BA)(A;;GA;;;SY)`；`PIPE_REJECT_REMOTE_CLIENTS`。
7. **`privileged_windows.go:312-355 launchElevatedCore`**（决定性证据）：
   - `verb = "runas"`（经 `shell32.dll!ShellExecuteExW`）⇒ **是否弹 UAC**：调用进程**未**提权 ⇒ 弹 UAC；调用进程**已**提权（管理员 cmd 启动便携版）⇒ 同用户同令牌，Windows 通常不再弹窗（**OS 行为，未在真机实测**，标记为未验证）。
   - `file = path` ——**就是 `ResolveExecutable()` 的返回值**，即 `HYPOMUX_ENGINE_PATH` 指向的便携引擎；**全仓 `privileged_windows.go` 内没有任何 `Program Files` / `HypoMux` 硬编码路径**（已 `grep` 复核）⇒ **便携版永远不会去启动安装版的引擎**。
   - `parameters = "serve-pipe --pipe " + pipeName + " --session-token " + token + " --host-pid " + strconv.Itoa(os.Getpid())`。
   - `Directory = filepathDir(path)`（`:357-366`，取最后一个 `\`/`/` 之前，否则 `"."`）；`Show = 0`；`Mask = seeMaskNoCloseProcess(0x40)`。
   - 失败文案：`ERROR_CANCELLED` ⇒ `ErrElevationCancelled`「用户取消了管理员权限请求」(`:41`)；「启动管理员核心失败：%w」；「管理员核心未返回进程句柄」；「读取管理员核心进程 ID：%w」。
8. `privileged_windows.go:175-198 accept`：验证客户端 PID == 期望 PID，否则「拒绝非预期核心进程（PID %d）」；`:243-301 authenticateCore` 读一行 JSON `{protocol,kind,token}`（`sessionProtocol=1`、`sessionAuthKind="hypomux.core.authenticate"`、`sessionReadyKind="hypomux.host.ready"`，超时 `sessionAuthTimeout=30s`），不匹配 ⇒ 回写 `{ok:false,error:"authentication_failed"}` 并报「核心一次性会话凭据不匹配」。
9. 失败时用户看到的串：`virtual_adapter.go:83`「创建虚拟网卡需要管理员权限的独立聚合核心：%w」/ `:99`「创建虚拟网卡失败：%w」；握手后回退失败 `client.go:240-265`「%v；UAC 兼容启动失败：%w」；面板 `notifyErrorRef.current(String(error), create)`（`VirtualAdapterPanel.tsx:176-178`）⇒ 红色 toast 原文 + 重试。

**结论**：便携版点「创建虚拟网卡」→ 提权路径**只可能**启动便携引擎（前提：服务已停，见 Q1-B 的反例），UAC 参数与安装版完全一致，不需要任何 Program Files 资源。

---

## §3 Q3 单实例

### 3.1 实现链（`desktop/main.go:84-96`）

```go
SingleInstance: application.SingleInstanceOptions{
    UniqueID: "io.hypomux.desktop",
    OnSecondInstanceLaunch: func(data application.SecondInstanceData) { ... },
},
```

- Wails v3 `pkg/application/single_instance_windows.go:34-54 acquire()`：互斥体名 `id := "wails-app-" + uniqueID`，用 `CreateMutex(nil,false, id+"-sim")`；`err != nil`（`ERROR_ALREADY_EXISTS`）⇒ 返回 `alreadyRunningError`（此时窗口尚未创建）。
- `golang.org/x/sys v0.47.0`（`desktop/go.mod:11`、`engine/go.mod:10`）的 `windows/zsyscall_windows.go` CreateMutex 包装为 `handle = Handle(r0); if handle == 0 || e1 == ERROR_ALREADY_EXISTS { err = errnoErr(e1) }` ⇒ **确实能检出已在运行的实例**。
- `pkg/application/application.go:182-196`：`errors.Is(err, alreadyRunningError) && manager != nil` ⇒ `manager.notifyFirstInstance()` 然后 `os.Exit(appOptions.SingleInstance.ExitCode)`；`desktop/main.go:84-96` **未设置 `ExitCode`** ⇒ 默认 0。
- `pkg/application/single_instance.go:177-208 notifyFirstInstance`：`SecondInstanceData{Args, WorkingDir, AdditionalData}` 序列化后经 `WM_COPYDATA`（`single_instance_windows.go:67-79` `FindWindowW` + `SendMessageToWindow`）发给先启动者。
- 先启动者的回调（`main.go:84-96`）：`for _, a := range data.Args { if a == "--silent" { return } }`，否则 `mainWindow.Show().Focus()`。

### 3.2 确定结论

- **便携版与安装版共用同一个 `UniqueID` ⇒ 不能并存。**
- 第二个实例（无论哪个版本）：**建窗口之前就 `os.Exit(0)` 静默退出**，没有气泡提示、没有日志窗口。
- 先启动者：收到的参数**不含** `--silent` ⇒ 窗口被 `Show().Focus()` 唤到前台；**含** `--silent` ⇒ 回调直接 return（不打扰）。
- 安装版通过 `HKCU\...\Run\HypoMux = "<安装路径>\hypomux.exe" --silent`（本机实测值）自启时用的就是 `--silent` 分支。

### 3.3 用户可见的坑（严重）

先开安装版 2.7.0 → 再运行 `启动便携版.cmd`：脚本会打印"已启动"，但 `hypomux.exe` 秒退；**用户看到并操作的窗口其实是安装版**（股票引擎）⇒ 点「创建虚拟网卡」只会得到「不支持」，用户会误判"便携版的虚拟网卡功能坏了"。
**缓解**：先彻底退出另一个版本；或启动后用「设置 → 关于」核对版本/路径（本报告不假设 UI 有该入口）。

---

## §4 Q4 数据隔离

### 4.1 `HYPOMUX_DATA_DIR` 的解析

`desktop/internal/services/settings.go:136-150 settingsDirectory()`：`os.Getenv("HYPOMUX_DATA_DIR")` → `filepath.Abs(os.ExpandEnv(...))`；否则 `os.UserHomeDir()/.hypomux`（再回退 `UserConfigDir()/HypoMux`、`TempDir()/HypoMux`）。脚本设的是 `%ROOT%\data`（`启动便携版.cmd:45`）⇒ **绝对路径**，与进程 cwd 无关（`filepath.Abs` 用进程 cwd，但这里是绝对路径）。

### 4.2 数据目录内的全部落盘点（= `settingsDirectory()` 的全部调用方，已逐个 grep 复核）

| 文件/目录 | 证据 |
|---|---|
| `settings.json` | `settings.go:103` |
| `settings.before-legacy-migration.json` | `settings.go:193`（仅显式迁移时） |
| `appearance.json`、`appearance/<图片>` | `appearance.go:48,94,121,136,189` |
| `blocked_domains.json` | `blocked_domains.go:39` |
| `hotspot.dat` | `hotspot_preferences.go:15,62` |
| `nat_servers.json` | `nat_servers.go:62` |
| `runtime/rule-sets/*` | `singbox_rules.go:77` |
| `logs/app.log` | `support_log.go:56` |
| `proxy-owned`（系统代理恢复快照） | `system_proxy_windows.go:37` |
| `proxy-settings.lock` | `system_proxy_lock_windows.go:20` |
| `cache/sing-box.db` | `tun_config.go:241` |
| `runtime/sing-box.json` | `tun_config.go:277` |
| `vnic/sing-box-vnic.json`（keeper 配置） | `vnic_config.go:93` |
| `mtu-originals.json` | `mtu.go:40`（取自 `settings.ConfigPath()` 的目录） |

⇒ **未发现任何写死 `%USERPROFILE%\.hypomux` 的写入点**：唯一硬编码该目录的是 `legacyConfigPath()`（`settings.go:586-592`），且：
- 只在 `reload()` 发现 `<data>\settings.json` **不存在**时才读（`:465-476`），`inspectLegacyConfig()`（`:570-584`）只 `os.Stat`；
- 读的是 `config.json`（旧版格式），**不是**官方 2.7.0 现在使用的 `.hypomux\settings.json`；
- 本机实测 `%USERPROFILE%\.hypomux` 下**没有** `config.json` ⇒ 便携版不会触发任何迁移；即便存在，也只是**读**它并在便携 data 目录里写 `settings.json`（`:474-486`）。
- ⚠️ 但由此得出一条**必须写进限制清单**的行为：**若便携包不带种子 `data\settings.json`，而目标机器上恰好存在 `%USERPROFILE%\.hypomux\config.json`（HypoMux 2.5 之前的旧配置），便携版首次启动会自动把它导入便携 data 目录**（读旧文件、写新目录；旧文件不改）。

### 4.3 数据目录**之外**的写入点（已全仓排查）

| # | 目标 | 代码 | 触发条件（何时发生） |
|---|---|---|---|
| 1 | `%ProgramData%\HypoMuxCoreRuntime\tun-config-*.json` | `engine/internal/tun/config_stage_windows.go:20-40,42-80,96-102`（SDDL `:18`） | **引擎已提权且 `ConfigSHA256 != ""`**（主 TUN：`desktop/internal/services/engine.go:947`；VNIC keeper：`desktop/internal/services/virtual_adapter.go:91`）⇒ **便携版每次开 TUN/建 VNIC 都会在此写一份临时配置**，正常路径由 stage 返回的 remove 闭包删除；进程被强杀时不保证清理。未提权则**原地使用 data 目录里的文件**（`:24-31`）。本机实测该目录现存 2 个旧文件（`tun-config-1388291783.json` 84542B、`tun-config-254382414.json` 101356B，属安装版残留） |
| 2 | `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`（ProxyEnable/ProxyServer/ProxyOverride） | `desktop/internal/services/system_proxy_windows.go:46,118,145` | **仅** `mode=="proxy" && settings.SystemProxyTakeover`（`engine.go:195-196,610,905`）；`:909` 记录 `system_proxy/takeover_skipped` 表示未接管；启动前 `engine.go:232 restoreSystemProxyDetailed()` 会按 `<data>\proxy-owned` 恢复。实测当前 `ProxyEnable=0` |
| 3 | `HKCU\...\Run\HypoMux` + `...\Explorer\StartupApproved\Run\HypoMux` | `desktop/internal/platform/autostart_windows.go:16-19,46,52,59,62,67` | **仅用户在便携版 UI 里动「开机自启」**（`settings.go:360-382` `SetAutostart` / `:384-405` `SetAutoStartEngine`）；**启动期不写**（§6.3） |
| 4 | 网络栈本身：Wintun 适配器、路由、DNS、WFP sublayer/filter、适配器 MTU | sing-box 由引擎驱动；WFP `engine/internal/wfp/dns_exemption_windows.go:183-258`（由 `engine/internal/server/server.go:636-660` 在 strict-route TUN 时安装）；MTU `engine/internal/platform/mtu_windows.go:26` | **用户点「启动」/「创建虚拟网卡」时**——这是产品功能本体，非逃逸 |
| 5 | BFE 服务状态（`Start-Service BFE` 等价） | `engine/internal/platform/wfp_windows.go:34-42` | 用户点「重新检测并修复」（`desktop/internal/services/engine.go:576-596`）且提权且 BFE 处于 Stopped |
| 6 | `%TEMP%\HypoMuxUpdate-*`；安装时写 Program Files/服务 | `desktop/internal/services/updater.go:388`；`updater_windows.go:16-57,59-85` | 用户在「关于」页点检查/下载/安装（`AboutPage.tsx:103,128,134,136`）；**无启动自动检查** |
| 7 | 命名管道 `\\.\pipe\HypoMux-Core-Service`（只连）/ `\\.\pipe\HypoMux-Core-<token>`（自建） | `service_windows.go:19-20`；`privileged_windows.go:133-173` | 提权核心会话 |

**无 HKLM 写入**（运行期）：`compatibility_windows.go:68`、`startup/wifi_windows.go:144`、`nat_firewall_windows.go:66-89`、`platform/webview2_windows.go:19-21` 全是只读查询（已 grep 复核 `registry.*HKLM|LOCAL_MACHINE` 的写调用）。
**不写 `C:\Program Files\HypoMux`**（除更新器安装流程外）；不安装/卸载/停启 `HypoMuxCore` 服务（便携版只 `OpenSCManager`+`OpenService` 查询，`service_windows.go:110-146`；停止/恢复服务由脚本与 `恢复环境.cmd` 显式执行）。

---

## §5 Q5 种子配置是否被尊重

### 5.1 9 键全部是真实字段

`settings.go:23-53` 的 `AppSettings` json tag 里，`mode / language / socks_port / http_port / system_proxy_takeover / auto_start_engine / strategy / selected_adapter_ids / adapter_weights` **全部存在**（`rule_sets`、`dns_adapter_id`、`wfp_compatibility_state`、`routing_match_order`、`auto_connect_wifi`、`update_channel` 为 `omitempty`）。未知键也不会报错（§5.2）。

### 5.2 加载期语义（`settings.go:461-524`）

- `os.ReadFile(<data>\settings.json)` 成功 ⇒ `json.Unmarshal(data, &loaded)`（`:492`）—— **全程没有 `DisallowUnknownFields`** ⇒ 种子多写字段也不会被拒。
- `:496-499` 再 `Unmarshal` 成 `storedFields map[string]json.RawMessage` 只用于"键是否存在"判断，**不影响加载**。
- 只有 4 类归一化：缺 `system_proxy_takeover`/`hide_virtual_adapters` 键 ⇒ 取默认 true（`:503-508`）；`Mode` 不是 `proxy|tun` ⇒ 设为默认 `"tun"`（`:509-511`）；端口为 0 ⇒ 10800/10801（`:512-517`）；dns 三项为空 ⇒ 默认（`:518-526`）。种子里这些键**都给了合法值** ⇒ **一个字段都不会被改**，也就**不存在"被改写回 tun"**（`mode:"tun"` 是合法值；若写 `"proxy"` 同样保留）。
- **加载期不调用 `validateSettings`**（只在 `updateLocked:324` 与 `migrateLegacySettings:625` 调用）⇒ 种子即使有语义问题也**不会导致启动被拒**；但**后续任何一次设置保存都会被校验拒绝**（例如 `auto_start_engine:true` 配 `autostart:false` ⇒ `settings.go:665-723` 报「开机自动启动加速需要先开启开机自启」）。**这条要写进限制清单：种子里不要给 `auto_start_engine:true`。**
- **加载期不写盘**：`reload()`/`NewSettingsService()`（`:102-115`）没有任何 `WriteFile`/`commitLocked` ⇒ **种子文件不会被启动流程重写**。
- **不触发 legacy 迁移**：迁移分支（`:465-486`）只在 `settings.json` **不存在**时进入；有种子就一定不走。

### 5.3 `selected_adapter_ids` 的所有写方（全仓 grep）

| 写方 | 调用方 | 何时发生 |
|---|---|---|
| `settings.go:352-358 updateSelectedAdapters` | `scheduling.go:45 SaveDiagnosticSelection` | 用户在诊断/调度页显式选择网卡 |
| `settings.go:437 updateHomeStrategy` | `engine.go:787`（启动模式与持久化 `mode` 不同）与 `:804`（失败回滚） | **仅当"点击启动的模式 ≠ 种子 mode"** |
| `settings.go:407-414 UpdateHome` | 前端首页保存 | 用户显式操作 |

⇒ **启动流程本身没有任何"自动选卡/自动改写 selected_adapter_ids"逻辑**（已 grep「自动选择 / autoSelect」：仅命中热点与 NAT 页文案）。`HideVirtualAdapters`（默认 `true`，`settings.go:73`）**只影响前端列表展示**（`desktop/frontend/src/state/adapterVisibility.ts:6`），Go 侧匹配与引擎选择完全不受影响；`adapters.go:52` 对 `HypoMux-Tun` 的过滤是硬编码的（`:136`），与 `hide_virtual_adapters` 无关。

**唯一会改写种子 `mode` 的路径**：`engine.go:784-789` —— 当用户点击启动的模式与种子里的 `mode` 不一致时，会 `updateHomeStrategy(mode, Weighted, SelectedAdapterIDs, AdapterWeights, strategy)` 把 `mode/selected_adapter_ids/adapter_weights/strategy` **一起写回** `settings.json`（失败时 `:790-812` 用 `previousMode` 回滚）。**只要用户按种子里的 `mode` 启动，就不会改文件。**

### 5.4 适配器 ID 与"名字匹配不到"

- `desktop/internal/services/adapters.go:83`：`id := item.Name` ⇒ **`AdapterView.ID` 就是 Windows 接口名**（如"以太网"），`SelectedAdapterIDs` 用它做精确（大小写敏感）匹配；`:52` 只过滤 `!FlagUp`、环回、`HypoMux-Tun`（`:136`）；`:80-82` **没有 IPv4 的接口整体不进列表**。
- 若 `["以太网"]` 匹配不到：
  1. **TUN 预检 blocker**：`tun_preflight.go:153-157` `no_adapter`「未选择活动网卡」/「请至少选择一张具有有效 IPv4 地址的活动网卡。」⇒ `engine.go:756-758` 抛出 `TUN 预检阻止启动：<标题>：<细节>`；
  2. **启动前硬校验**：`engine.go:651-653` `len(selected)==0` ⇒ `errors.New("请至少选择一张活动网卡")`（**两种模式都过这一关**：该判断在 `if mode == "tun"` 分支之前）；
  3. **UI**：`useEngineState.ts:506-518` 把 `error.message` 原样交给 `onError` ⇒ 红色错误 toast + 重试按钮（`EngineHero.tsx:75`「启动失败」）；`healthNotice.test.ts:13` 亦断言直接渲染原始错误串。
  ⇒ **引擎不会"静默用别的网卡"**，是明确拒绝 + 提示。

---

## §6 Q6 副作用面

### 6.1 `system_proxy_takeover=false` + proxy 模式：**不碰系统代理**

- `engine.go:195-196 shouldTakeOverSystemProxy(mode, settings) = mode=="proxy" && settings.SystemProxyTakeover`；`:610` 取值。
- 只有为真才 `:905 enableSystemProxy(settings.HTTPPort, settings.SOCKSPort)`；为假走 `:908-912` 只记事件 `system_proxy/takeover_skipped`。
- 反方向还有保护：`engine.go:232` 启动前先 `restoreSystemProxyDetailed()`（读 `<data>\proxy-owned`，不存在即 no-op）。
⇒ 种子里 `system_proxy_takeover:false` 期间，便携版**不会**写 `HKCU\...\Internet Settings`。实测本机该键当前 `ProxyEnable=0`（安装版也没在接管）。

### 6.2 启动期不写 `HKCU Run`

`settings.go:360-382 SetAutostart` / `:384-405 SetAutoStartEngine` 的调用方只有前端设置页动作（wails 绑定），**启动路径（`main.go:192-234 ApplicationStarted`）不调用它们**。读取路径 `AutostartEnabled`（`autostart_windows.go:88-128`）只做 `registry.OpenKey` + `GetStringValue`，并额外要求 Run 值等于**当前进程** `os.Executable()`（`:104-110`）⇒ 便携版读到的 `Autostart` 恒为 `false`（本机 Run 值指向 `C:\Program Files\HypoMux\hypomux.exe`），`Get()`（`settings.go:157-161`）随之强制 `AutoStartEngine=false`。

### 6.3 ⚠️ 风险（一般）：便携版 UI 会改掉安装版的 Run 键

- `autostart_windows.go:52,67`：`SetAutostart(true)` 写 `HKCU\...\Run\HypoMux = "<os.Executable()>" --silent` ⇒ 若用户**在便携版 UI 里打开「开机自启」**，会把安装版写的 `"C:\Program Files\HypoMux\hypomux.exe" --silent` **覆盖成便携包路径**（便携包一旦被移动/删除，开机自启就指向不存在的文件）。
- `autostart_windows.go:46`：`SetAutostart(false)` 直接 `DeleteValue("HypoMux")` ⇒ 若用户**在便携版 UI 里关掉「开机自启」**，会**删掉安装版的开机自启**。
- 两者都只写 **HKCU**（当前用户），不写 HKLM；触发都需要用户显式点开关。**建议**：便携版说明里明确写"不要在便携版里动开机自启开关"，或让便携版禁用该开关。

### 6.4 其他显式动作

- **更新器**：只有「关于」页按钮（`AboutPage.tsx:103,128,134,136`）会下载 `%TEMP%\HypoMuxUpdate-*` 并运行官方安装包（`updater.go:388`、`updater_windows.go:23-57,59-85` 强制安装包位于 `%TEMP%\HypoMuxUpdate-*` 并做 Authenticode 校验）⇒ 会写 Program Files 与服务，属用户显式动作，**建议在便携版说明里提示不要点"安装更新"**。
- **WFP 修复**：`engine.go:576-596` 的「重新检测并修复」在提权下会 `Start-Service BFE` 等价操作（`wfp_windows.go:34-42`）。
- **恢复环境.cmd**：`sc start HypoMuxCore`（用户显式，见 `启动便携版.cmd:147` 的说明文案）。

---

## §7 Q7 端口与 TUN 冲突

### 7.1 proxy 模式并存 ⇒ bind 失败

- 默认端口 `settings.go:66-67`（10800/10801）；`engine/internal/proxy/config.go:14 DefaultSOCKSPort = 10800`。
- 桌面把 `socks_port/http_port` 放进 payload（`engine.go:848-855`）；引擎绑定失败文案 `engine/internal/proxy/server.go:170-182`：`listen SOCKS: %w` / `listen HTTP: %w`（底层 Go net 错误形如 `listen tcp4 127.0.0.1:10800: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.`）。
- 引擎包装：`engine/internal/server/server.go:397-427` ⇒ `start_failed` / `could not start proxy engine`；客户端 `client.go:30-49` 拼成 `code + ": " + message`；桌面 `engine.go:858-860` ⇒「启动聚合核心失败：%w」。
- **UI**：`useEngineState.ts:506-518` 原样展示该串（红色 toast + 重试）。
- **没有任何端口占用预检**（已 grep「占用/端口可用/被占用」，零命中）⇒ 用户会看到一长串英文 errno。**提示级问题，建议后续加预检文案。**

### 7.2 TUN 模式的端口

`engineChannels`（`engine.go:1238-1266`）里**所有通道的 `port` 都是 0**（`nic_ethernet/nic_wifi/aggregation/direct/nic_<名>`），而 `engine/internal/proxy/server.go:195-209 startChannelListeners` 在 port=0 时是自适应分配 ⇒ **TUN 模式不与安装版抢固定端口**。TUN 模式下 clash API 端口也是临时分配：`desktop/internal/services/tun_config.go:329-342 reserveClashAPI()` 用 `net.Listen("tcp4", "127.0.0.1:0")` 取一个空闲端口后**立即关闭**并把该端口写进 `clash_api.external_controller`（`:263-265`）。（提示级：这是"先释放再交接"的写法，理论上存在极小概率的两进程抢同一端口窗口，属设计现状，非本次改动引入。）

### 7.3 TUN 模式并存 ⇒ 同名适配器争用（**必然冲突**）

- 接口名是**编译期常量**：`engine/internal/tun/supervisor.go:36 ManagedInterfaceName = "HypoMux-Tun"`、`:37 VNICInterfaceName = "HypoMux-VNIC"`、`:41 tunInterfaceName = ManagedInterfaceName`；主 TUN 配置写死 `desktop/internal/services/tun_config.go:231 "interface_name": "HypoMux-Tun"` ⇒ 便携版与安装版**没有可配置的命名空间隔离**，两个进程会争用同一块 Wintun 适配器/同一套默认路由。
- **清理逻辑的真实行为**（本报告实测发现的重要事实）：`engine/internal/tun/cleanup_windows.go:68` 的**快路径**先调 `hasOwnedTunDevice()`（`:105-140`：`SetupDiGetClassDevsEx` 枚举 Net 类，要求设备的 `SPDRP_FRIENDLYNAME`（回退 `DEVICEDESC`）**精确等于 `HypoMux-Tun`** 且 instanceID 含 `WINTUN`）；为假则**直接 `return nil`，整段 PowerShell 清理不执行**。
  本机只读实测：3 个 WINTUN 设备的 `FriendlyName` **全是 `sing-tun Tunnel`**（`{1146701F-…}` Present=True / `{ABA9CCE4-…}`、`{8F9E7C53-…}` ghost），而 `Get-NetAdapter -IncludeHidden` 里 `HypoMux-Tun`（ifIndex 51, Up, InterfaceDescription `sing-tun Tunnel`）确实存在 ⇒ **`hasOwnedTunDevice()` 恒为 false ⇒ 清理形同虚设**（这正好解释了 task-16 E2E 中主 TUN 未被误伤，也意味着"HypoMux-Tun 残留"warning 与清理承诺在现状下都不会真正触发）。
- 因此结论分两层：
  - **确定**：不会出现"便携版启动时把安装版正在用的默认路由/设备删掉"的静默破坏（因为快路径直接跳过）；
  - **确定**：但两个版本争用同名适配器 ⇒ 后启动的一方在 `WintunCreateAdapter`/`sing-tun` 建网卡阶段失败，用户只会看到启动失败；
  - **未验证**：第二个 sing-box 面对"同名 Wintun 适配器已存在"时的确切失败文案（需真机双开才能取证，本轮禁止）。
- 另：`engine/internal/tun/recover.go:5-10 Recover(ctx) = cleanupPlatform(ctx)` 同样受该快路径约束。

---

## §8 便携包使用限制清单（交付给用户/Lead 的硬性前提）

1. **必须用 `启动便携版.cmd` 启动**（管理员）。直接双击 `hypomux.exe` 不会设 `HYPOMUX_DATA_DIR`/`HYPOMUX_ENGINE_PATH`（`启动便携版.cmd:151`），会读写用户主目录 `.hypomux`，并可能连到安装版引擎。
2. **启动前必须停 `HypoMuxCore` 服务**（脚本已做；`sc stop` 失败会报错并要求人工执行）。原因：服务 RUNNING 且无既有提权会话时，`EnsureElevated` 会连股票引擎 ⇒「当前聚合核心不支持虚拟网卡」。
3. **另一个版本必须完全退出**（同名单实例 ⇒ 否则便携版静默退出，你操作的是安装版）。
4. **不要在便携版里动「开机自启」开关**（会覆盖/删除安装版的 `HKCU\...\Run\HypoMux`）。
5. **不要在便携版里点「安装更新」**（会把官方安装包装回系统）。
6. **种子 `settings.json` 不要给 `auto_start_engine: true`**（会与 `autostart` 冲突，后续任何设置保存都会被 validate 拒绝）。
7. **`selected_adapter_ids` 必须写本机真实存在的接口名**（如 `"以太网"`，就是 `Get-NetAdapter` 的 Name，大小写敏感），否则启动会被拒并提示"请至少选择一张活动网卡"。
8. **不要与安装版同时开 TUN**（同名 `HypoMux-Tun` 必然争用）；同一时刻只保证一个版本在跑。
9. **结束后用「恢复环境.cmd」** 恢复安装版环境（它会重新 `sc start HypoMuxCore`）。
10. 已知会写到 data 目录之外：`%ProgramData%\HypoMuxCoreRuntime\tun-config-*.json`（提权 TUN/VNIC 配置暂存，正常会自删）。

---

## §9 未验证项（本轮不允许启动便携版/改系统，故无法取证）

| # | 未验证事项 | 为什么 |
|---|---|---|
| 1 | UAC 弹窗**实际**行为（管理员启动便携版时是否真不弹窗；非管理员直启时弹几次） | 需真机交互，属 OS ShellExecuteExW 语义 |
| 2 | 便携版与安装版**双开 TUN** 的确切失败文案/是否静默成功（可能与 Wintun 版本行为相关） | 需真机双开，禁止 |
| 3 | `%ProgramData%\HypoMuxCoreRuntime` 残留在强杀场景下的实际清理结果 | 需运行+强杀 |
| 4 | 「创建虚拟网卡」在便携版下的端到端成功路径（真机是否真能建出 `<data>` 隔离下的 VNIC） | 需启动便携版，禁止；可参考 task-16 的引擎级 E2E（`reports/vnic/21-engine-e2e.md`），但那是**引擎二进制级**、非便携版可执行文件 |
| 5 | 便携启动脚本自身的边界（提权失败分支、中文路径、`%ROOT%` 带空格） | 属 T-P1 的报告范围（`reports/vnic/40-portable-package.md`），本报告只做运行期隔离性核查 |
| 6 | 前端在便携版下读取的 `bindings` 是否与引擎能力匹配 | 需 `wails3 generate bindings` + 运行期，禁止（`desktop/frontend/bindings/` 不存在，由 CI/构建生成） |

---

## 附：证据获取方式（可复现）

- 读代码：`read` / `grep` 工具（非 PowerShell `Get-Content`，避免 PS 5.1 把 UTF-8 中文解码成乱码）。
- 只读系统查询：
  - `Get-Service HypoMuxCore` → Running / Automatic
  - `Get-PnpDevice -Class Net | ? InstanceId -like '*WINTUN*'` → 3 项，FriendlyName 全为 `sing-tun Tunnel`
  - `Get-NetAdapter -IncludeHidden | ? Name -like 'HypoMux*'` → `HypoMux-Tun`（ifIndex 51, Up）
  - `Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name HypoMux` → `"C:\Program Files\HypoMux\hypomux.exe" --silent`
  - `Get-ItemProperty 'HKCU:\...\Internet Settings'` → `ProxyEnable=0`
  - `Test-Path %USERPROFILE%\.hypomux\config.json` → False
- 队友产物（**仅用于对齐包布局，不作证据**）：`reports/vnic/40-portable-package.md`；包内启动脚本原文：`dist/portable/HypoMux-Portable-2.7.0-vnic-preview/启动便携版.cmd`。
