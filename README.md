# GORM

GORM V2 moved to https://github.com/go-gorm/gorm

GORM V1 Doc https://v1.gorm.io/

The optional [DM8 dialect](dialects/dameng/README.md) provides generic database
support without application-specific models or schema policies. Its native
protocol driver requires the `dameng` build tag; ordinary builds keep their
existing driver dependencies.

The native driver is maintained as the independently versioned
[`github.com/yaklang/gorm/dm`](dm/README.md) child module. It preserves the pinned
upstream protocol and optional plugin behavior while allowing applications to
download the driver from GitHub-backed Go modules. Its source and licensing
status are documented separately from this parent module's MIT License.
