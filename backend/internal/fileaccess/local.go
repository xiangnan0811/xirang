package fileaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
)

type LocalTree struct {
	beforeOpen func()
}

func NewLocalTree() *LocalTree { return &LocalTree{} }

func (tree *LocalTree) List(ctx context.Context, root Root, locator Locator, policy Policy, request PageRequest) (EntryPage, error) {
	if err := contextError(ctx); err != nil {
		return EntryPage{}, err
	}
	request, err := request.Normalize()
	if err != nil {
		return EntryPage{}, err
	}
	relative, _, err := Resolve(root, locator, policy)
	if err != nil {
		return EntryPage{}, err
	}
	if policy == ProviderPolicy {
		strictRoot, err := openStrictRoot(root.Path)
		if err != nil {
			return EntryPage{}, err
		}
		defer func() { _ = strictRoot.Close() }()
		return strictRoot.List(ctx, relative, request)
	}
	if policy != LegacyPolicy {
		return EntryPage{}, fmt.Errorf("%w: unsupported local policy", ErrInvalidLocator)
	}
	openedRoot, err := os.OpenRoot(root.Path)
	if err != nil {
		return EntryPage{}, err
	}
	defer func() { _ = openedRoot.Close() }()
	directory, err := openedRoot.Open(relative)
	if err != nil {
		return EntryPage{}, err
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil {
		return EntryPage{}, err
	}
	if !info.IsDir() {
		return EntryPage{}, ErrNotDirectory
	}
	collector := newEntryCollector(request)
	for {
		if err := contextError(ctx); err != nil {
			return EntryPage{}, err
		}
		entries, readErr := directory.ReadDir(64)
		for _, directoryEntry := range entries {
			entryInfo, infoErr := directoryEntry.Info()
			if infoErr != nil {
				return EntryPage{}, infoErr
			}
			entry := entryFromFileInfo(directoryEntry.Name(), joinLocator(relative, directoryEntry.Name()), entryInfo)
			if err := collector.Add(entry); err != nil {
				return EntryPage{}, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return EntryPage{}, readErr
		}
	}
	return collector.Page(), nil
}

func (tree *LocalTree) Lstat(ctx context.Context, root Root, locator Locator, policy Policy) (Entry, error) {
	if err := contextError(ctx); err != nil {
		return Entry{}, err
	}
	relative, _, err := Resolve(root, locator, policy)
	if err != nil {
		return Entry{}, err
	}
	if policy == ProviderPolicy {
		strictRoot, err := openStrictRoot(root.Path)
		if err != nil {
			return Entry{}, err
		}
		defer func() { _ = strictRoot.Close() }()
		return strictRoot.Lstat(relative)
	}
	openedRoot, err := os.OpenRoot(root.Path)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = openedRoot.Close() }()
	info, err := openedRoot.Lstat(relative)
	if err != nil {
		return Entry{}, err
	}
	return entryFromFileInfo(filepath.Base(relative), Locator{Path: relative}, info), nil
}

func (tree *LocalTree) OpenRegular(ctx context.Context, root Root, locator Locator, policy Policy) (ReadHandle, ContentStat, error) {
	return tree.open(ctx, root, locator, policy, nil)
}

func (tree *LocalTree) OpenRange(ctx context.Context, root Root, locator Locator, policy Policy, byteRange ByteRange) (ReadHandle, ContentStat, error) {
	if err := byteRange.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	return tree.open(ctx, root, locator, policy, &byteRange)
}

func (tree *LocalTree) open(ctx context.Context, root Root, locator Locator, policy Policy, byteRange *ByteRange) (ReadHandle, ContentStat, error) {
	if err := contextError(ctx); err != nil {
		return nil, ContentStat{}, err
	}
	relative, _, err := Resolve(root, locator, policy)
	if err != nil {
		return nil, ContentStat{}, err
	}
	var file *os.File
	switch policy {
	case ProviderPolicy:
		strictRoot, openErr := openStrictRoot(root.Path)
		if openErr != nil {
			return nil, ContentStat{}, openErr
		}
		defer func() { _ = strictRoot.Close() }()
		if tree != nil && tree.beforeOpen != nil {
			tree.beforeOpen()
		}
		file, err = strictRoot.OpenRegular(relative)
	case LegacyPolicy:
		openedRoot, openErr := os.OpenRoot(root.Path)
		if openErr != nil {
			return nil, ContentStat{}, openErr
		}
		defer func() { _ = openedRoot.Close() }()
		if tree != nil && tree.beforeOpen != nil {
			tree.beforeOpen()
		}
		file, err = openedRoot.Open(relative)
	default:
		return nil, ContentStat{}, fmt.Errorf("%w: unsupported local policy", ErrInvalidLocator)
	}
	if err != nil {
		return nil, ContentStat{}, classifyOpenError(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ContentStat{}, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, ContentStat{}, ErrNotRegular
	}
	stat := contentStatFromFileInfo(info)
	var reader io.Reader = file
	if byteRange != nil {
		reader = io.NewSectionReader(file, byteRange.Offset, byteRange.Length)
	}
	return newLocalReadHandle(ctx, file, reader, snapshotFileInfo(info)), stat, nil
}

type entryCollector struct {
	request PageRequest
	items   []Entry
	scanned int
	bytes   int64
}

func newEntryCollector(request PageRequest) *entryCollector { return &entryCollector{request: request} }

func (collector *entryCollector) Add(entry Entry) error {
	collector.scanned++
	collector.bytes += int64(len(entry.Name))
	if collector.scanned > collector.request.MaxItems || collector.bytes > collector.request.MaxBytes {
		return ErrResourceLimit
	}
	if entry.Name <= collector.request.AfterName {
		return nil
	}
	index := sort.Search(len(collector.items), func(index int) bool { return collector.items[index].Name >= entry.Name })
	collector.items = append(collector.items, Entry{})
	copy(collector.items[index+1:], collector.items[index:])
	collector.items[index] = entry
	if len(collector.items) > collector.request.Limit+1 {
		collector.items = collector.items[:collector.request.Limit+1]
	}
	return nil
}

func (collector *entryCollector) Page() EntryPage {
	hasMore := len(collector.items) > collector.request.Limit
	if hasMore {
		collector.items = collector.items[:collector.request.Limit]
	}
	page := EntryPage{Items: collector.items, HasMore: hasMore}
	if len(page.Items) > 0 {
		page.LastDigest = page.Items[len(page.Items)-1].OpaqueDigest
	}
	return page
}

func entryFromFileInfo(name string, locator Locator, info os.FileInfo) Entry {
	entryType := modeEntryType(info.Mode())
	snapshot := snapshotFileInfo(info)
	return Entry{
		Name: name, Locator: locator, Type: entryType, Size: info.Size(), Mode: info.Mode().String(),
		ModTime: info.ModTime().UTC(), OpaqueDigest: entryDigest(name, entryType, snapshot), SourceRevision: snapshot.revision(),
	}
}

func contentStatFromFileInfo(info os.FileInfo) ContentStat {
	snapshot := snapshotFileInfo(info)
	return ContentStat{Size: info.Size(), ModTime: info.ModTime().UTC(), Mode: info.Mode().String(), SourceRevision: snapshot.revision()}
}

func modeEntryType(mode os.FileMode) EntryType {
	switch {
	case mode.IsRegular():
		return EntryFile
	case mode.IsDir():
		return EntryDirectory
	case mode&os.ModeSymlink != 0:
		return EntrySymlink
	default:
		return EntrySpecial
	}
}

func joinLocator(parent, name string) Locator {
	if parent == "." || parent == "" {
		return Locator{Path: name}
	}
	return Locator{Path: filepath.Join(parent, name)}
}

func entryDigest(name string, entryType EntryType, snapshot fileSnapshot) string {
	digest := sha256.Sum256([]byte(name + "\x00" + string(entryType) + "\x00" + snapshot.revision()))
	return hex.EncodeToString(digest[:])
}

type fileSnapshot struct {
	size    int64
	mode    os.FileMode
	modTime int64
	device  uint64
	inode   uint64
}

func snapshotFileInfo(info os.FileInfo) fileSnapshot {
	snapshot := fileSnapshot{size: info.Size(), mode: info.Mode(), modTime: info.ModTime().UnixNano()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		snapshot.device = uint64(stat.Dev)
		snapshot.inode = stat.Ino
	}
	return snapshot
}

func (snapshot fileSnapshot) revision() string {
	digest := sha256.Sum256([]byte(strconv.FormatInt(snapshot.size, 10) + ":" + strconv.FormatUint(uint64(snapshot.mode), 10) + ":" + strconv.FormatInt(snapshot.modTime, 10) + ":" + strconv.FormatUint(snapshot.device, 10) + ":" + strconv.FormatUint(snapshot.inode, 10)))
	return hex.EncodeToString(digest[:])
}

type localReadHandle struct {
	file     *os.File
	reader   io.Reader
	initial  fileSnapshot
	ctx      context.Context
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func newLocalReadHandle(ctx context.Context, file *os.File, reader io.Reader, initial fileSnapshot) *localReadHandle {
	handle := &localReadHandle{file: file, reader: reader, initial: initial, ctx: ctx, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			handle.finish(ctx.Err())
		case <-handle.done:
		}
	}()
	return handle
}

func (handle *localReadHandle) Read(buffer []byte) (int, error) {
	if err := contextError(handle.ctx); err != nil {
		return 0, err
	}
	return handle.reader.Read(buffer)
}

func (handle *localReadHandle) Close() error {
	handle.finish(nil)
	return handle.closeErr
}

func (handle *localReadHandle) finish(cause error) {
	handle.once.Do(func() {
		defer close(handle.done)
		if cause != nil {
			handle.closeErr = cause
			_ = handle.file.Close()
			return
		}
		info, err := handle.file.Stat()
		if err != nil {
			handle.closeErr = err
		} else if snapshotFileInfo(info) != handle.initial {
			handle.closeErr = ErrSourceChanged
		}
		if err := handle.file.Close(); handle.closeErr == nil && err != nil {
			handle.closeErr = err
		}
	})
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func classifyOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errorsIsSymlink(err) {
		return fmt.Errorf("%w", ErrSymlinkDenied)
	}
	return err
}

func errorsIsSymlink(err error) bool { return isStrictSymlinkError(err) }
