// Package address validates addresses and converts them to the canonical
// string form used everywhere in the gateway (database, chain adapters, API).
//
// Canonical forms:
//   - EVM:    EIP-55 mixed-case checksum, "0x" prefixed
//   - Tron:   base58check starting with "T"
//   - UTXO:   bech32 lower-case, or base58check as given
//   - Solana: base58 of a 32-byte public key
package address

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"golang.org/x/crypto/sha3"

	"cryptopay/internal/domain"
)

var ErrInvalid = errors.New("invalid address")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Keccak256 returns the legacy Keccak-256 hash used by Ethereum and Tron.
func Keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

// ---- EVM ----

// EVMFromBytes encodes a 20-byte address as an EIP-55 checksum string.
func EVMFromBytes(b []byte) (string, error) {
	if len(b) != 20 {
		return "", invalid("evm address must be 20 bytes, got %d", len(b))
	}
	lower := hex.EncodeToString(b)
	hash := hex.EncodeToString(Keccak256([]byte(lower)))
	out := []byte(lower)
	for i, c := range out {
		if c >= 'a' && c <= 'f' && hash[i] >= '8' {
			out[i] = c - 32
		}
	}
	return "0x" + string(out), nil
}

// EVMBytes decodes a hex EVM address (any case, 0x prefix required).
func EVMBytes(s string) ([]byte, error) {
	if len(s) != 42 || !(strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")) {
		return nil, invalid("evm address must be 0x + 40 hex chars")
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return nil, invalid("evm address is not hex")
	}
	return b, nil
}

// ParseEVM validates s and returns its EIP-55 form. A mixed-case input must
// carry a correct checksum; all-lower or all-upper inputs are accepted.
func ParseEVM(s string) (string, error) {
	b, err := EVMBytes(s)
	if err != nil {
		return "", err
	}
	canon, _ := EVMFromBytes(b)
	body := s[2:]
	if body != strings.ToLower(body) && body != strings.ToUpper(body) && canon[2:] != body {
		return "", invalid("evm address checksum mismatch")
	}
	return canon, nil
}

// ---- Tron ----

const tronPrefix = 0x41

func checksum4(b []byte) []byte {
	h1 := sha256.Sum256(b)
	h2 := sha256.Sum256(h1[:])
	return h2[:4]
}

func base58CheckEncode(payload []byte) string {
	return base58.Encode(append(append([]byte{}, payload...), checksum4(payload)...))
}

func base58CheckDecode(s string) ([]byte, error) {
	raw := base58.Decode(s)
	if len(raw) < 5 {
		return nil, invalid("base58 payload too short")
	}
	payload, sum := raw[:len(raw)-4], raw[len(raw)-4:]
	if !bytes.Equal(checksum4(payload), sum) {
		return nil, invalid("base58 checksum mismatch")
	}
	return payload, nil
}

// TronFromBytes encodes a 20-byte account hash as a base58 Tron address.
func TronFromBytes(b []byte) (string, error) {
	if len(b) != 20 {
		return "", invalid("tron address must be 20 bytes, got %d", len(b))
	}
	return base58CheckEncode(append([]byte{tronPrefix}, b...)), nil
}

// TronBytes returns the 20-byte account hash of a Tron address given either
// as base58 ("T...") or as hex ("41..." with optional 0x, or a bare 20-byte hex).
func TronBytes(s string) ([]byte, error) {
	if strings.HasPrefix(s, "T") && len(s) == 34 {
		p, err := base58CheckDecode(s)
		if err != nil {
			return nil, err
		}
		if len(p) != 21 || p[0] != tronPrefix {
			return nil, invalid("not a tron mainnet-format address")
		}
		return p[1:], nil
	}
	h := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, invalid("tron address is neither base58 nor hex")
	}
	switch {
	case len(b) == 21 && b[0] == tronPrefix:
		return b[1:], nil
	case len(b) == 20:
		return b, nil
	}
	return nil, invalid("tron hex address has wrong length")
}

// ParseTron validates s and returns the base58 form.
func ParseTron(s string) (string, error) {
	b, err := TronBytes(s)
	if err != nil {
		return "", err
	}
	return TronFromBytes(b)
}

// TronHex returns the "41"-prefixed lower-case hex form used by Tron node APIs.
func TronHex(s string) (string, error) {
	b, err := TronBytes(s)
	if err != nil {
		return "", err
	}
	return "41" + hex.EncodeToString(b), nil
}

// ---- Solana ----

// ParseSolana validates a base58 32-byte public key.
func ParseSolana(s string) (string, error) {
	if len(s) < 32 || len(s) > 44 {
		return "", invalid("solana address has wrong length")
	}
	b := base58.Decode(s)
	if len(b) != 32 || base58.Encode(b) != s {
		return "", invalid("solana address must be base58 of 32 bytes")
	}
	return s, nil
}

// ---- UTXO (Bitcoin / Litecoin) ----

// SegwitEncode encodes a witness program (v0: bech32, v1+: bech32m).
func SegwitEncode(hrp string, version byte, program []byte) (string, error) {
	conv, err := bech32.ConvertBits(program, 8, 5, true)
	if err != nil {
		return "", err
	}
	data := append([]byte{version}, conv...)
	if version == 0 {
		return bech32.Encode(hrp, data)
	}
	return bech32.EncodeM(hrp, data)
}

// ParseUTXO validates a Bitcoin-style address for the chain described by p
// and returns the canonical form.
func ParseUTXO(s string, p domain.ChainParam) (string, error) {
	if p.Bech32HRP == "" {
		return "", invalid("chain has no utxo parameters")
	}
	if strings.HasPrefix(strings.ToLower(s), p.Bech32HRP+"1") {
		if s != strings.ToLower(s) && s != strings.ToUpper(s) {
			return "", invalid("bech32 address has mixed case")
		}
		hrp, data, ver, err := bech32.DecodeGeneric(s)
		if err != nil {
			return "", invalid("bech32: %v", err)
		}
		if hrp != p.Bech32HRP || len(data) < 1 {
			return "", invalid("bech32 prefix does not match chain")
		}
		witVer := data[0]
		prog, err := bech32.ConvertBits(data[1:], 5, 8, false)
		if err != nil {
			return "", invalid("bech32 program: %v", err)
		}
		switch {
		case witVer == 0 && ver == bech32.Version0 && (len(prog) == 20 || len(prog) == 32):
		case witVer >= 1 && witVer <= 16 && ver == bech32.VersionM && len(prog) >= 2 && len(prog) <= 40:
		default:
			return "", invalid("unsupported witness program")
		}
		return strings.ToLower(s), nil
	}
	payload, err := base58CheckDecode(s)
	if err != nil {
		return "", err
	}
	if len(payload) != 21 || (payload[0] != p.P2PKHVersion && payload[0] != p.P2SHVersion) {
		return "", invalid("base58 address version does not match chain")
	}
	return s, nil
}

// ---- Generic ----

// Normalize validates s for chain c and returns its canonical form.
func Normalize(c domain.Chain, s string) (string, error) {
	s = strings.TrimSpace(s)
	switch c.Family {
	case domain.FamilyEVM:
		return ParseEVM(s)
	case domain.FamilyTron:
		return ParseTron(s)
	case domain.FamilySolana:
		return ParseSolana(s)
	case domain.FamilyUTXO:
		return ParseUTXO(s, c.Params)
	}
	return "", invalid("unknown chain family %q", c.Family)
}
