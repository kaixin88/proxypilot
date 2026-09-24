package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestFetchDetail 精确诊断 fetchSource 失败原因。
func TestFetchDetail(t *testing.T) {
	nm := NewNodeManager(t.TempDir())
	s := NodeSource{Kind: "clash.meta", Index: 1, File: "config.yaml"}
	for _, url := range s.mirrors() {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			t.Logf("构造请求失败 %s: %v", url, err)
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) ProxyPilot/1.0")
		resp, err := nm.client.Do(req)
		if err != nil {
			t.Logf("请求失败 %s -> %v", url, err)
			continue
		}
		buf := make([]byte, 600)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		body := string(buf[:n])
		t.Logf("URL=%s", url)
		t.Logf("  status=%d 读到=%d 字节", resp.StatusCode, n)
		head := body
		if len(head) > 120 {
			head = head[:120]
		}
		t.Logf("  前120字节=%q", head)
		t.Logf("  looksLikeConfig=%v (含proxies:=%v 含mixed-port=%v)",
			looksLikeConfig(body, s.File),
			strings.Contains(body, "proxies:"), strings.Contains(body, "mixed-port"))
	}
}
