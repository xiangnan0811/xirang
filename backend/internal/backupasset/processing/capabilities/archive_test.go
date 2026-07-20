package capabilities

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
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
