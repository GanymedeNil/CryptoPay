// Package deps pins the third-party modules the gateway is built on so that
// `go mod tidy` keeps them in go.mod while packages are developed in parallel.
package deps

import (
	_ "github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/btcsuite/btcd/btcutil/hdkeychain"
	_ "github.com/go-chi/chi/v5"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/pressly/goose/v3"
	_ "github.com/shopspring/decimal"
	_ "golang.org/x/crypto/bcrypt"
	_ "golang.org/x/time/rate"
)
