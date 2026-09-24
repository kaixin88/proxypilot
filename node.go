package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Node 是归一化后的一个代理节点，屏蔽各内核配置格式的差异。
type Node struct {
	ID        string `json:"id"`        // 稳定指纹，用于跨次运行去重
	Name      string `json:"name"`      // 展示名
	Protocol  string `json:"protocol"`  // hysteria / hysteria2 / vless / vmess / trojan / ss / juicity ...
	Server    string `json:"server"`    // 服务器地址（ip 或域名）
	Port      int    `json:"port"`      // 端口
	Source    string `json:"source"`    // 来源标签，如 clash.meta-ip1
	Transport string `json:"transport"` // 传输层: tcp/quic/xhttp/ws/grpc
	Security  string `json:"security"`  // tls / reality / 空
	Raw       map[string]string `json:"raw,omitempty"` // 各协议特有的附加参数

	// 以下为运行时状态，不持久化语义
	Latency int  `json:"latency"` // 探测延迟(ms)，-1 表示不可用，0 表示未探测
	OK      bool `json:"ok"`      // 最近一次探测是否可用
}

// fingerprint 生成稳定 ID：同服务器+端口+协议即视为同一节点。
func fingerprint(n *Node) string {
	key := strings.ToLower(strings.Join([]string{n.Protocol, n.Server, strconv.Itoa(n.Port)}, "|"))
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:])[:16]
}

func (n *Node) finalize() {
	if n.Raw == nil {
		n.Raw = map[string]string{}
	}
	// 去掉名称里常见的营销后缀空白
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" {
		n.Name = fmt.Sprintf("%s://%s:%d", n.Protocol, n.Server, n.Port)
	}
	if n.ID == "" {
		n.ID = fingerprint(n)
	}
	if n.Latency == 0 {
		n.Latency = 0
	}
}

// Endpoint 返回 host:port。
func (n *Node) Endpoint() string {
	return fmt.Sprintf("%s:%d", n.Server, n.Port)
}

// MaskedEndpoint 用于界面展示。
func (n *Node) MaskedEndpoint() string {
	return fmt.Sprintf("%s:%d", maskHost(n.Server), n.Port)
}

func maskHost(host string) string {
	if host == "" {
		return "?"
	}
	// IPv6
	if strings.Contains(host, ":") {
		return host
	}
	parts := strings.Split(host, ".")
	if len(parts) == 4 {
		return parts[0] + ".*.*." + parts[3]
	}
	return host
}
