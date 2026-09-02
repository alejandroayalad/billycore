package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

const (
	defaultTransactionLimit = 50
	maximumTransactionLimit = 200
)

type moneyResponse struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type supportedTextResponse struct {
	Value      string `json:"value"`
	Confidence string `json:"confidence"`
}

type transactionResponse struct {
	ID                  string                 `json:"id"`
	Amount              *moneyResponse         `json:"amount"`
	Direction           string                 `json:"direction"`
	FinancialStatus     string                 `json:"financial_status"`
	ReconciliationState string                 `json:"reconciliation_state"`
	OccurredAt          string                 `json:"occurred_at"`
	Merchant            *supportedTextResponse `json:"merchant"`
	Counterparty        *supportedTextResponse `json:"counterparty"`
	Account             *supportedTextResponse `json:"account"`

	// Internal is true when both sides of the movement are the user: a Cajita
	// or respaldo row (D62), or a SPEI between two of the user's accounts
	// (D75). A client that reports income or spending excludes it (D55).
	Internal      bool     `json:"internal"`
	EvidenceIDs   []string `json:"evidence_ids"`
	Relationships []any    `json:"relationships"`
}

type transactionListResponse struct {
	Data       []transactionResponse `json:"data"`
	NextCursor *string               `json:"next_cursor"`
}

type queryParameterError struct {
	Field  string
	Detail string
}

func (e queryParameterError) Error() string { return e.Detail }

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	query, err := parseTransactionQuery(r)
	if err != nil {
		var problem queryParameterError
		if !errors.As(err, &problem) {
			problem = queryParameterError{Detail: "is not valid"}
		}
		writeError(w, http.StatusBadRequest, typeMalformedRequest,
			"One or more query parameters are invalid.", violation{
				Code: "query.invalid", Field: problem.Field, Detail: problem.Detail,
			})
		return
	}
	page, err := s.transactions.List(r.Context(), query)
	if err != nil {
		slog.Error("cannot list transactions", "error", err)
		writeError(w, http.StatusInternalServerError, typeInternalError,
			"BillyCore could not read Transactions.")
		return
	}

	response := transactionListResponse{Data: make([]transactionResponse, 0, len(page.Transactions))}
	for _, item := range page.Transactions {
		response.Data = append(response.Data, transactionRepresentation(item))
	}
	if page.HasMore && len(page.Transactions) > 0 {
		last := page.Transactions[len(page.Transactions)-1].Transaction
		cursor := encodeTransactionCursor(app.TransactionCursor{OccurredAt: last.OccurredAt(), ID: last.ID()})
		response.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, response)
}

type totalsSideResponse struct {
	AmountMinor int64 `json:"amount_minor"`
	Count       int   `json:"count"`
}

type transactionTotalsResponse struct {
	Currency string             `json:"currency"`
	Income   totalsSideResponse `json:"income"`
	Expense  totalsSideResponse `json:"expense"`
	NetMinor int64              `json:"net_minor"`

	// ExcludedInternal is how many internal movements were left out of the
	// totals. They are the user's own money and are neither income nor spending
	// (D55), but a reader is told they exist.
	ExcludedInternal int `json:"excluded_internal"`
}

// handleTransactionTotals answers GET /v1/transactions/summary. It honours the
// from, to and currency filters and ignores the cursor and the limit.
func (s *Server) handleTransactionTotals(w http.ResponseWriter, r *http.Request) {
	query, err := parseTransactionQuery(r)
	if err != nil {
		var problem queryParameterError
		if !errors.As(err, &problem) {
			problem = queryParameterError{Detail: "is not valid"}
		}
		writeError(w, http.StatusBadRequest, typeMalformedRequest,
			"One or more query parameters are invalid.", violation{
				Code: "query.invalid", Field: problem.Field, Detail: problem.Detail,
			})
		return
	}
	totals, err := s.transactions.Totals(r.Context(), query)
	if err != nil {
		slog.Error("cannot total transactions", "error", err)
		writeError(w, http.StatusInternalServerError, typeInternalError,
			"BillyCore could not total Transactions.")
		return
	}
	writeJSON(w, http.StatusOK, transactionTotalsResponse{
		Currency:         totals.Currency.String(),
		Income:           totalsSideResponse{AmountMinor: totals.IncomeMinor, Count: totals.IncomeCount},
		Expense:          totalsSideResponse{AmountMinor: totals.ExpenseMinor, Count: totals.ExpenseCount},
		NetMinor:         totals.IncomeMinor - totals.ExpenseMinor,
		ExcludedInternal: totals.ExcludedInternal,
	})
}

func parseTransactionQuery(r *http.Request) (app.TransactionQuery, error) {
	values := r.URL.Query()
	query := app.TransactionQuery{Limit: defaultTransactionLimit}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maximumTransactionLimit {
			return query, queryParameterError{Field: "limit", Detail: fmt.Sprintf("must be between 1 and %d", maximumTransactionLimit)}
		}
		query.Limit = limit
	}
	var err error
	if query.From, err = optionalTime(values.Get("from")); err != nil {
		return query, queryParameterError{Field: "from", Detail: "must be RFC 3339"}
	}
	if query.To, err = optionalTime(values.Get("to")); err != nil {
		return query, queryParameterError{Field: "to", Detail: "must be RFC 3339"}
	}
	for _, raw := range values["direction"] {
		value := domain.TransactionDirection(raw)
		if err := value.Validate(); err != nil {
			return query, queryParameterError{Field: "direction", Detail: "must be INFLOW or OUTFLOW"}
		}
		query.Directions = append(query.Directions, value)
	}
	for _, raw := range values["financial_status"] {
		value := domain.FinancialStatus(raw)
		if err := value.Validate(); err != nil {
			return query, queryParameterError{Field: "financial_status", Detail: "is not a known financial status"}
		}
		query.FinancialStatuses = append(query.FinancialStatuses, value)
	}
	for _, raw := range values["reconciliation_state"] {
		value := domain.ReconciliationState(raw)
		if err := value.Validate(); err != nil {
			return query, queryParameterError{Field: "reconciliation_state", Detail: "is not a known reconciliation state"}
		}
		query.ReconciliationStates = append(query.ReconciliationStates, value)
	}
	if raw := values.Get("currency"); raw != "" {
		query.Currency = domain.Currency(raw)
		if err := query.Currency.Validate(); err != nil {
			return query, queryParameterError{Field: "currency", Detail: "is not a supported currency"}
		}
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := decodeTransactionCursor(raw)
		if err != nil {
			return query, queryParameterError{Field: "cursor", Detail: "is not valid"}
		}
		query.After = &cursor
	}
	return query, nil
}

func optionalTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func transactionRepresentation(item app.ListedTransaction) transactionResponse {
	tx := item.Transaction
	response := transactionResponse{
		ID: tx.ID(), Direction: tx.Direction().String(), FinancialStatus: tx.FinancialStatus().String(),
		ReconciliationState: tx.ReconciliationState().String(), OccurredAt: tx.OccurredAt().Format(timeLayout),
		EvidenceIDs: tx.EvidenceIDs(), Relationships: []any{},
	}
	if money, ok := tx.Money(); ok {
		response.Amount = &moneyResponse{AmountMinor: money.Minor(), Currency: money.Currency().String()}
	}
	if tx.Merchant() != "" {
		response.Merchant = &supportedTextResponse{Value: tx.Merchant(), Confidence: item.MerchantConfidence.String()}
	}
	// The counterparty carries the raw value, the reserved one included. The
	// human readers translate it and never print it raw; a machine client
	// reads `internal` (D65).
	if tx.Counterparty() != "" {
		response.Counterparty = &supportedTextResponse{Value: tx.Counterparty(), Confidence: item.CounterpartyConfidence.String()}
	}
	response.Internal = tx.Counterparty() == domain.CounterpartySelf || item.OwnAccountTransfer
	if tx.AccountIdentifier() != "" {
		response.Account = &supportedTextResponse{Value: tx.AccountIdentifier(), Confidence: item.AccountConfidence.String()}
	}
	return response
}

func encodeTransactionCursor(cursor app.TransactionCursor) string {
	payload, _ := json.Marshal(struct {
		Time string `json:"t"`
		ID   string `json:"id"`
	}{Time: cursor.OccurredAt.UTC().Format(timeLayout), ID: cursor.ID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeTransactionCursor(raw string) (app.TransactionCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return app.TransactionCursor{}, err
	}
	var wire struct {
		Time string `json:"t"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil || wire.ID == "" {
		return app.TransactionCursor{}, fmt.Errorf("invalid cursor")
	}
	occurredAt, err := time.Parse(timeLayout, wire.Time)
	if err != nil {
		return app.TransactionCursor{}, err
	}
	return app.TransactionCursor{OccurredAt: occurredAt, ID: wire.ID}, nil
}
