# Release Verification

This directory contains documentation for release integrity verification.

## Key Location

The GPG public key is stored at: `update/release_public.key`

## How It Works

1. The public key is embedded into the agent binary at build time via `go:embed`
2. During self-update, the agent verifies `checksums.txt` integrity
3. Future: GPG signature verification of `checksums.txt` (Stage 2)

## Key Rotation

1. Generate new GPG key pair:
   ```bash
   gpg --full-generate-key  # Choose RSA 4096
   gpg --armor --export YOUR_KEY_ID > update/release_public.key
   ```
2. Store private key in GitHub Secrets (`GPG_PRIVATE_KEY`)
3. Rebuild and release

## Build Process

No special build flags needed. The key is automatically embedded when running:
```bash
go build ./...
```
