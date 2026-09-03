// Package database defines the provider-neutral values and wire primitives for
// PicoClaw's database broker. It provides opaque store identities, readiness
// state, bounded structured errors, canonical JSON, length-prefixed frames,
// versioned request envelopes, handler contracts, and idempotency records.
// Transport, broker lifecycle, physical providers, and migration orchestration
// are introduced by later layers.
package database
