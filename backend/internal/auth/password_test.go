package auth

import "testing"

func TestValidatePasswordStrength(t *testing.T) {
	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "too-short", password: "Aa1!aaaa", wantErr: true},
		{name: "missing-upper", password: "aa1!aaaaaaaa", wantErr: true},
		{name: "missing-lower", password: "AA1!AAAAAAAA", wantErr: true},
		{name: "missing-digit", password: "Aa!aaaaaaaaa", wantErr: true},
		{name: "missing-special", password: "Aa1aaaaaaaaa", wantErr: true},
		{name: "valid", password: "Aa1!securepass", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordStrength(tc.password)
			if tc.wantErr && err == nil {
				t.Fatalf("期望校验失败")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望校验通过，实际失败: %v", err)
			}
		})
	}
}
