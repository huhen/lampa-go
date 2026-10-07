// Package builder integrates lampa-go with the lampa-web-builder service:
// the API client, the atomic frontend deployer and the update worker.
// The API contract is the builder spec §3
// (huhen/lampa-web-builder, docs/superpowers/specs/).
package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Sentinel errors mapped from builder HTTP statuses: all mean "retry later".
var (
	ErrBusy      = errors.New("builder busy (test build in progress)") // 409
	ErrQueueFull = errors.New("builder queue is full")                 // 429
	ErrNoVersion = errors.New("builder has no available version yet")  // 503
)

// Frontend build statuses as reported by the builder.
const (
	BuildQueued  = "queued"
	BuildRunning = "running"
	BuildSuccess = "success"
	BuildFailed  = "failed"
)

// Status mirrors GET /api/v1/status.
type Status struct {
	Version          string `json:"version"`
	AvailableCommit  string `json:"available_commit"`
	LatestSeenCommit string `json:"latest_seen_commit"`
	BadCommit        string `json:"bad_commit"`
	Poll             struct {
		Interval    string `json:"interval"`
		LastChecked string `json:"last_checked"`
		LastError   string `json:"last_error"`
	} `json:"poll"`
}

// StartBuild is the POST /api/v1/builds request body.
type StartBuild struct {
	Domain string `json:"domain"`
}

// BuildRef is the POST /api/v1/builds response.
type BuildRef struct {
	BuildID string `json:"build_id"`
	Cached  bool   `json:"cached"`
}

// Build mirrors GET /api/v1/builds/{id}.
type Build struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Domain string `json:"domain"`
	Commit string `json:"commit"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Client talks to the builder API. Safe for concurrent use.
type Client struct {
	base     *url.URL
	key      string
	hcAPI    *http.Client // JSON endpoints: bounded overall
	hcStream *http.Client // archive download: bounded by context only
}

// NewClient builds a client for the builder at rawURL using apiKey.
func NewClient(rawURL, apiKey string) (*Client, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse builder url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("builder url must be http(s), got %q", rawURL)
	}
	// The archive can take a while; the caller's context bounds it, but a
	// stalled server must not hang the request before any body byte arrives.
	streamTransport := http.DefaultTransport.(*http.Transport).Clone()
	streamTransport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		base: base,
		key:  apiKey,
		hcAPI: &http.Client{
			Timeout: 30 * time.Second,
		},
		hcStream: &http.Client{
			Transport: streamTransport,
		},
	}, nil
}

func (c *Client) newRequest(ctx context.Context, hc *http.Client, method, path string, body io.Reader) (*http.Request, error) {
	u := c.base.JoinPath(path)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.key)
	return req, nil
}

// statusError converts a non-2xx response into a sentinel or a descriptive error.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch resp.StatusCode {
	case http.StatusConflict:
		return ErrBusy
	case http.StatusTooManyRequests:
		return ErrQueueFull
	case http.StatusServiceUnavailable:
		return ErrNoVersion
	}
	return fmt.Errorf("builder returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
}

func decodeJSON(resp *http.Response, v any) error {
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode builder response: %w", err)
	}
	return nil
}

// Status returns the builder status snapshot.
func (c *Client) Status(ctx context.Context) (Status, error) {
	req, err := c.newRequest(ctx, c.hcAPI, http.MethodGet, "/api/v1/status", nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := c.hcAPI.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("builder status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, statusError(resp)
	}
	var st Status
	if err := decodeJSON(resp, &st); err != nil {
		return Status{}, err
	}
	return st, nil
}

// StartBuild orders a build for the domain. The builder always builds its
// latest available commit; the commit is not a parameter.
func (c *Client) StartBuild(ctx context.Context, domain string) (BuildRef, error) {
	body, err := json.Marshal(StartBuild{Domain: domain})
	if err != nil {
		return BuildRef{}, err
	}
	req, err := c.newRequest(ctx, c.hcAPI, http.MethodPost, "/api/v1/builds", bytes.NewReader(body))
	if err != nil {
		return BuildRef{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hcAPI.Do(req)
	if err != nil {
		return BuildRef{}, fmt.Errorf("builder start build: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return BuildRef{}, statusError(resp)
	}
	var ref BuildRef
	if err := decodeJSON(resp, &ref); err != nil {
		return BuildRef{}, err
	}
	if ref.BuildID == "" {
		return BuildRef{}, errors.New("builder returned an empty build_id")
	}
	return ref, nil
}

// Build returns the status of a single build.
func (c *Client) Build(ctx context.Context, id string) (Build, error) {
	req, err := c.newRequest(ctx, c.hcAPI, http.MethodGet, "/api/v1/builds/"+url.PathEscape(id), nil)
	if err != nil {
		return Build{}, err
	}
	resp, err := c.hcAPI.Do(req)
	if err != nil {
		return Build{}, fmt.Errorf("builder build %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Build{}, statusError(resp)
	}
	var b Build
	if err := decodeJSON(resp, &b); err != nil {
		return Build{}, err
	}
	return b, nil
}

// DownloadArchive streams the build archive into dest (tmp file + rename).
// Caller bounds the transfer via ctx.
func (c *Client) DownloadArchive(ctx context.Context, id, dest string) error {
	req, err := c.newRequest(ctx, c.hcStream, http.MethodGet, "/api/v1/builds/"+url.PathEscape(id)+"/archive", nil)
	if err != nil {
		return err
	}
	resp, err := c.hcStream.Do(req)
	if err != nil {
		return fmt.Errorf("builder archive %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".archive-*")
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	tmp := f.Name()
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download archive: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write archive: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("place archive: %w", err)
	}
	return nil
}
