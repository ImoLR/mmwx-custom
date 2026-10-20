package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDurableWriteFileKeepsPreviousCopy(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	for _, data := range []string{"first", "second", "third"} {
		if err := durableWriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for file, expected := range map[string]string{path: "third", path + ".bak": "second"} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != expected {
			t.Fatalf("%s = %q, err=%v; want %q", file, data, err, expected)
		}
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions: info=%v err=%v", file, info, err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected exactly primary and one backup: entries=%v err=%v", entries, err)
	}
}

func TestDurableWriteFileFailureCleansTemporary(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := durableWriteFile(path, []byte("new"), 0600); err == nil {
		t.Fatal("expected unreadable previous-file error")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("temporary file leaked: entries=%v err=%v", entries, err)
	}
}
