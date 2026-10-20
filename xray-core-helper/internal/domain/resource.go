package domain

import (
	"errors"
	"net/netip"
	"sort"
	"strings"
)

// ResourceKind identifies the kind of value accepted by NormalizeResource.
type ResourceKind string

const (
	DomainResource ResourceKind = "domain"
	IPv4Resource   ResourceKind = "ipv4"
)

var errInvalidResource = errors.New("invalid resource value")

// NormalizeResource trims value and classifies it as either a canonical IPv4
// address or a normalized DNS domain. It performs syntax validation only; no
// DNS lookups or policy decisions are made.
func NormalizeResource(value string) (string, ResourceKind, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", "", errInvalidResource
	}

	if addr, err := netip.ParseAddr(trimmed); err == nil {
		if !addr.Is4() {
			return "", "", errInvalidResource
		}
		return addr.String(), IPv4Resource, nil
	}

	name, err := Normalize(trimmed)
	if err != nil {
		return "", "", errInvalidResource
	}
	// A dotted candidate made entirely of numeric labels is an invalid or
	// malformed IPv4 address, never a DNS name. This also rejects forms such as
	// "999.0.0.1", "192.168.001.1", and "192.0.2.1.".
	if allNumericLabels(name) {
		return "", "", errInvalidResource
	}
	return name, DomainResource, nil
}

// SplitResources classifies, normalizes, deduplicates, and sorts values into
// domains and IPv4 addresses. Whitespace-only values are skipped. If any value
// is invalid, no partial results are returned.
func SplitResources(values []string) (domains []string, ipv4 []string, err error) {
	domainSet := make(map[string]struct{}, len(values))
	ipv4Set := make(map[string]struct{}, len(values))

	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		normalized, kind, normalizeErr := NormalizeResource(value)
		if normalizeErr != nil {
			return nil, nil, normalizeErr
		}
		switch kind {
		case DomainResource:
			domainSet[normalized] = struct{}{}
		case IPv4Resource:
			ipv4Set[normalized] = struct{}{}
		default:
			return nil, nil, errInvalidResource
		}
	}

	domains = make([]string, 0, len(domainSet))
	for name := range domainSet {
		domains = append(domains, name)
	}
	sort.Strings(domains)

	ipv4 = make([]string, 0, len(ipv4Set))
	for address := range ipv4Set {
		ipv4 = append(ipv4, address)
	}
	sort.Strings(ipv4)

	return domains, ipv4, nil
}

func allNumericLabels(name string) bool {
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 {
			return false
		}
		for _, char := range label {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}
