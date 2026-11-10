// Package cipher is a fixture of the seal scan: a call named Seal of another
// shape, and a function named seal of the store's shape.
package cipher

import "crypto/cipher"

// encrypt calls an AEAD's Seal, which takes four arguments.
func encrypt(aead cipher.AEAD, nonce, plain []byte) []byte {
	return aead.Seal(nil, nonce, plain, nil)
}

// seal is a function of the store's shape, named in lower case.
func seal(purpose, rowID, value string) string { return purpose + rowID + value }

func put(row, value string) string {
	return seal("cipher.lower", row, value)
}
