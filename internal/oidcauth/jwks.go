// Package oidcauth verifies OIDC ID tokens a client already obtained from
// its own identity provider via the device-authorization flow. This
// service never brokers that flow itself — it only publishes enough
// config for a client to find the provider (see RegisterRoutes) and
// verifies the token the client hands back.
package oidcauth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDocument struct {
	Keys []jwksKey `json:"keys"`
}

// Verifier fetches and caches a JWKS document to verify RS256 ID tokens
// issued by a single, fixed issuer for one of a fixed set of audiences.
type Verifier struct {
	jwksURL  string
	issuer   string
	audience []string

	cacheTTL time.Duration
	now      func() time.Time
	client   *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// NewVerifier builds a Verifier that trusts only RS256 tokens issued by
// issuer for one of audience, verified against the JWKS served at jwksURL.
func NewVerifier(jwksURL, issuer string, audience []string) *Verifier {
	return &Verifier{
		jwksURL:  jwksURL,
		issuer:   issuer,
		audience: audience,
		cacheTTL: 10 * time.Minute,
		now:      time.Now,
		client:   http.DefaultClient,
	}
}

func (v *Verifier) keyFunc(token *jwt.Token) (any, error) {
	kid, _ := token.Header["kid"].(string)
	if kid == "" {
		return nil, fmt.Errorf("identity token is missing a kid header")
	}
	keys, err := v.loadKeys()
	if err != nil {
		return nil, fmt.Errorf("fetching JWKS: %w", err)
	}
	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("no matching JWKS key for kid %q", kid)
	}
	return key, nil
}

func (v *Verifier) loadKeys() (map[string]*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.keys != nil && v.now().Sub(v.fetchedAt) < v.cacheTTL {
		return v.keys, nil
	}

	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var doc jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
	}

	v.keys = keys
	v.fetchedAt = v.now()
	return keys, nil
}

// VerifyIDToken parses and validates raw, returning its claims on success.
func (v *Verifier) VerifyIDToken(raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)

	token, err := parser.ParseWithClaims(raw, claims, v.keyFunc)
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid or expired identity token: %w", err)
	}

	audience, _ := claims.GetAudience()
	for _, got := range audience {
		for _, want := range v.audience {
			if got == want {
				return claims, nil
			}
		}
	}
	return nil, fmt.Errorf("identity token was not issued for this service")
}
