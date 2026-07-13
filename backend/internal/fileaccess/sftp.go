package fileaccess

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/sftp"
)

type SFTPFile interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	Stat() (os.FileInfo, error)
	Close() error
}

type SFTPBackend interface {
	RealPath(string) (string, error)
	Lstat(string) (os.FileInfo, error)
	Open(string) (SFTPFile, error)
}

type EnumerationLimits struct {
	MaxItems int
	MaxBytes int64
}

type DirectoryEnumerator interface {
	Bounded() bool
	Enumerate(context.Context, string, EnumerationLimits) ([]string, error)
}

type SFTPTree struct {
	backend       SFTPBackend
	enumerator    DirectoryEnumerator
	closeOnCancel func() error
}

func NewSFTPTree(backend SFTPBackend, enumerator DirectoryEnumerator, closeOnCancel func() error) *SFTPTree {
	return &SFTPTree{backend: backend, enumerator: enumerator, closeOnCancel: closeOnCancel}
}

func NewSFTPClientTree(client *sftp.Client, enumerator DirectoryEnumerator, closeOnCancel func() error) *SFTPTree {
	if client == nil {
		return NewSFTPTree(nil, enumerator, closeOnCancel)
	}
	return NewSFTPTree(sftpClientBackend{client: client}, enumerator, closeOnCancel)
}

func NewSFTPCompatibilityEnumerator(client *sftp.Client) DirectoryEnumerator {
	return sftpCompatibilityEnumerator{client: client}
}

func (tree *SFTPTree) List(ctx context.Context, root Root, locator Locator, policy Policy, request PageRequest) (EntryPage, error) {
	if err := contextError(ctx); err != nil {
		return EntryPage{}, err
	}
	if tree == nil || tree.backend == nil {
		return EntryPage{}, ErrStrictUnavailable
	}
	request, err := request.Normalize()
	if err != nil {
		return EntryPage{}, err
	}
	if tree.enumerator == nil || (policy == ProviderPolicy && !tree.enumerator.Bounded()) {
		return EntryPage{}, ErrStrictUnavailable
	}
	_, candidate, canonicalRoot, err := tree.resolve(root, locator, policy)
	if err != nil {
		return EntryPage{}, err
	}
	candidateInfo, err := tree.backend.Lstat(candidate)
	if err != nil {
		return EntryPage{}, err
	}
	if policy == ProviderPolicy && candidateInfo.Mode()&os.ModeSymlink != 0 {
		return EntryPage{}, ErrSymlinkDenied
	}
	candidateSnapshot := snapshotFileInfo(candidateInfo)
	resolved, err := tree.backend.RealPath(candidate)
	if err != nil || !Contains(canonicalRoot, resolved) {
		return EntryPage{}, ErrOutsideRoot
	}
	info, err := tree.backend.Lstat(resolved)
	if err != nil {
		return EntryPage{}, err
	}
	if !info.IsDir() {
		return EntryPage{}, ErrNotDirectory
	}
	targetSnapshot := snapshotFileInfo(info)
	names, err := tree.enumerator.Enumerate(ctx, resolved, EnumerationLimits{MaxItems: request.MaxItems, MaxBytes: request.MaxBytes})
	if err != nil {
		return EntryPage{}, err
	}
	if len(names) > request.MaxItems {
		return EntryPage{}, ErrResourceLimit
	}
	collector := newEntryCollector(request)
	seen := make(map[string]struct{}, len(names))
	entrySnapshots := make(map[string]fileSnapshot, len(names))
	var nameBytes int64
	for _, name := range names {
		if err := contextError(ctx); err != nil {
			return EntryPage{}, err
		}
		nameBytes += int64(len(name))
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\x00/\\") || nameBytes > request.MaxBytes {
			return EntryPage{}, ErrResourceLimit
		}
		if _, duplicate := seen[name]; duplicate {
			return EntryPage{}, ErrSourceChanged
		}
		seen[name] = struct{}{}
		entryInfo, statErr := tree.backend.Lstat(filepath.Join(resolved, name))
		if statErr != nil {
			return EntryPage{}, statErr
		}
		entrySnapshots[name] = snapshotFileInfo(entryInfo)
		entryLocator := Locator{Path: filepath.Join(candidate, name)}
		if policy == ProviderPolicy {
			relative, relErr := filepath.Rel(canonicalRoot, filepath.Join(resolved, name))
			if relErr != nil {
				return EntryPage{}, ErrOutsideRoot
			}
			entryLocator = Locator{Path: relative}
		}
		if err := collector.Add(entryFromFileInfo(name, entryLocator, entryInfo)); err != nil {
			return EntryPage{}, err
		}
	}
	for _, entry := range collector.items {
		current, statErr := tree.backend.Lstat(filepath.Join(resolved, entry.Name))
		if statErr != nil || snapshotFileInfo(current) != entrySnapshots[entry.Name] {
			return EntryPage{}, ErrSourceChanged
		}
	}
	postCandidate, candidateErr := tree.backend.Lstat(candidate)
	postResolved, resolvedErr := tree.backend.RealPath(candidate)
	postTarget, targetErr := tree.backend.Lstat(resolved)
	if candidateErr != nil || resolvedErr != nil || targetErr != nil || postResolved != resolved ||
		!Contains(canonicalRoot, postResolved) || snapshotFileInfo(postCandidate) != candidateSnapshot ||
		snapshotFileInfo(postTarget) != targetSnapshot {
		return EntryPage{}, ErrSourceChanged
	}
	return collector.Page(), nil
}

func (tree *SFTPTree) Lstat(ctx context.Context, root Root, locator Locator, policy Policy) (Entry, error) {
	if err := contextError(ctx); err != nil {
		return Entry{}, err
	}
	_, candidate, canonicalRoot, err := tree.resolve(root, locator, policy)
	if err != nil {
		return Entry{}, err
	}
	info, err := tree.backend.Lstat(candidate)
	if err != nil {
		return Entry{}, err
	}
	candidateSnapshot := snapshotFileInfo(info)
	resolved, err := tree.backend.RealPath(candidate)
	if err != nil || !Contains(canonicalRoot, resolved) {
		return Entry{}, ErrOutsideRoot
	}
	target, err := tree.backend.Lstat(resolved)
	if err != nil {
		return Entry{}, err
	}
	targetSnapshot := snapshotFileInfo(target)
	postCandidate, candidateErr := tree.backend.Lstat(candidate)
	postResolved, resolvedErr := tree.backend.RealPath(candidate)
	postTarget, targetErr := tree.backend.Lstat(resolved)
	if candidateErr != nil || resolvedErr != nil || targetErr != nil || postResolved != resolved ||
		!Contains(canonicalRoot, postResolved) || snapshotFileInfo(postCandidate) != candidateSnapshot ||
		snapshotFileInfo(postTarget) != targetSnapshot {
		return Entry{}, ErrSourceChanged
	}
	return entryFromFileInfo(filepath.Base(candidate), locator, info), nil
}

func (tree *SFTPTree) OpenRegular(ctx context.Context, root Root, locator Locator, policy Policy) (ReadHandle, ContentStat, error) {
	return tree.open(ctx, root, locator, policy, nil)
}

func (tree *SFTPTree) OpenRange(ctx context.Context, root Root, locator Locator, policy Policy, byteRange ByteRange) (ReadHandle, ContentStat, error) {
	if err := byteRange.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	return tree.open(ctx, root, locator, policy, &byteRange)
}

func (tree *SFTPTree) open(ctx context.Context, root Root, locator Locator, policy Policy, byteRange *ByteRange) (ReadHandle, ContentStat, error) {
	if err := contextError(ctx); err != nil {
		return nil, ContentStat{}, err
	}
	if tree == nil || tree.backend == nil {
		return nil, ContentStat{}, ErrStrictUnavailable
	}
	_, candidate, canonicalRoot, err := tree.resolve(root, locator, policy)
	if err != nil {
		return nil, ContentStat{}, err
	}
	preLink, err := tree.backend.Lstat(candidate)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if policy == ProviderPolicy && preLink.Mode()&os.ModeSymlink != 0 {
		return nil, ContentStat{}, ErrSymlinkDenied
	}
	preResolved, err := tree.backend.RealPath(candidate)
	if err != nil || !Contains(canonicalRoot, preResolved) {
		return nil, ContentStat{}, ErrOutsideRoot
	}
	preTarget, err := tree.backend.Lstat(preResolved)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if !preTarget.Mode().IsRegular() {
		return nil, ContentStat{}, ErrNotRegular
	}
	initial := snapshotFileInfo(preTarget)
	file, err := tree.backend.Open(candidate)
	if err != nil {
		return nil, ContentStat{}, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ContentStat{}, err
	}
	postResolved, err := tree.backend.RealPath(candidate)
	if err != nil || !Contains(canonicalRoot, postResolved) || postResolved != preResolved {
		_ = file.Close()
		return nil, ContentStat{}, ErrSourceChanged
	}
	postTarget, err := tree.backend.Lstat(postResolved)
	if err != nil || snapshotFileInfo(openedInfo) != initial || snapshotFileInfo(postTarget) != initial {
		_ = file.Close()
		return nil, ContentStat{}, ErrSourceChanged
	}
	var reader io.Reader = file
	if byteRange != nil {
		reader = io.NewSectionReader(file, byteRange.Offset, byteRange.Length)
	}
	postCheck := func() error {
		currentResolved, checkErr := tree.backend.RealPath(candidate)
		if checkErr != nil || currentResolved != preResolved || !Contains(canonicalRoot, currentResolved) {
			return ErrSourceChanged
		}
		current, checkErr := tree.backend.Lstat(currentResolved)
		if checkErr != nil || snapshotFileInfo(current) != initial {
			return ErrSourceChanged
		}
		return nil
	}
	return newSFTPReadHandle(ctx, file, reader, postCheck, tree.closeOnCancel), contentStatFromFileInfo(openedInfo), nil
}

func (tree *SFTPTree) resolve(root Root, locator Locator, policy Policy) (string, string, string, error) {
	if tree == nil || tree.backend == nil {
		return "", "", "", ErrStrictUnavailable
	}
	canonicalRoot, err := tree.backend.RealPath(filepath.Clean(root.Path))
	if err != nil {
		return "", "", "", err
	}
	relative, candidate, err := Resolve(Root{Path: canonicalRoot}, locator, policy)
	if err != nil {
		return "", "", "", err
	}
	return relative, candidate, filepath.Clean(canonicalRoot), nil
}

type sftpReadHandle struct {
	file          SFTPFile
	reader        io.Reader
	ctx           context.Context
	postCheck     func() error
	closeOnCancel func() error
	done          chan struct{}
	once          sync.Once
	closeErr      error
}

func newSFTPReadHandle(ctx context.Context, file SFTPFile, reader io.Reader, postCheck func() error, closeOnCancel func() error) *sftpReadHandle {
	handle := &sftpReadHandle{file: file, reader: reader, ctx: ctx, postCheck: postCheck, closeOnCancel: closeOnCancel, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			handle.finish(ctx.Err(), true)
		case <-handle.done:
		}
	}()
	return handle
}

func (handle *sftpReadHandle) Read(buffer []byte) (int, error) {
	if err := contextError(handle.ctx); err != nil {
		return 0, err
	}
	return handle.reader.Read(buffer)
}

func (handle *sftpReadHandle) Close() error {
	handle.finish(nil, false)
	return handle.closeErr
}

func (handle *sftpReadHandle) finish(cause error, canceled bool) {
	handle.once.Do(func() {
		defer close(handle.done)
		if cause == nil && handle.postCheck != nil {
			handle.closeErr = handle.postCheck()
		} else {
			handle.closeErr = cause
		}
		if err := handle.file.Close(); handle.closeErr == nil && err != nil {
			handle.closeErr = err
		}
		if canceled && handle.closeOnCancel != nil {
			_ = handle.closeOnCancel()
		}
	})
}

type sftpClientBackend struct{ client *sftp.Client }

func (backend sftpClientBackend) RealPath(path string) (string, error) {
	return backend.client.RealPath(path)
}
func (backend sftpClientBackend) Lstat(path string) (os.FileInfo, error) {
	return backend.client.Lstat(path)
}
func (backend sftpClientBackend) Open(path string) (SFTPFile, error) {
	return backend.client.Open(path)
}

type sftpCompatibilityEnumerator struct{ client *sftp.Client }

func (sftpCompatibilityEnumerator) Bounded() bool { return false }
func (enumerator sftpCompatibilityEnumerator) Enumerate(ctx context.Context, directory string, limits EnumerationLimits) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if enumerator.client == nil {
		return nil, ErrStrictUnavailable
	}
	infos, err := enumerator.client.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	if len(infos) > limits.MaxItems {
		return nil, ErrResourceLimit
	}
	names := make([]string, 0, len(infos))
	var total int64
	for _, info := range infos {
		total += int64(len(info.Name()))
		if total > limits.MaxBytes {
			return nil, ErrResourceLimit
		}
		names = append(names, info.Name())
	}
	return names, nil
}

var _ SFTPBackend = sftpClientBackend{}
var _ DirectoryEnumerator = sftpCompatibilityEnumerator{}
