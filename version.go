package sikasa

import (
	"log/slog"
	"runtime/debug"
	"strings"
)

// Version is the current version of the sikasa library.
const Version = "v1.2.0"

// GetBuildInfo retrieves the version, commit, and commit/build time of the library.
func GetBuildInfo() (ver string, commit string, commitTime string) {
	ver = Version
	commit = "unknown"
	commitTime = "unknown"

	if info, ok := debug.ReadBuildInfo(); ok {
		// Case 1: Built as a dependency of another module
		for _, dep := range info.Deps {
			if dep.Path == "github.com/dlcuy22/sikasa" {
				commit = dep.Version
				parts := strings.Split(dep.Version, "-")
				if len(parts) == 3 {
					commit = parts[2]
				}
				if len(commit) > 7 {
					commit = commit[:7]
				}
				break
			}
		}

		// Case 2: Running directly from the library repo
		if commit == "unknown" && (info.Main.Path == "github.com/dlcuy22/sikasa" || info.Main.Path == "") {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					commit = setting.Value
					if len(commit) > 7 {
						commit = commit[:7]
					}
				}
				if setting.Key == "vcs.time" {
					commitTime = setting.Value
				}
			}
		}
	}
	return ver, commit, commitTime
}

// printBuildInfo logs the current library version and the git commit.
func printBuildInfo() {
	ver, commit, _ := GetBuildInfo()
	slog.Info("sikasa library initialized", "version", ver, "commit", commit)
}
