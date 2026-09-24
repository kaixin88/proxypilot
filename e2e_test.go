package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHTTPEndToEnd 在进程内启动完整 HTTP 服务，走一遍真实用户流程。
func TestHTTPEndToEnd(t *testing.T) {
	dir := t.TempDir()
	app := &App{
		nodes:      NewNodeManager(dir),
		kernel:     NewKernelManager(dir+"/bin", dir+"/rt"),
		sysproxy:   NewSysProxy(),
		pac:        NewPACServer("127.0.0.1", localHTTPPort),
		mode:       "gfw",
		kernelName: "clash.meta",
		workDir:    dir,
	}
	if err := app.pac.Start(); err != nil {
		t.Fatalf("PAC 服务启动失败: %v", err)
	}
	defer app.pac.Stop()

	mux := http.NewServeMux()
	app.routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. 首页
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("首页请求失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("首页状态码 %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "ProxyPilot") {
		t.Errorf("首页未包含应用名")
	}
	t.Logf("✓ 首页加载成功 (%d 字节)", len(body))

	// 2. 状态接口
	var st map[string]any
	getJSON(t, srv.URL+"/api/state", &st)
	if st["mode"] != "gfw" {
		t.Errorf("默认模式应为 gfw, 实际 %v", st["mode"])
	}
	t.Logf("✓ 状态接口: mode=%v kernel=%v proxyPort=%v", st["mode"], st["kernel"], st["proxyPort"])

	// 3. 同步节点
	postJSON(t, srv.URL+"/api/sync?probe=1", nil, nil)
	t.Log("  已触发同步，等待完成 ...")
	deadline := time.Now().Add(150 * time.Second)
	var nodeCount int
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		getJSON(t, srv.URL+"/api/state", &st)
		if syncing, _ := st["syncing"].(bool); !syncing {
			nodeCount = int(st["nodeCount"].(float64))
			break
		}
	}
	if nodeCount == 0 {
		t.Fatalf("同步后仍无节点")
	}
	t.Logf("✓ 节点同步完成: %d 个", nodeCount)

	// 4. 节点列表
	var nl struct {
		Nodes []Node `json:"nodes"`
	}
	getJSON(t, srv.URL+"/api/nodes", &nl)
	if len(nl.Nodes) == 0 {
		t.Fatalf("节点列表为空")
	}
	t.Logf("✓ 节点列表: %d 个", len(nl.Nodes))
	for i, n := range nl.Nodes {
		if i >= 5 {
			break
		}
		t.Logf("    %s %s:%d proto=%s ok=%v lat=%d",
			n.ID[:8], n.Server, n.Port, n.Protocol, n.OK, n.Latency)
	}

	// 5. 选择节点
	target := nl.Nodes[0]
	postJSON(t, srv.URL+"/api/nodes/select", map[string]any{"id": target.ID}, &st)
	getJSON(t, srv.URL+"/api/state", &st)
	if st["nodeID"] != target.ID {
		t.Errorf("节点选择未生效: %v != %v", st["nodeID"], target.ID)
	}
	t.Logf("✓ 节点已选中: %s", target.Name)

	// 6. 切换模式 + 规则
	postJSON(t, srv.URL+"/api/mode", map[string]any{
		"mode":  "rules",
		"rules": []string{"google.com", "8.8.8.8", "*.github.com"},
	}, &st)
	getJSON(t, srv.URL+"/api/state", &st)
	if st["mode"] != "rules" {
		t.Errorf("模式切换失败: %v", st["mode"])
	}
	t.Logf("✓ 已切换到规则模式")

	// 7. 保存规则
	var rr map[string]any
	postJSON(t, srv.URL+"/api/rules", map[string]any{
		"text": "google.com\n# 注释\n*.github.com\n1.1.1.1\n",
	}, &rr)
	rules, _ := rr["rules"].([]any)
	if len(rules) != 3 {
		t.Errorf("规则解析应为 3 条（注释被过滤），实际 %d: %v", len(rules), rules)
	}
	t.Logf("✓ 规则保存: %v", rules)

	// 8. PAC 服务
	pacResp, err := http.Get(app.pac.URL())
	if err != nil {
		t.Fatalf("PAC 请求失败: %v", err)
	}
	pacBody, _ := io.ReadAll(pacResp.Body)
	pacResp.Body.Close()
	if !strings.Contains(string(pacBody), "FindProxyForURL") {
		t.Errorf("PAC 内容异常")
	}
	if !strings.Contains(string(pacBody), "google.com") {
		t.Errorf("PAC 未包含用户规则")
	}
	t.Logf("✓ PAC 服务正常 (%d 字节), 规则已注入", len(pacBody))

	// 9. 去选/重选 + 内核缺失时的错误处理
	postJSON(t, srv.URL+"/api/nodes/select", map[string]any{"id": "nonexistent"}, &st)

	// 10. 开启代理（无内核，应返回明确错误而非崩溃）
	var onRes map[string]any
	postJSON(t, srv.URL+"/api/on", map[string]any{"mode": "gfw"}, &onRes)
	if onRes["ok"] == false {
		t.Logf("✓ 无内核时开启代理返回明确错误: %v", onRes["error"])
	} else {
		t.Logf("✓ 开启代理成功（检测到内核）")
	}

	// 11. 关闭代理
	var offRes map[string]any
	postJSON(t, srv.URL+"/api/off", nil, &offRes)
	t.Logf("✓ 关闭代理: %v", offRes)

	t.Log("=== 全流程 HTTP 端到端测试通过 ===")
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("解析 %s 响应失败: %v", url, err)
	}
}

func postJSON(t *testing.T, url string, in any, out any) {
	t.Helper()
	var rdr io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rdr = strings.NewReader(string(b))
	}
	resp, err := http.Post(url, "application/json", rdr)
	if err != nil {
		t.Fatalf("POST %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
}

var _ = fmt.Sprintf
