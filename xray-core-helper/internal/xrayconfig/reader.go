package xrayconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

const maxConfigBytes = 16 << 20

var (
	vlessURI = regexp.MustCompile(`(?i)vless://\S+`)
	uuid     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	secret   = regexp.MustCompile(`(?i)\b(private[_-]?key|password|short[_-]?id|(?:access[_-]?)?token)\s*[:=]\s*\S+`)
)

type Settings struct {
	Binary   string
	Config   string
	API      string
	AssetDir string
}

type Reader struct {
	settings  Settings
	metadata  MetadataSource
	reconcile func(context.Context) error
}

type MetadataSource interface {
	ConfigMetadata(context.Context) (uint64, string, error)
}

type document struct {
	value    map[string]any
	metadata api.ConfigMetadata
	spans    *jsonNode
	source   []byte
	drift    bool
}

type jsonNode struct {
	start   int
	end     int
	object  map[string]*jsonNode
	members []jsonMember
	array   []*jsonNode
}

type jsonMember struct {
	key      string
	keyStart int
	value    *jsonNode
}

type parsedSource struct {
	value map[string]any
	spans *jsonNode
	clean []byte
}

// parseSource parses JSONC; clean keeps byte offsets with comments blanked.
func parseSource(source []byte) (*parsedSource, error) {
	clean, stripErr := stripJSONC(source)
	if stripErr != nil {
		return nil, &projectionError{code: "invalid_config", message: fmt.Sprintf("invalid JSONC at byte %d", stripErr.offset), status: 422}
	}
	var parsed map[string]any
	decoder := json.NewDecoder(bytes.NewReader(clean))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil || parsed == nil {
		position := 0
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			position = int(syntaxErr.Offset) - 1
		}
		return nil, &projectionError{code: "invalid_config", message: fmt.Sprintf("invalid JSONC at byte %d", position), status: 422}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, &projectionError{code: "invalid_config", message: "invalid JSONC: trailing content", status: 422}
	}
	spans, err := scanJSON(clean)
	if err != nil {
		return nil, &projectionError{code: "invalid_config", message: "invalid JSONC structure", status: 422}
	}
	return &parsedSource{value: parsed, spans: spans, clean: clean}, nil
}

type projectionError struct {
	code    string
	message string
	status  int
}

func (err *projectionError) Error() string { return err.message }

func ErrorInfo(err error) (code, message string, status int) {
	var projectionErr *projectionError
	if errors.As(err, &projectionErr) {
		return projectionErr.code, projectionErr.message, projectionErr.status
	}
	return "config_unavailable", "Xray configuration projection is unavailable", 500
}

func NewReader(settings Settings, metadata ...MetadataSource) Reader {
	reader := Reader{settings: settings}
	if len(metadata) != 0 {
		reader.metadata = metadata[0]
	}
	return reader
}

// WithDriftReconciler enables validation/adoption only on state observations.
// Low-level projections remain read-only, including inside mutation planners.
func (reader Reader) WithDriftReconciler(reconcile func(context.Context) error) Reader {
	reader.reconcile = reconcile
	return reader
}

func (reader Reader) reconcileState(ctx context.Context) error {
	if reader.reconcile != nil {
		return reader.reconcile(ctx)
	}
	return nil
}

func (reader Reader) Runtime() api.RuntimeResponse {
	return api.RuntimeResponse{
		Binary:          reader.settings.Binary,
		Config:          reader.settings.Config,
		API:             reader.settings.API,
		AssetDir:        reader.settings.AssetDir,
		BinaryAvailable: pathExists(reader.settings.Binary, false),
		ConfigAvailable: pathExists(reader.settings.Config, false),
		AssetDirPresent: pathExists(reader.settings.AssetDir, true),
	}
}

func (reader Reader) ExporterMetadata(ctx context.Context) (api.ExporterMetadataResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.ExporterMetadataResponse{}, err
	}
	return api.ExporterMetadataResponse{
		ConfigMetadata: doc.metadata,
		API:            reader.settings.API,
		InboundTags:    uniqueTags(doc.value["inbounds"]),
		OutboundTags:   uniqueTags(doc.value["outbounds"]),
	}, nil
}

func uniqueTags(value any) []string {
	tags := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range asSlice(value) {
		tag := safeString(asMap(item)["tag"])
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func (reader Reader) Status(ctx context.Context) api.StatusResponse {
	reconcileErr := reader.reconcileState(ctx)
	doc, err := reader.readDocument(ctx)
	status := statusProjection(reader.Runtime(), doc, err)
	if reconcileErr != nil && status.ConfigDrift {
		_, status.ConfigError, _ = transaction.ErrorInfo(reconcileErr)
	}
	return status
}

func statusProjection(runtime api.RuntimeResponse, doc *document, err error) api.StatusResponse {
	status := api.StatusResponse{Runtime: runtime}
	if err != nil {
		status.ConfigState = "unavailable"
		var projectionErr *projectionError
		if errors.As(err, &projectionErr) && projectionErr.code == "invalid_config" {
			status.ConfigState = "invalid"
		}
		status.ConfigError = err.Error()
		return status
	}
	status.ConfigReadable = true
	status.ConfigParsed = true
	status.ConfigMetadata = doc.metadata
	status.ConfigDrift = doc.drift
	status.ConfigState = "parsed"
	if doc.drift {
		status.ConfigState = "drift"
		status.ConfigError = "configuration differs from committed state"
	}
	return status
}

func (reader Reader) Summary(ctx context.Context) (api.ConfigSummaryResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.ConfigSummaryResponse{}, err
	}
	knownSections := []string{
		"env", "log", "api", "dns", "routing", "policy", "inbounds", "outbounds",
		"stats", "metrics", "observatory", "burstObservatory", "geodata", "fakedns", "version",
	}
	sections := make([]string, 0, len(knownSections))
	for _, section := range knownSections {
		if _, exists := doc.value[section]; exists {
			sections = append(sections, section)
		}
	}
	return api.ConfigSummaryResponse{ConfigMetadata: doc.metadata, Sections: sections}, nil
}

func (reader Reader) Inbounds(ctx context.Context) (api.InboundsResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.InboundsResponse{}, err
	}
	return inboundsProjection(doc), nil
}

func inboundsProjection(doc *document) api.InboundsResponse {
	items := make([]api.InboundProjection, 0)
	for _, value := range asSlice(doc.value["inbounds"]) {
		inbound := asMap(value)
		items = append(items, api.InboundProjection{
			Tag:      safeString(inbound["tag"]),
			Listen:   defaultString(safeString(inbound["listen"]), "0.0.0.0"),
			Port:     safeValue(inbound["port"]),
			Protocol: safeString(inbound["protocol"]),
		})
	}
	return api.InboundsResponse{ConfigMetadata: doc.metadata, Inbounds: items}
}

func (reader Reader) Outbounds(ctx context.Context) (api.OutboundsResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.OutboundsResponse{}, err
	}
	return outboundsProjection(doc), nil
}

func outboundsProjection(doc *document) api.OutboundsResponse {
	items := make([]api.OutboundProjection, 0)
	for _, value := range asSlice(doc.value["outbounds"]) {
		outbound := asMap(value)
		items = append(items, api.OutboundProjection{
			Tag:      safeString(outbound["tag"]),
			Protocol: safeString(outbound["protocol"]),
		})
	}
	return api.OutboundsResponse{ConfigMetadata: doc.metadata, Outbounds: items}
}

func (reader Reader) Routing(ctx context.Context) (api.RoutingResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.RoutingResponse{}, err
	}
	return routingProjection(doc), nil
}

func routingProjection(doc *document) api.RoutingResponse {
	routing := asMap(doc.value["routing"])
	routingNode := doc.spans.object["routing"]
	var ruleNodes []*jsonNode
	if routingNode != nil && routingNode.object["rules"] != nil {
		ruleNodes = routingNode.object["rules"].array
	}
	protection := protectionResolver(doc.value)
	rules := make([]api.RoutingRuleProjection, 0)
	for index, value := range asSlice(routing["rules"]) {
		rule := asMap(value)
		reason := protection(rule)
		var ruleNode *jsonNode
		if index < len(ruleNodes) {
			ruleNode = ruleNodes[index]
		}
		rules = append(rules, api.RoutingRuleProjection{
			Index:            index + 1,
			Tag:              safeString(rule["ruleTag"]),
			Type:             defaultString(safeString(rule["type"]), "field"),
			Description:      extractDescription(doc.source, ruleNode),
			Protected:        reason != "",
			ProtectionReason: reason,
			OutboundTag:      safeString(rule["outboundTag"]),
			BalancerTag:      safeString(rule["balancerTag"]),
			Conditions:       routingConditions(rule),
		})
	}
	balancers := make([]api.BalancerProjection, 0)
	for _, value := range asSlice(routing["balancers"]) {
		balancer := asMap(value)
		strategy := asMap(balancer["strategy"])
		balancers = append(balancers, api.BalancerProjection{
			Tag:      safeString(balancer["tag"]),
			Selector: stringValues(balancer["selector"]),
			Strategy: safeString(strategy["type"]),
		})
	}
	return api.RoutingResponse{
		ConfigMetadata: doc.metadata,
		DomainStrategy: safeString(routing["domainStrategy"]),
		Rules:          rules,
		Balancers:      balancers,
	}
}

func (reader Reader) Logging(ctx context.Context) (api.LoggingResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.LoggingResponse{}, err
	}
	return loggingProjection(doc), nil
}

func loggingProjection(doc *document) api.LoggingResponse {
	logging := asMap(doc.value["log"])
	return api.LoggingResponse{
		ConfigMetadata: doc.metadata,
		Access:         safeString(logging["access"]),
		Error:          safeString(logging["error"]),
		Loglevel:       defaultString(safeString(logging["loglevel"]), "warning"),
		DNSLog:         logging["dnsLog"] == true,
	}
}

func (reader Reader) Geodata(ctx context.Context) (api.GeodataResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.GeodataResponse{}, err
	}
	files := make([]api.GeodataFile, 0, 2)
	for _, name := range []string{"geosite.dat", "geoip.dat"} {
		path := filepath.Join(reader.settings.AssetDir, name)
		file := api.GeodataFile{Name: name, Path: path}
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			file.Present = true
			file.Size = info.Size()
			file.ModifiedAt = info.ModTime().UTC()
		}
		files = append(files, file)
	}
	return api.GeodataResponse{ConfigMetadata: doc.metadata, AssetDir: reader.settings.AssetDir, Files: files}, nil
}

func (reader Reader) readDocument(ctx context.Context) (*document, error) {
	if !filepath.IsAbs(reader.settings.Config) {
		return nil, &projectionError{code: "config_unavailable", message: "Xray configuration path is unavailable", status: 500}
	}
	file, err := os.Open(reader.settings.Config)
	if err != nil {
		return nil, &projectionError{code: "config_unavailable", message: "Xray configuration is unavailable", status: 500}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, &projectionError{code: "config_unavailable", message: "Xray configuration is unavailable", status: 500}
	}
	if info.Size() > maxConfigBytes {
		return nil, &projectionError{code: "invalid_config", message: "Xray configuration exceeds the size limit", status: 422}
	}
	source, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, &projectionError{code: "config_unavailable", message: "Xray configuration cannot be read", status: 500}
	}
	if len(source) > maxConfigBytes {
		return nil, &projectionError{code: "invalid_config", message: "Xray configuration exceeds the size limit", status: 422}
	}
	syntax, parseErr := parseSource(source)
	if parseErr != nil {
		return nil, parseErr
	}
	parsed, spans := syntax.value, syntax.spans
	hash := sha256.Sum256(source)
	contentHash := hex.EncodeToString(hash[:])
	generation := uint64(0)
	committedHash := ""
	if reader.metadata != nil {
		generation, committedHash, err = reader.metadata.ConfigMetadata(ctx)
		if err != nil {
			return nil, &projectionError{code: "config_state_unavailable", message: "configuration state is unavailable", status: 503}
		}
	}
	return &document{
		value: parsed,
		metadata: api.ConfigMetadata{
			Generation:  generation,
			ContentHash: contentHash,
			ModifiedAt:  info.ModTime().UTC(),
		},
		spans:  spans,
		source: source,
		drift:  committedHash != "" && committedHash != contentHash,
	}, nil
}

type stripError struct{ offset int }

func (err stripError) Error() string { return "unterminated JSONC comment" }

func stripJSONC(source []byte) ([]byte, *stripError) {
	clean := append([]byte(nil), source...)
	inString := false
	escaped := false
	for index := 0; index < len(clean); index++ {
		if inString {
			if escaped {
				escaped = false
			} else if clean[index] == '\\' {
				escaped = true
			} else if clean[index] == '"' {
				inString = false
			}
			continue
		}
		if clean[index] == '"' {
			inString = true
			continue
		}
		if clean[index] != '/' || index+1 >= len(clean) {
			continue
		}
		if clean[index+1] == '/' {
			for index < len(clean) && clean[index] != '\n' {
				clean[index] = ' '
				index++
			}
			continue
		}
		if clean[index+1] != '*' {
			continue
		}
		start := index
		clean[index], clean[index+1] = ' ', ' '
		index += 2
		closed := false
		for index < len(clean) {
			if clean[index] == '*' && index+1 < len(clean) && clean[index+1] == '/' {
				clean[index], clean[index+1] = ' ', ' '
				index++
				closed = true
				break
			}
			if clean[index] != '\n' && clean[index] != '\r' {
				clean[index] = ' '
			}
			index++
		}
		if !closed {
			return nil, &stripError{offset: start}
		}
	}
	inString = false
	escaped = false
	for index := 0; index < len(clean); index++ {
		if inString {
			if escaped {
				escaped = false
			} else if clean[index] == '\\' {
				escaped = true
			} else if clean[index] == '"' {
				inString = false
			}
			continue
		}
		if clean[index] == '"' {
			inString = true
			continue
		}
		if clean[index] != ',' {
			continue
		}
		next := index + 1
		for next < len(clean) && isJSONSpace(clean[next]) {
			next++
		}
		if next < len(clean) && (clean[next] == '}' || clean[next] == ']') {
			clean[index] = ' '
		}
	}
	return clean, nil
}

func scanJSON(source []byte) (*jsonNode, error) {
	index := 0
	root, err := scanJSONNode(source, &index)
	if err != nil {
		return nil, err
	}
	for index < len(source) && isJSONSpace(source[index]) {
		index++
	}
	if index != len(source) {
		return nil, errors.New("trailing JSON")
	}
	return root, nil
}

func scanJSONNode(source []byte, index *int) (*jsonNode, error) {
	for *index < len(source) && isJSONSpace(source[*index]) {
		*index++
	}
	if *index >= len(source) {
		return nil, errors.New("unexpected end of JSON")
	}
	node := &jsonNode{start: *index}
	switch source[*index] {
	case '{':
		node.object = make(map[string]*jsonNode)
		*index++
		for {
			for *index < len(source) && isJSONSpace(source[*index]) {
				*index++
			}
			if *index >= len(source) {
				return nil, errors.New("unterminated object")
			}
			if source[*index] == '}' {
				*index++
				node.end = *index
				return node, nil
			}
			keyStart := *index
			keyEnd, err := scanJSONString(source, *index)
			if err != nil {
				return nil, err
			}
			var key string
			if err := json.Unmarshal(source[keyStart:keyEnd], &key); err != nil {
				return nil, err
			}
			memberStart := keyStart
			*index = keyEnd
			for *index < len(source) && isJSONSpace(source[*index]) {
				*index++
			}
			if *index >= len(source) || source[*index] != ':' {
				return nil, errors.New("missing object colon")
			}
			*index++
			child, err := scanJSONNode(source, index)
			if err != nil {
				return nil, err
			}
			node.object[key] = child
			node.members = append(node.members, jsonMember{key: key, keyStart: memberStart, value: child})
			for *index < len(source) && isJSONSpace(source[*index]) {
				*index++
			}
			if *index < len(source) && source[*index] == ',' {
				*index++
				continue
			}
		}
	case '[':
		node.array = make([]*jsonNode, 0)
		*index++
		for {
			for *index < len(source) && isJSONSpace(source[*index]) {
				*index++
			}
			if *index >= len(source) {
				return nil, errors.New("unterminated array")
			}
			if source[*index] == ']' {
				*index++
				node.end = *index
				return node, nil
			}
			child, err := scanJSONNode(source, index)
			if err != nil {
				return nil, err
			}
			node.array = append(node.array, child)
			for *index < len(source) && isJSONSpace(source[*index]) {
				*index++
			}
			if *index < len(source) && source[*index] == ',' {
				*index++
				continue
			}
		}
	case '"':
		end, err := scanJSONString(source, *index)
		if err != nil {
			return nil, err
		}
		*index = end
		node.end = end
	default:
		for *index < len(source) && !isJSONSpace(source[*index]) && source[*index] != ',' && source[*index] != ']' && source[*index] != '}' {
			*index++
		}
		node.end = *index
	}
	return node, nil
}

func scanJSONString(source []byte, start int) (int, error) {
	if start >= len(source) || source[start] != '"' {
		return 0, errors.New("expected JSON string")
	}
	escaped := false
	for index := start + 1; index < len(source); index++ {
		if escaped {
			escaped = false
		} else if source[index] == '\\' {
			escaped = true
		} else if source[index] == '"' {
			return index + 1, nil
		}
	}
	return 0, errors.New("unterminated JSON string")
}

func extractDescription(source []byte, node *jsonNode) string {
	if node == nil || node.end > len(source) {
		return ""
	}
	fragment := source[node.start:node.end]
	depth := 0
	collecting := false
	comments := make([]string, 0)
	inString := false
	escaped := false
	for index := 0; index < len(fragment); index++ {
		current := fragment[index]
		if inString {
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			if !collecting && depth == 1 && !isJSONSpace(current) {
				collecting = false
			}
			continue
		}
		if current == '"' {
			if collecting && depth == 1 {
				break
			}
			inString = true
			continue
		}
		if current == '{' || current == '[' {
			depth++
			if collecting && depth == 2 {
				break
			}
			continue
		}
		if current == '}' || current == ']' {
			depth--
			continue
		}
		if depth == 1 && current == '/' && index+1 < len(fragment) {
			start := index
			if fragment[index+1] == '/' {
				index += 2
				for index < len(fragment) && fragment[index] != '\n' {
					index++
				}
				comments = append(comments, string(fragment[start:index]))
				collecting = true
				continue
			}
			if fragment[index+1] == '*' {
				index += 2
				for index+1 < len(fragment) && !(fragment[index] == '*' && fragment[index+1] == '/') {
					index++
				}
				if index+1 < len(fragment) {
					index++
				}
				comments = append(comments, string(fragment[start:index+1]))
				collecting = true
				continue
			}
		}
		if collecting && depth == 1 && !isJSONSpace(current) && current != ',' {
			break
		}
	}
	lines := make([]string, 0, len(comments))
	for _, comment := range comments {
		comment = strings.TrimPrefix(comment, "//")
		comment = strings.TrimPrefix(comment, "/*")
		comment = strings.TrimSuffix(comment, "*/")
		for _, line := range strings.Split(comment, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	return truncate(sanitize(strings.Join(lines, " ")), 1000)
}

func routingConditions(rule map[string]any) []api.RoutingCondition {
	keys := []string{"inboundTag", "domain", "ip", "port", "network", "protocol", "source", "sourcePort", "user", "email", "domainMatcher"}
	conditions := make([]api.RoutingCondition, 0)
	for _, key := range keys {
		value, exists := rule[key]
		if !exists {
			continue
		}
		text := summarizeValue(value)
		if text != "" {
			conditions = append(conditions, api.RoutingCondition{Key: key, Value: sanitize(text)})
		}
	}
	return conditions
}

func summarizeValue(value any) string {
	switch typed := value.(type) {
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := scalarString(item); text != "" {
				items = append(items, text)
			}
		}
		return strings.Join(items, ", ")
	default:
		return scalarString(value)
	}
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return fmt.Sprint(typed)
	default:
		return ""
	}
}

func safeValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitize(typed)
	case json.Number, bool:
		return typed
	}
	return nil
}

func safeString(value any) string { return sanitize(scalarString(value)) }

func sanitize(value string) string {
	value = vlessURI.ReplaceAllString(value, "[redacted URI]")
	value = uuid.ReplaceAllString(value, "[redacted UUID]")
	return secret.ReplaceAllString(value, "$1: [redacted]")
}

func asMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func asSlice(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func stringValues(value any) []string {
	values := make([]string, 0)
	if text, ok := value.(string); ok {
		return []string{sanitize(text)}
	}
	if texts, ok := value.([]string); ok {
		for _, text := range texts {
			values = append(values, sanitize(text))
		}
		return values
	}
	for _, item := range asSlice(value) {
		if text := scalarString(item); text != "" {
			values = append(values, sanitize(text))
		}
	}
	return values
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	default:
		return false
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func pathExists(path string, directory bool) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return value[:maximum]
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

// Overview reads and parses JSONC once. Each projection carries the same
// metadata; no cached document survives the request or hides an external edit.
func (reader Reader) Overview(ctx context.Context) api.XrayOverviewResponse {
	reconcileErr := reader.reconcileState(ctx)
	doc, err := reader.readDocument(ctx)
	result := api.XrayOverviewResponse{Status: statusProjection(reader.Runtime(), doc, err)}
	if reconcileErr != nil && result.Status.ConfigDrift {
		_, result.Status.ConfigError, _ = transaction.ErrorInfo(reconcileErr)
	}
	if err != nil {
		return result
	}
	result.Inbounds = inboundsProjection(doc)
	result.Outbounds = outboundsProjection(doc)
	result.Routing = routingProjection(doc)
	result.Logging = loggingProjection(doc)
	return result
}
