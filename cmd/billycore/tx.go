package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

type txPage struct {
	Data []txRow `json:"data"`
}

type txRow struct {
	Amount *struct {
		Minor    int64  `json:"amount_minor"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Direction           string   `json:"direction"`
	FinancialStatus     string   `json:"financial_status"`
	ReconciliationState string   `json:"reconciliation_state"`
	OccurredAt          string   `json:"occurred_at"`
	EvidenceIDs         []string `json:"evidence_ids"`
	Merchant            *struct {
		Value      string `json:"value"`
		Confidence string `json:"confidence"`
	} `json:"merchant"`
	Counterparty *struct {
		Value      string `json:"value"`
		Confidence string `json:"confidence"`
	} `json:"counterparty"`
	Internal bool `json:"internal"`
}

type txTotals struct {
	Currency string `json:"currency"`
	Income   struct {
		AmountMinor int64 `json:"amount_minor"`
		Count       int   `json:"count"`
	} `json:"income"`
	Expense struct {
		AmountMinor int64 `json:"amount_minor"`
		Count       int   `json:"count"`
	} `json:"expense"`
	NetMinor         int64 `json:"net_minor"`
	ExcludedInternal int   `json:"excluded_internal"`
}

func runTx(args []string) error {
	fs := flag.NewFlagSet("tx", flag.ContinueOnError)
	endpoint := fs.String("url", "http://127.0.0.1:8787", "BillyCore server URL")
	days := fs.Int("days", 30, "number of recent days to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *days < 1 {
		return fmt.Errorf("days must be positive")
	}
	token := os.Getenv(tokenEnv)
	if token == "" {
		return fmt.Errorf("%s is not set", tokenEnv)
	}
	return fetchAndWriteTransactions(http.DefaultClient, os.Stdout, *endpoint, token, time.Now().UTC().AddDate(0, 0, -*days))
}

func fetchAndWriteTransactions(client *http.Client, out io.Writer, endpoint, token string, from time.Time) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("transactions: invalid server URL: %w", err)
	}
	u.Path = "/v1/transactions"
	q := u.Query()
	q.Set("from", from.UTC().Format(time.RFC3339))
	q.Set("limit", strconv.Itoa(200))
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("transactions: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("transactions: request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("transactions: server returned %s", response.Status)
	}

	var page txPage
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&page); err != nil {
		return fmt.Errorf("transactions: decode response: %w", err)
	}
	if err := writeTransactionTable(out, page.Data); err != nil {
		return err
	}
	return fetchAndWriteTotals(client, out, endpoint, token, from)
}

// fetchAndWriteTotals reads the summary and prints a footer. Income and spending
// exclude internal movements, which are the user's own money (D55).
func fetchAndWriteTotals(client *http.Client, out io.Writer, endpoint, token string, from time.Time) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("totals: invalid server URL: %w", err)
	}
	u.Path = "/v1/transactions/summary"
	q := u.Query()
	q.Set("from", from.UTC().Format(time.RFC3339))
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("totals: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("totals: request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("totals: server returned %s", response.Status)
	}
	var totals txTotals
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&totals); err != nil {
		return fmt.Errorf("totals: decode response: %w", err)
	}
	return writeTotals(out, totals)
}

func writeTotals(out io.Writer, t txTotals) error {
	money := func(minor int64) string {
		m, err := domain.NewMoney(minor, domain.Currency(t.Currency))
		if err != nil {
			return "—"
		}
		return m.Decimal() // already carries the currency, e.g. "899.93 MXN"
	}
	net := money(t.NetMinor)
	if t.NetMinor < 0 {
		net = "-" + money(-t.NetMinor)
	}
	_, err := fmt.Fprintf(out,
		"\nIncome   %s (%d)\nExpense  %s (%d)\nNet      %s\nInternal movements excluded: %d\n",
		money(t.Income.AmountMinor), t.Income.Count,
		money(t.Expense.AmountMinor), t.Expense.Count, net, t.ExcludedInternal)
	return err
}

func writeTransactionTable(out io.Writer, rows []txRow) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "DATE\tDIRECTION\tAMOUNT\tCOUNTERPARTY\tINTERNAL\tSTATUS\tRECONCILIATION\tEVIDENCE"); err != nil {
		return err
	}
	for _, row := range rows {
		amount := "—"
		if row.Amount != nil {
			money, err := domain.NewMoney(row.Amount.Minor, domain.Currency(row.Amount.Currency))
			if err != nil {
				return fmt.Errorf("transactions: invalid money: %w", err)
			}
			amount = money.Decimal()
		}
		at, err := time.Parse(time.RFC3339, row.OccurredAt)
		if err != nil {
			return fmt.Errorf("transactions: invalid occurred_at: %w", err)
		}
		internal := "—"
		if row.Internal {
			internal = "yes"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			at.Local().Format("2006-01-02 15:04"), row.Direction, amount, otherParty(row), internal,
			row.FinancialStatus, row.ReconciliationState, len(row.EvidenceIDs)); err != nil {
			return err
		}
	}
	return w.Flush()
}

// otherParty names who was on the other side. The counterparty comes first,
// because a transfer names a person and a purchase names no counterparty. The
// reserved value shows as "self" and never as the raw URN (D65).
func otherParty(row txRow) string {
	if row.Counterparty != nil {
		if row.Counterparty.Value == domain.CounterpartySelf {
			return "self"
		}
		return row.Counterparty.Value + " [" + row.Counterparty.Confidence + "]"
	}
	if row.Merchant != nil {
		return row.Merchant.Value + " [" + row.Merchant.Confidence + "]"
	}
	return "—"
}
