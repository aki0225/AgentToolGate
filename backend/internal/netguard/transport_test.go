package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportRejectsMetadataAndLinkLocalAfterResolution(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		addresses []string
	}{
		{name: "IPv4 metadata", addresses: []string{"169.254.169.254"}},
		{name: "IPv4 link-local", addresses: []string{"169.254.10.20"}},
		{name: "IPv6 link-local", addresses: []string{"fe80::1"}},
		{name: "AWS IPv6 metadata", addresses: []string{"fd00:ec2::254"}},
		{name: "Alibaba metadata", addresses: []string{"100.100.100.200"}},
		{name: "mixed private and metadata", addresses: []string{"10.20.30.40", "169.254.169.254"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var dialCount atomic.Int32
			addresses := make([]netip.Addr, 0, len(tc.addresses))
			for _, rawAddress := range tc.addresses {
				addresses = append(addresses, netip.MustParseAddr(rawAddress))
			}
			client := NewClient(ClientOptions{
				AllowedAuthorities: []string{"blocked.test:8080"},
				Resolver: staticResolver{
					"blocked.test": addresses,
				},
				DialContext: func(context.Context, string, string) (net.Conn, error) {
					dialCount.Add(1)
					return nil, errors.New("dial must not run")
				},
			})

			_, err := client.Get("http://blocked.test:8080/resource")
			if !errors.Is(err, ErrTargetNotAllowed) {
				t.Fatalf("expected guarded target rejection, got %v", err)
			}
			if got := dialCount.Load(); got != 0 {
				t.Fatalf("unsafe resolved target must receive zero dials, got %d", got)
			}
		})
	}
}

func TestTransportRejectsLiteralMetadataEvenWhenAllowlisted(t *testing.T) {
	t.Parallel()

	var dialCount atomic.Int32
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{"169.254.169.254"},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCount.Add(1)
			return nil, errors.New("dial must not run")
		},
	})

	_, err := client.Get("http://169.254.169.254/latest/meta-data")
	if !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("allowlisted metadata literal must still be rejected, got %v", err)
	}
	if got := dialCount.Load(); got != 0 {
		t.Fatalf("metadata literal must receive zero dials, got %d", got)
	}
}

func TestTransportRejectsHostnameResolvingToLoopback(t *testing.T) {
	t.Parallel()

	var dialCount atomic.Int32
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{"service.test:8080"},
		Resolver: staticResolver{
			"service.test": {netip.MustParseAddr("127.0.0.1")},
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCount.Add(1)
			return nil, errors.New("dial must not run")
		},
	})

	_, err := client.Get("http://service.test:8080/resource")
	if !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("expected loopback alias rejection, got %v", err)
	}
	if got := dialCount.Load(); got != 0 {
		t.Fatalf("loopback alias must receive zero dials, got %d", got)
	}
}

func TestTransportAllowsOnlyCompleteAuthorityForLocalDemo(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split server authority: %v", err)
	}
	authority := net.JoinHostPort("localhost", port)
	target := "http://" + authority + "/demo"
	resolver := staticResolver{
		"localhost": {netip.MustParseAddr("127.0.0.1")},
	}

	allowedClient := NewClient(ClientOptions{
		AllowedAuthorities: []string{authority},
		Resolver:           resolver,
		Timeout:            time.Second,
	})
	resp, err := allowedClient.Get(target)
	if err != nil {
		t.Fatalf("explicit local demo authority should be allowed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected local demo response, got %d", resp.StatusCode)
	}

	incompleteClient := NewClient(ClientOptions{
		AllowedAuthorities: []string{"localhost"},
		Resolver:           resolver,
		Timeout:            time.Second,
	})
	if _, err := incompleteClient.Get(target); !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("incomplete localhost authority must not authorize another port, got %v", err)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("only the complete authority request should reach the server, got %d", got)
	}
}

func TestTransportAllowsExplicitLiteralLoopbackAuthorities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		authority string
		address   string
	}{
		{name: "IPv4 loopback", authority: "127.0.0.1:18080", address: "127.0.0.1:18080"},
		{name: "IPv6 loopback", authority: "[::1]:18080", address: "[::1]:18080"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := &dialRecorder{err: errors.New("synthetic dial stop")}
			client := NewClient(ClientOptions{
				AllowedAuthorities: []string{tc.authority},
				DialContext:        recorder.DialContext,
			})
			_, err := client.Get("http://" + tc.authority + "/demo")
			if err == nil || errors.Is(err, ErrTargetNotAllowed) {
				t.Fatalf("explicit loopback authority should reach fixed dialing, got %v", err)
			}
			addresses := recorder.addresses()
			if len(addresses) != 1 || addresses[0] != tc.address {
				t.Fatalf("loopback dial address = %v, want %q", addresses, tc.address)
			}
		})
	}
}

func TestTransportRejectsOtherLoopbackLiteralsEvenWhenAllowlisted(t *testing.T) {
	t.Parallel()

	var dialCount atomic.Int32
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{"127.0.0.2:18080"},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCount.Add(1)
			return nil, errors.New("dial must not run")
		},
	})

	_, err := client.Get("http://127.0.0.2:18080/demo")
	if !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("only 127.0.0.1 may use the IPv4 local demo exception, got %v", err)
	}
	if got := dialCount.Load(); got != 0 {
		t.Fatalf("other loopback literals must receive zero dials, got %d", got)
	}
}

func TestTransportPinsDialToValidatedPrivateAddress(t *testing.T) {
	t.Parallel()

	recorder := &dialRecorder{err: errors.New("synthetic dial stop")}
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{"internal.test:8443"},
		Resolver: staticResolver{
			"internal.test": {
				netip.MustParseAddr("10.20.30.40"),
				netip.MustParseAddr("10.20.30.41"),
			},
		},
		DialContext: recorder.DialContext,
	})

	_, err := client.Get("https://internal.test:8443/status")
	if err == nil || errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("explicitly allowlisted private authority should reach fixed dialing, got %v", err)
	}
	for _, address := range recorder.addresses() {
		if strings.Contains(address, "internal.test") {
			t.Fatalf("dial must use a validated IP instead of resolving the hostname again: %v", recorder.addresses())
		}
		if address != "10.20.30.40:8443" && address != "10.20.30.41:8443" {
			t.Fatalf("dial used an address outside the validated set: %v", recorder.addresses())
		}
	}
	if len(recorder.addresses()) == 0 {
		t.Fatal("expected at least one fixed-IP dial")
	}
}

func TestTransportRevalidatesDNSAcrossRequests(t *testing.T) {
	t.Parallel()

	resolver := &sequenceResolver{
		host: "rebind.test",
		results: [][]netip.Addr{
			{netip.MustParseAddr("10.20.30.40")},
			{netip.MustParseAddr("169.254.169.254")},
		},
	}
	recorder := &dialRecorder{err: errors.New("synthetic dial stop")}
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{"rebind.test:8080"},
		Resolver:           resolver,
		DialContext:        recorder.DialContext,
	})

	if _, err := client.Get("http://rebind.test:8080/first"); err == nil || errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("first explicitly allowlisted private resolution should reach fixed dialing, got %v", err)
	}
	firstDialCount := len(recorder.addresses())
	if firstDialCount == 0 {
		t.Fatal("expected first request to dial the validated private address")
	}

	if _, err := client.Get("http://rebind.test:8080/second"); !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("rebound metadata address must be rejected, got %v", err)
	}
	if got := len(recorder.addresses()); got != firstDialCount {
		t.Fatalf("rebound metadata address must not be dialed, before=%d after=%d", firstDialCount, got)
	}
}

func TestTransportRevalidatesRedirectResolution(t *testing.T) {
	t.Parallel()

	var sourceCount atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCount.Add(1)
		http.Redirect(w, r, "http://redirect.test:8080/metadata", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	parsed, err := url.Parse(source.URL)
	if err != nil {
		t.Fatalf("parse source URL: %v", err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split source authority: %v", err)
	}
	sourceAuthority := net.JoinHostPort("localhost", port)
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{sourceAuthority, "redirect.test:8080"},
		Resolver: staticResolver{
			"localhost":     {netip.MustParseAddr("127.0.0.1")},
			"redirect.test": {netip.MustParseAddr("169.254.169.254")},
		},
		Timeout: time.Second,
	})

	_, err = client.Get("http://" + sourceAuthority + "/start")
	if !errors.Is(err, ErrRedirectTargetNotAllowed) {
		t.Fatalf("expected redirect target revalidation failure, got %v", err)
	}
	if got := sourceCount.Load(); got != 1 {
		t.Fatalf("expected only the initial redirect endpoint request, got %d", got)
	}
}

func TestTransportRejectsCrossOriginRedirectEvenWhenBothAuthoritiesAreAllowlisted(t *testing.T) {
	t.Parallel()

	var targetCount atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	var sourceCount atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCount.Add(1)
		http.Redirect(w, r, target.URL+"/final", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	sourceURL, err := url.Parse(source.URL)
	if err != nil {
		t.Fatalf("parse source URL: %v", err)
	}
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{sourceURL.Host, targetURL.Host},
		Timeout:            time.Second,
	})

	_, err = client.Get(source.URL + "/start")
	if !errors.Is(err, ErrRedirectTargetNotAllowed) {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	if got := sourceCount.Load(); got != 1 {
		t.Fatalf("expected source endpoint to be called once, got %d", got)
	}
	if got := targetCount.Load(); got != 0 {
		t.Fatalf("cross-origin redirect target must not be called, got %d", got)
	}
}

func TestTransportAllowsSameOriginRedirect(t *testing.T) {
	t.Parallel()

	var finalCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		if r.URL.Path == "/final" {
			finalCount.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client := NewClient(ClientOptions{
		AllowedAuthorities: []string{parsed.Host},
		Timeout:            time.Second,
	})

	resp, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("same-origin redirect should be allowed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected final response status %d, got %d", http.StatusNoContent, resp.StatusCode)
	}
	if got := finalCount.Load(); got != 1 {
		t.Fatalf("expected same-origin final endpoint once, got %d", got)
	}
}

func TestTransportDisablesEnvironmentProxy(t *testing.T) {
	t.Parallel()

	transport := NewTransport(ClientOptions{
		AllowedAuthorities: []string{"example.test"},
		Resolver: staticResolver{
			"example.test": {netip.MustParseAddr("203.0.113.10")},
		},
	})
	if transport.base.Proxy != nil {
		t.Fatal("guarded transport must ignore environment proxies")
	}
	if transport.base.DialTLSContext != nil || transport.base.DialTLS != nil {
		t.Fatal("HTTPS must use the guarded DialContext instead of a separate TLS dialer")
	}
}

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	values, ok := r[strings.ToLower(host)]
	if !ok {
		return nil, errors.New("unexpected resolver host")
	}
	return append([]netip.Addr(nil), values...), nil
}

type dialRecorder struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *dialRecorder) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	r.mu.Lock()
	r.calls = append(r.calls, address)
	r.mu.Unlock()
	return nil, r.err
}

func (r *dialRecorder) addresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type sequenceResolver struct {
	mu      sync.Mutex
	host    string
	results [][]netip.Addr
}

func (r *sequenceResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !strings.EqualFold(host, r.host) || len(r.results) == 0 {
		return nil, errors.New("unexpected resolver lookup")
	}
	result := append([]netip.Addr(nil), r.results[0]...)
	r.results = r.results[1:]
	return result, nil
}
