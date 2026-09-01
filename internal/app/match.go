package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
	"github.com/alejandroayalad/billycore/internal/id"
)

// mxMinus6 is the fixed offset the day comparison uses (D72). Mexico City is
// UTC−6 all year since it dropped DST in 2022, so the emails (UTC) and the
// statement (local) are compared on the day they actually share.
var mxMinus6 = time.FixedZone("UTC-6", -6*60*60)

// basisComposite names the exact composite key in the merge event (D72). The
// tracking key names itself by its empty default in the store (D70).
const basisComposite = "composite"

// MatchResult summarises one matching pass.
type MatchResult struct {
	// CandidatesRecorded counts the new candidates this pass wrote. A re-run
	// finds them already there and records none, which is how a drain ends.
	CandidatesRecorded int
	Merged             int
	NoMatch            int
	Ambiguous          int
}

// Matcher is cross-Source reconciliation (DOMAIN.md §6). A transfer seen in a Nu
// email and in the bank statement is two Transactions until this pass decides
// they are one event and merges them (D70). It reads Transactions and writes
// candidates; it never touches the Evidence queue.
type Matcher struct {
	Repo  ReconciliationRepository
	NewID func() (string, error)
	Now   func() time.Time
}

func NewMatcher(repo ReconciliationRepository) *Matcher {
	return &Matcher{
		Repo:  repo,
		NewID: id.New,
		Now:   func() time.Time { return time.Now().UTC() },
	}
}

// Run reconciles in three sweeps and returns after all (D36, D71, D72). The
// tracking-key sweep is exact and merges; the composite sweep merges a
// cross-Source pair that a unique exact composite key identifies; the weak sweep
// records AMBIGUOUS or NO_MATCH for what is left, never a merge. Each sweep
// re-reads, so a merged row drops out. A re-run merges nothing new (D70).
func (m *Matcher) Run(ctx context.Context) (MatchResult, error) {
	now := m.Now().UTC()
	var result MatchResult

	keyed, err := m.Repo.UnreconciledWithTrackingKey(ctx)
	if err != nil {
		return result, fmt.Errorf("match: read tracking-keyed: %w", err)
	}
	if err := m.sweep(ctx, trackingKeyPairs(keyed), trackingKeyOutcome, "", now, &result); err != nil {
		return result, err
	}

	// The composite sweep reads the survivors of the sweep above (D72).
	sourced, err := m.Repo.UnreconciledTransactions(ctx)
	if err != nil {
		return result, fmt.Errorf("match: read unreconciled: %w", err)
	}
	if err := m.sweep(ctx, compositePairs(sourced), compositeOutcome, basisComposite, now, &result); err != nil {
		return result, err
	}

	// The weak sweep reads again, after the composite merges, and stars the
	// look-alikes that are left into AMBIGUOUS or NO_MATCH (D71).
	sourced, err = m.Repo.UnreconciledTransactions(ctx)
	if err != nil {
		return result, fmt.Errorf("match: read unreconciled: %w", err)
	}
	if err := m.sweep(ctx, weakPairs(sourced), weakOutcome, "", now, &result); err != nil {
		return result, err
	}
	return result, nil
}

// sweep records the candidate for each pair and tallies the outcome. A pair that
// already has a candidate is a no-op, which is how a drain ends (D70). basis
// names the signal behind a MATCH, for the audit event.
func (m *Matcher) sweep(ctx context.Context, pairs []transactionPair,
	classify func(a, b domain.Transaction) domain.ReconciliationOutcome, basis string, now time.Time, result *MatchResult) error {
	for _, pair := range pairs {
		if err := ctx.Err(); err != nil {
			return err
		}
		decision, err := m.decide(pair.left, pair.right, classify(pair.left, pair.right), basis, now)
		if err != nil {
			return err
		}
		created, err := m.Repo.Reconcile(ctx, decision, now)
		if err != nil {
			return fmt.Errorf("match: record candidate: %w", err)
		}
		if !created {
			continue // the pair already has a candidate; the constraint said so
		}
		result.CandidatesRecorded++
		switch decision.Outcome {
		case domain.Match:
			result.Merged++
		case domain.NoMatch:
			result.NoMatch++
		case domain.Ambiguous:
			result.Ambiguous++
		}
	}
	return nil
}

// decide builds the candidate for one canonical pair, and the merged survivor
// and loser when it is a MATCH (D70). The survivor is the smaller id, which is
// the pair's left side, so a re-run reaches the same survivor.
func (m *Matcher) decide(left, right domain.Transaction, outcome domain.ReconciliationOutcome,
	basis string, now time.Time) (ReconcileDecision, error) {
	candidateID, err := m.NewID()
	if err != nil {
		return ReconcileDecision{}, fmt.Errorf("match: generate candidate id: %w", err)
	}
	d := ReconcileDecision{
		CandidateID: candidateID,
		LeftID:      left.ID(),
		RightID:     right.ID(),
		Outcome:     outcome,
		Basis:       basis,
	}
	if outcome != domain.Match {
		return d, nil
	}
	survivor, err := left.ReconciledWith(right.EvidenceIDs(), now)
	if err != nil {
		return ReconcileDecision{}, err
	}
	superseded, err := right.SupersededByTransaction(survivor.ID(), now)
	if err != nil {
		return ReconcileDecision{}, err
	}
	d.Survivor, d.Superseded = survivor, superseded
	return d, nil
}

// trackingKeyOutcome decides a pair that already shares a tracking key (D36).
// The key is exact, so only two contradictions can still forbid the merge:
// opposite direction (DOMAIN.md §6) and internal against external (D55).
func trackingKeyOutcome(a, b domain.Transaction) domain.ReconciliationOutcome {
	if a.Direction() != b.Direction() {
		return domain.NoMatch
	}
	if isInternal(a) != isInternal(b) {
		return domain.NoMatch
	}
	return domain.Match
}

// compositeOutcome decides a pair the composite key already paired: same amount,
// currency, direction and merchant, same day, two Sources, unique on each (D72).
// The one contradiction that still forbids the merge is internal against
// external (D55); every other composite pair is a MATCH.
func compositeOutcome(a, b domain.Transaction) domain.ReconciliationOutcome {
	if isInternal(a) != isInternal(b) {
		return domain.NoMatch
	}
	return domain.Match
}

// weakOutcome decides a pair that shares an amount block but no tracking key and
// no composite match (D71). Direction, amount and currency already agree inside
// a block, so the one decided contradiction left is internal against external
// (D55). Every other pair is AMBIGUOUS and never merges.
func weakOutcome(a, b domain.Transaction) domain.ReconciliationOutcome {
	if isInternal(a) != isInternal(b) {
		return domain.NoMatch
	}
	return domain.Ambiguous
}

func isInternal(t domain.Transaction) bool {
	return t.Counterparty() == domain.CounterpartySelf
}

type transactionPair struct{ left, right domain.Transaction }

// trackingKeyPairs groups Transactions by tracking key and stars each group
// (D36). A group of three collapses onto one survivor, because the smallest id
// is the survivor of every pair (D70).
func trackingKeyPairs(keyed []TrackingKeyed) []transactionPair {
	groups := map[string][]domain.Transaction{}
	for _, k := range keyed {
		groups[k.TrackingKey] = append(groups[k.TrackingKey], k.Transaction)
	}
	return starPairs(groups)
}

// weakPairs groups Transactions by an exact amount block and stars each group
// (D71). The block key is currency, amount and direction, all exact, so it
// invents no tolerance. A Transaction without Money has no amount to block on
// and is left out: with no amount and no key there is no signal to pair on.
func weakPairs(sourced []SourcedTransaction) []transactionPair {
	groups := map[string][]domain.Transaction{}
	for _, s := range sourced {
		money, ok := s.Transaction.Money()
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s|%d|%s", money.Currency(), money.Minor(), s.Transaction.Direction())
		groups[key] = append(groups[key], s.Transaction)
	}
	return starPairs(groups)
}

// compositePairs pairs Transactions the exact composite key identifies (D72).
// The key is amount, currency, direction, trimmed-casefolded merchant, and the
// day in UTC−6. A pair is emitted only when a key holds exactly two
// Transactions from two Sources, so a collision or a same-Source look-alike is
// left for the weak sweep. A Transaction with no Money or no merchant is skipped.
func compositePairs(sourced []SourcedTransaction) []transactionPair {
	type member struct {
		tx     domain.Transaction
		source string
	}
	groups := map[string][]member{}
	for _, s := range sourced {
		key, ok := compositeKey(s.Transaction)
		if !ok {
			continue
		}
		groups[key] = append(groups[key], member{tx: s.Transaction, source: s.SourceID})
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var pairs []transactionPair
	for _, key := range keys {
		g := groups[key]
		if len(g) != 2 || g[0].source == g[1].source {
			continue // not unique on each side, or one Source: leave it AMBIGUOUS
		}
		left, right := g[0].tx, g[1].tx
		if right.ID() < left.ID() {
			left, right = right, left
		}
		pairs = append(pairs, transactionPair{left: left, right: right})
	}
	return pairs
}

// compositeKey is the exact composite key of one Transaction, and whether it has
// one (D72). A Transaction without Money or without a party has no key: those
// two are the identity the composite match rests on. The party is the merchant,
// or the counterparty where a Source fills that field instead — a Nu email names
// the party in merchant, a statement in counterparty, and both mean one person.
func compositeKey(t domain.Transaction) (string, bool) {
	money, ok := t.Money()
	if !ok {
		return "", false
	}
	party := compositeParty(t)
	if party == "" {
		return "", false
	}
	day := t.OccurredAt().In(mxMinus6).Format("2006-01-02")
	return fmt.Sprintf("%s|%d|%s|%s|%s", money.Currency(), money.Minor(), t.Direction(), party, day), true
}

// compositeParty is the other party to the movement, trimmed and casefolded
// (D72). It reads merchant first, then counterparty, so the field a Source
// happens to use does not hide the identity the two Sources agree on.
func compositeParty(t domain.Transaction) string {
	party := strings.TrimSpace(t.Merchant())
	if party == "" {
		party = strings.TrimSpace(t.Counterparty())
	}
	return strings.ToLower(party)
}

// starPairs pairs the smallest id of each group with every other one. The order
// is stable, and each pair is canonical: left.ID < right.ID. A group of one
// yields no pair.
func starPairs(groups map[string][]domain.Transaction) []transactionPair {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var pairs []transactionPair
	for _, key := range keys {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ID() < group[j].ID() })
		for _, other := range group[1:] {
			pairs = append(pairs, transactionPair{left: group[0], right: other})
		}
	}
	return pairs
}
