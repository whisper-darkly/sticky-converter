package main

import (
	overseer "github.com/whisper-darkly/sticky-overseer/v2"
	_ "github.com/whisper-darkly/sticky-converter/converter" // registers "converter" factory via init()
)

// version and commit are injected at build time via -ldflags.
var version = "dev"
var commit = "unknown"

func main() { overseer.RunCLI(version, commit) }
