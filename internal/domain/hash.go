package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CanonicalHash names a value by the JSON document it becomes on the wire, and
// not by the Go value it happens to be right now.
//
// The two are different bytes. A struct encodes its fields in declaration order,
// and the same document decoded into an interface encodes them sorted, so a
// record hashed where it was built and hashed again where it was read disagreed
// about its own name as soon as any part of it travelled through an interface.
// The Booking Decision is exactly that record: a refusal states what was
// required and what was offered as whatever the rule had to hand, so a Run
// refused on its accelerators recorded a decision whose id could not be
// re-derived from the decision, and safety.decision_is_reproducible is the rule
// that says an id nobody can re-derive is not an id. Hashing the document rather
// than the value is what makes every derived identity here checkable by the only
// reader that can check it, which is one reading the record back.
func CanonicalHash(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(document); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSpace(buf.Bytes()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
