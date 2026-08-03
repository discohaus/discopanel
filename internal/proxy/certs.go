package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Tls policy and material for one socket
type CertSource interface {
	// Best certificate for a client hello name
	MatchCertificate(serverName string) (*tls.Certificate, bool)
	// True when this panel terminates tls itself
	TerminatesTLS() bool
	// True when an upstream edge terminates ahead of us
	TrustsForwardedProto() bool
}

// One parsed certificate ready for sni matching
type certEntry struct {
	id      string
	names   []string
	expires time.Time
	issuer  string
	pair    tls.Certificate
}

// Immutable snapshot of loaded certificates
type certIndex struct {
	entries []certEntry
}

// Parses pem material into a matchable entry
func parseCertEntry(row *v1.Certificate) (certEntry, error) {
	pair, err := tls.X509KeyPair([]byte(row.CertPem), []byte(row.KeyPem))
	if err != nil {
		return certEntry{}, fmt.Errorf("invalid certificate pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return certEntry{}, fmt.Errorf("invalid leaf certificate: %w", err)
	}
	pair.Leaf = leaf
	names := leafNames(leaf)
	if len(names) == 0 {
		return certEntry{}, fmt.Errorf("certificate covers no names")
	}
	return certEntry{
		id:      row.Id,
		names:   names,
		expires: leaf.NotAfter,
		issuer:  leaf.Issuer.CommonName,
		pair:    pair,
	}, nil
}

// Names leaf cert answers for
func leafNames(leaf *x509.Certificate) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range leaf.DNSNames {
		add(name)
	}
	if len(out) == 0 && leaf.Subject.CommonName != "" {
		add(leaf.Subject.CommonName)
	}
	sort.Strings(out)
	return out
}

// Check san pattern coverage for hostname
func certNameMatches(pattern, hostname string) bool {
	if pattern == hostname {
		return true
	}
	// Wildcards cover exactly one leading label
	suffix, ok := strings.CutPrefix(pattern, "*.")
	if !ok {
		return false
	}
	head, tail, found := strings.Cut(hostname, ".")
	return found && head != "" && tail == suffix
}

// Best unexpired certificate for a server name
func (idx *certIndex) match(serverName string) (*tls.Certificate, bool) {
	if idx == nil {
		return nil, false
	}
	name := NormalizeHostname(strings.TrimSuffix(serverName, "."))
	if name == "" {
		return nil, false
	}
	now := time.Now()
	var best *certEntry
	bestScore := -1
	for i := range idx.entries {
		entry := &idx.entries[i]
		if now.After(entry.expires) {
			continue
		}
		for _, pattern := range entry.names {
			if !certNameMatches(pattern, name) {
				continue
			}
			// Exact names beat wildcards, longer beats shorter
			score := len(pattern) * 2
			if !strings.HasPrefix(pattern, "*.") {
				score++
			}
			if score > bestScore {
				bestScore = score
				best = entry
			}
		}
	}
	if best == nil {
		return nil, false
	}
	return &best.pair, true
}

// Best certificate for a client hello name
func (m *Manager) MatchCertificate(serverName string) (*tls.Certificate, bool) {
	return m.certIdx.Load().match(serverName)
}

// Https settings snapshot read on every accept
type tlsPolicy struct {
	enabled  bool
	provider v1.TlsProvider
}

// Replaces the policy lane dispatch reads
func (m *Manager) setTLSPolicy(enabled bool, provider v1.TlsProvider) {
	m.policy.Store(&tlsPolicy{enabled: enabled, provider: provider})
}

// True when the panel holds certificates and owns termination
func (m *Manager) TerminatesTLS() bool {
	idx := m.certIdx.Load()
	if idx == nil || len(idx.entries) == 0 {
		return false
	}
	p := m.policy.Load()
	return p != nil && p.enabled && p.provider == v1.TlsProvider_TLS_PROVIDER_DISCOPANEL
}

// True when an edge terminates and asserts the scheme
func (m *Manager) TrustsForwardedProto() bool {
	p := m.policy.Load()
	return p != nil && p.enabled && p.provider == v1.TlsProvider_TLS_PROVIDER_DNS
}

// Reloads certificate rows into the matching index
func (m *Manager) ReloadCertificates(ctx context.Context) error {
	rows, err := m.store.ListCertificates(ctx)
	if err != nil {
		return fmt.Errorf("failed to list certificates: %w", err)
	}
	idx := &certIndex{}
	for _, row := range rows {
		entry, err := parseCertEntry(row)
		if err != nil {
			m.logger.Error("Certificate %s skipped: %v", row.Id, err)
			continue
		}
		idx.entries = append(idx.entries, entry)
	}
	m.certIdx.Store(idx)
	return nil
}

// Fills derived coverage fields and drops the key
func FillCertificateDerived(row *v1.Certificate) {
	if entry, err := parseCertEntry(row); err == nil {
		row.CoveredNames = entry.names
		row.ExpiresAt = timestamppb.New(entry.expires)
		row.Issuer = entry.issuer
	}
	row.KeyPem = ""
}

// Validates an uploaded pair before storage
func ValidateCertificatePair(certPEM, keyPEM string) error {
	entry, err := parseCertEntry(&v1.Certificate{CertPem: certPEM, KeyPem: keyPEM})
	if err != nil {
		return err
	}
	if time.Now().After(entry.expires) {
		return fmt.Errorf("certificate expired on %s", entry.expires.Format("2006-01-02"))
	}
	return nil
}
