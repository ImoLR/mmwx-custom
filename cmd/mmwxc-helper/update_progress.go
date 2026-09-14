package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const updateProgressEndpoint = "/api/custom/agent/update-progress"

type componentUpdateProgress struct {
	Component     string    `json:"component"`
	Phase         string    `json:"phase"`
	TargetVersion string    `json:"target_version,omitempty"`
	Message       string    `json:"message,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func postUpdateProgress(ctx context.Context, client *http.Client, cfg config, component, phase, targetVersion, message string) error {
	body, err := json.Marshal(map[string]any{
		"server_id": cfg.ServerID, "helper_version": helperVersion, "component": component,
		"phase": phase, "target_version": targetVersion, "message": sanitizeManagementMessage(message),
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.CustomAPIURL, "/")+updateProgressEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return errors.New("controller rejected update progress")
	}
	return nil
}
