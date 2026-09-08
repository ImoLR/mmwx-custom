package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type coreClient struct {
	client *http.Client
}

func newCoreClient(socketPath string) *coreClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 2 * time.Second}
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: false,
	}
	return &coreClient{client: &http.Client{Transport: transport, Timeout: 4 * time.Second}}
}

func (client *coreClient) snapshot(ctx context.Context) (coreSnapshotResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mmwxc-core/v1/snapshot", nil)
	if err != nil {
		return coreSnapshotResponse{}, err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return coreSnapshotResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return coreSnapshotResponse{}, fmt.Errorf("core snapshot HTTP %d: %s", response.StatusCode, string(data))
	}
	var snapshot coreSnapshotResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return coreSnapshotResponse{}, fmt.Errorf("decode core snapshot: %w", err)
	}
	if snapshot.Version != 1 {
		return coreSnapshotResponse{}, fmt.Errorf("unsupported core interface version %d", snapshot.Version)
	}
	return snapshot, nil
}

func (client *coreClient) apply(ctx context.Context, settings connectionSettings) error {
	body, err := json.Marshal(settings.coreConfig())
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://mmwxc-core/v1/config", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("core config HTTP %d: %s", response.StatusCode, string(data))
	}
	var result struct {
		Success bool `json:"success"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode core config response: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("core rejected config")
	}
	return nil
}
