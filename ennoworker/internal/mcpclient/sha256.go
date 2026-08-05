package mcpclient

import (
	"crypto/sha256"
)

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
