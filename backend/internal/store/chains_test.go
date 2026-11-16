package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/domain"
	"cryptopay/internal/secret"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestListAndGetChains(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	chains, err := s.ListChains(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, chains)
	seenTestnet := false
	for _, c := range chains {
		if c.IsTestnet {
			seenTestnet = true
		} else {
			assert.False(t, seenTestnet, "mainnets come first")
		}
	}
	c, err := s.GetChain(ctx, "tron")
	require.NoError(t, err)
	assert.Equal(t, domain.FamilyTron, c.Family)
	assert.Equal(t, 19, c.Confirmations)
	assert.Equal(t, 5*time.Second, c.PollInterval)
	assert.Equal(t, 20, c.ScanBatch)
	assert.Contains(t, c.ExplorerTxURL, "{tx}")
	assert.Nil(t, c.WalletID)
	assert.Zero(t, c.ScanCursor)
	assert.Nil(t, c.LastScanAt)
	_, err = s.GetChain(ctx, "nope")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestUpdateChain(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store
	storetest.SeedCatalog(t, s)

	urls := []string{"https://eth-mainnet.g.alchemy.com/v2/SECRETKEY", " ", "wss://eth.example/ws?key=abc"}
	c, err := s.UpdateChain(ctx, "ethereum", store.ChainUpdate{
		Enabled: storetest.Ptr(false), RPCURLs: &urls, Confirmations: storetest.Ptr(20),
		PollInterval: storetest.Ptr(3 * time.Second), ScanBatch: storetest.Ptr(10),
	})
	require.NoError(t, err)
	assert.False(t, c.Enabled)
	assert.Equal(t, []string{"https://eth-mainnet.g.alchemy.com/v2/SECRETKEY", "wss://eth.example/ws?key=abc"}, c.RPCURLs)
	assert.Equal(t, 20, c.Confirmations)
	assert.Equal(t, 3*time.Second, c.PollInterval)
	assert.Equal(t, 10, c.ScanBatch)

	// Encrypted at rest.
	var enc string
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT rpc_urls_enc FROM chains WHERE id = 'ethereum'`).Scan(&enc))
	assert.True(t, secret.IsEncrypted(enc))
	assert.NotContains(t, enc, "SECRETKEY")
	assert.NotContains(t, enc, "alchemy")

	got, err := s.GetChain(ctx, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, c.RPCURLs, got.RPCURLs)
	urls2, src := store.ResolveRPC(got)
	assert.Equal(t, store.RPCSourceDatabase, src)
	assert.Equal(t, c.RPCURLs, urls2)

	// Untouched fields stay; empty slice clears the URLs.
	c, err = s.UpdateChain(ctx, "ethereum", store.ChainUpdate{RPCURLs: &[]string{}})
	require.NoError(t, err)
	assert.Empty(t, c.RPCURLs)
	assert.Equal(t, 20, c.Confirmations)
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT rpc_urls_enc FROM chains WHERE id = 'ethereum'`).Scan(&enc))
	assert.Empty(t, enc)

	// Schema checks → ErrInvalid.
	for _, upd := range []store.ChainUpdate{
		{Confirmations: storetest.Ptr(0)},
		{PollInterval: storetest.Ptr(100 * time.Millisecond)},
		{ScanBatch: storetest.Ptr(0)},
		{RPCURLs: &[]string{"ftp://x.example"}},
		{RPCURLs: &[]string{"https://"}},
	} {
		_, err := s.UpdateChain(ctx, "ethereum", upd)
		require.ErrorIs(t, err, store.ErrInvalid, "%+v", upd)
	}
	_, err = s.UpdateChain(ctx, "nope", store.ChainUpdate{Enabled: storetest.Ptr(true)})
	require.ErrorIs(t, err, store.ErrNotFound)

	// Wallet assignment.
	evm := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	tron := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	c, err = s.UpdateChain(ctx, "base", store.ChainUpdate{SetWallet: true, WalletID: &evm.ID})
	require.NoError(t, err)
	require.NotNil(t, c.WalletID)
	assert.Equal(t, evm.ID, *c.WalletID)
	_, err = s.UpdateChain(ctx, "base", store.ChainUpdate{SetWallet: true, WalletID: &tron.ID})
	require.ErrorIs(t, err, store.ErrInvalid, "family mismatch")
	unknown := uuid.New()
	_, err = s.UpdateChain(ctx, "base", store.ChainUpdate{SetWallet: true, WalletID: &unknown})
	require.ErrorIs(t, err, store.ErrForeignKey)
	// Not setting SetWallet keeps the wallet even if WalletID is nil.
	c, err = s.UpdateChain(ctx, "base", store.ChainUpdate{Enabled: storetest.Ptr(true)})
	require.NoError(t, err)
	require.NotNil(t, c.WalletID)
	c, err = s.UpdateChain(ctx, "base", store.ChainUpdate{SetWallet: true})
	require.NoError(t, err)
	assert.Nil(t, c.WalletID)
}

func TestScanStateDoesNotClobberAdminEdits(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	before, err := s.GetChain(ctx, "bitcoin")
	require.NoError(t, err)

	// Admin edits (in a transaction still open) and the scanner writes progress.
	err = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.UpdateChain(ctx, "bitcoin", store.ChainUpdate{Confirmations: storetest.Ptr(3)})
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			done <- s.UpdateScanState(ctx, "bitcoin", store.ScanState{Cursor: 900_000, Head: 900_010,
				ScannedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Error: "rpc: 429"})
		}()
		time.Sleep(50 * time.Millisecond) // scanner blocks on the row lock until commit
		return nil
	})
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	var after domain.Chain
	require.Eventually(t, func() bool {
		after, err = s.GetChain(ctx, "bitcoin")
		return err == nil && after.ScanCursor == 900_000
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, 3, after.Confirmations, "admin edit kept")
	assert.EqualValues(t, 900_010, after.LastHead)
	assert.Equal(t, "rpc: 429", after.LastError)
	require.NotNil(t, after.LastScanAt)
	assert.Equal(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), *after.LastScanAt)
	assert.Equal(t, before.UpdatedAt.After(after.UpdatedAt), false)

	// Scanner writes never touch config / updated_at.
	mid, _ := s.GetChain(ctx, "bitcoin")
	require.NoError(t, s.UpdateScanState(ctx, "bitcoin", store.ScanState{Cursor: 900_001, Head: 900_011}))
	end, _ := s.GetChain(ctx, "bitcoin")
	assert.Equal(t, mid.UpdatedAt, end.UpdatedAt)
	assert.Equal(t, "", end.LastError)
	assert.EqualValues(t, 900_001, end.ScanCursor)

	require.ErrorIs(t, s.UpdateScanState(ctx, "nope", store.ScanState{}), store.ErrNotFound)
	require.ErrorIs(t, s.UpdateScanState(ctx, "bitcoin", store.ScanState{Cursor: 1 << 63}), store.ErrInvalid)
}

// Not parallel: uses t.Setenv.
func TestResolveRPC(t *testing.T) {
	c := domain.Chain{ID: "tron-nile"}
	t.Setenv("RPC_TRON_NILE", "")
	urls, src := store.ResolveRPC(c)
	assert.Equal(t, store.RPCSourceDefault, src)
	var def []string
	for _, cc := range catalog.Chains() {
		if cc.ID == "tron-nile" {
			def = cc.RPCURLs
		}
	}
	assert.Equal(t, def, urls)

	t.Setenv("RPC_TRON_NILE", "https://nile.example/a, https://nile.example/b")
	urls, src = store.ResolveRPC(c)
	assert.Equal(t, store.RPCSourceEnv, src)
	assert.Equal(t, []string{"https://nile.example/a", "https://nile.example/b"}, urls)

	c.RPCURLs = []string{"https://db.example"}
	eff, src := store.EffectiveChain(c)
	assert.Equal(t, store.RPCSourceDatabase, src)
	assert.Equal(t, []string{"https://db.example"}, eff.RPCURLs)

	urls, src = store.ResolveRPC(domain.Chain{ID: "custom-chain"})
	assert.Equal(t, store.RPCSourceNone, src)
	assert.Empty(t, urls)

	// The returned slice is a copy.
	urls, _ = store.ResolveRPC(domain.Chain{ID: "ethereum"})
	urls[0] = "mutated"
	urls2, _ := store.ResolveRPC(domain.Chain{ID: "ethereum"})
	assert.NotEqual(t, "mutated", urls2[0])
}

func TestValidateRPCURL(t *testing.T) {
	for _, ok := range []string{"https://a.example", "http://127.0.0.1:8545", "wss://x.example/ws", "ws://h:1"} {
		assert.NoError(t, store.ValidateRPCURL(ok), ok)
	}
	for _, bad := range []string{"", "a.example", "ftp://a.example", "https://", "://x"} {
		assert.Error(t, store.ValidateRPCURL(bad), bad)
	}
	err := store.ValidateRPCURL("ftp://h.example/secret-key")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "secret-key"), "error must not leak the path: %v", err)
}
