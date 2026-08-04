// Package config provides configuration management utilities for the Wild Panel
// application, including version information, logging levels, database paths,
// and environment variable handling.
package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed version
var version string

//go:embed name
var name string

// LogLevel represents the logging level for the application.
type LogLevel string

// Logging level constants
const (
	Debug   LogLevel = "debug"
	Info    LogLevel = "info"
	Notice  LogLevel = "notice"
	Warning LogLevel = "warning"
	Error   LogLevel = "error"
)

// GetVersion returns the version string of Wild Panel.
func GetVersion() string {
	return strings.TrimSpace(version)
}

// GetName returns the display name of Wild Panel.
func GetName() string {
	return strings.TrimSpace(name)
}

// envFirst returns the first non-empty value among the given environment keys.
func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// GetLogLevel returns the current logging level based on environment variables or defaults to Info.
func GetLogLevel() LogLevel {
	if IsDebug() {
		return Debug
	}
	logLevel := envFirst("WILDPANEL_LOG_LEVEL", "VPNUI_LOG_LEVEL")
	if logLevel == "" {
		return Info
	}
	return LogLevel(logLevel)
}

// IsDebug returns true if debug mode is enabled.
func IsDebug() bool {
	return envFirst("WILDPANEL_DEBUG", "VPNUI_DEBUG") == "true"
}

// GetBinFolderPath returns the path to the binary folder.
func GetBinFolderPath() string {
	binFolderPath := envFirst("WILDPANEL_BIN_FOLDER", "VPNUI_BIN_FOLDER")
	if binFolderPath == "" {
		binFolderPath = "bin"
	}
	return binFolderPath
}

func getBaseDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	exeDir := filepath.Dir(exePath)
	exeDirLower := strings.ToLower(filepath.ToSlash(exeDir))
	if strings.Contains(exeDirLower, "/appdata/local/temp/") || strings.Contains(exeDirLower, "/go-build") {
		wd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return wd
	}
	return exeDir
}

// GetDBFolderPath returns the folder that holds the database file. It defaults to
// the directory of the binary (overridable with WILDPANEL_DB_FOLDER / VPNUI_DB_FOLDER).
func GetDBFolderPath() string {
	dbFolderPath := envFirst("WILDPANEL_DB_FOLDER", "VPNUI_DB_FOLDER")
	if dbFolderPath != "" {
		return dbFolderPath
	}
	return getBaseDir()
}

// dbBaseName is the database file's base name (without extension).
const dbBaseName = "wild-panel"

// GetDBPath returns the full path to the database file (next to the binary).
func GetDBPath() string {
	return fmt.Sprintf("%s/%s.db", GetDBFolderPath(), dbBaseName)
}

// LegacyDBPaths lists previous database names next to the binary to migrate from
// on first init when the current DB doesn't exist yet.
func LegacyDBPaths() []string {
	if envFirst("WILDPANEL_DB_FOLDER", "VPNUI_DB_FOLDER") != "" {
		return nil
	}
	current := GetDBPath()
	var out []string
	for _, p := range []string{
		fmt.Sprintf("%s/vpn-ui.db", GetDBFolderPath()),
		fmt.Sprintf("%s/x-ui.db", GetDBFolderPath()),
	} {
		if p != current {
			out = append(out, p)
		}
	}
	return out
}

// GetLogFolder returns the path to the log folder.
func GetLogFolder() string {
	logFolderPath := envFirst("WILDPANEL_LOG_FOLDER", "VPNUI_LOG_FOLDER")
	if logFolderPath != "" {
		return logFolderPath
	}
	return "/var/log/wild-panel"
}

// DB migration (moving/renaming a legacy database to GetDBPath) is handled
// cross-platform by database.InitDB via config.LegacyDBPaths — see migrateLegacyDB.
