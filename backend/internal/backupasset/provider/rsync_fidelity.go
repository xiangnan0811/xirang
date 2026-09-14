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

func validateRsyncTreeHardlinkFidelity(parentBefore, parentAfter, source, candidate rsyncTreeManifest) error {
	if err := validateRsyncTreeManifestIdentity(parentBefore); err != nil {
		return err
	}
	if err := validateRsyncTreeManifestIdentity(parentAfter); err != nil {
		return err
	}
	if err := validateRsyncTreeManifestIdentity(source); err != nil {
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
	sources := make(map[string]rsyncTreeManifestEntry, len(source.Entries))
	for _, entry := range source.Entries {
		sources[entry.RelativePath] = entry
	}
	candidates := make(map[string]rsyncTreeManifestEntry, len(candidate.Entries))
	for _, entry := range candidate.Entries {
		candidates[entry.RelativePath] = entry
	}
	if err := validateRsyncTreeHardlinkSourceCandidate(sources, candidates); err != nil {
		return err
	}
	parentGroups := rsyncTreeRegularInodeGroups(parentBefore)
	sourceGroups := rsyncTreeRegularInodeGroups(source)
	sourcePaths := rsyncTreeRegularPathSet(source)
	for _, sourceGroup := range sourceGroups {
		parentInode, compatible := rsyncTreeCompatibleParentGroup(parents, parentGroups, sourcePaths, sourceGroup)
		for _, sourceEntry := range sourceGroup {
			candidateEntry := candidates[sourceEntry.RelativePath]
			parentEntry, parentExists := parents[sourceEntry.RelativePath]
			shared := parentExists && parentEntry.Kind == rsyncTreeManifestRegular &&
				candidateEntry.Device == parentEntry.Device && candidateEntry.Inode == parentEntry.Inode
			if compatible {
				if !shared || candidateEntry.Device != parentInode.device || candidateEntry.Inode != parentInode.inode {
					return fmt.Errorf("%w: hardlink publication did not reuse compatible parent group at %s", errRsyncManagedTreeUnsafe, sourceEntry.RelativePath)
				}
			} else if shared {
				return fmt.Errorf("%w: hardlink publication reused an incompatible parent group at %s", errRsyncManagedTreeUnsafe, sourceEntry.RelativePath)
			}
		}
	}
	return nil
}

func validateRsyncTreeHardlinkSourceCandidate(sources, candidates map[string]rsyncTreeManifestEntry) error {
	sourceGroups := rsyncTreeRegularInodeGroupsFromEntries(sources)
	candidateGroups := rsyncTreeRegularInodeGroupsFromEntries(candidates)
	for path, sourceEntry := range sources {

		candidateEntry, exists := candidates[path]
		if !exists || candidateEntry.Kind != sourceEntry.Kind {
			return fmt.Errorf("%w: hardlink candidate entry set changed at %s", errRsyncManagedTreeUnsafe, path)
		}
		if sourceEntry.Kind != rsyncTreeManifestRegular {
			continue
		}
		if err := validateRsyncTreeHardlinkRegularEvidence(sourceEntry, "source"); err != nil {
			return err
		}
		if err := validateRsyncTreeHardlinkRegularEvidence(candidateEntry, "candidate"); err != nil {
			return err
		}
		if !rsyncTreeRegularEntriesEquivalent(sourceEntry, candidateEntry) {
			return fmt.Errorf("%w: hardlink candidate metadata changed at %s", errRsyncManagedTreeUnsafe, path)
		}
	}
	for path, candidateEntry := range candidates {
		if _, exists := sources[path]; !exists {
			return fmt.Errorf("%w: hardlink candidate added entry at %s", errRsyncManagedTreeUnsafe, path)
		}
		if candidateEntry.Kind == rsyncTreeManifestRegular {
			if err := validateRsyncTreeHardlinkRegularEvidence(candidateEntry, "candidate"); err != nil {
				return err
			}
		}
	}
	for sourceInode, sourceGroup := range sourceGroups {
		candidateInode := rsyncTreeInode{}
		for index, sourceEntry := range sourceGroup {
			entryInode := rsyncTreeInode{device: candidates[sourceEntry.RelativePath].Device, inode: candidates[sourceEntry.RelativePath].Inode}
			if index == 0 {
				candidateInode = entryInode
				continue
			}
			if entryInode != candidateInode {
				return fmt.Errorf("%w: hardlink candidate split source group device=%d inode=%d", errRsyncManagedTreeUnsafe, sourceInode.device, sourceInode.inode)
			}
		}
	}
	for candidateInode, candidateGroup := range candidateGroups {
		sourceInode := rsyncTreeInode{}
		for index, candidateEntry := range candidateGroup {
			entryInode := rsyncTreeInode{device: sources[candidateEntry.RelativePath].Device, inode: sources[candidateEntry.RelativePath].Inode}
			if index == 0 {
				sourceInode = entryInode
				continue
			}
			if entryInode != sourceInode {
				return fmt.Errorf("%w: hardlink candidate merged source groups into device=%d inode=%d", errRsyncManagedTreeUnsafe, candidateInode.device, candidateInode.inode)
			}
		}
	}
	return nil
}

func validateRsyncTreeHardlinkRegularEvidence(entry rsyncTreeManifestEntry, label string) error {
	if entry.Device == 0 || entry.Inode == 0 || entry.Nlink == 0 || !validRsyncTreeDigest(entry.ContentDigest) {
		return fmt.Errorf("%w: incomplete hardlink %s inode evidence", errRsyncManagedTreeUnsafe, label)
	}
	return nil
}

type rsyncTreeInode struct {
	device uint64
	inode  uint64
}

func rsyncTreeRegularInodeGroups(manifest rsyncTreeManifest) map[rsyncTreeInode][]rsyncTreeManifestEntry {
	groups := make(map[rsyncTreeInode][]rsyncTreeManifestEntry)
	for _, entry := range manifest.Entries {
		if entry.Kind != rsyncTreeManifestRegular {
			continue
		}
		inode := rsyncTreeInode{device: entry.Device, inode: entry.Inode}
		groups[inode] = append(groups[inode], entry)
	}
	return groups
}

func rsyncTreeRegularInodeGroupsFromEntries(entries map[string]rsyncTreeManifestEntry) map[rsyncTreeInode][]rsyncTreeManifestEntry {
	groups := make(map[rsyncTreeInode][]rsyncTreeManifestEntry)
	for _, entry := range entries {
		if entry.Kind != rsyncTreeManifestRegular {
			continue
		}
		inode := rsyncTreeInode{device: entry.Device, inode: entry.Inode}
		groups[inode] = append(groups[inode], entry)
	}
	return groups
}

func rsyncTreeRegularPathSet(manifest rsyncTreeManifest) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, entry := range manifest.Entries {
		if entry.Kind == rsyncTreeManifestRegular {
			paths[entry.RelativePath] = struct{}{}
		}
	}
	return paths
}

func rsyncTreeCompatibleParentGroup(
	parents map[string]rsyncTreeManifestEntry,
	parentGroups map[rsyncTreeInode][]rsyncTreeManifestEntry,
	stagedPaths map[string]struct{},
	stagedGroup []rsyncTreeManifestEntry,
) (rsyncTreeInode, bool) {
	if len(stagedGroup) == 0 {
		return rsyncTreeInode{}, false
	}
	stagedGroupPaths := make(map[string]struct{}, len(stagedGroup))
	var parentInode rsyncTreeInode
	for index, stagedEntry := range stagedGroup {
		stagedGroupPaths[stagedEntry.RelativePath] = struct{}{}
		parentEntry, exists := parents[stagedEntry.RelativePath]
		if !exists || !rsyncTreeRegularEntriesEquivalent(parentEntry, stagedEntry) {
			return rsyncTreeInode{}, false
		}
		inode := rsyncTreeInode{device: parentEntry.Device, inode: parentEntry.Inode}
		if index == 0 {
			parentInode = inode
			continue
		}
		if inode != parentInode {
			return rsyncTreeInode{}, false
		}
	}
	for _, parentEntry := range parentGroups[parentInode] {
		if _, present := stagedPaths[parentEntry.RelativePath]; !present {
			continue
		}
		if _, inGroup := stagedGroupPaths[parentEntry.RelativePath]; !inGroup {
			return rsyncTreeInode{}, false
		}
	}
	return parentInode, true
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
