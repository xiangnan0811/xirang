package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyNodeTargetPathSeparatesPoliciesOnSameNode(t *testing.T) {
	base := t.TempDir()
	first := PolicyNodeTargetPath(base, 11, 7)
	second := PolicyNodeTargetPath(base, 12, 7)
	if first == "" || second == "" {
		t.Fatal("expected valid isolated policy targets")
	}
	if first == second {
		t.Fatalf("different policies share target: %q", first)
	}
	wantFirst := filepath.Join(base, TargetNamespace, "policies", "11", "nodes", "7")
	if first != wantFirst {
		t.Fatalf("target=%q, want %q", first, wantFirst)
	}
}

func TestValidateTargetOwnersRejectsDistinctPolicyOverlap(t *testing.T) {
	base := t.TempDir()
	owners := []TargetOwner{
		{PolicyID: 11, NodeID: 7, Target: filepath.Join(base, "policy-a")},
		{PolicyID: 12, NodeID: 7, Target: filepath.Join(base, "policy-a", "nested")},
	}
	if err := ValidateTargetOwners(owners); err == nil {
		t.Fatal("expected overlapping policy targets to be rejected")
	}
}

func TestValidateTargetOwnershipResolvesSymlinkAlias(t *testing.T) {
	base := t.TempDir()
	realTarget := filepath.Join(base, "real")
	if err := os.MkdirAll(realTarget, 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realTarget, alias); err != nil {
		t.Fatal(err)
	}
	existing := []TargetOwner{{PolicyID: 11, NodeID: 7, Target: filepath.Join(realTarget, "data")}}
	if _, err := ValidateTargetOwnership(filepath.Join(alias, "data"), TargetOwner{PolicyID: 12, NodeID: 7}, existing); err == nil {
		t.Fatal("expected symlink alias to collide with existing target")
	}
}

func TestValidateTargetOwnershipAllowsExactSameTaskTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "same")
	owner := TargetOwner{PolicyID: 11, NodeID: 7, TaskID: 31, Target: target}
	canonical, err := ValidateTargetOwnership(target, owner, []TargetOwner{owner})
	if err != nil {
		t.Fatalf("same task target should be reusable: %v", err)
	}
	if canonical != target {
		t.Fatalf("canonical target=%q, want %q", canonical, target)
	}
}
func TestValidateTargetOwnershipRejectsDifferentTaskSamePolicyTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "same")
	existing := TargetOwner{PolicyID: 11, NodeID: 7, TaskID: 31, Target: target}
	candidate := TargetOwner{PolicyID: 11, NodeID: 7, TaskID: 32, Target: target}
	if _, err := ValidateTargetOwnership(target, candidate, []TargetOwner{existing}); err == nil {
		t.Fatal("different tasks under one policy/node must not reuse an exact target")
	}
}
