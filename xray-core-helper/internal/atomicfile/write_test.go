package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationBoundaryAndCleanup(t *testing.T) {
	failure := errors.New("injected failure")
	for _, stage := range []string{"prepare", "rename", "directory_sync", "success"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			prepare := func(f *os.File) error {
				if stage == "prepare" {
					return failure
				}
				return f.Chmod(0640)
			}
			rename := func(from, to string) error {
				if stage == "rename" {
					return failure
				}
				return os.Rename(from, to)
			}
			syncDir := func(dir string) error {
				if stage == "directory_sync" {
					return failure
				}
				return SyncDir(dir)
			}
			published, err := write(path, []byte("new"), prepare, rename, syncDir)
			wantPublished := stage == "directory_sync" || stage == "success"
			if published != wantPublished || (stage == "success" && err != nil) || (stage != "success" && !errors.Is(err, failure)) {
				t.Fatalf("published=%v err=%v", published, err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "old"
			if wantPublished {
				want = "new"
			}
			if string(contents) != want {
				t.Fatalf("contents=%q", contents)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			wantMode := os.FileMode(0600)
			if wantPublished {
				wantMode = 0640
			}
			if info.Mode().Perm() != wantMode {
				t.Fatalf("mode=%v", info.Mode())
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary files left: %v %v", entries, err)
			}
		})
	}
}
