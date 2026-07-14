package backupasset

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCanonicalSHA256UsesLengthDelimitedBigEndianOneShotEncoding(t *testing.T) {
	writer := NewCanonicalSHA256()
	writer.String("é")
	writer.Uint8(0x7f)
	writer.Uint32(0x01020304)
	writer.Uint64(0x0102030405060708)
	writer.Int64(-2)
	got, err := writer.HexDigest()
	if err != nil {
		t.Fatalf("finalize canonical digest: %v", err)
	}
	wantBytes := []byte{
		0x00, 0x00, 0x00, 0x02, 0xc3, 0xa9,
		0x7f,
		0x01, 0x02, 0x03, 0x04,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe,
	}
	wantHash := sha256.Sum256(wantBytes)
	if got != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("canonical digest=%s, want %x", got, wantHash)
	}
	if _, err := writer.HexDigest(); err == nil {
		t.Fatal("canonical writer allowed a second finalization")
	}

	overflow := NewCanonicalSHA256()
	overflow.maxStringLength = 1
	overflow.String("é")
	if _, err := overflow.HexDigest(); err == nil {
		t.Fatal("canonical writer accepted a string longer than its uint32 length limit")
	}
}
