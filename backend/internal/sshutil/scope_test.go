package sshutil

import (
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestValidateSSHKeyScopeAllowsEmptyScope(t *testing.T) {
	key := model.SSHKey{Name: "empty-scope"}
	node := model.Node{ID: 42, Tags: "prod,db"}

	if err := ValidateSSHKeyScope(key, node, PurposeTerminal); err != nil {
		t.Fatalf("empty scope should allow use: %v", err)
	}
}

func TestValidateSSHKeyScopeDeniesDisabledExpiredPurposeNodeAndTag(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	node := model.Node{ID: 7, Tags: "prod,db"}

	cases := []struct {
		name string
		key  model.SSHKey
		want string
	}{
		{
			name: "disabled",
			key:  model.SSHKey{Disabled: true},
			want: "已禁用",
		},
		{
			name: "expired",
			key:  model.SSHKey{ExpiresAt: &expired},
			want: "已过期",
		},
		{
			name: "purpose mismatch",
			key:  model.SSHKey{ExpiresAt: &future, AllowedPurposes: PurposeTaskCommand},
			want: "不允许用于当前操作",
		},
		{
			name: "node id mismatch",
			key:  model.SSHKey{ExpiresAt: &future, AllowedPurposes: PurposeTerminal, AllowedNodeIDs: "8"},
			want: "不允许用于该节点",
		},
		{
			name: "tag mismatch",
			key:  model.SSHKey{ExpiresAt: &future, AllowedPurposes: PurposeTerminal, AllowedNodeTags: "backup"},
			want: "不允许用于该节点标签",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSSHKeyScope(tc.key, node, PurposeTerminal)
			if err == nil {
				t.Fatalf("expected denial")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestNormalizeScopeLists(t *testing.T) {
	purposes, err := NormalizePurposeList("terminal, task_command\nterminal")
	if err != nil {
		t.Fatalf("normalize purposes: %v", err)
	}
	if purposes != "terminal,task_command" {
		t.Fatalf("unexpected purposes: %q", purposes)
	}

	nodeIDs, err := NormalizeNodeIDList(`["7", "8", "7"]`)
	if err != nil {
		t.Fatalf("normalize node IDs: %v", err)
	}
	if nodeIDs != "7,8" {
		t.Fatalf("unexpected node IDs: %q", nodeIDs)
	}

	tags := NormalizeTagList("prod; db\nprod")
	if tags != "prod,db" {
		t.Fatalf("unexpected tags: %q", tags)
	}
}

func TestRepositoryPurposesRemainIndependent(t *testing.T) {
	purposes := []string{PurposeRepositoryProbe, PurposeRepositoryList, PurposeRepositoryRead}
	for _, allowed := range purposes {
		key := model.SSHKey{AllowedPurposes: allowed}
		for _, requested := range purposes {
			err := ValidateSSHKeyScope(key, model.Node{ID: 1}, requested)
			if (allowed == requested) != (err == nil) {
				t.Fatalf("allowed=%s requested=%s err=%v", allowed, requested, err)
			}
		}
	}
}
