package capabilities

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestArchiveInspectRejectsTraversalLinksDevicesBombsAndEncryption(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		media   string
		limits  ArchiveLimits
	}{
		{"traversal", makeZip(t, zipEntry{name: "../escape.txt", content: []byte("x")}), "application/zip", testArchiveLimits()},
		{"symlink", makeTar(t, tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}, nil), "application/x-tar", testArchiveLimits()},
		{"device", makeTar(t, tar.Header{Name: "device", Typeflag: tar.TypeChar}, nil), "application/x-tar", testArchiveLimits()},
		{"bomb", makeZip(t, zipEntry{name: "large.txt", content: bytes.Repeat([]byte("a"), 1<<20)}), "application/zip", func() ArchiveLimits { v := testArchiveLimits(); v.MaxCompressionRatio = 2; return v }()},
		{"encrypted", makeEncryptedZipHeader(), "application/zip", testArchiveLimits()},
	}
	for _, testCase := range tests {
		if _, err := InspectArchive(testCase.payload, testCase.media, testCase.limits); err == nil {
			t.Fatalf("%s archive accepted", testCase.name)
		}
	}
}

func TestArchiveInspectRejectsUnsafeUnicodeComponentsWithoutRawPathLeak(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "ASCII control", path: "folder/member\u0001.txt"},
		{name: "tab control", path: "folder/member\t.txt"},
		{name: "bidi override", path: "folder/member\u202etxt"},
		{name: "zero width format", path: "folder/member\u200b.txt"},
		{name: "confusable dot", path: "\uff0e/member.txt"},
		{name: "confusable dot dot", path: "\uff0e\uff0e/member.txt"},
		{name: "confusable slash", path: "folder/member\uff0fescape.txt"},
		{name: "confusable reverse slash", path: "folder/member\uff3cescape.txt"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			index, err := InspectArchive(
				makeZip(t, zipEntry{name: testCase.path, content: []byte("member")}),
				"application/zip",
				testArchiveLimits(),
			)
			if err == nil {
				t.Fatalf("unsafe path accepted: entries=%+v", index.Entries)
			}
			if strings.Contains(err.Error(), testCase.path) || len(index.Entries) != 0 {
				t.Fatalf("unsafe archive leaked raw path %q: index=%+v err=%v", testCase.path, index, err)
			}
		})
	}
}

func TestArchiveInspectRejectsUnicodeNormalizationAndFoldCollisions(t *testing.T) {
	tests := []struct {
		name  string
		first string
		last  string
	}{
		{name: "canonical normalization", first: "folder/caf\u00e9.txt", last: "folder/cafe\u0301.txt"},
		{name: "compatibility normalization", first: "folder/A.txt", last: "folder/\uff21.txt"},
		{name: "Unicode case fold", first: "folder/Stra\u00dfe.txt", last: "folder/STRASSE.txt"},
		{name: "default ignorable code point", first: "folder/a.txt", last: "folder/a\u034f.txt"},
		{name: "variation selector", first: "folder/a.txt", last: "folder/a\ufe0f.txt"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			index, err := InspectArchive(
				makeZip(t,
					zipEntry{name: testCase.first, content: []byte("first")},
					zipEntry{name: testCase.last, content: []byte("last")},
				),
				"application/zip",
				testArchiveLimits(),
			)
			if err == nil {
				t.Fatalf("colliding paths accepted: entries=%+v", index.Entries)
			}
			if strings.Contains(err.Error(), testCase.first) || strings.Contains(err.Error(), testCase.last) || len(index.Entries) != 0 {
				t.Fatalf("colliding archive leaked a raw path: index=%+v err=%v", index, err)
			}
		})
	}
}

func TestArchiveInspectPreservesSafeNormalizedUnicodeDisplayName(t *testing.T) {
	const rawPath = "\u8d44\u6599/r\u00e9sum\u00e9-\u6771\u4eac-\u0645\u0644\u0641-\U0001f9fe.txt"
	index, err := InspectArchive(
		makeZip(t, zipEntry{name: rawPath, content: []byte("member")}),
		"application/zip",
		testArchiveLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 1 || index.Entries[0].DisplayName != "r\u00e9sum\u00e9-\u6771\u4eac-\u0645\u0644\u0641-\U0001f9fe.txt" ||
		strings.Contains(index.Entries[0].DisplayName, "\u8d44\u6599/") {
		t.Fatalf("safe Unicode index=%+v", index)
	}
}

func TestArchiveInspectPreservesLegalUnicodeNormalizationForms(t *testing.T) {
	for _, rawPath := range []string{
		"folder/cafe\u0301.txt",
		"folder/\uff21.txt",
	} {
		index, err := InspectArchive(
			makeZip(t, zipEntry{name: rawPath, content: []byte("member")}),
			"application/zip",
			testArchiveLimits(),
		)
		if err != nil || len(index.Entries) != 1 {
			t.Fatalf("legal Unicode normalization form %q rejected: index=%+v err=%v", rawPath, index, err)
		}
	}
	index, err := InspectArchive(
		makeZip(t,
			zipEntry{name: "left/member.txt", content: []byte("left")},
			zipEntry{name: "right/member.txt", content: []byte("right")},
		),
		"application/zip",
		testArchiveLimits(),
	)
	if err != nil || len(index.Entries) != 2 || index.Entries[0].ParentID == "" || index.Entries[0].ParentID == index.Entries[1].ParentID {
		t.Fatalf("legal duplicate basenames lost opaque parent identity: index=%+v err=%v", index, err)
	}
}

func TestArchiveInspectAssignsClosedCanonicalMemberMediaType(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0xff}
	tests := []struct {
		name    string
		archive []byte
		media   string
	}{
		{name: "zip", archive: makeZip(t, zipEntry{name: "member.bin", content: payload}), media: "application/zip"},
		{name: "tar", archive: makeTar(t, tar.Header{Name: "member.bin", Typeflag: tar.TypeReg}, payload), media: "application/x-tar"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			index, err := InspectArchive(testCase.archive, testCase.media, testArchiveLimits())
			if err != nil || len(index.Entries) != 1 {
				t.Fatalf("archive index=%+v err=%v", index, err)
			}
			if index.Entries[0].MediaType != "application/octet-stream" {
				t.Fatalf("member media type=%q, want closed octet-stream fallback", index.Entries[0].MediaType)
			}
		})
	}
}

func TestArchiveIndexUsesOpaqueMembersAndExtractsOneRegularEntry(t *testing.T) {
	payload := makeZip(t,
		zipEntry{name: "folder/first.txt", content: []byte("first")},
		zipEntry{name: "folder/second.txt", content: []byte("second")},
	)
	index, err := InspectArchive(payload, "application/zip", testArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 2 || len(index.Entries[0].ID) != 32 || index.Entries[0].DisplayName != "first.txt" {
		t.Fatalf("unexpected opaque index: %+v", index)
	}
	for _, entry := range index.Entries {
		if entry.ID == "folder/first.txt" || entry.ID == "folder/second.txt" {
			t.Fatalf("raw path used as member ID: %+v", entry)
		}
	}
	content, mediaType, err := ExtractArchiveEntry(payload, "application/zip", index.Entries[1].ID, testArchiveLimits())
	if err != nil || string(content) != "second" || mediaType != "text/plain; charset=utf-8" {
		t.Fatalf("member content=%q media=%q err=%v", content, mediaType, err)
	}
	if _, _, err := ExtractArchiveEntry(payload, "application/zip", "folder/second.txt", testArchiveLimits()); !errors.Is(err, ErrArchiveMember) {
		t.Fatalf("raw member path error=%v", err)
	}
}

func TestArchiveInspectRejectsDeclaredMIMEMagicMismatchAndTrailingTarData(t *testing.T) {
	zipPayload := makeZip(t, zipEntry{name: "member.txt", content: []byte("member")})
	tarPayload := makeTar(t, tar.Header{Name: "member.txt", Typeflag: tar.TypeReg}, []byte("member"))
	tests := []struct {
		name    string
		payload []byte
		media   string
	}{
		{name: "ZIP declared TAR", payload: zipPayload, media: "application/x-tar"},
		{name: "TAR declared ZIP", payload: tarPayload, media: "application/zip"},
		{name: "TAR trailing stream", payload: append(append([]byte(nil), tarPayload...), []byte("TRAILING")...), media: "application/x-tar"},
		{name: "truncated TAR", payload: tarPayload[:len(tarPayload)-257], media: "application/x-tar"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := InspectArchive(testCase.payload, testCase.media, testArchiveLimits()); err == nil {
				t.Fatalf("mismatched or non-canonical archive accepted as %s", testCase.media)
			}
		})
	}
}

func TestArchiveInspectCountsStreamedTARBytesAgainstExpansionLimit(t *testing.T) {
	payload := makeTar(t, tar.Header{Name: "member.txt", Typeflag: tar.TypeReg}, bytes.Repeat([]byte("x"), 4096))
	limits := testArchiveLimits()
	limits.MaxExpandedBytes = 1024
	if _, err := InspectArchive(payload, "application/x-tar", limits); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("expanded TAR limit error=%v", err)
	}
}

type zipEntry struct {
	name    string
	content []byte
}

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	for _, entry := range entries {
		part, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func makeTar(t *testing.T, header tar.Header, content []byte) []byte {
	t.Helper()
	var payload bytes.Buffer
	writer := tar.NewWriter(&payload)
	header.Mode = 0o600
	header.Size = int64(len(content))
	if err := writer.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func makeEncryptedZipHeader() []byte {
	return []byte{'P', 'K', 3, 4, 20, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 'x'}
}

func testArchiveLimits() ArchiveLimits {
	return ArchiveLimits{MaxEntries: 100, MaxDepth: 16, MaxExpandedBytes: 2 << 20, MaxCompressionRatio: 100, MaxMemberBytes: 1 << 20}
}
