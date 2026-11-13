package github

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/readout"
)

// waitingLine is the notice a reader writes when it starts waiting.
const waitingLine = "lookout: waiting for the network before reading github.com"

// emptySearch answers every query as a search that matched nothing.
func emptySearch(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(`{"data":{"search":{"issueCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)); err != nil {
		panic(err)
	}
}

// closedAddress is a loopback address nothing listens on, so a connection to
// it is refused. Another process could take the port in between, which would
// fail the spec rather than pass it.
func closedAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// refusal is the error a dial to a closed port gives, as the transport
// returns it.
func refusal(t *testing.T) error {
	t.Helper()
	conn, err := net.Dial("tcp", closedAddress(t))
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		t.Fatal("a closed port accepted a connection")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial error = %v, want a refusal", err)
	}
	return err
}

// testReader is a reader holding a token, sending queries to api, waiting
// waits before retrying, and writing its notices to the returned buffer.
func testReader(api string, waits ...time.Duration) (*Reader, *bytes.Buffer) {
	var notice bytes.Buffer
	r := NewReader(func(string) string { return "" }, api, &notice, waits)
	r.asked, r.token = true, "fixture-token-not-a-secret"
	return r, &notice
}

// transport is the reader's own transport, for a spec to change how it dials.
func (r *Reader) transport(t *testing.T) *http.Transport {
	t.Helper()
	tr, ok := r.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want the reader's own *http.Transport", r.client.Transport)
	}
	return tr
}

// read reads one github input over one owner's searches.
func read(ctx context.Context, r *Reader) readout.Reading {
	now := time.Now()
	in := config.Input{Name: "hub", Kind: config.KindGitHub}
	sc := readout.Scope{Owners: []config.Owner{{Host: host, Name: "owner"}}, Since: now.Add(-time.Hour)}
	return r.Read(ctx, in, sc, now)
}

// flaky fails the attempts fail says to with err, after delay, and sends the
// rest through next.
type flaky struct {
	next     http.RoundTripper
	fail     func(attempt int) bool
	err      error
	delay    time.Duration
	attempts atomic.Int32
}

func (f *flaky) RoundTrip(req *http.Request) (*http.Response, error) {
	n := int(f.attempts.Add(1))
	if f.fail(n) {
		time.Sleep(f.delay)
		if req.Body != nil {
			if err := req.Body.Close(); err != nil {
				return nil, err
			}
		}
		return nil, f.err
	}
	return f.next.RoundTrip(req)
}

// connectProxy routes the reader through a proxy that answers every CONNECT
// with status, as one that cannot or will not open a tunnel does.
func connectProxy(status int) func(*testing.T, *Reader) {
	return func(t *testing.T, r *Reader) {
		t.Helper()
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodConnect {
				t.Errorf("proxy got %s, want CONNECT", req.Method)
			}
			w.WriteHeader(status)
		}))
		t.Cleanup(proxy.Close)
		r.transport(t).Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.Listener.Addr().String()})
	}
}

func wantHealth(t *testing.T, got readout.Reading, status, detail string) {
	t.Helper()
	h := got.Health
	if string(h.Status) != status || !slices.Equal(h.Reasons, []string{"github_failed"}) {
		t.Errorf("health = %s %v, want %s [github_failed] (details %q)", h.Status, h.Reasons, status, h.Details)
	}
	if !slices.Contains(h.Details, detail) {
		t.Errorf("details = %q, want %q", h.Details, detail)
	}
}

func TestAReadRecoversWhenTheNetworkComesUpWhileItWaits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(emptySearch))
	t.Cleanup(srv.Close)
	r, notice := testReader(srv.URL, time.Millisecond, time.Millisecond, time.Millisecond)
	fl := &flaky{next: r.client.Transport, fail: func(n int) bool { return n <= 2 }, err: refusal(t)}
	r.client.Transport = fl

	got := read(t.Context(), r)
	if got.Health.Status != "ok" {
		t.Errorf("health = %s %v %q, want ok", got.Health.Status, got.Health.Reasons, got.Health.Details)
	}
	if notice.String() != waitingLine+"\n" {
		t.Errorf("notice = %q, want the waiting line once", notice.String())
	}
}

func TestAReadSaysWhatFailedOnceTheWaitsAreSpent(t *testing.T) {
	r, notice := testReader("http://"+closedAddress(t)+"/graphql", time.Millisecond, 2*time.Millisecond)
	start := time.Now()
	got := read(t.Context(), r)
	wantHealth(t, got, "error", "github.com refused the connection")
	if notice.String() != waitingLine+"\n" {
		t.Errorf("notice = %q, want the waiting line once across every request", notice.String())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("read took %s, past what its waits allow", elapsed)
	}
}

func TestAFailureAfterGitHubHasAnsweredIsNotWaitedOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(emptySearch))
	t.Cleanup(srv.Close)
	r, notice := testReader(srv.URL, time.Hour)
	r.client.Transport = &flaky{next: r.client.Transport, fail: func(n int) bool { return n > 1 }, err: refusal(t)}

	// A reader that waited would be interrupted here rather than hold the
	// spec for its hour.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got := read(ctx, r)
	wantHealth(t, got, "degraded", "github.com refused the connection")
	if notice.Len() != 0 {
		t.Errorf("notice = %q, want none", notice.String())
	}
}

func TestTimeSpentInAttemptsCountsAgainstTheWaits(t *testing.T) {
	waits := slices.Repeat([]time.Duration{10 * time.Millisecond}, 10)
	r, _ := testReader("http://"+closedAddress(t)+"/graphql", waits...)
	fl := &flaky{next: r.client.Transport, fail: func(int) bool { return true }, err: refusal(t), delay: 50 * time.Millisecond}
	r.client.Transport = fl

	got := read(t.Context(), r)
	wantHealth(t, got, "error", "github.com refused the connection")
	// The waits sum to 100ms. Attempts of 50ms each spend that deadline in a
	// few tries, then each later request is tried once; counting only the
	// pauses would try the first request once per wait.
	if n := fl.attempts.Load(); n >= int32(len(waits)) {
		t.Errorf("attempts = %d, want the deadline to end the retries well before every wait is taken", n)
	}
}

func TestAnInterruptDuringAWaitReportsTheInterrupt(t *testing.T) {
	// A refused dial to loopback fails at once, so by the time the interrupt
	// lands the reader is in its hour-long wait.
	r, _ := testReader("http://"+closedAddress(t)+"/graphql", time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	got := read(ctx, r)
	wantHealth(t, got, "error", "github.com: interrupted")
}

func TestAReadNamesTheKindOfFailure(t *testing.T) {
	// The stalling handlers hold a request well past the client's timeout,
	// but not forever: the server's Close waits for every handler to return,
	// and a client that gives up does not always cancel the handler's
	// context.
	hold := func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	stall := func(_ http.ResponseWriter, r *http.Request) { hold(r) }
	partial := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"data":`)); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		hold(r)
	}
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		tls     bool
		setup   func(t *testing.T, r *Reader)
		api     string
		detail  string
		waits   bool
	}{
		"name not resolved": {
			api: "http://lookout-spec.invalid/graphql",
			setup: func(t *testing.T, r *Reader) {
				resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, network, closedAddress(t))
				}}
				r.transport(t).DialContext = (&net.Dialer{Resolver: resolver}).DialContext
			},
			detail: "github.com: name not resolved", waits: true,
		},
		"no connection in time": {
			api: "http://" + "127.0.0.1:1" + "/graphql",
			setup: func(t *testing.T, r *Reader) {
				r.transport(t).DialContext = (&net.Dialer{Timeout: time.Nanosecond}).DialContext
			},
			detail: "github.com: no connection within " + dialTimeout.String(), waits: true,
		},
		"proxy not reachable": {
			setup: func(t *testing.T, r *Reader) {
				r.transport(t).Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: closedAddress(t)})
			},
			handler: emptySearch,
			detail:  "the proxy to github.com could not be reached", waits: true,
		},
		"proxy not connected in time": {
			setup: func(t *testing.T, r *Reader) {
				tr := r.transport(t)
				tr.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: closedAddress(t)})
				tr.DialContext = (&net.Dialer{Timeout: time.Nanosecond}).DialContext
			},
			handler: emptySearch,
			detail:  "the proxy to github.com could not be reached", waits: true,
		},
		"proxy cannot reach github.com yet": {
			api:    "https://lookout-spec.invalid/graphql",
			setup:  connectProxy(http.StatusBadGateway),
			detail: "the proxy to github.com answered 502", waits: true,
		},
		"proxy refuses the tunnel": {
			api:    "https://lookout-spec.invalid/graphql",
			setup:  connectProxy(http.StatusProxyAuthRequired),
			detail: "the proxy to github.com answered 407",
		},
		"proxy's certificate not trusted": {
			setup: func(t *testing.T, r *Reader) {
				proxy := httptest.NewTLSServer(http.HandlerFunc(emptySearch))
				t.Cleanup(proxy.Close)
				r.transport(t).Proxy = http.ProxyURL(&url.URL{Scheme: "https", Host: proxy.Listener.Addr().String()})
			},
			handler: emptySearch,
			detail:  "the proxy to github.com: TLS failed",
		},
		"certificate not trusted": {
			handler: emptySearch, tls: true,
			detail: "github.com: TLS failed",
		},
		"no answer in time": {
			handler: stall,
			setup:   func(_ *testing.T, r *Reader) { r.client.Timeout = 50 * time.Millisecond },
			detail:  "github.com: no answer within 50ms",
		},
		"response not finished in time": {
			handler: partial,
			setup:   func(_ *testing.T, r *Reader) { r.client.Timeout = 50 * time.Millisecond },
			detail:  "github.com: response not finished within 50ms",
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := tc.api
			if tc.handler != nil {
				srv := httptest.NewUnstartedServer(tc.handler)
				if tc.tls {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				t.Cleanup(srv.Close)
				api = srv.URL + "/graphql"
			}
			r, notice := testReader(api, time.Millisecond)
			if tc.setup != nil {
				tc.setup(t, r)
			}
			got := read(t.Context(), r)
			wantHealth(t, got, "error", tc.detail)
			if waited := notice.Len() > 0; waited != tc.waits {
				t.Errorf("waited = %v, want %v", waited, tc.waits)
			}
		})
	}
}

// TestFailureKindsAreCheckedInOrder pins which kind wins where an error
// carries several, and names the kinds no spec above can produce on demand,
// in the shapes the standard library builds them. The handshake timeout is
// stood in for by its text, the only part of it lookout can read, and the
// last case by any error lookout does not recognize.
func TestFailureKindsAreCheckedInOrder(t *testing.T) {
	dial := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://api.github.com/graphql", Err: &net.OpError{Op: "dial", Net: "tcp", Err: err}}
	}
	errno := func(e syscall.Errno) error { return dial(os.NewSyscallError("connect", e)) }
	for name, tc := range map[string]struct {
		err      error
		detail   string
		notReady bool
	}{
		"network unreachable": {errno(syscall.ENETUNREACH), "github.com: no route to it", true},
		"host unreachable":    {errno(syscall.EHOSTUNREACH), "github.com: no route to it", true},
		"network down":        {errno(syscall.ENETDOWN), "github.com: no route to it", true},
		"host down":           {errno(syscall.EHOSTDOWN), "github.com: no route to it", true},
		"no source address":   {errno(syscall.EADDRNOTAVAIL), "github.com: no route to it", true},
		"resolver timed out":  {dial(&net.DNSError{Err: "i/o timeout", Name: "api.github.com", IsTimeout: true}), "github.com: name not resolved", true},
		"TLS alert from github.com": {
			&url.Error{Op: "Post", URL: "https://api.github.com/graphql", Err: &net.OpError{Op: "remote error", Err: errors.New("tls: handshake failure")}},
			"github.com: TLS failed", false,
		},
		"TLS handshake too slow": {
			&url.Error{Op: "Post", URL: "https://api.github.com/graphql", Err: errors.New("net/http: TLS handshake timeout")},
			"github.com: TLS failed", false,
		},
		"something else": {errors.New("unexpected"), "github.com could not be reached", false},
	} {
		t.Run(name, func(t *testing.T) {
			f, notReady := transportFailure(t.Context(), tc.err, httpTimeout)
			if f.detail != tc.detail || notReady != tc.notReady {
				t.Errorf("got %q, not ready %v; want %q, not ready %v", f.detail, notReady, tc.detail, tc.notReady)
			}
		})
	}
}
