package content

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/fileaccess"
)

func TestBrokerSafePreviewUsesActualProviderAdaptersThroughIssueGrantAndServe(t *testing.T) {
	payload := bytes.Repeat([]byte("synthetic=value\r\n"), 400)
	for _, kind := range []backupasset.ProviderKind{
		backupasset.ProviderRestic,
		backupasset.ProviderRsync,
		backupasset.ProviderRclone,
	} {
		t.Run(string(kind), func(t *testing.T) {
			harness := newBrokerTestHarness(t)
			resolver := newBrokerAdapterSourceResolver(t, kind, payload, *harness.asset)
			harness.asset.Provider = kind
			harness.asset.Size = int64(len(payload))
			harness.asset.MediaType = "application/octet-stream"
			harness.asset.Path = "/synthetic/config"
			harness.asset.Name = "config"
			resolver.asset = *harness.asset
			harness.broker.source = resolver
			config := testBrokerConfig()
			config.Renderer.TextBytes = 512
			config.Renderer.HexBytes = 512
			harness.broker.config = func(context.Context) (BrokerConfig, error) { return config, nil }

			request := harness.adminSafePreviewRequest()
			request.Proof = &StepUpProof{
				Action: auth.StepUpActionAssetSecretReveal, ID: strings.Repeat("e", 32), ExpiresAt: harness.now.Add(time.Minute),
			}
			ticket, err := harness.broker.Issue(context.Background(), request)
			if err != nil {
				t.Fatalf("Issue through %s adapter: %v", kind, err)
			}
			if ticket.Descriptor.Renderer != RendererPlainText || ticket.Descriptor.Profile != ProfileTextV2 ||
				!ticket.Descriptor.Truncated {
				t.Fatalf("%s descriptor=%+v", kind, ticket.Descriptor)
			}
			assertBrokerGrantCount(t, harness.db, 1)

			response := &brokerDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			if err := harness.broker.Serve(context.Background(), GatewayRequest{
				DeliveryID: harness.material.DeliveryID, Method: http.MethodGet,
				RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
			}, response); err != nil {
				t.Fatalf("Serve through %s adapter: %v", kind, err)
			}
			if response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), "synthetic=value\r\n") {
				t.Fatalf("%s response status=%d body-prefix=%q", kind, response.Code, response.Body.String()[:min(32, response.Body.Len())])
			}
			if resolver.openCalls != 2 || resolver.readCalls != 2 {
				t.Fatalf("%s opens=%d reads=%d, want one Issue and one Serve bounded source read", kind, resolver.openCalls, resolver.readCalls)
			}
			if kind != backupasset.ProviderRsync {
				for index, handle := range resolver.commandTransport.handles {
					if !handle.prefixClosed || handle.ordinaryClosed {
						t.Fatalf("%s handle[%d] prefix=%t ordinary=%t", kind, index, handle.prefixClosed, handle.ordinaryClosed)
					}
				}
			}
		})
	}
}

type brokerProviderAdapter interface {
	provider.EntryStatter
	provider.SequentialReader
}

type brokerAdapterSourceResolver struct {
	adapter          brokerProviderAdapter
	snapshot         provider.ReadSnapshot
	point            provider.PointLocator
	entry            provider.EntryLocator
	asset            AuthorizedAsset
	rangeProven      bool
	openCalls        int
	readCalls        int
	commandTransport *brokerAdapterCommandTransport
}

func (resolver *brokerAdapterSourceResolver) OpenContentSource(ctx context.Context, request SourceRequest) (SourceSession, error) {
	resolver.openCalls++
	if request.Ref != resolver.asset.Ref || request.CatalogGenerationID != resolver.asset.CatalogGenerationID ||
		request.ExpectedSource != resolver.asset.SourceFingerprint || request.ExpectedEntry != resolver.asset.EntryFingerprint ||
		request.Mode != SourceModeSequential {
		return nil, ErrInvalidSourceRequest
	}
	entry, err := resolver.adapter.StatEntry(ctx, resolver.snapshot, resolver.point, resolver.entry)
	if err != nil {
		return nil, err
	}
	handle, _, err := resolver.adapter.OpenSequential(ctx, resolver.snapshot, resolver.point, resolver.entry, provider.ReadRequest{MaxBytes: request.MaxBytes})
	if err != nil {
		return nil, err
	}
	reporter, ok := handle.(provider.ProviderByteReporter)
	if !ok {
		_ = handle.Close()
		return nil, ErrContentSourceUnavailable
	}
	resolver.readCalls++
	modified := entry.ModTime.UTC()
	return &brokerAdapterSourceSession{
		reader: &brokerAdapterSourceReader{ReadHandle: handle, reporter: reporter},
		stat: SourceStat{
			Size: resolver.asset.Size, ModifiedAt: &modified, MediaType: resolver.asset.MediaType,
			SourceFingerprint: resolver.asset.SourceFingerprint, EntryFingerprint: resolver.asset.EntryFingerprint,
			FingerprintStrong: true,
		},
		capabilities: SourceCapabilities{Provider: resolver.asset.Provider, Sequential: true, Range: resolver.rangeProven},
	}, nil
}

func (*brokerAdapterSourceResolver) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type brokerAdapterSourceSession struct {
	reader       *brokerAdapterSourceReader
	stat         SourceStat
	capabilities SourceCapabilities
}

func (session *brokerAdapterSourceSession) Stat() SourceStat { return session.stat }
func (session *brokerAdapterSourceSession) Capabilities() SourceCapabilities {
	return session.capabilities
}
func (session *brokerAdapterSourceSession) Reader() SourceReader     { return session.reader }
func (*brokerAdapterSourceSession) Revalidate(context.Context) error { return nil }
func (session *brokerAdapterSourceSession) Close() error             { return session.reader.Close() }
func (session *brokerAdapterSourceSession) ClosePrefix() error {
	prefixCloser, ok := session.reader.ReadHandle.(interface{ ClosePrefix() error })
	if !ok {
		return session.reader.Close()
	}
	return prefixCloser.ClosePrefix()
}

type brokerAdapterSourceReader struct {
	provider.ReadHandle
	reporter provider.ProviderByteReporter
}

func (reader *brokerAdapterSourceReader) ProviderBytes() int64 {
	return reader.reporter.ProviderBytes()
}

type brokerAdapterCommandTransport struct {
	runOutputs map[provider.CommandOperation]provider.CommandOutput
	payload    []byte
	handles    []*brokerAdapterCommandHandle
}

func (transport *brokerAdapterCommandTransport) Run(_ context.Context, invocation provider.CommandInvocation, _ provider.OperationLimits) (provider.CommandOutput, error) {
	output, ok := transport.runOutputs[invocation.Operation]
	if !ok {
		return provider.CommandOutput{}, fmt.Errorf("unexpected synthetic provider operation")
	}
	return output, nil
}

func (transport *brokerAdapterCommandTransport) Open(_ context.Context, _ provider.CommandInvocation, _ provider.OperationLimits, _ int64) (provider.ReadHandle, error) {
	handle := &brokerAdapterCommandHandle{Reader: bytes.NewReader(transport.payload)}
	transport.handles = append(transport.handles, handle)
	return handle, nil
}

type brokerAdapterCommandHandle struct {
	*bytes.Reader
	prefixClosed   bool
	ordinaryClosed bool
}

func (handle *brokerAdapterCommandHandle) Close() error {
	handle.ordinaryClosed = true
	if handle.Len() > 0 {
		return errors.New("FAKE_EXPECTED_EARLY_COMMAND_ABORT_FOR_TEST_ONLY")
	}
	return nil
}

func (handle *brokerAdapterCommandHandle) ClosePrefix() error {
	handle.prefixClosed = true
	return nil
}

type brokerAdapterCursorKeys struct{ material backupasset.DomainKeyMaterial }

func (keys brokerAdapterCursorKeys) Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	return keys.material, nil
}

func (keys brokerAdapterCursorKeys) ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error) {
	return keys.material, nil
}

func newBrokerAdapterSourceResolver(t *testing.T, kind backupasset.ProviderKind, payload []byte, asset AuthorizedAsset) *brokerAdapterSourceResolver {
	t.Helper()
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	limits := provider.OperationLimits{Timeout: time.Minute, MaxMetadataBytes: 1 << 20, MaxStderrBytes: 64 << 10, MaxRecordBytes: 64 << 10, MaxItems: 1000}
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	cursors := provider.NewCursorCodec(brokerAdapterCursorKeys{material: material}, func() time.Time { return now }, time.Hour)
	modified := now.Add(-time.Minute).Format(time.RFC3339)
	resolver := &brokerAdapterSourceResolver{asset: asset}

	switch kind {
	case backupasset.ProviderRestic:
		snapshotID := strings.Repeat("e", 64)
		listing := fmt.Sprintf("{\"struct_type\":\"snapshot\",\"id\":%q}\n{\"struct_type\":\"node\",\"name\":\"config\",\"path\":\"/synthetic/config\",\"type\":\"file\",\"size\":%d,\"mtime\":%q}\n", snapshotID, len(payload), modified)
		transport := &brokerAdapterCommandTransport{
			runOutputs: map[provider.CommandOperation]provider.CommandOutput{provider.OperationResticList: {Stdout: []byte(listing)}},
			payload:    payload,
		}
		adapter, err := provider.NewResticAdapter(transport, cursors, limits, 100, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		binding := provider.AccessBinding{
			Provider: kind, RepositoryID: strings.Repeat("1", 32), Locator: "FAKE_SYNTHETIC_RESTIC_LOCATOR_FOR_TEST_ONLY",
			Secret:      []byte("FAKE_SYNTHETIC_RESTIC_SECRET_FOR_TEST_ONLY"),
			AdapterData: provider.ResticRuntimeAccess{NativeRepositoryID: strings.Repeat("a", 64)},
		}
		resolver.adapter = adapter
		resolver.snapshot = provider.ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: asset.SourceFingerprint, Access: binding}
		resolver.point = provider.PointLocator{Native: snapshotID}
		resolver.entry = provider.EntryLocator{Native: "/synthetic/config"}
		resolver.commandTransport = transport
		return resolver
	case backupasset.ProviderRsync:
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "synthetic"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "synthetic", "config"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
		adapter, err := provider.NewRsyncAdapter(cursors, limits, 100, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		binding := provider.AccessBinding{
			Provider: kind, RepositoryID: strings.Repeat("2", 32), TaskID: 7, NodeID: 9,
			IdentitySalt: bytes.Repeat([]byte{0x42}, provider.IdentitySaltBytes), EndpointFacts: []string{"synthetic", "filesystem"},
			AdapterData: provider.RsyncRuntimeAccess{Tree: fileaccess.NewLocalTree(), Root: fileaccess.Root{Path: root}},
		}
		observation, err := adapter.Probe(context.Background(), binding, limits)
		if err != nil {
			t.Fatal(err)
		}
		runtimeAccess := binding.AdapterData.(provider.RsyncRuntimeAccess)
		runtimeAccess.RangeProven = observation.Capabilities.OpenRange
		binding.AdapterData = runtimeAccess
		snapshot := provider.ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: observation.SourceRevision, Access: binding}
		points, err := adapter.ListPoints(context.Background(), snapshot, provider.PageRequest{Limit: 1})
		if err != nil || len(points.Items) != 1 {
			t.Fatalf("synthetic Rsync point page=%+v err=%v", points, err)
		}
		resolver.adapter, resolver.snapshot, resolver.point = adapter, snapshot, points.Items[0].Locator
		resolver.entry = provider.EntryLocator{Native: "synthetic/config"}
		resolver.rangeProven = observation.Capabilities.OpenRange
		return resolver
	case backupasset.ProviderRclone:
		listJSON := fmt.Sprintf(`[{"Path":"synthetic/config","Name":"config","Size":%d,"ModTime":%q,"IsDir":false}]`, len(payload), modified)
		statJSON := fmt.Sprintf(`{"Path":"synthetic/config","Name":"config","Size":%d,"MimeType":"application/octet-stream","ModTime":%q,"IsDir":false}`, len(payload), modified)
		transport := &brokerAdapterCommandTransport{
			runOutputs: map[provider.CommandOperation]provider.CommandOutput{
				provider.OperationRcloneVersion:  {Stdout: []byte("rclone v1.70.0")},
				provider.OperationRcloneFeatures: {Stdout: []byte(`{"Name":"s3","Features":{}}`)},
				provider.OperationRcloneList:     {Stdout: []byte(listJSON)},
				provider.OperationRcloneStat:     {Stdout: []byte(statJSON)},
			},
			payload: payload,
		}
		adapter, err := provider.NewRcloneAdapter(transport, cursors, limits, 100, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		binding := provider.AccessBinding{
			Provider: kind, RepositoryID: strings.Repeat("3", 32), TaskID: 7, NodeID: 9,
			IdentitySalt: bytes.Repeat([]byte{0x43}, provider.IdentitySaltBytes), EndpointFacts: []string{"synthetic", "remote"},
			Locator: "synthetic-remote:root", Secret: []byte("FAKE_SYNTHETIC_RCLONE_CONFIG_FOR_TEST_ONLY"),
		}
		observation, err := adapter.Probe(context.Background(), binding, limits)
		if err != nil {
			t.Fatal(err)
		}
		binding.AdapterData = provider.RcloneRuntimeAccess{Backend: "s3", RangeProven: observation.Capabilities.OpenRange}
		snapshot := provider.ReadSnapshot{RepositoryID: binding.RepositoryID, CapabilityRevision: 1, SourceRevision: observation.SourceRevision, Access: binding}
		points, err := adapter.ListPoints(context.Background(), snapshot, provider.PageRequest{Limit: 1})
		if err != nil || len(points.Items) != 1 {
			t.Fatalf("synthetic Rclone point page=%+v err=%v", points, err)
		}
		resolver.adapter, resolver.snapshot, resolver.point = adapter, snapshot, points.Items[0].Locator
		resolver.entry = provider.EntryLocator{Native: "synthetic/config"}
		resolver.rangeProven = observation.Capabilities.OpenRange
		resolver.commandTransport = transport
		return resolver
	default:
		t.Fatalf("unsupported synthetic provider %q", kind)
		return nil
	}
}
