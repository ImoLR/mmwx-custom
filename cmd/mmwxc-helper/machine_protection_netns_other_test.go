//go:build !amd64 && !arm64

package main

// The netns E2E only runs on amd64 and arm64 builders.
const sysSetns = ^uintptr(0)
