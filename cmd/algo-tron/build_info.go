package main

// buildCommit is set by release builds with -ldflags. Local go run/build
// commands intentionally keep the readable fallback instead of pretending to
// identify a reproducible release.
var buildCommit = "dev"
