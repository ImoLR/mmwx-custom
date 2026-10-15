package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type deletedSubscriptionNode struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
	Port int64  `json:"port,omitempty"`
}

var subscriptionNameField = regexp.MustCompile(`(?m)(?:["']?(?:name|tag|ps)["']?\s*[:=]\s*["']?)([^\r\n,}]+)`)
var subscriptionBase64 = regexp.MustCompile(`[A-Za-z0-9_+/=-]{12,}`)

func subscriptionContainsDeletedNode(content []byte, nodes []deletedSubscriptionNode, depth int) bool {
	if depth > 4 {
		return false
	}
	text := string(content)
	if decoded, err := url.QueryUnescape(text); err == nil {
		text = decoded
	}
	for _, n := range nodes {
		if n.Host != "" && strings.Contains(strings.ToLower(text), n.Host) && n.Port > 0 && regexp.MustCompile(`\b`+strconv.FormatInt(n.Port, 10)+`\b`).MatchString(text) {
			return true
		}
		if n.Name != "" {
			quoted, _ := json.Marshal(n.Name)
			if strings.Contains(text, string(quoted)) {
				return true
			}
			for _, field := range subscriptionNameField.FindAllStringSubmatch(text, -1) {
				if strings.Contains(field[1], n.Name) {
					return true
				}
			}
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if i := strings.Index(line, "name:"); i >= 0 && strings.Trim(strings.TrimSpace(line[i+5:]), "\"'") == n.Name {
					return true
				}
				if i := strings.LastIndex(line, "#"); i >= 0 && strings.Contains(line[i+1:], n.Name) {
					return true
				}
			}
		}
	}
	for _, encoded := range subscriptionBase64.FindAllString(text, -1) {
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			decoded, err := encoding.DecodeString(strings.TrimLeft(encoded, "/"))
			if err == nil && len(decoded) > 0 && subscriptionContainsDeletedNode(decoded, nodes, depth+1) {
				return true
			}
		}
	}
	return false
}

func (s *postgresAdminSessionStore) deletedSubscriptionNodes(ctx context.Context) ([]deletedSubscriptionNode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(plan->'subscription_nodes','[]'::jsonb) FROM mmwxc_server_deletions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []deletedSubscriptionNode{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var nodes []deletedSubscriptionNode
		if err = json.Unmarshal(raw, &nodes); err != nil {
			return nil, err
		}
		result = append(result, nodes...)
	}
	return result, rows.Err()
}

// Old temporary/static upstream subscriptions can outlive the node record.
// Refuse stale output, including encoded clients, rather than returning a working
// credential from a cache. Fresh subscriptions without retired nodes still pass.
func (a *app) subscriptionProxy(target *url.URL) http.Handler {
	proxy := mmwxAPIProxy(target).(*httputil.ReverseProxy)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("If-None-Match")
		r.Header.Del("If-Modified-Since")
		r.Header.Set("Accept-Encoding", "identity")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode != http.StatusOK {
			return nil
		}
		store, ok := a.adminStore.(*postgresAdminSessionStore)
		if !ok {
			return nil
		}
		nodes, err := store.deletedSubscriptionNodes(response.Request.Context())
		if err == nil && len(nodes) == 0 {
			return nil
		}
		response.Header.Set("Cache-Control", "no-store")
		response.Header.Del("ETag")
		original := response.Body
		defer original.Close()
		var reader io.Reader = original
		if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" && encoding != "gzip" {
			err = io.ErrUnexpectedEOF
		}
		if response.Header.Get("Content-Encoding") == "gzip" {
			gz, e := gzip.NewReader(original)
			if e != nil {
				err = e
			} else {
				defer gz.Close()
				reader = gz
			}
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, (16<<20)+1))
		status := http.StatusOK
		if err != nil || readErr != nil || len(body) > 16<<20 {
			status = http.StatusServiceUnavailable
			body = []byte("订阅清理状态暂时无法验证，请稍后重试\n")
		} else if subscriptionContainsDeletedNode(body, nodes, 0) {
			status = http.StatusGone
			body = []byte("此订阅缓存包含已删除节点，请重新生成订阅\n")
		}
		response.StatusCode = status
		response.Header.Del("Content-Encoding")
		response.Header.Set("Content-Length", strconv.Itoa(len(body)))
		if status != http.StatusOK {
			response.Header.Set("Content-Type", "text/plain; charset=utf-8")
		}
		response.ContentLength = int64(len(body))
		response.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	return proxy
}
