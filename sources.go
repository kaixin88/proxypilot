package main

import "fmt"

// NodeSource 描述一个云端节点配置来源。
// 参考原项目 D:\trae\Chrome153_AllNew_2026.9.12 的 ip_Update\ipN.bat 逻辑：
//
//	主源  https://gitlab.com/free9999/ipupdate/-/raw/master/backup/img/1/2/ipp/<dir>/<n>/<file>
//	备用  https://www.67867867.xyz/Alvin9999/PAC/refs/heads/master/backup/img/1/2/ipp/<dir>/<n>/<file>
//
// 原项目用 wget 依次尝试，本程序把「多 ip 源」全部抓下来做去重合并，
// 因此同一个内核可以同时拿到 ip1~ip6 的多组节点。
//
// 注意：远端目录名与本地目录名并不完全一致，必须以远端实际路径为准
// （例如本地是 clash.meta\，远端是 clash.meta2\）。
type NodeSource struct {
	Kind  string // 本地内核类型标识: xray / clash.meta / singbox / hysteria2 / hysteria
	Dir   string // 远端目录名，如 clash.meta2
	Index int    // 对应原项目的 ipN 编号
	File  string // config.json / config.yaml
}

// mirrors 返回该来源的所有镜像 URL（主源在前，备用在后）。
func (s NodeSource) mirrors() []string {
	name := s.path()
	return []string{
		"https://gitlab.com/free9999/ipupdate/-/raw/master/backup/img/1/2/ipp/" + name,
		"https://www.67867867.xyz/Alvin9999/PAC/refs/heads/master/backup/img/1/2/ipp/" + name,
		// GitHub 直连兜底（原项目未用，但同源仓库存在）
		"https://raw.githubusercontent.com/Alvin9999-newpac/PAC/master/backup/img/1/2/ipp/" + name,
	}
}

func (s NodeSource) path() string {
	return fmt.Sprintf("%s/%d/%s", s.Dir, s.Index, s.File)
}

// Label 给用户看的可读名称。
func (s NodeSource) Label() string {
	return fmt.Sprintf("%s-ip%d", s.Kind, s.Index)
}

// defaultSources 是内置的全部节点来源。
// 只保留能被 Go 原生解析的内核（Xray json / Clash.Meta yaml / sing-box json / hysteria2 json），
// 这样软件自身不依赖外部内核二进制即可完成「订阅解析 + 节点提取 + 直连探测」。
func defaultSources() []NodeSource {
	var out []NodeSource
	// Clash.Meta: ip1 ~ ip6 (远端目录 clash.meta2)
	for i := 1; i <= 6; i++ {
		out = append(out, NodeSource{Kind: "clash.meta", Dir: "clash.meta2", Index: i, File: "config.yaml"})
	}
	// Xray: ip1 ~ ip4
	for i := 1; i <= 4; i++ {
		out = append(out, NodeSource{Kind: "xray", Dir: "xray", Index: i, File: "config.json"})
	}
	// sing-box: ip1 ~ ip2
	for i := 1; i <= 2; i++ {
		out = append(out, NodeSource{Kind: "singbox", Dir: "singbox", Index: i, File: "config.json"})
	}
	// hysteria2: ip1 ~ ip4
	for i := 1; i <= 4; i++ {
		out = append(out, NodeSource{Kind: "hysteria2", Dir: "hysteria2", Index: i, File: "config.json"})
	}
	// hysteria (v1): ip1 ~ ip4
	for i := 1; i <= 4; i++ {
		out = append(out, NodeSource{Kind: "hysteria", Dir: "hysteria", Index: i, File: "config.json"})
	}
	// juicity / naiveproxy / mieru: ip1 ~ ip2（补充更多可用节点）
	for i := 1; i <= 2; i++ {
		out = append(out, NodeSource{Kind: "juicity", Dir: "juicity", Index: i, File: "config.json"})
		out = append(out, NodeSource{Kind: "naiveproxy", Dir: "naiveproxy", Index: i, File: "config.json"})
		out = append(out, NodeSource{Kind: "mieru", Dir: "mieru", Index: i, File: "config.json"})
	}
	return out
}
