package update

import (
	_ "embed"
)

// ReleasePublicKey is the GPG public key used to verify release signatures.
// It is embedded at build time from release_public.key in this directory.
//
// To rotate the key:
// 1. Replace update/release_public.key with the new public key
// 2. Rebuild the binary
//
//go:embed release_public.key
var ReleasePublicKey string
