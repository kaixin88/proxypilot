package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NodeManager 负责：抓取云端节点 -> 解析 -> 合并去重 -> 可用性探测 -> 持久化。
type NodeManager struct {
	mu       sync.RWMutex
	nodes    []Node
	dataDir  string
	client   *http.Client
	lastSync time.Time
	syncing  bool
	progress string
}

func NewNodeManager(dataDir string) *NodeManager {
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	}
	return &NodeManager{
		dataDir: dataDir,
		client: &http.Client{
			Transport: tr,
			Timeout:   25 * time.Second,
		},
	}
}

func (m *NodeManager) nodesFile() string { return filepath.Join(m.dataDir, "nodes.json") }

// Load 从磁盘载入上次保存的节点（含可用状态）。
func (m *NodeManager) Load() error {
	b, err := os.ReadFile(m.nodesFile())
	if err != nil {
		return err
	}
	var saved []Node
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	for i := range saved {
		if saved[i].ID == "" {
			saved[i].ID = fingerprint(&saved[i])
		}
	}
	m.mu.Lock()
	m.nodes = saved
	m.mu.Unlock()
	return nil
}

func (m *NodeManager) Save() error {
	m.mu.RLock()
	b, err := json.MarshalIndent(m.nodes, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dataDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.nodesFile(), b, 0o644)
}

// List 返回当前节点快照。
func (m *NodeManager) List() []Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Node, len(m.nodes))
	copy(out, m.nodes)
	return out
}

func (m *NodeManager) Get(id string) (Node, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, n := range m.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func (m *NodeManager) Status() (int, bool, string, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.nodes), m.syncing, m.progress, m.lastSync
}

func (m *NodeManager) setProgress(s string) {
	m.mu.Lock()
	m.progress = s
	m.mu.Unlock()
}

// SyncOptions 控制一次同步的行为。
type SyncOptions struct {
	Sources    []NodeSource // 要抓取的来源
	Probe      bool         // 是否做可用性探测
	TimeoutMs  int          // 单个节点探测超时
	KeepFailed bool         // 是否保留探测失败的节点（标记不可用）
}

// SyncFull 抓取所有默认来源，合并去重，探测，并按策略保留/剔除。
func (m *NodeManager) SyncFull(probe bool) (SyncResult, error) {
	return m.Sync(SyncOptions{
		Sources:    defaultSources(),
		Probe:      probe,
		TimeoutMs:  2500,
		KeepFailed: false,
	})
}

type SyncResult struct {
	FetchedSources int    `json:"fetchedSources"`
	TotalSources   int    `json:"totalSources"`
	Parsed         int    `json:"parsed"`
	Merged         int    `json:"merged"`
	Alive          int    `json:"alive"`
	Removed        int    `json:"removed"`
	Message        string `json:"message"`
}

func (m *NodeManager) Sync(opt SyncOptions) (SyncResult, error) {
	m.mu.Lock()
	if m.syncing {
		m.mu.Unlock()
		return SyncResult{}, fmt.Errorf("正在同步中，请稍候")
	}
	m.syncing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.syncing = false
		m.progress = ""
		m.mu.Unlock()
	}()

	res := SyncResult{TotalSources: len(opt.Sources)}

	// 1. 并发抓取所有来源
	type fetched struct {
		label string
		kind  string
		body  string
		ok    bool
	}
	results := make([]fetched, len(opt.Sources))
	var wg sync.WaitGroup
	for i, s := range opt.Sources {
		wg.Add(1)
		go func(i int, s NodeSource) {
			defer wg.Done()
			m.setProgress(fmt.Sprintf("正在获取 %s ...", s.Label()))
			body, ok := m.fetchSource(s)
			results[i] = fetched{label: s.Label(), kind: s.Kind, body: body, ok: ok}
		}(i, s)
	}
	wg.Wait()

	// 2. 解析
	var parsed []Node
	for _, r := range results {
		if !r.ok || r.body == "" {
			continue
		}
		res.FetchedSources++
		nodes := parseByKind(r.kind, "", r.body, r.label)
		parsed = append(parsed, nodes...)
	}
	res.Parsed = len(parsed)

	// 3. 合并：保底留下旧节点（防止某次抓取失败导致节点全无）
	m.mu.RLock()
	old := make([]Node, len(m.nodes))
	copy(old, m.nodes)
	m.mu.RUnlock()

	merged := mergeNodes(parsed, old)
	res.Merged = len(merged)

	// 4. 探测可用性
	if opt.Probe {
		m.setProgress(fmt.Sprintf("正在探测 %d 个节点可用性 ...", len(merged)))
		probeAll(merged, opt.TimeoutMs)
		for i := range merged {
			if !merged[i].OK {
				merged[i].Latency = -1
			}
		}
	}

	// 5. 按策略保留。
	// 注意：UDP/QUIC 类节点（hysteria 等）无法通过探测得到确定性结论，
	// 即使探测失败也保留（标记为待验证），否则会把大量可用节点误删。
	final := make([]Node, 0, len(merged))
	for _, n := range merged {
		if opt.Probe && !n.OK && !opt.KeepFailed && !isUDPProto(n) {
			res.Removed++
			continue
		}
		final = append(final, n)
	}

	// 排序：可用优先，延迟低的靠前
	sort.SliceStable(final, func(i, j int) bool {
		a, b := final[i], final[j]
		if a.OK != b.OK {
			return a.OK
		}
		la, lb := latencyRank(a), latencyRank(b)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})

	m.mu.Lock()
	m.nodes = final
	m.lastSync = time.Now()
	m.mu.Unlock()

	_ = m.Save()

	if opt.Probe {
		for _, n := range final {
			if n.OK {
				res.Alive++
			}
		}
	} else {
		res.Alive = len(final)
	}

	switch {
	case res.FetchedSources == 0:
		res.Message = "未能获取到任何节点源，请检查网络或稍后重试"
	case res.Parsed == 0:
		res.Message = "节点源已获取，但未解析出可用节点（源内容可能已变更）"
	default:
		res.Message = fmt.Sprintf("同步完成：%d/%d 个源可用，合并 %d 个节点，可用 %d 个",
			res.FetchedSources, res.TotalSources, res.Merged, res.Alive)
	}
	return res, nil
}

func latencyRank(n Node) int {
	if n.Latency <= 0 {
		return 1 << 30
	}
	return n.Latency
}

// fetchSource 依次尝试多个镜像，返回第一个成功且内容有效的响应。
func (m *NodeManager) fetchSource(s NodeSource) (string, bool) {
	for _, url := range s.mirrors() {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) ProxyPilot/1.0")
		resp, err := m.client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			continue
		}
		txt := string(body)
		if len(txt) < 20 {
			continue
		}
		if !looksLikeConfig(txt, s.File) {
			continue
		}
		return txt, true
	}
	return "", false
}

// looksLikeConfig 粗筛响应内容，避免把 404 页面当成配置。
// 只做「整份内容」级别的关键字判断——不能只看开头若干字节，
// 因为 Xray 的 outbounds 段可能出现在很靠后的位置（前面有大段 dns 配置）。
func looksLikeConfig(txt, file string) bool {
	if !strings.ContainsRune(txt, '\n') && len(txt) < 40 {
		return false
	}
	// 排除 HTML 错误页
	if strings.HasPrefix(strings.TrimSpace(txt), "<") {
		return false
	}
	if file == "config.yaml" {
		return strings.Contains(txt, "proxies:")
	}
	// json 类配置
	return strings.Contains(txt, "{") && strings.Contains(txt, "}")
}

// mergeNodes 以 ID 去重；新抓取的结果优先，但保留旧节点的可用性信息。
func mergeNodes(fresh []Node, old []Node) []Node {
	byID := map[string]Node{}
	order := []string{}

	add := func(n Node) {
		if n.ID == "" {
			n.finalize()
		}
		if _, ok := byID[n.ID]; !ok {
			order = append(order, n.ID)
		}
		byID[n.ID] = n
	}

	for _, n := range fresh {
		add(n)
	}
	// 旧节点：仅当本次没抓到同 ID 时补进来（保底），并沿用其延迟记录
	for _, n := range old {
		if _, exists := byID[n.ID]; !exists {
			add(n)
		}
	}
	out := make([]Node, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// ---- 可用性探测 ----

func probeAll(nodes []Node, timeoutMs int) {
	if timeoutMs <= 0 {
		timeoutMs = 2500
	}
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			ms, ok := probeNode(nodes[i], timeoutMs)
			nodes[i].Latency = ms
			nodes[i].OK = ok
		}(i)
	}
	wg.Wait()
}

// probeNode 按协议选择合适的探测方式：
//
//	TCP 类（vless/vmess/trojan/ss/naiveproxy/mieru） -> TCP 三次握手时延
//	UDP 类（hysteria/hysteria2/juicity/shadowquic）  -> UDP 可达性（无法握手，只能测网络层）
//
// UDP 类节点用 TCP 探测会产生大量假阴性，所以必须区分对待。
func probeNode(n Node, timeoutMs int) (int, bool) {
	if isUDPProto(n) {
		return probeUDP(n, timeoutMs)
	}
	return probeTCP(n, timeoutMs)
}

// isUDPProto 判断节点是否基于 QUIC/UDP 传输。
func isUDPProto(n Node) bool {
	switch strings.ToLower(n.Protocol) {
	case "hysteria", "hysteria2", "juicity", "shadowquic", "quic", "tuic":
		return true
	}
	return strings.Contains(strings.ToLower(n.Transport), "udp")
}

// probeTCP 测量 TCP 握手时延。
func probeTCP(n Node, timeoutMs int) (int, bool) {
	timeout := time.Duration(timeoutMs) * time.Millisecond
	addr := net.JoinHostPort(strings.Trim(n.Server, "[]"), strconv.Itoa(n.Port))

	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return -1, false
	}
	lat := int(time.Since(start).Milliseconds())
	_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	_ = conn.Close()
	if lat <= 0 {
		lat = 1
	}
	return lat, true
}

// probeUDP 测试 UDP 可达性。
// QUIC 服务端在收到非法握手包时通常不会返回 ICMP 端口不可达，
// 因此「没有被立刻拒绝」即视为可达；若收到 ICMP unreachable 则判为不可用。
func probeUDP(n Node, timeoutMs int) (int, bool) {
	timeout := time.Duration(timeoutMs) * time.Millisecond
	serverAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(strings.Trim(n.Server, "[]"), strconv.Itoa(n.Port)))
	if err != nil {
		return -1, false
	}
	conn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		return -1, false
	}
	defer conn.Close()

	// 发一个最小 QUIC Initial 风格的探测包（仅用于触发服务端响应/ICMP）
	payload := []byte{0xc0, 0x00, 0x00, 0x00, 0x01, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	start := time.Now()
	if _, err := conn.Write(payload); err != nil {
		return -1, false
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	buf := make([]byte, 1500)
	nRead, err := conn.Read(buf)
	lat := int(time.Since(start).Milliseconds())
	if lat <= 0 {
		lat = 1
	}
	if err == nil && nRead > 0 {
		// 服务端有回包，明确可达
		return lat, true
	}
	// 读取超时：可能是服务端静默（QUIC 常见），也可能是丢包。
	// 只要不是「连接被拒绝」类错误，就按可达处理。
	if isRefusedErr(err) {
		return -1, false
	}
	// 超时视为可达（QUIC 服务端通常不回非法包）
	return -1, true
}

// isRefusedErr 判断是否为「明确拒绝」类错误（ICMP port unreachable / conn refused）。
func isRefusedErr(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "refused") ||
		strings.Contains(msg, "unreachable") ||
		strings.Contains(msg, "forcibly closed") ||
		strings.Contains(msg, "reset")
}
