package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"github.com/op/go-logging"
)

func initGroupTestDB(t *testing.T) {
	t.Helper()
	logger.InitLogger(logging.CRITICAL)
	if err := database.InitDB(filepath.Join(t.TempDir(), "groups.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
}

func seedGroupInboundWithClients(t *testing.T, clients []map[string]any) *model.Inbound {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"clients": clients})
	if err != nil {
		t.Fatal(err)
	}
	ib := &model.Inbound{
		Remark:   "g1",
		Port:     10001,
		Protocol: "vless",
		Tag:      "inbound-g1",
		Enable:   true,
		Settings: string(raw),
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	return ib
}

func TestClientGroupsCreateListAssignRenameReset(t *testing.T) {
	initGroupTestDB(t)
	db := database.GetDB()
	svc := &ClientGroupService{Inbound: InboundService{}}

	if err := svc.CreateGroup("vip"); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := svc.CreateGroup("vip"); err == nil {
		t.Fatal("duplicate CreateGroup should fail")
	}

	rows, err := svc.ListGroups(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "vip" || rows[0].ClientCount != 0 {
		t.Fatalf("placeholder list: %+v", rows)
	}

	seedGroupInboundWithClients(t, []map[string]any{
		{"email": "a@b.c", "id": "1", "enable": true},
		{"email": "d@e.f", "id": "2", "enable": true},
	})
	for _, email := range []string{"a@b.c", "d@e.f"} {
		if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
			t.Fatal(err)
		}
	}

	affected, err := svc.AddToGroup(nil, []string{"a@b.c", "d@e.f"}, "vip")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 2 {
		t.Fatalf("affected=%d want 2", affected)
	}

	emails, err := svc.EmailsByGroup(nil, "vip")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 2 {
		t.Fatalf("emails=%v", emails)
	}

	rows, err = svc.ListGroups(nil)
	if err != nil {
		t.Fatal(err)
	}
	var vip GroupSummary
	for _, r := range rows {
		if r.Name == "vip" {
			vip = r
		}
	}
	if vip.ClientCount != 2 || vip.Up != 200 || vip.Down != 400 {
		t.Fatalf("vip summary: %+v", vip)
	}

	if err := svc.ResetGroupTraffic(nil, "vip"); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.ListGroups(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "vip" {
			if r.Up != 0 || r.Down != 0 || r.TrafficUsed != 0 {
				t.Fatalf("after reset display should be 0: %+v", r)
			}
		}
	}
	var ct xray.ClientTraffic
	if err := db.Where("email = ?", "a@b.c").First(&ct).Error; err != nil {
		t.Fatal(err)
	}
	if ct.Up != 100 || ct.Down != 200 {
		t.Fatalf("client counters must survive group reset: up=%d down=%d", ct.Up, ct.Down)
	}

	affected, err = svc.RenameGroup(nil, "vip", "gold")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 2 {
		t.Fatalf("rename affected=%d", affected)
	}
	emails, err = svc.EmailsByGroup(nil, "gold")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 2 {
		t.Fatalf("after rename: %v", emails)
	}

	affected, err = svc.DeleteGroup(nil, "gold")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 2 {
		t.Fatalf("delete affected=%d", affected)
	}
	emails, err = svc.EmailsByGroup(nil, "gold")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 0 {
		t.Fatalf("label should be cleared: %v", emails)
	}
	// Clients still in inbound settings.
	var ib model.Inbound
	if err := db.First(&ib).Error; err != nil {
		t.Fatal(err)
	}
	clients, err := svc.Inbound.GetClients(&ib)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 {
		t.Fatalf("clients deleted unexpectedly: %d", len(clients))
	}
	for _, c := range clients {
		if c.Group != "" {
			t.Fatalf("group should be empty after delete: %+v", c)
		}
	}
}

func TestClientGroupBaselineSurvivesClientReset(t *testing.T) {
	initGroupTestDB(t)
	db := database.GetDB()
	svc := &ClientGroupService{Inbound: InboundService{}}
	_ = svc.CreateGroup("team")
	seedGroupInboundWithClients(t, []map[string]any{
		{"email": "u1@x.y", "id": "1", "enable": true, "group": "team"},
	})
	if err := db.Create(&xray.ClientTraffic{Email: "u1@x.y", Enable: true, Up: 50, Down: 70}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetGroupTraffic(nil, "team"); err != nil {
		t.Fatal(err)
	}
	inboundSvc := &InboundService{}
	if err := inboundSvc.ResetClientTrafficByEmail("u1@x.y"); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.ListGroups(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "team" && r.TrafficUsed != 0 {
			// After client reset, baseline was reduced by the removed counters,
			// so displayed group traffic stays at 0 (not negative / not rebound).
			t.Fatalf("group traffic should stay 0 after client reset: %+v", r)
		}
	}
}
