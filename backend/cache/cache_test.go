package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type row struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// stub 是可注入失败的内存 backend。
type stub struct {
	mu       sync.Mutex
	data     map[string][]byte
	failGet  bool
	failSet  bool
	failDel  bool
	notReady bool
	sets     int
	dels     int
	gets     int
}

func newStub() *stub { return &stub{data: map[string][]byte{}} }

func (s *stub) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.failGet {
		return nil, errors.New("redis down")
	}
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("nil")
	}
	return v, nil
}

func (s *stub) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.failSet {
		return errors.New("redis down")
	}
	s.data[key] = value
	return nil
}

func (s *stub) Del(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dels++
	if s.failDel {
		return errors.New("redis down")
	}
	delete(s.data, key)
	return nil
}

func (s *stub) Ready(context.Context) bool { return !s.notReady }

func (s *stub) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok
}

func TestReadThroughBackfills(t *testing.T) {
	b := newStub()
	c := New(b, time.Minute)
	loads := 0
	load := func() (row, error) {
		loads++
		return row{Name: "kimi-1", Value: 7}, nil
	}

	got, err := ReadThrough(context.Background(), c, "k", load)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "kimi-1" || got.Value != 7 {
		t.Errorf("got %+v", got)
	}
	if !b.has("k") {
		t.Error("miss should backfill the cache")
	}

	if _, err = ReadThrough(context.Background(), c, "k", load); err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Errorf("loads = %d, want 1 (second read should hit cache)", loads)
	}
}

func TestReadThroughLoadError(t *testing.T) {
	c := New(newStub(), time.Minute)
	want := errors.New("not found")
	_, err := ReadThrough(context.Background(), c, "k", func() (row, error) { return row{}, want })
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestReadThroughSurvivesSetFailure(t *testing.T) {
	b := newStub()
	b.failSet = true
	c := New(b, time.Minute)

	got, err := ReadThrough(context.Background(), c, "k", func() (row, error) {
		return row{Name: "kimi-1"}, nil
	})
	if err != nil {
		t.Fatalf("cache write failure must not fail the read: %v", err)
	}
	if got.Name != "kimi-1" {
		t.Errorf("got %+v", got)
	}
	if b.dels == 0 {
		t.Error("failed Set should be followed by Del")
	}
}

func TestReadThroughSurvivesGetFailure(t *testing.T) {
	b := newStub()
	b.failGet = true
	c := New(b, time.Minute)

	got, err := ReadThrough(context.Background(), c, "k", func() (row, error) {
		return row{Name: "kimi-1"}, nil
	})
	if err != nil {
		t.Fatalf("cache read failure must degrade, not fail: %v", err)
	}
	if got.Name != "kimi-1" {
		t.Errorf("got %+v", got)
	}
}

func TestReadThroughDiscardsIncompatibleEntry(t *testing.T) {
	b := newStub()
	b.data["k"] = []byte(`["not","a","row"]`)
	c := New(b, time.Minute)

	got, err := ReadThrough(context.Background(), c, "k", func() (row, error) {
		return row{Name: "fresh"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "fresh" {
		t.Errorf("incompatible entry should be discarded, got %+v", got)
	}
}

func TestReadThroughWithoutBackend(t *testing.T) {
	c := New(nil, time.Minute)
	loads := 0
	load := func() (row, error) {
		loads++
		return row{Name: "kimi-1"}, nil
	}
	for range 2 {
		if _, err := ReadThrough(context.Background(), c, "k", load); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 2 {
		t.Errorf("loads = %d, want 2 (no cache configured)", loads)
	}
}

func TestWriteThroughPersistFailureSkipsCache(t *testing.T) {
	b := newStub()
	c := New(b, time.Minute)
	want := errors.New("pg down")

	err := WriteThrough(context.Background(), c, "k", row{Name: "kimi-1"}, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if b.sets != 0 || b.dels != 0 {
		t.Errorf("failed persist must not touch the cache (sets=%d dels=%d)", b.sets, b.dels)
	}
}

func TestWriteThroughPopulatesCache(t *testing.T) {
	b := newStub()
	c := New(b, time.Minute)

	if err := WriteThrough(context.Background(), c, "k", row{Name: "kimi-1"}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !b.has("k") {
		t.Error("successful persist should populate the cache")
	}
}

func TestWriteThroughSetFailureInvalidates(t *testing.T) {
	b := newStub()
	b.data["k"] = []byte(`{"name":"stale"}`)
	b.failSet = true
	c := New(b, time.Minute)

	persisted := false
	if err := WriteThrough(context.Background(), c, "k", row{Name: "fresh"}, func() error {
		persisted = true
		return nil
	}); err != nil {
		t.Fatalf("cache failure must not fail the write: %v", err)
	}
	if !persisted {
		t.Fatal("persist should have run")
	}
	if b.has("k") {
		t.Error("failed Set should leave the key absent, not stale")
	}
}

func TestWriteThroughWithoutBackend(t *testing.T) {
	c := New(nil, time.Minute)
	if err := WriteThrough(context.Background(), c, "k", row{}, func() error { return nil }); err != nil {
		t.Fatalf("no cache configured should still write: %v", err)
	}
}

func TestReady(t *testing.T) {
	if New(nil, time.Minute).Ready(context.Background()) {
		t.Error("nil backend is never ready")
	}
	b := newStub()
	if !New(b, time.Minute).Ready(context.Background()) {
		t.Error("healthy stub should be ready")
	}
	b.notReady = true
	if New(b, time.Minute).Ready(context.Background()) {
		t.Error("unhealthy stub should not be ready")
	}
}
