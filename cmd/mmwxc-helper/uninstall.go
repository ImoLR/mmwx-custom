package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/scripts"
)

const helperUninstallPath = "/var/lib/mmwxc/uninstall.sh"

func prepareHelperUninstall(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(helperUninstallPath), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(helperUninstallPath, []byte(scripts.UninstallHelper), 0700); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, "/bin/bash", helperUninstallPath, "--prepare").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (executor *commandExecutor) finishHelperUninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The transient service has a different cgroup, so stopping Helper cannot
	// interrupt deletion of its files. Never download code for this action.
	return exec.CommandContext(ctx, "systemd-run", "--quiet", "--collect", "--unit=mmwxc-uninstall", "/bin/bash", helperUninstallPath, "--finish").Run()
}

func uninstallAcknowledged(err error) bool {
	return err == nil || strings.HasPrefix(err.Error(), "HTTP 401:")
}
