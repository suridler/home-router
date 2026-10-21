package xrayconfig

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUserlistRoutingAssetsRequiresPermanentProxyReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{ // DNS alone must not activate a routing baseline.
 "dns":{"servers":[{"domains":["ext:dns_only.dat:USERLIST"]}]},
 "routing":{"rules":[
 {"ruleTag":"proxy-domains-permanent","outboundTag":"proxy","domain":["ext:renamed.dat:USERLIST", "ext:wrong_category.dat:OTHER"]},
 {"ruleTag":"proxy-ip-permanent","outboundTag":"direct","ip":["ext:geoip_user.dat:USERLIST"]},
 {"ruleTag":"other","outboundTag":"proxy","domain":["ext:other.dat:USERLIST"]}
 ]}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := NewReader(Settings{Config: path}).UserlistRoutingAssets(context.Background())
	if err != nil || !reflect.DeepEqual(got, map[string]bool{"renamed.dat": true}) {
		t.Fatalf("assets=%v err=%v", got, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("reader changed config")
	}
}
