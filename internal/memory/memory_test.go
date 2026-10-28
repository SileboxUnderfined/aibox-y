package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddForgetPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("меня зовут Валерий.", "voice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("Пользователь любит джаз", "model"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("Меня зовут  Валерий", "voice"); err != nil || len(s.List()) != 2 {
		t.Fatalf("duplicate stored: %v %d", err, len(s.List()))
	}
	if _, err := s.Add("  ", "voice"); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty fact: %v", err)
	}
	reopened, err := Open(path)
	if err != nil || len(reopened.List()) != 2 || reopened.List()[0].Text != "Меня зовут Валерий" {
		t.Fatalf("reopen: %v %+v", err, reopened.List())
	}
	if p := reopened.Prompt(); !strings.Contains(p, "- Меня зовут Валерий\n- Пользователь любит джаз") {
		t.Fatalf("prompt = %q", p)
	}
	if _, err := reopened.Forget("что я люблю погоду"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("weak match forgot something: %v", err)
	}
	f, err := reopened.Forget("что я люблю джаз")
	if err != nil || f.Text != "Пользователь любит джаз" {
		t.Fatalf("forget = %+v %v", f, err)
	}
	if err := reopened.Clear(); err != nil || reopened.Prompt() != "" {
		t.Fatalf("clear: %v", err)
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("file after clear = %s", data)
	}
}

func TestLimitAndEdit(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.json"))
	for i := 0; i < MaxFacts; i++ {
		if _, err := s.Add("факт номер "+string(rune('а'+i%32))+string(rune('а'+i/32)), "panel"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Add("лишний", "panel"); !errors.Is(err, ErrFull) {
		t.Fatalf("limit: %v", err)
	}
	id := s.List()[0].ID
	if f, err := s.Edit(id, "новый текст"); err != nil || f.Text != "Новый текст" {
		t.Fatalf("edit = %+v %v", f, err)
	}
	if err := s.Delete(id); err != nil || len(s.List()) != MaxFacts-1 {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
