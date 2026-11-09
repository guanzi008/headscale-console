package version

import "runtime"

// These values are replaced at build time with -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
	GoOS    string `json:"goos"`
	GoArch  string `json:"goarch"`
}

func Get() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		BuiltAt: BuiltAt,
		GoOS:    runtime.GOOS,
		GoArch:  runtime.GOARCH,
	}
}
