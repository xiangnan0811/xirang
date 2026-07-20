package capabilities

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

func TestDocumentPlanRejectsMalformedPDFMacroAndActiveContent(t *testing.T) {
	fixtures := []struct {
		name      string
		mediaType string
	}{
		{"malformed-document-truncated.pdf", "application/pdf"},
		{"office-macro.docm", "application/vnd.ms-word.document.macroenabled.12"},
		{"active-content.svg", "image/svg+xml"},
		{"active-content.html", "text/html"},
	}
	for _, testCase := range fixtures {
		if _, err := PlanDocument(readCapabilityFixture(t, testCase.name), testCase.mediaType); !errors.Is(err, capabilityspec.ErrUnsupportedMedia) && !errors.Is(err, ErrInvalidToolOutput) {
			t.Fatalf("fixture %s error=%v", testCase.name, err)
		}
	}
}

func TestDocumentPlanRejectsOOXMLExternalRelationshipBeforeLibreOffice(t *testing.T) {
	payload := makeDocumentZIP(t, []documentZIPEntry{
		{name: "[Content_Types].xml", content: `<Types/>`},
		{name: "word/_rels/document.xml.rels", content: `<Relationships><Relationship TargetMode="External" Target="https://FAKE_EXTERNAL_FOR_TEST_ONLY"/></Relationships>`},
	})
	if plan, err := PlanDocument(payload, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"); err == nil || plan.ExecutableID == ExecutableLibreOffice {
		t.Fatalf("external OOXML reached executable plan=%+v err=%v", plan, err)
	}
}

func TestDocumentPlanRejectsODFMacrosScriptsAndExternalXLinks(t *testing.T) {
	const mediaType = "application/vnd.oasis.opendocument.text"
	tests := []struct {
		name    string
		entries []documentZIPEntry
	}{
		{name: "scripts directory", entries: []documentZIPEntry{{name: "Scripts/python/start.py", content: "pass"}}},
		{name: "basic directory", entries: []documentZIPEntry{{name: "Basic/Standard/Module1.xml", content: `<module/>`}}},
		{name: "manifest script metadata", entries: []documentZIPEntry{{name: "META-INF/manifest.xml", content: `<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0"><manifest:file-entry manifest:full-path="Scripts/" manifest:media-type="application/binary"/></manifest:manifest>`}}},
		{name: "content external xlink", entries: []documentZIPEntry{{name: "content.xml", content: `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:xlink="http://www.w3.org/1999/xlink"><draw:image xlink:href="https://FAKE_EXTERNAL_FOR_TEST_ONLY"/></office:document-content>`}}},
		{name: "styles external xlink", entries: []documentZIPEntry{{name: "styles.xml", content: `<office:document-styles xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:xlink="http://www.w3.org/1999/xlink"><style:background-image xlink:href="file:///FAKE_EXTERNAL_FOR_TEST_ONLY"/></office:document-styles>`}}},
		{name: "settings external xlink", entries: []documentZIPEntry{{name: "settings.xml", content: `<office:document-settings xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:xlink="http://www.w3.org/1999/xlink"><config:item xlink:href="ftp://FAKE_EXTERNAL_FOR_TEST_ONLY"/></office:document-settings>`}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			entries := append([]documentZIPEntry{{name: "mimetype", content: mediaType, store: true}}, testCase.entries...)
			payload := makeDocumentZIP(t, entries)
			if plan, err := PlanDocument(payload, mediaType); err == nil || plan.ExecutableID == ExecutableLibreOffice {
				t.Fatalf("active ODF reached executable plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestDocumentPlanRejectsMalformedEncryptedDuplicateTraversalAndOversizedPackages(t *testing.T) {
	const ooxml = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	const odf = "application/vnd.oasis.opendocument.text"
	oversized := strings.Repeat("x", (1<<20)+1)
	tests := []struct {
		name      string
		mediaType string
		payload   func(*testing.T) []byte
	}{
		{name: "malformed", mediaType: ooxml, payload: func(*testing.T) []byte { return []byte{'P', 'K', 3, 4} }},
		{name: "encrypted", mediaType: ooxml, payload: func(t *testing.T) []byte {
			return markDocumentZIPEncrypted(t, makeDocumentZIP(t, []documentZIPEntry{{name: "[Content_Types].xml", content: `<Types/>`}}))
		}},
		{name: "duplicate", mediaType: ooxml, payload: func(t *testing.T) []byte {
			return makeDocumentZIP(t, []documentZIPEntry{{name: "[Content_Types].xml", content: `<Types/>`}, {name: "[Content_Types].xml", content: `<Types/>`}})
		}},
		{name: "traversal", mediaType: ooxml, payload: func(t *testing.T) []byte {
			return makeDocumentZIP(t, []documentZIPEntry{{name: "[Content_Types].xml", content: `<Types/>`}, {name: "../escape.xml", content: `<x/>`}})
		}},
		{name: "oversized allowlisted XML", mediaType: odf, payload: func(t *testing.T) []byte {
			return makeDocumentZIP(t, []documentZIPEntry{{name: "mimetype", content: odf, store: true}, {name: "content.xml", content: oversized}})
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if plan, err := PlanDocument(testCase.payload(t), testCase.mediaType); err == nil || plan.ExecutableID == ExecutableLibreOffice {
				t.Fatalf("unsafe package reached executable plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestDocumentPlanAllowsOnlySafeCanonicalOOXMLAndODF(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		entries   []documentZIPEntry
	}{
		{
			name: "safe OOXML", mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			entries: []documentZIPEntry{{name: "[Content_Types].xml", content: `<Types/>`}, {name: "_rels/.rels", content: `<Relationships/>`}, {name: "word/document.xml", content: `<document/>`}},
		},
		{
			name: "safe ODF", mediaType: "application/vnd.oasis.opendocument.text",
			entries: []documentZIPEntry{{name: "mimetype", content: "application/vnd.oasis.opendocument.text", store: true}, {name: "META-INF/manifest.xml", content: `<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0"/>`}, {name: "content.xml", content: `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"/>`}},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := PlanDocument(makeDocumentZIP(t, testCase.entries), testCase.mediaType)
			if err != nil || plan.ExecutableID != ExecutableLibreOffice || plan.ArgProfile != ArgsOfficePDF || len(plan.Warnings) != 0 {
				t.Fatalf("safe package plan=%+v err=%v", plan, err)
			}
		})
	}
}

type documentZIPEntry struct {
	name    string
	content string
	store   bool
}

func makeDocumentZIP(t *testing.T, entries []documentZIPEntry) []byte {
	t.Helper()
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.store {
			header.Method = zip.Store
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func markDocumentZIPEncrypted(t *testing.T, payload []byte) []byte {
	t.Helper()
	result := append([]byte(nil), payload...)
	local := bytes.Index(result, []byte{'P', 'K', 3, 4})
	central := bytes.Index(result, []byte{'P', 'K', 1, 2})
	if local < 0 || central < 0 {
		t.Fatal("ZIP headers missing")
	}
	result[local+6] |= 1
	result[central+8] |= 1
	return result
}
