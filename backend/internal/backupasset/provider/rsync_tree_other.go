//go:build !linux

package provider

func openRsyncManagedTree(string) (*rsyncManagedTree, error) {
	return nil, errRsyncManagedTreeUnsupported
}

func openRsyncManagedTreeReadOnly(string) (*rsyncManagedTree, error) {
	return nil, errRsyncManagedTreeUnsupported
}

func (*rsyncManagedTree) Close() error                    { return nil }
func (*rsyncManagedTree) VerifyRootIdentity() error       { return errRsyncManagedTreeUnsupported }
func (*rsyncManagedTree) CreateFreshStaging(string) error { return errRsyncManagedTreeUnsupported }
func (*rsyncManagedTree) VerifyFreshStaging(string) error { return errRsyncManagedTreeUnsupported }
func (*rsyncManagedTree) CreateFreshStagingTree(string) error {
	return errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) openStagingTree(string) (int, error) {
	return -1, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) openFinalTree(string) (int, error) {
	return -1, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) stagingTreePath(string) (string, error) {
	return "", errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) finalTreePath(string) (string, error) {
	return "", errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) WriteStagingMetadata(string, string, []byte) error {
	return errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) readFinalMetadata(string, string) ([]byte, error) {
	return nil, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) finalComponentExists(string) (bool, error) {
	return false, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) readStagingMetadata(string, string) ([]byte, error) {
	return nil, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) FsyncStagingTree(string) error      { return errRsyncManagedTreeUnsupported }
func (*rsyncManagedTree) CommitStaging(string, string) error { return errRsyncManagedTreeUnsupported }
func (*rsyncManagedTree) ProbeCommitPrimitives() (rsyncTreeCommitProbe, error) {
	return rsyncTreeCommitProbe{}, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) readRepositoryMarker() ([]byte, error) {
	return nil, errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) validateLocalSourceRoot(string) (string, error) {
	return "", errRsyncManagedTreeUnsupported
}
func (*rsyncManagedTree) capacitySnapshot() (rsyncTreeCapacitySnapshot, error) {
	return rsyncTreeCapacitySnapshot{}, errRsyncManagedTreeUnsupported
}
