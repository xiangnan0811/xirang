package capabilities

import (
	"bytes"
	_ "embed"
	"sort"
	"strings"
)

const productionToolchainSchemaVersion = "xirang.asset.toolchain.v3"

const maximumProductionAPKInputsBytes = 4096

// productionAPKInputDocument is generated from the retained input lock. Only
// inputs consumed by the Worker are embedded: a Core-only dependency refresh
// must not change the Worker binary or its runtime closure.
//
//go:embed production_apk_inputs.json
var productionAPKInputDocument []byte

type toolchainAPKInput struct {
	Role           string `json:"role"`
	Platform       string `json:"platform"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type productionAPKInputsDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Inputs        []toolchainAPKInput `json:"inputs"`
}

func productionAPKInputs() []toolchainAPKInput {
	inputs, err := decodeProductionAPKInputs(productionAPKInputDocument)
	if err != nil {
		return nil
	}
	return inputs
}

func decodeProductionAPKInputs(payload []byte) ([]toolchainAPKInput, error) {
	if len(payload) > maximumProductionAPKInputsBytes {
		return nil, ErrInvalidInvocation
	}
	var document productionAPKInputsDocument
	if err := decodeCanonicalToolchainJSON(bytes.TrimSpace(payload), maximumProductionAPKInputsBytes, &document); err != nil ||
		document.SchemaVersion != 1 {
		return nil, ErrInvalidInvocation
	}
	inputs, err := canonicalToolchainAPKInputs(document.Inputs)
	if err != nil {
		return nil, err
	}
	for index := range inputs {
		if inputs[index] != document.Inputs[index] {
			return nil, ErrInvalidInvocation
		}
	}
	return inputs, nil
}

func canonicalToolchainAPKInputs(inputs []toolchainAPKInput) ([]toolchainAPKInput, error) {
	if len(inputs) != 4 {
		return nil, ErrInvalidInvocation
	}
	result := append([]toolchainAPKInput(nil), inputs...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].Role != result[right].Role {
			return result[left].Role < result[right].Role
		}
		return result[left].Platform < result[right].Platform
	})
	for index, input := range result {
		role := "cgo-builder"
		if index >= 2 {
			role = "worker-runtime"
		}
		platform := "linux/amd64"
		if index%2 != 0 {
			platform = "linux/arm64"
		}
		if input.Role != role || input.Platform != platform || !lowerHexToolchain(input.ManifestSHA256, 64) ||
			input.ManifestSHA256 == strings.Repeat("0", 64) {
			return nil, ErrInvalidInvocation
		}
	}
	return result, nil
}
