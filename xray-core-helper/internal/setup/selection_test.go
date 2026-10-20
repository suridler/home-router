package setup

import (
	"errors"
	"testing"
)

func TestSharedSelectionAmbiguityAndExplicitVirtual(t *testing.T) {
	items := []Interface{{Name: "lan0", Addresses: []string{"10.0.0.1/24", "10.0.1.1/24"}}, {Name: "docker0", Addresses: []string{"172.17.0.1/16"}, Excluded: true, Reason: "virtual"}, {Name: "veth-down", Addresses: []string{"10.9.0.1/24"}, Excluded: true, Reason: "down"}}
	if _, err := SelectionPlan(items, "", ""); !errors.Is(err, ErrAddressRequired) {
		t.Fatal(err)
	}
	p, err := SelectionPlan(items, "lan0", "10.0.1.1")
	if err != nil || p.LANNetwork != "10.0.1.0/24" {
		t.Fatal(p, err)
	}
	if _, err = SelectionPlan(items, "docker0", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = SelectionPlan(items, "veth-down", ""); !errors.Is(err, ErrInvalidInterface) {
		t.Fatal(err)
	}
	if _, err = SelectionPlan(items, "lan0", "10.0.2.1"); !errors.Is(err, ErrInvalidInterface) {
		t.Fatal(err)
	}
}
