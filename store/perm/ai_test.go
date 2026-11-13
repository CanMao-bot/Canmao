package perm

import (
	"math"
	"sync"
	"testing"
)

func TestLegacyAIFlagRequiresNewOwnerAuthorization(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.db.Exec(`INSERT INTO groups(group_id,ai_enabled,bot_enabled) VALUES(10,1,1)`); err != nil {
		t.Fatal(err)
	}
	if s.GroupEnabled(10) {
		t.Fatal("legacy AI flag automatically trusted")
	}
	if !s.BotEnabled(10) {
		t.Fatal("bot setting changed")
	}
	on := true
	if err = s.ConfigureGroupAI(10, 2, 1, GroupAIPatch{Enabled: &on}); err == nil {
		t.Fatal("nonowner configured AI")
	}
	if s.GroupEnabled(10) {
		t.Fatal("unauthorized update committed")
	}
	if err = s.ConfigureGroupAI(10, 1, 1, GroupAIPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	g, err := s.GroupAI(10)
	if err != nil || !g.Enabled || g.EnabledBy != 1 {
		t.Fatal(g, err)
	}
}
func TestGroupAISettingsPersistAndRemainIsolated(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	p := 0.25
	if err = s.ConfigureGroupAI(10, 1, 1, GroupAIPatch{Enabled: &on, MentionOnly: &on, ReplyProbability: &p}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g, err := s.GroupAI(10)
	if err != nil || !g.Enabled || g.MentionOnly == nil || !*g.MentionOnly || g.ReplyProbability == nil || *g.ReplyProbability != p {
		t.Fatal(g, err)
	}
	other, _ := s.GroupAI(20)
	if other.Enabled || other.MentionOnly != nil || other.ReplyProbability != nil {
		t.Fatal("cross-group settings leaked")
	}
	if err = s.ConfigureGroupAI(10, 1, 1, GroupAIPatch{ResetTriggers: true}); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GroupAI(10)
	if !g.Enabled || g.MentionOnly != nil || g.ReplyProbability != nil {
		t.Fatal("reset changed enable state", g)
	}
	var rows int
	if err = s.db.QueryRow(`SELECT count(*) FROM group_ai_audit`).Scan(&rows); err != nil || rows != 2 {
		t.Fatal("missing audits", rows, err)
	}
}
func TestGroupAIProbabilityValidationAndConcurrentPatch(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, p := range []float64{-1, 1.1, math.NaN(), math.Inf(1)} {
		if err = s.ConfigureGroupAI(10, 1, 1, GroupAIPatch{ReplyProbability: &p}); err == nil {
			t.Fatal("invalid probability accepted", p)
		}
	}
	on := true
	p := 0.5
	var wg sync.WaitGroup
	for _, patch := range []GroupAIPatch{{Enabled: &on}, {MentionOnly: &on}, {ReplyProbability: &p}} {
		wg.Add(1)
		go func(patch GroupAIPatch) {
			defer wg.Done()
			if err := s.ConfigureGroupAI(10, 1, 1, patch); err != nil {
				t.Error(err)
			}
		}(patch)
	}
	wg.Wait()
	g, err := s.GroupAI(10)
	if err != nil || !g.Enabled || g.MentionOnly == nil || g.ReplyProbability == nil {
		t.Fatal("concurrent patch lost state", g, err)
	}
}
