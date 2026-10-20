package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/setup"
	"home-router/xray-core-helper/internal/unixhttp"
	"io"
	"net/http"
	"strings"
	"time"
)

func runSetup(arguments []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	config, err := bootstrap.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := unixhttp.NewClient(config.Server.Socket, 6*time.Minute)
	return runSetupWithClient(client, arguments, stdin, stdout, stderr)
}

// runSetupWithClient is shared by the daemon-backed CLI and standalone setup.
// It owns only the wizard/flag protocol; the caller owns transport creation and
// lifecycle.
func runSetupWithClient(client *http.Client, arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options := newSetupArguments(stderr)
	flags := options.flags
	name, address := options.name, options.address
	nonInteractive, replaceXray, adoptExisting := options.nonInteractive, options.replaceXray, options.adoptExisting
	statusOnly, recoverOnly := options.statusOnly, options.recoverOnly
	if err := flags.Parse(arguments[1:]); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	if *statusOnly || *recoverOnly {
		if *recoverOnly {
			var result map[string]bool
			if err := requestJSON(client, http.MethodPost, "/v1/setup/recover", nil, &result); err != nil {
				writeRequestError(stderr, "setup", err)
				return 1
			}
		}
		var status api.SetupStatusResponse
		if err := requestJSON(client, http.MethodGet, "/v1/setup", nil, &status); err != nil {
			writeRequestError(stderr, "setup", err)
			return 1
		}
		return writeJSONCode(stdout, status)
	}
	if *adoptExisting {
		if !*nonInteractive {
			fmt.Fprintln(stderr, "Use interactive setup, or explicitly supply --non-interactive for adoption")
			return 2
		}
		var status api.SetupStatusResponse
		if err := requestJSON(client, http.MethodGet, "/v1/setup", nil, &status); err != nil {
			writeRequestError(stderr, "setup", err)
			return 1
		}
		var result setup.Result
		if err := requestJSON(client, http.MethodPost, "/v1/setup/adopt", map[string]string{"configFingerprint": status.ConfigFingerprint}, &result); err != nil {
			writeRequestError(stderr, "setup", err)
			return 1
		}
		return writeJSONCode(stdout, result)
	}
	if !*nonInteractive && *name == "" && *address == "" {
		return runSetupWizard(client, stdin, stdout, stderr)
	}
	scanner := bufio.NewScanner(stdin)
	plan, err := requestSetupPlanWithPolicy(client, *name, *address, *replaceXray)
	if err != nil && strings.Contains(err.Error(), "interface_required") && !*nonInteractive {
		var choices api.SetupInterfacesResponse
		if requestJSON(client, http.MethodGet, "/v1/setup/interfaces", nil, &choices) != nil {
			return 1
		}
		for _, item := range choices.Interfaces {
			if !item.Excluded {
				fmt.Fprintf(stdout, "%s %s\n", item.Name, strings.Join(item.Addresses, ","))
			}
		}
		fmt.Fprint(stdout, "Interface: ")
		if !scanner.Scan() {
			fmt.Fprintln(stderr, "interface is required")
			return 1
		}
		*name = strings.TrimSpace(scanner.Text())
		plan, err = requestSetupPlanWithPolicy(client, *name, *address, *replaceXray)
	}
	if err != nil && strings.Contains(err.Error(), "address_required") && !*nonInteractive {
		fmt.Fprint(stdout, "IPv4 address: ")
		if scanner.Scan() {
			*address = strings.TrimSpace(scanner.Text())
			plan, err = requestSetupPlanWithPolicy(client, *name, *address, *replaceXray)
		}
	}
	if err != nil {
		writeRequestError(stderr, "setup", err)
		return 1
	}
	encoded, _ := json.MarshalIndent(plan, "", "  ")
	fmt.Fprintln(stdout, string(encoded))
	for _, artifact := range plan.Artifacts {
		if artifact.TemplateID == "nftables" {
			fmt.Fprintln(stderr, "Review the existing nftables file and intended template block locally before setup. Unmarked files receive an appended gateway block; existing bytes are preserved. Rule interactions are unchecked. Transparent routing remains disabled.")
			break
		}
	}
	if !*nonInteractive {
		fmt.Fprint(stdout, "Apply this setup plan? [y/N]: ")
		if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
			fmt.Fprintln(stderr, "setup canceled")
			return 1
		}
	}
	var result setup.Result
	if err = requestJSON(client, http.MethodPost, "/v1/setup/apply", plan, &result); err != nil {
		writeRequestError(stderr, "setup", err)
		return 1
	}
	return writeJSONCode(stdout, result)
}
func requestSetupPlan(client *http.Client, name string, addresses ...string) (setup.Plan, error) {
	var response struct {
		Plan setup.Plan `json:"plan"`
	}
	address := ""
	if len(addresses) > 0 {
		address = addresses[0]
	}
	err := requestJSON(client, http.MethodPost, "/v1/setup/plan", api.SetupPlanRequest{Interface: name, Address: address}, &response)
	return response.Plan, err
}

// Read-only local selection is available before installing or starting a daemon.
// The installer consumes its JSON instead of maintaining a shell selection policy.
func runSetupSelection(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("setup-select", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("interface", "", "LAN interface")
	address := flags.String("address", "", "explicit IPv4 address")
	nonInteractive := flags.Bool("non-interactive", false, "never prompt")
	if flags.Parse(args) != nil || len(flags.Args()) > 0 {
		return 2
	}
	items, err := setup.NewDiscoverer().Discover(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "setup_discovery_unavailable")
		return 1
	}
	input := bufio.NewScanner(stdin)
	for attempt := 0; attempt < 3; attempt++ {
		plan, err := setup.SelectionPlan(items, *name, *address)
		if err == nil {
			return writeJSONCode(stdout, plan)
		}
		if *nonInteractive {
			fmt.Fprintln(stderr, err)
			return 1
		}
		switch err {
		case setup.ErrInterfaceRequired:
			for _, item := range items {
				fmt.Fprintf(stderr, "%s %s (%s)\n", item.Name, strings.Join(item.Addresses, ","), item.Reason)
			}
			fmt.Fprint(stderr, "LAN interface: ")
			if !input.Scan() {
				return 1
			}
			*name = strings.TrimSpace(input.Text())
		case setup.ErrAddressRequired:
			fmt.Fprint(stderr, "IPv4 address: ")
			if !input.Scan() {
				return 1
			}
			*address = strings.TrimSpace(input.Text())
		default:
			fmt.Fprintln(stderr, "invalid_interface")
			return 1
		}
	}
	return 1
}
func writeJSONCode(w io.Writer, value any) int {
	if err := writeJSON(w, value); err != nil {
		return 1
	}
	return 0
}

func requestSetupPlanWithPolicy(client *http.Client, name, address string, replace bool) (setup.Plan, error) {
	var response struct {
		Plan setup.Plan `json:"plan"`
	}
	err := requestJSON(client, http.MethodPost, "/v1/setup/plan", api.SetupPlanRequest{Interface: name, Address: address, ReplaceXray: replace}, &response)
	return response.Plan, err
}

type setupArguments struct {
	flags                                                               *flag.FlagSet
	name, address                                                       *string
	nonInteractive, replaceXray, adoptExisting, statusOnly, recoverOnly *bool
}

func newSetupArguments(stderr io.Writer) setupArguments {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	return setupArguments{flags: flags,
		name:           flags.String("interface", "", "network interface"),
		address:        flags.String("address", "", "IPv4 address, required only when ambiguous"),
		nonInteractive: flags.Bool("non-interactive", false, "apply without prompt"),
		replaceXray:    flags.Bool("replace-xray", false, "explicitly replace existing Xray with the installation template"),
		adoptExisting:  flags.Bool("adopt-existing", false, "explicitly connect existing Xray without config or network changes"),
		statusOnly:     flags.Bool("status", false, "show shared persistent setup operation"),
		recoverOnly:    flags.Bool("recover", false, "explicitly retry setup recovery")}
}
func validSetupArguments(arguments []string, stderr io.Writer) bool {
	if len(arguments) == 0 {
		return false
	}
	options := newSetupArguments(stderr)
	return options.flags.Parse(arguments[1:]) == nil && len(options.flags.Args()) == 0
}
