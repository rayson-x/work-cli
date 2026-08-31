// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package cmd

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/larksuite/cli/internal/build"
	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/recovery"
	"github.com/larksuite/cli/internal/update"
	"github.com/spf13/cobra"
)

// runRootUpgrade locates the registered `update` subcommand and runs it, so the
// interactive root-command upgrade reuses exactly `work-cli update` behavior
// (install-method detection, output, error handling). Package-level var so
// tests can stub it and avoid real network / self-update.
var runRootUpgrade = func(cmd *cobra.Command) {
	for _, c := range cmd.Root().Commands() {
		if c.Name() == "update" && c.RunE != nil {
			_ = c.RunE(c, nil) // update prints its own output/errors; swallow here
			return
		}
	}
}

var checkRootCachedUpdate = update.CheckCached

// isBareRootInvocation reports whether this is a bare `work-cli` (no subcommand,
// no flags) — the only invocation that triggers the interactive upgrade prompt.
// Mirrors unknownSubcommandRunE's "bare group prints help" branch: args empty
// AND no flag tokens in the raw invocation.
func isBareRootInvocation(args []string) bool {
	return len(args) == 0 && len(flagTokensInArgs(rawInvocationArgs)) == 0
}

// readYes reads one line and reports whether it is an affirmative y/yes.
// EOF / empty / anything else → false (default No, matching the [y/N] prompt).
func readYes(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// offerRootUpgrade prompts for an interactive upgrade when running bare
// `work-cli` in an interactive terminal with a cached newer version. Every
// failure is swallowed — it must never affect help output or the exit code.
func offerRootUpgrade(f *cmdutil.Factory, cmd *cobra.Command, projector *recovery.Projector) {
	if f == nil || !projector.CanReference(recovery.TargetUpdate) {
		return
	}
	ios := f.IOStreams
	// Gates 1/2/3: need to read stdin AND show the prompt on stderr, and require
	// stdout TTY too so this only fires in a pure foreground terminal session.
	if !ios.IsTerminal || !ios.OutIsTerminal || !ios.StderrIsTerminal {
		return
	}
	// Gate 4: cached newer version. CheckCached applies opt-out (shouldSkip)
	// and the IsNewer/semver validation chain; it reads the on-disk cache that
	// the 24h-throttled RefreshCache maintains (CheckCached itself has no TTL).
	info := checkRootCachedUpdate(build.Version)
	if info == nil {
		return
	}
	// Deliberately no target version here: info.Latest comes from the on-disk
	// cache, which has no expiry (the 24h TTL only throttles refreshes, and a
	// failed refresh leaves the old value in place), so it can name a version
	// that is no longer the one npm would install. The version actually
	// installed is resolved live by the update subcommand, which prints
	// "Updating work-cli <cur> -> <latest> via <pm> ..." before installing —
	// that is where the user sees the real target.
	fmt.Fprintf(ios.ErrOut, "A newer work-cli is available (current %s). Upgrade now? [y/N]: ", info.Current)
	if !readYes(ios.In) {
		return
	}
	runRootUpgrade(cmd)
}

// installRootUpgradePrompt wraps the root command's RunE (set to
// unknownSubcommandRunE by installUnknownSubcommandGuard) so a bare `work-cli`
// invocation offers an interactive upgrade before printing help. Non-bare
// invocations are passed straight through, unchanged.
func installRootUpgradePrompt(
	f *cmdutil.Factory,
	root *cobra.Command,
	projector *recovery.Projector,
) {
	inner := root.RunE
	if inner == nil {
		return
	}
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if isBareRootInvocation(args) {
			offerRootUpgrade(f, cmd, projector)
		}
		return inner(cmd, args)
	}
}
