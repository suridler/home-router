package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestConfigArgument(t *testing.T) {
	for _, args := range [][]string{
		{"run", "-config", "/etc/xray/active.json"},
		{"-c=/etc/xray/active.json"},
		{"run", "--config=/etc/xray/active.json"},
	} {
		path, err := configArgument(args)
		if err != nil || path != "/etc/xray/active.json" {
			t.Fatalf("valid args: path=%q err=%v", path, err)
		}
	}
	for _, args := range [][]string{
		nil, {"run"}, {"-config"}, {"-config", "relative.json"},
		{"-config", "/etc/xray/../active.json"}, {"-config", "stdin:"},
		{"-config", "/one", "-config", "/two"}, {"-config", "/one", "-c", "/one"},
		{"-confdir", "/etc/xray"}, {"-config", "/one", "-confdir", "/etc/xray"},
		{"-config", "${XRAY_CONFIG}"}, {"-config", "/etc/%i.json"},
		{"-config", "/"}, {"-config", "/etc/secret\n.json"},
	} {
		if _, err := configArgument(args); !errors.Is(err, ErrConfig) {
			t.Fatalf("accepted unsupported arguments: %v", err)
		}
	}
}

func TestResolverUsesEffectiveServiceArguments(t *testing.T) {
	commands := [][]any{{"/usr/local/bin/xray", []string{"/usr/local/bin/xray", "run", "-c", "/etc/xray/drop-in config.json"}, false, 0, 0, 0, 0, 0, 0, 0}}
	var calls []string
	resolver := &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		value := map[string]any{"type": "s", "data": ""}
		switch args[len(args)-1] {
		case "xray.service":
			value = map[string]any{"type": "o", "data": []string{"/org/freedesktop/systemd1/unit/xray_2eservice"}}
		case "ExecStart":
			value = map[string]any{"type": "a(sasbttttuii)", "data": commands}
		}
		return json.Marshal(value)
	}}
	path, err := resolver.ConfigPath(context.Background(), "/usr/local/bin/xray")
	if err != nil || path != "/etc/xray/drop-in config.json" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if len(calls) != 4 || !strings.Contains(calls[0], "LoadUnit s xray.service") {
		t.Fatalf("unexpected queries: %v", calls)
	}
	commands = append(commands, commands[0])
	if _, err := resolver.ConfigPath(context.Background(), "/usr/local/bin/xray"); !errors.Is(err, ErrConfig) {
		t.Fatal("accepted multiple ExecStart commands")
	}
	commands = commands[:1]
	if _, err := resolver.ConfigPath(context.Background(), "/bin/another-xray"); !errors.Is(err, ErrConfig) {
		t.Fatal("accepted a wrapper/different executable")
	}
}

func TestResolverRejectsUnavailableOrMalformedServiceWithoutLeakingOutput(t *testing.T) {
	for _, raw := range []string{"secret service output", `{"type":"s","data":"/some/path"}`, `{"type":"o","data":[]}`, `{"type":"o","data":["/other/unit"]}`} {
		resolver := &Resolver{run: func(context.Context, []string) ([]byte, error) { return []byte(raw), nil }}
		if _, err := resolver.ConfigPath(context.Background(), "/usr/local/bin/xray"); err != ErrConfig {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	resolver := &Resolver{run: func(context.Context, []string) ([]byte, error) { return nil, errors.New("private output") }}
	if _, err := resolver.ConfigPath(context.Background(), "/usr/local/bin/xray"); err != ErrConfig {
		t.Fatalf("unexpected error: %v", err)
	}
}
