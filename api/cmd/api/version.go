package main

import "runtime/debug"

// buildVersion is the git commit the binary was built from, set when linking:
// go build -ldflags "-X main.buildVersion=<sha>" (the Dockerfile's VERSION argument).
var buildVersion string //nolint:gochecknoglobals // the linker can only set a package-level string (-X)

// version is the running build: buildVersion, else the commit Go recorded when building
// inside the git checkout (with "-dirty" for uncommitted changes), else "dev".
func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	switch {
	case rev == "":
		return "dev"
	case dirty:
		return rev + "-dirty"
	}
	return rev
}
