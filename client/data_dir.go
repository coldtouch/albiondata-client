package client

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/ao-data/albiondata-client/log"
)

// The NSIS installer defaults to C:\Program Files\Albion Data Client, which an
// unelevated process cannot write to. Before this helper existed every
// user-writable file (albiondata-client.log, config.yaml token save, logs\)
// silently failed on a default install: no log file ever appeared and device
// auth re-prompted on every launch because the token could not be persisted.
//
// DataDir prefers the executable's directory so users who installed to a
// writable location (or run a portable copy) keep the historical layout, and
// falls back to %LOCALAPPDATA%\AlbionDataClient otherwise.

var (
	dataDirOnce  sync.Once
	dataDirValue string
)

// dirWritable reports whether we can create a file inside dir.
func dirWritable(dir string) bool {
	probe := filepath.Join(dir, ".adc-write-probe")
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return false
	}
	f.Close()
	_ = os.Remove(probe)
	return true
}

// DataDir returns the directory for user-writable files (log file, config.yaml
// token save, logs\). Always returns an existing directory.
func DataDir() string {
	dataDirOnce.Do(func() {
		if exePath, err := os.Executable(); err == nil {
			exeDir := filepath.Dir(exePath)
			if dirWritable(exeDir) {
				dataDirValue = exeDir
				return
			}
		}

		base, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
		if err != nil {
			// Last resort: current working directory.
			dataDirValue = "."
			return
		}
		fallback := filepath.Join(base, "AlbionDataClient")
		if err := os.MkdirAll(fallback, 0755); err != nil {
			dataDirValue = "."
			return
		}
		dataDirValue = fallback
		log.Infof("[Config] Install directory is not writable — logs and config will be stored in %s", fallback)
	})
	return dataDirValue
}

// LogsDir returns DataDir()\logs, creating it if needed. Used for loot-event
// files, chest logs, unknown-event dumps, the relay spool and server-state.
func LogsDir() string {
	dir := filepath.Join(DataDir(), "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		// Fall back to a relative logs dir; callers treat failures softly.
		dir = "logs"
		_ = os.MkdirAll(dir, 0755)
	}
	return dir
}
