package hop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// sanitizeRel turns a wire path ("a/b/c.txt") into a safe relative filesystem path.
// It rejects empty components, ".", "..", absolute paths, backslashes, colons and NUL.
func sanitizeRel(p string) (string, bool) {
	parts := strings.Split(p, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." || strings.ContainsAny(c, "\\:\x00") {
			return "", false
		}
	}
	return filepath.Join(parts...), true
}

// sanitizeComponent makes a device name safe to use as one folder name.
func sanitizeComponent(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n == 60 {
			break
		}
		if r == '/' || r == '\\' || r == ':' || unicode.IsControl(r) {
			r = '_'
		}
		b.WriteRune(r)
		n++
	}
	s := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	if s == "" {
		return "unknown"
	}
	return s
}

func partPath(dest string) string { return dest + ".part" }

// uniquePath returns the first of name.ext, name (1).ext, name (2).ext, … that does not exist.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); err != nil {
		return p
	}
	dir, base := filepath.Split(p)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		c := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(c); err != nil {
			return c
		}
	}
}
