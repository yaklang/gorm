# DM protocol driver module

`github.com/yaklang/gorm/dm` is a module-path relocation of the pinned
`gitee.com/chunanyong/dm v1.8.23` snapshot (DM driver `8.1.4.200`). It provides the
native `database/sql` driver. GORM-specific behavior remains in the parent
module's `dialects/dameng` package; this module does not import GORM.

The initial relocation changes only the module directive and the driver's
self-import paths. It preserves protocol behavior, exported API, upstream
dependencies, copyright headers, and optional cipher plugin loading. The
original README is retained byte-for-byte in `UPSTREAM_README.md`.

## Use and isolation

Import this module when a native driver is required:

```go
import _ "github.com/yaklang/gorm/dm"
```

The driver retains the registered SQL name `dm`. Do not import both this module
and `gitee.com/chunanyong/dm` into one process: both register that same name, and
their exported Go types have different package identities.

The parent GORM adapter still requires the `dameng` build tag to enable its
native driver. This independent child module keeps the driver out of the
parent module's default `./...` package set. DM-enabled builds retain the
upstream optional `plugin` loader; relocating the module does not remove its
platform linking requirements.

## Source and synchronization

`UPSTREAM.json` records the original version, full commit, Go checksums, archive
checksum, and checksums for every upstream and relocated file. Obtain the
pinned archive through the public Go module proxy and verify the relocation:

```sh
curl -fsSLo /tmp/dm-v1.8.23.zip \
  https://proxy.golang.org/gitee.com/chunanyong/dm/@v/v1.8.23.zip
python3 verify_upstream.py --archive /tmp/dm-v1.8.23.zip
GOWORK=off go test -mod=readonly -race ./...
```

The checker rejects added or changed protocol source as well as an archive or
transformation that differs from the recorded snapshot. A future upstream
update must refresh the provenance deliberately and rerun the driver, adapter,
database and supported build regressions. Do not repoint an existing version.

This directory is a separate Go module in the `yaklang/gorm` repository. Its
release tags must have the `dm/` prefix, such as `dm/vX.Y.Z`. Publish and verify a
child module version before pinning it in the parent GORM module or consumers.
Do not rely on a local `replace` directive being inherited by consumers.

## Licensing status

The pinned upstream source contains DM copyright and `All rights reserved`
notices and has no standalone license grant. See `LICENSE` and `UPSTREAM.json`.
The parent GORM MIT License does not relicense this code. The applicable
redistribution authorization remains a separate rights-holder confirmation;
this mirror does not assert that it has been obtained.
