// Package runtimelog owns the desktop's frontend/backend files, not business data.
package runtimelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const RetentionDays = 30

// Entry is deliberately flat: no request bodies, arbitrary objects or headers.
type Entry struct {
	Timestamp  string `json:"timestamp,omitempty"`
	Level      string `json:"level"`
	Category   string `json:"category,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	Stack      string `json:"stack,omitempty"`
	TaskID     string `json:"taskId,omitempty"`
	CanvasID   string `json:"canvasId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	TraceID    string `json:"traceId,omitempty"`
	LaunchID   string `json:"launchId,omitempty"`
	Status     string `json:"status,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Route      string `json:"route,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

type Logger struct {
	mu     sync.Mutex
	dir    string
	now    func() time.Time
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	closed bool
}

// ProgramDirectory uses the executable, never the caller's working directory.
// On macOS keep Logs beside the app bundle, not inside its signed contents.
func ProgramDirectory(executable string) string {
	dir := filepath.Dir(executable)
	if filepath.Base(dir) == "MacOS" && filepath.Base(filepath.Dir(dir)) == "Contents" {
		bundle := filepath.Dir(filepath.Dir(dir))
		if strings.HasSuffix(strings.ToLower(bundle), ".app") {
			return filepath.Dir(bundle)
		}
	}
	return dir
}

func OpenProgram() (*Logger, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return Open(filepath.Join(ProgramDirectory(executable), "Logs"))
}

func Open(dir string) (*Logger, error) {
	l := &Logger{dir: dir, now: time.Now, stop: make(chan struct{}), done: make(chan struct{})}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("创建 Logs 目录失败，请检查程序目录写入权限: %w", err)
	}
	if err := l.cleanup(); err != nil {
		return nil, err
	}
	for _, source := range []string{"frontend", "backend"} {
		if err := l.Append(source, []Entry{{Level: "info", Category: "runtime", Code: "log_opened", Message: "Log stream opened; retention 30 days"}}); err != nil {
			return nil, err
		}
	}
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-ticker.C:
				l.Backend(Entry{Level: "info", Category: "runtime", Code: "runtime_alive", Message: fmt.Sprintf("Process running; goroutines=%d", runtime.NumGoroutine())})
				if err := l.cleanup(); err != nil {
					fmt.Fprintln(os.Stderr, "Logs retention cleanup failed:", err)
				}
			}
		}
	}()
	return l, nil
}

func (l *Logger) Append(source string, entries []Entry) error {
	if l == nil {
		return errors.New("runtime logs not initialized")
	}
	if source != "frontend" && source != "backend" {
		return errors.New("invalid log source")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("runtime logs closed")
	}
	now := l.now()
	f, err := os.OpenFile(filepath.Join(l.dir, source+"-"+now.Format("2006-01-02")+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	var data []byte
	for _, entry := range entries {
		entry = sanitize(entry)
		if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
			entry.Timestamp = now.UTC().Format(time.RFC3339Nano)
		}
		row := struct {
			Entry
			Source     string `json:"source"`
			RecordedAt string `json:"recordedAt"`
			PID        int    `json:"pid"`
		}{entry, source, now.UTC().Format(time.RFC3339Nano), os.Getpid()}
		encoded, err := json.Marshal(row)
		if err != nil {
			_ = f.Close()
			return err
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	written, err := f.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return errors.Join(err, f.Close())
}

func (l *Logger) Backend(entry Entry) {
	if l == nil {
		return
	}
	if err := l.Append("backend", []Entry{entry}); err != nil {
		// Never call the redirected log package from its own sink.
		fmt.Fprintln(os.Stderr, "Logs write failed:", err)
	}
}

func (l *Logger) Writer() io.Writer { return standardWriter{l} }

func (l *Logger) cleanup() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	now := l.now()
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(RetentionDays - 1))
	var failures []error
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if !logName.MatchString(name) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(name, "frontend-"), "backend-"), ".log")
		date, err := time.ParseInLocation("2006-01-02", day, now.Location())
		if err == nil && date.Before(cutoff) {
			if err := os.Remove(filepath.Join(l.dir, name)); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		l.Backend(Entry{Level: "info", Category: "runtime", Code: "runtime_stopped", Message: "Process shutdown"})
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
	})
}

type standardWriter struct{ logger *Logger }

func (w standardWriter) Write(p []byte) (int, error) {
	message := string(p)
	level := "info"
	lower := strings.ToLower(message)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "failure") || strings.Contains(message, "异常") || strings.Contains(message, "失败") {
		level = "error"
	} else if match := httpStatusCode.FindStringSubmatch(message); len(match) == 2 {
		if status, err := strconv.Atoi(match[1]); err == nil {
			if status >= 500 {
				level = "error"
			} else if status >= 400 {
				level = "warning"
			}
		}
	}
	err := w.logger.Append("backend", []Entry{{Level: level, Category: "runtime", Code: "backend_log", Message: message}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Logs write failed:", err)
	}
	return len(p), nil // A diagnostic disk failure must not break business work.
}

func (l *Logger) RedirectStandardLog() func() {
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, standardWriter{l}))
	return func() { log.SetOutput(previous) }
}

var (
	logName        = regexp.MustCompile(`^(frontend|backend)-\d{4}-\d{2}-\d{2}\.log$`)
	secretField    = regexp.MustCompile(`(?i)["']?(authorization|cookie|set-cookie|x-goog-api-key|x-canvas-upstream-headers|api[_-]?key|secret[_-]?key|password|token|pairing[_-]?code|配对码|prompt|input|query)["']?\s*[:=：][^\r\n]*`)
	secretValue    = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+|\bsk-[A-Za-z0-9_-]{8,}|\beyJ[A-Za-z0-9_-]{12,}(?:\.[A-Za-z0-9_-]+)*`)
	urlValue       = regexp.MustCompile(`(?i)(https?://|blob:|data:)[^\s"'<>]+`)
	pathValue      = regexp.MustCompile(`(?:[A-Za-z]:[\\/]|/(?:Users|home|private|tmp|var|Volumes|mnt|media|root|etc)/)[^\r\n:"'<> ]+`)
	httpStatusCode = regexp.MustCompile(`\s([45]\d\d)\s`)
)

func Redact(value string, limit int) string {
	value = secretField.ReplaceAllString(strings.TrimSpace(value), "$1=[REDACTED]")
	value = secretValue.ReplaceAllString(value, "[SECRET_REDACTED]")
	value = urlValue.ReplaceAllString(value, "[URL_REDACTED]")
	value = pathValue.ReplaceAllString(value, "[PATH_REDACTED]")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func sanitize(e Entry) Entry {
	for _, field := range []*string{&e.Category, &e.Code, &e.TaskID, &e.CanvasID, &e.NodeID, &e.RequestID, &e.TraceID, &e.LaunchID, &e.Status, &e.Reason} {
		*field = Redact(*field, 120)
	}
	e.Message = Redact(e.Message, 2000)
	e.Stack = Redact(e.Stack, 4000)
	e.Route = Redact(strings.SplitN(strings.SplitN(e.Route, "?", 2)[0], "#", 2)[0], 300)
	e.Timestamp = Redact(e.Timestamp, 64)
	switch e.Level {
	case "info", "warning", "error", "debug":
	default:
		e.Level = "info"
	}
	e.DurationMs = min(max(e.DurationMs, 0), 86400000)
	e.HTTPStatus = min(max(e.HTTPStatus, 0), 599)
	return e
}
