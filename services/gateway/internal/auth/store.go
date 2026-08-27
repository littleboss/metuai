package auth

import (
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrDuplicateEmail = errors.New("email_already_registered")
	ErrNotFound       = errors.New("user_not_found")
	ErrBadPassword    = errors.New("invalid_credentials")
)

// User 是可登录的本地账号（密码 bcrypt）。
type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	CreatedAt    time.Time
}

// UserStore 持久化注册用户。
type UserStore interface {
	CreateUser(email, passwordHash, displayName string) (User, error)
	FindByEmail(email string) (User, error)
}

type MemoryStore struct {
	mu     sync.Mutex
	byMail map[string]User
	seq    int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byMail: map[string]User{}}
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *MemoryStore) CreateUser(email, passwordHash, displayName string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := NormalizeEmail(email)
	if key == "" {
		return User{}, errors.New("email required")
	}
	if _, ok := s.byMail[key]; ok {
		return User{}, ErrDuplicateEmail
	}
	s.seq++
	u := User{
		ID:           randomUserID(s.seq),
		Email:        key,
		DisplayName:  strings.TrimSpace(displayName),
		PasswordHash: passwordHash,
		CreatedAt:    time.Now().UTC(),
	}
	if u.DisplayName == "" {
		u.DisplayName = localPart(key)
	}
	s.byMail[key] = u
	return u, nil
}

func (s *MemoryStore) FindByEmail(email string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byMail[NormalizeEmail(email)]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

func localPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}

func randomUserID(n int) string {
	return "usr_" + time.Now().UTC().Format("150405") + "_" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
