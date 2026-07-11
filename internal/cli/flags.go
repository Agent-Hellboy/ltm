package cli

import (
	"flag"
	"io"
	"strconv"
	"strings"
)

func parseGlobalFlags(args []string, cfg *Config) ([]string, error) {
	fs := flag.NewFlagSet("ltm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "storage path")
	fs.StringVar(&cfg.PIDFile, "pidfile", cfg.PIDFile, "pid file path")
	fs.BoolVar(&cfg.JSON, "json", false, "json output")
	ignore := multiStringFlag{values: append([]string{}, cfg.IgnorePaths...)}
	fs.Var(&ignore, "ignore-path", "path prefix to ignore")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg.IgnorePaths = ignore.values
	return fs.Args(), nil
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
