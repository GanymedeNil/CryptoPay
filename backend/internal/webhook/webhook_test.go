package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
	"cryptopay/internal/webhook"
)

type env struct {
	t     *testing.T
	st    *store.Store
	clock *storetest.Clock
	m     domain.Merchant
	inv   domain.Invoice
}

func setup(t *testing.T, settings *domain.Settings) *env {
	t.Helper()
	clock := storetest.FixedClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	st := storetest.New(t, store.WithClock(clock.Now))
	if settings != nil {
		if _, err := st.PutSettings(context.Background(), *settings); err != nil {
			t.Fatal(err)
		}
	}
	m := storetest.Merchant(t, st)
	inv := storetest.Invoice(t, st, m.ID)
	return &env{t: t, st: st, clock: clock, m: m, inv: inv}
}

func (e *env) event(url string, mods ...func(*domain.WebhookEvent)) domain.WebhookEvent {
	return storetest.WebhookEvent(e.t, e.st, e.inv, append([]func(*domain.WebhookEvent){func(w *domain.WebhookEvent) { w.URL = url }}, mods...)...)
}

func (e *env) disp(opts ...webhook.Option) *webhook.Dispatcher {
	return webhook.New(e.st, nil, e.clock.Now, append([]webhook.Option{webhook.AllowPrivateTargets(true), webhook.WithJitter(0)}, opts...)...)
}

func (e *env) get(id uuid.UUID) domain.WebhookEvent {
	e.t.Helper()
	ev, err := e.st.GetWebhookEvent(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ev
}

func TestSuccessfulDelivery(t *testing.T) {
	e := setup(t, nil)
	var mu sync.Mutex
	var gotBody []byte
	var gotHdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotHdr = b, r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ev := e.event(srv.URL)

	ok, failed, err := e.disp().DeliverDue(context.Background())
	if err != nil || ok != 1 || failed != 0 {
		t.Fatalf("got %d %d %v", ok, failed, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if string(gotBody) != string(ev.Payload) {
		t.Fatalf("body %q != payload %q", gotBody, ev.Payload)
	}
	if gotHdr.Get("Content-Type") != "application/json" || gotHdr.Get("User-Agent") != "CryptoPay-Webhook/1" ||
		gotHdr.Get(auth.HeaderEvent) != domain.EventPaid || gotHdr.Get(auth.HeaderDelivery) != ev.ID.String() {
		t.Fatalf("bad headers %v", gotHdr)
	}
	ts, err := strconv.ParseInt(gotHdr.Get(auth.HeaderTimestamp), 10, 64)
	if err != nil || ts != e.clock.Now().Unix() {
		t.Fatalf("timestamp %v %v", ts, err)
	}
	if err := auth.Verify(e.m.WebhookSecret, ts, gotBody, gotHdr.Get(auth.HeaderSignature), e.clock.Now(), auth.DefaultSignatureTolerance); err != nil {
		t.Fatal(err)
	}
	got := e.get(ev.ID)
	if got.Status != domain.WebhookDelivered || got.Attempts != 1 || got.LastStatusCode != 204 || got.DeliveredAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestBackoffSequenceAndExhaustion(t *testing.T) {
	s := domain.DefaultSettings()
	s.WebhookMaxAttempts = 14
	e := setup(t, &s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ev := e.event(srv.URL)
	d := e.disp()

	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second,
		960 * time.Second, 1920 * time.Second, 3840 * time.Second, 7680 * time.Second, 15360 * time.Second,
		6 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		_, failed, err := d.DeliverDue(context.Background())
		if err != nil || failed != 1 {
			t.Fatalf("attempt %d: failed=%d err=%v", i+1, failed, err)
		}
		got := e.get(ev.ID)
		if got.Status != domain.WebhookPending || got.Attempts != i+1 {
			t.Fatalf("attempt %d: %+v", i+1, got)
		}
		if diff := got.NextAttemptAt.Sub(e.clock.Now()); diff != w {
			t.Fatalf("attempt %d: next in %v, want %v", i+1, diff, w)
		}
		if got.LastStatusCode != 500 || got.LastResponse != "boom\n" || !strings.Contains(got.LastError, "500") {
			t.Fatalf("diagnostics %+v", got)
		}
		// Not due yet: nothing happens.
		if ok, f, _ := d.DeliverDue(context.Background()); ok+f != 0 {
			t.Fatal("delivered before due")
		}
		e.clock.Advance(w)
	}
	// 14th attempt exhausts.
	if _, failed, _ := d.DeliverDue(context.Background()); failed != 1 {
		t.Fatal("expected failure")
	}
	got := e.get(ev.ID)
	if got.Status != domain.WebhookFailed || got.Attempts != 14 {
		t.Fatalf("%+v", got)
	}
	e.clock.Advance(24 * time.Hour)
	if ok, f, _ := d.DeliverDue(context.Background()); ok+f != 0 {
		t.Fatal("failed event retried")
	}
}

func TestJitterBounds(t *testing.T) {
	e := setup(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	ev := e.event(srv.URL)
	d := webhook.New(e.st, nil, e.clock.Now, webhook.AllowPrivateTargets(true)) // default jitter
	if _, f, _ := d.DeliverDue(context.Background()); f != 1 {
		t.Fatal("no failure")
	}
	diff := e.get(ev.ID).NextAttemptAt.Sub(e.clock.Now())
	if diff < 30*time.Second || diff > 33*time.Second+time.Millisecond {
		t.Fatalf("jittered delay %v", diff)
	}
}

func TestTimeout(t *testing.T) {
	s := domain.DefaultSettings()
	s.WebhookTimeoutSeconds = 1
	e := setup(t, &s)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ev := e.event(srv.URL)
	start := time.Now()
	_, failed, _ := e.disp().DeliverDue(context.Background())
	if failed != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("failed=%d after %v", failed, time.Since(start))
	}
	got := e.get(ev.ID)
	if got.Attempts != 1 || !strings.Contains(got.LastError, "timeout") {
		t.Fatalf("%+v", got)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	e := setup(t, nil)
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	ev := e.event(srv.URL)
	_, failed, _ := e.disp().DeliverDue(context.Background())
	got := e.get(ev.ID)
	if failed != 1 || hits.Load() != 0 || got.LastStatusCode != 302 || got.Status != domain.WebhookPending {
		t.Fatalf("failed=%d hits=%d %+v", failed, hits.Load(), got)
	}
}

func TestLargeResponseTruncated(t *testing.T) {
	e := setup(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		io.WriteString(w, strings.Repeat("x", 1<<20))
	}))
	defer srv.Close()
	ev := e.event(srv.URL)
	e.disp().DeliverDue(context.Background())
	got := e.get(ev.ID)
	if len(got.LastResponse) != store.MaxWebhookResponseBytes {
		t.Fatalf("response len %d", len(got.LastResponse))
	}
}

func TestSSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	urls := []string{
		"http://127.0.0.1:" + port + "/",
		"http://10.1.2.3/hook",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]:" + port + "/",
		"http://[::ffff:127.0.0.1]:" + port + "/",
		"http://100.64.0.1/",
		"http://0.0.0.0:" + port + "/",
		"http://192.168.1.1/",
		"http://[fd00::1]/",
		"http://224.0.0.1/",
		"http://localhost:" + port + "/", // hostname resolving to loopback
		"ftp://example.com/",
	}
	for _, u := range urls {
		t.Run(u, func(t *testing.T) {
			e := setup(t, nil)
			ev := e.event(u)
			d := webhook.New(e.st, nil, e.clock.Now, webhook.WithJitter(0))
			_, failed, err := d.DeliverDue(context.Background())
			if err != nil || failed != 1 {
				t.Fatalf("failed=%d err=%v", failed, err)
			}
			got := e.get(ev.ID)
			if got.Attempts != 1 || got.LastError == "" || got.LastStatusCode != 0 {
				t.Fatalf("%+v", got)
			}
			if !strings.HasPrefix(u, "ftp") && !strings.Contains(got.LastError, "blocked") {
				t.Fatalf("error %q", got.LastError)
			}
		})
	}

	t.Run("guard off allows loopback", func(t *testing.T) {
		e := setup(t, nil)
		ev := e.event(srv.URL)
		ok, _, _ := e.disp().DeliverDue(context.Background())
		if ok != 1 || e.get(ev.ID).Status != domain.WebhookDelivered {
			t.Fatal("not delivered")
		}
	})

	t.Run("guard off allows localhost hostname", func(t *testing.T) {
		e := setup(t, nil)
		ev := e.event("http://localhost:" + port + "/")
		ok, _, _ := e.disp().DeliverDue(context.Background())
		if ok != 1 || e.get(ev.ID).Status != domain.WebhookDelivered {
			t.Fatal("not delivered")
		}
	})

	t.Run("resolution failure is not a guard verdict", func(t *testing.T) {
		e := setup(t, nil)
		ev := e.event("http://nx.example:" + port + "/")
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("no dns in tests")
		}}
		d := webhook.New(e.st, nil, e.clock.Now, webhook.WithJitter(0), webhook.WithResolver(r))
		_, failed, _ := d.DeliverDue(context.Background())
		got := e.get(ev.ID)
		if failed != 1 || strings.Contains(got.LastError, "blocked") {
			t.Fatalf("%+v", got)
		}
	})
}

func TestConcurrentDispatchersDeliverOnce(t *testing.T) {
	e := setup(t, nil)
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get(auth.HeaderDelivery)]++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}))
	defer srv.Close()
	const n = 40
	for range n {
		e.event(srv.URL)
	}
	var wg sync.WaitGroup
	var total atomic.Int32
	for range 4 {
		d := e.disp(webhook.WithConcurrency(4), webhook.WithBatchSize(5))
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := d.DeliverDue(context.Background())
			if err != nil {
				t.Error(err)
			}
			total.Add(int32(ok))
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n || total.Load() != n {
		t.Fatalf("seen %d delivered %d", len(seen), total.Load())
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("%s delivered %d times", id, c)
		}
	}
}

func TestRequeueAfterFailure(t *testing.T) {
	s := domain.DefaultSettings()
	s.WebhookMaxAttempts = 1
	e := setup(t, &s)
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	ev := e.event(srv.URL)
	d := e.disp()
	d.DeliverDue(context.Background())
	if got := e.get(ev.ID); got.Status != domain.WebhookFailed {
		t.Fatalf("%+v", got)
	}
	healthy.Store(true)
	if _, err := e.st.RequeueWebhook(context.Background(), ev.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	ok, _, _ := d.DeliverDue(context.Background())
	if got := e.get(ev.ID); ok != 1 || got.Status != domain.WebhookDelivered || got.Attempts != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestDisabledMerchantFailsPermanently(t *testing.T) {
	e := setup(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("must not be called") }))
	defer srv.Close()
	ev := e.event(srv.URL)
	st := domain.MerchantDisabled
	if _, err := e.st.UpdateMerchant(context.Background(), e.m.ID, store.MerchantUpdate{Status: &st}); err != nil {
		t.Fatal(err)
	}
	_, failed, _ := e.disp().DeliverDue(context.Background())
	got := e.get(ev.ID)
	if failed != 1 || got.Status != domain.WebhookFailed || got.Attempts != 1 || !strings.Contains(got.LastError, "disabled") {
		t.Fatalf("%+v", got)
	}
}

func TestRotatedSecretUsedAtSendTime(t *testing.T) {
	e := setup(t, nil)
	var sig, tsh string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig, tsh = r.Header.Get(auth.HeaderSignature), r.Header.Get(auth.HeaderTimestamp)
		body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	e.event(srv.URL)
	secret := auth.NewWebhookSecret()
	if _, err := e.st.RotateWebhookSecret(context.Background(), e.m.ID, secret); err != nil {
		t.Fatal(err)
	}
	e.disp().DeliverDue(context.Background())
	ts, _ := strconv.ParseInt(tsh, 10, 64)
	if err := auth.Verify(secret, ts, body, sig, time.Time{}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestRunStopsOnCancelAndDelivers(t *testing.T) {
	e := setup(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ev := e.event(srv.URL)
	d := e.disp(webhook.WithPollInterval(10 * time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.get(ev.ID).Status != domain.WebhookDelivered {
		if time.Now().After(deadline) {
			t.Fatal("not delivered by Run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil || time.Since(start) > 2*time.Second {
			t.Fatalf("err=%v after %v", err, time.Since(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunStopsWhileRequestInFlight(t *testing.T) {
	e := setup(t, nil)
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // lets the server notice a closed connection
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	ev := e.event(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.disp(webhook.WithPollInterval(10 * time.Millisecond)).Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop promptly")
	}
	if got := e.get(ev.ID); got.Attempts != 0 || got.Status != domain.WebhookPending {
		t.Fatalf("aborted attempt must not be recorded: %+v", got)
	}
}

type panicRT struct{}

func (panicRT) RoundTrip(*http.Request) (*http.Response, error) { panic("kaboom") }

func TestPanicRecovered(t *testing.T) {
	e := setup(t, nil)
	ev := e.event("http://example.com/")
	ev2 := e.event("http://example.com/2")
	d := e.disp(webhook.WithTransport(panicRT{}))
	ok, failed, err := d.DeliverDue(context.Background())
	if err != nil || ok != 0 || failed != 2 {
		t.Fatalf("%d %d %v", ok, failed, err)
	}
	for _, id := range []uuid.UUID{ev.ID, ev2.ID} {
		got := e.get(id)
		if got.Attempts != 1 || !strings.Contains(got.LastError, "panic") {
			t.Fatalf("%+v", got)
		}
	}
}

func TestBuildEnvelope(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 3, 4, 5, 6, 7, 800, time.FixedZone("x", 3600))
	raw, err := webhook.BuildEnvelope(id, domain.EventPaid, at, map[string]any{"id": "inv"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["id"] != id.String() || m["type"] != "invoice.paid" || m["created_at"] != "2026-03-04T04:06:07.0000008Z" {
		t.Fatalf("%s", raw)
	}
	if _, err := webhook.BuildEnvelope(id, "", at, nil); err == nil {
		t.Fatal("expected error")
	}
}
