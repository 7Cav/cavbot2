package panel

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/go-logfmt/logfmt"
)

// syncBuffer is a concurrency-safe sink for the package-wide test logger:
// the runtime under the panel logs from its own goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// testLogs is where every log line in this package's tests lands.
var testLogs = &syncBuffer{}

// TestMain initialises the utils Logger once, so the panel's log lines under
// test do not dereference a nil *slog.Logger. Written once, before any test
// goroutine, the same arrangement as the commands package: a later write
// would race the runtime's goroutines that read it. INFO is the most verbose
// level any assertion needs.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(testLogs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	os.Exit(m.Run())
}

// captureLogs clears the package-wide log sink and returns a reader of the
// records written since, each decoded with a real logfmt decoder so expected
// values stay independent of the emitter.
func captureLogs(t *testing.T) func() []map[string]string {
	t.Helper()
	testLogs.Reset()
	return func() []map[string]string {
		var records []map[string]string
		for _, raw := range strings.Split(strings.TrimRight(testLogs.String(), "\n"), "\n") {
			if raw == "" {
				continue
			}
			record := map[string]string{}
			dec := logfmt.NewDecoder(strings.NewReader(raw))
			for dec.ScanRecord() {
				for dec.ScanKeyval() {
					record[string(dec.Key())] = string(dec.Value())
				}
			}
			if err := dec.Err(); err != nil {
				t.Fatalf("log line is not decodable as logfmt: %v\nline: %s", err, raw)
			}
			records = append(records, record)
		}
		return records
	}
}
