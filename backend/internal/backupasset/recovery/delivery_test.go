package recovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
)

type recoveryResultReadTargetFake struct {
	closedTargetPortFake
	now         time.Time
	requests    []OpenOwnedResultRequest
	permitValid bool
	afterOpen   func()
	openErr     error
	readers     []*recoveryResultReadCloser
}

type recoveryResultReadCloser struct {
	*bytes.Reader
	closed bool
}

func (reader *recoveryResultReadCloser) Close() error {
	reader.closed = true
	return nil
}

func (fake *recoveryResultReadTargetFake) OpenOwnedResult(
	_ context.Context,
	permit TargetResultReadPermit,
	request OpenOwnedResultRequest,
) (io.ReadCloser, error) {
	fake.requests = append(fake.requests, request)
	fake.permitValid = permit.ValidateRequestAt(fake.now, request) == nil
	reader := &recoveryResultReadCloser{Reader: bytes.NewReader([]byte(recoveryExecutionPayload(request.ExpectedBytes)))}
	fake.readers = append(fake.readers, reader)
	if fake.afterOpen != nil {
		fake.afterOpen()
	}
	return reader, fake.openErr
}

var _ TargetPort = (*recoveryResultReadTargetFake)(nil)

func TestRecoveryResultResolverReturnsOnlyCurrentOwnedPublishedRegularResult(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish resolver fixture: %v", err)
	}
	resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("construct recovery result resolver: %v", err)
	}

	for _, result := range published.Results {
		resolved, err := resolver.Resolve(context.Background(), ResolveRecoveryResultRequest{
			RequesterID: fixture.requesterID, RecoveryJobID: fixture.job.ID, ResultID: result.ID,
		})
		if err != nil {
			t.Fatalf("resolve published regular result %s: %v", result.ID, err)
		}
		if resolved.RecoveryJobID != fixture.job.ID || resolved.ResultID != result.ID ||
			resolved.OwnerUserID != fixture.requesterID || resolved.PublicationRevision != published.JobRevision ||
			resolved.CleanupFence != 0 || resolved.ResultSetState != ResultSetStateReady ||
			resolved.ResultKind != RecoveryResultKindRegularFile ||
			resolved.TargetObject.PrivateRelativeLocator == "" ||
			!strings.HasPrefix(resolved.TargetObject.PrivateRelativeLocator, fixture.job.EncryptedWorkspaceRelativeLocator+"/") ||
			resolved.Size != result.Size || resolved.ContentDigest != result.ContentDigest {
			t.Fatalf("unexpected resolved recovery result: %+v", resolved)
		}
		if err := resolver.Revalidate(context.Background(), resolved); err != nil {
			t.Fatalf("revalidate current recovery result: %v", err)
		}
	}
}

func TestRecoveryResultRetainKeepsDeliveryAvailableBeyondInitialDeadline(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish retained resolver fixture: %v", err)
	}
	firstDeadline := published.PlaintextDeadline.Add(48 * time.Hour)
	retained, err := fixture.service.Retain(
		context.Background(), fixture.retainRequest(published, firstDeadline),
	)
	if err != nil {
		t.Fatalf("retain recovery result before initial deadline: %v", err)
	}

	resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("construct retained result resolver: %v", err)
	}
	request := ResolveRecoveryResultRequest{
		RequesterID: fixture.requesterID, RecoveryJobID: published.JobID, ResultID: published.Results[0].ID,
	}
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve retained recovery result: %v", err)
	}
	if !resolved.PlaintextDeadline.Equal(retained.PlaintextDeadline) {
		t.Fatalf("resolved retained deadline = %s, want %s", resolved.PlaintextDeadline, retained.PlaintextDeadline)
	}

	fixture.now = published.PlaintextDeadline.Add(time.Hour)
	secondDeadline := firstDeadline.Add(time.Hour)
	retained, err = fixture.service.Retain(
		context.Background(), fixture.retainRequest(published, secondDeadline),
	)
	if err != nil {
		t.Fatalf("retain recovery result after initial deadline: %v", err)
	}
	resolved, err = resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve recovery result after second retain: %v", err)
	}
	if !resolved.PlaintextDeadline.Equal(secondDeadline) ||
		!retained.PlaintextDeadline.Equal(secondDeadline) || !retained.HardDeadline.Equal(published.HardDeadline) {
		t.Fatalf("unexpected second retained product: retained=%+v resolved=%+v", retained, resolved)
	}
}

func TestRecoveryResultResolverCollapsesUnsafeBindings(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *recoveryResultLifecycleFixture, PublishedRecoveryResultSet)
		request func(*recoveryResultLifecycleFixture, PublishedRecoveryResultSet) ResolveRecoveryResultRequest
	}{
		{name: "wrong owner", request: func(fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet) ResolveRecoveryResultRequest {
			return ResolveRecoveryResultRequest{RequesterID: fixture.requesterID + 1, RecoveryJobID: fixture.job.ID, ResultID: published.Results[0].ID}
		}},
		{name: "wrong job", request: func(fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet) ResolveRecoveryResultRequest {
			return ResolveRecoveryResultRequest{RequesterID: fixture.requesterID, RecoveryJobID: strings.Repeat("f", 32), ResultID: published.Results[0].ID}
		}},
		{name: "path like result id", request: func(fixture *recoveryResultLifecycleFixture, _ PublishedRecoveryResultSet) ResolveRecoveryResultRequest {
			return ResolveRecoveryResultRequest{RequesterID: fixture.requesterID, RecoveryJobID: fixture.job.ID, ResultID: "../published-result"}
		}},
		{name: "revoking result set", mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet) {
			leaseExpiry := fixture.now.Add(time.Minute)
			nodeLeaseID := strings.Repeat("e", 32)
			if err := fixture.db.Create(&model.BackupAssetRecoveryNodeLease{
				ID: nodeLeaseID, NodeID: fixture.job.TargetNodeID, HolderKind: "recovery_cleanup", JobID: fixture.job.ID,
				OwnerID: "cleanup-owner", Fence: 1, State: "active", LeaseExpiresAt: leaseExpiry,
				CreatedAt: fixture.now, UpdatedAt: fixture.now,
			}).Error; err != nil {
				t.Fatalf("create resolver cleanup lease: %v", err)
			}
			if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
				Updates(map[string]any{
					"state": string(ResultSetStateRevoking), "cleanup_owner": "cleanup-owner",
					"cleanup_lease_expires_at": leaseExpiry, "cleanup_fence": 1,
					"node_lease_id": nodeLeaseID, "node_fence": 1, "cleanup_attempt": 1,
				}).Error; err != nil {
				t.Fatalf("revoke resolver result set: %v", err)
			}
		}},
		{name: "in place job", mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, _ PublishedRecoveryResultSet) {
			fixture.updateJob(t, map[string]any{"target_mode": string(TargetModeInPlace)})
		}},
		{name: "unpublished workspace", mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, _ PublishedRecoveryResultSet) {
			fixture.updateJob(t, map[string]any{"workspace_phase": string(WorkspacePhaseSealed)})
		}},
		{name: "marker mismatch", mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
				Update("marker_binding_digest", strings.Repeat("d", 64)).Error; err != nil {
				t.Fatalf("mutate resolver marker binding: %v", err)
			}
		}},
		{name: "directory row", mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryResult{}).Where("id = ?", published.Results[0].ID).
				Update("result_kind", "directory").Error; err != nil {
				t.Fatalf("mutate resolver result kind: %v", err)
			}
		}},
		{name: "active attempt", mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, _ PublishedRecoveryResultSet) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("job_id = ?", fixture.job.ID).
				Updates(map[string]any{"state": string(AttemptStateRunning), "closed_at": nil}).Error; err != nil {
				t.Fatalf("restore resolver active attempt: %v", err)
			}
		}},
		{name: "expired plaintext", mutate: func(_ *testing.T, fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet) {
			fixture.now = published.PlaintextDeadline.Add(time.Second)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
			if err != nil {
				t.Fatalf("publish resolver matrix fixture: %v", err)
			}
			resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
				DB: fixture.db, Now: func() time.Time { return fixture.now },
			})
			if err != nil {
				t.Fatalf("construct resolver matrix service: %v", err)
			}
			if test.mutate != nil {
				test.mutate(t, fixture, published)
			}
			request := ResolveRecoveryResultRequest{
				RequesterID: fixture.requesterID, RecoveryJobID: fixture.job.ID, ResultID: published.Results[0].ID,
			}
			if test.request != nil {
				request = test.request(fixture, published)
			}
			if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrRecoveryResultUnavailable) {
				t.Fatalf("unsafe resolver error = %v, want %v", err, ErrRecoveryResultUnavailable)
			}
		})
	}
}

func TestRecoveryResultDeliveryAdapterAuthorizesAndOpensPurposeExactSource(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish delivery adapter fixture: %v", err)
	}
	resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("construct delivery adapter resolver: %v", err)
	}
	target := &recoveryResultReadTargetFake{now: fixture.now}
	adapter, err := NewRecoveryResultDeliveryAdapter(RecoveryResultDeliveryAdapterDependencies{
		Resolver: resolver, Target: target, Now: func() time.Time { return fixture.now },
		ReadPermitTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("construct recovery result delivery adapter: %v", err)
	}
	var _ content.RecoveryResultAuthorizer = adapter
	var _ content.RecoveryResultSourceResolver = adapter

	result := published.Results[0]
	ref := content.RecoveryResultRef{RecoveryJobID: fixture.job.ID, ResultID: result.ID}
	actor := content.DeliveryActor{UserID: fixture.requesterID, Username: "admin", Role: "admin"}
	authorized, err := adapter.AuthorizeRecoveryResult(context.Background(), actor, ref, content.DeliveryDownload)
	if err != nil {
		t.Fatalf("authorize recovery result delivery: %v", err)
	}
	if authorized.Ref != ref || authorized.OwnerUserID != fixture.requesterID ||
		authorized.RepositoryID == "" || authorized.RecoveryPointID == "" ||
		authorized.Provider != backupasset.ProviderRestic || authorized.PublicationRevision != published.JobRevision ||
		authorized.CleanupFence != 0 || !validDigest(authorized.MarkerBindingDigest) ||
		!validDigest(authorized.PublicationFingerprint) || authorized.ContentDigest != result.ContentDigest ||
		authorized.Size != result.Size || authorized.MediaType != "application/octet-stream" ||
		authorized.RangeProven || authorized.Classification != content.ClassificationUnknown ||
		authorized.ClassificationRevision != result.Classification.Revision ||
		authorized.ClassificationSourceRevision != result.Classification.SourceRevision ||
		!authorized.PlaintextDeadline.Equal(published.PlaintextDeadline) ||
		!authorized.HardDeadline.Equal(published.HardDeadline) {
		t.Fatalf("authorized recovery result=%+v", authorized)
	}
	if err := adapter.ReauthorizeRecoveryResult(
		context.Background(), actor, authorized, content.DeliveryDownload,
	); err != nil {
		t.Fatalf("reauthorize recovery result delivery: %v", err)
	}
	statSource, err := adapter.OpenRecoveryResultSource(context.Background(), content.RecoveryResultSourceRequest{
		OwnerUserID: fixture.requesterID, Ref: ref, ExpectedPublication: authorized.PublicationFingerprint,
		ExpectedContent: authorized.ContentDigest, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatalf("open recovery result stat source: %v", err)
	}
	if statSource.Reader() != nil || len(target.requests) != 0 {
		t.Fatalf("stat-only recovery source reader=%v target_requests=%+v", statSource.Reader(), target.requests)
	}
	if err := statSource.Close(); err != nil {
		t.Fatalf("close recovery result stat source: %v", err)
	}
	if rangeSource, err := adapter.OpenRecoveryResultSource(context.Background(), content.RecoveryResultSourceRequest{
		OwnerUserID: fixture.requesterID, Ref: ref, ExpectedPublication: authorized.PublicationFingerprint,
		ExpectedContent: authorized.ContentDigest, Mode: content.SourceModeRange, MaxBytes: 1,
		Range: &content.ResolvedRange{Offset: 0, Length: 1},
	}); err != ErrRecoveryResultUnavailable || rangeSource != nil || len(target.requests) != 0 {
		t.Fatalf("range recovery source=%v error=%v target_requests=%+v", rangeSource, err, target.requests)
	}

	source, err := adapter.OpenRecoveryResultSource(context.Background(), content.RecoveryResultSourceRequest{
		OwnerUserID: fixture.requesterID, Ref: ref, ExpectedPublication: authorized.PublicationFingerprint,
		ExpectedContent: authorized.ContentDigest, Mode: content.SourceModeSequential, MaxBytes: authorized.Size,
	})
	if err != nil {
		t.Fatalf("open recovery result source: %v", err)
	}
	if len(target.requests) != 1 || !target.permitValid || target.requests[0].Object.PrivateRelativeLocator == "" ||
		target.requests[0].ExpectedBytes != authorized.Size ||
		target.requests[0].IdentityDigest != authorized.ContentDigest {
		t.Fatalf("recovery result target requests=%+v permit_valid=%t", target.requests, target.permitValid)
	}
	if stat, capabilities := source.Stat(), source.Capabilities(); stat.Size != authorized.Size || stat.SourceFingerprint != authorized.PublicationFingerprint ||
		stat.EntryFingerprint != authorized.ContentDigest || !stat.FingerprintStrong ||
		capabilities.Provider != authorized.Provider || !capabilities.Sequential || capabilities.Range {
		t.Fatalf("recovery result source stat=%+v capabilities=%+v", stat, capabilities)
	}
	payload, err := io.ReadAll(source.Reader())
	if err != nil || string(payload) != recoveryExecutionPayload(authorized.Size) {
		t.Fatalf("read recovery result source payload=%q err=%v", payload, err)
	}
	if source.Reader().ProviderBytes() != authorized.Size {
		t.Fatalf("recovery result provider bytes=%d want=%d", source.Reader().ProviderBytes(), authorized.Size)
	}
	if err := source.Revalidate(context.Background()); err != nil {
		t.Fatalf("revalidate open recovery result source: %v", err)
	}
	if err := source.Close(); err != nil || len(target.readers) != 1 || !target.readers[0].closed {
		t.Fatalf("close recovery result source err=%v readers=%+v", err, target.readers)
	}
}

func TestRecoveryResultDeliveryAdapterClosesSourceAfterPostOpenPublicationDrift(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish post-open drift fixture: %v", err)
	}
	resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("construct post-open drift resolver: %v", err)
	}
	target := &recoveryResultReadTargetFake{now: fixture.now}
	adapter, err := NewRecoveryResultDeliveryAdapter(RecoveryResultDeliveryAdapterDependencies{
		Resolver: resolver, Target: target, Now: func() time.Time { return fixture.now },
		ReadPermitTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("construct post-open drift adapter: %v", err)
	}
	result := published.Results[0]
	ref := content.RecoveryResultRef{RecoveryJobID: fixture.job.ID, ResultID: result.ID}
	actor := content.DeliveryActor{UserID: fixture.requesterID, Username: "admin", Role: "admin"}
	authorized, err := adapter.AuthorizeRecoveryResult(context.Background(), actor, ref, content.DeliveryDownload)
	if err != nil {
		t.Fatalf("authorize post-open drift result: %v", err)
	}
	target.afterOpen = func() {
		fixture.updateJob(t, map[string]any{"transition_revision": published.JobRevision + 1})
	}

	source, err := adapter.OpenRecoveryResultSource(context.Background(), content.RecoveryResultSourceRequest{
		OwnerUserID: fixture.requesterID, Ref: ref, ExpectedPublication: authorized.PublicationFingerprint,
		ExpectedContent: authorized.ContentDigest, Mode: content.SourceModeSequential, MaxBytes: authorized.Size,
	})
	if !errors.Is(err, ErrRecoveryResultUnavailable) || source != nil {
		t.Fatalf("post-open publication drift source=%v err=%v", source, err)
	}
	if len(target.readers) != 1 || !target.readers[0].closed {
		t.Fatalf("post-open publication drift readers=%+v", target.readers)
	}
}

func TestRecoveryResultDeliveryAdapterClosesReaderReturnedWithOpenError(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish open-error fixture: %v", err)
	}
	resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("construct open-error resolver: %v", err)
	}
	target := &recoveryResultReadTargetFake{now: fixture.now, openErr: errors.New("FAKE_TARGET_OPEN_ERROR_FOR_TEST_ONLY")}
	adapter, err := NewRecoveryResultDeliveryAdapter(RecoveryResultDeliveryAdapterDependencies{
		Resolver: resolver, Target: target, Now: func() time.Time { return fixture.now },
		ReadPermitTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("construct open-error adapter: %v", err)
	}
	result := published.Results[0]
	ref := content.RecoveryResultRef{RecoveryJobID: fixture.job.ID, ResultID: result.ID}
	actor := content.DeliveryActor{UserID: fixture.requesterID, Username: "admin", Role: "admin"}
	authorized, err := adapter.AuthorizeRecoveryResult(context.Background(), actor, ref, content.DeliveryDownload)
	if err != nil {
		t.Fatalf("authorize open-error result: %v", err)
	}

	source, err := adapter.OpenRecoveryResultSource(context.Background(), content.RecoveryResultSourceRequest{
		OwnerUserID: fixture.requesterID, Ref: ref, ExpectedPublication: authorized.PublicationFingerprint,
		ExpectedContent: authorized.ContentDigest, Mode: content.SourceModeSequential, MaxBytes: authorized.Size,
	})
	if !errors.Is(err, ErrRecoveryResultUnavailable) || source != nil {
		t.Fatalf("target open error source=%v err=%v", source, err)
	}
	if len(target.readers) != 1 || !target.readers[0].closed {
		t.Fatalf("target open error readers=%+v", target.readers)
	}
}

func TestRecoveryResultDeliveryAdapterReauthorizationRejectsPrivateAuthorityDrift(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish private-authority drift fixture: %v", err)
	}
	resolver, err := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("construct private-authority drift resolver: %v", err)
	}
	adapter, err := NewRecoveryResultDeliveryAdapter(RecoveryResultDeliveryAdapterDependencies{
		Resolver: resolver, Target: &recoveryResultReadTargetFake{now: fixture.now},
		Now: func() time.Time { return fixture.now }, ReadPermitTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("construct private-authority drift adapter: %v", err)
	}
	actor := content.DeliveryActor{UserID: fixture.requesterID, Username: "admin", Role: "admin"}
	ref := content.RecoveryResultRef{
		RecoveryJobID: published.JobID, ResultID: published.Results[0].ID,
	}
	authorized, err := adapter.AuthorizeRecoveryResult(
		context.Background(), actor, ref, content.DeliveryDownload,
	)
	if err != nil {
		t.Fatalf("authorize private-authority drift result: %v", err)
	}
	fixture.updateJob(t, map[string]any{
		"workspace_owner": "drifted-result-marker-owner", "workspace_fence": fixture.job.WorkspaceFence + 1,
	})

	err = adapter.ReauthorizeRecoveryResult(
		context.Background(), actor, authorized, content.DeliveryDownload,
	)
	if err != ErrRecoveryResultUnavailable {
		t.Fatalf("private-authority drift reauthorization error=%v, want exact unavailable", err)
	}
}
