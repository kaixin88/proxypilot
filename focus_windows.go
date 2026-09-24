package main

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procFindWindowW      = user32.NewProc("FindWindowW")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procIsIconic         = user32.NewProc("IsIconic")
	procGetWindowTextW   = user32.NewProc("GetWindowTextW")
	procEnumWindows      = user32.NewProc("EnumWindows")
	procGetWindowThread  = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadIn   = user32.NewProc("AttachThreadInput")
	procBringWindowToTop = user32.NewProc("BringWindowToTop")
	procGetForegroundWnd = user32.NewProc("GetForegroundWindow")
	procGetWindowVisible = user32.NewProc("IsWindowVisible")

	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentThread = kernel32.NewProc("GetCurrentThreadId")
)

const (
	swRestore   = 9
	swShow      = 5
	windowTitle = "ProxyPilot"
	maxTitleBuf = 512
)

// focusExistingWindow 找到已经在运行的 ProxyPilot 窗口并把它带到前台。
// 找不到窗口时（例如 GUI 创建失败），退回打开浏览器控制台。
func focusExistingWindow() {
	hwnd := findProxyPilotWindow()
	if hwnd == 0 {
		// 没有原生窗口，退回浏览器
		openBrowser(consoleURL())
		return
	}

	// 最小化时先还原
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procShowWindow.Call(hwnd, swShow)

	// 借助前台线程的输入队列才能真正抢占前台焦点
	fgWnd, _, _ := procGetForegroundWnd.Call()
	curTid, _, _ := procGetCurrentThread.Call()

	var fgTid uintptr
	if fgWnd != 0 {
		fgTid, _, _ = procGetWindowThread.Call(fgWnd, 0)
	} else {
		// 没有前台窗口时，退回用目标窗口自身线程
		fgTid, _, _ = procGetWindowThread.Call(hwnd, 0)
	}

	if fgTid != 0 && fgTid != curTid {
		procAttachThreadIn.Call(fgTid, curTid, 1)
		procBringWindowToTop.Call(hwnd)
		procSetForeground.Call(hwnd)
		procAttachThreadIn.Call(fgTid, curTid, 0)
	} else {
		procBringWindowToTop.Call(hwnd)
		procSetForeground.Call(hwnd)
	}
}

// findProxyPilotWindow 枚举顶层窗口，找到属于本进程且标题匹配的可见窗口。
func findProxyPilotWindow() uintptr {
	self := uint32(syscall.Getpid())
	var found uintptr

	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		// 只看可见窗口
		if r, _, _ := procGetWindowVisible.Call(hwnd); r == 0 {
			return 1
		}
		var wpid uint32
		procGetWindowThread.Call(hwnd, uintptr(unsafe.Pointer(&wpid)))
		if wpid != self {
			return 1 // 不是本进程的窗口
		}
		var buf [maxTitleBuf]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), maxTitleBuf)
		if n == 0 {
			return 1
		}
		title := syscall.UTF16ToString(buf[:n])
		if strings.Contains(strings.ToLower(title), strings.ToLower(windowTitle)) {
			found = hwnd
			return 0 // 找到了，停止枚举
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// focusByTitle 按标题模糊查找任意进程的窗口（备用，例如 WebView2 宿主窗口标题变化）。
func focusByTitle(title string) bool {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(t)), 0)
	if hwnd == 0 {
		return false
	}
	procShowWindow.Call(hwnd, swRestore)
	procBringWindowToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
	return true
}
