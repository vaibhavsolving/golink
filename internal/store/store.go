// Package store is a thread-safe, in-memory link store with LRU eviction,
// per-link TTL expiry and atomic snapshot persistence to disk.
package store

import (
	"container/list"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("link not found")
	ErrExpired  = errors.New("link expired")
)

const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Link is a single short link record.
type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"` // zero means never expires
	Hits      uint64    `json:"hits"`
}

func (l *Link) expired(now time.Time) bool {
	return !l.ExpiresAt.IsZero() && now.After(l.ExpiresAt)
}

// Store holds links. The most recently used link sits at the front of the list.
type Store struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
	now      func() time.Time
	codeLen  int
}

// New creates a store that keeps at most capacity links.
func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = 10000
	}
	return &Store{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
		now:      time.Now,
		codeLen:  7,
	}
}

func randomCode(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b), nil
}

// Create stores url under a fresh random code. ttl <= 0 means no expiry.
func (s *Store) Create(url string, ttl time.Duration) (Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var code string
	for {
		c, err := randomCode(s.codeLen)
		if err != nil {
			return Link{}, err
		}
		if _, taken := s.items[c]; !taken {
			code = c
			break
		}
	}

	now := s.now()
	l := &Link{Code: code, URL: url, CreatedAt: now}
	if ttl > 0 {
		l.ExpiresAt = now.Add(ttl)
	}
	s.items[code] = s.order.PushFront(l)
	s.evictLocked()
	return *l, nil
}

// Resolve returns the link for code and records a hit.
func (s *Store) Resolve(code string) (Link, error) {
	return s.get(code, true)
}

// Get returns the link for code without recording a hit.
func (s *Store) Get(code string) (Link, error) {
	return s.get(code, false)
}

func (s *Store) get(code string, hit bool) (Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	el, ok := s.items[code]
	if !ok {
		return Link{}, ErrNotFound
	}
	l := el.Value.(*Link)
	if l.expired(s.now()) {
		s.removeLocked(el)
		return Link{}, ErrExpired
	}
	if hit {
		l.Hits++
	}
	s.order.MoveToFront(el)
	return *l, nil
}

// Len returns the number of stored links.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}

// PurgeExpired removes every expired link and returns how many were removed.
func (s *Store) PurgeExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	removed := 0
	for el := s.order.Back(); el != nil; {
		prev := el.Prev()
		if el.Value.(*Link).expired(now) {
			s.removeLocked(el)
			removed++
		}
		el = prev
	}
	return removed
}

func (s *Store) evictLocked() {
	for s.order.Len() > s.capacity {
		s.removeLocked(s.order.Back())
	}
}

func (s *Store) removeLocked(el *list.Element) {
	s.order.Remove(el)
	delete(s.items, el.Value.(*Link).Code)
}

// Save writes all links to path atomically (temp file + rename), so a crash
// mid-write never leaves a corrupt snapshot behind.
func (s *Store) Save(path string) error {
	s.mu.Lock()
	links := make([]Link, 0, s.order.Len())
	// Oldest first, so Load can rebuild the same LRU order.
	for el := s.order.Back(); el != nil; el = el.Prev() {
		links = append(links, *el.Value.(*Link))
	}
	s.mu.Unlock()

	data, err := json.Marshal(links)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Load restores links from a snapshot. A missing file is not an error.
func (s *Store) Load(path string) (int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var links []Link
	if err := json.Unmarshal(data, &links); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	loaded := 0
	for i := range links {
		l := links[i]
		if l.expired(now) {
			continue
		}
		if old, ok := s.items[l.Code]; ok {
			s.removeLocked(old)
		}
		s.items[l.Code] = s.order.PushFront(&l)
		loaded++
	}
	s.evictLocked()
	return loaded, nil
}
