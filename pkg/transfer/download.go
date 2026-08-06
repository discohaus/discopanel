package transfer

import (
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/discohaus/discopanel/pkg/logger"
)

// A prepared download
type DownloadSession struct {
	ID          string
	FilePath    string // file to serve
	Filename    string // suggested download filename
	TotalSize   int64
	DeleteAfter bool // if true, file is deleted on session cleanup
	ExpiresAt   time.Time
}

func (s *DownloadSession) expiry() time.Time {
	return s.ExpiresAt
}

// Handles download sessions and their temp-file lifecycle
type DownloadManager struct {
	store      *store[*DownloadSession]
	tempDir    string
	sessionTTL time.Duration
	log        *logger.Logger
}

// New download manager and starts the cleanup loop
func NewDownloadManager(tempDir string, sessionTTL time.Duration, log *logger.Logger) *DownloadManager {
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		log.Error("Failed to create download temp directory: %v", err)
	}
	return &DownloadManager{
		store: newStore("download", log, func(s *DownloadSession) {
			if s.DeleteAfter && s.FilePath != "" {
				os.Remove(s.FilePath)
			}
		}),
		tempDir:    tempDir,
		sessionTTL: sessionTTL,
		log:        log,
	}
}

func (m *DownloadManager) TempDir() string {
	return m.tempDir
}

// Registers a downloadable file, optionally deleted on cleanup
func (m *DownloadManager) InitSession(filePath, filename string, totalSize int64, deleteAfter bool) *DownloadSession {
	session := &DownloadSession{
		ID:          uuid.New().String(),
		FilePath:    filePath,
		Filename:    filename,
		TotalSize:   totalSize,
		DeleteAfter: deleteAfter,
		ExpiresAt:   time.Now().Add(m.sessionTTL),
	}
	m.store.put(session.ID, session)
	// Id is the credential, logs get a stub only
	m.log.Info("Download session created: %s (file: %s, size: %d)", session.ID[:8], filename, totalSize)
	return session
}

// Get session by ID
func (m *DownloadManager) GetSession(id string) (*DownloadSession, error) {
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

// Cleanup session and its temp file
func (m *DownloadManager) CleanupSession(id string) {
	m.store.remove(id)
}

// Stop manager and clean up sessions
func (m *DownloadManager) Stop() {
	m.store.stop()
}
