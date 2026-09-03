package app

// Household is the set of accounts the user owns, and the Sources that post
// their ledgers (D78).
//
// An internal transfer is money that left one owned account and arrived in
// another. The matcher does not merge those rows (D75). Totals drop them
// when identity names both sides and amount, currency and time pick one pair.
type Household struct {
	// SourceAccount maps a Source id to the owned account that Source posts.
	// A statement Source has one entry. Two Sources may share an account.
	SourceAccount map[string]string

	// AliasAccount maps an exact merchant or counterparty string to an owned
	// account. The match is the stored text, not a normalised form (D78).
	AliasAccount map[string]string
}

// Empty reports if no Source is bound to an owned account. Cajita still uses
// the reserved counterparty (D55). No other row is an own-account transfer.
func (h Household) Empty() bool {
	return len(h.SourceAccount) == 0
}
