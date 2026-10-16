package run

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCloneCopiesOnlySlopslintCache(t *testing.T) {
	for _, present := range []bool{true, false} {
		name := "without cache"
		if present {
			name = "with cache"
		}
		t.Run(name, func(t *testing.T) {
			root := repo(t, "local-origin", atmYAML)
			if err := exclude(root, "/.tmp/"); err != nil {
				t.Fatal(err)
			}
			files := []struct {
				path, content string
				mode          os.FileMode
			}{
				{"v0.3.0/slopslint-linux-x64", "\x00pinned binary\xff\n", 0o751},
				{"v0.3.0/SHA256SUMS", "checksum\n", 0o640},
				{"other/nested/file", "other cached file\n", 0o444},
			}
			if err := os.MkdirAll(filepath.Join(root, ".tmp", "other-cache"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(root, ".tmp", "other-cache", "ignored"), "do not copy")
			if present {
				for _, f := range files {
					path := filepath.Join(root, ".tmp", "slopslint", f.path)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					writeFixture(t, path, f.content)
					if err := os.Chmod(path, f.mode); err != nil {
						t.Fatal(err)
					}
				}
			}
			dir, _, _, err := clone(root, "main", "", "", nil)
			t.Cleanup(func() {
				if err := release(dir, ""); err != nil {
					t.Error(err)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if !present {
				if _, err := os.Stat(filepath.Join(dir, ".tmp")); !os.IsNotExist(err) {
					t.Fatalf("clone without cache has .tmp: %v", err)
				}
				return
			}
			for _, f := range files {
				mode := cachedSourceMode(t, filepath.Join(root, ".tmp", "slopslint", f.path), f.mode)
				assertCachedFile(t, filepath.Join(dir, ".tmp", "slopslint", f.path), f.content, mode)
			}
			if _, err := os.Stat(filepath.Join(dir, ".tmp", "other-cache")); !os.IsNotExist(err) {
				t.Fatalf("unrelated cache was copied: %v", err)
			}
		})
	}
}

func cachedSourceMode(t *testing.T, path string, mode os.FileMode) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode() != mode {
		t.Errorf("source %s: mode %v, want %v", path, info.Mode(), mode)
	}
	return info.Mode()
}

func assertCachedFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("cached %s: got %q, %v; want %q", path, data, err, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != mode {
		t.Errorf("cached %s: mode %v, want %v", path, info.Mode(), mode)
	}
}
