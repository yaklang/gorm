//go:build dameng && dm_live

package dameng

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yaklang/gorm"
)

type dmFixtureRecord struct {
	gorm.Model
	Tenant  string `gorm:"size:64;unique_index:ux_dm_fixture_record"`
	Natural string `gorm:"size:64;unique_index:ux_dm_fixture_record"`
	Body    string `gorm:"type:text"`
	Number  int
	Enabled bool
	Data    []byte
	// This fixture-only hook consumes an identity between two batch rows.
	ConsumeIdentity bool `gorm:"-"`
}

func (*dmFixtureRecord) TableName() string { return "gorm_dm_fixture_records" }

var dmFixtureCountCreates atomic.Bool
var dmFixtureCreatedRows atomic.Int64

func (r *dmFixtureRecord) AfterCreate(scope *gorm.Scope) error {
	if dmFixtureCountCreates.Load() {
		dmFixtureCreatedRows.Add(1)
	}
	if r.ConsumeIdentity {
		// Use the batch's physical transaction, without recursively invoking a
		// GORM hook. The next row must read its own ID rather than infer +1.
		_, err := scope.SQLDB().Exec(`INSERT INTO "gorm_dm_fixture_records" ("tenant","natural","number","enabled") VALUES (?,?,0,0)`, r.Tenant, r.Natural+"-gap")
		return err
	}
	return nil
}

type dmFixtureCase struct {
	ID   uint
	Name string `gorm:"column:MiXeD;size:32"`
}

func (*dmFixtureCase) TableName() string { return "gorm_dm_fixture_MiXeD" }

type dmFixtureOnlyKey struct {
	Key string `gorm:"primary_key;size:32"`
}

func (*dmFixtureOnlyKey) TableName() string { return "gorm_dm_fixture_keys" }

type dmFixtureIdentity struct{ ID uint }

func (*dmFixtureIdentity) TableName() string { return "gorm_dm_fixture_identity" }

type dmFixtureDefaults struct {
	ID           uint
	DefaultTrue  bool `gorm:"default:true"`
	DefaultFalse bool `gorm:"default:false"`
}

func (*dmFixtureDefaults) TableName() string { return "gorm_dm_fixture_defaults" }

// Live tests deliberately require their own build tag and explicit fixture
// configuration. Missing credentials fail, rather than silently skipping a test.
func TestDMLiveGenericOperations(t *testing.T) {
	if os.Getenv("GORM_DM_ALLOW_FIXTURE") != "1" {
		t.Fatal("GORM_DM_ALLOW_FIXTURE=1 is required")
	}
	path := os.Getenv("GORM_DM_DSN_FILE")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("GORM_DM_DSN_FILE must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read fixture DSN file")
	}
	dsn := strings.TrimSpace(string(raw))
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "dm" || !strings.HasPrefix(u.Query().Get("schema"), "GORM_DM_") {
		t.Fatal("live test needs an isolated GORM_DM_ schema")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sqlDB, err := OpenDBContext(ctx, dsn)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(Dialect, sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	db.LogMode(false)
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	models := []interface{}{&dmFixtureRecord{}, &dmFixtureCase{}, &dmFixtureOnlyKey{}, &dmFixtureIdentity{}, &dmFixtureDefaults{}}
	for _, model := range models {
		if db.HasTable(model) {
			t.Fatal("fixture table already exists; refuse to reuse an unknown resource")
		}
		if err := db.AutoMigrate(model).Error; err != nil {
			t.Fatalf("migration: %v", err)
		}
		model := model
		t.Cleanup(func() {
			if err := db.DropTable(model).Error; err != nil {
				t.Errorf("drop fixture table: %v", err)
			}
		})
	}
	if err := db.Exec(`ALTER TABLE "gorm_dm_fixture_records" ADD CONSTRAINT "ck_dm_fixture_number" CHECK ("number">=0)`).Error; err != nil {
		t.Fatal(err)
	}
	load := func(t *testing.T, tenant, natural string) dmFixtureRecord {
		t.Helper()
		var row dmFixtureRecord
		if err := db.Unscoped().Where(`"tenant"=? AND "natural"=?`, tenant, natural).First(&row).Error; err != nil {
			t.Fatalf("load fixture row: %v", err)
		}
		return row
	}
	count := func(t *testing.T, tenant string) int {
		t.Helper()
		var total int
		if err := db.Unscoped().Model(&dmFixtureRecord{}).Where(`"tenant"=?`, tenant).Count(&total).Error; err != nil {
			t.Fatal(err)
		}
		return total
	}
	t.Run("MigrationCaseAndIndexReuse", func(t *testing.T) {
		for _, model := range models {
			if err := db.AutoMigrate(model).Error; err != nil {
				t.Fatalf("repeat migration: %v", err)
			}
		}
		if err := db.Model(&dmFixtureCase{}).AddIndex("idx_dm_fixture_lower", "MiXeD").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&dmFixtureCase{}).AddIndex("idx_dm_fixture_lower", "MiXeD").Error; err != nil {
			t.Fatalf("repeat explicit index: %v", err)
		}
		if !db.NewScope(&dmFixtureCase{}).Dialect().HasIndex("gorm_dm_fixture_MiXeD", "idx_dm_fixture_lower") {
			t.Fatal("unquoted index metadata lost")
		}
		if err := db.Model(&dmFixtureCase{}).ModifyColumn("MiXeD", "VARCHAR(64)").Error; err != nil {
			t.Fatalf("modify quoted column: %v", err)
		}
		row := dmFixtureCase{Name: "中文Case"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		var got dmFixtureCase
		if err := db.First(&got, row.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Name != row.Name {
			t.Fatal("mixed-case column was not read back")
		}
	})
	t.Run("DefaultValuesAndBooleanDefaults", func(t *testing.T) {
		identity := dmFixtureIdentity{}
		if err := db.Create(&identity).Error; err != nil {
			t.Fatalf("identity-only single create: %v", err)
		}
		if identity.ID == 0 {
			t.Fatal("identity-only create did not return its generated ID")
		}
		rows := []*dmFixtureIdentity{{}, {}}
		if err := db.CreateInBatches(rows, 2).Error; err != nil {
			t.Fatalf("identity-only batch create: %v", err)
		}
		if rows[0].ID == 0 || rows[1].ID == 0 || rows[0].ID == rows[1].ID || rows[0].ID == identity.ID || rows[1].ID == identity.ID {
			t.Fatal("identity-only batch identity readback failed")
		}
		defaults := dmFixtureDefaults{}
		if err := db.Create(&defaults).Error; err != nil {
			t.Fatalf("boolean-default create: %v", err)
		}
		var got dmFixtureDefaults
		if err := db.First(&got, defaults.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !got.DefaultTrue || got.DefaultFalse {
			t.Fatal("boolean defaults were not applied")
		}
	})
	t.Run("BatchCreateIdentityCLOBBlobAndBoolean", func(t *testing.T) {
		body := strings.Repeat("中文长文本🙂", 6000)
		data := []byte{0, 1, 127, 128, 255}
		rows := []*dmFixtureRecord{{Tenant: "create", Natural: "one", Body: body, Enabled: true, Data: data, ConsumeIdentity: true}, nil, {Tenant: "create", Natural: "two", Enabled: false}}
		if err := db.CreateInBatches(rows, 2).Error; err != nil {
			t.Fatal(err)
		}
		if rows[0].ID == 0 || rows[2].ID == 0 || rows[0].ID == rows[2].ID {
			t.Fatal("generated identity readback failed")
		}
		got := load(t, "create", "one")
		if got.ID != rows[0].ID || got.Body != body || !got.Enabled || !bytes.Equal(got.Data, data) {
			t.Fatal("CLOB/blob/boolean readback mismatch")
		}
		second := load(t, "create", "two")
		if second.ID != rows[2].ID || second.Enabled {
			t.Fatal("second row identity or false boolean changed")
		}
		gap := load(t, "create", "one-gap")
		if gap.ID <= got.ID || second.ID <= gap.ID || second.ID == got.ID+1 {
			t.Fatalf("fixture did not establish an identity gap: first=%d gap=%d second=%d", got.ID, gap.ID, second.ID)
		}
	})
	t.Run("MergePreservesIdentityCreationAndZeroValues", func(t *testing.T) {
		row := dmFixtureRecord{Tenant: "merge", Natural: "one", Body: "original", Number: 7, Enabled: true}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		before := load(t, "merge", "one")
		update := dmFixtureRecord{Model: gorm.Model{ID: row.ID + 100000, CreatedAt: before.CreatedAt.Add(-24 * time.Hour)}, Tenant: "merge", Natural: "one", Body: strings.Repeat("更新中文", 3000)}
		if err := db.SaveInBatches([]*dmFixtureRecord{&update, nil, {Tenant: "merge", Natural: "two", Number: 1}}, 1, "tenant", "natural").Error; err != nil {
			t.Fatal(err)
		}
		after := load(t, "merge", "one")
		if after.ID != before.ID || !after.CreatedAt.Equal(before.CreatedAt) || after.Number != 0 || after.Enabled || after.Body != update.Body {
			t.Fatal("MERGE replaced existing identity/creation time or lost zero values")
		}
		if update.ID != row.ID+100000 {
			t.Fatal("SaveInBatches rewrote caller identity")
		}
		if count(t, "merge") != 2 {
			t.Fatal("merge insert/update count mismatch")
		}
		if err := db.Delete(&after).Error; err != nil {
			t.Fatal(err)
		}
		if load(t, "merge", "one").DeletedAt == nil {
			t.Fatal("soft delete not persisted")
		}
		if err := db.SaveInBatches([]*dmFixtureRecord{{Tenant: "merge", Natural: "one", Body: "restored"}}, 1, "tenant", "natural").Error; err != nil {
			t.Fatal(err)
		}
		restored := load(t, "merge", "one")
		if restored.ID != before.ID || restored.DeletedAt != nil || !restored.CreatedAt.Equal(before.CreatedAt) {
			t.Fatal("soft-deleted row was not restored in place")
		}
	})
	t.Run("FailureRollsBackAcrossChunks", func(t *testing.T) {
		seed := dmFixtureRecord{Tenant: "rollback", Natural: "existing", Number: 5}
		if err := db.Create(&seed).Error; err != nil {
			t.Fatal(err)
		}
		before := load(t, "rollback", "existing")
		dmFixtureCountCreates.Store(true)
		defer dmFixtureCountCreates.Store(false)
		dmFixtureCreatedRows.Store(0)
		bad := []*dmFixtureRecord{{Tenant: "rollback", Natural: "existing", Number: 9}, {Tenant: "rollback", Natural: "new", Number: 1}, {Tenant: "rollback", Natural: "failure", Number: -1}}
		if err := db.SaveInBatches(bad, 2, "tenant", "natural").Error; err == nil || !strings.Contains(strings.ToLower(err.Error()), "ck_dm_fixture_number") {
			t.Fatalf("expected the injected check constraint violation, got: %v", err)
		}
		if completed := dmFixtureCreatedRows.Load(); completed != 2 {
			t.Fatalf("expected two successful MERGE statements before the injected failure, got %d", completed)
		}
		after := load(t, "rollback", "existing")
		if after.ID != before.ID || after.Number != 5 || count(t, "rollback") != 1 {
			t.Fatal("failed later chunk committed earlier writes")
		}
		creates := []*dmFixtureRecord{{Tenant: "create-rollback", Natural: "one"}, {Tenant: "create-rollback", Natural: "two"}, {Tenant: "create-rollback", Natural: "one"}}
		dmFixtureCreatedRows.Store(0)
		if err := db.CreateInBatches(creates, 2).Error; err == nil || (!strings.Contains(err.Error(), "-6602") && !strings.Contains(strings.ToLower(err.Error()), "ux_dm_fixture_record")) {
			t.Fatalf("expected the injected unique constraint violation, got: %v", err)
		}
		if completed := dmFixtureCreatedRows.Load(); completed != 2 {
			t.Fatalf("expected two successful INSERT statements before the injected failure, got %d", completed)
		}
		if count(t, "create-rollback") != 0 {
			t.Fatal("failed batch create committed earlier rows")
		}
	})
	t.Run("CallerTransactionOwnsCommit", func(t *testing.T) {
		tx := db.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		defer tx.Rollback()
		if err := tx.SaveInBatches([]*dmFixtureRecord{{Tenant: "caller", Natural: "one", Number: 1}}, 1, "tenant", "natural").Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback().Error; err != nil {
			t.Fatal(err)
		}
		if count(t, "caller") != 0 {
			t.Fatal("batch committed its caller-owned transaction")
		}

		commitTx := db.Begin()
		if commitTx.Error != nil {
			t.Fatal(commitTx.Error)
		}
		defer commitTx.Rollback()
		if err := commitTx.SaveInBatches([]*dmFixtureRecord{{Tenant: "caller-commit", Natural: "one", Number: 1}}, 1, "tenant", "natural").Error; err != nil {
			t.Fatal(err)
		}
		// A following write also proves the batch left this transaction open.
		if err := commitTx.Create(&dmFixtureRecord{Tenant: "caller-commit", Natural: "two", Number: 2}).Error; err != nil {
			t.Fatal(err)
		}
		if err := commitTx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if count(t, "caller-commit") != 2 || load(t, "caller-commit", "one").Number != 1 || load(t, "caller-commit", "two").Number != 2 {
			t.Fatal("caller commit did not persist both the batch and its following write")
		}
	})
	t.Run("PrimaryTargetAndNoUpdateColumns", func(t *testing.T) {
		fresh := dmFixtureRecord{Tenant: "primary", Natural: "one"}
		if err := db.SaveInBatches([]*dmFixtureRecord{&fresh}, 1).Error; err != nil {
			t.Fatalf("generated primary target: %v", err)
		}
		got := load(t, "primary", "one")
		got.Body = "updated by primary"
		if err := db.SaveInBatches([]*dmFixtureRecord{&got}, 1).Error; err != nil {
			t.Fatalf("existing primary target: %v", err)
		}
		if load(t, "primary", "one").Body != got.Body {
			t.Fatal("primary-target merge did not update")
		}
		for i := 0; i < 2; i++ {
			if err := db.SaveInBatches([]*dmFixtureOnlyKey{{Key: "same"}}, 1, "key").Error; err != nil {
				t.Fatalf("no-update merge: %v", err)
			}
		}
		var total int
		if err := db.Model(&dmFixtureOnlyKey{}).Count(&total).Error; err != nil || total != 1 {
			t.Fatal("key-only merge did not preserve one row")
		}
	})
}
