package util

import (
	"net/url"
	"strings"
)

// IsSameHostOrigin 判断 Origin 头的主机名是否与请求 Host 头的主机名一致（忽略端口和大小写）。
//
// 安全前提：此函数依赖浏览器保证 Host 头的真实性（Host 属于 forbidden header，
// JavaScript 无法篡改）。生产环境应始终通过反向代理强制设置 Host 头
// （如 Nginx 的 proxy_set_header Host $host），以免中间层改写导致误判。
func IsSameHostOrigin(origin string, requestHost string) bool {
	originHost := ParseOriginHost(origin)
	if originHost == "" {
		return false
	}
	currentHost := ParseRequestHost(requestHost)
	if currentHost == "" {
		return false
	}
	return strings.EqualFold(originHost, currentHost)
}

// ParseOriginHost 从 Origin 值中提取规范化的主机名。
// 只接受 http/https scheme，同时拒绝 "null" Origin（浏览器在 sandboxed
// iframe 或 file:// 协议下可能发送 Origin: null）。
func ParseOriginHost(origin string) string {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return NormalizeHostname(parsed.Hostname())
}

// ParseRequestHost 从 HTTP 请求的 Host 头值中提取规范化的主机名。
func ParseRequestHost(host string) string {
	parsed, err := url.Parse("http://" + strings.TrimSpace(host))
	if err != nil {
		return ""
	}
	return NormalizeHostname(parsed.Hostname())
}

// NormalizeHostname 对主机名做小写化并移除尾部点号。
func NormalizeHostname(host string) string {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	trimmed = strings.TrimSuffix(trimmed, ".")
	if trimmed == "" {
		return ""
	}
	return trimmed
}
