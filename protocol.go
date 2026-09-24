package main

import "strings"

// 内核协议支持表。
//
// 内置内核为 Mihomo v1.19.31（amd64-compatible），实测支持下列全部协议。
// 说明：Mihomo 旧版本（如 v1.19.30 之前的 32 位构建）不支持 naiveproxy / juicity，
// 配错时内核会 fatal 退出并只报"端口未就绪"，用户完全看不懂。
// 因此这里显式声明，并在节点列表里标记不兼容项。
var kernelProtocols = map[string]map[string]bool{
	"clash.meta": {
		// 实测 v1.19.31 全部支持
		"ss": true, "ssr": true, "vmess": true, "vless": true,
		"trojan": true, "snell": true, "http": true, "socks5": true,
		"hysteria": true, "hysteria2": true, "tuic": true, "anytls": true,
		"mieru": true, "wireguard": true, "ssh": true, "direct": true,
		"naiveproxy": true, "juicity": true, "shadowquic": true,
	},
	"xray": {
		"vmess": true, "vless": true, "trojan": true,
		"shadowsocks": true, "ss": true, "socks": true, "http": true,
		"wireguard":  true,
		"naiveproxy": false, "hysteria": false, "hysteria2": false,
		"mieru": false, "juicity": false, "tuic": false, "anytls": false,
	},
}

// normalizeProtocol 把解析器产出的各种写法归一化。
func normalizeProtocol(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "shadowsocks", "shadowsocksr":
		return "ss"
	case "socks", "socks5":
		return "socks5"
	case "hysteria2", "hy2":
		return "hysteria2"
	}
	return p
}

// protocolSupported 判断某内核是否支持该协议。
// 未知协议一律认为不支持——宁可让用户看到明确提示，也不要内核静默 fatal。
func protocolSupported(kernel, proto string) bool {
	p := normalizeProtocol(proto)
	table, ok := kernelProtocols[kernel]
	if !ok {
		return true // 未知内核不做限制
	}
	supported, known := table[p]
	if !known {
		return false
	}
	return supported
}

// filterByKernel 过滤出指定内核支持的节点。
func filterByKernel(nodes []Node, kernel string) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if protocolSupported(kernel, n.Protocol) {
			out = append(out, n)
		}
	}
	return out
}

// pickCompatibleNode 在节点中挑一个该内核支持的；优先可用的、延迟低的。
func pickCompatibleNode(nodes []Node, kernel string) (Node, bool) {
	var best Node
	found := false
	for _, n := range nodes {
		if !protocolSupported(kernel, n.Protocol) {
			continue
		}
		if !found {
			best, found = n, true
			continue
		}
		// 可用的优先；都可用时延迟低的优先
		if n.OK && !best.OK {
			best = n
		} else if n.OK == best.OK {
			if n.Latency > 0 && (best.Latency <= 0 || n.Latency < best.Latency) {
				best = n
			}
		}
	}
	return best, found
}
