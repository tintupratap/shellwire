package main

import (
	"crypto/rand"
	"encoding/hex"
)

func generateToken() string {
	b := make([]byte, 12)
	rand.Read(b) //nolint
	return hex.EncodeToString(b)
}
