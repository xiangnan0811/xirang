package capabilities

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

type Sensitivity string

const (
	SensitivityPublic  Sensitivity = "public"
	SensitivitySecret  Sensitivity = "secret"
	SensitivityUnknown Sensitivity = "unknown"
)

type SecretResult struct {
	Sensitivity Sensitivity
	Categories  []string
}

func ClassifySecret(input []byte, enabled bool) SecretResult {
	if !enabled || len(input) == 0 || len(input) > 16<<20 || !utf8.Valid(input) || bytes.IndexByte(input, 0) >= 0 {
		return SecretResult{Sensitivity: SensitivityUnknown}
	}
	lower := strings.ToLower(string(input))
	for _, marker := range []string{"token=", "password=", "private key", "authorization: bearer"} {
		if strings.Contains(lower, marker) {
			return SecretResult{Sensitivity: SensitivitySecret, Categories: []string{"credential_pattern"}}
		}
	}
	return SecretResult{Sensitivity: SensitivityPublic}
}
