package registry_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/chain"
	"cryptopay/internal/chain/registry"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

type fakeClient struct {
	chain  domain.Chain
	head   uint64
	err    error
	delay  time.Duration
	closed bool
}

func (f *fakeClient) Head(ctx context.Context) (uint64, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return f.head, f.err
}
func (f *fakeClient) ScanTransfers(context.Context, chain.ScanRequest) ([]chain.Transfer, error) {
	return nil, nil
}
func (f *fakeClient) VerifyTransfer(context.Context, chain.Transfer) (bool, error) { return true, nil }
func (f *fakeClient) Close() error                                                 { f.closed = true; return nil }

type loader struct {
	mu     sync.Mutex
	chains map[string]domain.Chain
}

func (l *loader) GetChain(_ context.Context, id string) (domain.Chain, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.chains[id]
	if !ok {
		return domain.Chain{}, store.ErrNotFound
	}
	return c, nil
}
func (l *loader) set(c domain.Chain) { l.mu.Lock(); defer l.mu.Unlock(); l.chains[c.ID] = c }

type builds struct {
	mu      sync.Mutex
	clients []*fakeClient
	err     error
	head    uint64
}

func (b *builds) factory(c domain.Chain) (chain.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, b.err
	}
	fc := &fakeClient{chain: c, head: b.head}
	b.clients = append(b.clients, fc)
	return fc, nil
}
func (b *builds) count() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.clients) }

func setup(c ...domain.Chain) (*registry.Registry, *loader, *builds) {
	l := &loader{chains: map[string]domain.Chain{}}
	for _, x := range c {
		l.set(x)
	}
	b := &builds{head: 42}
	return registry.New(l, registry.Factories{domain.FamilyEVM: b.factory}), l, b
}

// "custom-x" is not in the built-in catalog and has no RPC_ env variable, so
// its effective endpoints are exactly what the test configures.
func custom(urls ...string) domain.Chain {
	return domain.Chain{ID: "custom-x", Family: domain.FamilyEVM, NetworkID: "999", RPCURLs: urls, Enabled: true}
}

func TestGetCachesAndRebuildsOnRPCChange(t *testing.T) {
	ctx := context.Background()
	r, l, b := setup(custom("https://a.example/key1"))
	c1, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	c2, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	assert.Same(t, c1, c2)
	assert.Equal(t, 1, b.count())
	assert.Equal(t, []string{"https://a.example/key1"}, c1.(*fakeClient).chain.RPCURLs)

	// Non-RPC settings do not rebuild.
	ch := custom("https://a.example/key1")
	ch.Confirmations, ch.ScanBatch, ch.PollInterval = 99, 7, time.Minute
	l.set(ch)
	c3, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	assert.Same(t, c1, c3)

	// Admin edits the endpoint list → new client, old one closed.
	l.set(custom("https://a.example/key1", "https://b.example"))
	c4, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	assert.NotSame(t, c1, c4)
	assert.Equal(t, 2, b.count())
	assert.True(t, c1.(*fakeClient).closed)
	assert.Equal(t, []string{"https://a.example/key1", "https://b.example"}, c4.(*fakeClient).chain.RPCURLs)

	// Reordering counts as a change too (the first URL is the primary).
	l.set(custom("https://b.example", "https://a.example/key1"))
	c5, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	assert.NotSame(t, c4, c5)
}

func TestInvalidate(t *testing.T) {
	ctx := context.Background()
	r, _, b := setup(custom("https://a.example"))
	c1, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	r.Invalidate("custom-x")
	r.Invalidate("unknown") // no-op
	assert.True(t, c1.(*fakeClient).closed)
	c2, err := r.Get(ctx, "custom-x")
	require.NoError(t, err)
	assert.NotSame(t, c1, c2)
	assert.Equal(t, 2, b.count())
}

func TestGetErrors(t *testing.T) {
	ctx := context.Background()
	r, l, b := setup(custom())
	_, err := r.Get(ctx, "missing")
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, err = r.Get(ctx, "custom-x")
	assert.ErrorIs(t, err, chain.ErrNoRPC)
	assert.Equal(t, 0, b.count())

	l.set(domain.Chain{ID: "sol-x", Family: domain.FamilySolana, RPCURLs: []string{"https://s.example"}})
	_, err = r.Get(ctx, "sol-x")
	assert.ErrorIs(t, err, registry.ErrNoFactory)

	l.set(custom("https://a.example"))
	boom := errors.New("bad config")
	b.err = boom
	_, err = r.Get(ctx, "custom-x")
	assert.ErrorIs(t, err, boom)
	b.err = nil
	_, err = r.Get(ctx, "custom-x")
	assert.NoError(t, err, "a failed build is not cached")
}

func TestEffectiveChainFallsBackToEnvAndCatalogDefault(t *testing.T) {
	ctx := context.Background()
	// "ethereum" has a built-in public default endpoint.
	r, l, b := setup(domain.Chain{ID: "ethereum", Family: domain.FamilyEVM, NetworkID: "1"})
	c, err := r.Get(ctx, "ethereum")
	require.NoError(t, err)
	def := c.(*fakeClient).chain.RPCURLs
	require.NotEmpty(t, def)

	t.Setenv("RPC_ETHEREUM", "https://env.example/v2/secret")
	c, err = r.Get(ctx, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://env.example/v2/secret"}, c.(*fakeClient).chain.RPCURLs)
	assert.Equal(t, 2, b.count())

	l.set(domain.Chain{ID: "ethereum", Family: domain.FamilyEVM, NetworkID: "1", RPCURLs: []string{"https://db.example"}})
	c, err = r.Get(ctx, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://db.example"}, c.(*fakeClient).chain.RPCURLs)
}

func TestConcurrentGetBuildsOnce(t *testing.T) {
	r, _, b := setup(custom("https://a.example"))
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_, err := r.Get(context.Background(), "custom-x")
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, 1, b.count())
}

func TestProbe(t *testing.T) {
	ctx := context.Background()
	r, _, b := setup()
	ch := custom("https://a.example")

	head, lat, err := r.Probe(ctx, ch, "")
	require.NoError(t, err)
	assert.Equal(t, uint64(42), head)
	assert.GreaterOrEqual(t, lat, time.Duration(0))
	require.Equal(t, 1, b.count())
	assert.Equal(t, []string{"https://a.example"}, b.clients[0].chain.RPCURLs)
	assert.True(t, b.clients[0].closed, "probe clients are thrown away")

	// Override URL replaces the configured list and is not cached.
	_, _, err = r.Probe(ctx, ch, " https://override.example/x ")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://override.example/x"}, b.clients[1].chain.RPCURLs)
	_, err = r.For(ch)
	require.NoError(t, err)
	assert.Equal(t, 3, b.count(), "Probe must not populate the cache")

	// Override works for a chain without configured endpoints.
	_, _, err = r.Probe(ctx, custom(), "https://override.example")
	require.NoError(t, err)
	_, _, err = r.Probe(ctx, custom(), "")
	assert.ErrorIs(t, err, chain.ErrNoRPC)

	// Invalid override: rejected without leaking path / query.
	_, _, err = r.Probe(ctx, ch, "ftp://host.example/secret-key")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-key")
	_, _, err = r.Probe(ctx, ch, "not a url")
	require.Error(t, err)
}

func TestProbeErrorAndTimeout(t *testing.T) {
	l := &loader{chains: map[string]domain.Chain{}}
	boom := errors.New("node down")
	slow := registry.New(l, registry.Factories{
		domain.FamilyEVM:  func(domain.Chain) (chain.Client, error) { return &fakeClient{err: boom}, nil },
		domain.FamilyTron: func(domain.Chain) (chain.Client, error) { return &fakeClient{delay: time.Minute}, nil },
	})
	_, _, err := slow.Probe(context.Background(), custom("https://a.example"), "")
	assert.ErrorIs(t, err, boom)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, lat, err := slow.Probe(ctx, domain.Chain{ID: "tron-x", Family: domain.FamilyTron, RPCURLs: []string{"https://t.example"}}, "")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Greater(t, lat, time.Duration(0))
}

func TestDefaultFactoriesBuildEveryCatalogChain(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	for _, f := range []registry.Factories{registry.DefaultFactories(nil), registry.DefaultFactories(logger)} {
		for _, fam := range []domain.Family{domain.FamilyEVM, domain.FamilyTron, domain.FamilyUTXO, domain.FamilySolana} {
			assert.NotNil(t, f[fam], fam)
		}
		// Building a client does no network I/O; every built-in chain must
		// get an adapter of its family from its effective (default) endpoints.
		r := registry.New(nil, f)
		for _, c := range catalog.Chains() {
			c.RPCURLs = nil // as stored: no admin-configured endpoints
			client, err := r.For(c)
			require.NoError(t, err, c.ID)
			require.NotNil(t, client, c.ID)
			again, err := r.For(c)
			require.NoError(t, err)
			assert.Same(t, client, again, "%s: cached", c.ID)
		}
	}
	// A nil loader is reported, not dereferenced.
	_, err := registry.New(nil, registry.DefaultFactories(nil)).Get(context.Background(), "ethereum")
	assert.Error(t, err)
}

// *store.Store is the production ChainLoader.
var _ registry.ChainLoader = (*store.Store)(nil)

func TestGetWithStore(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	ctx := context.Background()
	b := &builds{head: 7}
	r := registry.New(s, registry.Factories{domain.FamilyTron: b.factory})

	c1, err := r.Get(ctx, "tron")
	require.NoError(t, err)
	def := c1.(*fakeClient).chain
	assert.Equal(t, "tron", def.ID)
	assert.NotEmpty(t, def.RPCURLs, "built-in default endpoint")

	// An admin edit of the endpoints is picked up by the next Get.
	_, err = s.UpdateChain(ctx, "tron", store.ChainUpdate{RPCURLs: &[]string{"https://tron.example/key"}})
	require.NoError(t, err)
	c2, err := r.Get(ctx, "tron")
	require.NoError(t, err)
	assert.NotSame(t, c1, c2)
	assert.Equal(t, []string{"https://tron.example/key"}, c2.(*fakeClient).chain.RPCURLs)
	assert.True(t, c1.(*fakeClient).closed)

	_, err = r.Get(ctx, "nope")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = r.Get(ctx, "ethereum")
	assert.ErrorIs(t, err, registry.ErrNoFactory)
}

func TestConcurrentGetWhileConfigChanges(t *testing.T) {
	r, l, _ := setup(custom("https://a.example"))
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for j := range 50 {
				if (i+j)%7 == 0 {
					l.set(custom("https://a.example", "https://b.example"))
				}
				c, err := r.Get(context.Background(), "custom-x")
				if assert.NoError(t, err) {
					assert.NotNil(t, c)
				}
				if j%10 == 0 {
					r.Invalidate("custom-x")
				}
			}
		})
	}
	wg.Wait()
}
