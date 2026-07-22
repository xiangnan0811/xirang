package updater

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestOnlineFetcherProductionTransportRequiresTLS13(t *testing.T) {
	config := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 1024, BundleMaxBytes: 1 << 20,
	}
	fetcher, err := NewOnlineFetcher(config)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := fetcher.httpClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("production updater transport does not require TLS 1.3: %#v", fetcher.httpClient.Transport)
	}
}

func TestOnlineFetcherRequiresExternalProxyAndExactHTTPSOrigin(t *testing.T) {
	base := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 1024, BundleMaxBytes: 1 << 20,
	}
	for _, mutate := range []func(*OnlineConfig){
		func(value *OnlineConfig) { value.ProxyURL = "" },
		func(value *OnlineConfig) { value.ProxyURL = "http://public-proxy.example.test" },
		func(value *OnlineConfig) { value.AllowedOrigins = []string{"http://updates.example.test"} },
		func(value *OnlineConfig) { value.AllowedOrigins = []string{"https://updates.example.test/path"} },
		func(value *OnlineConfig) { value.RequestTimeout = 0 },
	} {
		config := base
		mutate(&config)
		if _, err := NewOnlineFetcher(config); !errors.Is(err, ErrPolicyRejected) {
			t.Fatalf("unsafe online config=%+v error=%v", config, err)
		}
	}

	credential := []byte("updater-only-token")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://updates.example.test:443/v1/manifest" ||
			request.Header.Get("Authorization") != "Bearer "+string(credential) {
			t.Fatalf("unexpected allowlisted request: %s %v", request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader("manifest")), Request: request,
		}, nil
	})
	fetcher, err := newOnlineFetcher(base, credential, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fetcher.FetchMetadata(context.Background(), "https://updates.example.test:443/v1/manifest")
	if err != nil || string(payload) != "manifest" {
		t.Fatalf("payload=%q error=%v", payload, err)
	}
	for _, rawURL := range []string{
		"https://updates.example.test/v1/manifest",
		"https://other.example.test:443/v1/manifest",
		"https://updates.example.test:443/v1/../manifest",
		"https://user@updates.example.test:443/v1/manifest",
	} {
		if _, err := fetcher.FetchMetadata(context.Background(), rawURL); !errors.Is(err, ErrPolicyRejected) {
			t.Fatalf("unsafe URL %q error=%v", rawURL, err)
		}
	}
}

func TestOnlineFetcherRejectsRedirectOversizeAndCredentialBearingErrors(t *testing.T) {
	config := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 8, BundleMaxBytes: 16,
	}
	credential := []byte("never-return-this-token")
	responses := []roundTripFunc{
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://updates.example.test:443/other"}},
				Body: io.NopCloser(strings.NewReader("redirect")), Request: request,
			}, nil
		},
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Length": []string{"9"}},
				Body: io.NopCloser(strings.NewReader("123456789")), Request: request,
			}, nil
		},
		func(*http.Request) (*http.Response, error) { return nil, errors.New(string(credential)) },
	}
	for index, transport := range responses {
		fetcher, err := newOnlineFetcher(config, credential, &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetcher.FetchMetadata(context.Background(), "https://updates.example.test:443/manifest")
		if err == nil || strings.Contains(err.Error(), string(credential)) {
			t.Fatalf("unsafe result %d error=%v", index, err)
		}
	}
}

func TestOnlineFetcherBoundsInternalBundleStream(t *testing.T) {
	config := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 8, BundleMaxBytes: 16,
	}
	body := &trackingReadCloser{Reader: strings.NewReader(strings.Repeat("x", 17))}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept") != "application/octet-stream" {
			t.Fatalf("unexpected bundle Accept header: %q", request.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: -1,
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: body, Request: request,
		}, nil
	})
	fetcher, err := newOnlineFetcher(config, nil, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := fetcher.fetchStream(context.Background(), "https://updates.example.test:443/bundle", fetcher.bundleMaxBytes, "application/octet-stream")
	if err != nil {
		t.Fatalf("bounded internal bundle stream: %v", err)
	}
	payload, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if !errors.Is(readErr, ErrPolicyRejected) || closeErr != nil || len(payload) > int(config.BundleMaxBytes) || !body.closed {
		t.Fatalf("bounded stream bytes=%d read_err=%v close_err=%v body_closed=%v", len(payload), readErr, closeErr, body.closed)
	}

	wrongTypeBody := &trackingReadCloser{Reader: strings.NewReader("bundle")}
	wrongTypeFetcher, err := newOnlineFetcher(config, nil, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: 6,
			Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: wrongTypeBody, Request: request,
		}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	wrongTypeStream, err := wrongTypeFetcher.fetchStream(context.Background(), "https://updates.example.test:443/bundle", wrongTypeFetcher.bundleMaxBytes, "application/octet-stream")
	if !errors.Is(err, ErrPolicyRejected) || wrongTypeStream != nil || !wrongTypeBody.closed {
		t.Fatalf("wrong content type stream=%v err=%v body_closed=%v", wrongTypeStream, err, wrongTypeBody.closed)
	}
}

func TestOnlineFetcherBindsBundleOpenToOneUseVerifiedManifestAuthorization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, manifestPayload, bundlePayload := writeInboxCandidate(t, inboxRoot, "online", now)
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	manifestURL := "https://updates.example.test:443/v1/manifest.json"
	bundleURL := "https://updates.example.test:443/v1/bundle.tar"
	requests := make([]string, 0, 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		switch request.URL.String() {
		case manifestURL:
			return &http.Response{
				StatusCode: http.StatusOK, ContentLength: int64(len(manifestPayload)),
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:   io.NopCloser(bytes.NewReader(manifestPayload)), Request: request,
			}, nil
		case bundleURL:
			return &http.Response{
				StatusCode: http.StatusOK, ContentLength: int64(len(bundlePayload)),
				Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
				Body:   io.NopCloser(bytes.NewReader(bundlePayload)), Request: request,
			}, nil
		default:
			t.Fatalf("unexpected updater URL: %s", request.URL)
			return nil, nil
		}
	})
	config := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 1 << 20, BundleMaxBytes: 1 << 20,
	}
	fetcher, err := newOnlineFetcher(config, nil, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := fetcher.AuthorizeBundle(context.Background(), manifestURL, bundleURL, TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil || len(requests) != 1 || requests[0] != manifestURL {
		t.Fatalf("authorization requests=%v error=%v", requests, err)
	}
	verified, stream, err := fetcher.OpenVerifiedBundle(context.Background(), authorization)
	if err != nil || verified.ManifestDigest != SHA256Hex(manifestPayload) || verified.Manifest.BundleSHA256 != SHA256Hex(bundlePayload) {
		t.Fatalf("verified online bundle=%+v stream=%v error=%v", verified, stream, err)
	}
	payload, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(payload, bundlePayload) || len(requests) != 2 || requests[1] != bundleURL {
		t.Fatalf("bundle bytes=%d requests=%v read_err=%v close_err=%v", len(payload), requests, readErr, closeErr)
	}
	if _, replay, replayErr := fetcher.OpenVerifiedBundle(context.Background(), authorization); !errors.Is(replayErr, ErrPolicyRejected) || replay != nil {
		t.Fatalf("replayed authorization stream=%v error=%v", replay, replayErr)
	}
	other, err := newOnlineFetcher(config, nil, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, crossFetcher, crossErr := other.OpenVerifiedBundle(context.Background(), authorization); !errors.Is(crossErr, ErrPolicyRejected) || crossFetcher != nil {
		t.Fatalf("cross-fetcher authorization stream=%v error=%v", crossFetcher, crossErr)
	}
	if _, exists := reflect.TypeOf(fetcher).MethodByName("FetchBundle"); exists {
		t.Fatal("raw URL FetchBundle method still exposes an unverified bundle path")
	}
}

func TestServiceRegistersStoredCandidateAndCommitsActivation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{candidateID: strings.Repeat("1", 32)}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil || len(receipts) != 1 || receipts[0].CandidateID != client.candidateID {
		t.Fatalf("receipts=%+v error=%v", receipts, err)
	}
	journalPayload, err := os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"operator-package", inboxRoot, "manifest.json", "bundle.tar", "signature", "content", "path"} {
		if strings.Contains(strings.ToLower(string(journalPayload)), strings.ToLower(forbidden)) {
			t.Fatalf("candidate journal leaked %q: %s", forbidden, journalPayload)
		}
	}
	client.directive = &ActivationDirective{
		SchemaVersion: 1, CandidateID: client.candidateID, NewFingerprint: receipts[0].BundleFingerprint,
	}
	client.decision = ActivationReportResult{
		SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: receipts[0].BundleFingerprint,
	}
	retry, err := service.PollAndActivate(context.Background())
	if err != nil || retry != 5 {
		t.Fatalf("poll retry=%d error=%v", retry, err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != receipts[0].BundleFingerprint {
		t.Fatalf("active=%q error=%v", active, err)
	}
	journalPayload, err = os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil || string(journalPayload) != `{"schema_version":1,"candidates":[]}` {
		t.Fatalf("terminal candidate journal=%s error=%v", journalPayload, err)
	}
}

func TestCandidateJournalPersistsFullInboxCapacity(t *testing.T) {
	root := newStoreTestRoot(t)
	service := &Service{journalPath: filepath.Join(root, "candidate-journal.json")}
	journal := candidateJournal{SchemaVersion: 1, Candidates: make([]candidateJournalEntry, 0, maximumInboxCandidates)}
	for index := 0; index < maximumInboxCandidates; index++ {
		if err := addCandidateJournalEntry(&journal, candidateJournalEntry{
			CandidateID:       fmt.Sprintf("%032x", index+1),
			BundleFingerprint: fmt.Sprintf("%064x", index+1),
		}); err != nil {
			t.Fatalf("add candidate %d: %v", index, err)
		}
	}
	if err := service.writeCandidateJournal(journal); err != nil {
		t.Fatalf("full-capacity candidate journal write: %v", err)
	}
	decoded, err := service.readCandidateJournal()
	if err != nil || len(decoded.Candidates) != maximumInboxCandidates {
		t.Fatalf("decoded full-capacity journal candidates=%d error=%v", len(decoded.Candidates), err)
	}
	service.replayPath = filepath.Join(root, "candidate-receipts.json")
	replay := candidateReplayState{SchemaVersion: 1, Receipts: make([]candidateReplayEntry, 0, maximumInboxCandidates)}
	for index := 0; index < maximumInboxCandidates; index++ {
		receipt := protocolTestReceipt(time.Unix(1_800_000_000, 0).UTC())
		receipt.BundleFingerprint = fmt.Sprintf("%064x", index+1)
		if err := addCandidateReplayEntry(&replay, candidateReplayEntry{
			CandidateID: fmt.Sprintf("%032x", index+1), Receipt: receipt,
		}); err != nil {
			t.Fatalf("add replay candidate %d: %v", index, err)
		}
	}
	if err := service.writeCandidateReplayState(replay); err != nil {
		t.Fatalf("full-capacity replay write: %v", err)
	}
	decodedReplay, err := service.readCandidateReplayState()
	if err != nil || len(decodedReplay.Receipts) != maximumInboxCandidates {
		t.Fatalf("decoded full-capacity replay entries=%d error=%v", len(decodedReplay.Receipts), err)
	}
}

func TestServiceCommitClearsReplayReceiptByCandidateAndNewFingerprint(t *testing.T) {
	for _, testCase := range []struct {
		name string
		old  bool
	}{
		{name: "bootstrap", old: false},
		{name: "normal", old: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := newStoreTestRoot(t)
			store, err := NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			newBundle := verifiedBundleForStore(t, []BundleFilePayload{{Path: "data.dat", Mode: 0o444, Content: []byte("new")}})
			if _, err := store.StoreBundle(context.Background(), newBundle); err != nil {
				t.Fatal(err)
			}
			oldFingerprint := ""
			if testCase.old {
				oldBundle := verifiedBundleForStore(t, []BundleFilePayload{{Path: "data.dat", Mode: 0o444, Content: []byte("old")}})
				if _, err := store.StoreBundle(context.Background(), oldBundle); err != nil {
					t.Fatal(err)
				}
				oldFingerprint = oldBundle.BundleFingerprint
			}
			activator, err := NewActivator(root)
			if err != nil {
				t.Fatal(err)
			}
			if oldFingerprint != "" {
				if _, err := activator.Activate(context.Background(), ActivationRequest{
					CandidateID: strings.Repeat("a", 32), NewFingerprint: oldFingerprint,
				}); err != nil {
					t.Fatal(err)
				}
				if err := activator.Recover(context.Background(), oldFingerprint); err != nil {
					t.Fatal(err)
				}
			}
			candidateID := strings.Repeat("1", 32)
			if _, err := activator.Activate(context.Background(), ActivationRequest{
				CandidateID: candidateID, ExpectedOldFingerprint: oldFingerprint,
				NewFingerprint: newBundle.BundleFingerprint,
			}); err != nil {
				t.Fatal(err)
			}
			client := &serviceProtocolClient{decision: ActivationReportResult{
				SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: newBundle.BundleFingerprint,
			}}
			service := &Service{
				store: store, activator: activator, client: client,
				journalPath: filepath.Join(root, "candidate-journal.json"),
				replayPath:  filepath.Join(root, "candidate-receipts.json"),
			}
			receipt := protocolTestReceipt(time.Unix(1_800_000_000, 0).UTC())
			receipt.BundleFingerprint = newBundle.BundleFingerprint
			if err := service.writeCandidateReplayState(candidateReplayState{SchemaVersion: 1, Receipts: []candidateReplayEntry{{
				CandidateID: candidateID, Receipt: receipt,
			}}}); err != nil {
				t.Fatal(err)
			}
			if err := service.writeCandidateJournal(candidateJournal{SchemaVersion: 1, Candidates: []candidateJournalEntry{{
				CandidateID: candidateID, BundleFingerprint: newBundle.BundleFingerprint,
			}}}); err != nil {
				t.Fatal(err)
			}
			if err := service.reportAndFinalizeActivationLocked(context.Background(), ActivationReceipt{
				SchemaVersion: 1, CandidateID: candidateID, OldFingerprint: oldFingerprint,
				NewFingerprint: newBundle.BundleFingerprint, State: "swapped",
			}); err != nil {
				t.Fatal(err)
			}
			state, err := service.readCandidateReplayState()
			if err != nil || len(state.Receipts) != 0 {
				t.Fatalf("committed replay state=%+v error=%v", state, err)
			}
		})
	}
}

func TestServiceReplaysCandidateWhenJournalWriteWasInterrupted(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(storeRoot, "candidate-journal.json")
	candidateID := strings.Repeat("1", 32)
	client := &serviceProtocolClient{candidateID: candidateID}
	client.registerHook = func(call int, _ RegisterCandidateRequest) {
		if call != 1 {
			return
		}
		if err := os.Remove(journalPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(journalPath, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(inbox, store, activator, client, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	trust := TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}
	if _, err := service.ScanAndRegister(context.Background(), trust, now); !errors.Is(err, ErrActivationFailed) {
		t.Fatalf("interrupted journal write error=%v", err)
	}
	if err := os.RemoveAll(journalPath); err != nil {
		t.Fatal(err)
	}
	makeTreeWritable(inboxRoot)
	if err := os.RemoveAll(filepath.Join(inboxRoot, "operator-package")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	restartedActivator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restartedClient := &serviceProtocolClient{
		candidateID: candidateID,
		directive:   &ActivationDirective{SchemaVersion: 1, CandidateID: candidateID, NewFingerprint: client.registrations[0].Receipt.BundleFingerprint},
		decision:    ActivationReportResult{SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: client.registrations[0].Receipt.BundleFingerprint},
	}
	restarted, err := NewService(nil, store, restartedActivator, restartedClient, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.PollAndActivate(context.Background()); err != nil || retry != 5 {
		t.Fatalf("restarted replay retry=%d error=%v", retry, err)
	}
	if active, err := restartedActivator.ActiveFingerprint(); err != nil || active != client.registrations[0].Receipt.BundleFingerprint {
		t.Fatalf("restarted active=%q error=%v", active, err)
	}
}

func TestServiceScansInboxOnlyWhenCoreDeliversAuthorizedRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{candidateID: strings.Repeat("1", 32), scanRequested: true}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	trust := TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}
	if retry, err := service.PollScanAndActivate(context.Background(), trust, now); err != nil || retry != 5 || client.registerCalls != 1 {
		t.Fatalf("authorized scan retry=%d calls=%d err=%v", retry, client.registerCalls, err)
	}
	if retry, err := service.PollScanAndActivate(context.Background(), trust, now); err != nil || retry != 5 || client.registerCalls != 2 {
		t.Fatalf("non-requested scan retry=%d calls=%d err=%v", retry, client.registerCalls, err)
	}
}

func TestServiceProcessesInboxCandidatesSequentially(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, manifestPayload, bundle := writeInboxCandidate(t, inboxRoot, "candidate-a", now)
	writeInboxCandidatePayloads(t, inboxRoot, "candidate-b", manifestPayload, bundle)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.Repeat("1", 32)
	client := &serviceProtocolClient{candidateID: candidateID}
	client.registerHook = func(call int, _ RegisterCandidateRequest) {
		if call != 1 {
			return
		}
		path := filepath.Join(inboxRoot, "candidate-b", "bundle.tar")
		changed := append([]byte(nil), bundle...)
		changed[512] ^= 1
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, changed, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if !errors.Is(err, ErrPolicyRejected) || client.registerCalls != 1 {
		t.Fatalf("sequential scan error=%v register_calls=%d", err, client.registerCalls)
	}
	journal, readErr := os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if readErr != nil || !strings.Contains(string(journal), candidateID) {
		t.Fatalf("first candidate was not durably journaled: journal=%s err=%v", journal, readErr)
	}
}

func TestServiceStreams320MiBCanonicalBundleBelowHeapBudget(t *testing.T) {
	const (
		contentSize = int64(320 << 20)
		heapBudget  = uint64(128 << 20)
	)
	now := time.Unix(1_800_000_000, 0).UTC()
	inboxRoot := t.TempDir()
	publicKey, bundleSHA256, memberSHA256 := writeLargeStreamingInboxCandidate(t, inboxRoot, "large-candidate", now, contentSize)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, contentSize+(4<<20))
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{candidateID: strings.Repeat("1", 32)}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	previousGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previousGCPercent)
	done := make(chan struct{})
	peakResult := make(chan uint64, 1)
	go func() {
		peak := baseline.HeapAlloc
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				if sample.HeapAlloc > peak {
					peak = sample.HeapAlloc
				}
			case <-done:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				if sample.HeapAlloc > peak {
					peak = sample.HeapAlloc
				}
				peakResult <- peak
				return
			}
		}
	}()
	trust := TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}
	receipts, scanErr := service.ScanAndRegister(context.Background(), trust, now)
	existingSeen := false
	var existingErr error
	if scanErr == nil {
		existingErr = inbox.Scan(context.Background(), trust, now, func(packageCtx context.Context, candidate InboxCandidate) error {
			stored, err := store.storeInboxCandidate(packageCtx, candidate)
			if err != nil {
				return err
			}
			if !stored.AlreadyPresent {
				return errors.New("existing bundle was not recognized")
			}
			existingSeen = true
			return nil
		})
	}
	close(done)
	peak := <-peakResult
	if scanErr != nil || existingErr != nil || !existingSeen || len(receipts) != 1 {
		t.Fatalf("large streaming scan receipts=%+v scan_err=%v existing_err=%v existing_seen=%v", receipts, scanErr, existingErr, existingSeen)
	}
	peakDelta := uint64(0)
	if peak > baseline.HeapAlloc {
		peakDelta = peak - baseline.HeapAlloc
	}
	t.Logf("320 MiB streaming peak HeapAlloc delta: %.2f MiB", float64(peakDelta)/(1<<20))
	if peakDelta >= heapBudget {
		t.Fatalf("streaming heap delta=%d bytes, budget must stay below %d", peakDelta, heapBudget)
	}
	if receipts[0].BundleSHA256 != bundleSHA256 {
		t.Fatalf("bundle digest=%s, want %s", receipts[0].BundleSHA256, bundleSHA256)
	}
	storedPath := filepath.Join(storeRoot, "bundles", receipts[0].BundleFingerprint, "models", "large.dat")
	if digest, err := streamingFileSHA256(storedPath); err != nil || digest != memberSHA256 {
		t.Fatalf("stored member digest=%s want=%s err=%v", digest, memberSHA256, err)
	}
}

func TestServiceRejectsUnknownCandidateAndRollsBackRejectedActivation(t *testing.T) {
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	storedBundles := []VerifiedBundle{
		verifiedBundleForStore(t, []BundleFilePayload{{Path: "data.dat", Mode: 0o444, Content: []byte("old-data")}}),
		verifiedBundleForStore(t, []BundleFilePayload{{Path: "data.dat", Mode: 0o444, Content: []byte("new-data")}}),
	}
	for _, bundle := range storedBundles {
		if _, err := store.StoreBundle(context.Background(), bundle); err != nil {
			t.Fatal(err)
		}
	}
	oldFingerprint := storedBundles[0].BundleFingerprint
	newFingerprint := storedBundles[1].BundleFingerprint
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("9", 32), NewFingerprint: oldFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if err := activator.Recover(context.Background(), oldFingerprint); err != nil {
		t.Fatal(err)
	}
	journal := candidateJournal{SchemaVersion: 1, Candidates: []candidateJournalEntry{{
		CandidateID: strings.Repeat("1", 32), BundleFingerprint: newFingerprint,
	}}}
	payload, _ := json.Marshal(journal)
	journalPath := filepath.Join(storeRoot, "candidate-journal.json")
	if err := os.WriteFile(journalPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	replayReceipt := protocolTestReceipt(time.Unix(1_800_000_000, 0).UTC())
	replayReceipt.BundleFingerprint = newFingerprint
	replayPayload, err := json.Marshal(candidateReplayState{SchemaVersion: 1, Receipts: []candidateReplayEntry{{
		CandidateID: strings.Repeat("1", 32), Receipt: replayReceipt,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeRoot, "candidate-receipts.json"), replayPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{
		candidateID: strings.Repeat("1", 32),
		directive: &ActivationDirective{
			SchemaVersion: 1, CandidateID: strings.Repeat("2", 32), ExpectedOldFingerprint: oldFingerprint,
			NewFingerprint: newFingerprint,
		},
		decision: ActivationReportResult{SchemaVersion: 1, Decision: ActivationDecisionRollback, ActiveFingerprint: oldFingerprint},
	}
	service, err := NewService(nil, store, activator, client, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PollAndActivate(context.Background()); !errors.Is(err, ErrActivationFailed) {
		t.Fatalf("unknown candidate error=%v", err)
	}
	client.directive.CandidateID = strings.Repeat("1", 32)
	if _, err := service.PollAndActivate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != oldFingerprint {
		t.Fatalf("rolled back active=%q error=%v", active, err)
	}
}

func TestServiceReplaysPrivateCandidateReceiptAfterCoreRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.Repeat("1", 32)
	firstClient := &serviceProtocolClient{candidateID: candidateID}
	service, err := NewService(inbox, store, activator, firstClient, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now); err != nil {
		t.Fatal(err)
	}

	restartedClient := &serviceProtocolClient{candidateID: candidateID}
	restarted, err := NewService(nil, store, activator, restartedClient, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.PollAndActivate(context.Background()); err != nil || retry != 5 {
		t.Fatalf("restart replay retry=%d err=%v", retry, err)
	}
	if restartedClient.registerCalls != 1 || len(restartedClient.registrations) != 1 ||
		len(restartedClient.registrations[0].Receipt.Capabilities) == 0 {
		t.Fatalf("restart registrations=%+v calls=%d", restartedClient.registrations, restartedClient.registerCalls)
	}
	journalPayload, err := os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journalPayload), "capabilities") || strings.Contains(string(journalPayload), "profiles") {
		t.Fatalf("handle journal contains replay metadata: %s", journalPayload)
	}
	replayInfo, err := os.Lstat(filepath.Join(storeRoot, "candidate-receipts.json"))
	if err != nil || !replayInfo.Mode().IsRegular() || replayInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private replay metadata info=%v err=%v", replayInfo, err)
	}
}

func TestServiceReportsCrashAfterPointerSwapBeforePullingAgain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.Repeat("1", 32)
	client := &serviceProtocolClient{candidateID: candidateID}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("scan receipts=%+v err=%v", receipts, err)
	}
	client.directive = &ActivationDirective{
		SchemaVersion: 1, CandidateID: candidateID, NewFingerprint: receipts[0].BundleFingerprint,
	}
	client.decision = ActivationReportResult{
		SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: receipts[0].BundleFingerprint,
	}
	activator.fault = func(stage activationStage) error {
		if stage == activationAfterSwap {
			return errors.New("simulated updater crash after pointer swap")
		}
		return nil
	}
	if _, err := service.PollAndActivate(context.Background()); !errors.Is(err, ErrActivationFailed) || client.reportCalls != 0 {
		t.Fatalf("post-swap crash err=%v reports=%d", err, client.reportCalls)
	}

	restartedActivator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restartedClient := &serviceProtocolClient{
		candidateID: candidateID, directive: client.directive, decision: client.decision, clearDirectiveAfterReport: true,
	}
	restarted, err := NewService(nil, store, restartedActivator, restartedClient, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.PollAndActivate(context.Background()); err != nil || retry != 5 || restartedClient.reportCalls != 1 {
		t.Fatalf("recovered poll retry=%d reports=%d err=%v", retry, restartedClient.reportCalls, err)
	}
	if active, err := restartedActivator.ActiveFingerprint(); err != nil || active != receipts[0].BundleFingerprint {
		t.Fatalf("recovered active=%q err=%v", active, err)
	}
}

type serviceProtocolClient struct {
	candidateID               string
	directive                 *ActivationDirective
	decision                  ActivationReportResult
	scanRequested             bool
	clearDirectiveAfterReport bool
	registerCalls             int
	reportCalls               int
	registrations             []RegisterCandidateRequest
	registerHook              func(int, RegisterCandidateRequest)
}

func (client *serviceProtocolClient) RegisterCandidate(_ context.Context, request RegisterCandidateRequest) (RegisterCandidateResult, error) {
	client.registerCalls++
	client.registrations = append(client.registrations, request)
	if client.registerHook != nil {
		client.registerHook(client.registerCalls, request)
	}
	return RegisterCandidateResult{SchemaVersion: 1, CandidateID: client.candidateID}, nil
}

func (client *serviceProtocolClient) PullActivation(context.Context, PullActivationRequest) (PullActivationResult, error) {
	scanRequested := client.scanRequested
	client.scanRequested = false
	return PullActivationResult{
		SchemaVersion: 1, RetryAfterSeconds: 5, ScanRequested: scanRequested, Directive: client.directive,
	}, nil
}

func (client *serviceProtocolClient) ReportActivation(context.Context, ActivationReportRequest) (ActivationReportResult, error) {
	client.reportCalls++
	if client.clearDirectiveAfterReport {
		client.directive = nil
	}
	return client.decision, nil
}

func writeInboxCandidatePayloads(t *testing.T, root, name string, manifest, bundle []byte) {
	t.Helper()
	candidate := filepath.Join(root, name)
	if err := os.Mkdir(candidate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "manifest.json"), manifest, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "bundle.tar"), bundle, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(candidate, 0o555); err != nil {
		t.Fatal(err)
	}
}

func writeLargeStreamingInboxCandidate(
	t *testing.T,
	root string,
	name string,
	now time.Time,
	contentSize int64,
) (ed25519.PublicKey, string, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("xirang-updater-320-mib-streaming-test-key"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	candidate := filepath.Join(root, name)
	if err := os.Mkdir(candidate, 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(candidate, "bundle.tar")
	bundleFile, err := os.OpenFile(bundlePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	bundleDigest := sha256.New()
	tarWriter := tar.NewWriter(io.MultiWriter(bundleFile, bundleDigest))
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "models/large.dat", Mode: 0o444, Size: contentSize, Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	}); err != nil {
		t.Fatal(err)
	}
	memberDigest := sha256.New()
	written, err := io.CopyN(io.MultiWriter(tarWriter, memberDigest), zeroReader{}, contentSize)
	if err != nil || written != contentSize {
		t.Fatalf("write large canonical member bytes=%d err=%v", written, err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundleFile.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := bundleFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundlePath, 0o444); err != nil {
		t.Fatal(err)
	}
	bundleSHA256 := hex.EncodeToString(bundleDigest.Sum(nil))
	memberSHA256 := hex.EncodeToString(memberDigest.Sum(nil))
	manifest := Manifest{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline.large", Version: "1.0.0",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Capabilities: []ManifestCapability{{
			Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"},
			ToolRevision: "tesseract-5", ModelRevision: "large-model-v1", DataRevision: "none",
		}},
		Files:        []ManifestFile{{Path: "models/large.dat", Mode: 0o444, Size: contentSize, SHA256: memberSHA256}},
		BundleSHA256: bundleSHA256, SigningKeyID: "key-2026", SignatureAlgorithm: "ed25519",
	}
	if err := SignManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "manifest.json"), manifestPayload, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(candidate, 0o555); err != nil {
		t.Fatal(err)
	}
	return append(ed25519.PublicKey(nil), publicKey...), bundleSHA256, memberSHA256
}

func streamingFileSHA256(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, copyErr := io.CopyBuffer(digest, handle, make([]byte, 64<<10))
	closeErr := handle.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type zeroReader struct{}

func (zeroReader) Read(payload []byte) (int, error) {
	clear(payload)
	return len(payload), nil
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (reader *trackingReadCloser) Close() error {
	reader.closed = true
	return nil
}
