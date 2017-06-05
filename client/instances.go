package client

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/client/request"
)

// Instance is a struct holding the representation of an instance on the API.
type Instance struct {
	ID    string `json:"id"`
	Rev   string `json:"rev"`
	Attrs struct {
		Domain            string `json:"domain"`
		Locale            string `json:"locale"`
		Dev               bool   `json:"dev"`
		BytesDiskQuota    int64  `json:"disk_quota,string,omitempty"`
		IndexViewsVersion int    `json:"indexes_version"`
		PassphraseHash    []byte `json:"passphrase_hash,omitempty"`
		RegisterToken     []byte `json:"register_token,omitempty"`
	} `json:"attributes"`
}

// InstanceOptions contains the options passed on instance creation.
type InstanceOptions struct {
	Domain     string
	Locale     string
	Timezone   string
	Email      string
	PublicName string
	Settings   string
	DiskQuota  int64
	Apps       []string
	Dev        bool
	Passphrase string
}

// TokenOptions is a struct holding all the options to generate a token.
type TokenOptions struct {
	Domain   string
	Subject  string
	Audience string
	Scope    []string
	Expire   time.Duration
}

// OAuthClientOptions is a struct holding all the options to generate an OAuth
// client associated to an instance.
type OAuthClientOptions struct {
	Domain      string
	RedirectURI string
	ClientName  string
	SoftwareID  string
}

// GetInstance returns the instance associated with the specified domain.
func (c *Client) GetInstance(domain string) (*Instance, error) {
	res, err := c.Req(&request.Options{
		Method: "GET",
		Path:   "/instances/" + domain,
	})
	if err != nil {
		return nil, err
	}
	return readInstance(res)
}

// CreateInstance is used to create a new cozy instance of the specified domain
// and locale.
func (c *Client) CreateInstance(opts *InstanceOptions) (*Instance, error) {
	if !validDomain(opts.Domain) {
		return nil, fmt.Errorf("Invalid domain: %s", opts.Domain)
	}
	res, err := c.Req(&request.Options{
		Method: "POST",
		Path:   "/instances",
		Queries: url.Values{
			"Domain":     {opts.Domain},
			"Locale":     {opts.Locale},
			"Timezone":   {opts.Timezone},
			"Email":      {opts.Email},
			"PublicName": {opts.PublicName},
			"Settings":   {opts.Settings},
			"DiskQuota":  {strconv.FormatInt(opts.DiskQuota, 10)},
			"Apps":       {strings.Join(opts.Apps, ",")},
			"Dev":        {strconv.FormatBool(opts.Dev)},
			"Passphrase": {opts.Passphrase},
		},
	})
	if err != nil {
		return nil, err
	}
	return readInstance(res)
}

// ListInstances returns the list of instances recorded on the stack.
func (c *Client) ListInstances() ([]*Instance, error) {
	res, err := c.Req(&request.Options{
		Method: "GET",
		Path:   "/instances",
	})
	if err != nil {
		return nil, err
	}
	var list []*Instance
	if err = readJSONAPI(res.Body, &list, nil); err != nil {
		return nil, err
	}
	return list, nil
}

// ModifyInstance is used to update an instance.
func (c *Client) ModifyInstance(domain string, opts *InstanceOptions) (*Instance, error) {
	if !validDomain(domain) {
		return nil, fmt.Errorf("Invalid domain: %s", domain)
	}
	res, err := c.Req(&request.Options{
		Method: "PATCH",
		Path:   "/instances/" + domain,
		Queries: url.Values{
			"Locale":    {opts.Locale},
			"DiskQuota": {strconv.FormatInt(opts.DiskQuota, 10)},
		},
	})
	if err != nil {
		return nil, err
	}
	return readInstance(res)
}

// DestroyInstance is used to delete an instance and all its data.
func (c *Client) DestroyInstance(domain string) error {
	if !validDomain(domain) {
		return fmt.Errorf("Invalid domain: %s", domain)
	}
	_, err := c.Req(&request.Options{
		Method:     "DELETE",
		Path:       "/instances/" + domain,
		NoResponse: true,
	})
	return err
}

// GetToken is used to generate a toke with the specified options.
func (c *Client) GetToken(opts *TokenOptions) (string, error) {
	q := url.Values{
		"Domain":   {opts.Domain},
		"Subject":  {opts.Subject},
		"Audience": {opts.Audience},
		"Scope":    {strings.Join(opts.Scope, " ")},
		"Expire":   {opts.Expire.String()},
	}
	res, err := c.Req(&request.Options{
		Method:  "POST",
		Path:    "/instances/token",
		Queries: q,
	})
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RegisterOAuthClient register a new OAuth client associated to the specified
// instance.
func (c *Client) RegisterOAuthClient(opts *OAuthClientOptions) (string, error) {
	q := url.Values{
		"Domain":      {opts.Domain},
		"RedirectURI": {opts.RedirectURI},
		"ClientName":  {opts.ClientName},
		"SoftwareID":  {opts.SoftwareID},
	}
	res, err := c.Req(&request.Options{
		Method:  "POST",
		Path:    "/instances/oauth_client",
		Queries: q,
	})
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readInstance(res *http.Response) (*Instance, error) {
	in := &Instance{}
	if err := readJSONAPI(res.Body, &in, nil); err != nil {
		return nil, err
	}
	return in, nil
}

func validDomain(domain string) bool {
	return !strings.ContainsAny(domain, " /?#@\t\r\n")
}
