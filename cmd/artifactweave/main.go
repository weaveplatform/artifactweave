// Command artifactweave is the registry server and its operator commands.
// It stays a thin cobra shim: flags are parsed here and everything else lives
// in internal/ so it can be tested without a process.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/buildinfo"
	"github.com/weaveplatform/artifactweave/internal/config"
)

// exitNotImplemented is returned by commands whose phase has not landed, so
// scripts can tell "not yet" (2) from a real failure (1).
const exitNotImplemented = 2

var errNotImplemented = errors.New("not implemented")

// flagKeys maps persistent flags to the dotted config keys they override.
// Secrets have no flags on purpose: they would end up in shell history.
var flagKeys = map[string]string{
	"listen":     "listen",
	"base-url":   "base_url",
	"data-dir":   "data_dir",
	"log-level":  "log.level",
	"log-format": "log.format",
}

func main() {
	if err := newRoot().Execute(); err != nil {
		var ni *notImplementedError
		if errors.As(err, &ni) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitNotImplemented)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type notImplementedError struct{ phase, what string }

func (e *notImplementedError) Error() string {
	return fmt.Sprintf("%s lands in %s: %v", e.what, e.phase, errNotImplemented)
}

func newRoot() *cobra.Command {
	var configPath string
	root := &cobra.Command{
		Use:           "artifactweave",
		Short:         "Artifactory-compatible artifact registry",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&configPath, "config", "", "path to artifactweave.yaml")
	pf.String("listen", "", "listen address (default "+config.DefaultListen+")")
	pf.String("base-url", "", "public base URL used in embedded links")
	pf.String("data-dir", "", "data directory (default "+config.DefaultDataDir+")")
	pf.String("log-level", "", "debug, info, warn or error")
	pf.String("log-format", "", "json or text")

	load := func(cmd *cobra.Command, file string) (config.Config, error) {
		if file == "" {
			file = configPath
		}
		flags := map[string]string{}
		for name, key := range flagKeys {
			if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
				flags[key] = f.Value.String()
			}
		}
		return config.Load(config.LoadOptions{File: file, Env: config.OSEnv, Flags: flags})
	}

	root.AddCommand(
		serveCmd(load),
		migrateCmd(load),
		configCmd(load),
		keysCmd(load),
		versionCmd(),
	)
	return root
}

func serveCmd(load func(*cobra.Command, string) (config.Config, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the registry",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load(cmd, "")
			if err != nil {
				return err
			}
			if err := cfg.RequireMasterKey(); err != nil {
				return err
			}
			log := newLogger(cfg.Log)
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := app.Run(ctx, cfg, log); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}
}

func migrateCmd(load func(*cobra.Command, string) (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "migrate", Short: "Manage the database schema"}
	run := func(action string, to *int) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cfg, err := load(cmd, "")
			if err != nil {
				return err
			}
			target := -1
			if to != nil {
				target = *to
			}
			return app.Migrate(
				cmd.Context(),
				cfg,
				action,
				target,
				cmd.OutOrStdout(),
			) //nolint:wrapcheck // names the action
		}
	}
	var to int
	down := &cobra.Command{
		Use:   "down",
		Short: "Revert migrations newer than --to (default: the latest one)",
		RunE:  run("down", &to),
	}
	down.Flags().IntVar(&to, "to", -1, "schema version to revert to; 0 reverts everything")
	c.AddCommand(
		&cobra.Command{Use: "up", Short: "Apply every pending migration", RunE: run("up", nil)},
		down,
		&cobra.Command{
			Use:   "version",
			Short: "Print the applied and latest schema versions",
			RunE:  run("version", nil),
		},
	)
	return c
}

func configCmd(load func(*cobra.Command, string) (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Validate or apply configuration"}
	c.AddCommand(&cobra.Command{
		Use:   "validate [file]",
		Short: "Load and validate the application configuration, printing a redacted summary",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := ""
			if len(args) == 1 {
				file = args[0]
			}
			cfg, err := load(cmd, file)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), cfg.String())
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "apply <file>",
		Short: "Apply an Artifactory YAML descriptor",
		Args:  cobra.ExactArgs(1),
		RunE: func(*cobra.Command, []string) error {
			return &notImplementedError{phase: "P2", what: "descriptor apply"}
		},
	})
	return c
}

func keysCmd(load func(*cobra.Command, string) (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "keys", Short: "Manage the master key"}
	c.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Re-seal every secret under master_key (keeping master_key_previous until done)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load(cmd, "")
			if err != nil {
				return err
			}
			return app.RotateKeys(
				cmd.Context(),
				cfg,
				cmd.OutOrStdout(),
			) //nolint:wrapcheck // names the command
		},
	})
	return c
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "artifactweave %s", buildinfo.Version())
			if c := buildinfo.Commit(); c != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (%s", c)
				if d := buildinfo.BuildDate(); d != "" {
					fmt.Fprintf(cmd.OutOrStdout(), ", %s", d)
				}
				fmt.Fprint(cmd.OutOrStdout(), ")")
			}
			fmt.Fprintln(cmd.OutOrStdout())
		},
	}
}

func newLogger(c config.LogConfig) *slog.Logger {
	var level slog.Level
	switch c.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if c.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
