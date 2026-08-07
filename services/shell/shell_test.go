package shell

import (
	"context"
	"testing"
)

func TestRunShell(t *testing.T) {
	out, err := runShell(context.Background(), "echo hello && whoami", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("输出为空")
	}
}

func TestRunShellTimeout(t *testing.T) {
	_, err := runShell(context.Background(), "sleep 5", "", 1)
	if err == nil {
		t.Fatal("应超时报错")
	}
}
