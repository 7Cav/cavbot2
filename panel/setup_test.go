package panel_test

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/7cav/cavbot2/utils"
)

// TestMain initializes the utils package Logger once, before anything runs,
// so the handlers' utils.Info/Warn calls never hit a nil *slog.Logger. One
// assignment, as the commands package does it.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	os.Exit(m.Run())
}
