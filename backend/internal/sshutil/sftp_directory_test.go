package sshutil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

const (
	recoveryCleanupRemoveLimit = 256
	recoveryCleanupReadBatch   = 64
)

func appendRecoverySFTPTestUint32(value []byte, field uint32) []byte {
	return append(value, byte(field>>24), byte(field>>16), byte(field>>8), byte(field))
}

func appendRecoverySFTPTestUint64(value []byte, field uint64) []byte {
	value = appendRecoverySFTPTestUint32(value, uint32(field>>32))
	return appendRecoverySFTPTestUint32(value, uint32(field))
}

func appendRecoverySFTPTestString(value []byte, field string) []byte {
	value = appendRecoverySFTPTestUint32(value, uint32(len(field)))
	return append(value, field...)
}

func appendRecoverySFTPTestName(
	value []byte,
	name string,
	mode uint32,
	size uint64,
) []byte {
	value = appendRecoverySFTPTestString(value, name)
	value = appendRecoverySFTPTestString(value, "test-long-name")
	value = appendRecoverySFTPTestUint32(value, 0x0f)
	value = appendRecoverySFTPTestUint64(value, size)
	value = appendRecoverySFTPTestUint32(value, 1000)
	value = appendRecoverySFTPTestUint32(value, 1001)
	value = appendRecoverySFTPTestUint32(value, mode)
	value = appendRecoverySFTPTestUint32(value, 1_700_000_000)
	return appendRecoverySFTPTestUint32(value, 1_700_000_001)
}

func TestRecoveryBoundedSFTPDirectoryNamePacketMatrix(t *testing.T) {
	t.Run("valid closed kinds and dot filtering", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 17)
		payload = appendRecoverySFTPTestUint32(payload, 6)
		payload = appendRecoverySFTPTestName(payload, ".", syscall.S_IFDIR|0o700, 0)
		payload = appendRecoverySFTPTestName(payload, "..", syscall.S_IFDIR|0o700, 0)
		payload = appendRecoverySFTPTestName(payload, "regular", syscall.S_IFREG|0o600, 23)
		payload = appendRecoverySFTPTestName(payload, "directory", syscall.S_IFDIR|0o700, 0)
		payload = appendRecoverySFTPTestName(payload, "symlink", syscall.S_IFLNK|0o777, 9)
		payload = appendRecoverySFTPTestName(payload, "fifo", syscall.S_IFIFO|0o600, 0)

		entries, truncated, err := decodeSFTPDirectoryNamePacket(payload, 17, recoveryCleanupRemoveLimit+1)
		if err != nil || truncated || len(entries) != 4 {
			t.Fatalf("decode entries=%v truncated=%v error=%v, want four exact entries", entries, truncated, err)
		}
		wantNames := []string{"regular", "directory", "symlink", "fifo"}
		for index, entry := range entries {
			if entry.Name() != wantNames[index] {
				t.Fatalf("entry %d name=%q, want %q", index, entry.Name(), wantNames[index])
			}
		}
		if entries[0].Size() != 23 || !entries[0].Mode().IsRegular() || !entries[1].IsDir() ||
			entries[2].Mode()&os.ModeSymlink == 0 || entries[3].Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("decoded entry kinds=%v, want regular/directory/symlink/fifo", entries)
		}
		if entries[0].ModTime().Unix() != 1_700_000_001 {
			t.Fatalf("decoded modtime=%v, want exact wire mtime", entries[0].ModTime())
		}
	})

	t.Run("retained entry bound", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 19)
		payload = appendRecoverySFTPTestUint32(payload, recoveryCleanupRemoveLimit+2)
		for index := 0; index < recoveryCleanupRemoveLimit+2; index++ {
			payload = appendRecoverySFTPTestName(
				payload, fmt.Sprintf("entry-%03d", index), syscall.S_IFREG|0o600, uint64(index),
			)
		}
		entries, truncated, err := decodeSFTPDirectoryNamePacket(payload, 19, recoveryCleanupRemoveLimit+1)
		if err != nil || !truncated || len(entries) != recoveryCleanupRemoveLimit+1 {
			t.Fatalf("bounded decode len=%d truncated=%v error=%v, want %d/true",
				len(entries), truncated, err, recoveryCleanupRemoveLimit+1)
		}
		if entries[0].Name() != "entry-000" || entries[len(entries)-1].Name() != "entry-256" {
			t.Fatalf("bounded retained names first=%q last=%q", entries[0].Name(), entries[len(entries)-1].Name())
		}
	})

	t.Run("wrong response id", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 23)
		payload = appendRecoverySFTPTestUint32(payload, 0)
		entries, truncated, err := decodeSFTPDirectoryNamePacket(payload, 24, recoveryCleanupRemoveLimit+1)
		if entries != nil || truncated || !errors.Is(err, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("wrong-id decode entries=%v truncated=%v error=%v, want sanitized unavailable", entries, truncated, err)
		}
	})

	t.Run("malformed attributes", func(t *testing.T) {
		payload := appendRecoverySFTPTestUint32(nil, 29)
		payload = appendRecoverySFTPTestUint32(payload, 1)
		payload = appendRecoverySFTPTestString(payload, "broken")
		payload = appendRecoverySFTPTestString(payload, "broken-long")
		payload = appendRecoverySFTPTestUint32(payload, 0x0f)
		payload = appendRecoverySFTPTestUint64(payload, 1)
		entries, truncated, err := decodeSFTPDirectoryNamePacket(payload, 29, recoveryCleanupRemoveLimit+1)
		if entries != nil || truncated || !errors.Is(err, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("malformed decode entries=%v truncated=%v error=%v, want sanitized unavailable", entries, truncated, err)
		}
	})
}

type recoverySFTPDirectoryTransportForTest struct {
	reader     *bytes.Reader
	writes     bytes.Buffer
	closeCalls int
}

func (transport *recoverySFTPDirectoryTransportForTest) Read(value []byte) (int, error) {
	return transport.reader.Read(value)
}

func (transport *recoverySFTPDirectoryTransportForTest) Write(value []byte) (int, error) {
	return transport.writes.Write(value)
}

func (transport *recoverySFTPDirectoryTransportForTest) Close() error {
	transport.closeCalls++
	return nil
}

func recoverySFTPTestPacket(packetType byte, payload []byte) []byte {
	packet := appendRecoverySFTPTestUint32(nil, uint32(len(payload)+1))
	packet = append(packet, packetType)
	return append(packet, payload...)
}

func recoverySFTPTestStatusPacket(id, code uint32, message string) []byte {
	payload := appendRecoverySFTPTestUint32(nil, id)
	payload = appendRecoverySFTPTestUint32(payload, code)
	payload = appendRecoverySFTPTestString(payload, message)
	payload = appendRecoverySFTPTestString(payload, "")
	return recoverySFTPTestPacket(101, payload)
}

func recoverySFTPTestPacketTypes(t *testing.T, value []byte) []byte {
	t.Helper()
	types := make([]byte, 0, 8)
	for len(value) > 0 {
		if len(value) < 5 {
			t.Fatalf("short written packet header: %x", value)
		}
		length := int(binary.BigEndian.Uint32(value[:4]))
		if length < 1 || length > len(value)-4 {
			t.Fatalf("invalid written packet length=%d remaining=%d", length, len(value)-4)
		}
		types = append(types, value[4])
		value = value[4+length:]
	}
	return types
}

func openRecoveryBoundedTestDirectory(
	transport io.ReadWriteCloser, directoryPath string,
) (BoundedSFTPDirectoryReader, *BoundedSFTPDirectorySession, error) {
	session, err := NewBoundedSFTPDirectorySession(transport, recoveryCleanupRemoveLimit+1, recoveryCleanupReadBatch)
	if err != nil {
		return nil, nil, err
	}
	directory, err := session.OpenDirectory(directoryPath)
	if err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	return directory, session, nil
}

func TestRecoveryBoundedSFTPDirectoryProtocolPagesAndCloses(t *testing.T) {
	version := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
	handlePayload := appendRecoverySFTPTestUint32(nil, 1)
	handlePayload = appendRecoverySFTPTestString(handlePayload, "directory-handle")
	handle := recoverySFTPTestPacket(102, handlePayload)
	namePayload := appendRecoverySFTPTestUint32(nil, 2)
	namePayload = appendRecoverySFTPTestUint32(namePayload, 70)
	for index := 0; index < 70; index++ {
		namePayload = appendRecoverySFTPTestName(
			namePayload, fmt.Sprintf("entry-%02d", index), syscall.S_IFREG|0o600, uint64(index),
		)
	}
	names := recoverySFTPTestPacket(104, namePayload)
	responses := append(append(append(append([]byte{}, version...), handle...), names...),
		recoverySFTPTestStatusPacket(3, 1, "eof")...)
	responses = append(responses, recoverySFTPTestStatusPacket(4, 0, "ok")...)
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
	directory, session, err := openRecoveryBoundedTestDirectory(transport, "/owned/captured")
	if err != nil {
		t.Fatalf("open bounded directory: %v", err)
	}
	first, err := directory.ReadDir(recoveryCleanupReadBatch)
	if err != nil || len(first) != recoveryCleanupReadBatch || first[0].Name() != "entry-00" || first[63].Name() != "entry-63" {
		t.Fatalf("first page len=%d error=%v first=%v last=%v", len(first), err, first[0], first[len(first)-1])
	}
	second, err := directory.ReadDir(recoveryCleanupReadBatch)
	if err != nil || len(second) != 6 || second[0].Name() != "entry-64" || second[5].Name() != "entry-69" {
		t.Fatalf("second page len=%d error=%v entries=%v", len(second), err, second)
	}
	third, err := directory.ReadDir(recoveryCleanupReadBatch)
	if third != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF page=%v error=%v, want io.EOF", third, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close bounded directory: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("idempotent bounded directory close: %v", err)
	}
	if transport.closeCalls != 0 {
		t.Fatalf("directory close closed transport %d times, want session ownership", transport.closeCalls)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close bounded session: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent bounded session close: %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("transport close calls=%d, want one", transport.closeCalls)
	}
	if got, want := recoverySFTPTestPacketTypes(t, transport.writes.Bytes()), []byte{1, 11, 12, 12, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wire packet types=%v, want %v", got, want)
	}
}

func TestRecoveryBoundedSFTPDirectorySessionSharesOneTransport(t *testing.T) {
	responses := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
	firstHandle := appendRecoverySFTPTestUint32(nil, 1)
	firstHandle = appendRecoverySFTPTestString(firstHandle, "first-handle")
	responses = append(responses, recoverySFTPTestPacket(102, firstHandle)...)
	secondHandle := appendRecoverySFTPTestUint32(nil, 2)
	secondHandle = appendRecoverySFTPTestString(secondHandle, "second-handle")
	responses = append(responses, recoverySFTPTestPacket(102, secondHandle)...)
	responses = append(responses, recoverySFTPTestStatusPacket(3, 0, "ok")...)
	responses = append(responses, recoverySFTPTestStatusPacket(4, 0, "ok")...)
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
	session, err := NewBoundedSFTPDirectorySession(transport, recoveryCleanupRemoveLimit+1, recoveryCleanupReadBatch)
	if err != nil {
		t.Fatalf("open bounded directory session: %v", err)
	}
	first, err := session.OpenDirectory("/owned/captured")
	if err != nil {
		t.Fatalf("open first directory handle: %v", err)
	}
	second, err := session.OpenDirectory("/owned/captured/nested")
	if err != nil {
		t.Fatalf("open second directory handle: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second directory handle: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first directory handle: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close shared bounded directory session: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent shared bounded directory session close: %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("shared transport close calls=%d, want one", transport.closeCalls)
	}
	if got, want := recoverySFTPTestPacketTypes(t, transport.writes.Bytes()), []byte{1, 11, 11, 4, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared wire packet types=%v, want %v", got, want)
	}
}

func TestRecoveryBoundedSFTPDirectoryInteroperatesWithPkgSFTPServer(t *testing.T) {
	root := t.TempDir()
	const entryCount = 130
	for index := 0; index < entryCount; index++ {
		if err := os.WriteFile(
			filepath.Join(root, fmt.Sprintf("entry-%03d", index)), []byte("value"), 0o600,
		); err != nil {
			t.Fatalf("write interoperability entry %d: %v", index, err)
		}
	}
	clientConnection, serverConnection := net.Pipe()
	server, err := sftp.NewServer(serverConnection)
	if err != nil {
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatalf("create interoperability SFTP server: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = server.Serve()
		close(done)
	}()
	session, err := NewBoundedSFTPDirectorySession(clientConnection, recoveryCleanupRemoveLimit+1, recoveryCleanupReadBatch)
	if err != nil {
		_ = server.Close()
		t.Fatalf("open interoperability directory session: %v", err)
	}
	directory, err := session.OpenDirectory(root)
	if err != nil {
		_ = session.Close()
		_ = server.Close()
		t.Fatalf("open interoperability directory: %v", err)
	}
	seen := make(map[string]struct{}, entryCount)
	for {
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			t.Fatalf("read interoperability directory: %v", readErr)
		}
		if len(entries) == 0 || len(entries) > recoveryCleanupReadBatch {
			t.Fatalf("interoperability page length=%d, want 1..%d", len(entries), recoveryCleanupReadBatch)
		}
		for _, entry := range entries {
			seen[entry.Name()] = struct{}{}
		}
	}
	if len(seen) != entryCount {
		t.Fatalf("interoperability entries=%d, want %d", len(seen), entryCount)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close interoperability directory: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close interoperability session: %v", err)
	}
	_ = server.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("interoperability SFTP server did not stop")
	}
}

func TestRecoveryBoundedSFTPDirectoryRejectsOversizeAndRawStatus(t *testing.T) {
	openResponses := func() []byte {
		version := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
		handlePayload := appendRecoverySFTPTestUint32(nil, 1)
		handlePayload = appendRecoverySFTPTestString(handlePayload, "directory-handle")
		return append(version, recoverySFTPTestPacket(102, handlePayload)...)
	}

	t.Run("oversize packet", func(t *testing.T) {
		responses := openResponses()
		responses = appendRecoverySFTPTestUint32(responses, sftpDirectoryPacketMaxBytes+1)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		directory, session, err := openRecoveryBoundedTestDirectory(transport, "/owned/captured")
		if err != nil {
			t.Fatalf("open bounded directory: %v", err)
		}
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if entries != nil || !errors.Is(readErr, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("oversize read entries=%v error=%v, want sanitized unavailable", entries, readErr)
		}
		if closeErr := directory.Close(); !errors.Is(closeErr, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("oversize close error=%v, want unavailable", closeErr)
		}
		if closeErr := session.Close(); !errors.Is(closeErr, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("oversize session close error=%v, want unavailable", closeErr)
		}
		if transport.closeCalls != 1 {
			t.Fatalf("oversize transport close calls=%d, want one", transport.closeCalls)
		}
	})

	t.Run("raw status", func(t *testing.T) {
		const raw = "RAW_SFTP_DIRECTORY_STATUS_FOR_TEST_ONLY"
		responses := append(openResponses(), recoverySFTPTestStatusPacket(2, 4, raw)...)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		directory, session, err := openRecoveryBoundedTestDirectory(transport, "/owned/captured")
		if err != nil {
			t.Fatalf("open bounded directory: %v", err)
		}
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		if entries != nil || !errors.Is(readErr, ErrSFTPDirectoryUnavailable) || strings.Contains(readErr.Error(), raw) {
			t.Fatalf("status read entries=%v error=%v, want redacted unavailable", entries, readErr)
		}
		if closeErr := directory.Close(); !errors.Is(closeErr, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("status close error=%v, want unavailable", closeErr)
		}
		if closeErr := session.Close(); !errors.Is(closeErr, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("status session close error=%v, want unavailable", closeErr)
		}
		if transport.closeCalls != 1 {
			t.Fatalf("status transport close calls=%d, want one", transport.closeCalls)
		}
	})
}

func boundedDirectoryTestOpenResponses() []byte {
	version := recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 3))
	handle := appendRecoverySFTPTestUint32(nil, 1)
	handle = appendRecoverySFTPTestString(handle, "directory-handle")
	return append(version, recoverySFTPTestPacket(102, handle)...)
}

func boundedDirectoryTestNamePacket(id uint32, names []string) []byte {
	payload := appendRecoverySFTPTestUint32(nil, id)
	payload = appendRecoverySFTPTestUint32(payload, uint32(len(names)))
	for _, name := range names {
		payload = appendRecoverySFTPTestName(payload, name, syscall.S_IFREG|0o600, 0)
	}
	return recoverySFTPTestPacket(104, payload)
}

func boundedDirectoryTestNames(start, count int) []string {
	names := make([]string, count)
	for index := range names {
		names[index] = fmt.Sprintf("entry-%03d", start+index)
	}
	return names
}

func TestBoundedSFTPDirectoryConfigLimitAndEOF(t *testing.T) {
	const packetLimit = 129
	const batchSize = 64

	t.Run("128 entries and dots require real EOF", func(t *testing.T) {
		names := append([]string{".", "..", ".owner-v1", "unknown"}, boundedDirectoryTestNames(0, 126)...)
		responses := boundedDirectoryTestOpenResponses()
		responses = append(responses, boundedDirectoryTestNamePacket(2, names)...)
		responses = append(responses, recoverySFTPTestStatusPacket(3, 1, "eof")...)
		responses = append(responses, recoverySFTPTestStatusPacket(4, 0, "ok")...)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		session, err := NewBoundedSFTPDirectorySession(transport, packetLimit, batchSize)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := session.OpenDirectory("/owned/config")
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]bool)
		for {
			entries, readErr := directory.ReadDir(batchSize)
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				seen[entry.Name()] = true
			}
		}
		if len(seen) != 128 || seen["."] || seen[".."] || !seen[".owner-v1"] || !seen["unknown"] {
			t.Fatalf("decoded names count=%d dots=%t/%t marker=%t unknown=%t", len(seen), seen["."], seen[".."], seen[".owner-v1"], seen["unknown"])
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil || transport.closeCalls != 1 {
			t.Fatalf("close error=%v transport closes=%d", err, transport.closeCalls)
		}
	})

	t.Run("oversized single packet returns overflow", func(t *testing.T) {
		responses := boundedDirectoryTestOpenResponses()
		responses = append(responses, boundedDirectoryTestNamePacket(2, boundedDirectoryTestNames(0, 130))...)
		responses = append(responses, recoverySFTPTestStatusPacket(3, 0, "ok")...)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		session, err := NewBoundedSFTPDirectorySession(transport, packetLimit, batchSize)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := session.OpenDirectory("/owned/config")
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for {
			entries, readErr := directory.ReadDir(batchSize)
			seen += len(entries)
			if readErr != nil {
				if !errors.Is(readErr, ErrSFTPDirectoryOverflow) || errors.Is(readErr, io.EOF) {
					t.Fatalf("read error=%v, want overflow", readErr)
				}
				break
			}
		}
		if seen != packetLimit {
			t.Fatalf("retained entries=%d, want %d", seen, packetLimit)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if got, want := recoverySFTPTestPacketTypes(t, transport.writes.Bytes()), []byte{1, 11, 12, 4}; !reflect.DeepEqual(got, want) {
			t.Fatalf("wire packet types=%v, want %v", got, want)
		}
	})

	t.Run("129th entry across packets remains visible", func(t *testing.T) {
		responses := boundedDirectoryTestOpenResponses()
		responses = append(responses, boundedDirectoryTestNamePacket(2, boundedDirectoryTestNames(0, 64))...)
		responses = append(responses, boundedDirectoryTestNamePacket(3, boundedDirectoryTestNames(64, 64))...)
		responses = append(responses, boundedDirectoryTestNamePacket(4, []string{"unknown"})...)
		responses = append(responses, recoverySFTPTestStatusPacket(5, 0, "ok")...)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		session, err := NewBoundedSFTPDirectorySession(transport, packetLimit, batchSize)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := session.OpenDirectory("/owned/config")
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for seen <= 128 {
			entries, readErr := directory.ReadDir(batchSize)
			if readErr != nil {
				t.Fatalf("read before detecting 129th entry: %v", readErr)
			}
			seen += len(entries)
		}
		if seen != 129 {
			t.Fatalf("detected entries=%d, want 129", seen)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if got, want := recoverySFTPTestPacketTypes(t, transport.writes.Bytes()), []byte{1, 11, 12, 12, 12, 4}; !reflect.DeepEqual(got, want) {
			t.Fatalf("wire packet types=%v, want %v", got, want)
		}
	})

	t.Run("transport EOF is not directory EOF", func(t *testing.T) {
		responses := boundedDirectoryTestOpenResponses()
		responses = append(responses, boundedDirectoryTestNamePacket(2, boundedDirectoryTestNames(0, 128))...)
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
		session, err := NewBoundedSFTPDirectorySession(transport, packetLimit, batchSize)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := session.OpenDirectory("/owned/config")
		if err != nil {
			t.Fatal(err)
		}
		for index := range 2 {
			entries, readErr := directory.ReadDir(batchSize)
			if readErr != nil || len(entries) != batchSize {
				t.Fatalf("page=%d len=%d error=%v", index, len(entries), readErr)
			}
		}
		entries, readErr := directory.ReadDir(batchSize)
		if len(entries) != 0 || !errors.Is(readErr, ErrSFTPDirectoryUnavailable) || errors.Is(readErr, io.EOF) {
			t.Fatalf("incomplete listing entries=%d error=%v", len(entries), readErr)
		}
		if err := session.Close(); !errors.Is(err, ErrSFTPDirectoryUnavailable) || transport.closeCalls != 1 {
			t.Fatalf("close broken session error=%v transport closes=%d", err, transport.closeCalls)
		}
	})
}

func TestBoundedSFTPDirectoryRecoveryPagingAcrossPacketLimits(t *testing.T) {
	responses := boundedDirectoryTestOpenResponses()
	for page := range 5 {
		responses = append(responses, boundedDirectoryTestNamePacket(uint32(page+2), boundedDirectoryTestNames(page*64, 64))...)
	}
	responses = append(responses, recoverySFTPTestStatusPacket(7, 1, "eof")...)
	responses = append(responses, recoverySFTPTestStatusPacket(8, 0, "ok")...)
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(responses)}
	session, err := NewBoundedSFTPDirectorySession(transport, recoveryCleanupRemoveLimit+1, recoveryCleanupReadBatch)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := session.OpenDirectory("/owned/recovery")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for {
		entries, readErr := directory.ReadDir(recoveryCleanupReadBatch)
		seen += len(entries)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
	}
	if seen != 320 {
		t.Fatalf("paged recovery entries=%d, want 320", seen)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedSFTPDirectorySessionRejectsInvalidOptionsAndClosesTransport(t *testing.T) {
	for _, limits := range [][2]int{{0, 64}, {-1, 64}, {129, 0}, {129, -1}} {
		transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(nil)}
		session, err := NewBoundedSFTPDirectorySession(transport, limits[0], limits[1])
		if session != nil || !errors.Is(err, ErrSFTPDirectoryUnavailable) || transport.closeCalls != 1 || transport.writes.Len() != 0 {
			t.Fatalf("limits=%v session=%v error=%v closes=%d writes=%d", limits, session, err, transport.closeCalls, transport.writes.Len())
		}
	}
	transport := &recoverySFTPDirectoryTransportForTest{reader: bytes.NewReader(recoverySFTPTestPacket(2, appendRecoverySFTPTestUint32(nil, 4)))}
	session, err := NewBoundedSFTPDirectorySession(transport, 129, 64)
	if session != nil || !errors.Is(err, ErrSFTPDirectoryUnavailable) || transport.closeCalls != 1 {
		t.Fatalf("invalid version session=%v error=%v closes=%d", session, err, transport.closeCalls)
	}
}

func TestBoundedSFTPDirectoryRejectsHugeAdvertisedNameCount(t *testing.T) {
	payload := appendRecoverySFTPTestUint32(nil, 2)
	payload = appendRecoverySFTPTestUint32(payload, ^uint32(0))
	entries, truncated, err := decodeSFTPDirectoryNamePacket(payload, 2, int(^uint(0)>>1))
	if entries != nil || truncated || !errors.Is(err, ErrSFTPDirectoryUnavailable) {
		t.Fatalf("huge advertised count entries=%v truncated=%t error=%v", entries, truncated, err)
	}
}

type blockedDirectoryTransportForTest struct {
	*recoverySFTPDirectoryTransportForTest
	entered chan struct{}
	release chan struct{}
}

func (transport *blockedDirectoryTransportForTest) Read(value []byte) (int, error) {
	if transport.reader.Len() != 0 {
		return transport.reader.Read(value)
	}
	close(transport.entered)
	<-transport.release
	return 0, io.ErrClosedPipe
}

func (transport *blockedDirectoryTransportForTest) Close() error {
	transport.closeCalls++
	close(transport.release)
	return nil
}

func TestBoundedSFTPDirectorySessionCloseUnblocksRead(t *testing.T) {
	transport := &blockedDirectoryTransportForTest{
		recoverySFTPDirectoryTransportForTest: &recoverySFTPDirectoryTransportForTest{
			reader: bytes.NewReader(boundedDirectoryTestOpenResponses()),
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	session, err := NewBoundedSFTPDirectorySession(transport, 129, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	directory, err := session.OpenDirectory("/owned/config")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, readErr := directory.ReadDir(64)
		result <- readErr
	}()
	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("directory read did not reach the blocked transport")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close blocked session: %v", err)
	}
	select {
	case readErr := <-result:
		if !errors.Is(readErr, ErrSFTPDirectoryUnavailable) {
			t.Fatalf("unblocked read error=%v, want unavailable", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("closing the session did not unblock the read")
	}
	if err := session.Close(); err != nil || transport.closeCalls != 1 {
		t.Fatalf("idempotent close error=%v transport closes=%d", err, transport.closeCalls)
	}
}
