package zed

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// SessionTTL is how long a pending sign-in may wait for the operator to paste
	// the callback URL back.
	SessionTTL = 15 * time.Minute

	sessionCleanupEvery = 32
	sessionCleanupMin   = 32
)

// AuthSession is the server-side half of one sign-in attempt.
//
// The RSA private key stays here rather than going to the browser, because the
// callback's access_token is encrypted to the matching public key and can only be
// opened by this process. A lost session therefore makes its callback
// undecryptable and the operator must restart sign-in.
type AuthSession struct {
	PrivateKeyPEM string
	CreatedAt     time.Time
}

// SessionStore holds pending sign-in sessions in memory.
//
// Note this is per-process: in a multi-replica deployment the auth-URL request
// and the callback exchange must reach the same instance, or the flow fails with
// "session not found". The same constraint applies to the other OAuth platforms
// here.
type SessionStore struct {
	mu       sync.RWMutex
	data     map[string]*AuthSession
	setCount uint64
}

func NewSessionStore() *SessionStore {
	return &SessionStore{data: make(map[string]*AuthSession)}
}

func (s *SessionStore) Get(id string) (*AuthSession, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.data[id]
	if ok && sessionExpired(session, now) {
		delete(s.data, id)
		return nil, false
	}
	return session, ok
}

func (s *SessionStore) Set(id string, session *AuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCount++
	if len(s.data) >= sessionCleanupMin && s.setCount%sessionCleanupEvery == 0 {
		s.pruneExpiredLocked(time.Now())
	}
	s.data[id] = session
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
}

func (s *SessionStore) pruneExpiredLocked(now time.Time) {
	for id, session := range s.data {
		if sessionExpired(session, now) {
			delete(s.data, id)
		}
	}
}

func sessionExpired(session *AuthSession, now time.Time) bool {
	if session == nil {
		return true
	}
	return now.Sub(session.CreatedAt) > SessionTTL
}

// NewSessionID returns an unguessable session identifier.
func NewSessionID() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is not recoverable here; fall back to a
		// time-derived value rather than returning an empty session id.
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
