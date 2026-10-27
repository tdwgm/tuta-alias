// Package consts holds the protocol constants for the Tuta Mail REST API.
// Mirrors tuta_alias/consts.py — see project CLAUDE.md for the upstream
// source-of-truth table.
package consts

const (
	BaseURL = "https://app.tuta.com"
	// ClientVersion is only the fallback `cv` header. Tuta rejects stale
	// client versions with HTTP 474, so internal/api resolves the live value
	// from app.tuta.com/index.js at runtime (see api/clientversion.go).
	ClientVersion = "359.260904.0"

	// ModulePath and RepoURL are where an update comes from.
	ModulePath = "github.com/tdwgm/tuta-alias"
	RepoURL    = "https://github.com/tdwgm/tuta-alias"

	SysApp          = "sys"
	SysModelVersion = 150

	KDFBcrypt   = 0
	KDFArgon2ID = 1

	SecondFactorTypeTOTP = 1
	SecondFactorTypeU2F  = 0

	FormatZero        = "0"
	ErrorBodyMaxBytes = 200
	GroupTypeMail     = 5
)

// ClientIdentifier is the client string sent to Tuta. It carries the version,
// which version.go resolves from the build, so it cannot be a constant.
var ClientIdentifier = "tuta-alias/" + AppVersion() + " go"

// Argon2id parameters — typed for direct passing to argon2.IDKey.
const (
	Argon2IDIterations  uint32 = 4
	Argon2IDMemoryKiB   uint32 = 32 * 1024
	Argon2IDParallelism uint8  = 1
	Argon2IDKeyLength   uint32 = 32
)
