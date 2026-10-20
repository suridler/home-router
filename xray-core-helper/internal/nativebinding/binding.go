// Package nativebinding reads explicit scalar bindings in native comments.
// It knows token boundaries and declared types, not gateway rule semantics.
package nativebinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativeblock"
)

var (
	ErrInvalid    = errors.New("native_binding_invalid")
	ErrUnresolved = errors.New("native_binding_unresolved")
	ErrMismatch   = errors.New("native_binding_mismatch")
	ErrLimit      = errors.New("native_binding_limit")
)

const scalarTypes = `uint32|port|interface|ipv4-prefix|bool01|geosite-reference|geoip-reference|ipv4-address|loopback-endpoint|log-destination|log-level|boolean`

var declaration = regexp.MustCompile(`^(?:#|//) helper:v1 bind ([A-Za-z][A-Za-z0-9_-]{0,63}) type=(` + scalarTypes + `) source=([A-Za-z0-9_.-]+) token=(".*")$`)
var nativeSource = regexp.MustCompile(`^native\.(nftables|sysctl|tproxy-unit)\.[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type Binding struct {
	ID, Type, Source     string
	value                string
	start, end           int
	tokenStart, tokenEnd int
	jsonScalar           bool
}

// Document retains private source bytes. Neither it nor Binding is a public
// transport projection; callers must return only identifiers and status codes.
type Document struct {
	raw      []byte
	bindings []Binding
}

// Resolver returns semantic scalar values, not native quoting or source text.
// Implementations must be read-only and invoke providers only when requested.
type Resolver func(string) (string, error)

func Parse(raw []byte) (*Document, error) {
	if len(raw) > nativeblock.MaxBytes {
		return nil, ErrLimit
	}
	doc := &Document{raw: bytes.Clone(raw)}
	seen := map[string]bool{}
	var pending *Binding
	for offset := 0; offset < len(raw); {
		end := bytes.IndexByte(raw[offset:], '\n')
		after := len(raw)
		if end >= 0 {
			after = offset + end + 1
		}
		line := raw[offset:after]
		text := strings.TrimSpace(string(line))
		if isHelperComment(text) {
			if pending != nil {
				return nil, ErrInvalid
			}
			if text == nativeblock.Begin || text == nativeblock.End {
				offset = after
				continue
			}
			if len(line) > 4096 || !utf8.Valid(line) {
				return nil, ErrLimit
			}
			match := declaration.FindStringSubmatch(text)
			if match == nil || !providerAllowed(match[3]) || seen[match[1]] {
				return nil, ErrInvalid
			}
			var token string
			if err := json.Unmarshal([]byte(match[4]), &token); err != nil || token == "" {
				return nil, ErrInvalid
			}
			if isGeoReference(match[2]) && !validGeoLocator(token, match[2]) {
				return nil, ErrInvalid
			}
			// The token is the final declaration attribute. Deriving its byte
			// offset from the end avoids mistaking an attribute-looking byte
			// sequence inside the quoted token for the locator itself.
			trimmed := strings.TrimRight(string(line), " \t\r\n")
			tokenStart := offset + len(trimmed) - len(match[4])
			if tokenStart < offset {
				return nil, ErrInvalid
			}
			pending = &Binding{ID: match[1], Type: match[2], Source: match[3], value: token, tokenStart: tokenStart, tokenEnd: tokenStart + len(match[4]), jsonScalar: strings.HasPrefix(text, "//")}
			if jsonOnlyType(pending.Type) && !pending.jsonScalar {
				return nil, ErrInvalid
			}
			seen[pending.ID] = true
			if len(seen) > 256 {
				return nil, ErrLimit
			}
		} else if pending != nil {
			// Only physical continuations extend a marked logical statement.
			// A blank/comment line between declaration and statement is invalid.
			if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
				return nil, ErrInvalid
			}
			statementEnd := after
			for strings.HasSuffix(strings.TrimRight(string(raw[offset:statementEnd]), " \t\r\n"), "\\") {
				if statementEnd == len(raw) {
					return nil, ErrInvalid
				}
				next := bytes.IndexByte(raw[statementEnd:], '\n')
				if next < 0 {
					statementEnd = len(raw)
				} else {
					statementEnd += next + 1
				}
			}
			start, end, err := locateToken(raw[offset:statementEnd], pending.value, pending.jsonScalar)
			if err != nil {
				return nil, err
			}
			if _, err = normalize(pending.Type, pending.value); err != nil {
				return nil, err
			}
			if pending.jsonScalar && !validJSONScalar(pending.Type, pending.value) {
				return nil, ErrInvalid
			}
			pending.start, pending.end = offset+start, offset+end
			doc.bindings = append(doc.bindings, *pending)
			pending = nil
			after = statementEnd
		}
		offset = after
	}
	if pending != nil {
		return nil, ErrInvalid
	}
	return doc, nil
}

func isHelperComment(text string) bool {
	return strings.HasPrefix(text, "# helper:") || strings.HasPrefix(text, "// helper:")
}

func providerAllowed(source string) bool {
	switch source {
	case "native.literal", "helper.user-geosite", "helper.user-geoip", "helper.xray-api", "helper.logging.access-path", "helper.logging.error-path", "helper.logging.level", "helper.logging.dns-log", "helper.network.address", "helper.network.interface", "helper.network.ipv4-prefix", "helper.network.intercept-mark", "helper.network.policy-table":
		return true
	case "setup.interface", "setup.ipv4-prefix", "xray.service-gid", "xray.tproxy-port", "xray.intercept-mark", "xray.egress-mark":
		return true
	}
	return nativeSource.MatchString(source)
}

func (doc *Document) Bindings() []Binding { return append([]Binding(nil), doc.bindings...) }

// Check compares only explicitly declared values; an empty document needs no
// resolver and performs no checks. Resolver errors are redacted.
func (doc *Document) Check(resolve Resolver) error {
	for _, binding := range doc.bindings {
		if binding.Source == "native.literal" {
			continue
		}
		if resolve == nil {
			return ErrUnresolved
		}
		value, err := resolve(binding.Source)
		if err != nil {
			return ErrUnresolved
		}
		actual, _ := normalize(binding.Type, binding.value)
		expected, err := normalize(binding.Type, value)
		if err != nil {
			return ErrUnresolved
		}
		if actual != expected {
			return ErrMismatch
		}
	}
	return nil
}

// Render edits both the native literal and its locator metadata so the saved
// system file remains readable on the next status refresh.
func (doc *Document) Render(resolve Resolver) ([]byte, error) {
	return doc.RenderSelected(resolve, func(Binding) bool { return true })
}

// RenderSelected leaves relationship checks unchanged while setup substitutes
// only explicitly selected setup inputs.
func (doc *Document) RenderSelected(resolve Resolver, selected func(Binding) bool) ([]byte, error) {
	type edit struct {
		start, end int
		value      []byte
	}
	var edits []edit
	for _, binding := range doc.bindings {
		if binding.Source == "native.literal" || selected == nil || !selected(binding) {
			continue
		}
		if resolve == nil {
			return nil, ErrUnresolved
		}
		value, err := resolve(binding.Source)
		if err != nil {
			return nil, ErrUnresolved
		}
		normalized, err := normalize(binding.Type, value)
		if err != nil {
			return nil, ErrUnresolved
		}
		if isGeoReference(binding.Type) {
			encoded, err := rewriteGeoLocator(binding.value, normalized, binding.Type)
			if err != nil {
				return nil, ErrInvalid
			}
			metadata, err := json.Marshal(encoded)
			if err != nil {
				return nil, ErrInvalid
			}
			edits = append(edits, edit{binding.tokenStart, binding.tokenEnd, metadata}, edit{binding.start, binding.end, []byte(encoded)})
			continue
		}
		encoded := value
		if binding.jsonScalar {
			encoded = encodeJSONScalar(binding.Type, normalized)
		} else {
			if strings.HasPrefix(binding.value, "0x") || strings.HasPrefix(binding.value, "0X") {
				if binding.Type == "uint32" || binding.Type == "port" {
					number, _ := strconv.ParseUint(normalized, 10, 32)
					encoded = binding.value[:2] + strconv.FormatUint(number, 16)
				}
			}
			if strings.HasPrefix(binding.value, "\"") {
				encoded = strconv.Quote(encoded)
			}
		}
		metadata, err := json.Marshal(encoded)
		if err != nil {
			return nil, ErrInvalid
		}
		edits = append(edits, edit{binding.tokenStart, binding.tokenEnd, metadata}, edit{binding.start, binding.end, []byte(encoded)})
	}
	var out bytes.Buffer
	prior := 0
	for _, edit := range edits {
		if edit.start < prior {
			return nil, ErrInvalid
		}
		if out.Len()+edit.start-prior+len(edit.value) > nativeblock.MaxBytes {
			return nil, ErrLimit
		}
		out.Write(doc.raw[prior:edit.start])
		out.Write(edit.value)
		prior = edit.end
	}
	if out.Len()+len(doc.raw)-prior > nativeblock.MaxBytes {
		return nil, ErrLimit
	}
	out.Write(doc.raw[prior:])
	// Check newly encoded locators and the declaration-size bound before return.
	if _, err := Parse(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func normalize(kind, value string) (string, error) {
	if strings.HasPrefix(value, "\"") {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", ErrInvalid
		}
		value = decoded
	}
	switch kind {
	case "uint32", "port", "bool01":
		base := 10
		if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
			value = value[2:]
			base = 16
		}
		if value == "" {
			return "", ErrInvalid
		}
		for _, c := range value {
			if !(c >= '0' && c <= '9' || base == 16 && (c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')) {
				return "", ErrInvalid
			}
		}
		n, err := strconv.ParseUint(value, base, 32)
		if err != nil || kind == "port" && (n == 0 || n > 65535) || kind == "bool01" && (base != 10 || value != "0" && value != "1") {
			return "", ErrInvalid
		}
		return strconv.FormatUint(n, 10), nil
	case "interface":
		if len(value) == 0 || len(value) > 15 || value == "." || value == ".." || strings.ContainsAny(value, " \t\r\n/:\x00\"'\\") || !utf8.ValidString(value) {
			return "", ErrInvalid
		}
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return "", ErrInvalid
			}
		}
		return value, nil
	case "ipv4-prefix":
		prefix, err := netip.ParsePrefix(value)
		if err != nil || !prefix.Addr().Is4() {
			return "", ErrInvalid
		}
		return prefix.Masked().String(), nil
	case "geosite-reference", "geoip-reference":
		if filename, _, ok := parseGeoReference(value, kind); ok {
			return filename, nil
		}
		return "", ErrInvalid
	case "ipv4-address", "loopback-endpoint", "log-destination", "log-level", "boolean":
		return normalizeJSONValue(kind, value)
	}
	return "", ErrInvalid
}

const geositeReferencePrefix = "ext:"

// parseGeositeReference accepts either a managed filename or a complete Xray
// ext:file:category reference. The category is retained by the caller and is
// constrained to a bounded ASCII selector, never a path.
func parseGeoReference(value, kind string) (filename, category string, ok bool) {
	validName := bootstrap.ValidUserGeositeName
	if kind == "geoip-reference" {
		validName = bootstrap.ValidUserGeoipName
	}
	if validName(value) {
		return value, "", true
	}
	remainder, found := strings.CutPrefix(value, geositeReferencePrefix)
	if !found {
		return "", "", false
	}
	filename, category, found = strings.Cut(remainder, ":")
	if !found || !validName(filename) || !validGeositeCategory(category) {
		return "", "", false
	}
	return filename, category, true
}

func validGeositeCategory(category string) bool {
	if len(category) == 0 || len(category) > 64 {
		return false
	}
	for index := 0; index < len(category); index++ {
		character := category[index]
		if !(character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-') {
			return false
		}
	}
	return true
}

// validGeositeLocator requires the declaration token to be one complete quoted
// JSON string containing a full ext:file:category reference. A bare filename
// could otherwise locate only part of a larger native token.
func validGeoLocator(token, kind string) bool {
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return false
	}
	decoded, err := strconv.Unquote(token)
	if err != nil {
		return false
	}
	_, category, ok := parseGeoReference(decoded, kind)
	return ok && category != ""
}

// rewriteGeositeLocator changes only the filename in an already validated
// quoted ext reference, retaining its category and JSON string encoding.
func rewriteGeoLocator(token, filename, kind string) (string, error) {
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return "", ErrInvalid
	}
	decoded, err := strconv.Unquote(token)
	if err != nil {
		return "", ErrInvalid
	}
	_, category, ok := parseGeoReference(decoded, kind)
	_, _, nameOK := parseGeoReference(filename, kind)
	if !ok || category == "" || !nameOK {
		return "", ErrInvalid
	}
	return strconv.Quote(geositeReferencePrefix + filename + ":" + category), nil
}

func locateToken(raw []byte, want string, jsonComments bool) (int, int, error) {
	start, end, count := 0, 0, 0
	for i := 0; i < len(raw); {
		if raw[i] == '#' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		if jsonComments && raw[i] == '/' && i+1 < len(raw) && raw[i+1] == '/' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}

		if jsonComments && raw[i] == '/' && i+1 < len(raw) && raw[i+1] == '*' {
			end := bytes.Index(raw[i+2:], []byte("*/"))
			if end < 0 {
				return 0, 0, ErrInvalid
			}
			i += end + 4
			continue
		}
		if delimiter(raw[i]) {
			i++
			continue
		}
		from := i
		if raw[i] == '"' || raw[i] == '\'' {
			quote := raw[i]
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == '\\' {
					i += 2
					continue
				}
				if raw[i] == quote {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed || i > len(raw) {
				return 0, 0, ErrInvalid
			}
		} else {
			for i < len(raw) && !delimiter(raw[i]) && raw[i] != '#' {
				i++
			}
		}
		if string(raw[from:i]) == want {
			start, end, count = from, i, count+1
		}
	}
	if count != 1 {
		return 0, 0, ErrInvalid
	}
	return start, end, nil
}

func delimiter(c byte) bool { return strings.ContainsRune(" \t\r\n=,:;{}[]()\\", rune(c)) }

// Scalar returns the normalized value of an explicitly annotated native scalar.
func (doc *Document) Scalar(id string) (string, error) {
	for _, binding := range doc.bindings {
		if binding.ID == id {
			return normalize(binding.Type, binding.value)
		}
	}
	return "", ErrUnresolved
}
