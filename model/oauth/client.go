package oauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/model/bitwarden/settings"
	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/job"
	"github.com/cozy/cozy-stack/model/notification"
	"github.com/cozy/cozy-stack/model/permission"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/couchdb/mango"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/pkg/metadata"
	"github.com/cozy/cozy-stack/pkg/registry"

	jwt "github.com/golang-jwt/jwt/v4"
)

const (
	// PlatformFirebase platform using Firebase Cloud Messaging (FCM)
	PlatformFirebase = "firebase"
	// PlatformAPNS platform using APNS/2
	PlatformAPNS = "apns"
	// PlatformHuawei platform using Huawei Push Kit
	PlatformHuawei = "huawei"
)

// DocTypeVersion represents the doctype version. Each time this document
// structure is modified, update this value
const DocTypeVersion = "1"

// ClientSecretLen is the number of random bytes used for generating the client secret
const ClientSecretLen = 24

// ChallengeLen is the number of random bytes used for generating a nonce for
// certifying an android/iOS app.
const ChallengeLen = 24

// ScopeLogin is the special scope used by the manager or any other client
// for login/authentication purposes.
const ScopeLogin = "login"

// CleanMessage is used for messages to the clean-clients worker.
type CleanMessage struct {
	ClientID string `json:"client_id"`
}

// Client is a struct for OAuth2 client. Most of the fields are described in
// the OAuth 2.0 Dynamic Client Registration Protocol. The exception is
// `client_kind`, and it is an optional field.
// See https://tools.ietf.org/html/rfc7591
//
// CouchID and ClientID are the same. They are just two ways to serialize to
// JSON, one for CouchDB and the other for the Dynamic Client Registration
// Protocol.
type Client struct {
	CouchID  string `json:"_id,omitempty"`  // Generated by CouchDB
	CouchRev string `json:"_rev,omitempty"` // Generated by CouchDB

	ClientID          string `json:"client_id,omitempty"`                 // Same as CouchID
	ClientSecret      string `json:"client_secret,omitempty"`             // Generated by the server
	SecretExpiresAt   int    `json:"client_secret_expires_at"`            // Forced by the server to 0 (no expiration)
	RegistrationToken string `json:"registration_access_token,omitempty"` // Generated by the server
	AllowLoginScope   bool   `json:"allow_login_scope,omitempty"`         // Allow to generate token for a "login" scope (no permissions)
	Pending           bool   `json:"pending,omitempty"`                   // True until a token is generated

	RedirectURIs    []string `json:"redirect_uris"`              // Declared by the client (mandatory)
	GrantTypes      []string `json:"grant_types"`                // Forced by the server to ["authorization_code", "refresh_token"]
	ResponseTypes   []string `json:"response_types"`             // Forced by the server to ["code"]
	ClientName      string   `json:"client_name"`                // Declared by the client (mandatory)
	ClientKind      string   `json:"client_kind,omitempty"`      // Declared by the client (optional, can be "desktop", "mobile", "browser", etc.)
	ClientURI       string   `json:"client_uri,omitempty"`       // Declared by the client (optional)
	LogoURI         string   `json:"logo_uri,omitempty"`         // Declared by the client (optional)
	PolicyURI       string   `json:"policy_uri,omitempty"`       // Declared by the client (optional)
	SoftwareID      string   `json:"software_id"`                // Declared by the client (mandatory)
	SoftwareVersion string   `json:"software_version,omitempty"` // Declared by the client (optional)

	// Notifications parameters
	Notifications map[string]notification.Properties `json:"notifications,omitempty"`

	NotificationPlatform    string `json:"notification_platform,omitempty"`     // Declared by the client (optional)
	NotificationDeviceToken string `json:"notification_device_token,omitempty"` // Declared by the client (optional)

	// XXX omitempty does not work for time.Time, thus the interface{} type
	SynchronizedAt interface{} `json:"synchronized_at,omitempty"` // Date of the last synchronization, updated by /settings/synchronized

	Flagship bool `json:"flagship,omitempty"`

	OnboardingSecret      string `json:"onboarding_secret,omitempty"`
	OnboardingApp         string `json:"onboarding_app,omitempty"`
	OnboardingPermissions string `json:"onboarding_permissions,omitempty"`
	OnboardingState       string `json:"onboarding_state,omitempty"`

	Metadata *metadata.CozyMetadata `json:"cozyMetadata,omitempty"`
}

// ID returns the client qualified identifier
func (c *Client) ID() string { return c.CouchID }

// Rev returns the client revision
func (c *Client) Rev() string { return c.CouchRev }

// DocType returns the client document type
func (c *Client) DocType() string { return consts.OAuthClients }

// Clone implements couchdb.Doc
func (c *Client) Clone() couchdb.Doc {
	cloned := *c
	cloned.RedirectURIs = make([]string, len(c.RedirectURIs))
	copy(cloned.RedirectURIs, c.RedirectURIs)

	cloned.GrantTypes = make([]string, len(c.GrantTypes))
	copy(cloned.GrantTypes, c.GrantTypes)

	cloned.ResponseTypes = make([]string, len(c.ResponseTypes))
	copy(cloned.ResponseTypes, c.ResponseTypes)

	cloned.Notifications = make(map[string]notification.Properties)
	for k, v := range c.Notifications {
		props := (&v).Clone()
		cloned.Notifications[k] = *props
	}
	if c.Metadata != nil {
		cloned.Metadata = c.Metadata.Clone()
	}
	return &cloned
}

// SetID changes the client qualified identifier
func (c *Client) SetID(id string) { c.CouchID = id }

// SetRev changes the client revision
func (c *Client) SetRev(rev string) { c.CouchRev = rev }

// TransformIDAndRev makes the translation from the JSON of CouchDB to the
// one used in the dynamic client registration protocol
func (c *Client) TransformIDAndRev() {
	c.ClientID = c.CouchID
	c.CouchID = ""
	c.CouchRev = ""
}

// GetAll loads all the clients from the database, without the secret
func GetAll(inst *instance.Instance, limit int, bookmark string) ([]*Client, string, error) {
	res, err := couchdb.NormalDocs(inst, consts.OAuthClients, 0, limit, bookmark, false)
	if err != nil {
		return nil, "", err
	}
	clients := make([]*Client, len(res.Rows))
	for i, row := range res.Rows {
		var client Client
		if err := json.Unmarshal(row, &client); err != nil {
			return nil, "", err
		}
		client.ClientSecret = ""
		clients[i] = &client
	}
	return clients, res.Bookmark, nil
}

// GetNotifiables loads al the clients from the database containing a non-empty
// `notification_plaform` field.
func GetNotifiables(i *instance.Instance) ([]*Client, error) {
	var clients []*Client
	req := &couchdb.FindRequest{
		UseIndex: "by-notification-platform",
		Selector: mango.And(
			mango.Exists("notification_platform"),
			mango.Exists("notification_device_token"),
		),
	}
	err := couchdb.FindDocs(i, consts.OAuthClients, req, &clients)
	if err != nil {
		return nil, err
	}
	return clients, nil
}

// FindClient loads a client from the database
func FindClient(i *instance.Instance, id string) (*Client, error) {
	var c Client
	if err := couchdb.GetDoc(i, consts.OAuthClients, id, &c); err != nil {
		return nil, err
	}
	if c.ClientID == "" {
		c.ClientID = c.CouchID
	}
	return &c, nil
}

// FindClientBySoftwareID loads a client from the database
func FindClientBySoftwareID(i *instance.Instance, softwareID string) (*Client, error) {
	var results []*Client

	req := couchdb.FindRequest{
		Selector: mango.Equal("software_id", softwareID),
		Limit:    1,
	}
	// We should have very few requests. Only on instance creation.
	err := couchdb.FindDocsUnoptimized(i, consts.OAuthClients, &req, &results)
	if err != nil {
		return nil, err
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return nil, fmt.Errorf("Could not find client with software_id %s", softwareID)
}

// FindClientByOnBoardingSecret loads a client from the database with an OnboardingSecret
func FindClientByOnBoardingSecret(i *instance.Instance, onboardingSecret string) (*Client, error) {
	var results []*Client

	req := couchdb.FindRequest{
		Selector: mango.Equal("onboarding_secret", onboardingSecret),
		Limit:    1,
	}
	// We should have very few requests. Only on instance creation.
	err := couchdb.FindDocsUnoptimized(i, consts.OAuthClients, &req, &results)
	if err != nil {
		return nil, err
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return nil, fmt.Errorf("Could not find client with onboarding_secret %s", onboardingSecret)
}

// FindOnboardingClient loads a client from the database with an OnboardingSecret
func FindOnboardingClient(i *instance.Instance) (*Client, error) {
	var results []*Client

	req := couchdb.FindRequest{
		Selector: mango.Exists("onboarding_secret"),
		Limit:    1,
	}
	// We should have very few requests. Only on instance creation.
	err := couchdb.FindDocsUnoptimized(i, consts.OAuthClients, &req, &results)
	if err != nil {
		return nil, err
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return nil, fmt.Errorf("Could not find client with an onboarding_secret")
}

// ClientRegistrationError is a Client Registration Error Response, as described
// in the Client Dynamic Registration Protocol
// See https://tools.ietf.org/html/rfc7591#section-3.2.2 for errors
type ClientRegistrationError struct {
	Code        int    `json:"-"`
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

func (c *Client) checkMandatoryFields(i *instance.Instance) *ClientRegistrationError {
	if len(c.RedirectURIs) == 0 {
		return &ClientRegistrationError{
			Code:        http.StatusBadRequest,
			Error:       "invalid_redirect_uri",
			Description: "redirect_uris is mandatory",
		}
	}
	for _, redirectURI := range c.RedirectURIs {
		u, err := url.Parse(redirectURI)
		if err != nil ||
			u.Host == i.Domain ||
			u.Fragment != "" {
			return &ClientRegistrationError{
				Code:        http.StatusBadRequest,
				Error:       "invalid_redirect_uri",
				Description: fmt.Sprintf("%s is invalid", redirectURI),
			}
		}
	}
	if c.ClientName == "" {
		return &ClientRegistrationError{
			Code:        http.StatusBadRequest,
			Error:       "invalid_client_metadata",
			Description: "client_name is mandatory",
		}
	}
	if c.SoftwareID == "" {
		return &ClientRegistrationError{
			Code:        http.StatusBadRequest,
			Error:       "invalid_client_metadata",
			Description: "software_id is mandatory",
		}
	}
	c.NotificationPlatform = strings.ToLower(c.NotificationPlatform)
	switch c.NotificationPlatform {
	case "", PlatformFirebase, PlatformAPNS, PlatformHuawei:
	case "ios", "android": // retro-compatibility
	default:
		return &ClientRegistrationError{
			Code:  http.StatusBadRequest,
			Error: "invalid_client_metadata",
		}
	}
	return nil
}

// CheckSoftwareID checks if a SoftwareID is valid
func (c *Client) CheckSoftwareID(instance *instance.Instance) *ClientRegistrationError {
	if strings.HasPrefix(c.SoftwareID, "registry://") {
		appSlug := strings.TrimPrefix(c.SoftwareID, "registry://")
		if appSlug == consts.StoreSlug || appSlug == consts.SettingsSlug {
			return &ClientRegistrationError{
				Code:        http.StatusBadRequest,
				Error:       "unapproved_software_id",
				Description: "Link with store/settings is forbidden",
			}
		}
		_, err := registry.GetApplication(appSlug, instance.Registries())
		if err != nil {
			return &ClientRegistrationError{
				Code:        http.StatusBadRequest,
				Error:       "unapproved_software_id",
				Description: "Application was not found on instance registries",
			}
		}
	}
	return nil
}

// CreateOptions can be used to give options when creating an OAuth client
type CreateOptions int

const (
	// NotPending option won't set the pending flag, and will avoid creating a
	// trigger to check if the client should be cleaned. It is used for
	// sharings by example, as a token is created just after the client
	// creation.
	NotPending CreateOptions = iota + 1
)

func hasOptions(needle CreateOptions, haystack []CreateOptions) bool {
	for _, opt := range haystack {
		if opt == needle {
			return true
		}
	}
	return false
}

// Create is a function that sets some fields, and then save it in Couch.
func (c *Client) Create(i *instance.Instance, opts ...CreateOptions) *ClientRegistrationError {
	if err := c.checkMandatoryFields(i); err != nil {
		return err
	}
	if err := c.CheckSoftwareID(i); err != nil {
		return err
	}

	var results []*Client
	req := &couchdb.FindRequest{
		UseIndex: "by-client-name",
		Selector: mango.StartWith("client_name", c.ClientName),
	}
	err := couchdb.FindDocs(i, consts.OAuthClients, req, &results)
	if err != nil && !couchdb.IsNoDatabaseError(err) {
		return &ClientRegistrationError{
			Code:  http.StatusInternalServerError,
			Error: "internal_server_error",
		}
	}

	// Find the correct suffix to apply to the client name in case it is already
	// used.
	suffix := ""
	if len(results) > 0 {
		n := 1
		found := false
		prefix := c.ClientName + "-"
		for _, r := range results {
			name := r.ClientName
			if name == c.ClientName {
				found = true
				continue
			}
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			var m int
			m, err = strconv.Atoi(name[len(prefix):])
			if err == nil && m > n {
				n = m
			}
		}
		if found {
			suffix = strconv.Itoa(n + 1)
		}
	}
	if suffix != "" {
		c.ClientName = c.ClientName + "-" + suffix
	}

	if !hasOptions(NotPending, opts) {
		c.Pending = true
	}
	c.CouchID = ""
	c.CouchRev = ""
	c.ClientID = ""
	secret := crypto.GenerateRandomBytes(ClientSecretLen)
	c.ClientSecret = string(crypto.Base64Encode(secret))
	c.SecretExpiresAt = 0
	c.RegistrationToken = ""
	c.GrantTypes = []string{"authorization_code", "refresh_token"}
	c.ResponseTypes = []string{"code"}

	// Adding Metadata
	md := metadata.New()
	if strings.HasPrefix(c.SoftwareID, "registry://") {
		md.CreatedByApp = strings.TrimPrefix(c.SoftwareID, "registry://")
		md.CreatedByAppVersion = c.SoftwareVersion
	}
	md.DocTypeVersion = DocTypeVersion
	c.Metadata = md

	if err = couchdb.CreateDoc(i, c); err != nil {
		return &ClientRegistrationError{
			Code:  http.StatusInternalServerError,
			Error: "internal_server_error",
		}
	}

	if !hasOptions(NotPending, opts) {
		if err := setupTrigger(i, c.CouchID); err != nil {
			i.Logger().WithNamespace("oauth").
				Warnf("Cannot create trigger: %s", err)
		}
	}

	c.RegistrationToken, err = crypto.NewJWT(i.OAuthSecret, crypto.StandardClaims{
		Audience: consts.RegistrationTokenAudience,
		Issuer:   i.Domain,
		IssuedAt: time.Now().Unix(),
		Subject:  c.CouchID,
	})
	if err != nil {
		i.Logger().WithNamespace("oauth").
			Errorf("Failed to create the registration access token: %s", err)
		return &ClientRegistrationError{
			Code:  http.StatusInternalServerError,
			Error: "internal_server_error",
		}
	}

	c.TransformIDAndRev()
	return nil
}

func setupTrigger(inst *instance.Instance, clientID string) error {
	sched := job.System()
	msg := &CleanMessage{ClientID: clientID}
	t, err := job.NewTrigger(inst, job.TriggerInfos{
		Type:       "@in",
		WorkerType: "clean-clients",
		Arguments:  "1h",
	}, msg)
	if err != nil {
		return err
	}
	return sched.AddTrigger(t)
}

// Update will update the client metadata
func (c *Client) Update(i *instance.Instance, old *Client) *ClientRegistrationError {
	if c.ClientID != old.ClientID {
		return &ClientRegistrationError{
			Code:        http.StatusBadRequest,
			Error:       "invalid_client_id",
			Description: "client_id is mandatory",
		}
	}

	if err := c.checkMandatoryFields(i); err != nil {
		return err
	}

	switch c.ClientSecret {
	case "":
		c.ClientSecret = old.ClientSecret
	case old.ClientSecret:
		secret := crypto.GenerateRandomBytes(ClientSecretLen)
		c.ClientSecret = string(crypto.Base64Encode(secret))
	default:
		return &ClientRegistrationError{
			Code:        http.StatusBadRequest,
			Error:       "invalid_client_secret",
			Description: "client_secret is invalid",
		}
	}

	c.CouchID = old.CouchID
	c.CouchRev = old.CouchRev
	c.ClientName = old.ClientName
	c.ClientID = ""
	c.SecretExpiresAt = 0
	c.RegistrationToken = ""
	c.GrantTypes = []string{"authorization_code", "refresh_token"}
	c.ResponseTypes = []string{"code"}
	c.AllowLoginScope = old.AllowLoginScope
	c.OnboardingSecret = ""
	c.OnboardingApp = ""
	c.OnboardingPermissions = ""
	c.OnboardingState = ""
	if c.NotificationPlatform == "" {
		c.NotificationPlatform = old.NotificationPlatform
	}
	if c.NotificationDeviceToken == "" {
		c.NotificationDeviceToken = old.NotificationDeviceToken
	}

	// Updating metadata
	md := metadata.New()
	if strings.HasPrefix(c.SoftwareID, "registry://") {
		md.CreatedByApp = strings.TrimPrefix(c.SoftwareID, "registry://")
		md.CreatedByAppVersion = c.SoftwareVersion
	}
	md.DocTypeVersion = DocTypeVersion

	if old.Metadata == nil {
		c.Metadata = md
	} else {
		c.Metadata = old.Metadata
		c.Metadata.ChangeUpdatedAt()
	}

	if err := couchdb.UpdateDoc(i, c); err != nil {
		return &ClientRegistrationError{
			Code:  http.StatusInternalServerError,
			Error: "internal_server_error",
		}
	}

	c.TransformIDAndRev()
	return nil
}

// Delete is a function that unregister a client
func (c *Client) Delete(i *instance.Instance) *ClientRegistrationError {
	if err := couchdb.DeleteDoc(i, c); err != nil {
		return &ClientRegistrationError{
			Code:  http.StatusInternalServerError,
			Error: "internal_server_error",
		}
	}
	return nil
}

func (c *Client) CreateChallenge(inst *instance.Instance) (string, error) {
	nonce := crypto.GenerateRandomString(ChallengeLen)
	store := GetStore()
	if err := store.SaveChallenge(inst, c.ID(), nonce); err != nil {
		return "", err
	}
	inst.Logger().Debugf("OAuth client %s has requested a challenge: %s", c.ID(), nonce)
	return nonce, nil
}

// AcceptRedirectURI returns true if the given URI matches the registered
// redirect_uris
func (c *Client) AcceptRedirectURI(u string) bool {
	for _, uri := range c.RedirectURIs {
		if u == uri {
			return true
		}
	}
	return false
}

// CreateJWT returns a new JSON Web Token for the given instance and audience
func (c *Client) CreateJWT(i *instance.Instance, audience, scope string) (string, error) {
	token, err := crypto.NewJWT(i.OAuthSecret, permission.Claims{
		StandardClaims: crypto.StandardClaims{
			Audience: audience,
			Issuer:   i.Domain,
			IssuedAt: crypto.Timestamp(),
			Subject:  c.CouchID,
		},
		Scope: scope,
	})
	if err != nil {
		i.Logger().WithNamespace("oauth").
			Errorf("Failed to create the %s token: %s", audience, err)
	}
	return token, err
}

func validToken(i *instance.Instance, audience, token string) (permission.Claims, bool) {
	claims := permission.Claims{}
	if token == "" {
		return claims, false
	}
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		return i.OAuthSecret, nil
	}
	if err := crypto.ParseJWT(token, keyFunc, &claims); err != nil {
		i.Logger().WithNamespace("oauth").
			Errorf("Failed to verify the %s token: %s", audience, err)
		return claims, false
	}
	if claims.Expired() {
		i.Logger().WithNamespace("oauth").
			Errorf("Failed to verify the %s token: expired", audience)
		return claims, false
	}
	// Note: the refresh and registration tokens don't expire, no need to check its issue date
	if claims.Audience != audience {
		i.Logger().WithNamespace("oauth").
			Errorf("Unexpected audience for %s token: %s", audience, claims.Audience)
		return claims, false
	}
	if claims.Issuer != i.Domain {
		i.Logger().WithNamespace("oauth").
			Errorf("Expected %s issuer for %s token, but was: %s", audience, i.Domain, claims.Issuer)
		return claims, false
	}
	return claims, true
}

// ValidTokenWithSStamp checks that the JWT is valid and returns the associate
// claims. You should use client.ValidToken if you know the client, as it also
// checks that the claims are associated to this client.
func ValidTokenWithSStamp(i *instance.Instance, audience, token string) (permission.Claims, bool) {
	claims, valid := validToken(i, audience, token)
	if !valid {
		return claims, valid
	}
	settings, err := settings.Get(i)
	if err != nil {
		i.Logger().WithNamespace("oauth").
			Errorf("Error while getting bitwarden settings: %s", err)
		return claims, false
	}
	if claims.SStamp != settings.SecurityStamp {
		i.Logger().WithNamespace("oauth").
			Errorf("Expected %s security stamp for %s token, but was: %s",
				settings.SecurityStamp, claims.Subject, claims.SStamp)
		return claims, false
	}
	return claims, true
}

// ValidToken checks that the JWT is valid and returns the associate claims.
// It is expected to be used for registration token and refresh token, and
// it doesn't check when they were issued as they don't expire.
func (c *Client) ValidToken(i *instance.Instance, audience, token string) (permission.Claims, bool) {
	claims, valid := validToken(i, audience, token)
	if !valid {
		return claims, valid
	}
	if claims.Subject != c.CouchID {
		i.Logger().WithNamespace("oauth").
			Errorf("Expected %s subject for %s token, but was: %s", audience, c.CouchID, claims.Subject)
		return claims, false
	}
	return claims, true
}

// IsLinkedApp checks if an OAuth client has a linked app
func IsLinkedApp(softwareID string) bool {
	return strings.HasPrefix(softwareID, "registry://")
}

// GetLinkedAppSlug returns a linked app slug from a softwareID
func GetLinkedAppSlug(softwareID string) string {
	if !IsLinkedApp(softwareID) {
		return ""
	}
	return strings.TrimPrefix(softwareID, "registry://")
}

// BuildLinkedAppScope returns a formatted scope for a linked app
func BuildLinkedAppScope(slug string) string {
	return fmt.Sprintf("@%s/%s", consts.Apps, slug)
}

var _ couchdb.Doc = &Client{}
