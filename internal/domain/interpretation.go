package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInterpretationNoID       = errors.New("interpretation: id is required")
	ErrInterpretationNoEvidence = errors.New("interpretation: an interpretation is always of one piece of Evidence")
	ErrInterpretationNoClaims   = errors.New("interpretation: an interpretation with no claims is not an interpretation")

	// ErrInterpretationNegativeUnread guards D59's count. A reading that could
	// not read a negative number of rows is a defect in a parser.
	ErrInterpretationNegativeUnread = errors.New("interpretation: the count of unread rows cannot be negative")
)

// Interpretation is one complete reading of one piece of Evidence. It holds the
// Claims that one parser pass found (D46). An email gives one Claim, and a bank
// statement gives one Claim for each movement. Billy activates the complete set
// or none of it, and one artifact has one active interpretation.
type Interpretation struct {
	id         string
	evidenceID string
	claims     []Claim
	unreadRows int
	supersedes string
	createdAt  time.Time
}

// NewInterpretation validates a complete reading of one artifact.
//
// `unreadRows` is how many movements of the artifact no shape of the parser
// read (D59). A reading of an email is always 0: one artifact is one movement.
// A statement of about 180 rows is read as far as it can be, and this says how
// far.
//
// `supersedes` is the id of the interpretation that this one replaces. It is
// empty for a first reading. The caller supplies it, because a read of the
// database finds it, and the domain does no I/O (D6).
func NewInterpretation(id, evidenceID string, claims []Claim, unreadRows int, supersedes string, createdAt time.Time) (Interpretation, error) {
	if id == "" {
		return Interpretation{}, ErrInterpretationNoID
	}
	if evidenceID == "" {
		return Interpretation{}, ErrInterpretationNoEvidence
	}
	if supersedes == id {
		return Interpretation{}, errors.New("interpretation: an interpretation cannot supersede itself")
	}
	if createdAt.IsZero() {
		return Interpretation{}, errors.New("interpretation: created_at is required")
	}
	if unreadRows < 0 {
		return Interpretation{}, ErrInterpretationNegativeUnread
	}
	if len(claims) == 0 {
		// An artifact that no parser recognises gives no interpretation. The
		// pipeline advances that artifact with MarkExtracted instead.
		return Interpretation{}, ErrInterpretationNoClaims
	}

	seen := map[string]bool{}
	out := make([]Claim, 0, len(claims))
	for _, c := range claims {
		if c.ID() == "" {
			return Interpretation{}, errors.New("interpretation: a claim without an id cannot be part of a set")
		}
		if seen[c.ID()] {
			return Interpretation{}, fmt.Errorf("interpretation: claim %s appears twice in one interpretation", c.ID())
		}
		seen[c.ID()] = true

		// Each member is ACTIVE, because an interpretation is what Billy uses.
		// A PROPOSED Claim is an offer from outside and takes no slot (D11, D38).
		if c.State() != ClaimActive {
			return Interpretation{}, fmt.Errorf("interpretation: claim %s is %s, and an interpretation is a set of ACTIVE claims", c.ID(), c.State())
		}

		// Check the provenance. A foreign key shows that the Claim exists. It
		// does not show that the Claim is about this artifact.
		if !supportsEvidence(c, evidenceID) {
			return Interpretation{}, fmt.Errorf("interpretation: claim %s does not rest on evidence %s", c.ID(), evidenceID)
		}
		out = append(out, c)
	}

	return Interpretation{
		id:         id,
		evidenceID: evidenceID,
		claims:     out,
		unreadRows: unreadRows,
		supersedes: supersedes,
		createdAt:  createdAt.UTC(),
	}, nil
}

func supportsEvidence(c Claim, evidenceID string) bool {
	for _, id := range c.EvidenceIDs() {
		if id == evidenceID {
			return true
		}
	}
	return false
}

func (i Interpretation) ID() string           { return i.id }
func (i Interpretation) EvidenceID() string   { return i.evidenceID }
func (i Interpretation) CreatedAt() time.Time { return i.createdAt }

// UnreadRows is how many movements of the artifact this reading did not read.
//
// A count above zero is a reading that asks to be looked at, and re-extraction
// is how it is corrected (D48, D59). Zero for every email: the four Nu
// templates are each a receipt for one movement.
func (i Interpretation) UnreadRows() int { return i.unreadRows }

// Supersedes is the id of the interpretation that this one replaces. It is
// empty for a first reading of the artifact. It is the lineage that D47 records,
// so you do not have to sort timestamps to find the previous reading.
func (i Interpretation) Supersedes() string { return i.supersedes }

// Claims returns a copy. If it returned the slice, a caller could change a
// recorded interpretation through the reference.
func (i Interpretation) Claims() []Claim {
	out := make([]Claim, len(i.claims))
	copy(out, i.claims)
	return out
}

// ClaimIDs returns the ids of the members, in the order that the caller gave.
func (i Interpretation) ClaimIDs() []string {
	out := make([]string, len(i.claims))
	for n, c := range i.claims {
		out[n] = c.ID()
	}
	return out
}
