package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
)

func TestCatalogIsConsistent(t *testing.T) {
	chains := map[string]domain.Chain{}
	for _, c := range Chains() {
		require.NotContains(t, chains, c.ID, "duplicate chain id")
		require.True(t, c.Family.Valid(), c.ID)
		require.NotEmpty(t, c.RPCURLs, c.ID)
		require.Positive(t, c.Confirmations, c.ID)
		require.Positive(t, c.ScanBatch, c.ID)
		require.Positive(t, int64(c.PollInterval), c.ID)
		require.Contains(t, c.ExplorerTxURL, "{tx}", c.ID)
		require.Contains(t, c.ExplorerAddr, "{address}", c.ID)
		if c.Family == domain.FamilyEVM {
			require.NotEmpty(t, c.NetworkID, c.ID)
		}
		if c.Family == domain.FamilyUTXO {
			require.NotEmpty(t, c.Params.Bech32HRP, c.ID)
			require.NotEmpty(t, c.Params.URIScheme, c.ID)
		}
		assert.Equal(t, !c.IsTestnet, c.Enabled, c.ID)
		chains[c.ID] = c
	}

	ids := map[string]bool{}
	contracts := map[string]bool{}
	for _, a := range Assets() {
		require.False(t, ids[a.ID], "duplicate asset id %s", a.ID)
		ids[a.ID] = true
		c, ok := chains[a.ChainID]
		require.True(t, ok, "asset %s references unknown chain", a.ID)
		require.LessOrEqual(t, a.QuotePrecision, a.Decimals, a.ID)
		require.NotEmpty(t, a.RateKey, a.ID)
		key := a.ChainID + "/" + a.ContractAddress
		require.False(t, contracts[key], "duplicate contract %s", key)
		contracts[key] = true
		if !a.IsNative() {
			canon, err := address.Normalize(c, a.ContractAddress)
			require.NoError(t, err, a.ID)
			require.Equal(t, canon, a.ContractAddress, "asset %s contract must be canonical", a.ID)
		} else {
			require.Equal(t, c.NativeSymbol, a.Symbol, a.ID)
		}
	}
}
