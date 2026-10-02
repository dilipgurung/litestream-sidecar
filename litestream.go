// Package litestream manages a litestream child process as a sidecar.
//
// It provides two key operations:
//   - RestoreIfNeeded: runs "litestream restore" to recover the database from
//     a replica, but only if the local DB file doesn't already exist.
//   - Replicate: starts "litestream replicate" as a long-running subprocess and
//     returns an *exec.Cmd so the caller can monitor it via cmd.Wait().
//
// The package supports two modes:
//   - Config-file mode: uses a litestream.yml config file.
//   - URL mode: uses the LITESTREAM_REPLICA_URL env var to replicate a single
//     database directly to a replica URL without a config file.
package litestream
