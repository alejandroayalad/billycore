// Package id generates the identifiers BillyCore puts in its database and on
// its wire.
//
// It is not in internal/domain: reading randomness is I/O, and the domain
// performs none (D6). That is why domain constructors take an id rather than
// making one.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a random UUID v4.
//
// DATA_MODEL.md §2 stores ids as TEXT and does not depend on a UUID version;
// API.md §1 says clients must treat them as opaque. Random rather than
// time-ordered on purpose — an id that leaks when an artifact was ingested is a
// small fact about someone's financial life given away for free.
func New() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:]), nil
}
