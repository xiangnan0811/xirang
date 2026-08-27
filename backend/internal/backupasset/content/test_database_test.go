package content

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

var contentTestDBSequence atomic.Uint64

// contentTestDBName keeps shared in-memory SQLite fixtures isolated across
// repeated `go test -count` runs in the same test process.
func contentTestDBName(t testing.TB) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), contentTestDBSequence.Add(1))
}
