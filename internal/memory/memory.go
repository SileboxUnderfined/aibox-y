// Package memory keeps short facts about the user that every chat can see.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aiboxy/internal/chats"
)

const (
	MaxFacts = 50
	MaxRunes = 300
)

var (
	ErrFull     = errors.New("memory is full")
	ErrEmpty    = errors.New("empty fact")
	ErrNotFound = errors.New("fact not found")
)

type Fact struct {
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	Added  time.Time `json:"added"`
	Source string    `json:"source"` // "voice", "model" or "panel"
}

type Store struct {
	path  string
	mu    sync.Mutex
	facts []Fact
	now   func() time.Time
}

// Open loads the facts file; a missing file is an empty memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.facts); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) List() []Fact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Fact(nil), s.facts...)
}

// Add saves a fact; the same fact said again is not stored twice.
func (s *Store) Add(text, source string) (Fact, error) {
	text = clean(text)
	if text == "" {
		return Fact{}, ErrEmpty
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.facts {
		if chats.Normalize(f.Text) == chats.Normalize(text) {
			return f, nil
		}
	}
	if len(s.facts) >= MaxFacts {
		return Fact{}, ErrFull
	}
	f := Fact{ID: newID(), Text: text, Added: s.now(), Source: source}
	s.facts = append(s.facts, f)
	if err := s.saveLocked(); err != nil {
		s.facts = s.facts[:len(s.facts)-1]
		return Fact{}, err
	}
	return f, nil
}

func (s *Store) Edit(id, text string) (Fact, error) {
	text = clean(text)
	if text == "" {
		return Fact{}, ErrEmpty
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.facts {
		if f.ID == id {
			old := f.Text
			s.facts[i].Text = text
			if err := s.saveLocked(); err != nil {
				s.facts[i].Text = old
				return Fact{}, err
			}
			return s.facts[i], nil
		}
	}
	return Fact{}, ErrNotFound
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.facts {
		if f.ID == id {
			kept := append(append([]Fact(nil), s.facts[:i]...), s.facts[i+1:]...)
			return s.replaceLocked(kept)
		}
	}
	return ErrNotFound
}

func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceLocked(nil)
}

// Forget deletes the fact that best matches a spoken description of it.
func (s *Store) Forget(spoken string) (Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best, bestScore := -1, forgetThreshold
	for i, f := range s.facts {
		// >= so that of equal matches the newest one goes.
		if score := match(spoken, f.Text); score >= bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return Fact{}, ErrNotFound
	}
	f := s.facts[best]
	kept := append(append([]Fact(nil), s.facts[:best]...), s.facts[best+1:]...)
	return f, s.replaceLocked(kept)
}

// Prompt is the block added to the system prompt, empty with no facts.
func (s *Store) Prompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.facts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Что пользователь рассказал о себе в прошлых разговорах (учитывай, но не перечисляй без просьбы):")
	for _, f := range s.facts {
		b.WriteString("\n- ")
		b.WriteString(f.Text)
	}
	return b.String()
}

func (s *Store) replaceLocked(facts []Fact) error {
	old := s.facts
	s.facts = facts
	if err := s.saveLocked(); err != nil {
		s.facts = old
		return err
	}
	return nil
}

// saveLocked writes the file atomically so a power cut never leaves half of it.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	facts := s.facts
	if facts == nil {
		facts = []Fact{}
	}
	data, err := json.MarshalIndent(facts, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(s.path), ".memory.tmp")
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
	return os.Rename(tmp, s.path)
}

func clean(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimRight(text, " .")
	if r := []rune(text); len(r) > MaxRunes {
		text = string(r[:MaxRunes])
	}
	if text == "" {
		return ""
	}
	r := []rune(text)
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const forgetThreshold = 0.6

// stop words carry no meaning for telling one fact from another, and the
// pronouns differ between "у меня кот" and "у пользователя кот".
var stop = map[string]bool{"что": true, "меня": true, "мне": true, "мой": true, "моя": true, "мое": true, "мои": true, "моего": true, "моей": true, "моих": true, "про": true, "обо": true, "это": true, "как": true, "есть": true, "был": true, "была": true, "было": true, "тоже": true, "очень": true, "еще": true, "пользователь": true, "пользователя": true, "пользователю": true, "пользователем": true, "его": true, "него": true, "ему": true, "так": true, "там": true, "тут": true, "все": true, "всё": true}

// match scores 0..1 how many meaningful words of a spoken query appear in a fact.
func match(query, fact string) float64 {
	var q []string
	for _, w := range strings.Fields(chats.Normalize(query)) {
		if len([]rune(w)) >= 3 && !stop[w] {
			q = append(q, w)
		}
	}
	if len(q) == 0 {
		return 0
	}
	f := strings.Fields(chats.Normalize(fact))
	hit := 0
	for _, qw := range q {
		for _, fw := range f {
			if sameStem(qw, fw) {
				hit++
				break
			}
		}
	}
	return float64(hit) / float64(len(q))
}

// sameStem accepts different endings: "люблю" and "любит", "кот" and "кота".
func sameStem(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	common := 0
	for common < len(ra) && common < len(rb) && ra[common] == rb[common] {
		common++
	}
	shorter := min(len(ra), len(rb))
	return common == shorter || (common >= 3 && common >= shorter-2)
}
