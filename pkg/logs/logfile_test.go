package logs

import (
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestSetupMessageOnlyLogFileInternal(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	logFile, err := SetupMessageOnlyLogFile(t.TempDir(), "updater-test", slog.LevelInfo)
	if err != nil {
		t.Fatalf("SetupMessageOnlyLogFile() error = %v", err)
	}
	t.Cleanup(func() {
		if err := logFile.Close(); err != nil {
			t.Errorf("close log file: %v", err)
		}
	})

	slog.Info("container updated", "container", "web")
	if err := logFile.Sync(); err != nil {
		t.Fatalf("sync log file: %v", err)
	}

	content, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "container updated") || !strings.Contains(got, `container="web"`) {
		t.Fatalf("log file content = %q, want message-only entry with attrs", got)
	}
}
