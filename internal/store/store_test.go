package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreateAndResolve(t *testing.T) {
	s := New(10)
	l, err := s.Create("https://example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Code) != 7 {
		t.Fatalf("code length = %d, want 7", len(l.Code))
	}
	got, err := s.Resolve(l.Code)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com" || got.Hits != 1 {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.Resolve("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTTLExpiry(t *testing.T) {
	s := New(10)
	now := time.Now()
	s.now = func() time.Time { return now }
	l, _ := s.Create("https://example.com", time.Minute)

	now = now.Add(2 * time.Minute)
	if _, err := s.Resolve(l.Code); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if s.Len() != 0 {
		t.Fatalf("expired link not removed")
	}
}

func TestPurgeExpired(t *testing.T) {
	s := New(10)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.Create("https://a.com", time.Minute)
	s.Create("https://b.com", time.Hour)
	s.Create("https://c.com", 0)

	now = now.Add(5 * time.Minute)
	if n := s.PurgeExpired(); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d, want 2", s.Len())
	}
}

func TestLRUEviction(t *testing.T) {
	s := New(2)
	a, _ := s.Create("https://a.com", 0)
	b, _ := s.Create("https://b.com", 0)
	s.Get(a.Code) // a is now most recently used
	s.Create("https://c.com", 0)

	if _, err := s.Get(b.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b should have been evicted")
	}
	if _, err := s.Get(a.Code); err != nil {
		t.Fatalf("a should still exist: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "links.json")
	s := New(10)
	l, _ := s.Create("https://example.com", 0)
	s.Resolve(l.Code)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	s2 := New(10)
	n, err := s2.Load(path)
	if err != nil || n != 1 {
		t.Fatalf("loaded %d, err %v", n, err)
	}
	got, err := s2.Get(l.Code)
	if err != nil || got.URL != l.URL || got.Hits != 1 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	n, err := New(10).Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l, err := s.Create("https://example.com", 0)
				if err != nil {
					t.Error(err)
					return
				}
				s.Resolve(l.Code)
			}
		}()
	}
	wg.Wait()
	if s.Len() != 1000 {
		t.Fatalf("len = %d, want 1000 (capacity)", s.Len())
	}
}

func BenchmarkResolve(b *testing.B) {
	s := New(100000)
	l, _ := s.Create("https://example.com", 0)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Resolve(l.Code)
		}
	})
}
