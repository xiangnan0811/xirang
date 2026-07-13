package provider

import (
	"bytes"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestScopedEndpointIdentityIsTaskBoundAndDomainSeparated(t *testing.T) {
	salt := bytes.Repeat([]byte{0x42}, IdentitySaltBytes)
	doc := ScopedIdentityDocument{Provider: backupasset.ProviderRsync, TaskID: 7, NodeID: 9, EndpointFacts: []string{"root-fact", "filesystem-fact"}}
	identity, err := DeriveScopedIdentity(salt, doc)
	if err != nil {
		t.Fatal(err)
	}
	otherTask := doc
	otherTask.TaskID = 8
	other, _ := DeriveScopedIdentity(salt, otherTask)
	fingerprint, _ := DeriveConfigFingerprint(salt, []byte("canonical-binding"))
	if identity == other || strings.TrimPrefix(identity, ScopedIdentityPrefix(backupasset.ProviderRsync)) == fingerprint {
		t.Fatal("identity must be task-scoped and config-domain-separated")
	}
	for _, raw := range doc.EndpointFacts {
		if strings.Contains(identity, raw) || strings.Contains(fingerprint, raw) {
			t.Fatalf("raw endpoint fact leaked: %q", raw)
		}
	}
}

func TestScopedEndpointIdentityCanonicalizesFacts(t *testing.T) {
	salt := bytes.Repeat([]byte{0x24}, IdentitySaltBytes)
	left := ScopedIdentityDocument{Provider: backupasset.ProviderRclone, TaskID: 1, NodeID: 2, EndpointFacts: []string{"z", "a"}}
	right := left
	right.EndpointFacts = []string{"a", "z"}
	leftID, err := DeriveScopedIdentity(salt, left)
	if err != nil {
		t.Fatal(err)
	}
	rightID, err := DeriveScopedIdentity(salt, right)
	if err != nil || leftID != rightID {
		t.Fatalf("canonical identity mismatch left=%q right=%q err=%v", leftID, rightID, err)
	}
	for _, facts := range [][]string{{}, {""}, {"same", "same"}, {"line\nbreak"}} {
		left.EndpointFacts = facts
		if _, err := DeriveScopedIdentity(salt, left); err == nil {
			t.Fatalf("invalid facts accepted: %#v", facts)
		}
	}
}

func TestScopedEndpointIdentityRequiresExactSalt(t *testing.T) {
	doc := ScopedIdentityDocument{Provider: backupasset.ProviderRsync, TaskID: 1, NodeID: 2, EndpointFacts: []string{"root"}}
	for _, size := range []int{0, IdentitySaltBytes - 1, IdentitySaltBytes + 1} {
		if _, err := DeriveScopedIdentity(make([]byte, size), doc); err == nil {
			t.Fatalf("salt size %d accepted", size)
		}
	}
}

func TestNativeRepositoryIdentityValidatesResticID(t *testing.T) {
	nativeID := strings.Repeat("a", 64)
	identity, err := NativeRepositoryIdentity(backupasset.ProviderRestic, nativeID)
	if err != nil || identity != NativeResticIdentityPrefix+nativeID {
		t.Fatalf("identity=%q err=%v", identity, err)
	}
	for _, invalid := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := NativeRepositoryIdentity(backupasset.ProviderRestic, invalid); err == nil {
			t.Fatalf("invalid native ID accepted: %q", invalid)
		}
	}
	if _, err := NativeRepositoryIdentity(backupasset.ProviderRsync, nativeID); err == nil {
		t.Fatal("Rsync native identity unexpectedly accepted")
	}
}
