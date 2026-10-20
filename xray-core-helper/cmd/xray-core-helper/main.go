package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"home-router/xray-core-helper/internal/gatewaycheck"
	"home-router/xray-core-helper/internal/geodatalimits"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/unixhttp"
)

var version = "dev"

const maxAPIResponseBytes = 1 << 20

var errDaemonUnavailable = errors.New("helper daemon unavailable")
var errInvalidAPIResponse = errors.New("invalid API response")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("xray-core-helper", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", bootstrap.DefaultConfigPath, "path to helper TOML config")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	arguments := flags.Args()
	if len(arguments) > 0 && arguments[0] == "install" {
		return runInstall(arguments[1:], *configPath, os.Stdin, stdout, stderr)
	}
	if len(arguments) > 0 && arguments[0] == "update" {
		return runUpdate(arguments[1:], *configPath, stdout, stderr)
	}
	if len(arguments) == 1 && arguments[0] == "version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(arguments) == 0 {
		writeUsage(stderr)
		return 2
	}
	if len(arguments) > 0 && arguments[0] == "network-probe" {
		if len(arguments) > 2 || (len(arguments) == 2 && arguments[1] != "status") {
			writeUsage(stderr)
			return 2
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 45*time.Second)
		var response any
		method := http.MethodPost
		var body any = api.NetworkProbeRequest{Stage: "manual"}
		if len(arguments) == 2 {
			method = http.MethodGet
			body = nil
		}
		if err := requestJSON(client, method, "/v1/network/probe", body, &response); err != nil {
			writeRequestError(stderr, "network-probe", err)
			return 1
		}
		return writeJSONCode(stdout, response)
	}
	if len(arguments) == 1 && arguments[0] == "network-check" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := gatewaycheck.Wait(ctx, *configPath); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSONCode(stdout, map[string]string{"status": "ready", "forwarding": "unverified"})
	}
	if len(arguments) > 0 && arguments[0] == "discover-xray" {
		return runDiscovery(arguments[1:], *configPath, stdout, stderr)
	}
	if len(arguments) == 1 && arguments[0] == "installation-settings" {
		cfg, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSONCode(stdout, struct {
			Socket string `json:"socket"`
		}{cfg.Server.Socket})
	}
	if len(arguments) > 0 && arguments[0] == "setup-select" {
		return runSetupSelection(arguments[1:], os.Stdin, stdout, stderr)
	}
	if len(arguments) >= 1 && arguments[0] == "errors" {
		if len(arguments) > 2 || (len(arguments) == 2 && arguments[1] != "collect") {
			writeUsage(stderr)
			return 2
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 20*time.Second)
		var response api.ErrorHistoryResponse
		method, path := http.MethodGet, "/v1/errors"
		var body any
		if len(arguments) == 2 {
			method, path, body = http.MethodPost, "/v1/errors/collect", struct{}{}
		}
		if err := requestJSON(client, method, path, body, &response); err != nil {
			writeRequestError(stderr, "errors", err)
			return 1
		}
		if writeJSON(stdout, response) != nil {
			return 1
		}
		return 0
	}
	if len(arguments) == 1 && arguments[0] == "health" {
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 5*time.Second)
		var response api.HealthResponse
		if err := requestJSON(client, http.MethodGet, "/v1/health", nil, &response); err != nil {
			writeRequestError(stderr, "xray-core-helper", err)
			return 1
		}
		if err := writeJSON(stdout, response); err != nil {
			fmt.Fprintln(stderr, "could not write command output")
			return 1
		}
		return 0
	}
	if len(arguments) > 0 && arguments[0] == "setup" {
		return runStandaloneSetup(arguments, *configPath, os.Stdin, stdout, stderr)
	}
	if len(arguments) > 0 && arguments[0] == "safe-apply" {
		return runSafeApply(arguments, *configPath, stdout, stderr)
	}

	if len(arguments) == 1 && (arguments[0] == "validate" || arguments[0] == "diagnostics") {
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 15*time.Second)
		if err := runOperation(client, arguments[0], nil, stdout); err != nil {
			writeRequestError(stderr, arguments[0], err)
			return 1
		}
		return 0
	}

	if len(arguments) == 2 && arguments[0] == "templates" && (arguments[1] == "status" || arguments[1] == "refresh") {
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 30*time.Second)
		method, path := http.MethodGet, "/v1/templates"
		if arguments[1] == "refresh" {
			method, path = http.MethodPost, "/v1/templates/refresh"
		}
		var response api.TemplateStatusResponse
		if err := requestJSON(client, method, path, nil, &response); err != nil {
			writeRequestError(stderr, "templates", err)
			return 1
		}
		if err := writeJSON(stdout, response); err != nil {
			fmt.Fprintln(stderr, "could not write command output")
			return 1
		}
		return 0
	}

	if arguments[0] == "templates" && len(arguments) >= 2 && arguments[1] == "accept-system" {
		if len(arguments) != 7 || arguments[3] != "--expected-generation" || arguments[5] != "--expected-revision" || arguments[6] == "" {
			writeUsage(stderr)
			return 2
		}
		generation, err := strconv.ParseUint(arguments[4], 10, 64)
		if err != nil {
			writeUsage(stderr)
			return 2
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 30*time.Second)
		path := "/v1/templates/" + url.PathEscape(arguments[2]) + "/accept-system"
		request := api.AcceptSystemRequest{ExpectedGeneration: &generation, ExpectedRevision: arguments[6]}
		var response api.AcceptSystemResponse
		if err := requestJSON(client, http.MethodPost, path, request, &response); err != nil {
			writeRequestError(stderr, "templates", err)
			return 1
		}
		if err := writeJSON(stdout, response); err != nil {
			fmt.Fprintln(stderr, "could not write command output")
			return 1
		}
		return 0
	}

	if arguments[0] == "templates" && len(arguments) >= 2 && arguments[1] == "apply" {
		return runApplyTemplate(arguments, *configPath, os.Stdin, stdout, stderr)
	}

	if arguments[0] == "templates" && len(arguments) >= 2 && (arguments[1] == "backups" || arguments[1] == "restore") {
		valid := arguments[1] == "backups" && len(arguments) == 3
		var generation uint64
		if arguments[1] == "restore" && len(arguments) == 6 && arguments[4] == "--expected-generation" {
			parsed, err := strconv.ParseUint(arguments[5], 10, 64)
			if err == nil {
				generation = parsed
				valid = true
			}
		}
		if !valid {
			writeUsage(stderr)
			return 2
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 30*time.Second)
		path := "/v1/templates/" + url.PathEscape(arguments[2]) + "/backups"
		if arguments[1] == "backups" {
			var response api.TemplateBackupsResponse
			if err := requestJSON(client, http.MethodGet, path, nil, &response); err != nil {
				writeRequestError(stderr, "templates", err)
				return 1
			}
			if err := writeJSON(stdout, response); err != nil {
				fmt.Fprintln(stderr, "could not write command output")
				return 1
			}
			return 0
		}
		path += "/" + url.PathEscape(arguments[3]) + "/restore"
		var response api.RestoreTemplateBackupResponse
		request := api.RestoreTemplateBackupRequest{ExpectedGeneration: &generation}
		if err := requestJSON(client, http.MethodPost, path, request, &response); err != nil {
			writeRequestError(stderr, "templates", err)
			return 1
		}
		if err := writeJSON(stdout, response); err != nil {
			fmt.Fprintln(stderr, "could not write command output")
			return 1
		}
		return 0
	}

	if len(arguments) < 2 {
		writeUsage(stderr)
		return 2
	}
	if arguments[0] == "service" {
		if len(arguments) != 3 || !validServiceAlias(arguments[1]) || !validServiceAction(arguments[2]) {
			writeUsage(stderr)
			return 2
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 15*time.Second)
		if err := runOperation(client, "service", arguments[1:], stdout); err != nil {
			writeRequestError(stderr, "service", err)
			return 1
		}
		return 0
	}
	if arguments[0] == "logs" {
		if len(arguments) != 2 && len(arguments) != 4 {
			writeUsage(stderr)
			return 2
		}
		if arguments[1] != "access" && arguments[1] != "error" {
			writeUsage(stderr)
			return 2
		}
		limit := 100
		if len(arguments) == 4 {
			parsed, err := strconv.Atoi(arguments[3])
			if arguments[2] != "--limit" || err != nil || parsed < 1 || parsed > 200 {
				writeUsage(stderr)
				return 2
			}
			limit = parsed
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		client := unixhttp.NewClient(config.Server.Socket, 15*time.Second)
		if err := runOperation(client, "logs", []string{arguments[1], strconv.Itoa(limit)}, stdout); err != nil {
			writeRequestError(stderr, "logs", err)
			return 1
		}
		return 0
	}
	if arguments[0] == "geodata" {
		command := arguments[1]
		switch command {
		case "settings", "status":
			if len(arguments) != 2 {
				writeUsage(stderr)
				return 2
			}
		case "update":
			if len(arguments) != 2 && (len(arguments) != 4 || arguments[2] != "--idempotency-key" ||
				!api.ValidIdempotencyKey(arguments[3])) {
				writeUsage(stderr)
				return 2
			}
		default:
			writeUsage(stderr)
			return 2
		}
		config, err := bootstrap.Load(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		timeout := 5 * time.Second
		if command == "update" {
			timeout = 15 * time.Minute
		}
		client := unixhttp.NewClient(config.Server.Socket, timeout)
		key := ""
		if command == "update" {
			if len(arguments) == 4 {
				key = arguments[3]
			} else {
				var err error
				key, err = newIdempotencyKey()
				if err != nil {
					fmt.Fprintln(stderr, "could not generate geodata update key")
					return 1
				}
			}
		}
		if err := runGeodata(client, command, key, stdout); err != nil {
			writeRequestError(stderr, "geodata", err)
			return 1
		}
		return 0
	}
	if arguments[0] != "proxy-domain" {
		writeUsage(stderr)
		return 2
	}
	command := arguments[1]
	switch command {
	case "list":
		if len(arguments) != 2 {
			writeUsage(stderr)
			return 2
		}
	case "add", "delete":
		if len(arguments) != 3 || arguments[2] == "" {
			writeUsage(stderr)
			return 2
		}
	case "apply", "reconcile":
		if len(arguments) != 2 {
			writeUsage(stderr)
			return 2
		}
	default:
		writeUsage(stderr)
		return 2
	}

	config, err := bootstrap.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	timeout := 5 * time.Second
	if command == "reconcile" {
		timeout = 20 * time.Second
	}
	client := unixhttp.NewClient(config.Server.Socket, timeout)
	if err := runProxyDomain(client, command, arguments[2:], stdout); err != nil {
		writeProxyDomainError(stderr, err)
		return 1
	}
	return 0
}

func newIdempotencyKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "cli-" + hex.EncodeToString(value[:]), nil
}

func runOperation(client *http.Client, command string, arguments []string, stdout io.Writer) error {
	switch command {
	case "validate":
		var response api.ValidationResponse
		if err := requestJSON(client, http.MethodPost, "/v1/xray/config/validate", struct{}{}, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "diagnostics":
		var response api.DiagnosticsResponse
		if err := requestJSON(client, http.MethodGet, "/v1/xray/diagnostics", nil, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "service":
		var response api.ServiceActionResponse
		path := "/v1/xray/services/" + arguments[0] + "/" + arguments[1]
		if err := requestJSON(client, http.MethodPost, path, struct{}{}, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "logs":
		var response api.LogReadResponse
		path := "/v1/logs/" + arguments[0] + "?limit=" + arguments[1]
		if err := requestJSON(client, http.MethodGet, path, nil, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	default:
		return errors.New("unsupported operation command")
	}
}

func validServiceAlias(value string) bool {
	return value == "xray" || value == "tproxy"
}

func validServiceAction(value string) bool {
	return value == "start" || value == "stop" || value == "restart"
}

func runGeodata(client *http.Client, command, key string, stdout io.Writer) error {
	switch command {
	case "settings":
		var response api.VersionedGeodataSettings
		if err := requestJSON(client, http.MethodGet, "/v1/xray/geodata/settings", nil, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "status":
		var response api.GeodataStatusResponse
		if err := requestJSON(client, http.MethodGet, "/v1/xray/geodata/update/status", nil, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "update":
		var response api.GeodataUpdateResponse
		if err := requestJSONWithHeaders(client, http.MethodPost, "/v1/xray/geodata/update", nil,
			map[string]string{"Idempotency-Key": key}, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	default:
		return errors.New("unsupported geodata command")
	}
}

func runProxyDomain(client *http.Client, command string, arguments []string, stdout io.Writer) error {
	switch command {
	case "reconcile":
		var response api.ApplyProxyDomainsResponse
		if err := requestJSON(client, http.MethodPost, "/v1/proxy-domains/reconcile", struct{}{}, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "list":
		var response api.ProxyDomainsResponse
		if err := requestJSON(client, http.MethodGet, "/v1/proxy-domains", nil, &response); err != nil {
			return err
		}
		return writeJSON(stdout, response)
	case "add":
		request := api.AddProxyDomainRequest{Domain: arguments[0]}
		var response api.ProxyDomainMutationResponse
		if err := requestJSON(client, http.MethodPost, "/v1/proxy-domains", request, &response); err != nil {
			return err
		}
		return writeMutationResult(stdout, response)
	case "delete":
		path := "/v1/proxy-domains/" + url.PathEscape(arguments[0])
		var response api.ProxyDomainMutationResponse
		if err := requestJSON(client, http.MethodDelete, path, nil, &response); err != nil {
			return err
		}
		return writeMutationResult(stdout, response)
	case "apply":
		var response api.ApplyProxyDomainsResponse
		if err := requestJSON(client, http.MethodPost, "/v1/proxy-domains/apply", struct{}{}, &response); err != nil {
			return err
		}
		if response.Applied {
			_, err := fmt.Fprintln(stdout, "Proxy-domain runtime synchronized.")
			return err
		}
		_, err := fmt.Fprintln(stdout, "Proxy-domain runtime synchronization is pending.")
		return err
	default:
		return errors.New("unsupported proxy-domain command")
	}
}

func requestJSON(client *http.Client, method, path string, requestBody, responseBody any) error {
	return requestJSONWithHeaders(client, method, path, requestBody, nil, responseBody)
}

func requestJSONWithHeaders(client *http.Client, method, path string, requestBody any, headers map[string]string, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return errors.New("could not encode request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, "http://helper"+path, body)
	if err != nil {
		return errors.New("could not create request")
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return errDaemonUnavailable
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil || len(payload) > maxAPIResponseBytes {
		return errInvalidAPIResponse
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return apiErrorFromResponse(response.StatusCode, payload)
	}
	if err := decodeStrictJSON(payload, responseBody); err != nil {
		return errInvalidAPIResponse
	}
	return nil
}

func apiErrorFromResponse(status int, payload []byte) error {
	var response api.ErrorResponse
	if err := decodeStrictJSON(payload, &response); err != nil {
		return &requestAPIError{status: status, code: "api_error"}
	}
	code := response.Error.Code
	if !isSafeErrorCode(code) {
		code = "api_error"
	}
	return &requestAPIError{status: status, code: code, maxAssetSizeMB: response.Error.MaxAssetSizeMB}
}

func decodeStrictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing data")
	}
	return nil
}

func isSafeErrorCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' {
			return false
		}
	}
	return true
}

type requestAPIError struct {
	maxAssetSizeMB int
	status         int
	code           string
}

func (requestError *requestAPIError) Error() string {
	return fmt.Sprintf("HTTP %d %s", requestError.status, requestError.code)
}

func writeRequestError(stderr io.Writer, command string, err error) {
	if errors.Is(err, errDaemonUnavailable) {
		fmt.Fprintf(stderr, "%s: helper daemon unavailable\n", command)
		return
	}
	var apiError *requestAPIError
	if errors.As(err, &apiError) {
		if apiError.code == "geodata_asset_too_large" && geodatalimits.ValidMB(apiError.maxAssetSizeMB) {
			fmt.Fprintf(stderr, "%s: %s (HTTP %d)\n", command, (&geodatalimits.Exceeded{LimitBytes: geodatalimits.Bytes(apiError.maxAssetSizeMB)}).Error(), apiError.status)
		} else {
			fmt.Fprintf(stderr, "%s: %s (HTTP %d)\n", command, apiError.code, apiError.status)
		}
		return
	}
	if errors.Is(err, errInvalidAPIResponse) {
		fmt.Fprintf(stderr, "%s: invalid response from helper\n", command)
		return
	}
	fmt.Fprintf(stderr, "%s: request failed\n", command)
}

// writeProxyDomainError keeps the stable error code and HTTP status, and adds a
// fixed explanation so raw helper messages are never printed. A runtime failure
// after a mutation is ambiguous: state may already be saved, so the operator is
// asked to re-read it instead of being told the change was reverted.
func writeProxyDomainError(stderr io.Writer, err error) {
	const unavailableExplanation = "the change may already be saved, but runtime synchronization failed. Re-read the list before retrying."
	var apiError *requestAPIError
	if errors.As(err, &apiError) {
		switch apiError.code {
		case "invalid_domain":
			fmt.Fprintf(stderr, "proxy-domain: invalid_domain (HTTP %d): enter a domain name or IPv4 address; IPv6, CIDR and URLs are not supported.\n", apiError.status)
			return
		case "domain_runtime_sync_failed", "domain_runtime_apply_failed":
			fmt.Fprintf(stderr, "proxy-domain: %s (HTTP %d): %s\n", apiError.code, apiError.status, unavailableExplanation)
			return
		}
	}
	writeRequestError(stderr, "proxy-domain", err)
}

func writeMutationResult(stdout io.Writer, response api.ProxyDomainMutationResponse) error {
	state := "runtime synchronization pending"
	if response.Applied {
		state = "applied"
	}
	_, err := fmt.Fprintf(stdout, "Resource %s: %s.\n", strconv.Quote(response.Domain), state)
	return err
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: xray-core-helper [-config PATH] health")
	fmt.Fprintln(writer, "       xray-core-helper version | install [--version TAG | --bundle ROOT | --recover] [--with-web|--without-web]")
	fmt.Fprintln(writer, "       xray-core-helper update check|status|prepare [TAG]")
	fmt.Fprintln(writer, "       xray-core-helper update apply|retry|rollback OPERATION_ID ACTION_ID")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] setup [--interface NAME] [--non-interactive]")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] validate")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] diagnostics")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] templates {status|refresh}")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] templates backups TEMPLATE_ID")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] templates restore TEMPLATE_ID BACKUP_ID --expected-generation N")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] templates accept-system TEMPLATE_ID --expected-generation N --expected-revision REVISION")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] templates apply TEMPLATE_ID --expected-generation N [-force]")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] safe-apply {status|rollback-pending}")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] network-probe [status]")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] safe-apply {confirm|rollback|cancel-reboot} TRANSACTION_ID")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] safe-apply cancel-reboot TRANSACTION_ID --offline")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] service {xray|tproxy} {start|stop|restart}")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] errors [collect]")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] logs {access|error} [--limit N]")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] geodata settings")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] geodata status")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] geodata update [--idempotency-key KEY]")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] proxy-domain list")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] proxy-domain add DOMAIN|IPV4")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] proxy-domain delete DOMAIN|IPV4")
	fmt.Fprintln(writer, "       xray-core-helper [-config PATH] proxy-domain apply|reconcile")
}
