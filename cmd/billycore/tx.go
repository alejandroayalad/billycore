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
	return writeTransactionTable(out, page.Data)
}

func writeTransactionTable(out io.Writer, rows []txRow) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "DATE\tDIRECTION\tAMOUNT\tCOUNTERPARTY\tSTATUS\tRECONCILIATION\tEVIDENCE"); err != nil {
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
		counterparty := "—"
		if row.Merchant != nil {
			counterparty = row.Merchant.Value + " [" + row.Merchant.Confidence + "]"
		}
		at, err := time.Parse(time.RFC3339, row.OccurredAt)
		if err != nil {
			return fmt.Errorf("transactions: invalid occurred_at: %w", err)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			at.Local().Format("2006-01-02 15:04"), row.Direction, amount, counterparty,
			row.FinancialStatus, row.ReconciliationState, len(row.EvidenceIDs)); err != nil {
			return err
		}
	}
	return w.Flush()
}
