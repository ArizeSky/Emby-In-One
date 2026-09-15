package backend

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"strings"
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
