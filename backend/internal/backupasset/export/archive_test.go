package export

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
)

func archiveEntriesWithTestRootIdentity(entries []ArchiveEntry) []ArchiveEntry {
	withIdentity := append([]ArchiveEntry(nil), entries...)
	for index := range withIdentity {
		entry := &withIdentity[index]
		if backupasset.ValidateOpaqueID(entry.RecoveryPointID) != nil {
			entry.RecoveryPointID = strings.Repeat("a", 32)
		}
		if !lowerHex(entry.EntryID, 64) {
			entry.EntryID = entry.ItemID + entry.ItemID
		}
	}
	return withIdentity
}

func TestWriteArchiveProducesDeterministicClosedProfileBytes(t *testing.T) {
	modified := time.Date(2026, time.July, 24, 1, 2, 3, 0, time.UTC)
	selectionDigest := strings.Repeat("a", 64)
	payload := []byte("archive bytes")
	entry := ArchiveEntry{
		ItemID: "11111111111111111111111111111111", Components: []string{"folder", "asset.txt"},
		Type: backupasset.CatalogEntryFile, Size: int64(len(payload)), ModifiedAt: modified,
		Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}
	entry = archiveEntriesWithTestRootIdentity([]ArchiveEntry{entry})[0]
	expectedReport := ArchiveReport{
		SchemaVersion: 1, SelectionDigest: selectionDigest, ResultKind: ResultComplete,
		Packed: 1, LogicalBytes: int64(len(payload)), ProviderBytes: int64(len(payload)),
		Items: []ArchiveItemReport{{
			ItemID: entry.ItemID, MemberPath: "folder/asset.txt", State: ItemPacked,
			LogicalBytes: int64(len(payload)), ProviderBytes: int64(len(payload)),
		}},
	}
	for _, testCase := range []struct {
		name    string
		format  ArchiveFormat
		profile string
	}{
		{name: "zip deflate", format: ArchiveZIP, profile: "zip_deflate_v1"},
		{name: "tar none", format: ArchiveTAR, profile: "tar_none_v1"},
		{name: "tar gzip", format: ArchiveTAR, profile: "tar_gzip_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outputs := make([][]byte, 2)
			for index := range outputs {
				var output bytes.Buffer
				report, err := WriteArchive(
					context.Background(), &output, testCase.format, testCase.profile,
					selectionDigest, []ArchiveEntry{entry},
					ArchiveLimits{MaxItems: 2, MaxLogicalBytes: 32, MaxProviderBytes: 32},
				)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(report, expectedReport) {
					t.Fatalf("report=%+v want %+v", report, expectedReport)
				}
				outputs[index] = append([]byte(nil), output.Bytes()...)
			}
			if !bytes.Equal(outputs[0], outputs[1]) {
				t.Fatal("same snapshot/profile produced non-deterministic bytes")
			}

			switch testCase.profile {
			case "zip_deflate_v1":
				reader, err := zip.NewReader(bytes.NewReader(outputs[0]), int64(len(outputs[0])))
				if err != nil {
					t.Fatal(err)
				}
				if len(reader.File) != 2 || reader.File[0].Name != "folder/asset.txt" || reader.File[0].Method != zip.Deflate ||
					reader.File[1].Name != archiveReportName {
					t.Fatalf("zip files=%+v", reader.File)
				}
				assertArchiveRoundTripMembers(
					t,
					readZIPArchiveMember(t, reader.File[0]),
					readZIPArchiveMember(t, reader.File[1]),
					payload,
					expectedReport,
				)
			case "tar_none_v1":
				assertTARArchiveRoundTrip(t, outputs[0], payload, expectedReport)
			case "tar_gzip_v1":
				gzipReader, err := gzip.NewReader(bytes.NewReader(outputs[0]))
				if err != nil {
					t.Fatal(err)
				}
				if !gzipReader.ModTime.IsZero() || gzipReader.Name != "" || gzipReader.Comment != "" ||
					len(gzipReader.Extra) != 0 || gzipReader.OS != 255 {
					t.Fatalf("gzip header=%+v", gzipReader.Header)
				}
				tarBytes, readErr := io.ReadAll(gzipReader)
				closeErr := gzipReader.Close()
				if readErr != nil || closeErr != nil {
					t.Fatal(errors.Join(readErr, closeErr))
				}
				assertTARArchiveRoundTrip(t, tarBytes, payload, expectedReport)
				var canonical bytes.Buffer
				writer, err := gzip.NewWriterLevel(&canonical, 6)
				if err != nil {
					t.Fatal(err)
				}
				writer.ModTime = time.Time{}
				writer.OS = 255
				if _, err := writer.Write(tarBytes); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(outputs[0], canonical.Bytes()) {
					t.Fatal("tar_gzip_v1 is not deterministic Go gzip level 6")
				}
			}
		})
	}
}

func TestWriteArchiveReportsPreHeaderFailedFileWithoutOpeningIt(t *testing.T) {
	selectionDigest := strings.Repeat("a", 64)
	opened := false
	var output bytes.Buffer
	report, err := WriteArchive(context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1, selectionDigest,
		archiveEntriesWithTestRootIdentity([]ArchiveEntry{
			{
				ItemID: "11111111111111111111111111111111", Components: []string{"failed.txt"},
				Type: backupasset.CatalogEntryFile, Size: 7, ProviderBytes: 7, PreHeaderFailure: "source_changed",
				Open: func(context.Context) (io.ReadCloser, error) {
					opened = true
					return nil, errors.New("failed file must not open")
				},
			},
			{
				ItemID: "22222222222222222222222222222222", Components: []string{"good.txt"},
				Type: backupasset.CatalogEntryFile, Size: 4,
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("good")), nil
				},
			},
		}), ArchiveLimits{MaxItems: 2, MaxLogicalBytes: 16, MaxProviderBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("pre-header failed file was opened")
	}
	if report.ResultKind != ResultPartial || report.Packed != 1 || report.Failed != 1 || report.ProviderBytes != 11 {
		t.Fatalf("report=%+v", report)
	}
	if got := report.Items[0]; got.State != ItemFailed || got.MemberPath != "" || got.LogicalBytes != 0 ||
		got.ProviderBytes != 7 || got.ErrorCategory != "source_changed" {
		t.Fatalf("failed item report=%+v", got)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 || reader.File[0].Name != "good.txt" || reader.File[1].Name != archiveReportName {
		t.Fatalf("zip members=%+v", reader.File)
	}
}

func TestWriteArchiveConvertsItemBoundOpenFailureToPreHeaderPartial(t *testing.T) {
	selectionDigest := strings.Repeat("b", 64)
	failedItemID := "33333333333333333333333333333333"
	var output bytes.Buffer
	report, err := WriteArchive(context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1, selectionDigest,
		archiveEntriesWithTestRootIdentity([]ArchiveEntry{
			{
				ItemID: failedItemID, Components: []string{"reopen-failed.txt"},
				Type: backupasset.CatalogEntryFile, Size: 7, ProviderBytes: 9, ProviderEvidence: true,
				Open: func(context.Context) (io.ReadCloser, error) {
					return nil, newPreHeaderSpoolFailureAfterAuthentication(
						errors.Join(ErrStoreObjectAbsent, os.ErrNotExist), failedItemID, 9,
					)
				},
			},
			{
				ItemID: "44444444444444444444444444444444", Components: []string{"good.txt"},
				Type: backupasset.CatalogEntryFile, Size: 4,
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("good")), nil
				},
			},
		}), ArchiveLimits{MaxItems: 2, MaxLogicalBytes: 16, MaxProviderBytes: 20})
	if err != nil {
		t.Fatal(err)
	}
	if report.ResultKind != ResultPartial || report.Packed != 1 || report.Failed != 1 || report.ProviderBytes != 13 {
		t.Fatalf("report=%+v", report)
	}
	var failed ArchiveItemReport
	for _, item := range report.Items {
		if item.ItemID == failedItemID {
			failed = item
			break
		}
	}
	if failed.State != ItemFailed || failed.MemberPath != "" || failed.LogicalBytes != 0 ||
		failed.ProviderBytes != 9 || failed.ErrorCategory != "internal_failure" || !failed.preHeaderSpoolRecovered {
		t.Fatalf("reopen-failed item report=%+v", failed)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 || reader.File[0].Name != "good.txt" || reader.File[1].Name != archiveReportName {
		t.Fatalf("zip members=%+v", reader.File)
	}
}

func readZIPArchiveMember(t *testing.T, file *zip.File) []byte {
	t.Helper()
	stream, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal(errors.Join(readErr, closeErr))
	}
	return value
}

func assertArchiveRoundTripMembers(
	t *testing.T,
	regularMember []byte,
	reportMember []byte,
	expectedPayload []byte,
	expectedReport ArchiveReport,
) {
	t.Helper()
	if !bytes.Equal(regularMember, expectedPayload) {
		t.Fatalf("regular member=%q want %q", regularMember, expectedPayload)
	}
	var report ArchiveReport
	if err := json.Unmarshal(reportMember, &report); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, expectedReport) {
		t.Fatalf("embedded report=%+v want %+v", report, expectedReport)
	}
}

func assertTARArchiveRoundTrip(
	t *testing.T,
	value []byte,
	expectedPayload []byte,
	expectedReport ArchiveReport,
) {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(value))
	names := make([]string, 0, 2)
	var regularMember []byte
	var reportMember []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		member, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		switch header.Name {
		case "folder/asset.txt":
			regularMember = member
		case archiveReportName:
			reportMember = member
		}
	}
	if got, want := strings.Join(names, ","), "folder/asset.txt,"+archiveReportName; got != want {
		t.Fatalf("tar names=%q want %q", got, want)
	}
	assertArchiveRoundTripMembers(t, regularMember, reportMember, expectedPayload, expectedReport)
}

func TestCloseArchiveLayersClosesTARBeforeGzipAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("tar close failed")
	secondErr := errors.New("gzip close failed")
	order := make([]string, 0, 2)
	err := closeArchiveLayers(
		&recordingArchiveCloser{name: "tar", order: &order, err: firstErr},
		&recordingArchiveCloser{name: "gzip", order: &order, err: secondErr},
	)
	if got := strings.Join(order, ","); got != "tar,gzip" {
		t.Fatalf("close order=%q want tar,gzip", got)
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("close error=%v, want both layer errors", err)
	}
}

type recordingArchiveCloser struct {
	name  string
	order *[]string
	err   error
}

func (closer *recordingArchiveCloser) Close() error {
	*closer.order = append(*closer.order, closer.name)
	return closer.err
}

func TestWriteArchiveZIPSkipsLinksWithoutReadingAndReportsPartial(t *testing.T) {
	readCount := 0
	entries := []ArchiveEntry{
		{ItemID: "11111111111111111111111111111111", Components: []string{"folder"}, Type: backupasset.CatalogEntryDirectory},
		{ItemID: "22222222222222222222222222222222", Components: []string{"folder", "ok.txt"}, Type: backupasset.CatalogEntryFile,
			Size: 5, ModifiedAt: time.Now().UTC().Truncate(time.Second), Open: func(context.Context) (io.ReadCloser, error) {
				readCount++
				return io.NopCloser(strings.NewReader("hello")), nil
			}},
		{ItemID: "33333333333333333333333333333333", Components: []string{"link"}, Type: backupasset.CatalogEntrySymlink,
			Open: func(context.Context) (io.ReadCloser, error) {
				readCount++
				return io.NopCloser(strings.NewReader("must-not-read")), nil
			}},
	}
	entries = archiveEntriesWithTestRootIdentity(entries)
	var output bytes.Buffer
	report, err := WriteArchive(context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1, strings.Repeat("a", 64), entries, ArchiveLimits{
		MaxItems: 10, MaxLogicalBytes: 100, MaxProviderBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ResultKind != ResultPartial || report.Packed != 2 || report.Skipped != 1 || readCount != 1 {
		t.Fatalf("report=%+v readCount=%d", report, readCount)
	}
	if report.Items[2].ErrorCategory != ItemErrorLinkMetadataUnavailable {
		t.Fatalf("link result=%+v", report.Items[2])
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	if strings.Join(names, ",") != "folder/,folder/ok.txt,xirang-export-report.v1.json" {
		t.Fatalf("zip names=%v", names)
	}
}

func TestWriteArchiveDirectoryReportMatchesEmittedMember(t *testing.T) {
	entries := []ArchiveEntry{{
		ItemID:     "11111111111111111111111111111111",
		Components: []string{"folder"},
		Type:       backupasset.CatalogEntryDirectory,
	}}
	entries = archiveEntriesWithTestRootIdentity(entries)
	for _, testCase := range []struct {
		name    string
		format  ArchiveFormat
		profile string
	}{
		{name: "ZIP", format: ArchiveZIP, profile: ArchiveProfileZIPDeflateV1},
		{name: "TAR", format: ArchiveTAR, profile: ArchiveProfileTARNoneV1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var output bytes.Buffer
			reported, err := WriteArchive(
				context.Background(), &output, testCase.format, testCase.profile,
				strings.Repeat("a", 64), entries,
				ArchiveLimits{MaxItems: 2, MaxLogicalBytes: 1, MaxProviderBytes: 1},
			)
			if err != nil {
				t.Fatal(err)
			}

			names, embedded := readArchivePathMembers(t, testCase.format, output.Bytes())
			if !reflect.DeepEqual(reported, embedded) {
				t.Fatalf("returned report=%+v embedded report=%+v", reported, embedded)
			}
			if got, want := strings.Join(names, ","), "folder/,"+archiveReportName; got != want {
				t.Fatalf("archive members=%q want %q", got, want)
			}
			if got, want := embedded.Items[0].MemberPath, names[0]; got != want {
				t.Fatalf("directory report member=%q want emitted member %q", got, want)
			}
		})
	}
}

func TestSanitizeArchiveComponentsRejectsUnsafeInputs(t *testing.T) {
	for _, components := range [][]string{
		{".."},
		{"a/b"},
		{"NUL\x00name"},
		{"\u2060ignored.txt"},
	} {
		if _, err := SanitizeArchiveComponents(components); !errors.Is(err, ErrArchiveSource) {
			t.Fatalf("components=%q err=%v", components, err)
		}
	}
}

func TestSanitizeArchiveComponentsRejectsWindowsUnsafeColon(t *testing.T) {
	for _, component := range []string{"safe:stream", "safe\uff1astream"} {
		t.Run(component, func(t *testing.T) {
			if _, err := SanitizeArchiveComponents([]string{component}); !errors.Is(err, ErrArchiveSource) {
				t.Fatalf("component=%q err=%v", component, err)
			}
		})
	}
}

func TestSanitizeArchiveComponentsContractVectors(t *testing.T) {
	for _, test := range []struct {
		name      string
		component string
		want      string
		wantErr   bool
	}{
		{name: "absolute path", component: "/var/lib/archive", wantErr: true},
		{name: "UNC path", component: `\\server\share`, wantErr: true},
		{name: "drive root", component: "C:", wantErr: true},
		{name: "normalized drive root", component: "\uff23\uff1a", wantErr: true},
		{name: "control character", component: "archive\x01.txt", wantErr: true},
		{name: "invalid UTF-8", component: string([]byte{0xff}), wantErr: true},
		{name: "trailing dot and space", component: "archive.txt. ", want: "archive.txt"},
		{name: "NFKC NUL device", component: "\uff2e\uff35\uff2c.tar.gz", want: "NUL_.tar.gz"},
		{name: "NFKC COM device", component: "\uff23\uff2f\uff2d\uff11.log", want: "COM1_.log"},
		{name: "legal filename", component: "safe-file_1.tar.gz", want: "safe-file_1.tar.gz"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := SanitizeArchiveComponents([]string{test.component})
			if test.wantErr {
				if !errors.Is(err, ErrArchiveSource) {
					t.Fatalf("component=%q err=%v", test.component, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("component=%q err=%v want %q", got, err, test.want)
			}
		})
	}
}

func TestSanitizeArchiveComponentsNormalizesWindowsReservedMultiExtensionBasenames(t *testing.T) {
	for _, test := range []struct {
		component string
		want      string
	}{
		{component: "NUL.tar.gz", want: "NUL_.tar.gz"},
		{component: "nul.tar.gz", want: "nul_.tar.gz"},
		{component: "archive.tar.gz", want: "archive.tar.gz"},
	} {
		t.Run(test.component, func(t *testing.T) {
			if got, err := SanitizeArchiveComponents([]string{test.component}); err != nil || got != test.want {
				t.Fatalf("component=%q err=%v want %q", got, err, test.want)
			}
		})
	}
}

func TestWriteArchiveNormalizesReservedMultiExtensionMember(t *testing.T) {
	entry := ArchiveEntry{
		ItemID:     "11111111111111111111111111111111",
		Components: []string{"NUL.tar.gz"},
		Type:       backupasset.CatalogEntryFile,
		Size:       1,
		Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("x")), nil
		},
	}
	entry = archiveEntriesWithTestRootIdentity([]ArchiveEntry{entry})[0]
	var output bytes.Buffer
	report, err := WriteArchive(
		context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1,
		strings.Repeat("a", 64), []ArchiveEntry{entry},
		ArchiveLimits{MaxItems: 2, MaxLogicalBytes: 2, MaxProviderBytes: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	names, embedded := readArchivePathMembers(t, ArchiveZIP, output.Bytes())
	if got, want := strings.Join(names, ","), "NUL_.tar.gz,"+archiveReportName; got != want {
		t.Fatalf("archive members=%q want %q", got, want)
	}
	if got, want := report.Items[0].MemberPath, "NUL_.tar.gz"; got != want {
		t.Fatalf("returned report member=%q want %q", got, want)
	}
	if got, want := embedded.Items[0].MemberPath, "NUL_.tar.gz"; got != want {
		t.Fatalf("embedded report member=%q want %q", got, want)
	}
}

func TestWriteArchiveDisambiguatesNFKCCasefoldCollisions(t *testing.T) {
	entries := []ArchiveEntry{
		{ItemID: "11111111111111111111111111111111", Components: []string{"Ａ.txt"}, Type: backupasset.CatalogEntryFile,
			Size: 1, Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a")), nil }},
		{ItemID: "22222222222222222222222222222222", Components: []string{"a.txt"}, Type: backupasset.CatalogEntryFile,
			Size: 1, Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("b")), nil }},
	}
	entries = archiveEntriesWithTestRootIdentity(entries)
	var output bytes.Buffer
	if _, err := WriteArchive(context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1, strings.Repeat("a", 64), entries, ArchiveLimits{
		MaxItems: 4, MaxLogicalBytes: 4, MaxProviderBytes: 4,
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	if got, want := strings.Join(names, ","), "A.txt,a~1.txt,xirang-export-report.v1.json"; got != want {
		t.Fatalf("collision-safe names=%q want %q", got, want)
	}
}

func TestWriteArchiveBoundsCrossRootScopePathsAndPreservesZIPTARMapping(t *testing.T) {
	component := strings.Repeat("界", 85) // 255 UTF-8 bytes, but still valid UTF-8.
	components := make([]string, maxArchivePathDepth)
	for index := range components {
		components[index] = component
	}
	entries := []ArchiveEntry{
		{
			ItemID: "11111111111111111111111111111111", Components: append([]string(nil), components...),
			RecoveryPointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EntryID: "entry-a",
			SelectionRootOrdinal: 4, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a")), nil },
		},
		{
			ItemID: "22222222222222222222222222222222", Components: append([]string(nil), components...),
			RecoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EntryID: "entry-b",
			SelectionRootOrdinal: 9, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("b")), nil },
		},
	}

	forward := writeArchivePathMapping(t, entries)
	reversed := append([]ArchiveEntry(nil), entries...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reversedMapping := writeArchivePathMapping(t, reversed)
	if !reflect.DeepEqual(forward.zip.report, forward.tar.report) ||
		!reflect.DeepEqual(forward.zip.report, reversedMapping.zip.report) {
		t.Fatalf("ZIP/TAR or reversed reports differ: zip=%+v tar=%+v reversed=%+v", forward.zip.report, forward.tar.report, reversedMapping.zip.report)
	}
	if !reflect.DeepEqual(forward.zip.pathsByItem, forward.tar.pathsByItem) ||
		!reflect.DeepEqual(forward.zip.pathsByItem, reversedMapping.zip.pathsByItem) {
		t.Fatalf("ZIP/TAR or reversed member mappings differ: zip=%v tar=%v reversed=%v", forward.zip.pathsByItem, forward.tar.pathsByItem, reversedMapping.zip.pathsByItem)
	}

	for _, entry := range entries {
		member := forward.zip.pathsByItem[entry.ItemID]
		assertBoundArchiveMemberPath(t, member)
		scope := archiveRootScope(entry)
		if !strings.HasPrefix(member, scope+"/") {
			t.Fatalf("member=%q does not retain cross-root scope %q", member, scope)
		}
	}
}

func TestWriteArchiveScopesCrossRootCanonicalAncestorCollisionsBeforeSuffixing(t *testing.T) {
	entries := []ArchiveEntry{
		{
			ItemID: "11111111111111111111111111111111", Components: []string{"Ａ"},
			RecoveryPointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EntryID: "entry-a",
			SelectionRootOrdinal: 4, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a")), nil },
		},
		{
			ItemID: "22222222222222222222222222222222", Components: []string{"a", "child.txt"},
			RecoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EntryID: "entry-b",
			SelectionRootOrdinal: 9, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("b")), nil },
		},
		{
			ItemID: "33333333333333333333333333333333", Components: []string{"a", "nested", "leaf.txt"},
			RecoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EntryID: "entry-c",
			SelectionRootOrdinal: 9, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("c")), nil },
		},
	}

	forward := writeArchivePathMapping(t, entries)
	reversed := []ArchiveEntry{entries[2], entries[1], entries[0]}
	reversedMapping := writeArchivePathMapping(t, reversed)
	if !reflect.DeepEqual(forward.zip.report, reversedMapping.zip.report) ||
		!reflect.DeepEqual(forward.zip.pathsByItem, reversedMapping.zip.pathsByItem) {
		t.Fatalf("reversed archive mapping changed: forward=%v reversed=%v", forward.zip.pathsByItem, reversedMapping.zip.pathsByItem)
	}

	want := map[string]string{
		entries[0].ItemID: "rp-aaaaaaaa/root-4/A",
		entries[1].ItemID: "rp-bbbbbbbb/root-9/a/child.txt",
		entries[2].ItemID: "rp-bbbbbbbb/root-9/a/nested/leaf.txt",
	}
	if !reflect.DeepEqual(forward.zip.pathsByItem, want) {
		t.Fatalf("cross-root ancestor paths=%v want %v", forward.zip.pathsByItem, want)
	}
	for _, member := range forward.zip.pathsByItem {
		assertBoundArchiveMemberPath(t, member)
		if strings.Contains(member, "~") {
			t.Fatalf("cross-root ancestor collision was suffixed instead of scoped: %q", member)
		}
	}
}

func TestWriteArchiveScopesCrossRootDenseAncestorGroupsWithoutRescanning(t *testing.T) {
	const denseGroupSize = 64
	const ancestorScope = "rp-aaaaaaaa/root-4"
	const descendantScope = "rp-bbbbbbbb/root-9"

	prepared := make([]preparedArchiveEntry, 0, denseGroupSize*2)
	for index := 0; index < denseGroupSize; index++ {
		prepared = append(prepared, preparedArchiveEntry{
			baseCollision: "ancestor",
			rootScope:     ancestorScope,
		})
	}
	for index := 0; index < denseGroupSize; index++ {
		prepared = append(prepared, preparedArchiveEntry{
			baseCollision: "ancestor/child-" + strings.Repeat("a", index+1),
			rootScope:     descendantScope,
		})
	}

	classifier := archivePathScopeClassifier{}
	classifier.classify(prepared)
	if got, want := classifier.entryFlagApplications, len(prepared); got != want {
		t.Fatalf("scope flag applications=%d want %d; dense ancestor group was rescanned per descendant", got, want)
	}
	for index, entry := range prepared {
		if !entry.scopeCrossRootCollision {
			t.Fatalf("prepared[%d] was not scoped", index)
		}
	}
}

func TestPrepareArchiveEntriesBoundsAllocatorWorkFor100KUniquePaths(t *testing.T) {
	const entryCount = 100_000
	entries := make([]ArchiveEntry, 0, entryCount)
	for index := 0; index < entryCount; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID:     fmt.Sprintf("%032x", index+1),
			Components: []string{"unique", fmt.Sprintf("%05d.txt", index)},
			Type:       backupasset.CatalogEntryFile,
		})
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	stats := &archivePathAllocationStats{maxWork: entryCount * 16}
	prepared, err := prepareArchiveEntriesWithStats(entries, stats)
	if err != nil {
		t.Fatalf("prepare unique paths: %v (visits=%d probes=%d limit=%d)", err, stats.visits, stats.probes, stats.maxWork)
	}
	if len(prepared) != entryCount {
		t.Fatalf("prepared entries=%d want %d", len(prepared), entryCount)
	}
	for index, entry := range prepared {
		want := fmt.Sprintf("unique/%05d.txt", index)
		if entry.path != want {
			t.Fatalf("prepared[%d].path=%q want %q", index, entry.path, want)
		}
	}
}

func TestPrepareArchiveEntriesBoundsAllocatorWorkForDenseExactAndAncestorGroups(t *testing.T) {
	const groupSize = 256
	const recoveryPointID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	entries := make([]ArchiveEntry, 0, groupSize*2+1)
	for index := 0; index < groupSize; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID: fmt.Sprintf("%032x", index+1), Components: []string{"same.txt"},
			RecoveryPointID: recoveryPointID, EntryID: fmt.Sprintf("same-%04d", index),
			SelectionRootOrdinal: 2, Type: backupasset.CatalogEntryFile,
		})
	}
	entries = append(entries, ArchiveEntry{
		ItemID: fmt.Sprintf("%032x", groupSize+1), Components: []string{"Ａ"},
		RecoveryPointID: recoveryPointID, EntryID: "ancestor",
		SelectionRootOrdinal: 2, Type: backupasset.CatalogEntryFile,
	})
	for index := 0; index < groupSize; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID: fmt.Sprintf("%032x", groupSize+index+2), Components: []string{"a", "CHILD.txt"},
			RecoveryPointID: recoveryPointID, EntryID: fmt.Sprintf("child-%04d", index),
			SelectionRootOrdinal: 2, Type: backupasset.CatalogEntryFile,
		})
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	stats := &archivePathAllocationStats{maxWork: len(entries) * 32}
	prepared, err := prepareArchiveEntriesWithStats(entries, stats)
	if err != nil {
		t.Fatalf("prepare dense paths: %v (visits=%d probes=%d limit=%d)", err, stats.visits, stats.probes, stats.maxWork)
	}
	seen := make(map[string]struct{}, len(prepared))
	for _, entry := range prepared {
		key := workerCapabilities.CanonicalNFKCCasefold(entry.path)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate final collision key %q for path %q", key, entry.path)
		}
		seen[key] = struct{}{}
	}
}

func TestPrepareArchiveEntriesBoundsAllocatorWorkForTruncatedSuffixSeries(t *testing.T) {
	const groupSize = 64
	prefix := strings.Repeat("p", maxArchiveComponentSize-2)
	entries := make([]ArchiveEntry, 0, groupSize*2)
	for index := 0; index < groupSize; index++ {
		suffix := string([]byte{
			byte('A' + index/26),
			byte('A' + index%26),
		})
		entries = append(entries,
			ArchiveEntry{
				ItemID: fmt.Sprintf("%032x", index*2+1), Components: []string{prefix + suffix},
				Type: backupasset.CatalogEntryFile,
			},
			ArchiveEntry{
				ItemID: fmt.Sprintf("%032x", index*2+2), Components: []string{prefix + strings.ToLower(suffix)},
				Type: backupasset.CatalogEntryFile,
			},
		)
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	stats := &archivePathAllocationStats{maxWork: len(entries) * 32}
	prepared, err := prepareArchiveEntriesWithStats(entries, stats)
	if err != nil {
		t.Fatalf("prepare truncated suffix paths: %v (visits=%d probes=%d limit=%d)", err, stats.visits, stats.probes, stats.maxWork)
	}
	seen := make(map[string]struct{}, len(prepared))
	for _, entry := range prepared {
		key := workerCapabilities.CanonicalNFKCCasefold(entry.path)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate final collision key %q for path %q", key, entry.path)
		}
		seen[key] = struct{}{}
	}
}

func TestPrepareArchiveEntriesAllocatesFirstComponentSuffixesWithoutGaps(t *testing.T) {
	entries := []ArchiveEntry{
		{ItemID: "11111111111111111111111111111111", Components: []string{"A"}, Type: backupasset.CatalogEntryFile},
		{ItemID: "22222222222222222222222222222222", Components: []string{"a", "B"}, Type: backupasset.CatalogEntryFile},
		{ItemID: "33333333333333333333333333333333", Components: []string{"a", "b"}, Type: backupasset.CatalogEntryFile},
		{ItemID: "44444444444444444444444444444444", Components: []string{"a", "Ｂ"}, Type: backupasset.CatalogEntryFile},
		{ItemID: "55555555555555555555555555555555", Components: []string{"a", "ｂ"}, Type: backupasset.CatalogEntryFile},
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	prepared, err := prepareArchiveEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	pathsByItem := make(map[string]string, len(prepared))
	for _, entry := range prepared {
		pathsByItem[entry.entry.ItemID] = entry.path
	}
	want := map[string]string{
		entries[0].ItemID: "A",
		entries[1].ItemID: "a~1/B",
		entries[2].ItemID: "a~2/b",
		entries[3].ItemID: "a~3/B",
		entries[4].ItemID: "a~4/b",
	}
	if !reflect.DeepEqual(pathsByItem, want) {
		t.Fatalf("first-component suffix paths=%v want %v", pathsByItem, want)
	}
}

func TestArchiveSuffixSeriesKeepsFirstAndLastProgramsSeparate(t *testing.T) {
	first := archiveSuffixSeriesKey("a.txt", backupasset.CatalogEntryFile, 0, 1)
	last := archiveSuffixSeriesKey("a.txt", backupasset.CatalogEntryFile, -1, 1)
	if first == last {
		t.Fatal("first- and last-component suffix programs shared successor state")
	}
}

func TestPrepareArchiveEntriesTransitionsFirstComponentSuffixesAtTen(t *testing.T) {
	entries := make([]ArchiveEntry, 0, 11)
	entries = append(entries, ArchiveEntry{
		ItemID: "11111111111111111111111111111111", Components: []string{"A"}, Type: backupasset.CatalogEntryFile,
	})
	for index := 1; index <= 10; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID: fmt.Sprintf("%032x", index+1), Components: []string{"a", "child.txt"}, Type: backupasset.CatalogEntryFile,
		})
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	prepared, err := prepareArchiveEntries(entries)
	if err != nil {
		t.Fatalf("allocate first-component suffixes: %v", err)
	}
	pathsByItem := make(map[string]string, len(prepared))
	for _, entry := range prepared {
		pathsByItem[entry.entry.ItemID] = entry.path
	}
	if got, want := pathsByItem[entries[10].ItemID], "a~10/child.txt"; got != want {
		t.Fatalf("first-component suffix ten path=%q want %q", got, want)
	}
}

func TestPrepareArchiveEntriesBoundsFirstComponentSuffixWorkAcrossDistinctDescendantTails(t *testing.T) {
	const caseVariantCount = 63
	entries := make([]ArchiveEntry, 0, caseVariantCount*2+1)
	entries = append(entries, ArchiveEntry{
		ItemID: "11111111111111111111111111111111", Components: []string{"aaaaaa"}, Type: backupasset.CatalogEntryFile,
	})
	for index := 0; index < caseVariantCount; index++ {
		variant := []byte("aaaaaa")
		for offset := range variant {
			if index&(1<<offset) != 0 {
				variant[offset] = variant[offset] - ('a' - 'A')
			}
		}
		entries = append(entries, ArchiveEntry{
			ItemID:     fmt.Sprintf("%032x", index+2),
			Components: []string{string(variant), "CHILD"},
			Type:       backupasset.CatalogEntryFile,
		})
	}
	for index := 0; index < caseVariantCount; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID:     fmt.Sprintf("%032x", caseVariantCount+index+2),
			Components: []string{"aaaaaa", "child", fmt.Sprintf("leaf-%03d.txt", index)},
			Type:       backupasset.CatalogEntryFile,
		})
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	stats := &archivePathAllocationStats{maxWork: len(entries) * 32}
	prepared, err := prepareArchiveEntriesWithStats(entries, stats)
	if err != nil {
		t.Fatalf("prepare distinct descendant suffix paths: %v (visits=%d probes=%d limit=%d)", err, stats.visits, stats.probes, stats.maxWork)
	}
	seen := make(map[string]struct{}, len(prepared))
	for _, entry := range prepared {
		key := workerCapabilities.CanonicalNFKCCasefold(entry.path)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate final collision key %q for path %q", key, entry.path)
		}
		seen[key] = struct{}{}
	}
}

func TestPrepareArchiveEntriesBoundsFirstComponentPrefixWorkAcrossDistinctDescendants(t *testing.T) {
	const blockerCount = 96
	entries := make([]ArchiveEntry, 0, blockerCount*2+1)
	for index := 0; index <= blockerCount; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID: fmt.Sprintf("%032x", index+1), Components: []string{"a"}, Type: backupasset.CatalogEntryFile,
		})
	}
	for index := 0; index < blockerCount; index++ {
		entries = append(entries, ArchiveEntry{
			ItemID:     fmt.Sprintf("%032x", blockerCount+index+2),
			Components: []string{"a", fmt.Sprintf("child-%03d.txt", index)}, Type: backupasset.CatalogEntryFile,
		})
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	stats := &archivePathAllocationStats{maxWork: len(entries) * 20}
	prepared, err := prepareArchiveEntriesWithStats(entries, stats)
	if err != nil {
		t.Fatalf("prepare first-component prefix paths: %v (visits=%d probes=%d limit=%d)", err, stats.visits, stats.probes, stats.maxWork)
	}
	seen := make(map[string]struct{}, len(prepared))
	for _, entry := range prepared {
		key := workerCapabilities.CanonicalNFKCCasefold(entry.path)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate final collision key %q for path %q", key, entry.path)
		}
		seen[key] = struct{}{}
	}
}

func TestPrepareArchiveEntriesTransitionsTruncatedSuffixSeriesAtTen(t *testing.T) {
	component := strings.Repeat("A", maxArchiveComponentSize)
	entries := make([]ArchiveEntry, 0, 12)
	for index := 0; index < cap(entries); index++ {
		entries = append(entries, ArchiveEntry{
			ItemID: fmt.Sprintf("%032x", index+1), Components: []string{component}, Type: backupasset.CatalogEntryFile,
		})
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	prepared, err := prepareArchiveEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	pathsByItem := make(map[string]string, len(prepared))
	for _, entry := range prepared {
		pathsByItem[entry.entry.ItemID] = entry.path
	}
	if got, want := pathsByItem[entries[9].ItemID], strings.Repeat("A", maxArchiveComponentSize-2)+"~9"; got != want {
		t.Fatalf("suffix nine path=%q want %q", got, want)
	}
	if got, want := pathsByItem[entries[10].ItemID], strings.Repeat("A", maxArchiveComponentSize-3)+"~10"; got != want {
		t.Fatalf("suffix ten path=%q want %q", got, want)
	}
	if got, want := pathsByItem[entries[11].ItemID], strings.Repeat("A", maxArchiveComponentSize-3)+"~11"; got != want {
		t.Fatalf("suffix eleven path=%q want %q", got, want)
	}
}

func TestPrepareArchiveEntriesRekeysTruncatedSuffixSeriesAtTen(t *testing.T) {
	const duplicatesPerBase = 11
	prefix := strings.Repeat("A", maxArchiveComponentSize-3)
	variants := []byte("0123456789!#$%&'()+,-;=@[]^_`{|}~")
	entries := make([]ArchiveEntry, 0, len(variants)*duplicatesPerBase)
	itemID := 1
	for _, variant := range variants {
		component := prefix + string(variant) + "xy"
		for duplicate := 0; duplicate < duplicatesPerBase; duplicate++ {
			entries = append(entries, ArchiveEntry{
				ItemID: fmt.Sprintf("%032x", itemID), Components: []string{component}, Type: backupasset.CatalogEntryFile,
			})
			itemID++
		}
	}
	entries = archiveEntriesWithTestRootIdentity(entries)

	stats := &archivePathAllocationStats{maxWork: len(entries) * 49 / 10}
	prepared, err := prepareArchiveEntriesWithStats(entries, stats)
	if err != nil {
		t.Fatalf("prepare truncated suffix cache: %v (visits=%d probes=%d limit=%d)", err, stats.visits, stats.probes, stats.maxWork)
	}
	seen := make(map[string]struct{}, len(prepared))
	for _, entry := range prepared {
		key := workerCapabilities.CanonicalNFKCCasefold(entry.path)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate final collision key %q for path %q", key, entry.path)
		}
		seen[key] = struct{}{}
	}
}

func TestPrepareArchiveEntriesRejectsCrossRootCollisionWithoutScopeIdentity(t *testing.T) {
	entries := []ArchiveEntry{
		{
			ItemID: "11111111111111111111111111111111", Components: []string{"Ａ"},
			RecoveryPointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EntryID: "entry-a",
			SelectionRootOrdinal: 4, Type: backupasset.CatalogEntryFile,
		},
		{
			ItemID: "22222222222222222222222222222222", Components: []string{"a", "child.txt"},
			Type: backupasset.CatalogEntryFile,
		},
	}

	if _, err := prepareArchiveEntries(entries); !errors.Is(err, ErrArchiveSource) {
		t.Fatalf("cross-root collision without scope identity err=%v want ErrArchiveSource", err)
	}
}

func TestPrepareArchiveEntriesRejectsInvalidRootIdentityWithoutCollision(t *testing.T) {
	valid := ArchiveEntry{
		ItemID:               "11111111111111111111111111111111",
		Components:           []string{"entry.txt"},
		RecoveryPointID:      strings.Repeat("a", 32),
		EntryID:              strings.Repeat("b", 64),
		SelectionRootOrdinal: 0,
		Type:                 backupasset.CatalogEntryFile,
	}
	for _, test := range []struct {
		name   string
		mutate func(*ArchiveEntry)
	}{
		{name: "missing recovery point", mutate: func(entry *ArchiveEntry) { entry.RecoveryPointID = "" }},
		{name: "invalid entry", mutate: func(entry *ArchiveEntry) { entry.EntryID = "not-an-entry-id" }},
		{name: "negative root ordinal", mutate: func(entry *ArchiveEntry) { entry.SelectionRootOrdinal = -1 }},
		{name: "uppercase recovery point", mutate: func(entry *ArchiveEntry) { entry.RecoveryPointID = strings.ToUpper(entry.RecoveryPointID) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := valid
			test.mutate(&entry)
			if _, err := prepareArchiveEntries([]ArchiveEntry{entry}); !errors.Is(err, ErrArchiveSource) {
				t.Fatalf("prepare invalid root identity error=%v, want ErrArchiveSource", err)
			}
		})
	}
}

func TestWriteArchiveAllocatorPreservesFullRootIdentityAndComponentBoundaries(t *testing.T) {
	entries := []ArchiveEntry{
		{
			ItemID: "11111111111111111111111111111111", Components: []string{"Ａ"},
			RecoveryPointID: "aaaaaaaa111111111111111111111111", EntryID: "entry-a",
			SelectionRootOrdinal: 4, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a")), nil },
		},
		{
			ItemID: "22222222222222222222222222222222", Components: []string{"a", "child.txt"},
			RecoveryPointID: "aaaaaaaa222222222222222222222222", EntryID: "entry-b",
			SelectionRootOrdinal: 4, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("b")), nil },
		},
		{
			ItemID: "33333333333333333333333333333333", Components: []string{"ab", "child.txt"},
			RecoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EntryID: "entry-c",
			SelectionRootOrdinal: 9, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("c")), nil },
		},
	}

	forward := writeArchivePathMapping(t, entries)
	reversed := []ArchiveEntry{entries[2], entries[1], entries[0]}
	reversedMapping := writeArchivePathMapping(t, reversed)
	if !reflect.DeepEqual(forward.zip.report, forward.tar.report) ||
		!reflect.DeepEqual(forward.zip.report, reversedMapping.zip.report) {
		t.Fatalf("ZIP/TAR or reversed reports differ: zip=%+v tar=%+v reversed=%+v", forward.zip.report, forward.tar.report, reversedMapping.zip.report)
	}
	if !reflect.DeepEqual(forward.zip.pathsByItem, forward.tar.pathsByItem) ||
		!reflect.DeepEqual(forward.zip.pathsByItem, reversedMapping.zip.pathsByItem) {
		t.Fatalf("ZIP/TAR or reversed member mappings differ: zip=%v tar=%v reversed=%v", forward.zip.pathsByItem, forward.tar.pathsByItem, reversedMapping.zip.pathsByItem)
	}

	want := map[string]string{
		entries[0].ItemID: "rp-aaaaaaaa111111111111111111111111/root-4/A",
		entries[1].ItemID: "rp-aaaaaaaa222222222222222222222222/root-4/a/child.txt",
		entries[2].ItemID: "ab/child.txt",
	}
	if !reflect.DeepEqual(forward.zip.pathsByItem, want) {
		t.Fatalf("full root identity paths=%v want %v", forward.zip.pathsByItem, want)
	}
}

func TestWriteArchiveLeavesCrossRootDirectoryAncestorUnscoped(t *testing.T) {
	entries := []ArchiveEntry{
		{
			ItemID: "11111111111111111111111111111111", Components: []string{"Ａ"},
			RecoveryPointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EntryID: "directory",
			SelectionRootOrdinal: 4, Type: backupasset.CatalogEntryDirectory,
		},
		{
			ItemID: "22222222222222222222222222222222", Components: []string{"a", "child.txt"},
			RecoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", EntryID: "child",
			SelectionRootOrdinal: 9, Type: backupasset.CatalogEntryFile, Size: 1,
			Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("b")), nil },
		},
	}

	forward := writeArchivePathMapping(t, entries)
	reversed := []ArchiveEntry{entries[1], entries[0]}
	reversedMapping := writeArchivePathMapping(t, reversed)
	if !reflect.DeepEqual(forward.zip.report, forward.tar.report) ||
		!reflect.DeepEqual(forward.zip.report, reversedMapping.zip.report) {
		t.Fatalf("ZIP/TAR or reversed reports differ: zip=%+v tar=%+v reversed=%+v", forward.zip.report, forward.tar.report, reversedMapping.zip.report)
	}
	if !reflect.DeepEqual(forward.zip.pathsByItem, forward.tar.pathsByItem) ||
		!reflect.DeepEqual(forward.zip.pathsByItem, reversedMapping.zip.pathsByItem) {
		t.Fatalf("ZIP/TAR or reversed member mappings differ: zip=%v tar=%v reversed=%v", forward.zip.pathsByItem, forward.tar.pathsByItem, reversedMapping.zip.pathsByItem)
	}

	want := map[string]string{
		entries[0].ItemID: "A/",
		entries[1].ItemID: "a/child.txt",
	}
	if !reflect.DeepEqual(forward.zip.pathsByItem, want) {
		t.Fatalf("directory ancestor paths=%v want %v", forward.zip.pathsByItem, want)
	}
}

func TestWriteArchiveBoundsCollisionSuffixesAcrossPathShapes(t *testing.T) {
	tests := []struct {
		name       string
		upper      string
		lower      string
		wantSuffix string
	}{
		{
			name:  "dotfile extension consumes component",
			upper: "." + strings.Repeat("A", 254), lower: "." + strings.Repeat("a", 254),
			wantSuffix: "~1",
		},
		{
			name:  "one byte basename huge extension",
			upper: "x." + strings.Repeat("A", 253), lower: "x." + strings.Repeat("a", 253),
			wantSuffix: "~1",
		},
		{
			name:  "UTF-8 basename near byte limit",
			upper: strings.Repeat("界", 83) + "A.txt", lower: strings.Repeat("界", 83) + "a.txt",
			wantSuffix: "~1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := []ArchiveEntry{
				{
					ItemID: "11111111111111111111111111111111", Components: []string{test.upper},
					Type: backupasset.CatalogEntryFile, Size: 1,
					Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a")), nil },
				},
				{
					ItemID: "22222222222222222222222222222222", Components: []string{test.lower},
					Type: backupasset.CatalogEntryFile, Size: 1,
					Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("b")), nil },
				},
			}
			forward := writeArchivePathMapping(t, entries)
			reversed := []ArchiveEntry{entries[1], entries[0]}
			reversedMapping := writeArchivePathMapping(t, reversed)
			if !reflect.DeepEqual(forward.zip.report, forward.tar.report) ||
				!reflect.DeepEqual(forward.zip.report, reversedMapping.zip.report) {
				t.Fatalf("ZIP/TAR or reversed reports differ: zip=%+v tar=%+v reversed=%+v", forward.zip.report, forward.tar.report, reversedMapping.zip.report)
			}
			if !reflect.DeepEqual(forward.zip.pathsByItem, forward.tar.pathsByItem) ||
				!reflect.DeepEqual(forward.zip.pathsByItem, reversedMapping.zip.pathsByItem) {
				t.Fatalf("ZIP/TAR or reversed member mappings differ: zip=%v tar=%v reversed=%v", forward.zip.pathsByItem, forward.tar.pathsByItem, reversedMapping.zip.pathsByItem)
			}
			for _, member := range forward.zip.pathsByItem {
				assertBoundArchiveMemberPath(t, member)
			}
			paths := forward.zip.pathsByItem
			if !strings.Contains(paths[entries[1].ItemID], test.wantSuffix) {
				t.Fatalf("collision member=%q does not contain stable suffix %q", paths[entries[1].ItemID], test.wantSuffix)
			}
		})
	}
}

type archivePathMappingSet struct {
	zip archivePathMapping
	tar archivePathMapping
}

type archivePathMapping struct {
	report      ArchiveReport
	pathsByItem map[string]string
}

func writeArchivePathMapping(t *testing.T, entries []ArchiveEntry) archivePathMappingSet {
	t.Helper()
	entries = archiveEntriesWithTestRootIdentity(entries)
	write := func(format ArchiveFormat, profile string) archivePathMapping {
		var output bytes.Buffer
		report, err := WriteArchive(context.Background(), &output, format, profile, strings.Repeat("a", 64), entries, ArchiveLimits{
			MaxItems: 8, MaxLogicalBytes: 32, MaxProviderBytes: 32,
		})
		if err != nil {
			t.Fatalf("format=%s write archive: %v", format, err)
		}
		names, embedded := readArchivePathMembers(t, format, output.Bytes())
		if !reflect.DeepEqual(report, embedded) {
			t.Fatalf("format=%s returned/embedded report differ: returned=%+v embedded=%+v", format, report, embedded)
		}
		pathsByItem := make(map[string]string, len(report.Items))
		expectedNames := make([]string, 0, len(report.Items)+1)
		for _, item := range report.Items {
			pathsByItem[item.ItemID] = item.MemberPath
			expectedNames = append(expectedNames, item.MemberPath)
		}
		expectedNames = append(expectedNames, archiveReportName)
		if !reflect.DeepEqual(names, expectedNames) {
			t.Fatalf("format=%s physical names=%v report names=%v", format, names, expectedNames)
		}
		return archivePathMapping{report: report, pathsByItem: pathsByItem}
	}
	return archivePathMappingSet{
		zip: write(ArchiveZIP, ArchiveProfileZIPDeflateV1),
		tar: write(ArchiveTAR, ArchiveProfileTARNoneV1),
	}
}

func readArchivePathMembers(t *testing.T, format ArchiveFormat, value []byte) ([]string, ArchiveReport) {
	t.Helper()
	names := make([]string, 0)
	var report ArchiveReport
	readReport := func(name string, reader io.Reader) {
		if name != archiveReportName {
			return
		}
		if err := json.NewDecoder(reader).Decode(&report); err != nil {
			t.Fatalf("decode report: %v", err)
		}
	}
	switch format {
	case ArchiveZIP:
		archive, err := zip.NewReader(bytes.NewReader(value), int64(len(value)))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range archive.File {
			stream, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, file.Name)
			readReport(file.Name, stream)
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
		}
	case ArchiveTAR:
		reader := tar.NewReader(bytes.NewReader(value))
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, header.Name)
			readReport(header.Name, reader)
		}
	default:
		t.Fatalf("unsupported test format %q", format)
	}
	return names, report
}

func assertBoundArchiveMemberPath(t *testing.T, member string) {
	t.Helper()
	components := strings.Split(member, "/")
	if len(components) > maxArchivePathDepth {
		t.Fatalf("member depth=%d exceeds %d: %q", len(components), maxArchivePathDepth, member)
	}
	if len(member) > maxArchiveMemberSize {
		t.Fatalf("member bytes=%d exceeds %d: %q", len(member), maxArchiveMemberSize, member)
	}
	for _, component := range components {
		if len(component) > maxArchiveComponentSize {
			t.Fatalf("component bytes=%d exceeds %d: %q", len(component), maxArchiveComponentSize, component)
		}
		if !utf8.ValidString(component) {
			t.Fatalf("component is invalid UTF-8: %q", component)
		}
	}
}

func TestWriteArchiveReservesInternalReportPath(t *testing.T) {
	entries := []ArchiveEntry{
		{ItemID: "11111111111111111111111111111111", Components: []string{"xirang-export-report.v1.json"}, Type: backupasset.CatalogEntryFile,
			Size: 5, Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("asset")), nil }},
	}
	entries = archiveEntriesWithTestRootIdentity(entries)
	var output bytes.Buffer
	if _, err := WriteArchive(context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1, strings.Repeat("a", 64), entries, ArchiveLimits{
		MaxItems: 2, MaxLogicalBytes: 8, MaxProviderBytes: 8,
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	if got, want := strings.Join(names, ","), "xirang-export-report.v1~1.json,xirang-export-report.v1.json"; got != want {
		t.Fatalf("reserved report path names=%q want %q", got, want)
	}
}

func TestWriteArchiveBindsFrozenSelectionDigestInReport(t *testing.T) {
	selectionDigest := strings.Repeat("a", 64)
	entries := []ArchiveEntry{
		{ItemID: "11111111111111111111111111111111", Components: []string{"asset.txt"}, Type: backupasset.CatalogEntryFile,
			Size: 5, Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("asset")), nil }},
	}
	entries = archiveEntriesWithTestRootIdentity(entries)
	var output bytes.Buffer
	if _, err := WriteArchive(context.Background(), &output, ArchiveZIP, ArchiveProfileZIPDeflateV1, selectionDigest, entries, ArchiveLimits{
		MaxItems: 2, MaxLogicalBytes: 8, MaxProviderBytes: 8,
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != archiveReportName {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() {
			if closeErr := stream.Close(); closeErr != nil {
				t.Errorf("close report stream: %v", closeErr)
			}
		}()
		var report ArchiveReport
		if decodeErr := json.NewDecoder(stream).Decode(&report); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if report.SelectionDigest != selectionDigest {
			t.Fatalf("report selection digest=%q want %q", report.SelectionDigest, selectionDigest)
		}
		return
	}
	t.Fatalf("%s missing", archiveReportName)
}

func TestWriteArchiveFailsClosedOnProviderByteLimit(t *testing.T) {
	entry := ArchiveEntry{
		ItemID: "44444444444444444444444444444444", Components: []string{"large.bin"},
		Type: backupasset.CatalogEntryFile, Size: 8,
		Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("12345678")), nil },
	}
	entry = archiveEntriesWithTestRootIdentity([]ArchiveEntry{entry})[0]
	var output bytes.Buffer
	if _, err := WriteArchive(context.Background(), &output, ArchiveTAR, ArchiveProfileTARNoneV1, strings.Repeat("a", 64), []ArchiveEntry{entry}, ArchiveLimits{
		MaxItems: 2, MaxLogicalBytes: 16, MaxProviderBytes: 4,
	}); err == nil {
		t.Fatal("provider byte limit unexpectedly succeeded")
	}
}
