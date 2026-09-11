package model

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestRsyncCaptureManifestCodecV2PreservesPathBytes(t *testing.T) {
	manifest := RsyncCaptureManifest{
		Version: 1,
		Layout:  TaskRunCaptureLayoutDirectoryRoot,
		Root:    string([]byte{0xff, 'r', '\n'}),
		Entries: []RsyncCaptureManifestEntry{
			{Path: string([]byte{0xfe, '/', 'f'}), Kind: "file", Size: 3, SHA256: strings.Repeat("a", 64)},
			{Path: "link", Kind: "symlink", LinkTarget: string([]byte{'t', '\n', 0xfd})},
		},
	}
	raw, err := EncodeRsyncCaptureManifest(manifest)
	if err != nil {
		t.Fatalf("encode v2 manifest: %v", err)
	}
	if !strings.Contains(raw, `"version":2`) || !strings.Contains(raw, `"root_b64"`) || strings.Contains(raw, `"root":"`) {
		t.Fatalf("manifest is not canonical v2: %s", raw)
	}
	decoded, err := DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode v2 manifest: %v", err)
	}
	if decoded.Version != 2 || decoded.Root != manifest.Root || len(decoded.Entries) != len(manifest.Entries) {
		t.Fatalf("decoded manifest=%+v, want bytes from %+v", decoded, manifest)
	}
	for index, entry := range manifest.Entries {
		if decoded.Entries[index] != entry {
			t.Fatalf("decoded entry[%d]=%+v, want %+v", index, decoded.Entries[index], entry)
		}
	}
}

func TestRsyncCaptureManifestCodecDecodesStoredV1(t *testing.T) {
	raw := `{"version":1,"layout":"directory_contents","entries":[{"path":"line\nname","kind":"symlink","link_target":"target -> part\n\n"}]}`
	decoded, err := DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode v1 manifest: %v", err)
	}
	if decoded.Version != 1 || decoded.Root != "" || decoded.Entries[0].Path != "line\nname" || decoded.Entries[0].LinkTarget != "target -> part\n\n" {
		t.Fatalf("decoded v1 manifest=%+v", decoded)
	}
}

func TestRsyncCaptureManifestCodecRejectsNonCanonicalV2(t *testing.T) {
	valid, err := EncodeRsyncCaptureManifest(RsyncCaptureManifest{
		Layout:  TaskRunCaptureLayoutDirectoryContents,
		Entries: []RsyncCaptureManifestEntry{{Path: "", Kind: "directory"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valid), &object); err != nil {
		t.Fatal(err)
	}
	object["root_b64"] = json.RawMessage(`"YQ=="`)
	mutated, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRsyncCaptureManifest(string(mutated)); err == nil {
		t.Fatal("padded base64 unexpectedly accepted")
	}
	duplicate := strings.TrimSuffix(valid, "}") + `,"root_b64":""}`
	if _, err := DecodeRsyncCaptureManifest(duplicate); err == nil {
		t.Fatal("duplicate v2 field unexpectedly accepted")
	}
}

func TestRsyncCaptureRootSidecarCodec(t *testing.T) {
	root := string([]byte{0xff, 'r', '\n'})
	sidecar, err := EncodeRsyncCaptureRootSidecar(root)
	if err != nil {
		t.Fatal(err)
	}
	if sidecar != "v2:"+base64.RawStdEncoding.EncodeToString([]byte(root)) {
		t.Fatalf("sidecar=%q", sidecar)
	}
	decoded, err := DecodeRsyncCaptureRootSidecar(sidecar, 2)
	if err != nil || decoded != root {
		t.Fatalf("decoded sidecar=%q err=%v", decoded, err)
	}
	legacy, err := DecodeRsyncCaptureRootSidecar("legacy-root", 1)
	if err != nil || legacy != "legacy-root" {
		t.Fatalf("legacy sidecar=%q err=%v", legacy, err)
	}
}
