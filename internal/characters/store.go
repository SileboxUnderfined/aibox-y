// Package characters keeps the global list of AIBox characters and which one
// is active. Characters are not tied to a chat: the active choice applies to
// every conversation, and the store never touches the old state, prompt or
// provider data.
package characters

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// DefaultID names the built-in fallback. It is never written as a normal
	// character: an empty active id means this fallback is selected.
	DefaultID = "default"
	// DefaultName is the name shown for the fallback character.
	DefaultName = "По умолчанию"

	// MaxNameRunes, MaxPromptRunes, MaxRules and MaxRuleRunes bound every
	// stored field. They keep a malicious or accidental panel request from
	// filling the small settings partition with an unbounded persona.
	MaxNameRunes   = 80
	MaxPromptRunes = 8000
	MaxRules       = 30
	MaxRuleRunes   = 500

	formatVersion = 1
)

var (
	ErrNotFound      = errors.New("character not found")
	ErrDuplicateName = errors.New("character name already exists")
	ErrEmptyName     = errors.New("character name is empty")
	ErrDefault       = errors.New("the default character cannot be changed")
	ErrNameTooLong   = errors.New("character name is too long")
	ErrPromptTooLong = errors.New("character prompt is too long")
	ErrTooManyRules  = errors.New("too many character rules")
	ErrRuleTooLong   = errors.New("character rule is too long")
)

// Character is one persona: a stable id, a display name, its own prompt and
// a list of rules. The prompt is separate from the rules on purpose: the
// panel can edit them as different fields.
type Character struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Prompt string   `json:"prompt,omitempty"`
	Rules  []string `json:"rules"`
}

// Default is the reserved fallback used when no character is selected and
// after the active character is deleted. Its empty prompt means the existing
// system prompt stays exactly as it is.
func Default() Character {
	return Character{ID: DefaultID, Name: DefaultName, Rules: []string{}}
}

// DefaultCharacter is kept as a descriptive alias for callers that prefer the
// longer name.
func DefaultCharacter() Character { return Default() }

// NormalizeName compares names case-insensitively and without differences in
// leading, trailing or repeated whitespace. It changes only those two
// properties; punctuation and other spellings stay significant.
func NormalizeName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// NewID returns a stable, opaque id for a character. IDs do not depend on the
// name, so renaming a character keeps its identity.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Store is a single global character list with one active choice. All methods
// are safe for concurrent use.
type Store struct {
	path string

	mu         sync.Mutex
	characters []Character
	activeID   string
}

// fileData is the on-disk envelope. Keeping a version and a named character
// list leaves room for future fields without guessing about old files.
type fileData struct {
	Version    int         `json:"version"`
	Active     string      `json:"active,omitempty"`
	Characters []Character `json:"characters"`
}

// Open loads characters from path. A missing file is an empty list with the
// default character active. Malformed JSON is moved aside to a .corrupt file
// and the store starts from the default, so one bad write cannot stop chat
// settings from loading.
func Open(path string) (*Store, error) {
	s := &Store{path: path, characters: []Character{}}
	if path == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	file, err := decode(data)
	if err != nil {
		if qerr := s.quarantine(); qerr != nil {
			return nil, fmt.Errorf("characters are corrupted and could not be saved aside: %w", qerr)
		}
		return s, nil
	}
	s.loadLocked(file)
	return s, nil
}

// decode accepts both the current envelope and a bare JSON array. The bare
// array is a compatibility path for early files that had no active id.
func decode(data []byte) (fileData, error) {
	var file fileData
	if err := json.Unmarshal(data, &file); err == nil {
		return file, nil
	}
	var list []Character
	if err := json.Unmarshal(data, &list); err == nil {
		return fileData{Characters: list}, nil
	}
	return fileData{}, errors.New("invalid character data")
}

// loadLocked sanitizes untrusted or partially corrupted data: bad records are
// dropped, duplicate ids and normalized names keep the first entry, and an
// active id that no longer exists falls back to the default.
func (s *Store) loadLocked(file fileData) {
	seenIDs := map[string]bool{DefaultID: true}
	seenNames := map[string]bool{NormalizeName(DefaultName): true}
	characters := make([]Character, 0, len(file.Characters))
	for _, c := range file.Characters {
		c = cleanCharacter(c)
		if c.ID == "" || c.Name == "" {
			continue
		}
		if c.ID == DefaultID || seenIDs[c.ID] || seenNames[NormalizeName(c.Name)] {
			continue
		}
		seenIDs[c.ID] = true
		seenNames[NormalizeName(c.Name)] = true
		characters = append(characters, c)
	}
	s.characters = characters
	s.activeID = cleanID(file.Active)
	if s.activeID == DefaultID || !seenIDs[s.activeID] {
		s.activeID = ""
	}
}

func (s *Store) quarantine() error {
	if s.path == "" {
		return nil
	}
	base := s.path + ".corrupt"
	target := base
	for i := 2; ; i++ {
		_, err := os.Stat(target)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		target = fmt.Sprintf("%s-%d", base, i)
	}
	return os.Rename(s.path, target)
}

// Active returns the selected character. The built-in default is returned
// when nothing is selected or the selection became invalid.
func (s *Store) Active() Character {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.findLocked(s.activeID); ok {
		return c
	}
	return Default()
}

// ActiveID is the id of the selected character; the default id when the
// fallback is active.
func (s *Store) ActiveID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeID == "" {
		return DefaultID
	}
	return s.activeID
}

// List returns user characters in creation order. The built-in default is
// represented by Default and ActiveID rather than stored in this list.
func (s *Store) List() []Character {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Character, len(s.characters))
	for i, c := range s.characters {
		out[i] = clone(c)
	}
	return out
}

// Get returns one character by id. The default id returns the built-in
// fallback so callers can render every selection uniformly.
func (s *Store) Get(id string) (Character, error) {
	id = cleanID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == DefaultID {
		return Default(), nil
	}
	c, ok := s.findLocked(id)
	if !ok {
		return Character{}, ErrNotFound
	}
	return c, nil
}

// Create adds a character. The id is generated here and cannot be supplied by
// the caller, which keeps identities stable across renames.
func (s *Store) Create(c Character) (Character, error) {
	name := cleanName(c.Name)
	prompt := cleanPrompt(c.Prompt)
	rules := cleanRules(c.Rules)
	if err := validateFields(name, prompt, rules); err != nil {
		return Character{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkNameLocked(name, ""); err != nil {
		return Character{}, err
	}
	c = Character{ID: s.newIDLocked(), Name: name, Prompt: prompt, Rules: rules}
	next := append(cloneAll(s.characters), c)
	if err := s.saveLocked(next, s.activeID); err != nil {
		return Character{}, err
	}
	s.characters = next
	return clone(c), nil
}

// Update changes the name, prompt and rules of an existing character. The id
// is stable and cannot be changed; the default character is reserved.
func (s *Store) Update(c Character) (Character, error) {
	id := cleanID(c.ID)
	if id == DefaultID {
		return Character{}, ErrDefault
	}
	name := cleanName(c.Name)
	prompt := cleanPrompt(c.Prompt)
	rules := cleanRules(c.Rules)
	if err := validateFields(name, prompt, rules); err != nil {
		return Character{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.characters {
		if s.characters[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Character{}, ErrNotFound
	}
	if err := s.checkNameLocked(name, id); err != nil {
		return Character{}, err
	}
	next := cloneAll(s.characters)
	next[index] = Character{ID: id, Name: name, Prompt: prompt, Rules: rules}
	if err := s.saveLocked(next, s.activeID); err != nil {
		return Character{}, err
	}
	s.characters = next
	return clone(next[index]), nil
}

// Delete removes a character. Deleting the active one clears the selection so
// the default takes over immediately.
func (s *Store) Delete(id string) error {
	id = cleanID(id)
	if id == DefaultID {
		return ErrDefault
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.characters {
		if s.characters[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrNotFound
	}
	next := append(cloneAll(s.characters[:index]), cloneAll(s.characters[index+1:])...)
	active := s.activeID
	if active == id {
		active = ""
	}
	if err := s.saveLocked(next, active); err != nil {
		return err
	}
	s.characters = next
	s.activeID = active
	return nil
}

// SetActive selects a character for every chat. An empty id and DefaultID
// both select the built-in fallback.
func (s *Store) SetActive(id string) error {
	id = cleanID(id)
	if id == DefaultID {
		id = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		if _, ok := s.findLocked(id); !ok {
			return ErrNotFound
		}
	}
	if err := s.saveLocked(s.characters, id); err != nil {
		return err
	}
	s.activeID = id
	return nil
}

func (s *Store) findLocked(id string) (Character, bool) {
	if id == "" || id == DefaultID {
		return Default(), id == DefaultID
	}
	for _, c := range s.characters {
		if c.ID == id {
			return clone(c), true
		}
	}
	return Character{}, false
}

func (s *Store) checkNameLocked(name, selfID string) error {
	normalized := NormalizeName(name)
	if normalized == NormalizeName(DefaultName) {
		return ErrDuplicateName
	}
	for _, c := range s.characters {
		if c.ID != selfID && NormalizeName(c.Name) == normalized {
			return ErrDuplicateName
		}
	}
	return nil
}

func (s *Store) newIDLocked() string {
	for {
		id := NewID()
		if id == DefaultID {
			continue
		}
		if _, ok := s.findLocked(id); !ok {
			return id
		}
	}
}

func (s *Store) saveLocked(list []Character, active string) error {
	if s.path == "" {
		return nil
	}
	file := fileData{Version: formatVersion, Active: active, Characters: cloneAll(list)}
	if file.Characters == nil {
		file.Characters = []Character{}
	}
	if err := checkUnique(file.Characters); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
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
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func checkUnique(list []Character) error {
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, c := range list {
		if c.ID == "" || c.ID == DefaultID || ids[c.ID] || names[NormalizeName(c.Name)] {
			return ErrDuplicateName
		}
		ids[c.ID] = true
		names[NormalizeName(c.Name)] = true
	}
	return nil
}

func cleanCharacter(c Character) Character {
	return Character{
		ID:     cleanID(c.ID),
		Name:   cleanName(c.Name),
		Prompt: cleanPrompt(c.Prompt),
		Rules:  cleanRules(c.Rules),
	}
}

func cleanID(id string) string {
	return strings.TrimSpace(id)
}

// cleanName collapses whitespace and trims the name but keeps its case, so
// "  Alice   Smith " is displayed as "Alice Smith".
func cleanName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

func cleanPrompt(prompt string) string {
	return strings.TrimSpace(prompt)
}

// cleanRules stores each rule once, without surrounding whitespace or blank
// entries, and always as a non-nil list.
func cleanRules(rules []string) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule != "" {
			out = append(out, rule)
		}
	}
	return out
}

// validateFields enforces the field limits on already-normalized values. It
// is shared by Create, Update and the panel, so every write path has the same
// bounds and no caller can bypass them by posting directly to the API.
func validateFields(name, prompt string, rules []string) error {
	if name == "" {
		return ErrEmptyName
	}
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return ErrNameTooLong
	}
	if utf8.RuneCountInString(prompt) > MaxPromptRunes {
		return ErrPromptTooLong
	}
	rules = cleanRules(rules)
	if len(rules) > MaxRules {
		return ErrTooManyRules
	}
	for _, rule := range rules {
		if utf8.RuneCountInString(rule) > MaxRuleRunes {
			return ErrRuleTooLong
		}
	}
	return nil
}

func clone(c Character) Character {
	c.Rules = append([]string{}, c.Rules...)
	return c
}

func cloneAll(list []Character) []Character {
	out := make([]Character, len(list))
	for i, c := range list {
		out[i] = clone(c)
	}
	return out
}
