package domainstore

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMixedResourcePersistenceAndConsolidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resources.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, value := range []string{" Example.COM. ", " 192.0.2.1 ", "192.0.2.1"} {
		if err := store.AddDesired(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := store.BeginConsolidation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeConsolidation(ctx, prepared.Desired); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDesired(ctx, " 192.0.2.1 "); err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Desired: []string{"example.com"}, Baseline: []string{"192.0.2.1", "example.com"}}
	for _, mutate := range []func(context.Context, string) error{store.AddDesired, store.DeleteDesired} {
		for _, value := range []string{"999.0.0.1", "192.168.001.1", "::1", "192.0.2.0/24"} {
			if err := mutate(ctx, value); err == nil {
				t.Fatalf("invalid resource accepted: %q", value)
			}
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed snapshot did not survive reopen: %+v, %v", got, err)
	}
}
