// Package domain holds forge's core model: usage events, attribution of AI
// spend to GitHub issues, pricing and the task lifecycle.
//
// It is pure Go with no I/O and no dependencies on adapters (harnesses,
// GitHub, storage, identity). Everything else in the codebase depends on it,
// never the other way round.
package domain
