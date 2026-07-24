package lab

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// Entropy derives independent samples from a seed and semantic key. It has no
// mutable PRNG cursor, so an unrelated sample cannot shift another outcome.
type Entropy struct {
	seed []byte
}

func NewEntropy(seed string) (Entropy, error) {
	if seed == "" {
		return Entropy{}, fmt.Errorf("Lab entropy seed is required")
	}
	return Entropy{seed: []byte(seed)}, nil
}

func (entropy Entropy) Uint64(key string) (uint64, error) {
	if key == "" {
		return 0, fmt.Errorf("Lab entropy semantic key is required")
	}
	mac := hmac.New(sha256.New, entropy.seed)
	_, _ = mac.Write([]byte(key))
	return binary.BigEndian.Uint64(mac.Sum(nil)[:8]), nil
}

func DeterministicID(seed, kind, semanticKey string) string {
	sum := sha256.Sum256([]byte(seed + "\x00" + kind + "\x00" + semanticKey))
	prefix := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(kind))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}
