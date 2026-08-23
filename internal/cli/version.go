package cli

import (
	"path/filepath"
	"runtime/debug"
	"strings"
)

func resolvedVersion(release string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if normalized := normalizeVersion(release); normalized != "devel" {
		return normalized
	}
	if readBuildInfo != nil {
		if info, ok := readBuildInfo(); ok && info != nil {
			return normalizeVersion(info.Main.Version)
		}
	}
	return "devel"
}

func normalizeVersion(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" || value == "(devel)" || value == "devel" || strings.Contains(value, "+dirty") {
		return "devel"
	}
	return value
}

func versionOutput(version string) string {
	if version == "devel" {
		return "dataporch devel\n"
	}
	return "dataporch v" + version + "\n"
}

func invocationPath(argument string, lookPath func(string) (string, error), abs func(string) (string, error)) (string, error) {
	path := argument
	if !strings.ContainsRune(path, filepath.Separator) && lookPath != nil {
		resolved, err := lookPath(path)
		if err != nil {
			return "", err
		}
		path = resolved
	}
	return abs(path)
}
