package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const apiBase = "https://gmail.googleapis.com/gmail/v1/users/me"

// MaxArtifactBytes bounds a single message read into memory.
//
// SECURITY.md §7 requires a cap at ingestion: Evidence is hostile input, and
// anyone who knows the address can put bytes into this path. 25 MB is above
// Gmail's own per-message ceiling, so it rejects only the pathological case.
const MaxArtifactBytes = 25 << 20

// Client talks to the Gmail REST API. It fetches; it never parses, and it never
// decides what a message means (DOMAIN.md §9).
type Client struct {
	http  *http.Client
	token string
}

// NewClient loads credentials, refreshes the access token when needed, and
// persists the refreshed token so the next run starts warm.
func NewClient(ctx context.Context, dir string) (*Client, error) {
	creds, err := LoadCredentials(dir)
	if err != nil {
		return nil, err
	}
	if creds.Gmail == nil || creds.Gmail.RefreshToken == "" {
		return nil, fmt.Errorf("no Gmail credentials in %s: run `billycore auth` first", dir)
	}
	token, changed, err := AccessTokenFor(ctx, creds.Gmail)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := creds.Save(dir); err != nil {
			return nil, err
		}
	}
	return &Client{http: &http.Client{Timeout: 60 * time.Second}, token: token}, nil
}

// Message is the metadata BillyCore reads about a Gmail message.
//
// This is not Evidence. It is what a Source reports about an artifact before
// anything decides to record it.
type Message struct {
	ID           string
	ThreadID     string
	InternalDate time.Time
	From         string
	Subject      string
	SizeEstimate int
}

// ListIDs returns message ids matching a Gmail search query, newest first,
// following pagination up to limit.
func (c *Client) ListIDs(ctx context.Context, query string, limit int) ([]string, error) {
	var ids []string
	pageToken := ""
	for len(ids) < limit {
		q := url.Values{"q": {query}, "maxResults": {strconv.Itoa(min(500, limit-len(ids)))}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.get(ctx, apiBase+"/messages?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		for _, m := range page.Messages {
			ids = append(ids, m.ID)
		}
		if page.NextPageToken == "" || len(page.Messages) == 0 {
			break
		}
		pageToken = page.NextPageToken
	}
	return ids, nil
}

// GetMetadata fetches headers only — no body is transferred.
func (c *Client) GetMetadata(ctx context.Context, id string) (*Message, error) {
	q := url.Values{
		"format":          {"metadata"},
		"metadataHeaders": {"From"},
	}
	u := apiBase + "/messages/" + url.PathEscape(id) + "?" + q.Encode() + "&metadataHeaders=Subject&metadataHeaders=Date"

	var raw struct {
		ID           string `json:"id"`
		ThreadID     string `json:"threadId"`
		InternalDate string `json:"internalDate"`
		SizeEstimate int    `json:"sizeEstimate"`
		Payload      struct {
			Headers []struct{ Name, Value string } `json:"headers"`
		} `json:"payload"`
	}
	if err := c.get(ctx, u, &raw); err != nil {
		return nil, err
	}

	m := &Message{ID: raw.ID, ThreadID: raw.ThreadID, SizeEstimate: raw.SizeEstimate}
	// internalDate is the server's own receive time in epoch milliseconds. The
	// Date: header is sender-controlled and therefore spoofable, and Evidence is
	// attacker-influenceable input (SECURITY.md §7, §8) — so the server's clock
	// is the one Billy trusts for when it observed something.
	if ms, err := strconv.ParseInt(raw.InternalDate, 10, 64); err == nil {
		m.InternalDate = time.UnixMilli(ms).UTC()
	}
	for _, h := range raw.Payload.Headers {
		switch h.Name {
		case "From":
			m.From = h.Value
		case "Subject":
			m.Subject = h.Value
		}
	}
	return m, nil
}

func (c *Client) get(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxArtifactBytes+1))
	if err != nil {
		return err
	}
	if len(body) > MaxArtifactBytes {
		return fmt.Errorf("gmail response exceeds %d bytes: refusing to read further", MaxArtifactBytes)
	}
	if resp.StatusCode != http.StatusOK {
		// Report the status, never the body: an error body can carry message
		// content, and content never reaches a log (SECURITY.md §10).
		return fmt.Errorf("gmail API returned %s", resp.Status)
	}
	return json.Unmarshal(body, into)
}
