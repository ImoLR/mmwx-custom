package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnedServiceFileModes(t *testing.T) {
	for name, contents := range map[string]string{
		"mmwxc-core.service": coreServiceUnit, "mmwxc-helper.service": helperServiceUnit,
		"90-mmwxc-owner.conf": ownershipDropIn,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			old := strings.Replace(contents, "LogRateLimitIntervalSec=30s\nLogRateLimitBurst=300\n", "", 1)
			if err := os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			changed, err := repairOwnedServiceFile(path, contents)
			if err != nil || !changed {
				t.Fatalf("repair: changed=%t err=%v", changed, err)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0644 || !exactFileContents(path, contents) {
				t.Fatalf("mode=%o or contents incorrect", info.Mode().Perm())
			}
			if changed, err := repairOwnedServiceFile(path, contents); err != nil || changed {
				t.Fatalf("repair is not idempotent: %t %v", changed, err)
			}
			secret := contents + "Environment=TOKEN=private\n"
			if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(path, 0600)
			if changed, err := repairOwnedServiceFile(path, contents); err == nil || changed {
				t.Fatalf("modified file was accepted: %t %v", changed, err)
			}
			info, _ = os.Stat(path)
			if info.Mode().Perm() != 0600 || !exactFileContents(path, secret) {
				t.Fatal("secret-bearing unit changed")
			}
		})
	}
}

func TestOwnedServiceFileRejectsSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unit")
	if err := os.Symlink("unrelated", path); err != nil {
		t.Fatal(err)
	}
	if changed, err := repairOwnedServiceFile(path, coreServiceUnit); changed || err == nil {
		t.Fatalf("symlink repair: %t %v", changed, err)
	}
}

func TestWriteUnitCorrectsExistingTemporaryMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unit")
	if err := os.WriteFile(path+".new", []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeExactFile(path, ownershipDropIn, 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatalf("written unit mode=%o", info.Mode().Perm())
	}
}

func TestInactiveServiceCache(t *testing.T) {
	var cache inactiveServiceCache
	now := time.Unix(1000, 0)
	calls := 0
	active := false
	probe := func() bool { calls++; return active }
	for i := 0; i < 24; i++ {
		if cache.active(now.Add(time.Duration(i/4)*5*time.Second), false, probe) {
			t.Fatal("inactive service reported active")
		}
	}
	if calls != 1 {
		t.Fatalf("inactive service polled %d times", calls)
	}
	cache.active(now.Add(30*time.Second), false, probe)
	if calls != 2 {
		t.Fatal("cache did not expire")
	}
	active = true
	if !cache.active(now.Add(31*time.Second), true, probe) {
		t.Fatal("new cgroup did not invalidate negative cache immediately")
	}
	active = false
	cache.active(now.Add(32*time.Second), false, probe)
	active = true
	cache.invalidate()
	if !cache.active(now.Add(33*time.Second), false, probe) {
		t.Fatal("lifecycle operation did not invalidate negative cache")
	}
}
