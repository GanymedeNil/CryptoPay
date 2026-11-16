// Package hdwallet derives watch-only receiving addresses from an
// account-level extended public key at path xpub/0/index. No private key is
// ever accepted or needed.
package hdwallet

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
)

// ErrPrivateKey is returned when an extended private key is supplied.
var ErrPrivateKey = errors.New("hdwallet: this is an extended PRIVATE key; never paste private keys here, provide the account-level extended PUBLIC key (xpub/zpub/tpub/vpub...) only")

// parsePublic decodes any SLIP-132 flavoured extended key and insists it is
// public. Only the key material matters, not the version bytes.
func parsePublic(xpub string) (*hdkeychain.ExtendedKey, error) {
	k, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return nil, fmt.Errorf("hdwallet: invalid extended public key: %w", err)
	}
	if k.IsPrivate() {
		k.Zero()
		return nil, ErrPrivateKey
	}
	return k, nil
}

// Validate checks that xpub is a usable extended public key for the family.
func Validate(family domain.Family, xpub string) error {
	switch family {
	case domain.FamilyEVM, domain.FamilyTron, domain.FamilyUTXO:
	case domain.FamilySolana:
		return errors.New("hdwallet: solana does not support xpub wallets; use a static address")
	default:
		return fmt.Errorf("hdwallet: unsupported family %q", family)
	}
	k, err := parsePublic(xpub)
	if err != nil {
		return err
	}
	// Prove derivation works (also rejects invalid points).
	if _, err := deriveKey(k, 0); err != nil {
		return err
	}
	return nil
}

func deriveKey(k *hdkeychain.ExtendedKey, index uint32) (*hdkeychain.ExtendedKey, error) {
	if index >= hdkeychain.HardenedKeyStart {
		return nil, errors.New("hdwallet: hardened index cannot be derived from a public key")
	}
	external, err := k.Derive(0)
	if err != nil {
		return nil, fmt.Errorf("hdwallet: derive external chain: %w", err)
	}
	child, err := external.Derive(index)
	if err != nil {
		return nil, fmt.Errorf("hdwallet: derive index %d: %w", index, err)
	}
	return child, nil
}

// Derive returns the canonical receiving address for xpub/0/index on chain c.
func Derive(c domain.Chain, xpub string, index uint32) (string, error) {
	if c.Family == domain.FamilySolana {
		return "", errors.New("hdwallet: solana has no public key derivation; use a static wallet")
	}
	k, err := parsePublic(xpub)
	if err != nil {
		return "", err
	}
	child, err := deriveKey(k, index)
	if err != nil {
		return "", err
	}
	pub, err := child.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("hdwallet: %w", err)
	}
	switch c.Family {
	case domain.FamilyEVM, domain.FamilyTron:
		h := address.Keccak256(pub.SerializeUncompressed()[1:])[12:]
		if c.Family == domain.FamilyEVM {
			return address.EVMFromBytes(h)
		}
		return address.TronFromBytes(h)
	case domain.FamilyUTXO:
		if c.Params.Bech32HRP == "" {
			return "", errors.New("hdwallet: chain has no bech32 hrp")
		}
		return address.SegwitEncode(c.Params.Bech32HRP, 0, btcutil.Hash160(pub.SerializeCompressed()))
	}
	return "", fmt.Errorf("hdwallet: unsupported family %q", c.Family)
}
