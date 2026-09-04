package oidcauth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/oidcauth"
)

const (
	testIssuer   = "https://sso.example.com"
	testKeyID    = "test-key"
	testWebAud   = "template-web"
	testCLIAud   = "template-cli"
	testSubject  = "user-42"
)

func startJWKSServer(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": testKeyID,
				"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.RegisteredClaims, kid string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestVerifyIDTokenAcceptsAValidToken(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	verifier := oidcauth.NewVerifier(server.URL, testIssuer, []string{testWebAud, testCLIAud})

	raw := signToken(t, key, jwt.RegisteredClaims{
		Issuer:    testIssuer,
		Subject:   testSubject,
		Audience:  []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}, testKeyID)

	claims, err := verifier.VerifyIDToken(raw)
	if err != nil {
		t.Fatalf("expected a valid token to verify, got %v", err)
	}
	if claims["sub"] != testSubject {
		t.Fatalf("expected sub %q, got %v", testSubject, claims["sub"])
	}
}

func TestVerifyIDTokenRejectsWrongAudience(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	verifier := oidcauth.NewVerifier(server.URL, testIssuer, []string{testWebAud, testCLIAud})

	raw := signToken(t, key, jwt.RegisteredClaims{
		Issuer:    testIssuer,
		Subject:   testSubject,
		Audience:  []string{"someone-else"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	if _, err := verifier.VerifyIDToken(raw); err == nil {
		t.Fatal("expected an error for a token issued for a different audience")
	}
}

func TestVerifyIDTokenRejectsWrongIssuer(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	verifier := oidcauth.NewVerifier(server.URL, testIssuer, []string{testCLIAud})

	raw := signToken(t, key, jwt.RegisteredClaims{
		Issuer:    "https://not-the-real-issuer.example.com",
		Subject:   testSubject,
		Audience:  []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	if _, err := verifier.VerifyIDToken(raw); err == nil {
		t.Fatal("expected an error for a token from an unexpected issuer")
	}
}

func TestVerifyIDTokenRejectsExpiredToken(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	verifier := oidcauth.NewVerifier(server.URL, testIssuer, []string{testCLIAud})

	raw := signToken(t, key, jwt.RegisteredClaims{
		Issuer:    testIssuer,
		Subject:   testSubject,
		Audience:  []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-5 * time.Minute)),
	}, testKeyID)

	if _, err := verifier.VerifyIDToken(raw); err == nil {
		t.Fatal("expected an error for an expired token")
	}
}

func TestVerifyIDTokenRejectsWrongSigningKey(t *testing.T) {
	key := newTestKey(t)
	otherKey := newTestKey(t)
	server := startJWKSServer(t, key) // JWKS only knows about `key`, not `otherKey`
	verifier := oidcauth.NewVerifier(server.URL, testIssuer, []string{testCLIAud})

	raw := signToken(t, otherKey, jwt.RegisteredClaims{
		Issuer:    testIssuer,
		Subject:   testSubject,
		Audience:  []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	if _, err := verifier.VerifyIDToken(raw); err == nil {
		t.Fatal("expected an error for a token signed by an untrusted key")
	}
}
