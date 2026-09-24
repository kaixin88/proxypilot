package main

import (
	"fmt"
	"runtime"
	"time"

	webview "github.com/webview/webview_go"
)

// runGUI 把内嵌的控制台页面渲染进一个原生窗口（基于系统自带的 WebView2），
// 不依赖任何外部浏览器。窗口关闭时返回。
//
// 注意：webview 必须在主线程创建并进入消息循环，所以这里用
// runtime.LockOSThread 把当前 goroutine 钉在主线程上。
func runGUI(addr string, onClose func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := webview.New(false)
	if w == nil {
		// WebView2 不可用（极少数精简版系统），退回浏览器方案
		fmt.Println("WebView2 不可用，改用默认浏览器打开控制台")
		openBrowser("http://" + addr + "/")
		blockForever()
		return
	}
	defer w.Destroy()

	w.SetTitle("ProxyPilot · 代理控制台")
	w.SetSize(1020, 780, webview.HintNone)
	w.SetSize(760, 560, webview.HintMin)

	// 等本地服务起来再加载，避免出现"无法访问"
	go func() {
		for i := 0; i < 50; i++ {
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
