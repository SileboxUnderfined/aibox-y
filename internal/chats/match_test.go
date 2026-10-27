package chats

import "testing"

func TestSimilarity(t *testing.T) {
	good := [][2]string{
		{"космос", "Космос"},
		{"про космос", "космос"},
		{"космосе", "космос"},
		{"рецепты пиццы", "Рецепт пиццы"},
		{"черные дыры", "Чёрные дыры"},
		{"поход в горы", "поход в горы"},
	}
	for _, c := range good {
		if s := Similarity(c[0], c[1]); s < MatchThreshold {
			t.Errorf("%q vs %q = %.2f, want match", c[0], c[1], s)
		}
	}
	bad := [][2]string{
		{"космос", "рецепты пиццы"},
		{"погода", "поход в горы"},
		{"кот", "котлеты на ужин"},
	}
	for _, c := range bad {
		if s := Similarity(c[0], c[1]); s >= MatchThreshold {
			t.Errorf("%q vs %q = %.2f, want no match", c[0], c[1], s)
		}
	}
}

func TestStore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &Chat{ID: NewID(), Name: "Космос"}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	if got := s.UniqueName("космос", ""); got != "космос 2" {
		t.Errorf("UniqueName = %q", got)
	}
	found, err := s.Find("про космос")
	if err != nil || found.ID != c.ID {
		t.Fatalf("Find = %v, %v", found, err)
	}
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find("космос"); err != ErrNotFound {
		t.Errorf("after delete err = %v", err)
	}
}
