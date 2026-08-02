package transfer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/discohaus/discopanel/pkg/logger"
)

// Represents an active upload session
type UploadSession struct {
	ID             string
	Filename       string
	TotalSize      int64
	ChunkSize      int32
	TotalChunks    int32
	ReceivedChunks map[int32]bool
	BytesReceived  int64
	TempPath       string
	ExpiresAt      time.Time
	Completed      bool
	mu             sync.Mutex
	file           *os.File
}

func (s *UploadSession) expiry() time.Time {
	return s.ExpiresAt
}

// Closes the temp file handle if still open
func (s *UploadSession) closeFile() {
	s.mu.Lock()
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
	s.mu.Unlock()
}

// Handles upload sessions
type UploadManager struct {
	store         *store[*UploadSession]
	tempDir       string
	sessionTTL    time.Duration
	maxUploadSize int64
	log           *logger.Logger
}

// Creates a new upload manager
func NewUploadManager(tempDir string, sessionTTL time.Duration, maxUploadSize int64, log *logger.Logger) *UploadManager {
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		log.Error("Failed to create upload temp directory: %v", err)
	}
	return &UploadManager{
		store: newStore("upload", log, func(s *UploadSession) {
			s.closeFile()
			if s.TempPath != "" {
				os.Remove(s.TempPath)
			}
		}),
		tempDir:       tempDir,
		sessionTTL:    sessionTTL,
		maxUploadSize: maxUploadSize,
		log:           log,
	}
}

// Creates a new upload session
func (m *UploadManager) InitSession(filename string, totalSize int64, chunkSize int32) (*UploadSession, error) {
	if m.maxUploadSize > 0 && totalSize > m.maxUploadSize {
		return nil, ErrFileTooLarge
	}

	totalChunks := int32((totalSize + int64(chunkSize) - 1) / int64(chunkSize))
	if totalChunks == 0 {
		totalChunks = 1
	}

	sessionID := uuid.New().String()
	tempPath := filepath.Join(m.tempDir, fmt.Sprintf("upload-%s-%s", sessionID, sanitizeFilename(filename)))

	file, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Pre-allocate file size for efficient random writes
	if err := file.Truncate(totalSize); err != nil {
		file.Close()
		os.Remove(tempPath)
		return nil, fmt.Errorf("failed to allocate file space: %w", err)
	}

	session := &UploadSession{
		ID:             sessionID,
		Filename:       filename,
		TotalSize:      totalSize,
		ChunkSize:      chunkSize,
		TotalChunks:    totalChunks,
		ReceivedChunks: make(map[int32]bool),
		TempPath:       tempPath,
		ExpiresAt:      time.Now().Add(m.sessionTTL),
		file:           file,
	}
	m.store.put(sessionID, session)
	m.log.Info("Upload session created: %s (file: %s, size: %d, chunks: %d)", sessionID, filename, totalSize, totalChunks)
	return session, nil
}

// Retrieves a session by ID
func (m *UploadManager) GetSession(id string) (*UploadSession, error) {
	session, ok := m.store.get(id)
	if !ok {
		return nil, ErrSessionNotFound
	}
	if time.Now().After(session.ExpiresAt) {
		m.store.remove(id)
		return nil, ErrSessionExpired
	}
	return session, nil
}

// Writes a chunk to the session's temp file
func (m *UploadManager) WriteChunk(sessionID string, chunkIndex int32, data []byte) (completed bool, err error) {
	session, err := m.GetSession(sessionID)
	if err != nil {
		return false, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.Completed {
		return true, ErrSessionCompleted
	}
	if chunkIndex < 0 || chunkIndex >= session.TotalChunks {
		return false, ErrInvalidChunk
	}
	// Every chunk except the last must fill its slot exactly
	expected := int64(session.ChunkSize)
	if chunkIndex == session.TotalChunks-1 {
		expected = session.TotalSize - int64(session.ChunkSize)*int64(session.TotalChunks-1)
	}
	if int64(len(data)) != expected {
		return false, ErrInvalidChunk
	}
	// Re-upload of the same chunk is idempotent
	if session.ReceivedChunks[chunkIndex] {
		return session.Completed, nil
	}

	offset := int64(chunkIndex) * int64(session.ChunkSize)
	if _, err = session.file.WriteAt(data, offset); err != nil {
		return false, fmt.Errorf("failed to write chunk: %w", err)
	}

	session.ReceivedChunks[chunkIndex] = true
	session.BytesReceived += int64(len(data))

	if len(session.ReceivedChunks) >= int(session.TotalChunks) {
		session.Completed = true
		m.finishFile(session, sessionID)
	}
	return session.Completed, nil
}

// Syncs and closes a completed session's file, lock held
func (m *UploadManager) finishFile(session *UploadSession, sessionID string) {
	if err := session.file.Sync(); err != nil {
		m.log.Error("Failed to sync file for session %s: %v", sessionID, err)
	}
	if err := session.file.Close(); err != nil {
		m.log.Error("Failed to close file for session %s: %v", sessionID, err)
	}
	session.file = nil
	m.log.Info("Upload session completed: %s", sessionID)
}

// Writes reader data to session file at offset
func (m *UploadManager) WriteStream(sessionID string, r io.Reader, offset int64) (bytesWritten int64, completed bool, err error) {
	session, err := m.GetSession(sessionID)
	if err != nil {
		return 0, false, err
	}

	session.mu.Lock()
	if session.Completed {
		session.mu.Unlock()
		return 0, true, ErrSessionCompleted
	}
	if offset < 0 || offset > session.TotalSize {
		session.mu.Unlock()
		return 0, false, ErrInvalidOffset
	}
	file := session.file
	if file == nil {
		session.mu.Unlock()
		return 0, false, errors.New("session file not open")
	}
	session.mu.Unlock()

	buf := make([]byte, 256*1024)
	pos := offset

	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			// Writes past the declared size are rejected
			if pos+int64(n) > session.TotalSize {
				return pos - offset, false, ErrFileTooLarge
			}
			if _, writeErr := file.WriteAt(buf[:n], pos); writeErr != nil {
				return pos - offset, false, fmt.Errorf("failed to write: %w", writeErr)
			}
			pos += int64(n)

			session.mu.Lock()
			session.BytesReceived = pos
			session.mu.Unlock()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return pos - offset, false, readErr
		}
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.BytesReceived >= session.TotalSize && !session.Completed {
		session.Completed = true
		m.finishFile(session, sessionID)
	}
	return pos - offset, session.Completed, nil
}

// Lists chunk indices that haven't been received
func (m *UploadManager) GetMissingChunks(sessionID string) ([]int32, error) {
	session, err := m.GetSession(sessionID)
	if err != nil {
		return nil, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	var missing []int32
	for i := int32(0); i < session.TotalChunks; i++ {
		if !session.ReceivedChunks[i] {
			missing = append(missing, i)
		}
	}
	return missing, nil
}

// Returns the temp file path for a completed session
func (m *UploadManager) GetTempPath(sessionID string) (string, string, error) {
	session, err := m.GetSession(sessionID)
	if err != nil {
		return "", "", err
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if !session.Completed {
		return "", "", errors.New("upload not completed")
	}
	return session.TempPath, session.Filename, nil
}

// Returns the status of a session
func (m *UploadManager) GetSessionStatus(sessionID string) (bytesReceived int64, totalBytes int64, chunksReceived int32, totalChunks int32, completed bool, tempPath string, err error) {
	session, err := m.GetSession(sessionID)
	if err != nil {
		return 0, 0, 0, 0, false, "", err
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	tempPathResult := ""
	if session.Completed {
		tempPathResult = session.TempPath
	}
	return session.BytesReceived, session.TotalSize, int32(len(session.ReceivedChunks)), session.TotalChunks, session.Completed, tempPathResult, nil
}

// Cancels an upload session and cleans up
func (m *UploadManager) Cancel(sessionID string) error {
	session, ok := m.store.take(sessionID)
	if !ok {
		return ErrSessionNotFound
	}
	session.closeFile()
	if session.TempPath != "" {
		os.Remove(session.TempPath)
	}
	m.log.Info("Upload session cancelled: %s", sessionID)
	return nil
}

// Removes session only, caller already moved the temp file
func (m *UploadManager) CleanupSession(sessionID string) error {
	session, ok := m.store.take(sessionID)
	if !ok {
		return nil
	}
	session.closeFile()
	m.log.Info("Upload session cleaned up: %s", sessionID)
	return nil
}

// Stops the manager and cleans up all sessions
func (m *UploadManager) Stop() {
	m.store.stop()
}

// Removes potentially dangerous characters from filename
func sanitizeFilename(filename string) string {
	result := make([]byte, 0, len(filename))
	for i := 0; i < len(filename); i++ {
		c := filename[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			result = append(result, c)
		}
	}
	if len(result) == 0 {
		return "upload"
	}
	return string(result)
}
