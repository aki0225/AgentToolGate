package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const defaultRedirectLimit = 10

var (
	ErrTargetNotAllowed         = errors.New("network target is not allowed")
	ErrRedirectTargetNotAllowed = errors.New("network redirect target is not allowed")
	ErrRedirectLimitExceeded    = errors.New("network redirect limit exceeded")
)

var alwaysBlockedMetadataAddresses = map[netip.Addr]struct{}{
	netip.MustParseAddr("100.100.100.200"): {},
	netip.MustParseAddr("169.254.169.254"): {},
	netip.MustParseAddr("fd00:ec2::254"):   {},
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type ClientOptions struct {
	Timeout            time.Duration
	AllowedAuthorities []string
	Resolver           Resolver
	DialContext        func(ctx context.Context, network, address string) (net.Conn, error)
	MaxRedirects       int
}

type Transport struct {
	base                  *http.Transport
	resolver              Resolver
	allowedAuthorities    map[string]struct{}
	underlyingDialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

type dialTarget struct {
	hostname  string
	port      string
	addresses []netip.Addr
}

type dialTargetContextKey struct{}

func NewClient(options ClientOptions) *http.Client {
	redirectLimit := options.MaxRedirects
	if redirectLimit <= 0 {
		redirectLimit = defaultRedirectLimit
	}
	return &http.Client{
		Timeout:   options.Timeout,
		Transport: NewTransport(options),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= redirectLimit {
				return ErrRedirectLimitExceeded
			}
			if len(via) == 0 || !sameOrigin(via[0].URL, req.URL) {
				return ErrRedirectTargetNotAllowed
			}
			return nil
		},
	}
}

func NewTransport(options ClientOptions) *Transport {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialContext := options.DialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{}).DialContext
	}

	transport := &Transport{
		base:                  cloneDefaultTransportWithoutProxy(),
		resolver:              resolver,
		allowedAuthorities:    normalizedAuthoritySet(options.AllowedAuthorities),
		underlyingDialContext: dialContext,
	}
	transport.base.DialContext = transport.dialContext
	return transport
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("%w: request URL is required", ErrTargetNotAllowed)
	}

	target, err := t.resolveTarget(req.Context(), req)
	if err != nil {
		if errors.Is(err, ErrTargetNotAllowed) && req.Response != nil {
			return nil, fmt.Errorf("%w: %v", ErrRedirectTargetNotAllowed, err)
		}
		return nil, err
	}

	ctx := context.WithValue(req.Context(), dialTargetContextKey{}, target)
	return t.base.RoundTrip(req.Clone(ctx))
}

func (t *Transport) resolveTarget(ctx context.Context, req *http.Request) (dialTarget, error) {
	scheme := strings.ToLower(strings.TrimSpace(req.URL.Scheme))
	if scheme != "http" && scheme != "https" {
		return dialTarget{}, fmt.Errorf("%w: unsupported URL scheme", ErrTargetNotAllowed)
	}

	authority := normalizeAuthority(req.URL.Host)
	if authority == "" {
		return dialTarget{}, fmt.Errorf("%w: URL authority is required", ErrTargetNotAllowed)
	}
	if _, ok := t.allowedAuthorities[authority]; !ok {
		return dialTarget{}, fmt.Errorf("%w: URL authority is not allowlisted", ErrTargetNotAllowed)
	}

	hostname := strings.ToLower(strings.TrimSpace(req.URL.Hostname()))
	if hostname == "" {
		return dialTarget{}, fmt.Errorf("%w: URL hostname is required", ErrTargetNotAllowed)
	}
	port := req.URL.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	addresses, err := t.lookupAddresses(ctx, hostname)
	if err != nil {
		return dialTarget{}, fmt.Errorf("resolve guarded network target: %w", err)
	}
	if len(addresses) == 0 {
		return dialTarget{}, errors.New("resolve guarded network target: no addresses")
	}

	validated := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if err := validateResolvedAddress(hostname, address); err != nil {
			return dialTarget{}, err
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		validated = append(validated, address)
	}

	return dialTarget{
		hostname:  hostname,
		port:      port,
		addresses: validated,
	}, nil
}

func (t *Transport) lookupAddresses(ctx context.Context, hostname string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(hostname); err == nil {
		return []netip.Addr{address}, nil
	}
	return t.resolver.LookupNetIP(ctx, "ip", hostname)
}

func (t *Transport) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	target, ok := ctx.Value(dialTargetContextKey{}).(dialTarget)
	if !ok {
		return nil, errors.New("guarded network dial target is missing")
	}

	hostname, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse guarded network dial address: %w", err)
	}
	hostname = strings.ToLower(strings.Trim(hostname, "[]"))
	if hostname != target.hostname || port != target.port {
		return nil, errors.New("guarded network dial target changed after validation")
	}

	var lastErr error
	for _, resolvedAddress := range target.addresses {
		if strings.HasSuffix(network, "4") && !resolvedAddress.Is4() {
			continue
		}
		if strings.HasSuffix(network, "6") && !resolvedAddress.Is6() {
			continue
		}
		fixedAddress := net.JoinHostPort(resolvedAddress.String(), target.port)
		conn, err := t.underlyingDialContext(ctx, network, fixedAddress)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("guarded network target has no address for requested network")
}

func validateResolvedAddress(hostname string, address netip.Addr) error {
	if !address.IsValid() || address.Zone() != "" {
		return fmt.Errorf("%w: invalid resolved address", ErrTargetNotAllowed)
	}
	if address.IsUnspecified() || address.IsMulticast() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: non-routable or link-local address", ErrTargetNotAllowed)
	}
	if _, blocked := alwaysBlockedMetadataAddresses[address]; blocked {
		return fmt.Errorf("%w: metadata address", ErrTargetNotAllowed)
	}
	if address.IsLoopback() {
		if !isLocalDemoHostname(hostname) {
			return fmt.Errorf("%w: hostname resolved to loopback", ErrTargetNotAllowed)
		}
		return nil
	}
	if isLocalDemoHostname(hostname) {
		return fmt.Errorf("%w: local demo hostname resolved outside loopback", ErrTargetNotAllowed)
	}
	if !address.IsGlobalUnicast() {
		return fmt.Errorf("%w: non-unicast address", ErrTargetNotAllowed)
	}
	return nil
}

func isLocalDemoHostname(hostname string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(hostname), "[]"))
	if normalized == "localhost" {
		return true
	}
	address, err := netip.ParseAddr(normalized)
	if err != nil {
		return false
	}
	address = address.Unmap()
	return address == netip.MustParseAddr("127.0.0.1") || address == netip.IPv6Loopback()
}

func normalizedAuthoritySet(values []string) map[string]struct{} {
	normalized := make(map[string]struct{}, len(values))
	for _, value := range values {
		if authority := normalizeAuthority(value); authority != "" {
			normalized[authority] = struct{}{}
		}
	}
	return normalized
}

func normalizeAuthority(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sameOrigin(base, candidate *url.URL) bool {
	if base == nil || candidate == nil || base.User != nil || candidate.User != nil {
		return false
	}

	baseScheme := strings.ToLower(strings.TrimSpace(base.Scheme))
	candidateScheme := strings.ToLower(strings.TrimSpace(candidate.Scheme))
	if baseScheme != candidateScheme || (baseScheme != "http" && baseScheme != "https") {
		return false
	}

	return normalizeOriginAuthority(base) == normalizeOriginAuthority(candidate)
}

func normalizeOriginAuthority(value *url.URL) string {
	if value == nil {
		return ""
	}

	hostname := strings.ToLower(strings.TrimSpace(value.Hostname()))
	if hostname == "" {
		return ""
	}
	port := value.Port()
	if port == "" {
		switch strings.ToLower(strings.TrimSpace(value.Scheme)) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ""
		}
	}
	return net.JoinHostPort(hostname, port)
}

func cloneDefaultTransportWithoutProxy() *http.Transport {
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := defaultTransport.Clone()
		cloned.Proxy = nil
		cloned.DialTLSContext = nil
		cloned.DialTLS = nil
		return cloned
	}
	return &http.Transport{Proxy: nil}
}
