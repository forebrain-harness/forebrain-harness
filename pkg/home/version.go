package home

import "runtime/debug"

// Version is this build's version, spelled like its git tag ("v0.3.1").
//
// Release builds (make build, the npm packages, the container image) set it at
// link time with -ldflags "-X .../pkg/home.Version=vX.Y.Z". A `go install
// module/cmd/forebrain@vX.Y.Z` build has no link flags, so the version is the
// module version the go command recorded in the binary. A build from a
// checkout gets whatever the go command stamped for it — a pseudo-version, or
// "(devel)".
var Version = ""

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolveVersion(Version, info, ok)
}

// resolveVersion prefers the link-time version, then the recorded module
// version, and reports "(devel)" when neither exists.
func resolveVersion(linked string, info *debug.BuildInfo, ok bool) string {
	if linked != "" {
		return linked
	}
	if ok && info != nil && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
