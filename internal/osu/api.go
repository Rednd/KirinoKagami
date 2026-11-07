// Package osu talks to osu.ppy.sh: the API v2 (metadata) and the website
// (downloading .osz files with a logged-in account).
package osu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"kirinokagami/internal/config"
)

const BaseURL = "https://osu.ppy.sh"

var (
	ErrNotFound = errors.New("osu: not found")
)

// Beatmapset is the subset of the API v2 beatmapset object we use.
type Beatmapset struct {
	ID             int        `json:"id"`
	Artist         string     `json:"artist"`
	ArtistUnicode  string     `json:"artist_unicode"`
	Title          string     `json:"title"`
	TitleUnicode   string     `json:"title_unicode"`
	Creator        string     `json:"creator"`
	UserID         int        `json:"user_id"`
	Source         string     `json:"source"`
	Tags           string     `json:"tags"`
	Video          bool       `json:"video"`
	Storyboard     bool       `json:"storyboard"`
	Ranked         int        `json:"ranked"`
	RankedDate     *time.Time `json:"ranked_date"`
	SubmittedDate  *time.Time `json:"submitted_date"`
	LastUpdated    *time.Time `json:"last_updated"`
	FavouriteCount int        `json:"favourite_count"`
	GenreID        *int       `json:"genre_id"`
	LanguageID     *int       `json:"language_id"`
	Genre          *idName    `json:"genre"`
	Language       *idName    `json:"language"`
	Beatmaps       []Beatmap  `json:"beatmaps"`
}

type idName struct {
	ID *int `json:"id"`
}

// GenreLanguage returns genre and language IDs if the response contained them.
func (s *Beatmapset) GenreLanguage() (genre, language int, ok bool) {
	g, l := s.GenreID, s.LanguageID
	if g == nil && s.Genre != nil {
		g = s.Genre.ID
	}
	if l == nil && s.Language != nil {
		l = s.Language.ID
	}
	if g == nil || l == nil {
		return 0, 0, false
	}
	return *g, *l, true
}

// Beatmap is the subset of the API v2 beatmap object we use.
type Beatmap struct {
	ID               int         `json:"id"`
	BeatmapsetID     int         `json:"beatmapset_id"`
	Version          string      `json:"version"`
	Checksum         *string     `json:"checksum"`
	ModeInt          int         `json:"mode_int"`
	BPM              float64     `json:"bpm"`
	AR               float32     `json:"ar"`
	Accuracy         float32     `json:"accuracy"`
	CS               float32     `json:"cs"`
	Drain            float32     `json:"drain"`
	TotalLength      int         `json:"total_length"`
	HitLength        int         `json:"hit_length"`
	Playcount        int         `json:"playcount"`
	Passcount        int         `json:"passcount"`
	MaxCombo         *int        `json:"max_combo"`
	DifficultyRating float64     `json:"difficulty_rating"`
	Beatmapset       *Beatmapset `json:"beatmapset"`
}

type SearchResponse struct {
	Beatmapsets  []Beatmapset `json:"beatmapsets"`
	CursorString *string      `json:"cursor_string"`
}

// Client is an osu! API v2 client using the client-credentials grant.
type Client struct {
	cfg     config.API
	ua      string
	http    *http.Client
	limiter *rate.Limiter
	log     *slog.Logger

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func NewClient(cfg config.API, userAgent string, log *slog.Logger) *Client {
	return &Client{
		cfg:     cfg,
		ua:      userAgent,
		http:    &http.Client{Timeout: 30 * time.Second},
		limiter: rate.NewLimiter(rate.Limit(float64(cfg.RequestsPerMinute)/60), max(cfg.Burst, 1)),
		log:     log,
	}
}

func (c *Client) Configured() bool { return c.cfg.ClientID != 0 && c.cfg.ClientSecret != "" }

func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && time.Now().Before(c.expiry) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]any{
		"client_id":     c.cfg.ClientID,
		"client_secret": c.cfg.ClientSecret,
		"grant_type":    "client_credentials",
		"scope":         "public",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("osu oauth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("osu oauth: status %d: %s (check api.client_id / api.client_secret)", resp.StatusCode, b)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("osu oauth: decoding token: %w", err)
	}
	c.token = tok.AccessToken
	// Refresh a little before the real expiry.
	c.expiry = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 5*time.Minute)
	c.log.Info("obtained osu! API access token", "expires_in", time.Duration(tok.ExpiresIn)*time.Second)
	return c.token, nil
}

// get performs a rate-limited GET against /api/v2 and decodes the JSON
// response into out. It retries on 401 (token refresh), 429 and 5xx.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := BaseURL + "/api/v2" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	forceToken := false
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}
		tok, err := c.accessToken(ctx, forceToken)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.ua)

		resp, err := c.http.Do(req)
		if err != nil {
			if attempt >= 4 || ctx.Err() != nil {
				return fmt.Errorf("GET %s: %w", path, err)
			}
			c.log.Warn("osu API request failed, retrying", "path", path, "err", err)
			if err := sleep(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			err := json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("GET %s: decoding: %w", path, err)
			}
			return nil
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized && !forceToken:
			resp.Body.Close()
			forceToken = true
			continue
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < 6:
			wait := backoff
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
				wait = time.Duration(ra) * time.Second
			}
			resp.Body.Close()
			c.log.Warn("osu API throttled / unavailable, backing off", "path", path, "status", resp.StatusCode, "wait", wait)
			if err := sleep(ctx, wait); err != nil {
				return err
			}
			backoff = min(backoff*2, 2*time.Minute)
			continue
		default:
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, b)
		}
	}
}

// SearchBeatmapsets calls /beatmapsets/search. Typical params: s (status
// filter: any, ranked, loved, qualified, pending, graveyard), sort
// (e.g. updated_desc, ranked_desc), cursor_string.
func (c *Client) SearchBeatmapsets(ctx context.Context, params url.Values) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.get(ctx, "/beatmapsets/search", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBeatmapset fetches a full beatmapset with all difficulties.
func (c *Client) GetBeatmapset(ctx context.Context, id int) (*Beatmapset, error) {
	var out Beatmapset
	if err := c.get(ctx, "/beatmapsets/"+strconv.Itoa(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LookupBeatmapsetByBeatmap fetches the set that contains a beatmap ID.
func (c *Client) LookupBeatmapsetByBeatmap(ctx context.Context, beatmapID int) (*Beatmapset, error) {
	var out Beatmapset
	q := url.Values{"beatmap_id": {strconv.Itoa(beatmapID)}}
	if err := c.get(ctx, "/beatmapsets/lookup", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LookupBeatmapByChecksum finds a beatmap by its .osu MD5.
func (c *Client) LookupBeatmapByChecksum(ctx context.Context, md5 string) (*Beatmap, error) {
	var out Beatmap
	q := url.Values{"checksum": {md5}}
	if err := c.get(ctx, "/beatmaps/lookup", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBeatmaps fetches up to 50 beatmaps by ID (each includes its beatmapset).
func (c *Client) GetBeatmaps(ctx context.Context, ids []int) ([]Beatmap, error) {
	q := url.Values{}
	for _, id := range ids {
		q.Add("ids[]", strconv.Itoa(id))
	}
	var out struct {
		Beatmaps []Beatmap `json:"beatmaps"`
	}
	if err := c.get(ctx, "/beatmaps", q, &out); err != nil {
		return nil, err
	}
	return out.Beatmaps, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
