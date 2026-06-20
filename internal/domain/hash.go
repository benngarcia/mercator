package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func CanonicalHash(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	bytes := bytes.TrimSpace(buf.Bytes())
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
