package main

import (
	"crypto/sha256"
	"encoding/hex"
)

func rawSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
