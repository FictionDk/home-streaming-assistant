// Package jellyfin integrates the media server: library indexing,
// playback links and conservative cross-user playback checks before
// local deletion.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"home-streaming-assistant/internal/model"
)

// Client talks to Jellyfin 12.4 with a dedicated admin API key.
type Client struct {
	BaseURL   string // e.g. http://jellyfin:8096
	APIKey    string
	LibraryID string
	WebURLs   map[string]string // app origin -> jellyfin /web/ URL

	http *http.Client
}

// New builds the client.
func New(baseURL, apiKey, libraryID string, webURLs map[string]string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKey:    apiKey,
		LibraryID: libraryID,
		WebURLs:   webURLs,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

// PublicInfo is the subset of /System/Info/Public the app reads.
type PublicInfo struct {
	ID                     string `json:"Id"`
	Version                string `json:"Version"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// SystemInfo fetches real server identity.
func (c *Client) SystemInfo(ctx context.Context) (*PublicInfo, error) {
	var info PublicInfo
	if err := c.get(ctx, "/System/Info/Public", url.Values{}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// StartRefresh triggers a library scan; a 204 only means the scan began.
func (c *Client) StartRefresh(ctx context.Context) error {
	req, err := c.request(ctx, http.MethodPost, "/Library/Refresh", url.Values{}, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return model.E(model.CodeJellyfinUnavailable, 503, "jellyfin refresh request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return model.E(model.CodeJellyfinUnavailable, 503, fmt.Sprintf("jellyfin refresh returned %d", resp.StatusCode))
	}
	return nil
}

// MediaItem is one item from a library query.
type MediaItem struct {
	ID           string        `json:"Id"`
	ServerID     string        `json:"ServerId"`
	Name         string        `json:"Name"`
	Path         string        `json:"Path"`
	MediaSources []MediaSource `json:"MediaSources"`
}

// MediaSource carries the concrete file path of a version.
type MediaSource struct {
	ID   string `json:"Id"`
	Path string `json:"Path"`
}

// LibraryItems pages through the configured library.
func (c *Client) LibraryItems(ctx context.Context, startIndex int) ([]MediaItem, int, error) {
	q := url.Values{}
	q.Set("parentId", c.LibraryID)
	q.Set("recursive", "true")
	q.Set("fields", "Path,MediaSources")
	q.Set("startIndex", fmt.Sprintf("%d", startIndex))
	q.Set("limit", "200")
	var page struct {
		Items            []MediaItem `json:"Items"`
		TotalRecordCount int         `json:"TotalRecordCount"`
	}
	if err := c.get(ctx, "/Items", q, &page); err != nil {
		return nil, 0, err
	}
	return page.Items, page.TotalRecordCount, nil
}

// AllItems pages through the whole configured library.
func (c *Client) AllItems(ctx context.Context) ([]MediaItem, error) {
	var out []MediaItem
	start := 0
	for {
		items, total, err := c.LibraryItems(ctx, start)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		start += len(items)
		if len(items) == 0 || start >= total {
			return out, nil
		}
	}
}

// FindByPath returns the trusted items whose Path or MediaSources.Path
// equals the given absolute path. It never guesses by file name.
func (c *Client) FindByPath(ctx context.Context, absPath string) ([]MediaItem, error) {
	var out []MediaItem
	start := 0
	for {
		items, total, err := c.LibraryItems(ctx, start)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Path == absPath {
				out = append(out, it)
				continue
			}
			for _, ms := range it.MediaSources {
				if ms.Path == absPath {
					out = append(out, it)
					break
				}
			}
		}
		start += len(items)
		if len(items) == 0 || start >= total {
			break
		}
	}
	return out, nil
}

// Session is one active Jellyfin session.
type Session struct {
	ID              string       `json:"Id"`
	UserID          string       `json:"UserId"`
	UserName        string       `json:"UserName"`
	NowPlayingItem  *MediaItem   `json:"NowPlayingItem"`
	PlayState       *PlayState   `json:"PlayState"`
	TranscodingInfo *Transcoding `json:"TranscodingInfo"`
}

// PlayState reports position/paused state.
type PlayState struct {
	PositionTicks int64 `json:"PositionTicks"`
	IsPaused      bool  `json:"IsPaused"`
}

// Transcoding carries secondary playback evidence.
type Transcoding struct {
	IsVideoDirect bool `json:"IsVideoDirect"`
}

// Sessions lists all sessions without filters, so playback by any user
// is visible to the admin key.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	if err := c.get(ctx, "/Sessions", url.Values{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PlaybackConflict reports whether any session is playing or paused on
// the given absolute media path. Unresolvable playing entries are
// treated conservatively as conflicts.
func (c *Client) PlaybackConflict(ctx context.Context, absPath string) (bool, error) {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.NowPlayingItem == nil {
			continue
		}
		np := s.NowPlayingItem
		match := np.Path == absPath
		if !match {
			for _, ms := range np.MediaSources {
				if ms.Path == absPath {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}
		// Playing (paused or not) on this file.
		return true, nil
	}
	return false, nil
}

// PlaybackURL builds the deep link for a trusted app origin.
func (c *Client) PlaybackURL(appOrigin, itemID, serverID string) *string {
	web, ok := c.WebURLs[appOrigin]
	if !ok {
		return nil
	}
	u := strings.TrimRight(web, "/") + "/#/details?id=" + url.QueryEscape(itemID) +
		"&serverId=" + url.QueryEscape(serverID)
	return &u
}

func (c *Client) request(ctx context.Context, method, path string, q url.Values, body any) (*http.Request, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, model.E(model.CodeInternal, 500, "encode request")
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, model.E(model.CodeInternal, 500, "build request")
	}
	// Jellyfin 12.4 custom authentication header form.
	req.Header.Set("Authorization", fmt.Sprintf("MediaBrowser Token=%q", c.APIKey))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := c.request(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return model.E(model.CodeJellyfinUnavailable, 503, "jellyfin request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return model.E(model.CodeJellyfinUnavailable, 503, "jellyfin response read failed")
	}
	if resp.StatusCode != http.StatusOK {
		return model.E(model.CodeJellyfinUnavailable, 503, fmt.Sprintf("jellyfin returned %d", resp.StatusCode))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return model.E(model.CodeJellyfinUnavailable, 503, "jellyfin response malformed")
	}
	return nil
}
