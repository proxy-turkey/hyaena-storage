package config

import (
	"crypto/sha256"

	"golang.org/x/crypto/pbkdf2"
)

// pbkdf2Key, paroladan PBKDF2-HMAC-SHA256 anahtar türetir (Python ile birebir).
func pbkdf2Key(password string) []byte {
	const (
		salt   = "tgshare-admin"
		iter   = 100_000
		keyLen = 32
	)
	return pbkdf2.Key([]byte(password), []byte(salt), iter, keyLen, sha256.New)
}
