package utils

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
)

// TestMain initializes the package-level Logger so calls to Info/Warn/Debug
// inside the code under test don't panic on a nil *slog.Logger.
func TestMain(m *testing.M) {
	Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	os.Exit(m.Run())
}

// withTestAPIServer redirects makeAPIRequest at the given httptest.Server for
// the duration of the test, then restores the production URL on cleanup.
func withTestAPIServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Cleanup(SetAPIBaseURLForTest(srv.URL))
}
