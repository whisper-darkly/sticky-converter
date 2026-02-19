// Package overseer wraps github.com/whisper-darkly/sticky-overseer so the
// rest of sticky-refinery never imports that package directly.
package overseer

import (
	"database/sql"
	"net"
	"net/http"

	real "github.com/whisper-darkly/sticky-overseer"
)

// Re-export the types callers need.
type HubConfig = real.HubConfig
type Hub = real.Hub
type Hubber = real.Hubber

// Provider bundles all overseer entry points for dependency injection.
type Provider struct {
	OpenDB             func(path string) (*sql.DB, error)
	NewHub             func(cfg HubConfig) *Hub
	NewHandler         func(h Hubber, trustedNets []*net.IPNet) http.HandlerFunc
	ParseTrustedCIDRs  func(s string) ([]*net.IPNet, error)
	DetectLocalSubnets func() []*net.IPNet
}

// StubProvider returns a Provider wired to the real sticky-overseer package.
// The name "Stub" is kept for call-site compatibility; it always uses the real package.
func StubProvider() Provider {
	return Provider{
		OpenDB:             real.OpenDB,
		NewHub:             real.NewHub,
		NewHandler:         real.NewHandler,
		ParseTrustedCIDRs:  real.ParseTrustedCIDRs,
		DetectLocalSubnets: real.DetectLocalSubnets,
	}
}
