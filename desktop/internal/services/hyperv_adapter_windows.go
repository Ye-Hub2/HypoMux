//go:build windows

package services

import (
	"bytes"
	"context"
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
//   - -EncodedCommand **之后不得有任何尾随 token**：5.1 会把它当成「第二条命令」，打印用法
//     横幅后以 0xFFFD0000 退出，脚本从未执行（reports/vnic/76）。payload 只能经
//     hypervSeededCommandBody 播种进正文第一行。
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

// hypervInventorySnapshot 是**读取路径**的执行层：一条完全常量的 powershell 命令，
// 结果从 stdout 直接读回来。
//
// 刻意不走上面的 hypervExecuteUnelevated —— 那条链路是为「常量脚本 + base64 注入可变
// 数据 + 结果文件」设计的。读取路径没有任何可变数据（就两条只读查询），所以整条命令行
// 零插值：既没有注入面，也就不需要 base64 / 结果文件 / -EncodedCommand。
// reports/vnic/76 记录的故障正是重链路本身出了问题（powershell.exe 不接受
// -EncodedCommand 之后追加的位置参数），读取路径绕开它是最小且最准的修法。
//
// hypervReadInventoryScript 是 Go 常量且只用单引号，因此 exec 的参数转义与 PowerShell
// 自己的引号解析不会互相干扰。
func hypervInventorySnapshot(ctx context.Context, timeout time.Duration) (*hypervScriptResult, error) {
	powershell, err := hypervPowerShellPath()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, powershell,
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", hypervReadInventoryScript,
	)
	configureBackgroundCommand(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()

	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return nil, errHypervScriptTimeout
		}
		return nil, ctxErr
	}

	result, parseErr := hypervParseInventoryOutput(stdout.Bytes())
	if parseErr == nil {
		// stdout 里有合法 JSON 就以它为准：脚本自己把异常包成 ok=false 了，退出码没有
		// 解释力（PowerShell 任何非零退出都可能只是 exit 语句本身）。
		return result, nil
	}
	if runErr != nil {
		return nil, fmt.Errorf("%w；powershell.exe %v，stderr：%s", parseErr, runErr, hypervOutputTail(stderr.String()))
	}
	return nil, parseErr
}

// hypervOutputTail 截取 stderr 末段：错误文案比退出码有信息量得多，但也不能让日志爆炸。
func hypervOutputTail(stderr string) string {
	const limit = 300
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return "（空）"
	}
	if len(trimmed) > limit {
		return trimmed[len(trimmed)-limit:]
	}
	return trimmed
}

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

// hypervPowerShellCommand 组装命令行：payload 经 base64 播种进**常量脚本正文的第一行**，
// 可变数据因此完全不进命令行本身。
//
// 修复前是把 payload base64 作为尾随参数跟在 -EncodedCommand 后面，那在 PowerShell 5.1 上
// 是非法的（reports/vnic/76）：powershell.exe 把它当成第二条命令，打印用法横幅后以
// 0xFFFD0000 退出，脚本从未执行、结果文件从未生成，create/remove/waitip 全部必然失败。
//
// 现在 arguments 在 -EncodedCommand 之后**没有任何尾随 token**，所以
//   - 非提权路径（exec.CommandContext）拿到的就是干净的参数数组；
//   - 提权路径（ShellExecuteExW 把 arguments 用空格 join 回去）也天然安全：正文经
//     base64 编码后同样不含空格。
func hypervPowerShellCommand(payload []byte) (string, []string, error) {
	powershell, err := hypervPowerShellPath()
	if err != nil {
		return "", nil, err
	}
	return powershell, hypervPowerShellArguments(payload), nil
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
