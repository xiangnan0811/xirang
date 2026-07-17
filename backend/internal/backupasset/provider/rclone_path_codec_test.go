package provider

import (
	"crypto/md5" //nolint:gosec // S3 ETag compatibility in a local conformance fixture only
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestRcloneV1744S3PathCodecGoldenRoundTrip(t *testing.T) {
	for _, test := range []struct {
		logical  string
		physical string
	}{
		{"plain/file.txt", "plain/file.txt"},
		{"目录/文件.txt", "目录/文件.txt"},
		{"．/．．", "．/．．"},
		{"‛．/‛．‛．", "‛．/‛．‛．"},
		{"solidus／name", "solidus／name"},
		{"solidus‛／name", "solidus‛／name"},
		{"quote‛‛name", "quote‛‛name"},
		{"replacement�name", "replacement�name"},
	} {
		t.Run(test.logical, func(t *testing.T) {
			physical, err := EncodeRcloneV1744S3Path(test.logical)
			if err != nil || physical != test.physical {
				t.Fatalf("Encode(%q)=%q err=%v, want %q", test.logical, physical, err, test.physical)
			}
			logical, err := DecodeRcloneV1744S3Path(physical)
			if err != nil || logical != test.logical {
				t.Fatalf("Decode(%q)=%q err=%v, want %q", physical, logical, err, test.logical)
			}
		})
	}
}

func TestRcloneV1744S3PathCodecRejectsInvalidAndNonCanonicalPaths(t *testing.T) {
	for _, logical := range []string{"", "/absolute", "trailing/", "double//component", ".", "..", "quote‛name", string([]byte{'a', 0xff, 'b'}), "nul\x00name"} {
		if _, err := EncodeRcloneV1744S3Path(logical); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("logical %q error=%v, want invalid state", logical, err)
		}
	}
	for _, physical := range []string{
		"", "/absolute", "trailing/", "double//component",
		"dangling‛", "quoted-byte-‛41", "invalid-byte-‛BF", "nul␀name",
	} {
		if _, err := DecodeRcloneV1744S3Path(physical); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("physical %q error=%v, want invalid state", physical, err)
		}
	}
}

func TestRcloneV1744S3PathCodecSetRejectsDuplicatesAndPhysicalDrift(t *testing.T) {
	mapping, err := ValidateRcloneV1744S3PathSet([]string{"a", "目录/文件", "．"})
	if err != nil || mapping["．"] != "．" || len(mapping) != 3 {
		t.Fatalf("mapping=%+v err=%v", mapping, err)
	}
	if _, err := ValidateRcloneV1744S3PathSet([]string{"same", "same"}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("duplicate set error=%v", err)
	}
}

func TestRcloneV1744PathCodecConformance(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("RCLONE_TEST_BINARY"))
	if binary == "" {
		t.Skip("RCLONE_TEST_BINARY is required for pinned Rclone path-codec conformance")
	}
	versionOutput, err := exec.Command(binary, "version").CombinedOutput() //nolint:gosec // explicit test-only binary selected by the completion gate
	if err != nil || !strings.Contains(string(versionOutput), "rclone v1.74.4") {
		t.Fatalf("RCLONE_TEST_BINARY is not the pinned v1.74.4 binary")
	}

	payload := []byte("x")
	putKeys := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer request.Body.Close() //nolint:errcheck // test server consumes best-effort request bodies
		_, _ = io.Copy(io.Discard, request.Body)
		if request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/bucket/") {
			putKeys <- strings.TrimPrefix(request.URL.Path, "/bucket/")
			etag := fmt.Sprintf("\"%x\"", md5.Sum(payload)) //nolint:gosec // S3 ETag fixture, not a security digest
			response.Header().Set("ETag", etag)
			response.Header().Set("x-amz-version-id", "FAKE_RCLONE_CODEC_VERSION_FOR_TEST_ONLY")
			response.WriteHeader(http.StatusOK)
			return
		}
		response.Header().Set("Content-Type", "application/xml")
		response.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(response, `<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
	}))
	defer server.Close()

	directory := t.TempDir()
	source := filepath.Join(directory, "payload")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "rclone.conf")
	configuration := fmt.Sprintf(`[test]
type = s3
provider = Other
env_auth = false
access_key_id = FAKE_RCLONE_CODEC_ACCESS_KEY_FOR_TEST_ONLY
secret_access_key = FAKE_RCLONE_CODEC_SECRET_KEY_FOR_TEST_ONLY
region = us-east-1
endpoint = %s
force_path_style = true
no_check_bucket = true
no_head = true
disable_checksum = true
encoding = Slash,InvalidUtf8,Dot
`, server.URL)
	if err := os.WriteFile(config, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, logical := range []string{
		"plain/file.txt",
		"目录/文件.txt",
		"．/．．",
		"‛．/‛．‛．",
		"solidus／name",
		"solidus‛／name",
		"quote‛‛name",
	} {
		t.Run(logical, func(t *testing.T) {
			want, err := EncodeRcloneV1744S3Path(logical)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(binary, "copyto", source, "test:bucket/"+logical, "--config", config, "--retries", "1", "--low-level-retries", "1") //nolint:gosec // fixed test args and pinned binary
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("pinned Rclone copyto failed: %v (%d output bytes)", err, len(output))
			}
			select {
			case got := <-putKeys:
				if got != want {
					t.Fatalf("pinned Rclone encoded key=%q, want %q", got, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pinned Rclone did not issue PutObject")
			}
		})
	}
}
