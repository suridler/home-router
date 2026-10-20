package logfileprep

import (
	"context"
	"errors"
	"testing"
)

func TestSystemIdentityResolverRejectsCanceledContextWithoutLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity, err := NewSystemIdentityResolver().ResolveXrayIdentity(ctx)
	if !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatalf("ResolveXrayIdentity() error = %v; want ErrIdentityUnavailable", err)
	}
	if identity != (Identity{}) {
		t.Fatalf("ResolveXrayIdentity() identity = %#v; want empty", identity)
	}
}
