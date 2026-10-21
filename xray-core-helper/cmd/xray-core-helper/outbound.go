package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/unixhttp"
)

// Credentials are read from stdin, never from command-line arguments.
func runOutbound(args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "outbound: list | get TAG | references TAG | import | create | update TAG | replace TAG | delete TAG")
		return 2
	}
	operation := args[0]
	rest := args[1:]
	tag := ""
	switch operation {
	case "get", "references", "update", "replace", "delete":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "outbound: tag is required")
			return 2
		}
		tag = rest[0]
		rest = rest[1:]
	case "list", "import", "create":
	default:
		fmt.Fprintln(stderr, "outbound: unsupported command")
		return 2
	}
	flags := flag.NewFlagSet("outbound", flag.ContinueOnError)
	flags.SetOutput(stderr)
	generation := flags.Uint64("expected-generation", 0, "displayed configuration generation (required for mutations)")
	restart := flags.Bool("restart", false, "restart Xray after validation")
	confirm := flags.Bool("confirm-replacement", false, "explicitly discard the previous connection except tag and helper bindings")
	if flags.Parse(rest) != nil || len(flags.Args()) != 0 {
		return 2
	}
	mutation := operation == "create" || operation == "update" || operation == "replace" || operation == "delete"
	hasGeneration := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "expected-generation" {
			hasGeneration = true
		}
	})
	if mutation && !hasGeneration {
		fmt.Fprintln(stderr, "outbound: --expected-generation is required")
		return 2
	}
	if operation == "replace" && !*confirm {
		fmt.Fprintln(stderr, "outbound: --confirm-replacement is required")
		return 2
	}
	if operation != "replace" && *confirm {
		fmt.Fprintln(stderr, "outbound: confirmation is only valid for replacement")
		return 2
	}
	if !mutation && (hasGeneration || *restart) {
		fmt.Fprintln(stderr, "outbound: mutation flags are not valid for this command")
		return 2
	}
	method, path := http.MethodGet, "/v1/xray/outbounds"
	var body any
	if tag != "" {
		path += "/" + url.PathEscape(tag)
	}
	if operation == "references" {
		path += "/references"
	}
	if operation == "import" || operation == "create" || operation == "update" || operation == "replace" {
		raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20+1))
		if err != nil || len(raw) > 1<<20 || len(bytes.TrimSpace(raw)) == 0 {
			fmt.Fprintln(stderr, "outbound: valid bounded input on stdin is required")
			return 2
		}
		if operation == "import" {
			method, path = http.MethodPost, "/v1/xray/outbound-drafts/import"
			body = api.OutboundImportRequest{URI: strings.TrimSpace(string(raw))}
		} else {
			if !json.Valid(raw) || bytes.TrimSpace(raw)[0] != '{' {
				fmt.Fprintln(stderr, "outbound: one native outbound JSON object on stdin is required")
				return 2
			}
			method = http.MethodPost
			if operation == "create" {
				path = "/v1/xray/outbound-drafts/apply"
			} else if operation == "update" {
				method = http.MethodPut
			} else {
				path += "/replace"
			}
			body = api.OutboundWriteRequest{MutationRequest: api.MutationRequest{ExpectedGeneration: generation, Restart: *restart}, Outbound: json.RawMessage(raw), ConfirmReplacement: *confirm}
		}
	}
	if operation == "delete" {
		method = http.MethodDelete
		body = api.MutationRequest{ExpectedGeneration: generation, Restart: *restart}
	}
	cfg, err := bootstrap.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := unixhttp.NewClient(cfg.Server.Socket, 20*time.Minute)
	var response any = new(api.MutationResponse)
	switch operation {
	case "list":
		response = new(api.OutboundsResponse)
	case "get":
		response = new(api.OutboundEditResponse)
	case "references":
		response = new(api.OutboundReferencesResponse)
	case "import":
		response = new(api.OutboundImportResponse)
	}
	if err := requestJSON(client, method, path, body, response); err != nil {
		writeRequestError(stderr, "outbound", err)
		return 1
	}
	return writeJSONCode(stdout, response)
}
