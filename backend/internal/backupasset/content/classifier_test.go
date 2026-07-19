package content

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestClassificationPathNameAndContentSecretSignalsFailClosed(t *testing.T) {
	classifier, err := NewClassifier(ClassificationConfig{ScanBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		request ClassificationRequest
		content string
	}{
		{name: "private key path", request: classificationRequest("/home/app/.ssh/id_rsa", "id_rsa", 5), content: "hello"},
		{name: "dotenv name", request: classificationRequest("config/.env.production", ".env.production", 5), content: "hello"},
		{name: "kube config", request: classificationRequest("/home/app/.kube/config", "config", 5), content: "hello"},
		{name: "pem private key", request: classificationRequest("notes.txt", "notes.txt", 64), content: "-----BEGIN PRIVATE KEY-----\nFAKE_KEY_FOR_TEST_ONLY"},
		{name: "password assignment", request: classificationRequest("app.conf", "app.conf", 32), content: "password = FAKE_PASSWORD_FOR_TEST_ONLY"},
		{name: "api token assignment", request: classificationRequest("app.conf", "app.conf", 32), content: "api_token: FAKE_TOKEN_FOR_TEST_ONLY"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.request.SourceSize = int64(len(testCase.content))
			result, err := classifier.Classify(context.Background(), testCase.request, strings.NewReader(testCase.content))
			if err != nil || result.Classification != ClassificationSecret || result.PolicyRevision != 1 {
				t.Fatalf("classification=%+v err=%v", result, err)
			}
		})
	}
}

func TestClassificationProducesNonSecretOnlyForCompleteClosedText(t *testing.T) {
	classifier, err := NewClassifier(ClassificationConfig{ScanBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("ordinary bounded log line\n")
	request := classificationRequest("logs/app.log", "app.log", int64(len(plain)))
	result, err := classifier.Classify(context.Background(), request, bytes.NewReader(plain))
	if err != nil || result.Classification != ClassificationNonSecret || result.BytesScanned != int64(len(plain)) ||
		result.DetectedMediaType != "text/plain; charset=utf-8" {
		t.Fatalf("plain classification=%+v err=%v", result, err)
	}

	for _, testCase := range []struct {
		name    string
		request ClassificationRequest
		reader  io.Reader
	}{
		{name: "truncated", request: classificationRequest("large.log", "large.log", 64), reader: strings.NewReader(strings.Repeat("a", 64))},
		{name: "binary unknown", request: classificationRequest("blob.bin", "blob.bin", 4), reader: bytes.NewReader([]byte{0, 1, 2, 3})},
		{name: "invalid utf16", request: classificationRequest("bad.txt", "bad.txt", 3), reader: bytes.NewReader([]byte{0xff, 0xfe, 0x00})},
		{name: "read error", request: classificationRequest("error.txt", "error.txt", 10), reader: classificationFailReader{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, classifyErr := classifier.Classify(context.Background(), testCase.request, testCase.reader)
			if classifyErr != nil || got.Classification != ClassificationUnknown {
				t.Fatalf("classification=%+v err=%v", got, classifyErr)
			}
		})
	}
}

func TestClassificationScanIsBoundedAndCancellationBecomesUnknown(t *testing.T) {
	classifier, err := NewClassifier(ClassificationConfig{ScanBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	reader := &classificationCountingReader{reader: strings.NewReader(strings.Repeat("a", 1_000))}
	result, err := classifier.Classify(context.Background(), classificationRequest("large.txt", "large.txt", 1_000), reader)
	if err != nil || result.Classification != ClassificationUnknown || reader.bytes > 17 {
		t.Fatalf("bounded scan result=%+v bytes=%d err=%v", result, reader.bytes, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = classifier.Classify(ctx, classificationRequest("canceled.txt", "canceled.txt", 1), strings.NewReader("a"))
	if err != nil || result.Classification != ClassificationUnknown || result.Reason != ClassificationReasonScanUnavailable {
		t.Fatalf("canceled classification=%+v err=%v", result, err)
	}
}

func TestClassificationSearchEvidenceCanOnlyElevateExactGeneration(t *testing.T) {
	classifier, err := NewClassifier(ClassificationConfig{ScanBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	request := classificationRequest("safe.txt", "safe.txt", 4)
	request.CatalogGenerationID = strings.Repeat("a", 32)
	request.SourceFingerprint = "source-v1"
	request.Search = &SearchClassificationEvidence{
		Classification: ClassificationSecret, CatalogGenerationID: request.CatalogGenerationID,
		SourceFingerprint: request.SourceFingerprint, Revision: 2,
	}
	result, err := classifier.Classify(context.Background(), request, strings.NewReader("safe"))
	if err != nil || result.Classification != ClassificationSecret || result.SourceRevision != 2 {
		t.Fatalf("exact Search elevation=%+v err=%v", result, err)
	}

	request.Search.Classification = ClassificationNonSecret
	result, err = classifier.Classify(context.Background(), request, bytes.NewReader([]byte{0, 1, 2, 3}))
	if err != nil || result.Classification != ClassificationUnknown || result.SourceRevision != 2 {
		t.Fatalf("Search non-secret downgraded unknown: %+v err=%v", result, err)
	}
	request.Search.Classification = ClassificationSecret
	request.Search.SourceFingerprint = "stale-source"
	result, err = classifier.Classify(context.Background(), request, strings.NewReader("safe"))
	if err != nil || result.Classification != ClassificationNonSecret || result.SourceRevision != 1 {
		t.Fatalf("stale Search evidence changed core result: %+v err=%v", result, err)
	}
}

func TestClassificationMIMEConflictAndActiveFormatsRemainUnknown(t *testing.T) {
	classifier, err := NewClassifier(ClassificationConfig{ScanBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name, mime, content string
	}{
		{name: "provider mime conflict", mime: "image/png", content: "ordinary text"},
		{name: "html active", mime: "text/html", content: "<!doctype html><script>alert(1)</script>"},
		{name: "xml active", mime: "application/xml", content: "<?xml version=\"1.0\"?><root/>"},
		{name: "svg active", mime: "image/svg+xml", content: "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := classificationRequest("asset", "asset", int64(len(testCase.content)))
			request.ProviderMediaType = testCase.mime
			result, err := classifier.Classify(context.Background(), request, strings.NewReader(testCase.content))
			if err != nil || result.Classification != ClassificationUnknown {
				t.Fatalf("classification=%+v err=%v", result, err)
			}
		})
	}
}

func TestClassificationRejectsInvalidContracts(t *testing.T) {
	if _, err := NewClassifier(ClassificationConfig{}); !errors.Is(err, ErrInvalidClassificationRequest) {
		t.Fatalf("config error=%v", err)
	}
	classifier, err := NewClassifier(ClassificationConfig{ScanBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []ClassificationRequest{{}, {Path: "a", Name: "a", SourceSize: -1}} {
		if _, err := classifier.Classify(context.Background(), request, strings.NewReader("")); !errors.Is(err, ErrInvalidClassificationRequest) {
			t.Fatalf("request=%+v error=%v", request, err)
		}
	}
}

type classificationFailReader struct{}

func (classificationFailReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type classificationCountingReader struct {
	reader io.Reader
	bytes  int
}

func (reader *classificationCountingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.bytes += count
	return count, err
}

func classificationRequest(path, name string, size int64) ClassificationRequest {
	return ClassificationRequest{Path: path, Name: name, SourceSize: size}
}
