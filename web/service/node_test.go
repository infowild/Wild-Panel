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

	// First observation SEEDS the baseline only: pre-existing remote usage is not
	// imported onto the master. Master consumption starts counting from here.
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
	if after.Up != 0 || after.Down != 0 || after.AllTime != 0 {
		t.Fatalf("first pull must seed only: up=%d down=%d all=%d want 0/0/0", after.Up, after.Down, after.AllTime)
	}

	// Same absolute counters again — no delta, master stays at zero.
	err = db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 100, 200, 300)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 0 || after.Down != 0 || after.AllTime != 0 {
		t.Fatalf("identical pull mutated counters: up=%d down=%d all=%d", after.Up, after.Down, after.AllTime)
	}

	// Remote grew by 10/20/30 — only the delta lands on the master.
	err = db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 110, 220, 330)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 10 || after.Down != 20 || after.AllTime != 30 {
		t.Fatalf("delta pull: up=%d down=%d all=%d want 10/20/30", after.Up, after.Down, after.AllTime)
	}

	// Remote RESET (up/down fall back to near-zero, AllTime keeps climbing). The
	// master must not lose a single byte: negative deltas clamp to zero and only
	// the genuine post-reset growth is added.
	err = db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 3, 4, 340)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 10 || after.Down != 20 {
		t.Fatalf("remote reset rewound master usage: up=%d down=%d want 10/20", after.Up, after.Down)
	}
	if after.AllTime != 40 {
		t.Fatalf("all-time should follow monotonic remote: got %d want 40", after.AllTime)
	}

	// Growth measured from the reset point forward, never from the old peak.
	err = db.Transaction(func(tx *gorm.DB) error {
		return mergeClientDelta(tx, 7, "a@b.c", 13, 14, 350)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", "a@b.c").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Up != 20 || after.Down != 30 || after.AllTime != 50 {
		t.Fatalf("post-reset growth: up=%d down=%d all=%d want 20/30/50", after.Up, after.Down, after.AllTime)
	}
}
