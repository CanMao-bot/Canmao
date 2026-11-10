package shell

import (
	"context"
	"os/exec"
	"testing"
)

func TestRunShell(t *testing.T) {
	if _, err := exec.LookPath("/bin/bash"); err != nil {
		t.Skip("需要 Linux /bin/bash")
	}
	out, err := runShell(context.Background(), "echo hello && whoami", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("输出为空")
	}
}

func TestRunShellTimeout(t *testing.T) {
	if _, err := exec.LookPath("/bin/bash"); err != nil {
		t.Skip("需要 Linux /bin/bash")
	}
	_, err := runShell(context.Background(), "sleep 5", "", 1)
	if err == nil {
		t.Fatal("应超时报错")
	}
}
