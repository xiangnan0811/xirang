package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestPointDeleterIsOptionalRegistryCapability(t *testing.T) {
	missing := NewRegistry()
	if err := missing.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	_, err := missing.PointDeleter(backupasset.ProviderRestic)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("missing PointDeleter error=%v, want capability unavailable", err)
	}
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityDeletionUnavailable {
		t.Fatalf("missing PointDeleter safe reason: %#v", err)
	}
	if _, err := missing.PointDeleter(backupasset.ProviderCommand); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unsupported PointDeleter error=%v", err)
	}

	port := &countingPointDeleter{kind: backupasset.ProviderRestic}
	registered := NewRegistry()
	if err := registered.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}, PointDeleter: port}); err != nil {
		t.Fatal(err)
	}
	got, err := registered.PointDeleter(backupasset.ProviderRestic)
	if err != nil || got != port {
		t.Fatalf("PointDeleter=%T err=%v", got, err)
	}
}

func TestPointDeleterRegistrationRejectsKindMismatch(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(backupasset.ProviderRsync, Registration{
		Prober:       &fakeProvider{},
		PointDeleter: &countingPointDeleter{kind: backupasset.ProviderRestic},
	})
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("Register mismatched PointDeleter kind error=%v, want ErrInvalidState", err)
	}
	if _, err := registry.PointDeleter(backupasset.ProviderRsync); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("mismatched PointDeleter remained registered: %v", err)
	}
}

func TestPointDeleterDoesNotMutateReadOnlyAdapter(t *testing.T) {
	if _, ok := any(&ResticAdapter{}).(PointDeleter); ok {
		t.Fatal("ResticAdapter unexpectedly implements PointDeleter")
	}
	if _, ok := any(&RcloneAdapter{}).(PointDeleter); ok {
		t.Fatal("RcloneAdapter unexpectedly implements PointDeleter")
	}
	if _, ok := any(&RsyncAdapter{}).(PointDeleter); ok {
		t.Fatal("RsyncAdapter unexpectedly implements PointDeleter")
	}

	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, Registration{
		Prober: &fakeProvider{}, PointLister: &fakeProvider{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PointDeleter(backupasset.ProviderRestic); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("read-only registration unexpectedly exposed PointDeleter: %v", err)
	}
}

func TestPointDeleterHidesLocatorAndBindingFromJSON(t *testing.T) {
	request := validResticDeletePointRequest(t)
	request.Snapshot.Access.Locator = "FAKE_PRIVATE_DELETION_LOCATOR_FOR_TEST_ONLY"
	request.Snapshot.Access.Secret = []byte("FAKE_PRIVATE_DELETION_SECRET_FOR_TEST_ONLY")
	request.Point.Native = strings.Repeat("a", 64)

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	for _, forbidden := range []string{
		"FAKE_PRIVATE_DELETION_LOCATOR_FOR_TEST_ONLY",
		"FAKE_PRIVATE_DELETION_SECRET_FOR_TEST_ONLY",
		strings.Repeat("a", 64),
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("private deletion value %q leaked in %s", forbidden, payload)
		}
	}
}

type countingPointDeleter struct {
	kind     backupasset.ProviderKind
	calls    int
	request  DeletePointRequest
	result   DeletePointResult
	err      error
	canceled bool
}

func (port *countingPointDeleter) ProviderKind() backupasset.ProviderKind {
	return port.kind
}

func (port *countingPointDeleter) DeletePoint(ctx context.Context, request DeletePointRequest) (DeletePointResult, error) {
	port.calls++
	port.request = request
	if ctx != nil && ctx.Err() != nil {
		port.canceled = true
		return DeletePointResult{}, ctx.Err()
	}
	return port.result, port.err
}

var _ PointDeleter = (*countingPointDeleter)(nil)
