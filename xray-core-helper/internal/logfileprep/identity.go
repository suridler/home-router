package logfileprep

import (
	"context"
	"home-router/xray-core-helper/internal/xrayidentity"
)

type systemIdentityResolver struct{}

func NewSystemIdentityResolver() IdentityResolver { return systemIdentityResolver{} }

func (systemIdentityResolver) ResolveXrayIdentity(ctx context.Context) (Identity, error) {
	if ctx == nil || ctx.Err() != nil {
		return Identity{}, ErrIdentityUnavailable
	}
	uid, gid, err := xrayidentity.New().IDs(ctx)
	if err != nil {
		return Identity{}, ErrIdentityUnavailable
	}
	identity := Identity{UID: uid, GID: gid}
	if !validIdentity(identity) {
		return Identity{}, ErrInvalidIdentity
	}
	return identity, nil
}
