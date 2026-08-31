package wechat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestWechatPassesArgumentsAndStreams(t *testing.T) {
	t.Cleanup(resetForTest)
	ensureRuntime = func() (string, error) { return "C:/runtime/wechat-cli.exe", nil }
	var gotPath string
	var gotArgs []string
	runProcess = func(_ context.Context, path string, args []string, _ io.Reader, out, errOut io.Writer) error {
		gotPath, gotArgs = path, append([]string(nil), args...)
		_, _ = io.WriteString(out, "{\"ok\":true}\n")
		_, _ = io.WriteString(errOut, "warning\n")
		return nil
	}
	command := NewCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"history", "群聊", "--files"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotPath != "C:/runtime/wechat-cli.exe" || len(gotArgs) != 3 || gotArgs[2] != "--files" {
		t.Fatalf("forwarded = %q %#v", gotPath, gotArgs)
	}
	if stdout.String() != "{\"ok\":true}\n" || stderr.String() != "warning\n" {
		t.Fatalf("streams = %q / %q", stdout.String(), stderr.String())
	}
}

func TestWechatForwardsFutureReaderCommandsUnchanged(t *testing.T) {
	t.Cleanup(resetForTest)
	ensureRuntime = func() (string, error) { return "C:/runtime/wechat-cli.exe", nil }
	var gotArgs []string
	runProcess = func(_ context.Context, _ string, args []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}
	command := NewCmd()
	command.SetArgs([]string{"new-reader-command", "--new-flag", "value"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"new-reader-command", "--new-flag", "value"}
	if len(gotArgs) != len(want) {
		t.Fatalf("forwarded arguments = %#v", gotArgs)
	}
	for index := range want {
		if gotArgs[index] != want[index] {
			t.Fatalf("forwarded arguments = %#v, want %#v", gotArgs, want)
		}
	}
}

func TestWechatHelpUsesBundledReaderHelp(t *testing.T) {
	t.Cleanup(resetForTest)
	ensureRuntime = func() (string, error) { return "C:/runtime/wechat-cli.exe", nil }
	var gotArgs []string
	runProcess = func(_ context.Context, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
		gotArgs = append([]string(nil), args...)
		_, _ = io.WriteString(out, "reader help\n")
		return nil
	}
	command := NewCmd()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--help" || stdout.String() != "reader help\n" {
		t.Fatalf("reader help = %#v / %q", gotArgs, stdout.String())
	}
}

func TestWechatReturnsRuntimeFailure(t *testing.T) {
	t.Cleanup(resetForTest)
	ensureRuntime = func() (string, error) { return "", errors.New("not bundled") }
	command := NewCmd()
	command.SetArgs([]string{"doctor"})
	err := command.Execute()
	if err == nil || err.Error() == "" {
		t.Fatal("expected runtime error")
	}
}
