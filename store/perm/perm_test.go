package perm

import "testing"

func TestDefaultGroupsDisabled(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 新群默认关闭
	if st.BotEnabled(999) {
		t.Fatal("新群默认应 bot 关闭")
	}
	if st.GroupEnabled(999) {
		t.Fatal("新群默认应 AI 关闭")
	}

	// 开启后生效
	if err := st.SetGroupBot(999, true); err != nil {
		t.Fatal(err)
	}
	if !st.BotEnabled(999) {
		t.Fatal("开启后 bot 应启用")
	}
	if st.GroupEnabled(999) {
		t.Fatal("开启 bot 不应隐式开启 AI")
	}
	on := true
	if err := st.ConfigureGroupAI(999, 1, 1, GroupAIPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if !st.GroupEnabled(999) {
		t.Fatal("开启后 AI 应启用")
	}

	// 关闭后失效
	st.SetGroupBot(999, false)
	if st.BotEnabled(999) {
		t.Fatal("关闭后 bot 应禁用")
	}
}

func TestEnablingAIDoesNotEnableBot(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := true
	if err = s.ConfigureGroupAI(123, 1, 1, GroupAIPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if s.BotEnabled(123) {
		t.Fatal("AI setting implicitly enabled bot")
	}
}
