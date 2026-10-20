package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDurableWriteFileRotatesOneBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, value := range []string{"first", "second", "third"} {
		if err := durableWriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]string{path: "third", path + ".bak": "second"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s: got %q, %v; want %q", path, data, err, want)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s: incorrect mode: %v, %v", path, info, err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("expected only primary and one backup: %v, %v", files, err)
	}
}

func TestDurableWriteFileFailureKeepsPrimaryAndCleansTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if err := durableWriteFile(path, []byte("new"), 0600); err == nil {
		t.Fatal("backup destination is a directory: expected an error")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("primary changed on failure: %q, %v", got, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}

func TestOpenHelperStateCorruption(t *testing.T) {
	cases := map[string][]byte{
		"NUL":       bytes.Repeat([]byte{0}, 4568),
		"truncated": []byte(`{"servers":`),
		"empty":     {},
		"null":      []byte("null"),
	}
	const valid = `{"servers":{"2":{"official_remote_server_id":"2","helper_token_hash":"preserved-token-hash"}}}`
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			for _, backupValid := range []bool{true, false} {
				label := "both_bad"
				if backupValid {
					label = "backup_valid"
				}
				t.Run(label, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "helper-state.json")
					if err := os.WriteFile(path, bad, 0600); err != nil {
						t.Fatal(err)
					}
					backup := []byte(valid)
					if !backupValid {
						backup = []byte(`{"servers":`)
					}
					if err := os.WriteFile(path+".bak", backup, 0600); err != nil {
						t.Fatal(err)
					}
					state, err := openHelperState(path, time.Minute)
					if !backupValid {
						if err == nil || state != nil || !strings.Contains(err.Error(), "backup") {
							t.Fatalf("must fail closed with a clear error: %v, %v", state, err)
						}
						got, readErr := os.ReadFile(path)
						if readErr != nil || !bytes.Equal(got, bad) {
							t.Fatalf("both-bad primary was altered: %q, %v", got, readErr)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if state.data.Servers["2"].HelperTokenHash != "preserved-token-hash" {
						t.Fatal("backup helper token was lost")
					}
					if err := state.saveLocked(); err != nil {
						t.Fatal(err)
					}
					_, restored, err := readHelperState(path + ".bak")
					if err != nil || restored.Servers["2"].HelperTokenHash != "preserved-token-hash" {
						t.Fatalf("save replaced valid backup with corrupt data: %v, %v", restored, err)
					}
				})
			}
		})
	}
}

func TestOpenHelperStateMissingPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper-state.json")
	state, err := openHelperState(path, time.Minute)
	if err != nil || len(state.data.Servers) != 0 {
		t.Fatalf("new installation: %v, %v", state, err)
	}
	if err := os.WriteFile(path+".bak", []byte(`{"servers":{"2":{"helper_token_hash":"retained"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = openHelperState(path, time.Minute)
	if err != nil || state.data.Servers["2"].HelperTokenHash != "retained" {
		t.Fatalf("missing primary must recover backup: %v, %v", state, err)
	}
}
