package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

type updateProgressDetails struct {
	Percent    *int
	BytesDone  int64
	BytesTotal int64
	LogLine    string
}

var progressPrivateText = regexp.MustCompile(`(?i)(?:https?|ftp)://\S+|\b(?:authorization|bearer|token|password|secret|api[_-]?key)\s*[:=]?\s*\S+|\b(?:[a-z0-9_-]+\.)+[a-z]{2,}\b|\b(?:\d{1,3}\.){3}\d{1,3}\b|[a-z0-9_+/=-]{32,}`)

func progressMessage(value, token string) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, "[已隐藏]")
	}
	value = progressPrivateText.ReplaceAllString(value, "[已隐藏]")
	return strings.ToValidUTF8(sanitizeManagementMessage(strings.Join(strings.Fields(value), " ")), "")
}

func postUpdateProgress(ctx context.Context, client *http.Client, cfg config, component, phase, targetVersion, message string, details ...updateProgressDetails) error {
	fields := map[string]any{
		"server_id": cfg.ServerID, "helper_version": helperVersion, "component": component,
		"phase": phase, "target_version": targetVersion, "message": progressMessage(message, cfg.Token),
	}
	if len(details) > 0 {
		detail := details[0]
		if detail.Percent != nil {
			fields["percent"] = *detail.Percent
		}
		if detail.BytesTotal > 0 {
			fields["bytes_done"], fields["bytes_total"] = detail.BytesDone, detail.BytesTotal
		}
		if detail.LogLine != "" {
			fields["log_line"] = progressMessage(detail.LogLine, cfg.Token)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		body, err := json.Marshal(fields)
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
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return nil
		}
		if response.StatusCode != http.StatusBadRequest || attempt != 0 || len(fields) == 6 {
			break
		}
		// Old controllers reject unknown fields. Retry once with their exact schema.
		if fields["message"] == "" && fields["log_line"] != nil {
			fields["message"] = fields["log_line"]
		}
		for _, field := range []string{"percent", "bytes_done", "bytes_total", "log_line"} {
			delete(fields, field)
		}
	}
	return errors.New("controller rejected update progress")
}

type progressPost struct {
	component, phase, version, message string
	details                            []updateProgressDetails
}

type updateProgressReporter struct {
	queue  chan progressPost
	done   chan struct{}
	cancel context.CancelFunc
}

func newUpdateProgressReporter(client *http.Client, cfg config) *updateProgressReporter {
	ctx, cancel := context.WithCancel(context.Background())
	reporter := &updateProgressReporter{queue: make(chan progressPost, 64), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(reporter.done)
		for item := range reporter.queue {
			if ctx.Err() != nil {
				return
			}
			postCtx, postCancel := context.WithTimeout(ctx, 5*time.Second)
			_ = postUpdateProgress(postCtx, client, cfg, item.component, item.phase, item.version, item.message, item.details...)
			postCancel()
		}
	}()
	return reporter
}

func (reporter *updateProgressReporter) report(component, phase, version, message string, details ...updateProgressDetails) {
	// The update never waits for the controller. A slow/unreachable controller
	// may lose progress samples, but cannot block installation or flood goroutines.
	select {
	case reporter.queue <- progressPost{component, phase, version, message, details}:
	default:
	}
}

func (reporter *updateProgressReporter) close() {
	close(reporter.queue)
	defer reporter.cancel()
	// Only the exiting worker waits, after installation/rollback has finished.
	select {
	case <-reporter.done:
	case <-time.After(5 * time.Second):
	}
}

type downloadProgressWriter struct {
	total, done int64
	lastPercent int
	lastAt      time.Time
	now         func() time.Time
	report      func(updateProgressDetails)
}

func (writer *downloadProgressWriter) Write(data []byte) (int, error) {
	writer.done += int64(len(data))
	if writer.total <= 0 {
		return len(data), nil
	}
	percent := int(min(writer.done, writer.total) * 100 / writer.total)
	now := writer.now()
	if percent-writer.lastPercent >= 5 && now.Sub(writer.lastAt) >= time.Second {
		writer.lastAt, writer.lastPercent = now, percent
		writer.report(updateProgressDetails{Percent: &percent, BytesDone: min(writer.done, writer.total), BytesTotal: writer.total})
	}
	return len(data), nil
}

func (manager *lifecycleManager) updateHealthProgress(version string, timeout time.Duration) func() {
	started := time.Now()
	last := 0
	return func() {
		n := min(int(time.Since(started)/time.Second)+1, int(timeout/time.Second))
		if n != last {
			last = n
			manager.reportProgress("core", "reconnecting", version, fmt.Sprintf("等待健康检查 %d/%d", n, int(timeout/time.Second)))
		}
	}
}
