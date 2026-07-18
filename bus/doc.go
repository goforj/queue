// Package bus preserves the legacy workflow API as a compatibility facade over queue.
//
// Deprecated: use the top-level queue package. The low-level raw-runtime route
// remains temporarily available for existing integrations, but all orchestration
// behavior is owned by queue's internal workflow engine. Compatible model aliases
// resolve to physical root queue types; legacy boundary DTOs and interfaces remain
// here only where aliasing would change source behavior.
package bus
