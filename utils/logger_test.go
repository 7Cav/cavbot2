package utils

import (
	"os"
	"strings"
	"testing"

	"github.com/go-logfmt/logfmt"
)

// A logger from NewLogger writes where the bot log does, at the level
// LOG_LEVEL gives the bot log. A DEBUG record goes out with the bot log at
// DEBUG, and not with it at INFO.
func TestNewLoggerWritesAtTheBotLogsLevel(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  int
	}{
		{"DEBUG", 1},
		{"INFO", 0},
	} {
		t.Run(tc.level, func(t *testing.T) {
			records := stdoutRecords(t, func() {
				InitLogger(tc.level)
				logger, _ := NewLogger()
				logger.Debug("opening voice conn")
			})

			got := 0
			for _, r := range records {
				if r["level"] == "DEBUG" && r["msg"] == "opening voice conn" {
					got++
				}
			}
			if got != tc.want {
				t.Errorf("DEBUG records %q with LOG_LEVEL=%s = %d, want %d, in %v", "opening voice conn", tc.level, got, tc.want, records)
			}
		})
	}
}

// stdoutRecords runs f with the process's stdout in a file, and returns each
// line f wrote there, decoded as the logfmt the bot log writes. The bot log
// is left as it was.
func stdoutRecords(t *testing.T, f func()) []map[string]string {
	t.Helper()
	out, err := os.Create(t.TempDir() + "/stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	prevLogger, prevStdout := Logger, os.Stdout
	t.Cleanup(func() { Logger = prevLogger })
	os.Stdout = out
	f()
	os.Stdout = prevStdout

	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]string
	dec := logfmt.NewDecoder(strings.NewReader(string(b)))
	for dec.ScanRecord() {
		r := map[string]string{}
		for dec.ScanKeyval() {
			r[string(dec.Key())] = string(dec.Value())
		}
		records = append(records, r)
	}
	if err := dec.Err(); err != nil {
		t.Fatalf("stdout is not logfmt: %v\n%s", err, b)
	}
	return records
}
