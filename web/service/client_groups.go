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

// scanMembers walks every inbound's settings.clients and joins client_traffics.
// When allowedEmails is non-nil, only those emails (already lowercased) are kept —
// used to scope reseller views without exposing other operators' accounts.
func (s *ClientGroupService) scanMembers(allowedEmails map[string]struct{}) ([]clientGroupMember, error) {
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
			if allowedEmails != nil {
				if _, ok := allowedEmails[key]; !ok {
					continue
				}
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

func (s *ClientGroupService) allowedSet(user *model.User) (map[string]struct{}, error) {
	if user == nil || !user.IsReseller {
		return nil, nil
	}
	owned, err := (&ResellerService{}).OwnedEmails(user.Id)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(owned))
	for email := range owned {
		set[email] = struct{}{}
	}
	return set, nil
}

// ListGroups merges placeholder client_groups rows with distinct group labels
// currently set on clients. Traffic is absolute sum minus ResetUp/ResetDown baselines.
func (s *ClientGroupService) ListGroups(user *model.User) ([]GroupSummary, error) {
	allowed, err := s.allowedSet(user)
	if err != nil {
		return nil, err
	}
	members, err := s.scanMembers(allowed)
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
	allowed, err := s.allowedSet(user)
	if err != nil {
		return nil, err
	}
	members, err := s.scanMembers(allowed)
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
	// Also refuse if clients already use this label (derived group).
	members, err := s.scanMembers(nil)
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
func (s *ClientGroupService) ResetGroupTraffic(user *model.User, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return common.NewError("group name is required")
	}
	emails, err := s.EmailsByGroup(user, name)
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
	if err := s.ensureOwned(user, cleaned); err != nil {
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
			members, err := s.scanMembers(nil)
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

	return s.patchClientGroups(cleaned, func(current string) (string, bool) {
		if current == group {
			return current, false
		}
		return group, true
	})
}

func (s *ClientGroupService) replaceGroupValue(user *model.User, oldName, newName string) (int, error) {
	db := database.GetDB()
	if newName == "" {
		if err := db.Where("name = ?", oldName).Delete(&model.ClientGroup{}).Error; err != nil {
			return 0, err
		}
	} else {
		var exists int64
		if err := db.Model(&model.ClientGroup{}).Where("name = ?", newName).Count(&exists).Error; err != nil {
			return 0, err
		}
		if exists > 0 {
			return 0, common.NewError("group already exists")
		}
		res := db.Model(&model.ClientGroup{}).Where("name = ?", oldName).Update("name", newName)
		if res.Error != nil {
			return 0, res.Error
		}
		if res.RowsAffected == 0 {
			// Derived-only group: ensure a row exists under the new name so baselines
			// (if any later) and ListGroups keep showing it after all clients move.
			_ = db.Create(&model.ClientGroup{Name: newName}).Error
			_ = db.Where("name = ?", oldName).Delete(&model.ClientGroup{}).Error
		}
	}

	emails, err := s.EmailsByGroup(user, oldName)
	if err != nil {
		return 0, err
	}
	if len(emails) == 0 {
		return 0, nil
	}
	if err := s.ensureOwned(user, emails); err != nil {
		return 0, err
	}
	return s.patchClientGroups(emails, func(current string) (string, bool) {
		if current != oldName {
			return current, false
		}
		return newName, true
	})
}

// patchClientGroups rewrites settings.clients[].group for the given emails.
// mutate returns (newGroup, changed). Empty newGroup deletes the JSON key.
func (s *ClientGroupService) patchClientGroups(emails []string, mutate func(current string) (string, bool)) (int, error) {
	emailSet := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		emailSet[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
	db := database.GetDB()
	affected := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		var inbounds []*model.Inbound
		if err := tx.Find(&inbounds).Error; err != nil {
			return err
		}
		for _, ib := range inbounds {
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
				return err
			}
			if err := tx.Model(&model.Inbound{}).Where("id = ?", ib.Id).
				Update("settings", string(raw)).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return affected, err
}

func (s *ClientGroupService) ensureOwned(user *model.User, emails []string) error {
	if user == nil || !user.IsReseller {
		return nil
	}
	owned, err := (&ResellerService{}).OwnedEmails(user.Id)
	if err != nil {
		return err
	}
	for _, e := range emails {
		if !owned[strings.ToLower(strings.TrimSpace(e))] {
			return common.NewError("client not owned by reseller")
		}
	}
	return nil
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
