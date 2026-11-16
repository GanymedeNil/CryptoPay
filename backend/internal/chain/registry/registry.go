// Package registry builds and caches one chain.Client per configured chain.
//
// A client is built from the chain's *effective* configuration
// (store.EffectiveChain: RPC endpoints from the database, else the
// RPC_<CHAIN_ID> environment variable, else the built-in public default) by
// the chain.Factory registered for the chain's family. The cached client is
// rebuilt transparently when the effective endpoint list (or another
// attribute an adapter is constructed from) changes, e.g. after an admin
// edit, so callers simply call Get before every use.
package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"cryptopay/internal/chain"
	"cryptopay/internal/chain/evm"
	"cryptopay/internal/chain/solana"
	"cryptopay/internal/chain/tron"
	"cryptopay/internal/chain/utxo"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

// ErrNoFactory is returned when no adapter factory is registered for a
// chain's family.
var ErrNoFactory = errors.New("registry: no adapter for chain family")

// DefaultProbeTimeout bounds Probe when the context has no deadline.
const DefaultProbeTimeout = 15 * time.Second

// ChainLoader loads a chain's stored configuration (*store.Store satisfies it).
type ChainLoader interface {
	GetChain(ctx context.Context, id string) (domain.Chain, error)
}

// Factories maps a chain family to its adapter factory.
type Factories map[domain.Family]chain.Factory

// DefaultFactories returns the production adapters of all four families,
// logging to logger (nil = adapters' default).
func DefaultFactories(logger *slog.Logger) Factories {
	if logger == nil {
		return Factories{
			domain.FamilyEVM:    evm.Factory(),
			domain.FamilyTron:   tron.NewFactory(),
			domain.FamilyUTXO:   utxo.Factory(),
			domain.FamilySolana: solana.Factory(),
		}
	}
	return Factories{
		domain.FamilyEVM:    evm.Factory(evm.WithLogger(logger)),
		domain.FamilyTron:   tron.NewFactory(tron.WithLogger(logger)),
		domain.FamilyUTXO:   utxo.Factory(utxo.WithLogger(logger)),
		domain.FamilySolana: solana.Factory(solana.WithLogger(logger)),
	}
}

type entry struct {
	client chain.Client
	sig    string
}

// Registry caches chain clients. It is safe for concurrent use.
type Registry struct {
	loader    ChainLoader
	factories Factories

	mu      sync.Mutex
	clients map[string]entry
}

// New returns a Registry loading chain configuration from loader and
// building clients with factories (copied; use DefaultFactories in
// production, fakes in tests).
func New(loader ChainLoader, factories Factories) *Registry {
	f := make(Factories, len(factories))
	for k, v := range factories {
		f[k] = v
	}
	return &Registry{loader: loader, factories: f, clients: map[string]entry{}}
}

// Get returns the client of the chain with the given id, loading the chain's
// current configuration first, so an RPC change made in the admin console
// takes effect on the next call. Errors: the loader's error for an unknown
// chain (store.ErrNotFound), chain.ErrNoRPC when no endpoint is configured,
// ErrNoFactory for an unsupported family, or the factory's error.
func (r *Registry) Get(ctx context.Context, chainID string) (chain.Client, error) {
	if r.loader == nil {
		return nil, errors.New("registry: no chain loader configured")
	}
	c, err := r.loader.GetChain(ctx, chainID)
	if err != nil {
		return nil, fmt.Errorf("registry: load chain %s: %w", chainID, err)
	}
	return r.For(c)
}

// For returns the client for an already loaded chain (as returned by the
// store: RPCURLs = admin-configured endpoints). The cached client is reused
// unless the effective configuration changed since it was built.
func (r *Registry) For(c domain.Chain) (chain.Client, error) {
	eff, _ := store.EffectiveChain(c)
	sig := signature(eff)

	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.clients[c.ID]; ok {
		if e.sig == sig {
			return e.client, nil
		}
		closeClient(e.client)
		delete(r.clients, c.ID)
	}
	client, err := r.build(eff)
	if err != nil {
		return nil, err
	}
	r.clients[c.ID] = entry{client: client, sig: sig}
	return client, nil
}

// Invalidate drops the cached client of a chain; the next Get builds a new
// one. Call it when a chain stops being scanned or after persistent RPC
// failures (a fresh client starts again from the primary endpoint).
func (r *Registry) Invalidate(chainID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.clients[chainID]; ok {
		closeClient(e.client)
		delete(r.clients, chainID)
	}
}

// Probe checks an RPC endpoint for the admin "test RPC" button: it builds a
// throw-away client for c — using only overrideURL when it is non-empty,
// else the chain's effective endpoints — asks for the chain head and returns
// it with the time the call took. Nothing is cached. When ctx has no
// deadline DefaultProbeTimeout applies. Error messages never contain the
// URL's path or query (they may carry API keys).
func (r *Registry) Probe(ctx context.Context, c domain.Chain, overrideURL string) (head uint64, latency time.Duration, err error) {
	eff, _ := store.EffectiveChain(c)
	if u := strings.TrimSpace(overrideURL); u != "" {
		if err := store.ValidateRPCURL(u); err != nil {
			return 0, 0, fmt.Errorf("registry: %w", err)
		}
		eff.RPCURLs = []string{u}
	}
	client, err := r.build(eff)
	if err != nil {
		return 0, 0, err
	}
	defer closeClient(client)
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultProbeTimeout)
		defer cancel()
	}
	start := time.Now()
	head, err = client.Head(ctx)
	latency = time.Since(start)
	if err != nil {
		return 0, latency, fmt.Errorf("registry: probe %s: %w", c.ID, err)
	}
	return head, latency, nil
}

func (r *Registry) build(eff domain.Chain) (chain.Client, error) {
	if len(eff.RPCURLs) == 0 {
		return nil, fmt.Errorf("registry: chain %s: %w", eff.ID, chain.ErrNoRPC)
	}
	f, ok := r.factories[eff.Family]
	if !ok || f == nil {
		return nil, fmt.Errorf("%w: %s (chain %s)", ErrNoFactory, eff.Family, eff.ID)
	}
	client, err := f(eff)
	if err != nil {
		return nil, fmt.Errorf("registry: build client for %s: %w", eff.ID, err)
	}
	if client == nil {
		return nil, fmt.Errorf("registry: factory for %s returned no client", eff.ID)
	}
	return client, nil
}

// signature captures everything an adapter is constructed from. The
// confirmation count, poll interval and scan batch are read by the scanner
// on every tick and deliberately not part of it.
func signature(c domain.Chain) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%t|%+v", c.ID, c.Family, c.NetworkID, c.IsTestnet, c.Params)
	for _, u := range c.RPCURLs {
		b.WriteString("|")
		b.WriteString(u)
	}
	return b.String()
}

func closeClient(c chain.Client) {
	if cl, ok := c.(io.Closer); ok {
		_ = cl.Close()
	}
}
