// Package e2e holds end-to-end tests of the whole gateway: the real HTTP
// router, invoice service, chain scanner and webhook dispatcher wired
// together against PostgreSQL, with scripted fake chains instead of RPC
// endpoints and an httptest merchant endpoint receiving the webhooks.
//
// The tests need TEST_DATABASE_URL (they are skipped without it).
package e2e
