package logger

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Holds the buffer and active follow for one key
type LogStream struct {
	key         string
	logs        []*v1.LogEntry
	maxEntries  int
	containerID string
	active      bool
	gen         int // Stale follows check generation before touching state
	cancelFunc  context.CancelFunc
	mu          sync.RWMutex
}

// LogStreamer manages log buffers and container follows for all keys
type LogStreamer struct {
	docker      *client.Client
	streams     map[string]*LogStream // key -> stream
	mu          sync.RWMutex
	log         *Logger
	maxEntries  int
	subscribers map[string]map[chan *v1.LogEntry]bool // key -> set of subscriber channels
	subMu       sync.RWMutex
}

// NewLogStreamer creates a new log streamer
func NewLogStreamer(dockerClient *client.Client, log *Logger, maxEntriesPerStream int) *LogStreamer {
	if maxEntriesPerStream <= 0 {
		maxEntriesPerStream = 10000
	}
	return &LogStreamer{
		docker:      dockerClient,
		streams:     make(map[string]*LogStream),
		log:         log,
		maxEntries:  maxEntriesPerStream,
		subscribers: make(map[string]map[chan *v1.LogEntry]bool),
	}
}

func (ls *LogStreamer) getOrCreateStream(key string) *LogStream {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if stream, exists := ls.streams[key]; exists {
		return stream
	}
	stream := &LogStream{
		key:        key,
		logs:       make([]*v1.LogEntry, 0, ls.maxEntries),
		maxEntries: ls.maxEntries,
	}
	ls.streams[key] = stream
	return stream
}

// Attaches a container follow to the key
func (ls *LogStreamer) StartStreaming(key, containerID string) error {
	if key == "" || containerID == "" {
		return fmt.Errorf("log streaming requires a key and container ID")
	}

	stream := ls.getOrCreateStream(key)

	stream.mu.Lock()
	if stream.active && stream.containerID == containerID {
		stream.mu.Unlock()
		return nil
	}
	if stream.cancelFunc != nil {
		stream.cancelFunc()
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Unfollowed containers resume from container start time
	newContainer := stream.containerID != containerID
	stream.containerID = containerID
	stream.cancelFunc = cancel
	stream.active = true
	stream.gen++
	gen := stream.gen
	seedTail := len(stream.logs) == 0
	var since time.Time
	if !seedTail && !newContainer {
		since = stream.logs[len(stream.logs)-1].Timestamp.AsTime().Add(time.Nanosecond)
	}
	stream.mu.Unlock()

	go ls.streamLogs(ctx, stream, containerID, gen, seedTail, since)

	return nil
}

// Follows container output and appends to the stream buffer
func (ls *LogStreamer) streamLogs(ctx context.Context, stream *LogStream, containerID string, gen int, seedTail bool, since time.Time) {
	defer func() {
		stream.mu.Lock()
		if stream.gen == gen {
			stream.active = false
		}
		stream.mu.Unlock()
	}()

	// Check if container has TTY enabled
	inspect, err := ls.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		ls.log.Error("Failed to inspect container %s: %v", containerID, err)
		return
	}

	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	}
	if seedTail {
		options.Tail = "500"
	} else {
		if since.IsZero() {
			// New containers capture from their actual start
			if started, perr := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); perr == nil {
				since = started
			} else {
				since = time.Now()
			}
		}
		options.Since = since.Format(time.RFC3339Nano)
	}

	reader, err := ls.docker.ContainerLogs(ctx, containerID, options)
	if err != nil {
		ls.log.Error("Failed to start log streaming for container %s: %v", containerID, err)
		return
	}
	defer reader.Close()

	// Without TTY docker multiplexes the stream
	var logReader io.Reader
	if !inspect.Config.Tty {
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			_, err := stdcopy.StdCopy(pw, pw, reader)
			if err != nil && err != io.EOF {
				ls.log.Error("Error demultiplexing logs for container %s: %v", containerID, err)
			}
		}()
		logReader = pr
	} else {
		// TTY streams arrive raw without headers
		logReader = reader
	}

	scanner := bufio.NewScanner(logReader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for long lines

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			line := scanner.Text()
			// Split on \r carriage return and take last chunk
			if strings.Contains(line, "\r") {
				parts := strings.Split(line, "\r")
				line = parts[len(parts)-1]
			}

			// Filter out RCON spam
			if ls.shouldFilterLine(line) {
				continue
			}

			if line == "" {
				continue
			}

			level := detectLevel(line)
			entry := &v1.LogEntry{
				Timestamp: timestamppb.New(time.Now()),
				Message:   line,
				Level:     level,
				Source:    "stdout",
				IsCommand: false,
				IsError:   level == "error" || level == "fatal",
			}

			stream.mu.Lock()
			if stream.gen != gen {
				// A newer follow replaced this one, stop writing
				stream.mu.Unlock()
				return
			}
			stream.appendLocked(entry)
			stream.mu.Unlock()

			ls.broadcast(stream.key, entry)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		ls.log.Error("Error reading logs for container %s: %v", containerID, err)
	}
}

// Appends an entry and trims, callers hold mu
func (s *LogStream) appendLocked(entry *v1.LogEntry) {
	s.logs = append(s.logs, entry)
	if len(s.logs) > s.maxEntries {
		s.logs = s.logs[len(s.logs)-s.maxEntries:]
	}
}

// Finds the log4j level token in the line frame
var levelPattern = regexp.MustCompile(`\[(?:[^\]]*[/ ])?(TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL)\]`)

// Matches supervisor tagged warn and error lines
var runtimeLevelPattern = regexp.MustCompile(`^\[discoruntime\] (WARN|ERROR)`)

// Pulls the log level from a console line
func detectLevel(line string) string {
	// Frame sits at the front so skip chat bodies
	head := line
	if len(head) > 96 {
		head = head[:96]
	}
	if m := runtimeLevelPattern.FindStringSubmatch(head); m != nil {
		return strings.ToLower(m[1])
	}
	if m := levelPattern.FindStringSubmatch(head); m != nil {
		if m[1] == "WARNING" {
			return "warn"
		}
		return strings.ToLower(m[1])
	}
	return "info"
}

// shouldFilterLine checks if a log line should be filtered
func (ls *LogStreamer) shouldFilterLine(line string) bool {
	// Filter out RCON connection spam
	filters := []string{
		"Thread RCON Client",
		"[RCON Listener",
		"Rcon connection from",
	}

	for _, filter := range filters {
		if strings.Contains(line, filter) {
			return true
		}
	}

	return false
}

// Records a panel side setup line in the console
func (ls *LogStreamer) AddSystemEntry(key, message string) {
	stream := ls.getOrCreateStream(key)

	entry := &v1.LogEntry{
		Timestamp: timestamppb.New(time.Now()),
		Message:   "[setup] " + message,
		Level:     "info",
		Source:    "system",
		IsCommand: false,
		IsError:   false,
	}

	stream.mu.Lock()
	stream.appendLocked(entry)
	stream.mu.Unlock()

	ls.broadcast(key, entry)
}

// AddCommandEntry records command input in the stream
func (ls *LogStreamer) AddCommandEntry(key, command string, timestamp time.Time) {
	stream := ls.getOrCreateStream(key)

	// ANSI reset prefix prevents color bleed from prior output
	entry := &v1.LogEntry{
		Timestamp: timestamppb.New(timestamp),
		Message:   "\u001b[0m" + command,
		Level:     "debug",
		Source:    "command",
		IsCommand: true,
		IsError:   false,
	}

	stream.mu.Lock()
	stream.appendLocked(entry)
	stream.mu.Unlock()

	ls.broadcast(key, entry)
}

// AddCommandOutput records command output in the stream (after execution)
func (ls *LogStreamer) AddCommandOutput(key, output string, success bool, timestamp time.Time) {
	stream := ls.getOrCreateStream(key)

	var entries []*v1.LogEntry

	stream.mu.Lock()

	// Adds output entry, ANSI reset prevents color bleed
	if output != "" {
		output = "\u001b[0m" + output + "\u001b[0m"
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line != "" {
				entry := &v1.LogEntry{
					Timestamp: timestamppb.New(timestamp),
					Message:   line,
					Level:     "debug",
					Source:    "command_output",
					IsCommand: false,
					IsError:   !success,
				}
				stream.appendLocked(entry)
				entries = append(entries, entry)
			}
		}
	} else if !success {
		// Add error message if command failed with no output
		entry := &v1.LogEntry{
			Timestamp: timestamppb.New(timestamp),
			Message:   "Command failed to execute",
			Level:     "error",
			Source:    "command_output",
			IsCommand: false,
			IsError:   true,
		}
		stream.appendLocked(entry)
		entries = append(entries, entry)
	}

	stream.mu.Unlock()

	for _, entry := range entries {
		ls.broadcast(key, entry)
	}
}

// GetLogs gets buffered logs for a key
func (ls *LogStreamer) GetLogs(key string, tail int) []*v1.LogEntry {
	ls.mu.RLock()
	stream, exists := ls.streams[key]
	ls.mu.RUnlock()

	if !exists {
		return []*v1.LogEntry{}
	}

	stream.mu.RLock()
	defer stream.mu.RUnlock()

	if tail <= 0 || tail > len(stream.logs) {
		result := make([]*v1.LogEntry, len(stream.logs))
		copy(result, stream.logs)
		return result
	}

	start := len(stream.logs) - tail
	result := make([]*v1.LogEntry, tail)
	copy(result, stream.logs[start:])
	return result
}

// ClearLogs clears buffered logs for a key
func (ls *LogStreamer) ClearLogs(key string) {
	ls.mu.RLock()
	stream, exists := ls.streams[key]
	ls.mu.RUnlock()

	if exists {
		stream.mu.Lock()
		stream.logs = make([]*v1.LogEntry, 0, stream.maxEntries)
		stream.mu.Unlock()
	}
}

// Creates a channel receiving new entries for a key
func (ls *LogStreamer) Subscribe(key string) chan *v1.LogEntry {
	ls.subMu.Lock()
	defer ls.subMu.Unlock()

	ch := make(chan *v1.LogEntry, 100) // Buffered channel

	if ls.subscribers[key] == nil {
		ls.subscribers[key] = make(map[chan *v1.LogEntry]bool)
	}
	ls.subscribers[key][ch] = true

	return ch
}

// Unsubscribe removes and closes a subscriber channel for a key
func (ls *LogStreamer) Unsubscribe(key string, ch chan *v1.LogEntry) {
	ls.subMu.Lock()
	defer ls.subMu.Unlock()

	if subs, ok := ls.subscribers[key]; ok {
		if _, exists := subs[ch]; !exists {
			return
		}
		delete(subs, ch)
		close(ch)
		if len(subs) == 0 {
			delete(ls.subscribers, key)
		}
	}
}

// Sends a log entry to all key subscribers
func (ls *LogStreamer) broadcast(key string, entry *v1.LogEntry) {
	ls.subMu.RLock()
	subs, ok := ls.subscribers[key]
	if !ok || len(subs) == 0 {
		ls.subMu.RUnlock()
		return
	}

	// Copy subscriber list to avoid holding lock during sends
	channels := make([]chan *v1.LogEntry, 0, len(subs))
	for ch := range subs {
		channels = append(channels, ch)
	}
	ls.subMu.RUnlock()

	// Non-blocking send to all subscribers
	for _, ch := range channels {
		select {
		case ch <- entry:
		default:
			// Channel full, skip this entry for this subscriber
		}
	}
}
