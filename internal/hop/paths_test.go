package hop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeRel(t *testing.T) {
	for _, bad := range []string{"", "../etc/passwd", "a/../../b", "/abs", "a//b", "C:/x", "a\\b", ".", "a/./b", "x\x00y"} {
		if _, ok := sanitizeRel(bad); ok {
			t.Errorf("sanitizeRel(%q) accepted", bad)
		}
	}
	if got, ok := sanitizeRel("dir/sub/f.txt"); !ok || got != filepath.Join("dir", "sub", "f.txt") {
		t.Errorf("sanitizeRel(dir/sub/f.txt) = %q, %v", got, ok)
	}
}

func TestSanitizeComponent(t *testing.T) {
	for in, want := range map[string]string{"../evil": "_evil", "  ": "unknown", "Mohsin's Mac": "Mohsin's Mac", "a/b:c": "a_b_c"} {
		if got := sanitizeComponent(in); got != want {
			t.Errorf("sanitizeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if uniquePath(f) != f {
		t.Fatal("free path changed")
	}
	os.WriteFile(f, []byte("x"), 0o644)
	if got := uniquePath(f); got != filepath.Join(dir, "a (1).txt") {
		t.Fatalf("got %s", got)
	}
	os.WriteFile(filepath.Join(dir, "a (1).txt"), []byte("x"), 0o644)
	if got := uniquePath(f); got != filepath.Join(dir, "a (2).txt") {
		t.Fatalf("got %s", got)
	}
}
