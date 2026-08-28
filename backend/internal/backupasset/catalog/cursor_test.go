package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type catalogCursorTestKeys struct {
	active   backupasset.DomainKeyMaterial
	versions map[int]backupasset.DomainKeyMaterial
}

func (keys *catalogCursorTestKeys) Active(_ context.Context, domain backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainCursorSigning {
		return backupasset.DomainKeyMaterial{}, errors.New("wrong domain")
	}
	return keys.active, nil
}

func (keys *catalogCursorTestKeys) ByVersion(_ context.Context, domain backupasset.KeyDomain, version int) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainCursorSigning {
		return backupasset.DomainKeyMaterial{}, errors.New("wrong domain")
	}
	material, ok := keys.versions[version]
	if !ok {
		return backupasset.DomainKeyMaterial{}, errors.New("missing version")
	}
	return material, nil
}

func TestCatalogCursorBindsEntryScopeWithoutPrivateSortFacts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	keys := &catalogCursorTestKeys{
		active: backupasset.DomainKeyMaterial{Version: 2, State: backupasset.DomainKeyActive, Key: []byte("FAKE_CATALOG_CURSOR_KEY_VERSION_2_FOR_TEST_ONLY")},
		versions: map[int]backupasset.DomainKeyMaterial{
			2: {Version: 2, State: backupasset.DomainKeyActive, Key: []byte("FAKE_CATALOG_CURSOR_KEY_VERSION_2_FOR_TEST_ONLY")},
		},
	}
	codec := NewCursorCodec(keys, func() time.Time { return now }, 15*time.Minute)
	scope := CursorScope{
		Endpoint: CursorEndpointEntries, Direction: CursorForward,
		UserID: 7, Role: "operator", Sort: EntrySortNameAsc,
		RepositoryID:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecoveryPointID:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		GenerationID:     "cccccccccccccccccccccccccccccccc",
		ParentEntryID:    strings.Repeat("d", 64),
		ProjectionDigest: strings.Repeat("f", 64),
		Anchor:           CursorAnchor{EntryID: strings.Repeat("e", 64)},
	}
	token, err := codec.Encode(context.Background(), scope)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(token, "report.txt") || strings.Contains(token, "/private") {
		t.Fatalf("cursor leaked private sort facts: %q", token)
	}
	decoded, err := codec.Decode(context.Background(), token, scope)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != scope {
		t.Fatalf("decoded=%#v want=%#v", decoded, scope)
	}
	expectedWithoutAnchor := scope
	expectedWithoutAnchor.Anchor = CursorAnchor{}
	decoded, err = codec.Decode(context.Background(), token, expectedWithoutAnchor)
	if err != nil || decoded.Anchor != scope.Anchor {
		t.Fatalf("decode with server-known scope: decoded=%#v err=%v", decoded, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*CursorScope)
	}{
		{"user", func(changed *CursorScope) { changed.UserID++ }},
		{"role", func(changed *CursorScope) { changed.Role = "admin" }},
		{"direction", func(changed *CursorScope) { changed.Direction = CursorBackward }},
		{"sort", func(changed *CursorScope) { changed.Sort = EntrySortNameDesc }},
		{"repository", func(changed *CursorScope) { changed.RepositoryID = strings.Repeat("0", 32) }},
		{"recovery point", func(changed *CursorScope) { changed.RecoveryPointID = strings.Repeat("1", 32) }},
		{"generation", func(changed *CursorScope) { changed.GenerationID = strings.Repeat("2", 32) }},
		{"parent", func(changed *CursorScope) { changed.ParentEntryID = strings.Repeat("3", 64) }},
		{"directory digest", func(changed *CursorScope) { changed.ProjectionDigest = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := expectedWithoutAnchor
			test.mutate(&changed)
			if _, err := codec.Decode(context.Background(), token, changed); !errors.Is(err, ErrStaleCursor) {
				t.Fatalf("scope change error=%v", err)
			}
		})
	}
	if _, err := codec.Decode(context.Background(), token+"x", expectedWithoutAnchor); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered entry cursor error=%v", err)
	}
}

func TestCatalogCursorBindsExactTwoPointDiffAndRejectsTamper(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, State: backupasset.DomainKeyActive, Key: []byte("FAKE_CATALOG_CURSOR_KEY_VERSION_1_FOR_TEST_ONLY")}
	keys := &catalogCursorTestKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	codec := NewCursorCodec(keys, func() time.Time { return now }, 15*time.Minute)
	scope := CursorScope{
		Endpoint: CursorEndpointDiff, Direction: CursorForward,
		UserID: 1, Role: "admin", Sort: DiffSortPathAsc,
		RepositoryID:           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseRecoveryPointID:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CompareRecoveryPointID: "cccccccccccccccccccccccccccccccc",
		BaseGenerationID:       "dddddddddddddddddddddddddddddddd",
		CompareGenerationID:    "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		BaseParentEntryID:      strings.Repeat("1", 64),
		CompareParentEntryID:   strings.Repeat("2", 64),
		Anchor:                 CursorAnchor{BaseEntryID: strings.Repeat("3", 64), CompareEntryID: strings.Repeat("4", 64), ChangeKind: DiffModified},
	}
	token, err := codec.Encode(context.Background(), scope)
	if err != nil {
		t.Fatalf("encode diff: %v", err)
	}
	if _, err := codec.Decode(context.Background(), token+"x", scope); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor error=%v", err)
	}
	if _, err := codec.Decode(context.Background(), token, scope); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
}

func TestCatalogCursorSupportsVerifyOnlyOverlapAndExpiry(t *testing.T) {
	t.Parallel()

	issuedAt := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	oldMaterial := backupasset.DomainKeyMaterial{Version: 1, State: backupasset.DomainKeyActive, Key: []byte("FAKE_CATALOG_CURSOR_OLD_KEY_FOR_TEST_ONLY")}
	keys := &catalogCursorTestKeys{active: oldMaterial, versions: map[int]backupasset.DomainKeyMaterial{1: oldMaterial}}
	clock := issuedAt
	codec := NewCursorCodec(keys, func() time.Time { return clock }, 15*time.Minute)
	scope := CursorScope{
		Endpoint: CursorEndpointRepositories, Direction: CursorForward,
		UserID: 1, Role: "admin", Sort: RepositorySortCreatedDesc,
		Anchor: CursorAnchor{RepositoryID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	token, err := codec.Encode(context.Background(), scope)
	if err != nil {
		t.Fatalf("encode old cursor: %v", err)
	}
	verifyUntil := issuedAt.Add(time.Hour)
	keys.active = backupasset.DomainKeyMaterial{Version: 2, State: backupasset.DomainKeyActive, Key: []byte("FAKE_CATALOG_CURSOR_NEW_KEY_FOR_TEST_ONLY")}
	keys.versions[1] = backupasset.DomainKeyMaterial{Version: 1, State: backupasset.DomainKeyVerifyOnly, Key: oldMaterial.Key, VerifyUntil: &verifyUntil}
	keys.versions[2] = keys.active
	clock = issuedAt.Add(5 * time.Minute)
	if _, err := codec.Decode(context.Background(), token, scope); err != nil {
		t.Fatalf("verify-only overlap decode: %v", err)
	}
	clock = issuedAt.Add(16 * time.Minute)
	if _, err := codec.Decode(context.Background(), token, scope); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("expired cursor error=%v", err)
	}
	if _, err := codec.Decode(context.Background(), strings.Repeat("x", maxCursorTokenBytes+1), scope); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("oversized cursor error=%v", err)
	}
}
