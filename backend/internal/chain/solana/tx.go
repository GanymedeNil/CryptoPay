package solana

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"cryptopay/internal/chain"
)

// txResult is the subset of a jsonParsed getTransaction result we use.
type txResult struct {
	Slot        uint64  `json:"slot"`
	BlockTime   *int64  `json:"blockTime"`
	Meta        *txMeta `json:"meta"`
	Transaction struct {
		Signatures []string `json:"signatures"`
		Message    struct {
			AccountKeys []accountKey `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
}

type txMeta struct {
	Err               json.RawMessage `json:"err"`
	PreBalances       []json.Number   `json:"preBalances"`
	PostBalances      []json.Number   `json:"postBalances"`
	PreTokenBalances  []tokenBalance  `json:"preTokenBalances"`
	PostTokenBalances []tokenBalance  `json:"postTokenBalances"`
	// LoadedAddresses is only present for non-parsed encodings; with
	// jsonParsed the lookup-table keys are already part of accountKeys
	// (source "lookupTable"). Handled anyway for robustness.
	LoadedAddresses *struct {
		Writable []string `json:"writable"`
		Readonly []string `json:"readonly"`
	} `json:"loadedAddresses"`
}

type tokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	ProgramID     string `json:"programId"`
	UITokenAmount struct {
		// Amount is the raw integer amount in base units. uiAmount (a float)
		// is deliberately never read.
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

// accountKey is a jsonParsed account key object; a bare string (non-parsed
// encodings) is accepted too.
type accountKey struct {
	Pubkey string `json:"pubkey"`
	Signer bool   `json:"signer"`
	Source string `json:"source"`
}

func (k *accountKey) UnmarshalJSON(b []byte) error {
	if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '"' {
		*k = accountKey{}
		return json.Unmarshal(b, &k.Pubkey)
	}
	type plain accountKey
	return json.Unmarshal(b, (*plain)(k))
}

// accountKeys returns the full account list the balance arrays refer to:
// static keys, then lookup-table writable, then lookup-table readonly keys.
func (tx *txResult) accountKeys() []accountKey {
	keys := tx.Transaction.Message.AccountKeys
	la := tx.Meta.LoadedAddresses
	if la == nil || len(keys) >= len(tx.Meta.PreBalances) {
		return keys
	}
	out := make([]accountKey, 0, len(keys)+len(la.Writable)+len(la.Readonly))
	out = append(out, keys...)
	for _, a := range la.Writable {
		out = append(out, accountKey{Pubkey: a, Source: "lookupTable"})
	}
	for _, a := range la.Readonly {
		out = append(out, accountKey{Pubkey: a, Source: "lookupTable"})
	}
	return out
}

// extractTransfers computes the incoming transfers of tx for the watch set
// from balance deltas. Failed transactions yield nothing; malformed data is
// an error (never a silent skip).
func extractTransfers(sig signatureInfo, tx *txResult, w *watchSet, tokenOwner map[string]string) ([]chain.Transfer, error) {
	if len(tx.Transaction.Signatures) == 0 || tx.Transaction.Signatures[0] != sig.Signature {
		return nil, errors.New("rpc returned a different transaction")
	}
	if tx.Slot != sig.Slot {
		return nil, fmt.Errorf("transaction slot %d differs from signature list slot %d", tx.Slot, sig.Slot)
	}
	if tx.Meta == nil {
		return nil, errors.New("transaction status meta missing")
	}
	meta := tx.Meta
	if !isNullJSON(meta.Err) {
		return nil, nil
	}
	keys := tx.accountKeys()
	n := len(keys)
	if n == 0 {
		return nil, errors.New("transaction has no account keys")
	}
	if len(meta.PreBalances) != n || len(meta.PostBalances) != n {
		return nil, fmt.Errorf("%d account keys but %d pre / %d post balances", n, len(meta.PreBalances), len(meta.PostBalances))
	}
	for i, k := range keys {
		if k.Pubkey == "" {
			return nil, fmt.Errorf("account key %d is empty", i)
		}
	}

	base := chain.Transfer{TxHash: sig.Signature, BlockNumber: tx.Slot}
	switch {
	case tx.BlockTime != nil:
		base.BlockTime = time.Unix(*tx.BlockTime, 0).UTC()
	case sig.BlockTime != nil:
		base.BlockTime = time.Unix(*sig.BlockTime, 0).UTC()
	}
	feePayer := keys[0].Pubkey

	var out []chain.Transfer
	if w.native {
		for i, k := range keys {
			if !w.isOwner(k.Pubkey) {
				continue
			}
			pre, err := parseUint(string(meta.PreBalances[i]))
			if err != nil {
				return nil, fmt.Errorf("pre balance %d: %w", i, err)
			}
			post, err := parseUint(string(meta.PostBalances[i]))
			if err != nil {
				return nil, fmt.Errorf("post balance %d: %w", i, err)
			}
			if delta := post.Sub(post, pre); delta.Sign() > 0 {
				t := base
				t.Index, t.To, t.Amount = i, k.Pubkey, delta
				t.From = nativeSender(keys, meta, i, feePayer)
				out = append(out, t)
			}
		}
	}

	if len(w.mints) == 0 {
		return out, nil
	}
	preTok := make(map[int]tokenBalance, len(meta.PreTokenBalances))
	for _, b := range meta.PreTokenBalances {
		if b.AccountIndex < 0 || b.AccountIndex >= n {
			return nil, fmt.Errorf("pre token balance account index %d out of range", b.AccountIndex)
		}
		if _, dup := preTok[b.AccountIndex]; dup {
			return nil, fmt.Errorf("duplicate pre token balance for account index %d", b.AccountIndex)
		}
		preTok[b.AccountIndex] = b
	}
	seenPost := make(map[int]struct{}, len(meta.PostTokenBalances))
	for _, b := range meta.PostTokenBalances {
		idx := b.AccountIndex
		if idx < 0 || idx >= n {
			return nil, fmt.Errorf("post token balance account index %d out of range", idx)
		}
		if _, dup := seenPost[idx]; dup {
			return nil, fmt.Errorf("duplicate post token balance for account index %d", idx)
		}
		seenPost[idx] = struct{}{}
		if !w.isMint(b.Mint) {
			continue
		}
		owner := b.Owner
		if owner == "" {
			// Very old transactions carry no owner; fall back to the
			// token accounts we looked up for the watched owners.
			owner = tokenOwner[keys[idx].Pubkey]
		}
		if !w.isOwner(owner) {
			continue
		}
		post, err := parseUint(b.UITokenAmount.Amount)
		if err != nil {
			return nil, fmt.Errorf("post token amount of account %d: %w", idx, err)
		}
		pre := new(big.Int)
		if p, ok := preTok[idx]; ok && p.Mint == b.Mint {
			if pre, err = parseUint(p.UITokenAmount.Amount); err != nil {
				return nil, fmt.Errorf("pre token amount of account %d: %w", idx, err)
			}
		}
		if delta := post.Sub(post, pre); delta.Sign() > 0 {
			t := base
			t.Index, t.To, t.Token, t.Amount = idx, owner, b.Mint, delta
			if keys[idx].Pubkey == owner {
				t.Index += SelfOwnedIndexOffset
			}
			t.From = tokenSender(meta, b.Mint, owner, feePayer)
			out = append(out, t)
		}
	}
	return out, nil
}

// nativeSender returns the signer (other than the receiver) whose lamport
// balance decreased the most, or the fee payer.
func nativeSender(keys []accountKey, meta *txMeta, recv int, feePayer string) string {
	best, bestDelta := -1, new(big.Int)
	for i, k := range keys {
		if i == recv || !k.Signer {
			continue
		}
		pre, err1 := parseUint(string(meta.PreBalances[i]))
		post, err2 := parseUint(string(meta.PostBalances[i]))
		if err1 != nil || err2 != nil {
			continue
		}
		if d := post.Sub(post, pre); d.Sign() < 0 && (best < 0 || d.Cmp(bestDelta) < 0) {
			best, bestDelta = i, d
		}
	}
	if best >= 0 {
		return keys[best].Pubkey
	}
	return feePayer
}

// tokenSender returns the owner (other than the receiver) of the mint's token
// balance that decreased the most (including accounts closed by the
// transaction), or the fee payer.
func tokenSender(meta *txMeta, mint, recvOwner, feePayer string) string {
	type acc struct {
		owner string
		delta *big.Int
	}
	accs := make(map[int]*acc)
	for _, b := range meta.PreTokenBalances {
		if v, err := parseUint(b.UITokenAmount.Amount); err == nil && b.Mint == mint {
			accs[b.AccountIndex] = &acc{owner: b.Owner, delta: v.Neg(v)}
		}
	}
	for _, b := range meta.PostTokenBalances {
		v, err := parseUint(b.UITokenAmount.Amount)
		if err != nil || b.Mint != mint {
			continue
		}
		if a, ok := accs[b.AccountIndex]; ok {
			a.delta.Add(a.delta, v)
			if b.Owner != "" {
				a.owner = b.Owner
			}
		} else {
			accs[b.AccountIndex] = &acc{owner: b.Owner, delta: v}
		}
	}
	bestIdx, best := -1, (*acc)(nil)
	for idx, a := range accs {
		if a.owner == "" || a.owner == recvOwner || a.delta.Sign() >= 0 {
			continue
		}
		if best == nil {
			bestIdx, best = idx, a
			continue
		}
		if c := a.delta.Cmp(best.delta); c < 0 || (c == 0 && idx < bestIdx) {
			bestIdx, best = idx, a
		}
	}
	if best != nil {
		return best.owner
	}
	return feePayer
}

// parseUint parses a non-negative base-10 integer of any size.
func parseUint(s string) (*big.Int, error) {
	if s == "" || s[0] == '-' || s[0] == '+' {
		return nil, fmt.Errorf("invalid unsigned integer %q", s)
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid unsigned integer %q", s)
	}
	return v, nil
}
