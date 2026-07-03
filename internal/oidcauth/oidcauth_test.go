package oidcauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/config"
)

// fakeIdP is a minimal OpenID Connect provider: discovery + JWKS + RS256
// token minting, following the repo convention of real HTTP servers over
// mocking frameworks.
type fakeIdP struct {
	t      *testing.T
	key    *rsa.PrivateKey
	server *httptest.Server
	// issuer reported in the discovery document (may differ from the
	// server URL to exercise issuer validation).
	issuer string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{t: t, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":   idp.issuer,
			"jwks_uri": idp.server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	idp.issuer = idp.server.URL
	return idp
}

// mint signs an RS256 JWT with the given claims.
func (f *fakeIdP) mint(claims map[string]any) string {
	f.t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"test"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		f.t.Fatal(err)
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIdP) claims(mutate func(map[string]any)) map[string]any {
	c := map[string]any{
		"iss":   f.issuer,
		"aud":   "obsidian-mcp",
		"sub":   "user-1",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Add(-time.Minute).Unix(),
		"scope": "openid profile",
	}
	if mutate != nil {
		mutate(c)
	}
	return c
}

func (f *fakeIdP) verifier(t *testing.T) *Verifier {
	t.Helper()
	v, err := New(context.Background(), &config.OAuth{
		Issuer:   f.issuer,
		Audience: "obsidian-mcp",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerifyValidToken(t *testing.T) {
	idp := newFakeIdP(t)
	v := idp.verifier(t)
	info, err := v.Verify(context.Background(), idp.mint(idp.claims(nil)))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if info.UserID != "user-1" {
		t.Errorf("UserID = %q", info.UserID)
	}
	if want := []string{"openid", "profile"}; fmt.Sprint(info.Scopes) != fmt.Sprint(want) {
		t.Errorf("Scopes = %v", info.Scopes)
	}
	if info.Expiration.IsZero() || info.Expiration.Before(time.Now()) {
		t.Errorf("Expiration = %v", info.Expiration)
	}
}

func TestVerifyAzpFallback(t *testing.T) {
	idp := newFakeIdP(t)
	v := idp.verifier(t)
	tok := idp.mint(idp.claims(func(c map[string]any) {
		c["aud"] = "account"
		c["azp"] = "obsidian-mcp"
	}))
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("Verify with azp fallback: %v", err)
	}
}

func TestVerifyRejections(t *testing.T) {
	idp := newFakeIdP(t)
	v := idp.verifier(t)
	cases := []struct {
		name string
		tok  string
	}{
		{"garbage", "not-a-jwt"},
		{"wrong audience", idp.mint(idp.claims(func(c map[string]any) {
			c["aud"] = "someone-else"
			delete(c, "azp")
		}))},
		{"wrong azp only", idp.mint(idp.claims(func(c map[string]any) {
			c["aud"] = "account"
			c["azp"] = "someone-else"
		}))},
		{"expired", idp.mint(idp.claims(func(c map[string]any) {
			c["exp"] = time.Now().Add(-time.Hour).Unix()
		}))},
		{"wrong issuer", idp.mint(idp.claims(func(c map[string]any) {
			c["iss"] = "https://evil.example.com"
		}))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), c.tok); err == nil {
				t.Error("Verify accepted the token")
			}
		})
	}
}

func TestVerifyRejectsMalformedScopeClaim(t *testing.T) {
	idp := newFakeIdP(t)
	v := idp.verifier(t)
	tok := idp.mint(idp.claims(func(c map[string]any) {
		c["scope"] = 123 // not a string: claims parsing must fail closed
	}))
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Error("Verify accepted a token with a malformed scope claim")
	}
}

func TestVerifyRejectsForgedSignature(t *testing.T) {
	idp := newFakeIdP(t)
	forger := newFakeIdP(t) // different key, same claims shape
	forger.issuer = idp.issuer
	v := idp.verifier(t)
	if _, err := v.Verify(context.Background(), forger.mint(idp.claims(nil))); err == nil {
		t.Error("Verify accepted a token signed by the wrong key")
	}
}

func TestInternalIssuerFetch(t *testing.T) {
	idp := newFakeIdP(t)
	// Discovery reports a public issuer that is NOT where we fetched from,
	// mirroring a cluster-internal Keycloak URL with a public frontend URL.
	idp.issuer = "https://public.example.com/realms/lab"
	v, err := New(context.Background(), &config.OAuth{
		Issuer:         "https://public.example.com/realms/lab",
		InternalIssuer: idp.server.URL,
		Audience:       "obsidian-mcp",
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := v.Verify(context.Background(), idp.mint(idp.claims(nil))); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	idp := newFakeIdP(t)
	cases := []struct {
		name string
		cfg  *config.OAuth
	}{
		{"unreachable", &config.OAuth{Issuer: "http://127.0.0.1:1", Audience: "a"}},
		{"issuer mismatch", &config.OAuth{Issuer: "https://elsewhere.example.com", InternalIssuer: idp.server.URL, Audience: "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(context.Background(), c.cfg, nil); err == nil {
				t.Error("New succeeded")
			}
		})
	}
	t.Run("discovery 404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
		defer srv.Close()
		if _, err := New(context.Background(), &config.OAuth{Issuer: srv.URL, Audience: "a"}, nil); err == nil {
			t.Error("New succeeded on 404 discovery")
		}
	})
	t.Run("invalid issuer URL", func(t *testing.T) {
		if _, err := New(context.Background(), &config.OAuth{Issuer: "http://example.com/\x7f\x00bad", InternalIssuer: "http://\x7f", Audience: "a"}, nil); err == nil {
			t.Error("New succeeded on unparseable issuer URL")
		}
	})
	t.Run("bad discovery json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "not json")
		}))
		defer srv.Close()
		if _, err := New(context.Background(), &config.OAuth{Issuer: srv.URL, Audience: "a"}, nil); err == nil {
			t.Error("New succeeded on bad discovery JSON")
		}
	})
	t.Run("missing jwks_uri", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "openid-configuration") {
				fmt.Fprintf(w, `{"issuer":%q}`, "http://"+r.Host)
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		if _, err := New(context.Background(), &config.OAuth{Issuer: srv.URL, Audience: "a"}, nil); err == nil {
			t.Error("New succeeded without jwks_uri")
		}
	})
}
