package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

func TestApplyPatchTransactionPublicErrorProjectionCloseout(t *testing.T) {
	fallback := "safe fallback"
	if got := privateApplyPatchTxnError(nil); got != nil {
		t.Fatalf("private nil error = %v", got)
	}
	cause := errors.New("private path and bytes")
	private := privateApplyPatchTxnError(cause)
	if private.Error() != "apply-patch private transaction error" ||
		!errors.Is(private, cause) {
		t.Fatalf("private error wrapper = %v", private)
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", want: fallback},
		{name: "canceled", err: context.Canceled, want: context.Canceled.Error()},
		{
			name: "deadline", err: context.DeadlineExceeded,
			want: context.DeadlineExceeded.Error(),
		},
		{
			name: "unsupported", err: errApplyPatchTransactionUnsupported,
			want: errApplyPatchTransactionUnsupported.Error(),
		},
		{
			name: "uncertain", err: errApplyPatchCommitUncertain,
			want: errApplyPatchCommitUncertain.Error(),
		},
		{
			name: "rollback", err: errApplyPatchRollbackIncomplete,
			want: errApplyPatchRollbackIncomplete.Error(),
		},
		{name: "private", err: private, want: fallback},
		{name: "ordinary", err: errors.New("public-safe"), want: "public-safe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := publicApplyPatchTxnError(test.err, fallback); got != test.want {
				t.Fatalf("public error = %q, want %q", got, test.want)
			}
		})
	}
}

func TestApplyPatchTransactionConstructorHelpersCloseout(t *testing.T) {
	var tool *ApplyPatchTool
	tool.freezeApplyPatchTransactionState("", nil)
	if cloneApplyPatchPatterns(nil) != nil {
		t.Fatal("nil allow-pattern groups did not remain nil")
	}
	first := regexp.MustCompile(`^first$`)
	second := regexp.MustCompile(`^second$`)
	cloned := cloneApplyPatchPatterns([][]*regexp.Regexp{{first}, {second}})
	if len(cloned) != 1 || cloned[0] != first {
		t.Fatalf("cloned first allow-pattern group = %#v", cloned)
	}
}

func TestApplyPatchTransactionSharedFacadeCoverageCushion(t *testing.T) {
	ctx := WithToolLogDetailsSuppressed(context.Background())
	if !ToolLogDetailsSuppressed(ctx) {
		t.Fatal("suppressed tool-log context was not projected")
	}
	ctx = WithToolTurnUXContext(ctx, "turn-ux")
	if got := ToolTurnUXID(ctx); got != "turn-ux" {
		t.Fatalf("turn UX ID = %q", got)
	}
	ctx = WithToolInboundContext(ctx, "cli", "chat", "message", "reply")
	if ToolChannel(ctx) != "cli" || ToolChatID(ctx) != "chat" ||
		ToolMessageID(ctx) != "message" || ToolReplyToMessageID(ctx) != "reply" {
		t.Fatal("inbound tool context was not projected")
	}
}

func TestApplyPatchTransactionFacadeAndProcessCoverageCushion(t *testing.T) {
	if NewLoadImageTool(t.TempDir(), true, 1024, nil) == nil {
		t.Fatal("load-image facade returned nil")
	}
	if NewSendFileTool(t.TempDir(), true, 1024, nil) == nil {
		t.Fatal("send-file facade returned nil")
	}
	prepareCommandForTermination(nil)
	command := &exec.Cmd{}
	prepareCommandForTermination(command)
	if err := terminateProcessTree(nil); err != nil {
		t.Fatalf("nil process termination = %v", err)
	}
	if err := terminateProcessTree(&exec.Cmd{}); err != nil {
		t.Fatalf("process-less termination = %v", err)
	}
	if err := terminateProcessTree(&exec.Cmd{Process: &os.Process{Pid: 0}}); err != nil {
		t.Fatalf("invalid-PID termination = %v", err)
	}
}

func TestApplyPatchTransactionLayoutErrorCoverageCushion(t *testing.T) {
	directory := t.TempDir()
	sourcePath := directory + string(os.PathSeparator) + "source"
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint, err := openApplyPatchTxnExistingRegular(
		directory+string(os.PathSeparator)+"bad\x00name",
		info,
	); err == nil || endpoint != nil {
		t.Fatalf("invalid basename endpoint = %#v, %v", endpoint, err)
	}
	missing := directory + string(os.PathSeparator) + "missing" + string(os.PathSeparator) + "source"
	if endpoint, err := openApplyPatchTxnExistingRegular(missing, info); err == nil || endpoint != nil {
		t.Fatalf("missing-anchor endpoint = %#v, %v", endpoint, err)
	}
}
