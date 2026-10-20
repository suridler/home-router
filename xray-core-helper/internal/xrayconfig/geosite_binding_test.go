package xrayconfig

import (
	"bytes"
	"strings"
	"testing"
)

const userGeositeBinding = `{
 // keep this comment
 "routing":{"rules":[{"domain":[
 // helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_original.dat:USERLIST\""
 "ext:geosite_original.dat:USERLIST",
 "ext:geosite_unmanaged.dat:OTHER"
 ],"outboundTag":"proxy","unknown":true}]}}
`

func TestUserGeositeBindingPreservesUnmarkedValuesAndCategory(t *testing.T) {
	got, err := BindUserGeosite([]byte(userGeositeBinding), "geosite_custom.dat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSource(got); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"// keep this comment", "ext:geosite_unmanaged.dat:OTHER", `"outboundTag":"proxy","unknown":true`} {
		if !strings.Contains(string(got), keep) {
			t.Fatalf("lost %s", keep)
		}
	}
	if bytes.Contains(got, []byte("geosite_original.dat")) || !bytes.Contains(got, []byte("ext:geosite_custom.dat:USERLIST")) {
		t.Fatal("filename binding failed")
	}
	again, err := BindUserGeosite(got, "geosite_custom.dat")
	if err != nil || !bytes.Equal(got, again) {
		t.Fatal("non-idempotent binding", err)
	}
}

func TestUserGeositeBindingRequiresAnnotationAndSafeName(t *testing.T) {
	raw := []byte(`{"domain":["ext:geosite_IR.dat:USERLIST"]}`)
	got, err := BindUserGeosite(raw, "geosite_user.dat")
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("unmarked reference changed", err)
	}
	for _, name := range []string{"../geosite_user.dat", "geoip_user.dat", "geosite.dat"} {
		if _, err := BindUserGeosite([]byte(userGeositeBinding), name); err == nil {
			t.Fatal("unsafe name", name)
		}
	}
}

func TestUserGeositeBindingIgnoresBlockCommentLocator(t *testing.T) {
	raw := strings.Replace(userGeositeBinding, `"ext:geosite_original.dat:USERLIST",`, `/* "ext:geosite_original.dat:USERLIST" */ "ext:geosite_original.dat:USERLIST",`, 1)
	got, err := BindUserGeosite([]byte(raw), "geosite_user.dat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `/* "ext:geosite_original.dat:USERLIST" */ "ext:geosite_user.dat:USERLIST"`) {
		t.Fatal("block comment was used as scalar")
	}
}
