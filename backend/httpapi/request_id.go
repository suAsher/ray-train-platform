package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

func RequestID(header string) string {
	value := strings.TrimSpace(header)
	if value != "" && len(value) <= 128 {
		return value
	}

	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err == nil {
		return hex.EncodeToString(bytes)
	}
	return "request-id-unavailable"
}
