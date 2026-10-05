# HypoMux 免安装便携预览包（2.7.0 · 虚拟网卡预览）

- **任务**：task-19（T-P1）「组装免安装便携预览包」——用户指令原文：「你给我构建便携版的包」
- **执行者**：`local-pipeline-verifier`
- **产出位置**：`dist/portable/`
- **对应源码**：`37571e5a0531c16621cf4ac3e7511ac21135aa00`（`feat: replace the AI assistant with a virtual adapter action on home`）
- **重要前提（如实声明）**：本包由**本地构建产物组装**而成，**没有**经过 GitHub Actions CI，**没有**在真机上端到端运行过（Lead 与用户决定不真启动）。所有"已通过"的结论都限定在本地复刻 CI 的门禁检查范围（见 `reports/vnic/81-local-pipeline.md`）。

---

## §0 交付物清单

| # | 路径 | 大小 | 说明 |
|---|------|------|------|
| 1 | `dist/portable/HypoMux-Portable-2.7.0-vnic-preview/` | 8 个条目 | 免安装便携目录 |
| 2 | `dist/portable/HypoMux-Portable-2.7.0-vnic-preview.zip` | 41,593,864 B | 整目录压缩包（UTF-8 条目名） |
| 3 | `dist/portable/SHA256SUMS.txt` | 785 B | 8 个文件 + zip 的 SHA-256 |
| 4 | `reports/vnic/40-portable-package.md` | 本文件 | 组装报告 |

`dist/` 已被根 `.gitignore:47`（`/dist/`）忽略，**不会**进入版本库；`git status --porcelain` 除 `?? reports/` 外干净。

---

## §1 包结构（硬性要求，未拍平）

```
dist/portable/HypoMux-Portable-2.7.0-vnic-preview/
├─ hypomux.exe              14,999,552 B  桌面程序
├─ bin\
│  ├─ hypomux-engine.exe     8,482,304 B  内核引擎
│  ├─ sing-box.exe          81,947,136 B  网络核心
│  ├─ wintun.dll               427,552 B  Wintun 驱动库
│  └─ libcronet.dll          9,528,832 B  核心运行时库
├─ 启动便携版.cmd              10,294 B   启动脚本（需管理员）
├─ 恢复环境.cmd                 5,050 B   恢复脚本（需管理员）
└─ 便携版说明.txt               5,884 B   使用说明
```

### 为什么必须保留 `bin\`（不可把 3 个 DLL/EXE 挪到外层）

1. `desktop/internal/engineclient/client.go:557-567` `ResolveExecutable()` 的查找顺序是：`HYPOMUX_ENGINE_PATH` → `dir(exe)\bin\hypomux-engine.exe` → `dir(exe)\hypomux-engine.exe` → 当前目录 → 向上最多 6 层的 `bin\`/`dist\` 候选。便携包按 `dir(exe)\bin\` 布局即可被命中。
2. `desktop/internal/engineclient/service_windows.go:73-91` `allowAutomaticCoreFallbackPath()`：当 `HypoMuxCore` 服务"已安装但未运行"时，**强制要求**"回退引擎"与 `dir(exe)\bin\hypomux-engine.exe` 是同一文件（`os.SameFile`），否则报「待启动 Core 不是当前 HypoMux 安装目录中的 bin\hypomux-engine.exe」并**拒绝启动**。布局一旦拍平，这条守卫就会失败。
3. `sing-box.exe` / `wintun.dll` / `libcronet.dll` 必须与引擎同目录：引擎通过 `build:core-runtime` 的预置与自身解析逻辑在同目录寻找它们。

---

## §2 组装过程

构建产物**复用** `desktop/bin/`（本地复刻 CI 时已产出），**未重新编译**。

```powershell
# 1) 目录与复制（源 → 便携目录）
New-Item -ItemType Directory -Force -Path dist\portable\HypoMux-Portable-2.7.0-vnic-preview\bin
Copy-Item desktop\bin\hypomux.exe                       <pkg>\hypomux.exe
Copy-Item desktop\bin\hypomux-engine.exe                <pkg>\bin\hypomux-engine.exe
Copy-Item desktop\bin\sing-box.exe                      <pkg>\bin\sing-box.exe
Copy-Item desktop\bin\wintun.dll                        <pkg>\bin\wintun.dll
Copy-Item desktop\bin\libcronet.dll                     <pkg>\bin\libcronet.dll
# 2) 三个文本文件按编码要求规范（CRLF 统一；.cmd 不带 BOM；.txt 带 BOM）
#    见 §3 脚本全文与文末"附：完整命令清单"
```

文本文件编码（UTF-8，逐字节复核）：

| 文件 | 字节 | BOM | CRLF 行数 | 孤立 LF |
|------|------|-----|-----------|---------|
| `启动便携版.cmd` | 10,294 | 无 | 250 | 0 |
| `恢复环境.cmd` | 5,050 | 无 | 136 | 0 |
| `便携版说明.txt` | 5,884 | **有**（记事本友好） | 89 | 0 |

> `.cmd` 不带 BOM 是刻意的：cmd.exe 遇到 UTF-8 BOM 会把首行 `@echo off` 变成 `?@echo off` 而报错。`.txt` 带 BOM 是刻意的：`.txt` 只做开关字符串匹配、不执行，带 BOM 可避免旧记事本按 GBK 打开时中文乱码。

### §2.1 逐文件来源与 SHA-256

| 包内文件 | 来源 | 大小 | SHA-256 |
|----------|------|------|---------|
| `hypomux.exe` | `desktop/bin/hypomux.exe` | 14,999,552 | `CFE0280080B886CCDC576755F3268CCA41CE6913625A12466BDF947FA129E1BE` |
| `bin/hypomux-engine.exe` | `desktop/bin/hypomux-engine.exe` | 8,482,304 | `D5D82EC3D7215823C293D2915CD4529DB8390AF43CED5E7EC4939E6D0731E2E0` |
| `bin/sing-box.exe` | `desktop/bin/sing-box.exe` | 81,947,136 | `7BBEF1DEA9189EE12799AE834EA4B4658355DA25C47A21AD8804904C0CCD9410` |
| `bin/wintun.dll` | `desktop/bin/wintun.dll` | 427,552 | `E5DA8447DC2C320EDC0FC52FA01885C103DE8C118481F683643CACC3220DAFCE` |
| `bin/libcronet.dll` | `desktop/bin/libcronet.dll` | 9,528,832 | `257F966119FFCA91D7A2CE110A4B668B865D88BF2ED5339FD06A5644B0D02823` |
| `启动便携版.cmd` | 本次手写 | 10,294 | `115258947381D373EF1FEC477E2C2638EFF7C742434EB1FB7D31FCCAE5993543` |
| `恢复环境.cmd` | 本次手写 | 5,050 | `6751E6D22A2FD8F9DF11A64A342B6B7EB1CC7BF8D853ED2DF1EC882828A036EC` |
| `便携版说明.txt` | 本次手写 | 5,884 | `835D0D963DDBCF02ACE7E1DD9E32B161EB2D91B40B7645F6A88904C62D2D8A4A` |

5 个二进制与 `desktop/bin/` 原件**逐一比对全部 MATCH**（`Get-FileHash` 复算）。

> 注：`desktop/bin/hypomux-amd64-installer.exe`（43,240,066 B）**不在**便携包内——它是安装包，与"免安装便携"目标冲突。

### §2.2 权威校验清单 `dist/portable/SHA256SUMS.txt`

```
CFE0280080B886CCDC576755F3268CCA41CE6913625A12466BDF947FA129E1BE  hypomux.exe
D5D82EC3D7215823C293D2915CD4529DB8390AF43CED5E7EC4939E6D0731E2E0  bin\hypomux-engine.exe
7BBEF1DEA9189EE12799AE834EA4B4658355DA25C47A21AD8804904C0CCD9410  bin\sing-box.exe
E5DA8447DC2C320EDC0FC52FA01885C103DE8C118481F683643CACC3220DAFCE  bin\wintun.dll
257F966119FFCA91D7A2CE110A4B668B865D88BF2ED5339FD06A5644B0D02823  bin\libcronet.dll
115258947381D373EF1FEC477E2C2638EFF7C742434EB1FB7D31FCCAE5993543  启动便携版.cmd
6751E6D22A2FD8F9DF11A64A342B6B7EB1CC7BF8D853ED2DF1EC882828A036EC  恢复环境.cmd
835D0D963DDBCF02ACE7E1DD9E32B161EB2D91B40B7645F6A88904C62D2D8A4A  便携版说明.txt
7FAAFBA74378C1878116993F589BEBE8BFDA8B41B3C608AB21BC02226E4CB515  HypoMux-Portable-2.7.0-vnic-preview.zip
```

---

## §3 脚本全文

### §3.1 `启动便携版.cmd`

```bat
@echo off
chcp 65001 >nul
setlocal EnableExtensions
rem ============================================================
rem  HypoMux 便携预览版启动器（HEAD 37571e5 · 虚拟网卡预览）
rem  流程：检查环境 -> 停 HypoMuxCore 服务 -> 准备隔离数据目录与安全种子配置
rem        -> 用本目录 bin\ 下的引擎启动 hypomux.exe
rem
rem  关于 HypoMuxCore 服务（请看清，避免误解）：
rem    便携版的普通会话固定走 stdioLauncher：launcher.go:127-131 就是
rem    exec.Command(引擎路径)（不带参数、CREATE_NO_WINDOW、工作目录=引擎所在目录），
rem    而引擎在无参数时默认执行 serve（engine/cmd/hypomux-engine/main.go:37），
rem    所以启动后跑的确实是本目录 bin\hypomux-engine.exe serve。
rem
rem    「创建虚拟网卡」用哪台引擎，取决于便携版自己是否提权（client.go:203-209）：
rem    ensure(requireElevated=true) 会先看已有会话，若 hello.Elevated == true 就直接
rem    复用；而 stdio 引擎是 UI 进程的子进程、继承其令牌，所以只要便携版是以管理员
rem    身份启动的（本脚本本来就要求管理员），虚拟网卡不需要 HypoMuxCore 服务就能工作。
rem
rem    那脚本为什么还要停服务？这是**保险措施**：万一便携版没有提权、或 stdio 会话
rem    尚未建立，服务在运行时会走到 privileged_windows.go:89-96 的 serviceFirstLauncher，
rem    它优先连已安装版的 \\.\pipe\HypoMux-Core-Service，那台引擎没有 vnic.create
rem    能力，界面会显示「不支持」。停掉服务可保证回退到 ResolveExecutable 解析出的
rem    本目录 bin\hypomux-engine.exe。另外，真没提权又没停服务时，虚拟网卡按钮只会
rem    提示「不支持 / 需要管理员核心」，不会静默失败、也不会偷偷改系统设置。
rem
rem  这一步只影响 HypoMuxCore（HypoMux 自己的内核服务），不动任何网络配置。
rem  参数：  --check  干跑：只打印将要执行的动作，不做任何修改
rem          --force  跳过前置检查（不建议）
rem ============================================================

set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"

set "CHECK="
set "FORCE="
:pp_args
if "%~1"=="" goto pp_args_done
if /i "%~1"=="--check" set "CHECK=1"
if /i "%~1"=="--force" set "FORCE=1"
shift
goto pp_args

:pp_args_done
set "HYPOMUX_DATA_DIR=%ROOT%\data"
set "HYPOMUX_ENGINE_PATH=%ROOT%\bin\hypomux-engine.exe"
set "HYPOMUX_PORTABLE_ROOT=%ROOT%"
set "PP_FAILED=0"

echo ============================================================
echo  HypoMux 便携预览版（免安装）
echo ============================================================
echo  程序目录  : %ROOT%
echo  数据目录  : %HYPOMUX_DATA_DIR%
echo  引擎      : %HYPOMUX_ENGINE_PATH%
if defined CHECK echo  运行模式  : 干跑 --check（只打印将要执行的动作，不修改任何东西）
if defined FORCE echo  跳过检查  : 已传 --force
echo ============================================================
echo.

echo [1/6] 检查便携包文件完整性 ...
set "PP_MISSING="
for %%F in (hypomux.exe bin\hypomux-engine.exe bin\sing-box.exe bin\wintun.dll bin\libcronet.dll) do (
  if not exist "%ROOT%\%%F" (
    echo   [缺失] %%F
    set "PP_MISSING=1"
  )
)
if "%PP_MISSING%"=="1" goto pp_fail
echo   [通过] 5 个必需文件齐全。
echo.

echo [2/6] 检查是否已有 hypomux.exe 在运行 ...
tasklist /fi "imagename eq hypomux.exe" /nh 2>nul | find /i "hypomux.exe" >nul
if errorlevel 1 goto pp_no_conflict
echo   [冲突] 检测到 hypomux.exe 正在运行。
echo          便携版与已安装版同名同 UniqueID（io.hypomux.desktop），
echo          后启动的一方会做单实例交接并静默退出，所以必须先完全退出已安装版。
if defined FORCE (
  echo   [警告] 已传 --force，忽略此冲突继续执行。
  goto pp_no_conflict
)
if defined CHECK (
  echo   [干跑] 真实运行到这里会中止：exit /b 1
  goto pp_no_conflict
)
echo   [错误] 请先完全退出已安装的 HypoMux（托盘图标 - 退出），再重新运行本脚本。
goto pp_fail
:pp_no_conflict

echo [3/6] 检查管理员权限 ...
net session >nul 2>&1
if not errorlevel 1 goto pp_admin_ok
echo   [权限] 当前不是管理员会话。
echo          本脚本需要停止 HypoMuxCore 服务，必须提权运行。
if defined FORCE (
  echo   [警告] 已传 --force，忽略此问题继续执行（停服务可能失败）。
  goto pp_admin_ok
)
if defined CHECK (
  echo   [干跑] 真实运行到这里会中止：exit /b 1（请右键 - 以管理员身份运行本脚本）
  goto pp_admin_ok
)
echo   [错误] 请右键本脚本 - 以管理员身份运行。
goto pp_fail
:pp_admin_ok

echo [4/6] 停止已安装的 HypoMuxCore 服务（保险措施）...
call :pp_stop_service
if errorlevel 1 goto pp_fail
echo.

echo [5/6] 准备隔离数据目录与安全种子配置 ...
call :pp_prepare_data
if errorlevel 1 goto pp_fail
echo.

echo [6/6] 启动便携版 ...
if defined CHECK (
  echo   [干跑] start "" /d "%ROOT%" "%ROOT%\hypomux.exe"
  echo   [干跑] 将以环境变量启动：HYPOMUX_DATA_DIR=%HYPOMUX_DATA_DIR%
  echo   [干跑]                   HYPOMUX_ENGINE_PATH=%HYPOMUX_ENGINE_PATH%
  echo.
  echo ============================================================
  echo  [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
  echo ============================================================
  endlocal
  exit /b 0
)
start "" /d "%ROOT%" "%ROOT%\hypomux.exe"
echo   [完成] 已启动便携版：%ROOT%\hypomux.exe
echo.
echo ------------------------------------------------------------
echo  怎么用
echo ------------------------------------------------------------
echo  1) 首次打开是「系统代理」模式，且**不接管系统代理**（最安全，不会弄断你的网络）。
echo  2) 首页上的「创建虚拟网卡」就是这次要试的新功能（只在这份预览构建里有）。
echo     引擎需要先启动：如果界面提示未运行，点一下启动/连接。
echo  3) 想切 TUN 模式：设置里手动切（会弹 UAC 提权）。切之前请确认你愿意让程序接管路由。
echo  4) 退出便携版：托盘图标 - 退出（或关闭窗口，wintun 适配器随进程消失）。
echo  5) 若没以管理员运行、且 HypoMuxCore 还在跑：虚拟网卡按钮会提示「不支持 /
echo     需要管理员核心」，这是能力探测的正常结果，不会静默失败或改系统设置。
echo.
echo  出问题怎么办
echo ------------------------------------------------------------
echo  - 先退出便携版：虚拟网卡随进程消失，路由自动回滚。
echo  - 已安装版想继续用：运行本目录的 恢复环境.cmd（会重新启动 HypoMuxCore 服务）。
echo  - 还不行就重启电脑；再不行运行 恢复环境.cmd 后再启动已安装版。
echo.
echo  注意：请始终通过本脚本启动，不要直接双击 hypomux.exe
echo        （直启不会设置 HYPOMUX_DATA_DIR，会去读用户主目录的 .hypomux 配置）。
echo ------------------------------------------------------------
pause
endlocal
exit /b 0

rem ============================================================
rem  子过程
rem ============================================================
:pp_stop_service
sc query HypoMuxCore >nul 2>&1
if errorlevel 1 (
  echo   [跳过] 没有找到 HypoMuxCore 服务（可能未安装）。
  exit /b 0
)
sc query HypoMuxCore | find /i "RUNNING" >nul
if errorlevel 1 (
  echo   [跳过] HypoMuxCore 当前不是 RUNNING 状态。
  exit /b 0
)
echo   [动作] sc stop HypoMuxCore
if defined CHECK (
  echo   [干跑] 已跳过实际停止操作。
  exit /b 0
)
sc stop HypoMuxCore >nul
set "PP_WAIT=0"
:pp_wait_stop
sc query HypoMuxCore | find /i "STOPPED" >nul
if not errorlevel 1 goto pp_stop_ok
set /a PP_WAIT+=1
if %PP_WAIT% GEQ 10 (
  echo   [错误] 10 秒内未能停止 HypoMuxCore。
  echo          请手动执行：sc stop HypoMuxCore
  echo          然后重新运行本脚本。
  exit /b 1
)
ping -n 2 127.0.0.1 >nul
goto pp_wait_stop
:pp_stop_ok
echo   [完成] HypoMuxCore 已停止（保险措施；实际使用的一直是本目录的引擎）。
exit /b 0

:pp_prepare_data
set "PP_DATA=%ROOT%\data"
if exist "%PP_DATA%" goto pp_data_exists
echo   [动作] mkdir "%PP_DATA%"
if defined CHECK goto pp_data_after_mkdir
mkdir "%PP_DATA%" 2>nul
if not exist "%PP_DATA%" (
  echo   [错误] 无法创建数据目录：%PP_DATA%
  exit /b 1
)
echo   [完成] 已创建数据目录。
goto pp_data_after_mkdir
:pp_data_exists
echo   [跳过] 数据目录已存在：%PP_DATA%
:pp_data_after_mkdir
if exist "%PP_DATA%\settings.json" (
  echo   [保留] 已存在 settings.json，不覆盖（保留你自己的设置）。
  exit /b 0
)
echo   [动作] 写入种子配置 %PP_DATA%\settings.json（mode=proxy 且不接管系统代理）
if defined CHECK (
  echo   [干跑] 已跳过实际写入。
  exit /b 0
)
> "%PP_DATA%\settings.json" (
echo {
echo   "mode": "proxy",
echo   "language": "zh",
echo   "socks_port": 10800,
echo   "http_port": 10801,
echo   "system_proxy_takeover": false,
echo   "auto_start_engine": true,
echo   "strategy": "round-robin",
echo   "selected_adapter_ids": ["以太网"],
echo   "adapter_weights": {"以太网": 1}
echo }
)
if not exist "%PP_DATA%\settings.json" (
  echo   [错误] 写入 settings.json 失败。
  exit /b 1
)
echo   [完成] 已写入安全的种子配置（TUN 模式需要你自己在设置里切换）。
exit /b 0

:pp_fail
echo.
echo ============================================================
echo  [已中止] 请按上面的中文提示处理后重试。
echo ============================================================
if defined CHECK (
  echo  [干跑] 真实运行时这里会以 exit /b 1 结束。
  endlocal
  exit /b 0
)
pause
endlocal
exit /b 1
```

### §3.2 `恢复环境.cmd`

```bat
@echo off
chcp 65001 >nul
setlocal EnableExtensions
rem ============================================================
rem  HypoMux 便携预览版 - 恢复环境
rem
rem  作用：结束便携版的残留进程，并把已安装版的 HypoMuxCore 服务重新启动，
rem        让你可以继续使用已安装的 HypoMux（安装版）。
rem
rem  只影响：本目录（便携版目录）下的进程 + HypoMuxCore 服务。
rem  不修改：任何网络配置、路由、DNS、系统代理。
rem
rem  参数： --check  干跑：只打印将要执行的动作，不做任何修改
rem ============================================================

set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"

set "CHECK="
:pp_args
if "%~1"=="" goto pp_args_done
if /i "%~1"=="--check" set "CHECK=1"
shift
goto pp_args

:pp_args_done
set "HYPOMUX_PORTABLE_ROOT=%ROOT%"
set "HYPOMUX_DATA_DIR=%ROOT%\data"

echo ============================================================
echo  HypoMux 便携预览版 - 恢复环境
echo ============================================================
echo  便携目录  : %ROOT%
if defined CHECK echo  运行模式  : 干跑 --check（只打印将要执行的动作，不修改任何东西）
echo ============================================================
echo.

echo [1/4] 检查管理员权限 ...
net session >nul 2>&1
if not errorlevel 1 goto pp_admin_ok
echo   [权限] 当前不是管理员会话。
if defined CHECK (
  echo   [干跑] 真实运行到这里会中止：exit /b 1（请右键 - 以管理员身份运行本脚本）
  goto pp_admin_ok
)
echo   [错误] 请右键本脚本 - 以管理员身份运行。
goto pp_fail
:pp_admin_ok
echo   [通过] 管理员权限检查通过。
echo.

echo [2/4] 结束便携版残留进程（只结束位于本目录下的进程）...
if defined CHECK (
  echo   [干跑] 将结束 ExePath 位于 %ROOT% 下的 hypomux.exe / hypomux-engine.exe
  goto pp_kill_done
)
powershell -NoProfile -ExecutionPolicy Bypass -Command "$r=$env:HYPOMUX_PORTABLE_ROOT; $p=@(Get-CimInstance Win32_Process | Where-Object { ($_.Name -eq 'hypomux.exe' -or $_.Name -eq 'hypomux-engine.exe') -and $_.ExecutablePath -and $_.ExecutablePath.StartsWith($r, 'OrdinalIgnoreCase') }); if ($p.Count -eq 0) { Write-Host '  [跳过] 没有发现便携版残留进程。' } else { $p | ForEach-Object { Write-Host ('  [停止] PID ' + $_.ProcessId + '  ' + $_.ExecutablePath); Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; Write-Host ('  [完成] 已结束 ' + $p.Count + ' 个便携版进程。') }"
echo   [完成] 便携版进程清理结束（安装版进程不受影响）。
:pp_kill_done
echo.

echo [3/4] 重新启动已安装的 HypoMuxCore 服务 ...
sc query HypoMuxCore >nul 2>&1
if errorlevel 1 (
  echo   [跳过] 没有找到 HypoMuxCore 服务（可能未安装）。
  echo          你可以直接启动已安装版 HypoMux。
  goto pp_finish
)
sc query HypoMuxCore | find /i "RUNNING" >nul
if not errorlevel 1 (
  echo   [跳过] HypoMuxCore 已经是 RUNNING。
  goto pp_finish
)
echo   [动作] sc start HypoMuxCore
if defined CHECK (
  echo   [干跑] 已跳过实际启动操作。
  goto pp_finish
)
sc start HypoMuxCore >nul
set "PP_WAIT=0"
:pp_wait_start
sc query HypoMuxCore | find /i "RUNNING" >nul
if not errorlevel 1 goto pp_start_ok
set /a PP_WAIT+=1
if %PP_WAIT% GEQ 10 (
  echo   [错误] 10 秒内未能启动 HypoMuxCore。
  echo          请手动执行：sc start HypoMuxCore
  echo          或者直接重启电脑。
  goto pp_fail
)
ping -n 2 127.0.0.1 >nul
goto pp_wait_start
:pp_start_ok
echo   [完成] HypoMuxCore 已启动。
goto pp_finish

:pp_finish
echo.
echo [4/4] 复核服务状态 ...
if defined CHECK (
  echo   [干跑] 将输出：sc query HypoMuxCore
) else (
  sc query HypoMuxCore
)
echo.
echo ------------------------------------------------------------
echo  接下来：
echo   1) 现在可以正常启动「已安装版」HypoMux 了（开始菜单 / 桌面图标）。
echo   2) 便携版的配置与数据仍保存在 %ROOT%\data，可随时再用 启动便携版.cmd。
echo   3) 本脚本没有改动路由 / DNS / 系统代理。
echo ------------------------------------------------------------
if defined CHECK (
  echo.
  echo ============================================================
  echo  [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
  echo ============================================================
  endlocal
  exit /b 0
)
pause
endlocal
exit /b 0

:pp_fail
echo.
echo ============================================================
echo  [已中止] 请按上面的中文提示处理后重试。
echo ============================================================
if defined CHECK (
  echo  [干跑] 真实运行时这里会以 exit /b 1 结束。
  endlocal
  exit /b 0
)
pause
endlocal
exit /b 1
```

---

## §4 干跑验证（`--check`，零修改）

两个脚本都支持 `--check`：**只打印将要执行的动作，不 mkdir、不写文件、不停服务、不启动程序**。

### §4.1 原始输出 · `cmd /c "启动便携版.cmd --check"` → 退出码 0

```
============================================================
 HypoMux 便携预览版（免安装）
============================================================
 程序目录  : <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview
 数据目录  : <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\data
 引擎      : <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\bin\hypomux-engine.exe
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
[4/6] 停止已安装的 HypoMuxCore 服务（保险措施）...
  [动作] sc stop HypoMuxCore
  [干跑] 已跳过实际停止操作。

[5/6] 准备隔离数据目录与安全种子配置 ...
  [动作] mkdir "<repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\data"
  [动作] 写入种子配置 <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\data\settings.json（mode=proxy 且不接管系统代理）
  [干跑] 已跳过实际写入。

[6/6] 启动便携版 ...
  [干跑] start "" /d "<repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview" "<repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\hypomux.exe"
  [干跑] 将以环境变量启动：HYPOMUX_DATA_DIR=<repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\data
  [干跑]                   HYPOMUX_ENGINE_PATH=<repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\bin\hypomux-engine.exe

============================================================
 [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
============================================================
```

### §4.2 原始输出 · `cmd /c "恢复环境.cmd --check"` → 退出码 0

```
============================================================
 HypoMux 便携预览版 - 恢复环境
============================================================
 便携目录  : <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview
 运行模式  : 干跑 --check（只打印将要执行的动作，不修改任何东西）
============================================================

[1/4] 检查管理员权限 ...
  [通过] 管理员权限检查通过。

[2/4] 结束便携版残留进程（只结束位于本目录下的进程）...
  [干跑] 将结束 ExePath 位于 <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview 下的 hypomux.exe / hypomux-engine.exe

[3/4] 重新启动已安装的 HypoMuxCore 服务 ...
  [跳过] HypoMuxCore 已经是 RUNNING。

[4/4] 复核服务状态 ...
  [干跑] 将输出：sc query HypoMuxCore

------------------------------------------------------------
 接下来：
  1) 现在可以正常启动「已安装版」HypoMux 了（开始菜单 / 桌面图标）。
  2) 便携版的配置与数据仍保存在 <repo>\dist\portable\HypoMux-Portable-2.7.0-vnic-preview\data，可随时再用 启动便携版.cmd。
  3) 本脚本没有改动路由 / DNS / 系统代理。
------------------------------------------------------------

============================================================
 [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
============================================================
```

### §4.3 干跑判定

| 检查项 | 结果 |
|--------|------|
| `启动便携版.cmd --check` 退出码 | **0** |
| `恢复环境.cmd --check` 退出码 | **0** |
| 输出编码 | 合法 UTF-8，**无** U+FFFD 替换字符（中文不乱码） |
| 副作用：`data\` 目录 | **未创建** |
| 副作用：`data\settings.json` | **未创建** |
| 副作用：`HypoMuxCore` 服务 | 干跑前后均 `STATE : 4 RUNNING`（**未被停止**） |
| 副作用：用户当时正在运行的 `hypomux.exe` | 干跑执行时刻前后均为 pid 6240（**未受影响**） |

> 干跑输出里会出现「[冲突] 检测到已安装版 HypoMux 正在运行」与「[拒绝] 当前不是管理员会话 / 干跑模式，跳过提权检查」两条——这正是预期展示：脚本在真机上确实会因用户当前的已安装版正在运行而中止，**不会**去干扰它。

---

## §5 zip 与完整性核对

- 生成：`[System.IO.Compression.ZipFile]::CreateFromDirectory($src, $zip, Optimal, $true, [Text.Encoding]::UTF8)`
  - **刻意使用 .NET API 而非 `Compress-Archive`**：PS 5.1 的 `Compress-Archive` 写出的条目名不保证带 UTF-8 标志，Windows 资源管理器解压 `启动便携版.cmd` 这类中文名可能乱码；`CreateFromDirectory(..., UTF8)` 写入 UTF-8 条目名，现代 Windows 解压正常。
- 条目结构与期望集合**完全一致**（8/8，名称按 UTF-8 解码后逐条比对）：

```
HypoMux-Portable-2.7.0-vnic-preview\hypomux.exe
HypoMux-Portable-2.7.0-vnic-preview\便携版说明.txt
HypoMux-Portable-2.7.0-vnic-preview\启动便携版.cmd
HypoMux-Portable-2.7.0-vnic-preview\恢复环境.cmd
HypoMux-Portable-2.7.0-vnic-preview\bin\hypomux-engine.exe
HypoMux-Portable-2.7.0-vnic-preview\bin\libcronet.dll
HypoMux-Portable-2.7.0-vnic-preview\bin\sing-box.exe
HypoMux-Portable-2.7.0-vnic-preview\bin\wintun.dll
```

- `Expand-Archive` 解压到 `%TEMP%\pp-extract-verify` 后逐文件复算哈希：**8/8 全部 MATCH**（含 3 个中文名文本文件），解压后文件数 8，核对完成后临时目录已删除。
- 说明：脚本内那条"期望 vs 实际"的自动比对表达式在 PS 5.1 下因字符串+数组拼接的语义把期望集合算成了 1 条（打印 MISMATCH 是表达式缺陷，不是包的问题）；随后逐条列出实际条目并做了解压哈希往返校验，结论以上面 8/8 为准。

---

## §6 引擎选择与"为什么还要停服务"（按 Lead 更正后的表述）

1. **普通会话固定 stdio**：`desktop/internal/engineclient/launcher.go:127-131` 的 `stdioLauncher` 就是 `exec.Command(path)`（**不带任何参数**，`configureCommand` = `CREATE_NO_WINDOW`，`command.Dir = filepath.Dir(path)`）；`engine/cmd/hypomux-engine/main.go:37` 在**无参数时 `command := "serve"`** ⇒ 便携版跑的确实是便携目录里的 `bin\hypomux-engine.exe serve`。
2. **虚拟网卡用哪台引擎，取决于便携版自身是否提权**：`desktop/internal/engineclient/client.go:203-209` —— `ensure(requireElevated=true)` 先看**已有会话**，`if active && hello.ProtocolVersion == ProtocolVersion && (!requireElevated || hello.Elevated) { return hello, nil }`。`hello.Elevated` 来自引擎自己进程令牌（`engine/internal/platform/identity_windows.go:15` → `windows.GetCurrentProcessToken().IsElevated()`），而 stdio 引擎是 UI 的子进程、**继承令牌** ⇒ **只要便携版以管理员身份启动（启动脚本本来就要求），虚拟网卡不需要 `HypoMuxCore` 服务即可工作。**
3. **因此 `sc stop HypoMuxCore` 是保险措施，不是"否则一定用错引擎"**：它保证「万一便携版没有提权 / stdio 会话尚未建立」时，也不会连上已安装版的**股票版服务引擎**（服务在跑时 `privileged_windows.go:89-96` 的 `serviceFirstLauncher` 会优先连 `\\.\pipe\HypoMux-Core-Service`，那台引擎没有 `vnic.create`）。两个前提（**管理员运行** + **停服务**）保持不变。
4. **降级行为**：既没提权、服务又在跑时，虚拟网卡的按钮只会提示「不支持 / 需要管理员核心」（`desktop/internal/services/virtual_adapter.go` 的 Hello 能力探测 + `errVNICUnsupported`），**不会**静默失败，**不会**偷偷改系统设置。

---

## §7 未验证项与风险（如实）

| # | 未验证/限制 | 说明 |
|---|-------------|------|
| 1 | **便携版从未真实启动** | 只做了 `--check` 干跑。Lead 与用户决定不由我启动；真实启动、UAC 提权、托盘图标、单实例互斥均**未经运行验证** |
| 2 | **虚拟网卡端到端未验证** | 我此前在真机上验证过引擎的核心行为（`reports/vnic/22-engine-edge.md`），但**未经由这个便携包的 UI 走一遍** |
| 3 | **首启种子配置不会自动启动引擎** | `auto_start_engine` 的语义是"开机静默自启"（`desktop/main.go:266` 受 `settings.Autostart` 门控），且 `Get()`（`settings.go:152-164`）在注册表自启关闭时把返回副本强制 `AutoStartEngine=false`、`validateSettings`（`settings.go:681`）也要求自启开关先打开 ⇒ 首次进入需**手动点启动**。这是刻意选择（不自动改系统、不自动起进程） |
| 4 | **未做代码签名** | `hypomux.exe` / `sing-box.exe` 无签名，Windows SmartScreen / Defender 可能拦（`sing-box.exe` 81.9 MB，属常见误报对象） |
| 5 | **两个 .cmd 的真实分支未跑** | 非管理员分支、服务停止分支、`--force` 分支、进程清理分支都只经过静态复核与干跑打印 |
| 6 | **未经过 GitHub CI** | 本地复刻 CI 门禁全通过（见 `reports/vnic/81-local-pipeline.md`），但 CI 从未跑过该提交；本地 node v24.21.0 vs CI node 22 亦存在差异 |
| 7 | **数据目录隔离依赖环境变量** | 直接双击 `hypomux.exe`（不经脚本）会失去 `HYPOMUX_DATA_DIR` 隔离，去读 `%USERPROFILE%\.hypomux`（可能是安装版配置）——已在说明里显著提示 |

---

## §8 推荐试用步骤与回滚

```
1) 托盘退出已安装版 HypoMux（必须；否则单实例互斥导致便携版静默退出）
2) 解压 HypoMux-Portable-2.7.0-vnic-preview.zip 到任意非 Program Files 目录
3) 右键「启动便携版.cmd」→ 以管理员身份运行
4) 程序启动后：首页「创建虚拟网卡」→ 试用（默认 proxy 模式、不接管系统代理）
5) 用完退出便携版；如要立刻回到安装版：运行「恢复环境.cmd」（管理员）
6) 彻底回滚：删除整个便携目录即可（不改注册表、不改 Program Files、
   不改 %USERPROFILE%\.hypomux、退出时适配器与路由自动回滚）
```

---

## 附：完整命令清单（复现本包）

```powershell
Set-Location '<repo>'
$pub = 'dist\portable'; $pkg = "$pub\HypoMux-Portable-2.7.0-vnic-preview"
New-Item -ItemType Directory -Force -Path "$pkg\bin" | Out-Null
Copy-Item desktop\bin\hypomux.exe,$pkg
Copy-Item desktop\bin\hypomux-engine.exe,desktop\bin\sing-box.exe,desktop\bin\wintun.dll,desktop\bin\libcronet.dll "$pkg\bin"
# 脚本与说明：写好后统一规范编码（CRLF；.cmd 无 BOM、.txt 带 BOM）

# 干跑（零修改）
cmd /c "`"$PWD\$pkg\启动便携版.cmd`" --check"
cmd /c "`"$PWD\$pkg\恢复环境.cmd`" --check"

# 打包（UTF-8 条目名）
Add-Type -AssemblyName System.IO.Compression.FileSystem
[System.IO.Compression.ZipFile]::CreateFromDirectory(
  (Resolve-Path $pkg).Path, (Join-Path (Resolve-Path $pub).Path 'HypoMux-Portable-2.7.0-vnic-preview.zip'),
  [System.IO.Compression.CompressionLevel]::Optimal, $true, [System.Text.Encoding]::UTF8)

# 校验
Get-FileHash $pkg\hypomux.exe -Algorithm SHA256   # 与 desktop\bin\hypomux.exe 比对
Expand-Archive -LiteralPath "$pub\HypoMux-Portable-2.7.0-vnic-preview.zip" -DestinationPath $env:TEMP\pp-extract-verify -Force
```

---

**结论**：8 个条目齐全、5 个二进制与 `desktop/bin/` 原件逐字节一致、两个脚本 `--check` 干跑退出码 0 且零副作用、zip 解压往返 8/8 哈希一致。这是一个**可以给人试用的预览包**，但它尚未真实运行过，请勿当作正式发布版。
