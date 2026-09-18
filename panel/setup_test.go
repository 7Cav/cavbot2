package panel

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/7cav/cavbot2/utils"
)

// TestMain initialises the utils Logger once, so the panel's log lines under
// test do not dereference a nil *slog.Logger. Written once, before any test
// goroutine, the same arrangement as the commands package.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	os.Exit(m.Run())
}
