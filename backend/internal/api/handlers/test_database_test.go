package handlers

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

var handlerTestDBSequence atomic.Uint64

// handlerTestDBName keeps shared in-memory SQLite fixtures isolated across
// repeated `go test -count` runs in the same test process.
func handlerTestDBName(t testing.TB) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), handlerTestDBSequence.Add(1))
}
