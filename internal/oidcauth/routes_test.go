package oidcauth_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/listings"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/oidcauth"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

const testUserID = "usera0000000001"

func testConfig(jwksURL string) oidcauth.Config {
	return oidcauth.Config{
		Enabled:     true,
		IssuerURL:   testIssuer,
		JWKSURL:     jwksURL,
		WebClientID: testWebAud,
		CLIClientID: testCLIAud,
		Scopes:      []string{"openid", "profile", "email"},
	}
}

func TestOIDCRoutesNotRegisteredWhenDisabled(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, oidcauth.Config{Enabled: false})
	handler := testsupport.BuildHandler(t, rg)

	rec := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/auth/oidc/config", "", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when OIDC is disabled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOIDCConfigEndpoint(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	rg := testsupport.NewRouter(t, pbApp)
	cfg := testConfig("https://issuer.example.com/jwks.json")
	oidcauth.RegisterRoutes(rg, cfg)
	handler := testsupport.BuildHandler(t, rg)

	rec := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/auth/oidc/config", "", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := testsupport.Decode(t, rec)
	if body["issuer"] != testIssuer || body["web_client_id"] != testWebAud || body["cli_client_id"] != testCLIAud {
		t.Fatalf("unexpected config body: %v", body)
	}
	if body["token_endpoint"] != testIssuer+"/api/oidc/token" {
		t.Fatalf("expected token_endpoint derived from issuer, got %v", body["token_endpoint"])
	}
}

func TestExchangeWithNoLinkedIdentityIsForbidden(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	pbApp := testsupport.NewMigratedApp(t)
	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, testConfig(server.URL))
	handler := testsupport.BuildHandler(t, rg)

	idToken := signToken(t, key, jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "unlinked-subject", Audience: []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/exchange", "",
		map[string]any{"id_token": idToken}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unlinked identity, got %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := testsupport.Decode(t, rec)["data"].(map[string]any)
	if data["code"] != "identity_unlinked" {
		t.Fatalf("expected data.code = identity_unlinked, got %v", data)
	}
}

func TestExchangeRejectsInvalidToken(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	pbApp := testsupport.NewMigratedApp(t)
	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, testConfig(server.URL))
	handler := testsupport.BuildHandler(t, rg)

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/exchange", "",
		map[string]any{"id_token": "not-a-real-jwt"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a malformed token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLinkThenExchangeAuthenticates(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	passwordToken := testsupport.AuthToken(t, user)

	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, testConfig(server.URL))
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)

	idToken := signToken(t, key, jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "user-42-sso", Audience: []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	link := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/link", passwordToken,
		map[string]any{"id_token": idToken}, nil)
	if link.Code != http.StatusOK {
		t.Fatalf("expected 200 linking a fresh identity, got %d: %s", link.Code, link.Body.String())
	}

	exchange := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/exchange", "",
		map[string]any{"id_token": idToken}, nil)
	if exchange.Code != http.StatusOK {
		t.Fatalf("expected 200 exchanging a linked identity, got %d: %s", exchange.Code, exchange.Body.String())
	}
	ssoToken, _ := testsupport.Decode(t, exchange)["token"].(string)
	if ssoToken == "" {
		t.Fatalf("expected a token in the exchange response, got %v", testsupport.Decode(t, exchange))
	}

	listResources := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings", ssoToken, nil, nil)
	if listResources.Code != http.StatusOK {
		t.Fatalf("expected the SSO-issued token to authenticate against resources/listings, got %d: %s", listResources.Code, listResources.Body.String())
	}
}

func TestLinkingAnIdentityAlreadyLinkedElsewhereConflicts(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	pbApp := testsupport.NewMigratedApp(t)
	userA := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	userB := testsupport.AddUser(t, pbApp, "userb0000000002", "b@example.com")
	tokenA := testsupport.AuthToken(t, userA)
	tokenB := testsupport.AuthToken(t, userB)

	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, testConfig(server.URL))
	handler := testsupport.BuildHandler(t, rg)

	idToken := signToken(t, key, jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "shared-subject", Audience: []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/link", tokenA,
		map[string]any{"id_token": idToken}, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200 for the first link, got %d: %s", first.Code, first.Body.String())
	}

	second := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/link", tokenB,
		map[string]any{"id_token": idToken}, nil)
	if second.Code != http.StatusConflict {
		t.Fatalf("expected 409 linking an identity already linked to a different account, got %d: %s", second.Code, second.Body.String())
	}
}

func TestLinkingASecondIdentityToTheSameAccountConflicts(t *testing.T) {
	key := newTestKey(t)
	server := startJWKSServer(t, key)
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	token := testsupport.AuthToken(t, user)

	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, testConfig(server.URL))
	handler := testsupport.BuildHandler(t, rg)

	firstIDToken := signToken(t, key, jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "first-subject", Audience: []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)
	secondIDToken := signToken(t, key, jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "second-subject", Audience: []string{testCLIAud},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	}, testKeyID)

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/link", token,
		map[string]any{"id_token": firstIDToken}, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200 for the first link, got %d: %s", first.Code, first.Body.String())
	}

	second := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/auth/oidc/link", token,
		map[string]any{"id_token": secondIDToken}, nil)
	if second.Code != http.StatusConflict {
		t.Fatalf("expected 409 linking a second identity to an account that already has one, got %d: %s", second.Code, second.Body.String())
	}
}

func TestPasswordAuthStillWorksAlongsideOIDC(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	oidcauth.RegisterRoutes(rg, testConfig("https://issuer.example.com/jwks.json"))
	handler := testsupport.BuildHandler(t, rg)

	rec := testsupport.Do(t, handler, http.MethodPost, "/api/collections/users/auth-with-password", "",
		map[string]any{"identity": "a@example.com", "password": "Test-Password-1234!"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the stock password flow to still work, got %d: %s", rec.Code, rec.Body.String())
	}
}
