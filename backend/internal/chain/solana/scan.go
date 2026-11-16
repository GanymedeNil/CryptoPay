package solana

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"cryptopay/internal/address"
	"cryptopay/internal/chain"
)

// watchSet is a validated, de-duplicated ScanRequest.
type watchSet struct {
	from, to  uint64
	native    bool
	owners    map[string]struct{}
	ownerList []string // sorted
	mints     map[string]struct{}
	mintList  []string // sorted
}

func newWatchSet(req chain.ScanRequest) (*watchSet, error) {
	if req.FromBlock > req.ToBlock {
		return nil, fmt.Errorf("solana: invalid scan range [%d, %d]", req.FromBlock, req.ToBlock)
	}
	if len(req.Addresses) == 0 {
		return nil, errors.New("solana: scan request has no addresses")
	}
	w := &watchSet{
		from: req.FromBlock, to: req.ToBlock, native: req.Native,
		owners: make(map[string]struct{}, len(req.Addresses)),
		mints:  make(map[string]struct{}, len(req.Tokens)),
	}
	for _, a := range req.Addresses {
		canon, err := address.ParseSolana(a)
		if err != nil {
			return nil, fmt.Errorf("solana: watched address %q: %w", a, err)
		}
		if _, dup := w.owners[canon]; !dup {
			w.owners[canon] = struct{}{}
			w.ownerList = append(w.ownerList, canon)
		}
	}
	for _, m := range req.Tokens {
		canon, err := address.ParseSolana(m)
		if err != nil {
			return nil, fmt.Errorf("solana: token mint %q: %w", m, err)
		}
		if _, dup := w.mints[canon]; !dup {
			w.mints[canon] = struct{}{}
			w.mintList = append(w.mintList, canon)
		}
	}
	slices.Sort(w.ownerList)
	slices.Sort(w.mintList)
	return w, nil
}

func (w *watchSet) isOwner(a string) bool { _, ok := w.owners[a]; return ok }
func (w *watchSet) isMint(m string) bool  { _, ok := w.mints[m]; return ok }

// ScanTransfers returns every successful incoming SOL / SPL transfer to
// req.Addresses in the finalized slot range [req.FromBlock, req.ToBlock],
// ordered by (slot, signature, index). It is exhaustive or fails: any RPC
// error, a page budget overrun or a finalized transaction that cannot be
// fetched aborts the scan and returns no partial result.
//
// Steps: (1) token accounts of every watched owner for every requested mint
// (getTokenAccountsByOwner); (2) signatures of the owners and those token
// accounts within the range (getSignaturesForAddress, newest to oldest,
// failed ones dropped, de-duplicated); (3) getTransaction for each and
// balance-delta extraction. A token account created inside the paying
// transaction references the owner, so the owner's own signatures cover it.
func (c *Client) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	w, err := newWatchSet(req)
	if err != nil {
		return nil, err
	}
	if !w.native && len(w.mints) == 0 {
		return nil, nil
	}

	tokenOwner, err := c.tokenAccounts(ctx, w)
	if err != nil {
		return nil, err
	}
	accounts := slices.Clone(w.ownerList)
	for acc := range tokenOwner {
		if !w.isOwner(acc) {
			accounts = append(accounts, acc)
		}
	}
	slices.Sort(accounts)

	sigs, err := c.collectSignatures(ctx, accounts, w.from, w.to)
	if err != nil {
		return nil, err
	}

	perTx := make([][]chain.Transfer, len(sigs))
	err = runLimited(ctx, len(sigs), c.concurrency, func(ctx context.Context, i int) error {
		tx, err := c.getTransaction(ctx, sigs[i].Signature)
		if err != nil {
			return err
		}
		ts, err := extractTransfers(sigs[i], tx, w, tokenOwner)
		if err != nil {
			return fmt.Errorf("solana: transaction %s: %w", sigs[i].Signature, err)
		}
		perTx[i] = ts
		return nil
	})
	if err != nil {
		return nil, err
	}

	var out []chain.Transfer
	for _, ts := range perTx {
		out = append(out, ts...)
	}
	slices.SortFunc(out, func(a, b chain.Transfer) int {
		return cmp.Or(cmp.Compare(a.BlockNumber, b.BlockNumber), cmp.Compare(a.TxHash, b.TxHash), cmp.Compare(a.Index, b.Index))
	})
	return out, nil
}

type tokenAccountsResult struct {
	Value []struct {
		Pubkey  string `json:"pubkey"`
		Account struct {
			Data json.RawMessage `json:"data"`
		} `json:"account"`
	} `json:"value"`
}

type parsedTokenAccount struct {
	Parsed struct {
		Info struct {
			Mint  string `json:"mint"`
			Owner string `json:"owner"`
		} `json:"info"`
	} `json:"parsed"`
}

// tokenAccounts returns token account → owner for every (owner, mint) pair.
// It covers the associated token account and any auxiliary account, for both
// the Token and the Token-2022 program.
func (c *Client) tokenAccounts(ctx context.Context, w *watchSet) (map[string]string, error) {
	type pair struct{ owner, mint string }
	var pairs []pair
	for _, o := range w.ownerList {
		for _, m := range w.mintList {
			pairs = append(pairs, pair{o, m})
		}
	}
	found := make([][]string, len(pairs))
	err := runLimited(ctx, len(pairs), c.concurrency, func(ctx context.Context, i int) error {
		p := pairs[i]
		params := []any{
			p.owner,
			map[string]any{"mint": p.mint},
			map[string]any{"encoding": "jsonParsed", "commitment": commitmentFinalized, "minContextSlot": w.to},
		}
		var res tokenAccountsResult
		if err := c.rpc.call(ctx, "getTokenAccountsByOwner", params, &res); err != nil {
			return fmt.Errorf("solana: token accounts of %s for mint %s: %w", p.owner, p.mint, err)
		}
		for _, v := range res.Value {
			if _, err := address.ParseSolana(v.Pubkey); err != nil {
				return fmt.Errorf("solana: token accounts of %s: bad pubkey %q", p.owner, v.Pubkey)
			}
			var parsed parsedTokenAccount
			if json.Unmarshal(v.Account.Data, &parsed) == nil && parsed.Parsed.Info.Owner != "" {
				if info := parsed.Parsed.Info; info.Owner != p.owner || info.Mint != p.mint {
					return fmt.Errorf("solana: token account %s returned for owner %s / mint %s belongs to %s / %s",
						v.Pubkey, p.owner, p.mint, info.Owner, info.Mint)
				}
			}
			found[i] = append(found[i], v.Pubkey)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for i, accs := range found {
		for _, a := range accs {
			if prev, ok := out[a]; ok && prev != pairs[i].owner {
				return nil, fmt.Errorf("solana: token account %s reported for owners %s and %s", a, prev, pairs[i].owner)
			}
			out[a] = pairs[i].owner
		}
	}
	return out, nil
}

// signatureInfo is one getSignaturesForAddress entry.
type signatureInfo struct {
	Signature string          `json:"signature"`
	Slot      uint64          `json:"slot"`
	Err       json.RawMessage `json:"err"`
	BlockTime *int64          `json:"blockTime"`
}

// collectSignatures returns the de-duplicated successful signatures touching
// any of accounts within [from, to], sorted by (slot, signature).
func (c *Client) collectSignatures(ctx context.Context, accounts []string, from, to uint64) ([]signatureInfo, error) {
	lists := make([][]signatureInfo, len(accounts))
	err := runLimited(ctx, len(accounts), c.concurrency, func(ctx context.Context, i int) error {
		l, err := c.signaturesInRange(ctx, accounts[i], from, to)
		lists[i] = l
		return err
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var out []signatureInfo
	for _, l := range lists {
		for _, s := range l {
			if _, dup := seen[s.Signature]; dup {
				continue
			}
			seen[s.Signature] = struct{}{}
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b signatureInfo) int {
		return cmp.Or(cmp.Compare(a.Slot, b.Slot), cmp.Compare(a.Signature, b.Signature))
	})
	return out, nil
}

// signaturesInRange pages through the account's signatures (newest first)
// until it passes below from. Paging stops only on an empty page or a page
// reaching below from, never on a short page, so providers that cap the
// page size below 1000 cannot cause silent truncation.
func (c *Client) signaturesInRange(ctx context.Context, account string, from, to uint64) ([]signatureInfo, error) {
	var (
		out    []signatureInfo
		before string
		oldest uint64
	)
	for page := 0; ; page++ {
		if page == c.maxSigPages {
			err := fmt.Errorf("solana: account %s: %w (%d pages of %d, reached slot %d, range [%d, %d])",
				account, ErrTooManyPages, c.maxSigPages, signaturePageSize, oldest, from, to)
			if oldest <= to {
				// The budget ran out inside the range: a smaller range helps.
				err = fmt.Errorf("%w: %w", err, chain.ErrRangeTooLarge)
			}
			return nil, err
		}
		cfg := map[string]any{"limit": signaturePageSize, "commitment": commitmentFinalized, "minContextSlot": to}
		if before != "" {
			cfg["before"] = before
		}
		var res []signatureInfo
		if err := c.rpc.call(ctx, "getSignaturesForAddress", []any{account, cfg}, &res); err != nil {
			return nil, fmt.Errorf("solana: signatures of %s: %w", account, err)
		}
		if len(res) == 0 {
			return out, nil
		}
		oldest = res[0].Slot
		for _, s := range res {
			if s.Signature == "" {
				return nil, fmt.Errorf("solana: signatures of %s: entry without signature", account)
			}
			oldest = min(oldest, s.Slot)
			if s.Slot < from || s.Slot > to || !isNullJSON(s.Err) {
				continue
			}
			out = append(out, s)
		}
		if oldest < from {
			return out, nil
		}
		before = res[len(res)-1].Signature
	}
}

// getTransaction fetches a finalized transaction. A null result (node lagging
// or missing history) is retried on every endpoint before failing with
// ErrTransactionNotFound.
func (c *Client) getTransaction(ctx context.Context, sig string) (*txResult, error) {
	params := []any{sig, map[string]any{
		"encoding":                       "jsonParsed",
		"maxSupportedTransactionVersion": c.maxTxVersion,
		"commitment":                     commitmentFinalized,
	}}
	var tx txResult
	if err := c.rpc.callOpt(ctx, "getTransaction", params, &tx, ErrTransactionNotFound); err != nil {
		return nil, fmt.Errorf("solana: transaction %s: %w", sig, err)
	}
	return &tx, nil
}

// runLimited runs fn(ctx, i) for i in [0, n) with at most limit concurrent
// calls. The first error cancels the rest and is returned.
func runLimited(ctx context.Context, n, limit int, fn func(ctx context.Context, i int) error) error {
	if n == 0 {
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	sem := make(chan struct{}, max(limit, 1))
dispatch:
	for i := 0; i < n; i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
