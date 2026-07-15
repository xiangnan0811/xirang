package provider

import (
	"fmt"

	"xirang/backend/internal/backupasset"
)

func validateRsyncTreeFullCopyFidelity(manifest rsyncTreeManifest) error {
	if manifest.DigestAlgorithm != "sha256" || !validRsyncTreeDigest(manifest.Digest) || manifest.EntryCount != uint64(len(manifest.Entries)) {
		return fmt.Errorf("%w: invalid full-copy manifest evidence", backupasset.ErrInvalidState)
	}
	counts := make(map[rsyncTreeInode]uint64)
	for _, entry := range manifest.Entries {
		if entry.Kind != rsyncTreeManifestRegular {
			continue
		}
		if entry.Device == 0 || entry.Inode == 0 || entry.Nlink == 0 || !validRsyncTreeDigest(entry.ContentDigest) {
			return fmt.Errorf("%w: incomplete full-copy inode evidence", errRsyncManagedTreeUnsafe)
		}
		counts[rsyncTreeInode{device: entry.Device, inode: entry.Inode}]++
	}
	for inode, inTreeCount := range counts {
		if expectedNlink := manifestNlinkForInode(manifest.Entries, inode); expectedNlink != inTreeCount {
			return fmt.Errorf("%w: full-copy tree shares an external inode", errRsyncManagedTreeUnsafe)
		}
	}
	return nil
}

func validateRsyncTreeHardlinkFidelity(parentBefore, parentAfter, candidate rsyncTreeManifest) error {
	if err := validateRsyncTreeManifestIdentity(parentBefore); err != nil {
		return err
	}
	if err := validateRsyncTreeManifestIdentity(parentAfter); err != nil {
		return err
	}
	if err := validateRsyncTreeManifestIdentity(candidate); err != nil {
		return err
	}
	if parentBefore.Digest != parentAfter.Digest || string(parentBefore.Encoded) != string(parentAfter.Encoded) {
		return fmt.Errorf("%w: hardlink parent tree changed during publication", errRsyncManagedTreeUnsafe)
	}
	parents := make(map[string]rsyncTreeManifestEntry, len(parentBefore.Entries))
	for _, entry := range parentBefore.Entries {
		parents[entry.RelativePath] = entry
	}
	for _, entry := range candidate.Entries {
		if entry.Kind != rsyncTreeManifestRegular {
			continue
		}
		if entry.Device == 0 || entry.Inode == 0 || entry.Nlink == 0 || !validRsyncTreeDigest(entry.ContentDigest) {
			return fmt.Errorf("%w: incomplete hardlink candidate inode evidence", errRsyncManagedTreeUnsafe)
		}
		parent, exists := parents[entry.RelativePath]
		if !exists || parent.Kind != rsyncTreeManifestRegular {
			continue
		}
		shared := entry.Device == parent.Device && entry.Inode == parent.Inode
		if rsyncTreeRegularEntriesEquivalent(parent, entry) && !shared {
			return fmt.Errorf("%w: hardlink publication copied an eligible parent file", errRsyncManagedTreeUnsafe)
		}
		if !rsyncTreeRegularEntriesEquivalent(parent, entry) && shared {
			return fmt.Errorf("%w: hardlink publication shared a changed parent file", errRsyncManagedTreeUnsafe)
		}
	}
	return nil
}

type rsyncTreeInode struct {
	device uint64
	inode  uint64
}

func manifestNlinkForInode(entries []rsyncTreeManifestEntry, target rsyncTreeInode) uint64 {
	for _, entry := range entries {
		if entry.Kind == rsyncTreeManifestRegular && entry.Device == target.device && entry.Inode == target.inode {
			return entry.Nlink
		}
	}
	return 0
}

func validateRsyncTreeManifestIdentity(manifest rsyncTreeManifest) error {
	if manifest.DigestAlgorithm != "sha256" || !validRsyncTreeDigest(manifest.Digest) || manifest.EntryCount != uint64(len(manifest.Entries)) {
		return fmt.Errorf("%w: invalid managed Rsync manifest", backupasset.ErrInvalidState)
	}
	return nil
}

func rsyncTreeRegularEntriesEquivalent(left, right rsyncTreeManifestEntry) bool {
	return left.Kind == rsyncTreeManifestRegular && right.Kind == rsyncTreeManifestRegular &&
		left.Mode == right.Mode && left.UID == right.UID && left.GID == right.GID && left.ModTimeNS == right.ModTimeNS &&
		left.Size == right.Size && left.ContentDigest == right.ContentDigest
}
