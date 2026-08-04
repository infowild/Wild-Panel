// Package crypto provides cryptographic utilities for password hashing and verification.
package crypto

import (
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"
)

// HashPasswordAsBcrypt generates a bcrypt hash of the given password.
func HashPasswordAsBcrypt(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// CheckPasswordHash verifies if the given password matches the bcrypt hash.
func CheckPasswordHash(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// HashTokenSHA256 returns the lowercase hex SHA-256 of plaintext. Used for
// API Bearer tokens stored at rest (see model.ApiToken).
func HashTokenSHA256(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
