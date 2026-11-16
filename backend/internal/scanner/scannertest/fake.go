// Package scannertest provides a scriptable in-memory chain.Client and
// client provider for tests of the scanner and of anything built on it
// (end-to-end payment flows without a blockchain).
package scannertest

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"cryptopay/internal/chain"
)

// FakeChain is an in-memory chain implementing chain.Client. Tests set the
// head, add transfers at block heights, simulate reorgs (Remove / Move) and
// inject errors. It is safe for concurrent use.
//
// VerifyTransfer answers from the chain's content by default: true iff a
// transfer with the same (TxHash, Index) currently exists at the same block
// number and block hash.
type FakeChain struct {
	mu        sync.Mutex
	head      uint64
	transfers []chain.Transfer

	headErrs   []error // consumed one per Head call
	scanErrs   []error // consumed one per ScanTransfers call
	verifyErrs []error // consumed one per VerifyTransfer call
	headErr    error   // sticky
	scanErr    error   // sticky
	verifyErr  error   // sticky
	maxRange   uint64  // > 0: ranges longer than this fail with chain.ErrRangeTooLarge
	panicHead  any     // != nil: Head panics with it

	headCalls   int
	scanCalls   []chain.ScanRequest
	verifyCalls []chain.Transfer
}

// NewFakeChain returns an empty chain whose head is at the given height.
func NewFakeChain(head uint64) *FakeChain { return &FakeChain{head: head} }

// SetHead sets the chain head.
func (f *FakeChain) SetHead(h uint64) { f.mu.Lock(); defer f.mu.Unlock(); f.head = h }

// Advance moves the head forward by n blocks and returns the new head.
func (f *FakeChain) Advance(n uint64) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head += n
	return f.head
}

// BlockHash is the hash the fake uses for a block of the canonical chain.
func BlockHash(block uint64) string { return fmt.Sprintf("0xblock%d", block) }

// Add puts a transfer on the chain. Empty BlockHash / zero BlockTime get
// defaults (BlockHash(t.BlockNumber), a fixed time); nil Amount becomes 0.
// It returns the transfer as stored.
func (f *FakeChain) Add(t chain.Transfer) chain.Transfer {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.BlockHash == "" {
		t.BlockHash = BlockHash(t.BlockNumber)
	}
	if t.BlockTime.IsZero() {
		t.BlockTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(t.BlockNumber) * time.Second)
	}
	if t.Amount == nil {
		t.Amount = new(big.Int)
	}
	f.transfers = append(f.transfers, t)
	return t
}

// Pay is a shorthand for Add: amount base units of token ("" = native) to
// address `to` in tx txHash at the given block.
func (f *FakeChain) Pay(block uint64, txHash, to, token string, amount *big.Int) chain.Transfer {
	return f.Add(chain.Transfer{TxHash: txHash, BlockNumber: block, From: "fake-sender", To: to, Token: token, Amount: amount})
}

// Remove drops every transfer of txHash (a reorg dropped the transaction)
// and reports whether any existed.
func (f *FakeChain) Remove(txHash string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.transfers[:0]
	removed := false
	for _, t := range f.transfers {
		if t.TxHash == txHash {
			removed = true
			continue
		}
		kept = append(kept, t)
	}
	f.transfers = kept
	return removed
}

// Move re-includes the transfers of txHash in another block (a reorg), with
// blockHash (empty = "0xreorg<block>").
func (f *FakeChain) Move(txHash string, block uint64, blockHash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if blockHash == "" {
		blockHash = fmt.Sprintf("0xreorg%d", block)
	}
	for i := range f.transfers {
		if f.transfers[i].TxHash == txHash {
			f.transfers[i].BlockNumber, f.transfers[i].BlockHash = block, blockHash
		}
	}
}

// FailHead makes the next len(errs) Head calls fail with errs in order.
func (f *FakeChain) FailHead(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headErrs = append(f.headErrs, errs...)
}

// FailScan makes the next len(errs) ScanTransfers calls fail with errs in order.
func (f *FakeChain) FailScan(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanErrs = append(f.scanErrs, errs...)
}

// FailVerify makes the next len(errs) VerifyTransfer calls fail with errs in order.
func (f *FakeChain) FailVerify(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyErrs = append(f.verifyErrs, errs...)
}

// SetHeadError makes every Head call fail with err until reset with nil.
func (f *FakeChain) SetHeadError(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.headErr = err }

// SetScanError makes every ScanTransfers call fail with err until reset with nil.
func (f *FakeChain) SetScanError(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.scanErr = err }

// SetVerifyError makes every VerifyTransfer call fail with err until reset with nil.
func (f *FakeChain) SetVerifyError(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.verifyErr = err }

// SetMaxRange makes ScanTransfers reject ranges of more than n blocks with
// chain.ErrRangeTooLarge (0 = no limit).
func (f *FakeChain) SetMaxRange(n uint64) { f.mu.Lock(); defer f.mu.Unlock(); f.maxRange = n }

// PanicOnHead makes Head panic with v (nil restores normal behaviour).
func (f *FakeChain) PanicOnHead(v any) { f.mu.Lock(); defer f.mu.Unlock(); f.panicHead = v }

// HeadCalls returns how often Head was called.
func (f *FakeChain) HeadCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.headCalls }

// ScanCalls returns every ScanTransfers request so far (including failed ones).
func (f *FakeChain) ScanCalls() []chain.ScanRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chain.ScanRequest(nil), f.scanCalls...)
}

// VerifyCalls returns every transfer VerifyTransfer was asked about.
func (f *FakeChain) VerifyCalls() []chain.Transfer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chain.Transfer(nil), f.verifyCalls...)
}

// ResetCalls forgets the recorded calls.
func (f *FakeChain) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls, f.scanCalls, f.verifyCalls = 0, nil, nil
}

func pop(q *[]error) error {
	if len(*q) == 0 {
		return nil
	}
	err := (*q)[0]
	*q = (*q)[1:]
	return err
}

// Head implements chain.Client.
func (f *FakeChain) Head(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	if f.panicHead != nil {
		panic(f.panicHead)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := pop(&f.headErrs); err != nil {
		return 0, err
	}
	if f.headErr != nil {
		return 0, f.headErr
	}
	return f.head, nil
}

// ScanTransfers implements chain.Client: the transfers in the block range
// whose recipient is requested and whose token is requested (native coin:
// req.Native).
func (f *FakeChain) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanCalls = append(f.scanCalls, req)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := pop(&f.scanErrs); err != nil {
		return nil, err
	}
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	if req.ToBlock < req.FromBlock {
		return nil, fmt.Errorf("fake chain: invalid range %d-%d", req.FromBlock, req.ToBlock)
	}
	if f.maxRange > 0 && req.ToBlock-req.FromBlock+1 > f.maxRange {
		return nil, fmt.Errorf("fake chain: %d blocks requested: %w", req.ToBlock-req.FromBlock+1, chain.ErrRangeTooLarge)
	}
	addrs := map[string]bool{}
	for _, a := range req.Addresses {
		addrs[a] = true
	}
	tokens := map[string]bool{}
	for _, t := range req.Tokens {
		tokens[t] = true
	}
	var out []chain.Transfer
	for _, t := range f.transfers {
		if t.BlockNumber < req.FromBlock || t.BlockNumber > req.ToBlock || !addrs[t.To] {
			continue
		}
		if (t.Token == "" && !req.Native) || (t.Token != "" && !tokens[t.Token]) {
			continue
		}
		c := t
		c.Amount = new(big.Int).Set(t.Amount)
		out = append(out, c)
	}
	return out, nil
}

// VerifyTransfer implements chain.Client.
func (f *FakeChain) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyCalls = append(f.verifyCalls, t)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := pop(&f.verifyErrs); err != nil {
		return false, err
	}
	if f.verifyErr != nil {
		return false, f.verifyErr
	}
	for _, x := range f.transfers {
		if x.TxHash == t.TxHash && x.Index == t.Index {
			return x.BlockNumber == t.BlockNumber && x.BlockHash == t.BlockHash, nil
		}
	}
	return false, nil
}

// Provider is a scanner.ClientProvider backed by a fixed set of clients.
type Provider struct {
	mu          sync.Mutex
	clients     map[string]chain.Client
	invalidated []string
}

// NewProvider returns a Provider serving clients (chain id → client).
func NewProvider(clients map[string]chain.Client) *Provider {
	p := &Provider{clients: map[string]chain.Client{}}
	for id, c := range clients {
		p.clients[id] = c
	}
	return p
}

// Set registers (or replaces) the client of a chain.
func (p *Provider) Set(chainID string, c chain.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients[chainID] = c
}

// Get implements scanner.ClientProvider; chain.ErrNoRPC for an unknown chain.
func (p *Provider) Get(_ context.Context, chainID string) (chain.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.clients[chainID]
	if !ok {
		return nil, fmt.Errorf("fake provider: chain %s: %w", chainID, chain.ErrNoRPC)
	}
	return c, nil
}

// Invalidate implements scanner.ClientProvider (it only records the call).
func (p *Provider) Invalidate(chainID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidated = append(p.invalidated, chainID)
}

// Invalidated returns the chain ids Invalidate was called with.
func (p *Provider) Invalidated() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.invalidated...)
}
