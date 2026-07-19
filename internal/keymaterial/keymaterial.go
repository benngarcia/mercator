// Package keymaterial decodes cryptographic keys supplied through configuration.
package keymaterial

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Decode accepts a present hex- or base64-encoded key and enforces its minimum
// decoded length. Callers decide whether an absent value is allowed.
func Decode(name, raw string, minimumBytes int) ([]byte, error) {
	var key []byte
	if decoded, err := hex.DecodeString(raw); err == nil {
		key = decoded
	} else if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		key = decoded
	} else {
		return nil, fmt.Errorf("%s must be hex or base64", name)
	}
	if len(key) < minimumBytes {
		return nil, fmt.Errorf("%s must decode to at least %d bytes", name, minimumBytes)
	}
	return key, nil
}
