package updater

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpdaterClientUsesOnlyClosedJSONRoutesAndValidatesResults(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	requests := make([]string, 0, 3)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected request metadata: %s %s %v", request.Method, request.URL, request.Header)
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"manifest.json", "bundle.tar", "bundle_bytes", "path", "credential", "signature\""} {
			if strings.Contains(strings.ToLower(string(payload)), forbidden) {
				t.Fatalf("request leaked %q: %s", forbidden, payload)
			}
		}
		requests = append(requests, request.URL.Path)
		var result any
		switch request.URL.Path {
		case "/internal/v1/asset-worker-updater/candidates":
			result = RegisterCandidateResult{SchemaVersion: 1, CandidateID: strings.Repeat("1", 32)}
		case "/internal/v1/asset-worker-updater/activations/pull":
			result = PullActivationResult{SchemaVersion: 1, RetryAfterSeconds: 5, Directive: &ActivationDirective{
				SchemaVersion: 1, CandidateID: strings.Repeat("1", 32), ExpectedOldFingerprint: strings.Repeat("a", 64),
				NewFingerprint: strings.Repeat("b", 64),
			}}
		case "/internal/v1/asset-worker-updater/activations/report":
			result = ActivationReportResult{SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: strings.Repeat("b", 64)}
		default:
			t.Fatalf("open-ended updater route: %s", request.URL.Path)
		}
		return updaterTestResponse(http.StatusOK, "ok", result), nil
	})
	client, err := newUpdaterClient(&http.Client{Transport: transport}, "http://asset-worker-updater.local", 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := client.RegisterCandidate(context.Background(), RegisterCandidateRequest{
		SchemaVersion: 1, Receipt: protocolTestReceipt(now),
	})
	if err != nil || registration.CandidateID != strings.Repeat("1", 32) {
		t.Fatalf("registration=%+v err=%v", registration, err)
	}
	pull, err := client.PullActivation(context.Background(), PullActivationRequest{
		SchemaVersion: 1, ActiveFingerprint: strings.Repeat("a", 64),
	})
	if err != nil || pull.Directive == nil || pull.Directive.NewFingerprint != strings.Repeat("b", 64) {
		t.Fatalf("pull=%+v err=%v", pull, err)
	}
	report, err := client.ReportActivation(context.Background(), ActivationReportRequest{SchemaVersion: 1, Receipt: ActivationReceipt{
		SchemaVersion: 1, CandidateID: strings.Repeat("1", 32), OldFingerprint: strings.Repeat("a", 64),
		NewFingerprint: strings.Repeat("b", 64), State: "swapped",
	}})
	if err != nil || report.Decision != ActivationDecisionCommit {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := []string{
		"/internal/v1/asset-worker-updater/candidates",
		"/internal/v1/asset-worker-updater/activations/pull",
		"/internal/v1/asset-worker-updater/activations/report",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes=%v want=%v", requests, want)
	}
}

func TestUpdaterClientRejectsUnsafeConfigAndMalformedResponses(t *testing.T) {
	for _, config := range []UpdaterClientConfig{
		{},
		{SocketPath: "relative.sock"},
		{SocketPath: "/tmp/updater.sock", RequestTimeout: -time.Second},
		{SocketPath: "/tmp/updater.sock", JSONMaxBytes: 128 << 10},
	} {
		if _, err := NewUpdaterClient(config); !errors.Is(err, ErrTransportUnsafe) {
			t.Fatalf("unsafe config=%+v error=%v", config, err)
		}
	}

	responses := []*http.Response{
		updaterTestResponse(http.StatusOK, "ok", RegisterCandidateResult{SchemaVersion: 1, CandidateID: "public-name"}),
		updaterTestResponse(http.StatusBadRequest, "invalid_request", nil),
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("not json"))},
	}
	for index, response := range responses {
		client, err := newUpdaterClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response, nil
		})}, "http://asset-worker-updater.local", 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.RegisterCandidate(context.Background(), RegisterCandidateRequest{
			SchemaVersion: 1, Receipt: protocolTestReceipt(time.Now().UTC()),
		})
		if index == 1 {
			if !errors.Is(err, ErrProtocolInvalid) {
				t.Fatalf("mapped response %d error=%v", index, err)
			}
		} else if !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("malformed response %d error=%v", index, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func updaterTestResponse(status int, code string, data any) *http.Response {
	payload, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Code          string `json:"code"`
		Data          any    `json:"data,omitempty"`
	}{SchemaVersion: 1, Code: code, Data: data})
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(payload))),
	}
}
