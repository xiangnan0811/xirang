package node

import "testing"

func TestValidateBasePath(t *testing.T) {
	for _, raw := range []string{"", "/", "/home/.hidden", "/a/..suffix", "/a/internal space", "/a//./b/", "/路径"} {
		if err := ValidateBasePath(raw); err != nil {
			t.Errorf("valid path %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"relative", " /root", "/root\u00a0", "/a/../b", "/..", "/a/..//", "/a\x00b", "/a\xff"} {
		if err := ValidateBasePath(raw); err == nil {
			t.Errorf("invalid path %q accepted", raw)
		}
	}
}

func TestFileBrowserBasePathLegacyInterpretation(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{"", ""},
		{" \t", ""},
		{"  /a//b/../c/  ", "/a/c"},
		{"relative/./path", "relative/path"},
		{"/", "/"},
	} {
		if got := FileBrowserBasePath(tc.stored); got != tc.want {
			t.Errorf("FileBrowserBasePath(%q) = %q, want %q", tc.stored, got, tc.want)
		}
	}
}
