@echo off
chcp 65001 >nul
setlocal EnableExtensions
rem ============================================================
rem  HypoMux 便携预览版启动器（虚拟网卡预览）
rem  流程：检查环境 -> 停 HypoMuxCore 服务 -> 准备隔离数据目录与安全种子配置
rem        -> 用本目录 bin\ 下的引擎启动 hypomux.exe
rem
rem  为什么必须先停 HypoMuxCore 服务（**必需前置条件**，不是可选项）：
rem    1) 普通会话固定走 stdioLauncher（launcher.go:127-131）：exec.Command(引擎路径)，
rem       不带参数、CREATE_NO_WINDOW、工作目录 = 引擎所在目录；引擎在无参数时默认执行
rem       serve（engine/cmd/hypomux-engine/main.go:37），所以跑的是本目录
rem       bin\hypomux-engine.exe serve。
rem    2) 「创建虚拟网卡」走的是提权路径：virtual_adapter.go:62 的 readyClient() 只返回
rem       已有客户端、不会新建会话（Status 用的 connectedClient() 同理，未连接即 absent），
rem       接着 virtual_adapter.go:81 调用 client.EnsureElevated（client.go:188-190）。
rem    3) EnsureElevated 只在「已有会话且 hello.Elevated 为真」时才直接复用
rem       （client.go:203-209）；否则它会先杀掉当前会话（client.go:210-212），再改用
rem       提权启动器（client.go:220-224）。而这个提权启动器是 serviceFirstLauncher
rem       （privileged_windows.go:89-96），它**优先连服务管道**：
rem       windowsServiceLauncher.Launch 会忽略传入的引擎路径（service_windows.go:31-41），
rem       只要 HypoMuxCore 已安装且处于 RUNNING，就连 \\.\pipe\HypoMux-Core-Service
rem       （service_windows.go:19-20）—— 那是**已安装的股票版**引擎，没有 vnic.create。
rem    4) 结果就是 virtual_adapter.go:85-87 返回 errVNICUnsupported，界面提示
rem       「当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试」
rem       （virtual_adapter.go:27）。注意：这不是"能力探测的正常提示"，
rem       而是本可以避免的功能不可用。
rem    结论：只要 HypoMuxCore 在运行，虚拟网卡就可能连到已安装版引擎并失败，所以本脚本
rem    必须先把 HypoMuxCore 停掉（同时要求以管理员身份运行）。服务停止后，提权启动器
rem    会回退到 privilegedLauncher，用 ResolveExecutable 解析出的本目录
rem    bin\hypomux-engine.exe 拉起一台自带 vnic.create 的管理员引擎。
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
echo.
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
echo   [通过] 管理员权限检查通过。
echo [4/6] 停止已安装的 HypoMuxCore 服务（必需前置条件）...
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
echo  5) 若虚拟网卡提示「当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试」：
echo     说明程序连上的是已安装版的 HypoMuxCore 引擎（没有虚拟网卡能力）。
echo     处理：退出便携版，确认 HypoMuxCore 已停止，再重新运行本脚本（见上面 [4/6]）。
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
echo   [完成] HypoMuxCore 已停止（必需前置条件；否则虚拟网卡会连到安装版引擎）。
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
