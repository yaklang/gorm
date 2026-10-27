# Optional DM8 dialect

This package provides database behavior for GORM v1. It contains no SSA models,
application indexes, cache handling, historical-data conversion, or engine build
policy. Importing it registers the `dm` dialect without importing the native DM
protocol driver. Build with `-tags dameng` to enable the driver.

```go
import "github.com/yaklang/gorm/dialects/dameng"

// Read the DSN from private configuration; do not print credentials.
db, err := dameng.Open(dsn)
```

A native URL has this form:

```text
dm://APP_USER:URL_ENCODED_PASSWORD@dm.example:5236?schema=APP_SCHEMA
```

The schema argument is optional; without it, the default user schema is used.
When supplied, its exact selected name is verified before returning the pool.
The connection helper never creates schemas. Applications own provisioning,
privileges, migration approval and their own transaction boundaries.

`OpenDBContext` returns a standard `*sql.DB` for callers that need connection
settings or their own GORM initialization. A caller-supplied `*sql.DB` can also
be used with `gorm.Open("dm", sqlDB)`; that caller then owns its driver and
cancellation behavior. Use the package helpers for the native-driver safeguards
below. Opening through these helpers without the tag returns
`ErrNativeUnavailable` before connecting, with no fallback database.

## Names and types

Quoted table and column names preserve their spelling, including case. Raw SQL
is passed through unchanged; DM folds unquoted names to uppercase. Therefore a
model's default lowercase `item_key` column must be referenced as `"item_key"`
in a raw SQL fragment:

```go
db.Where(`"item_key" = ?`, key).First(&item)
```

GORM struct/map conditions quote model names automatically. Applications with
an existing uppercase schema must select their own model/table naming policy;
this adapter does not rewrite arbitrary SQL or alter existing objects.

Strings default to `VARCHAR(255)` and honor `size`; explicit `text`, `longtext`,
`mediumtext`, `json` and `jsonb` types map to CLOB. The latter two preserve text
payloads; they do not provide native JSONB operators. Byte slices map to BLOB,
booleans to BIT (literal `true`/`false` defaults become `1`/`0`), integer identity
keys to BIGINT IDENTITY, and times to TIMESTAMP(6). Declare CLOB or explicit
lengths for large application payloads.
DM timestamp precision and empty-string behavior remain database behavior;
applications must choose their own versioning and comparison policies.

Metadata lookup matches table and column names exactly. GORM emits explicit
`AddIndex` names as SQL fragments: an unquoted name folds to uppercase, while a
quoted name retains case. Tag-generated index names are quoted. Index lookup
and removal follow the same distinction. Repeated migration reuses existing
names; duplicate-column index detection, data repair and destructive schema
conversion are not performed.

## Batch behavior

`CreateInBatches` executes one INSERT per row, with callbacks and each generated
identity read from that row's result. It does not assume consecutive IDs. The
whole call shares a transaction, or participates in an existing transaction;
this conservative fallback trades throughput for correct identity recovery.

`SaveInBatches` uses a single-source-row MERGE for each input row. The logical
conflict columns come from the caller and must have a database unique key.
Matching rows retain their primary keys and creation timestamps. Non-key fields,
including zero values and soft-delete state, follow the common batch-save
contract. It never implements update as deletion plus insertion. A late failure
rolls back the whole automatically owned call; a caller-owned transaction remains
the caller's responsibility. Returned structs do not receive generated IDs from
upserts, matching the existing API.

The adapter does not add automatic retry of unique conflicts, deadlocks or
connection loss. Applications must validate concurrent workloads and choose
retry/idempotency policy for their transaction. MERGE support and performance
must be checked against the target DM version and configuration.

## Driver isolation and cancellation

Only `native_dm.go` imports the independent `github.com/yaklang/gorm/dm` driver
module from this repository's `dm/` directory. Ordinary builds, including
SQLite/PostgreSQL consumers, do not import its SDK or `plugin` loader. DM-enabled
builds retain the upstream native protocol and optional cipher plugin loader.
The driver snapshot is a module-path relocation of `gitee.com/chunanyong/dm
v1.8.23`; its source, checksums, synchronization and licensing status are recorded
in `dm/UPSTREAM.json` and `dm/README.md`. The parent GORM MIT License does not
relicense the driver.

The child module is independently versioned. A public immutable driver version
must be available before the parent module or applications pin it; local
`replace` directives are not inherited by consumers. The current pseudo-version
is a review candidate, not a tagged driver release. Review the outstanding
upstream redistribution authorization before merging or publishing the mirror.

The child driver's full vet run retains existing upstream findings, including
unreachable parser statements, lock copies, unsafe pointer conversions and
shifts. CI compares every finding's path, line and message against
`.github/scripts/dm_driver_vet_baseline.json` and rejects new or changed findings;
different Go versions may report a subset. It does not disable those analyzers.
The parent GORM default and native adapter vet checks remain strict.

Each physical connection receives a private upstream connector. Handshakes
through this adapter are serialized to protect upstream mutable global state.
Context cancellation closes the underlying socket rather than closing active
statements concurrently. The custom dial registration pool reuses retired names
because the upstream API has no unregister operation. Custom `dialName` DSN
options are rejected by the cancellation-safe helper.

## Verification

Run unit checks without a database:

```sh
go test -mod=readonly -race ./dialects/dameng
go test -mod=readonly -race -tags dameng ./dialects/dameng
```

Live checks use an additional `dm_live` tag so ordinary tests do not silently
skip a missing database. Provision an independent schema whose name starts
with `GORM_DM_`, and store its URL in a private regular file (mode 0600):

```sh
GORM_DM_ALLOW_FIXTURE=1 GORM_DM_DSN_FILE=/private/dm-test.url \
  go test -mod=readonly -tags dameng,dm_live ./dialects/dameng \
  -run '^TestDMLiveGenericOperations$' -count=1 -timeout=3m
```

The test refuses pre-existing fixture tables, creates only its fixture tables,
and drops them afterward. The outer runner owns creation and deletion of the
independent schema. Its cases cover repeat migration, mixed-case names, explicit
indexes, column modification, CLOB/BLOB/boolean readback, generated identities,
in-place MERGE, soft-delete restore, cross-chunk rollback and caller transactions.
Production data import, application-specific queries, concurrent compiler
workloads and production-scale performance require their own validation.
