// Package tiers answers "does this caller hold this tier" for Predirx services. Contract ids are
// supplier, pharmacy, outlet, patient, platform; the live ids (distributor, vendor, agent, user, root)
// stay accepted until the rename sweep finishes, and both spellings are treated as the same tier.
package tiers

import (
	"strings"

	"github.com/factory24/flow-system/pkg/models"
)

const (
	Supplier = "supplier"
	Pharmacy = "pharmacy"
	Outlet   = "outlet"
	Patient  = "patient"
	Platform = "platform"
)

var alias = map[string]string{
	"distributor": Supplier,
	"vendor":      Pharmacy,
	"agent":       Outlet,
	"user":        Patient,
	"root":        Platform,
}

// Normalize maps a live or contract id to the contract id; unknown ids come back lower-cased and unchanged.
func Normalize(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if to, ok := alias[id]; ok {
		return to
	}
	return id
}

// Equal reports whether two ids name the same tier, in either spelling.
func Equal(a, b string) bool {
	return a != "" && b != "" && Normalize(a) == Normalize(b)
}

// Holds reports whether the caller's primary audience is the tier, or the tier is one of its realm roles.
func Holds(claims *models.KeycloakUser, tier string) bool {
	if claims == nil || tier == "" {
		return false
	}
	if Equal(claims.Audience, tier) {
		return true
	}
	for _, r := range claims.RealmAccess.Roles {
		if Equal(r, tier) {
			return true
		}
	}
	return false
}
