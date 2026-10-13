package report

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

// P19 同源发现归并：给"同一个漏洞"一个稳定指纹。
//
// 指纹只由两样**可核实**的东西算出来：漏洞类型 + 该发现绑定流量的归一化端点集合。
// 刻意不掺标题/摘要（同一漏洞换措辞重报很常见，掺进去就认不出来了），也刻意不给
// 没有绑定流量的发现算指纹——那样的"指纹"只能靠资产+类型硬凑，会把同一资产上两个
// 不同的洞认成一个。宁可少报"疑似重复"，不可误导人合并。

// EndpointRef 一条绑定流量的端点（方法 + URL）。
type EndpointRef struct {
	Method string
	URL    string
}

// NormalizeEndpoint 归一化一条端点：方法大写、host 小写、去掉默认端口与尾斜杠、
// 查询串只保留**键名**（排序）。
//
// 只留键名是刻意的：`?id=1` 与 `?id=2` 是同一个注入点，而同一条路径上换了参数名
// （`?id=` vs `?q=`）是不同的注入面，必须算成不同的端点。URL 解析失败时退回去空白的
// 原文（不丢样本，只降级归一化强度）。
func NormalizeEndpoint(method, rawURL string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	raw := strings.TrimSpace(rawURL)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return m + " " + strings.TrimRight(strings.ToLower(raw), "/")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = "/"
	}
	keys := make([]string, 0, 8)
	for k := range u.Query() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	q := ""
	if len(keys) > 0 {
		q = "?" + strings.Join(keys, ",")
	}
	return m + " " + host + path + q
}

// Fingerprint 是发现的稳定指纹：漏洞类型 + 排序后的归一化端点集合，取 sha256 前 16 位。
// 没有端点（没有绑定流量）时返回空串——调用方据此判断"这条不参与归并"。
func Fingerprint(vulnClass string, endpoints []EndpointRef) string {
	if len(endpoints) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var norm []string
	for _, e := range endpoints {
		n := NormalizeEndpoint(e.Method, e.URL)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		norm = append(norm, n)
	}
	if len(norm) == 0 {
		return ""
	}
	sort.Strings(norm)
	class := strings.ToLower(strings.TrimSpace(vulnClass))
	sum := sha256.Sum256([]byte(class + "|" + strings.Join(norm, "|")))
	return hex.EncodeToString(sum[:8])
}
