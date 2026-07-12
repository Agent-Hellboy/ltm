package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// parseGlobalFlags recognizes global flags at the head of args and stops at
// the first token that isn't one of them — exactly like the stdlib flag
// package's own parsing, which every subcommand's flag.FlagSet already relies
// on. Stopping early (rather than scanning the whole slice) means flag-shaped
// words inside a subcommand's own positional arguments, such as a `query`
// question containing "--db", are never mistaken for global flags. Both
// "-name" and "--name" spellings are accepted, matching how the stdlib flag
// package treats single and double dashes as equivalent.
func parseGlobalFlags(args []string, cfg *Config) ([]string, error) {
	cfg.IgnorePaths = append([]string{}, cfg.IgnorePaths...)

	i := 0
loop:
	for ; i < len(args); i++ {
		name, value, hasValue := splitFlag(args[i])
		switch name {
		case "json":
			if hasValue {
				b, err := strconv.ParseBool(value)
				if err != nil {
					return nil, fmt.Errorf("invalid boolean value %q for -json", value)
				}
				cfg.JSON = b
			} else {
				cfg.JSON = true
			}
		case "db":
			v, err := flagValue(args, &i, name, value, hasValue)
			if err != nil {
				return nil, err
			}
			cfg.DBPath = v
		case "pidfile":
			v, err := flagValue(args, &i, name, value, hasValue)
			if err != nil {
				return nil, err
			}
			cfg.PIDFile = v
		case "ignore-path":
			v, err := flagValue(args, &i, name, value, hasValue)
			if err != nil {
				return nil, err
			}
			cfg.IgnorePaths = append(cfg.IgnorePaths, v)
		default:
			// Not a recognized global flag (including "--" and bare "-"):
			// stop here and pass everything from here on through unchanged.
			break loop
		}
	}

	return append([]string{}, args[i:]...), nil
}

// splitFlag reports whether arg is a "-name" or "--name" style flag, and
// splits out an "=value" suffix if present. name is empty if arg is not
// flag-shaped.
func splitFlag(arg string) (name, value string, hasValue bool) {
	switch {
	case strings.HasPrefix(arg, "--"):
		arg = arg[2:]
	case strings.HasPrefix(arg, "-") && arg != "-":
		arg = arg[1:]
	default:
		return "", "", false
	}
	if arg == "" {
		return "", "", false // bare "--"
	}
	if before, after, found := strings.Cut(arg, "="); found {
		return before, after, true
	}
	return arg, "", false
}

// flagValue resolves the value for a non-boolean flag, either inline
// ("-name=value") or as the next argument ("-name value"), advancing *i past
// whichever tokens it consumed.
func flagValue(args []string, i *int, name, inlineValue string, hasInline bool) (string, error) {
	if hasInline {
		return inlineValue, nil
	}
	if *i+1 >= len(args) {
		return "", fmt.Errorf("flag needs an argument: -%s", name)
	}
	*i++
	return args[*i], nil
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
