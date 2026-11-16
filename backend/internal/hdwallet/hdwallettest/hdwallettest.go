// Package hdwallettest holds test-only helpers that turn a mnemonic into an
// account-level extended public key. Never use it in production code: it
// handles seed material.
package hdwallettest

import (
	"fmt"
	"strconv"
	"strings"

	"crypto/sha512"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"golang.org/x/crypto/pbkdf2"
)

// Seed returns the BIP-39 seed for a mnemonic with an empty passphrase.
func Seed(mnemonic string) []byte {
	return pbkdf2.Key([]byte(mnemonic), []byte("mnemonic"), 2048, 64, sha512.New)
}

// AccountXpub derives the extended PUBLIC key at path (e.g. "m/44'/60'/0'")
// from the mnemonic and returns it in plain xpub serialization. It panics on
// malformed input (test helper).
func AccountXpub(mnemonic, path string) string {
	k, err := hdkeychain.NewMaster(Seed(mnemonic), &chaincfg.MainNetParams)
	if err != nil {
		panic(err)
	}
	parts := strings.Split(path, "/")
	if parts[0] != "m" {
		panic("hdwallettest: path must start with m")
	}
	for _, p := range parts[1:] {
		var idx uint32
		hardened := strings.HasSuffix(p, "'") || strings.HasSuffix(p, "h")
		n, err := strconv.ParseUint(strings.TrimRight(p, "'h"), 10, 31)
		if err != nil {
			panic(fmt.Sprintf("hdwallettest: bad path element %q", p))
		}
		idx = uint32(n)
		if hardened {
			idx += hdkeychain.HardenedKeyStart
		}
		if k, err = k.Derive(idx); err != nil {
			panic(err)
		}
	}
	pub, err := k.Neuter()
	if err != nil {
		panic(err)
	}
	return pub.String()
}
