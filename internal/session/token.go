package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"syscall"
)

const (
	MinTokenBytes = 16
	MaxTokenBytes = 512
)

var ErrToken = errors.New("token_rejected")

// TokenEqual compares two secrets by SHA-256, then a constant-time compare
// of the equal-length digests. Callers still reject empty and oversized
// inputs before treating a value as a token.
func TokenEqual(a, b string) bool {
	da := sha256.Sum256([]byte(a))
	db := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1
}

// GenerateToken reads 32 bytes from the OS CSPRNG and encodes them as
// base64url without padding.
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := base64.RawURLEncoding.EncodeToString(buf)
	if len(out) < MinTokenBytes {
		return "", ErrToken
	}
	return out, nil
}

// WriteTokenFile creates a new file mode 0600. It refuses an existing path,
// including a symlink, and writes the token with no trailing newline.
func WriteTokenFile(path, token string) error {
	if len(token) < MinTokenBytes || len(token) > MaxTokenBytes {
		return ErrToken
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return ErrToken
	}
	_, werr := io.WriteString(f, token)
	cerr := f.Chmod(0o600)
	closeErr := f.Close()
	if werr != nil || cerr != nil || closeErr != nil {
		_ = os.Remove(path)
		return ErrToken
	}
	return nil
}

// ReadTokenFile reads a mode-0600 regular file. Group or world permissions,
// a symlink, a short or oversized file, or a newline or NUL all fail closed.
func ReadTokenFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", ErrToken
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", ErrToken
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return "", ErrToken
	}
	if st.Size() < MinTokenBytes || st.Size() > MaxTokenBytes {
		return "", ErrToken
	}
	buf, err := io.ReadAll(io.LimitReader(f, MaxTokenBytes+1))
	if err != nil || len(buf) < MinTokenBytes || len(buf) > MaxTokenBytes {
		return "", ErrToken
	}
	for _, b := range buf {
		if b == '\n' || b == '\r' || b == 0 {
			return "", ErrToken
		}
	}
	return string(buf), nil
}
