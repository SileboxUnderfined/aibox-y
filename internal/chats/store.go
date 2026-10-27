// Package chats keeps saved LLM conversations as JSON files and finds them
// by a spoken, possibly misrecognized name.
package chats

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aiboxy/internal/providers"
)

type Chat struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Created  time.Time           `json:"created"`
	Updated  time.Time           `json:"updated"`
	Messages []providers.Message `json:"messages"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) List() ([]*Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() ([]*Chat, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Chat
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var c Chat
		if json.Unmarshal(data, &c) == nil && c.ID != "" {
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Save writes the chat atomically so a power cut never leaves half a file.
func (s *Store) Save(c *Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path(c.ID) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path(c.ID))
}

// Delete moves the chat into a trash folder instead of erasing it.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	trash := filepath.Join(s.dir, "trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	return os.Rename(s.path(id), filepath.Join(trash, id+".json"))
}

// UniqueName adds a number when another chat already has the name.
func (s *Store) UniqueName(name, selfID string) string {
	list, _ := s.List()
	taken := map[string]bool{}
	for _, c := range list {
		if c.ID != selfID {
			taken[Normalize(c.Name)] = true
		}
	}
	if !taken[Normalize(name)] {
		return name
	}
	for i := 2; ; i++ {
		candidate := name + " " + itoa(i)
		if !taken[Normalize(candidate)] {
			return candidate
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

var ErrNotFound = errors.New("chat not found")

// Find returns the saved chat whose name best matches a spoken query.
func (s *Store) Find(query string) (*Chat, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	var best *Chat
	bestScore := 0.0
	for _, c := range list {
		if score := Similarity(query, c.Name); score > bestScore {
			best, bestScore = c, score
		}
	}
	if best == nil || bestScore < MatchThreshold {
		return nil, ErrNotFound
	}
	return best, nil
}
