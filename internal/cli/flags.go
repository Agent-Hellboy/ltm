package cli

import (
	"fmt"
	"strconv"
	"strings"
)

func parseGlobalFlags(args []string, cfg *Config) ([]string, error) {
	rest := make([]string, 0, len(args))
	cfg.IgnorePaths = append([]string{}, cfg.IgnorePaths...)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append(rest, args[i:]...)
			return rest, nil
		case arg == "--json":
			cfg.JSON = true
		case arg == "--db":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			i++
			cfg.DBPath = args[i]
		case strings.HasPrefix(arg, "--db="):
			cfg.DBPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--pidfile":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			i++
			cfg.PIDFile = args[i]
		case strings.HasPrefix(arg, "--pidfile="):
			cfg.PIDFile = strings.TrimPrefix(arg, "--pidfile=")
		case arg == "--ignore-path":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			i++
			cfg.IgnorePaths = append(cfg.IgnorePaths, args[i])
		case strings.HasPrefix(arg, "--ignore-path="):
			cfg.IgnorePaths = append(cfg.IgnorePaths, strings.TrimPrefix(arg, "--ignore-path="))
		default:
			rest = append(rest, arg)
		}
	}

	return rest, nil
}

type multiStringFlag struct {
	values []string
}

func (m *multiStringFlag) String() string {
	return strings.Join(m.values, ",")
}

func (m *multiStringFlag) Set(v string) error {
	m.values = append(m.values, v)
	return nil
}

func (m *multiStringFlag) Type() string { return "stringSlice" }

type multiIntFlag struct {
	values []int
}

func (m *multiIntFlag) String() string {
	parts := make([]string, len(m.values))
	for i, v := range m.values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func (m *multiIntFlag) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	m.values = append(m.values, n)
	return nil
}

func (m *multiIntFlag) Type() string { return "intSlice" }

func daemonArgs(cfg Config) []string {
	args := []string{"--db", cfg.DBPath, "--pidfile", cfg.PIDFile}
	for _, path := range customIgnorePaths(cfg.IgnorePaths) {
		args = append(args, "--ignore-path", path)
	}
	return append(args, "daemon", "--foreground")
}

func customIgnorePaths(paths []string) []string {
	defaultSet := sliceToSet(defaultConfig().IgnorePaths)
	var out []string
	for _, path := range paths {
		if !defaultSet[path] {
			out = append(out, path)
		}
	}
	return out
}

func sliceToSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}
