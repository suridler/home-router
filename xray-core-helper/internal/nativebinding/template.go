package nativebinding

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var templateDeclaration = regexp.MustCompile(`^(?:#|//) helper:v1 bind ([A-Za-z][A-Za-z0-9_-]{0,63}) type=(` + scalarTypes + `) source=([A-Za-z0-9_.-]+)$`)
var placeholder = regexp.MustCompile(`\$\{[A-Za-z][A-Za-z0-9_]{0,63}\}`)

// TemplateSources inspects explicit declarations without evaluating placeholders.
// Parse still requires concrete native literals for installed files.
func TemplateSources(raw []byte) ([]string, error) {
	if len(raw) > 1<<20 {
		return nil, ErrLimit
	}
	var sources []string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if !isHelperComment(s) || s == "# helper:begin gateway" || s == "# helper:end gateway" {
			continue
		}
		m := templateDeclaration.FindStringSubmatch(s)
		if m == nil {
			m = declaration.FindStringSubmatch(s)
		}
		if m == nil || !providerAllowed(m[3]) {
			return nil, ErrInvalid
		}
		sources = append(sources, m[3])
	}
	return sources, nil
}

// Prepare expands only a declared placeholder in the immediately following
// logical statement. No shell, environment lookup or global substitution occurs.
// Concrete output retains a typed literal locator for later drift checks.
func Prepare(raw []byte, resolve Resolver) ([]byte, error) {
	if _, err := TemplateSources(raw); err != nil {
		return nil, err
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	var out bytes.Buffer
	for i := 0; i < len(lines); i++ {
		line := string(lines[i])
		m := templateDeclaration.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			out.WriteString(line)
			continue
		}
		jsonScalar := strings.HasPrefix(strings.TrimSpace(line), "//")
		if jsonOnlyType(m[2]) && !jsonScalar {
			return nil, ErrInvalid
		}
		if resolve == nil || m[3] == "native.literal" {
			return nil, ErrUnresolved
		}
		if i+1 >= len(lines) {
			return nil, ErrInvalid
		}
		i++
		statement := string(lines[i])
		for strings.HasSuffix(strings.TrimSpace(statement), "\\") {
			if i+1 >= len(lines) {
				return nil, ErrInvalid
			}
			i++
			statement += string(lines[i])
		}
		matches := placeholder.FindAllStringIndex(statement, -1)
		if len(matches) != 1 {
			return nil, ErrInvalid
		}
		start, end := matches[0][0], matches[0][1]
		// A placeholder is a whole unquoted native scalar, not part of a string,
		// comment, longer identifier, or another expansion.
		probe := statement[:start] + "987654321" + statement[end:]
		located, stop, err := locateToken([]byte(probe), "987654321", jsonScalar)
		if err != nil || located != start || stop != start+9 {
			return nil, ErrInvalid
		}
		value, err := resolve(m[3])
		if err != nil {
			return nil, ErrUnresolved
		}
		value, err = normalize(m[2], value)
		if err != nil {
			return nil, ErrUnresolved
		}
		encoded := value
		if jsonScalar {
			if isGeoReference(m[2]) {
				// Template user-list references always select the managed category;
				// concrete annotations preserve their existing category instead.
				encoded = strconv.Quote("ext:" + value + ":USERLIST")
			} else {
				encoded = encodeJSONScalar(m[2], value)
			}
		} else if m[2] == "interface" {
			encoded = strconv.Quote(value)
		}
		token, _ := json.Marshal(encoded)
		ending := "\n"
		if strings.HasSuffix(line, "\r\n") {
			ending = "\r\n"
		}
		out.WriteString(strings.TrimRight(line, " \t\r\n") + " token=" + string(token) + ending)
		out.WriteString(statement[:start] + encoded + statement[end:])
		if out.Len() > 1<<20 {
			return nil, ErrLimit
		}
	}
	result := out.Bytes()
	if _, err := Parse(result); err != nil {
		return nil, err
	}
	return result, nil
}
