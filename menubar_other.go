//go:build !darwin

package main

// registerOpenHandler is a no-op on non-macOS platforms; only darwin
// receives kAEOpenDocuments Apple Events.
func registerOpenHandler() {}
