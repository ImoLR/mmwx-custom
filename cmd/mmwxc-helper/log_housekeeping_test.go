package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJournalSampleIsBoundedAndCountsUnitsWithoutMessages(t *testing.T) {
	input := "{\"_SYSTEMD_UNIT\":\"xray.service\"}\n{\"_SYSTEMD_UNIT\":\"mmw-agent.service\"}\n{\"_SYSTEMD_UNIT\":\"xray.service\"}\n"
	counts, complete := journalUnitCounts(bufio.NewScanner(strings.NewReader(input)), 3)
	if !complete || counts["xray.service"] != 2 || counts["mmw-agent.service"] != 1 {
		t.Fatalf("sample %v complete=%t", counts, complete)
	}
	counts, complete = journalUnitCounts(bufio.NewScanner(strings.NewReader(input)), 1)
	if complete || counts["xray.service"] != 1 {
		t.Fatal("sample limit was ignored")
	}
	if _, complete := journalUnitCounts(bufio.NewScanner(strings.NewReader("invalid\n")), 3); complete {
		t.Fatal("invalid sample marked complete")
	}
}

func TestLogHousekeepingInstallsOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/log/mmw-agent"), 0755); err != nil {
		t.Fatal(err)
	}
	var calls, messages []string
	run := func(_ context.Context, args ...string) error { calls = append(calls, args...); return nil }
	report := func(s string) { messages = append(messages, s) }
	for i := 0; i < 100; i++ {
		installLogHousekeeping(context.Background(), root, run, report)
	}
	if !reflect.DeepEqual(calls, []string{"restart", "systemd-journald.service"}) || len(messages) != 2 {
		t.Fatalf("calls=%v logs=%v", calls, messages)
	}
	for path, want := range map[string]string{"etc/systemd/journald.conf.d/90-mmwxc-cap.conf": journalCap, "etc/logrotate.d/mmw-agent": agentLogrotate} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", path, got, err)
		}
		info, _ := os.Stat(filepath.Join(root, path))
		if info.Mode().Perm() != 0644 {
			t.Fatalf("mode %s: %v", path, info.Mode())
		}
	}
}

func TestLogHousekeepingPreservesExistingSettings(t *testing.T) {
	for _, path := range []string{"etc/systemd/journald.conf", "etc/systemd/journald.conf.d/other.conf"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			capPath := filepath.Join(root, path)
			if err := createHousekeepingFile(capPath, "#SystemMaxUse=1M\n[Journal]\nSystemMaxUse = 64M\n"); err != nil {
				t.Fatal(err)
			}
			rotation := filepath.Join(root, "etc/logrotate.d/mmw-agent")
			if err := createHousekeepingFile(rotation, "existing\n"); err != nil {
				t.Fatal(err)
			}
			installLogHousekeeping(context.Background(), root, func(context.Context, ...string) error { t.Fatal("unexpected restart"); return nil }, func(s string) { t.Fatalf("unexpected log %s", s) })
			if got, _ := os.ReadFile(rotation); string(got) != "existing\n" {
				t.Fatal("rotation overwritten")
			}
			if _, err := os.Stat(filepath.Join(root, "etc/systemd/journald.conf.d/90-mmwxc-cap.conf")); !os.IsNotExist(err) {
				t.Fatal("existing cap ignored")
			}
		})
	}
}

func TestLogHousekeepingIgnoresCommentsAndSkipsMissingAgent(t *testing.T) {
	root := t.TempDir()
	if err := createHousekeepingFile(filepath.Join(root, "etc/systemd/journald.conf"), "#SystemMaxUse=128M\n;SystemMaxUse=128M\nSystemMaxUse=\n"); err != nil {
		t.Fatal(err)
	}
	count := 0
	installLogHousekeeping(context.Background(), root, func(context.Context, ...string) error { count++; return nil }, func(string) {})
	if count != 1 {
		t.Fatal("comment mistaken for cap")
	}
	if _, err := os.Stat(filepath.Join(root, "etc/logrotate.d/mmw-agent")); !os.IsNotExist(err) {
		t.Fatal("agent rule installed without directory")
	}
}
