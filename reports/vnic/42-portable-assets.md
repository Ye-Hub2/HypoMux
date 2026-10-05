# task-21 · 便携包用户向启动资产入库（desktop/portable/）

- **任务**：task-21 —— 把便携包的用户向启动资产从 `dist/`（未跟踪、被根 `.gitignore` 的 `/dist/` 忽略）迁入 git 跟踪目录，让 GitHub Actions 能在 CI 里组装便携包。
- **执行者**：`local-pipeline-verifier`
- **写入范围（严格）**：`desktop/portable/**` 与 `reports/vnic/42-portable-assets.md`。**未**触碰 `.github/**`（由 ci-runner 负责）、`.gitattributes`、`.gitignore`，未 commit/push。
- **前提事实**：`HEAD=37571e5a0531c16621cf4ac3e7511ac21135aa00`；工作树在我开工前只有 `?? reports/`。

---

## §0 交付物：三个 ASCII 文件名资产

| 仓库内路径（ASCII） | 包内最终名字（CI 改名后） | 字节 | SHA-256 |
|---|---|---|---|
| `desktop/portable/launch-portable.cmd` | `启动便携版.cmd` | 11,102 | `ADC7B5A1DCA2F248993CB0B2EDE88EC749D473A4E169FFEBCD721C618CE68A3F` |
| `desktop/portable/restore-environment.cmd` | `恢复环境.cmd` | 5,050 | `6751E6D22A2FD8F9DF11A64A342B6B7EB1CC7BF8D853ED2DF1EC882828A036EC` |
| `desktop/portable/PORTABLE-README.txt` | `便携版说明.txt` | 5,626 | `302B7F7F824844B1592173C84DA203E841ADC662F6EC8CE25AE147CE7B878D65` |

`restore-environment.cmd` 是 `dist/.../恢复环境.cmd` 的**逐字节语义复制**（`.NET File.ReadAllText/WriteAllText`，UTF-8 无 BOM）——它的 SHA-256 与 dist 版**完全相同**（`6751E6D2…`），证明复制没有改变任何字节。

`git status --porcelain` 在我完成后的输出：

```
?? desktop/portable/
?? reports/
```

---

## §1 为什么用 ASCII 文件名 + CI 组装契约

仓库里放 ASCII 名、打包时再改成中文，是为了避免中文文件名穿过 git / YAML / 压缩包这几层（不同工具的区域设置会把中文名写坏）。**CI 侧必须遵守以下约定**：

1. **改名映射（唯一权威）**

   | 仓库内 | 组装进包时改名为 |
   |---|---|
   | `desktop/portable/launch-portable.cmd` | `启动便携版.cmd` |
   | `desktop/portable/restore-environment.cmd` | `恢复环境.cmd` |
   | `desktop/portable/PORTABLE-README.txt` | `便携版说明.txt` |

2. **编码约定（必须保持，否则会坏）**
   - 两个 `.cmd`：UTF-8 **无 BOM** + CRLF。带 BOM 时 `cmd.exe` 会把第一行 `@echo off` 读成 `?@echo off` 并报错。
   - `.txt`：UTF-8 **带 BOM** + CRLF（旧版记事本按 GBK 打开会乱码）。
   - **不要**用 PowerShell 5.1 的 `Set-Content -Encoding utf8` / `Out-File -Encoding utf8` 重写这些文件：PS 5.1 的 `utf8` 会**加 BOM**，且在某些区域设置下会把中文改坏。要用
     `[System.IO.File]::WriteAllText($p, $t, [System.Text.UTF8Encoding]::new($false))`（cmd）或 `…::new($true)`（txt）。
   - 复制文件时用 `Copy-Item` 或 `[System.IO.File]::Copy` 逐字节复制即可；**不要**先 `Get-Content` 再 `Set-Content`。

3. **zip 打包约定**：用 `[System.IO.Compression.ZipFile]::CreateFromDirectory($src,$zip,[System.IO.Compression.CompressionLevel]::Optimal,$true,[System.Text.Encoding]::UTF8)`，**不要**用 PS 5.1 的 `Compress-Archive`（不保证条目名带 UTF-8 标志，资源管理器解压中文名会乱码）。
   条目结构（8 项，已实测）：`hypomux.exe`、`bin\h...`、`启动便携版.cmd`、`恢复环境.cmd`、`便携版说明.txt`，全部在顶层目录 `HypoMux-Portable-<版本>-vnic-preview\` 之下。

4. **脚本内的名字引用**：脚本 `echo` 文案里出现中文包内名（如 `恢复环境.cmd`）是**正确**的（CI 会把 ASCII 文件改名成中文后再打包）。仓库内**绝不要**用中文文件名去 `call`/`start`：实测 grep `call .*\.cmd|start .*\.cmd` 在两个脚本里 **0 命中**（脚本只用 `call :内部标签` 与 `start "" /d "%ROOT%" "%ROOT%\hypomux.exe"` 这类 ASCII 路径）。

---

## §2 措辞修正：「停止 HypoMuxCore」是**必需前置条件**，不是「保险措施」

integration-verifier 的 Q1 结论（已被 Lead 复核为真）：原脚本把「停服务」写成可选的保险措施，这是**错的**。我读源码逐条核验后确认其结论成立，并据此重写了文案。

### §2.1 逐条 before / after

| # | 位置 | before（`dist/portable/HypoMux-Portable-2.7.0-vnic-preview/启动便携版.cmd`） | after（`desktop/portable/launch-portable.cmd`） |
|---|---|---|---|
| 1 | rem 段标题 | `L9: rem  关于 HypoMuxCore 服务（请看清，避免误解）：` | `L9: rem  为什么必须先停 HypoMuxCore 服务（**必需前置条件**，不是可选项）：` |
| 2 | rem 段核心 | `L20: rem    那脚本为什么还要停服务？这是**保险措施**：万一便携版没有提权、或 stdio 会话`<br>`L21: rem    尚未建立，服务在运行时会走到 privileged_windows.go:89-96 的 serviceFirstLauncher，`<br>`L22: rem    它优先连已安装版的 \\.\pipe\HypoMux-Core-Service，那台引擎没有 vnic.create`<br>`L23: rem    能力，界面会显示「不支持」。停掉服务可保证回退到 ResolveExecutable 解析出的`<br>`L24: rem    本目录 bin\hypomux-engine.exe。另外，真没提权又没停服务时，虚拟网卡按钮只会`<br>`L25: rem    提示「不支持 / 需要管理员核心」，不会静默失败、也不会偷偷改系统设置。` | 重写为 4 步因果链（见 §2.2）：`L9-27` 逐条给出 file:line 证据，明确「只要 HypoMuxCore 在运行，需要提权拉起引擎时必然连到已安装的股票版引擎（无 vnic.create）而失败」，服务停止后才会回退到 `privilegedLauncher` + `ResolveExecutable` 解析出的本目录 `bin\hypomux-engine.exe`；并注明唯一例外（点击时已存在 `hello.Elevated == true` 的会话）不可依赖 |
| 3 | `[4/6]` 标题 | `L108: echo [4/6] 停止已安装的 HypoMuxCore 服务（保险措施）...` | `L114: echo [4/6] 停止已安装的 HypoMuxCore 服务（必需前置条件）...` |
| 4 | 停止完成提示 | `L191: echo   [完成] HypoMuxCore 已停止（保险措施；实际使用的一直是本目录的引擎）。` | `L198: echo   [完成] HypoMuxCore 已停止（必需前置条件；否则虚拟网卡会连到安装版引擎）。` |
| 5 | 用法第 5 条 | `L141: echo  5) 若没以管理员运行、且 HypoMuxCore 还在跑：虚拟网卡按钮会提示「不支持 /`<br>`L142: echo     需要管理员核心」，这是能力探测的正常结果，不会静默失败或改系统设置。` | `L147: echo  5) 若虚拟网卡提示「当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试」：`<br>`L148: echo     说明程序连上的是已安装版的 HypoMuxCore 引擎（没有虚拟网卡能力）。`<br>`L149: echo     处理：退出便携版，确认 HypoMuxCore 已停止，再重新运行本脚本（见上面 [4/6]）。` |
| 6 | 中性化（§3） | `L6: rem  流程：…`；`L15-18` 的「取决于便携版自己是否提权」段 | 中性化后并入新的 §2.2 因果链；标题改为「HypoMux 便携预览版启动器（虚拟网卡预览）」，不再写死版本/commit |

顺带修正的三处**与真实行为不符**的表述：
- 原文案说未提权时界面提示「不支持 / 需要管理员核心」——**实际文案**是 `errVNICUnsupported`：`desktop/internal/services/virtual_adapter.go:27` → `errors.New("当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试")`。after 已改为引用这条真实文案。
- 原文案称之为「能力探测的正常结果」——实际是**本可避免的功能不可用**（只要先停服务/以管理员运行就能用）。after 已改为「说明程序连上的是已安装版的引擎」+ 处理办法。
- 原文案暗示「以管理员运行即可，不需要停服务」——错误（见下）。

### §2.2 真实链路（我逐行读源码核验，写进了脚本顶部 rem）

1. **普通会话固定 stdio**：`desktop/internal/engineclient/launcher.go:127-131` 的 `stdioLauncher` = `exec.Command(path)`（不带参数、`configureCommand` 设 `CREATE_NO_WINDOW`、`command.Dir = filepath.Dir(path)`）；`engine/cmd/hypomux-engine/main.go:37` 无参数时 `command := "serve"` ⇒ 启动后跑的确实是本目录 `bin\hypomux-engine.exe serve`。
2. **「创建虚拟网卡」用哪个客户端**：`desktop/internal/services/virtual_adapter.go:62` `client, err := s.readyClient()` —— `readyClient()`（同文件 `:166-177`）**只返回已有客户端、不新建会话**；`Status` 用的 `connectedClient()` 同理（未连接 → 降级 `absent`）。默认状态（引擎刚启动、还没建立提权会话）点「创建虚拟网卡」时**没有既有会话**。
3. **于是它必须提权**：`virtual_adapter.go:81` `hello, err := client.EnsureElevated(ctx)` → `desktop/internal/engineclient/client.go:188-190` `EnsureElevated = ensure(ctx, true)`（注释原文：`The desktop UI process itself is never elevated.`）。
4. **EnsureElevated 的复用与重启**（`client.go:192-224`）：`:207-209` `if active && hello.ProtocolVersion == ProtocolVersion && (!requireElevated || hello.Elevated) { return hello, nil }` —— **只有已有提权会话才复用**；否则 `:210-212` `c.killCurrent(...)`，`:220-224` 改用 `c.elevatedLauncher`。
5. **提权启动器 = 优先连服务**：`desktop/internal/engineclient/privileged_windows.go:89-96` `newPrivilegedLauncher()` 返回 `serviceFirstLauncher{service: windowsServiceLauncher{}, fallback: privilegedLauncher{}, …}`；`desktop/internal/engineclient/service_windows.go:31-71` 的 `windowsServiceLauncher.Launch(ctx, _ string)` **忽略传入的引擎路径**，只要 `HypoMuxCore` 已安装且进程在跑（`:39-41` pid≠0 分支）就连管道 `\\.\pipe\HypoMux-Core-Service`（`:19-20` 定义 `CoreServiceName="HypoMuxCore"`）。
6. **连上的是股票版引擎** ⇒ 没有 `vnic.create` ⇒ `virtual_adapter.go:85-87` 返回 `errVNICUnsupported`（文案见上）。
7. **不会自动回退到本目录引擎**：`service_windows.go:93-108` `allowServicePostHandshakeFallback` 只在**协议不兼容**或服务已消失/换 PID 时才允许回退；`client.go:233-266` 该回退只在 `negotiateErr != nil`（握手失败）时触发。服务在 RUNNING 且握手成功 ⇒ 不回退。

**结论**：`sc stop HypoMuxCore` + **以管理员身份运行**是「创建虚拟网卡」能否工作的**必需前置条件**，两者缺一不可。唯一例外是点击按钮时**已经**存在 `hello.Elevated == true` 的会话（`client.go:207-209` 会直接复用），不能依赖。

### §2.3 附带修复的一处 UX 缺陷

`[3/6] 检查管理员权限 ...` 在**通过**时原来不打印任何结果行（`:pp_admin_ok` 后面没有 echo），看起来像"跳过"了检查。after 增加 `desktop/portable/launch-portable.cmd:113` `echo   [通过] 管理员权限检查通过。`；`[2/6]` 冲突分支之后也补了显式空行。

---

## §3 中性化：不再写死版本 / commit

脚本会被 CI 打进**多个版本**，所以任何写死的版本号都会变成假话。改动：

| 位置 | before | after |
|---|---|---|
| 标题 | `HypoMux 便携预览版（免安装）` + rem 里写死 `HEAD 37571e5` 等 | 标题 `HypoMux 便携预览版启动器（虚拟网卡预览）`；rem 只写机制，不写版本/commit |
| `便携版说明.txt` 首段 | 「本包由本地构建产物组装…没有经过 GitHub Actions CI 验证」+ 写死提交号 | 「这是『免安装』的预览包…它由 HypoMux 仓库（GitHub: Ye-Hub2/HypoMux）的构建流水线组装而成，具体版本以发布页/构建号为准。」 |

中性化核验（在 `desktop/portable/` 上 grep，全部 **0 命中**）：`37571e5` = 0、`GitHub Actions` = 0、`未经 GitHub CI|未经过 GitHub Actions` = 0。
另外说明.txt 里的**二进制字节数也被去掉了**（CI 每次构建大小可能变），改成"以发布包为准"式表述。

说明.txt 里**没有**「保险措施」这类错误措辞（grep = 0 命中）；本次把它【运行前提】第 2 条与「为什么必须停 HypoMuxCore」段扩写成与脚本 rem 一致的因果链（普通会话 stdio ≠ 提权会话语义 → `EnsureElevated` → `serviceFirstLauncher` 优先连 `\\.\pipe\HypoMux-Core-Service` → 股票版引擎无 `vnic.create` → 所以必须停服务 + 必须管理员运行，两者缺一不可），并把「提示不支持」改为引用真实文案「当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试」。

> 注意：`dist/portable/` 里的旧包仍是**修正前**的文案（`dist/` 不在我的写入范围，我没有动它）。若要让用户拿到修正版，需要在我这份资产落库后重新组装一次便携包；映射见 §1。

---

## §4 编码字节自检（.NET 逐字节，非目测）

```powershell
$b = [System.IO.File]::ReadAllBytes($p)
$t = [System.Text.Encoding]::UTF8.GetString($b)
$bom  = ($b.Length -ge 3 -and $b[0] -eq 0xEF -and $b[1] -eq 0xBB -and $b[2] -eq 0xBF)
$crlf = ([regex]::Matches($t,"`r`n")).Count
$lone = ([regex]::Matches($t,"(?<!`r)`n")).Count
```

| 文件 | 字节 | BOM | CRLF 数量 | 孤立 LF | 第 1 行 | U+FFFD 替换字符 |
|---|---|---|---|---|---|---|
| `launch-portable.cmd` | 11,102 | **False** ✅ | 257 | **0** ✅ | `@echo off` ✅ | 0 |
| `restore-environment.cmd` | 5,050 | **False** ✅ | 136 | **0** ✅ | `@echo off` ✅ | 0 |
| `PORTABLE-README.txt` | 5,626 | **True** ✅ | 85 | **0** ✅ | `\uFEFF====…`（BOM 之后是分隔线，符合预期） | 0 |

（`U+FFFD` = 0 表示没有解码失败/乱码字符。）

---

## §5 模拟 CI 改名组装的实测（在临时目录做，跑完已删除）

模拟步骤（等价于 CI 的组装）：把 `desktop/portable/` 三个 ASCII 文件**改名**成中文名，连同 `dist/portable/...` 的 5 个二进制一起放进 `%TEMP%\hd-portable-sim\`（`hypomux.exe` 在根、4 个在 `bin\`），然后真跑两个 `--check`。

```
### 模拟 CI 改名组装（%LOCALAPPDATA%\Temp\hd-portable-sim）
  bin\hypomux-engine.exe      8482304 B
  bin\libcronet.dll           9528832 B
  bin\sing-box.exe           81947136 B
  bin\wintun.dll               427552 B
  hypomux.exe                14999552 B
  便携版说明.txt                 5626 B
  恢复环境.cmd                   5050 B
  启动便携版.cmd                 11102 B
```

### §5.1 `cmd /c "启动便携版.cmd --check"` → **exit 0**

输出 2,171 B / 38 行，`U+FFFD` = 0（中文不乱码）。原文：

```
============================================================
 HypoMux 便携预览版（免安装）
============================================================
 程序目录  : %LOCALAPPDATA%\Temp\hd-portable-sim
 数据目录  : %LOCALAPPDATA%\Temp\hd-portable-sim\data
 引擎      : %LOCALAPPDATA%\Temp\hd-portable-sim\bin\hypomux-engine.exe
 运行模式  : 干跑 --check（只打印将要执行的动作，不修改任何东西）
============================================================

[1/6] 检查便携包文件完整性 ...
  [通过] 5 个必需文件齐全。

[2/6] 检查是否已有 hypomux.exe 在运行 ...
  [冲突] 检测到 hypomux.exe 正在运行。
         便携版与已安装版同名同 UniqueID（io.hypomux.desktop），
         后启动的一方会做单实例交接并静默退出，所以必须先完全退出已安装版。
  [干跑] 真实运行到这里会中止：exit /b 1

[3/6] 检查管理员权限 ...
  [通过] 管理员权限检查通过。
[4/6] 停止已安装的 HypoMuxCore 服务（必需前置条件）...
  [动作] sc stop HypoMuxCore
  [干跑] 已跳过实际停止操作。

[5/6] 准备隔离数据目录与安全种子配置 ...
  [动作] mkdir "%LOCALAPPDATA%\Temp\hd-portable-sim\data"
  [动作] 写入种子配置 %LOCALAPPDATA%\Temp\hd-portable-sim\data\settings.json（mode=proxy 且不接管系统代理）
  [干跑] 已跳过实际写入。

[6/6] 启动便携版 ...
  [干跑] start "" /d "%LOCALAPPDATA%\Temp\hd-portable-sim" "%LOCALAPPDATA%\Temp\hd-portable-sim\hypomux.exe"
  [干跑] 将以环境变量启动：HYPOMUX_DATA_DIR=%LOCALAPPDATA%\Temp\hd-portable-sim\data
  [干跑]                   HYPOMUX_ENGINE_PATH=%LOCALAPPDATA%\Temp\hd-portable-sim\bin\hypomux-engine.exe

============================================================
 [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
============================================================
```

> `[2/6]` 报「冲突」是因为用户已安装的 `hypomux.exe`(pid 6240) 当时确实在运行——这是**正确**行为，说明单实例冲突检查真的生效（干跑不会因此失败，真跑会 `exit /b 1`）。

### §5.2 `cmd /c "恢复环境.cmd --check"` → **exit 0**

输出 1,522 B / 30 行，`U+FFFD` = 0。原文节选：

```
============================================================
 HypoMux 便携预览版 - 恢复环境
============================================================
 便携目录  : %LOCALAPPDATA%\Temp\hd-portable-sim
 运行模式  : 干跑 --check（只打印将要执行的动作，不修改任何东西）
============================================================

[1/4] 检查管理员权限 ...
  [通过] 管理员权限检查通过。

[2/4] 结束便携版残留进程（只结束位于本目录下的进程）...
  [干跑] 将结束 ExePath 位于 %LOCALAPPDATA%\Temp\hd-portable-sim 下的 hypomux.exe / hypomux-engine.exe

[3/4] 重新启动已安装的 HypoMuxCore 服务 ...
  [跳过] HypoMuxCore 已经是 RUNNING。

[4/4] 复核服务状态 ...
  [干跑] 将输出：sc query HypoMuxCore
…
 [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
============================================================
```

### §5.3 零副作用与清理（实测）

| 检查项 | 结果 |
|---|---|
| `hd-portable-sim\data\` | **未创建** |
| `hd-portable-sim\data\settings.json` | **未创建** |
| `HypoMuxCore` 服务 | 全程 `STATE : 4 RUNNING`（**未被停止**） |
| `hypomux.exe` 进程 | 仍是用户那个 pid=6240，进程数 1（**未受影响**） |
| **是否真的启动过便携版** | **没有**。两个 `--check` 全程只打印；`start` 那一行是 `[干跑]` 文本 |
| 模拟目录与临时残留 | `%TEMP%\hd-portable-sim` 已递归删除（`Test-Path` = False），`%TEMP%\hd-portable*` 无残留 |

---

## §6 路径冲突核查：`desktop/portable/` 与既有约定**不冲突**

Lead 要求发现冲突立刻上报。我做了以下核查，结论是**没有冲突**，因此按原路径执行：

1. **不被忽略**：`git check-ignore -v desktop/portable/{launch-portable.cmd,restore-environment.cmd,PORTABLE-README.txt}` → **无输出**（未被任何 ignore 规则命中）；`git status` 显示 `?? desktop/portable/`。`desktop/.gitignore` 的规则是 `/.task/`、`/bin/`、`/wails_windows_*.syso`、`/frontend/**`、`/build/windows/**`、`*.log` 等，与 `portable/` 无关。
2. **仓库内无既有 portable 约定**：`git grep -n -i "desktop/portable" -- . ':!.github'` → **无输出**（除了我新增的文件本身尚未入库）。`.github/workflows/**` 里搜 `portable|launch-portable|restore-environment` → **无命中**（ci-runner 的工作尚未落盘，这两份资产是他们要引用的输入）。
3. **不破坏 Go 门禁**：`desktop/portable/` 内**没有 `.go` 文件**，`go -C desktop build ./...` / `vet ./...` / `test ./...` 只遍历 Go 包，非 Go 目录被忽略；`build.yml` 的 gofmt 步骤用的是 `git ls-files -- '*.go'`，只会变多 `.go` 文件时才有影响（本次 0 个）。
4. **不破坏 Wails / NSIS 打包**：`desktop/build/windows/Taskfile.yml`（`windows:build` / `windows:package`）只引用 `build/windows/**`、`bin/**`、`frontend/dist/**`，不 glob `desktop/*`；`build/windows/nsis/project.nsi` 引用的是具名路径。多一个不含构建产物的目录不会进入任何打包规则。
5. 目录名 `portable` 全小写、无空格，符合仓库既有命名习惯（与 `build/`、`cmd/`、`internal/` 同级）。

> 如果后续发现需要换路径（例如 CI 侧已有别的约定），请 Lead 直接指定，我不自行改。

---

## §7 未验证项（如实）

| # | 未验证 | 说明 |
|---|---|---|
| 1 | 两个脚本的**真实分支**未运行 | 非管理员分支、`[4/6]` 真的 `sc stop`、等待 STOPPED 的轮询与超时、`[5/6]` 真的 `mkdir` + 写 `settings.json`、进程清理、`--force` 分支——只在 `--check` 打印过 |
| 2 | **UAC 提权弹窗**未验证 | 干跑不做提权；真机上会弹 UAC（以管理员运行脚本时是脚本自身提权） |
| 3 | **便携版从未启动** | 按 Lead 要求严禁真启动（会与用户已安装的官方 2.7.0 抢单实例、抢 TUN） |
| 4 | CI 侧组装未验证 | 由 ci-runner 在 `.github/workflows/build.yml` 里实现；我提供的改名/编码/zip 契约来自本地实测，未在真实 Runner 上跑过 |
| 5 | `dist/` 里的旧包仍是旧文案 | `dist/` 不在我的写入范围；要用户拿到修正版需重新组装 |
| 6 | 脚本内的行号引用（`launcher.go:127-131` 等）会随代码变动过期 | 若后续源码行号变化，注释里的引用需要复核；逻辑判断不依赖这些行号 |

---

## §8 给 ci-runner 的一页接口契约

```
输入（git 跟踪，ASCII 名，勿改编码）：
  desktop/portable/launch-portable.cmd        11,102 B  no-BOM UTF-8 + CRLF
  desktop/portable/restore-environment.cmd     5,050 B  no-BOM UTF-8 + CRLF
  desktop/portable/PORTABLE-README.txt         5,626 B  BOM UTF-8 + CRLF

组装（PowerShell，勿用 Set-Content / Compress-Archive）：
  $pkg = "dist/portable/HypoMux-Portable-<版本>-vnic-preview"
  New-Item -ItemType Directory -Force -Path "$pkg/bin"
  Copy-Item desktop/portable/launch-portable.cmd     "$pkg/启动便携版.cmd"
  Copy-Item desktop/portable/restore-environment.cmd "$pkg/恢复环境.cmd"
  Copy-Item desktop/portable/PORTABLE-README.txt     "$pkg/便携版说明.txt"
  Copy-Item desktop/bin/hypomux.exe                  "$pkg/hypomux.exe"
  Copy-Item desktop/bin/hypomux-engine.exe,desktop/bin/sing-box.exe,desktop/bin/wintun.dll,desktop/bin/libcronet.dll "$pkg/bin"

打包（UTF-8 条目名）：
  [System.IO.Compression.ZipFile]::CreateFromDirectory(
    (Resolve-Path $pkg).Path, "$pkg.zip",
    [System.IO.Compression.CompressionLevel]::Optimal, $true, [System.Text.Encoding]::UTF8)

自检（可选但推荐）：
  cmd /c "`"$pkg/启动便携版.cmd`" --check"     # 期望 exit 0，输出为合法 UTF-8
  cmd /c "`"$pkg/恢复环境.cmd`" --check"       # 期望 exit 0
```

**不要**在 CI 里真启动 `hypomux.exe`（会与运行中的安装版抢单实例、抢 TUN）；用 `--check` 做组装自检即可。
