// Package transfer manages chunked upload and download sessions
package transfer

import (
	"errors"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
)

var (
	ErrSessionNotFound  = errors.New("transfer session not found")
	ErrSessionExpired   = errors.New("transfer session expired")
	ErrSessionCompleted = errors.New("upload session already completed")
	ErrInvalidChunk     = errors.New("invalid chunk index")
	ErrInvalidOffset    = errors.New("offset outside declared upload size")
	ErrFileTooLarge     = errors.New("file exceeds maximum allowed size")
)

// Constraint for sessions the store can expire
type expirable interface {
	expiry() time.Time
}

// Expiring session registry with periodic cleanup
type store[S expirable] struct {
	mu       sync.RWMutex
	sessions map[string]S
	kind     string
	log      *logger.Logger
	release  func(S)
	stopCh   chan struct{}
}

func newStore[S expirable](kind string, log *logger.Logger, release func(S)) *store[S] {
	s := &store[S]{
		sessions: make(map[string]S),
		kind:     kind,
		log:      log,
		release:  release,
		stopCh:   make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *store[S]) put(id string, sess S) {
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
}

func (s *store[S]) get(id string) (S, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Removes a session without releasing its resources
func (s *store[S]) take(id string) (S, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	return sess, ok
}

// Removes one session and releases its resources
func (s *store[S]) remove(id string) {
	if sess, ok := s.take(id); ok {
		s.release(sess)
	}
}

func (s *store[S]) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.stopCh:
			return
		}
	}
}

func (s *store[S]) cleanupExpired() {
	s.mu.RLock()
	var expired []string
	now := time.Now()
	for id, sess := range s.sessions {
		if now.After(sess.expiry()) {
			expired = append(expired, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range expired {
		s.remove(id)
	}
	if len(expired) > 0 {
		s.log.Info("Cleaned up %d expired %s sessions", len(expired), s.kind)
	}
}

// Stops the loop and releases every session
func (s *store[S]) stop() {
	close(s.stopCh)
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = make(map[string]S)
	s.mu.Unlock()
	for _, sess := range sessions {
		s.release(sess)
	}
}
