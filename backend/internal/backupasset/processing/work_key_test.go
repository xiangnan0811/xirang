package processing

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkKeyIncludesEveryOutputAffectingField(t *testing.T) {
	base := validWorkDescriptor()
	baseKey, err := ComputeWorkKey(base)
	if err != nil {
		t.Fatalf("ComputeWorkKey(base): %v", err)
	}
	mutations := map[string]func(*WorkDescriptorV1){
		"source recovery point":         func(value *WorkDescriptorV1) { value.Source.RecoveryPointID = strings.Repeat("d", 32) },
		"source entry":                  func(value *WorkDescriptorV1) { value.Source.EntryID = strings.Repeat("e", 64) },
		"catalog generation":            func(value *WorkDescriptorV1) { value.CatalogGenerationID = strings.Repeat("f", 32) },
		"source fingerprint":            func(value *WorkDescriptorV1) { value.SourceFingerprint += "-next" },
		"entry fingerprint":             func(value *WorkDescriptorV1) { value.EntryFingerprint += "-next" },
		"provider capability revision":  func(value *WorkDescriptorV1) { value.ProviderCapabilityRevision++ },
		"capability":                    func(value *WorkDescriptorV1) { value.Capability = "noop-next" },
		"capability schema":             func(value *WorkDescriptorV1) { value.CapabilitySchema = "noop.v2" },
		"pipeline fingerprint":          func(value *WorkDescriptorV1) { value.PipelineFingerprint += "-next" },
		"output profile":                func(value *WorkDescriptorV1) { value.OutputProfile = "noop.v2" },
		"security policy revision":      func(value *WorkDescriptorV1) { value.SecurityPolicyRevision += "-next" },
		"width":                         func(value *WorkDescriptorV1) { value.Parameters.Width++ },
		"height":                        func(value *WorkDescriptorV1) { value.Parameters.Height++ },
		"codec":                         func(value *WorkDescriptorV1) { value.Parameters.Codec = "webp" },
		"page start":                    func(value *WorkDescriptorV1) { value.Parameters.PageStart++ },
		"page end":                      func(value *WorkDescriptorV1) { value.Parameters.PageEnd++ },
		"quality":                       func(value *WorkDescriptorV1) { value.Parameters.Quality++ },
		"language":                      func(value *WorkDescriptorV1) { value.Parameters.Language = "en-US" },
		"model":                         func(value *WorkDescriptorV1) { value.Parameters.Model += "-next" },
		"font profile":                  func(value *WorkDescriptorV1) { value.Parameters.FontProfile += "-next" },
		"member range":                  func(value *WorkDescriptorV1) { value.Parameters.MemberEnd++ },
		"frame range":                   func(value *WorkDescriptorV1) { value.Parameters.FrameEnd++ },
		"time range":                    func(value *WorkDescriptorV1) { value.Parameters.TimeEndMillis++ },
		"orientation":                   func(value *WorkDescriptorV1) { value.Parameters.Orientation = "rotate90" },
		"crop":                          func(value *WorkDescriptorV1) { value.Parameters.CropWidth-- },
		"max pages":                     func(value *WorkDescriptorV1) { value.Parameters.MaxPages++ },
		"max pixels":                    func(value *WorkDescriptorV1) { value.Parameters.MaxPixels++ },
		"max duration":                  func(value *WorkDescriptorV1) { value.Parameters.MaxDurationMillis++ },
		"max expanded bytes":            func(value *WorkDescriptorV1) { value.Parameters.MaxExpandedBytes++ },
		"max output bytes":              func(value *WorkDescriptorV1) { value.Parameters.MaxOutputBytes++ },
		"max output count":              func(value *WorkDescriptorV1) { value.Parameters.MaxOutputCount++ },
		"truncation policy":             func(value *WorkDescriptorV1) { value.Parameters.TruncationPolicy = "partial" },
		"materialization output policy": func(value *WorkDescriptorV1) { value.Parameters.RequiresMaterialization = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			got, err := ComputeWorkKey(candidate)
			if err != nil {
				t.Fatalf("ComputeWorkKey(%s): %v", name, err)
			}
			if got == baseKey {
				t.Fatalf("%s change coalesced into base work key %s", name, got)
			}
		})
	}
}

func TestWorkKeyExcludesSchedulingAndRequesterFacts(t *testing.T) {
	descriptor := validWorkDescriptor()
	first, err := ComputeWorkKey(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeWorkKey(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same descriptor produced unstable keys: %s != %s", first, second)
	}
	if len(first) != 64 || strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("work key is not lowercase SHA-256: %q", first)
	}
}

func TestCanonicalDescriptorStrictDecode(t *testing.T) {
	descriptor := validWorkDescriptor()
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkDescriptorV1(encoded)
	if err != nil {
		t.Fatalf("DecodeWorkDescriptorV1(valid): %v\n%s", err, encoded)
	}
	wantKey, _ := ComputeWorkKey(descriptor)
	gotKey, _ := ComputeWorkKey(decoded)
	if gotKey != wantKey {
		t.Fatalf("strict decode changed canonical identity: got=%s want=%s", gotKey, wantKey)
	}

	duplicate := bytes.Replace(encoded, []byte(`{"schema_version":1,`), []byte(`{"schema_version":1,"schema_version":1,`), 1)
	unknown := bytes.Replace(encoded, []byte(`{"schema_version":1,`), []byte(`{"schema_version":1,"future_field":1,`), 1)
	nonInteger := bytes.Replace(encoded, []byte(`"quality":80`), []byte(`"quality":80.0`), 1)
	missingRequired := bytes.Replace(encoded, []byte(`,"quality":80`), nil, 1)
	outOfBounds := bytes.Replace(encoded, []byte(`"width":1280`), []byte(`"width":1000000`), 1)
	unsupportedParameterSchema := bytes.Replace(encoded, []byte(`"parameters":{"schema_version":1`), []byte(`"parameters":{"schema_version":2`), 1)
	invalidUTF8 := append([]byte(nil), encoded...)
	invalidUTF8 = bytes.Replace(invalidUTF8, []byte(`"codec":"png"`), []byte{'"', 'c', 'o', 'd', 'e', 'c', '"', ':', '"', 0xff, '"'}, 1)
	for name, payload := range map[string][]byte{
		"duplicate":                    duplicate,
		"unknown":                      unknown,
		"non-integer number":           nonInteger,
		"missing required default":     missingRequired,
		"out of bounds":                outOfBounds,
		"unsupported parameter schema": unsupportedParameterSchema,
		"invalid utf8":                 invalidUTF8,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkDescriptorV1(payload); err == nil {
				t.Fatalf("invalid descriptor was accepted: %s", payload)
			}
		})
	}
}
