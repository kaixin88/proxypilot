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

// findKernelExe 按关键字模糊匹配内核可执行文件。
// 用户手上的内核命名五花八门（clash.meta-windows.exe / mihomo-windows-amd64.exe
// / clash.meta-windows-386.exe ...），只按固定文件名找会漏掉，所以改为扫描目录。
// exclude 用于避免把 xray 认成 clash（反之亦然）。
func (k *KernelManager) findKernelExe(keywords []string, exclude []string) (string, string) {
	dirs := []string{k.binDir, filepath.Join(k.workDir, "bin"), k.workDir}
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			lower := strings.ToLower(e.Name())
			if !strings.HasSuffix(lower, ".exe") {
				continue
			}
			// 跳过本程序与内核无关的工具
			if strings.Contains(lower, "proxypilot") {
				continue
			}
			matched := false
			for _, kw := range keywords {
				if strings.Contains(lower, kw) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			bad := false
			for _, ex := range exclude {
				if strings.Contains(lower, ex) {
					bad = true
					break
				}
			}
			if bad {
				continue
			}
			return filepath.Join(d, e.Name()), d
		}
	}
	return "", ""
}

func (k *KernelManager) Available() map[string]string {
	out := map[string]string{}
	if p, _ := k.FindBinary("xray.exe", "xray-windows-amd64.exe"); p != "" {
		out["xray"] = p
	} else if p, _ := k.findKernelExe([]string{"xray"}, []string{"clash", "mihomo"}); p != "" {
		out["xray"] = p
	}
	if p, _ := k.FindBinary("clash.meta.exe", "clash.meta-windows-amd64.exe", "clash-meta.exe", "mihomo.exe"); p != "" {
		out["clash.meta"] = p
	} else if p, _ := k.findKernelExe(
		[]string{"clash.meta", "clash-meta", "clashmeta", "mihomo", "clash.meta-windows"},
		[]string{"xray", "v2ray"},
	); p != "" {
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
		bin, binDir := k.FindBinary("xray.exe", "xray-windows-amd64.exe")
		if bin == "" {
			bin, binDir = k.findKernelExe([]string{"xray"}, []string{"clash", "mihomo"})
		}
		if bin == "" {
			return fmt.Errorf("未找到 xray.exe，请把内核放到程序目录或 bin 子目录")
		}
		if binDir == "" {
			binDir = filepath.Dir(bin)
		}
		cfgPath := filepath.Join(k.workDir, "xray_runtime.json")
		cfg := buildXrayConfig(node, mode, rules)
		b, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
			return err
		}
		// xray 在自身目录查找 geoip.dat / geosite.dat
		k.copyGeoData(k.workDir, binDir)
		cmd := exec.Command(bin, "run", "-c", cfgPath)
		cmd.Dir = binDir
		cmd.SysProcAttr = hiddenProcAttr()
		if err := cmd.Start(); err != nil {
			return err
		}
		k.cmd = cmd
		k.running = "xray"

	case "clash.meta":
		bin, binDir := k.FindBinary("clash.meta.exe", "clash.meta-windows-amd64.exe", "clash-meta.exe", "mihomo.exe")
		if bin == "" {
			bin, binDir = k.findKernelExe(
				[]string{"clash.meta", "clash-meta", "clashmeta", "mihomo"},
				[]string{"xray", "v2ray"},
			)
		}
		if bin == "" {
			return fmt.Errorf("未找到 clash.meta 内核，请把 clash.meta.exe 放到程序目录或 bin 子目录")
		}
		if binDir == "" {
			binDir = filepath.Dir(bin)
		}
		cfgPath := filepath.Join(k.workDir, "clash_runtime.yaml")
		cfg := buildClashConfig(node, mode, rules)
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
			return err
		}
		// clash.meta 会在配置目录里找 Country.mmdb / GeoSite.dat，
		// 而用户通常把它们和内核放在一起，这里补齐到运行目录，否则内核起不来。
		k.copyGeoData(binDir, k.workDir)
		cmd := exec.Command(bin, "-d", k.workDir, "-f", cfgPath)
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

// copyGeoData 把内核目录里的 GeoIP/GeoSite 数据复制到运行目录。
// clash.meta 从 `-d` 指定的目录读取 geosite.dat / geoip.dat（或 Country.mmdb），
// 缺失时启动会尝试联网下载，离线环境下会直接报错退出。
func (k *KernelManager) copyGeoData(srcDir, dstDir string) {
	if srcDir == "" || dstDir == "" || srcDir == dstDir {
		return
	}
	names := []string{
		"geosite.dat", "GeoSite.dat",
		"geoip.dat", "GeoIP.dat",
		"geoip.metadb", "Country.mmdb", "country.mmdb",
	}
	for _, n := range names {
		src := filepath.Join(srcDir, n)
		if st, err := os.Stat(src); err != nil || st.IsDir() {
			continue
		}
		dst := filepath.Join(dstDir, n)
		if _, err := os.Stat(dst); err == nil {
			continue // 已存在，不覆盖
		}
		if data, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(dst, data, 0o644)
		}
	}
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
				"serverName":    node.Raw["sni"],
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
				"network":  firstNonEmpty(node.Transport, "tcp"),
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
	// 用 .dat 版 GeoIP/GeoSite，并禁止内核联网下载。
	// 默认 MMDB 模式在找不到 Country.mmdb 时会尝试联网下载，
	// 受限网络下会卡住导致端口起不来（表现为"端口未就绪"）。
	sb.WriteString("geodata-mode: true\n")
	sb.WriteString("geodata-loader: standard\n")
	sb.WriteString("geo-auto-update: false\n")
	sb.WriteString("dns:\n  enable: true\n  listen: 127.0.0.1:0\n")
	sb.WriteString("  nameserver:\n    - 119.29.29.29\n    - 223.5.5.5\n")
	sb.WriteString("  fallback:\n    - 8.8.8.8\n    - 1.1.1.1\n")

	sb.WriteString("proxies:\n")
	sb.WriteString(fmt.Sprintf("  - name: ProxyPilotNode\n    type: %s\n    server: %s\n    port: %d\n",
		clashType(node.Protocol), yamlVal(node.Server), node.Port))
	sb.WriteString(clashProxyExtra(node))

	sb.WriteString("proxy-groups:\n")
	sb.WriteString("  - name: PROXY\n    type: select\n    proxies:\n      - ProxyPilotNode\n      - DIRECT\n")

	sb.WriteString("rules:\n")
	switch mode {
	case "global":
		sb.WriteString("  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve\n")
		sb.WriteString("  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve\n")
		sb.WriteString("  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve\n")
		sb.WriteString("  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve\n")
		sb.WriteString("  - MATCH,PROXY\n")
	case "gfw":
		sb.WriteString("  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve\n")
		sb.WriteString("  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve\n")
		sb.WriteString("  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve\n")
		sb.WriteString("  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve\n")
		// 国内直连：优先用 geo 数据，缺失时内核会跳过该条（非致命）
		sb.WriteString("  - GEOSITE,cn,DIRECT\n")
		sb.WriteString("  - GEOIP,CN,DIRECT\n")
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
	case "socks", "socks5":
		return "socks5"
	}
	return strings.ToLower(proto)
}

// clashProxyExtra 输出各协议在 clash 配置里必需的附加字段。
//
// 这一步非常关键：内核会对缺失字段直接 fatal 退出（例如
// mieru 要求 username/password/transport），表现为"端口未就绪"，
// 用户完全无法理解。所以每个支持的协议都必须补齐自己的必填项。
func clashProxyExtra(node Node) string {
	var b strings.Builder
	w := func(k, v string) {
		if v != "" {
			b.WriteString("    " + k + ": " + yamlVal(v) + "\n")
		}
	}
	wb := func(k string, v bool) {
		b.WriteString("    " + k + ": " + boolStr(v) + "\n")
	}
	raw := node.Raw
	proto := strings.ToLower(node.Protocol)

	switch proto {
	case "hysteria", "hysteria2":
		w("auth-str", firstNonEmpty(raw["auth-str"], raw["auth_str"]))
		w("auth", firstNonEmpty(raw["auth"], raw["password"]))
		w("password", raw["password"])
		w("sni", firstNonEmpty(raw["sni"], raw["peer"]))
		w("obfs", raw["obfs"])
		w("obfs-password", raw["obfs-password"])
		w("up", firstNonEmpty(raw["up"], raw["upmbps"]))
		w("down", firstNonEmpty(raw["down"], raw["downmbps"]))
		wb("skip-cert-verify", true)

	case "ss", "shadowsocks":
		w("cipher", firstNonEmpty(raw["cipher"], raw["method"]))
		w("password", raw["password"])
		w("plugin", raw["plugin"])
		w("plugin-opts", raw["plugin-opts"])

	case "ssr":
		w("cipher", firstNonEmpty(raw["cipher"], raw["method"]))
		w("password", raw["password"])
		w("protocol", raw["protocol"])
		w("obfs", raw["obfs"])
		w("protocol-param", raw["protocol-param"])
		w("obfs-param", raw["obfs-param"])

	case "vmess":
		w("uuid", firstNonEmpty(raw["uuid"], raw["id"]))
		w("alterId", firstNonEmpty(raw["alterId"], "0"))
		w("cipher", firstNonEmpty(raw["cipher"], "auto"))
		w("network", firstNonEmpty(node.Transport, raw["network"], "tcp"))
		w("servername", raw["sni"])
		writeWSOpts(&b, raw, node)

	case "vless":
		w("uuid", firstNonEmpty(raw["uuid"], raw["id"]))
		w("network", firstNonEmpty(node.Transport, raw["network"], "tcp"))
		w("flow", raw["flow"])
		w("servername", firstNonEmpty(raw["sni"], raw["servername"]))
		w("client-fingerprint", raw["fp"])
		w("reality-opts", "")
		if node.Security == "reality" || raw["pbk"] != "" {
			b.WriteString("    reality-opts:\n")
			if pbk := firstNonEmpty(raw["pbk"], raw["public-key"]); pbk != "" {
				b.WriteString("      public-key: " + yamlVal(pbk) + "\n")
			}
			if sid := raw["sid"]; sid != "" {
				b.WriteString("      short-id: " + yamlVal(sid) + "\n")
			}
		}
		wb("tls", node.Security == "tls" || node.Security == "reality")
		wb("skip-cert-verify", true)
		writeWSOpts(&b, raw, node)

	case "trojan":
		w("password", raw["password"])
		w("sni", firstNonEmpty(raw["sni"], raw["peer"]))
		w("network", firstNonEmpty(node.Transport, raw["network"]))
		w("alpn", raw["alpn"])
		wb("skip-cert-verify", true)
		writeWSOpts(&b, raw, node)

	case "mieru":
		// mieru 三个必填：username / password / transport
		w("username", firstNonEmpty(raw["username"], raw["user"]))
		w("password", raw["password"])
		// clash 要求大写 TCP/UDP
		w("transport", strings.ToUpper(firstNonEmpty(raw["transport"], node.Transport, "TCP")))
		wb("skip-cert-verify", true)

	case "anytls":
		w("password", raw["password"])
		w("sni", firstNonEmpty(raw["sni"], raw["peer"]))
		wb("skip-cert-verify", true)

	case "tuic":
		w("uuid", firstNonEmpty(raw["uuid"], raw["id"]))
		w("password", raw["password"])
		w("sni", firstNonEmpty(raw["sni"], raw["peer"]))
		w("alpn", raw["alpn"])
		wb("skip-cert-verify", true)

	case "snell":
		w("psk", firstNonEmpty(raw["psk"], raw["password"]))
		w("version", firstNonEmpty(raw["version"], "1"))

	case "socks5", "http":
		w("username", firstNonEmpty(raw["username"], raw["user"]))
		w("password", raw["password"])
		wb("tls", node.Security == "tls")

	case "wireguard":
		w("private-key", firstNonEmpty(raw["private-key"], raw["secretKey"]))
		w("public-key", raw["public-key"])
		w("server", node.Server)
		w("pre-shared-key", raw["pre-shared-key"])
		w("ip", raw["ip"])
		w("reserved", raw["reserved"])
		w("mtu", raw["mtu"])
		w("udp", "true")
	}
	return b.String()
}

// writeWSOpts 输出 ws/grpc/h2 等传输层参数。
func writeWSOpts(b *strings.Builder, raw map[string]string, node Node) {
	net := strings.ToLower(firstNonEmpty(node.Transport, raw["network"]))
	switch net {
	case "ws":
		b.WriteString("    ws-opts:\n")
		path := firstNonEmpty(raw["path"], "/")
		b.WriteString("      path: " + yamlVal(path) + "\n")
		host := firstNonEmpty(raw["host"], raw["sni"])
		if host != "" {
			b.WriteString("      headers:\n        Host: " + yamlVal(host) + "\n")
		}
	case "grpc":
		b.WriteString("    grpc-opts:\n")
		if sn := firstNonEmpty(raw["serviceName"], raw["servicename"]); sn != "" {
			b.WriteString("      grpc-service-name: " + yamlVal(sn) + "\n")
		}
	case "h2", "http":
		b.WriteString("    h2-opts:\n")
		if h := firstNonEmpty(raw["host"]); h != "" {
			b.WriteString("      host:\n        - " + yamlVal(h) + "\n")
		}
		if p := firstNonEmpty(raw["path"], "/"); p != "" {
			b.WriteString("      path: " + yamlVal(p) + "\n")
		}
	}
}

// yamlVal 对需要引号的值做安全包裹，避免特殊字符破坏 YAML。
func yamlVal(s string) string {
	if s == "" {
		return ""
	}
	needQuote := strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`,") ||
		strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") ||
		strings.Contains(s, "\n")
	if !needQuote {
		return s
	}
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\"") + "\""
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
