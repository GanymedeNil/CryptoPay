// Command gateway is the CryptoPay server binary.
//
//	gateway [serve] [--no-migrate] [--addr :8080]   run the HTTP API, chain scanners and webhook dispatcher
//	gateway migrate                                  apply database migrations and exit
//	gateway create-admin --email E --password P [--role owner|admin|viewer]
//	gateway seed-dev [--merchant-name N] [--webhook-url U]   development data (refused in production)
//	gateway healthcheck [--url U]                    probe /healthz (container HEALTHCHECK; exit 0 when healthy)
//	gateway version
//
// Configuration comes from environment variables (see package config).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

// Set with -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const usage = `usage: gateway <command> [flags]

commands:
  serve         run the HTTP API, chain scanners, webhook dispatcher (default)
  migrate       apply database migrations and exit
  create-admin  create an admin user (--email, --password, --role)
  seed-dev      create development data: demo merchant + API key, test-only
                testnet wallets (refused when APP_ENV=production)
  healthcheck   probe the local /healthz endpoint (for container health checks)
  version       print the version

Run "gateway <command> -h" for the flags of a command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "gateway:", err)
		var u usageError
		if errors.As(err, &u) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// usageError is a command line mistake (exit status 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

// run executes the command line args (without the program name) with the
// environment lookup getenv, writing output and logs to stdout. It returns
// when the command is done or, for serve, after ctx is cancelled and the
// server has shut down.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return runServe(ctx, args, getenv, stdout)
	case "migrate":
		return runMigrate(ctx, args, getenv, stdout)
	case "create-admin":
		return runCreateAdmin(ctx, args, getenv, stdout)
	case "seed-dev":
		return runSeedDev(ctx, args, getenv, stdout)
	case "healthcheck":
		return runHealthcheck(ctx, args, getenv, stdout)
	case "version":
		if err := parseFlags(newFlagSet("version", stdout), args); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "cryptopay gateway %s (commit %s, built %s, %s)\n", version, commit, date, runtime.Version())
		return nil
	case "help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stdout, usage)
	return usagef("unknown command %q", cmd)
}

func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

// parseFlags parses args and rejects positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	if fs.NArg() > 0 {
		return usagef("%s: unexpected argument %q", fs.Name(), fs.Arg(0))
	}
	return nil
}

// newLogger returns the process logger: JSON lines for the server, text for
// one-shot commands.
func newLogger(w io.Writer, level slog.Level, json bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if json {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
