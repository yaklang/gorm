//go:build !dameng

package dameng

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeDriverDisabledDoesNotConnect(t *testing.T) {
	if NativeEnabled() {
		t.Fatal("default build enabled native driver")
	}
	for _, dsn := range []string{"dm://user:private-password@127.0.0.1:1", "dm://user:private-password@127.0.0.1:1?schema=DEDICATED"} {
		db, err := OpenDBContext(context.Background(), dsn)
		if db != nil || !errors.Is(err, ErrNativeUnavailable) || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("unexpected no-driver result: %v", err)
		}
	}
}
