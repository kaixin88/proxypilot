package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// PACServer 提供本地 PAC 脚本，实现「指定国外网址才走代理」与「指定网站走代理」两种模式。
type PACServer struct {
	mu       sync.RWMutex
	mode     string   // gfw=国外才走代理; rules=仅列表走代理
	rules    []string // 用户规则列表（支持 ip、域名、通配符 *）
	proxyHost string
	proxyPort int
	srv      *http.Server
	port     int
}

func NewPACServer(proxyHost string, proxyPort int) *PACServer {
	return &PACServer{mode: "gfw", proxyHost: proxyHost, proxyPort: proxyPort, port: 7899}
}

func (p *PACServer) Update(mode string, rules []string, proxyHost string, proxyPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	p.rules = append([]string(nil), rules...)
	p.proxyHost = proxyHost
	p.proxyPort = proxyPort
}

func (p *PACServer) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/proxy.pac", p.handlePAC)
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", p.port), Handler: mux}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	p.srv = srv
	go srv.Serve(ln)
	return nil
}

func (p *PACServer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		_ = p.srv.Close()
		p.srv = nil
	}
}

func (p *PACServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/proxy.pac", p.port)
}

func (p *PACServer) Port() int { return p.port }

func (p *PACServer) handlePAC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(p.script()))
}

// script 动态生成 PAC 脚本。规则在每次请求时读取，改列表无需重启。
func (p *PACServer) script() string {
	p.mu.RLock()
	mode := p.mode
	rules := append([]string(nil), p.rules...)
	proxy := fmt.Sprintf("PROXY %s:%d", p.proxyHost, p.proxyPort)
	p.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("// ProxyPilot PAC\n")
	sb.WriteString(fmt.Sprintf("var PROXY = \"%s\";\n", proxy))
	sb.WriteString("var DIRECT = \"DIRECT\";\n\n")

	// 本地/内网一律直连
	sb.WriteString(`function isLocal(host) {
  if (!host) return true;
  if (host === "localhost" || host === "127.0.0.1" || host === "::1") return true;
  if (/^10\./.test(host)) return true;
  if (/^192\.168\./.test(host)) return true;
  if (/^169\.254\./.test(host)) return true;
  if (/^172\.(1[6-9]|2[0-9]|3[01])\./.test(host)) return true;
  if (/^fc00:/i.test(host) || /^fe80:/i.test(host)) return true;
  return false;
}

function isIP(host) {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.indexOf(":") >= 0;
}

function ipToLong(ip) {
  var p = ip.split(".");
  if (p.length !== 4) return -1;
  var n = 0;
  for (var i = 0; i < 4; i++) {
    var v = parseInt(p[i], 10);
    if (isNaN(v) || v < 0 || v > 255) return -1;
    n = n * 256 + v;
  }
  return n;
}

function ipInCIDR(ip, cidr) {
  var parts = cidr.split("/");
  if (parts.length !== 2) return ip === cidr;
  var base = ipToLong(parts[0]);
  var cur = ipToLong(ip);
  if (base < 0 || cur < 0) return false;
  var bits = parseInt(parts[1], 10);
  if (isNaN(bits) || bits < 0 || bits > 32) return false;
  var mask = bits === 0 ? 0 : (~0 << (32 - bits)) >>> 0;
  return ((base & mask) >>> 0) === ((cur & mask) >>> 0);
}

// 通配符匹配: * 匹配任意字符
function wildcardMatch(pattern, str) {
  var re = "^" + pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*") + "$";
  return new RegExp(re, "i").test(str);
}
`)

	// 中国域名/常见国内域名列表（用于 gfw 模式判断）
	sb.WriteString("\nvar CN_DOMAIN_SUFFIX = [\n")
	for _, d := range cnDomainSuffixes {
		sb.WriteString(fmt.Sprintf("  \"%s\",\n", d))
	}
	sb.WriteString("];\n\n")

	sb.WriteString(`function inList(host, list) {
  for (var i = 0; i < list.length; i++) {
    var suf = list[i];
    if (host === suf || host.lastIndexOf("." + suf) === host.length - suf.length - 1) return true;
  }
  return false;
}

function matchesRules(host, rules) {
  for (var i = 0; i < rules.length; i++) {
    var r = rules[i];
    if (!r) continue;
    if (r.indexOf("/") >= 0) { if (ipInCIDR(host, r)) return true; continue; }
    if (isIP(r)) { if (isIP(host) && ipInCIDR(host + "/32", r + "/32")) return true; continue; }
    if (wildcardMatch(r, host)) return true;
  }
  return false;
}

function FindProxyForURL(url, host) {
  if (isLocal(host)) return DIRECT;
`)

	switch mode {
	case "rules":
		sb.WriteString("  var RULES = " + jsRuleArray(rules) + ";\n")
		sb.WriteString("  if (matchesRules(host, RULES)) return PROXY;\n")
		sb.WriteString("  return DIRECT;\n")
	default: // gfw: 国外才走代理
		sb.WriteString("  if (inList(host, CN_DOMAIN_SUFFIX)) return DIRECT;\n")
		sb.WriteString("  if (isIP(host)) {\n")
		sb.WriteString("    if (ipInCIDR(host, \"10.0.0.0/8\") || ipInCIDR(host, \"172.16.0.0/12\") ||\n")
		sb.WriteString("        ipInCIDR(host, \"192.168.0.0/16\") || ipInCIDR(host, \"127.0.0.0/8\")) return DIRECT;\n")
		sb.WriteString("    return PROXY;\n") // 国外 IP 走代理
		sb.WriteString("  }\n")
		sb.WriteString("  return PROXY;\n")
	}

	sb.WriteString("}\n")
	return sb.String()
}

func jsRuleArray(rules []string) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" || strings.HasPrefix(r, "#") {
			continue
		}
		r = strings.ReplaceAll(r, `"`, `\"`)
		parts = append(parts, fmt.Sprintf("%q", r))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// cnDomainSuffixes 常用国内域名后缀（gfw 模式直连白名单）。
var cnDomainSuffixes = []string{
	"cn", "com.cn", "net.cn", "org.cn", "gov.cn", "edu.cn", "mil.cn", "ac.cn",
	"baidu.com", "bdstatic.com", "bdimg.com", "qq.com", "gtimg.com", "qpic.cn",
	"weixin.qq.com", "wechat.com", "taobao.com", "tmall.com", "alicdn.com",
	"alibaba.com", "alipay.com", "aliyun.com", "alipayobjects.com", "taobao.org",
	"jd.com", "jd.hk", "360buyimg.com", "bilibili.com", "hdslb.com", "bilivideo.com",
	"weibo.com", "sina.com.cn", "sinaimg.cn", "sina.com", "sohu.com", "163.com",
	"126.net", "netease.com", "music.163.com", "youku.com", "iqiyi.com", "qiyi.com",
	"douyin.com", "bytedance.com", "byteimg.com", "toutiao.com", "ixigua.com",
	"csdn.net", "cnblogs.com", "gitee.com", "oschina.net", "zhihu.com", "zhimg.com",
	"douban.com", "meituan.com", "dianping.com", "pinduoduo.com", "yangkeduo.com",
	"xiaomi.com", "mi.com", "miui.com", "huawei.com", "hicloud.com", "hk",
	"tmall.hk", "amap.com", "autonavi.com", "gaode.com", "12306.cn", "ctrip.com",
	"qunar.com", "trip.com", "xunlei.com", "kuaishou.com", "kuaishouapp.com",
	"upaiyun.com", "qiniu.com", "qbox.me", "jianshu.com", "zhipin.com", "58.com",
	"58corp.com", "lagou.com", "ele.me", "alibaba-inc.com", "dingtalk.com",
	"aliyuncs.com", "alicdn.com", "tanx.com", "mmstat.com", "cnzz.com",
	"umeng.com", "umengcloud.com", "gepush.com", "getui.com", "jpush.cn",
	"wps.cn", "wps.com", "kdocs.cn", "kingsoft.com", "icloud.com.cn",
	"apple.com.cn", "icloud.com", "microsoft.com", "msn.cn", "bing.com",
	"baiducontent.com", "baidupcs.com", "pan.baidu.com", "weiyun.com",
	"smzdm.com", "huya.com", "douyu.com", "zhanqi.tv", "pptv.com", "le.com",
	"letv.com", "so.com", "360.cn", "360.com", "qihoo.com", "sogou.com",
	"liepin.com", "zhaopin.com", "51job.com", "yingjiesheng.com", "nowcoder.com",
	"runoob.com", "w3school.com.cn", "segmentfault.com", "juejin.cn",
	"csdnimg.cn", "tencent.com", "tencent-cloud.com", "myqcloud.com",
	"qcloud.com", "weixin.com", "mp.weixin.qq.com", "sogoucdn.com",
	"baidustatic.com", "baidu.cn", "nuomi.com", "baike.com", "hupu.com",
	"tieba.baidu.com", "xiaohongshu.com", "xhscdn.com", "kuaishoucdn.com",
	"epicgames.com", "steamcommunity.com", "steampowered.com",
}
