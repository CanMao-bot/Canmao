package ai

import "testing"

func TestCmdOf(t *testing.T) {
	cases := []struct {
		in      string
		cmd, arg string
	}{
		{"/model", "/model", ""},
		{"/model deepseek-v4-pro", "/model", "deepseek-v4-pro"},
		{"/model   deepseek  v4", "/model", "deepseek v4"},
		{"@bot /model deepseek", "@bot", "/model deepseek"},
		{"/provider add deepseek https://api.deepseek.com/v1 sk-x", "/provider", "add deepseek https://api.deepseek.com/v1 sk-x"},
		{"", "", ""},
		{"/ai status", "/ai", "status"},
	}
	for _, c := range cases {
		cmd, arg := cmdOf(c.in)
		if cmd != c.cmd || arg != c.arg {
			t.Errorf("cmdOf(%q) = (%q,%q), want (%q,%q)", c.in, cmd, arg, c.cmd, c.arg)
		}
	}
}
