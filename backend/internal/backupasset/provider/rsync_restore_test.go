package provider

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestNewRsyncRestoreIntentCarriesPinnedCapabilityNotPath(t *testing.T) {
	request := validRsyncRestoreRequest(t)
	source := &declaredRsyncRestoreSourceFake{}
	writer := &rsyncTargetWriterFake{}

	intent, err := NewRsyncRestoreIntent(request, source, writer)

	if err != nil {
		t.Fatalf("NewRsyncRestoreIntent: %v", err)
	}
	if intent.Source != source {
		t.Fatalf("intent source = %T, want exact declared-entry capability", intent.Source)
	}
	if intent.TargetWriter != writer {
		t.Fatalf("intent target writer = %T, want exact bound writer capability", intent.TargetWriter)
	}
	if intent.Target.NodeID != request.Target.NodeID || intent.Target.TargetBindingDigest != request.Target.BindingDigest ||
		intent.ManifestDigest != request.Rsync.ManifestDigest {
		t.Fatalf("intent lost frozen target/request facts: %#v", intent)
	}
	intentType := reflect.TypeOf(RsyncRestoreIntent{})
	sourceField, ok := intentType.FieldByName("Source")
	if !ok || sourceField.Type != reflect.TypeOf((*RsyncRestoreSource)(nil)).Elem() {
		t.Fatalf("RsyncRestoreIntent.Source = %#v, want declared-entry source capability", sourceField)
	}
	writerField, ok := intentType.FieldByName("TargetWriter")
	if !ok || writerField.Type != reflect.TypeOf((*RsyncTargetWriter)(nil)).Elem() {
		t.Fatalf("RsyncRestoreIntent.TargetWriter = %#v, want bound target writer capability", writerField)
	}
	for index := 0; index < intentType.NumField(); index++ {
		field := intentType.Field(index)
		if strings.Contains(strings.ToLower(field.Name), "path") || strings.Contains(strings.ToLower(field.Name), "locator") {
			t.Fatalf("RsyncRestoreIntent exposes source path/locator field %#v", field)
		}
	}
}

func TestNewRsyncRestoreIntentRejectsRawSourceOrInvalidScalarRef(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RestoreRequest)
	}{
		{
			name: "raw source arm",
			mutate: func(request *RestoreRequest) {
				request.Source = RestoreSource{Provider: backupasset.ProviderRsync, RepositoryID: strings.Repeat("1", 32), RecoveryPointID: request.Rsync.SourceRef.RecoveryPointID, Locator: "FAKE_RAW_RSYNC_SOURCE_FOR_TEST_ONLY"}
			},
		},
		{
			name: "manifest mismatch",
			mutate: func(request *RestoreRequest) {
				request.Rsync.SourceRef.ManifestDigest = strings.Repeat("f", 64)
			},
		},
		{
			name: "missing plan binding",
			mutate: func(request *RestoreRequest) {
				request.Rsync.SourceRef.PlanBindingDigest = ""
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validRsyncRestoreRequest(t)
			test.mutate(&request)
			if _, err := NewRsyncRestoreIntent(request, &declaredRsyncRestoreSourceFake{}, &rsyncTargetWriterFake{}); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("NewRsyncRestoreIntent error=%v, want invalid restore request", err)
			}
		})
	}
}

func TestNewRsyncRestoreIntentCopiesFrozenEntries(t *testing.T) {
	request := validRsyncRestoreRequest(t)
	intent, err := NewRsyncRestoreIntent(request, &declaredRsyncRestoreSourceFake{}, &rsyncTargetWriterFake{})
	if err != nil {
		t.Fatal(err)
	}
	request.Entries[0].ExpectedSize++
	if intent.Entries[0].ExpectedSize == request.Entries[0].ExpectedSize {
		t.Fatal("Rsync restore intent aliases mutable request entries")
	}
}

func TestNewRsyncRestoreIntentRejectsNilPinnedSource(t *testing.T) {
	if _, err := NewRsyncRestoreIntent(validRsyncRestoreRequest(t), nil, &rsyncTargetWriterFake{}); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("nil source error=%v, want invalid restore request", err)
	}
	if _, err := NewRsyncRestoreIntent(validRsyncRestoreRequest(t), &declaredRsyncRestoreSourceFake{}, nil); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("nil target writer error=%v, want invalid restore request", err)
	}
}

func validRsyncRestoreRequest(t *testing.T) RestoreRequest {
	t.Helper()
	request := cloneRestoreRequest(validResticRestoreRequest(t))
	ref := RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64), RepositoryID: strings.Repeat("3", 32),
		RecoveryPointID: request.Source.RecoveryPointID, CatalogGenerationID: strings.Repeat("4", 32), SelectionDigest: strings.Repeat("5", 64),
		SourceRevisionDigest: strings.Repeat("6", 64), ManifestDigest: strings.Repeat("7", 64),
	}
	request.Provider = backupasset.ProviderRsync
	request.Source = RestoreSource{}
	request.Rsync = &RsyncRestoreRequest{ManifestDigest: ref.ManifestDigest, SourceRef: ref}
	request.Restic = nil
	return request
}

type declaredRsyncRestoreSourceFake struct{}

func (*declaredRsyncRestoreSourceFake) OpenDeclaredRegular(context.Context, RestoreEntry) (RsyncRestoreSourceStream, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (*declaredRsyncRestoreSourceFake) MaterializeDeclaredEntries(_ context.Context, entries []RestoreEntry) ([]RestoreEntry, error) {
	return append([]RestoreEntry(nil), entries...), nil
}

func (*declaredRsyncRestoreSourceFake) Revalidate(context.Context) error { return nil }
func (*declaredRsyncRestoreSourceFake) Close() error                     { return nil }

type rsyncTargetWriterFake struct{}

func (*rsyncTargetWriterFake) WriteDeclaredRegular(context.Context, RsyncTargetWriteCall) error {
	return nil
}
