// Package usergeoip encodes user-provided IPv4 addresses as a deterministic
// GeoIPList protobuf for the USERLIST category.
package usergeoip

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"sort"

	"home-router/xray-core-helper/internal/domain"
)

const (
	// maxInputValues bounds the number of values accepted by Encode.
	maxInputValues = 4096

	userListCategory = "USERLIST"

	wireVarint          = 0
	wireLengthDelimited = 2

	geoIPListEntryField = 1
	geoIPCountryField   = 1
	geoIPCIDRField      = 2
	cidrIPField         = 1
	cidrPrefixField     = 2
	ipv4PrefixLength    = 32
)

var (
	errTooManyValues  = errors.New("usergeoip: too many input values")
	errInvalidAddress = errors.New("usergeoip: input contains an invalid IPv4 address")
)

// Encode serializes values as a GeoIPList containing exactly one GeoIP entry
// for the USERLIST category. Every value must be an IPv4 address; otherwise no
// bytes are returned. Valid addresses are normalized, deduplicated, and sorted
// before encoding.
func Encode(values []string) ([]byte, error) {
	if len(values) > maxInputValues {
		return nil, errTooManyValues
	}

	seen := make(map[string]struct{}, len(values))
	addresses := make([]string, 0, len(values))
	for _, value := range values {
		normalized, kind, err := domain.NormalizeResource(value)
		if err != nil || kind != domain.IPv4Resource {
			return nil, errInvalidAddress
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		addresses = append(addresses, normalized)
	}
	sort.Strings(addresses)

	geoIP := make([]byte, 0, 16+len(addresses)*10)
	geoIP = appendStringField(geoIP, geoIPCountryField, userListCategory)
	for _, address := range addresses {
		ip, err := ipv4Bytes(address)
		if err != nil {
			return nil, errInvalidAddress
		}

		cidr := make([]byte, 0, 10)
		cidr = appendBytesField(cidr, cidrIPField, ip)
		cidr = appendUint32Field(cidr, cidrPrefixField, ipv4PrefixLength)
		geoIP = appendMessageField(geoIP, geoIPCIDRField, cidr)
	}

	result := make([]byte, 0, len(geoIP)+2)
	result = appendMessageField(result, geoIPListEntryField, geoIP)
	return result, nil
}

func ipv4Bytes(value string) ([]byte, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() {
		return nil, errInvalidAddress
	}
	octets := addr.As4()
	return []byte{octets[0], octets[1], octets[2], octets[3]}, nil
}

func appendTag(dst []byte, field, wire int) []byte {
	return append(dst, byte(field<<3|wire))
}

func appendMessageField(dst []byte, field int, message []byte) []byte {
	dst = appendTag(dst, field, wireLengthDelimited)
	dst = binary.AppendUvarint(dst, uint64(len(message)))
	return append(dst, message...)
}

func appendBytesField(dst []byte, field int, value []byte) []byte {
	return appendMessageField(dst, field, value)
}

func appendStringField(dst []byte, field int, value string) []byte {
	dst = appendTag(dst, field, wireLengthDelimited)
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendUint32Field(dst []byte, field int, value uint32) []byte {
	dst = appendTag(dst, field, wireVarint)
	return binary.AppendUvarint(dst, uint64(value))
}
