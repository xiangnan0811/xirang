package repository

import (
	"context"
	"testing"

	"xirang/backend/internal/backupasset/provider"
)

func TestNewServiceInjectsOptionalRecoverySourceNamespaceAuthority(t *testing.T) {
	authority := &repositoryRecoverySourceNamespaceAuthorityFake{}
	service, err := NewService(Dependencies{
		Foundation:              enabledFoundation(),
		RecoverySourceNamespace: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.recoverySourceNamespace != authority {
		t.Fatal("Recovery source-namespace authority was not retained")
	}
}

type repositoryRecoverySourceNamespaceAuthorityFake struct{}

func (*repositoryRecoverySourceNamespaceAuthorityFake) ObserveRecoverySourceNamespace(
	context.Context,
	RecoverySourceNamespaceRequest,
	provider.RsyncRestoreSource,
) (provider.RsyncRestoreSource, error) {
	return nil, nil
}

var _ RecoverySourceNamespaceAuthority = (*repositoryRecoverySourceNamespaceAuthorityFake)(nil)
