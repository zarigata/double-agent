package policy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zarigata/double-agent/internal/policy"
)

// TestParseScopesRejectsAnUnknownScope checks that a scope outside of the
// vocabulary is refused.
func TestParseScopesRejectsAnUnknownScope(t *testing.T) {
	cases := [][]string{
		{"instances:read", "instances:teleport"},
		{"admin:all"},
		{"INSTANCES:READ"},
		{"instances read"},
	}
	for _, raw := range cases {
		t.Run(strings.Join(raw, ","), func(t *testing.T) {
			scopes, err := policy.ParseScopes(raw)
			if !errors.Is(err, policy.ErrUnknownScope) {
				t.Fatalf("the scopes %v returned %v, want an unknown scope", raw, err)
			}
			if scopes != nil {
				t.Errorf("the scopes %v were returned with the error: %v", raw, scopes)
			}
		})
	}
}

// TestParseScopesAcceptsTheKnownScopes checks that every documented scope is
// accepted.
func TestParseScopesAcceptsTheKnownScopes(t *testing.T) {
	raw := make([]string, 0, len(policy.AllScopes))
	for _, scope := range policy.AllScopes {
		raw = append(raw, string(scope))
	}
	scopes, err := policy.ParseScopes(raw)
	if err != nil {
		t.Fatalf("parse the scopes: %v", err)
	}
	if len(scopes) != len(policy.AllScopes) {
		t.Errorf("the parser returned %d scopes, want %d", len(scopes), len(policy.AllScopes))
	}
}

// TestParseScopesSortsAndRemovesTheDuplicates checks the normalisation of a
// scope list.
func TestParseScopesSortsAndRemovesTheDuplicates(t *testing.T) {
	scopes, err := policy.ParseScopes([]string{
		"jobs:read", "instances:read", "jobs:read", "  capabilities:read  ", "",
	})
	if err != nil {
		t.Fatalf("parse the scopes: %v", err)
	}
	want := []policy.Scope{policy.ScopeCapabilitiesRead, policy.ScopeInstancesRead, policy.ScopeJobsRead}
	if len(scopes) != len(want) {
		t.Fatalf("the parser returned %v, want %v", scopes, want)
	}
	for i, scope := range scopes {
		if scope != want[i] {
			t.Errorf("the scope %d is %q, want %q", i, scope, want[i])
		}
	}
}

// TestTheScopeSetHonoursTheAdminScope checks that an administrator holds
// every scope.
func TestTheScopeSetHonoursTheAdminScope(t *testing.T) {
	admin := policy.NewScopeSet([]policy.Scope{policy.ScopeAdmin})
	if !admin.IsAdmin() {
		t.Error("the administrator scope was not recognised")
	}
	if !admin.Allows(policy.ScopeInstancesCreate) {
		t.Error("an administrator does not hold the create scope")
	}
	if !admin.Allows(policy.ScopeUsersManage) {
		t.Error("an administrator does not hold the user management scope")
	}
	if !admin.Has(policy.ScopeAdmin) {
		t.Error("the administrator scope is missing from the set")
	}

	user := policy.NewScopeSet([]policy.Scope{policy.ScopeInstancesRead})
	if user.IsAdmin() {
		t.Error("a regular scope set is an administrator")
	}
	if user.Allows(policy.ScopeInstancesCreate) {
		t.Error("a regular scope set holds the create scope")
	}
	if !user.Allows(policy.ScopeInstancesRead) {
		t.Error("the granted scope is not allowed")
	}
}

// TestTheScopeSetListsItsScopesInAStableOrder checks the listing of a scope
// set.
func TestTheScopeSetListsItsScopesInAStableOrder(t *testing.T) {
	set := policy.NewScopeSet([]policy.Scope{policy.ScopeJobsRead, policy.ScopeAdmin, policy.ScopeCapabilitiesRead})
	list := set.List()
	if len(list) != 3 {
		t.Fatalf("the listing holds %d scopes, want 3", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1] > list[i] {
			t.Errorf("the listing is not sorted: %v", list)
			break
		}
	}
}

// TestEvaluateAllowsAndExplains checks the decision and its reason.
func TestEvaluateAllowsAndExplains(t *testing.T) {
	actor := policy.Actor{
		Type:   "user",
		ID:     "usr_1",
		Scopes: policy.NewScopeSet([]policy.Scope{policy.ScopeInstancesRead}),
	}
	decision := policy.Evaluate(actor, policy.ScopeInstancesRead)
	if !decision.Allowed {
		t.Errorf("the granted scope was refused: %s", decision.Reason)
	}
	if decision.Scope != policy.ScopeInstancesRead {
		t.Errorf("the decision carries the scope %q", decision.Scope)
	}
	if decision.Reason != "" {
		t.Errorf("an allowed decision carries the reason %q", decision.Reason)
	}

	denied := policy.Evaluate(actor, policy.ScopeInstancesDestroy)
	if denied.Allowed {
		t.Error("a missing scope was allowed")
	}
	if denied.Scope != policy.ScopeInstancesDestroy {
		t.Errorf("the decision carries the scope %q", denied.Scope)
	}
	if !strings.Contains(denied.Reason, string(policy.ScopeInstancesDestroy)) {
		t.Errorf("the reason %q does not name the missing scope", denied.Reason)
	}
}

// TestEvaluateAllowsEverythingForAnAdministrator checks that the admin scope
// passes every check.
func TestEvaluateAllowsEverythingForAnAdministrator(t *testing.T) {
	actor := policy.Actor{
		Type:   "user",
		ID:     "usr_admin",
		Scopes: policy.NewScopeSet(policy.AdminScopes),
	}
	for _, scope := range policy.AllScopes {
		if !policy.Evaluate(actor, scope).Allowed {
			t.Errorf("an administrator was refused the scope %q", scope)
		}
	}
}

// TestOwnsPermitsTheOwnerAndTheAdministrators checks the ownership rule.
func TestOwnsPermitsTheOwnerAndTheAdministrators(t *testing.T) {
	owner := policy.Actor{
		Type:   "user",
		ID:     "usr_owner",
		Scopes: policy.NewScopeSet(policy.DefaultUserScopes),
	}
	other := policy.Actor{
		Type:   "user",
		ID:     "usr_other",
		Scopes: policy.NewScopeSet(policy.DefaultUserScopes),
	}
	admin := policy.Actor{
		Type:    "user",
		ID:      "usr_admin",
		IsAdmin: true,
		Scopes:  policy.NewScopeSet(policy.AdminScopes),
	}

	if !policy.Owns(owner, "usr_owner") {
		t.Error("the owner does not own its own resource")
	}
	if policy.Owns(other, "usr_owner") {
		t.Error("another user owns the resource")
	}
	if !policy.Owns(admin, "usr_owner") {
		t.Error("an administrator does not own the resource")
	}
	// An actor without an identity owns nothing.
	anonymous := policy.Actor{Type: "user"}
	if policy.Owns(anonymous, "") {
		t.Error("an anonymous actor owns a resource")
	}
	// The admin flag alone is not enough: the scope has to be held too.
	impostor := policy.Actor{Type: "user", ID: "usr_other", IsAdmin: true}
	if policy.Owns(impostor, "usr_owner") {
		t.Error("an actor without the admin scope owns the resource")
	}
}

// TestTheAdminScopesAreASupersetOfTheUserScopes checks the two role lists.
func TestTheAdminScopesAreASupersetOfTheUserScopes(t *testing.T) {
	admin := policy.NewScopeSet(policy.AdminScopes)
	for _, scope := range policy.DefaultUserScopes {
		if !admin.Has(scope) {
			t.Errorf("an administrator does not hold the scope %q", scope)
		}
	}
	if !admin.Has(policy.ScopeUsersManage) {
		t.Error("an administrator does not hold the user management scope")
	}
}
