package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSyncAndParse 验证节点抓取、解析、去重与探测全链路。
func TestSyncAndParse(t *testing.T) {
	nm := NewNodeManager(t.TempDir())
	res, err := nm.Sync(SyncOptions{
		Sources:    defaultSources(),
		Probe:      false,
		TimeoutMs:  2500,
		KeepFailed: true,
	})
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	t.Logf("源: %d/%d 可用, 解析 %d, 合并 %d",
		res.FetchedSources, res.TotalSources, res.Parsed, res.Merged)
	t.Logf("提示: %s", res.Message)

	nodes := nm.List()
	if len(nodes) == 0 {
		t.Fatalf("未解析出任何节点（可能网络不通或源已变更）")
	}
	t.Logf("节点总数: %d", len(nodes))

	// 检查去重：ID 必须唯一
	seen := map[string]bool{}
	for _, n := range nodes {
		if seen[n.ID] {
			t.Errorf("节点 ID 重复: %s (%s)", n.ID, n.Name)
		}
		seen[n.ID] = true
		if n.Server == "" || n.Port == 0 {
			t.Errorf("节点信息不完整: %+v", n)
		}
	}

	// 打印前 15 个
	for i, n := range nodes {
		if i >= 15 {
			t.Logf("... 还有 %d 个节点", len(nodes)-15)
			break
		}
		t.Logf("  %-10s %-20s:%-6d proto=%-9s transport=%-5s src=%s",
			n.ID[:8], n.Server, n.Port, n.Protocol, n.Transport, n.Source)
	}

	// 统计协议分布
	dist := map[string]int{}
	for _, n := range nodes {
		dist[n.Protocol]++
	}
	t.Logf("协议分布: %v", dist)
}

// TestProbe 验证 TCP 可用性探测。
func TestProbe(t *testing.T) {
	nm := NewNodeManager(t.TempDir())
	_, _ = nm.Sync(SyncOptions{Sources: defaultSources()[:4], Probe: false, KeepFailed: true})
	nodes := nm.List()
	if len(nodes) == 0 {
		t.Skip("无节点可探测")
	}
	if len(nodes) > 10 {
		nodes = nodes[:10]
	}
	probeAll(nodes, 2500)
	alive := 0
	for _, n := range nodes {
		st := "×"
		if n.OK {
			st = "√"
			alive++
		}
		t.Logf("  %s %-20s:%-6d %5d ms", st, n.Server, n.Port, n.Latency)
	}
	t.Logf("可用 %d/%d", alive, len(nodes))
}

// TestPAC 验证 PAC 脚本生成与规则编译。
func TestPAC(t *testing.T) {
	pac := NewPACServer("127.0.0.1", localHTTPPort)

	pac.Update("rules", []string{"google.com", "*.githubusercontent.com", "8.8.8.8", "104.16.0.0/12", "# comment"}, "127.0.0.1", localHTTPPort)
	script := pac.script()
	if len(script) < 800 {
		t.Errorf("PAC 内容过短: %d 字节", len(script))
	}
	for _, fn := range []string{"FindProxyForURL", "ipInCIDR", "wildcardMatch", "matchesRules", "isLocal", "PROXY 127.0.0.1:7897"} {
		if !strings.Contains(script, fn) {
			t.Errorf("PAC 缺少关键内容: %s", fn)
		}
	}
	if strings.Contains(script, "comment") {
		t.Errorf("注释行未被过滤")
	}
	t.Logf("规则模式 PAC: %d 字节", len(script))

	pac.Update("gfw", nil, "127.0.0.1", localHTTPPort)
	gfw := pac.script()
	if !strings.Contains(gfw, "CN_DOMAIN_SUFFIX") {
		t.Errorf("分流模式 PAC 缺少国内域名表")
	}
	t.Logf("分流模式 PAC: %d 字节, 国内后缀 %d 条", len(gfw), len(cnDomainSuffixes))
}

// TestSysProxy 验证注册表读写与还原。
func TestSysProxy(t *testing.T) {
	sp := NewSysProxy()
	snap := sp.Snapshot()
	t.Logf("原始: enable=%v server=%q bypass=%q autoCfg=%q",
		snap.Enable, snap.Server, snap.Bypass, snap.AutoCfg)

	if err := sp.SetGlobal("127.0.0.1:7897", nil); err != nil {
		t.Fatalf("设置全局代理失败: %v", err)
	}
	cur := sp.Current()
	if cur["server"] != "127.0.0.1:7897" || cur["enable"] != true {
		t.Errorf("全局代理未生效: %v", cur)
	}
	t.Logf("全局代理已设置: %v", cur["server"])

	if err := sp.SetPAC("http://127.0.0.1:7899/proxy.pac"); err != nil {
		t.Fatalf("设置 PAC 失败: %v", err)
	}
	cur = sp.Current()
	if cur["autoCfg"] != "http://127.0.0.1:7899/proxy.pac" {
		t.Errorf("PAC 未生效: %v", cur)
	}
	t.Logf("PAC 已设置: %v", cur["autoCfg"])

	if err := sp.Restore(snap); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	cur = sp.Current()
	t.Logf("还原后: enable=%v server=%q autoCfg=%q", cur["enable"], cur["server"], cur["autoCfg"])
	if snap.HasServer && cur["server"] != snap.Server {
		t.Errorf("还原 server 失败: 期望 %q 得到 %q", snap.Server, cur["server"])
	}
	if snap.HasAuto && cur["autoCfg"] != snap.AutoCfg {
		t.Errorf("还原 autoCfg 失败: 期望 %q 得到 %q", snap.AutoCfg, cur["autoCfg"])
	}
}

// TestConfigGen 验证内核配置生成。
func TestConfigGen(t *testing.T) {
	node := Node{
		Name: "test-vless", Protocol: "vless", Server: "1.2.3.4", Port: 443,
		Transport: "xhttp", Security: "reality",
		Raw: map[string]string{
			"id": "abc-123", "sni": "www.yahoo.com", "public-key": "pk123",
			"short-id": "ab12", "fingerprint": "chrome", "path": "/p", "mode": "auto",
		},
	}
	node.finalize()

	for _, mode := range []string{"gfw", "global", "rules"} {
		cfg := buildXrayConfig(node, mode, []string{"google.com"})
		if cfg == nil {
			t.Fatalf("Xray[%s] 配置为空", mode)
		}
		ob := cfg["outbounds"].([]map[string]any)
		if ob[0]["protocol"] != "vless" {
			t.Errorf("Xray[%s] 出站协议错误: %v", mode, ob[0]["protocol"])
		}
		routing := cfg["routing"].(map[string]any)
		rules := routing["rules"].([]map[string]any)
		t.Logf("Xray[%s]: %d 条路由规则", mode, len(rules))
	}

	// clash 配置
	y := buildClashConfig(node, "rules", []string{"google.com", "8.8.8.8", "104.16.0.0/12", "*.github.com"})
	for _, want := range []string{"mixed-port: 7897", "type: vless", "DOMAIN-SUFFIX,google.com",
		"IP-CIDR,8.8.8.8/32", "IP-CIDR,104.16.0.0/12", "DOMAIN-SUFFIX,github.com"} {
		if !strings.Contains(y, want) {
			t.Errorf("Clash 配置缺少: %s", want)
		}
	}
	t.Logf("Clash rules 配置:\n%s", y)

	// hysteria 节点 -> clash 配置
	hy := Node{Name: "hy", Protocol: "hysteria", Server: "5.6.7.8", Port: 1234,
		Raw: map[string]string{"auth-str": "auth1", "sni": "bing.com"}}
	hy.finalize()
	yh := buildClashConfig(hy, "global", nil)
	if !strings.Contains(yh, "type: hysteria") || !strings.Contains(yh, "auth-str: auth1") {
		t.Errorf("hysteria Clash 配置错误:\n%s", yh)
	}
	t.Logf("hysteria Clash 配置生成 %d 字节", len(yh))

	// 非 xray 支持的协议应回退
	rules := classifyRule("*.github.com")
	if rules != "DOMAIN-SUFFIX,github.com" {
		t.Errorf("通配符规则编译错误: %s", rules)
	}
	if r := classifyRule("8.8.8.8"); r != "IP-CIDR,8.8.8.8/32,no-resolve" {
		t.Errorf("IP 规则编译错误: %s", r)
	}
	if r := classifyRule("# note"); r != "" {
		t.Errorf("注释未被过滤: %s", r)
	}
}

// TestMergeKeepOld 验证「抓取失败时保留旧节点」的保底逻辑。
func TestMergeKeepOld(t *testing.T) {
	old := []Node{
		{ID: "aaa", Name: "old-node", Server: "1.1.1.1", Port: 1, Protocol: "vless"},
		{ID: "bbb", Name: "shared", Server: "2.2.2.2", Port: 2, Protocol: "vmess"},
	}
	fresh := []Node{
		{ID: "bbb", Name: "shared-updated", Server: "2.2.2.2", Port: 2, Protocol: "vmess"},
		{ID: "ccc", Name: "new-node", Server: "3.3.3.3", Port: 3, Protocol: "trojan"},
	}
	merged := mergeNodes(fresh, old)
	if len(merged) != 3 {
		t.Fatalf("合并结果应为 3 个，实际 %d", len(merged))
	}
	byID := map[string]Node{}
	for _, n := range merged {
		byID[n.ID] = n
	}
	if byID["aaa"].Name != "old-node" {
		t.Errorf("旧节点未被保留")
	}
	if byID["bbb"].Name != "shared-updated" {
		t.Errorf("新数据未覆盖旧数据: %s", byID["bbb"].Name)
	}
	if _, ok := byID["ccc"]; !ok {
		t.Errorf("新节点丢失")
	}
	t.Logf("合并保底逻辑正确: 旧节点保留 + 新数据优先")
}

// TestParseFormats 验证四种配置格式解析。
func TestParseFormats(t *testing.T) {
	clash := `mixed-port: 7890
proxies:
  - name: "node-a"
    type: hysteria
    server: 1.1.1.1
    port: 100
    auth-str: xyz
    sni: bing.com
    skip-cert-verify: true
  - name: node-b
    type: vless
    server: 2.2.2.2
    port: 200
proxy-groups:
  - name: G
    type: select
`
	got := parseClashYAML(clash, "test")
	if len(got) != 2 {
		t.Fatalf("clash 解析应得 2 个节点，实际 %d", len(got))
	}
	if got[0].Protocol != "hysteria" || got[0].Server != "1.1.1.1" || got[0].Port != 100 {
		t.Errorf("clash 节点1 解析错误: %+v", got[0])
	}
	if got[1].Protocol != "vless" || got[1].Port != 200 {
		t.Errorf("clash 节点2 解析错误: %+v", got[1])
	}

	xray := `{"outbounds":[
	  {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"3.3.3.3","port":443,"users":[{"id":"uuid-1"}]}]},
	   "streamSettings":{"network":"xhttp","security":"reality","realitySettings":{"serverName":"a.com","publicKey":"pk","shortId":"sid"}}},
	  {"tag":"direct","protocol":"freedom"}]}`
	xn := parseXrayJSON(xray, "test")
	if len(xn) != 1 {
		t.Fatalf("xray 解析应得 1 个节点，实际 %d", len(xn))
	}
	if xn[0].Server != "3.3.3.3" || xn[0].Port != 443 || xn[0].Raw["public-key"] != "pk" {
		t.Errorf("xray 解析错误: %+v", xn[0])
	}

	sb := `{"outbounds":[{"type":"hysteria","tag":"hy","server":"4.4.4.4","server_port":8080,
	   "auth_str":"au","tls":{"enabled":true,"server_name":"b.com","insecure":true}},
	   {"type":"direct","tag":"direct"}]}`
	sn := parseSingboxJSON(sb, "test")
	if len(sn) != 1 || sn[0].Server != "4.4.4.4" || sn[0].Port != 8080 {
		t.Fatalf("singbox 解析错误: %+v", sn)
	}

	hy := `{"server":"[2001:db8::1]:22000","auth":"dongtai","tls":{"sni":"m.com","insecure":true},"socks5":{"listen":"127.0.0.1:1080"}}`
	hn := parseHysteriaJSON(hy, "test", true)
	if len(hn) != 1 || hn[0].Port != 22000 {
		t.Fatalf("hysteria ipv6 解析错误: %+v", hn)
	}
	t.Logf("四种格式解析全部正确")
}

var _ = fmt.Sprintf
var _ = time.Now
