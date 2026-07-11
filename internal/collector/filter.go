package collector

import (
	"path/filepath"
	"strings"
)

var defaultIgnorePaths = []string{
	"/proc",
	"/sys",
	"/dev",
	"/var/cache/apt",
	"/var/cache/dnf",
	"/var/cache/pacman",
}

func newIgnoreRules(extra []string) []string {
	return normalizeIgnoreRules(append(defaultIgnorePaths, extra...))
}

func shouldIgnorePath(rules []string, path string) bool {
	if path == "" {
		return false
	}
	normalized := filepath.Clean(path)
	for _, rule := range rules {
		// Match the rule itself or a path beneath it, so "/proc" does not also
		// swallow "/procession".
		if normalized == rule || strings.HasPrefix(normalized, rule+"/") {
			return true
		}
	}
	return false
}

func normalizeIgnoreRules(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
