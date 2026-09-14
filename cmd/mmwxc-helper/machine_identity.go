package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func loadOrCreateMachineID(path, fallback string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		value := strings.TrimSpace(string(data))
		if validMachineID(value) {
			return value, nil
		}
		return "", errors.New("persisted machine identity is invalid")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	value := strings.TrimSpace(fallback)
	if !validMachineID(value) {
		bytes := make([]byte, 16)
		if _, err := rand.Read(bytes); err != nil {
			return "", err
		}
		value = hex.EncodeToString(bytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(value+"\n"), 0600); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	return value, nil
}

func validMachineID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
