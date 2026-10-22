//go:build dameng

package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestNativeDriverRegistration(t *testing.T) {
	if !NativeEnabled() {
		t.Fatal("native driver missing")
	}
	db, err := sql.Open(Dialect, "dm://user:private-password@127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !nativeDriver.matches(db.Driver()) {
		t.Fatal("wrong driver")
	}
	if _, ok := db.Driver().(driver.DriverContext); !ok {
		t.Fatal("native driver lacks connectors")
	}
}

func TestNativeHandshakeCancellationClosesSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		var first [1]byte
		if _, err := io.ReadFull(conn, first[:]); err != nil {
			peerDone <- err
			return
		}
		cancel()
		_, err = io.Copy(io.Discard, conn)
		peerDone <- err
	}()
	done := make(chan error, 1)
	go func() {
		db, err := OpenDBContext(ctx, "dm://user:private-password@"+listener.Addr().String())
		if db != nil {
			_ = db.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("canceled handshake did not return")
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatalf("peer did not observe orderly closure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled handshake left socket open")
	}
}
