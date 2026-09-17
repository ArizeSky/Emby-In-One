package backend

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
)

var strictHashedPasswordPattern = regexp.MustCompile(`(?i)^[0-9a-f]{32}:[0-9a-f]{128}$`)

func IsHashedPassword(stored string) bool {
	return strictHashedPasswordPattern.MatchString(strings.TrimSpace(stored))
}

func HashPassword(plain string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := scryptKey([]byte(plain), salt, 16384, 8, 1, 64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(derived), nil
}

// dummyPasswordHash is a valid stored-hash value built with the production parameters.
// Verifying against it costs the same as verifying a real password, which keeps the timing
// of "no such user" indistinguishable from "wrong password".
//
// It is built on first use rather than at start-up: building it costs one scrypt, and this
// package is linked into the CLI too, where every --version or --reset-password invocation
// would otherwise pay for a hash the process never uses. The server warms it in
// NewAuthManager so the very first unknown-username attempt is not measurably slower than
// the ones after it.
var (
	dummyHashOnce sync.Once
	dummyHash     string
)

func dummyPasswordHash() string {
	dummyHashOnce.Do(func() {
		salt := make([]byte, 16)
		derived, err := scryptKey([]byte("emby-in-one-timing-equalizer"), salt, 16384, 8, 1, 64)
		if err != nil {
			return
		}
		dummyHash = hex.EncodeToString(salt) + ":" + hex.EncodeToString(derived)
	})
	return dummyHash
}

// spendVerifyTime runs a password verification that always fails, for the branches that
// have no hash of their own to check. Its only purpose is the timing it consumes.
func spendVerifyTime(password string) {
	VerifyPassword(password, dummyPasswordHash())
}

func VerifyPassword(plain, stored string) bool {
	if plain == "" || stored == "" {
		return false
	}
	if !IsHashedPassword(stored) {
		return false
	}
	parts := strings.SplitN(stored, ":", 2)
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	derived, err := scryptKey([]byte(plain), salt, 16384, 8, 1, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(expected, derived) == 1
}
