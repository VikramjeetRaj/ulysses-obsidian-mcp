package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// New opens logPath for appending and returns a slog logger that writes text records to it.
// The file stays open for the life of the process.
func New(logPath string) (*slog.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return slog.New(slog.NewTextHandler(file, nil)), nil
}
