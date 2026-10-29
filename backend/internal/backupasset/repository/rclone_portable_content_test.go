package repository

import (
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestRclonePortableContentTupleSealsManifestProjection(t *testing.T) {
	modified := time.Date(2026, 7, 16, 1, 0, 1, 123456789, time.UTC)
	record := exactContentRecord{
		entry: model.CatalogEntry{NormalizedPath: "dir/file", Name: "file", EntryType: string(backupasset.CatalogEntryFile),
			Size: 5, ModifiedAt: &modified, Mode: "100640", Owner: "1000:1000", Fingerprint: "hash", FingerprintStrength: "strong"},
		locator: provider.EntryLocator{Native: "portable:dir/file"},
	}
	proof := provider.CatalogRecord{NormalizedPath: "dir/file", ParentNormalizedPath: "dir", Name: "file",
		Type: backupasset.CatalogEntryFile, Size: 5, ModifiedAt: &modified, Mode: "100640", Owner: "1000:1000",
		Fingerprint: "hash", FingerprintStrength: "strong", ProviderLocator: record.locator}
	if !portableContentRecordMatchesCatalog(proof, record) {
		t.Fatal("matching sealed tuple was rejected")
	}
	otherTime := modified.Add(time.Nanosecond)
	changes := map[string]func(*provider.CatalogRecord){
		"path":         func(value *provider.CatalogRecord) { value.NormalizedPath = "dir/other" },
		"parent":       func(value *provider.CatalogRecord) { value.ParentNormalizedPath = "other" },
		"name":         func(value *provider.CatalogRecord) { value.Name = "other" },
		"kind":         func(value *provider.CatalogRecord) { value.Type = backupasset.CatalogEntryDirectory },
		"size":         func(value *provider.CatalogRecord) { value.Size++ },
		"time":         func(value *provider.CatalogRecord) { value.ModifiedAt = &otherTime },
		"missing_time": func(value *provider.CatalogRecord) { value.ModifiedAt = nil },
		"mode":         func(value *provider.CatalogRecord) { value.Mode = "100600" },
		"owner":        func(value *provider.CatalogRecord) { value.Owner = "1001:1000" },
		"fingerprint":  func(value *provider.CatalogRecord) { value.Fingerprint = "other" },
		"strength":     func(value *provider.CatalogRecord) { value.FingerprintStrength = "none" },
		"locator":      func(value *provider.CatalogRecord) { value.ProviderLocator.Native = "portable:dir/other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			change(&candidate)
			if portableContentRecordMatchesCatalog(candidate, record) {
				t.Fatal("changed sealed tuple accepted")
			}
		})
	}
}

func TestRclonePortableContentV1AndV2TimeChecksRemainExact(t *testing.T) {
	logical := time.Date(2026, 7, 16, 1, 0, 1, 123456789, time.UTC)
	physical := logical.Truncate(time.Second)
	record := exactContentRecord{entry: model.CatalogEntry{Size: 5, ModifiedAt: &logical}}
	entry := provider.Entry{Type: backupasset.CatalogEntryFile, Size: 5, ModTime: physical, SourceRevision: "revision"}
	opened := provider.ContentStat{Size: 5, ModTime: physical, SourceRevision: "revision"}
	if validateContentEntryStat(record, entry, false) == nil || validateContentOpenStat(record, entry, opened, false) == nil {
		t.Fatal("historical V1 ignored exact Catalog time")
	}
	record.physicalModTime = &physical
	if err := validateContentEntryStat(record, entry, false); err != nil {
		t.Fatal(err)
	}
	if err := validateContentOpenStat(record, entry, opened, false); err != nil {
		t.Fatal(err)
	}
	entry.ModTime = physical.Add(time.Second)
	opened.ModTime = entry.ModTime
	if validateContentEntryStat(record, entry, false) == nil || validateContentOpenStat(record, entry, opened, false) == nil {
		t.Fatal("V2 accepted same-size destination time drift")
	}
	zero := time.Time{}
	record.exactModTime = true
	record.physicalModTime = &zero
	if validateContentEntryStat(record, entry, false) == nil || validateContentOpenStat(record, entry, opened, false) == nil {
		t.Fatal("V2 ignored an authenticated zero destination time")
	}
	record.physicalModTime = nil
	record.entry.ModifiedAt = &zero
	if validateContentEntryStat(record, entry, false) == nil || validateContentOpenStat(record, entry, opened, false) == nil {
		t.Fatal("V1 ignored an authenticated zero Catalog time")
	}
}
