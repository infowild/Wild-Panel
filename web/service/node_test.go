package service

import (
	"net/url"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"github.com/op/go-logging"
	"gorm.io/gorm"
)

func TestFormFingerprintStable(t *testing.T) {
	in := &model.Inbound{
		Remark:   "n1",
		Enable:   true,
		Port:     443,
		Protocol: "vless",
		Tag:      "inbound-443",
		Settings: `{"clients":[]}`,
	}
	a := WireInboundForm(in)
	b := WireInboundForm(in)
	if FormFingerprint(a) != FormFingerprint(b) {
		t.Fatal("identical forms produced different fingerprints")
	}
	b.Set("remark", "changed")
	if FormFingerprint(a) == FormFingerprint(b) {
		t.Fatal("changed form kept the same fingerprint")
	}
	_ = url.Values{}
}

func TestMergeClientDeltaAggregatesOnce(t *testing.T) {
	logger.InitLogger(logging.CRITICAL)
	if err := database.InitDB(filepath.Join(t.TempDir(), "nodedelta.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()
	inbound := &model.Inbound{Remark: "local", Port: 10000, Protocol: "vless", Tag: "t1", Enable: true, Settings: "{}"}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	ct := &xray.ClientTraffic{InboundId: inbound.Id, Email: "a@b.c", Enable: true}
	if err := db.Create(ct).Error; err != nil {
		t.Fatal(err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 100, 200, 300)
	})
	if err != nil {
		t.Fatal(err)
	}
	var after xray.ClientTraffic
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 100 || after.Down != 200 || after.AllTime != 300 {
		t.Fatalf("first pull: up=%d down=%d all=%d", after.Up, after.Down, after.AllTime)
	}

	// Same absolute counters again — master must not double-count.
	err = db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 100, 200, 300)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 100 || after.Down != 200 || after.AllTime != 300 {
		t.Fatalf("second identical pull mutated counters: up=%d down=%d all=%d", after.Up, after.Down, after.AllTime)
	}

	// Remote grew by 10/20 — only the delta lands on the master.
	err = db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 110, 220, 330)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 110 || after.Down != 220 || after.AllTime != 330 {
		t.Fatalf("delta pull: up=%d down=%d all=%d want 110/220/330", after.Up, after.Down, after.AllTime)
	}
}
