package routingservice

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt in with a disposable Xray binary. All config/assets/listeners belong to
// this test; installed services, config and runtime API are never contacted.
func TestResourceClientWithIsolatedXray(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set XRAY_TEST_BINARY to run against an isolated Xray process")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("XRAY_TEST_BINARY must be absolute")
	}
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	config := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"RoutingService"}},
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door", "tag": "api-in", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}, map[string]any{"tag": "proxy", "protocol": "blackhole"}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "ruleTag": "api-route", "inboundTag": []string{"api-in"}, "outboundTag": "api"}, map[string]any{"type": "field", "ruleTag": "static", "domain": []string{"full:static.example"}, "outboundTag": "direct"}}},
	}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "xray.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "-config", path)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	client, err := NewClient(binary, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = client.ListRules(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated API unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	payload := resourcePayload(t, []string{"one.example", "two.example"}, []string{"192.0.2.1"})
	for i := 0; i < 2; i++ {
		if err := client.ApplyRouting(ctx, payload); err != nil {
			t.Fatal(err)
		}
	}
	tags, err := client.resourceTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 5 || !tags["api-route"] || !tags["static"] {
		t.Fatalf("unexpected routing tags after append/replay: %v", tags)
	}
	if err := client.ApplyRouting(ctx, resourcePayload(t, []string{"two.example"}, nil)); err != nil {
		t.Fatal(err)
	}
	tags, err = client.resourceTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 3 || !tags["api-route"] || !tags["static"] {
		t.Fatalf("static rules lost or obsolete resources retained: %v", tags)
	}
	if err := client.ApplyRouting(ctx, resourcePayload(t, nil, nil)); err != nil {
		t.Fatal(err)
	}
	tags, err = client.resourceTags(ctx)
	if err != nil || len(tags) != 2 {
		t.Fatalf("empty desired did not retain exactly static rules: %v, %v", tags, err)
	}
}
