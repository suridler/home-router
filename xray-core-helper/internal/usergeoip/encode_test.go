package usergeoip

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

const (
	testMaxInputValues      = 4096
	testWireVarint          = 0
	testWireLengthDelimited = 2
	testGeoIPListEntryField = 1
	testGeoIPCountryField   = 1
	testGeoIPCIDRField      = 2
	testCIDRIPField         = 1
	testCIDRPrefixField     = 2
)

type decodedCIDR struct {
	ip     []byte
	prefix uint32
}

type decodedGeoIP struct {
	countryCode string
	cidrs       []decodedCIDR
}

type decodedGeoIPList struct {
	entries []decodedGeoIP
}

func TestEncodeEmptyList(t *testing.T) {
	// "USERLIST" has 8 bytes, so the valid protobuf lengths are 0x0a for the
	// GeoIPList entry and 0x08 for the country code string.
	want := mustHex(t, "0a0a0a08555345524c495354")

	for _, testCase := range []struct {
		name   string
		values []string
	}{
		{name: "nil", values: nil},
		{name: "empty", values: []string{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Encode(testCase.values)
			if err != nil {
				t.Fatalf("Encode(%#v) error: %v", testCase.values, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Encode(%#v) = %x; want %x", testCase.values, got, want)
			}

			list := decodeGeoIPList(t, got)
			if len(list.entries) != 1 {
				t.Fatalf("decoded %d entries; want 1", len(list.entries))
			}
			if list.entries[0].countryCode != "USERLIST" {
				t.Fatalf("country code = %q; want USERLIST", list.entries[0].countryCode)
			}
			if len(list.entries[0].cidrs) != 0 {
				t.Fatalf("decoded %d CIDRs; want none", len(list.entries[0].cidrs))
			}
		})
	}
}

func TestEncodeSingleIPv4WireEncoding(t *testing.T) {
	want := mustHex(t, "0a140a08555345524c49535412080a04c00002011020")

	got, err := Encode([]string{"192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = %x; want %x", got, want)
	}

	list := decodeGeoIPList(t, got)
	wantCIDRs := []decodedCIDR{{ip: []byte{192, 0, 2, 1}, prefix: 32}}
	if len(list.entries) != 1 || list.entries[0].countryCode != "USERLIST" {
		t.Fatalf("decoded entries = %#v; want one USERLIST entry", list.entries)
	}
	if !reflect.DeepEqual(list.entries[0].cidrs, wantCIDRs) {
		t.Fatalf("decoded CIDRs = %#v; want %#v", list.entries[0].cidrs, wantCIDRs)
	}
}

func TestEncodeMultipleShuffledDuplicatesIsDeterministic(t *testing.T) {
	values := []string{
		"198.51.100.7",
		" 192.0.2.1 ",
		"10.0.0.1",
		"192.0.2.1",
		"198.51.100.7",
		"10.0.0.1",
	}
	reversed := make([]string, len(values))
	for index := range values {
		reversed[index] = values[len(values)-1-index]
	}

	first, err := Encode(values)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Encode is not deterministic:\nfirst  = %x\nsecond = %x", first, second)
	}

	list := decodeGeoIPList(t, first)
	wantCIDRs := []decodedCIDR{
		{ip: []byte{10, 0, 0, 1}, prefix: 32},
		{ip: []byte{192, 0, 2, 1}, prefix: 32},
		{ip: []byte{198, 51, 100, 7}, prefix: 32},
	}
	if len(list.entries) != 1 {
		t.Fatalf("decoded %d entries; want 1", len(list.entries))
	}
	if list.entries[0].countryCode != "USERLIST" {
		t.Fatalf("country code = %q; want USERLIST", list.entries[0].countryCode)
	}
	if !reflect.DeepEqual(list.entries[0].cidrs, wantCIDRs) {
		t.Fatalf("decoded CIDRs = %#v; want %#v", list.entries[0].cidrs, wantCIDRs)
	}
}

func TestEncodeInvalidValuesReturnNoPartialOutput(t *testing.T) {
	invalidValues := []string{
		"",
		" \t\n",
		"example.com",
		"Sub.Example.COM",
		"localhost",
		"1.2.3",
		"2001:db8::1",
		"::1",
		"::ffff:192.0.2.1",
		"192.0.2.0/24",
		"192.0.2.1/32",
		"999.0.0.1",
		"192.168.001.1",
		"192.0.2.1.",
		"192.0.2.1:443",
		"https://example.com",
		"*.example.com",
	}

	for _, invalid := range invalidValues {
		t.Run(invalid, func(t *testing.T) {
			got, err := Encode([]string{"192.0.2.1", invalid, "198.51.100.7"})
			if err == nil {
				t.Fatalf("Encode(%q) succeeded with %x", invalid, got)
			}
			if got != nil {
				t.Fatalf("Encode(%q) returned partial output %x", invalid, got)
			}
			if strings.TrimSpace(invalid) != "" && strings.Contains(err.Error(), invalid) {
				t.Fatalf("error leaks raw input %q: %v", invalid, err)
			}
		})
	}
}

func TestEncodeInputLengthBound(t *testing.T) {
	values := make([]string, testMaxInputValues)
	for index := range values {
		values[index] = "192.0.2.1"
	}

	got, err := Encode(values)
	if err != nil {
		t.Fatalf("Encode with %d values error: %v", len(values), err)
	}
	if len(got) == 0 {
		t.Fatal("Encode with maximum values returned no output")
	}

	tooMany := append(values, "192.0.2.1")
	got, err = Encode(tooMany)
	if err == nil {
		t.Fatalf("Encode with %d values succeeded", len(tooMany))
	}
	if got != nil {
		t.Fatalf("Encode with %d values returned partial output %x", len(tooMany), got)
	}
}

func TestEncodeErrorsRedactInput(t *testing.T) {
	secrets := []string{
		"SENSITIVE.example",
		"999.0.0.1",
		"2001:db8::dead",
		"secret-token",
	}

	for _, secret := range secrets {
		t.Run(secret, func(t *testing.T) {
			got, err := Encode([]string{secret})
			if err == nil {
				t.Fatalf("Encode(%q) succeeded with %x", secret, got)
			}
			if got != nil {
				t.Fatalf("Encode(%q) returned partial output %x", secret, got)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks raw input %q: %v", secret, err)
			}
		})
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("invalid test fixture %q: %v", value, err)
	}
	return decoded
}

func decodeGeoIPList(t *testing.T, data []byte) decodedGeoIPList {
	t.Helper()
	var list decodedGeoIPList
	for len(data) > 0 {
		field, wire, consumed := readTag(t, data)
		data = data[consumed:]
		if field != testGeoIPListEntryField || wire != testWireLengthDelimited {
			t.Fatalf("unexpected GeoIPList field %d wire %d", field, wire)
		}
		payload, consumed := readLengthDelimited(t, data)
		data = data[consumed:]
		list.entries = append(list.entries, decodeGeoIP(t, payload))
	}
	return list
}

func decodeGeoIP(t *testing.T, data []byte) decodedGeoIP {
	t.Helper()
	var entry decodedGeoIP
	for len(data) > 0 {
		field, wire, consumed := readTag(t, data)
		data = data[consumed:]
		switch field {
		case testGeoIPCountryField:
			if wire != testWireLengthDelimited {
				t.Fatalf("country code wire type = %d; want length-delimited", wire)
			}
			payload, consumed := readLengthDelimited(t, data)
			data = data[consumed:]
			entry.countryCode = string(payload)
		case testGeoIPCIDRField:
			if wire != testWireLengthDelimited {
				t.Fatalf("CIDR wire type = %d; want length-delimited", wire)
			}
			payload, consumed := readLengthDelimited(t, data)
			data = data[consumed:]
			entry.cidrs = append(entry.cidrs, decodeCIDR(t, payload))
		default:
			t.Fatalf("unexpected GeoIP field %d", field)
		}
	}
	return entry
}

func decodeCIDR(t *testing.T, data []byte) decodedCIDR {
	t.Helper()
	var cidr decodedCIDR
	for len(data) > 0 {
		field, wire, consumed := readTag(t, data)
		data = data[consumed:]
		switch field {
		case testCIDRIPField:
			if wire != testWireLengthDelimited {
				t.Fatalf("IP wire type = %d; want length-delimited", wire)
			}
			payload, consumed := readLengthDelimited(t, data)
			data = data[consumed:]
			cidr.ip = append([]byte(nil), payload...)
		case testCIDRPrefixField:
			if wire != testWireVarint {
				t.Fatalf("prefix wire type = %d; want varint", wire)
			}
			value, consumed := readUvarint(t, data)
			data = data[consumed:]
			cidr.prefix = uint32(value)
		default:
			t.Fatalf("unexpected CIDR field %d", field)
		}
	}
	return cidr
}

func readTag(t *testing.T, data []byte) (field, wire, consumed int) {
	t.Helper()
	tag, consumed := readUvarint(t, data)
	return int(tag >> 3), int(tag & 7), consumed
}

func readUvarint(t *testing.T, data []byte) (uint64, int) {
	t.Helper()
	value, consumed := binary.Uvarint(data)
	if consumed <= 0 {
		t.Fatalf("invalid varint in %x", data)
	}
	return value, consumed
}

func readLengthDelimited(t *testing.T, data []byte) ([]byte, int) {
	t.Helper()
	length, prefix := readUvarint(t, data)
	if length > uint64(len(data)-prefix) {
		t.Fatalf("length %d exceeds remaining %d bytes", length, len(data)-prefix)
	}
	end := prefix + int(length)
	return data[prefix:end], end
}
