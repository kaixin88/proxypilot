package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFS embed.FS

// App 汇总所有管理器，暴露给 HTTP API。
type App struct {
	nodes    *NodeManager
	kernel   *KernelManager
	sysproxy *SysProxy
	pac      *PACServer

	mu         sync.Mutex
	enabled    bool
	mode       string // global / gfw / rules
	nodeID     string
	kernelName string // xray / clash.meta
	rules      []string
	snapshot   ProxySnapshot
	workDir    string
	lastErr    string
}

func main() {
	exePath, _ := os.Executable()
	root := filepath.Dir(exePath)
	// 若在开发环境运行，用可执行文件目录下的 data
	dataDir := filepath.Join(root, "data")
	_ = os.MkdirAll(dataDir, 0o755)

	app := &App{
		nodes:      NewNodeManager(dataDir),
		kernel:     NewKernelManager(filepath.Join(root, "bin"), filepath.Join(dataDir, "runtime")),
		sysproxy:   NewSysProxy(),
		pac:        NewPACServer("127.0.0.1", localHTTPPort),
		mode:       "gfw",
		kernelName: "clash.meta",
		workDir:    root,
	}
	if len(os.Args) > 1 {
		app.workDir = os.Args[1]
		app.kernel = NewKernelManager(filepath.Join(app.workDir, "bin"), filepath.Join(app.workDir, "data", "runtime"))
	}

	_ = app.nodes.Load()
	_ = app.pac.Start()
	app.autoSelectKernel()

	// 后台静默同步一次节点
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if len(app.nodes.List()) == 0 {
			_, _ = app.nodes.SyncFull(true)
		}
	}()

	mux := http.NewServeMux()
	app.routes(mux)

	addr := "127.0.0.1:17987"
	fmt.Printf("ProxyPilot 已启动: http://%s\n", addr)
	go openBrowser("http://" + addr)

	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Println("服务启动失败:", err)
		pause()
	}
}

func (a *App) autoSelectKernel() {
	avail := a.kernel.Available()
	if _, ok := avail["clash.meta"]; ok {
		a.kernelName = "clash.meta"
	} else if _, ok := avail["xray"]; ok {
		a.kernelName = "xray"
	}
}

func (a *App) routes(mux *http.ServeMux) {
	// 静态前端
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/state", a.jsonHandler(a.handleState))
	mux.HandleFunc("/api/nodes", a.jsonHandler(a.handleNodes))
	mux.HandleFunc("/api/nodes/select", a.jsonHandler(a.handleSelect))
	mux.HandleFunc("/api/sync", a.jsonHandler(a.handleSync))
	mux.HandleFunc("/api/probe", a.jsonHandler(a.handleProbe))
	mux.HandleFunc("/api/on", a.jsonHandler(a.handleOn))
	mux.HandleFunc("/api/off", a.jsonHandler(a.handleOff))
	mux.HandleFunc("/api/mode", a.jsonHandler(a.handleMode))
	mux.HandleFunc("/api/rules", a.jsonHandler(a.handleRules))
	mux.HandleFunc("/api/kernels", a.jsonHandler(a.handleKernels))
	mux.HandleFunc("/api/openfolder", a.jsonHandler(a.handleOpenFolder))
}

func (a *App) jsonHandler(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fn(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	count, syncing, progress, last := a.nodes.Status()
	a.mu.Lock()
	st := map[string]any{
		"enabled":    a.enabled,
		"mode":       a.mode,
		"nodeID":     a.nodeID,
		"kernel":     a.kernelName,
		"rules":      a.rules,
		"lastErr":    a.lastErr,
		"nodeCount":  count,
		"syncing":    syncing,
		"progress":   progress,
		"lastSync":   last.Format("2006-01-02 15:04:05"),
		"proxyPort":  localHTTPPort,
		"socksPort":  localSOCKSPort,
		"pacURL":     a.pac.URL(),
		"kernels":    a.kernel.Available(),
		"running":    a.kernel.Running(),
		"systemProxy": a.sysproxy.Current(),
	}
	a.mu.Unlock()
	writeJSON(w, 200, st)
}

func (a *App) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"nodes": a.nodes.List()})
}

func (a *App) handleSelect(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string `json:"id"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "参数错误"})
		return
	}
	if _, ok := a.nodes.Get(req.ID); !ok {
		writeJSON(w, 404, map[string]any{"error": "节点不存在"})
		return
	}
	a.mu.Lock()
	a.nodeID = req.ID
	a.mu.Unlock()
	_ = a.nodes.Save()
	// 若代理已开启，热切换节点
	a.mu.Lock()
	on := a.enabled
	a.mu.Unlock()
	if on {
		if err := a.restart(); err != nil {
			writeJSON(w, 200, map[string]any{"ok": true, "warn": err.Error()})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	probe := r.URL.Query().Get("probe") != "0"
	go func() { _, _ = a.nodes.SyncFull(probe) }()
	writeJSON(w, 200, map[string]any{"ok": true, "message": "已开始同步"})
}

func (a *App) handleProbe(w http.ResponseWriter, r *http.Request) {
	go func() {
		nodes := a.nodes.List()
		probeAll(nodes, 2500)
		a.nodes.mu.Lock()
		a.nodes.nodes = nodes
		a.nodes.mu.Unlock()
		_ = a.nodes.Save()
	}()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleOn 开启代理：启动内核 + 设置系统代理。
func (a *App) handleOn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"nodeId"`
		Mode   string `json:"mode"`
		Kernel string `json:"kernel"`
		Rules  []string `json:"rules"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	if req.Mode != "" {
		a.mode = req.Mode
	}
	if req.Kernel != "" {
		a.kernelName = req.Kernel
	}
	if req.Rules != nil {
		a.rules = req.Rules
	}
	if req.NodeID != "" {
		a.nodeID = req.NodeID
	}
	a.mu.Unlock()

	if err := a.start(); err != nil {
		a.mu.Lock()
		a.lastErr = err.Error()
		a.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleOff(w http.ResponseWriter, r *http.Request) {
	if err := a.stop(); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode  string   `json:"mode"`
		Rules []string `json:"rules"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	a.mu.Lock()
	if req.Mode != "" {
		a.mode = req.Mode
	}
	if req.Rules != nil {
		a.rules = req.Rules
	}
	enabled := a.enabled
	mode := a.mode
	rules := append([]string(nil), a.rules...)
	a.mu.Unlock()

	a.pac.Update(pacMode(mode), rules, "127.0.0.1", localHTTPPort)

	if enabled {
		if err := a.applySystemProxy(); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules []string `json:"rules"`
		Text  string   `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	rules := req.Rules
	if req.Text != "" {
		rules = nil
		for _, line := range strings.Split(req.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rules = append(rules, line)
		}
	}
	a.mu.Lock()
	a.rules = rules
	a.mu.Unlock()
	a.pac.Update(pacMode(a.currentMode()), rules, "127.0.0.1", localHTTPPort)
	writeJSON(w, 200, map[string]any{"ok": true, "rules": rules})
}

func (a *App) handleKernels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"kernels": a.kernel.Available(), "selected": a.kernelName})
}

func (a *App) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	var req struct{ Which string `json:"which"` }
	_ = json.NewDecoder(r.Body).Decode(&req)
	dir := a.workDir
	switch req.Which {
	case "bin":
		dir = filepath.Join(a.workDir, "bin")
		_ = os.MkdirAll(dir, 0o755)
	case "data":
		dir = filepath.Join(a.workDir, "data")
	}
	_ = exec.Command("explorer", dir).Start()
	writeJSON(w, 200, map[string]any{"ok": true, "dir": dir})
}

func (a *App) currentMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func pacMode(mode string) string {
	if mode == "rules" {
		return "rules"
	}
	return "gfw"
}

// start 应用当前配置：启动内核并按模式设置系统代理。
func (a *App) start() error {
	a.mu.Lock()
	nodeID := a.nodeID
	mode := a.mode
	kernelName := a.kernelName
	rules := append([]string(nil), a.rules...)
	a.mu.Unlock()

	node, ok := a.nodes.Get(nodeID)
	if !ok {
		list := a.nodes.List()
		if len(list) == 0 {
			return fmt.Errorf("暂无可用节点，请先点击「获取节点」")
		}
		// 自动挑选第一个可用节点
		node = list[0]
		for _, n := range list {
			if n.OK {
				node = n
				break
			}
		}
		a.mu.Lock()
		a.nodeID = node.ID
		a.mu.Unlock()
	}

	if node.Protocol != strings.ToLower(node.Protocol) {
		node.Protocol = strings.ToLower(node.Protocol)
	}

	// 依据节点协议挑选合适内核（xray 不支持 hysteria 系）
	kernelName = a.pickKernel(kernelName, node)

	if err := a.kernel.Start(kernelName, node, mode, rules); err != nil {
		return err
	}

	a.pac.Update(pacMode(mode), rules, "127.0.0.1", localHTTPPort)

	if err := a.applySystemProxy(); err != nil {
		a.kernel.Stop()
		return err
	}

	a.mu.Lock()
	a.enabled = true
	a.kernelName = kernelName
	a.lastErr = ""
	a.mu.Unlock()
	return nil
}

// pickKernel 若所选内核不支持该协议，自动切换到支持的内核。
func (a *App) pickKernel(preferred string, node Node) string {
	avail := a.kernel.Available()
	p := strings.ToLower(node.Protocol)
	xrayOK := map[string]bool{"vless": true, "vmess": true, "trojan": true, "ss": true, "shadowsocks": true}
	clashOK := map[string]bool{"vless": true, "vmess": true, "trojan": true, "ss": true,
		"shadowsocks": true, "hysteria": true, "hysteria2": true}

	if preferred == "xray" && !xrayOK[p] {
		if _, ok := avail["clash.meta"]; ok {
			return "clash.meta"
		}
	}
	if preferred == "clash.meta" && !clashOK[p] {
		if _, ok := avail["xray"]; ok {
			return "xray"
		}
	}
	return preferred
}

func (a *App) applySystemProxy() error {
	a.mu.Lock()
	mode := a.mode
	snap := a.snapshot
	already := a.enabled
	a.mu.Unlock()

	if !already {
		snap = a.sysproxy.Snapshot()
		a.mu.Lock()
		a.snapshot = snap
		a.mu.Unlock()
	}

	switch mode {
	case "global":
		hostPort := fmt.Sprintf("127.0.0.1:%d", localHTTPPort)
		return a.sysproxy.SetGlobal(hostPort, nil)
	case "gfw", "rules":
		return a.sysproxy.SetPAC(a.pac.URL())
	}
	return fmt.Errorf("未知模式: %s", mode)
}

// restart 保持开启状态，用当前节点重启内核。
func (a *App) restart() error {
	a.mu.Lock()
	enabled := a.enabled
	a.mu.Unlock()
	if !enabled {
		return a.start()
	}
	a.kernel.Stop()
	a.mu.Lock()
	a.enabled = false
	a.mu.Unlock()
	return a.start()
}

func (a *App) stop() error {
	a.kernel.Stop()
	a.mu.Lock()
	snap := a.snapshot
	a.mu.Unlock()
	err := a.sysproxy.Restore(snap)
	if err != nil {
		// 还原失败则至少关掉
		_ = a.sysproxy.Off()
	}
	a.mu.Lock()
	a.enabled = false
	a.mu.Unlock()
	return err
}

func openBrowser(url string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = hiddenProcAttr()
	_ = cmd.Start()
}

func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

func pause() {
	fmt.Println("按回车退出...")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}
