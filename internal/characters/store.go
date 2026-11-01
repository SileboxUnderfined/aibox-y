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
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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

// NormalizeName is the one canonical spelling used for uniqueness and for
// matching a spoken name. Recognition lowercases text, folds ё to е and turns
// punctuation such as the hyphen in «Человек-паук» into a space, so a stored
// name must be folded the same way before it is compared. Unicode is composed
// first, so a decomposed «и» + combining breve compares equal to «й» and a
// decomposed «е» + diaeresis compares equal to «ё».
func NormalizeName(name string) string {
	name = norm.NFC.String(name)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "ё", "е")
	name = norm.NFC.String(name)
	var b strings.Builder
	space := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case unicode.IsMark(r):
			// Keep marks that NFC cannot compose with the preceding letter;
			// they are part of the spoken token rather than a separator.
			if b.Len() > 0 && !space {
				b.WriteRune(r)
			}
		case !space && b.Len() > 0:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
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
// settings from loading. If valid JSON contains records that cannot be kept,
// the original bytes are copied to a .corrupt file before any later save can
// replace the live file.
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
	if dropped := s.loadLocked(file); dropped {
		if berr := s.backup(data); berr != nil {
			return nil, fmt.Errorf("characters contain invalid records and could not be saved aside: %w", berr)
		}
	}
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

// loadLocked preserves every valid stored record. Names that become equal only
// after normalization are kept: they are real user data, and dropping one here
// would make the next save permanently lose it. Only records that cannot be
// represented at all (empty id/name, the reserved default id, or a repeated
// id) are dropped; Open then keeps a .corrupt copy of the source. Duplicate
// normalized names are resolved explicitly when a voice command selects one.
func (s *Store) loadLocked(file fileData) (dropped bool) {
	seenIDs := map[string]bool{DefaultID: true}
	characters := make([]Character, 0, len(file.Characters))
	for _, c := range file.Characters {
		c = cleanCharacter(c)
		if c.ID == "" || c.Name == "" {
			dropped = true
			continue
		}
		if c.ID == DefaultID || seenIDs[c.ID] {
			dropped = true
			continue
		}
		seenIDs[c.ID] = true
		characters = append(characters, c)
	}
	s.characters = characters
	s.activeID = cleanID(file.Active)
	if s.activeID == DefaultID || !seenIDs[s.activeID] {
		s.activeID = ""
	}
	return dropped
}

func (s *Store) quarantine() error {
	if s.path == "" {
		return nil
	}
	target, err := freeBackupPath(s.path + ".corrupt")
	if err != nil {
		return err
	}
	return os.Rename(s.path, target)
}

// backup copies the current bytes aside without touching the live file, so a
// later sanitized save cannot destroy the original damaged records. Identical
// repeated opens reuse the same backup instead of producing endless copies.
func (s *Store) backup(data []byte) error {
	if s.path == "" {
		return nil
	}
	base := s.path + ".corrupt"
	for i := 1; ; i++ {
		target := base
		if i > 1 {
			target = fmt.Sprintf("%s-%d", base, i)
		}
		existing, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			return os.WriteFile(target, data, 0o600)
		}
		if err != nil {
			return err
		}
		if string(existing) == string(data) {
			return nil
		}
	}
}

func freeBackupPath(base string) (string, error) {
	target := base
	for i := 2; ; i++ {
		_, err := os.Stat(target)
		if errors.Is(err, os.ErrNotExist) {
			return target, nil
		}
		if err != nil {
			return "", err
		}
		target = fmt.Sprintf("%s-%d", base, i)
	}
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
// fallback is active or the stored selection no longer exists.
func (s *Store) ActiveID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeIDLocked()
}

// Snapshot returns the character list and the active id from one locked read,
// so callers cannot observe an id from one moment next to a list from another.
// The active id is always either DefaultID or an id present in the list.
func (s *Store) Snapshot() ([]Character, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAll(s.characters), s.activeIDLocked()
}

// List returns user characters in creation order. The built-in default is
// represented by Default and ActiveID rather than stored in this list.
func (s *Store) List() []Character {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAll(s.characters)
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
	// A legacy file may already contain two speech-equivalent names. Editing
	// the prompt of one of those records must not fail or drop its sibling;
	// only a rename that would create a new collision is rejected.
	sameSpeechName := NormalizeName(s.characters[index].Name) == NormalizeName(name)
	if err := s.checkNameLocked(name, id); err != nil {
		if !(sameSpeechName && errors.Is(err, ErrDuplicateName)) {
			return Character{}, err
		}
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

// activeIDLocked canonicalizes a missing or stale selection to DefaultID so
// ActiveID and Snapshot never expose an id that is absent from the list.
func (s *Store) activeIDLocked() string {
	if s.activeID == "" {
		return DefaultID
	}
	if _, ok := s.findLocked(s.activeID); !ok {
		return DefaultID
	}
	return s.activeID
}

func (s *Store) checkNameLocked(name, selfID string) error {
	normalized := NormalizeName(name)
	if normalized == "" {
		return ErrEmptyName
	}
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

// checkUnique protects the on-disk identity invariants. Normalized name
// duplicates are deliberately not rejected here: a legacy file may contain
// them and must round-trip without losing either record. New collisions are
// prevented at the Create/Update boundary by checkNameLocked.
func checkUnique(list []Character) error {
	ids := map[string]bool{}
	for _, c := range list {
		if c.ID == "" || c.ID == DefaultID || c.Name == "" || ids[c.ID] {
			return ErrDuplicateName
		}
		ids[c.ID] = true
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
	if name == "" || NormalizeName(name) == "" {
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
