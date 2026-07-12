package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Build information, overridden at link time via -ldflags -X (see .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type Config struct {
	DBPath      string
	PIDFile     string
	JSON        bool
	IgnorePaths []string
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return Config{
		DBPath:  filepath.Join(home, ".local", "share", "ltm", "ltm.db"),
		PIDFile: filepath.Join(home, ".local", "run", "ltm.pid"),
		IgnorePaths: []string{
			"/proc",
			"/sys",
			"/dev",
			filepath.Join(home, ".cache"),
			filepath.Join(home, "Library", "Caches"),
		},
	}
}

func Execute() error {
	cfg := defaultConfig()
	// Strip global flags (single- or double-dash) ourselves before handing
	// off to cobra: pflag only recognizes multi-character "--name" flags,
	// not "-name", so a leading "-db" would otherwise reach cobra's own
	// root-level flag routing and fail before parseGlobalFlags ever saw it.
	rest, err := parseGlobalFlags(os.Args[1:], &cfg)
	if err != nil {
		return err
	}
	root := newRootCmd(&cfg)
	root.SetArgs(rest)
	return root.Execute()
}

func newRootCmd(cfg *Config) *cobra.Command {
	ignore := multiStringFlag{values: append([]string{}, cfg.IgnorePaths...)}
	showVersion := false

	root := &cobra.Command{
		Use:           "ltm",
		Short:         "Linux Time Machine",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			cfg.IgnorePaths = ignore.values
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				return printVersion(os.Stdout, cfg.JSON)
			}
			return cmd.Help()
		},
	}

	root.PersistentFlags().StringVar(&cfg.DBPath, "db", cfg.DBPath, "storage path")
	root.PersistentFlags().StringVar(&cfg.PIDFile, "pidfile", cfg.PIDFile, "pid file path")
	root.PersistentFlags().BoolVar(&cfg.JSON, "json", cfg.JSON, "json output")
	root.PersistentFlags().Var(&ignore, "ignore-path", "path prefix to ignore")
	// Local (not persistent): --version only makes sense at the root, and
	// DisableFlagParsing on every subcommand means it wouldn't work there
	// anyway — PersistentFlags would falsely advertise it as inherited.
	root.Flags().BoolVarP(&showVersion, "version", "v", false, "print version")

	root.AddCommand(
		newPassthroughCmd("start", "begin recording (eBPF; requires root)", cfg, runStart),
		newPassthroughCmd("stop", "stop recording", cfg, runStop),
		newPassthroughCmd("status", "show recorder status", cfg, runStatus),
		newPassthroughCmd("timeline", "show filtered event timeline", cfg, runTimeline),
		newPassthroughCmd("watch", "tail new events", cfg, runWatch),
		newPassthroughCmd("diff", "summarize machine-state changes", cfg, runDiff),
		newPassthroughCmd("query", "run plain-English or SQL queries", cfg, runQuery),
		newPassthroughCmd("benchmark", "write synthetic demo events", cfg, runBenchmark),
		newPassthroughCmd("daemon", "run recorder in foreground", cfg, runDaemon),
		newPassthroughCmd("sql", "run read-only SQL", cfg, runSQL),
		newPassthroughCmd("prune", "delete old events and vacuum", cfg, runPrune),
		&cobra.Command{
			Use:   "version",
			Short: "print version information",
			RunE: func(cmd *cobra.Command, args []string) error {
				return printVersion(os.Stdout, cfg.JSON)
			},
		},
	)

	return root
}

func newPassthroughCmd(name, short string, cfg *Config, run func(Config, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:                name,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			local := *cfg
			rest, err := parseGlobalFlags(args, &local)
			if err != nil {
				return err
			}
			return run(local, rest)
		},
	}
}
