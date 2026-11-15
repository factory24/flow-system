package tiers

import (
	"testing"

	"github.com/factory24/flow-system/pkg/models"
)

func TestNormalizeAcceptsBothSpellings(t *testing.T) {
	for in, want := range map[string]string{"vendor": Pharmacy, "Pharmacy": Pharmacy, "distributor": Supplier, "agent": Outlet, "user": Patient, "root": Platform, "supplier": Supplier, "x": "x"} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHolds(t *testing.T) {
	c := &models.KeycloakUser{Audience: "vendor"}
	c.RealmAccess.Roles = []string{"supplier"}
	for _, tier := range []string{Pharmacy, "vendor", Supplier, "distributor"} {
		if !Holds(c, tier) {
			t.Errorf("should hold %s", tier)
		}
	}
	if Holds(c, Platform) || Holds(c, Patient) || Holds(nil, Pharmacy) || Holds(c, "") {
		t.Error("held a tier it does not have")
	}
}
