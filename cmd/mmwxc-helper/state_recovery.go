package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type stateRecoveryPaths struct {
	dropIn         string
	image          string
	core           string
	lastGoodConfig string
	backupRoot     string
	machineID      string
}

func defaultStateRecoveryPaths() stateRecoveryPaths {
	return stateRecoveryPaths{
		dropIn: ownershipDropInPath, image: ownershipImagePath, core: coreBinaryPath,
		lastGoodConfig: ownershipConfigBackup, backupRoot: ownershipBackupRoot,
		machineID: defaultMachineIDPath,
	}
}

func loadLocalStateWithRecovery(path string, paths stateRecoveryPaths) (localState, string, error) {
	state, primaryErr := readLocalState(path)
	if primaryErr == nil {
		return state, "", nil
	}
	backup, backupErr := readLocalState(path + ".bak")
	if errors.Is(primaryErr, os.ErrNotExist) && errors.Is(backupErr, os.ErrNotExist) {
		quarantined, _ := localStateQuarantines(path)
		if len(quarantined) == 0 {
			return localState{Settings: defaultConnectionSettings()}, "", nil
		}
	}
	quarantineErr := quarantineLocalState(path)
	if backupErr == nil {
		data, err := os.ReadFile(path + ".bak")
		if err == nil {
			err = durableReplaceFile(path, data, 0600)
		}
		return backup, "loaded last-good backup " + path + ".bak", errors.Join(quarantineErr, err)
	}
	state = localState{Settings: defaultConnectionSettings()}
	recovery := "quarantined invalid state; started from defaults with external ownership disabled"
	if ownership, err := recoverExternalOwnership(paths); err == nil {
		state.ExternalOwnership = ownership
		state.Takeover = takeoverState{Mode: "takeover", Status: "completed", Message: "external ownership recovered from disk evidence"}
		recovery = "quarantined invalid state; reconstructed external ownership from matching disk evidence"
	}
	if data, err := os.ReadFile(paths.machineID); err == nil && validMachineID(strings.TrimSpace(string(data))) {
		state.MachineID = strings.TrimSpace(string(data))
	}
	if quarantineErr != nil {
		return state, recovery, quarantineErr
	}
	return state, recovery, saveLocalState(path, state)
}

func quarantineLocalState(path string) error {
	quarantined := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, quarantined); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("quarantine state: %w", err)
	}
	names, err := localStateQuarantines(path)
	if err != nil {
		return err
	}
	for len(names) > 2 {
		// Remove also accepts an empty directory quarantined from an unreadable
		// primary, but never recursively deletes unrelated directory contents.
		if err := os.Remove(filepath.Join(filepath.Dir(path), names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return syncParentDirectory(path)
}

func localStateQuarantines(path string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(path)+".corrupt-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func recoverExternalOwnership(paths stateRecoveryPaths) (externalOwnershipState, error) {
	if !exactFileContents(paths.dropIn, ownershipDropIn) {
		return externalOwnershipState{}, errors.New("owner drop-in is missing or inconsistent")
	}
	coreHash, err := sha256File(paths.core)
	if err != nil {
		return externalOwnershipState{}, err
	}
	imageHash, err := sha256File(paths.image)
	if err != nil || coreHash != imageHash {
		return externalOwnershipState{}, errors.New("owned Core and protected image do not match")
	}
	configHash, err := hashValidJSONFile(paths.lastGoodConfig)
	if err != nil {
		return externalOwnershipState{}, err
	}
	entries, err := os.ReadDir(paths.backupRoot)
	if err != nil {
		return externalOwnershipState{}, err
	}
	var newest string
	var newestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return externalOwnershipState{}, err
		}
		if newest == "" || info.ModTime().After(newestTime) || (info.ModTime().Equal(newestTime) && entry.Name() > newest) {
			newest, newestTime = entry.Name(), info.ModTime()
		}
	}
	if newest == "" {
		return externalOwnershipState{}, errors.New("external ownership rollback directory is missing")
	}
	return externalOwnershipState{
		Prepared: true, Enabled: true, BackupDir: filepath.Join(paths.backupRoot, newest),
		ExpectedCoreSHA: coreHash, LastGoodConfigSHA: configHash,
	}, nil
}
