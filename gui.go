package main

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/webview/webview_go"
)

// runGUI 把控制台页面渲染进一个原生窗口（基于系统自带的 WebView2 运行时），
// 不依赖任何外部浏览器。窗口关闭时返回。
//
// 注意：webview 必须在主线程创建并进入消息循环，所以这里用
// runtime.LockOSThread 把当前 goroutine 钉在主线程上。
func runGUI(addr string, onClose func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	runtime.GOMAXPROCS(runtime.NumCPU())

	w := webview.New(false)
	if w == nil {
		// WebView2 运行时缺失（精简版系统或未安装 Edge）。
		// windowsgui 版本没有控制台，必须用弹窗告知，否则用户会以为程序卡死。
		msg := "未能创建程序窗口：本机缺少 WebView2 运行时。\n\n" +
			"已自动改用默认浏览器打开控制台：" + consoleURL() + "\n" +
			"如需原生窗口体验，请安装「Microsoft Edge WebView2 运行时」。"
		messageBox("ProxyPilot", msg)
		openBrowser(consoleURL())
		blockForever()
		return
	}
	defer w.Destroy()

	w.SetTitle("ProxyPilot · 代理控制台")
	w.SetSize(1020, 780, webview.HintNone)
	w.SetSize(760, 560, webview.HintMin)

	// 等本地服务起来再加载，避免出现"无法访问"白屏
	go func() {
		for i := 0; i < 60; i++ {
			if runningInstanceOn(addr) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		w.Navigate("http://" + addr + "/")
		w.Dispatch(func() {
			w.SetTitle("ProxyPilot · 代理控制台")
		})
	}()

	w.Run() // 阻塞直到窗口关闭
	if onClose != nil {
		onClose()
	}
}

// blockForever 在没有图形界面时保持进程存活。
func blockForever() {
	select {}
}

var (
	procMessageBoxW = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
)

const (
	mbOK             = 0x00000000
	mbIconInformation = 0x00000040
	mbSetForeground  = 0x00010000
	mbTopmost        = 0x00040000
)

// messageBox 弹出原生提示框。windowsgui 构建下没有控制台，这是唯一的告知途径。
func messageBox(title, text string) {
	t, err1 := syscall.UTF16PtrFromString(title)
	c, err2 := syscall.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		fmt.Println(title, text)
		return
	}
	procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(c)),
		uintptr(unsafe.Pointer(t)),
		mbOK|mbIconInformation|mbSetForeground|mbTopmost)
}
