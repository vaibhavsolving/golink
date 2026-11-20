package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenRefill(t *testing.T) {
	l := New(1, 3) // 1 token/sec, burst 3
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("ip") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if l.Allow("ip") {
		t.Fatal("4th request should be blocked")
	}

	now = now.Add(time.Second)
	if !l.Allow("ip") {
		t.Fatal("should refill one token after 1s")
	}
	if !l.Allow("other-ip") {
		t.Fatal("keys must be independent")
	}
}

func TestCleanup(t *testing.T) {
	l := New(1, 1)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.Allow("a")
	now = now.Add(10 * time.Minute)
	l.Allow("b")
	if n := l.Cleanup(5 * time.Minute); n != 1 {
		t.Fatalf("cleaned %d, want 1", n)
	}
}
