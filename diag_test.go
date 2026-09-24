package main

import (
	"testing"
)

// TestSourceDiagnostics 逐个来源诊断抓取与解析结果。
func TestSourceDiagnostics(t *testing.T) {
	nm := NewNodeManager(t.TempDir())
	srcs := defaultSources()
	for _, s := range srcs {
		body, ok := nm.fetchSource(s)
		if !ok {
			t.Logf("%-18s 抓取失败", s.Label())
			continue
		}
		nodes := parseByKind(s.Kind, s.File, body, s.Label())
		desc := ""
		if len(nodes) > 0 {
			desc = nodes[0].Protocol + " " + nodes[0].Server + ":" +
				itoa(nodes[0].Port)
		}
		t.Logf("%-18s OK  %6d 字节  解析 %d 个节点   首个: %s",
			s.Label(), len(body), len(nodes), desc)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
