package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"
)

func Hash(pw string) string {
	s := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(s[:])
}

func Check(gotHash, want string) bool {
	// 恒定时比较，防止时序侧信道
	return len(want) > 0 && constantTimeEqual(gotHash, Hash(want))
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func RandomDigits(n int) (string, error) {
	b := make([]byte, n)
	for i := range b {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		b[i] = '0' + byte(d.Int64())
	}
	return string(b), nil
}

func RandomSecret(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func NewToken() string {
	return RandomSecret("", 32)
}

type SessionStore struct {
	mu      sync.Mutex
	tokens  map[string]time.Time
	ttl     time.Duration
	enabled bool
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{
		tokens:  map[string]time.Time{},
		ttl:     ttl,
		enabled: true,
	}
}

func (s *SessionStore) Create() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := NewToken()
	s.tokens[token] = time.Now().Add(s.ttl)
	return token
}

func (s *SessionStore) Valid(token string) bool {
	if !s.enabled || token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.tokens, token)
		return false
	}
	return true
}

func (s *SessionStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = map[string]time.Time{}
}

func (s *SessionStore) Disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	s.tokens = map[string]time.Time{}
}

// APIKeyAuth 校验 /v1 的 Bearer Token。
type APIKeyAuth struct {
	mu    sync.RWMutex
	spec  map[string]bool // 明文 key -> 是否启用
	extra map[string]bool // 运行时动态 key（启动自动生成的）
}

func NewAPIKeyAuth(configured []string) *APIKeyAuth {
	a := &APIKeyAuth{spec: map[string]bool{}, extra: map[string]bool{}}
	for _, k := range configured {
		if k != "" {
			a.spec[k] = true
		}
	}
	return a
}

func (a *APIKeyAuth) AddRuntime(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.extra[key] = true
}

func (a *APIKeyAuth) Valid(bearer string) bool {
	if bearer == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.spec[bearer] || a.extra[bearer]
}

func (a *APIKeyAuth) Empty() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.spec) == 0 && len(a.extra) == 0
}

func LogSecurity(label, secret string) {
	fmt.Printf("\033[1;36m┌──────────────────────────────────────────\033[0m\n")
	fmt.Printf("\033[1;36m│\033[0m %s\033[0m\n", label)
	fmt.Printf("\033[1;36m│\033[0m   \033[1;97m%s\033[0m\n", secret)
	fmt.Printf("\033[1;36m└──────────────────────────────────────────\033[0m\n")
}