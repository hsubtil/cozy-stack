package sharing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cozy/cozy-stack/client/auth"
	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/logger"
	"github.com/cozy/cozy-stack/pkg/oauth"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/web/jsonapi"
)

// RegisterClient asks the Cozy of the member to register a new OAuth client
func (m *Member) RegisterClient(inst *instance.Instance, u *url.URL) (*auth.Client, error) {
	req := &auth.Request{
		Domain: u.Host,
		Scheme: u.Scheme,
	}

	publicName, _ := inst.PublicName()
	if publicName == "" {
		publicName = inst.Domain
	}
	redirectURI := inst.PageURL("/sharings/answer", nil)
	clientURI := inst.PageURL("", nil)
	authClient := &auth.Client{
		RedirectURIs: []string{redirectURI},
		ClientName:   publicName,
		ClientKind:   "sharing",
		SoftwareID:   "github.com/cozy/cozy-stack",
		ClientURI:    clientURI,
	}

	resClient, err := req.RegisterClient(authClient)
	if err != nil {
		return nil, err
	}
	m.Instance = u.String()
	return resClient, nil
}

// CreateSharingRequest sends information about the sharing to the recipient's cozy
func (m *Member) CreateSharingRequest(inst *instance.Instance, s *Sharing, u *url.URL) error {
	// TODO translate ids of files/folders in the rules sent to the recipients
	sh := APISharing{
		&Sharing{
			SID:         s.SID,
			Active:      false,
			Owner:       false,
			Open:        s.Open,
			Description: s.Description,
			AppSlug:     s.AppSlug,
			PreviewPath: s.PreviewPath,
			CreatedAt:   s.CreatedAt,
			UpdatedAt:   s.UpdatedAt,
			Rules:       s.Rules,
			Members:     s.Members,
		},
		nil,
	}
	data, err := jsonapi.MarshalObject(&sh)
	if err != nil {
		return err
	}
	body, err := json.Marshal(jsonapi.Document{Data: &data})
	if err != nil {
		return err
	}
	res, err := request.Req(&request.Options{
		Method: http.MethodPut,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID,
		Headers: request.Headers{
			"Accept":       "application/vnd.api+json",
			"Content-Type": "application/vnd.api+json",
		},
		Body: bytes.NewReader(body),
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode/100 != 2 {
		return ErrRequestFailed
	}

	return nil
}

// RegisterCozyURL saves a new Cozy URL for a member
func (s *Sharing) RegisterCozyURL(inst *instance.Instance, m *Member, u *url.URL) error {
	if u.Host == "" {
		return ErrInvalidURL
	}
	if u.Scheme == "" {
		u.Scheme = "https" // Set https as the default scheme
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	if !s.Owner {
		return ErrInvalidSharing
	}
	if len(s.Members) != len(s.Credentials)+1 {
		return ErrInvalidSharing
	}
	var creds *Credentials
	for i, member := range s.Members {
		if *m == member {
			creds = &s.Credentials[i-1]
		}
	}
	if creds == nil {
		return ErrInvalidSharing
	}

	client, err := m.RegisterClient(inst, u)
	if err != nil {
		logger.WithDomain(inst.Domain).Warnf("[sharing] Error on OAuth client registration: %s", err)
		return ErrInvalidURL
	}
	creds.Client = client

	if err = m.CreateSharingRequest(inst, s, u); err != nil {
		logger.WithDomain(inst.Domain).Warnf("[sharing] Error on sharing request: %s", err)
		return ErrRequestFailed
	}
	return couchdb.UpdateDoc(inst, s)
}

// GenerateOAuthURL takes care of creating a correct OAuth request for
// the given member of the sharing.
func (m *Member) GenerateOAuthURL(s *Sharing) (string, error) {
	if !s.Owner {
		return "", ErrInvalidSharing
	}
	if len(s.Members) != len(s.Credentials)+1 {
		return "", ErrInvalidSharing
	}
	var creds *Credentials
	for i, member := range s.Members {
		if *m == member {
			creds = &s.Credentials[i-1]
		}
	}
	if creds == nil {
		return "", ErrInvalidSharing
	}
	if m.Instance == "" || creds.Client.ClientID == "" {
		return "", ErrNoOAuthClient
	}

	u, err := url.Parse(m.Instance)
	if err != nil {
		return "", err
	}
	u.Path = "/auth/authorize/sharing"

	q := url.Values{
		"sharing_id": {s.SID},
		"client_id":  {creds.Client.ClientID},
		"state":      {creds.State},
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// Create an OAuth client for a recipient of the given sharing
func createOAuthClient(inst *instance.Instance, m *Member) (*oauth.Client, error) {
	if m.Instance == "" {
		return nil, ErrInvalidURL
	}
	cli := oauth.Client{
		RedirectURIs: []string{m.Instance + "/sharings/answer"},
		ClientName:   "Sharing " + m.Name,
		ClientKind:   "sharing",
		SoftwareID:   "github.com/cozy/cozy-stack",
		ClientURI:    m.Instance + "/",
	}
	if err := cli.Create(inst); err != nil {
		return nil, ErrInternalServerError
	}
	return &cli, nil
}

// Convert an OAuth client from one type (pkg/oauth.Client) to another
// (client/auth.Client)
func convertOAuthClient(c *oauth.Client) *auth.Client {
	return &auth.Client{
		ClientID:          c.ClientID,
		ClientSecret:      c.ClientSecret,
		SecretExpiresAt:   c.SecretExpiresAt,
		RegistrationToken: c.RegistrationToken,
		RedirectURIs:      c.RedirectURIs,
		ClientName:        c.ClientName,
		ClientKind:        c.ClientKind,
		ClientURI:         c.ClientURI,
		LogoURI:           c.LogoURI,
		PolicyURI:         c.PolicyURI,
		SoftwareID:        c.SoftwareID,
		SoftwareVersion:   c.SoftwareVersion,
	}
}

// ProcessAnswer takes somes credentials and update the sharing with those.
func (s *Sharing) ProcessAnswer(inst *instance.Instance, creds *Credentials) (*APICredentials, error) {
	if !s.Owner || len(s.Members) != len(s.Credentials)+1 {
		return nil, ErrInvalidSharing
	}
	for i, c := range s.Credentials {
		if c.State == creds.State {
			c.AccessToken = creds.AccessToken
			if err := couchdb.UpdateDoc(inst, s); err != nil {
				return nil, err
			}
			ac := APICredentials{CID: s.SID, Credentials: &Credentials{}}
			if !s.ReadOnly() {
				cli, err := createOAuthClient(inst, &s.Members[i+1])
				if err != nil {
					return &ac, nil
				}
				ac.Credentials.Client = convertOAuthClient(cli)
				scope := consts.Sharings + ":" + s.SID
				refresh, err := cli.CreateJWT(inst, permissions.RefreshTokenAudience, scope)
				if err != nil {
					return &ac, nil
				}
				access, err := cli.CreateJWT(inst, permissions.AccessTokenAudience, scope)
				if err != nil {
					return &ac, nil
				}
				ac.Credentials.AccessToken = &auth.AccessToken{
					TokenType:    "bearer",
					AccessToken:  access,
					RefreshToken: refresh,
					Scope:        scope,
				}
			}
			return &ac, nil
		}
	}
	return nil, ErrMemberNotFound
}
