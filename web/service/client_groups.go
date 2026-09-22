package service

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"gorm.io/gorm"
)

// GroupSummary is one row on the Groups page / ListGroups API (Sanaei shape).
type GroupSummary struct {
	Name        string `json:"name"`
	ClientCount int    `json:"clientCount"`
	TrafficUsed int64  `json:"trafficUsed"`
	Up          int64  `json:"up"`
	Down        int64  `json:"down"`
}

// ClientGroupService manages client group labels stored on inbound settings JSON
// plus placeholder rows in client_groups (Sanaei parity without a clients table).
type ClientGroupService struct {
	Inbound InboundService
}

type clientGroupMember struct {
	Email string
	Group string
	Up    int64
	Down  int64
}

// groupScope is what one caller may see and touch in the Groups feature.
//
// Two different scopes exist because the panel has two different kinds of limited
// operator, and the Groups feature reaches BOTH of them:
//
//   - a plain admin is scoped by INBOUND GRANT (model.InboundAccess). Everything on a
//     granted inbound is theirs to see; anything unticked "does not exist" for them.
//   - a reseller is scoped by ACCOUNT OWNERSHIP. They see only the clients they sold,
//     even on an inbound they share with an admin, so the inbound grant they also hold
//     is too wide and the email set is the only scope that means anything.
//
// A nil set means UNRESTRICTED and is only ever produced for a super admin. Every other
// caller gets a non-nil (possibly empty) set, so a scope that could not be resolved
// fails CLOSED rather than degrading to "see everything" — which is exactly how this
// feature leaked every panel-wide client email to any admin holding PermAccessInbounds.
type groupScope struct {
	// inboundIds limits which inbound rows are read and written. nil = every inbound.
	inboundIds map[int]struct{}
	// emails limits which accounts count, already lower-cased. nil = every account.
	emails map[string]struct{}
}

// unrestrictedScope is the super-admin scope, and the scope for the panel-wide
// uniqueness questions (does this group name already exist anywhere?) that must be
// answered over the whole table whoever is asking.
func unrestrictedScope() groupScope { return groupScope{} }

// allowsInbound reports whether this scope covers that inbound row.
func (g groupScope) allowsInbound(id int) bool {
	if g.inboundIds == nil {
		return true
	}
	_, ok := g.inboundIds[id]
	return ok
}

// allowsEmail reports whether this scope covers that account. The key is folded here
// rather than at each call site: one that forgets matches nothing, which fails closed
// but is indistinguishable from an operator who owns nothing, and so would ship.
func (g groupScope) allowsEmail(email string) bool {
	if g.emails == nil {
		return true
	}
	_, ok := g.emails[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

// scanMembers walks the inbounds this scope covers and joins client_traffics.
func (s *ClientGroupService) scanMembers(scope groupScope) ([]clientGroupMember, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	if err := db.Model(&model.Inbound{}).Find(&inbounds).Error; err != nil {
		return nil, err
	}

	var traffics []xray.ClientTraffic
	if err := db.Model(&xray.ClientTraffic{}).Find(&traffics).Error; err != nil {
		return nil, err
	}
	byEmail := make(map[string]xray.ClientTraffic, len(traffics))
	for _, t := range traffics {
		byEmail[strings.ToLower(strings.TrimSpace(t.Email))] = t
	}

	seen := make(map[string]clientGroupMember)
	for _, ib := range inbounds {
		if !scope.allowsInbound(ib.Id) {
			continue
		}
		clients, err := s.Inbound.GetClients(ib)
		if err != nil {
			continue
		}
		for _, c := range clients {
			email := strings.TrimSpace(c.Email)
			if email == "" {
				continue
			}
			key := strings.ToLower(email)
			if !scope.allowsEmail(key) {
				continue
			}
			group := strings.TrimSpace(c.Group)
			t := byEmail[key]
			// Prefer the first non-empty group if the same email appears on
			// multiple inbounds (Wild Panel emails are unique in practice).
			if prev, ok := seen[key]; ok {
				if prev.Group == "" && group != "" {
					prev.Group = group
					seen[key] = prev
				}
				continue
			}
			seen[key] = clientGroupMember{
				Email: email,
				Group: group,
				Up:    t.Up,
				Down:  t.Down,
			}
		}
	}
	out := make([]clientGroupMember, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	return out, nil
}

// scopeFor resolves what this caller may see and touch. See groupScope for why the
// two roles are scoped on different axes.
//
// Fails CLOSED in every direction: a nil user and an unreadable grant/ownership table
// both produce an EMPTY (non-nil) scope, which shows nothing and authorizes nothing.
// The previous version returned a nil (= unrestricted) set for everyone who was not a
// reseller, which silently handed every plain admin the whole panel.
func (s *ClientGroupService) scopeFor(user *model.User) (groupScope, error) {
	if user == nil {
		return groupScope{inboundIds: map[int]struct{}{}, emails: map[string]struct{}{}}, nil
	}
	if user.IsSuperAdmin {
		return unrestrictedScope(), nil
	}
	if user.IsReseller {
		owned, err := (&ResellerService{}).OwnedEmails(user.Id)
		if err != nil {
			return groupScope{emails: map[string]struct{}{}}, err
		}
		set := make(map[string]struct{}, len(owned))
		for email := range owned {
			set[email] = struct{}{}
		}
		return groupScope{emails: set}, nil
	}
	ids, err := (&AdminService{}).AccessibleInboundIds(user.Id)
	if err != nil {
		return groupScope{inboundIds: map[int]struct{}{}}, err
	}
	set := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return groupScope{inboundIds: set}, nil
}

// ListGroups merges placeholder client_groups rows with distinct group labels
// currently set on clients. Traffic is absolute sum minus ResetUp/ResetDown baselines.
func (s *ClientGroupService) ListGroups(user *model.User) ([]GroupSummary, error) {
	scope, err := s.scopeFor(user)
	if err != nil {
		return nil, err
	}
	members, err := s.scanMembers(scope)
	if err != nil {
		return nil, err
	}
	db := database.GetDB()
	var stored []model.ClientGroup
	if err := db.Find(&stored).Error; err != nil {
		return nil, err
	}

	type agg struct {
		count int
		up    int64
		down  int64
	}
	merged := make(map[string]agg)
	baseUp := make(map[string]int64, len(stored))
	baseDown := make(map[string]int64, len(stored))
	for _, g := range stored {
		merged[g.Name] = agg{}
		baseUp[g.Name] = g.ResetUp
		baseDown[g.Name] = g.ResetDown
	}
	for _, m := range members {
		if m.Group == "" {
			continue
		}
		a := merged[m.Group]
		a.count++
		a.up += m.Up
		a.down += m.Down
		merged[m.Group] = a
	}

	out := make([]GroupSummary, 0, len(merged))
	for name, a := range merged {
		up := a.up - baseUp[name]
		if up < 0 {
			up = 0
		}
		down := a.down - baseDown[name]
		if down < 0 {
			down = 0
		}
		out = append(out, GroupSummary{
			Name:        name,
			ClientCount: a.count,
			TrafficUsed: up + down,
			Up:          up,
			Down:        down,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// EmailsByGroup returns member emails sorted ascending.
func (s *ClientGroupService) EmailsByGroup(user *model.User, name string) ([]string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return []string{}, nil
	}
	scope, err := s.scopeFor(user)
	if err != nil {
		return nil, err
	}
	return s.membersOfGroup(scope, name)
}

// CreateGroup inserts an empty placeholder so the label is selectable before assignment.
func (s *ClientGroupService) CreateGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return common.NewError("group name is required")
	}
	db := database.GetDB()
	var count int64
	if err := db.Model(&model.ClientGroup{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return common.NewError("group already exists")
	}
	// Also refuse if clients already use this label (derived group). Deliberately
	// UNSCOPED: name uniqueness is a property of the whole panel, so a caller who
	// cannot see the colliding clients must still be refused rather than handed a
	// duplicate that later merges with someone else's group.
	members, err := s.scanMembers(unrestrictedScope())
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.Group == name {
			return common.NewError("group already exists")
		}
	}
	return db.Create(&model.ClientGroup{Name: name}).Error
}

// ResetGroupTraffic snapshots current member counters into baselines without
// touching client_traffics (Sanaei behaviour).
//
// The baseline it writes is a property of the WHOLE group (ListGroups subtracts it
// from the full member sum), so it is computed over every member and only offered to
// a caller who can see every member. It used to sum the CALLER'S members and store
// that panel-wide: a reseller's reset then left an admin reading a partially-reset
// group, and an admin's reset left the reseller's scoped sum below the full baseline,
// clamped to zero, showing that group as permanently empty for them.
func (s *ClientGroupService) ResetGroupTraffic(user *model.User, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return common.NewError("group name is required")
	}
	if err := s.ensureWholeGroupVisible(user, name); err != nil {
		return err
	}
	emails, err := s.membersOfGroup(unrestrictedScope(), name)
	if err != nil {
		return err
	}
	db := database.GetDB()
	var up, down int64
	if len(emails) > 0 {
		type sumRow struct {
			Up   int64
			Down int64
		}
		var sum sumRow
		if err := db.Model(&xray.ClientTraffic{}).
			Select("COALESCE(SUM(up),0) AS up, COALESCE(SUM(down),0) AS down").
			Where("email IN ?", emails).
			Scan(&sum).Error; err != nil {
			return err
		}
		up, down = sum.Up, sum.Down
	}
	var count int64
	if err := db.Model(&model.ClientGroup{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return db.Create(&model.ClientGroup{Name: name, ResetUp: up, ResetDown: down}).Error
	}
	return db.Model(&model.ClientGroup{}).Where("name = ?", name).
		Updates(map[string]any{"reset_up": up, "reset_down": down}).Error
}

// RenameGroup renames the placeholder row and rewrites every matching client label.
func (s *ClientGroupService) RenameGroup(user *model.User, oldName, newName string) (int, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" {
		return 0, common.NewError("old group name is required")
	}
	if newName == "" {
		return 0, common.NewError("new group name is required")
	}
	if oldName == newName {
		return 0, nil
	}
	return s.replaceGroupValue(user, oldName, newName)
}

// DeleteGroup drops the placeholder and clears the label on members (clients kept).
func (s *ClientGroupService) DeleteGroup(user *model.User, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, common.NewError("group name is required")
	}
	return s.replaceGroupValue(user, name, "")
}

// RemoveFromGroup clears the group label on the given emails.
func (s *ClientGroupService) RemoveFromGroup(user *model.User, emails []string) (int, error) {
	return s.AddToGroup(user, emails, "")
}

// AddToGroup assigns emails to group (or clears when group is empty). Auto-creates
// the placeholder row when the name is brand new.
func (s *ClientGroupService) AddToGroup(user *model.User, emails []string, group string) (int, error) {
	group = strings.TrimSpace(group)
	cleaned := uniqueEmails(emails)
	if len(cleaned) == 0 {
		return 0, nil
	}
	scope, err := s.scopeFor(user)
	if err != nil {
		return 0, err
	}
	if err := s.ensureInScope(scope, cleaned); err != nil {
		return 0, err
	}

	db := database.GetDB()
	if group != "" {
		var exists int64
		if err := db.Model(&model.ClientGroup{}).Where("name = ?", group).Count(&exists).Error; err != nil {
			return 0, err
		}
		if exists == 0 {
			// Only create placeholder if no client already carries this label.
			// Unscoped for the same reason as CreateGroup: whether the label is
			// already derived is a panel-wide fact.
			members, err := s.scanMembers(unrestrictedScope())
			if err != nil {
				return 0, err
			}
			derived := false
			for _, m := range members {
				if m.Group == group {
					derived = true
					break
				}
			}
			if !derived {
				if err := db.Create(&model.ClientGroup{Name: group}).Error; err != nil {
					return 0, err
				}
			}
		}
	}

	return s.patchClientGroups(scope, cleaned, func(current string) (string, bool) {
		if current == group {
			return current, false
		}
		return group, true
	})
}

// replaceGroupValue renames a group (newName != "") or deletes it (newName == ""),
// moving the shared client_groups row and every member label together.
//
// AUTHORIZE FIRST, THEN WRITE. The previous version mutated the panel-wide
// client_groups row at the top and only afterwards resolved which members the caller
// could see — so a caller with no members in the group still deleted the shared row
// (and its reset baselines) and got a "0 changed, no error" success, and a caller who
// failed the ownership check further down got their error back with the row already
// renamed and no rollback. Every check now runs before the first write.
func (s *ClientGroupService) replaceGroupValue(user *model.User, oldName, newName string) (int, error) {
	// The shared row is panel-wide, so a partial-visibility caller may not touch it.
	if err := s.ensureWholeGroupVisible(user, oldName); err != nil {
		return 0, err
	}
	scope, err := s.scopeFor(user)
	if err != nil {
		return 0, err
	}
	emails, err := s.membersOfGroup(scope, oldName)
	if err != nil {
		return 0, err
	}
	if err := s.ensureInScope(scope, emails); err != nil {
		return 0, err
	}

	db := database.GetDB()
	if newName != "" {
		var exists int64
		if err := db.Model(&model.ClientGroup{}).Where("name = ?", newName).Count(&exists).Error; err != nil {
			return 0, err
		}
		if exists > 0 {
			return 0, common.NewError("group already exists")
		}
	}

	// The shared row and the member labels move in ONE transaction: a failure partway
	// used to leave the row renamed and the clients still carrying the old label,
	// which is the split-group state this function exists to avoid.
	affected := 0
	err = db.Transaction(func(tx *gorm.DB) error {
		if newName == "" {
			if err := tx.Where("name = ?", oldName).Delete(&model.ClientGroup{}).Error; err != nil {
				return err
			}
		} else {
			res := tx.Model(&model.ClientGroup{}).Where("name = ?", oldName).Update("name", newName)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				// Derived-only group: ensure a row exists under the new name so
				// baselines (if any later) and ListGroups keep showing it after all
				// clients move.
				if err := tx.Create(&model.ClientGroup{Name: newName}).Error; err != nil {
					return err
				}
				if err := tx.Where("name = ?", oldName).Delete(&model.ClientGroup{}).Error; err != nil {
					return err
				}
			}
		}
		if len(emails) == 0 {
			return nil
		}
		n, perr := patchClientGroupsTx(tx, scope, emails, func(current string) (string, bool) {
			if current != oldName {
				return current, false
			}
			return newName, true
		})
		affected = n
		return perr
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// patchClientGroups rewrites settings.clients[].group for the given emails, in its
// own transaction. Callers that already hold one use patchClientGroupsTx.
func (s *ClientGroupService) patchClientGroups(scope groupScope, emails []string, mutate func(current string) (string, bool)) (int, error) {
	affected := 0
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		n, perr := patchClientGroupsTx(tx, scope, emails, mutate)
		affected = n
		return perr
	})
	return affected, err
}

// patchClientGroupsTx is the body of the patch, on a caller-supplied tx.
//
// mutate returns (newGroup, changed); an empty newGroup deletes the JSON key.
//
// The scope is enforced HERE as well as at the door, and that redundancy is the point:
// this function rewrites inbound settings blobs by email, so without it a caller who
// reached it by any route could write the group label onto accounts living on inbounds
// they were never granted. Callers still pre-authorize with ensureInScope so a request
// naming an inaccessible account is REFUSED rather than silently reduced to the part
// the caller may touch.
func patchClientGroupsTx(tx *gorm.DB, scope groupScope, emails []string, mutate func(current string) (string, bool)) (int, error) {
	emailSet := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		key := strings.ToLower(strings.TrimSpace(e))
		if !scope.allowsEmail(key) {
			continue
		}
		emailSet[key] = struct{}{}
	}
	affected := 0
	{
		var inbounds []*model.Inbound
		if err := tx.Find(&inbounds).Error; err != nil {
			return 0, err
		}
		for _, ib := range inbounds {
			if !scope.allowsInbound(ib.Id) {
				continue
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(ib.Settings), &settings); err != nil {
				continue
			}
			clients, ok := settings["clients"].([]any)
			if !ok {
				continue
			}
			modified := false
			for i := range clients {
				cm, ok := clients[i].(map[string]any)
				if !ok {
					continue
				}
				email, _ := cm["email"].(string)
				if _, hit := emailSet[strings.ToLower(strings.TrimSpace(email))]; !hit {
					continue
				}
				current, _ := cm["group"].(string)
				next, changed := mutate(strings.TrimSpace(current))
				if !changed {
					continue
				}
				if next == "" {
					delete(cm, "group")
				} else {
					cm["group"] = next
				}
				clients[i] = cm
				modified = true
				affected++
			}
			if !modified {
				continue
			}
			settings["clients"] = clients
			raw, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				return 0, err
			}
			if err := tx.Model(&model.Inbound{}).Where("id = ?", ib.Id).
				Update("settings", string(raw)).Error; err != nil {
				return 0, err
			}
		}
	}
	return affected, nil
}

// ensureInScope refuses the whole request unless EVERY named account is one this
// caller can already see.
//
// Checked against the members the scope actually resolves to, not against the scope
// sets directly, because the two roles answer "may I touch this account" differently
// (ownership vs. inbound grant) and only the member scan knows which inbound an email
// lives on. One predicate, so neither role can be fixed without the other.
//
// All-or-nothing on purpose: patchClientGroups writes each inbound's settings blob in
// one transaction, so silently skipping the accounts a caller may not touch would
// report a partial success as a full one.
func (s *ClientGroupService) ensureInScope(scope groupScope, emails []string) error {
	if scope.inboundIds == nil && scope.emails == nil {
		return nil // super admin
	}
	members, err := s.scanMembers(scope)
	if err != nil {
		return err
	}
	visible := make(map[string]struct{}, len(members))
	for _, m := range members {
		visible[strings.ToLower(strings.TrimSpace(m.Email))] = struct{}{}
	}
	for _, e := range emails {
		if _, ok := visible[strings.ToLower(strings.TrimSpace(e))]; !ok {
			return common.NewError("client not accessible: ", e)
		}
	}
	return nil
}

// ensureWholeGroupVisible refuses an operation that rewrites the group's SHARED state
// (the client_groups row: its name, its existence, its reset baselines) unless this
// caller can see every member of the group.
//
// That row is panel-wide while the member list a scoped caller sees is not, so a
// partial-visibility caller acting on it corrupts the group for everyone else: a
// rename relabels only their own clients and SPLITS the group in two, a delete drops
// baselines that belonged to members they cannot see, and a traffic reset writes a
// baseline summed over their subset which every other operator then reads as the
// whole group's. Refusing is the only outcome that keeps the shared row true.
//
// A scoped operator whose group holds only their own clients — the normal case — sees
// every member and is unaffected.
func (s *ClientGroupService) ensureWholeGroupVisible(user *model.User, name string) error {
	if user != nil && user.IsSuperAdmin {
		return nil
	}
	scope, err := s.scopeFor(user)
	if err != nil {
		return err
	}
	scoped, err := s.membersOfGroup(scope, name)
	if err != nil {
		return err
	}
	all, err := s.membersOfGroup(unrestrictedScope(), name)
	if err != nil {
		return err
	}
	if len(scoped) != len(all) {
		return common.NewError("this group also holds clients you cannot access: ", name)
	}
	return nil
}

// membersOfGroup lists the emails carrying this label under the given scope.
func (s *ClientGroupService) membersOfGroup(scope groupScope, name string) ([]string, error) {
	members, err := s.scanMembers(scope)
	if err != nil {
		return nil, err
	}
	emails := make([]string, 0)
	for _, m := range members {
		if m.Group == name {
			emails = append(emails, m.Email)
		}
	}
	sort.Strings(emails)
	return emails, nil
}

func uniqueEmails(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	out := make([]string, 0, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		key := strings.ToLower(e)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}

// adjustGroupBaselinesForRemovedTraffic shifts group baselines down by the
// clients' current counters so ListGroups totals survive a traffic reset or
// client delete (Sanaei #5675). Call BEFORE zeroing/deleting client_traffics.
func adjustGroupBaselinesForRemovedTraffic(tx *gorm.DB, emails []string) error {
	if tx == nil {
		return nil
	}
	cleaned := uniqueEmails(emails)
	if len(cleaned) == 0 {
		return nil
	}

	want := make(map[string]struct{}, len(cleaned))
	for _, e := range cleaned {
		want[strings.ToLower(e)] = struct{}{}
	}

	var inbounds []*model.Inbound
	if err := tx.Find(&inbounds).Error; err != nil {
		return err
	}
	emailGroup := make(map[string]string)
	inboundSvc := InboundService{}
	for _, ib := range inbounds {
		clients, err := inboundSvc.GetClients(ib)
		if err != nil {
			continue
		}
		for _, c := range clients {
			key := strings.ToLower(strings.TrimSpace(c.Email))
			if _, ok := want[key]; !ok {
				continue
			}
			g := strings.TrimSpace(c.Group)
			if g == "" {
				continue
			}
			if _, have := emailGroup[key]; !have {
				emailGroup[key] = g
			}
		}
	}
	if len(emailGroup) == 0 {
		return nil
	}

	type delta struct {
		Up   int64
		Down int64
	}
	byGroup := make(map[string]*delta)
	for key, group := range emailGroup {
		var t xray.ClientTraffic
		if err := tx.Where("LOWER(email) = ?", key).First(&t).Error; err != nil {
			continue
		}
		d := byGroup[group]
		if d == nil {
			d = &delta{}
			byGroup[group] = d
		}
		d.Up += t.Up
		d.Down += t.Down
	}

	for name, d := range byGroup {
		if d.Up == 0 && d.Down == 0 {
			continue
		}
		res := tx.Model(&model.ClientGroup{}).Where("name = ?", name).Updates(map[string]any{
			"reset_up":   gorm.Expr("reset_up - ?", d.Up),
			"reset_down": gorm.Expr("reset_down - ?", d.Down),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if err := tx.Create(&model.ClientGroup{Name: name, ResetUp: -d.Up, ResetDown: -d.Down}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
