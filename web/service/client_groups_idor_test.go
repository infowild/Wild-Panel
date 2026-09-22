package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// The Groups feature reaches clients by EMAIL across every inbound in the panel, so
// it needs the same scoping the inbound routes get from requireInboundAccess. It did
// not have it: scoping was written for resellers only, and every other non-super
// admin fell through the `!user.IsReseller` guard into an unrestricted view.
//
// These tests pin the admin half of that contract. The reseller half is covered by
// the ownership tests in reseller_security_test.go.

// seedGroupInboundNamed makes one inbound with its own tag/port so several can exist.
func seedGroupInboundNamed(t *testing.T, tag string, port int, clients []map[string]any) *model.Inbound {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"clients": clients})
	if err != nil {
		t.Fatal(err)
	}
	ib := &model.Inbound{
		Remark:   tag,
		Port:     port,
		Protocol: "vless",
		Tag:      tag,
		Enable:   true,
		Settings: string(raw),
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	return ib
}

// seedScopedAdmin creates a non-super admin granted exactly the given inbounds.
func seedScopedAdmin(t *testing.T, username string, inboundIds ...int) *model.User {
	t.Helper()
	u := &model.User{Username: username, Password: "x", Enable: true}
	if err := database.GetDB().Create(u).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range inboundIds {
		if err := database.GetDB().Create(&model.InboundAccess{UserId: u.Id, InboundId: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return u
}

// twoInboundsOneAdmin sets up the shared fixture: two inbounds, one client each, and
// an admin granted only the first.
func twoInboundsOneAdmin(t *testing.T) (svc ClientGroupService, mine, theirs *model.Inbound, admin *model.User) {
	t.Helper()
	initGroupTestDB(t)
	mine = seedGroupInboundNamed(t, "mine", 20001, []map[string]any{
		{"email": "mine@x", "id": "u1", "group": "shared"},
	})
	theirs = seedGroupInboundNamed(t, "theirs", 20002, []map[string]any{
		{"email": "theirs@x", "id": "u2", "group": "shared"},
	})
	admin = seedScopedAdmin(t, "scoped-admin", mine.Id)
	svc = ClientGroupService{Inbound: InboundService{}}
	return svc, mine, theirs, admin
}

func TestGroupEmailsHidesClientsOnUngrantedInbounds(t *testing.T) {
	svc, _, _, admin := twoInboundsOneAdmin(t)

	emails, err := svc.EmailsByGroup(admin, "shared")
	if err != nil {
		t.Fatalf("EmailsByGroup: %v", err)
	}
	for _, e := range emails {
		if strings.EqualFold(e, "theirs@x") {
			t.Fatalf("an admin granted only one inbound was handed %q from the other: %v", e, emails)
		}
	}
	if len(emails) != 1 || !strings.EqualFold(emails[0], "mine@x") {
		t.Fatalf("emails = %v, want exactly [mine@x]", emails)
	}
}

func TestGroupListCountsOnlyGrantedInbounds(t *testing.T) {
	svc, _, _, admin := twoInboundsOneAdmin(t)

	groups, err := svc.ListGroups(admin)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	for _, g := range groups {
		if g.Name != "shared" {
			continue
		}
		if g.ClientCount != 1 {
			t.Fatalf("ClientCount = %d, want 1: the other admin's client is being counted", g.ClientCount)
		}
		return
	}
	t.Fatal("group \"shared\" missing from the scoped listing")
}

func TestGroupBulkAddRefusesAnUngrantedClient(t *testing.T) {
	svc, _, theirs, admin := twoInboundsOneAdmin(t)

	if _, err := svc.AddToGroup(admin, []string{"theirs@x"}, "grabbed"); err == nil {
		t.Fatal("AddToGroup accepted a client on an inbound this admin was never granted")
	}

	// And it must not have written anything on the way to refusing.
	var row model.Inbound
	if err := database.GetDB().First(&row, theirs.Id).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Settings, "grabbed") {
		t.Fatalf("the refused label was written anyway: %s", row.Settings)
	}
}

func TestGroupBulkRemoveRefusesAnUngrantedClient(t *testing.T) {
	svc, _, theirs, admin := twoInboundsOneAdmin(t)

	if _, err := svc.RemoveFromGroup(admin, []string{"theirs@x"}); err == nil {
		t.Fatal("RemoveFromGroup accepted a client on an inbound this admin was never granted")
	}

	var row model.Inbound
	if err := database.GetDB().First(&row, theirs.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.Settings, `"group": "shared"`) && !strings.Contains(row.Settings, `"group":"shared"`) {
		t.Fatalf("the other admin's client lost its label: %s", row.Settings)
	}
}

// A partial-visibility caller may not touch the shared client_groups row at all: a
// rename would relabel only their own members and split the group in two.
func TestGroupRenameRefusedWhenGroupSpansInaccessibleClients(t *testing.T) {
	svc, _, _, admin := twoInboundsOneAdmin(t)

	if _, err := svc.RenameGroup(admin, "shared", "mine-only"); err == nil {
		t.Fatal("RenameGroup let a scoped admin rename a group holding clients they cannot see")
	}
}

// The delete path is where the ordering bug was worst: the shared row went first and
// the scope check came after, so a refusal still left the row destroyed.
func TestGroupDeleteRefusalLeavesTheSharedRowIntact(t *testing.T) {
	svc, _, _, admin := twoInboundsOneAdmin(t)
	// Insert the shared row directly: CreateGroup refuses a name clients already
	// carry, and the fixture's clients carry this one.
	if err := database.GetDB().Create(&model.ClientGroup{Name: "shared"}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.DeleteGroup(admin, "shared"); err == nil {
		t.Fatal("DeleteGroup let a scoped admin delete a group holding clients they cannot see")
	}

	var n int64
	if err := database.GetDB().Model(&model.ClientGroup{}).Where("name = ?", "shared").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("the shared client_groups row was deleted by a call that reported failure")
	}
}

// The baseline ResetGroupTraffic stores is read back as the WHOLE group's, so a caller
// who can only see part of the group must not be able to write it.
func TestGroupResetTrafficRefusedOnPartialVisibility(t *testing.T) {
	svc, _, _, admin := twoInboundsOneAdmin(t)

	if err := svc.ResetGroupTraffic(admin, "shared"); err == nil {
		t.Fatal("ResetGroupTraffic let a scoped admin write a panel-wide baseline from their subset")
	}
}

// The scoped operations must still work normally when the group is entirely theirs.
func TestGroupOperationsStillWorkWithinScope(t *testing.T) {
	initGroupTestDB(t)
	mine := seedGroupInboundNamed(t, "mine", 20001, []map[string]any{
		{"email": "mine@x", "id": "u1", "group": "solo"},
	})
	seedGroupInboundNamed(t, "theirs", 20002, []map[string]any{
		{"email": "theirs@x", "id": "u2", "group": "other"},
	})
	admin := seedScopedAdmin(t, "scoped-admin", mine.Id)
	svc := ClientGroupService{Inbound: InboundService{}}

	if _, err := svc.RenameGroup(admin, "solo", "solo2"); err != nil {
		t.Fatalf("renaming a group that is entirely this admin's must still work: %v", err)
	}
	emails, err := svc.EmailsByGroup(admin, "solo2")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || !strings.EqualFold(emails[0], "mine@x") {
		t.Fatalf("after rename emails = %v, want [mine@x]", emails)
	}
}

// A super admin is unscoped and must keep seeing everything.
func TestGroupSuperAdminStillSeesEveryClient(t *testing.T) {
	svc, _, _, _ := twoInboundsOneAdmin(t)
	super := &model.User{Id: 999, Username: "root", IsSuperAdmin: true}

	emails, err := svc.EmailsByGroup(super, "shared")
	if err != nil {
		t.Fatalf("EmailsByGroup: %v", err)
	}
	if len(emails) != 2 {
		t.Fatalf("super admin saw %v, want both clients", emails)
	}
}
