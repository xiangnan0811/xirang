package sshutil

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// ErrSFTPDirectoryUnavailable indicates an invalid request, transport, or SFTP response.
var ErrSFTPDirectoryUnavailable = errors.New("sftp directory unavailable")

// ErrSFTPDirectoryOverflow indicates that one NAME packet exceeded its entry bound.
var ErrSFTPDirectoryOverflow = errors.New("sftp directory entry limit exceeded")

// BoundedSFTPDirectoryReader reads at most the configured batch size at a time.
// Close releases its directory handle; the session owns the underlying transport.
type BoundedSFTPDirectoryReader interface {
	ReadDir(int) ([]os.FileInfo, error)
	Close() error
}

const (
	sftpDirectoryAttributeSize       = uint32(0x00000001)
	sftpDirectoryAttributeUIDGID     = uint32(0x00000002)
	sftpDirectoryAttributePermission = uint32(0x00000004)
	sftpDirectoryAttributeTimes      = uint32(0x00000008)
	sftpDirectoryAttributeExtended   = uint32(0x80000000)

	sftpDirectoryPacketInit    = byte(1)
	sftpDirectoryPacketVersion = byte(2)
	sftpDirectoryPacketClose   = byte(4)
	sftpDirectoryPacketOpenDir = byte(11)
	sftpDirectoryPacketReadDir = byte(12)
	sftpDirectoryPacketStatus  = byte(101)
	sftpDirectoryPacketHandle  = byte(102)
	sftpDirectoryPacketName    = byte(104)

	sftpDirectoryStatusOK        = uint32(0)
	sftpDirectoryStatusEOF       = uint32(1)
	sftpDirectoryProtocolVersion = uint32(3)
	sftpDirectoryPacketMaxBytes  = uint32(256 << 10)
)

type sftpDirectoryWireReader struct {
	value  []byte
	offset int
}

func (reader *sftpDirectoryWireReader) readUint32() (uint32, bool) {
	if reader == nil || len(reader.value)-reader.offset < 4 {
		return 0, false
	}
	value := binary.BigEndian.Uint32(reader.value[reader.offset : reader.offset+4])
	reader.offset += 4
	return value, true
}

func (reader *sftpDirectoryWireReader) readUint64() (uint64, bool) {
	if reader == nil || len(reader.value)-reader.offset < 8 {
		return 0, false
	}
	value := binary.BigEndian.Uint64(reader.value[reader.offset : reader.offset+8])
	reader.offset += 8
	return value, true
}

func (reader *sftpDirectoryWireReader) readString() (string, bool) {
	length, ok := reader.readUint32()
	if !ok || uint64(length) > uint64(len(reader.value)-reader.offset) {
		return "", false
	}
	value := string(reader.value[reader.offset : reader.offset+int(length)])
	reader.offset += int(length)
	return value, true
}

func (reader *sftpDirectoryWireReader) done() bool {
	return reader != nil && reader.offset == len(reader.value)
}

type sftpDirectoryFileInfo struct {
	name string
	stat sftp.FileStat
}

func (info sftpDirectoryFileInfo) Name() string       { return info.name }
func (info sftpDirectoryFileInfo) Size() int64        { return int64(info.stat.Size) }
func (info sftpDirectoryFileInfo) Mode() os.FileMode  { return info.stat.FileMode() }
func (info sftpDirectoryFileInfo) ModTime() time.Time { return info.stat.ModTime() }
func (info sftpDirectoryFileInfo) IsDir() bool        { return info.Mode().IsDir() }
func (info sftpDirectoryFileInfo) Sys() any           { return &info.stat }

func decodeSFTPDirectoryAttributes(reader *sftpDirectoryWireReader) (sftp.FileStat, bool) {
	flags, ok := reader.readUint32()
	if !ok || flags&^(sftpDirectoryAttributeSize|sftpDirectoryAttributeUIDGID|
		sftpDirectoryAttributePermission|sftpDirectoryAttributeTimes|sftpDirectoryAttributeExtended) != 0 {
		return sftp.FileStat{}, false
	}
	stat := sftp.FileStat{}
	if flags&sftpDirectoryAttributeSize != 0 {
		stat.Size, ok = reader.readUint64()
		if !ok || stat.Size > math.MaxInt64 {
			return sftp.FileStat{}, false
		}
	}
	if flags&sftpDirectoryAttributeUIDGID != 0 {
		stat.UID, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
		stat.GID, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
	}
	if flags&sftpDirectoryAttributePermission != 0 {
		stat.Mode, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
	}
	if flags&sftpDirectoryAttributeTimes != 0 {
		stat.Atime, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
		stat.Mtime, ok = reader.readUint32()
		if !ok {
			return sftp.FileStat{}, false
		}
	}
	if flags&sftpDirectoryAttributeExtended != 0 {
		count, countOK := reader.readUint32()
		if !countOK {
			return sftp.FileStat{}, false
		}
		for index := uint32(0); index < count; index++ {
			if _, ok = reader.readString(); !ok {
				return sftp.FileStat{}, false
			}
			if _, ok = reader.readString(); !ok {
				return sftp.FileStat{}, false
			}
		}
	}
	return stat, true
}

func decodeSFTPDirectoryNamePacket(
	payload []byte,
	expectedID uint32,
	maximumEntries int,
) ([]os.FileInfo, bool, error) {
	reader := &sftpDirectoryWireReader{value: payload}
	responseID, ok := reader.readUint32()
	if !ok || responseID != expectedID {
		return nil, false, ErrSFTPDirectoryUnavailable
	}
	count, ok := reader.readUint32()
	if !ok {
		return nil, false, ErrSFTPDirectoryUnavailable
	}
	// Each name consumes at least three uint32 fields. Bound allocation by the
	// packet itself even when its advertised count or the caller limit is huge.
	capacity := min(maximumEntries, (len(payload)-8)/12)
	if uint64(count) < uint64(capacity) {
		capacity = int(count)
	}
	entries := make([]os.FileInfo, 0, capacity)
	truncated := false
	for index := uint32(0); index < count; index++ {
		name, nameOK := reader.readString()
		if !nameOK {
			return nil, false, ErrSFTPDirectoryUnavailable
		}
		if _, longNameOK := reader.readString(); !longNameOK {
			return nil, false, ErrSFTPDirectoryUnavailable
		}
		stat, statOK := decodeSFTPDirectoryAttributes(reader)
		if !statOK {
			return nil, false, ErrSFTPDirectoryUnavailable
		}
		if name == "." || name == ".." {
			continue
		}
		if len(entries) >= maximumEntries {
			truncated = true
			continue
		}
		entries = append(entries, sftpDirectoryFileInfo{name: name, stat: stat})
	}
	if !reader.done() {
		return nil, false, ErrSFTPDirectoryUnavailable
	}
	return entries, truncated, nil
}

// BoundedSFTPDirectorySession owns a single SFTP directory subsystem transport.
type BoundedSFTPDirectorySession struct {
	requestMu      sync.Mutex
	stateMu        sync.Mutex
	transport      io.ReadWriteCloser
	maximumEntries int
	batchSize      int
	nextID         uint32
	wireBroken     bool
	closed         bool
	closeOnce      sync.Once
	closeErr       error
}

type boundedSFTPDirectory struct {
	session   *BoundedSFTPDirectorySession
	handle    string
	pending   []os.FileInfo
	truncated bool
	eof       bool
	closeOnce sync.Once
	closeErr  error
}

func appendSFTPDirectoryUint32(value []byte, field uint32) []byte {
	return binary.BigEndian.AppendUint32(value, field)
}

func appendSFTPDirectoryString(value []byte, field string) []byte {
	value = appendSFTPDirectoryUint32(value, uint32(len(field)))
	return append(value, field...)
}

func writeSFTPDirectoryPacket(
	transport io.ReadWriteCloser,
	packetType byte,
	payload []byte,
) error {
	if transport == nil || uint64(len(payload))+1 > uint64(sftpDirectoryPacketMaxBytes) {
		return ErrSFTPDirectoryUnavailable
	}
	packet := make([]byte, 0, len(payload)+5)
	packet = appendSFTPDirectoryUint32(packet, uint32(len(payload)+1))
	packet = append(packet, packetType)
	packet = append(packet, payload...)
	for len(packet) > 0 {
		written, err := transport.Write(packet)
		if written > 0 {
			packet = packet[written:]
		}
		if err != nil || written <= 0 {
			return ErrSFTPDirectoryUnavailable
		}
	}
	return nil
}

func readSFTPDirectoryPacket(
	transport io.ReadWriteCloser,
) (byte, []byte, error) {
	if transport == nil {
		return 0, nil, ErrSFTPDirectoryUnavailable
	}
	var header [4]byte
	if _, err := io.ReadFull(transport, header[:]); err != nil {
		return 0, nil, ErrSFTPDirectoryUnavailable
	}
	length := binary.BigEndian.Uint32(header[:])
	if length < 1 || length > sftpDirectoryPacketMaxBytes {
		return 0, nil, ErrSFTPDirectoryUnavailable
	}
	packet := make([]byte, int(length))
	if _, err := io.ReadFull(transport, packet); err != nil {
		return 0, nil, ErrSFTPDirectoryUnavailable
	}
	return packet[0], packet[1:], nil
}

func decodeSFTPDirectoryVersion(payload []byte) error {
	reader := &sftpDirectoryWireReader{value: payload}
	version, ok := reader.readUint32()
	if !ok || version != sftpDirectoryProtocolVersion {
		return ErrSFTPDirectoryUnavailable
	}
	for !reader.done() {
		if _, ok := reader.readString(); !ok {
			return ErrSFTPDirectoryUnavailable
		}
		if _, ok := reader.readString(); !ok {
			return ErrSFTPDirectoryUnavailable
		}
	}
	return nil
}

func decodeSFTPDirectoryHandle(payload []byte, expectedID uint32) (string, error) {
	reader := &sftpDirectoryWireReader{value: payload}
	responseID, ok := reader.readUint32()
	if !ok || responseID != expectedID {
		return "", ErrSFTPDirectoryUnavailable
	}
	handle, ok := reader.readString()
	if !ok || handle == "" || !reader.done() {
		return "", ErrSFTPDirectoryUnavailable
	}
	return handle, nil
}

func decodeSFTPDirectoryStatus(payload []byte, expectedID uint32) (uint32, error) {
	reader := &sftpDirectoryWireReader{value: payload}
	responseID, ok := reader.readUint32()
	if !ok || responseID != expectedID {
		return 0, ErrSFTPDirectoryUnavailable
	}
	status, ok := reader.readUint32()
	if !ok {
		return 0, ErrSFTPDirectoryUnavailable
	}
	if _, ok := reader.readString(); !ok {
		return 0, ErrSFTPDirectoryUnavailable
	}
	if _, ok := reader.readString(); !ok || !reader.done() {
		return 0, ErrSFTPDirectoryUnavailable
	}
	return status, nil
}

// NewBoundedSFTPDirectorySession performs the SFTP v3 handshake and owns
// transport, including on handshake or configuration failure. maximumEntries
// bounds non-dot entries retained from each NAME packet, not the total over
// multiple packets. The reader returns ErrSFTPDirectoryOverflow after those
// retained entries if a packet exceeded the bound. Callers requiring a total
// directory limit must count all returned entries and require an actual EOF.
// batchSize is the largest accepted ReadDir argument; both limits must be positive.
func NewBoundedSFTPDirectorySession(
	transport io.ReadWriteCloser, maximumEntries int, batchSize int,
) (*BoundedSFTPDirectorySession, error) {
	if transport == nil {
		return nil, ErrSFTPDirectoryUnavailable
	}
	if maximumEntries <= 0 || batchSize <= 0 {
		_ = transport.Close()
		return nil, ErrSFTPDirectoryUnavailable
	}
	session := &BoundedSFTPDirectorySession{
		transport: transport, maximumEntries: maximumEntries, batchSize: batchSize, nextID: 1,
	}
	if err := writeSFTPDirectoryPacket(
		transport, sftpDirectoryPacketInit, appendSFTPDirectoryUint32(nil, sftpDirectoryProtocolVersion),
	); err != nil {
		_ = transport.Close()
		return nil, err
	}
	packetType, payload, err := readSFTPDirectoryPacket(transport)
	if err != nil || packetType != sftpDirectoryPacketVersion || decodeSFTPDirectoryVersion(payload) != nil {
		_ = transport.Close()
		return nil, ErrSFTPDirectoryUnavailable
	}
	return session, nil
}

func (session *BoundedSFTPDirectorySession) activeTransport() (
	io.ReadWriteCloser,
	error,
) {
	if session == nil {
		return nil, ErrSFTPDirectoryUnavailable
	}
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if session.closed || session.wireBroken || session.transport == nil {
		return nil, ErrSFTPDirectoryUnavailable
	}
	return session.transport, nil
}

func (session *BoundedSFTPDirectorySession) markWireBroken() {
	if session == nil {
		return
	}
	session.stateMu.Lock()
	session.wireBroken = true
	session.stateMu.Unlock()
}

func (session *BoundedSFTPDirectorySession) nextRequestIDLocked() (uint32, error) {
	if session == nil || session.nextID == 0 || session.nextID == math.MaxUint32 {
		return 0, ErrSFTPDirectoryUnavailable
	}
	requestID := session.nextID
	session.nextID++
	return requestID, nil
}

// OpenDirectory opens a clean absolute path on this session. Closing the reader
// releases only its remote handle; callers must also close the owning session.
func (session *BoundedSFTPDirectorySession) OpenDirectory(
	directoryPath string,
) (BoundedSFTPDirectoryReader, error) {
	if session == nil || !path.IsAbs(directoryPath) || path.Clean(directoryPath) != directoryPath {
		return nil, ErrSFTPDirectoryUnavailable
	}
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	transport, err := session.activeTransport()
	if err != nil {
		return nil, err
	}
	requestID, err := session.nextRequestIDLocked()
	if err != nil {
		return nil, err
	}
	request := appendSFTPDirectoryUint32(nil, requestID)
	request = appendSFTPDirectoryString(request, directoryPath)
	if err := writeSFTPDirectoryPacket(transport, sftpDirectoryPacketOpenDir, request); err != nil {
		session.markWireBroken()
		return nil, ErrSFTPDirectoryUnavailable
	}
	packetType, payload, err := readSFTPDirectoryPacket(transport)
	if err != nil || packetType != sftpDirectoryPacketHandle {
		session.markWireBroken()
		return nil, ErrSFTPDirectoryUnavailable
	}
	handle, err := decodeSFTPDirectoryHandle(payload, requestID)
	if err != nil {
		session.markWireBroken()
		return nil, ErrSFTPDirectoryUnavailable
	}
	return &boundedSFTPDirectory{session: session, handle: handle}, nil
}

func (session *BoundedSFTPDirectorySession) readDirectory(
	handle string,
) (byte, []byte, uint32, error) {
	if session == nil || handle == "" {
		return 0, nil, 0, ErrSFTPDirectoryUnavailable
	}
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	transport, err := session.activeTransport()
	if err != nil {
		return 0, nil, 0, err
	}
	requestID, err := session.nextRequestIDLocked()
	if err != nil {
		return 0, nil, 0, err
	}
	request := appendSFTPDirectoryUint32(nil, requestID)
	request = appendSFTPDirectoryString(request, handle)
	if err := writeSFTPDirectoryPacket(transport, sftpDirectoryPacketReadDir, request); err != nil {
		session.markWireBroken()
		return 0, nil, 0, ErrSFTPDirectoryUnavailable
	}
	packetType, payload, err := readSFTPDirectoryPacket(transport)
	if err != nil {
		session.markWireBroken()
		return 0, nil, 0, ErrSFTPDirectoryUnavailable
	}
	return packetType, payload, requestID, nil
}

func (session *BoundedSFTPDirectorySession) closeDirectory(handle string) error {
	if session == nil || handle == "" {
		return ErrSFTPDirectoryUnavailable
	}
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	transport, err := session.activeTransport()
	if err != nil {
		return err
	}
	requestID, err := session.nextRequestIDLocked()
	if err != nil {
		return err
	}
	request := appendSFTPDirectoryUint32(nil, requestID)
	request = appendSFTPDirectoryString(request, handle)
	if err := writeSFTPDirectoryPacket(transport, sftpDirectoryPacketClose, request); err != nil {
		session.markWireBroken()
		return ErrSFTPDirectoryUnavailable
	}
	packetType, payload, err := readSFTPDirectoryPacket(transport)
	if err != nil || packetType != sftpDirectoryPacketStatus {
		session.markWireBroken()
		return ErrSFTPDirectoryUnavailable
	}
	status, err := decodeSFTPDirectoryStatus(payload, requestID)
	if err != nil || status != sftpDirectoryStatusOK {
		session.markWireBroken()
		return ErrSFTPDirectoryUnavailable
	}
	return nil
}

// Close closes the owned transport once, including when a read is blocked.
func (session *BoundedSFTPDirectorySession) Close() error {
	if session == nil {
		return ErrSFTPDirectoryUnavailable
	}
	session.closeOnce.Do(func() {
		session.stateMu.Lock()
		transport := session.transport
		wireBroken := session.wireBroken
		session.closed = true
		session.transport = nil
		session.stateMu.Unlock()
		if wireBroken {
			session.closeErr = ErrSFTPDirectoryUnavailable
		}
		if transport == nil {
			if session.closeErr == nil {
				session.closeErr = ErrSFTPDirectoryUnavailable
			}
			return
		}
		if err := transport.Close(); err != nil && session.closeErr == nil {
			session.closeErr = ErrSFTPDirectoryUnavailable
		}
	})
	return session.closeErr
}

func (directory *boundedSFTPDirectory) ReadDir(n int) ([]os.FileInfo, error) {
	if directory == nil || directory.session == nil || directory.handle == "" ||
		n <= 0 || n > directory.session.batchSize {
		return nil, ErrSFTPDirectoryUnavailable
	}
	if len(directory.pending) > 0 {
		count := min(n, len(directory.pending))
		entries := append([]os.FileInfo(nil), directory.pending[:count]...)
		directory.pending = directory.pending[count:]
		return entries, nil
	}
	if directory.truncated {
		return nil, ErrSFTPDirectoryOverflow
	}
	if directory.eof {
		return nil, io.EOF
	}
	packetType, payload, requestID, err := directory.session.readDirectory(directory.handle)
	if err != nil {
		return nil, err
	}
	switch packetType {
	case sftpDirectoryPacketName:
		directory.pending, directory.truncated, err = decodeSFTPDirectoryNamePacket(
			payload, requestID, directory.session.maximumEntries,
		)
		if err != nil || len(directory.pending) == 0 {
			directory.session.markWireBroken()
			return nil, ErrSFTPDirectoryUnavailable
		}
		return directory.ReadDir(n)
	case sftpDirectoryPacketStatus:
		status, statusErr := decodeSFTPDirectoryStatus(payload, requestID)
		if statusErr != nil {
			directory.session.markWireBroken()
			return nil, statusErr
		}
		if status == sftpDirectoryStatusEOF {
			directory.eof = true
			return nil, io.EOF
		}
		directory.session.markWireBroken()
		return nil, ErrSFTPDirectoryUnavailable
	default:
		directory.session.markWireBroken()
		return nil, ErrSFTPDirectoryUnavailable
	}
}

func (directory *boundedSFTPDirectory) Close() error {
	if directory == nil {
		return ErrSFTPDirectoryUnavailable
	}
	directory.closeOnce.Do(func() {
		if directory.session == nil || directory.handle == "" {
			directory.closeErr = ErrSFTPDirectoryUnavailable
			return
		}
		if err := directory.session.closeDirectory(directory.handle); err != nil {
			directory.closeErr = ErrSFTPDirectoryUnavailable
		}
		directory.handle = ""
	})
	return directory.closeErr
}
