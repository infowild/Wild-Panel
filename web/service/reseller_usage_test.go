package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"gorm.io/gorm"
)

func seedResellerUsage(t *testing.T) (*model.User, *model.User, *xray.ClientTraffic) {
	t.Helper()
	newInboundDB(t)
	db := database.GetDB()

	admin := &model.User{Username: "usage-admin", Password: "x", Enable: true, IsSuperAdmin: true}
	reseller := &model.User{Username: "usage-reseller", Password: "x", Enable: true, IsReseller: true}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(reseller).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ResellerProfile{
		UserId: reseller.Id, CreatedBy: admin.Id,
		AllowanceBytes: 5000, SpentBytes: 3000, UsageBytes: 700,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Inbound{
		Id: 1, UserId: admin.Id, Remark: "usage-main", Port: 31001,
		Protocol: model.VMESS, Tag: "usage-main", Enable: true,
		Up: 400, Down: 600, AllTime: 5000, Settings: `{"clients":[]}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ct := &xray.ClientTraffic{InboundId: 1, Email: "usage-owned", Enable: true, Up: 40, Down: 60, AllTime: 900}
	if err := db.Create(ct).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ResellerClient{
		Email: ct.Email, InboundId: ct.InboundId, UserId: reseller.Id,
		ChargedBytes: 3000, AllTimeBase: 100,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return admin, reseller, ct
}

func TestAddResellerUsageTracksOnlyOwnedRawTraffic(t *testing.T) {
	_, reseller, _ := seedResellerUsage(t)
	db := database.GetDB()

	if err := db.Transaction(func(tx *gorm.DB) error {
		return addResellerUsage(tx, map[string]int64{
			"usage-owned": 125,
			"admin-owned": 999, // no ResellerClient row: must not hit any meter
			"":            50,
			"negative":    -1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	var profile model.ResellerProfile
	if err := db.Where("user_id = ?", reseller.Id).First(&profile).Error; err != nil {
		t.Fatal(err)
	}
	if profile.UsageBytes != 825 {
		t.Fatalf("usage_bytes=%d, want 825 (700 existing + 125 owned raw bytes)", profile.UsageBytes)
	}
	if profile.AllowanceBytes != 5000 || profile.SpentBytes != 3000 {
		t.Fatalf("usage accrual moved allocation ledger: allowance=%d spent=%d",
			profile.AllowanceBytes, profile.SpentBytes)
	}
}

func TestClientTrafficTickAccruesResellerUsage(t *testing.T) {
	_, reseller, before := seedResellerUsage(t)
	db := database.GetDB()
	err := db.Transaction(func(tx *gorm.DB) error {
		return (&InboundService{}).addClientTraffic(tx, []*xray.ClientTraffic{{
			Email: before.Email, Up: 25, Down: 75,
		}})
	})
	if err != nil {
		t.Fatal(err)
	}

	var profile model.ResellerProfile
	if err := db.Where("user_id = ?", reseller.Id).First(&profile).Error; err != nil {
		t.Fatal(err)
	}
	if profile.UsageBytes != 800 {
		t.Fatalf("usage_bytes=%d, want 800 after a 100-byte raw traffic tick", profile.UsageBytes)
	}
	var after xray.ClientTraffic
	if err := db.Where("email = ?", before.Email).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.AllTime != before.AllTime+100 {
		t.Fatalf("all_time=%d, want %d", after.AllTime, before.AllTime+100)
	}
}

func TestRemoteNodeResetCannotRewindResellerUsage(t *testing.T) {
	_, reseller, before := seedResellerUsage(t)
	db := database.GetDB()
	if err := db.Create(&model.NodeClientTraffic{
		NodeId: 7, Email: before.Email, Up: 100, Down: 200, AllTime: 300,
	}).Error; err != nil {
		t.Fatal(err)
	}

	merge := func(up, down, allTime int64) {
		t.Helper()
		if err := db.Transaction(func(tx *gorm.DB) error {
			return mergeClientDelta(tx, 7, before.Email, up, down, allTime)
		}); err != nil {
			t.Fatal(err)
		}
	}
	merge(110, 220, 330) // +30 raw lifetime bytes
	merge(0, 0, 330)     // remote reset: no decrement and no new usage
	merge(5, 6, 341)     // +11 after the reset

	var profile model.ResellerProfile
	if err := db.Where("user_id = ?", reseller.Id).First(&profile).Error; err != nil {
		t.Fatal(err)
	}
	if profile.UsageBytes != 741 {
		t.Fatalf("usage_bytes=%d, want 741; remote reset must not rewind the card meter", profile.UsageBytes)
	}
}

func TestResetResellerUsageDoesNotResetMainPanelTrafficOrAllocation(t *testing.T) {
	admin, reseller, before := seedResellerUsage(t)
	views, err := (&ResellerService{}).GetResellers(admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].UsageBytes != 700 {
		t.Fatalf("reseller card usage=%+v, want one view with usageBytes=700", views)
	}
	if err := (&ResellerService{}).ResetUsage(admin, reseller.Id); err != nil {
		t.Fatal(err)
	}

	db := database.GetDB()
	var profile model.ResellerProfile
	if err := db.Where("user_id = ?", reseller.Id).First(&profile).Error; err != nil {
		t.Fatal(err)
	}
	if profile.UsageBytes != 0 {
		t.Fatalf("usage_bytes=%d, want 0", profile.UsageBytes)
	}
	if profile.AllowanceBytes != 5000 || profile.SpentBytes != 3000 {
		t.Fatalf("reset moved allocation ledger: allowance=%d spent=%d",
			profile.AllowanceBytes, profile.SpentBytes)
	}

	var after xray.ClientTraffic
	if err := db.Where("email = ?", before.Email).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != before.Up || after.Down != before.Down || after.AllTime != before.AllTime {
		t.Fatalf("reset touched authoritative traffic: before=%d/%d/%d after=%d/%d/%d",
			before.Up, before.Down, before.AllTime, after.Up, after.Down, after.AllTime)
	}
	var inbound model.Inbound
	if err := db.First(&inbound, before.InboundId).Error; err != nil {
		t.Fatal(err)
	}
	if inbound.Up != 400 || inbound.Down != 600 || inbound.AllTime != 5000 {
		t.Fatalf("reset touched main inbound traffic: %d/%d/%d", inbound.Up, inbound.Down, inbound.AllTime)
	}
}

func TestResetResellerUsageRespectsManagerScope(t *testing.T) {
	_, reseller, _ := seedResellerUsage(t)
	otherAdmin := &model.User{Id: 999, Username: "other", Enable: true}
	err := (&ResellerService{}).ResetUsage(otherAdmin, reseller.Id)
	if err != ErrResellerNotFound {
		t.Fatalf("ResetUsage by unrelated admin error=%v, want ErrResellerNotFound", err)
	}

	var profile model.ResellerProfile
	if dbErr := database.GetDB().Where("user_id = ?", reseller.Id).First(&profile).Error; dbErr != nil {
		t.Fatal(dbErr)
	}
	if profile.UsageBytes != 700 {
		t.Fatalf("unauthorized reset changed usage_bytes to %d", profile.UsageBytes)
	}
}
