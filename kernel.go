package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// KernelManager 负责生成内核配置、启动/停止内核进程。
// 支持 Xray 与 Clash.Meta 两种内核（都可作为本地 HTTP 代理入口）。
type KernelManager struct {
	mu        sync.Mutex
	binDir    string // 内核可执行文件所在目录（用户放置 xray.exe / clash.meta.exe）
	workDir   string // 运行目录
	cmd       *exec.Cmd
	running   string // 当前运行的内核名
	proxyPort int    // 本地 HTTP 混合端口
}

const (
	localHTTPPort  = 7897 // 本程序本地 HTTP 代理端口
	localSOCKSPort = 7898
)

func NewKernelManager(binDir, workDir string) *KernelManager {
	return &KernelManager{binDir: binDir, workDir: workDir, proxyPort: localHTTPPort}
}

// FindBinary 在若干候选目录里查找内核可执行文件。
func (k *KernelManager) FindBinary(names ...string) (string, string) {
	dirs := []string{k.binDir, k.workDir, filepath.Join(k.workDir, "bin")}
	for _, name := range names {
		for _, d := range dirs {
			if d == "" {
				continue
			}
			p := filepath.Join(d, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, d
			}
		}
	}
	return "", ""
}

func (k *KernelManager) Available() map[string]string {
	out := map[string]string{}
	if p, _ := k.FindBinary("xray.exe", "xray-windows-amd64.exe"); p != "" {
		out["xray"] = p
	}
	if p, _ := k.FindBinary("clash.meta.exe", "clash.meta-windows-amd64.exe", "clash-meta.exe", "mihomo.exe"); p != "" {
		out["clash.meta"] = p
	}
	return out
}

func (k *KernelManager) Running() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cmd != nil && k.cmd.Process != nil {
		return k.running
	}
	return ""
}

// Start 用指定节点启动内核。
func (k *KernelManager) Start(kernel string, node Node, mode string, rules []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.stopLocked()

	if err := os.MkdirAll(k.workDir, 0o755); err != nil {
		return err
	}

	switch kernel {
	case "xray":
		bin, _ := k.FindBinary("xray.exe", "xray-windows-amd64.exe")
		if bin == "" {
			return fmt.Errorf("未找到 xray.exe，请把内核放到程序目录或 bin 子目录")
		}
		cfgPath := filepath.Join(k.workDir, "xray_runtime.json")
		cfg := buildXrayConfig(node, mode, rules)
		b, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
			return err
		}
		// 复制 geo 数据（若内核目录存在）
		cmd := exec.Command(bin, "run", "-c", cfgPath)
		cmd.Dir = filepath.Dir(bin)
		cmd.SysProcAttr = hiddenProcAttr()
		if err := cmd.Start(); err != nil {
			return err
		}
		k.cmd = cmd
		k.running = "xray"

	case "clash.meta":
		bin, binDir := k.FindBinary("clash.meta.exe", "clash.meta-windows-amd64.exe", "clash-meta.exe", "mihomo.exe")
		if bin == "" {
			return fmt.Errorf("未找到 clash.meta 内核，请把 clash.meta.exe 放到程序目录或 bin 子目录")
		}
		cfgPath := filepath.Join(k.workDir, "clash_runtime.yaml")
		cfg := buildClashConfig(node, mode, rules)
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
			return err
		}
		cmd := exec.Command(bin, "-d", filepath.Dir(cfgPath), "-f", cfgPath)
		cmd.Dir = binDir
		cmd.SysProcAttr = hiddenProcAttr()
		if err := cmd.Start(); err != nil {
			return err
		}
		k.cmd = cmd
		k.running = "clash.meta"

	default:
		return fmt.Errorf("不支持的内核: %s", kernel)
	}

	// 等待端口就绪
	if !waitPort(localHTTPPort, 8*time.Second) {
		k.stopLocked()
		return fmt.Errorf("内核启动后本地代理端口 %d 未就绪，请检查配置或查看日志", localHTTPPort)
	}
	return nil
}

func (k *KernelManager) Stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopLocked()
}

func (k *KernelManager) stopLocked() {
	if k.cmd != nil && k.cmd.Process != nil {
		_ = k.cmd.Process.Kill()
		_, _ = k.cmd.Process.Wait()
	}
	k.cmd = nil
	k.running = ""
}

func waitPort(port int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if portOpen(port) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// portOpen 检测本地端口是否已监听。
func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ---- 配置生成 ----

// buildXrayConfig 生成 Xray 运行配置：
// 入站 HTTP(7897)+SOCKS(7898)，出站使用所选节点，路由按模式决定。
func buildXrayConfig(node Node, mode string, rules []string) map[string]any {
	outbound := xrayOutbound(node)

	routingRules := []map[string]any{
		{"type": "field", "outboundTag": "block", "ip": []string{"geoip:private"}},
	}
	inboundTags := []string{"http-in", "socks-in"}

	switch mode {
	case "global":
		// 除本地外全部走代理
		routingRules = append(routingRules, map[string]any{
			"type": "field", "outboundTag": "direct",
			"domain": []string{"geosite:private"},
		})
		routingRules = append(routingRules, map[string]any{
			"type": "field", "outboundTag": "proxy",
			"inboundTag": inboundTags, "port": "0-65535",
		})
	case "gfw":
		// 仅国外/被墙站点走代理，其余直连
		routingRules = append(routingRules, map[string]any{
			"type": "field", "outboundTag": "direct",
			"domain": []string{"geosite:cn", "geosite:private"},
		})
		routingRules = append(routingRules, map[string]any{
			"type": "field", "outboundTag": "direct", "ip": []string{"geoip:cn", "geoip:private"},
		})
		routingRules = append(routingRules, map[string]any{
			"type": "field", "outboundTag": "proxy",
			"inboundTag": inboundTags, "port": "0-65535",
		})
	case "rules":
		// 仅规则列表内的目标走代理
		if len(rules) > 0 {
			routingRules = append(routingRules, map[string]any{
				"type": "field", "outboundTag": "proxy",
				"inboundTag": inboundTags, "domain": rules,
			})
		}
		routingRules = append(routingRules, map[string]any{
			"type": "field", "outboundTag": "direct",
			"inboundTag": inboundTags, "port": "0-65535",
		})
	}

	return map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []map[string]any{
			{
				"tag": "http-in", "port": localHTTPPort, "listen": "127.0.0.1",
				"protocol": "http",
				"settings": map[string]any{"auth": "noauth"},
				"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls"}},
			},
			{
				"tag": "socks-in", "port": localSOCKSPort, "listen": "127.0.0.1",
				"protocol": "socks",
				"settings": map[string]any{"auth": "noauth", "udp": true},
				"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls"}},
			},
		},
		"outbounds": []map[string]any{
			outbound,
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"},
		},
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"rules":          routingRules,
		},
	}
}

func xrayOutbound(node Node) map[string]any {
	switch strings.ToLower(node.Protocol) {
	case "vless":
		user := map[string]any{
			"id":         node.Raw["id"],
			"encryption": firstNonEmpty(node.Raw["encryption"], "none"),
		}
		if f := node.Raw["flow"]; f != "" {
			user["flow"] = f
		}
		stream := map[string]any{
			"network":  firstNonEmpty(node.Transport, "tcp"),
			"security": firstNonEmpty(node.Security, "none"),
		}
		if node.Security == "reality" {
			stream["realitySettings"] = map[string]any{
				"serverName":  node.Raw["sni"],
				"fingerprint": firstNonEmpty(node.Raw["fingerprint"], "chrome"),
				"publicKey":   node.Raw["public-key"],
				"shortId":     node.Raw["short-id"],
			}
		} else if node.Security == "tls" {
			stream["tlsSettings"] = map[string]any{
				"serverName": node.Raw["sni"],
				"allowInsecure": node.Raw["skip-cert-verify"] == "true",
			}
		}
		if node.Transport == "xhttp" {
			stream["xhttpSettings"] = map[string]any{
				"path": firstNonEmpty(node.Raw["path"], "/"),
				"mode": firstNonEmpty(node.Raw["mode"], "auto"),
			}
		}
		if node.Transport == "ws" {
			stream["wsSettings"] = map[string]any{
				"path": firstNonEmpty(node.Raw["path"], "/"),
				"host": node.Raw["host"],
			}
		}
		return map[string]any{
			"tag": "proxy", "protocol": "vless",
			"settings": map[string]any{
				"vnext": []map[string]any{{
					"address": node.Server, "port": node.Port, "users": []map[string]any{user},
				}},
			},
			"streamSettings": stream,
		}
	case "vmess":
		return map[string]any{
			"tag": "proxy", "protocol": "vmess",
			"settings": map[string]any{
				"vnext": []map[string]any{{
					"address": node.Server, "port": node.Port,
					"users": []map[string]any{{
						"id": node.Raw["id"], "alterId": 0,
						"security": firstNonEmpty(node.Raw["security"], "auto"),
					}},
				}},
			},
			"streamSettings": map[string]any{
				"network": firstNonEmpty(node.Transport, "tcp"),
				"security": firstNonEmpty(node.Security, "none"),
			},
		}
	case "trojan":
		return map[string]any{
			"tag": "proxy", "protocol": "trojan",
			"settings": map[string]any{
				"servers": []map[string]any{{
					"address": node.Server, "port": node.Port, "password": node.Raw["password"],
				}},
			},
			"streamSettings": map[string]any{
				"network": "tcp", "security": "tls",
				"tlsSettings": map[string]any{
					"serverName": node.Raw["sni"], "allowInsecure": true,
				},
			},
		}
	case "shadowsocks", "ss":
		return map[string]any{
			"tag": "proxy", "protocol": "shadowsocks",
			"settings": map[string]any{
				"servers": []map[string]any{{
					"address": node.Server, "port": node.Port,
					"method": node.Raw["method"], "password": node.Raw["password"],
				}},
			},
		}
	}
	// 兜底：当作直连以免崩溃
	return map[string]any{"tag": "proxy", "protocol": "freedom"}
}

// buildClashConfig 生成 clash.meta 运行配置。
func buildClashConfig(node Node, mode string, rules []string) string {
	var sb strings.Builder
	sb.WriteString("mixed-port: " + fmt.Sprintf("%d", localHTTPPort) + "\n")
	sb.WriteString("allow-lan: false\n")
	sb.WriteString("mode: rule\n")
	sb.WriteString("log-level: warning\n")
	sb.WriteString("dns:\n  enable: true\n  listen: 127.0.0.1:0\n")
	sb.WriteString("  nameserver:\n    - 119.29.29.29\n    - 223.5.5.5\n")
	sb.WriteString("  fallback:\n    - 8.8.8.8\n    - 1.1.1.1\n")

	sb.WriteString("proxies:\n")
	sb.WriteString(fmt.Sprintf("  - name: ProxyPilotNode\n    type: %s\n    server: %s\n    port: %d\n",
		clashType(node.Protocol), node.Server, node.Port))
	switch strings.ToLower(node.Protocol) {
	case "hysteria", "hysteria2":
		if node.Raw["auth-str"] != "" {
			sb.WriteString("    auth-str: " + node.Raw["auth-str"] + "\n")
		}
		if node.Raw["auth_str"] != "" {
			sb.WriteString("    auth-str: " + node.Raw["auth_str"] + "\n")
		}
		if node.Raw["password"] != "" {
			sb.WriteString("    password: " + node.Raw["password"] + "\n")
		}
		if node.Raw["sni"] != "" {
			sb.WriteString("    sni: " + node.Raw["sni"] + "\n")
		}
		sb.WriteString("    skip-cert-verify: true\n    alpn:\n      - h3\n")
	case "ss", "shadowsocks":
		sb.WriteString("    cipher: " + firstNonEmpty(node.Raw["cipher"], node.Raw["method"]) + "\n")
		sb.WriteString("    password: " + node.Raw["password"] + "\n")
	case "vless":
		sb.WriteString("    uuid: " + node.Raw["id"] + "\n")
		sb.WriteString("    network: " + firstNonEmpty(node.Transport, "tcp") + "\n")
		sb.WriteString("    tls: " + boolStr(node.Security == "tls" || node.Security == "reality") + "\n")
		sb.WriteString("    servername: " + node.Raw["sni"] + "\n")
	case "trojan":
		sb.WriteString("    password: " + node.Raw["password"] + "\n")
		sb.WriteString("    sni: " + node.Raw["sni"] + "\n")
		sb.WriteString("    skip-cert-verify: true\n")
	}

	sb.WriteString("proxy-groups:\n")
	sb.WriteString("  - name: PROXY\n    type: select\n    proxies:\n      - ProxyPilotNode\n      - DIRECT\n")

	sb.WriteString("rules:\n")
	switch mode {
	case "global":
		sb.WriteString("  - GEOIP,private,DIRECT\n")
		sb.WriteString("  - GEOSITE,private,DIRECT\n")
		sb.WriteString("  - MATCH,PROXY\n")
	case "gfw":
		sb.WriteString("  - GEOSITE,cn,DIRECT\n")
		sb.WriteString("  - GEOIP,CN,DIRECT\n")
		sb.WriteString("  - GEOSITE,private,DIRECT\n")
		sb.WriteString("  - MATCH,PROXY\n")
	case "rules":
		for _, r := range rules {
			t := classifyRule(r)
			if t == "" {
				continue
			}
			sb.WriteString("  - " + t + ",PROXY\n")
		}
		sb.WriteString("  - MATCH,DIRECT\n")
	}
	return sb.String()
}

func clashType(proto string) string {
	switch strings.ToLower(proto) {
	case "hysteria":
		return "hysteria"
	case "hysteria2":
		return "hysteria2"
	case "vless":
		return "vless"
	case "vmess":
		return "vmess"
	case "trojan":
		return "trojan"
	case "ss", "shadowsocks":
		return "ss"
	}
	return strings.ToLower(proto)
}

func classifyRule(r string) string {
	r = strings.TrimSpace(r)
	if r == "" || strings.HasPrefix(r, "#") {
		return ""
	}
	// 含 * 视为关键字/域名匹配
	if strings.ContainsAny(r, "*") {
		return "DOMAIN-SUFFIX," + strings.TrimPrefix(r, "*.")
	}
	// 纯 IP 或 IP 段
	if isIPLike(r) {
		return "IP-CIDR," + normalizeCIDR(r) + ",no-resolve"
	}
	return "DOMAIN-SUFFIX," + r
}

func isIPLike(s string) bool {
	host := s
	if i := strings.Index(s, "/"); i > 0 {
		host = s[:i]
	}
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func normalizeCIDR(s string) string {
	if strings.Contains(s, "/") {
		return s
	}
	return s + "/32"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
