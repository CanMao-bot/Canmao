package community

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestConcurrentTransfersConserveAndRollback(t *testing.T) {
	s := openTest(t)
	_, err := s.Change("group:1", []int64{1, 2}, 1, "seed", func(m map[int64]*Account) (string, error) { m[1].Coins = 1000; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Change("group:1", []int64{1, 2}, 1, "transfer", func(m map[int64]*Account) (string, error) {
				if m[1].Coins < 20 {
					return "", errors.New("insufficient")
				}
				m[1].Coins -= 20
				m[2].Coins += 20
				return "", nil
			})
			if err != nil && err.Error() != "insufficient" {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	a, _ := s.Account("group:1", 1)
	b, _ := s.Account("group:1", 2)
	if a.Coins != 0 || b.Coins != 1000 {
		t.Fatalf("lost money: %+v %+v", a, b)
	}
	_, err = s.Change("group:1", []int64{1, 2}, 1, "overflow", func(m map[int64]*Account) (string, error) {
		m[2].Coins -= 10
		m[1].Coins = MaxMoney + 1
		return "", nil
	})
	if err == nil {
		t.Fatal("overflow accepted")
	}
	a, _ = s.Account("group:1", 1)
	b, _ = s.Account("group:1", 2)
	if a.Coins != 0 || b.Coins != 1000 {
		t.Fatal("partial transaction committed")
	}
}
func TestDedupConcurrentAndExpiry(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Claim("same-message", now)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("claims=%d", claims)
	}
	ok, err := s.Claim("same-message", now.Add(8*24*time.Hour))
	if err != nil || !ok {
		t.Fatal("expired claim not pruned", err)
	}
}
func TestRestartPreservesGamesAndScopes(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Change("global", []int64{7}, 7, "state", func(m map[int64]*Account) (string, error) {
		a := m[7]
		a.Coins = 123
		a.BiteAt = 100
		a.EscapeAt = 200
		a.Plots = []Plot{{Crop: "小麦", Ready: 300}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.Account("global", 7)
	if err != nil || a.BiteAt != 100 || a.EscapeAt != 200 || a.Plots[0].Ready != 300 || a.Coins != 123 {
		t.Fatalf("state not restored: %+v %v", a, err)
	}
	b, _ := s.Account("group:1", 7)
	if b.Coins != 0 {
		t.Fatal("scope leaked")
	}
}
func TestSettingsConcurrentEditsAndAudit(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	for _, key := range []string{"sign", "farm", "bank", "dice"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			if err := s.ChangeSettings(10, 1, func(g *Settings) error { g.Features[key] = false; return nil }); err != nil {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	g, err := s.Settings(10)
	if err != nil || len(g.Features) != 4 {
		t.Fatal("lost setting update", g, err)
	}
	rows, err := s.Audits(10)
	if err != nil || len(rows) != 4 {
		t.Fatal("missing audit", len(rows), err)
	}
}
