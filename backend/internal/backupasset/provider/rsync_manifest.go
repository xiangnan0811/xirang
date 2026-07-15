package provider

type rsyncTreeManifestKind string

const (
	rsyncTreeManifestDirectory rsyncTreeManifestKind = "directory"
	rsyncTreeManifestRegular   rsyncTreeManifestKind = "regular"
	rsyncTreeManifestSymlink   rsyncTreeManifestKind = "symlink"
)

// rsyncTreeManifestEntry keeps filesystem identity private to the provider.
// Device, inode, and link count are used for fidelity validation but are not
// serialized into manifest.jsonl or any public DTO.
type rsyncTreeManifestEntry struct {
	RelativePath  string
	Kind          rsyncTreeManifestKind
	Mode          uint32
	UID           uint32
	GID           uint32
	ModTimeNS     int64
	Size          uint64
	ContentDigest string
	LinkTarget    string
	Device        uint64
	Inode         uint64
	Nlink         uint64
}

type rsyncTreeManifest struct {
	DigestAlgorithm string
	Digest          string
	EntryCount      uint64
	LogicalBytes    uint64
	Encoded         []byte
	Entries         []rsyncTreeManifestEntry
}

func validRsyncTreeManifestLimits(limits ManifestLimits) bool {
	return limits.Timeout > 0 && limits.MaxBytes > 0 && limits.MaxEntries > 0 && limits.MaxRecordBytes > 0 && limits.MaxDepth >= 0
}
