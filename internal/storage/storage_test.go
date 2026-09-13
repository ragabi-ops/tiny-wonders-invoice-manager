package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, root
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	store, _ := newStore(t)

	data := []byte("a stored artifact")
	sum := Fingerprint(data)

	path, err := store.Save("documents/2026/09/doc.pdf", Blob{Bytes: data, SHA256: sum})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(path, sum)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("loaded %q, want %q", got, data)
	}
}

func TestLoadRefusesATamperedFile(t *testing.T) {
	store, root := newStore(t)

	data := []byte("the original artifact")
	sum := Fingerprint(data)
	path, err := store.Save("documents/doc.pdf", Blob{Bytes: data, SHA256: sum})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Rewrite the file behind the store's back, as an attacker or a corrupt
	// disk would.
	full := filepath.Join(root, path)
	if err := os.Chmod(full, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(full, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	// It must refuse rather than serve something that is not what was stored.
	if _, err := store.Load(path, sum); err == nil {
		t.Fatal("Load returned a file that does not match its recorded hash")
	} else if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v, want it to name the hash mismatch", err)
	}
}

func TestLoadReportsAMissingFileDistinctly(t *testing.T) {
	store, _ := newStore(t)

	// "The file is gone" and "the file is wrong" need different responses from
	// an operator, so they are different errors.
	_, err := store.Load("documents/never-written.pdf", Fingerprint(nil))
	if err == nil {
		t.Fatal("Load succeeded for a file that was never written")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v, want it to say the file is missing", err)
	}
}

func TestPathsCannotEscapeTheStorageRoot(t *testing.T) {
	store, root := newStore(t)

	// A crafted identifier must not be able to write or read outside the store.
	escapes := []string{
		"../escaped.pdf",
		"../../escaped.pdf",
		"documents/../../escaped.pdf",
		"/etc/passwd",
		"documents/../../../tmp/escaped.pdf",
	}

	for _, path := range escapes {
		t.Run(path, func(t *testing.T) {
			data := []byte("should never land outside")
			written, err := store.Save(path, Blob{Bytes: data, SHA256: Fingerprint(data)})
			if err != nil {
				// Refusing outright is a fine outcome.
				return
			}

			// If it was accepted, the path handed back for storage must be a
			// canonical one inside the root — no traversal left in it.
			if strings.Contains(written, "..") {
				t.Fatalf("Save(%q) returned %q, which still contains traversal", path, written)
			}

			absolute, err := filepath.Abs(filepath.Join(root, written))
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			absoluteRoot, err := filepath.Abs(root)
			if err != nil {
				t.Fatalf("abs root: %v", err)
			}
			if !strings.HasPrefix(absolute, absoluteRoot+string(os.PathSeparator)) {
				t.Fatalf("%q was written to %q, outside the store root %q", path, absolute, absoluteRoot)
			}
			if _, err := os.Stat(absolute); err != nil {
				t.Fatalf("the returned path %q does not point at the written file: %v", written, err)
			}

			// And it must round-trip: what was stored can be read back.
			if _, err := store.Load(written, Fingerprint(data)); err != nil {
				t.Fatalf("Load(%q): %v", written, err)
			}
		})
	}
}

func TestSaveIsAtomic(t *testing.T) {
	store, root := newStore(t)

	data := []byte("published only when complete")
	if _, err := store.Save("documents/atomic.pdf", Blob{Bytes: data, SHA256: Fingerprint(data)}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No temporary files may be left behind; a half-written artifact that looks
	// finished is worse than none.
	entries, err := os.ReadDir(filepath.Join(root, "documents"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}

func TestGeneratedPathsAreFolderedAndPredictable(t *testing.T) {
	moment := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	if got := DocumentPDFPath("abc", moment); got != filepath.Join("documents", "2026", "09", "abc.pdf") {
		t.Errorf("DocumentPDFPath = %q", got)
	}
	if got := AttachmentPath("def", moment, ".png"); got != filepath.Join("attachments", "2026", "09", "def.png") {
		t.Errorf("AttachmentPath = %q", got)
	}
	// Each logo upload gets its own name, so an earlier one is never overwritten
	// and documents issued under it keep rendering as issued.
	first := BusinessLogoPath("one", moment, ".png")
	second := BusinessLogoPath("two", moment, ".png")
	if first == second {
		t.Fatal("two logo uploads resolved to the same path")
	}
}
