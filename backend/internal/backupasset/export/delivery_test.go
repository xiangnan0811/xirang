package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDeliveryBudgetReserveFinalizeReplayAndConservativeCrash(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	grant := deliveryGrantFixture(now)
	grant.MaxRequests = 4
	grant.MaxCumulativeBytes = 1000
	grant.MaxInFlight = 2
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := newDeliveryBudget(db, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new budget: %v", err)
	}

	start, end := int64(10), int64(110)
	reservation, err := budget.Reserve(context.Background(), DeliveryReservationIntent{
		RequestID: strings.Repeat("2", 32), GrantID: grant.ID, Method: "GET",
		Range: content.HTTPRange{
			Kind: content.HTTPRangeNormal, Start: &start, EndExclusive: &end,
			Offset: start, Length: end - start,
		},
		ReservedBytes: 120,
	})
	if err != nil || reservation.AlreadyReserved || reservation.ReservedBytes != 120 {
		t.Fatalf("reserve=%+v err=%v", reservation, err)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 120, 0, 1)

	replay, err := budget.Reserve(context.Background(), DeliveryReservationIntent{
		RequestID: strings.Repeat("2", 32), GrantID: grant.ID, Method: "GET",
		Range: content.HTTPRange{
			Kind: content.HTTPRangeNormal, Start: &start, EndExclusive: &end,
			Offset: start, Length: end - start,
		},
		ReservedBytes: 120,
	})
	if err != nil || !replay.AlreadyReserved {
		t.Fatalf("reserve replay=%+v err=%v", replay, err)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 120, 0, 1)

	otherEnd := int64(111)
	_, err = budget.Reserve(context.Background(), DeliveryReservationIntent{
		RequestID: strings.Repeat("2", 32), GrantID: grant.ID, Method: "GET",
		Range: content.HTTPRange{
			Kind: content.HTTPRangeNormal, Start: &start, EndExclusive: &otherEnd,
			Offset: start, Length: otherEnd - start,
		},
		ReservedBytes: 120,
	})
	if !errors.Is(err, ErrDeliveryReplay) {
		t.Fatalf("different replay error=%v", err)
	}

	finalized, err := budget.Finalize(context.Background(), DeliveryFinalizeIntent{
		RequestID: strings.Repeat("2", 32), State: DeliveryRequestSucceeded,
		EvidenceKnown: true, PlaintextBytes: 100, CiphertextBytes: 116,
	})
	if err != nil || finalized.AlreadyFinalized || finalized.ChargedBytes != 116 {
		t.Fatalf("finalize=%+v err=%v", finalized, err)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 0, 116, 0)
	replayFinal, err := budget.Finalize(context.Background(), DeliveryFinalizeIntent{
		RequestID: strings.Repeat("2", 32), State: DeliveryRequestSucceeded,
		EvidenceKnown: true, PlaintextBytes: 100, CiphertextBytes: 116,
	})
	if err != nil || !replayFinal.AlreadyFinalized || replayFinal.ChargedBytes != 116 {
		t.Fatalf("finalize replay=%+v err=%v", replayFinal, err)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 0, 116, 0)
	if _, err := budget.Finalize(context.Background(), DeliveryFinalizeIntent{
		RequestID: strings.Repeat("2", 32), State: DeliveryRequestFailed,
		EvidenceKnown: true, PlaintextBytes: 99, CiphertextBytes: 115, FailureCode: "delivery_failed",
	}); !errors.Is(err, ErrDeliveryReplay) {
		t.Fatalf("changed finalize replay error=%v", err)
	}
	if _, err := budget.Reserve(context.Background(), DeliveryReservationIntent{
		RequestID: strings.Repeat("2", 32), GrantID: grant.ID, Method: "GET",
		Range: content.HTTPRange{
			Kind: content.HTTPRangeNormal, Start: &start, EndExclusive: &end,
			Offset: start, Length: end - start,
		},
		ReservedBytes: 120,
	}); !errors.Is(err, ErrDeliveryReplay) {
		t.Fatalf("terminal reserve replay error=%v", err)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 0, 116, 0)

	_, err = budget.Reserve(context.Background(), DeliveryReservationIntent{
		RequestID: strings.Repeat("3", 32), GrantID: grant.ID, Method: "GET",
		Range: content.HTTPRange{Kind: content.HTTPRangeFull, Length: 180}, ReservedBytes: 200,
	})
	if err != nil {
		t.Fatalf("reserve crash request: %v", err)
	}
	if err := budget.ReconcilePending(context.Background()); err != nil {
		t.Fatalf("reconcile pending: %v", err)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 2, 0, 316, 0)
	var crashed model.BackupAssetExportDeliveryRequest
	if err := db.Where("id = ?", strings.Repeat("3", 32)).Take(&crashed).Error; err != nil {
		t.Fatal(err)
	}
	if crashed.State != string(DeliveryRequestReconciled) || crashed.PlaintextBytes != 0 ||
		crashed.CiphertextBytes != 0 || crashed.FailureCode != "reconciled_crash" || crashed.FinishedAt == nil {
		t.Fatalf("reconciled request=%+v", crashed)
	}
}

func TestExportDeliveryHeadersUseClosedArchivePairMIMEAndSuffix(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		format      string
		profile     string
		contentType string
		suffix      string
	}{
		{name: "zip deflate", format: "zip", profile: "zip_deflate_v1", contentType: "application/zip", suffix: ".zip"},
		{name: "tar none", format: "tar", profile: "tar_none_v1", contentType: "application/x-tar", suffix: ".tar"},
		{name: "tar gzip", format: "tar", profile: "tar_gzip_v1", contentType: "application/gzip", suffix: ".tar.gz"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			jobID := strings.Repeat("a", 32)
			headers := exportDeliveryHeaders(exportDeliveryBinding{job: model.BackupAssetExportJob{
				ID: jobID, ArchiveFormat: testCase.format, ArchiveProfile: testCase.profile,
			}}, content.RepresentationPlan{ContentLength: 17, AcceptRanges: "bytes"}, `"etag"`)
			if got := headers.Get("Content-Type"); got != testCase.contentType {
				t.Fatalf("Content-Type=%q want %q", got, testCase.contentType)
			}
			wantDisposition := `attachment; filename="xirang-export-` + jobID[:16] + testCase.suffix + `"`
			if got := headers.Get("Content-Disposition"); got != wantDisposition {
				t.Fatalf("Content-Disposition=%q want %q", got, wantDisposition)
			}
		})
	}
}

func TestDeliveryBudgetRejectsRangeIntegerOverflow(t *testing.T) {
	start, length := int64(math.MaxInt64-4), int64(8)
	wrappedEnd := start + length
	intent := DeliveryReservationIntent{
		RequestID: strings.Repeat("1", 32), GrantID: strings.Repeat("2", 32), Method: http.MethodGet,
		Range: content.HTTPRange{
			Kind: content.HTTPRangeNormal, Start: &start, EndExclusive: &wrappedEnd,
			Offset: start, Length: length,
		},
		ReservedBytes: length,
	}
	if validDeliveryReservationIntent(intent) {
		t.Fatal("overflowing normal Range accepted by the 000068 ledger")
	}
}

func TestDeliveryBudgetConcurrentCASAdmitsOnlyOneReservation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	grant := deliveryGrantFixture(now)
	grant.MaxRequests, grant.MaxInFlight, grant.MaxCumulativeBytes = 2, 1, 100
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := newDeliveryBudget(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, requestID := range []string{strings.Repeat("2", 32), strings.Repeat("3", 32)} {
		requestID := requestID
		go func() {
			<-start
			_, reserveErr := budget.Reserve(context.Background(), DeliveryReservationIntent{
				RequestID: requestID, GrantID: grant.ID, Method: http.MethodGet,
				Range:         content.HTTPRange{Kind: content.HTTPRangeFull, Offset: 0, Length: 50},
				ReservedBytes: 100,
			})
			results <- reserveErr
		}()
	}
	close(start)
	succeeded, blocked := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDeliveryBudgetExceeded):
			blocked++
		default:
			t.Fatalf("unexpected concurrent reserve error=%v", err)
		}
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("succeeded=%d blocked=%d", succeeded, blocked)
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 100, 0, 1)
	var rows int64
	if err := db.Model(&model.BackupAssetExportDeliveryRequest{}).Where("grant_id = ?", grant.ID).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("request rows=%d", rows)
	}
}

func TestDeliveryGatewayIssueExportFreezesExactReadyArtifactAndCookieBinding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
	for _, row := range []any{&job, &attempt, &key, &artifact} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	// GORM applies the model's default:true tag when a false value is inserted;
	// publication reaches the sealed state through an explicit true -> false update.
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	var persistedJob model.BackupAssetExportJob
	var persistedAttempt model.BackupAssetExportAttempt
	var persistedArtifact model.BackupAssetExportArtifact
	if err := db.Where("id = ?", job.ID).Take(&persistedJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", attempt.ID).Take(&persistedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", artifact.ID).Take(&persistedArtifact).Error; err != nil {
		t.Fatal(err)
	}
	if !validReadyDeliveryJob(persistedJob, now) {
		t.Fatalf("persisted job does not satisfy ready-delivery contract: %+v", persistedJob)
	}
	if !validReadyDeliveryArtifact(persistedJob, persistedAttempt, persistedArtifact, now) {
		t.Fatalf("persisted artifact tuple does not satisfy ready-delivery contract: job=%+v attempt=%+v artifact=%+v", persistedJob, persistedAttempt, persistedArtifact)
	}
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := &deliverySessionValidatorStub{}
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: sessions,
		Store: store, Audit: mustDeliveryAudit(t), Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive,
			Key: bytes.Repeat([]byte{1}, 32),
		}},
		RequestID:      func() (string, error) { return strings.Repeat("8", 32), nil },
		TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4,
			MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	proof := content.StepUpProof{
		Action: auth.StepUpActionAssetExportDownload,
		ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(20 * time.Minute),
	}
	session := content.DeliverySession{
		JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
		TokenVersion: 3, ExpiresAt: now.Add(30 * time.Minute),
	}
	issued, err := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
		Actor:   content.DeliveryActor{UserID: job.OwnerUserID, Username: "admin", Role: "admin"},
		Session: session, ExportJobID: job.ID, Proof: proof, SecureCookie: true,
	})
	if err != nil {
		t.Fatalf("issue export: %v", err)
	}
	wantExpiry := now.Add(5 * time.Minute)
	if issued.Cookie == nil || issued.Cookie.Name != content.DeliveryCookieName ||
		issued.Cookie.Value != material.CookieSecret || issued.Cookie.Path != issued.Descriptor.ContentURL ||
		!issued.Cookie.HttpOnly || !issued.Cookie.Secure || !issued.Cookie.Expires.Equal(wantExpiry) {
		t.Fatalf("issued cookie=%+v descriptor=%+v", issued.Cookie, issued.Descriptor)
	}
	wantArtifactDigest := exportDeliveryBindingDigest(job, attempt, artifact, key)
	if issued.Descriptor.ContentLength != artifact.PlaintextSize ||
		issued.Descriptor.Range != content.RangeSingle ||
		issued.Descriptor.ETag != `"`+wantArtifactDigest+`"` ||
		!issued.Descriptor.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("descriptor=%+v", issued.Descriptor)
	}
	if len(sessions.values) != 1 || sessions.values[0] != session {
		t.Fatalf("validated sessions=%+v", sessions.values)
	}

	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("delivery_id = ?", material.DeliveryID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.ResourceKind != "export_archive" || grant.ExportJobID == nil || *grant.ExportJobID != job.ID ||
		grant.ExportArtifactID == nil || *grant.ExportArtifactID != artifact.ID ||
		grant.ExportAttemptID == nil || *grant.ExportAttemptID != attempt.ID ||
		grant.ExportFenceDigest != attempt.FenceDigest || grant.SelectionDigest != job.SelectionDigest ||
		grant.ArtifactDigest != wantArtifactDigest || grant.PlaintextSize != artifact.PlaintextSize ||
		grant.CiphertextSize != artifact.CiphertextSize || grant.FormatVersion != artifact.FormatVersion ||
		grant.ChunkBytes != artifact.ChunkBytes || grant.JobKeyID == nil || *grant.JobKeyID != key.ID ||
		grant.JobKeyVersion != int(key.KeyRevision) || grant.OwnerUserID != job.OwnerUserID ||
		grant.SessionJTI != session.JTI || grant.TokenVersion != int64(session.TokenVersion) ||
		grant.RoleRevision != int64(session.TokenVersion) || grant.ProofAction != string(proof.Action) ||
		grant.ProofID != proof.ID || !grant.ProofExpiresAt.Equal(proof.ExpiresAt) ||
		grant.CookieSecretHash != material.CookieSecretHash || grant.State != "active" ||
		grant.Action != "export_download" || grant.MethodPolicy != string(content.MethodGetHead) ||
		grant.RangePolicy != string(content.RangeSingle) || grant.CanonicalPath != issued.Descriptor.ContentURL ||
		!grant.AbsoluteExpiresAt.Equal(wantExpiry) || grant.MaxRequests != 4 ||
		grant.MaxCumulativeBytes != 1<<20 || grant.MaxInFlight != 2 {
		t.Fatalf("grant=%+v", grant)
	}
	if grant.CookieSecretHash == material.CookieSecret || strings.Contains(fmt.Sprintf("%+v", grant), material.CookieSecret) {
		t.Fatal("raw cookie secret persisted")
	}
}

func TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope(t *testing.T) {
	tests := []struct {
		name           string
		state          backupasset.DomainKeyState
		domain         backupasset.KeyDomain
		version        int
		key            []byte
		keyErr         error
		returnAny      bool
		mutateEnvelope func(*model.BackupAssetExportKey)
		wantSuccess    bool
	}{
		{name: "active", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), wantSuccess: true},
		{name: "verify only", state: backupasset.DomainKeyVerifyOnly, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), wantSuccess: true},
		{name: "missing", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), keyErr: errors.New("missing test KEK")},
		{name: "retired", state: backupasset.DomainKeyRetired, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32)},
		{name: "lost", state: backupasset.DomainKeyLost, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32)},
		{name: "wrong domain", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainDerivedStore, version: 2, key: bytes.Repeat([]byte{1}, 32), returnAny: true},
		{name: "wrong version", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 3, key: bytes.Repeat([]byte{1}, 32), returnAny: true},
		{name: "malformed key", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 31)},
		{name: "wrong key", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{2}, 32)},
		{name: "malformed nonce", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), mutateEnvelope: func(key *model.BackupAssetExportKey) {
			key.EnvelopeNonce = []byte{1}
		}},
		{name: "tampered nonce", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), mutateEnvelope: func(key *model.BackupAssetExportKey) {
			key.EnvelopeNonce = append([]byte(nil), key.EnvelopeNonce...)
			key.EnvelopeNonce[0] ^= 0x80
		}},
		{name: "malformed wrapped DEK", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), mutateEnvelope: func(key *model.BackupAssetExportKey) {
			key.WrappedDEK = []byte{1}
		}},
		{name: "tampered wrapped DEK", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), mutateEnvelope: func(key *model.BackupAssetExportKey) {
			key.WrappedDEK = append([]byte(nil), key.WrappedDEK...)
			key.WrappedDEK[len(key.WrappedDEK)-1] ^= 0x80
		}},
		{name: "wrong wrap algorithm", state: backupasset.DomainKeyActive, domain: backupasset.KeyDomainExportStore, version: 2, key: bytes.Repeat([]byte{1}, 32), mutateEnvelope: func(key *model.BackupAssetExportKey) {
			key.WrapAlgorithm = "aes-256-gcm-v2"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			db := openDeliveryTestDB(t)
			job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
			if test.mutateEnvelope != nil {
				test.mutateEnvelope(&key)
			}
			for _, row := range []any{&job, &attempt, &key, &artifact} {
				if err := db.Create(row).Error; err != nil {
					t.Fatalf("seed %T: %v", row, err)
				}
			}
			if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
				Update("is_current", false).Error; err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(StoreConfig{Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ticket, err := content.NewTicketMaterial()
			if err != nil {
				t.Fatal(err)
			}
			keys := &deliveryKeySourceStub{
				material: backupasset.DomainKeyMaterial{
					Domain: test.domain, Version: test.version, State: test.state, Key: test.key,
				},
				err: test.keyErr, returnMaterialForAnyRequest: test.returnAny,
			}
			audit := &deliveryAuditorRecorder{}
			gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
				DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{},
				Store: store, Keys: keys, Audit: audit,
				TicketMaterial: func() (content.TicketMaterial, error) { return ticket, nil },
				Config: DeliveryGatewayConfig{
					TicketTTL: time.Minute, MaxRequests: 2, MaxCumulativeBytes: 1 << 20, MaxInFlight: 1,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			issued, issueErr := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
				Actor: content.DeliveryActor{UserID: job.OwnerUserID, Username: "admin", Role: "admin"},
				Session: content.DeliverySession{
					JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
					TokenVersion: 1, ExpiresAt: now.Add(time.Hour),
				},
				ExportJobID: job.ID,
				Proof: content.StepUpProof{
					Action: auth.StepUpActionAssetExportDownload,
					ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(time.Hour),
				},
				SecureCookie: true,
			})
			if keys.calls != 1 {
				t.Fatalf("ByVersion calls=%d, want 1", keys.calls)
			}
			var grants int64
			if err := db.Model(&model.BackupAssetExportDeliveryGrant{}).Count(&grants).Error; err != nil {
				t.Fatal(err)
			}
			if test.wantSuccess {
				if issueErr != nil || issued.Cookie == nil || grants != 1 || len(audit.events) != 1 {
					t.Fatalf("issue=%v cookie=%v grants=%d audit=%+v", issueErr, issued.Cookie, grants, audit.events)
				}
				return
			}
			if !errors.Is(issueErr, ErrNotFound) || issued.Cookie != nil || issued.Descriptor.ContentURL != "" ||
				grants != 0 || len(audit.events) != 0 {
				t.Fatalf("issue=%v issued=%+v grants=%d audit=%+v", issueErr, issued, grants, audit.events)
			}
		})
	}
}

func TestDeliveryGatewayClearsCallerOwnedKeyMaterial(t *testing.T) {
	issue := func(t *testing.T, harness *readyDeliveryGatewayHarness) error {
		t.Helper()
		material, err := content.NewTicketMaterial()
		if err != nil {
			t.Fatal(err)
		}
		harness.gateway.ticketMaterial = func() (content.TicketMaterial, error) { return material, nil }
		_, err = harness.gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
			Actor: content.DeliveryActor{UserID: harness.job.OwnerUserID, Role: "admin"},
			Session: content.DeliverySession{
				JTI: strings.Repeat("d", 32), UserID: harness.job.OwnerUserID, Role: "admin",
				TokenVersion: 4, ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
			},
			ExportJobID: harness.job.ID,
			Proof: content.StepUpProof{
				Action: auth.StepUpActionAssetExportDownload,
				ID:     strings.Repeat("c", 32), ExpiresAt: time.Now().UTC().Add(20 * time.Minute),
			},
			SecureCookie: true,
		})
		return err
	}

	t.Run("ticket issue success", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
		keys := &zeroTrackingDeliveryKeySource{inner: harness.gateway.keys}
		harness.gateway.keys = keys
		if err := issue(t, harness); err != nil {
			t.Fatal(err)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 1)
	})

	t.Run("ticket issue unwrap failure", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
		keys := &zeroTrackingDeliveryKeySource{inner: harness.gateway.keys}
		harness.gateway.keys = keys
		wrapped := append([]byte(nil), harness.key.WrappedDEK...)
		wrapped[len(wrapped)-1] ^= 0x80
		if err := harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
			Update("wrapped_dek", wrapped).Error; err != nil {
			t.Fatal(err)
		}
		if err := issue(t, harness); !errors.Is(err, ErrNotFound) {
			t.Fatalf("IssueExport error=%v, want ErrNotFound", err)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 1)
	})

	t.Run("serve authenticated archive", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
		keys := &zeroTrackingDeliveryKeySource{inner: harness.gateway.keys}
		harness.gateway.keys = keys
		harness.requestIDs <- strings.Repeat("1", 32)
		response := httptest.NewRecorder()
		if err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
		}, response); err != nil {
			t.Fatal(err)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 1)
	})
}

func TestDeliveryGatewayIssueExportRejectsMalformedArtifactMetadataWithoutLedgerMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.BackupAssetExportJob, *model.BackupAssetExportAttempt, *model.BackupAssetExportArtifact)
	}{
		{name: "unsupported format version", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			artifact.FormatVersion = 2
		}},
		{name: "job artifact chunk mismatch", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportArtifact) {
			job.ChunkBytes /= 2
		}},
		{name: "oversized v1 chunk", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			job.ChunkBytes = maxCipherChunkBytesV1 + 1
			artifact.ChunkBytes = job.ChunkBytes
		}},
		{name: "impossible chunk count", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			artifact.ChunkCount++
		}},
		{name: "impossible ciphertext size", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			artifact.CiphertextSize++
			job.ArtifactBytes = artifact.CiphertextSize
		}},
		{name: "spool locator", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			artifact.Locator = strings.Repeat("7", 32) + ".xrs"
		}},
		{name: "path locator", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			artifact.Locator = "../" + strings.Repeat("7", 32) + ".xre"
		}},
		{name: "noncanonical locator", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, artifact *model.BackupAssetExportArtifact) {
			artifact.Locator = strings.Repeat("A", 32) + ".xre"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			db := openDeliveryTestDB(t)
			job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
			test.mutate(&job, &attempt, &artifact)
			for _, row := range []any{&job, &attempt, &key, &artifact} {
				if err := db.Create(row).Error; err != nil {
					t.Fatalf("seed %T: %v", row, err)
				}
			}
			if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
				Update("is_current", false).Error; err != nil {
				t.Fatal(err)
			}

			store, err := OpenStore(StoreConfig{Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			material, err := content.NewTicketMaterial()
			if err != nil {
				t.Fatal(err)
			}
			keys := &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
				Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive,
				Key: bytes.Repeat([]byte{1}, 32),
			}}
			audit := &deliveryAuditorRecorder{}
			gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
				DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{},
				Store: store, Keys: keys, Audit: audit,
				TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
				Config: DeliveryGatewayConfig{
					TicketTTL: time.Minute, MaxRequests: 2, MaxCumulativeBytes: 1 << 20, MaxInFlight: 1,
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			issued, issueErr := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
				Actor: content.DeliveryActor{UserID: job.OwnerUserID, Username: "admin", Role: "admin"},
				Session: content.DeliverySession{
					JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
					TokenVersion: 1, ExpiresAt: now.Add(time.Hour),
				},
				ExportJobID: job.ID,
				Proof: content.StepUpProof{
					Action: auth.StepUpActionAssetExportDownload,
					ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(time.Hour),
				},
				SecureCookie: true,
			})
			if !errors.Is(issueErr, ErrNotFound) || issued.Cookie != nil || issued.Descriptor.ContentURL != "" {
				t.Fatalf("issue=%v issued=%+v", issueErr, issued)
			}
			if keys.calls != 0 || len(audit.events) != 0 {
				t.Fatalf("key calls=%d audit=%+v", keys.calls, audit.events)
			}
			var grants, requests int64
			if err := db.Model(&model.BackupAssetExportDeliveryGrant{}).Count(&grants).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.BackupAssetExportDeliveryRequest{}).Count(&requests).Error; err != nil {
				t.Fatal(err)
			}
			if grants != 0 || requests != 0 {
				t.Fatalf("durable delivery ledger mutated: grants=%d requests=%d", grants, requests)
			}
		})
	}
}

func TestDeliveryGatewayIssueExportRejectsForgedAttemptFenceDigestBeforeSideEffects(t *testing.T) {
	audit := &deliveryAuditorRecorder{}
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{
		attemptFenceDigest: strings.Repeat("f", 64),
		deferIssue:         true,
		audit:              audit,
	})
	if harness.issued.Cookie != nil {
		t.Fatal("deferred harness unexpectedly issued a delivery ticket")
	}
	expiresAt := harness.job.ExpiresAt.UTC()
	issued, err := harness.gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: harness.job.OwnerUserID, Username: "admin", Role: "admin"},
		Session: content.DeliverySession{
			JTI: strings.Repeat("e", 32), UserID: harness.job.OwnerUserID, Role: "admin",
			TokenVersion: 3, ExpiresAt: expiresAt,
		},
		ExportJobID: harness.job.ID,
		Proof: content.StepUpProof{
			Action: auth.StepUpActionAssetExportDownload,
			ID:     strings.Repeat("9", 32), ExpiresAt: expiresAt,
		},
		SecureCookie: true,
	})
	if !errors.Is(err, ErrNotFound) || issued.Cookie != nil || issued.Descriptor.ContentURL != "" {
		t.Fatalf("IssueExport error=%v issued=%+v", err, issued)
	}
	if harness.keys.calls != 0 || len(audit.events) != 0 {
		t.Fatalf("key calls=%d audit events=%+v", harness.keys.calls, audit.events)
	}
	var grants, requests int64
	if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).Count(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).Count(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if grants != 0 || requests != 0 {
		t.Fatalf("durable delivery ledger mutated: grants=%d requests=%d", grants, requests)
	}
}

func TestDeliveryGatewayServeRejectsForgedAttemptFenceDigestBeforeDeliveryRequest(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{
		attemptFenceDigest: strings.Repeat("f", 64),
		deferIssue:         true,
	})
	now := harness.job.ReadyAt.UTC().Add(30 * time.Second)
	expiresAt := now.Add(5 * time.Minute)
	jobID, artifactID, attemptID, keyID := harness.job.ID, harness.artifact.ID, harness.attempt.ID, harness.key.ID
	grant := model.BackupAssetExportDeliveryGrant{
		ID: harness.material.GrantID, DeliveryID: harness.material.DeliveryID, ResourceKind: "export_archive",
		ExportJobID: &jobID, ExportArtifactID: &artifactID, ExportAttemptID: &attemptID,
		ExportFenceDigest: harness.attempt.FenceDigest, SelectionDigest: harness.job.SelectionDigest,
		ArtifactDigest: exportDeliveryBindingDigest(harness.job, harness.attempt, harness.artifact, harness.key),
		PlaintextSize:  harness.artifact.PlaintextSize, CiphertextSize: harness.artifact.CiphertextSize,
		FormatVersion: harness.artifact.FormatVersion, ChunkBytes: harness.artifact.ChunkBytes,
		JobKeyID: &keyID, JobKeyVersion: int(harness.key.KeyRevision), OwnerUserID: harness.job.OwnerUserID,
		SessionJTI: strings.Repeat("e", 32), TokenVersion: 3, RoleRevision: 3,
		ProofAction: "asset.export_download", ProofID: strings.Repeat("9", 32), ProofExpiresAt: expiresAt,
		CookieSecretHash: harness.material.CookieSecretHash, Action: "export_download",
		CanonicalPath: "/api/v1/asset-content/" + harness.material.DeliveryID,
		MethodPolicy:  string(content.MethodGetHead), RangePolicy: string(content.RangeSingle), State: "active",
		IdleExpiresAt: expiresAt, AbsoluteExpiresAt: expiresAt,
		MaxRequests: harness.gateway.config.MaxRequests, MaxCumulativeBytes: harness.gateway.config.MaxCumulativeBytes,
		MaxInFlight: harness.gateway.config.MaxInFlight, IssuedAt: now, CreatedAt: now, UpdatedAt: now, Version: 2,
	}
	if err := harness.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	const keyQueryCallback = "test:forged_delivery_fence_rejects_before_export_key_query"
	attemptRowQueries, keyRowQueries := 0, 0
	if err := harness.db.Callback().Query().Before("gorm:query").Register(keyQueryCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_attempts":
			attemptRowQueries++
		case "backup_asset_export_keys":
			keyRowQueries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(keyQueryCallback) })
	cookie, err := content.NewDeliveryCookie(harness.material.DeliveryID, harness.material.CookieSecret, expiresAt, true)
	if err != nil {
		t.Fatal(err)
	}
	requestID := strings.Repeat("8", 32)
	response, err := harness.serve(requestID, content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: cookie.String(),
	})
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
		t.Fatalf("serve error=%v headers=%v body_len=%d", err, response.Header(), response.Body.Len())
	}
	if harness.keys.calls != 0 {
		t.Fatalf("key calls=%d", harness.keys.calls)
	}
	if attemptRowQueries != 1 {
		t.Fatalf("export attempt row queries=%d", attemptRowQueries)
	}
	if keyRowQueries != 0 {
		t.Fatalf("export key row queries=%d", keyRowQueries)
	}
	var requests int64
	if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).Where("id = ?", requestID).Count(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("delivery request rows=%d", requests)
	}
}

func TestDeliveryGatewayIssueArchiveMemberFreezesExactDerivedTupleWithRangeNone(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	asset := content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		CatalogGenerationID: strings.Repeat("3", 32), RepositoryID: strings.Repeat("4", 32),
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 9,
		SourceFingerprint: strings.Repeat("5", 64), EntryFingerprint: strings.Repeat("6", 64),
		FingerprintStrength: "strong", Size: 1024, MediaType: "application/zip",
	}
	binding := content.ResolvedArchiveMemberArtifact{
		MemberRequestID: strings.Repeat("7", 32), OwnerUserID: 42, Ref: asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, MemberChainDigest: strings.Repeat("8", 64),
		ProcessingJobID: strings.Repeat("9", 32), ProcessingAttemptID: strings.Repeat("a", 32),
		DerivedArtifactSetID: strings.Repeat("b", 32), DerivedArtifactID: strings.Repeat("c", 32),
		DerivedBlobID: strings.Repeat("d", 32), DerivedDigest: strings.Repeat("e", 64),
		DerivedSize: 14, MediaType: "text/plain", AbsoluteExpiresAt: now.Add(4 * time.Minute),
		Provider: asset.Provider, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		FingerprintStrength: asset.FingerprintStrength, SourceSize: asset.Size,
		SourceMediaType: asset.MediaType, SecurityPolicyRevision: "security-policy-v1",
	}
	members := &archiveMemberDeliverySourceStub{binding: binding, payload: []byte("member payload")}
	authorizer := &archiveMemberAssetAuthorizerStub{asset: asset}
	audit := &deliveryAuditorRecorder{}
	sessions := &deliverySessionValidatorStub{}
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: sessions,
		Store: store, Audit: audit, ArchiveMembers: members, ArchiveMemberAuthorize: authorizer,
		Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 1, State: backupasset.DomainKeyActive,
			Key: bytes.Repeat([]byte{1}, 32),
		}},
		TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4, MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	proof := content.StepUpProof{
		Action: auth.StepUpActionAssetDownload, ID: strings.Repeat("f", 32), ExpiresAt: now.Add(20 * time.Minute),
	}
	session := content.DeliverySession{
		JTI: strings.Repeat("0", 32), UserID: 42, Role: "admin", TokenVersion: 3,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	issued, err := gateway.IssueArchiveMember(context.Background(), ArchiveMemberDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: 42, Username: "admin", Role: "admin"}, Session: session,
		Asset: asset, MemberRequestID: binding.MemberRequestID, Proof: proof, SecureCookie: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Cookie == nil || issued.Cookie.Value != material.CookieSecret ||
		issued.Descriptor.Range != content.RangeNone || issued.Descriptor.ContentLength != binding.DerivedSize ||
		issued.Descriptor.ContentType != binding.MediaType || issued.Descriptor.ETag != `"`+binding.DerivedDigest+`"` ||
		!issued.Descriptor.ExpiresAt.Equal(binding.AbsoluteExpiresAt) {
		t.Fatalf("issued member ticket=%+v cookie=%+v", issued.Descriptor, issued.Cookie)
	}
	if len(members.resolveRequests) != 1 || members.resolveRequests[0] != (content.ArchiveMemberArtifactRequest{
		RequestID: binding.MemberRequestID, OwnerUserID: 42, Asset: asset,
	}) {
		t.Fatalf("member resolve requests=%+v", members.resolveRequests)
	}
	if len(authorizer.actions) != 1 || authorizer.actions[0] != content.DeliveryDownload {
		t.Fatalf("member authorization actions=%v", authorizer.actions)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("delivery_id = ?", material.DeliveryID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.ResourceKind != "archive_member" || grant.ExportJobID != nil || grant.ExportArtifactID != nil ||
		grant.ExportAttemptID != nil || grant.JobKeyID != nil || grant.MemberRequestID == nil ||
		*grant.MemberRequestID != binding.MemberRequestID || grant.OuterRecoveryPointID != asset.Ref.RecoveryPointID ||
		grant.OuterEntryID != asset.Ref.EntryID || grant.OuterSourceFingerprint != asset.SourceFingerprint ||
		grant.OuterEntryFingerprint != asset.EntryFingerprint || grant.MemberChainDigest != binding.MemberChainDigest ||
		grant.ProcessingJobID == nil || *grant.ProcessingJobID != binding.ProcessingJobID ||
		grant.ProcessingAttemptID == nil || *grant.ProcessingAttemptID != binding.ProcessingAttemptID ||
		grant.DerivedArtifactSetID == nil || *grant.DerivedArtifactSetID != binding.DerivedArtifactSetID ||
		grant.DerivedArtifactID == nil || *grant.DerivedArtifactID != binding.DerivedArtifactID ||
		grant.DerivedBlobID == nil || *grant.DerivedBlobID != binding.DerivedBlobID ||
		grant.DerivedDigest != binding.DerivedDigest || grant.DerivedSize != binding.DerivedSize ||
		grant.PlaintextSize != 0 || grant.CiphertextSize != 0 || grant.ProofAction != string(auth.StepUpActionAssetDownload) ||
		grant.Action != "archive_member_download" || grant.RangePolicy != string(content.RangeNone) ||
		grant.MethodPolicy != string(content.MethodGetHead) || grant.State != "active" {
		t.Fatalf("member grant=%+v", grant)
	}
}

func TestDeliveryGatewayServesExactArchiveMemberThroughIndependentLedger(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	resourceKind, err := harness.gateway.loadDeliveryResourceKind(context.Background(), harness.material.DeliveryID)
	if err != nil || resourceKind != "archive_member" {
		t.Fatalf("resource kind=%q err=%v", resourceKind, err)
	}
	secret, err := content.ParseDeliveryCookie(harness.issued.Cookie.String())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := harness.gateway.loadArchiveMemberDeliveryBinding(
		context.Background(), harness.material.DeliveryID, secret,
	)
	if err != nil || !sameArchiveMemberDeliveryBinding(loaded, archiveMemberDeliveryBinding{
		grant: loaded.grant, asset: harness.asset, member: harness.binding,
	}) {
		t.Fatalf("member binding=%+v err=%v", loaded, err)
	}
	requestID := strings.Repeat("1", 32)
	harness.requestIDs <- requestID
	response := httptest.NewRecorder()
	err = harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	}, response)
	if err != nil {
		var request model.BackupAssetExportDeliveryRequest
		_ = harness.db.Where("id = ?", requestID).Take(&request).Error
		t.Fatalf("serve err=%v request=%+v resolves=%d reads=%d response=%d/%q",
			err, request, len(harness.source.resolveRequests), len(harness.source.readBindings),
			response.Code, response.Body.Bytes())
	}
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), harness.source.payload) ||
		response.Header().Get("Content-Type") != harness.binding.MediaType ||
		response.Header().Get("Content-Length") != fmt.Sprint(harness.binding.DerivedSize) ||
		response.Header().Get("Accept-Ranges") != "none" ||
		response.Header().Get("ETag") != `"`+harness.binding.DerivedDigest+`"` ||
		response.Header().Get("Content-Disposition") != `attachment; filename="xirang-archive-member.bin"` {
		t.Fatalf("response code=%d headers=%v body=%q", response.Code, response.Header(), response.Body.Bytes())
	}
	if len(harness.source.readBindings) != 1 || harness.source.readBindings[0] != harness.binding {
		t.Fatalf("member read bindings=%+v", harness.source.readBindings)
	}
	if harness.keys.calls != 0 {
		t.Fatalf("member delivery touched Export key source %d times", harness.keys.calls)
	}
	var delivered model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&delivered).Error; err != nil {
		t.Fatal(err)
	}
	if delivered.State != string(DeliveryRequestSucceeded) || delivered.ReservedBytes != harness.binding.DerivedSize ||
		delivered.PlaintextBytes != harness.binding.DerivedSize ||
		delivered.CiphertextBytes != harness.binding.DerivedSize || delivered.FinishedAt == nil {
		t.Fatalf("member delivery request=%+v", delivered)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, harness.binding.DerivedSize, 0)
}

func TestDeliveryGatewayArchiveMemberBindingMutationAfterReserveWritesNoByteAndChargesConservatively(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	requestID := strings.Repeat("1", 32)
	harness.requestIDs <- requestID
	harness.source.beforeWrite = func() {
		if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
			Where("id = ?", harness.material.GrantID).
			UpdateColumn("derived_digest", strings.Repeat("f", 64)).Error; err != nil {
			t.Fatalf("mutate member grant: %v", err)
		}
	}
	response := httptest.NewRecorder()
	err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	}, response)
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 {
		t.Fatalf("serve err=%v status=%d body=%q", err, response.Code, response.Body.Bytes())
	}
	var delivered model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&delivered).Error; err != nil {
		t.Fatal(err)
	}
	if delivered.State != string(DeliveryRequestFailed) || delivered.FailureCode != "delivery_failed" ||
		delivered.ReservedBytes != harness.binding.DerivedSize || delivered.PlaintextBytes != 0 ||
		delivered.CiphertextBytes != 0 || delivered.FinishedAt == nil {
		t.Fatalf("member delivery request=%+v", delivered)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, harness.binding.DerivedSize, 0)
}

func TestDeliveryGatewayArchiveMemberHeadAndRangePolicyUseExactLedger(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	cookie := harness.issued.Cookie.String()

	headID := strings.Repeat("2", 32)
	harness.requestIDs <- headID
	head := httptest.NewRecorder()
	if err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: cookie,
	}, head); err != nil {
		t.Fatal(err)
	}
	if head.Code != http.StatusOK || head.Body.Len() != 0 ||
		head.Header().Get("Content-Length") != fmt.Sprint(harness.binding.DerivedSize) ||
		head.Header().Get("Accept-Ranges") != "none" {
		t.Fatalf("HEAD status=%d headers=%v body_len=%d", head.Code, head.Header(), head.Body.Len())
	}

	for _, test := range []struct {
		name        string
		requestID   string
		ranges      []string
		failureCode content.RequestFailureCode
	}{
		{name: "single", requestID: strings.Repeat("3", 32), ranges: []string{"bytes=0-1"}, failureCode: content.RequestFailureRangeNotAllowed},
		{name: "multiple", requestID: strings.Repeat("4", 32), ranges: []string{"bytes=0-1", "bytes=2-3"}, failureCode: content.RequestFailureInvalidRange},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness.requestIDs <- test.requestID
			response := httptest.NewRecorder()
			err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
				RawCookie: cookie, RangeHeaders: test.ranges,
			}, response)
			if err != nil || response.Code != http.StatusRequestedRangeNotSatisfiable || response.Body.Len() != 0 ||
				response.Header().Get("Accept-Ranges") != "none" ||
				response.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", harness.binding.DerivedSize) {
				t.Fatalf("serve err=%v status=%d headers=%v body=%q", err, response.Code, response.Header(), response.Body.Bytes())
			}
			var request model.BackupAssetExportDeliveryRequest
			if err := harness.db.Where("id = ?", test.requestID).Take(&request).Error; err != nil {
				t.Fatal(err)
			}
			if request.State != string(DeliveryRequestBlocked) || request.FailureCode != string(test.failureCode) ||
				request.ReservedBytes != 0 || request.FinishedAt == nil {
				t.Fatalf("request=%+v", request)
			}
		})
	}
	if len(harness.source.readBindings) != 0 || harness.keys.calls != 0 {
		t.Fatalf("member reads=%d export key calls=%d", len(harness.source.readBindings), harness.keys.calls)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 3, 0, 0, 0)
}

func TestDeliveryGatewayExportHeadRevalidatesBindingBeforeMetadataCommit(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	requestID := strings.Repeat("a", 32)
	harness.requestIDs <- requestID
	writer := newBlockingDeliveryHeaderWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
		}, writer)
	}()

	select {
	case <-writer.headerRequested:
	case <-time.After(5 * time.Second):
		t.Fatal("export HEAD did not reach the metadata-commit barrier")
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 1)

	var grant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.gateway.RevokeSession(context.Background(), grant.SessionJTI, "logout"); err != nil {
		t.Fatal(err)
	}
	close(writer.releaseHeader)

	select {
	case err := <-serveDone:
		if !errors.Is(err, content.ErrContentNotFound) {
			t.Fatalf("export HEAD after session revoke error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("export HEAD did not finish after session revoke")
	}
	assertNoSuccessfulDeliveryMetadata(t, writer)

	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestFailed) || request.FailureCode != "delivery_failed" ||
		request.ReservedBytes != 0 || request.PlaintextBytes != 0 || request.CiphertextBytes != 0 || request.FinishedAt == nil {
		t.Fatalf("export HEAD request=%+v", request)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 0)
}

func TestDeliveryGatewayArchiveMemberHeadRevalidatesBindingBeforeMetadataCommit(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	requestID := strings.Repeat("b", 32)
	harness.requestIDs <- requestID
	writer := newBlockingDeliveryHeaderWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
		}, writer)
	}()

	select {
	case <-writer.headerRequested:
	case <-time.After(5 * time.Second):
		t.Fatal("archive-member HEAD did not reach the metadata-commit barrier")
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 1)

	if err := harness.gateway.RevokeArchiveMember(
		context.Background(), harness.binding.MemberRequestID, "member_canceled",
	); err != nil {
		t.Fatal(err)
	}
	close(writer.releaseHeader)

	select {
	case err := <-serveDone:
		if !errors.Is(err, content.ErrContentNotFound) {
			t.Fatalf("archive-member HEAD after revoke error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("archive-member HEAD did not finish after revoke")
	}
	assertNoSuccessfulDeliveryMetadata(t, writer)

	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestFailed) || request.FailureCode != "delivery_failed" ||
		request.ReservedBytes != 0 || request.PlaintextBytes != 0 || request.CiphertextBytes != 0 || request.FinishedAt == nil {
		t.Fatalf("archive-member HEAD request=%+v", request)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 0)
}

func TestDeliveryGatewayArchiveMemberHeadRejectsLiveProofBindingDriftBeforeMetadataCommit(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	requestID := strings.Repeat("c", 32)
	harness.requestIDs <- requestID
	writer := newBlockingDeliveryHeaderWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
		}, writer)
	}()

	select {
	case <-writer.headerRequested:
	case <-time.After(5 * time.Second):
		t.Fatal("archive-member HEAD did not reach the metadata-commit barrier")
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 1)

	if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ?", harness.material.GrantID).
		Update("proof_id", strings.Repeat("1", 32)).Error; err != nil {
		t.Fatal(err)
	}
	close(writer.releaseHeader)

	select {
	case err := <-serveDone:
		if !errors.Is(err, content.ErrContentNotFound) {
			t.Fatalf("archive-member HEAD after proof binding drift error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("archive-member HEAD did not finish after proof binding drift")
	}
	assertNoSuccessfulDeliveryMetadata(t, writer)

	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestFailed) || request.FailureCode != "delivery_failed" ||
		request.ReservedBytes != 0 || request.PlaintextBytes != 0 || request.CiphertextBytes != 0 || request.FinishedAt == nil {
		t.Fatalf("archive-member HEAD request=%+v", request)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 0)
}

func TestDeliveryGatewayExportHeadRejectsLiveArtifactBindingDriftBeforeMetadataCommit(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	requestID := strings.Repeat("d", 32)
	harness.requestIDs <- requestID
	writer := newBlockingDeliveryHeaderWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
		}, writer)
	}()

	select {
	case <-writer.headerRequested:
	case <-time.After(5 * time.Second):
		t.Fatal("export HEAD did not reach the metadata-commit barrier")
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 1)

	driftedArtifact := harness.artifact
	driftedArtifact.Locator = strings.Repeat("8", 32) + ".xre"
	driftedDigest := exportDeliveryBindingDigest(harness.job, harness.attempt, driftedArtifact, harness.key)
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
			Update("locator", driftedArtifact.Locator).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportDeliveryGrant{}).Where("id = ?", harness.material.GrantID).
			Update("artifact_digest", driftedDigest).Error
	}); err != nil {
		t.Fatal(err)
	}
	close(writer.releaseHeader)

	select {
	case err := <-serveDone:
		if !errors.Is(err, content.ErrContentNotFound) {
			t.Fatalf("export HEAD after artifact binding drift error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("export HEAD did not finish after artifact binding drift")
	}
	assertNoSuccessfulDeliveryMetadata(t, writer)

	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestFailed) || request.FailureCode != "delivery_failed" ||
		request.ReservedBytes != 0 || request.PlaintextBytes != 0 || request.CiphertextBytes != 0 || request.FinishedAt == nil {
		t.Fatalf("export HEAD request=%+v", request)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 0)
}

func TestDeliveryGatewayRejectsExportGrantWithArchiveMemberTupleContamination(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	derivedSetID := strings.Repeat("8", 32)
	if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ?", harness.material.GrantID).
		UpdateColumn("derived_artifact_set_id", derivedSetID).Error; err != nil {
		t.Fatal(err)
	}
	requestID := strings.Repeat("1", 32)
	response, err := harness.serve(requestID, content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	})
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 {
		t.Fatalf("serve err=%v status=%d body_len=%d", err, response.Code, response.Body.Len())
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).
		Where("id = ?", requestID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("delivery request count=%d err=%v", count, err)
	}
}

func TestDeliveryGatewayArchiveMemberDeniesCookieActionPathSessionAndSubjectDrift(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, *archiveMemberDeliveryGatewayHarness)
		rawCookie func(*archiveMemberDeliveryGatewayHarness) string
	}{
		{
			name: "cookie",
			rawCookie: func(harness *archiveMemberDeliveryGatewayHarness) string {
				cookie := *harness.issued.Cookie
				cookie.Value = strings.Repeat("a", 64)
				return cookie.String()
			},
		},
		{
			name: "action",
			mutate: func(t *testing.T, harness *archiveMemberDeliveryGatewayHarness) {
				t.Helper()
				if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
					Where("id = ?", harness.material.GrantID).UpdateColumn("action", "export_download").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "proof action",
			mutate: func(t *testing.T, harness *archiveMemberDeliveryGatewayHarness) {
				t.Helper()
				if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
					Where("id = ?", harness.material.GrantID).UpdateColumn("proof_action", "asset.export_download").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "canonical path",
			mutate: func(t *testing.T, harness *archiveMemberDeliveryGatewayHarness) {
				t.Helper()
				if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
					Where("id = ?", harness.material.GrantID).
					UpdateColumn("canonical_path", "/api/v1/asset-content/"+strings.Repeat("f", 32)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ended session",
			mutate: func(_ *testing.T, harness *archiveMemberDeliveryGatewayHarness) {
				harness.sessions.err = errors.New("ended session")
			},
		},
		{
			name: "subject",
			mutate: func(t *testing.T, harness *archiveMemberDeliveryGatewayHarness) {
				t.Helper()
				if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
					Where("id = ?", harness.material.GrantID).UpdateColumn("owner_user_id", 43).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newArchiveMemberDeliveryGatewayHarness(t)
			if testCase.mutate != nil {
				testCase.mutate(t, harness)
			}
			cookie := harness.issued.Cookie.String()
			if testCase.rawCookie != nil {
				cookie = testCase.rawCookie(harness)
			}
			response := httptest.NewRecorder()
			err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: cookie,
			}, response)
			if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 ||
				len(harness.source.readBindings) != 0 {
				t.Fatalf("serve err=%v body=%q reads=%d", err, response.Body.Bytes(), len(harness.source.readBindings))
			}
			var requests int64
			if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).Count(&requests).Error; err != nil || requests != 0 {
				t.Fatalf("delivery requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestDeliveryGatewayRejectsArchiveMemberGrantWithExportTupleContamination(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	exportJobID := strings.Repeat("f", 32)
	if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ?", harness.material.GrantID).
		UpdateColumn("export_job_id", exportJobID).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	}, response)
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 ||
		len(harness.source.readBindings) != 0 {
		t.Fatalf("serve err=%v body=%q reads=%d", err, response.Body.Bytes(), len(harness.source.readBindings))
	}
	var requests int64
	if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).Count(&requests).Error; err != nil || requests != 0 {
		t.Fatalf("delivery requests=%d err=%v", requests, err)
	}
}

func TestDeliveryGatewayRevokeEmitsClosedArchiveMemberReadSummary(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	harness.requestIDs <- strings.Repeat("5", 32)
	response := httptest.NewRecorder()
	if err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	}, response); err != nil {
		t.Fatal(err)
	}
	if err := harness.gateway.RevokeSession(
		context.Background(), strings.Repeat("0", 32), "logout",
	); err != nil {
		t.Fatal(err)
	}
	if len(harness.audit.events) != 2 {
		t.Fatalf("audit events=%+v", harness.audit.events)
	}
	read := harness.audit.events[1]
	if read.Action != backupasset.AuditActionArchiveMember || read.Outcome != backupasset.AuditOutcomeSuccess ||
		read.RecoveryPointID != harness.binding.Ref.RecoveryPointID || read.EntryID != harness.binding.Ref.EntryID ||
		read.SelectionDigest != harness.binding.MemberChainDigest || read.ItemCount != 1 ||
		read.ByteCount != harness.binding.DerivedSize || read.RangeCount != 0 || read.RangeBytes != 0 ||
		read.Mode != "read" || read.ExportJobID != "" || read.ArchiveFormat != "" || read.ErrorCategory != "" {
		t.Fatalf("member read audit=%+v", read)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != "revoked" || grant.AuditState != "emitted" || grant.AuditRequestCount != 1 {
		t.Fatalf("grant=%+v", grant)
	}
}

func TestDeliveryGatewayArchiveMemberAuditFailurePersistsAndRestartConvergesOnce(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	requestID := strings.Repeat("5", 32)
	harness.requestIDs <- requestID
	response := httptest.NewRecorder()
	if err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	}, response); err != nil {
		t.Fatal(err)
	}
	harness.audit.err = errors.New("FAKE_RAW_MEMBER_AUDIT_FAILURE_FOR_TEST_ONLY")
	if err := harness.gateway.RevokeArchiveMember(
		context.Background(), harness.binding.MemberRequestID, "member_canceled",
	); !errors.Is(err, ErrDeliveryAudit) || strings.Contains(err.Error(), "FAKE_RAW") {
		t.Fatalf("member revoke audit error=%v", err)
	}
	var failed model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&failed).Error; err != nil {
		t.Fatal(err)
	}
	if failed.State != "revoked" || failed.AuditState != "retry_wait" || failed.AuditRequestCount != 1 ||
		failed.AuditSuccessCount != 1 || failed.AuditAttemptCount != 1 ||
		failed.AuditFailureCode != "audit_write_failed" || failed.AuditNextAttemptAt == nil {
		t.Fatalf("failed member audit grant=%+v", failed)
	}
	harness.audit.err = nil
	*harness.clock = failed.AuditNextAttemptAt.Add(time.Second)
	restarted, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: harness.db, Now: func() time.Time { return *harness.clock }, Session: harness.sessions,
		Store: harness.store, Keys: harness.keys, ArchiveMembers: harness.source,
		ArchiveMemberAuthorize: harness.authorizer, Audit: harness.audit,
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4, MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	var emitted model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&emitted).Error; err != nil {
		t.Fatal(err)
	}
	if emitted.AuditState != "emitted" || emitted.AuditAttemptCount != 1 ||
		emitted.AuditFailureCode != "" || emitted.AuditNextAttemptAt != nil {
		t.Fatalf("reconciled member audit grant=%+v", emitted)
	}
	memberReads := 0
	for _, event := range harness.audit.events {
		if event.Action == backupasset.AuditActionArchiveMember && event.Mode == "read" {
			memberReads++
		}
	}
	if memberReads != 2 {
		t.Fatalf("member audit attempts=%d events=%+v", memberReads, harness.audit.events)
	}
	eventCount := len(harness.audit.events)
	if err := restarted.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("repeat restart reconcile: %v", err)
	}
	if len(harness.audit.events) != eventCount {
		t.Fatalf("repeat reconciliation duplicated member audit events=%+v", harness.audit.events[eventCount:])
	}
}

func TestDeliveryGatewayArchiveMemberRestartChargesReservedRequestConservativelyAndRejectsReplay(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	requestID := strings.Repeat("6", 32)
	intent := DeliveryReservationIntent{
		RequestID: requestID, GrantID: harness.material.GrantID, Method: http.MethodGet,
		Range:         content.HTTPRange{Kind: content.HTTPRangeFull, Offset: 0, Length: harness.binding.DerivedSize},
		ReservedBytes: harness.binding.DerivedSize,
	}
	if _, err := harness.gateway.budget.Reserve(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := harness.gateway.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestReconciled) || request.FailureCode != "reconciled_crash" ||
		request.ReservedBytes != harness.binding.DerivedSize || request.PlaintextBytes != 0 ||
		request.CiphertextBytes != 0 || request.FinishedAt == nil {
		t.Fatalf("reconciled member request=%+v", request)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, harness.binding.DerivedSize, 0)
	if _, err := harness.gateway.budget.Reserve(context.Background(), intent); !errors.Is(err, ErrDeliveryReplay) {
		t.Fatalf("terminal request replay error=%v", err)
	}
	finalized, err := harness.gateway.budget.Finalize(context.Background(), DeliveryFinalizeIntent{
		RequestID: requestID, State: DeliveryRequestReconciled, FailureCode: "reconciled_crash",
	})
	if err != nil || !finalized.AlreadyFinalized || finalized.ChargedBytes != harness.binding.DerivedSize {
		t.Fatalf("idempotent finalization=%+v err=%v", finalized, err)
	}
	if err := harness.gateway.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("repeat reconcile: %v", err)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, harness.binding.DerivedSize, 0)
}

func TestDeliveryGatewayArchiveMemberRevokeDeniesFurtherServe(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	if err := harness.gateway.RevokeArchiveMember(
		context.Background(), harness.binding.MemberRequestID, "member_canceled",
	); err != nil {
		t.Fatal(err)
	}
	harness.requestIDs <- strings.Repeat("6", 32)
	response := httptest.NewRecorder()
	err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	}, response)
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 ||
		len(harness.source.readBindings) != 0 {
		t.Fatalf("serve err=%v body=%q reads=%d", err, response.Body.Bytes(), len(harness.source.readBindings))
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != "revoked" || grant.RevokeReason != "member_canceled" || grant.RevokedAt == nil {
		t.Fatalf("grant=%+v", grant)
	}
}

func TestDeliveryGatewayArchiveMemberRevokeDrainsActiveRead(t *testing.T) {
	harness := newArchiveMemberDeliveryGatewayHarness(t)
	requestID := strings.Repeat("7", 32)
	harness.requestIDs <- requestID
	writer := newBlockingDeliveryWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: harness.issued.Cookie.String(),
		}, writer)
	}()
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("member read did not reach response writer")
	}
	revokeDone := make(chan error, 1)
	go func() {
		revokeDone <- harness.gateway.RevokeArchiveMember(
			context.Background(), harness.binding.MemberRequestID, "member_canceled",
		)
	}()
	waitForExportGrantState(t, harness.db, harness.material.GrantID, "draining")
	close(writer.release)
	if err := <-serveDone; !errors.Is(err, context.Canceled) || !errors.Is(err, content.ErrContentNotFound) {
		t.Fatalf("serve error=%v", err)
	}
	if err := <-revokeDone; err != nil {
		t.Fatalf("revoke error=%v", err)
	}
	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestCanceled) || request.FailureCode != "client_canceled" ||
		request.FinishedAt == nil {
		t.Fatalf("request=%+v", request)
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, harness.binding.DerivedSize, 0)
}

func TestDeliveryGatewayAuditFailureRevokesIssuedGrantBeforeReturningTicket(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
	for _, row := range []any{&job, &attempt, &key, &artifact} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	sink := &exportAuditSinkSpy{err: errors.New("FAKE_RAW_AUDIT_FAILURE_FOR_TEST_ONLY")}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{},
		Store: store, Audit: audit, Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive,
			Key: bytes.Repeat([]byte{1}, 32),
		}},
		TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: time.Minute, MaxRequests: 2, MaxCumulativeBytes: 1 << 20, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: job.OwnerUserID, Username: "admin", Role: "admin"},
		Session: content.DeliverySession{
			JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
			TokenVersion: 1, ExpiresAt: now.Add(time.Hour),
		},
		ExportJobID: job.ID,
		Proof: content.StepUpProof{
			Action: auth.StepUpActionAssetExportDownload,
			ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(time.Hour),
		},
		SecureCookie: true,
	})
	if !errors.Is(err, ErrDeliveryAudit) || issued.Cookie != nil || issued.Descriptor.ContentURL != "" ||
		strings.Contains(err.Error(), "FAKE_RAW") {
		t.Fatalf("issued=%+v error=%v", issued, err)
	}
	if len(sink.inputs) != 1 || sink.inputs[0].Action != backupasset.AuditActionExportDownloadTicket ||
		sink.inputs[0].ExportJobID != job.ID || sink.inputs[0].StepUpProofID != "" || sink.inputs[0].GrantID != "" {
		t.Fatalf("audit inputs=%+v", sink.inputs)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != "revoked" || grant.RevokeReason != "audit_failed" || grant.RevokedAt == nil {
		t.Fatalf("grant=%+v", grant)
	}
}

func TestDeliveryGatewayRevokeEmitsSuccessfulReadSummaryWithoutRawData(t *testing.T) {
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{audit: audit})
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
		Update("item_count", 3).Error; err != nil {
		t.Fatal(err)
	}

	response, err := harness.serve(strings.Repeat("8", 32), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(), RangeHeaders: []string{"bytes=7-31"},
	})
	if err != nil || response.Code != http.StatusPartialContent || response.Body.Len() != 25 {
		t.Fatalf("serve status=%d bytes=%d err=%v", response.Code, response.Body.Len(), err)
	}
	if err := harness.gateway.RevokeSession(
		context.Background(), strings.Repeat("e", 32), "logout",
	); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	var reads []backupasset.AuditEventInput
	for _, input := range sink.inputs {
		if input.Action == backupasset.AuditActionExportDownload {
			reads = append(reads, input)
		}
	}
	if len(reads) != 1 {
		t.Fatalf("export download audits=%d all=%+v", len(reads), sink.inputs)
	}
	input := reads[0]
	if input.Actor.UserID != harness.job.OwnerUserID || input.Actor.Role != "admin" ||
		input.Outcome != backupasset.AuditOutcomeSuccess || input.ExportJobID != harness.job.ID ||
		input.ItemCount != 3 || input.ByteCount != 25 || input.Range.Count != 1 || input.Range.Bytes != 25 ||
		input.FailureCode != "" {
		t.Fatalf("read audit=%+v", input)
	}
	if input.GrantID != "" || input.StepUpAction != "" || input.StepUpProofID != "" ||
		input.RepositoryID != "" || input.RecoveryPointID != "" || input.EntryID != "" ||
		input.Fingerprints.Path != "" || input.Fingerprints.Query != "" {
		t.Fatalf("read audit carried forbidden identity=%+v", input)
	}
	wantFields := map[backupasset.AuditField]any{
		backupasset.AuditFieldSource: harness.job.SelectionDigest,
		backupasset.AuditFieldFormat: harness.job.ArchiveFormat,
	}
	if fmt.Sprint(input.Fields) != fmt.Sprint(wantFields) {
		t.Fatalf("read audit fields=%v want=%v", input.Fields, wantFields)
	}
}

func TestDeliveryGatewayAuditFailurePersistsRetryAndRestartConvergesOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	clock := now
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{
		audit: audit, now: func() time.Time { return clock },
	})
	requestID := strings.Repeat("8", 32)
	response, err := harness.serve(requestID, content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(), RangeHeaders: []string{"bytes=7-31"},
	})
	if err != nil || response.Code != http.StatusPartialContent || response.Body.Len() != 25 {
		t.Fatalf("serve status=%d bytes=%d err=%v", response.Code, response.Body.Len(), err)
	}

	sink.err = errors.New("FAKE_RAW_AUDIT_FAILURE_FOR_TEST_ONLY")
	if err := harness.gateway.RevokeSession(
		context.Background(), strings.Repeat("e", 32), "logout",
	); !errors.Is(err, ErrDeliveryAudit) {
		t.Fatalf("revoke audit error=%v", err)
	}
	var failedGrant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&failedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if failedGrant.State != "revoked" || failedGrant.RevokeReason != "logout" ||
		failedGrant.InFlight != 0 || failedGrant.ReservedBytes != 0 || failedGrant.ConsumedBytes <= 0 ||
		failedGrant.AuditState != "retry_wait" || failedGrant.AuditRequestCount != 1 ||
		failedGrant.AuditSuccessCount != 1 || failedGrant.AuditBlockedCount != 0 ||
		failedGrant.AuditFailureCount != 0 || failedGrant.AuditAttemptCount != 1 ||
		failedGrant.AuditFailureCode != "audit_write_failed" || failedGrant.AuditNextAttemptAt == nil ||
		!failedGrant.AuditNextAttemptAt.After(clock) {
		t.Fatalf("failed audit grant=%+v", failedGrant)
	}
	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestSucceeded) || request.PlaintextBytes != 25 ||
		request.CiphertextBytes <= 0 || request.FinishedAt == nil {
		t.Fatalf("request truth rolled back after audit failure=%+v", request)
	}

	failedAttemptCount := countExportDownloadAuditInputs(sink.inputs)
	if failedAttemptCount != 1 {
		t.Fatalf("failed audit attempts=%d inputs=%+v", failedAttemptCount, sink.inputs)
	}
	sink.err = nil
	clock = failedGrant.AuditNextAttemptAt.Add(time.Second)
	restarted, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: harness.db, Now: func() time.Time { return clock }, Session: &deliverySessionValidatorStub{},
		Store: harness.store, Audit: audit, Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive,
			Key: bytes.Repeat([]byte{9}, 32),
		}},
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 32,
			MaxCumulativeBytes: 64 * harness.result.CiphertextBytes, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	var emitted model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&emitted).Error; err != nil {
		t.Fatal(err)
	}
	if emitted.AuditState != "emitted" || emitted.AuditAttemptCount != 1 ||
		emitted.AuditFailureCode != "" || emitted.AuditNextAttemptAt != nil {
		t.Fatalf("reconciled audit grant=%+v", emitted)
	}
	if attempts := countExportDownloadAuditInputs(sink.inputs); attempts != 2 {
		t.Fatalf("audit attempts after restart=%d inputs=%+v", attempts, sink.inputs)
	}
	if err := restarted.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("repeat restart reconcile: %v", err)
	}
	if attempts := countExportDownloadAuditInputs(sink.inputs); attempts != 2 {
		t.Fatalf("repeat reconcile duplicated audit attempts=%d inputs=%+v", attempts, sink.inputs)
	}
}

func TestDeliveryGatewayAuditReceiptAmbiguityRetriesAtLeastOnceWithoutGrantIdentity(t *testing.T) {
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{audit: audit})
	requestID := strings.Repeat("8", 32)
	response, err := harness.serve(requestID, content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	})
	if err != nil || response.Code != http.StatusOK || response.Body.Len() != len(harness.plaintext) {
		t.Fatalf("serve status=%d bytes=%d err=%v", response.Code, response.Body.Len(), err)
	}

	sink.afterWrite = func(input backupasset.AuditEventInput) {
		if input.Action != backupasset.AuditActionExportDownload {
			return
		}
		if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
			Where("id = ?", harness.material.GrantID).
			Update("version", gorm.Expr("version + 1")).Error; err != nil {
			t.Fatalf("inject post-sink receipt conflict: %v", err)
		}
	}
	if err := harness.gateway.RevokeSession(
		context.Background(), strings.Repeat("e", 32), "logout",
	); !errors.Is(err, ErrDeliveryAudit) {
		t.Fatalf("revoke audit receipt error=%v", err)
	}
	if attempts := countExportDownloadAuditInputs(sink.inputs); attempts != 1 {
		t.Fatalf("audit attempts before restart=%d inputs=%+v", attempts, sink.inputs)
	}

	sink.afterWrite = nil
	if err := harness.gateway.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("restart reconcile ambiguous receipt: %v", err)
	}
	if attempts := countExportDownloadAuditInputs(sink.inputs); attempts != 2 {
		t.Fatalf("ambiguous audit was not retried at least once attempts=%d inputs=%+v", attempts, sink.inputs)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.AuditState != "emitted" || grant.AuditFailureCode != "" ||
		grant.AuditNextAttemptAt != nil {
		t.Fatalf("ambiguous audit grant=%+v", grant)
	}
	if err := harness.gateway.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("repeat reconcile emitted receipt: %v", err)
	}
	if attempts := countExportDownloadAuditInputs(sink.inputs); attempts != 2 {
		t.Fatalf("settled audit was emitted again attempts=%d inputs=%+v", attempts, sink.inputs)
	}
}

func TestDeliveryAuditAggregatePrecedenceIsOrderIndependent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	job, _, _, _ := readyExportDeliveryFixture(t, now)
	grant := deliveryGrantFixture(now)
	grant.AuditState = "pending"
	grant.RequestCount = 5
	grant.ConsumedBytes = 17
	grant.AuditRequestCount = 5
	grant.AuditSuccessCount = 1
	grant.AuditBlockedCount = 1
	grant.AuditFailureCount = 3
	grant.AuditRangeCount = 2
	grant.AuditRangeBytes = 4
	finished := now
	offset, length := int64(7), int64(4)
	requests := []model.BackupAssetExportDeliveryRequest{
		{ID: strings.Repeat("1", 32), GrantID: grant.ID, Method: http.MethodGet, State: string(DeliveryRequestSucceeded), PlaintextBytes: 10, FinishedAt: &finished},
		{ID: strings.Repeat("2", 32), GrantID: grant.ID, Method: http.MethodGet, State: string(DeliveryRequestBlocked), RangeOffset: &offset, RangeLength: &length, FailureCode: "request_too_large", FinishedAt: &finished},
		{ID: strings.Repeat("3", 32), GrantID: grant.ID, Method: http.MethodGet, State: string(DeliveryRequestCanceled), RangeOffset: &offset, RangeLength: &length, PlaintextBytes: 4, FailureCode: "client_canceled", FinishedAt: &finished},
		{ID: strings.Repeat("4", 32), GrantID: grant.ID, Method: http.MethodGet, State: string(DeliveryRequestFailed), PlaintextBytes: 3, FailureCode: "delivery_failed", FinishedAt: &finished},
		{ID: strings.Repeat("5", 32), GrantID: grant.ID, Method: http.MethodGet, State: string(DeliveryRequestReconciled), FailureCode: "reconciled_crash", FinishedAt: &finished},
	}
	orders := [][]model.BackupAssetExportDeliveryRequest{
		requests,
		{requests[4], requests[3], requests[2], requests[1], requests[0]},
	}
	for index, ordered := range orders {
		event, err := deliveryAuditEvent(grant, job, ordered)
		if err != nil {
			t.Fatalf("order %d audit event: %v", index, err)
		}
		if event.Outcome != backupasset.AuditOutcomeFailure || event.ErrorCategory != "reconciled_crash" ||
			event.ByteCount != 17 || event.RangeCount != 2 || event.RangeBytes != 4 {
			t.Fatalf("order %d audit event=%+v", index, event)
		}
	}
}

func TestDeliveryGatewayFullRequestLimitAuditDoesNotClaimRange(t *testing.T) {
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{audit: audit})
	if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ?", harness.material.GrantID).
		Update("max_cumulative_bytes", int64(32)).Error; err != nil {
		t.Fatal(err)
	}
	response, err := harness.serve(strings.Repeat("8", 32), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.issued.Cookie.String(),
	})
	if err != nil || response.Code != http.StatusRequestEntityTooLarge || response.Body.Len() != 0 {
		t.Fatalf("full limit response status=%d body=%q err=%v", response.Code, response.Body.Bytes(), err)
	}
	if err := harness.gateway.RevokeSession(
		context.Background(), strings.Repeat("e", 32), "logout",
	); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	var input *backupasset.AuditEventInput
	for index := range sink.inputs {
		if sink.inputs[index].Action == backupasset.AuditActionExportDownload {
			input = &sink.inputs[index]
		}
	}
	if input == nil || input.Outcome != backupasset.AuditOutcomeBlocked ||
		input.FailureCode != "request_too_large" || input.ByteCount != 0 ||
		input.Range.Count != 0 || input.Range.Bytes != 0 {
		t.Fatalf("full limit audit=%+v all=%+v", input, sink.inputs)
	}
}

func countExportDownloadAuditInputs(inputs []backupasset.AuditEventInput) int {
	count := 0
	for _, input := range inputs {
		if input.Action == backupasset.AuditActionExportDownload {
			count++
		}
	}
	return count
}

func TestDeliveryGatewayServesAuthenticatedPlaintextRangeWithIndependentBudget(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 12_000)
	dek := bytes.Repeat([]byte{7}, 32)
	kek := bytes.Repeat([]byte{9}, 32)
	envelope, err := WrapJobDEK(JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: 2,
		WrapAlgorithm: JobKeyWrapAlgorithmV1,
	}, kek, dek)
	if err != nil {
		t.Fatal(err)
	}
	key.WrappedDEK, key.EnvelopeNonce, key.KEKVersion = envelope.Ciphertext, envelope.Nonce, 2
	staging, err := store.CreateStaging(job.ID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := CipherBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, ArchiveProfile: job.ArchiveProfile,
		FormatVersion: 1, AttemptFenceDigest: attempt.FenceDigest, Purpose: CipherPurposeFinalArchive,
	}
	result, err := EncryptStream(context.Background(), staging.File, bytes.NewReader(plaintext), dek, binding, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	attempt.NoncePrefix = append([]byte(nil), result.NoncePrefix...)
	artifact.Locator, artifact.NoncePrefix = locator, append([]byte(nil), result.NoncePrefix...)
	artifact.ChunkBytes, artifact.ChunkCount = 64<<10, result.ChunkCount
	artifact.PlaintextDigest, artifact.ArchiveDigest = result.PlaintextDigest, result.ArchiveDigest
	artifact.CiphertextDigest = result.CiphertextDigest
	artifact.PlaintextSize, artifact.CiphertextSize = result.PlaintextBytes, result.CiphertextBytes
	job.ArtifactBytes = result.CiphertextBytes
	for _, row := range []any{&job, &attempt, &key, &artifact} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	keySource := &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
		Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive, Key: kek,
	}}
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{},
		Store: store, Audit: mustDeliveryAudit(t), Keys: keySource, RequestID: func() (string, error) { return strings.Repeat("8", 32), nil },
		TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4,
			MaxCumulativeBytes: 4 * result.CiphertextBytes, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	proof := content.StepUpProof{
		Action: auth.StepUpActionAssetExportDownload,
		ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(20 * time.Minute),
	}
	issued, err := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: job.OwnerUserID, Role: "admin"},
		Session: content.DeliverySession{
			JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
			TokenVersion: 3, ExpiresAt: now.Add(30 * time.Minute),
		},
		ExportJobID: job.ID, Proof: proof, SecureCookie: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	matched, err := gateway.MatchesDelivery(context.Background(), material.DeliveryID)
	if err != nil || !matched {
		t.Fatalf("match=%v err=%v", matched, err)
	}

	response := httptest.NewRecorder()
	err = gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: material.DeliveryID, Method: http.MethodGet,
		RawCookie: issued.Cookie.String(), RangeHeaders: []string{"bytes=65530-65590"},
	}, response)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), plaintext[65530:65591]) {
		t.Fatalf("status=%d body_len=%d", response.Code, response.Body.Len())
	}
	if response.Header().Get("Accept-Ranges") != "bytes" ||
		response.Header().Get("Content-Range") != fmt.Sprintf("bytes 65530-65590/%d", len(plaintext)) ||
		response.Header().Get("ETag") != issued.Descriptor.ETag ||
		response.Header().Get("Content-Disposition") != `attachment; filename="xirang-export-aaaaaaaaaaaaaaaa.zip"` {
		t.Fatalf("headers=%v", response.Header())
	}
	var request model.BackupAssetExportDeliveryRequest
	if err := db.Where("grant_id = ?", material.GrantID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestSucceeded) || request.PlaintextBytes != 61 ||
		request.CiphertextBytes <= request.PlaintextBytes || request.FinishedAt == nil {
		t.Fatalf("request=%+v", request)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.RequestCount != 1 || grant.ReservedBytes != 0 || grant.InFlight != 0 ||
		grant.ConsumedBytes != request.CiphertextBytes {
		t.Fatalf("grant=%+v", grant)
	}
	if keySource.calls != 2 {
		t.Fatalf("key ByVersion calls=%d", keySource.calls)
	}

	gateway.requestID = func() (string, error) { return strings.Repeat("7", 32), nil }
	blockedResponse := httptest.NewRecorder()
	err = gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: material.DeliveryID, Method: http.MethodGet, RawCookie: issued.Cookie.String(),
		RangeHeaders: []string{"bytes=0-1,4-5"},
	}, blockedResponse)
	if err != nil {
		t.Fatalf("blocked range serve: %v", err)
	}
	if blockedResponse.Code != http.StatusRequestedRangeNotSatisfiable || blockedResponse.Body.Len() != 0 ||
		blockedResponse.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", len(plaintext)) {
		t.Fatalf("blocked status=%d headers=%v body=%q", blockedResponse.Code, blockedResponse.Header(), blockedResponse.Body.Bytes())
	}
	var blocked model.BackupAssetExportDeliveryRequest
	if err := db.Where("id = ?", strings.Repeat("7", 32)).Take(&blocked).Error; err != nil {
		t.Fatal(err)
	}
	if blocked.State != string(DeliveryRequestBlocked) || blocked.FailureCode != string(content.RequestFailureInvalidRange) ||
		blocked.ReservedBytes != 0 || blocked.PlaintextBytes != 0 || blocked.CiphertextBytes != 0 || blocked.FinishedAt == nil {
		t.Fatalf("blocked request=%+v", blocked)
	}
	if keySource.calls != 2 {
		t.Fatalf("blocked range unexpectedly loaded key: calls=%d", keySource.calls)
	}

	gateway.requestID = func() (string, error) { return strings.Repeat("6", 32), nil }
	headResponse := httptest.NewRecorder()
	err = gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: material.DeliveryID, Method: http.MethodHead, RawCookie: issued.Cookie.String(),
	}, headResponse)
	if err != nil {
		t.Fatalf("HEAD serve: %v", err)
	}
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 ||
		headResponse.Header().Get("Content-Length") != fmt.Sprintf("%d", len(plaintext)) {
		t.Fatalf("HEAD status=%d headers=%v body=%q", headResponse.Code, headResponse.Header(), headResponse.Body.Bytes())
	}
	if keySource.calls != 2 {
		t.Fatalf("HEAD unexpectedly loaded/decrypted key: calls=%d", keySource.calls)
	}
	var headRequest model.BackupAssetExportDeliveryRequest
	if err := db.Where("id = ?", strings.Repeat("6", 32)).Take(&headRequest).Error; err != nil {
		t.Fatal(err)
	}
	if headRequest.State != string(DeliveryRequestSucceeded) || headRequest.ReservedBytes != 0 ||
		headRequest.PlaintextBytes != 0 || headRequest.CiphertextBytes != 0 || headRequest.FinishedAt == nil {
		t.Fatalf("HEAD request=%+v", headRequest)
	}

	gateway.requestID = func() (string, error) { return strings.Repeat("5", 32), nil }
	blockingWriter := newBlockingDeliveryWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: material.DeliveryID, Method: http.MethodGet, RawCookie: issued.Cookie.String(),
			RangeHeaders: []string{"bytes=0-131071"},
		}, blockingWriter)
	}()
	select {
	case <-blockingWriter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not reach first authenticated chunk")
	}
	revokeDone := make(chan error, 1)
	go func() { revokeDone <- gateway.RevokeSession(context.Background(), strings.Repeat("e", 32), "logout") }()
	waitForExportGrantState(t, db, material.GrantID, "draining")
	close(blockingWriter.release)
	select {
	case err := <-serveDone:
		if !errors.Is(err, content.ErrContentNotFound) {
			t.Fatalf("serve after mid-stream revoke error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked stream did not exit")
	}
	select {
	case err := <-revokeDone:
		if err != nil {
			t.Fatalf("revoke active stream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoke did not drain active stream")
	}
	if blockingWriter.body.Len() != 64<<10 {
		t.Fatalf("revoked stream wrote %d bytes, want exactly first authenticated chunk", blockingWriter.body.Len())
	}
	var canceledRequest model.BackupAssetExportDeliveryRequest
	if err := db.Where("id = ?", strings.Repeat("5", 32)).Take(&canceledRequest).Error; err != nil {
		t.Fatal(err)
	}
	if canceledRequest.State != string(DeliveryRequestCanceled) || canceledRequest.FinishedAt == nil {
		t.Fatalf("canceled request=%+v", canceledRequest)
	}
	var revokedGrant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", material.GrantID).Take(&revokedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if revokedGrant.State != "revoked" || revokedGrant.RevokeReason != "logout" ||
		revokedGrant.InFlight != 0 || revokedGrant.ReservedBytes != 0 {
		t.Fatalf("revoked grant=%+v", revokedGrant)
	}
}

func TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	if err := fixture.harness.db.AutoMigrate(&model.BackupAssetDeliveryGrant{}); err != nil {
		t.Fatal(err)
	}
	published, err := fixture.worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || published.Action != PersistentReconcilePublished {
		t.Fatalf("publish worker artifact=%+v err=%v", published, err)
	}
	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ?", fixture.artifactID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	requestIDs := make(chan string, 2)
	requestIDs <- strings.Repeat("8", 32)
	requestIDs <- strings.Repeat("9", 32)
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: fixture.harness.db, Now: func() time.Time { return fixture.clock },
		Session: &deliverySessionValidatorStub{}, Store: fixture.store, Keys: fixture.ring,
		Audit: mustDeliveryAudit(t), TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		RequestID: func() (string, error) { return <-requestIDs, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4,
			MaxCumulativeBytes: artifact.CiphertextSize * 8, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: 100, Role: "admin"},
		Session: content.DeliverySession{
			JTI: strings.Repeat("7", 32), UserID: 100, Role: "admin", TokenVersion: 1,
			ExpiresAt: fixture.clock.Add(30 * time.Minute),
		},
		ExportJobID: fixture.jobID,
		Proof: content.StepUpProof{
			Action: auth.StepUpActionAssetExportDownload, ID: strings.Repeat("6", 32),
			ExpiresAt: fixture.clock.Add(20 * time.Minute),
		},
		SecureCookie: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	full := httptest.NewRecorder()
	if err := gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: material.DeliveryID, Method: http.MethodGet, RawCookie: issued.Cookie.String(),
	}, full); err != nil {
		t.Fatalf("serve worker artifact: %v", err)
	}
	if full.Code != http.StatusOK || int64(full.Body.Len()) != artifact.PlaintextSize {
		t.Fatalf("full response status=%d bytes=%d want=%d", full.Code, full.Body.Len(), artifact.PlaintextSize)
	}
	if _, err := zip.NewReader(bytes.NewReader(full.Body.Bytes()), int64(full.Body.Len())); err != nil {
		t.Fatalf("delivered worker artifact is not a ZIP: %v", err)
	}

	start, end := int64(17), int64(96)
	partial := httptest.NewRecorder()
	if err := gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: material.DeliveryID, Method: http.MethodGet, RawCookie: issued.Cookie.String(),
		RangeHeaders: []string{fmt.Sprintf("bytes=%d-%d", start, end)},
	}, partial); err != nil {
		t.Fatalf("serve worker artifact range: %v", err)
	}
	if partial.Code != http.StatusPartialContent ||
		!bytes.Equal(partial.Body.Bytes(), full.Body.Bytes()[start:end+1]) {
		t.Fatalf("range status=%d bytes=%x", partial.Code, partial.Body.Bytes())
	}
}

func TestDeliveryGatewayTamperedCiphertextFailsClosedAndChargesReservation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	root := t.TempDir()
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
	plaintext := bytes.Repeat([]byte("authenticated-export"), 8192)
	dek := bytes.Repeat([]byte{7}, 32)
	kek := bytes.Repeat([]byte{9}, 32)
	envelope, err := WrapJobDEK(JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: 2,
		WrapAlgorithm: JobKeyWrapAlgorithmV1,
	}, kek, dek)
	if err != nil {
		t.Fatal(err)
	}
	key.WrappedDEK, key.EnvelopeNonce, key.KEKVersion = envelope.Ciphertext, envelope.Nonce, 2
	staging, err := store.CreateStaging(job.ID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := CipherBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, ArchiveProfile: job.ArchiveProfile,
		FormatVersion: 1, AttemptFenceDigest: attempt.FenceDigest, Purpose: CipherPurposeFinalArchive,
	}
	result, err := EncryptStream(context.Background(), staging.File, bytes.NewReader(plaintext), dek, binding, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	attempt.NoncePrefix = append([]byte(nil), result.NoncePrefix...)
	artifact.Locator, artifact.NoncePrefix = locator, append([]byte(nil), result.NoncePrefix...)
	artifact.ChunkBytes, artifact.ChunkCount = 64<<10, result.ChunkCount
	artifact.PlaintextDigest, artifact.ArchiveDigest = result.PlaintextDigest, result.ArchiveDigest
	artifact.CiphertextDigest = result.CiphertextDigest
	artifact.PlaintextSize, artifact.CiphertextSize = result.PlaintextBytes, result.CiphertextBytes
	job.ArtifactBytes = result.CiphertextBytes
	for _, row := range []any{&job, &attempt, &key, &artifact} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	requestID := strings.Repeat("8", 32)
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{},
		Store: store, Audit: mustDeliveryAudit(t), Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive, Key: kek,
		}},
		RequestID:      func() (string, error) { return requestID, nil },
		TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 2,
			MaxCumulativeBytes: 4 * result.CiphertextBytes, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: job.OwnerUserID, Role: "admin"},
		Session: content.DeliverySession{
			JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
			TokenVersion: 3, ExpiresAt: now.Add(30 * time.Minute),
		},
		ExportJobID: job.ID,
		Proof: content.StepUpProof{
			Action: auth.StepUpActionAssetExportDownload,
			ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(20 * time.Minute),
		},
		SecureCookie: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, locator), result.CiphertextBytes-1); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	err = gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: material.DeliveryID, Method: http.MethodGet, RawCookie: issued.Cookie.String(),
	}, response)
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
		t.Fatalf("serve error=%v status=%d headers=%v body=%q", err, response.Code, response.Header(), response.Body.Bytes())
	}
	var request model.BackupAssetExportDeliveryRequest
	if err := db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestFailed) || request.FailureCode != "delivery_failed" ||
		request.PlaintextBytes != 0 || request.CiphertextBytes != 0 || request.FinishedAt == nil {
		t.Fatalf("request=%+v", request)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", material.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	wantCharge := result.CiphertextBytes * 2
	if grant.RequestCount != 1 || grant.ReservedBytes != 0 || grant.InFlight != 0 ||
		grant.ConsumedBytes != wantCharge {
		t.Fatalf("grant=%+v want_charge=%d", grant, wantCharge)
	}
}

type blockingDeliveryHeaderWriter struct {
	header          http.Header
	headerRequested chan struct{}
	releaseHeader   chan struct{}
	once            sync.Once
	status          int
}

func newBlockingDeliveryHeaderWriter() *blockingDeliveryHeaderWriter {
	return &blockingDeliveryHeaderWriter{
		header: make(http.Header), headerRequested: make(chan struct{}), releaseHeader: make(chan struct{}),
	}
}

func (writer *blockingDeliveryHeaderWriter) Header() http.Header {
	writer.once.Do(func() {
		close(writer.headerRequested)
		<-writer.releaseHeader
	})
	return writer.header
}

func (writer *blockingDeliveryHeaderWriter) WriteHeader(status int) { writer.status = status }

func (writer *blockingDeliveryHeaderWriter) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func assertNoSuccessfulDeliveryMetadata(t *testing.T, writer *blockingDeliveryHeaderWriter) {
	t.Helper()
	if writer.status == http.StatusOK || writer.status == http.StatusPartialContent || len(writer.header) != 0 {
		t.Fatalf("successful metadata status=%d headers=%v", writer.status, writer.header)
	}
}

type blockingDeliveryWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	body    bytes.Buffer
	status  int
}

func newBlockingDeliveryWriter() *blockingDeliveryWriter {
	return &blockingDeliveryWriter{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
}

func (writer *blockingDeliveryWriter) Header() http.Header { return writer.header }

func (writer *blockingDeliveryWriter) WriteHeader(status int) { writer.status = status }

func (writer *blockingDeliveryWriter) Write(payload []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.body.Write(payload)
}

func waitForExportGrantState(t *testing.T, db *gorm.DB, grantID, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var grant model.BackupAssetExportDeliveryGrant
		if err := db.Select("state").Where("id = ?", grantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if grant.State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("grant %s did not reach state %s", grantID, state)
}

func TestDeliveryGatewayRevokeSessionClosesOnlyExactLedgerBindings(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{}, Store: store,
		Audit: mustDeliveryAudit(t),
		Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 1, State: backupasset.DomainKeyActive,
			Key: bytes.Repeat([]byte{1}, 32),
		}},
		Config: DeliveryGatewayConfig{
			TicketTTL: time.Minute, MaxRequests: 4, MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionJTI := strings.Repeat("e", 32)
	first := deliveryGrantFixture(now)
	second := deliveryGrantFixture(now)
	second.ID, second.DeliveryID = strings.Repeat("5", 32), strings.Repeat("6", 32)
	third := deliveryGrantFixture(now)
	third.ID, third.DeliveryID, third.SessionJTI = strings.Repeat("7", 32), strings.Repeat("8", 32), strings.Repeat("9", 32)
	for _, grant := range []*model.BackupAssetExportDeliveryGrant{&first, &second, &third} {
		if err := db.Create(grant).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := gateway.RevokeSession(context.Background(), sessionJTI, "logout"); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	var rows []model.BackupAssetExportDeliveryGrant
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.SessionJTI == sessionJTI {
			if row.State != "revoked" || row.RevokeReason != "logout" || row.RevokedAt == nil ||
				!row.RevokedAt.Equal(now) || row.Version != 3 {
				t.Fatalf("revoked row=%+v", row)
			}
		} else if row.State != "active" || row.RevokedAt != nil || row.Version != 1 {
			t.Fatalf("foreign session row changed=%+v", row)
		}
	}
	matched, err := gateway.MatchesDelivery(context.Background(), first.DeliveryID)
	if err != nil || !matched {
		t.Fatalf("revoked typed match=%v err=%v", matched, err)
	}
	response := httptest.NewRecorder()
	if err := gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: first.DeliveryID, Method: http.MethodGet,
		RawCookie: content.DeliveryCookieName + "=v1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}, response); !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 {
		t.Fatalf("serve after revoke err=%v body=%q", err, response.Body.Bytes())
	}
}

func TestDeliveryGatewayRevokesAndDrainsOnlyExactExportJob(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})

	if err := harness.gateway.BeginRevokeExportJob(
		context.Background(), harness.job.ID, "job_canceled",
	); err != nil {
		t.Fatalf("begin export job revoke: %v", err)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("delivery_id = ?", harness.material.DeliveryID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != "draining" || grant.RevokeReason != "job_canceled" || grant.RevokedAt != nil {
		t.Fatalf("grant after begin revoke=%+v", grant)
	}
	response, err := harness.serve(strings.Repeat("a", 32), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: harness.material.CookieSecret,
	})
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 {
		t.Fatalf("serve after begin revoke error=%v bytes=%d", err, response.Body.Len())
	}

	if err := harness.gateway.DrainExportJob(context.Background(), harness.job.ID); err != nil {
		t.Fatalf("drain export job: %v", err)
	}
	if err := harness.db.Where("delivery_id = ?", harness.material.DeliveryID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != "revoked" || grant.RevokeReason != "job_canceled" || grant.RevokedAt == nil {
		t.Fatalf("grant after drain=%+v", grant)
	}
	if err := harness.gateway.BeginRevokeExportJob(
		context.Background(), strings.Repeat("b", 32), "FAKE_UNBOUNDED_REASON_FOR_TEST_ONLY",
	); !errors.Is(err, ErrInvalidDeliveryRequest) {
		t.Fatalf("invalid revoke reason error=%v", err)
	}
}

func TestControlledProcessSIGKILLThenRestartReconciles(t *testing.T) {
	if os.Getenv("BACKUP_ASSET_SIGKILL_HELPER") == "1" {
		select {}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestControlledProcessSIGKILLThenRestartReconciles$")
	cmd.Env = append(os.Environ(), "BACKUP_ASSET_SIGKILL_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation(t)
}

func TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{}, Store: store,
		Audit: mustDeliveryAudit(t),
		Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
			Domain: backupasset.KeyDomainExportStore, Version: 1, State: backupasset.DomainKeyActive,
			Key: bytes.Repeat([]byte{1}, 32),
		}},
		Config: DeliveryGatewayConfig{
			TicketTTL: time.Minute, MaxRequests: 4, MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _, _ := readyExportDeliveryFixture(t, now)
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	draining := deliveryGrantFixture(now)
	draining.State, draining.RevokeReason = "draining", "logout"
	draining.RequestCount, draining.ReservedBytes, draining.InFlight = 1, 200, 1
	active := deliveryGrantFixture(now)
	active.ID, active.DeliveryID, active.SessionJTI = strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32)
	if err := db.Create(&draining).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	pending := model.BackupAssetExportDeliveryRequest{
		ID: strings.Repeat("8", 32), GrantID: draining.ID, Method: http.MethodGet,
		State: string(DeliveryRequestReserved), ReservedBytes: 200,
		StartedAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if err := gateway.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("reconcile deliveries: %v", err)
	}
	if err := gateway.ReconcileDeliveries(context.Background()); err != nil {
		t.Fatalf("idempotent reconcile deliveries: %v", err)
	}
	var reconciledRequest model.BackupAssetExportDeliveryRequest
	if err := db.Where("id = ?", pending.ID).Take(&reconciledRequest).Error; err != nil {
		t.Fatal(err)
	}
	if reconciledRequest.State != string(DeliveryRequestReconciled) ||
		reconciledRequest.FailureCode != "reconciled_crash" || reconciledRequest.FinishedAt == nil {
		t.Fatalf("request=%+v", reconciledRequest)
	}
	for _, id := range []string{draining.ID, active.ID} {
		var grant model.BackupAssetExportDeliveryGrant
		if err := db.Where("id = ?", id).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if grant.State != "revoked" || grant.RevokeReason != "process_restarted" || grant.RevokedAt == nil {
			t.Fatalf("grant=%+v", grant)
		}
		if id == draining.ID && (grant.ReservedBytes != 0 || grant.InFlight != 0 || grant.ConsumedBytes != 200) {
			t.Fatalf("reconciled counters=%+v", grant)
		}
	}
}

func TestDeliveryGatewayMaintenanceFlushesTerminalAuditWithoutRevokingActiveDelivery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	clock := now
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{
		audit: audit,
		now:   func() time.Time { return clock },
	})

	harness.requestIDs <- strings.Repeat("8", 32)
	response := httptest.NewRecorder()
	if err := harness.gateway.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID,
		Method:     http.MethodGet,
		RawCookie:  harness.issued.Cookie.String(),
	}, response); err != nil || response.Code != http.StatusOK {
		t.Fatalf("seed terminal audit response=%d err=%v", response.Code, err)
	}
	sink.err = errors.New("FAKE_MAINTENANCE_AUDIT_FAILURE_FOR_TEST_ONLY")
	if err := harness.gateway.RevokeSession(context.Background(), strings.Repeat("e", 32), "logout"); !errors.Is(err, ErrDeliveryAudit) {
		t.Fatalf("seed audit failure=%v", err)
	}
	var retrying model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&retrying).Error; err != nil {
		t.Fatal(err)
	}
	if retrying.AuditState != "retry_wait" || retrying.AuditNextAttemptAt == nil {
		t.Fatalf("terminal audit was not queued for retry: %+v", retrying)
	}

	active := deliveryGrantFixture(now)
	active.ID = strings.Repeat("7", 32)
	active.DeliveryID = strings.Repeat("6", 32)
	active.SessionJTI = strings.Repeat("5", 32)
	if err := harness.db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	pending := model.BackupAssetExportDeliveryRequest{
		ID: strings.Repeat("4", 32), GrantID: active.ID, Method: http.MethodGet,
		State: string(DeliveryRequestReserved), ReservedBytes: 200,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := harness.db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}

	sink.err = nil
	clock = retrying.AuditNextAttemptAt.Add(time.Second)
	if err := harness.gateway.MaintainDeliveries(context.Background()); err != nil {
		t.Fatalf("maintain deliveries: %v", err)
	}

	var emitted model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", retrying.ID).Take(&emitted).Error; err != nil {
		t.Fatal(err)
	}
	if emitted.AuditState != "emitted" {
		t.Fatalf("terminal audit not emitted: %+v", emitted)
	}
	var preservedGrant model.BackupAssetExportDeliveryGrant
	if err := harness.db.Where("id = ?", active.ID).Take(&preservedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if preservedGrant.State != "active" || preservedGrant.RevokeReason != "" || preservedGrant.RevokedAt != nil {
		t.Fatalf("maintenance changed active grant: %+v", preservedGrant)
	}
	var preservedRequest model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", pending.ID).Take(&preservedRequest).Error; err != nil {
		t.Fatal(err)
	}
	if preservedRequest.State != string(DeliveryRequestReserved) || preservedRequest.FinishedAt != nil {
		t.Fatalf("maintenance reconciled open request: %+v", preservedRequest)
	}
}

func TestDeliveryGatewayServesFullSingleRangeAndIfRangeMatrix(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	tests := []struct {
		name       string
		method     string
		ranges     []string
		ifRanges   []string
		wantStatus int
		wantBody   []byte
		wantRange  string
	}{
		{name: "full GET", method: http.MethodGet, wantStatus: http.StatusOK, wantBody: harness.plaintext},
		{name: "normal Range", method: http.MethodGet, ranges: []string{"bytes=7-31"}, wantStatus: http.StatusPartialContent, wantBody: harness.plaintext[7:32], wantRange: fmt.Sprintf("bytes 7-31/%d", len(harness.plaintext))},
		{name: "open Range", method: http.MethodGet, ranges: []string{"bytes=65530-"}, wantStatus: http.StatusPartialContent, wantBody: harness.plaintext[65530:], wantRange: fmt.Sprintf("bytes 65530-%d/%d", len(harness.plaintext)-1, len(harness.plaintext))},
		{name: "suffix Range", method: http.MethodGet, ranges: []string{"bytes=-19"}, wantStatus: http.StatusPartialContent, wantBody: harness.plaintext[len(harness.plaintext)-19:], wantRange: fmt.Sprintf("bytes %d-%d/%d", len(harness.plaintext)-19, len(harness.plaintext)-1, len(harness.plaintext))},
		{name: "matching If-Range", method: http.MethodGet, ranges: []string{"bytes=64-79"}, ifRanges: []string{harness.issued.Descriptor.ETag}, wantStatus: http.StatusPartialContent, wantBody: harness.plaintext[64:80], wantRange: fmt.Sprintf("bytes 64-79/%d", len(harness.plaintext))},
		{name: "mismatching If-Range falls back full", method: http.MethodGet, ranges: []string{"bytes=64-79"}, ifRanges: []string{`"other"`}, wantStatus: http.StatusOK, wantBody: harness.plaintext},
		{name: "HEAD Range", method: http.MethodHead, ranges: []string{"bytes=20-29"}, wantStatus: http.StatusPartialContent, wantRange: fmt.Sprintf("bytes 20-29/%d", len(harness.plaintext))},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := fmt.Sprintf("%032x", 100+index)
			response, err := harness.serve(requestID, content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: test.method,
				RawCookie: harness.issued.Cookie.String(), RangeHeaders: test.ranges, IfRangeHeaders: test.ifRanges,
			})
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
			if response.Code != test.wantStatus || !bytes.Equal(response.Body.Bytes(), test.wantBody) ||
				response.Header().Get("Content-Range") != test.wantRange ||
				response.Header().Get("ETag") != harness.issued.Descriptor.ETag {
				t.Fatalf("status=%d headers=%v body_len=%d", response.Code, response.Header(), response.Body.Len())
			}
			var request model.BackupAssetExportDeliveryRequest
			if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
				t.Fatal(err)
			}
			if request.State != string(DeliveryRequestSucceeded) || request.FinishedAt == nil {
				t.Fatalf("request=%+v", request)
			}
			if test.method == http.MethodHead && (request.PlaintextBytes != 0 || request.CiphertextBytes != 0) {
				t.Fatalf("HEAD request=%+v", request)
			}
		})
	}
}

func TestDeliveryGatewayServesReadyExportWithVerifyOnlyKEKAfterRotation(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	keys, ok := harness.gateway.keys.(*deliveryKeySourceStub)
	if !ok {
		t.Fatalf("delivery key source type=%T", harness.gateway.keys)
	}
	keys.material.State = backupasset.DomainKeyVerifyOnly

	response, err := harness.serve(strings.Repeat("f", 32), content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID,
		Method:     http.MethodGet,
		RawCookie:  harness.issued.Cookie.String(),
	})
	if err != nil {
		t.Fatalf("serve ready export with verify-only KEK: %v", err)
	}
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), harness.plaintext) {
		t.Fatalf("status=%d body_len=%d", response.Code, response.Body.Len())
	}
}

func TestDeliveryGatewayRecordsInvalidMultiAndOverflowRanges(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	tests := []struct {
		name   string
		ranges []string
	}{
		{name: "multipart", ranges: []string{"bytes=0-1,4-5"}},
		{name: "duplicate fields", ranges: []string{"bytes=0-1", "bytes=4-5"}},
		{name: "start overflow", ranges: []string{"bytes=9223372036854775808-"}},
		{name: "end overflow", ranges: []string{"bytes=0-9223372036854775808"}},
		{name: "suffix overflow", ranges: []string{"bytes=-9223372036854775808"}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := fmt.Sprintf("%032x", 200+index)
			response, err := harness.serve(requestID, content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
				RawCookie: harness.issued.Cookie.String(), RangeHeaders: test.ranges,
			})
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
			if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Body.Len() != 0 ||
				response.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", len(harness.plaintext)) {
				t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.Bytes())
			}
			var request model.BackupAssetExportDeliveryRequest
			if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
				t.Fatal(err)
			}
			if request.State != string(DeliveryRequestBlocked) ||
				request.FailureCode != string(content.RequestFailureInvalidRange) || request.FinishedAt == nil {
				t.Fatalf("request=%+v", request)
			}
		})
	}
}

func TestDeliveryGatewayIssueExportRejectsClosedStateMatrix(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*model.BackupAssetExportJob, *model.BackupAssetExportAttempt, *model.BackupAssetExportKey, *model.BackupAssetExportArtifact)
		foreign    bool
		secondCopy bool
	}{
		{name: "non-ready job", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, _ *model.BackupAssetExportArtifact) {
			job.ExecutionState = string(ExecutionRunning)
		}},
		{name: "expired job", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, artifact *model.BackupAssetExportArtifact) {
			expired := time.Now().UTC().Add(-time.Minute)
			job.ExpiresAt, artifact.ExpiresAt = &expired, &expired
		}},
		{name: "key lost", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, key *model.BackupAssetExportKey, _ *model.BackupAssetExportArtifact) {
			key.State = "destroyed"
		}},
		{name: "tampered metadata", mutate: func(_ *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, artifact *model.BackupAssetExportArtifact) {
			artifact.CiphertextDigest = "not-a-digest"
		}},
		{name: "unknown archive profile", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, _ *model.BackupAssetExportArtifact) {
			job.ArchiveProfile = "future_v2"
		}},
		{name: "zip crossed with tar none", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, _ *model.BackupAssetExportArtifact) {
			job.ArchiveProfile = "tar_none_v1"
		}},
		{name: "zip crossed with tar gzip", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, _ *model.BackupAssetExportArtifact) {
			job.ArchiveProfile = "tar_gzip_v1"
		}},
		{name: "tar crossed with zip", mutate: func(job *model.BackupAssetExportJob, _ *model.BackupAssetExportAttempt, _ *model.BackupAssetExportKey, _ *model.BackupAssetExportArtifact) {
			job.ArchiveFormat = string(ArchiveTAR)
		}},
		{name: "foreign owner", foreign: true},
		{name: "second sealed artifact", secondCopy: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			db := openDeliveryTestDB(t)
			job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
			if test.mutate != nil {
				test.mutate(&job, &attempt, &key, &artifact)
			}
			for _, row := range []any{&job, &attempt, &key, &artifact} {
				if err := db.Create(row).Error; err != nil {
					t.Fatalf("seed %T: %v", row, err)
				}
			}
			if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
				Update("is_current", false).Error; err != nil {
				t.Fatal(err)
			}
			if test.secondCopy {
				second := artifact
				second.ID = strings.Repeat("8", 32)
				if err := db.Create(&second).Error; err != nil {
					t.Fatal(err)
				}
			}
			store, err := OpenStore(StoreConfig{Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			material, err := content.NewTicketMaterial()
			if err != nil {
				t.Fatal(err)
			}
			gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
				DB: db, Now: func() time.Time { return now }, Session: &deliverySessionValidatorStub{}, Store: store,
				Audit: mustDeliveryAudit(t),
				Keys: &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
					Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive,
					Key: bytes.Repeat([]byte{1}, 32),
				}},
				TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
				Config: DeliveryGatewayConfig{
					TicketTTL: time.Minute, MaxRequests: 2, MaxCumulativeBytes: 1 << 20, MaxInFlight: 1,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			owner := job.OwnerUserID
			if test.foreign {
				owner++
			}
			_, err = gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
				Actor: content.DeliveryActor{UserID: owner, Role: "admin"},
				Session: content.DeliverySession{
					JTI: strings.Repeat("e", 32), UserID: owner, Role: "admin",
					TokenVersion: 1, ExpiresAt: now.Add(time.Hour),
				},
				ExportJobID: job.ID,
				Proof: content.StepUpProof{
					Action: auth.StepUpActionAssetExportDownload,
					ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(time.Hour),
				},
				SecureCookie: true,
			})
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("issue error=%v", err)
			}
			var grants int64
			if err := db.Model(&model.BackupAssetExportDeliveryGrant{}).Count(&grants).Error; err != nil {
				t.Fatal(err)
			}
			if grants != 0 {
				t.Fatalf("delivery grants=%d", grants)
			}
		})
	}
}

func TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *readyDeliveryGatewayHarness) error
	}{
		{name: "selection digest", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
				Update("selection_digest", strings.Repeat("9", 64)).Error
		}},
		{name: "archive format", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
				Update("archive_format", string(ArchiveTAR)).Error
		}},
		{name: "archive profile", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
				Update("archive_profile", "tar_none_v1").Error
		}},
		{name: "attempt fence", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", harness.attempt.ID).
				Update("fence_digest", strings.Repeat("9", 64)).Error
		}},
		{name: "artifact locator", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("locator", strings.Repeat("8", 32)+".xre").Error
		}},
		{name: "artifact nonce tuple", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			nonce := []byte("87654321")
			return harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", harness.attempt.ID).
					Update("nonce_prefix", nonce).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
					Update("nonce_prefix", nonce).Error
			})
		}},
		{name: "artifact chunk count", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("chunk_count", harness.artifact.ChunkCount+1).Error
		}},
		{name: "artifact format version", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("format_version", harness.artifact.FormatVersion+1).Error
		}},
		{name: "artifact plaintext digest", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("plaintext_digest", strings.Repeat("9", 64)).Error
		}},
		{name: "artifact archive digest", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("archive_digest", strings.Repeat("9", 64)).Error
		}},
		{name: "artifact ciphertext digest", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("ciphertext_digest", strings.Repeat("9", 64)).Error
		}},
		{name: "artifact plaintext size", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
				Update("plaintext_size", harness.artifact.PlaintextSize+1).Error
		}},
		{name: "artifact ciphertext size", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
					Update("artifact_bytes", harness.artifact.CiphertextSize+1).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).
					Update("ciphertext_size", harness.artifact.CiphertextSize+1).Error
			})
		}},
		{name: "key revision", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
				Update("key_revision", harness.key.KeyRevision+1).Error
		}},
		{name: "KEK version", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
				Update("kek_version", harness.key.KEKVersion+1).Error
		}},
		{name: "wrap algorithm", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			return harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
				Update("wrap_algorithm", "aes-256-gcm-v2").Error
		}},
		{name: "envelope nonce", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			nonce := append([]byte(nil), harness.key.EnvelopeNonce...)
			nonce[0] ^= 0x80
			return harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
				Update("envelope_nonce", nonce).Error
		}},
		{name: "wrapped DEK", mutate: func(_ *testing.T, harness *readyDeliveryGatewayHarness) error {
			wrapped := append([]byte(nil), harness.key.WrappedDEK...)
			wrapped[len(wrapped)-1] ^= 0x80
			return harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
				Update("wrapped_dek", wrapped).Error
		}},
		{name: "same revision valid rewrap", mutate: func(t *testing.T, harness *readyDeliveryGatewayHarness) error {
			envelope, err := WrapJobDEK(JobKeyBinding{
				ExportID: harness.job.ID, SelectionDigest: harness.job.SelectionDigest,
				KEKVersion: 3, WrapAlgorithm: JobKeyWrapAlgorithmV1,
			}, bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{7}, 32))
			if err != nil {
				t.Fatalf("valid same-revision rewrap: %v", err)
			}
			return harness.db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).
				Updates(map[string]any{
					"wrapped_dek": envelope.Ciphertext, "envelope_nonce": envelope.Nonce,
					"kek_version": 3, "wrap_algorithm": JobKeyWrapAlgorithmV1,
				}).Error
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
			if err := test.mutate(t, harness); err != nil {
				t.Fatalf("mutate binding: %v", err)
			}
			_, err := harness.gateway.loadExportDeliveryBinding(
				context.Background(), harness.material.DeliveryID, harness.material.CookieSecret,
			)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("binding after drift error=%v, want ErrNotFound", err)
			}
		})
	}
}

func TestDeliveryGatewayRejectsTicketBindingDriftBeforeServing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, *readyDeliveryGatewayHarness) error
	}{
		{name: "grant action", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).Where("id = ?", harness.material.GrantID).Update("action", "download").Error
		}},
		{name: "grant path", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).Where("id = ?", harness.material.GrantID).Update("canonical_path", "/api/v1/asset-content/"+strings.Repeat("0", 32)).Error
		}},
		{name: "foreign subject", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).Where("id = ?", harness.material.GrantID).Update("owner_user_id", harness.job.OwnerUserID+1).Error
		}},
		{name: "artifact metadata", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			return db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", harness.artifact.ID).Update("ciphertext_digest", strings.Repeat("0", 64)).Error
		}},
		{name: "second sealed artifact", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			second := harness.artifact
			second.ID = strings.Repeat("0", 32)
			return db.Create(&second).Error
		}},
		{name: "key revision", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			return db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).Update("key_revision", harness.key.KeyRevision+1).Error
		}},
		{name: "key destroyed", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			return db.Model(&model.BackupAssetExportKey{}).Where("id = ?", harness.key.ID).Update("state", "destroyed").Error
		}},
		{name: "union contamination", mutate: func(db *gorm.DB, harness *readyDeliveryGatewayHarness) error {
			memberID := strings.Repeat("7", 32)
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).Where("id = ?", harness.material.GrantID).
				Update("member_request_id", memberID).Error
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
			if err := test.mutate(harness.db, harness); err != nil {
				t.Fatal(err)
			}
			response, err := harness.serve(strings.Repeat("8", 32), content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
			})
			if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
				t.Fatalf("serve error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
			}
			var requests int64
			if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).Count(&requests).Error; err != nil {
				t.Fatal(err)
			}
			if requests != 0 {
				t.Fatalf("delivery requests=%d", requests)
			}
		})
	}
}

func TestDeliveryGatewayRejectsWrongCookieDeliveryAndMethodBindings(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	tests := []struct {
		name    string
		request content.GatewayRequest
	}{
		{name: "wrong cookie", request: content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: content.DeliveryCookieName + "=v1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		}},
		{name: "wrong delivery", request: content.GatewayRequest{
			DeliveryID: strings.Repeat("0", 32), Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
		}},
		{name: "wrong method", request: content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodPost, RawCookie: harness.issued.Cookie.String(),
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := harness.serve(fmt.Sprintf("%032x", 300+index), test.request)
			if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
				t.Fatalf("serve error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
			}
		})
	}
	var requests int64
	if err := harness.db.Model(&model.BackupAssetExportDeliveryRequest{}).Count(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("delivery requests=%d", requests)
	}
}

func TestDeliveryGatewayRejectsTamperedArchivePairBeforeHEADServe(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		format  string
		profile string
	}{
		{name: "unknown profile", format: "zip", profile: "future_v2"},
		{name: "zip crossed with tar none", format: "zip", profile: "tar_none_v1"},
		{name: "zip crossed with tar gzip", format: "zip", profile: "tar_gzip_v1"},
		{name: "tar crossed with zip", format: "tar", profile: "zip_deflate_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
				UpdateColumns(map[string]any{
					"archive_format": testCase.format, "archive_profile": testCase.profile,
				}).Error; err != nil {
				t.Fatal(err)
			}
			tamperedJob := harness.job
			tamperedJob.ArchiveFormat, tamperedJob.ArchiveProfile = testCase.format, testCase.profile
			tamperedDigest := exportDeliveryBindingDigest(tamperedJob, harness.attempt, harness.artifact, harness.key)
			if err := harness.db.Model(&model.BackupAssetExportDeliveryGrant{}).
				Where("delivery_id = ?", harness.material.DeliveryID).
				Update("artifact_digest", tamperedDigest).Error; err != nil {
				t.Fatal(err)
			}

			response, err := harness.serve(strings.Repeat("7", 32), content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodHead,
				RawCookie: harness.issued.Cookie.String(),
			})
			if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
				t.Fatalf("serve pair=(%q, %q) error=%v headers=%v body=%q",
					testCase.format, testCase.profile, err, response.Header(), response.Body.Bytes())
			}
		})
	}
}

func TestDeliveryGatewayEnforcesRequestByteAndInFlightBudgets(t *testing.T) {
	t.Run("request count", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{maxRequests: 1})
		if _, err := harness.serve(strings.Repeat("1", 32), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
		}); err != nil {
			t.Fatalf("first request: %v", err)
		}
		response, err := harness.serve(strings.Repeat("2", 32), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
		})
		if !errors.Is(err, content.ErrContentBudgetExceeded) || response.Body.Len() != 0 || len(response.Header()) != 0 {
			t.Fatalf("second request error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
		}
		assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 0)
	})

	t.Run("byte reservation", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{maxCumulativeMultiplier: 1})
		response, err := harness.serve(strings.Repeat("3", 32), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
		})
		if !errors.Is(err, content.ErrContentBudgetExceeded) || response.Body.Len() != 0 || len(response.Header()) != 0 {
			t.Fatalf("serve error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
		}
		assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 0, 0, 0, 0)
	})

	t.Run("cumulative bytes", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{maxCumulativeMultiplier: 2})
		if _, err := harness.serve(strings.Repeat("4", 32), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
		}); err != nil {
			t.Fatalf("first request: %v", err)
		}
		response, err := harness.serve(strings.Repeat("5", 32), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: harness.issued.Cookie.String(),
		})
		if !errors.Is(err, content.ErrContentBudgetExceeded) || response.Body.Len() != 0 || len(response.Header()) != 0 {
			t.Fatalf("second request error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
		}
		var grant model.BackupAssetExportDeliveryGrant
		if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if grant.RequestCount != 1 || grant.ConsumedBytes <= 0 || grant.ReservedBytes != 0 || grant.InFlight != 0 {
			t.Fatalf("grant=%+v", grant)
		}
	})

	t.Run("in flight", func(t *testing.T) {
		harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{maxInFlight: 1})
		firstID := strings.Repeat("6", 32)
		harness.requestIDs <- firstID
		blockingWriter := newBlockingDeliveryWriter()
		serveDone := make(chan error, 1)
		go func() {
			serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
				RawCookie: harness.issued.Cookie.String(), RangeHeaders: []string{"bytes=0-131071"},
			}, blockingWriter)
		}()
		select {
		case <-blockingWriter.started:
		case <-time.After(5 * time.Second):
			t.Fatal("first request did not begin streaming")
		}
		response, err := harness.serve(strings.Repeat("7", 32), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
		})
		if !errors.Is(err, content.ErrContentBudgetExceeded) || response.Body.Len() != 0 || len(response.Header()) != 0 {
			t.Fatalf("second request error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
		}
		close(blockingWriter.release)
		select {
		case err := <-serveDone:
			if err != nil {
				t.Fatalf("first request: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first request did not finish")
		}
		var grant model.BackupAssetExportDeliveryGrant
		if err := harness.db.Where("id = ?", harness.material.GrantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if grant.RequestCount != 1 || grant.ReservedBytes != 0 || grant.InFlight != 0 {
			t.Fatalf("grant=%+v", grant)
		}
	})
}

func TestDeliveryGatewayRejectsTerminalRequestIDReplay(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	requestID := strings.Repeat("8", 32)
	request := content.GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: harness.issued.Cookie.String(),
	}
	if _, err := harness.serve(requestID, request); err != nil {
		t.Fatalf("first request: %v", err)
	}
	response, err := harness.serve(requestID, request)
	if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
		t.Fatalf("replay error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
	}
	assertDeliveryGrantCounters(t, harness.db, harness.material.GrantID, 1, 0, 0, 0)
}

func TestDeliveryBudgetConcurrentFinalizeCASChargesOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openDeliveryTestDB(t)
	grant := deliveryGrantFixture(now)
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := newDeliveryBudget(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	requestID := strings.Repeat("2", 32)
	if _, err := budget.Reserve(context.Background(), DeliveryReservationIntent{
		RequestID: requestID, GrantID: grant.ID, Method: http.MethodGet,
		Range: content.HTTPRange{Kind: content.HTTPRangeFull, Length: 50}, ReservedBytes: 100,
	}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, finalizeErr := budget.Finalize(context.Background(), DeliveryFinalizeIntent{
				RequestID: requestID, State: DeliveryRequestSucceeded,
				EvidenceKnown: true, PlaintextBytes: 50, CiphertextBytes: 80,
			})
			results <- finalizeErr
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent finalize: %v", err)
		}
	}
	assertDeliveryGrantCounters(t, db, grant.ID, 1, 0, 80, 0)
}

func TestDeliveryGatewayMalformedAndTamperedCiphertextFailClosedAndChargeReservation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*os.File, int64) error
	}{
		{name: "malformed header", mutate: func(file *os.File, _ int64) error {
			_, err := file.WriteAt([]byte{0xff}, 0)
			return err
		}},
		{name: "tampered body", mutate: func(file *os.File, size int64) error {
			_, err := file.WriteAt([]byte{0xff}, size/2)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
			file, err := os.OpenFile(filepath.Join(harness.store.root, harness.artifact.Locator), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(file, harness.result.CiphertextBytes); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			requestID := strings.Repeat("8", 32)
			response, err := harness.serve(requestID, content.GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
				RawCookie: harness.issued.Cookie.String(),
			})
			if !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 || len(response.Header()) != 0 {
				t.Fatalf("serve error=%v headers=%v body=%q", err, response.Header(), response.Body.Bytes())
			}
			var request model.BackupAssetExportDeliveryRequest
			if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
				t.Fatal(err)
			}
			if request.State != string(DeliveryRequestFailed) || request.PlaintextBytes != 0 ||
				request.CiphertextBytes != 0 || request.FinishedAt == nil {
				t.Fatalf("request=%+v", request)
			}
			assertDeliveryGrantCounters(
				t, harness.db, harness.material.GrantID, 1, 0, harness.result.CiphertextBytes*2, 0,
			)
		})
	}
}

func TestDeliveryGatewayRevalidatesExactBindingBeforeEachChunk(t *testing.T) {
	harness := newReadyDeliveryGatewayHarness(t, readyDeliveryGatewayHarnessOptions{})
	requestID := strings.Repeat("8", 32)
	harness.requestIDs <- requestID
	blockingWriter := newBlockingDeliveryWriter()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- harness.gateway.Serve(context.Background(), content.GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: harness.issued.Cookie.String(), RangeHeaders: []string{"bytes=0-131071"},
		}, blockingWriter)
	}()
	select {
	case <-blockingWriter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not reach first authenticated chunk")
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", harness.job.ID).
		Update("execution_state", string(ExecutionCanceled)).Error; err != nil {
		t.Fatal(err)
	}
	close(blockingWriter.release)
	select {
	case err := <-serveDone:
		if !errors.Is(err, content.ErrContentNotFound) {
			t.Fatalf("serve after binding drift error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not stop after binding drift")
	}
	if blockingWriter.body.Len() != 64<<10 {
		t.Fatalf("stream wrote %d bytes after binding drift", blockingWriter.body.Len())
	}
	var request model.BackupAssetExportDeliveryRequest
	if err := harness.db.Where("id = ?", requestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(DeliveryRequestFailed) || request.FinishedAt == nil {
		t.Fatalf("request=%+v", request)
	}
}

type deliveryKeySourceStub struct {
	material                    backupasset.DomainKeyMaterial
	calls                       int
	err                         error
	returnMaterialForAnyRequest bool
}

func (source *deliveryKeySourceStub) ByVersion(
	_ context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	source.calls++
	if !source.returnMaterialForAnyRequest &&
		(domain != backupasset.KeyDomainExportStore || version != source.material.Version) {
		return backupasset.DomainKeyMaterial{}, ErrUnavailable
	}
	material := source.material
	material.Key = append([]byte(nil), material.Key...)
	return material, source.err
}

type zeroTrackingDeliveryKeySource struct {
	inner    DeliveryKeySource
	returned [][]byte
}

func (source *zeroTrackingDeliveryKeySource) ByVersion(
	ctx context.Context, domain backupasset.KeyDomain, version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.inner.ByVersion(ctx, domain, version)
	material.Key = append([]byte(nil), material.Key...)
	source.returned = append(source.returned, material.Key)
	return material, err
}

type deliverySessionValidatorStub struct {
	values []content.DeliverySession
	err    error
}

func (validator *deliverySessionValidatorStub) Validate(_ context.Context, session content.DeliverySession) error {
	validator.values = append(validator.values, session)
	return validator.err
}

type archiveMemberDeliverySourceStub struct {
	binding         content.ResolvedArchiveMemberArtifact
	payload         []byte
	resolveRequests []content.ArchiveMemberArtifactRequest
	readBindings    []content.ResolvedArchiveMemberArtifact
	beforeWrite     func()
	err             error
}

type archiveMemberAssetAuthorizerStub struct {
	asset   content.AuthorizedAsset
	actions []content.DeliveryAction
	err     error
}

func (authorizer *archiveMemberAssetAuthorizerStub) Authorize(
	_ context.Context,
	_ content.DeliveryActor,
	_ backupasset.AssetRef,
	action content.DeliveryAction,
) (content.AuthorizedAsset, error) {
	authorizer.actions = append(authorizer.actions, action)
	return authorizer.asset, authorizer.err
}

func (authorizer *archiveMemberAssetAuthorizerStub) Reauthorize(
	_ context.Context,
	_ content.DeliveryActor,
	_ content.AuthorizedAsset,
	_ content.DeliveryAction,
) error {
	return authorizer.err
}

func (source *archiveMemberDeliverySourceStub) ResolveArchiveMember(
	_ context.Context,
	request content.ArchiveMemberArtifactRequest,
) (content.ResolvedArchiveMemberArtifact, error) {
	source.resolveRequests = append(source.resolveRequests, request)
	return source.binding, source.err
}

func (source *archiveMemberDeliverySourceStub) ReadArchiveMember(
	ctx context.Context,
	binding content.ResolvedArchiveMemberArtifact,
	destination io.Writer,
) error {
	source.readBindings = append(source.readBindings, binding)
	if source.err != nil {
		return source.err
	}
	if source.beforeWrite != nil {
		source.beforeWrite()
	}
	_, err := destination.Write(source.payload)
	if err != nil {
		return err
	}
	return ctx.Err()
}

type deliveryAuditorRecorder struct {
	events []DeliveryAuditEvent
	err    error
}

func (recorder *deliveryAuditorRecorder) Write(_ context.Context, event DeliveryAuditEvent) error {
	recorder.events = append(recorder.events, event)
	return recorder.err
}

func mustDeliveryAudit(t *testing.T) *DeliveryAudit {
	t.Helper()
	audit, err := NewDeliveryAudit(&exportAuditSinkSpy{})
	if err != nil {
		t.Fatal(err)
	}
	return audit
}

type readyDeliveryGatewayHarnessOptions struct {
	maxRequests             int64
	maxCumulativeMultiplier int64
	maxInFlight             int64
	attemptFenceDigest      string
	deferIssue              bool
	audit                   DeliveryAuditor
	now                     func() time.Time
}

type archiveMemberDeliveryGatewayHarness struct {
	db         *gorm.DB
	gateway    *DeliveryGateway
	store      *Store
	asset      content.AuthorizedAsset
	binding    content.ResolvedArchiveMemberArtifact
	source     *archiveMemberDeliverySourceStub
	authorizer *archiveMemberAssetAuthorizerStub
	keys       *deliveryKeySourceStub
	material   content.TicketMaterial
	issued     IssuedDeliveryTicket
	requestIDs chan string
	audit      *deliveryAuditorRecorder
	sessions   *deliverySessionValidatorStub
	clock      *time.Time
}

func newArchiveMemberDeliveryGatewayHarness(t *testing.T) *archiveMemberDeliveryGatewayHarness {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	clock := now
	db := openDeliveryTestDB(t)
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	asset := content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		CatalogGenerationID: strings.Repeat("3", 32), RepositoryID: strings.Repeat("4", 32),
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 9,
		SourceFingerprint: strings.Repeat("5", 64), EntryFingerprint: strings.Repeat("6", 64),
		FingerprintStrength: "strong", Size: 1024, MediaType: "application/zip",
	}
	binding := content.ResolvedArchiveMemberArtifact{
		MemberRequestID: strings.Repeat("7", 32), OwnerUserID: 42, Ref: asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, MemberChainDigest: strings.Repeat("8", 64),
		ProcessingJobID: strings.Repeat("9", 32), ProcessingAttemptID: strings.Repeat("a", 32),
		DerivedArtifactSetID: strings.Repeat("b", 32), DerivedArtifactID: strings.Repeat("c", 32),
		DerivedBlobID: strings.Repeat("d", 32), DerivedDigest: strings.Repeat("e", 64),
		DerivedSize: 14, MediaType: "text/plain", AbsoluteExpiresAt: now.Add(4 * time.Minute),
		Provider: asset.Provider, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		FingerprintStrength: asset.FingerprintStrength, SourceSize: asset.Size,
		SourceMediaType: asset.MediaType, SecurityPolicyRevision: "security-policy-v1",
	}
	source := &archiveMemberDeliverySourceStub{binding: binding, payload: []byte("member payload")}
	authorizer := &archiveMemberAssetAuthorizerStub{asset: asset}
	keys := &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
		Domain: backupasset.KeyDomainExportStore, Version: 1, State: backupasset.DomainKeyActive,
		Key: bytes.Repeat([]byte{1}, 32),
	}}
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	requestIDs := make(chan string, 8)
	audit := &deliveryAuditorRecorder{}
	sessions := &deliverySessionValidatorStub{}
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time { return clock }, Session: sessions,
		Store: store, Keys: keys, ArchiveMembers: source, ArchiveMemberAuthorize: authorizer,
		Audit: audit, TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		RequestID: func() (string, error) {
			select {
			case value := <-requestIDs:
				return value, nil
			default:
				return "", errors.New("missing member delivery request ID")
			}
		},
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4, MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := gateway.IssueArchiveMember(context.Background(), ArchiveMemberDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: 42, Username: "admin", Role: "admin"},
		Session: content.DeliverySession{
			JTI: strings.Repeat("0", 32), UserID: 42, Role: "admin", TokenVersion: 3,
			ExpiresAt: now.Add(30 * time.Minute),
		},
		Asset: asset, MemberRequestID: binding.MemberRequestID,
		Proof: content.StepUpProof{
			Action: auth.StepUpActionAssetDownload, ID: strings.Repeat("f", 32), ExpiresAt: now.Add(20 * time.Minute),
		},
		SecureCookie: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &archiveMemberDeliveryGatewayHarness{
		db: db, gateway: gateway, store: store, asset: asset, binding: binding, source: source,
		authorizer: authorizer, keys: keys, material: material, issued: issued, requestIDs: requestIDs,
		audit: audit, sessions: sessions, clock: &clock,
	}
}

type readyDeliveryGatewayHarness struct {
	db         *gorm.DB
	store      *Store
	gateway    *DeliveryGateway
	job        model.BackupAssetExportJob
	attempt    model.BackupAssetExportAttempt
	key        model.BackupAssetExportKey
	artifact   model.BackupAssetExportArtifact
	plaintext  []byte
	result     CipherResult
	material   content.TicketMaterial
	issued     IssuedDeliveryTicket
	requestIDs chan string
	keys       *deliveryKeySourceStub
}

func newReadyDeliveryGatewayHarness(
	t *testing.T,
	options readyDeliveryGatewayHarnessOptions,
) *readyDeliveryGatewayHarness {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if options.now != nil {
		now = options.now().UTC()
	}
	db := openDeliveryTestDB(t)
	store, err := OpenStore(StoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, attempt, key, artifact := readyExportDeliveryFixture(t, now)
	if options.attemptFenceDigest != "" {
		attempt.FenceDigest = options.attemptFenceDigest
	}
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 12_000)
	dek := bytes.Repeat([]byte{7}, 32)
	kek := bytes.Repeat([]byte{9}, 32)
	envelope, err := WrapJobDEK(JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: 2,
		WrapAlgorithm: JobKeyWrapAlgorithmV1,
	}, kek, dek)
	if err != nil {
		t.Fatal(err)
	}
	key.WrappedDEK, key.EnvelopeNonce, key.KEKVersion = envelope.Ciphertext, envelope.Nonce, 2
	staging, err := store.CreateStaging(job.ID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := CipherBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, ArchiveProfile: job.ArchiveProfile,
		FormatVersion: 1, AttemptFenceDigest: attempt.FenceDigest, Purpose: CipherPurposeFinalArchive,
	}
	result, err := EncryptStream(context.Background(), staging.File, bytes.NewReader(plaintext), dek, binding, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	attempt.NoncePrefix = append([]byte(nil), result.NoncePrefix...)
	artifact.Locator, artifact.NoncePrefix = locator, append([]byte(nil), result.NoncePrefix...)
	artifact.ChunkBytes, artifact.ChunkCount = 64<<10, result.ChunkCount
	artifact.PlaintextDigest, artifact.ArchiveDigest = result.PlaintextDigest, result.ArchiveDigest
	artifact.CiphertextDigest = result.CiphertextDigest
	artifact.PlaintextSize, artifact.CiphertextSize = result.PlaintextBytes, result.CiphertextBytes
	job.ArtifactBytes = result.CiphertextBytes
	for _, row := range []any{&job, &attempt, &key, &artifact} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	material, err := content.NewTicketMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if options.maxRequests == 0 {
		options.maxRequests = 32
	}
	if options.maxCumulativeMultiplier == 0 {
		options.maxCumulativeMultiplier = 64
	}
	if options.maxInFlight == 0 {
		options.maxInFlight = 2
	}
	requestIDs := make(chan string, int(options.maxRequests)+4)
	if options.audit == nil {
		options.audit = mustDeliveryAudit(t)
	}
	keys := &deliveryKeySourceStub{material: backupasset.DomainKeyMaterial{
		Domain: backupasset.KeyDomainExportStore, Version: 2, State: backupasset.DomainKeyActive, Key: kek,
	}}
	gateway, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: db, Now: func() time.Time {
			if options.now != nil {
				return options.now().UTC()
			}
			return now
		}, Session: &deliverySessionValidatorStub{},
		Store: store, Audit: options.audit, Keys: keys,
		RequestID: func() (string, error) {
			select {
			case requestID := <-requestIDs:
				return requestID, nil
			default:
				return "", errors.New("missing test request ID")
			}
		},
		TicketMaterial: func() (content.TicketMaterial, error) { return material, nil },
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: options.maxRequests,
			MaxCumulativeBytes: options.maxCumulativeMultiplier * result.CiphertextBytes,
			MaxInFlight:        options.maxInFlight,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var issued IssuedDeliveryTicket
	if !options.deferIssue {
		issued, err = gateway.IssueExport(context.Background(), ExportDeliveryIssueRequest{
			Actor: content.DeliveryActor{UserID: job.OwnerUserID, Role: "admin"},
			Session: content.DeliverySession{
				JTI: strings.Repeat("e", 32), UserID: job.OwnerUserID, Role: "admin",
				TokenVersion: 3, ExpiresAt: now.Add(30 * time.Minute),
			},
			ExportJobID: job.ID,
			Proof: content.StepUpProof{
				Action: auth.StepUpActionAssetExportDownload,
				ID:     strings.Repeat("9", 32), ExpiresAt: now.Add(20 * time.Minute),
			},
			SecureCookie: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return &readyDeliveryGatewayHarness{
		db: db, store: store, gateway: gateway, job: job, attempt: attempt, key: key, artifact: artifact,
		plaintext: plaintext, result: result, material: material, issued: issued, requestIDs: requestIDs, keys: keys,
	}
}

func (harness *readyDeliveryGatewayHarness) serve(
	requestID string,
	request content.GatewayRequest,
) (*httptest.ResponseRecorder, error) {
	harness.requestIDs <- requestID
	response := httptest.NewRecorder()
	return response, harness.gateway.Serve(context.Background(), request, response)
}

func openDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "delivery.db") +
		"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			t.Errorf("close delivery test database: %v", closeErr)
		}
	})
	if err := db.AutoMigrate(
		&model.BackupAssetDeliveryGrant{},
		&model.BackupAssetExportJob{}, &model.BackupAssetExportAttempt{}, &model.BackupAssetExportKey{},
		&model.BackupAssetExportArtifact{}, &model.BackupAssetExportDeliveryGrant{},
		&model.BackupAssetExportDeliveryRequest{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func readyExportDeliveryFixture(t *testing.T, now time.Time) (
	model.BackupAssetExportJob,
	model.BackupAssetExportAttempt,
	model.BackupAssetExportKey,
	model.BackupAssetExportArtifact,
) {
	t.Helper()
	const chunkBytes int64 = 64 << 10
	const plaintextSize int64 = 500
	chunkCount, err := cipherChunkCountV1(plaintextSize, chunkBytes)
	if err != nil {
		t.Fatalf("compute delivery fixture chunk count: %v", err)
	}
	ciphertextSize, err := ciphertextSizeV1(plaintextSize, chunkBytes)
	if err != nil {
		t.Fatalf("compute delivery fixture ciphertext size: %v", err)
	}
	jobID := strings.Repeat("a", 32)
	attemptID := strings.Repeat("c", 32)
	keyID := strings.Repeat("d", 32)
	artifactID := strings.Repeat("b", 32)
	expiresAt := now.Add(time.Hour)
	sealedAt := now.Add(-time.Minute)
	readyAt := now.Add(-30 * time.Second)
	job := model.BackupAssetExportJob{
		ID: jobID, OwnerUserID: 7, SelectionDigest: strings.Repeat("2", 64),
		SelectionSchemaVersion: 1, ArchiveFormat: "zip", ArchiveProfile: "zip_deflate_v1",
		ChunkBytes:     chunkBytes,
		ExecutionState: string(ExecutionReady), ResultKind: string(ResultPartial), CleanupState: string(CleanupNone),
		CurrentAttemptID: &attemptID, CurrentFenceRevision: 1, AbsoluteDeadline: now.Add(2 * time.Hour),
		ReadyAt: &readyAt, ExpiresAt: &expiresAt, ArtifactBytes: ciphertextSize, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	fenceToken := []byte("01234567890123456789012345678901")
	fenceDigest := sha256.Sum256(fenceToken)
	attempt := model.BackupAssetExportAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerOwner: "worker-1",
		State: string(AttemptSealed), FenceToken: fenceToken,
		FenceDigest: hex.EncodeToString(fenceDigest[:]), NoncePrefix: []byte("12345678"),
		LeaseExpiresAt: now.Add(time.Hour), IsCurrent: false, StartedAt: now.Add(-time.Hour),
		FinishedAt: &sealedAt, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	envelope, err := WrapJobDEK(JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: 2,
		WrapAlgorithm: JobKeyWrapAlgorithmV1,
	}, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("wrap delivery fixture DEK: %v", err)
	}
	key := model.BackupAssetExportKey{
		ID: keyID, JobID: jobID, State: "active", WrappedDEK: envelope.Ciphertext,
		EnvelopeNonce: envelope.Nonce, KEKVersion: 2, WrapAlgorithm: JobKeyWrapAlgorithmV1,
		KeyRevision: 7, CreatedAt: now.Add(-time.Hour),
	}
	artifact := model.BackupAssetExportArtifact{
		ID: artifactID, JobID: jobID, AttemptID: attemptID, JobKeyID: keyID, State: "sealed",
		Locator: strings.Repeat("7", 32) + ".xre", CipherVersion: 1, ChunkBytes: chunkBytes,
		FormatVersion: 1, NoncePrefix: []byte("12345678"), ChunkCount: chunkCount,
		PlaintextDigest: strings.Repeat("4", 64), ArchiveDigest: strings.Repeat("5", 64),
		CiphertextDigest: strings.Repeat("6", 64), PlaintextSize: plaintextSize, CiphertextSize: ciphertextSize,
		SealedAt: &sealedAt, ExpiresAt: &expiresAt, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	return job, attempt, key, artifact
}

func deliveryGrantFixture(now time.Time) model.BackupAssetExportDeliveryGrant {
	jobID := strings.Repeat("a", 32)
	artifactID := strings.Repeat("b", 32)
	attemptID := strings.Repeat("c", 32)
	keyID := strings.Repeat("d", 32)
	return model.BackupAssetExportDeliveryGrant{
		ID: strings.Repeat("1", 32), DeliveryID: strings.Repeat("f", 32), ResourceKind: "export_archive",
		ExportJobID: &jobID, ExportArtifactID: &artifactID, ExportAttemptID: &attemptID,
		ExportFenceDigest: strings.Repeat("1", 64), SelectionDigest: strings.Repeat("2", 64),
		ArtifactDigest: strings.Repeat("3", 64), PlaintextSize: 500, CiphertextSize: 700,
		FormatVersion: 1, ChunkBytes: 64 << 10, JobKeyID: &keyID, JobKeyVersion: 1,
		OwnerUserID: 7, SessionJTI: strings.Repeat("e", 32), TokenVersion: 3, RoleRevision: 3,
		ProofAction: "asset.export_download", ProofID: strings.Repeat("9", 32), ProofExpiresAt: now.Add(time.Hour),
		CookieSecretHash: strings.Repeat("4", 64), Action: "export_download",
		CanonicalPath: "/api/v1/asset-content/" + strings.Repeat("f", 32), MethodPolicy: "get_head",
		RangePolicy: "single", State: "active", IdleExpiresAt: now.Add(5 * time.Minute),
		AbsoluteExpiresAt: now.Add(10 * time.Minute), MaxRequests: 4, MaxCumulativeBytes: 1000,
		MaxInFlight: 2, IssuedAt: now, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func assertDeliveryGrantCounters(
	t *testing.T,
	db *gorm.DB,
	grantID string,
	requests int64,
	reserved int64,
	consumed int64,
	inFlight int64,
) {
	t.Helper()
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", grantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.RequestCount != requests || grant.ReservedBytes != reserved ||
		grant.ConsumedBytes != consumed || grant.InFlight != inFlight {
		t.Fatalf("grant counters requests=%d reserved=%d consumed=%d in_flight=%d", grant.RequestCount, grant.ReservedBytes, grant.ConsumedBytes, grant.InFlight)
	}
}
