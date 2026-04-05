package main

import (
	"bufio"
	"os"
	"strings"
)

// getSanitizedEnv returns a clean environment map for spawned processes.
func getSanitizedEnv() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	// Remove multiplexer/terminal confusion vars
	for _, k := range []string{"TMUX", "TMUX_PANE", "STY", "WINDOW", "WINDOWID", "TERMCAP", "COLUMNS", "LINES"} {
		delete(env, k)
	}
	env["TERM"] = "xterm-256color"
	return env
}

func envToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// getShells reads /etc/shells and returns available shells.
func getShells() []string {
	f, err := os.Open("/etc/shells")
	if err != nil {
		if sh := os.Getenv("SHELL"); sh != "" {
			return []string{sh}
		}
		return []string{"/bin/sh"}
	}
	defer f.Close()

	var shells []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.SplitN(sc.Text(), "#", 2)[0])
		if line != "" {
			shells = append(shells, line)
		}
	}

	// Prefer $SHELL first
	if sh := os.Getenv("SHELL"); sh != "" {
		filtered := []string{sh}
		for _, s := range shells {
			if s != sh {
				filtered = append(filtered, s)
			}
		}
		return filtered
	}
	return shells
}
