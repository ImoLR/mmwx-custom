package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type localState struct {
	Settings              connectionSettings `json:"settings"`
	PendingResult         *managementResult  `json:"pending_result,omitempty"`
	CompletedCommandIDs   []string           `json:"completed_command_ids,omitempty"`
	LastOperation         *managementResult  `json:"last_operation,omitempty"`
	HeartbeatAt           time.Time          `json:"heartbeat_at,omitempty"`
	ControllerConnectedAt time.Time          `json:"controller_connected_at,omitempty"`
	HelperVersion         string             `json:"helper_version,omitempty"`
}

func loadLocalState(path string) (localState, error) {
	state := localState{Settings: defaultConnectionSettings()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return localState{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return localState{}, err
	}
	if state.Settings.OnlineIPGracePeriodSeconds <= 0 {
		state.Settings.OnlineIPGracePeriodSeconds = 30
	}
	if state.Settings.Users == nil {
		state.Settings.Users = []userConnectionSettings{}
	}
	return state, nil
}

func saveLocalState(path string, state localState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
