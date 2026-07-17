package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const rcloneV1744ProviderSchemaSHA256 = "f8d4c275818bc7c806568ca8569137d846f919c3aeffbe9a8181e08cc695ec93"

func TestRcloneProviderSchemaV1744FixtureAndClassificationAreExact(t *testing.T) {
	payload, err := os.ReadFile("testdata/rclone/v1.74.4-config-providers.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != rcloneV1744ProviderSchemaSHA256 {
		t.Fatalf("Rclone provider schema digest=%s", got)
	}
	var providers []struct {
		Name    string `json:"Name"`
		Options []struct {
			Name string `json:"Name"`
		} `json:"Options"`
	}
	if err := json.Unmarshal(payload, &providers); err != nil {
		t.Fatalf("decode provider schema: %v", err)
	}
	fixtureNames := make([]string, 0, len(providers))
	seenFixture := make(map[string]bool, len(providers))
	fixtureOptions := make(map[string]map[string]bool, len(providers))
	for _, provider := range providers {
		if provider.Name == "" || seenFixture[provider.Name] {
			t.Fatalf("duplicate/empty provider fixture name %q", provider.Name)
		}
		seenFixture[provider.Name] = true
		fixtureNames = append(fixtureNames, provider.Name)
		options := make(map[string]bool, len(provider.Options))
		for _, option := range provider.Options {
			if option.Name == "" {
				t.Fatalf("provider %q has an empty option name", provider.Name)
			}
			options[option.Name] = true
		}
		fixtureOptions[provider.Name] = options
	}
	classifications := RcloneBackendClassificationsV1744()
	seenClassified := make(map[string]bool, len(classifications))
	for _, classification := range classifications {
		if seenClassified[classification.Name] {
			t.Fatalf("duplicate classification %q", classification.Name)
		}
		seenClassified[classification.Name] = true
		switch classification.Class {
		case RcloneBackendLiteralSelfContained:
			if classification.UnsupportedReason != "" {
				t.Fatalf("literal backend %q has unsupported reason %q", classification.Name, classification.UnsupportedReason)
			}
		case RcloneBackendClosureWrapper:
			if classification.Name != "crypt" || classification.UnsupportedReason != "" {
				t.Fatalf("unexpected closure backend: %+v", classification)
			}
		case RcloneBackendUnsupported:
			if classification.UnsupportedReason == "" {
				t.Fatalf("unsupported backend %q lacks stable reason", classification.Name)
			}
		default:
			t.Fatalf("unknown classification: %+v", classification)
		}
	}
	sort.Strings(fixtureNames)
	classifiedNames := make([]string, 0, len(seenClassified))
	for name := range seenClassified {
		classifiedNames = append(classifiedNames, name)
	}
	sort.Strings(classifiedNames)
	if !reflect.DeepEqual(classifiedNames, fixtureNames) {
		t.Fatalf("provider classification set drifted:\nfixture=%v\nclassified=%v", fixtureNames, classifiedNames)
	}
	for backend, certified := range rcloneCertifiedOptions {
		available, ok := fixtureOptions[backend]
		if !ok {
			t.Fatalf("certified backend %q is absent from provider fixture", backend)
		}
		for option := range certified {
			if !available[option] {
				t.Errorf("certified option %q.%s is absent from provider fixture", backend, option)
			}
		}
	}
}

func TestRcloneBoundConfigAcceptsCertifiedLiteralAndCryptClosureFixtures(t *testing.T) {
	tests := map[string]struct {
		file         string
		target       string
		backend      string
		dependencies []string
	}{
		"s3":         {file: "s3.conf", target: "backup", backend: "s3", dependencies: []string{"backup"}},
		"azure blob": {file: "azure-blob.conf", target: "backup", backend: "azureblob", dependencies: []string{"backup"}},
		"gcs":        {file: "gcs.conf", target: "backup", backend: "google cloud storage", dependencies: []string{"backup"}},
		"b2":         {file: "b2.conf", target: "backup", backend: "b2", dependencies: []string{"backup"}},
		"sftp":       {file: "sftp.conf", target: "backup", backend: "sftp", dependencies: []string{"backup"}},
		"webdav":     {file: "webdav.conf", target: "backup", backend: "webdav", dependencies: []string{"backup"}},
		"swift":      {file: "swift.conf", target: "backup", backend: "swift", dependencies: []string{"backup"}},
		"ftp":        {file: "ftp.conf", target: "backup", backend: "ftp", dependencies: []string{"backup"}},
		"crypt":      {file: "crypt-closure.conf", target: "encrypted", backend: "crypt", dependencies: []string{"encrypted", "storage"}},
	}
	key := []byte("FAKE_BOUND_CONFIG_IDENTITY_KEY_32_BYTES_FOR_TEST_ONLY")
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			payload, err := os.ReadFile(filepath.Join("testdata/rclone/bound-config", test.file))
			if err != nil {
				t.Fatal(err)
			}
			bound, err := ValidateRcloneBoundConfigV1744(payload, test.target, key, 64<<10)
			if err != nil {
				t.Fatalf("validate fixture: %v", err)
			}
			if bound.TargetRemote() != test.target || bound.Backend() != test.backend || bound.ClassificationRevision() != 1 ||
				!reflect.DeepEqual(bound.DependencyRemotes(), test.dependencies) || bound.KeyedDigest() == "" || !bytes.Equal(bound.ExactBytes(), payload) {
				t.Fatalf("bound config facts=%+v dependencies=%v", bound, bound.DependencyRemotes())
			}
			copyBytes := bound.ExactBytes()
			copyBytes[0] ^= 0xff
			if bytes.Equal(copyBytes, bound.ExactBytes()) {
				t.Fatal("bound config exposed mutable exact bytes")
			}
		})
	}
}

func TestRcloneBoundConfigRejectsAmbiguityDynamicIdentityAndUnsupportedProfiles(t *testing.T) {
	tests := map[string]struct {
		config string
		target string
		reason RcloneBoundConfigReasonCode
	}{
		"duplicate section":        {config: "[backup]\ntype = s3\n[backup]\ntype = s3\n", target: "backup", reason: RcloneBoundConfigDuplicateSection},
		"duplicate key":            {config: "[backup]\ntype = s3\ntype = s3\n", target: "backup", reason: RcloneBoundConfigDuplicateKey},
		"extra stanza":             {config: "[backup]\ntype = b2\naccount = a\nkey = k\n[unused]\ntype = ftp\nhost = x\nuser = u\npass = p\n", target: "backup", reason: RcloneBoundConfigUnusedSection},
		"cycle":                    {config: "[a]\ntype = crypt\nremote = b:root\npassword = p\n[b]\ntype = crypt\nremote = a:root\npassword = p\n", target: "a", reason: RcloneBoundConfigDependencyCycle},
		"dangling dependency":      {config: "[a]\ntype = crypt\nremote = missing:root\npassword = p\n", target: "a", reason: RcloneBoundConfigDependencyMissing},
		"ambiguous dependency":     {config: "[a]\ntype = crypt\nremote = missing\npassword = p\n", target: "a", reason: RcloneBoundConfigDependencyAmbiguous},
		"dynamic env":              {config: "[backup]\ntype = s3\nenv_auth = true\n", target: "backup", reason: RcloneBoundConfigDynamicCredentialSource},
		"external credential file": {config: "[backup]\ntype = sftp\nhost = x\nuser = u\nkey_file = /tmp/key\n", target: "backup", reason: RcloneBoundConfigDynamicCredentialSource},
		"credential command":       {config: "[backup]\ntype = webdav\nurl = https://example.invalid\nbearer_token_command = helper token\n", target: "backup", reason: RcloneBoundConfigDynamicCredentialSource},
		"unknown option":           {config: "[backup]\ntype = b2\naccount = a\nkey = k\nfuture_option = value\n", target: "backup", reason: RcloneBoundConfigUnknownOption},
		"unknown backend":          {config: "[backup]\ntype = future-backend\nsecret = x\n", target: "backup", reason: RcloneBoundConfigUnknownBackend},
		"uncertified wrapper":      {config: "[backup]\ntype = alias\nremote = storage:root\n", target: "backup", reason: RcloneBoundConfigUncertifiedWrapper},
		"identity refresh":         {config: "[backup]\ntype = drive\ntoken = x\n", target: "backup", reason: RcloneBoundConfigIdentityRefreshUnbounded},
		"non remote":               {config: "[backup]\ntype = local\n", target: "backup", reason: RcloneBoundConfigNonRemoteBackend},
		"not certified":            {config: "[backup]\ntype = doi\n", target: "backup", reason: RcloneBoundConfigBackendNotCertified},
		"missing credentials":      {config: "[backup]\ntype = b2\naccount = a\n", target: "backup", reason: RcloneBoundConfigCredentialIncomplete},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateRcloneBoundConfigV1744([]byte(test.config), test.target, []byte("FAKE_BOUND_CONFIG_IDENTITY_KEY_32_BYTES_FOR_TEST_ONLY"), 64<<10)
			var typed *RcloneBoundConfigError
			if !errors.As(err, &typed) || typed.Reason != test.reason {
				t.Fatalf("error=%v typed=%+v want reason=%q", err, typed, test.reason)
			}
		})
	}
}

func TestRcloneBoundConfigKeyedIdentityChangesForEveryBoundFact(t *testing.T) {
	payload, err := os.ReadFile("testdata/rclone/bound-config/b2.conf")
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("FAKE_BOUND_CONFIG_IDENTITY_KEY_32_BYTES_FOR_TEST_ONLY")
	base, err := ValidateRcloneBoundConfigV1744(payload, "backup", key, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	changedBytes, err := ValidateRcloneBoundConfigV1744(append(append([]byte(nil), payload...), []byte("trailing")...), "backup", key, 64<<10)
	if err == nil || changedBytes.KeyedDigest() == base.KeyedDigest() {
		t.Fatalf("trailing malformed config did not fail closed: bound=%+v err=%v", changedBytes, err)
	}
	changedKey, err := ValidateRcloneBoundConfigV1744(payload, "backup", []byte("A_DIFFERENT_FAKE_BOUND_CONFIG_KEY_32_BYTES_FOR_TEST_ONLY"), 64<<10)
	if err != nil || changedKey.KeyedDigest() == base.KeyedDigest() {
		t.Fatalf("keyed config identity ignored key rotation: err=%v", err)
	}
}

func TestRcloneV1744ProviderSchemaConformance(t *testing.T) {
	binary := os.Getenv("RCLONE_TEST_BINARY")
	if binary == "" {
		t.Skip("RCLONE_TEST_BINARY is required only for the final conformance gate")
	}
	version, err := exec.Command(binary, "version").Output()
	if err != nil || ValidateManagedRcloneVersion(version) != nil {
		t.Fatalf("RCLONE_TEST_BINARY is not exact v1.74.4: err=%v output=%q", err, version)
	}
	got, err := exec.Command(binary, "config", "providers").Output()
	if err != nil {
		t.Fatalf("rclone config providers: %v", err)
	}
	want, err := os.ReadFile("testdata/rclone/v1.74.4-config-providers.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		gotDigest := sha256.Sum256(got)
		wantDigest := sha256.Sum256(want)
		t.Fatalf("provider schema drift got=%s want=%s", hex.EncodeToString(gotDigest[:]), hex.EncodeToString(wantDigest[:]))
	}
	for _, name := range []string{"s3.conf", "azure-blob.conf", "gcs.conf", "b2.conf", "sftp.conf", "webdav.conf", "swift.conf", "ftp.conf", "crypt-closure.conf"} {
		config := filepath.Join("testdata/rclone/bound-config", name)
		output, err := exec.Command(binary, "--config", config, "listremotes").CombinedOutput()
		if err != nil || !strings.Contains(string(output), ":") {
			t.Fatalf("Rclone rejected bound config %s: %v %s", name, err, output)
		}
	}
}

func TestRcloneV1744PortableConformance(t *testing.T) {
	binary := os.Getenv("RCLONE_TEST_BINARY")
	if binary == "" {
		t.Skip("RCLONE_TEST_BINARY is required only for the final conformance gate")
	}
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source")
	destinationPath := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(sourcePath, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "file.txt"), []byte("portable bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(sourcePath, "link")); err != nil {
		t.Fatal(err)
	}
	source, err := NewRclonePrivateLocator(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := NewRclonePrivateLocator(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(5 * time.Minute)
	boundConfig := []byte("[xirang-local-conformance]\ntype = local\n")
	run := func(operation CommandOperation, source, destination *RclonePrivateLocator, stagedSource *StagedPayloadRef) []byte {
		t.Helper()
		invocation := CommandInvocation{
			Tool: ToolRclone, Operation: operation, Purpose: CommandPurposePublish,
			SecretStdin: boundConfig, RcloneSource: source, RcloneDestination: destination,
			RcloneStagedSource: stagedSource, RcloneLowLevelRetries: 1, AbsoluteDeadline: deadline,
		}
		arguments, err := managedRcloneArguments(invocation)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, arguments...)
		command.Stdin = bytes.NewReader(invocation.SecretStdin)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("rclone %s: %v\n%s", operation, err, output)
		}
		return output
	}
	run(OperationRcloneManagedCopy, &source, &destination, nil)
	if payload, err := os.ReadFile(filepath.Join(destinationPath, "file.txt")); err != nil || string(payload) != "portable bytes" {
		t.Fatalf("copied bytes=%q err=%v", payload, err)
	}
	if info, err := os.Stat(filepath.Join(destinationPath, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory did not round trip: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(destinationPath, "link")); err != nil || target != "file.txt" {
		t.Fatalf("symlink target=%q err=%v", target, err)
	}
	listing := run(OperationRcloneManagedRecursiveList, &destination, nil, nil)
	options := rcloneManifestOptionsForTest()
	options.SymlinkTargetReader = func(_ context.Context, physicalPath string, maxBytes int64) ([]byte, error) {
		locator, err := joinRclonePrivateLocator(destination, physicalPath)
		if err != nil {
			return nil, err
		}
		payload := run(OperationRcloneManagedCat, &locator, nil, nil)
		if int64(len(payload)) > maxBytes {
			return nil, ErrRcloneManifestLimitExceeded
		}
		return payload, nil
	}
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(listing), options)
	if err != nil || manifest.EntryCount < 3 || !manifest.Fidelity.SymlinkTargetsPreserved || !manifest.Fidelity.EmptyDirectoriesPreserved {
		t.Fatalf("portable manifest=%+v err=%v listing=%s", manifest, err, listing)
	}
	run(OperationRcloneManagedCheckDownload, &source, &destination, nil)

	controlDirectory := filepath.Join(root, strings.Repeat("a", 32))
	if err := os.Mkdir(controlDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(controlDirectory, "commit.json")
	if err := os.WriteFile(controlPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	controlRef := stagedPayloadRefForCommandTest(controlPath)
	controlDestination, err := NewRclonePrivateLocator(filepath.Join(destinationPath, "control", "commit.json"))
	if err != nil {
		t.Fatal(err)
	}
	run(OperationRcloneManagedCopyTo, nil, &controlDestination, &controlRef)
	readback := run(OperationRcloneManagedCat, &controlDestination, nil, nil)
	if string(readback) != `{"version":1}` {
		t.Fatalf("control readback=%q", readback)
	}
}
