package util

import "testing"

func TestIsSameHostOrigin(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		requestHost string
		want        bool
	}{
		{"同主机跨端口", "http://192.168.1.20:5173", "192.168.1.20:8080", true},
		{"同主机无端口", "https://example.com", "example.com", true},
		{"同主机大小写", "http://Example.COM:3000", "example.com:8080", true},
		{"同主机尾部点号", "http://example.com.", "example.com:8080", true},
		{"不同主机", "http://evil.com:5173", "192.168.1.20:8080", false},
		{"null Origin", "null", "192.168.1.20:8080", false},
		{"空 Origin", "", "192.168.1.20:8080", false},
		{"ftp scheme", "ftp://192.168.1.20", "192.168.1.20:8080", false},
		{"javascript scheme", "javascript:alert(1)", "localhost:8080", false},
		{"空 Host", "http://localhost:5173", "", false},
		{"无主机 Origin", "http://", "localhost:8080", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSameHostOrigin(tt.origin, tt.requestHost); got != tt.want {
				t.Errorf("IsSameHostOrigin(%q, %q) = %v, want %v", tt.origin, tt.requestHost, got, tt.want)
			}
		})
	}
}

func TestParseOriginHost(t *testing.T) {
	tests := []struct {
		origin string
		want   string
	}{
		{"http://example.com:8080", "example.com"},
		{"https://Example.COM", "example.com"},
		{"http://192.168.1.20:5173", "192.168.1.20"},
		{"null", ""},
		{"ftp://host", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			if got := ParseOriginHost(tt.origin); got != tt.want {
				t.Errorf("ParseOriginHost(%q) = %q, want %q", tt.origin, got, tt.want)
			}
		})
	}
}

func TestNormalizeHostname(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"Example.COM", "example.com"},
		{"example.com.", "example.com"},
		{"  HOST  ", "host"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := NormalizeHostname(tt.host); got != tt.want {
				t.Errorf("NormalizeHostname(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
