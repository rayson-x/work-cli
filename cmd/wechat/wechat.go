// Package wechat exposes the bundled local WeChat reader through work-cli.
package wechat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/output"
	"github.com/larksuite/cli/internal/wechatruntime"
	"github.com/spf13/cobra"
)

type processRunner func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

var ensureRuntime = wechatruntime.Ensure
var runProcess processRunner = run

// NewCmd returns an argument-transparent local WeChat reader command.
func NewCmd() *cobra.Command {
	command := &cobra.Command{
		Use:                "wechat <command> [args...]",
		Short:              "Read local WeChat messages and exported files",
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"--help"}
			}
			path, err := ensureRuntime()
			if err != nil {
				return errs.NewConfigError(errs.SubtypeNotConfigured, "%s", err).
					WithHint("run `work-cli update` and retry")
			}
			err = runProcess(cmd.Context(), path, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err == nil {
				return nil
			}
			var exited *exec.ExitError
			if errors.As(err, &exited) {
				return output.ErrBare(exited.ExitCode())
			}
			return errs.NewInternalError(errs.SubtypeUnknown, "run WeChat reader: %s", err).WithCause(err)
		},
	}
	cmdutil.DisableAuthCheck(command)
	command.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		path, err := ensureRuntime()
		if err != nil {
			fmt.Fprintln(cmd.OutOrStdout(), "WeChat reading is unavailable in this build. Run `work-cli update`, then retry.")
			return
		}
		if len(args) == 0 {
			args = []string{"--help"}
		}
		if err := runProcess(cmd.Context(), path, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Unable to show WeChat reader help: %v\n", err)
		}
	})
	return command
}

func run(ctx context.Context, path string, args []string, input io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, path, args...)
	command.Stdin = input
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}

func resetForTest() {
	ensureRuntime = wechatruntime.Ensure
	runProcess = run
}
