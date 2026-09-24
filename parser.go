package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ---- Clash.Meta config.yaml (只解析 proxies 段，避免引入 yaml 依赖) ----

// parseClashYAML 解析 clash.meta 的 config.yaml，提取 proxies 列表。
// 手写轻量解析：clash 配置结构非常规整，proxies 是顶层键，
// 其下每项以 "  - name: xxx" 开始，缩进 4 空格为字段。
func parseClashYAML(src string, sourceLabel string) []Node {
	var nodes []Node
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	inProxies := false
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		n := clashMapToNode(cur, sourceLabel)
		if n != nil {
			nodes = append(nodes, *n)
		}
		cur = nil
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// 顶层键
		if !strings.HasPrefix(line, " ") {
			flush()
			inProxies = trimmed == "proxies:"
			continue
		}
		if !inProxies {
			continue
		}

		indent := len(line) - len(trimmed)
		// 新节点项："  - name: xxx" (indent==2 且以 - 开头)
		if indent == 2 && strings.HasPrefix(trimmed, "- ") {
			flush()
			cur = map[string]string{}
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}
		if cur == nil {
			continue
		}
		// 忽略续行 "      - h3" 这种列表项里没有 key 的行
		idx := strings.Index(trimmed, ":")
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(trimmed[:idx])
		v := strings.TrimSpace(trimmed[idx+1:])
		v = strings.Trim(v, `"'`)
		if k == "alpn" && v == "" { // 数组头部，后续行单独处理
			continue
		}
		if _, exists := cur[k]; !exists {
			cur[k] = v
		}
	}
	flush()
	return nodes
}

func clashMapToNode(m map[string]string, sourceLabel string) *Node {
	server := m["server"]
	port, _ := strconv.Atoi(m["port"])
	if server == "" || port == 0 {
		return nil
	}
	proto := strings.ToLower(m["type"])
	switch proto {
	case "hysteria":
		proto = "hysteria"
	case "hysteria2", "hy2":
		proto = "hysteria2"
	case "ss", "shadowsocks":
		proto = "ss"
	case "":
		return nil
	}
	n := &Node{
		Name:     m["name"],
		Protocol: proto,
		Server:   server,
		Port:     port,
		Source:   sourceLabel,
		Raw:      map[string]string{},
	}
	switch proto {
	case "hysteria":
		n.Transport = "udp"
		n.Security = "tls"
		n.Raw["auth-str"] = m["auth-str"]
		n.Raw["sni"] = m["sni"]
		n.Raw["up"] = m["up"]
		n.Raw["down"] = m["down"]
		n.Raw["alpn"] = m["alpn"]
		n.Raw["skip-cert-verify"] = m["skip-cert-verify"]
	case "hysteria2":
		n.Transport = "udp"
		n.Security = "tls"
		n.Raw["password"] = m["password"]
		n.Raw["sni"] = m["sni"]
	case "ss":
		n.Transport = "tcp"
		n.Raw["cipher"] = m["cipher"]
		n.Raw["password"] = m["password"]
	}
	if n.Raw["sni"] == "" {
		n.Raw["sni"] = m["servername"]
	}
	n.finalize()
	return n
}

// ---- Xray config.json ----

type xrayConfig struct {
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
}

func parseXrayJSON(src string, sourceLabel string) []Node {
	var cfg xrayConfig
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		return nil
	}
	var nodes []Node
	for _, ob := range cfg.Outbounds {
		proto, _ := ob["protocol"].(string)
		if proto == "" || proto == "freedom" || proto == "blackhole" || proto == "dns" {
			continue
		}
		n := xrayOutboundToNode(ob, proto, sourceLabel)
		if n != nil {
			nodes = append(nodes, *n)
		}
	}
	return nodes
}

func xrayOutboundToNode(ob map[string]any, proto string, sourceLabel string) *Node {
	settings, _ := ob["settings"].(map[string]any)
	stream, _ := ob["streamSettings"].(map[string]any)
	tag, _ := ob["tag"].(string)

	n := &Node{Name: tag, Protocol: proto, Source: sourceLabel, Raw: map[string]string{}}

	switch proto {
	case "vless", "vmess":
		vnext, _ := settings["vnext"].([]any)
		if len(vnext) == 0 {
			return nil
		}
		first, _ := vnext[0].(map[string]any)
		n.Server, _ = first["address"].(string)
		n.Port = anyToInt(first["port"])
		users, _ := first["users"].([]any)
		if len(users) > 0 {
			u, _ := users[0].(map[string]any)
			if proto == "vless" {
				n.Raw["id"] = anyToString(u["id"])
				n.Raw["flow"] = anyToString(u["flow"])
				n.Raw["encryption"] = anyToString(u["encryption"])
			} else {
				n.Raw["id"] = anyToString(u["id"])
				n.Raw["security"] = anyToString(u["security"])
			}
		}
	case "trojan":
		servers, _ := settings["servers"].([]any)
		if len(servers) == 0 {
			return nil
		}
		first, _ := servers[0].(map[string]any)
		n.Server, _ = first["address"].(string)
		n.Port = anyToInt(first["port"])
		n.Raw["password"] = anyToString(first["password"])
	case "shadowsocks":
		servers, _ := settings["servers"].([]any)
		if len(servers) == 0 {
			return nil
		}
		first, _ := servers[0].(map[string]any)
		n.Server, _ = first["address"].(string)
		n.Port = anyToInt(first["port"])
		n.Raw["method"] = anyToString(first["method"])
		n.Raw["password"] = anyToString(first["password"])
	default:
		return nil
	}

	if n.Server == "" || n.Port == 0 {
		return nil
	}

	if stream != nil {
		if net, ok := stream["network"].(string); ok {
			n.Transport = net
		}
		if sec, ok := stream["security"].(string); ok {
			n.Security = sec
		}
		// sni
		if tls, ok := stream["tlsSettings"].(map[string]any); ok {
			n.Raw["sni"] = anyToString(tls["serverName"])
			n.Raw["alpn"] = anyToStringSlice(tls["alpn"])
		}
		if reality, ok := stream["realitySettings"].(map[string]any); ok {
			n.Raw["sni"] = anyToString(reality["serverName"])
			n.Raw["public-key"] = anyToString(reality["publicKey"])
			n.Raw["short-id"] = anyToString(reality["shortId"])
			n.Raw["fingerprint"] = anyToString(reality["fingerprint"])
		}
		if xhttp, ok := stream["xhttpSettings"].(map[string]any); ok {
			n.Raw["path"] = anyToString(xhttp["path"])
			n.Raw["mode"] = anyToString(xhttp["mode"])
		}
		if ws, ok := stream["wsSettings"].(map[string]any); ok {
			n.Raw["path"] = anyToString(ws["path"])
			n.Raw["host"] = anyToString(ws["host"])
		}
		if grpc, ok := stream["grpcSettings"].(map[string]any); ok {
			n.Raw["service-name"] = anyToString(grpc["serviceName"])
		}
	}
	n.finalize()
	return n
}

// ---- sing-box config.json ----

func parseSingboxJSON(src string, sourceLabel string) []Node {
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		return nil
	}
	var nodes []Node
	for _, ob := range cfg.Outbounds {
		proto, _ := ob["type"].(string)
		if proto == "" || proto == "direct" || proto == "block" || proto == "dns" || proto == "selector" || proto == "urltest" {
			continue
		}
		tag, _ := ob["tag"].(string)
		server, _ := ob["server"].(string)
		port := anyToInt(ob["server_port"])
		if server == "" || port == 0 {
			continue
		}
		n := Node{
			Name:      tag,
			Protocol:  normalizeProto(proto),
			Server:    server,
			Port:      port,
			Source:    sourceLabel,
			Transport: "udp",
			Raw:       map[string]string{},
		}
		if tls, ok := ob["tls"].(map[string]any); ok {
			if en, ok := tls["enabled"].(bool); ok && en {
				n.Security = "tls"
			}
			n.Raw["sni"] = anyToString(tls["server_name"])
			n.Raw["insecure"] = anyToString(tls["insecure"])
		}
		n.Raw["auth_str"] = anyToString(ob["auth_str"])
		if n.Raw["auth_str"] == "" {
			n.Raw["auth_str"] = anyToString(ob["password"])
		}
		n.Raw["up_mbps"] = anyToString(ob["up_mbps"])
		n.Raw["down_mbps"] = anyToString(ob["down_mbps"])
		n.Raw["uuid"] = anyToString(ob["uuid"])
		n.finalize()
		nodes = append(nodes, n)
	}
	return nodes
}

// ---- hysteria2 / hysteria 独立 config.json ----

func parseHysteriaJSON(src string, sourceLabel string, isV2 bool) []Node {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		return nil
	}
	server, _ := cfg["server"].(string)
	if server == "" {
		return nil
	}
	proto := "hysteria"
	if isV2 {
		proto = "hysteria2"
	}
	n := Node{
		Name:      sourceLabel + " " + proto,
		Protocol:  proto,
		Source:    sourceLabel,
		Transport: "udp",
		Security:  "tls",
		Raw:       map[string]string{},
	}
	// server 可能是 [ipv6]:port 或 ip:port 或纯 ip
	if strings.HasPrefix(server, "[") {
		if idx := strings.LastIndex(server, "]:"); idx > 0 {
			n.Server = server[:idx+1]
			n.Port = atoiSafe(server[idx+2:])
		} else {
			n.Server = server
		}
	} else if idx := strings.LastIndex(server, ":"); idx > 0 {
		n.Server = server[:idx]
		n.Port = atoiSafe(server[idx+1:])
	} else {
		n.Server = server
	}
	if n.Port == 0 {
		return nil
	}
	n.Raw["auth"] = anyToString(cfg["auth"])
	if tls, ok := cfg["tls"].(map[string]any); ok {
		n.Raw["sni"] = anyToString(tls["sni"])
		n.Raw["insecure"] = anyToString(tls["insecure"])
	}
	if socks, ok := cfg["socks5"].(map[string]any); ok {
		n.Raw["socks5-listen"] = anyToString(socks["listen"])
	}
	n.finalize()
	return []Node{n}
}

// ---- 工具函数 ----

func normalizeProto(p string) string {
	switch strings.ToLower(p) {
	case "hysteria":
		return "hysteria"
	case "hysteria2":
		return "hysteria2"
	case "vless", "vmess", "trojan", "shadowsocks", "ss", "juicity", "mieru", "shadowquic":
		return strings.ToLower(p)
	}
	return strings.ToLower(p)
}

func anyToInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		return atoiSafe(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	}
	return 0
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

func anyToStringSlice(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return anyToString(v)
	}
	parts := make([]string, 0, len(arr))
	for _, item := range arr {
		parts = append(parts, anyToString(item))
	}
	return strings.Join(parts, ",")
}

func atoiSafe(s string) int {
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return i
}

// ---- juicity config.json ----

// parseJuicityJSON 解析 juicity 配置。格式:
//
//	{ "listen":"127.0.0.1:1080", "server":"host:port", "uuid":"...", "password":"...", "sni":"..." }
func parseJuicityJSON(src string, sourceLabel string) []Node {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		return nil
	}
	server := anyToString(cfg["server"])
	if server == "" {
		return nil
	}
	host, port := splitHostPort(server)
	if host == "" || port == 0 {
		return nil
	}
	n := Node{
		Name: sourceLabel + " juicity", Protocol: "juicity",
		Server: host, Port: port, Source: sourceLabel,
		Transport: "quic", Security: "tls",
		Raw: map[string]string{
			"uuid":     anyToString(cfg["uuid"]),
			"password": anyToString(cfg["password"]),
			"sni":      anyToString(cfg["sni"]),
		},
	}
	n.finalize()
	return []Node{n}
}

// ---- naiveproxy config.json ----

// parseNaiveJSON 解析 naiveproxy 配置。格式:
//
//	{ "listen":"socks://127.0.0.1:1080",
//	  "proxy":"https://user:pass@host:port" }
func parseNaiveJSON(src string, sourceLabel string) []Node {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		return nil
	}
	proxy := anyToString(cfg["proxy"])
	if proxy == "" {
		return nil
	}
	// 去掉协议前缀
	rest := proxy
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	// 取出 user:pass@
	scheme := "https"
	userinfo := ""
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		userinfo = rest[:at]
		rest = rest[at+1:]
	}
	host, port := splitHostPort(rest)
	if host == "" || port == 0 {
		return nil
	}
	raw := map[string]string{"scheme": scheme}
	if userinfo != "" {
		parts := strings.SplitN(userinfo, ":", 2)
		raw["username"] = parts[0]
		if len(parts) > 1 {
			raw["password"] = parts[1]
		}
	}
	n := Node{
		Name: sourceLabel + " naiveproxy", Protocol: "naiveproxy",
		Server: host, Port: port, Source: sourceLabel,
		Transport: "tcp", Security: "tls", Raw: raw,
	}
	n.finalize()
	return []Node{n}
}

// ---- mieru config.json ----

// parseMieruJSON 解析 mieru 配置，提取 profiles[0].servers 中的节点。
func parseMieruJSON(src string, sourceLabel string) []Node {
	var cfg struct {
		Profiles []struct {
			ProfileName string `json:"profileName"`
			User        struct {
				Name     string `json:"name"`
				Password string `json:"password"`
			} `json:"user"`
			Servers []struct {
				IPAddress    string `json:"ipAddress"`
				DomainName   string `json:"domainName"`
				PortBindings []struct {
					Port     int    `json:"port"`
					Protocol string `json:"protocol"`
				} `json:"portBindings"`
			} `json:"servers"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		return nil
	}
	var out []Node
	for _, p := range cfg.Profiles {
		for _, s := range p.Servers {
			host := s.IPAddress
			if host == "" {
				host = s.DomainName
			}
			if host == "" {
				continue
			}
			for _, pb := range s.PortBindings {
				if pb.Port == 0 {
					continue
				}
				n := Node{
					Name: sourceLabel + " mieru", Protocol: "mieru",
					Server: host, Port: pb.Port, Source: sourceLabel,
					Transport: strings.ToLower(pb.Protocol), Security: "tls",
					Raw: map[string]string{
						"username": p.User.Name,
						"password": p.User.Password,
					},
				}
				if n.Transport == "" {
					n.Transport = "tcp"
				}
				n.finalize()
				out = append(out, n)
			}
		}
	}
	return out
}

// splitHostPort 拆分 "host:port" / "[ipv6]:port" / "host"。
func splitHostPort(s string) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}
	if strings.HasPrefix(s, "[") {
		if idx := strings.LastIndex(s, "]:"); idx > 0 {
			return s[:idx+1], atoiSafe(s[idx+2:])
		}
		return s, 0
	}
	if idx := strings.LastIndex(s, ":"); idx > 0 {
		if p := atoiSafe(s[idx+1:]); p > 0 {
			return s[:idx], p
		}
	}
	return s, 0
}

// parseByKind 按来源类型分发解析。
func parseByKind(kind, file, content, label string) []Node {
	switch kind {
	case "clash.meta":
		return parseClashYAML(content, label)
	case "xray":
		return parseXrayJSON(content, label)
	case "singbox":
		return parseSingboxJSON(content, label)
	case "hysteria2":
		return parseHysteriaJSON(content, label, true)
	case "hysteria":
		return parseHysteriaJSON(content, label, false)
	case "juicity":
		return parseJuicityJSON(content, label)
	case "naiveproxy":
		return parseNaiveJSON(content, label)
	case "mieru":
		return parseMieruJSON(content, label)
	}
	return nil
}
