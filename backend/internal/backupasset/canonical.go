package backupasset

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
)

type CanonicalSHA256 struct {
	hash            hash.Hash
	err             error
	finalized       bool
	maxStringLength uint64
}

func NewCanonicalSHA256() *CanonicalSHA256 {
	return &CanonicalSHA256{hash: sha256.New(), maxStringLength: math.MaxUint32}
}

func (writer *CanonicalSHA256) String(value string) {
	if !writer.ready() {
		return
	}
	if uint64(len(value)) > writer.maxStringLength {
		writer.err = fmt.Errorf("canonical string exceeds uint32 length limit")
		return
	}
	writer.Uint32(uint32(len(value)))
	writer.write([]byte(value))
}

func (writer *CanonicalSHA256) Uint8(value uint8) {
	if !writer.ready() {
		return
	}
	writer.write([]byte{value})
}

func (writer *CanonicalSHA256) Uint32(value uint32) {
	if !writer.ready() {
		return
	}
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	writer.write(encoded[:])
}

func (writer *CanonicalSHA256) Uint64(value uint64) {
	if !writer.ready() {
		return
	}
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	writer.write(encoded[:])
}

func (writer *CanonicalSHA256) Int64(value int64) {
	writer.Uint64(uint64(value))
}

func (writer *CanonicalSHA256) HexDigest() (string, error) {
	if writer == nil || writer.hash == nil {
		return "", fmt.Errorf("canonical writer is unavailable")
	}
	if writer.finalized {
		return "", fmt.Errorf("canonical writer already finalized")
	}
	writer.finalized = true
	if writer.err != nil {
		return "", writer.err
	}
	return hex.EncodeToString(writer.hash.Sum(nil)), nil
}

func (writer *CanonicalSHA256) ready() bool {
	if writer == nil || writer.hash == nil {
		return false
	}
	if writer.finalized {
		writer.err = fmt.Errorf("canonical writer already finalized")
		return false
	}
	return writer.err == nil
}

func (writer *CanonicalSHA256) write(value []byte) {
	if writer.err != nil {
		return
	}
	if _, err := writer.hash.Write(value); err != nil {
		writer.err = err
	}
}
