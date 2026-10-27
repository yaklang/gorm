package dm_test

import (
	"database/sql"
	"database/sql/driver"
	"testing"

	dm "github.com/yaklang/gorm/dm"
)

var _ driver.DriverContext = (*dm.DmDriver)(nil)

func TestRelocatedDriverRegistration(t *testing.T) {
	// sql.Open parses the DSN but does not open a network connection.
	db, err := sql.Open("dm", "dm://APP:fixture@127.0.0.1:5236?schema=APP")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, ok := db.Driver().(*dm.DmDriver); !ok {
		t.Fatalf("SQL driver is not the relocated driver: %T", db.Driver())
	}
	if got := dm.GetDriverVersion(); got != "8.1.4.200" {
		t.Fatalf("upstream driver version changed: %q", got)
	}
}
