// Package database defines the provider-neutral protocol and owner-only local
// IPC foundation for PicoClaw's database broker. It provides opaque store
// identities, readiness, bounded errors, canonical frames, authenticated local
// client/server transport, secure discovery, process fences, broker lifecycle,
// and idempotency records. Physical providers, catalogs, supervision, migration,
// commands, and application adoption are introduced by later layers.
package database
