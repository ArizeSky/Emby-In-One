package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type LogConfig struct {
	Level     string
	FileLevel string
	DataDir   string
	MaxBuffer int
	// MaxSizeBytes and KeepFiles bound the log file: it is rotated once it grows
	// past MaxSizeBytes, keeping KeepFiles backups. Zero means "use the default".
	MaxSizeBytes int64
	KeepFiles    int
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

type Logger struct {
	mu           sync.Mutex
	consoleLevel int
	fileLevel    int
	maxBuffer    int
	filePath     string
	file         *rotatingFile
	buffer       []LogEntry
}

const (
	// logBufferSlack lets the in-memory buffer overshoot before it is trimmed.
	logBufferSlack = 128
	// defaultLogMaxSize rotates the log file at 10 MiB, keeping 3 backups.
	defaultLogMaxSize = 10 << 20
	defaultLogKeep    = 3
)

var logLevels = map[string]int{
	"debug": 10,
	"info":  20,
	"warn":  30,
	"error": 40,
}

func NewLogger(cfg LogConfig) *Logger {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	_ = os.MkdirAll(dataDir, 0o755)

	filePath := filepath.Join(dataDir, "emby-in-one.log")

	maxBuffer := cfg.MaxBuffer
	if maxBuffer <= 0 {
		maxBuffer = 500
	}

	return &Logger{
		consoleLevel: levelValue(envOrDefault("LOG_LEVEL", cfg.Level, "info")),
		fileLevel:    levelValue(envOrDefault("FILE_LOG_LEVEL", cfg.FileLevel, "info")),
		maxBuffer:    maxBuffer,
		filePath:     filePath,
		file:         newRotatingFile(filePath, logMaxSize(cfg), logKeepFiles(cfg)),
		buffer:       make([]LogEntry, 0, maxBuffer),
	}
}

// logMaxSize is the rotation threshold in bytes: LOG_MAX_SIZE_MB (in MiB) wins
// over the config value, which wins over the default.
func logMaxSize(cfg LogConfig) int64 {
	if megabytes := envInt("LOG_MAX_SIZE_MB", 0); megabytes > 0 {
		return int64(megabytes) << 20
	}
	if cfg.MaxSizeBytes > 0 {
		return cfg.MaxSizeBytes
	}
	return defaultLogMaxSize
}

// logKeepFiles is the number of backups kept: LOG_KEEP wins over the config
// value, which wins over the default. Zero means "unset" for both.
func logKeepFiles(cfg LogConfig) int {
	if keep := envInt("LOG_KEEP", 0); keep > 0 {
		return keep
	}
	if cfg.KeepFiles > 0 {
		return cfg.KeepFiles
	}
	return defaultLogKeep
}

func defaultDataDir() string {
	if _, err := os.Stat("/app/data"); err == nil {
		return "/app/data"
	}
	return filepath.Clean(filepath.Join("data"))
}

func envOrDefault(envKey, configured, fallback string) string {
	if envValue := os.Getenv(envKey); envValue != "" {
		return envValue
	}
	if configured != "" {
		return configured
	}
	return fallback
}

// envInt reads a positive integer override from the environment, ignoring
// values that are absent, unparseable, or not positive.
func envInt(envKey string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func levelValue(level string) int {
	if v, ok := logLevels[strings.ToLower(level)]; ok {
		return v
	}
	return logLevels["info"]
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

func (l *Logger) log(level, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	level = strings.ToLower(level)
	entry := LogEntry{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Level:     level,
		Message:   message,
	}
	// Trim in batches: copying on every append once the buffer is full would
	// reallocate maxBuffer entries for every single log line.
	l.buffer = append(l.buffer, entry)
	if len(l.buffer) > l.maxBuffer+logBufferSlack {
		l.buffer = append([]LogEntry(nil), l.buffer[len(l.buffer)-l.maxBuffer:]...)
	}

	line := fmt.Sprintf("%s [%s] %s\n", entry.Timestamp, strings.ToUpper(level), message)
	if levelValue(level) >= l.consoleLevel {
		_, _ = os.Stdout.WriteString(line)
	}
	if l.file != nil && levelValue(level) >= l.fileLevel {
		_, _ = l.file.WriteString(line)
	}
}

// The in-memory buffer keeps every level regardless of the console/file filters, so
// it stays the full-fidelity view behind the admin log panel. Formatting therefore
// cannot be skipped for a message that is only filtered out of the file or stdout.
func (l *Logger) Debugf(format string, args ...any) { l.log("debug", fmt.Sprintf(format, args...)) }
func (l *Logger) Infof(format string, args ...any)  { l.log("info", fmt.Sprintf(format, args...)) }
func (l *Logger) Warnf(format string, args ...any)  { l.log("warn", fmt.Sprintf(format, args...)) }
func (l *Logger) Errorf(format string, args ...any) { l.log("error", fmt.Sprintf(format, args...)) }

func (l *Logger) Entries(limit int) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > l.maxBuffer {
		limit = l.maxBuffer
	}
	if limit > len(l.buffer) {
		limit = len(l.buffer)
	}
	start := len(l.buffer) - limit
	out := make([]LogEntry, limit)
	copy(out, l.buffer[start:])
	return out
}

func (l *Logger) FilePath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filePath
}

// ClearFile truncates the log file, removes its rotation backups, and empties the
// in-memory buffer.
func (l *Logger) ClearFile() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	if err := l.file.Clear(); err != nil {
		return err
	}
	l.buffer = l.buffer[:0]
	return nil
}
