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
