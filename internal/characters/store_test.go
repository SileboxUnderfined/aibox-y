package characters

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "characters.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func mustCreate(t *testing.T, s *Store, c Character) Character {
	t.Helper()
	got, err := s.Create(c)
	if err != nil {
		t.Fatalf("Create(%+v): %v", c, err)
	}
	return got
}

func TestDefaultAndActiveSelection(t *testing.T) {
	s, path := openStore(t)

	active := s.Active()
	if active.ID != DefaultID || active.Name != DefaultName {
		t.Fatalf("initial active = %+v", active)
	}
	if s.ActiveID() != DefaultID || len(s.List()) != 0 {
		t.Fatalf("initial id/list = %q %+v", s.ActiveID(), s.List())
	}
	if got, err := s.Get(" " + DefaultID + " "); err != nil || got.ID != DefaultID {
		t.Fatalf("Get(default) = %+v %v", got, err)
	}

	alice := mustCreate(t, s, Character{Name: "  Алиса   Северина ", Prompt: "  Отвечай как Алиса.  ", Rules: []string{"  Будь краткой. ", "", "Не выдумывай."}})
	if alice.ID == "" || alice.ID == DefaultID {
		t.Fatalf("generated id = %q", alice.ID)
	}
	if alice.Name != "Алиса Северина" || alice.Prompt != "Отвечай как Алиса." || len(alice.Rules) != 2 {
		t.Fatalf("cleaned character = %+v", alice)
	}

	second := mustCreate(t, s, Character{Name: "Шерлок", Prompt: "p", Rules: []string{"r"}})
	if err := s.SetActive(alice.ID); err != nil {
		t.Fatal(err)
	}
	if active := s.Active(); active.ID != alice.ID || active.Prompt != alice.Prompt {
		t.Fatalf("selected active = %+v", active)
	}

	// The selection and every field must survive a process restart.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Active(); got.ID != alice.ID || got.Name != alice.Name || got.Prompt != alice.Prompt || len(got.Rules) != 2 {
		t.Fatalf("active after reopen = %+v", got)
	}
	if list := reopened.List(); len(list) != 2 || list[0].ID != alice.ID || list[1].ID != second.ID {
		t.Fatalf("list after reopen = %+v", list)
	}

	// Select the default with either spelling and persist that choice.
	if err := reopened.SetActive(DefaultID); err != nil {
		t.Fatal(err)
	}
	if reopened.ActiveID() != DefaultID || reopened.Active().ID != DefaultID {
		t.Fatalf("default not selected: %q %+v", reopened.ActiveID(), reopened.Active())
	}
	again, err := Open(path)
	if err != nil || again.ActiveID() != DefaultID {
		t.Fatalf("default choice after reopen = %q %v", again.ActiveID(), err)
	}
	if err := again.SetActive(""); err != nil || again.ActiveID() != DefaultID {
		t.Fatalf("empty id did not select default: %q %v", again.ActiveID(), err)
	}
	if err := again.SetActive("no-such-character"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown selection = %v", err)
	}
}

func TestNameUniquenessNormalizesCaseAndSpaces(t *testing.T) {
	s, _ := openStore(t)
	first := mustCreate(t, s, Character{Name: "Alice   Smith"})

	for _, name := range []string{"alice smith", "  ALICE SMITH  ", "Alice\tSmith", "По  умолчанию"} {
		if _, err := s.Create(Character{Name: name}); !errors.Is(err, ErrDuplicateName) {
			t.Errorf("Create(%q) error = %v, want ErrDuplicateName", name, err)
		}
	}
	if _, err := s.Create(Character{Name: "   "}); !errors.Is(err, ErrEmptyName) {
		t.Errorf("empty name error = %v", err)
	}

	second := mustCreate(t, s, Character{Name: "Bob"})
	if _, err := s.Update(Character{ID: second.ID, Name: "ALICE  smith"}); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("duplicate rename error = %v", err)
	}
	// Keeping one's own name, even with different case/spacing, is allowed.
	updated, err := s.Update(Character{ID: first.ID, Name: "  alice   SMITH ", Prompt: "new"})
	if err != nil || updated.ID != first.ID || updated.Prompt != "new" {
		t.Fatalf("self rename = %+v %v", updated, err)
	}
	if list := s.List(); len(list) != 2 || list[0].ID != first.ID || list[1].Name != "Bob" {
		t.Fatalf("list after rejected operations = %+v", list)
	}
	if _, err := s.Update(Character{ID: "missing", Name: "X"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update error = %v", err)
	}
}

func TestDeleteActiveFallsBackToDefault(t *testing.T) {
	s, path := openStore(t)
	first := mustCreate(t, s, Character{Name: "Первый"})
	second := mustCreate(t, s, Character{Name: "Второй"})
	if err := s.SetActive(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	if s.ActiveID() != DefaultID || s.Active().ID != DefaultID {
		t.Fatalf("active after deleting selection = %q %+v", s.ActiveID(), s.Active())
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ActiveID() != DefaultID {
		t.Fatalf("deleted selection came back: %q", reopened.ActiveID())
	}
	if list := reopened.List(); len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("remaining list = %+v", list)
	}

	if err := s.Delete(DefaultID); !errors.Is(err, ErrDefault) {
		t.Fatalf("delete default = %v", err)
	}
	if _, err := s.Update(Character{ID: DefaultID, Name: "Другой"}); !errors.Is(err, ErrDefault) {
		t.Fatalf("update default = %v", err)
	}
	if err := s.Delete("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing = %v", err)
	}
	// Deleting an inactive character must not disturb the active one.
	if err := s.SetActive(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(second.ID); err != nil || s.ActiveID() != DefaultID {
		t.Fatalf("delete active again = %v %q", err, s.ActiveID())
	}
}

func TestCorruptFileIsPreservedAndStoreRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "characters.json")
	broken := []byte(`{"version":1,"characters":[{"id":"a",`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(corrupt) = %v", err)
	}
	if s.ActiveID() != DefaultID || len(s.List()) != 0 {
		t.Fatalf("corrupt store did not fall back: %q %+v", s.ActiveID(), s.List())
	}
	saved, err := os.ReadFile(path + ".corrupt")
	if err != nil || string(saved) != string(broken) {
		t.Fatalf("corrupt file not preserved: %q %v", saved, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt file still at the live path: %v", err)
	}

	// The recovered store must be fully usable and durable.
	created := mustCreate(t, s, Character{Name: "После сбоя", Prompt: "p"})
	if err := s.SetActive(created.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil || reopened.ActiveID() != created.ID || len(reopened.List()) != 1 {
		t.Fatalf("recovered store after reopen = %+v %v", reopened, err)
	}

	// A second corruption gets its own backup instead of overwriting the first.
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".corrupt-2"); err != nil {
		t.Fatalf("second corrupt backup missing: %v", err)
	}
}

func TestSemanticallyBrokenFileIsSanitized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "characters.json")
	data := `{
  "version": 1,
  "active": "missing",
  "characters": [
    {"id": "", "name": "Без id"},
    {"id": "default", "name": "Подмена"},
    {"id": "one", "name": "  Alice   Smith ", "prompt": " p ", "rules": [" r ", "", "  "]},
    {"id": "one", "name": "Дубликат id"},
    {"id": "two", "name": "alice smith"},
    {"id": "default-name", "name": "по  умолчанию"},
    {"id": "three", "name": "Третий", "prompt": "q", "rules": null}
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 || list[0].ID != "one" || list[0].Name != "Alice Smith" || list[1].ID != "three" {
		t.Fatalf("sanitized list = %+v", list)
	}
	if list[0].Prompt != "p" || len(list[0].Rules) != 1 || list[0].Rules[0] != "r" {
		t.Fatalf("sanitized fields = %+v", list[0])
	}
	if s.ActiveID() != DefaultID {
		t.Fatalf("invalid active id = %q", s.ActiveID())
	}
	if err := s.SetActive("one"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil || reopened.ActiveID() != "one" {
		t.Fatalf("sanitized store after save/reopen = %q %v", reopened.ActiveID(), err)
	}
}

func TestLegacyArrayIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "characters.json")
	legacy := `[{"id":"one","name":" Alice   Smith ","prompt":"p","rules":[" r ",""]}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if list := s.List(); len(list) != 1 || list[0].Name != "Alice Smith" || len(list[0].Rules) != 1 {
		t.Fatalf("legacy list = %+v", list)
	}
	if s.ActiveID() != DefaultID {
		t.Fatalf("legacy active = %q", s.ActiveID())
	}
	if err := s.SetActive("one"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil || reopened.ActiveID() != "one" || len(reopened.List()) != 1 {
		t.Fatalf("legacy store after save/reopen = %+v %v", reopened, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"version": 1`) || !strings.Contains(string(data), `"active": "one"`) {
		t.Fatalf("saved envelope = %s %v", data, err)
	}
}

func TestFailedSaveRollsBackMemoryAndKeepsOldFile(t *testing.T) {
	s, path := openStore(t)
	first := mustCreate(t, s, Character{Name: "Первый", Prompt: "старый"})
	second := mustCreate(t, s, Character{Name: "Второй"})
	if err := s.SetActive(second.ID); err != nil {
		t.Fatal(err)
	}
	before := s.List()
	beforeActive := s.ActiveID()
	beforeFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A missing parent makes every write fail after the in-memory candidate
	// has been built, so this exercises the rollback paths.
	s.path = filepath.Join(t.TempDir(), "missing", "characters.json")
	if _, err := s.Create(Character{Name: "Третий"}); err == nil {
		t.Fatal("Create succeeded with an unwritable path")
	}
	if _, err := s.Update(Character{ID: first.ID, Name: "Изменённый", Prompt: "новый"}); err == nil {
		t.Fatal("Update succeeded with an unwritable path")
	}
	if err := s.Delete(first.ID); err == nil {
		t.Fatal("Delete succeeded with an unwritable path")
	}
	if err := s.SetActive(first.ID); err == nil {
		t.Fatal("SetActive succeeded with an unwritable path")
	}
	if s.ActiveID() != beforeActive {
		t.Fatalf("active changed after failed writes: %q", s.ActiveID())
	}
	after := s.List()
	if len(after) != len(before) || after[0].Name != first.Name || after[0].Prompt != first.Prompt || after[1].ID != second.ID {
		t.Fatalf("memory changed after failed writes:\nbefore %+v\nafter  %+v", before, after)
	}

	// The original file was never replaced, and a normal restart sees it.
	s.path = path
	onDisk, err := os.ReadFile(path)
	if err != nil || string(onDisk) != string(beforeFile) {
		t.Fatalf("state file changed after failed writes: %v", err)
	}
	reopened, err := Open(path)
	if err != nil || reopened.ActiveID() != second.ID || len(reopened.List()) != 2 {
		t.Fatalf("reopened after failed writes = %+v %v", reopened, err)
	}
}

func TestReturnedDataIsACopy(t *testing.T) {
	s, _ := openStore(t)
	rules := []string{"Первое"}
	created := mustCreate(t, s, Character{Name: "Копия", Prompt: "p", Rules: rules})
	created.Rules[0] = "испорчено"
	rules[0] = "тоже испорчено"
	if got, _ := s.Get(created.ID); got.Rules[0] != "Первое" {
		t.Fatalf("stored rules changed through a returned slice: %+v", got)
	}
	list := s.List()
	list[0].Name = "испорчено"
	if got, _ := s.Get(created.ID); got.Name != "Копия" {
		t.Fatalf("stored name changed through List: %+v", got)
	}
}

func TestConcurrentCreatesArePersisted(t *testing.T) {
	s, path := openStore(t)
	const count = 24
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Create(Character{Name: "Персонаж " + string(rune('A'+i)), Rules: []string{"правило"}})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.List()); got != count {
		t.Fatalf("persisted characters = %d, want %d", got, count)
	}
	seen := map[string]bool{}
	for _, c := range reopened.List() {
		if seen[c.ID] {
			t.Fatalf("duplicate id %q", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestSavedFileIsPrivateAndHasNoTemporaryLeftovers(t *testing.T) {
	s, path := openStore(t)
	mustCreate(t, s, Character{Name: "Файл"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("characters file mode = %o, want 600", perm)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file left behind: %v", err)
	}
}
