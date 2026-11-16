package store_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestWalletCRUD(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)

	x, err := s.CreateWallet(ctx, domain.Wallet{Name: " EVM xpub ", Family: domain.FamilyEVM, Kind: domain.WalletXpub,
		Xpub: storetest.TestXpub, Address: "ignored"})
	require.NoError(t, err)
	assert.Equal(t, "EVM xpub", x.Name)
	assert.Equal(t, "", x.Address)
	assert.Zero(t, x.NextIndex)

	st, err := s.CreateWallet(ctx, domain.Wallet{Name: "Sol", Family: domain.FamilySolana, Kind: domain.WalletStatic,
		Address: storetest.StaticAddresses[domain.FamilySolana], Xpub: "ignored", NextIndex: 9})
	require.NoError(t, err)
	assert.Equal(t, "", st.Xpub)
	assert.Zero(t, st.NextIndex)

	for _, bad := range []domain.Wallet{
		{Name: "", Family: domain.FamilyEVM, Kind: domain.WalletXpub, Xpub: "x"},
		{Name: "n", Family: "doge", Kind: domain.WalletXpub, Xpub: "x"},
		{Name: "n", Family: domain.FamilyEVM, Kind: "hot", Xpub: "x"},
		{Name: "n", Family: domain.FamilyEVM, Kind: domain.WalletXpub},
		{Name: "n", Family: domain.FamilyEVM, Kind: domain.WalletStatic},
	} {
		_, err := s.CreateWallet(ctx, bad)
		require.ErrorIs(t, err, store.ErrInvalid, "%+v", bad)
	}

	got, err := s.GetWallet(ctx, x.ID)
	require.NoError(t, err)
	assert.Equal(t, x, got)
	_, err = s.GetWallet(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	storetest.AssignWallet(t, s, "polygon", x.ID)
	storetest.AssignWallet(t, s, "ethereum", x.ID)
	list, err := s.ListWallets(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, x.ID, list[0].ID)
	assert.Equal(t, []string{"ethereum", "polygon"}, list[0].ChainIDs)
	assert.Equal(t, []string{}, list[1].ChainIDs)
	v, err := s.GetWalletView(ctx, x.ID)
	require.NoError(t, err)
	assert.Equal(t, list[0], v)

	r, err := s.RenameWallet(ctx, x.ID, "Main")
	require.NoError(t, err)
	assert.Equal(t, "Main", r.Name)
	_, err = s.RenameWallet(ctx, x.ID, " ")
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.RenameWallet(ctx, uuid.New(), "x")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteWallet(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	w := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	storetest.AssignWallet(t, s, "tron", w.ID)

	used, err := s.WalletInUse(ctx, w.ID)
	require.NoError(t, err)
	assert.False(t, used)

	m := storetest.Merchant(t, s)
	inv := storetest.Invoice(t, s, m.ID)
	storetest.Quote(t, s, inv, "usdt-tron", w)
	used, err = s.WalletInUse(ctx, w.ID)
	require.NoError(t, err)
	assert.True(t, used)
	require.ErrorIs(t, s.DeleteWallet(ctx, w.ID), store.ErrInUse)

	w2 := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletStatic)
	storetest.AssignWallet(t, s, "tron-nile", w2.ID)
	require.NoError(t, s.DeleteWallet(ctx, w2.ID))
	c, err := s.GetChain(ctx, "tron-nile")
	require.NoError(t, err)
	assert.Nil(t, c.WalletID, "chain unassigned by ON DELETE SET NULL")
	require.ErrorIs(t, s.DeleteWallet(ctx, w2.ID), store.ErrNotFound)
}

func TestAllocateAddressIndexConcurrent(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	w := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)

	const workers, per = 16, 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[uint32]bool{}
	for range workers {
		wg.Go(func() {
			for range per {
				idx, err := s.AllocateAddressIndex(ctx, w.ID)
				if !assert.NoError(t, err) {
					return
				}
				mu.Lock()
				assert.False(t, seen[idx], "index %d handed out twice", idx)
				seen[idx] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Len(t, seen, workers*per)
	for i := range uint32(workers * per) {
		assert.True(t, seen[i], "missing index %d", i)
	}
	got, err := s.GetWallet(ctx, w.ID)
	require.NoError(t, err)
	assert.EqualValues(t, workers*per, got.NextIndex)
}

func TestAllocateAddressIndexErrors(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store
	_, err := s.AllocateAddressIndex(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	st := storetest.Wallet(t, s, domain.FamilySolana, domain.WalletStatic)
	_, err = s.AllocateAddressIndex(ctx, st.ID)
	require.ErrorIs(t, err, store.ErrInvalid)

	x := storetest.Wallet(t, s, domain.FamilyUTXO, domain.WalletXpub)
	_, err = d.Pool.Exec(ctx, `UPDATE wallets SET next_index = $2 WHERE id = $1`, x.ID, int64(store.MaxAddressIndex))
	require.NoError(t, err)
	idx, err := s.AllocateAddressIndex(ctx, x.ID)
	require.NoError(t, err)
	assert.EqualValues(t, store.MaxAddressIndex, idx, "last non-hardened index")
	_, err = s.AllocateAddressIndex(ctx, x.ID)
	require.ErrorIs(t, err, store.ErrInvalid, "exhausted")

	// Inside a transaction a rollback returns the index.
	y := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	_ = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.AllocateAddressIndex(ctx, y.ID)
		require.NoError(t, err)
		return assert.AnError
	})
	idx, err = s.AllocateAddressIndex(ctx, y.ID)
	require.NoError(t, err)
	assert.Zero(t, idx)
}
