package gmail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ScopeReadonly is the only scope BillyCore ever requests.
//
// SECURITY.md §5: Billy reads email. It never needs to send, delete, or modify,
// and the token it holds should not be able to.
const ScopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"

const (
	authEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint = "https://oauth2.googleapis.com/token"
)

// Authorize runs the installed-app loopback flow and returns credentials
// carrying a refresh token.
//
// Interactive by design and by necessity: Google will not issue a refresh token
// without a human granting consent in a browser. This is why it lives behind a
// `billycore auth` subcommand rather than inside the daemon — SECURITY.md §6
// refuses to turn BillyCore into a binary you babysit through every restart.
func Authorize(ctx context.Context, clientID, clientSecret string) (*GmailCredentials, error) {
	// Port 0: the OS picks a free port. Google permits any port on a loopback
	// redirect for Desktop clients, so nothing has to be pre-registered.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("cannot open loopback listener for the OAuth callback: %w", err)
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	verifier, challenge, err := pkce()
	if err != nil {
		return nil, err
	}
	state, err := randomString()
	if err != nil {
		return nil, err
	}

	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {ScopeReadonly},
		"access_type":           {"offline"}, // without this there is no refresh token
		"prompt":                {"consent"}, // force one, even on a re-grant
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	authURL := authEndpoint + "?" + q.Encode()

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		qs := r.URL.Query()
		// Compare state before trusting anything else in the callback: it is
		// what ties this response to the request we actually made.
		if qs.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("OAuth callback state mismatch: the response does not belong to this request")}
			return
		}
		if e := qs.Get("error"); e != "" {
			http.Error(w, "authorization denied", http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("authorization denied: %s", e)}
			return
		}
		code := qs.Get("code")
		if code == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("OAuth callback carried no authorization code")}
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("BillyCore is authorized. You can close this tab."))
		resCh <- result{code: code}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Println("Open this URL to authorize BillyCore (read-only Gmail access):")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	openBrowser(authURL)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		return exchange(ctx, clientID, clientSecret, res.code, verifier, redirectURI)
	}
}

// tokenResponse is Google's token endpoint payload. Only the fields BillyCore
// uses are decoded.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func exchange(ctx context.Context, clientID, clientSecret, code, verifier, redirectURI string) (*GmailCredentials, error) {
	tok, err := postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	})
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		return nil, fmt.Errorf("Google returned no refresh token; re-run with a fresh consent grant")
	}
	return &GmailCredentials{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RefreshToken: tok.RefreshToken,
		AccessToken:  tok.AccessToken,
		TokenExpiry:  time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}, nil
}

// AccessTokenFor returns a usable access token, refreshing it when it is within
// a minute of expiry. The caller persists the credentials if changed reports true.
func AccessTokenFor(ctx context.Context, g *GmailCredentials) (token string, changed bool, err error) {
	if g.AccessToken != "" && time.Until(g.TokenExpiry) > time.Minute {
		return g.AccessToken, false, nil
	}
	tok, err := postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"client_id":     {g.ClientID},
		"client_secret": {g.ClientSecret},
	})
	if err != nil {
		return "", false, err
	}
	g.AccessToken = tok.AccessToken
	g.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return g.AccessToken, true, nil
}

func postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("token endpoint returned unreadable JSON (status %d): %w", resp.StatusCode, err)
	}
	if tok.Error != "" {
		// The form carries the client secret and the refresh token. Report the
		// endpoint's own error strings and never the request. SECURITY.md §10.
		return nil, fmt.Errorf("token endpoint rejected the request: %s: %s", tok.Error, tok.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}
	return &tok, nil
}

// pkce returns a verifier and its S256 challenge. PKCE binds the authorization
// code to this process, so a code intercepted on the loopback redirect is not
// redeemable without the verifier, which never leaves memory.
func pkce() (verifier, challenge string, err error) {
	verifier, err = randomString()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openBrowser is a convenience, never a requirement: the URL is always printed
// first so a headless or SSH session can copy it by hand.
func openBrowser(u string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "explorer"
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, u).Start()
}
