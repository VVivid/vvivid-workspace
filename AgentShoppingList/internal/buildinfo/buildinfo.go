// Package buildinfo identifies the immutable binary, including development builds.
package buildinfo

import "runtime"

// Release builds supply these values with -ldflags; no runtime secret enters them.
var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, BuiltAt: BuiltAt, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
}
