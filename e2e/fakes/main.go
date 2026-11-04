//go:build e2e

// Command fakes runs what the end-to-end flows need around tenure and that
// cannot run in CI. It is built only with the e2e tag, is never part of the
// tenure binary, and listens on the loopback only.
//
// It runs three fakes:
//   - Socrate's authorize, token, userinfo and revoke endpoints and its JWKS.
//     /oauth/authorize signs the browser in at once, as E2E_SUB, with a fresh
//     MFA authentication, so step-up routes pass.
//   - An SMTP sink (internal/notify/smtptest) and GET /__e2e/mail, which lists
//     what it received.
//   - The INSEE IRL series, with sample values.
//
// It writes the environment tenure needs to the file named by -env and runs
// until it is stopped.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ovander/tenure-api/internal/notify/smtptest"
)

// The client the e2e tenure is configured with. Test-only values.
const (
	clientID     = "tenure-e2e"
	clientSecret = "e2e-client-secret"
	adminRole    = "tenure-admin"
	kid          = "e2e"
)

// Illustrative IRL values, not a reference.
const inseeSample = `<?xml version="1.0" encoding="UTF-8"?><message:StructureSpecificData xmlns:message="m"><message:DataSet>
<Series IDBANK="001515333"><Obs TIME_PERIOD="2025-Q4" OBS_VALUE="146.10"/><Obs TIME_PERIOD="2025-Q3" OBS_VALUE="145.80"/>
<Obs TIME_PERIOD="2025-Q2" OBS_VALUE="146.68"/><Obs TIME_PERIOD="2025-Q1" OBS_VALUE="145.47"/></Series></message:DataSet></message:StructureSpecificData>`

type grant struct {
	sub, challenge, redirect string
	created                  time.Time
}

type fakes struct {
	key    *rsa.PrivateKey
	issuer string
	sub    string
	mail   *smtptest.Server
	mu     sync.Mutex
	codes  map[string]grant
}

func main() {
	envFile := flag.String("env", "", "file to write tenure's Socrate, SMTP and IRL settings to")
	flag.Parse()
	if *envFile == "" {
		log.Fatal("fakes: -env is required")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	mail, err := smtptest.Start()
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	sub := os.Getenv("E2E_SUB")
	if sub == "" {
		sub = "e2e-owner"
	}
	f := &fakes{key: key, issuer: "http://" + ln.Addr().String(), sub: sub, mail: mail, codes: map[string]grant{}}
	env := strings.Join([]string{
		"SOCRATE_ISSUER=" + f.issuer,
		"SOCRATE_JWKS_URL=" + f.issuer + "/.well-known/jwks.json",
		"SOCRATE_CLIENT_ID=" + clientID,
		"SOCRATE_CLIENT_SECRET=" + clientSecret,
		"TENURE_SOCRATE_ADMIN_ROLE=" + adminRole,
		"TENURE_SMTP_ADDR=" + mail.Addr,
		"TENURE_IRL_SOURCE_URL=" + f.issuer + "/insee/irl",
		"E2E_FAKES_URL=" + f.issuer,
	}, "\n") + "\n"
	if err := os.WriteFile(*envFile, []byte(env), 0o600); err != nil {
		log.Fatal(err)
	}
	log.Printf("fakes: Socrate on %s, SMTP on %s", f.issuer, mail.Addr)
	srv := &http.Server{Handler: f.routes(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.Serve(ln))
}

func (f *fakes) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", f.jwks)
	mux.HandleFunc("GET /oauth/authorize", f.authorize)
	mux.HandleFunc("POST /oauth/token", f.token)
	mux.HandleFunc("GET /oauth/userinfo", f.userinfo)
	mux.HandleFunc("POST /oauth/revoke", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /insee/irl", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(inseeSample))
	})
	mux.HandleFunc("GET /__e2e/mail", func(w http.ResponseWriter, _ *http.Request) {
		type m struct {
			To      []string `json:"to"`
			Subject string   `json:"subject"`
		}
		out := []m{}
		for _, x := range f.mail.Messages() {
			out = append(out, m{To: x.To, Subject: header(x.Data, "Subject")})
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

func (f *fakes) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// authorize signs the browser in at once: it checks the request as Socrate
// would (client, PKCE S256, state) and sends the browser back with a code.
func (f *fakes) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if q.Get("client_id") != clientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || err != nil || back.Host == "" {
		http.Error(w, "invalid authorize request", http.StatusBadRequest)
		return
	}
	code := randomToken()
	f.mu.Lock()
	f.codes[code] = grant{sub: f.sub, challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), created: time.Now()}
	f.mu.Unlock()
	v := back.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakes) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if id != clientID || secret != clientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	var sub string
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		g, found := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !found || time.Since(g.created) > time.Minute || g.redirect != r.Form.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		sub = g.sub
	case "refresh_token":
		var found bool
		if sub, found = strings.CutPrefix(r.Form.Get("refresh_token"), "refresh:"); !found {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": f.sign(sub), "refresh_token": "refresh:" + sub,
		"token_type": "Bearer", "expires_in": 600, "roles": []string{adminRole}})
}

func (f *fakes) sign(sub string) string {
	now := time.Now()
	c := jwt.MapClaims{"iss": f.issuer, "aud": clientID, "sub": sub, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"role": adminRole, "auth_time": now.Unix(), "amr": []string{"pwd", "otp", "mfa"}}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	t.Header["kid"] = kid
	s, err := t.SignedString(f.key)
	if err != nil {
		log.Fatal(err)
	}
	return s
}

func (f *fakes) userinfo(w http.ResponseWriter, r *http.Request) {
	raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return &f.key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil || !tok.Valid {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	sub, _ := tok.Claims.GetSubject()
	writeJSON(w, http.StatusOK, map[string]string{"sub": sub, "name": "Bailleur E2E", "email": "e2e@example.invalid"})
}

func header(data, name string) string {
	for _, line := range strings.Split(data, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), name+": "); ok {
			return v
		}
	}
	return ""
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("random: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
