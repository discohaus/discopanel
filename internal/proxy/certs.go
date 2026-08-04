package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"sort"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
)

// One parsed certificate ready for sni matching
type certEntry struct {
	names   []string
	expires time.Time
	pair    tls.Certificate
}

// Immutable snapshot of file loaded certificates
type certIndex struct {
	entries []certEntry
}

// Loads config file pem pairs into a matchable index
func LoadTLSCertificates(cfgs []config.TLSCertificate, log *logger.Logger) *certIndex {
	idx := &certIndex{}
	for _, c := range cfgs {
		if c.CertFile == "" || c.KeyFile == "" {
			log.Error("TLS certificate entry needs cert_file and key_file")
			continue
		}
		pair, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			log.Error("TLS certificate %s skipped: %v", c.CertFile, err)
			continue
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			log.Error("TLS certificate %s has no readable leaf: %v", c.CertFile, err)
			continue
		}
		pair.Leaf = leaf
		names := leafNames(leaf)
		if len(names) == 0 {
			log.Error("TLS certificate %s covers no names", c.CertFile)
			continue
		}
		idx.entries = append(idx.entries, certEntry{
			names:   names,
			expires: leaf.NotAfter,
			pair:    pair,
		})
		log.Info("Loaded TLS certificate for %s", strings.Join(names, ", "))
	}
	if len(idx.entries) == 0 {
		return nil
	}
	return idx
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
