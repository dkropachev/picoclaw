// Package database defines PicoClaw's provider-neutral, single-owner database
// broker boundary. It contains only logical store identities, typed readiness,
// structured errors, authenticated local IPC, discovery, process fencing, and
// broker lifecycle primitives. Physical database providers and offline
// migration implementations live behind this boundary.
package database
