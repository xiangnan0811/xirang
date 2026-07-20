package content

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBrokerIssueOrdersAuthorizationLeaseSourceAuditBeforeCookieActivation(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"authorize", "lease", "source", "audit"}
	if strings.Join(*harness.order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("issuance order=%v", *harness.order)
	}
	if ticket.Cookie == nil || ticket.Cookie.Value == "" || ticket.Cookie.Path != ticket.Descriptor.ContentURL ||
		!ticket.Cookie.HttpOnly || ticket.Cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("ticket cookie=%+v descriptor=%+v", ticket.Cookie, ticket.Descriptor)
	}
	if ticket.Descriptor.ContentURL != "/api/v1/asset-content/"+harness.material.DeliveryID ||
		ticket.Descriptor.Renderer != RendererSafeRaster || ticket.Descriptor.Profile != ProfileRasterV1 ||
		ticket.Descriptor.Classification != ClassificationNonSecret || ticket.Descriptor.ContentLength <= 0 ||
		ticket.Descriptor.Range != RangeSingle {
		t.Fatalf("descriptor=%+v", ticket.Descriptor)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryActive) || grant.DeliveryID != harness.material.DeliveryID ||
		grant.CookieSecretHash != harness.material.CookieSecretHash || grant.SessionJTI != harness.session.JTI ||
		grant.StepUpAction != nil || grant.RepresentationSize != ticket.Descriptor.ContentLength {
		t.Fatalf("active grant=%+v", grant)
	}
	if len(harness.audit.inputs) != 1 {
		t.Fatalf("audit inputs=%d", len(harness.audit.inputs))
	}
	audit := harness.audit.inputs[0]
	if audit.GrantID != harness.material.GrantID || audit.Action != backupasset.AuditActionPreviewTicket ||
		audit.RecoveryPointID != harness.asset.Ref.RecoveryPointID || audit.EntryID != harness.asset.Ref.EntryID {
		t.Fatalf("ticket audit=%+v", audit)
	}
	for _, forbidden := range []string{harness.material.DeliveryID, harness.material.CookieSecret, harness.asset.Path, harness.asset.Name} {
		if strings.Contains(stringifyAuditInput(audit), forbidden) {
			t.Fatalf("audit contains forbidden fact %q: %+v", forbidden, audit)
		}
	}
}

func TestBrokerIssueFailureBoundsDetachedLeaseRelease(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.source.asset = nil

	if _, err := harness.broker.Issue(context.Background(), harness.issueRequest()); err == nil {
		t.Fatal("issuance with unavailable source succeeded")
	}
	snapshot := harness.lease.releaseContext()
	if snapshot.err != nil || !snapshot.hasDeadline || !snapshot.deadline.After(time.Now()) ||
		snapshot.deadline.After(time.Now().Add(6*time.Second)) {
		t.Fatalf("lease release cleanup context=%+v", snapshot)
	}
}

func TestBrokerViewerAndWrongDownloadProofFailBeforeProviderOrGrant(t *testing.T) {
	t.Run("viewer", func(t *testing.T) {
		harness := newBrokerTestHarness(t)
		request := harness.issueRequest()
		request.Actor.Role = "viewer"
		request.Session.Role = "viewer"
		if _, err := harness.broker.Issue(context.Background(), request); !errors.Is(err, backupasset.ErrForbidden) {
			t.Fatalf("viewer error=%v", err)
		}
		if len(*harness.order) != 0 || harness.source.openCalls != 0 {
			t.Fatalf("viewer touched dependencies order=%v source=%d", *harness.order, harness.source.openCalls)
		}
		assertBrokerGrantCount(t, harness.db, 0)
	})

	t.Run("operator download permission", func(t *testing.T) {
		harness := newBrokerTestHarness(t)
		request := harness.issueRequest()
		request.Action = DeliveryDownload
		request.Renderer = RendererAttachment
		request.Profile = ProfileOriginalV1
		request.Proof = &StepUpProof{
			Action: auth.StepUpActionAssetDownload, ID: strings.Repeat("e", 32),
			ExpiresAt: harness.now.Add(time.Minute),
		}
		if _, err := harness.broker.Issue(context.Background(), request); !errors.Is(err, backupasset.ErrForbidden) {
			t.Fatalf("operator download error=%v", err)
		}
		if harness.source.openCalls != 0 || len(*harness.order) != 0 {
			t.Fatalf("operator download touched dependencies order=%v source=%d", *harness.order, harness.source.openCalls)
		}
		assertBrokerGrantCount(t, harness.db, 0)
	})

	t.Run("download wrong proof", func(t *testing.T) {
		harness := newBrokerTestHarness(t)
		request := harness.issueRequest()
		request.Action = DeliveryDownload
		request.Renderer = RendererAttachment
		request.Profile = ProfileOriginalV1
		request.Actor.Role = "admin"
		request.Session.Role = "admin"
		request.Proof = &StepUpProof{
			Action: auth.StepUpActionAssetSecretReveal, ID: strings.Repeat("e", 32),
			ExpiresAt: harness.now.Add(time.Minute),
		}
		if _, err := harness.broker.Issue(context.Background(), request); !errors.Is(err, ErrInvalidDeliveryProduct) {
			t.Fatalf("wrong proof error=%v", err)
		}
		if harness.source.openCalls != 0 || len(*harness.order) != 0 {
			t.Fatalf("wrong proof touched provider order=%v source=%d", *harness.order, harness.source.openCalls)
		}
		assertBrokerGrantCount(t, harness.db, 0)
	})
}

func TestBrokerSecretPreviewRequiresExactProofAndBindsProofExpiry(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.asset.Path = "/home/app/.ssh/id_rsa"
	request := harness.issueRequest()
	if _, err := harness.broker.Issue(context.Background(), request); !errors.Is(err, ErrInvalidDeliveryProduct) {
		t.Fatalf("secret without proof error=%v", err)
	}
	assertBrokerGrantCount(t, harness.db, 0)
	if len(harness.lease.releaseFences) != 1 {
		t.Fatalf("failed secret lease releases=%d", len(harness.lease.releaseFences))
	}

	harness = newBrokerTestHarness(t)
	harness.asset.Path = "/home/app/.ssh/id_rsa"
	request = harness.issueRequest()
	proofExpiry := harness.now.Add(45 * time.Second)
	request.Proof = &StepUpProof{Action: auth.StepUpActionAssetSecretReveal, ID: strings.Repeat("e", 32), ExpiresAt: proofExpiry}
	ticket, err := harness.broker.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.Classification != string(ClassificationSecret) || grant.StepUpAction == nil ||
		*grant.StepUpAction != string(auth.StepUpActionAssetSecretReveal) || grant.StepUpExpiresAt == nil ||
		!grant.AbsoluteExpiresAt.Equal(proofExpiry) || !ticket.Descriptor.ExpiresAt.Equal(proofExpiry) {
		t.Fatalf("secret grant=%+v ticket=%+v", grant, ticket)
	}
}

func TestBrokerIssueBindsSearchClassificationRevisionIntoGrantAndETag(t *testing.T) {
	etags := make(map[int64]string, 2)
	for _, revision := range []int64{7, 8} {
		t.Run(strconv.FormatInt(revision, 10), func(t *testing.T) {
			harness := newBrokerTestHarness(t)
			harness.asset.SearchClassification = ClassificationNonSecret
			harness.asset.SearchClassificationRevision = revision
			ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
			if err != nil {
				t.Fatal(err)
			}
			var grant model.BackupAssetDeliveryGrant
			if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
				t.Fatal(err)
			}
			if grant.ClassificationSourceRevision != revision {
				t.Fatalf("classification source revision=%d want=%d", grant.ClassificationSourceRevision, revision)
			}
			etags[revision] = ticket.Descriptor.ETag
		})
	}
	if etags[7] == etags[8] {
		t.Fatalf("classification revision did not change ETag: %s", etags[7])
	}
}

func TestRepresentationETagBindsStableModifiedTime(t *testing.T) {
	asset := AuthorizedAsset{
		Ref: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64),
		},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "source-v1",
		EntryFingerprint: "entry-v1", FingerprintStrength: "strong", Size: 4,
	}
	product := DeliveryProduct{
		Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Classification: ClassificationNonSecret,
	}
	plan := RenderPlan{MediaType: "image/png", SourceBytes: 4, Size: 4}
	classification := ClassificationResult{Classification: ClassificationNonSecret, PolicyRevision: 1, SourceRevision: 1}
	withoutModified := representationETag(asset, product, plan, classification)
	modified := time.Date(2026, 7, 19, 3, 4, 5, 123456789, time.UTC)
	asset.ModifiedAt = &modified
	withModified := representationETag(asset, product, plan, classification)
	if withoutModified == withModified {
		t.Fatalf("stable modification time did not change ETag: %s", withoutModified)
	}
}

func TestBrokerIssueSearchSecretEvidenceElevatesPreview(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.asset.SearchClassification = ClassificationSecret
	harness.asset.SearchClassificationRevision = 9
	request := harness.issueRequest()
	request.Proof = &StepUpProof{
		Action: auth.StepUpActionAssetSecretReveal, ID: strings.Repeat("e", 32),
		ExpiresAt: harness.now.Add(time.Minute),
	}
	ticket, err := harness.broker.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if ticket.Descriptor.Classification != ClassificationSecret || grant.Classification != string(ClassificationSecret) ||
		grant.ClassificationSourceRevision != 9 {
		t.Fatalf("Search-classified ticket=%+v grant=%+v", ticket.Descriptor, grant)
	}
}

func TestBrokerIssueLargeAssetLeavesBoundedPrefixProbeHeadroom(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.source.payload = append(append([]byte(nil), harness.source.payload...), bytes.Repeat([]byte{0}, 8<<10)...)
	harness.asset.Size = int64(len(harness.source.payload))

	classifier, renderer, err := newBrokerPolicies(testBrokerConfig())
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, _, err := harness.broker.readTicketPrefix(context.Background(), *harness.asset, nil, classifier, renderer)
	if err != nil {
		t.Fatalf("read large asset prefix: %v", err)
	}
	if len(harness.source.requests) != 1 {
		t.Fatalf("source requests=%d want=1", len(harness.source.requests))
	}
	request := harness.source.requests[0]
	wantPrefixBytes := int64(4<<10) + 1
	if int64(len(prefix)) != wantPrefixBytes || request.Mode != SourceModeSequential || request.MaxBytes != wantPrefixBytes+1 {
		t.Fatalf("prefix source request=%+v want max_bytes=%d", request, wantPrefixBytes+1)
	}
}

func TestBrokerIssueUsesCurrentAtomicClassificationAndRendererPolicy(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.source.payload = []byte("abcdefghij")
	harness.asset.Size = int64(len(harness.source.payload))
	harness.asset.MediaType = "text/plain"
	harness.asset.Path = "/logs/current.log"
	harness.asset.Name = "current.log"
	harness.asset.RangeProven = false
	config := testBrokerConfig()
	config.Classification = ClassificationConfig{ScanBytes: 16}
	config.Renderer = RendererConfig{
		TextBytes: 4, HexBytes: 4, RasterMaxPixels: 1 << 20,
		PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20,
	}
	harness.broker.config = func(context.Context) (BrokerConfig, error) { return config, nil }
	request := harness.issueRequest()
	request.Renderer = RendererEscapedText
	request.Profile = ProfileTextV1
	ticket, err := harness.broker.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Descriptor.ContentLength != 4 {
		t.Fatalf("dynamic text representation length=%d want=4", ticket.Descriptor.ContentLength)
	}
}

func TestBrokerTicketAuditFailureRevokesGrantReleasesLeaseAndReturnsNoCookie(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.audit.err = errors.New("audit unavailable")
	if ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest()); !errors.Is(err, ErrContentAuditUnavailable) || ticket.Cookie != nil {
		t.Fatalf("ticket=%+v error=%v", ticket, err)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "audit_failed" || grant.RevokedAt == nil {
		t.Fatalf("audit-failed grant=%+v", grant)
	}
	if len(harness.lease.releaseFences) != 1 {
		t.Fatalf("lease releases=%d", len(harness.lease.releaseFences))
	}
}

func TestBrokerIssueAuditsBlockedAndFailureOutcomesWithoutTicket(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		mutate      func(*brokerTestHarness)
		wantErr     error
		wantOutcome backupasset.AuditOutcome
		wantFailure string
		wantMetric  MetricOutcome
	}{
		{
			name: "secret preview without proof is blocked",
			mutate: func(harness *brokerTestHarness) {
				harness.asset.Path = "/home/app/.ssh/id_rsa"
			},
			wantErr: ErrInvalidDeliveryProduct, wantOutcome: backupasset.AuditOutcomeBlocked,
			wantFailure: "request_blocked", wantMetric: MetricOutcomeBlocked,
		},
		{
			name: "source binding failure",
			mutate: func(harness *brokerTestHarness) {
				harness.source.payload = harness.source.payload[:len(harness.source.payload)-1]
			},
			wantErr: ErrContentSourceUnavailable, wantOutcome: backupasset.AuditOutcomeFailure,
			wantFailure: "request_failed", wantMetric: MetricOutcomeFailure,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newBrokerTestHarness(t)
			testCase.mutate(harness)
			ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
			if !errors.Is(err, testCase.wantErr) || ticket.Cookie != nil {
				t.Fatalf("ticket=%+v error=%v want=%v", ticket, err, testCase.wantErr)
			}
			assertBrokerGrantCount(t, harness.db, 0)
			if len(harness.audit.inputs) != 1 {
				t.Fatalf("ticket audit inputs=%d", len(harness.audit.inputs))
			}
			audit := harness.audit.inputs[0]
			if audit.Action != backupasset.AuditActionPreviewTicket || audit.Outcome != testCase.wantOutcome ||
				audit.FailureCode != testCase.wantFailure || audit.GrantID != harness.material.GrantID {
				t.Fatalf("ticket audit=%+v", audit)
			}
			if harness.metrics.ticketCount(DeliveryPreview, testCase.wantMetric) != 1 {
				t.Fatalf("ticket metrics=%+v", harness.metrics.snapshot())
			}
			for _, forbidden := range []string{
				harness.material.DeliveryID, harness.material.CookieSecret, harness.asset.Path, harness.asset.Name,
			} {
				if strings.Contains(stringifyAuditInput(audit), forbidden) {
					t.Fatalf("audit contains forbidden fact %q: %+v", forbidden, audit)
				}
			}
		})
	}
}

func TestBrokerServeAuthenticatesCookieReservesBeforeSourceAndFreezesGetHead(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	rawCookie := ticket.Cookie.Name + "=" + ticket.Cookie.Value

	get := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
	}, get); err != nil {
		t.Fatal(err)
	}
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), harness.source.payload) {
		t.Fatalf("GET status=%d body=%x", get.Code, get.Body.Bytes())
	}
	if get.Header().Get("Content-Type") != "image/png" || get.Header().Get("Content-Length") == "" ||
		get.Header().Get("ETag") != ticket.Descriptor.ETag || get.Header().Get("Accept-Ranges") != "bytes" ||
		len(get.deadlines) == 0 {
		t.Fatalf("GET headers=%v deadlines=%v", get.Header(), get.deadlines)
	}

	head := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodHead, RawCookie: rawCookie,
	}, head); err != nil {
		t.Fatal(err)
	}
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("HEAD status=%d headers=%v body=%x", head.Code, head.Header(), head.Body.Bytes())
	}
	if harness.source.openCalls != 3 {
		t.Fatalf("source opens=%d want issuance+GET+HEAD", harness.source.openCalls)
	}
	var requests int64
	if err := harness.db.Model(&model.BackupAssetDeliveryRequest{}).Count(&requests).Error; err != nil || requests != 2 {
		t.Fatalf("delivery requests=%d err=%v", requests, err)
	}
}

func TestBrokerDerivedPreviewUsesBrokerBytesAndReauthorizesOriginalAsset(t *testing.T) {
	harness := newBrokerTestHarness(t)
	payload := []byte("<derived> searchable text\n")
	modified := harness.now.Add(-time.Minute)
	derived := &brokerDerivedResolverFake{
		payload: payload,
		binding: DerivedRepresentation{
			artifactID: strings.Repeat("1", 32), artifactSetID: strings.Repeat("2", 32), blobID: strings.Repeat("3", 32),
			setCompleteness: "complete",
			Ref:             harness.asset.Ref, CatalogGenerationID: harness.asset.CatalogGenerationID,
			SourceFingerprint: harness.asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
			Provider: harness.asset.Provider, Renderer: RendererEscapedText, Profile: ProfileTextV1,
			Role: "content", MediaType: "text/plain", Size: int64(len(payload)),
			EntryFingerprint: strings.Repeat("4", 64), Completeness: "complete", ModifiedAt: &modified,
		},
	}
	harness.broker.derived = derived
	harness.broker.securityPolicyRevision = func(context.Context) (string, error) { return "security-policy-v1", nil }
	request := harness.issueRequest()
	request.Renderer = RendererEscapedText
	request.Profile = ProfileTextV1

	ticket, err := harness.broker.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if harness.source.openCalls != 0 || derived.resolveCalls != 1 || derived.openCalls != 1 {
		t.Fatalf("issue source opens provider=%d derived resolve=%d open=%d", harness.source.openCalls, derived.resolveCalls, derived.openCalls)
	}
	recorder := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
	}, recorder); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "&lt;derived&gt; searchable text\n" || harness.source.openCalls != 0 {
		t.Fatalf("derived response status=%d body=%q provider opens=%d", recorder.Code, recorder.Body.String(), harness.source.openCalls)
	}
	var deliveryRequest model.BackupAssetDeliveryRequest
	if err := harness.db.Order("created_at DESC").First(&deliveryRequest).Error; err != nil {
		t.Fatal(err)
	}
	if deliveryRequest.ProviderBytes != 0 || deliveryRequest.ResponseBytes != int64(recorder.Body.Len()) {
		t.Fatalf("derived byte accounting provider=%d response=%d", deliveryRequest.ProviderBytes, deliveryRequest.ResponseBytes)
	}
	if len(harness.authorizer.reauthorized) == 0 || harness.authorizer.reauthorized[0] != *harness.asset {
		t.Fatalf("reauthorized asset=%+v want original=%+v", harness.authorizer.reauthorized, *harness.asset)
	}

	derived.stale = true
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}); !errors.Is(err, ErrContentUnavailable) {
		t.Fatalf("stale Derived read error=%v", err)
	}
	if harness.source.openCalls != 0 {
		t.Fatalf("stale Derived read fell back to Provider opens=%d", harness.source.openCalls)
	}
}

func TestBrokerDerivedPreviewRevokesTicketWhenSecurityPolicyChangesBeforeRead(t *testing.T) {
	harness := newBrokerTestHarness(t)
	payload := []byte("derived preview")
	modified := harness.now.Add(-time.Minute)
	derived := &brokerDerivedResolverFake{
		payload: payload,
		binding: DerivedRepresentation{
			artifactID: strings.Repeat("1", 32), artifactSetID: strings.Repeat("2", 32), blobID: strings.Repeat("3", 32),
			setCompleteness: "complete",
			Ref:             harness.asset.Ref, CatalogGenerationID: harness.asset.CatalogGenerationID,
			SourceFingerprint: harness.asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
			Provider: harness.asset.Provider, Renderer: RendererEscapedText, Profile: ProfileTextV1,
			Role: "content", MediaType: "text/plain", Size: int64(len(payload)),
			EntryFingerprint: strings.Repeat("4", 64), Completeness: "complete", ModifiedAt: &modified,
		},
	}
	harness.broker.derived = derived
	harness.broker.securityPolicyRevision = func(context.Context) (string, error) { return "security-policy-v1", nil }
	request := harness.issueRequest()
	request.Renderer = RendererEscapedText
	request.Profile = ProfileTextV1
	ticket, err := harness.broker.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	openedAtIssue := derived.openCalls
	harness.broker.securityPolicyRevision = func(context.Context) (string, error) { return "security-policy-v2", nil }
	err = harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()})
	if !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("policy-drift read error=%v, want content not found", err)
	}
	if derived.openCalls != openedAtIssue || harness.source.openCalls != 0 {
		t.Fatalf("policy-drift read opened bytes: derived=%d provider=%d", derived.openCalls, harness.source.openCalls)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "policy_changed" {
		t.Fatalf("policy-drift grant state=%q reason=%q", grant.State, grant.RevocationReason)
	}
}

func TestBrokerLongReadRenewsLeaseWithoutWriterProgress(t *testing.T) {
	harness := newBrokerTestHarness(t)
	config := testBrokerConfig()
	config.LeaseHeartbeat = 25 * time.Millisecond
	harness.broker.config = func(context.Context) (BrokerConfig, error) { return config, nil }
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}

	realStart := time.Now()
	harness.broker.now = func() time.Time { return harness.now.Add(time.Since(realStart)) }
	harness.lease.now = harness.now
	harness.lease.nowFn = harness.broker.now
	harness.lease.renewed = make(chan struct{}, 16)
	harness.source.blockReads = true
	harness.source.readStarted = make(chan struct{})
	harness.source.readCanceled = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- harness.broker.Serve(ctx, GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
		}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()})
	}()
	select {
	case <-harness.source.readStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source read did not start")
	}
	baseline := harness.lease.renewCount()
	deadline := time.After(500 * time.Millisecond)
	for harness.lease.renewCount() == baseline {
		select {
		case <-harness.lease.renewed:
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("lease renewals stayed at %d while source read was blocked", baseline)
		}
	}
	cancel()
	if serveErr := <-done; !errors.Is(serveErr, context.Canceled) {
		t.Fatalf("serve cancellation error=%v", serveErr)
	}
}

func TestBrokerCanceledReadBoundsDetachedFinalizationWithAudit(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	budgetCapture := &brokerBudgetContextCapture{BrokerBudget: harness.broker.budget}
	harness.broker.budget = budgetCapture
	harness.source.blockReads = true
	harness.source.readStarted = make(chan struct{})
	harness.source.readCanceled = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- harness.broker.Serve(ctx, GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
		}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()})
	}()
	select {
	case <-harness.source.readStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source read did not start")
	}
	cancel()
	if serveErr := <-done; !errors.Is(serveErr, context.Canceled) {
		t.Fatalf("serve cancellation error=%v", serveErr)
	}
	snapshot := budgetCapture.snapshot()
	if snapshot.err != nil || !snapshot.hasDeadline || !snapshot.deadline.After(time.Now()) ||
		snapshot.deadline.After(time.Now().Add(6*time.Second)) {
		t.Fatalf("finalization and audit cleanup context=%+v", snapshot)
	}
}

func TestBrokerFinalizationPersistsReadAuditExactlyOnce(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
		RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}); err != nil {
		t.Fatal(err)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.AuditRequestCount != 1 || grant.AuditSuccessCount != 1 {
		t.Fatalf("read audit was not persisted exactly once: %+v", grant)
	}
}

func TestGatewayHeartbeatUsesPersistedIdleExpiry(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ParseDeliveryCookie(ticket.Cookie.Name + "=" + ticket.Cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	grant, actor, session, asset, lease, err := harness.broker.authorizeGatewayRequest(
		context.Background(), harness.material.DeliveryID, secret,
	)
	if err != nil {
		t.Fatal(err)
	}

	refreshedIdleExpiry := harness.now.Add(90 * time.Second)
	if err := harness.db.Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ?", grant.ID).Update("idle_expires_at", refreshedIdleExpiry).Error; err != nil {
		t.Fatal(err)
	}
	current := harness.now.Add(61 * time.Second)
	harness.broker.now = func() time.Time { return current }
	harness.lease.nowFn = harness.broker.now
	if err := harness.broker.gatewayHeartbeat(
		context.Background(), grant, actor, session, asset, lease, time.Minute,
	); err != nil {
		t.Fatalf("heartbeat rejected refreshed persisted idle expiry: %v", err)
	}

	current = refreshedIdleExpiry.Add(time.Second)
	if err := harness.broker.gatewayHeartbeat(
		context.Background(), grant, actor, session, asset, lease, time.Minute,
	); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("heartbeat after persisted idle expiry error=%v", err)
	}
}

func TestGatewayReadStateSamplesProviderBytesAfterSourceCloseProbe(t *testing.T) {
	probeErr := errors.New("FAKE_CLOSE_PROBE_LIMIT_FOR_TEST_ONLY")
	reader := &brokerCloseProbeReader{providerBytes: 5, closeProbeBytes: 1, closeErr: probeErr}
	state := &gatewayReadState{
		source:       &brokerCloseProbeSourceSession{reader: reader},
		sourceReader: reader,
	}

	providerBytes, err := state.close()
	if providerBytes != 6 || !errors.Is(err, probeErr) {
		t.Fatalf("close provider bytes=%d err=%v, want 6 and probe error", providerBytes, err)
	}
}

func TestBrokerServeSingleRangeAndMalformedRangeAccounting(t *testing.T) {
	harness := newBrokerTestHarness(t)
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	rawCookie := ticket.Cookie.Name + "=" + ticket.Cookie.Value
	harness.source.onOpen = func(request SourceRequest) {
		if request.Mode != SourceModeRange {
			return
		}
		var count int64
		if err := harness.db.Model(&model.BackupAssetDeliveryRequest{}).
			Where("state = ?", RequestReserved).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("source opened before reservation count=%d err=%v", count, err)
		}
	}

	ranged := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
		RangeHeaders: []string{"bytes=1-3"},
	}, ranged); err != nil {
		t.Fatal(err)
	}
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), harness.source.payload[1:4]) ||
		ranged.Header().Get("Content-Range") != "bytes 1-3/"+strconv.Itoa(len(harness.source.payload)) ||
		ranged.Header().Get("Content-Length") != "3" {
		t.Fatalf("range status=%d headers=%v body=%x", ranged.Code, ranged.Header(), ranged.Body.Bytes())
	}
	lastRequest := harness.source.requests[len(harness.source.requests)-1]
	if lastRequest.Mode != SourceModeRange || lastRequest.Range == nil || lastRequest.Range.Offset != 1 || lastRequest.Range.Length != 3 {
		t.Fatalf("source range request=%+v", lastRequest)
	}

	openCalls := harness.source.openCalls
	malformed := httptest.NewRecorder()
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
		RangeHeaders: []string{"bytes=0-1,3-4"},
	}, malformed); err != nil {
		t.Fatal(err)
	}
	if malformed.Code != http.StatusRequestedRangeNotSatisfiable ||
		malformed.Header().Get("Content-Range") != "bytes */"+strconv.Itoa(len(harness.source.payload)) ||
		harness.source.openCalls != openCalls {
		t.Fatalf("malformed range status=%d headers=%v source opens=%d", malformed.Code, malformed.Header(), harness.source.openCalls)
	}

	duplicateCookie := rawCookie + "; " + rawCookie
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: duplicateCookie,
	}, httptest.NewRecorder()); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("duplicate cookie error=%v", err)
	}
	if harness.source.openCalls != openCalls {
		t.Fatalf("duplicate cookie opened source: %d -> %d", openCalls, harness.source.openCalls)
	}
}

func TestBrokerNoRangeProviderMaterializesAuthenticatedCacheBeforeRangeUpgrade(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.asset.RangeProven = false
	cacheConfig := testCacheConfig("")
	cacheConfig.DiskEnabled = false
	cacheConfig.MemoryObjectBytes = 1 << 20
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: cacheConfig, Now: func() time.Time { return harness.broker.now().UTC() }, Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	if err := harness.broker.SetCache(cache); err != nil {
		t.Fatal(err)
	}

	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Descriptor.Range != RangeNone {
		t.Fatalf("initial range=%q want none", ticket.Descriptor.Range)
	}
	rawCookie := ticket.Cookie.Name + "=" + ticket.Cookie.Value
	full := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
	}, full); err != nil {
		t.Fatal(err)
	}
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), harness.source.payload) {
		t.Fatalf("full status=%d body=%x", full.Code, full.Body.Bytes())
	}

	ranged := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
		RangeHeaders: []string{"bytes=1-3"},
	}, ranged); err != nil {
		t.Fatal(err)
	}
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), harness.source.payload[1:4]) ||
		ranged.Header().Get("Accept-Ranges") != "bytes" || harness.source.openCalls != 4 {
		t.Fatalf("range status=%d headers=%v body=%x source_opens=%d", ranged.Code, ranged.Header(), ranged.Body.Bytes(), harness.source.openCalls)
	}
	wantModes := []SourceMode{SourceModeSequential, SourceModeSequential, SourceModeStat, SourceModeStat}
	for index, want := range wantModes {
		if harness.source.requests[index].Mode != want {
			t.Fatalf("source request[%d]=%+v want mode=%s", index, harness.source.requests[index], want)
		}
	}
	if !harness.authorizer.onlyReauthorizedProviderRange(false) {
		t.Fatalf("cache capability escaped as Provider Range: %+v", harness.authorizer.reauthorized)
	}
	var requests []model.BackupAssetDeliveryRequest
	if err := harness.db.Order("started_at ASC").Find(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ProviderBytes != int64(len(harness.source.payload)) ||
		requests[0].ResponseBytes != int64(len(harness.source.payload)) || requests[1].ProviderBytes != 0 || requests[1].ResponseBytes != 3 {
		t.Fatalf("cache request byte evidence=%+v", requests)
	}
}

func TestBrokerCacheIntegrityFailureRevokesCacheBackedGrant(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.asset.RangeProven = false
	cacheConfig := testCacheConfig(filepath.Join(t.TempDir(), "cache"))
	cacheConfig.MemoryObjectBytes = 1
	cache := newDiskCacheForTest(t, cacheConfig)
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	if err := harness.broker.SetCache(cache); err != nil {
		t.Fatal(err)
	}
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	rawCookie := ticket.Cookie.Name + "=" + ticket.Cookie.Value
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}); err != nil {
		t.Fatal(err)
	}
	object := cacheObjectForAsset(42, *harness.asset, RendererSafeRaster, ProfileRasterV1)
	entry := cacheEntryForTest(t, cache, object)
	chunkPath := filepath.Join(cache.rootPath, entry.chunks[0].name)
	sealed, err := os.ReadFile(chunkPath)
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0x40
	if err := os.WriteFile(chunkPath, sealed, 0o600); err != nil {
		t.Fatal(err)
	}

	serveErr := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
		RangeHeaders: []string{"bytes=0-1"},
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()})
	if !errors.Is(serveErr, ErrCacheIntegrity) {
		t.Fatalf("tampered cache serve error=%v", serveErr)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "cache_invalid" {
		t.Fatalf("tampered cache grant state=%s reason=%s", grant.State, grant.RevocationReason)
	}
}

func TestBrokerCacheBackedRangeMissRevokesGrantWithoutProviderSeek(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.asset.RangeProven = false
	cacheConfig := testCacheConfig("")
	cacheConfig.DiskEnabled = false
	cacheConfig.MemoryObjectBytes = 1 << 20
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: cacheConfig, Now: func() time.Time { return harness.broker.now().UTC() }, Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	if err := harness.broker.SetCache(cache); err != nil {
		t.Fatal(err)
	}
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	rawCookie := ticket.Cookie.Name + "=" + ticket.Cookie.Value
	if err := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}); err != nil {
		t.Fatal(err)
	}
	object := cacheObjectForAsset(42, *harness.asset, RendererSafeRaster, ProfileRasterV1)
	if err := cache.Evict(object); err != nil {
		t.Fatal(err)
	}
	openCalls := harness.source.openCalls
	serveErr := harness.broker.Serve(context.Background(), GatewayRequest{
		DeliveryID: harness.material.DeliveryID, Method: http.MethodGet, RawCookie: rawCookie,
		RangeHeaders: []string{"bytes=0-1"},
	}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()})
	if !errors.Is(serveErr, ErrContentNotFound) || harness.source.openCalls != openCalls {
		t.Fatalf("cache miss error=%v source opens=%d want=%d", serveErr, harness.source.openCalls, openCalls)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != string(DeliveryRevoked) || grant.RevocationReason != "cache_invalid" {
		t.Fatalf("cache-miss grant state=%s reason=%s", grant.State, grant.RevocationReason)
	}
}

func TestBrokerDrainRevokesAndReleasesButResumeAllowsNewTickets(t *testing.T) {
	harness := newBrokerTestHarness(t)
	if _, err := harness.broker.Issue(context.Background(), harness.issueRequest()); err != nil {
		t.Fatal(err)
	}
	if err := harness.broker.Drain(context.Background(), "feature_disabled"); err != nil {
		t.Fatal(err)
	}
	var first model.BackupAssetDeliveryGrant
	if err := harness.db.First(&first, "id = ?", harness.material.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if first.State != string(DeliveryRevoked) || first.RevocationReason != "feature_disabled" {
		t.Fatalf("drained grant state=%s reason=%s", first.State, first.RevocationReason)
	}
	if _, err := harness.broker.Issue(context.Background(), harness.issueRequest()); err == nil {
		t.Fatal("paused Broker issued a ticket")
	}
	if err := harness.broker.Resume(); err != nil {
		t.Fatal(err)
	}
	materialBytes := append(bytes.Repeat([]byte{0x44}, 16), bytes.Repeat([]byte{0x55}, 16)...)
	materialBytes = append(materialBytes, bytes.Repeat([]byte{0x66}, 32)...)
	second, err := newTicketMaterialFrom(bytes.NewReader(materialBytes))
	if err != nil {
		t.Fatal(err)
	}
	harness.broker.ticketMaterial = func() (TicketMaterial, error) { return second, nil }
	if _, err := harness.broker.Issue(context.Background(), harness.issueRequest()); err != nil {
		t.Fatalf("issue after Resume: %v", err)
	}
	harness.lease.mu.Lock()
	releases := len(harness.lease.releaseFences)
	harness.lease.mu.Unlock()
	if releases != 1 {
		t.Fatalf("drain lease releases=%d want=1", releases)
	}
}

func TestBrokerMetricsCoverTicketReadCacheAndInFlightLifecycle(t *testing.T) {
	harness := newBrokerTestHarness(t)
	harness.asset.RangeProven = false
	cacheConfig := testCacheConfig("")
	cacheConfig.DiskEnabled = false
	cacheConfig.MemoryObjectBytes = 1 << 20
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: cacheConfig, Now: func() time.Time { return harness.broker.now().UTC() }, Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	if err := harness.broker.SetCache(cache); err != nil {
		t.Fatal(err)
	}
	ticket, err := harness.broker.Issue(context.Background(), harness.issueRequest())
	if err != nil {
		t.Fatal(err)
	}
	rawCookie := ticket.Cookie.Name + "=" + ticket.Cookie.Value
	for _, rangeHeaders := range [][]string{nil, {"bytes=0-1"}} {
		if err := harness.broker.Serve(context.Background(), GatewayRequest{
			DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
			RawCookie: rawCookie, RangeHeaders: rangeHeaders,
		}, &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}); err != nil {
			t.Fatal(err)
		}
	}
	if harness.metrics.ticketCount(DeliveryPreview, MetricOutcomeSuccess) != 1 ||
		harness.metrics.readCount(DeliveryPreview, MetricOutcomeSuccess) != 2 ||
		harness.metrics.cacheCount(MetricCacheMiss) == 0 || harness.metrics.cacheCount(MetricCacheHit) == 0 ||
		harness.metrics.peakInFlight(backupasset.ProviderRsync) < 1 || harness.metrics.currentInFlight(backupasset.ProviderRsync) != 0 {
		t.Fatalf("metrics=%+v", harness.metrics.snapshot())
	}
}

type brokerDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (recorder *brokerDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}

type brokerTestHarness struct {
	db         *gorm.DB
	broker     *Broker
	now        time.Time
	asset      *AuthorizedAsset
	session    DeliverySession
	material   TicketMaterial
	order      *[]string
	lease      *brokerLeaseControllerFake
	source     *brokerSourceResolverFake
	audit      *brokerAuditFake
	authorizer *brokerAssetAuthorizerFake
	metrics    *brokerMetricsFake
}

func newBrokerTestHarness(t *testing.T) *brokerTestHarness {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{}, &model.BackupAssetDeliveryUsage{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	pngPayload := encodeRasterForTest(t, "png", 2, 2)
	asset := AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID: strings.Repeat("c", 32), RepositoryID: strings.Repeat("d", 32),
		Provider: backupasset.ProviderRsync, SourceFingerprint: "source-v1", EntryFingerprint: "entry-v1",
		FingerprintStrength: "strong", Size: int64(len(pngPayload)), MediaType: "image/png", RangeProven: true,
		Path: "/images/safe.png", Name: "safe.png",
	}
	materialBytes := append(bytes.Repeat([]byte{0x11}, 16), bytes.Repeat([]byte{0x22}, 16)...)
	materialBytes = append(materialBytes, bytes.Repeat([]byte{0x33}, 32)...)
	material, err := newTicketMaterialFrom(bytes.NewReader(materialBytes))
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	authorizer := &brokerAssetAuthorizerFake{asset: &asset, order: &order}
	lease := &brokerLeaseControllerFake{now: now, order: &order}
	source := &brokerSourceResolverFake{payload: pngPayload, asset: &asset, order: &order}
	audit := &brokerAuditFake{order: &order}
	metrics := newBrokerMetricsFake()
	budget, err := NewBudgetService(BudgetDependencies{
		DB: db, Now: func() time.Time { return now },
		Limits: func(context.Context) (BudgetLimits, error) { return testBudgetLimits(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(BrokerDependencies{
		DB: db, Now: func() time.Time { return now }, FeatureEnabled: func(context.Context) (bool, error) { return true, nil },
		Authorize: authorizer, Session: brokerSessionValidatorFake{}, Lease: lease, Source: source,
		Audit: audit, Budget: budget, Metrics: metrics,
		TicketMaterial: func() (TicketMaterial, error) { return material, nil },
		Config:         func(context.Context) (BrokerConfig, error) { return testBrokerConfig(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &brokerTestHarness{
		db: db, broker: broker, now: now, asset: &asset, authorizer: authorizer,
		session:  DeliverySession{JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 1, ExpiresAt: now.Add(time.Hour)},
		material: material, order: &order, lease: lease, source: source, audit: audit, metrics: metrics,
	}
}

func (harness *brokerTestHarness) issueRequest() IssueRequest {
	return IssueRequest{
		Actor: DeliveryActor{UserID: 42, Username: "operator", Role: "operator"}, Session: harness.session,
		Ref: harness.asset.Ref, Action: DeliveryPreview, Renderer: RendererSafeRaster, Profile: ProfileRasterV1,
		SecureCookie: true,
	}
}

type brokerAssetAuthorizerFake struct {
	mu           sync.Mutex
	asset        *AuthorizedAsset
	order        *[]string
	reauthorized []AuthorizedAsset
}

func (fake *brokerAssetAuthorizerFake) Authorize(_ context.Context, _ DeliveryActor, ref backupasset.AssetRef, _ DeliveryAction) (AuthorizedAsset, error) {
	*fake.order = append(*fake.order, "authorize")
	if fake.asset == nil || fake.asset.Ref != ref {
		return AuthorizedAsset{}, backupasset.ErrNotFound
	}
	return *fake.asset, nil
}

func (fake *brokerAssetAuthorizerFake) Reauthorize(_ context.Context, _ DeliveryActor, asset AuthorizedAsset, _ DeliveryAction) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.reauthorized = append(fake.reauthorized, asset)
	return nil
}

func (fake *brokerAssetAuthorizerFake) onlyReauthorizedProviderRange(want bool) bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.reauthorized) == 0 {
		return false
	}
	for _, asset := range fake.reauthorized {
		if asset.RangeProven != want {
			return false
		}
	}
	return true
}

type brokerSessionValidatorFake struct{}

func (brokerSessionValidatorFake) Validate(context.Context, DeliverySession) error { return nil }

type brokerLeaseControllerFake struct {
	mu            sync.Mutex
	now           time.Time
	nowFn         func() time.Time
	order         *[]string
	renewFences   []backupasset.LeaseFence
	releaseFences []backupasset.LeaseFence
	releaseCtx    brokerCleanupContextSnapshot
	renewed       chan struct{}
}

func (fake *brokerLeaseControllerFake) Acquire(_ context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	*fake.order = append(*fake.order, "lease")
	fence := backupasset.LeaseFence{
		LeaseID: request.OwnerID, RecoveryPointID: request.RecoveryPointID,
		HolderType: request.HolderType, OwnerID: request.OwnerID,
		AttemptID: strings.Repeat("7", 32), FenceToken: strings.Repeat("8", 64),
	}
	return backupasset.Lease{
		ID: fence.LeaseID, RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType,
		OwnerID: request.OwnerID, Status: backupasset.LeaseActive, LeaseExpiresAt: fake.now.Add(5 * time.Minute),
		AbsoluteDeadline: fake.now.Add(time.Hour), LastHeartbeatAt: fake.now, Fence: fence,
	}, nil
}

func (fake *brokerLeaseControllerFake) Renew(_ context.Context, fence backupasset.LeaseFence) (backupasset.Lease, error) {
	fake.mu.Lock()
	now := fake.now
	if fake.nowFn != nil {
		now = fake.nowFn().UTC()
	}
	fake.renewFences = append(fake.renewFences, fence)
	renewed := fake.renewed
	fake.mu.Unlock()
	if renewed != nil {
		select {
		case renewed <- struct{}{}:
		default:
		}
	}
	return backupasset.Lease{
		ID: fence.LeaseID, RecoveryPointID: fence.RecoveryPointID, HolderType: fence.HolderType,
		OwnerID: fence.OwnerID, Status: backupasset.LeaseActive, LeaseExpiresAt: now.Add(5 * time.Minute),
		AbsoluteDeadline: fake.now.Add(time.Hour), LastHeartbeatAt: now, Fence: fence,
	}, nil
}

func (*brokerLeaseControllerFake) ValidateFence(context.Context, backupasset.LeaseFence) error {
	return nil
}

func (fake *brokerLeaseControllerFake) Release(ctx context.Context, fence backupasset.LeaseFence) error {
	deadline, hasDeadline := ctx.Deadline()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.releaseFences = append(fake.releaseFences, fence)
	fake.releaseCtx = brokerCleanupContextSnapshot{hasDeadline: hasDeadline, deadline: deadline, err: ctx.Err()}
	return nil
}

func (fake *brokerLeaseControllerFake) releaseContext() brokerCleanupContextSnapshot {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.releaseCtx
}

func (fake *brokerLeaseControllerFake) renewCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.renewFences)
}

func (*brokerLeaseControllerFake) Takeover(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
	return backupasset.Lease{}, nil
}

type brokerSourceResolverFake struct {
	payload      []byte
	asset        *AuthorizedAsset
	order        *[]string
	openCalls    int
	requests     []SourceRequest
	onOpen       func(SourceRequest)
	blockReads   bool
	readStarted  chan struct{}
	readCanceled chan struct{}
}

type brokerDerivedResolverFake struct {
	binding      DerivedRepresentation
	payload      []byte
	resolveCalls int
	openCalls    int
	stale        bool
}

func (fake *brokerDerivedResolverFake) Resolve(
	_ context.Context,
	request DerivedRepresentationRequest,
) (DerivedRepresentation, error) {
	fake.resolveCalls++
	if fake.stale || request.Ref != fake.binding.Ref || request.CatalogGenerationID != fake.binding.CatalogGenerationID ||
		request.SourceFingerprint != fake.binding.SourceFingerprint ||
		request.SecurityPolicyRevision != fake.binding.SecurityPolicyRevision || request.Provider != fake.binding.Provider ||
		request.Renderer != fake.binding.Renderer || request.Profile != fake.binding.Profile {
		return DerivedRepresentation{}, ErrDerivedRepresentationUnavailable
	}
	return fake.binding, nil
}

func (fake *brokerDerivedResolverFake) Open(
	_ context.Context,
	binding DerivedRepresentation,
	request SourceRequest,
) (SourceSession, error) {
	fake.openCalls++
	if fake.stale || binding != fake.binding || request.Ref != binding.Ref ||
		request.CatalogGenerationID != binding.CatalogGenerationID || request.ExpectedSource != binding.SourceFingerprint ||
		request.ExpectedEntry != binding.EntryFingerprint || request.Mode == SourceModeRange {
		return nil, ErrDerivedRepresentationUnavailable
	}
	return &brokerSourceSessionFake{
		payload: append([]byte(nil), fake.payload...),
		stat: SourceStat{
			Size: binding.Size, ModifiedAt: binding.ModifiedAt, MediaType: binding.MediaType,
			SourceFingerprint: binding.SourceFingerprint, EntryFingerprint: binding.EntryFingerprint,
			FingerprintStrong: true,
		},
		capabilities: SourceCapabilities{Provider: binding.Provider, Sequential: true, Range: false},
		reader:       &brokerDerivedSourceReader{Reader: bytes.NewReader(fake.payload)},
	}, nil
}

func (fake *brokerDerivedResolverFake) Revalidate(context.Context, DerivedRepresentation) error {
	if fake.stale {
		return ErrDerivedRepresentationUnavailable
	}
	return nil
}

type brokerDerivedSourceReader struct {
	*bytes.Reader
}

func (*brokerDerivedSourceReader) Close() error         { return nil }
func (*brokerDerivedSourceReader) ProviderBytes() int64 { return 0 }

func (fake *brokerSourceResolverFake) OpenContentSource(ctx context.Context, request SourceRequest) (SourceSession, error) {
	fake.openCalls++
	fake.requests = append(fake.requests, request)
	*fake.order = append(*fake.order, "source")
	if fake.asset == nil || request.Ref != fake.asset.Ref || request.CatalogGenerationID != fake.asset.CatalogGenerationID ||
		request.ExpectedSource != fake.asset.SourceFingerprint || request.ExpectedEntry != fake.asset.EntryFingerprint {
		return nil, ErrInvalidSourceRequest
	}
	if fake.onOpen != nil {
		fake.onOpen(request)
	}
	payload := fake.payload
	if request.Mode == SourceModeRange {
		end := request.Range.Offset + request.Range.Length
		if request.Range.Offset < 0 || end > int64(len(payload)) {
			return nil, ErrInvalidSourceRequest
		}
		payload = payload[request.Range.Offset:end]
	}
	session := &brokerSourceSessionFake{
		payload: append([]byte(nil), payload...),
		stat: SourceStat{
			Size: int64(len(fake.payload)), ModifiedAt: fake.asset.ModifiedAt, MediaType: fake.asset.MediaType,
			SourceFingerprint: fake.asset.SourceFingerprint, EntryFingerprint: fake.asset.EntryFingerprint,
			FingerprintStrong: fake.asset.FingerprintStrength == "strong",
		},
		capabilities: SourceCapabilities{
			Provider: fake.asset.Provider, Sequential: true, Range: fake.asset.RangeProven,
		},
	}
	if fake.blockReads && request.Mode != SourceModeStat {
		session.reader = &blockingBrokerSourceReader{
			ctx: ctx, started: fake.readStarted, canceled: fake.readCanceled,
		}
	}
	return session, nil
}

func (*brokerSourceResolverFake) ValidateContentCacheRoot(context.Context, string) error { return nil }

type brokerSourceSessionFake struct {
	payload      []byte
	stat         SourceStat
	capabilities SourceCapabilities
	reader       SourceReader
	closeErr     error
}

func (fake *brokerSourceSessionFake) Stat() SourceStat { return fake.stat }

func (fake *brokerSourceSessionFake) Capabilities() SourceCapabilities { return fake.capabilities }

func (fake *brokerSourceSessionFake) Reader() SourceReader {
	if fake.reader == nil {
		fake.reader = &brokerSourceReaderFake{Reader: bytes.NewReader(fake.payload)}
	}
	return fake.reader
}

func (*brokerSourceSessionFake) Revalidate(context.Context) error { return nil }

func (fake *brokerSourceSessionFake) Close() error {
	if fake.reader != nil {
		_ = fake.reader.Close()
	}
	return fake.closeErr
}

type brokerSourceReaderFake struct {
	*bytes.Reader
	providerBytes int64
}

func (reader *brokerSourceReaderFake) Read(payload []byte) (int, error) {
	count, err := reader.Reader.Read(payload)
	reader.providerBytes += int64(count)
	return count, err
}

func (*brokerSourceReaderFake) Close() error { return nil }

func (reader *brokerSourceReaderFake) ProviderBytes() int64 { return reader.providerBytes }

type brokerCloseProbeSourceSession struct {
	reader *brokerCloseProbeReader
}

func (*brokerCloseProbeSourceSession) Stat() SourceStat { return SourceStat{} }

func (*brokerCloseProbeSourceSession) Capabilities() SourceCapabilities {
	return SourceCapabilities{}
}

func (session *brokerCloseProbeSourceSession) Reader() SourceReader { return session.reader }

func (*brokerCloseProbeSourceSession) Revalidate(context.Context) error { return nil }

func (session *brokerCloseProbeSourceSession) Close() error { return session.reader.Close() }

type brokerCloseProbeReader struct {
	providerBytes   int64
	closeProbeBytes int64
	closeErr        error
}

func (*brokerCloseProbeReader) Read([]byte) (int, error) { return 0, io.EOF }

func (reader *brokerCloseProbeReader) Close() error {
	reader.providerBytes += reader.closeProbeBytes
	return reader.closeErr
}

func (reader *brokerCloseProbeReader) ProviderBytes() int64 { return reader.providerBytes }

type blockingBrokerSourceReader struct {
	ctx       context.Context
	started   chan struct{}
	canceled  chan struct{}
	didStart  bool
	didCancel bool
}

func (reader *blockingBrokerSourceReader) Read([]byte) (int, error) {
	if !reader.didStart {
		reader.didStart = true
		close(reader.started)
	}
	<-reader.ctx.Done()
	if !reader.didCancel {
		reader.didCancel = true
		close(reader.canceled)
	}
	return 0, reader.ctx.Err()
}

func (*blockingBrokerSourceReader) Close() error { return nil }

func (*blockingBrokerSourceReader) ProviderBytes() int64 { return 0 }

type brokerAuditFake struct {
	inputs []backupasset.AuditEventInput
	err    error
	order  *[]string
}

type brokerCleanupContextSnapshot struct {
	hasDeadline bool
	deadline    time.Time
	err         error
}

type brokerBudgetContextCapture struct {
	BrokerBudget
	mu      sync.Mutex
	context brokerCleanupContextSnapshot
}

func (capture *brokerBudgetContextCapture) Finalize(ctx context.Context, intent FinalizeIntent) (Finalization, error) {
	deadline, hasDeadline := ctx.Deadline()
	capture.mu.Lock()
	capture.context = brokerCleanupContextSnapshot{hasDeadline: hasDeadline, deadline: deadline, err: ctx.Err()}
	capture.mu.Unlock()
	return capture.BrokerBudget.Finalize(ctx, intent)
}

func (capture *brokerBudgetContextCapture) snapshot() brokerCleanupContextSnapshot {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.context
}

type brokerMetricsSnapshot struct {
	tickets     map[[2]string]int
	reads       map[[2]string]int
	cache       map[string]int
	inFlight    map[string]int
	maxInFlight map[string]int
}

type brokerMetricsFake struct {
	mu           sync.Mutex
	tickets      map[[2]string]int
	reads        map[[2]string]int
	cache        map[string]int
	inFlight     map[string]int
	maxInFlight  map[string]int
	auditBacklog int
	auditRetries int
	reconcileAge time.Duration
}

func newBrokerMetricsFake() *brokerMetricsFake {
	return &brokerMetricsFake{
		tickets: make(map[[2]string]int), reads: make(map[[2]string]int), cache: make(map[string]int),
		inFlight: make(map[string]int), maxInFlight: make(map[string]int),
	}
}

func (fake *brokerMetricsFake) ObserveTicket(action DeliveryAction, outcome MetricOutcome) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.tickets[[2]string{string(action), string(outcome)}]++
}

func (fake *brokerMetricsFake) ObserveRead(action DeliveryAction, outcome MetricOutcome) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.reads[[2]string{string(action), string(outcome)}]++
}

func (fake *brokerMetricsFake) SetInFlight(provider backupasset.ProviderKind, count int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := string(provider)
	fake.inFlight[key] = count
	if count > fake.maxInFlight[key] {
		fake.maxInFlight[key] = count
	}
}

func (*brokerMetricsFake) AddBytes(MetricByteKind, int64) {}
func (*brokerMetricsFake) ObserveReason(MetricReason)     {}

func (fake *brokerMetricsFake) ObserveCache(outcome MetricCacheOutcome) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.cache[string(outcome)]++
}

func (fake *brokerMetricsFake) SetAuditBacklog(count int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.auditBacklog = count
}

func (fake *brokerMetricsFake) ObserveAuditRetry() {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.auditRetries++
}

func (fake *brokerMetricsFake) SetReconciliationAge(age time.Duration) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.reconcileAge = age
}

func (fake *brokerMetricsFake) auditState() (int, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.auditBacklog, fake.auditRetries
}

func (fake *brokerMetricsFake) reconciliationAge() time.Duration {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.reconcileAge
}

func (fake *brokerMetricsFake) ticketCount(action DeliveryAction, outcome MetricOutcome) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.tickets[[2]string{string(action), string(outcome)}]
}

func (fake *brokerMetricsFake) readCount(action DeliveryAction, outcome MetricOutcome) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.reads[[2]string{string(action), string(outcome)}]
}

func (fake *brokerMetricsFake) cacheCount(outcome MetricCacheOutcome) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.cache[string(outcome)]
}

func (fake *brokerMetricsFake) currentInFlight(provider backupasset.ProviderKind) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.inFlight[string(provider)]
}

func (fake *brokerMetricsFake) peakInFlight(provider backupasset.ProviderKind) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.maxInFlight[string(provider)]
}

func (fake *brokerMetricsFake) snapshot() brokerMetricsSnapshot {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return brokerMetricsSnapshot{
		tickets: maps.Clone(fake.tickets), reads: maps.Clone(fake.reads), cache: maps.Clone(fake.cache),
		inFlight: maps.Clone(fake.inFlight), maxInFlight: maps.Clone(fake.maxInFlight),
	}
}

func (fake *brokerAuditFake) Write(_ context.Context, input backupasset.AuditEventInput) error {
	*fake.order = append(*fake.order, "audit")
	fake.inputs = append(fake.inputs, input)
	return fake.err
}

func (*brokerAuditFake) BacklogAvailable(context.Context) error { return nil }

func testBrokerConfig() BrokerConfig {
	return BrokerConfig{
		TicketTimeout: 5 * time.Second, PreviewTTL: 2 * time.Minute, MediaTTL: 15 * time.Minute,
		IdleTTL: time.Minute, WriteIdleTimeout: 30 * time.Second, LeaseHeartbeat: time.Minute,
		MaxBytesPerRequest: 1 << 20, MaxCumulativeBytes: 4 << 20,
		MaxRequests: 100, MaxInFlight: 2,
		Classification: ClassificationConfig{ScanBytes: 4 << 10},
		Renderer: RendererConfig{
			TextBytes: 1 << 10, HexBytes: 1 << 10, RasterMaxPixels: 1 << 20,
			PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20,
		},
	}
}

func assertBrokerGrantCount(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&model.BackupAssetDeliveryGrant{}).Count(&count).Error; err != nil || count != want {
		t.Fatalf("grant count=%d want=%d err=%v", count, want, err)
	}
}

func stringifyAuditInput(input backupasset.AuditEventInput) string {
	return strings.Join([]string{
		input.Actor.Username, input.Actor.Role, string(input.Action), string(input.Outcome),
		input.RepositoryID, input.RecoveryPointID, input.EntryID, input.GrantID,
	}, "|")
}
