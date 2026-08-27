// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

// Package uuid is the single UUID entry point of this repository. It builds on
// the Go 1.27 standard library uuid package; no third-party UUID library is used.
package uuid

import (
	"crypto/sha1"
	"uuid"
)

// NameSpaceURL is the URL namespace defined by RFC 9562 §6.6, used by NewV5.
var NameSpaceURL = uuid.MustParse("6ba7b811-9dad-11d1-80b4-00c04fd430c8")

// NewV7 returns a string UUIDv7 identifier.
func NewV7() string {
	return uuid.NewV7().String()
}

// NewV5 returns the name-based (SHA-1) UUID of name inside namespace, per RFC 9562 §5.5.
//
// The standard library only generates random UUIDs (v4/v7), but deterministic
// derivation is required wherever the same input must yield the same id
// (capsule build datasets), so the name-based variant lives here.
func NewV5(namespace uuid.UUID, name []byte) uuid.UUID {
	buf := make([]byte, 0, len(namespace)+len(name))
	buf = append(buf, namespace[:]...)
	buf = append(buf, name...)
	sum := sha1.Sum(buf)

	id := uuid.UUID(sum[:16])
	id[6] = id[6]&0x0f | 0x50 // version 5
	id[8] = id[8]&0x3f | 0x80 // RFC 9562 variant
	return id
}

// IsValid reports whether s is a valid UUID string.
func IsValid(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
