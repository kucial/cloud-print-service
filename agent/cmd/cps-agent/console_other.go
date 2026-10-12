//go:build !windows

package main

// disableQuickEdit is a Windows console setting; other terminals do not pause writes.
func disableQuickEdit() {}
