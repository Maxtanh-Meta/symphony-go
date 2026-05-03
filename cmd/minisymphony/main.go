// Command minisymphony is a local-first orchestrator that drives Codex or
// Claude Code on GitHub issues. See SPEC.md for the design.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "run":
		os.Exit(runCommand(args))
	case "doctor":
		os.Exit(doctorCommand(args))
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `minisymphony — drives Codex/Claude on GitHub issues

usage:
  minisymphony run    [--once] --config <path>
  minisymphony doctor          --config <path>

If --config is omitted, the following are searched in order:
  $MINISYMPHONY_CONFIG
  $XDG_CONFIG_HOME/minisymphony/config.yml
  ~/.minisymphony/config.yml`)
}

func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	once := fs.Bool("once", false, "run a single dispatch cycle and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resolved, err := resolveConfigPath(*configPath)
	if err != nil {
		slog.Error("config not found", "err", err)
		return 2
	}
	slog.Info("run", "config", resolved, "once", *once)
	// TODO(M4): wire orchestrator.Run(resolved, *once)
	fmt.Fprintln(os.Stderr, "minisymphony run: not yet implemented (see SPEC.md M4)")
	return 1
}

func doctorCommand(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resolved, err := resolveConfigPath(*configPath)
	if err != nil {
		slog.Error("config not found", "err", err)
		return 2
	}
	slog.Info("doctor", "config", resolved)
	// TODO(M1): wire config.Validate + doctor checks.
	fmt.Fprintln(os.Stderr, "minisymphony doctor: not yet implemented (see SPEC.md M1)")
	return 1
}

func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if v := os.Getenv("MINISYMPHONY_CONFIG"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v + "/minisymphony/config.yml", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no --config and no fallback: %w", err)
	}
	return home + "/.minisymphony/config.yml", nil
}
