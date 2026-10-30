package main

import (
	"crypto/sha256"
	"fmt"
)

func skillInstanceID(dataDir string) string {
	return fmt.Sprintf("beeftv-%x", sha256.Sum256([]byte(dataDir)))
}
