package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestAuditLogs(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	actor := uuid.New()

	l1, err := s.InsertAuditLog(ctx, domain.AuditLog{ActorID: &actor, ActorEmail: "Owner@Example.com", Action: "chain.update",
		TargetType: "chain", TargetID: "ethereum", Detail: json.RawMessage(`{"enabled": false}`), IP: "1.2.3.4"})
	require.NoError(t, err)
	assert.Positive(t, l1.ID)
	assert.Equal(t, actor, *l1.ActorID)
	assert.JSONEq(t, `{"enabled": false}`, string(l1.Detail))
	assert.Equal(t, clk.Now(), l1.CreatedAt)
	clk.Advance(time.Second)
	l2, err := s.InsertAuditLog(ctx, domain.AuditLog{ActorEmail: "admin_1@example.com", Action: "merchant.create",
		TargetType: "merchant", TargetID: "m1"})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(l2.Detail))
	assert.Nil(t, l2.ActorID)
	clk.Advance(time.Second)
	l3, err := s.InsertAuditLog(ctx, domain.AuditLog{ActorEmail: "adminX1@example.com", Action: "chain.update", TargetType: "chain", TargetID: "tron"})
	require.NoError(t, err)

	_, err = s.InsertAuditLog(ctx, domain.AuditLog{})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.InsertAuditLog(ctx, domain.AuditLog{Action: "x", Detail: json.RawMessage(`nope`)})
	require.ErrorIs(t, err, store.ErrInvalid)

	ids := func(f store.AuditFilter) []int64 {
		t.Helper()
		p, err := s.ListAuditLogs(ctx, f)
		require.NoError(t, err)
		out := make([]int64, len(p.Items))
		for i, l := range p.Items {
			out[i] = l.ID
		}
		return out
	}
	assert.Equal(t, []int64{l3.ID, l2.ID, l1.ID}, ids(store.AuditFilter{}))
	assert.Equal(t, []int64{l3.ID, l1.ID}, ids(store.AuditFilter{Action: "chain.update"}))
	assert.Equal(t, []int64{l1.ID}, ids(store.AuditFilter{Actor: "owner@"}))
	assert.Equal(t, []int64{l2.ID}, ids(store.AuditFilter{Actor: "admin_1"}), "underscore is literal")
	assert.Equal(t, []int64{l3.ID}, ids(store.AuditFilter{TargetType: "chain", TargetID: "tron"}))
	assert.Equal(t, []int64{l2.ID}, ids(store.AuditFilter{Page: 2, PageSize: 1}))
	assert.Empty(t, ids(store.AuditFilter{Action: "nope"}))
}
