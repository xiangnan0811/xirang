package processing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkerInputActivationV2AndBoundedServerLimit(t *testing.T) {
	const localCap = int64(64 << 10)
	var mu sync.Mutex
	var schema int
	var serverCap int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if schema == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"schema_version":1,"code":"ok","data":{"schema_version":1,"session_id":"`+strings.Repeat("a", 32)+`","transition_revision":2,"expires_at":"2099-01-01T00:00:00Z","source":{"size":3,"media_type":"","fingerprint_strong":false,"sequential":true,"range":false}}}`)
			return
		}
		writeWorkerClientTestResponse(t, w, WorkerInputActivation{
			SchemaVersion: schema, SessionID: strings.Repeat("a", 32), TransitionRevision: 2,
			ExpiresAt: time.Now().UTC().Add(time.Minute), Source: WorkerInputSourceInfo{Size: 3, Sequential: true, MaxBytesPerRequest: serverCap},
		})
	}))
	defer server.Close()
	client, err := newWorkerClient(server.Client(), server.URL, 64<<10, localCap)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerInputActivateRequest{JobID: strings.Repeat("b", 32)}
	for _, tc := range []struct {
		schema  int
		cap     int64
		want    int64
		invalid bool
	}{
		{2, localCap * 2, localCap, false}, {2, localCap / 2, localCap / 2, false},
		{1, localCap, 0, true}, {2, 0, 0, true}, {2, -1, 0, true},
	} {
		mu.Lock()
		schema, serverCap = tc.schema, tc.cap
		mu.Unlock()
		got, err := client.ActivateInput(context.Background(), request)
		if tc.invalid {
			if !errors.Is(err, ErrProtocolInvalid) {
				t.Fatalf("schema=%d cap=%d: %v", schema, serverCap, err)
			}
		} else if err != nil || got.Source.MaxBytesPerRequest != tc.want {
			t.Fatalf("schema=%d cap=%d got=%+v err=%v", schema, serverCap, got, err)
		}
	}
	// A worker built against the prior DTO must reject the extra source field.
	encoded, err := json.Marshal(WorkerInputActivation{SchemaVersion: 2, Source: WorkerInputSourceInfo{MaxBytesPerRequest: 64}})
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		SchemaVersion      int       `json:"schema_version"`
		SessionID          string    `json:"session_id"`
		TransitionRevision int64     `json:"transition_revision"`
		ExpiresAt          time.Time `json:"expires_at"`
		Source             struct {
			Size              int64  `json:"size"`
			MediaType         string `json:"media_type"`
			FingerprintStrong bool   `json:"fingerprint_strong"`
			Sequential        bool   `json:"sequential"`
			Range             bool   `json:"range"`
		} `json:"source"`
	}
	if err := DecodeWorkerJSON(encoded, &legacy); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("old activation DTO accepted v2: %v", err)
	}
}

type inputReadRoundTripper func(*http.Request) (*http.Response, error)

func (fn inputReadRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type trackedInputBody struct {
	reader   io.Reader
	closeErr error
	closes   int
}

func (body *trackedInputBody) Read(p []byte) (int, error) { return body.reader.Read(p) }
func (body *trackedInputBody) Close() error               { body.closes++; return body.closeErr }

type failedInputBody struct{}

func (failedInputBody) Read([]byte) (int, error) { return 0, errors.New("private read error") }

func TestWorkerInputReadRejectsTerminalFailuresAndClosesOnce(t *testing.T) {
	for _, tc := range []struct {
		name             string
		bytes            string
		trailer          []string
		readFailure      bool
		closeFailure     bool
		status           int
		transportFailure bool
	}{
		{"ok", "abc", []string{"ok"}, false, false, 200, false},
		{"missing", "abc", nil, false, false, 200, false},
		{"failed", "abc", []string{"failed"}, false, false, 200, false},
		{"multiple", "abc", []string{"ok", "ok"}, false, false, 200, false},
		{"overlong", "abcd", []string{"ok"}, false, false, 200, false},
		{"read_error", "", []string{"ok"}, true, false, 200, false},
		{"close_error", "abc", []string{"ok"}, false, true, 200, false},
		{"transport_error", "", nil, false, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedInputBody{reader: bytes.NewReader([]byte(tc.bytes))}
			if tc.readFailure {
				body.reader = failedInputBody{}
			}
			if tc.closeFailure {
				body.closeErr = errors.New("private close error")
			}
			calls := 0
			httpClient := &http.Client{Transport: inputReadRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if tc.transportFailure {
					return nil, errors.New("private transport error")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/octet-stream"}, "Cache-Control": {"no-store"}}, Trailer: http.Header{"X-Xirang-Input-Status": tc.trailer}, Body: body}, nil
			})}
			client, err := newWorkerClient(httpClient, "http://worker.test", 64<<10, math.MaxInt64)
			if err != nil {
				t.Fatal(err)
			}
			request := WorkerInputReadRequest{SessionID: strings.Repeat("a", 32), Mode: "range", Length: 3}
			got, err := client.ReadInput(context.Background(), request)
			if tc.name == "ok" {
				if err != nil || string(got) != "abc" {
					t.Fatalf("got=%q err=%v", got, err)
				}
			} else if !errors.Is(err, ErrProtocolInvalid) || len(got) != 0 {
				t.Fatalf("got=%q err=%v", got, err)
			}
			if !tc.transportFailure && body.closes != 1 {
				t.Fatalf("body closed %d times", body.closes)
			}
			request.Length = math.MaxInt64
			if _, err := client.ReadInput(context.Background(), request); !errors.Is(err, ErrProtocolInvalid) || calls != 1 {
				t.Fatalf("overflow err=%v calls=%d", err, calls)
			}
		})
	}
}
