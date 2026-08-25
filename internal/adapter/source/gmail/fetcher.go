package gmail

import (
	"context"

	"github.com/alejandroayalad/billycore/internal/app"
)

// ContentType is what Evidence from Gmail holds: the whole RFC 822 message
// (D24). Not `text/html` — that describes one MIME part, and Billy stores the
// artifact, not a part of it.
const ContentType = "message/rfc822"

// Fetcher adapts a Gmail account to app.SourceFetcher.
//
// It carries its own query, so the use case above it never learns that a Source
// has one (D6, D26).
type Fetcher struct {
	client *Client
	query  string
}

func NewFetcher(client *Client, query string) *Fetcher {
	return &Fetcher{client: client, query: query}
}

// ListReferences returns every message id matching the Source's query.
//
// Unbounded on purpose: a sync that silently stopped at the first N would report
// `artifacts_discovered` as a fact about a limit rather than about the mailbox.
func (f *Fetcher) ListReferences(ctx context.Context) ([]string, error) {
	return f.client.ListIDs(ctx, f.query, 0)
}

// Fetch returns one message as the artifact it arrived as.
func (f *Fetcher) Fetch(ctx context.Context, reference string) (app.Artifact, error) {
	message, content, err := f.client.GetRaw(ctx, reference)
	if err != nil {
		return app.Artifact{}, err
	}
	return artifactFrom(message, content), nil
}

// artifactFrom is separate so the rule below is testable without a network.
func artifactFrom(message *Message, content []byte) app.Artifact {
	return app.Artifact{
		Reference:   message.ID,
		ContentType: ContentType,
		// internalDate, never the Date: header. The header is sender-controlled,
		// and Evidence is hostile input (SECURITY.md §7, §8).
		ObservedAt: message.InternalDate,
		Content:    content,
	}
}
