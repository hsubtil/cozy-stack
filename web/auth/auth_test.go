// spec package is introduced to avoid circular dependencies since this
// particular test requires to depend on routing directly to expose the API and
// the APP server.
package auth_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"testing"

	app "github.com/cozy/cozy-stack/pkg/apps"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/oauth"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/pkg/sessions"
	"github.com/cozy/cozy-stack/web"
	"github.com/cozy/cozy-stack/web/apps"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo"
	"github.com/stretchr/testify/assert"
	jwt "gopkg.in/dgrijalva/jwt-go.v3"
)

const domain = "cozy.example.net"

var ts *httptest.Server
var testInstance *instance.Instance
var instanceURL *url.URL

// Stupid http.CookieJar which always returns all cookies.
// NOTE golang stdlib uses cookies for the URL (ie the testserver),
// not for the host (ie the instance), so we do it manually
type testJar struct {
	Jar *cookiejar.Jar
}

func (j *testJar) Cookies(u *url.URL) (cookies []*http.Cookie) {
	return j.Jar.Cookies(instanceURL)
}

func (j *testJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.Jar.SetCookies(instanceURL, cookies)
}

var jar *testJar
var client *http.Client
var clientID string
var clientSecret string
var registrationToken string
var altClientID string
var altRegistrationToken string
var csrfToken string
var code string
var refreshToken string

func TestIsLoggedInWhenNotLoggedIn(t *testing.T) {
	content, err := getTestURL()
	assert.NoError(t, err)
	assert.Equal(t, "who_are_you", content)
}

func TestHomeWhenNotLoggedIn(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	if assert.Equal(t, "303 See Other", res.Status) {
		assert.Equal(t, "https://cozy.example.net/auth/login",
			res.Header.Get("Location"))
	}
}

func TestShowLoginPage(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/login", nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "Enter your password to access your Cozy")
}

func TestShowLoginPageWithRedirectBadURL(t *testing.T) {
	req1, _ := http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape(" "), nil)
	req1.Host = domain
	res1, err := client.Do(req1)
	assert.NoError(t, err)
	defer res1.Body.Close()
	assert.Equal(t, "400 Bad Request", res1.Status)
	assert.Equal(t, "text/plain; charset=UTF-8", res1.Header.Get("Content-Type"))

	req2, _ := http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape("foo.bar"), nil)
	req2.Host = domain
	res2, err := client.Do(req2)
	assert.NoError(t, err)
	defer res2.Body.Close()
	assert.Equal(t, "400 Bad Request", res2.Status)
	assert.Equal(t, "text/plain; charset=UTF-8", res2.Header.Get("Content-Type"))

	req3, _ := http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape("ftp://sub."+domain+"/foo"), nil)
	req3.Host = domain
	res3, err := client.Do(req3)
	assert.NoError(t, err)
	defer res3.Body.Close()
	assert.Equal(t, "400 Bad Request", res3.Status)
	assert.Equal(t, "text/plain; charset=UTF-8", res3.Header.Get("Content-Type"))
}

func TestShowLoginPageWithRedirectXSS(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape("https://sub."+domain+"/<script>alert('foo')</script>"), nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.NotContains(t, string(body), "<script>")
	assert.Contains(t, string(body), "%3Cscript%3Ealert%28%27foo%27%29%3C/script%3E")
}

func TestShowLoginPageWithRedirectFragment(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape("https://sub."+domain+"/#myfragment"), nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.NotContains(t, string(body), "myfragment")
	assert.Contains(t, string(body), `<input id="redirect" type="hidden" name="redirect" value="https://sub.cozy.example.net/#" />`)
}

func TestShowLoginPageWithRedirectSuccess(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape("https://sub."+domain+"/foo/bar?query=foo#myfragment"), nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.NotContains(t, string(body), "myfragment")
	assert.Contains(t, string(body), `<input id="redirect" type="hidden" name="redirect" value="https://sub.cozy.example.net/foo/bar?query=foo#" />`)
}

func TestLoginWithBadPassphrase(t *testing.T) {
	res, err := postForm("/auth/login", &url.Values{
		"passphrase": {"Nope"},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "401 Unauthorized", res.Status)
}

func TestLoginWithGoodPassphrase(t *testing.T) {
	res, err := postForm("/auth/login", &url.Values{
		"passphrase": {"MyPassphrase"},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	if assert.Equal(t, "303 See Other", res.Status) {
		assert.Equal(t, "https://files.cozy.example.net/#",
			res.Header.Get("Location"))
		cookies := res.Cookies()
		assert.Len(t, cookies, 1)
		assert.Equal(t, cookies[0].Name, sessions.SessionCookieName)
		assert.NotEmpty(t, cookies[0].Value)
	}
}

func TestLoginWithRedirect(t *testing.T) {
	res1, err := postForm("/auth/login", &url.Values{
		"passphrase": {"MyPassphrase"},
		"redirect":   {"foo.bar"},
	})
	assert.NoError(t, err)
	defer res1.Body.Close()
	assert.Equal(t, "400 Bad Request", res1.Status)

	res2, err := postForm("/auth/login", &url.Values{
		"passphrase": {"MyPassphrase"},
		"redirect":   {"https://sub." + domain + "/#myfragment"},
	})
	assert.NoError(t, err)
	defer res2.Body.Close()
	if assert.Equal(t, "303 See Other", res2.Status) {
		assert.Equal(t, "https://sub.cozy.example.net/#",
			res2.Header.Get("Location"))
	}
}

func TestLoginWithSessionCode(t *testing.T) {
	cfg := config.GetConfig()
	cfg.Subdomains = config.FlatSubdomains
	defer func() { cfg.Subdomains = config.NestedSubdomains }()

	// Logout
	req, _ := http.NewRequest("DELETE", ts.URL+"/auth/login", nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	res.Body.Close()

	// Login
	res, err = postForm("/auth/login", &url.Values{
		"passphrase": {"MyPassphrase"},
		"redirect":   {"https://cozy-app.example.net/private"},
	})
	assert.NoError(t, err)
	res.Body.Close()
	if assert.Equal(t, "303 See Other", res.Status) {
		location, err2 := url.Parse(res.Header.Get("Location"))
		assert.NoError(t, err2)
		assert.Equal(t, "cozy-app.example.net", location.Host)
		assert.Equal(t, "/private", location.Path)
		code2 := location.Query().Get("code")
		assert.Len(t, code2, 22)
	}

	// Already logged-in (GET)
	req, err = http.NewRequest("GET", ts.URL+"/auth/login?redirect="+url.QueryEscape("https://cozy-app.example.net/private"), nil)
	assert.NoError(t, err)
	req.Host = domain
	res, err = client.Do(req)
	assert.NoError(t, err)
	res.Body.Close()
	if assert.Equal(t, "303 See Other", res.Status) {
		location, err2 := url.Parse(res.Header.Get("Location"))
		assert.NoError(t, err2)
		assert.Equal(t, "cozy-app.example.net", location.Host)
		assert.Equal(t, "/private", location.Path)
		code2 := location.Query().Get("code")
		assert.Len(t, code2, 22)
	}

	// Already logged-in (POST)
	res, err = postForm("/auth/login", &url.Values{
		"passphrase": {"MyPassphrase"},
		"redirect":   {"https://cozy-app.example.net/private"},
	})
	assert.NoError(t, err)
	res.Body.Close()
	if assert.Equal(t, "303 See Other", res.Status) {
		location, err2 := url.Parse(res.Header.Get("Location"))
		assert.NoError(t, err2)
		assert.Equal(t, "cozy-app.example.net", location.Host)
		assert.Equal(t, "/private", location.Path)
		code2 := location.Query().Get("code")
		assert.Len(t, code2, 22)
	}
}

func TestIsLoggedInAfterLogin(t *testing.T) {
	content, err := getTestURL()
	assert.NoError(t, err)
	assert.Equal(t, "logged_in", content)
}

func TestHomeWhenLoggedIn(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	if assert.Equal(t, "303 See Other", res.Status) {
		assert.Equal(t, "https://files.cozy.example.net/",
			res.Header.Get("Location"))
	}
}

func TestRegisterClientNotJSON(t *testing.T) {
	res, err := postForm("/auth/register", &url.Values{"foo": {"bar"}})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	res.Body.Close()
}

func TestRegisterClientNoRedirectURI(t *testing.T) {
	res, err := postJSON("/auth/register", echo.Map{
		"client_name": "cozy-test",
		"software_id": "github.com/cozy/cozy-test",
	})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	var body map[string]string
	err = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, err)
	assert.Equal(t, "invalid_redirect_uri", body["error"])
	assert.Equal(t, "redirect_uris is mandatory", body["error_description"])
}

func TestRegisterClientInvalidRedirectURI(t *testing.T) {
	res, err := postJSON("/auth/register", echo.Map{
		"redirect_uris": []string{"http://example.org/foo#bar"},
		"client_name":   "cozy-test",
		"software_id":   "github.com/cozy/cozy-test",
	})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	var body map[string]string
	err = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, err)
	assert.Equal(t, "invalid_redirect_uri", body["error"])
	assert.Equal(t, "http://example.org/foo#bar is invalid", body["error_description"])
}

func TestRegisterClientNoClientName(t *testing.T) {
	res, err := postJSON("/auth/register", echo.Map{
		"redirect_uris": []string{"https://example.org/oauth/callback"},
		"software_id":   "github.com/cozy/cozy-test",
	})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	var body map[string]string
	err = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, err)
	assert.Equal(t, "invalid_client_metadata", body["error"])
	assert.Equal(t, "client_name is mandatory", body["error_description"])
}

func TestRegisterClientNoSoftwareID(t *testing.T) {
	res, err := postJSON("/auth/register", echo.Map{
		"redirect_uris": []string{"https://example.org/oauth/callback"},
		"client_name":   "cozy-test",
	})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	var body map[string]string
	err = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, err)
	assert.Equal(t, "invalid_client_metadata", body["error"])
	assert.Equal(t, "software_id is mandatory", body["error_description"])
}

func TestRegisterClientSuccessWithJustMandatoryFields(t *testing.T) {
	res, err := postJSON("/auth/register", echo.Map{
		"redirect_uris": []string{"https://example.org/oauth/callback"},
		"client_name":   "cozy-test",
		"software_id":   "github.com/cozy/cozy-test",
	})
	assert.NoError(t, err)
	assert.Equal(t, "201 Created", res.Status)
	var client oauth.Client
	err = json.NewDecoder(res.Body).Decode(&client)
	assert.NoError(t, err)
	assert.NotEqual(t, client.ClientID, "")
	assert.NotEqual(t, client.ClientID, "ignored")
	assert.NotEqual(t, client.ClientSecret, "")
	assert.NotEqual(t, client.ClientSecret, "ignored")
	assert.NotEqual(t, client.RegistrationToken, "")
	assert.NotEqual(t, client.RegistrationToken, "ignored")
	assert.Equal(t, client.SecretExpiresAt, 0)
	assert.Equal(t, client.RedirectURIs, []string{"https://example.org/oauth/callback"})
	assert.Equal(t, client.GrantTypes, []string{"authorization_code", "refresh_token"})
	assert.Equal(t, client.ResponseTypes, []string{"code"})
	assert.Equal(t, client.ClientName, "cozy-test")
	assert.Equal(t, client.SoftwareID, "github.com/cozy/cozy-test")
	clientID = client.ClientID
	clientSecret = client.ClientSecret
	registrationToken = client.RegistrationToken
}

func TestRegisterClientSuccessWithAllFields(t *testing.T) {
	res, err := postJSON("/auth/register", echo.Map{
		"_id":                       "ignored",
		"_rev":                      "ignored",
		"client_id":                 "ignored",
		"client_secret":             "ignored",
		"client_secret_expires_at":  42,
		"registration_access_token": "ignored",
		"redirect_uris":             []string{"https://example.org/oauth/callback"},
		"grant_types":               []string{"ignored"},
		"response_types":            []string{"ignored"},
		"client_name":               "cozy-test",
		"client_kind":               "test",
		"client_uri":                "https://github.com/cozy/cozy-test",
		"logo_uri":                  "https://raw.github.com/cozy/cozy-setup/gh-pages/assets/images/happycloud.png",
		"policy_uri":                "https://github/com/cozy/cozy-test/master/policy.md",
		"software_id":               "github.com/cozy/cozy-test",
		"software_version":          "v0.1.2",
	})
	assert.NoError(t, err)
	assert.Equal(t, "201 Created", res.Status)
	var client oauth.Client
	err = json.NewDecoder(res.Body).Decode(&client)
	assert.NoError(t, err)
	assert.Equal(t, client.CouchID, "")
	assert.Equal(t, client.CouchRev, "")
	assert.NotEqual(t, client.ClientID, "")
	assert.NotEqual(t, client.ClientID, "ignored")
	assert.NotEqual(t, client.ClientID, clientID)
	assert.NotEqual(t, client.ClientSecret, "")
	assert.NotEqual(t, client.ClientSecret, "ignored")
	assert.NotEqual(t, client.RegistrationToken, "")
	assert.NotEqual(t, client.RegistrationToken, "ignored")
	assert.Equal(t, client.SecretExpiresAt, 0)
	assert.Equal(t, client.RedirectURIs, []string{"https://example.org/oauth/callback"})
	assert.Equal(t, client.GrantTypes, []string{"authorization_code", "refresh_token"})
	assert.Equal(t, client.ResponseTypes, []string{"code"})
	assert.Equal(t, client.ClientName, "cozy-test")
	assert.Equal(t, client.ClientKind, "test")
	assert.Equal(t, client.ClientURI, "https://github.com/cozy/cozy-test")
	assert.Equal(t, client.LogoURI, "https://raw.github.com/cozy/cozy-setup/gh-pages/assets/images/happycloud.png")
	assert.Equal(t, client.PolicyURI, "https://github/com/cozy/cozy-test/master/policy.md")
	assert.Equal(t, client.SoftwareID, "github.com/cozy/cozy-test")
	assert.Equal(t, client.SoftwareVersion, "v0.1.2")
	altClientID = client.ClientID
	altRegistrationToken = client.RegistrationToken
}

func TestDeleteClientInvalidClientID(t *testing.T) {
	req, _ := http.NewRequest("DELETE", ts.URL+"/auth/register/123456789", nil)
	req.Host = domain
	req.Header.Add("Authorization", "Bearer "+altRegistrationToken)
	res, err := client.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, "404 Not Found", res.Status)
}

func TestDeleteClientNoToken(t *testing.T) {
	req, _ := http.NewRequest("DELETE", ts.URL+"/auth/register/"+altClientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
}

func TestDeleteClientSuccess(t *testing.T) {
	req, _ := http.NewRequest("DELETE", ts.URL+"/auth/register/"+altClientID, nil)
	req.Host = domain
	req.Header.Add("Authorization", "Bearer "+altRegistrationToken)
	res, err := client.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, "204 No Content", res.Status)
}

func TestReadClientInvalidToken(t *testing.T) {
	res, err := getJSON("/auth/register/"+clientID, altRegistrationToken)
	assert.NoError(t, err)
	assert.Equal(t, "401 Unauthorized", res.Status)
}

func TestReadClientInvalidClientID(t *testing.T) {
	res, err := getJSON("/auth/register/"+altClientID, registrationToken)
	assert.NoError(t, err)
	assert.Equal(t, "404 Not Found", res.Status)
}

func TestReadClientSuccess(t *testing.T) {
	res, err := getJSON("/auth/register/"+clientID, registrationToken)
	assert.NoError(t, err)
	assert.Equal(t, "200 OK", res.Status)
	var client oauth.Client
	err = json.NewDecoder(res.Body).Decode(&client)
	assert.NoError(t, err)
	assert.Equal(t, client.ClientID, clientID)
	assert.Equal(t, client.ClientSecret, clientSecret)
	assert.Equal(t, client.SecretExpiresAt, 0)
	assert.Equal(t, client.RegistrationToken, "")
	assert.Equal(t, client.RedirectURIs, []string{"https://example.org/oauth/callback"})
	assert.Equal(t, client.GrantTypes, []string{"authorization_code", "refresh_token"})
	assert.Equal(t, client.ResponseTypes, []string{"code"})
	assert.Equal(t, client.ClientName, "cozy-test")
	assert.Equal(t, client.SoftwareID, "github.com/cozy/cozy-test")
}

func TestUpdateClientDeletedClientID(t *testing.T) {
	res, err := putJSON("/auth/register/"+altClientID, registrationToken, echo.Map{
		"client_id": altClientID,
	})
	assert.NoError(t, err)
	assert.Equal(t, "404 Not Found", res.Status)
}

func TestUpdateClientInvalidClientID(t *testing.T) {
	res, err := putJSON("/auth/register/"+clientID, registrationToken, echo.Map{
		"client_id": "123456789",
	})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	var body map[string]string
	err = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, err)
	assert.Equal(t, "invalid_client_id", body["error"])
	assert.Equal(t, "client_id is mandatory", body["error_description"])
}

func TestUpdateClientNoRedirectURI(t *testing.T) {
	res, err := putJSON("/auth/register/"+clientID, registrationToken, echo.Map{
		"client_id":   clientID,
		"client_name": "cozy-test",
		"software_id": "github.com/cozy/cozy-test",
	})
	assert.NoError(t, err)
	assert.Equal(t, "400 Bad Request", res.Status)
	var body map[string]string
	err = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, err)
	assert.Equal(t, "invalid_redirect_uri", body["error"])
	assert.Equal(t, "redirect_uris is mandatory", body["error_description"])
}

func TestUpdateClientSuccess(t *testing.T) {
	res, err := putJSON("/auth/register/"+clientID, registrationToken, echo.Map{
		"client_id":        clientID,
		"redirect_uris":    []string{"https://example.org/oauth/callback"},
		"client_name":      "cozy-test",
		"software_id":      "github.com/cozy/cozy-test",
		"software_version": "v0.1.3",
	})
	assert.NoError(t, err)
	assert.NoError(t, err)
	assert.Equal(t, "200 OK", res.Status)
	var client oauth.Client
	err = json.NewDecoder(res.Body).Decode(&client)
	assert.NoError(t, err)
	assert.Equal(t, client.ClientID, clientID)
	assert.Equal(t, client.ClientSecret, clientSecret)
	assert.Equal(t, client.SecretExpiresAt, 0)
	assert.Equal(t, client.RegistrationToken, "")
	assert.Equal(t, client.RedirectURIs, []string{"https://example.org/oauth/callback"})
	assert.Equal(t, client.GrantTypes, []string{"authorization_code", "refresh_token"})
	assert.Equal(t, client.ResponseTypes, []string{"code"})
	assert.Equal(t, client.ClientName, "cozy-test")
	assert.Equal(t, client.SoftwareID, "github.com/cozy/cozy-test")
	assert.Equal(t, client.SoftwareVersion, "v0.1.3")
}

func TestUpdateClientSecret(t *testing.T) {
	res, err := putJSON("/auth/register/"+clientID, registrationToken, echo.Map{
		"client_id":        clientID,
		"client_secret":    clientSecret,
		"redirect_uris":    []string{"https://example.org/oauth/callback"},
		"client_name":      "cozy-test",
		"software_id":      "github.com/cozy/cozy-test",
		"software_version": "v0.1.4",
	})
	assert.NoError(t, err)
	assert.NoError(t, err)
	assert.Equal(t, "200 OK", res.Status)
	var client oauth.Client
	err = json.NewDecoder(res.Body).Decode(&client)
	assert.NoError(t, err)
	assert.Equal(t, client.ClientID, clientID)
	assert.NotEqual(t, client.ClientSecret, "")
	assert.NotEqual(t, client.ClientSecret, clientSecret)
	assert.Equal(t, client.SecretExpiresAt, 0)
	assert.Equal(t, client.RegistrationToken, "")
	assert.Equal(t, client.RedirectURIs, []string{"https://example.org/oauth/callback"})
	assert.Equal(t, client.GrantTypes, []string{"authorization_code", "refresh_token"})
	assert.Equal(t, client.ResponseTypes, []string{"code"})
	assert.Equal(t, client.ClientName, "cozy-test")
	assert.Equal(t, client.SoftwareID, "github.com/cozy/cozy-test")
	assert.Equal(t, client.SoftwareVersion, "v0.1.4")
	clientSecret = client.ClientSecret
}

func TestAuthorizeFormRedirectsWhenNotLoggedIn(t *testing.T) {
	anonymousClient := &http.Client{CheckRedirect: noRedirect}
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&scope=files:read&redirect_uri="+u+"&client_id="+clientID, nil)
	req.Host = domain
	res, err := anonymousClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "303 See Other", res.Status)
}

func TestAuthorizeFormBadResponseType(t *testing.T) {
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=token&state=123456&scope=files:read&redirect_uri="+u+"&client_id="+clientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "Invalid response type")
}

func TestAuthorizeFormNoState(t *testing.T) {
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&scope=files:read&redirect_uri="+u+"&client_id="+clientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The state parameter is mandatory")
}

func TestAuthorizeFormNoClientId(t *testing.T) {
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&scope=files:read&redirect_uri="+u, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The client_id parameter is mandatory")
}

func TestAuthorizeFormNoRedirectURI(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&scope=files:read&client_id="+clientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The redirect_uri parameter is mandatory")
}

func TestAuthorizeFormNoScope(t *testing.T) {
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&redirect_uri="+u+"&client_id="+clientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The scope parameter is mandatory")
}

func TestAuthorizeFormInvalidClient(t *testing.T) {
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&scope=files:read&redirect_uri="+u+"&client_id=f00", nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The client must be registered")
}

func TestAuthorizeFormInvalidRedirectURI(t *testing.T) {
	u := url.QueryEscape("https://evil.com/")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&scope=files:read&redirect_uri="+u+"&client_id="+clientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The redirect_uri parameter doesn&#39;t match the registered ones")
}

func TestAuthorizeFormSuccess(t *testing.T) {
	u := url.QueryEscape("https://example.org/oauth/callback")
	req, _ := http.NewRequest("GET", ts.URL+"/auth/authorize?response_type=code&state=123456&scope=files:read&redirect_uri="+u+"&client_id="+clientID, nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "would like permission to access your Cozy")
	re := regexp.MustCompile(`<input type="hidden" name="csrf_token" value="(\w+)"`)
	matches := re.FindStringSubmatch(string(body))
	if assert.Len(t, matches, 2) {
		csrfToken = matches[1]
	}
}

func TestAuthorizeWhenNotLoggedIn(t *testing.T) {
	anonymousClient := &http.Client{CheckRedirect: noRedirect}
	v := &url.Values{
		"state":        {"123456"},
		"client_id":    {clientID},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {csrfToken},
	}
	req, _ := http.NewRequest("POST", ts.URL+"/auth/authorize", bytes.NewBufferString(v.Encode()))
	req.Host = domain
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	res, err := anonymousClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "403 Forbidden", res.Status)
}

func TestAuthorizeWithInvalidCSRFToken(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":        {"123456"},
		"client_id":    {clientID},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {"azertyuiop"},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "403 Forbidden", res.Status)
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "Invalid csrf token")
}

func TestAuthorizeWithNoState(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"client_id":    {clientID},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The state parameter is mandatory")
}

func TestAuthorizeWithNoClientID(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":        {"123456"},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The client_id parameter is mandatory")
}

func TestAuthorizeWithInvalidClientID(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":        {"123456"},
		"client_id":    {"987"},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The client must be registered")
}

func TestAuthorizeWithNoRedirectURI(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":      {"123456"},
		"client_id":  {clientID},
		"scope":      {"files:read"},
		"csrf_token": {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The redirect_uri parameter is invalid")
}

func TestAuthorizeWithInvalidURI(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":        {"123456"},
		"client_id":    {clientID},
		"redirect_uri": {"/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The redirect_uri parameter doesn&#39;t match the registered ones")
}

func TestAuthorizeWithNoScope(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":        {"123456"},
		"client_id":    {clientID},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"csrf_token":   {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	assert.Equal(t, "text/html; charset=UTF-8", res.Header.Get("Content-Type"))
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), "The scope parameter is mandatory")
}

func TestAuthorizeSuccess(t *testing.T) {
	res, err := postForm("/auth/authorize", &url.Values{
		"state":        {"123456"},
		"client_id":    {clientID},
		"redirect_uri": {"https://example.org/oauth/callback"},
		"scope":        {"files:read"},
		"csrf_token":   {csrfToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	if assert.Equal(t, "302 Found", res.Status) {
		var results []oauth.AccessCode
		req := &couchdb.AllDocsRequest{}
		couchdb.GetAllDocs(testInstance, consts.OAuthAccessCodes, req, &results)
		if assert.Len(t, results, 1) {
			code = results[0].Code
			expected := fmt.Sprintf("https://example.org/oauth/callback?access_code=%s&state=123456#", code)
			assert.Equal(t, expected, res.Header.Get("Location"))
			assert.Equal(t, results[0].ClientID, clientID)
			assert.Equal(t, results[0].Scope, "files:read")
		}
	}
}

func TestAccessTokenNoGrantType(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "the grant_type parameter is mandatory")
}

func TestAccessTokenInvalidGrantType(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "invalid grant type")
}

func TestAccessTokenNoClientID(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"authorization_code"},
		"client_secret": {clientSecret},
		"code":          {code},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "the client_id parameter is mandatory")
}

func TestAccessTokenInvalidClientID(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"foo"},
		"client_secret": {clientSecret},
		"code":          {code},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "the client must be registered")
}

func TestAccessTokenNoClientSecret(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type": {"authorization_code"},
		"client_id":  {clientID},
		"code":       {code},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "the client_secret parameter is mandatory")
}

func TestAccessTokenInvalidClientSecret(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {"foo"},
		"code":          {code},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "invalid client_secret")
}

func TestAccessTokenNoCode(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "the code parameter is mandatory")
}

func TestAccessTokenInvalidCode(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {"foo"},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "invalid code")
}

func TestAccessTokenSuccess(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	var response map[string]string
	err = json.NewDecoder(res.Body).Decode(&response)
	assert.NoError(t, err)
	assert.Equal(t, "bearer", response["token_type"])
	assert.Equal(t, "files:read", response["scope"])
	assertValidToken(t, response["access_token"], "access")
	assertValidToken(t, response["refresh_token"], "refresh")
	refreshToken = response["refresh_token"]
}

func TestRefreshTokenNoToken(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "invalid refresh token")
}

func TestRefreshTokenInvalidToken(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {"foo"},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "invalid refresh token")
}

func TestRefreshTokenInvalidSigningMethod(t *testing.T) {
	claims := permissions.Claims{
		StandardClaims: jwt.StandardClaims{
			Audience: "refresh",
			Issuer:   domain,
			IssuedAt: crypto.Timestamp(),
			Subject:  clientID,
		},
		Scope: "files:write",
	}
	token := jwt.NewWithClaims(jwt.GetSigningMethod("none"), claims)
	fakeToken, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	assert.NoError(t, err)
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {fakeToken},
	})
	assert.NoError(t, err)
	assertJSONError(t, res, "invalid refresh token")
}

func TestRefreshTokenSuccess(t *testing.T) {
	res, err := postForm("/auth/access_token", &url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
	})
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	var response map[string]string
	err = json.NewDecoder(res.Body).Decode(&response)
	assert.NoError(t, err)
	assert.Equal(t, "bearer", response["token_type"])
	assert.Equal(t, "files:read", response["scope"])
	assert.Equal(t, "", response["refresh_token"])
	assertValidToken(t, response["access_token"], "access")
}

func TestLogoutNoToken(t *testing.T) {
	req, _ := http.NewRequest("DELETE", ts.URL+"/auth/login", nil)
	req.Host = domain
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "401 Unauthorized", res.Status)
	cookies := jar.Cookies(instanceURL)
	assert.Len(t, cookies, 2) // cozysessid and _csrf
}

func TestLogoutSuccess(t *testing.T) {
	a := app.Manifest{Slug: "home"}
	token := testInstance.BuildAppToken(&a)
	permissions.CreateAppSet(testInstance, a.Slug, permissions.Set{})
	req, _ := http.NewRequest("DELETE", ts.URL+"/auth/login", nil)
	req.Host = domain
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	permissions.DestroyApp(testInstance, "home")

	assert.Equal(t, "204 No Content", res.Status)
	cookies := jar.Cookies(instanceURL)
	assert.Len(t, cookies, 1) // _csrf
	assert.Equal(t, "_csrf", cookies[0].Name)
}

func TestPassphraseResetLoggedIn(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/passphrase_reset", nil)
	req.Host = domain
	res, err := client.Do(req)
	if !assert.NoError(t, err) {
		return
	}
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), `Are you sure`)
	assert.Contains(t, string(body), `<input type="hidden" name="csrf_token"`)
}

func TestPassphraseReset(t *testing.T) {
	req1, _ := http.NewRequest("GET", ts.URL+"/auth/passphrase_reset", nil)
	req1.Host = domain
	res1, err := client.Do(req1)
	if !assert.NoError(t, err) {
		return
	}
	defer res1.Body.Close()
	assert.Equal(t, "200 OK", res1.Status)
	csrfCookie := res1.Cookies()[0]
	assert.Equal(t, "_csrf", csrfCookie.Name)
	res2, err := postForm("/auth/passphrase_reset", &url.Values{
		"csrf_token": {csrfCookie.Value},
	})
	if !assert.NoError(t, err) {
		return
	}
	defer res2.Body.Close()
	if assert.Equal(t, "303 See Other", res2.Status) {
		assert.Equal(t, "https://cozy.example.net/auth/login",
			res2.Header.Get("Location"))
	}
}

func TestPassphraseRenewFormNoToken(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/passphrase_renew", nil)
	req.Host = domain
	res, err := client.Do(req)
	if !assert.NoError(t, err) {
		return
	}
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), `{"error":"invalid_token"}`)
}

func TestPassphraseRenewFormBadToken(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/passphrase_renew?token=zzzz", nil)
	req.Host = domain
	res, err := client.Do(req)
	if !assert.NoError(t, err) {
		return
	}
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), `{"error":"invalid_token"}`)
}

func TestPassphraseRenewFormWithToken(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/auth/passphrase_renew?token=badbee", nil)
	req.Host = domain
	res, err := client.Do(req)
	if !assert.NoError(t, err) {
		return
	}
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)
	body, _ := ioutil.ReadAll(res.Body)
	assert.Contains(t, string(body), `type="hidden" name="passphrase_reset_token" value="badbee" />`)
}

func TestPassphraseRenew(t *testing.T) {
	d := "test.cozycloud.cc.web_reset_form"
	instance.Destroy(d)
	in1, err := instance.Create(&instance.Options{
		Domain: d,
		Locale: "en",
		Email:  "coucou@coucou.com",
	})
	if !assert.NoError(t, err) {
		return
	}
	defer func() {
		instance.Destroy(d)
	}()
	err = in1.RegisterPassphrase([]byte("MyPass"), in1.RegisterToken)
	if !assert.NoError(t, err) {
		return
	}
	req1, _ := http.NewRequest("GET", ts.URL+"/auth/passphrase_reset", nil)
	req1.Host = domain
	res1, err := client.Do(req1)
	if !assert.NoError(t, err) {
		return
	}
	defer res1.Body.Close()
	csrfCookie := res1.Cookies()[0]
	assert.Equal(t, "_csrf", csrfCookie.Name)
	res2, err := postFormDomain(d, "/auth/passphrase_reset", &url.Values{
		"csrf_token": {csrfCookie.Value},
	})
	if !assert.NoError(t, err) {
		return
	}
	defer res2.Body.Close()
	assert.Equal(t, "303 See Other", res2.Status)
	in2, err := instance.Get(d)
	if !assert.NoError(t, err) {
		return
	}
	res3, err := postFormDomain(d, "/auth/passphrase_renew", &url.Values{
		"passphrase_reset_token": {hex.EncodeToString(in2.PassphraseResetToken)},
		"passphrase":             {"NewPassphrase"},
		"csrf_token":             {csrfCookie.Value},
	})
	if !assert.NoError(t, err) {
		return
	}
	defer res3.Body.Close()
	if assert.Equal(t, "303 See Other", res3.Status) {
		assert.Equal(t, "https://test.cozycloud.cc.web_reset_form/auth/login",
			res3.Header.Get("Location"))
	}
}

func TestIsLoggedOutAfterLogout(t *testing.T) {
	content, err := getTestURL()
	assert.NoError(t, err)
	assert.Equal(t, "who_are_you", content)
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	config.GetConfig().Assets = "../../assets"
	web.LoadSupportedLocales()
	instanceURL, _ = url.Parse("https://" + domain + "/")
	j, _ := cookiejar.New(nil)
	jar = &testJar{
		Jar: j,
	}
	client = &http.Client{
		CheckRedirect: noRedirect,
		Jar:           jar,
	}
	instance.Destroy(domain)
	testInstance, _ = instance.Create(&instance.Options{
		Domain: domain,
		Locale: "en",
	})
	testInstance.RegisterPassphrase([]byte("MyPassphrase"), testInstance.RegisterToken)

	r := echo.New()
	r.GET("/test", func(c echo.Context) error {
		var content string
		if middlewares.IsLoggedIn(c) {
			content = "logged_in"
		} else {
			content = "who_are_you"
		}
		return c.String(http.StatusOK, content)
	}, middlewares.NeedInstance, middlewares.LoadSession)

	handler, err := web.CreateSubdomainProxy(r, apps.Serve)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	ts = httptest.NewServer(handler)
	res := m.Run()
	ts.Close()
	instance.Destroy(domain)
	os.Exit(res)
}

func noRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func getJSON(u, token string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", ts.URL+u, nil)
	req.Host = domain
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	return client.Do(req)
}

func postJSON(u string, v echo.Map) (*http.Response, error) {
	body, _ := json.Marshal(v)
	req, _ := http.NewRequest("POST", ts.URL+u, bytes.NewBuffer(body))
	req.Host = domain
	req.Header.Add("Content-Type", "application/json; charset=utf-8")
	req.Header.Add("Accept", "application/json")
	return client.Do(req)
}

func putJSON(u, token string, v echo.Map) (*http.Response, error) {
	body, _ := json.Marshal(v)
	req, _ := http.NewRequest("PUT", ts.URL+u, bytes.NewBuffer(body))
	req.Host = domain
	req.Header.Add("Content-Type", "application/json; charset=utf-8")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	return client.Do(req)
}

func postForm(u string, v *url.Values) (*http.Response, error) {
	req, _ := http.NewRequest("POST", ts.URL+u, bytes.NewBufferString(v.Encode()))
	req.Host = domain
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	return client.Do(req)
}

func postFormDomain(domain, u string, v *url.Values) (*http.Response, error) {
	req, _ := http.NewRequest("POST", ts.URL+u, bytes.NewBufferString(v.Encode()))
	req.Host = domain
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	return client.Do(req)
}

func putForm(u string, v *url.Values) (*http.Response, error) {
	req, _ := http.NewRequest("PUT", ts.URL+u, bytes.NewBufferString(v.Encode()))
	req.Host = domain
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	return client.Do(req)
}

func getTestURL() (string, error) {
	req, _ := http.NewRequest("GET", ts.URL+"/test", nil)
	req.Host = domain
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	content, _ := ioutil.ReadAll(res.Body)
	return string(content), nil
}

func assertValidToken(t *testing.T, token, audience string) {
	claims := permissions.Claims{}
	err := crypto.ParseJWT(token, func(token *jwt.Token) (interface{}, error) {
		return testInstance.OAuthSecret, nil
	}, &claims)
	assert.NoError(t, err)
	assert.Equal(t, audience, claims.Audience)
	assert.Equal(t, domain, claims.Issuer)
	assert.Equal(t, clientID, claims.Subject)
	assert.Equal(t, "files:read", claims.Scope)
}

func assertJSONError(t *testing.T, res *http.Response, message string) {
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
	var response map[string]string
	err := json.NewDecoder(res.Body).Decode(&response)
	assert.NoError(t, err)
	assert.Equal(t, message, response["error"])
}
