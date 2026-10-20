// Package xrayidentity resolves Xray's effective socket GID, without starting it.
package xrayidentity

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/commandrunner"
	"os/user"
	"strconv"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("xray_socket_identity_unavailable")

type Resolver struct {
	show  func(context.Context) ([]byte, error)
	user  func(string) (*user.User, error)
	group func(string) (*user.Group, error)
}

func New() *Resolver {
	return &Resolver{show: func(ctx context.Context) ([]byte, error) {
		r, err := commandrunner.New("/usr/bin/systemctl", 10*time.Second)
		if err != nil {
			return nil, ErrUnavailable
		}
		out, err := r.Run(ctx, []string{"show", "--property=User", "--property=Group", "--property=DynamicUser", "xray.service"})
		return out.Stdout(), err
	}, user: func(name string) (*user.User, error) {
		if _, err := strconv.ParseUint(name, 10, 32); err == nil {
			return user.LookupId(name)
		}
		return user.Lookup(name)
	}, group: func(name string) (*user.Group, error) {
		if _, err := strconv.ParseUint(name, 10, 32); err == nil {
			return user.LookupGroupId(name)
		}
		return user.LookupGroup(name)
	}}
}
func (r *Resolver) GID(ctx context.Context) (int, error) {
	_, gid, err := r.resolve(ctx)
	return gid, err
}

// IDs resolves the configured static service user and effective primary group
// from a single systemd snapshot. Supplementary groups do not replace this GID.
func (r *Resolver) IDs(ctx context.Context) (int, int, error) {
	u, gid, err := r.resolve(ctx)
	if err != nil {
		return 0, 0, ErrUnavailable
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil || uid == 1<<32-1 {
		return 0, 0, ErrUnavailable
	}
	return int(uid), gid, nil
}

func (r *Resolver) resolve(ctx context.Context) (*user.User, int, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, 0, ErrUnavailable
	}
	raw, err := r.show(ctx)
	if err != nil || len(raw) > 4096 {
		return nil, 0, ErrUnavailable
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, 0, ErrUnavailable
		}
		if _, exists := values[key]; exists {
			return nil, 0, ErrUnavailable
		}
		if key != "User" && key != "Group" && key != "DynamicUser" {
			return nil, 0, ErrUnavailable
		}
		values[key] = value
	}
	if len(values) != 3 || values["DynamicUser"] != "no" {
		return nil, 0, ErrUnavailable
	}
	var gid string
	name := values["User"]
	if name == "" {
		name = "root"
	}
	u, e := r.user(name)
	if e != nil || u == nil {
		return nil, 0, ErrUnavailable
	}
	if name := values["Group"]; name != "" {
		g, e := r.group(name)
		if e != nil || g == nil {
			return nil, 0, ErrUnavailable
		}
		gid = g.Gid
	} else {
		gid = u.Gid
	}
	n, e := strconv.ParseUint(gid, 10, 32)
	if e != nil || n == 1<<32-1 {
		return nil, 0, ErrUnavailable
	}
	return u, int(n), nil
}
