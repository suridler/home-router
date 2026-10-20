// Package filemetadata defines destination metadata separately from private
// working templates. Backup restoration still uses the recorded original owner.
package filemetadata

import (
	"errors"
	"os"
	"os/user"
	"strconv"

	"home-router/xray-core-helper/internal/templatecatalog"
)

type Metadata struct {
	Mode     os.FileMode
	UID, GID int
}

func Destination(id templatecatalog.ID) (Metadata, error) {
	m := Metadata{Mode: 0644, UID: 0, GID: 0}
	group := ""
	switch id {
	case templatecatalog.XrayConfig:
		m.Mode = 0640
		group = "xray"
	case templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit:
	default:
		return Metadata{}, errors.New("unknown destination metadata")
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return Metadata{}, errors.New("destination group unavailable")
		}
		gid, err := strconv.Atoi(g.Gid)
		if err != nil {
			return Metadata{}, errors.New("destination group invalid")
		}
		m.GID = gid
	}
	return m, nil
}

// Working matches the private root-owned template files declared by tmpfiles.
// The daemon primary group is for socket access, not template ownership.
func Working() Metadata {
	return Metadata{Mode: 0600, UID: 0, GID: 0}
}
