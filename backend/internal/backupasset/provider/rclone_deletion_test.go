package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

func TestRcloneExactPointDeletionDeletesCommittedPrefixOnly(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	parent := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points")
	transport := &rcloneDeletionTransport{prefixPresent: true}
	deleter := newRclonePrefixPointDeleterForTest(t, transport)
	request := validRclonePrefixDeleteRequest(t, prefix)
	result, err := deleter.DeletePoint(context.Background(), request)
	if err != nil {
		t.Fatalf("DeletePoint exact prefix: %v", err)
	}
	if result.Outcome != DeletePointDeleted || !lowerHex(result.ReceiptDigest, 64) {
		t.Fatalf("prefix delete result=%+v", result)
	}
	if transport.deleteCalls != 1 || transport.deleted.value != prefix.value {
		t.Fatalf("deleted prefix=%q calls=%d, want exact committed prefix", transport.deleted.value, transport.deleteCalls)
	}
	if transport.deleted.value == parent.value || strings.HasPrefix(parent.value, transport.deleted.value) && transport.deleted.value != prefix.value {
		t.Fatal("parent prefix was deleted")
	}

	parentRequest := request
	parentRequest.Point.Native = parent.value
	if access, ok := parentRequest.Snapshot.Access.AdapterData.(RclonePrefixDeletionAccess); ok {
		access.Prefix = parent
		parentRequest.Snapshot.Access.AdapterData = access
	}
	if _, err := deleter.DeletePoint(context.Background(), parentRequest); !errors.Is(err, ErrInvalidDeletePointRequest) {
		t.Fatalf("parent prefix error=%v, want invalid delete request", err)
	}

	if err := (CommandInvocation{
		Tool: ToolRclone, Operation: OperationRcloneManagedDeleteExactPrefix, Purpose: CommandPurposeDelete,
		SecretStdin: []byte("FAKE_RCLONE_CONFIG_FOR_TEST_ONLY"), RcloneSource: &prefix,
		RcloneLowLevelRetries: 1, AbsoluteDeadline: time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC),
	}).Validate(); err != nil {
		t.Fatalf("exact prefix delete allowlist rejected: %v", err)
	}
}

func TestRcloneExactPointDeletionClassifiesPrefixPresenceFromJoinedExitCodes(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	request := validRclonePrefixDeleteRequest(t, prefix)

	t.Run("run_failed_join_exit_3_already_absent", func(t *testing.T) {
		transport := newRclonePrefixPresenceTransport(t, sshutil.ErrCommandFailed, []CommandCompletion{
			{ExitCodeKnown: true, ExitCode: 3},
		})
		result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
		if err != nil || result.Outcome != DeletePointAlreadyAbsent || !lowerHex(result.ReceiptDigest, 64) {
			t.Fatalf("joined exit 3 result=%+v err=%v, want already_absent", result, err)
		}
		if transport.deleteCalls != 0 || transport.statJoins != 1 {
			t.Fatalf("absent prefix purge/joins=%d/%d, want 0/1", transport.deleteCalls, transport.statJoins)
		}
	})

	t.Run("capability_unavailable_run_join_exit_0_then_3_deleted", func(t *testing.T) {
		transport := newRclonePrefixPresenceTransport(t, newCapabilityError(backupasset.CapabilityProviderUnavailable), []CommandCompletion{
			{ExitCodeKnown: true, ExitCode: 0},
			{ExitCodeKnown: true, ExitCode: 3},
		})
		result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
		if err != nil || result.Outcome != DeletePointDeleted || !lowerHex(result.ReceiptDigest, 64) {
			t.Fatalf("joined present-then-absent result=%+v err=%v, want deleted", result, err)
		}
		if transport.deleteCalls != 1 || transport.statJoins != 2 {
			t.Fatalf("deleted prefix purge/joins=%d/%d, want 1/2", transport.deleteCalls, transport.statJoins)
		}
	})

	t.Run("exit_17_is_unknown_not_absent", func(t *testing.T) {
		transport := newRclonePrefixPresenceTransport(t, sshutil.ErrCommandFailed, []CommandCompletion{
			{ExitCodeKnown: true, ExitCode: 17},
		})
		result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
		if !errors.Is(err, ErrRcloneAttemptPresenceUnknown) || result.Outcome == DeletePointAlreadyAbsent || result.Outcome == DeletePointDeleted {
			t.Fatalf("exit 17 result=%+v err=%v, want presence unknown", result, err)
		}
		if transport.deleteCalls != 0 {
			t.Fatalf("unknown presence issued purge %d time(s)", transport.deleteCalls)
		}
	})

	t.Run("unknown_exit_is_error_not_absent", func(t *testing.T) {
		transport := newRclonePrefixPresenceTransport(t, newCapabilityError(backupasset.CapabilityProviderUnavailable), []CommandCompletion{
			{},
		})
		result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
		if !errors.Is(err, ErrRcloneAttemptPresenceUnknown) || result.Outcome == DeletePointAlreadyAbsent || result.Outcome == DeletePointDeleted {
			t.Fatalf("unknown exit result=%+v err=%v, want presence unknown", result, err)
		}
		if transport.deleteCalls != 0 {
			t.Fatalf("unknown exit issued purge %d time(s)", transport.deleteCalls)
		}
	})

	t.Run("post_purge_exit_0_is_not_deleted", func(t *testing.T) {
		transport := newRclonePrefixPresenceTransport(t, sshutil.ErrCommandFailed, []CommandCompletion{
			{ExitCodeKnown: true, ExitCode: 0},
			{ExitCodeKnown: true, ExitCode: 0},
		})
		result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
		if err == nil || result.Outcome == DeletePointDeleted || result.Outcome == DeletePointAlreadyAbsent {
			t.Fatalf("post-purge exit 0 result=%+v err=%v, want remaining-prefix error", result, err)
		}
		if transport.deleteCalls != 1 {
			t.Fatalf("present prefix purge calls=%d, want 1", transport.deleteCalls)
		}
	})
}

func TestRcloneExactPointDeletionRejectsPrefixPathEscape(t *testing.T) {
	hexID := strings.Repeat("a", 32)
	transport := &rcloneDeletionTransport{prefixPresent: true}
	deleter := newRclonePrefixPointDeleterForTest(t, transport)
	for _, value := range []string{
		"backup:managed/v1/points/" + hexID + "/../sibling",
		"backup:managed/v1/points/" + hexID + "/..",
		"backup:managed/v1/points/" + hexID + "/data/../..",
		"backup:managed/../other/points/" + hexID,
		"backup:../points/" + hexID,
	} {
		t.Run(value, func(t *testing.T) {
			prefix := mustRclonePrivateLocatorForTest(t, value)
			if _, err := deleter.DeletePoint(context.Background(), validRclonePrefixDeleteRequest(t, prefix)); !errors.Is(err, ErrInvalidDeletePointRequest) {
				t.Fatalf("escaped prefix %q error=%v, want invalid delete request", value, err)
			}
			if transport.deleteCalls != 0 {
				t.Fatalf("escaped prefix %q reached purge %d time(s)", value, transport.deleteCalls)
			}
		})
	}
}

func TestRcloneExactPointDeletionCarriesSSHRuntime(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	runtime := &RemoteCommandAccess{Node: model.Node{ID: 9}}
	request := validRclonePrefixDeleteRequest(t, prefix)
	access := request.Snapshot.Access.AdapterData.(RclonePrefixDeletionAccess)
	access.Command = runtime
	request.Snapshot.Access.AdapterData = access

	transport := &rcloneDeletionTransport{prefixPresent: true}
	result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
	if err != nil || result.Outcome != DeletePointDeleted {
		t.Fatalf("DeletePoint with Runtime result=%+v err=%v", result, err)
	}
	for name, invocation := range map[string]CommandInvocation{
		"stat":  transport.statInvocation,
		"purge": transport.purgeInvocation,
	} {
		if invocation.Runtime != runtime || invocation.Runtime.Node.ID == 0 {
			t.Fatalf("%s invocation Runtime=%+v, want Node.ID=9", name, invocation.Runtime)
		}
	}

	sshTransport, err := newSSHCommandTransport(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
		return &fakeRemoteCommandRunner{}, &trackingCloser{}, nil
	}, 1, ToolBinaries{Rclone: "rclone"})
	if err != nil {
		t.Fatalf("newSSHCommandTransport: %v", err)
	}
	specInvocation := transport.purgeInvocation
	specInvocation.AbsoluteDeadline = time.Now().UTC().Add(time.Hour)
	if _, _, _, err := sshTransport.commandSpec(specInvocation, testOperationLimits(), 1024); err != nil {
		t.Fatalf("commandSpec completeness rejected Runtime-bearing purge: %v", err)
	}
}

func TestRcloneExactPointDeletionRejectsMissingRemoteRuntime(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	transport := &rcloneDeletionTransport{prefixPresent: true}
	request := validRclonePrefixDeleteRequest(t, prefix)
	if access, ok := request.Snapshot.Access.AdapterData.(RclonePrefixDeletionAccess); ok {
		access.Command = nil
		request.Snapshot.Access.AdapterData = access
	}
	if _, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request); !errors.Is(err, ErrInvalidDeletePointRequest) {
		t.Fatalf("missing Runtime error=%v, want invalid delete request", err)
	}
	if transport.deleteCalls != 0 {
		t.Fatalf("missing Runtime reached purge %d time(s)", transport.deleteCalls)
	}
}

func TestRcloneExactPointDeletionPrefixAlreadyAbsentIsIdempotent(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	transport := &rcloneDeletionTransport{}
	deleter := newRclonePrefixPointDeleterForTest(t, transport)
	request := validRclonePrefixDeleteRequest(t, prefix)
	result, err := deleter.DeletePoint(context.Background(), request)
	if err != nil || result.Outcome != DeletePointAlreadyAbsent {
		t.Fatalf("already-absent prefix result=%+v err=%v", result, err)
	}
	if transport.deleteCalls != 0 {
		t.Fatalf("already-absent prefix issued delete %d time(s)", transport.deleteCalls)
	}
}

func TestRcloneExactPointDeletionAbsentPrefixOnWrongLiveIdentityIsConflict(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	transport := &rcloneDeletionTransport{liveBackend: "s3"}
	deleter := newRclonePrefixPointDeleterForTest(t, transport)
	result, err := deleter.DeletePoint(context.Background(), validRclonePrefixDeleteRequest(t, prefix))
	if !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("wrong live repo error=%v result=%+v, want identity conflict", err, result)
	}
	if result.Outcome == DeletePointAlreadyAbsent {
		t.Fatal("absent prefix on wrong live repository returned already-absent")
	}
	if transport.deleteCalls != 0 {
		t.Fatalf("wrong live repo issued purge %d time(s)", transport.deleteCalls)
	}
}

func TestRcloneExactPointDeletionAbsentPrefixOnWrongSameBackendIdentityIsConflict(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	ownedRoot, err := rcloneManagedRootFromPointPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	request := validRclonePrefixDeleteRequest(t, prefix)
	access := request.Snapshot.Access.AdapterData.(RclonePrefixDeletionAccess)
	access.ExpectedBackend = "s3"
	access.ConfigDigest = strings.Repeat("c", 64)
	access.ExpectedRootIdentity = rclonePortableRootIdentity(strings.Repeat("a", 64), ownedRoot)
	if access.ExpectedRootIdentity == rclonePortableRootIdentity(access.ConfigDigest, ownedRoot) {
		t.Fatal("same-backend S3 bindings must have distinct root identities")
	}
	request.Snapshot.Access.AdapterData = access

	transport := &rcloneDeletionTransport{liveBackend: "s3"}
	result, err := newRclonePrefixPointDeleterForTest(t, transport).DeletePoint(context.Background(), request)
	if !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("same-backend wrong live repo error=%v result=%+v, want identity conflict", err, result)
	}
	if result.Outcome == DeletePointAlreadyAbsent {
		t.Fatal("absent prefix on wrong same-backend repository returned already-absent")
	}
	if transport.deleteCalls != 0 {
		t.Fatalf("wrong same-backend repo issued purge %d time(s)", transport.deleteCalls)
	}
}

func TestRcloneExactPointDeletionRejectsLivePrefixMarkerDrift(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	transport := &rcloneDeletionTransport{
		prefixPresent:    true,
		liveMarkerDigest: strings.Repeat("c", 64),
	}
	deleter := newRclonePrefixPointDeleterForTest(t, transport)
	result, err := deleter.DeletePoint(context.Background(), validRclonePrefixDeleteRequest(t, prefix))
	if !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("live marker drift error=%v result=%+v, want identity conflict", err, result)
	}
	if transport.deleteCalls != 0 {
		t.Fatalf("mismatched live marker deleted %d time(s)", transport.deleteCalls)
	}
}

func TestRcloneExactPointDeletionDeletesFrozenNativeVersions(t *testing.T) {
	versions := []RcloneNativeExactVersion{
		{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
		{PhysicalKey: "managed/v1/control/commit.json", VersionID: "v-owned-2"},
	}
	native := &rcloneNativeDeletionFake{present: map[string]bool{
		"managed/v1/data/file.bin\x00v-owned-1":       true,
		"managed/v1/control/commit.json\x00v-owned-2": true,
	}}
	deleter := newRcloneNativePointDeleterForTest(t, native)
	result, err := deleter.DeletePoint(context.Background(), validRcloneNativeDeleteRequest(t, versions))
	if err != nil {
		t.Fatalf("DeletePoint frozen versions: %v", err)
	}
	if result.Outcome != DeletePointDeleted || !lowerHex(result.ReceiptDigest, 64) {
		t.Fatalf("native version delete result=%+v", result)
	}
	if len(native.deleted) != 2 {
		t.Fatalf("deleted versions=%d, want 2", len(native.deleted))
	}
	for _, version := range versions {
		if !native.deleted[version.PhysicalKey+"\x00"+version.VersionID] {
			t.Fatalf("missing exact version delete for %+v", version)
		}
	}
}

func TestRclonePointDeleterRoutesNativeAccessToExactVersionClient(t *testing.T) {
	transport := &rcloneDeletionTransport{prefixPresent: true}
	mux := newRclonePointDeleterForTest(t, transport)
	versions := []RcloneNativeExactVersion{{
		PhysicalKey: "managed/v1/control/commit.json", VersionID: "v-owned-1",
	}}
	native := &rcloneNativeDeletionFake{present: map[string]bool{
		"managed/v1/control/commit.json\x00v-owned-1": true,
	}}
	request := validRcloneNativeDeleteRequest(t, versions)
	access := request.Snapshot.Access.AdapterData.(RcloneNativeDeletionAccess)
	access.Client = native
	request.Snapshot.Access.AdapterData = access
	result, err := mux.DeletePoint(context.Background(), request)
	if err != nil {
		t.Fatalf("mux native DeletePoint: %v", err)
	}
	if result.Outcome != DeletePointDeleted {
		t.Fatalf("mux native outcome=%s, want deleted", result.Outcome)
	}
	if transport.deleteCalls != 0 {
		t.Fatalf("prefix deleter was invoked during native delete: calls=%d", transport.deleteCalls)
	}
	if !native.deleted["managed/v1/control/commit.json\x00v-owned-1"] {
		t.Fatal("mux did not route native access to the exact-version client")
	}
}

func TestRclonePointDeleterRoutesPrefixAccessToPrefixDeleter(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"."+strings.Repeat("b", 32))
	transport := &rcloneDeletionTransport{prefixPresent: true}
	mux := newRclonePointDeleterForTest(t, transport)
	result, err := mux.DeletePoint(context.Background(), validRclonePrefixDeleteRequest(t, prefix))
	if err != nil {
		t.Fatalf("mux prefix DeletePoint: %v", err)
	}
	if result.Outcome != DeletePointDeleted || transport.deleteCalls != 1 || transport.deleted.value != prefix.value {
		t.Fatalf("mux prefix result=%+v deleted=%q calls=%d", result, transport.deleted.value, transport.deleteCalls)
	}
}

func TestValidCommittedRclonePointPrefixAcceptsPublicationAttemptRoot(t *testing.T) {
	pointID := strings.Repeat("a", 32)
	attemptID := strings.Repeat("b", 32)
	if !validCommittedRclonePointPrefix("backup:managed/v1/points/" + pointID + "." + attemptID) {
		t.Fatal("publication portable attempt root must be an exact committed prefix")
	}
	if validCommittedRclonePointPrefix("backup:managed/v1/points") {
		t.Fatal("parent points prefix must stay rejected")
	}
}

func TestRcloneExactPointDeletionForbidsUnversionedCurrentDelete(t *testing.T) {
	native := &rcloneNativeDeletionFake{present: map[string]bool{"managed/v1/data/file.bin\x00v-owned-1": true}}
	deleter := newRcloneNativePointDeleterForTest(t, native)
	request := validRcloneNativeDeleteRequest(t, []RcloneNativeExactVersion{{
		PhysicalKey: "managed/v1/data/file.bin", VersionID: "",
	}})
	if _, err := deleter.DeletePoint(context.Background(), request); !errors.Is(err, ErrInvalidDeletePointRequest) {
		t.Fatalf("unversioned delete error=%v, want invalid delete request", err)
	}
	if native.unversionedDeletes != 0 || len(native.deleted) != 0 {
		t.Fatalf("unversioned/current delete reached native port deleted=%d unversioned=%d", len(native.deleted), native.unversionedDeletes)
	}
}

func TestRcloneExactPointDeletionBlocksWORM(t *testing.T) {
	native := &rcloneNativeDeletionFake{
		present: map[string]bool{"managed/v1/data/file.bin\x00v-owned-1": true},
		locked:  map[string]bool{"managed/v1/data/file.bin\x00v-owned-1": true},
	}
	deleter := newRcloneNativePointDeleterForTest(t, native)
	result, err := deleter.DeletePoint(context.Background(), validRcloneNativeDeleteRequest(t, []RcloneNativeExactVersion{{
		PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1",
	}}))
	if !errors.Is(err, ErrDeletePointWORM) {
		t.Fatalf("WORM delete error=%v, want typed WORM", err)
	}
	if result.Outcome != DeletePointBlockedWORM {
		t.Fatalf("WORM outcome=%q, want blocked_worm", result.Outcome)
	}
	if len(native.deleted) != 0 {
		t.Fatalf("WORM still deleted versions: %v", native.deleted)
	}
}

func TestRcloneExactPointDeletionHidesPrefixAndVersionIDs(t *testing.T) {
	prefix := mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data")
	payload, err := json.Marshal(validRclonePrefixDeleteRequest(t, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), prefix.value) {
		t.Fatalf("committed rclone prefix leaked in %s", payload)
	}
	nativePayload, err := json.Marshal(validRcloneNativeDeleteRequest(t, []RcloneNativeExactVersion{{
		PhysicalKey: "managed/v1/data/file.bin", VersionID: "FAKE_NATIVE_VERSION_ID_FOR_TEST_ONLY",
	}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"managed/v1/data/file.bin", "FAKE_NATIVE_VERSION_ID_FOR_TEST_ONLY"} {
		if strings.Contains(string(nativePayload), forbidden) {
			t.Fatalf("native version fact %q leaked in %s", forbidden, nativePayload)
		}
	}
}

func TestRclonePrefixDeletionAccessOmitsMarkerDigest(t *testing.T) {
	digest := strings.Repeat("b", 64)
	identity := strings.Repeat("c", 64)
	payload, err := json.Marshal(RclonePrefixDeletionAccess{
		Prefix:               mustRclonePrivateLocatorForTest(t, "backup:managed/v1/points/"+strings.Repeat("a", 32)+"/data"),
		MarkerDigest:         digest,
		ExpectedRootIdentity: identity,
		ConfigDigest:         strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(payload)
	for _, leaked := range []string{digest, identity, strings.Repeat("d", 64), "MarkerDigest", "marker_digest", "ExpectedRootIdentity", "ConfigDigest"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("private rclone deletion identity leaked in %s", payload)
		}
	}
}

func validRclonePrefixDeleteRequest(t *testing.T, prefix RclonePrivateLocator) DeletePointRequest {
	t.Helper()
	root, err := rcloneManagedRootFromPointPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	configDigest := strings.Repeat("d", 64)
	repositoryID := strings.Repeat("1", 32)
	binding := AccessBinding{
		Provider: backupasset.ProviderRclone, RepositoryID: repositoryID,
		Secret: []byte("FAKE_RCLONE_CONFIG_FOR_TEST_ONLY"),
		AdapterData: RclonePrefixDeletionAccess{
			Prefix:               prefix,
			MarkerDigest:         strings.Repeat("b", 64),
			ExpectedBackend:      "local",
			ExpectedRootIdentity: rclonePortableRootIdentity(configDigest, root),
			ConfigDigest:         configDigest,
			Command:              &RemoteCommandAccess{Node: model.Node{ID: 9}},
		},
	}
	return DeletePointRequest{
		Snapshot: ReadSnapshot{
			RepositoryID: repositoryID, CapabilityRevision: 1,
			SourceRevision: strings.Repeat("b", 64), Access: binding,
		},
		Point:                  PointLocator{Native: prefix.value},
		ExpectedSourceRevision: strings.Repeat("b", 64),
		OperationID:            strings.Repeat("e", 32),
	}
}

func validRcloneNativeDeleteRequest(t *testing.T, versions []RcloneNativeExactVersion) DeletePointRequest {
	t.Helper()
	repositoryID := strings.Repeat("1", 32)
	binding := AccessBinding{
		Provider: backupasset.ProviderRclone, RepositoryID: repositoryID,
		AdapterData: RcloneNativeDeletionAccess{Versions: versions},
	}
	return DeletePointRequest{
		Snapshot: ReadSnapshot{
			RepositoryID: repositoryID, CapabilityRevision: 1,
			SourceRevision: strings.Repeat("b", 64), Access: binding,
		},
		Point:                  PointLocator{Native: strings.Repeat("a", 32)},
		ExpectedSourceRevision: strings.Repeat("b", 64),
		OperationID:            strings.Repeat("e", 32),
	}
}

func newRclonePointDeleterForTest(t *testing.T, transport CommandTransport) *RclonePointDeleter {
	t.Helper()
	mux, err := NewRclonePointDeleter(newRclonePrefixPointDeleterForTest(t, transport), func() time.Time {
		return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewRclonePointDeleter: %v", err)
	}
	return mux
}

func newRclonePrefixPointDeleterForTest(t *testing.T, transport CommandTransport) *RclonePrefixPointDeleter {
	t.Helper()
	deleter, err := NewRclonePrefixPointDeleter(transport, func() (OperationLimits, error) {
		return testOperationLimits(), nil
	}, func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewRclonePrefixPointDeleter: %v", err)
	}
	return deleter
}

func newRclonePrefixPresenceTransport(t *testing.T, runStatErr error, completions []CommandCompletion) *rclonePrefixPresenceTransport {
	t.Helper()
	return &rclonePrefixPresenceTransport{runStatErr: runStatErr, completions: completions}
}

func newRcloneNativePointDeleterForTest(t *testing.T, native *rcloneNativeDeletionFake) *RcloneNativePointDeleter {
	t.Helper()
	deleter, err := NewRcloneNativePointDeleter(native, func() time.Time {
		return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewRcloneNativePointDeleter: %v", err)
	}
	return deleter
}

type rcloneDeletionTransport struct {
	prefixPresent    bool
	liveBackend      string
	liveMarkerDigest string
	deleteCalls      int
	deleted          RclonePrivateLocator
	statInvocation   CommandInvocation
	purgeInvocation  CommandInvocation
}

func (transport *rcloneDeletionTransport) Run(_ context.Context, invocation CommandInvocation, _ OperationLimits) (CommandOutput, error) {
	switch invocation.Operation {
	case OperationRcloneManagedFeatures:
		backend := transport.liveBackend
		if backend == "" {
			backend = "local"
		}
		return CommandOutput{Stdout: []byte(`{"Name":"` + backend + `","Features":{}}`)}, nil
	case OperationRcloneManagedExactStat:
		if !transport.prefixPresent || invocation.RcloneSource == nil {
			return CommandOutput{}, newCapabilityError(backupasset.CapabilityProviderUnavailable)
		}
		return CommandOutput{Stdout: []byte(`{"Path":"data","Name":"data","IsDir":true}`)}, nil
	case OperationRcloneManagedCat:
		digest := transport.liveMarkerDigest
		if digest == "" {
			digest = strings.Repeat("b", 64)
		}
		return CommandOutput{Stdout: []byte(`{"source_fingerprint":"` + digest + `"}`)}, nil
	case OperationRcloneManagedDeleteExactPrefix:
		transport.deleteCalls++
		transport.purgeInvocation = invocation
		if invocation.RcloneSource != nil {
			transport.deleted = *invocation.RcloneSource
		}
		transport.prefixPresent = false
		return CommandOutput{}, nil
	default:
		return CommandOutput{}, errors.New("unexpected rclone deletion operation")
	}
}

func (*rcloneDeletionTransport) Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error) {
	return nil, errors.New("unexpected rclone deletion stream")
}

func (transport *rcloneDeletionTransport) OpenExecution(_ context.Context, invocation CommandInvocation, _ OperationLimits, _ int64) (CommandExecution, error) {
	if invocation.Operation != OperationRcloneManagedExactStat {
		return nil, errors.New("unexpected rclone deletion execution")
	}
	transport.statInvocation = invocation
	completion := CommandCompletion{ExitCodeKnown: true, ExitCode: 3}
	if transport.prefixPresent && invocation.RcloneSource != nil {
		completion.ExitCode = 0
	}
	return &rclonePrefixPresenceExecution{completion: completion}, nil
}

type rclonePrefixPresenceTransport struct {
	runStatErr  error
	completions []CommandCompletion
	statJoins   int
	deleteCalls int
}

func (transport *rclonePrefixPresenceTransport) Run(_ context.Context, invocation CommandInvocation, _ OperationLimits) (CommandOutput, error) {
	switch invocation.Operation {
	case OperationRcloneManagedFeatures:
		return CommandOutput{Stdout: []byte(`{"Name":"local","Features":{}}`)}, nil
	case OperationRcloneManagedExactStat:
		if transport.runStatErr != nil {
			return CommandOutput{}, transport.runStatErr
		}
		return CommandOutput{}, sshutil.ErrCommandFailed
	case OperationRcloneManagedCat:
		return CommandOutput{Stdout: []byte(`{"source_fingerprint":"` + strings.Repeat("b", 64) + `"}`)}, nil
	case OperationRcloneManagedDeleteExactPrefix:
		transport.deleteCalls++
		return CommandOutput{}, nil
	default:
		return CommandOutput{}, errors.New("unexpected rclone presence transport operation")
	}
}

func (*rclonePrefixPresenceTransport) Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error) {
	return nil, errors.New("unexpected rclone presence stream open")
}

func (transport *rclonePrefixPresenceTransport) OpenExecution(_ context.Context, invocation CommandInvocation, _ OperationLimits, _ int64) (CommandExecution, error) {
	if invocation.Operation != OperationRcloneManagedExactStat {
		return nil, errors.New("unexpected rclone presence execution")
	}
	completion := CommandCompletion{}
	if transport.statJoins < len(transport.completions) {
		completion = transport.completions[transport.statJoins]
	}
	transport.statJoins++
	return &rclonePrefixPresenceExecution{completion: completion}, nil
}

type rclonePrefixPresenceExecution struct {
	completion CommandCompletion
	canceled   bool
}

func (*rclonePrefixPresenceExecution) Read([]byte) (int, error) { return 0, io.EOF }

func (execution *rclonePrefixPresenceExecution) Join() (CommandCompletion, error) {
	return execution.completion, nil
}

func (execution *rclonePrefixPresenceExecution) Cancel() error {
	execution.canceled = true
	return nil
}

type rcloneNativeDeletionFake struct {
	present            map[string]bool
	locked             map[string]bool
	deleted            map[string]bool
	unversionedDeletes int
}

func (fake *rcloneNativeDeletionFake) ProbeExactVersion(_ context.Context, version RcloneNativeExactVersion) (RcloneNativeVersionProbe, error) {
	key := version.PhysicalKey + "\x00" + version.VersionID
	return RcloneNativeVersionProbe{Present: fake.present[key], Locked: fake.locked[key]}, nil
}

func (fake *rcloneNativeDeletionFake) DeleteExactVersion(_ context.Context, version RcloneNativeExactVersion) error {
	if version.VersionID == "" {
		fake.unversionedDeletes++
		return errors.New("unversioned delete")
	}
	if fake.deleted == nil {
		fake.deleted = map[string]bool{}
	}
	key := version.PhysicalKey + "\x00" + version.VersionID
	if fake.locked[key] {
		return ErrDeletePointWORM
	}
	fake.deleted[key] = true
	delete(fake.present, key)
	return nil
}
