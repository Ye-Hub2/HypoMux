//go:build windows

package services

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 平台的提权执行层。共享层（hyperv_adapter.go）只提供数据与顺序，这一层负责
// 「怎么把一个常量脚本以管理员身份跑起来、怎么把结果读回来」。
//
// 硬约束（reports/vnic/70-frozen-hyperv-interface.md §3.3）：
//   - 脚本正文是 Go 常量，绝不按输入拼装；所有可变数据走 base64 注入。
//   - -EncodedCommand 必须是 UTF-16LE base64（Windows PowerShell 5.1 只认这个）。
//   - 必须用 powershell.exe，不能用 pwsh（模块栈与 cmdlet 行为不一致）。
//   - ShellExecuteExW + runas 拿不到子进程 stdout，结果只能走结果文件。
//   - 不新增 exe、不新增 helper 子命令、不复用 EnsureElevated。

// hypervShellMaskNoCloseProcess = SEE_MASK_NOCLOSEPROCESS：等 runas 拉起的那个
// powershell.exe 跑完再收结果文件。
const hypervShellMaskNoCloseProcess = 0x00000040

// hypervShellExecuteInfo 复刻 SHELLEXECUTEINFOW 的内存布局。字段顺序与宽度必须与
// shell32.dll 的 ABI 逐字一致（Size/Mask 之后是 8 字节对齐的句柄与指针，Show 之后
// 需要一个 4 字节填充），否则 ShellExecuteExW 会写到错的地方。
type hypervShellExecuteInfo struct {
	Size        uint32
	Mask        uint32
	Window      windows.Handle
	Verb        *uint16
	File        *uint16
	Parameters  *uint16
	Directory   *uint16
	Show        int32
	_           uint32
	Instance    windows.Handle
	IDList      uintptr
	Class       *uint16
	ClassKey    windows.Handle
	HotKey      uint32
	_           uint32
	IconMonitor windows.Handle
	Process     windows.Handle
}

func hypervPlatformSupported() bool { return true }

// hypervExecuteUnelevated 直接以当前权限跑脚本，**永远不弹 UAC**。List()/Switches()/
// DHCP 等待都走这条路径（§3.8）。
func hypervExecuteUnelevated(ctx context.Context, payload []byte, timeout time.Duration) error {
	return hypervRunDirect(ctx, payload, timeout)
}

// hypervExecuteElevated 以管理员权限跑脚本：本进程已提权就直接 exec（省掉一次 UAC），
// 否则 ShellExecuteExW + runas 弹框。
func hypervExecuteElevated(ctx context.Context, payload []byte, timeout time.Duration) error {
	if hypervProcessElevated() {
		return hypervRunDirect(ctx, payload, timeout)
	}
	return hypervRunElevatedShellExecute(ctx, payload, timeout)
}

func hypervProcessElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// hypervRunDirect 已经拿到管理员权限时的执行路径：普通子进程，可被 ctx 取消。
func hypervRunDirect(ctx context.Context, payload []byte, timeout time.Duration) error {
	powershell, arguments, err := hypervPowerShellCommand(payload)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, powershell, arguments...)
	configureBackgroundCommand(command)
	runErr := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return errHypervScriptTimeout
		}
		return ctxErr
	}
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		// 退出码非 0 本身不是「进程起不来」，而是脚本在报告问题；结果文件才是真相。
		if code := exitErr.ExitCode(); code != 0 {
			return fmt.Errorf("PowerShell 以退出码 %d 结束", code)
		}
		return nil
	}
	return runErr
}

// hypervRunElevatedShellExecute UAC 路径。注意三点：
//  1. ShellExecuteExW 的默认工作目录是 System32，所以脚本内容里不允许出现相对路径。
//  2. 提权子进程属于另一个会话，本进程杀不掉它；超时的语义只能是「放弃等待」。
//  3. 用户点「否」时返回 ERROR_CANCELLED，要区分于「执行失败」。
func hypervRunElevatedShellExecute(ctx context.Context, payload []byte, timeout time.Duration) error {
	powershell, arguments, err := hypervPowerShellCommand(payload)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(powershell)
	if err != nil {
		return err
	}
	parameterPointer, err := windows.UTF16PtrFromString(strings.Join(arguments, " "))
	if err != nil {
		return err
	}
	info := hypervShellExecuteInfo{
		Mask:       hypervShellMaskNoCloseProcess,
		Verb:       verb,
		File:       file,
		Parameters: parameterPointer,
		Show:       0, // SW_HIDE
	}
	info.Size = uint32(unsafe.Sizeof(info))
	procedure := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
	result, _, callErr := procedure.Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) || errors.Is(callErr, syscall.Errno(windows.ERROR_CANCELLED)) {
			return errHypervElevationCancelled
		}
		if callErr != nil {
			return callErr
		}
		return errors.New("ShellExecuteExW 未能启动提权脚本")
	}
	if info.Process == 0 || info.Process == windows.InvalidHandle {
		return errors.New("提权脚本没有返回进程句柄")
	}
	defer windows.CloseHandle(info.Process)

	// 超时后不杀进程（杀不掉），只停止等待；脚本自身有界，退出后结果文件仍会落盘，
	// 用户刷新一次就能看到真实状态。
	waitMilliseconds := uint32(timeout / time.Millisecond)
	if waitMilliseconds == 0 {
		waitMilliseconds = 1
	}
	waitResult, waitErr := windows.WaitForSingleObject(info.Process, waitMilliseconds)
	if waitErr != nil {
		return waitErr
	}
	if waitResult == uint32(windows.WAIT_TIMEOUT) {
		return errHypervScriptTimeout
	}
	if waitResult != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("等待提权脚本结束失败（WaitForSingleObject = %d）", waitResult)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.Process, &exitCode); err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("PowerShell 以退出码 %d 结束", exitCode)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return nil
}

// hypervPowerShellCommand 组装命令行：常量脚本编成 UTF-16LE base64，可变数据编成
// UTF-8 base64 后作为 $args[0] 跟在后面。base64 字母表里没有空格，所以拼成
// ShellExecuteExW 的参数字符串是安全的。
func hypervPowerShellCommand(payload []byte) (string, []string, error) {
	powershell, err := hypervPowerShellPath()
	if err != nil {
		return "", nil, err
	}
	arguments := []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(hypervUTF16LE(hypervPowerShellScript)),
		base64.StdEncoding.EncodeToString(payload),
	}
	return powershell, arguments, nil
}

// hypervPowerShellPath 优先用绝对路径：ShellExecuteExW 的默认工作目录是 System32，
// 相对路径会在那边解析失败。兜底复用包内既有的解析器。
func hypervPowerShellPath() (string, error) {
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	candidate := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	return resolveWindowsPowerShellExecutable()
}
