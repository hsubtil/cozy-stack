package sharing

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cozy/cozy-stack/client/auth"
	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/oauth"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/web/jsonapi"
)

// CreateSharingRequest sends information about the sharing to the recipient's cozy
func (m *Member) CreateSharingRequest(inst *instance.Instance, s *Sharing, c *Credentials, u *url.URL) error {
	rules := make([]Rule, 0, len(s.Rules))
	for _, rule := range s.Rules {
		if rule.Local {
			continue
		}
		if rule.FilesByID() {
			values := make([]string, len(rule.Values))
			for i, v := range rule.Values {
				values[i] = XorID(v, c.XorKey)
			}
			rule.Values = values
		}
		rules = append(rules, rule)
	}
	members := make([]Member, len(s.Members))
	for i, m := range s.Members {
		// Instance and name are private...
		members[i] = Member{
			Status:     m.Status,
			PublicName: m.PublicName,
			Email:      m.Email,
		}
		// ... except for the sharer and the recipient of this request
		if i == 0 || &s.Credentials[i-1] == c {
			members[i].Instance = m.Instance
		}
	}
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
			Rules:       rules,
			Members:     members,
		},
		nil,
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
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return ErrRequestFailed
	}
	return nil
}

// RegisterCozyURL saves a new Cozy URL for a member
func (s *Sharing) RegisterCozyURL(inst *instance.Instance, m *Member, cozyURL string) error {
	if !s.Owner {
		return ErrInvalidSharing
	}

	cozyURL = strings.TrimSpace(cozyURL)
	if !strings.Contains(cozyURL, "://") {
		cozyURL = "https://" + cozyURL
	}
	u, err := url.Parse(cozyURL)
	if err != nil || u.Host == "" {
		return ErrInvalidURL
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	m.Instance = u.String()

	creds := s.FindCredentials(m)
	if creds == nil {
		return ErrInvalidSharing
	}
	if err = m.CreateSharingRequest(inst, s, creds, u); err != nil {
		inst.Logger().WithField("nspace", "sharing").Warnf("Error on sharing request: %s", err)
		return ErrRequestFailed
	}
	return couchdb.UpdateDoc(inst, s)
}

// GenerateOAuthURL takes care of creating a correct OAuth request for
// the given member of the sharing.
func (m *Member) GenerateOAuthURL(s *Sharing) (string, error) {
	if !s.Owner || len(s.Members) != len(s.Credentials)+1 {
		return "", ErrInvalidSharing
	}
	creds := s.FindCredentials(m)
	if creds == nil {
		return "", ErrInvalidSharing
	}
	if m.Instance == "" {
		return "", ErrNoOAuthClient
	}

	u, err := url.Parse(m.Instance)
	if err != nil {
		return "", err
	}
	u.Path = "/auth/authorize/sharing"

	q := url.Values{
		"sharing_id": {s.SID},
		"state":      {creds.State},
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// CreateOAuthClient creates an OAuth client for a recipient of the given sharing
func CreateOAuthClient(inst *instance.Instance, m *Member) (*oauth.Client, error) {
	if m.Instance == "" {
		return nil, ErrInvalidURL
	}
	cli := oauth.Client{
		RedirectURIs: []string{m.Instance + "/sharings/answer"},
		ClientName:   "Sharing " + m.PublicName,
		ClientKind:   "sharing",
		SoftwareID:   "github.com/cozy/cozy-stack",
		ClientURI:    m.Instance + "/",
	}
	if err := cli.Create(inst); err != nil {
		return nil, ErrInternalServerError
	}
	return &cli, nil
}

// DeleteOAuthClient removes the client associated to the given member
func DeleteOAuthClient(inst *instance.Instance, m *Member, cred *Credentials) error {
	if m.Instance == "" {
		return ErrInvalidURL
	}
	clientID := cred.InboundClientID
	if clientID == "" {
		return nil
	}
	client, err := oauth.FindClient(inst, clientID)
	if err != nil {
		return err
	}
	if cerr := client.Delete(inst); cerr != nil {
		return errors.New(cerr.Error)
	}
	return nil
}

// ConvertOAuthClient converts an OAuth client from one type (pkg/oauth.Client)
// to another (client/auth.Client)
func ConvertOAuthClient(c *oauth.Client) *auth.Client {
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

// CreateAccessToken creates an access token for the given OAuth client,
// with a scope on this sharing.
func CreateAccessToken(inst *instance.Instance, cli *oauth.Client, sharingID string, verb permissions.VerbSet) (*auth.AccessToken, error) {
	scope := consts.Sharings + ":" + verb.String() + ":" + sharingID
	cli.CouchID = cli.ClientID // XXX CouchID is required by CreateJWT
	refresh, err := cli.CreateJWT(inst, permissions.RefreshTokenAudience, scope)
	if err != nil {
		return nil, err
	}
	access, err := cli.CreateJWT(inst, permissions.AccessTokenAudience, scope)
	if err != nil {
		return nil, err
	}
	return &auth.AccessToken{
		TokenType:    "bearer",
		AccessToken:  access,
		RefreshToken: refresh,
		Scope:        scope,
	}, nil
}

// SendAnswer says to the sharer's Cozy that the sharing has been accepted, and
// materialize that by an exchange of credentials.
func (s *Sharing) SendAnswer(inst *instance.Instance, state string) error {
	if s.Owner || len(s.Members) < 2 || len(s.Credentials) != 1 {
		return ErrInvalidSharing
	}
	u, err := url.Parse(s.Members[0].Instance)
	if s.Members[0].Instance == "" || err != nil {
		return ErrInvalidSharing
	}
	cli, err := CreateOAuthClient(inst, &s.Members[0])
	if err != nil {
		return err
	}
	token, err := CreateAccessToken(inst, cli, s.SID, permissions.ALL)
	if err != nil {
		return err
	}
	name, err := inst.PublicName()
	if err != nil {
		inst.Logger().WithField("nspace", "sharing").
			Infof("No name for instance %v", inst)
	}
	ac := APICredentials{
		Credentials: &Credentials{
			State:       state,
			Client:      ConvertOAuthClient(cli),
			AccessToken: token,
		},
		PublicName: name,
		CID:        s.SID,
	}
	data, err := jsonapi.MarshalObject(&ac)
	if err != nil {
		return err
	}
	body, err := json.Marshal(jsonapi.Document{Data: &data})
	if err != nil {
		return err
	}
	res, err := request.Req(&request.Options{
		Method: http.MethodPost,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/answer",
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

	for i, m := range s.Members {
		if i > 0 && m.Instance != "" {
			if m.Status == MemberStatusMailNotSent ||
				m.Status == MemberStatusPendingInvitation {
				m.Status = MemberStatusReady
			}
		}
	}

	if err = s.SetupReceiver(inst); err != nil {
		return err
	}

	var creds Credentials
	if _, err = jsonapi.Bind(res.Body, &creds); err != nil {
		return ErrRequestFailed
	}
	s.Credentials[0].XorKey = creds.XorKey
	s.Credentials[0].InboundClientID = cli.ClientID
	s.Credentials[0].AccessToken = creds.AccessToken
	s.Credentials[0].Client = creds.Client
	s.Active = true
	return couchdb.UpdateDoc(inst, s)
}

// ProcessAnswer takes somes credentials and update the sharing with those.
func (s *Sharing) ProcessAnswer(inst *instance.Instance, creds *APICredentials) (*APICredentials, error) {
	if !s.Owner || len(s.Members) != len(s.Credentials)+1 {
		return nil, ErrInvalidSharing
	}
	for i, c := range s.Credentials {
		if c.State == creds.State {
			s.Members[i+1].Status = MemberStatusReady
			s.Members[i+1].PublicName = creds.PublicName
			s.Credentials[i].Client = creds.Client
			s.Credentials[i].AccessToken = creds.AccessToken
			ac := APICredentials{
				CID: s.SID,
				Credentials: &Credentials{
					XorKey: c.XorKey,
				},
			}
			// Create the credentials for the recipient
			cli, err := CreateOAuthClient(inst, &s.Members[i+1])
			if err != nil {
				return &ac, nil
			}
			s.Credentials[i].InboundClientID = cli.ClientID
			ac.Credentials.Client = ConvertOAuthClient(cli)
			var verb permissions.VerbSet
			// In case of read-only, the recipient only needs read access on the
			// sharing, e.g. to notify the sharer of a revocation
			if s.ReadOnly() {
				verb = permissions.Verbs(permissions.GET)
			} else {
				verb = permissions.ALL
			}
			token, err := CreateAccessToken(inst, cli, s.SID, verb)
			if err != nil {
				return &ac, nil
			}
			ac.Credentials.AccessToken = token

			s.Active = true
			if err := couchdb.UpdateDoc(inst, s); err != nil {
				return nil, err
			}
			go s.Setup(inst, &s.Members[i+1])
			return &ac, nil
		}
	}
	return nil, ErrMemberNotFound
}

// RefreshToken is used after a failed request with a 4xx error code.
// It renews the access token and retries the request
func RefreshToken(inst *instance.Instance, s *Sharing, m *Member, creds *Credentials, opts *request.Options, body []byte) (*http.Response, error) {
	if err := creds.Refresh(inst, s, m); err != nil {
		return nil, err
	}
	opts.Headers["Authorization"] = creds.AccessToken.AccessToken
	if body != nil {
		opts.Body = bytes.NewReader(body)
	}
	res, err := request.Req(opts)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 == 5 {
		res.Body.Close()
		return nil, ErrInternalServerError
	}
	if res.StatusCode/100 != 2 {
		res.Body.Close()
		return nil, ErrClientError
	}
	return res, nil
}
