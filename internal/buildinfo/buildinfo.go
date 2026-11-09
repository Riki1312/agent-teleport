// Package buildinfo reports the version of the running agent-teleport build.
package buildinfo

import "runtime/debug"

// Version returns the module version stamped into the build, or "devel" with
// the VCS revision when the build has no module version.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	version := "devel"
	for _, setting := range info.Settings {
		switch {
		case setting.Key == "vcs.revision" && len(setting.Value) >= 12:
			version += " " + setting.Value[:12]
		case setting.Key == "vcs.modified" && setting.Value == "true":
			version += "+dirty"
		}
	}
	return version
}
