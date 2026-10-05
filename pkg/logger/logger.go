package logger

import (
	"fmt"
	"github.com/discohaus/discopanel/pkg/config"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

type Logger struct {
	*log.Logger
	fileLogger *lumberjack.Logger
	mu         sync.Mutex
	buffer     []string
	maxBuffer  int
}

// Plain stdout logger, used by tests
func New() *Logger {
	return &Logger{
		Logger:    log.New(os.Stdout, "", 0),
		buffer:    make([]string, 0, 1000),
		maxBuffer: 1000,
	}
}

func NewWithConfig(cfg *config.LoggingConfig) *Logger {
	writers := []io.Writer{os.Stdout}

	var fileLogger *lumberjack.Logger
	if cfg != nil && cfg.Enabled && cfg.FilePath != "" {
		fileLogger = &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    cfg.MaxSize,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAge,
			Compress:   cfg.Compress,
		}
		writers = append(writers, fileLogger)
	}

	multiWriter := io.MultiWriter(writers...)

	return &Logger{
		Logger:     log.New(multiWriter, "", 0),
		fileLogger: fileLogger,
		buffer:     make([]string, 0, 1000),
		maxBuffer:  1000,
	}
}

func (l *Logger) log(level, format string, args ...any) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, message)

	// Store in buffer for support bundle generation
	l.mu.Lock()
	l.buffer = append(l.buffer, logLine)
	if len(l.buffer) > l.maxBuffer {
		// Keep only the last maxBuffer entries
		l.buffer = l.buffer[len(l.buffer)-l.maxBuffer:]
	}
	l.mu.Unlock()

	l.Printf("%s", logLine)
}

func (l *Logger) Info(format string, args ...any) {
	l.log("INFO", format, args...)
}

func (l *Logger) Error(format string, args ...any) {
	l.log("ERROR", format, args...)
}

func (l *Logger) Warn(format string, args ...any) {
	l.log("WARN", format, args...)
}

func (l *Logger) Debug(format string, args ...any) {
	l.log("DEBUG", format, args...)
}

func (l *Logger) Fatal(format string, args ...any) {
	l.log("FATAL", format, args...)
	os.Exit(1)
}

func (l *Logger) GetRecentLogs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Copy buffer
	logs := make([]string, len(l.buffer))
	copy(logs, l.buffer)
	return logs
}

// Close file logger
func (l *Logger) Close() error {
	if l.fileLogger != nil {
		return l.fileLogger.Close()
	}
	return nil
}

// Get current log file path
func (l *Logger) GetLogFilePath() string {
	if l.fileLogger != nil {
		return l.fileLogger.Filename
	}
	return ""
}
