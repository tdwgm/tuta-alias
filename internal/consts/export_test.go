package consts

import "sync"

// resetVersionCache drops what AppVersion, CommitHash and Modified memoised,
// so a test can change the link-time stamps and see the effect. Production
// code reads each exactly once, which is why they memoise at all.
func resetVersionCache() {
	AppVersion = sync.OnceValue(appVersion)
	CommitHash = sync.OnceValue(commitHash)
	Modified = sync.OnceValue(modified)
	ClientIdentifier = "tuta-alias/" + AppVersion() + " go"
}
