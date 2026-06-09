package main

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// setSink points both the legacy log.* package and slog at w. Both are kept
// in sync because log.Fatalf (used for boot errors in main.go) still goes
// through stdlib log, while everything else now uses slog for levels.
func setSink(w io.Writer) {
	log.SetOutput(w)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func setupLogging(dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("log dir: %v", err)
		return
	}
	logPath := filepath.Join(dir, "tron.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("log file: %v", err)
		return
	}
	setSink(io.MultiWriter(os.Stderr, f))
	go rotateLogs(dir, logPath, f)
}

func rotateLogs(dir, logPath string, current *os.File) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		time.Sleep(time.Until(next))

		rotated := filepath.Join(dir, "tron-"+now.Format("2006-01-02")+".log")
		os.Rename(logPath, rotated)
		current.Close()

		pruneOldLogs(dir)

		current, _ = os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if current != nil {
			setSink(io.MultiWriter(os.Stderr, current))
		}
	}
}

func pruneOldLogs(dir string) {
	cutoff := time.Now().AddDate(0, 0, -7)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "tron-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, name))
		}
	}
}
