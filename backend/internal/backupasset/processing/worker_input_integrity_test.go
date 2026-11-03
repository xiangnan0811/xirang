package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
)

const testABCDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
const testEmptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type terminalInputReader struct {
	payload []byte
	err     error
}

func (reader *terminalInputReader) Read(p []byte) (int, error) {
	if len(reader.payload) == 0 {
		return 0, reader.err
	}
	n := copy(p, reader.payload)
	reader.payload = reader.payload[n:]
	if len(reader.payload) == 0 {
		return n, reader.err
	}
	return n, nil
}

func TestMalwareVerifiedInput(t *testing.T) {
	sourceErr := errors.New("source failed")
	cases := []struct {
		name    string
		input   io.Reader
		size    int64
		digest  string
		want    string
		wantErr error
	}{
		{"matching", bytes.NewReader([]byte("abc")), 3, testABCDigest, "abc", nil},
		{"n_and_eof", &terminalInputReader{payload: []byte("abc"), err: io.EOF}, 3, testABCDigest, "abc", nil},
		{"empty", bytes.NewReader(nil), 0, testEmptyDigest, "", nil},
		{"same_size_different_content", bytes.NewReader([]byte("abd")), 3, testABCDigest, "", ErrProtocolInvalid},
		{"n_and_eof_digest_failure", &terminalInputReader{payload: []byte("abd"), err: io.EOF}, 3, testABCDigest, "", ErrProtocolInvalid},
		{"short", bytes.NewReader([]byte("ab")), 3, testABCDigest, "", io.ErrUnexpectedEOF},
		{"overlong", bytes.NewReader([]byte("abcd")), 3, testABCDigest, "", ErrProtocolInvalid},
		{"source_error", &terminalInputReader{payload: []byte("ab"), err: sourceErr}, 3, testABCDigest, "", sourceErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := newMalwareVerifiedInput(tc.input, tc.size, tc.digest)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := reader.Read(nil); n != 0 || err != nil {
				t.Fatalf("zero read = %d, %v", n, err)
			}
			got, err := io.ReadAll(reader)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && string(got) != tc.want) {
				t.Fatalf("read=%q err=%v, want %q / %v", got, err, tc.want, tc.wantErr)
			}
			if tc.wantErr != nil {
				if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, tc.wantErr) {
					t.Fatalf("failure not sticky: n=%d err=%v", n, err)
				}
			}
		})
	}
	for _, tc := range []struct {
		source io.Reader
		size   int64
		digest string
	}{
		{nil, 0, testEmptyDigest}, {bytes.NewReader(nil), -1, testEmptyDigest},
		{bytes.NewReader(nil), 0, strings.ToUpper(testEmptyDigest)}, {bytes.NewReader(nil), 0, "short"},
	} {
		if _, err := newMalwareVerifiedInput(tc.source, tc.size, tc.digest); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("invalid verifier parameters accepted: %v", err)
		}
	}
}

type inputHTTPTrace struct {
	mu       sync.Mutex
	requests []WorkerInputReadRequest
}

// This exercises the real WorkerClient and production reader over HTTP. The
// router/AttemptBroker boundary has its own API-package integration test.
func TestProductionCapabilityInputExactBytes(t *testing.T) {
	const capBytes = int64(64 << 10)
	large := make([]byte, 2<<20+17)
	for i := range large {
		large[i] = byte((i/65536*37 + i*13 + i/257) % 251)
	}
	for _, tc := range []struct {
		name        string
		data        []byte
		rangeMode   bool
		fault       string
		maxRequests int
		wantSuccess bool
	}{
		{"large_ranges", large, true, "", 40, true},
		{"malware_exact", large[:capBytes+7], true, "", 4, true},
		{"sequential_fits", large[:capBytes], false, "", 1, true},
		{"sequential_too_large", large[:capBytes+1], false, "", 1, false},
		{"failed_final_close", large, true, "failed_close", 40, false},
		{"missing_trailer", large, true, "missing_trailer", 40, false},
		{"short_final_response", large, true, "short", 40, false},
		{"same_size_changed_content", large, true, "changed", 40, false},
		{"request_budget_exhausted", large, true, "", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := &inputHTTPTrace{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request WorkerInputReadRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode: %v", err)
					w.WriteHeader(400)
					return
				}
				trace.mu.Lock()
				trace.requests = append(trace.requests, request)
				count := len(trace.requests)
				trace.mu.Unlock()
				if count > tc.maxRequests {
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, `{"schema_version":1,"code":"conflict"}`)
					return
				}
				start := request.Offset
				if request.Mode == "sequential" {
					start = 0
				}
				end := start + request.Length
				if start < 0 || request.Length <= 0 || request.Length > capBytes || end > int64(len(tc.data)) {
					t.Errorf("invalid range %+v", request)
					w.WriteHeader(400)
					return
				}
				payload := append([]byte(nil), tc.data[start:end]...)
				last := end == int64(len(tc.data))
				if last && tc.fault == "changed" {
					payload[len(payload)-1] ^= 1
				}
				if last && tc.fault == "short" {
					payload = payload[:len(payload)-1]
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Cache-Control", "no-store")
				if !last || tc.fault != "missing_trailer" {
					w.Header().Add("Trailer", "X-Xirang-Input-Status")
				}
				_, _ = w.Write(payload)
				if last && tc.fault == "failed_close" {
					w.Header().Set("X-Xirang-Input-Status", "failed")
				} else if !last || tc.fault != "missing_trailer" {
					w.Header().Set("X-Xirang-Input-Status", "ok")
				}
			}))
			defer server.Close()
			client, err := newWorkerClient(server.Client(), server.URL, 64<<10, capBytes)
			if err != nil {
				t.Fatal(err)
			}
			info := WorkerInputSourceInfo{Size: int64(len(tc.data)), MediaType: "application/octet-stream", FingerprintStrong: true, Sequential: true, Range: tc.rangeMode, MaxBytesPerRequest: capBytes}
			input := &workerCapabilityInput{client: client, workerID: strings.Repeat("1", 32), instanceID: strings.Repeat("2", 32), sessionID: strings.Repeat("3", 32), info: info}
			digest := sha256.Sum256(tc.data)
			if tc.name == "large_ranges" || tc.name == "sequential_fits" {
				reader, err := newProductionCapabilityInputReader(context.Background(), input, 1<<30)
				if err != nil {
					t.Fatal(err)
				}
				verified, err := newMalwareVerifiedInput(reader, int64(len(tc.data)), fmt.Sprintf("%x", digest))
				if err != nil {
					t.Fatal(err)
				}
				var got bytes.Buffer
				buffer := make([]byte, 13)
				for {
					n, readErr := verified.Read(buffer)
					_, _ = got.Write(buffer[:n])
					if readErr == io.EOF {
						break
					}
					if readErr != nil {
						t.Fatal(readErr)
					}
				}
				if !bytes.Equal(got.Bytes(), tc.data) {
					t.Fatal("input bytes changed")
				}
			} else {
				runner := &productionToolRunnerFake{}
				capability := productionMalwareCapabilityForTest(t, runner)
				artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
					Parameters: validWorkDescriptor().Parameters, EntryFingerprint: fmt.Sprintf("%x", digest), Input: input,
				})
				if tc.wantSuccess {
					if err != nil || len(artifacts) != 1 || !bytes.Equal(runner.input, tc.data) {
						t.Fatalf("verified input artifacts=%d err=%v", len(artifacts), err)
					}
				} else if err == nil || len(artifacts) != 0 {
					t.Fatalf("failure returned artifacts=%v err=%v", len(artifacts), err)
				}
				if tc.name == "sequential_too_large" && (!errors.Is(err, workerCapabilities.ErrInputLimit) || runner.runCalls != 0) {
					t.Fatalf("large sequential input reached tool: %v", err)
				}
			}
			trace.mu.Lock()
			defer trace.mu.Unlock()
			if tc.name == "large_ranges" {
				if len(trace.requests) != 33 {
					t.Fatalf("requests=%d, want 33", len(trace.requests))
				}
				for i, request := range trace.requests {
					if request.Mode != "range" || request.Offset != int64(i)*capBytes || request.Length != min(capBytes, int64(len(tc.data))-request.Offset) {
						t.Fatalf("range %d = %+v", i, request)
					}
				}
			}
			if tc.name == "sequential_fits" && (len(trace.requests) != 1 || trace.requests[0].Mode != "sequential" || trace.requests[0].Offset != 0) {
				t.Fatalf("sequential requests=%+v", trace.requests)
			}
			if tc.name == "sequential_too_large" && len(trace.requests) != 0 {
				t.Fatalf("oversize sequential requests=%+v", trace.requests)
			}
		})
	}
}

type prefixBudgetInput struct {
	info    WorkerInputSourceInfo
	lengths []int64
}

func (input *prefixBudgetInput) Info() WorkerInputSourceInfo { return input.info }
func (*prefixBudgetInput) ReadSequential(context.Context, int64) ([]byte, error) {
	return nil, errors.New("unexpected sequential read")
}
func (input *prefixBudgetInput) ReadRange(_ context.Context, _ int64, length int64) ([]byte, error) {
	input.lengths = append(input.lengths, length)
	return make([]byte, length), nil
}

func TestProductionCapabilityInputEmptyCancellationAndPrefixBudget(t *testing.T) {
	t.Run("empty_strong", func(t *testing.T) {
		input := &workerCapabilityMemoryInput{info: WorkerInputSourceInfo{Size: 0, MediaType: "application/octet-stream", FingerprintStrong: true, Sequential: true, MaxBytesPerRequest: 64}}
		runner := &productionToolRunnerFake{}
		capability := productionMalwareCapabilityForTest(t, runner)
		artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{Parameters: validWorkDescriptor().Parameters, EntryFingerprint: testEmptyDigest, Input: input})
		if err != nil || len(artifacts) != 1 || input.sequentialCalls != 0 {
			t.Fatalf("empty artifacts=%d calls=%d err=%v", len(artifacts), input.sequentialCalls, err)
		}
	})
	t.Run("invalid_strong_digest", func(t *testing.T) {
		input := &workerCapabilityMemoryInput{info: WorkerInputSourceInfo{Size: 0, MediaType: "application/octet-stream", FingerprintStrong: true, Sequential: true, MaxBytesPerRequest: 64}}
		runner := &productionToolRunnerFake{}
		artifacts, err := productionMalwareCapabilityForTest(t, runner).Execute(context.Background(), WorkerCapabilityJob{Parameters: validWorkDescriptor().Parameters, EntryFingerprint: strings.ToUpper(testEmptyDigest), Input: input})
		if !errors.Is(err, ErrProtocolInvalid) || len(artifacts) != 0 || runner.runCalls != 0 || input.sequentialCalls != 0 {
			t.Fatalf("bad digest artifacts=%d calls=%d err=%v", len(artifacts), runner.runCalls, err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		input := &workerCapabilityMemoryInput{content: []byte("abc"), info: WorkerInputSourceInfo{Size: 3, Sequential: true, MaxBytesPerRequest: 64}}
		ctx, cancel := context.WithCancel(context.Background())
		reader, err := newProductionCapabilityInputReader(ctx, input, 64)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if n, err := reader.Read(nil); n != 0 || err != nil {
			t.Fatalf("zero read n=%d err=%v", n, err)
		}
		if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, context.Canceled) || input.sequentialCalls != 0 {
			t.Fatalf("canceled n=%d err=%v", n, err)
		}
	})
	for _, name := range []string{"media.probe", "media.transcode"} {
		t.Run(name+"_prefix", func(t *testing.T) {
			input := &prefixBudgetInput{info: WorkerInputSourceInfo{Size: 100, MediaType: "video/mp4", Range: true, MaxBytesPerRequest: 4}}
			capability := productionCapabilityWithRunnerForTest(t, name, &productionToolRunnerFake{}).(productionWorkerCapability)
			_ = capability.preflightExternalInput(context.Background(), input)
			if len(input.lengths) != 1 || input.lengths[0] != 4 {
				t.Fatalf("prefix requests=%v", input.lengths)
			}
		})
	}
}
