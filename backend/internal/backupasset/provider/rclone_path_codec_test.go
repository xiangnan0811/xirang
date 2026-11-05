package provider

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // S3 ETag compatibility in a local conformance fixture only
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
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

type rclonePathSetNativeDataPlaneFake struct {
	observe func(context.Context, RcloneNativePublicationRequest) (RcloneManifestBundle, error)
	sync    func(context.Context, RcloneNativePublicationRequest) error
}

func (fake *rclonePathSetNativeDataPlaneFake) ObserveSource(ctx context.Context, request RcloneNativePublicationRequest) (RcloneManifestBundle, error) {
	return fake.observe(ctx, request)
}

func (fake *rclonePathSetNativeDataPlaneFake) Sync(ctx context.Context, request RcloneNativePublicationRequest) error {
	return fake.sync(ctx, request)
}

func (*rclonePathSetNativeDataPlaneFake) VerifyFullBytes(_ context.Context, _ RcloneNativePublicationRequest, expected uint64) (RcloneFullByteProof, error) {
	return RcloneFullByteProof{VerifiedBytes: expected, Complete: true}, nil
}

func TestRcloneV1744S3PathCodecSetRejectsDuplicatesAndPhysicalDrift(t *testing.T) {
	for _, logical := range []string{"a", "目录/文件.txt", "．"} {
		physical, err := EncodeRcloneV1744S3Path(logical)
		if err != nil || physical != logical {
			t.Fatalf("EncodeRcloneV1744S3Path(%q)=%q err=%v", logical, physical, err)
		}
		decoded, err := DecodeRcloneV1744S3Path(physical)
		if err != nil || decoded != logical {
			t.Fatalf("DecodeRcloneV1744S3Path(%q)=%q err=%v", physical, decoded, err)
		}
	}

	now := time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC)
	profile := validRcloneNativeProfileForTest()
	payload := []byte("unicode-positive-control")
	payloadDigest := sha256Hex(payload)
	b0, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{PageCount: 1}, RcloneNativeFullObservation{PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	newRequest := func(s3 *rcloneNativePublisherS3Fake, manifest RcloneManifestBundle) RcloneNativePublicationRequest {
		attempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
		attempt.Native.B0VersionGraphDigest = b0.Digest
		session := newRcloneNativeSession(
			"FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY",
			"123456789012", attempt.Native.RoleSessionIdentityDigest, attempt.Native.SessionExpiresAt,
		)
		config, err := BuildRcloneNativeRcloneConfig(profile, RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, session)
		if err != nil {
			t.Fatal(err)
		}
		attempt.ConfigDigest = sha256Hex(config)
		return RcloneNativePublicationRequest{
			Attempt: attempt, Profile: profile, Session: session, ClientFactory: rcloneNativeClientFactoryFake{s3: s3},
			Source: mustRclonePrivateLocatorForTest(t, "/srv/source"), RcloneConfig: config,
			Runtime: RemoteCommandAccess{Node: model.Node{ID: 9}}, Manifest: manifest,
			ManifestOptions:          rcloneManifestOptionsForTest(),
			ObservationLimits:        RcloneNativeObservationLimits{PageSize: 1000, MaxPages: 4, MaxRecords: 100},
			Encryption:               RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
			EncryptionEvidence:       RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1},
			MarkerKey:                []byte("FAKE_NATIVE_MARKER_AUTH_KEY_32_BYTES_FOR_TEST_ONLY"),
			CapabilityEvidenceDigest: strings.Repeat("a", 64), CostEvidenceDigest: strings.Repeat("b", 64),
			MaxVerifyBytes: 1 << 20, ControlPayloadMaxBytes: 1 << 20, LowLevelRetries: 3,
		}
	}

	unicodeListing := fmt.Sprintf(
		`[{"Path":"目录/文件.txt","Name":"文件.txt","Size":%d,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"%s"},"Metadata":{"mode":"100640","uid":"1000","gid":"1000","mtime":"2026-07-16T01:00:01Z"}}]`,
		len(payload), payloadDigest,
	)
	for name, listing := range map[string]string{
		"duplicate logical path": fmt.Sprintf(
			`[{"Path":"same","Name":"same","Size":%d,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"%s"}},{"Path":"same","Name":"same","Size":%d,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"%s"}}]`,
			len(payload), payloadDigest, len(payload), payloadDigest,
		),
		"reserved rclonelink regular file": fmt.Sprintf(
			`[{"Path":"plain.rclonelink","Name":"plain.rclonelink","Size":%d,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"%s"},"Metadata":{"mode":"100644"}}]`,
			len(payload), payloadDigest,
		),
	} {
		t.Run(name, func(t *testing.T) {
			events := make([]string, 0)
			s3 := newRcloneNativePublisherS3Fake(profile, now, &events)
			request := newRequest(s3, RcloneManifestBundle{})
			dataPlane := &rclonePathSetNativeDataPlaneFake{
				observe: func(ctx context.Context, current RcloneNativePublicationRequest) (RcloneManifestBundle, error) {
					return BuildRcloneManifestV1(ctx, strings.NewReader(listing), current.ManifestOptions)
				},
				sync: func(context.Context, RcloneNativePublicationRequest) error {
					t.Fatal("Sync must not run when source manifest preparation rejects unsafe paths")
					return nil
				},
			}
			publisher := NewRcloneNativePublisher(dataPlane, func() time.Time { return now })
			strategy, err := NewRclonePublicationStrategy(
				NewRclonePortablePublisher(&fakeRclonePortableRemote{}, func(time.Duration) {}, func() time.Time { return now }),
				publisher,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = strategy.Prepare(context.Background(), PublicationPrepareRequest{
				Attempt:     NewRclonePublicationAttempt(request.Attempt),
				RcloneInput: &RclonePublicationInput{ManifestLimits: request.ManifestOptions.Limits, NativeRequest: &request},
			})
			if !errors.Is(err, ErrRcloneManifestPathUnsafe) {
				t.Fatalf("Prepare(%s) err=%v, want %v", name, err, ErrRcloneManifestPathUnsafe)
			}
			fact := ClassifyRclonePublicationFailure(err)
			if !fact.Unattempted || fact.KnownNonzero || fact.CompletionUnproven || fact.WriterQuiesced == nil || !*fact.WriterQuiesced {
				t.Fatalf("Prepare(%s) facts=%+v", name, fact)
			}
			if len(s3.writes) != 0 {
				t.Fatalf("Prepare(%s) unexpectedly wrote remote objects: %v", name, s3.writes)
			}
		})
	}

	t.Run("valid unicode path positive control", func(t *testing.T) {
		events := make([]string, 0)
		s3 := newRcloneNativePublisherS3Fake(profile, now, &events)
		request := newRequest(s3, RcloneManifestBundle{})
		dataPlane := &rclonePathSetNativeDataPlaneFake{
			observe: func(ctx context.Context, current RcloneNativePublicationRequest) (RcloneManifestBundle, error) {
				return BuildRcloneManifestV1(ctx, strings.NewReader(unicodeListing), current.ManifestOptions)
			},
			sync: func(_ context.Context, current RcloneNativePublicationRequest) error {
				physical, err := EncodeRcloneV1744S3Path("目录/文件.txt")
				if err != nil {
					return err
				}
				s3.addObject(current.Profile.ManagedPrefix+"data/"+physical, "opaque-unicode-v1", payload, current.Encryption.Profile, "", false)
				return nil
			},
		}
		publisher := NewRcloneNativePublisher(dataPlane, func() time.Time { return now })
		strategy, err := NewRclonePublicationStrategy(
			NewRclonePortablePublisher(&fakeRclonePortableRemote{}, func(time.Duration) {}, func() time.Time { return now }),
			publisher,
		)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{
			Attempt:     NewRclonePublicationAttempt(request.Attempt),
			RcloneInput: &RclonePublicationInput{ManifestLimits: request.ManifestOptions.Limits, NativeRequest: &request},
		})
		if err != nil {
			t.Fatalf("Prepare unicode positive control: %v", err)
		}
		commit, err := publisher.Publish(context.Background(), *prepared.RcloneInput.NativeRequest)
		if err != nil {
			t.Fatalf("Publish unicode positive control: %v", err)
		}
		if err := commit.Validate(); err != nil || commit.Native == nil || commit.Native.CommitVersionID == "" {
			t.Fatalf("unicode commit=%+v err=%v", commit, err)
		}
		if got := path.Base(s3.writes[len(s3.writes)-1]); got != "commit.json" {
			t.Fatalf("last write=%q writes=%v", got, s3.writes)
		}
		reconciled, err := publisher.Reconcile(context.Background(), *prepared.RcloneInput.NativeRequest)
		if err != nil || !reflect.DeepEqual(reconciled, commit) {
			t.Fatalf("reconciled=%+v err=%v want=%+v", reconciled, err, commit)
		}
	})

	t.Run("controlled duplicate physical object key in native publish", func(t *testing.T) {
		manifest, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(unicodeListing), rcloneManifestOptionsForTest())
		if err != nil {
			t.Fatal(err)
		}
		firstLine := bytes.TrimSpace(manifest.Chunks[0].Encoded)
		var duplicateEntry rcloneCanonicalManifestEntry
		if err := json.Unmarshal(firstLine, &duplicateEntry); err != nil {
			t.Fatal(err)
		}
		duplicateEntry.Path = "目录/文件-alias.txt"
		secondLine, err := json.Marshal(duplicateEntry)
		if err != nil {
			t.Fatal(err)
		}
		injectedChunk := append(append(append([]byte(nil), firstLine...), '\n'), append(secondLine, '\n')...)
		injectedManifest := manifest
		injectedManifest.EntryCount = 2
		injectedManifest.LogicalBytes = uint64(2 * len(payload))
		injectedManifest.Chunks = []RcloneManifestChunk{{
			Ordinal:    0,
			Digest:     sha256Hex(injectedChunk),
			Size:       int64(len(injectedChunk)),
			EntryCount: 2,
			Encoded:    injectedChunk,
		}}

		events := make([]string, 0)
		s3 := newRcloneNativePublisherS3Fake(profile, now, &events)
		request := newRequest(s3, injectedManifest)
		dataPlane := &rclonePathSetNativeDataPlaneFake{
			observe: func(context.Context, RcloneNativePublicationRequest) (RcloneManifestBundle, error) {
				return injectedManifest, nil
			},
			sync: func(_ context.Context, current RcloneNativePublicationRequest) error {
				physical, err := EncodeRcloneV1744S3Path("目录/文件.txt")
				if err != nil {
					return err
				}
				s3.addObject(current.Profile.ManagedPrefix+"data/"+physical, "opaque-dup-v1", payload, current.Encryption.Profile, "", false)
				return nil
			},
		}
		publisher := NewRcloneNativePublisher(dataPlane, func() time.Time { return now })
		_, err = publisher.Publish(context.Background(), request)
		if rcloneNativeReason(err) != backupasset.RcloneReasonUnexpectedVersion {
			t.Fatalf("duplicate physical key err=%v reason=%q, want %q", err, rcloneNativeReason(err), backupasset.RcloneReasonUnexpectedVersion)
		}
		fact := ClassifyRclonePublicationFailure(err)
		if fact.Unattempted || fact.KnownNonzero || !fact.CompletionUnproven || fact.WriterQuiesced == nil || !*fact.WriterQuiesced {
			t.Fatalf("duplicate physical key facts=%+v", fact)
		}
		controlPrefix := rcloneNativeAttemptControlPrefix(request)
		wantWrites := []string{controlPrefix + "start.json", controlPrefix + "end.json"}
		if !reflect.DeepEqual(s3.writes, wantWrites) {
			t.Fatalf("pre-commit writes=%v, want only start/end markers %v", s3.writes, wantWrites)
		}
		for _, written := range s3.writes {
			base := path.Base(written)
			if base == "commit.json" || base == "manifest-index.json" || strings.HasPrefix(base, "manifest-") {
				t.Fatalf("unexpected formal control/commit object written on rejected publish: %q", written)
			}
		}
		if _, err := publisher.Reconcile(context.Background(), request); rcloneNativeReason(err) != backupasset.RcloneReasonMarkerMismatch {
			t.Fatalf("Reconcile after rejected publish err=%v reason=%q, want %q", err, rcloneNativeReason(err), backupasset.RcloneReasonMarkerMismatch)
		}
	})
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
