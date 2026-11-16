package solana

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func slogTo(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{w: w}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestNew(t *testing.T) {
	sol := domain.Chain{ID: "solana", Family: domain.FamilySolana, RPCURLs: []string{"https://api.mainnet-beta.solana.com"}}

	c, err := New(sol)
	require.NoError(t, err)
	var _ chain.Client = c

	_, err = New(domain.Chain{ID: "solana", Family: domain.FamilySolana})
	assert.ErrorIs(t, err, chain.ErrNoRPC)
	_, err = New(domain.Chain{ID: "solana", Family: domain.FamilySolana, RPCURLs: []string{""}})
	assert.ErrorIs(t, err, chain.ErrNoRPC)

	_, err = New(domain.Chain{ID: "ethereum", Family: domain.FamilyEVM, RPCURLs: sol.RPCURLs})
	assert.Error(t, err)

	for _, bad := range []string{"api.mainnet-beta.solana.com", "ws://host", "https://", "://x"} {
		_, err = New(domain.Chain{ID: "solana", Family: domain.FamilySolana, RPCURLs: []string{bad}})
		assert.Error(t, err, bad)
	}

	// Options with nonsensical values keep the defaults instead of breaking the client.
	c, err = New(sol, WithConcurrency(0), WithMaxSignaturePages(-1), WithRetries(-1), WithHTTPClient(nil),
		WithBackoff(0, 0), WithRequestTimeout(0), WithMaxTransactionVersion(300), WithLogger(nil), WithMaxResponseBytes(0))
	require.NoError(t, err)
	assert.Equal(t, defaultConcurrency, c.concurrency)
	assert.Equal(t, defaultMaxSigPages, c.maxSigPages)
	assert.Equal(t, defaultMaxTxVersion, c.maxTxVersion)
	assert.Equal(t, defaultRetries, c.rpc.retries)
	assert.Equal(t, defaultRequestTimeout, c.rpc.timeout)

	hc := &http.Client{}
	c, err = New(sol, WithHTTPClient(hc), WithConcurrency(2), WithMaxSignaturePages(3), WithMaxTransactionVersion(0))
	require.NoError(t, err)
	assert.Same(t, hc, c.rpc.hc)
	assert.Equal(t, 2, c.concurrency)
	assert.Equal(t, 3, c.maxSigPages)
	assert.Equal(t, 0, c.maxTxVersion)
}

func TestFactory(t *testing.T) {
	cl, err := Factory()(domain.Chain{ID: "solana", Family: domain.FamilySolana})
	require.ErrorIs(t, err, chain.ErrNoRPC)
	assert.Nil(t, cl, "no typed-nil interface on error")

	cl, err = Factory(WithConcurrency(1))(domain.Chain{ID: "solana", Family: domain.FamilySolana, RPCURLs: []string{"http://127.0.0.1:1"}})
	require.NoError(t, err)
	assert.IsType(t, &Client{}, cl)
}

func TestVerifyTransfer(t *testing.T) {
	f := newFakeNode(t)
	c := f.client(t)
	finalized := fakeSig("finalized")
	failed := fakeSig("failed")
	confirmed := fakeSig("confirmed")
	legacyRooted := fakeSig("legacy-rooted")
	// Shapes as returned by api.mainnet-beta.solana.com.
	f.setStatus(finalized, `{"confirmationStatus":"finalized","confirmations":null,"err":null,"slot":470031877,"status":{"Ok":null}}`)
	f.setStatus(failed, `{"confirmationStatus":"finalized","confirmations":null,"err":{"InstructionError":[3,{"Custom":6001}]},"slot":470031877,"status":{"Err":{"InstructionError":[3,{"Custom":6001}]}}}`)
	f.setStatus(confirmed, `{"confirmationStatus":"confirmed","confirmations":12,"err":null,"slot":470031877,"status":{"Ok":null}}`)
	f.setStatus(legacyRooted, `{"confirmations":null,"err":null,"slot":470031877,"status":{"Ok":null}}`)

	verify := func(sig string, slot uint64) (bool, error) {
		return c.VerifyTransfer(context.Background(), chain.Transfer{TxHash: sig, BlockNumber: slot})
	}

	ok, err := verify(finalized, 470031877)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = verify(legacyRooted, 470031877)
	require.NoError(t, err)
	assert.True(t, ok, "rooted status from nodes without confirmationStatus")

	ok, err = verify(finalized, 470031878)
	require.NoError(t, err)
	assert.False(t, ok, "not at the recorded slot")

	ok, err = verify(fakeSig("unknown"), 470031877)
	require.NoError(t, err)
	assert.False(t, ok, "unknown to the cluster")

	ok, err = verify(failed, 470031877)
	require.NoError(t, err)
	assert.False(t, ok, "failed transaction")

	ok, err = verify(confirmed, 470031877)
	require.Error(t, err, "not finalized yet: retry later rather than orphan")
	assert.False(t, ok)

	calls := f.count("getSignatureStatuses")
	_, err = verify("0xdeadbeef", 1)
	require.Error(t, err)
	assert.Equal(t, calls, f.count("getSignatureStatuses"), "invalid signature rejected locally")
}
