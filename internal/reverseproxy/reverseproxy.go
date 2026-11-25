// Package reverseproxy defines reverse-proxy provider specifications, defaults, and allowlist lookup.
package reverseproxy

import (
	"fmt"
	"maps"
	"slices"
)

// DACPolicy defines fallback ownership and POSIX permission bits applied to deployed proxy assets.
type DACPolicy struct {
	Owner     string
	DirMode   string
	FileMode  string
	Recursive bool
}

// Provider exposes engine-specific metadata and fallback asset policies for a reverse-proxy backend.
type Provider interface {
	Type() string
	SyntaxCheckArgs() []string
	DefaultConfDACPolicy(groupName string) DACPolicy
	DefaultCertDACPolicy(ownerName string) DACPolicy
}

var providers = map[string]Provider{
	TypeAngie: angieProvider{},
}

// Lookup returns the registered reverse-proxy provider for reverseProxyType or an error if unsupported.
func Lookup(reverseProxyType string) (Provider, error) {
	provider, ok := providers[reverseProxyType]
	if !ok {
		return nil, fmt.Errorf("unsupported reverse-proxy provider type %q", reverseProxyType)
	}
	return provider, nil
}

// IsSupported reports whether reverseProxyType matches a registered reverse-proxy provider.
func IsSupported(reverseProxyType string) bool {
	_, ok := providers[reverseProxyType]
	return ok
}

// SupportedTypes returns all registered reverse-proxy provider types in sorted order.
func SupportedTypes() []string {
	return slices.Sorted(maps.Keys(providers))
}
