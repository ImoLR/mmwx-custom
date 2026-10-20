package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func durableWriteFile(path string, data []byte, perm os.FileMode) error {
	temporary, err := stageDurableFile(path, data, perm)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)

	previous, err := os.ReadFile(path)
	if err == nil {
		// Publish and sync the previous contents before replacing the primary.
		// A crash leaves either the old primary or its durable backup available.
		if err := durableReplaceFile(path+".bak", previous, perm); err != nil {
			return fmt.Errorf("save previous %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncParentDirectory(path)
}

// Recovery uses this after validating the backup, so corrupt primary contents
// can never replace the last good backup.
func durableReplaceFile(path string, data []byte, perm os.FileMode) error {
	temporary, err := stageDurableFile(path, data, perm)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func stageDurableFile(path string, data []byte, perm os.FileMode) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := file.Chmod(perm); err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

func syncParentDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
