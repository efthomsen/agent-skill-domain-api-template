package oidcauth

import (
	"net/http"
	"os"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
)

// provider is the value stored in the system _externalAuths collection's
// "provider" field. PocketBase validates this against its own registered
// tools/auth provider names, and "oidc" is one of them, so this passes
// that validation without registering a full oauth2 provider.
const provider = "oidc"

// Config controls whether OIDC routes are exposed and how they discover
// and verify the identity provider. The zero value (Enabled: false) keeps
// the stock password flow as the only auth path.
type Config struct {
	Enabled     bool
	IssuerURL   string
	JWKSURL     string
	WebClientID string
	CLIClientID string
	Scopes      []string
}

// LoadConfig builds a Config from environment variables, so a deployment
// with no OIDC provider configured gets Enabled: false for free.
func LoadConfig() Config {
	issuer := envOr("TEMPLATE_OIDC_ISSUER", "https://sso.example.com")
	return Config{
		Enabled:     os.Getenv("TEMPLATE_OIDC_ENABLED") == "true",
		IssuerURL:   issuer,
		JWKSURL:     envOr("TEMPLATE_OIDC_JWKS_URL", issuer+"/jwks.json"),
		WebClientID: envOr("TEMPLATE_OIDC_WEB_CLIENT_ID", "template-web"),
		CLIClientID: envOr("TEMPLATE_OIDC_CLI_CLIENT_ID", "template-cli"),
		Scopes:      []string{"openid", "profile", "email"},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RegisterRoutes wires the OIDC discovery/exchange/link endpoints onto rg,
// only when cfg.Enabled.
func RegisterRoutes(rg *router.Router[*core.RequestEvent], cfg Config) {
	if !cfg.Enabled {
		return
	}
	verifier := NewVerifier(cfg.JWKSURL, cfg.IssuerURL, []string{cfg.WebClientID, cfg.CLIClientID})

	rg.GET(httpapi.APIPrefix+"/auth/oidc/config", configAction(cfg))
	rg.POST(httpapi.APIPrefix+"/auth/oidc/exchange", exchangeAction(verifier))
	rg.POST(httpapi.APIPrefix+"/auth/oidc/link", linkAction(verifier)).Bind(apis.RequireAuth())
}

func configAction(cfg Config) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]any{
			"enabled":                       true,
			"issuer":                        cfg.IssuerURL,
			"authorization_endpoint":        cfg.IssuerURL + "/api/oidc/authorization",
			"token_endpoint":                cfg.IssuerURL + "/api/oidc/token",
			"device_authorization_endpoint": cfg.IssuerURL + "/api/oidc/device-authorization",
			"web_client_id":                 cfg.WebClientID,
			"cli_client_id":                 cfg.CLIClientID,
			"scopes":                        cfg.Scopes,
		})
	}
}

func exchangeAction(verifier *Verifier) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		subject, err := verifiedSubject(e, verifier)
		if err != nil {
			return err
		}

		usersCollection, err := e.App.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		link, err := e.App.FindFirstExternalAuthByExpr(dbx.HashExp{
			"collectionRef": usersCollection.Id,
			"provider":      provider,
			"providerId":    subject,
		})
		if err != nil {
			return httpapi.IdentityUnlinkedError()
		}
		user, err := e.App.FindRecordById(usersCollection, link.RecordRef())
		if err != nil {
			return httpapi.IdentityUnlinkedError()
		}

		return apis.RecordAuthResponse(e, user, provider, nil)
	}
}

func linkAction(verifier *Verifier) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		subject, err := verifiedSubject(e, verifier)
		if err != nil {
			return err
		}

		usersCollection := e.Auth.Collection()

		if existing, ferr := e.App.FindFirstExternalAuthByExpr(dbx.HashExp{
			"collectionRef": usersCollection.Id,
			"provider":      provider,
			"providerId":    subject,
		}); ferr == nil && existing.RecordRef() != e.Auth.Id {
			return httpapi.IdentityAlreadyLinkedError()
		}

		if existing, ferr := e.App.FindFirstExternalAuthByExpr(dbx.HashExp{
			"collectionRef": usersCollection.Id,
			"provider":      provider,
			"recordRef":     e.Auth.Id,
		}); ferr == nil && existing.ProviderId() != subject {
			return httpapi.IdentityAlreadyLinkedError()
		}

		link := core.NewExternalAuth(e.App)
		link.SetCollectionRef(usersCollection.Id)
		link.SetRecordRef(e.Auth.Id)
		link.SetProvider(provider)
		link.SetProviderId(subject)
		if err := e.App.Save(link); err != nil {
			return err
		}

		return apis.RecordAuthResponse(e, e.Auth, provider, nil)
	}
}

func verifiedSubject(e *core.RequestEvent, verifier *Verifier) (string, error) {
	body, err := httpapi.Body(e)
	if err != nil {
		return "", err
	}
	idToken, _ := body["id_token"].(string)
	if idToken == "" {
		return "", httpapi.IdentityInvalidError()
	}
	claims, err := verifier.VerifyIDToken(idToken)
	if err != nil {
		return "", httpapi.IdentityInvalidError()
	}
	subject, _ := claims["sub"].(string)
	if subject == "" {
		return "", httpapi.IdentityInvalidError()
	}
	return subject, nil
}
