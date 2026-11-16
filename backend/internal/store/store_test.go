package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

var ctx = context.Background()

func TestPingAndAccessors(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	require.NoError(t, d.Store.Ping(ctx))
	assert.Same(t, d.Pool, d.Store.Pool())
	assert.NotNil(t, d.Store.Box())
	assert.False(t, d.Store.InTx())
	now := d.Store.Now()
	assert.Equal(t, time.UTC, now.Location())
	assert.Zero(t, now.Nanosecond()%1000, "µs precision")
}

func TestNewPanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { store.New(nil, storetest.Box()) })
}

func TestWithClock(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 2, 3, 4, 5, 123456789, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	assert.Equal(t, time.Date(2030, 1, 2, 3, 4, 5, 123456000, time.UTC), s.Now())
	m := storetest.Merchant(t, s)
	assert.Equal(t, s.Now(), m.CreatedAt)
	assert.Equal(t, s.Now(), m.UpdatedAt)
	clk.Advance(time.Hour)
	name := "renamed"
	m2, err := s.UpdateMerchant(ctx, m.ID, store.MerchantUpdate{Name: &name})
	require.NoError(t, err)
	assert.Equal(t, m.CreatedAt.Add(time.Hour), m2.UpdatedAt)
}

func TestWithTxCommitAndRollback(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)

	var committed domain.Merchant
	err := s.WithTx(ctx, func(tx *store.Store) error {
		assert.True(t, tx.InTx())
		var err error
		committed, err = tx.CreateMerchant(ctx, domain.Merchant{Name: "committed"})
		return err
	})
	require.NoError(t, err)
	_, err = s.GetMerchant(ctx, committed.ID)
	require.NoError(t, err)

	boom := errors.New("boom")
	var rolled domain.Merchant
	err = s.WithTx(ctx, func(tx *store.Store) error {
		var err error
		rolled, err = tx.CreateMerchant(ctx, domain.Merchant{Name: "rolled back"})
		require.NoError(t, err)
		// Visible inside the transaction …
		_, err = tx.GetMerchant(ctx, rolled.ID)
		require.NoError(t, err)
		// … but not outside before commit.
		_, err = s.GetMerchant(ctx, rolled.ID)
		require.ErrorIs(t, err, store.ErrNotFound)
		return boom
	})
	require.ErrorIs(t, err, boom)
	_, err = s.GetMerchant(ctx, rolled.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestWithTxPanicRollsBack(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	id := uuid.New()
	assert.PanicsWithValue(t, "kaboom", func() {
		_ = s.WithTx(ctx, func(tx *store.Store) error {
			_, err := tx.CreateMerchant(ctx, domain.Merchant{ID: id, Name: "x"})
			require.NoError(t, err)
			panic("kaboom")
		})
	})
	_, err := s.GetMerchant(ctx, id)
	require.ErrorIs(t, err, store.ErrNotFound)
	// The pool is still healthy.
	require.NoError(t, s.Ping(ctx))
}

func TestNestedWithTxIsSavepoint(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	outerID, innerID := uuid.New(), uuid.New()
	err := s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.CreateMerchant(ctx, domain.Merchant{ID: outerID, Name: "outer"})
		require.NoError(t, err)
		inner := tx.WithTx(ctx, func(tx2 *store.Store) error {
			_, err := tx2.CreateMerchant(ctx, domain.Merchant{ID: innerID, Name: "inner"})
			require.NoError(t, err)
			return errors.New("undo inner")
		})
		require.Error(t, inner)
		// Outer transaction is still usable.
		_, err = tx.GetMerchant(ctx, outerID)
		return err
	})
	require.NoError(t, err)
	_, err = s.GetMerchant(ctx, outerID)
	require.NoError(t, err)
	_, err = s.GetMerchant(ctx, innerID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestSerializationFailureIsRetried(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	m := storetest.Merchant(t, s)

	// Two serializable transactions doing read-modify-write on the same row
	// conflict; with retries both eventually succeed.
	var wg sync.WaitGroup
	var mu sync.Mutex
	attempts := 0
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Go(func() {
			<-start
			errs[i] = s.WithTxOptions(ctx, store.TxOptions{Isolation: store.IsolationSerializable}, func(tx *store.Store) error {
				mu.Lock()
				attempts++
				mu.Unlock()
				cur, err := tx.GetMerchant(ctx, m.ID)
				if err != nil {
					return err
				}
				time.Sleep(50 * time.Millisecond) // make the transactions overlap
				name := cur.Name + "+"
				_, err = tx.UpdateMerchant(ctx, m.ID, store.MerchantUpdate{Name: &name})
				return err
			})
		})
	}
	close(start)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	got, err := s.GetMerchant(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, m.Name+"++", got.Name, "both increments applied exactly once")
	assert.Greater(t, attempts, 2, "at least one retry happened")
}

func TestSerializationRetriesExhausted(t *testing.T) {
	t.Parallel()
	s := storetest.New(t, store.WithTxRetries(0))
	m := storetest.Merchant(t, s)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Go(func() {
			<-start
			errs[i] = s.WithTxOptions(ctx, store.TxOptions{Isolation: store.IsolationSerializable}, func(tx *store.Store) error {
				cur, err := tx.GetMerchant(ctx, m.ID)
				if err != nil {
					return err
				}
				time.Sleep(50 * time.Millisecond)
				name := cur.Name + "+"
				_, err = tx.UpdateMerchant(ctx, m.ID, store.MerchantUpdate{Name: &name})
				return err
			})
		})
	}
	close(start)
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			require.ErrorIs(t, err, store.ErrSerialization)
			failed++
		}
	}
	assert.Equal(t, 1, failed)
}

func TestReadOnlyTx(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	err := s.WithTxOptions(ctx, store.TxOptions{ReadOnly: true}, func(tx *store.Store) error {
		_, err := tx.CreateMerchant(ctx, domain.Merchant{Name: "nope"})
		return err
	})
	require.Error(t, err)
}

func TestForUpdateRequiresTx(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	_, err := s.GetInvoiceForUpdate(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrTxRequired)
	_, err = s.GetQuoteForUpdate(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrTxRequired)
	_, err = s.GetTransferForUpdate(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrTxRequired)
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := s.ListMerchants(cctx, store.MerchantFilter{})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSchemasAreIsolated(t *testing.T) {
	t.Parallel()
	a := storetest.New(t)
	b := storetest.New(t)
	m := storetest.Merchant(t, a)
	_, err := b.GetMerchant(ctx, m.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestConstraintHelpers(t *testing.T) {
	assert.Equal(t, "", store.ConstraintName(errors.New("x")))
	assert.False(t, store.IsConstraint(nil, "x"))
}
