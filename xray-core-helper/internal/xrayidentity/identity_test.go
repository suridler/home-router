package xrayidentity

import (
	"context"
	"errors"
	"os/user"
	"testing"
)

func TestEffectiveGIDResolution(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
		fail bool
	}{
		{"User=xray\nGroup=egress\nDynamicUser=no\n", 4242, false}, {"User=xray\nGroup=\nDynamicUser=no\n", 1234, false},
		{"User=\nGroup=\nDynamicUser=no\n", 0, false}, {"User=xray\nGroup=\nDynamicUser=yes\n", 0, true},
		{"User=xray\nGroup=unknown\nDynamicUser=no\n", 0, true}, {"User=xray\nGroup=egress\nGroup=other\nDynamicUser=no\n", 0, true},
		{"User=xray\nGroup=egress\n", 0, true},
	} {
		r := &Resolver{show: func(context.Context) ([]byte, error) { return []byte(test.raw), nil }, user: func(name string) (*user.User, error) {
			gid := "1234"
			if name == "root" {
				gid = "0"
			}
			return &user.User{Gid: gid}, nil
		}, group: func(name string) (*user.Group, error) {
			if name != "egress" {
				return nil, errors.New("secret")
			}
			return &user.Group{Gid: "4242"}, nil
		}}
		got, err := r.GID(context.Background())
		if (err != nil) != test.fail || (!test.fail && got != test.want) {
			t.Fatalf("gid=%d err=%v", got, err)
		}
		if err != nil && err.Error() != "xray_socket_identity_unavailable" {
			t.Fatal("identity details leaked")
		}
	}
}

func TestEffectiveUserAndGroupUseOneServiceSnapshot(t *testing.T) {
	calls := 0
	resolver := &Resolver{
		show: func(context.Context) ([]byte, error) {
			calls++
			return []byte("User=nobody\nGroup=egress\nDynamicUser=no\n"), nil
		},
		user: func(name string) (*user.User, error) {
			if name != "nobody" {
				t.Fatalf("looked up fixed account %q", name)
			}
			return &user.User{Uid: "65534", Gid: "65534"}, nil
		},
		group: func(name string) (*user.Group, error) {
			if name != "egress" {
				t.Fatalf("group %q", name)
			}
			return &user.Group{Gid: "4242"}, nil
		},
	}
	uid, gid, err := resolver.IDs(context.Background())
	if err != nil || uid != 65534 || gid != 4242 || calls != 1 {
		t.Fatalf("identity=%d:%d calls=%d err=%v", uid, gid, calls, err)
	}
}
