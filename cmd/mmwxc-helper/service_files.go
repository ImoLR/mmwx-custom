package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const helperServicePath = "/etc/systemd/system/mmwxc-helper.service"

const helperServiceUnit = `[Unit]
Description=MMWXC Custom Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/mmwxc-helper.env
ExecStart=/usr/local/bin/mmwxc-helper
StateDirectory=mmwxc-helper
Restart=on-failure
RestartSec=3
LogRateLimitIntervalSec=30s
LogRateLimitBurst=300

[Install]
WantedBy=multi-user.target
`

// Only exact, known secret-free templates may become world-readable. Locally
// modified units (which might embed credentials) retain their permissions.
func repairOwnedServiceFile(path, contents string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("leaving non-regular unit unchanged: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	oldHelper := strings.Replace(helperServiceUnit, "LogRateLimitIntervalSec=30s\nLogRateLimitBurst=300\n", "", 1)
	if string(data) != contents && !(contents == helperServiceUnit && string(data) == oldHelper) {
		return false, fmt.Errorf("leaving modified unit unchanged (contents not verified secret-free): %s", path)
	}
	if string(data) != contents {
		return true, writeExactFile(path, contents, 0644)
	}
	if info.Mode().Perm() != 0644 {
		return true, os.Chmod(path, 0644)
	}
	return false, nil
}
