package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"regexp"

	"github.com/yaklang/gorm"
)

var connectionGate = make(chan struct{}, 1)
var schemaName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]{0,127}$`)

// Hide connection details in the public message, but preserve cancellation and
// driver causes for errors.Is/errors.As. Callers should avoid logging causes
// containing connection credentials.
type connectionError struct{ cause error }

func (*connectionError) Error() string   { return "DM connection initialization failed" }
func (e *connectionError) Unwrap() error { return e.cause }

func enterConnectionGate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case connectionGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			leaveConnectionGate()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func leaveConnectionGate() { <-connectionGate }

// Open opens a GORM handle with the cancellation-safe native connection pool.
// Build with -tags dameng to enable the protocol driver.
func Open(dsn string) (*gorm.DB, error) {
	db, err := OpenDB(dsn)
	if err != nil {
		return nil, err
	}
	orm, err := gorm.Open(Dialect, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return orm, nil
}

func OpenDB(dsn string) (*sql.DB, error) { return OpenDBContext(context.Background(), dsn) }

// OpenDBContext creates a cancellation-safe pool. Every physical connection
// receives a private upstream connector: the pinned driver mutates connector
// negotiation fields that established connections continue to read. Handshakes
// are also serialized because upstream mutates process-global state.
//
// The optional schema query argument is checked before returning the pool.
// Without it, the driver's default user schema is used. No schema is created.
func OpenDBContext(ctx context.Context, dsn string) (*sql.DB, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != Dialect || u.Host == "" {
		return nil, errors.New("invalid DM connection URL")
	}
	requested := u.Query().Get("schema")
	if requested != "" && !schemaName.MatchString(requested) {
		return nil, errors.New("invalid DM schema name")
	}
	if !NativeEnabled() {
		return nil, ErrNativeUnavailable
	}
	if err := enterConnectionGate(ctx); err != nil {
		return nil, err
	}
	probe, err := sql.Open(Dialect, dsn)
	if err != nil {
		leaveConnectionGate()
		return nil, &connectionError{err}
	}
	base := probe.Driver()
	_ = probe.Close()
	leaveConnectionGate()
	source, ok := base.(driver.DriverContext)
	if !ok {
		return nil, errors.New("DM driver does not support independent connectors")
	}
	db := sql.OpenDB(&privateConnector{dsn: dsn, base: base, source: source})
	var actual string
	if err := db.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL`).Scan(&actual); err != nil {
		_ = db.Close()
		return nil, &connectionError{err}
	}
	if requested != "" && actual != requested {
		_ = db.Close()
		return nil, errors.New("DM connection selected a different schema")
	}
	return db, nil
}

type privateConnector struct {
	dsn    string
	base   driver.Driver
	source driver.DriverContext
}

func (c *privateConnector) Driver() driver.Driver { return c.base }
func (c *privateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := enterConnectionGate(ctx); err != nil {
		return nil, err
	}
	defer leaveConnectionGate()
	dsn := c.dsn
	var socket *connectionSocket
	var request *dialRequest
	var releaseDial func()
	keepDial := false
	connectContext := ctx
	if nativeDriver.matches(c.base) {
		socket = &connectionSocket{}
		request = &dialRequest{context: ctx, socket: socket}
		name, release := registerSocketDial(request)
		releaseDial = release
		defer func() {
			request.connecting.Store(false)
			if !keepDial {
				release()
			}
		}()
		adapted, timeout, err := cancellationDSN(dsn, name)
		if err != nil {
			return nil, err
		}
		dsn, request.timeout = adapted, timeout
		connectContext = context.WithoutCancel(ctx)
	}
	connector, err := c.source.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	if socket == nil {
		return connector.Connect(ctx)
	}
	guard, err := socket.watch(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := connector.Connect(connectContext)
	if canceled := guard.end(); canceled != nil {
		// Upstream can return a typed nil connection on handshake failure.
		if err == nil && conn != nil {
			_ = conn.Close()
		}
		return nil, canceled
	}
	if err != nil {
		socket.abort()
		return nil, err
	}
	keepDial = true
	return &cancellationConn{Conn: conn, socket: socket, releaseDial: releaseDial}, nil
}
