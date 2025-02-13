package settings

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/model/bitwarden/settings"
	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/model/oauth"
	"github.com/cozy/cozy-stack/model/session"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/prefixer"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/cozy/cozy-stack/web/auth"
	"github.com/cozy/cozy-stack/web/errors"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/cozy/cozy-stack/worker/mails"
)

var ts, tsB, tsC *httptest.Server
var testInstance, testInstanceFlagship *instance.Instance
var instanceRev string
var token string
var oauthClientID string

func TestGetContext(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/settings/context", nil)
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestPatchWithGoodRev(t *testing.T) {
	// We are going to patch an instance with newer values, and give the good rev
	body := `{
		"data": {
			"type": "io.cozy.settings",
			"id": "io.cozy.settings.instance",
			"meta": {
				"rev": "%s"
			},
			"attributes": {
				"tz": "Europe/London",
				"email": "alice@example.org",
				"locale": "fr"
			}
		}
	}`

	doc1, err := testInstance.SettingsDocument()
	assert.NoError(t, err)
	body = fmt.Sprintf(body, doc1.Rev())
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestPatchWithBadRev(t *testing.T) {
	// We are going to patch an instance with newer values, but with a totally
	// random rev
	rev := "6-2d9b7ef014d10549c2b4e206672d3e44"
	body := `{
		"data": {
			"type": "io.cozy.settings",
			"id": "io.cozy.settings.instance",
			"meta": {
				"rev": "%s"
			},
			"attributes": {
				"tz": "Europe/Berlin",
				"email": "alice@example.com",
				"locale": "en"
			}
		}
	}`
	body = fmt.Sprintf(body, rev)
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.StatusCode)
}

func TestPatchWithBadRevNoChanges(t *testing.T) {
	// We are defining a random rev, but make no changes in the instance values
	rev := "6-2d9b7ef014d10549c2b4e206672d3e44"
	body := `{
		"data": {
			"type": "io.cozy.settings",
			"id": "io.cozy.settings.instance",
			"meta": {
				"rev": "%s"
			},
			"attributes": {
				"tz": "Europe/London",
				"email": "alice@example.org",
				"locale": "fr"
			}
		}
	}`
	body = fmt.Sprintf(body, rev)
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestPatchWithBadRevAndChanges(t *testing.T) {
	// We are defining a random rev, but make changes in the instance values
	rev := "6-2d9b7ef014d10549c2b4e206672d3e44"
	body := `{
			"data": {
				"type": "io.cozy.settings",
				"id": "io.cozy.settings.instance",
				"meta": {
					"rev": "%s"
				},
				"attributes": {
					"tz": "Europe/London",
					"email": "alice@example.com",
					"locale": "en"
				}
			}
		}`
	body = fmt.Sprintf(body, rev)
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.StatusCode)
}

func TestDiskUsage(t *testing.T) {
	res, err := http.Get(ts.URL + "/settings/disk-usage")
	assert.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/settings/disk-usage?include=trash", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	assert.NoError(t, err)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data, ok := result["data"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "io.cozy.settings", data["type"].(string))
	assert.Equal(t, "io.cozy.settings.disk-usage", data["id"].(string))
	attrs, ok := data["attributes"].(map[string]interface{})
	assert.True(t, ok)
	used, ok := attrs["used"].(string)
	assert.True(t, ok)
	assert.Equal(t, "0", used)
	files, ok := attrs["files"].(string)
	assert.True(t, ok)
	assert.Equal(t, "0", files)
	versions, ok := attrs["versions"].(string)
	assert.True(t, ok)
	assert.Equal(t, "0", versions)
	trash, ok := attrs["trash"].(string)
	assert.True(t, ok)
	assert.Equal(t, "0", trash)
}

func TestRegisterPassphraseWrongToken(t *testing.T) {
	args, _ := json.Marshal(&echo.Map{
		"passphrase":     "MyFirstPassphrase",
		"iterations":     5000,
		"register_token": "BADBEEF",
	})
	res1, err := http.Post(ts.URL+"/settings/passphrase", "application/json", bytes.NewReader(args))
	assert.NoError(t, err)
	defer res1.Body.Close()
	assert.Equal(t, "400 Bad Request", res1.Status)

	args, _ = json.Marshal(&echo.Map{
		"passphrase":     "MyFirstPassphrase",
		"iterations":     5000,
		"register_token": "XYZ",
	})
	res2, err := http.Post(ts.URL+"/settings/passphrase", "application/json", bytes.NewReader(args))
	assert.NoError(t, err)
	defer res2.Body.Close()
	assert.Equal(t, "400 Bad Request", res2.Status)
}

func TestRegisterPassphraseCorrectToken(t *testing.T) {
	args, _ := json.Marshal(&echo.Map{
		"passphrase":     "MyFirstPassphrase",
		"iterations":     5000,
		"register_token": hex.EncodeToString(testInstance.RegisterToken),
		"key":            "xxx",
	})
	res, err := http.Post(ts.URL+"/settings/passphrase", "application/json", bytes.NewReader(args))
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, 200, res.StatusCode)
	cookies := res.Cookies()
	assert.Len(t, cookies, 1)
	assert.Equal(t, cookies[0].Name, session.CookieName(testInstance))
	assert.NotEmpty(t, cookies[0].Value)
}

func TestRegisterPassphraseForFlagshipApp(t *testing.T) {
	oauthClient := &oauth.Client{
		RedirectURIs:    []string{"http:/localhost:4000/oauth/callback"},
		ClientName:      "Cozy-desktop on my-new-laptop",
		ClientKind:      "desktop",
		ClientURI:       "https://docs.cozy.io/en/mobile/desktop.html",
		LogoURI:         "https://docs.cozy.io/assets/images/cozy-logo-docs.svg",
		PolicyURI:       "https://cozy.io/policy",
		SoftwareID:      "/github.com/cozy-labs/cozy-desktop",
		SoftwareVersion: "0.16.0",
	}
	require.Nil(t, oauthClient.Create(testInstanceFlagship))
	client, err := oauth.FindClient(testInstanceFlagship, oauthClient.ClientID)
	require.NoError(t, err)
	require.NoError(t, client.SetFlagship(testInstanceFlagship))

	args, _ := json.Marshal(&echo.Map{
		"passphrase":     "MyFirstPassphrase",
		"iterations":     5000,
		"register_token": hex.EncodeToString(testInstanceFlagship.RegisterToken),
		"key":            "xxx-key-xxx",
		"public_key":     "xxx-public-key-xxx",
		"private_key":    "xxx-private-key-xxx",
		"client_id":      client.CouchID,
		"client_secret":  client.ClientSecret,
	})
	res, err := http.Post(tsC.URL+"/settings/passphrase/flagship", "application/json", bytes.NewReader(args))
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, 200, res.StatusCode)
	var resbody map[string]interface{}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&resbody))
	assert.NotNil(t, resbody["access_token"])
	assert.NotNil(t, resbody["refresh_token"])
	assert.Equal(t, "*", resbody["scope"])
	assert.Equal(t, "bearer", resbody["token_type"])
}

func TestUpdatePassphraseWithWrongPassphrase(t *testing.T) {
	args, _ := json.Marshal(&echo.Map{
		"new_passphrase":     "MyPassphrase",
		"current_passphrase": "BADBEEF",
		"iterations":         5000,
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/passphrase", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)
}

func TestUpdatePassphraseSuccess(t *testing.T) {
	args, _ := json.Marshal(&echo.Map{
		"new_passphrase":     "MyUpdatedPassphrase",
		"current_passphrase": "MyFirstPassphrase",
		"iterations":         5000,
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/passphrase", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "204 No Content", res.Status)
	cookies := res.Cookies()
	assert.Len(t, cookies, 1)
	assert.Equal(t, cookies[0].Name, session.CookieName(testInstance))
	assert.NotEmpty(t, cookies[0].Value)
}

func TestUpdatePassphraseWithForce(t *testing.T) {
	args, _ := json.Marshal(&echo.Map{
		"new_passphrase": "MyPassphrase",
		"iterations":     5000,
		"force":          true,
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/passphrase", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "400 Bad Request", res.Status)

	cfg := config.GetConfig().Authentication
	cfg["test-context"] = map[string]interface{}{
		"disable_password_authentication": true,
	}
	defer delete(cfg, "test-context")

	req, _ = http.NewRequest("PUT", ts.URL+"/settings/passphrase?Force=true", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "204 No Content", res.Status)
}

func TestGetHint(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/settings/hint", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "404 Not Found", res.Status)

	setting, err := settings.Get(testInstance)
	assert.NoError(t, err)
	setting.PassphraseHint = "my hint"
	err = couchdb.UpdateDoc(testInstance, setting)
	assert.NoError(t, err)

	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "204 No Content", res.Status)
}

func TestUpdateHint(t *testing.T) {
	args, _ := json.Marshal(&echo.Map{
		"hint": "my updated hint",
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/hint", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "204 No Content", res.Status)

	setting, err := settings.Get(testInstance)
	assert.NoError(t, err)
	assert.Equal(t, "my updated hint", setting.PassphraseHint)
}

func TestGetPassphraseParameters(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/settings/passphrase", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, 200, res.StatusCode)

	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data, ok := result["data"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "io.cozy.settings", data["type"])
	assert.Equal(t, "io.cozy.settings.passphrase", data["id"])
	attrs, ok := data["attributes"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "me@"+testInstance.Domain, attrs["salt"])
	assert.Equal(t, float64(0), attrs["kdf"])
	assert.Equal(t, float64(5000), attrs["iterations"])
}

func TestGetCapabilities(t *testing.T) {
	res, err := http.Get(ts.URL + "/settings/instance")
	assert.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/settings/capabilities", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	assert.NoError(t, err)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data, ok := result["data"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "io.cozy.settings", data["type"].(string))
	assert.Equal(t, "io.cozy.settings.capabilities", data["id"].(string))
	attrs, ok := data["attributes"].(map[string]interface{})
	assert.True(t, ok)
	versioning, ok := attrs["file_versioning"].(bool)
	assert.True(t, ok)
	assert.True(t, versioning)
	pass, ok := attrs["can_auth_with_password"].(bool)
	assert.True(t, ok)
	assert.True(t, pass)
	oidc, ok := attrs["can_auth_with_oidc"].(bool)
	assert.True(t, ok)
	assert.False(t, oidc)
}

func TestGetInstance(t *testing.T) {
	res, err := http.Get(ts.URL + "/settings/instance")
	assert.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)

	testInstance.RegisterToken = []byte("test")
	res, err = http.Get(ts.URL + "/settings/instance?registerToken=74657374")
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	testInstance.RegisterToken = []byte{}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/settings/instance", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	assert.NoError(t, err)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data, ok := result["data"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "io.cozy.settings", data["type"].(string))
	assert.Equal(t, "io.cozy.settings.instance", data["id"].(string))
	meta, ok := data["meta"].(map[string]interface{})
	assert.True(t, ok)
	instanceRev = meta["rev"].(string)
	assert.NotEmpty(t, instanceRev)
	attrs, ok := data["attributes"].(map[string]interface{})
	assert.True(t, ok)
	email, ok := attrs["email"].(string)
	assert.True(t, ok)
	assert.Equal(t, "alice@example.org", email)
	tz, ok := attrs["tz"].(string)
	assert.True(t, ok)
	assert.Equal(t, "Europe/London", tz)
	locale, ok := attrs["locale"].(string)
	assert.True(t, ok)
	assert.Equal(t, "en", locale)
}

func TestUpdateInstance(t *testing.T) {
	var newRev string

	checkResult := func(res *http.Response) {
		assert.Equal(t, 200, res.StatusCode)
		var result map[string]interface{}
		err := json.NewDecoder(res.Body).Decode(&result)
		assert.NoError(t, err)
		data, ok := result["data"].(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "io.cozy.settings", data["type"].(string))
		assert.Equal(t, "io.cozy.settings.instance", data["id"].(string))
		meta, ok := data["meta"].(map[string]interface{})
		assert.True(t, ok)
		rev := meta["rev"].(string)
		assert.NotEmpty(t, rev)
		assert.NotEqual(t, instanceRev, rev)
		newRev = rev
		attrs, ok := data["attributes"].(map[string]interface{})
		assert.True(t, ok)
		email, ok := attrs["email"].(string)
		assert.True(t, ok)
		assert.Equal(t, "alice@example.net", email)
		tz, ok := attrs["tz"].(string)
		assert.True(t, ok)
		assert.Equal(t, "Europe/Paris", tz)
		locale, ok := attrs["locale"].(string)
		assert.True(t, ok)
		assert.Equal(t, "fr", locale)
	}

	body := `{
		"data": {
			"type": "io.cozy.settings",
			"id": "io.cozy.settings.instance",
			"meta": {
				"rev": "%s"
			},
			"attributes": {
				"tz": "Europe/Paris",
				"email": "alice@example.net",
				"locale": "fr"
			}
		}
	}`
	body = fmt.Sprintf(body, instanceRev)
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	checkResult(res)

	req, _ = http.NewRequest("GET", ts.URL+"/settings/instance", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	checkResult(res)

	instanceRev = newRev
}

func TestUpdatePassphraseWithTwoFactorAuth(t *testing.T) {
	body := `{
		"auth_mode": "two_factor_mail"
	}`
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance/auth_mode", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	if !assert.Equal(t, "204 No Content", res.Status) {
		return
	}

	mailPassCode, err := testInstance.GenerateMailConfirmationCode()
	assert.NoError(t, err)
	body = `{
		"auth_mode": "two_factor_mail",
		"two_factor_activation_code": "%s"
	}`
	body = fmt.Sprintf(body, mailPassCode)
	req, _ = http.NewRequest("PUT", ts.URL+"/settings/instance/auth_mode", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	if !assert.Equal(t, "204 No Content", res.Status) {
		return
	}

	args, _ := json.Marshal(&echo.Map{
		"current_passphrase": "MyPassphrase",
	})
	req, _ = http.NewRequest("PUT", ts.URL+"/settings/passphrase", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "200 OK", res.Status)

	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)

	{
		twoFactorToken, ok := result["two_factor_token"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, twoFactorToken)
	}

	twoFactorToken, twoFactorPasscode, err := testInstance.GenerateTwoFactorSecrets()
	assert.NoError(t, err)

	args, _ = json.Marshal(&echo.Map{
		"new_passphrase":      "MyLastPassphrase",
		"two_factor_token":    twoFactorToken,
		"two_factor_passcode": twoFactorPasscode,
	})
	req, _ = http.NewRequest("PUT", ts.URL+"/settings/passphrase", bytes.NewReader(args))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "204 No Content", res.Status)
}

func TestListClients(t *testing.T) {
	res, err := http.Get(ts.URL + "/settings/clients")
	assert.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)

	client := &oauth.Client{
		RedirectURIs:    []string{"http:/localhost:4000/oauth/callback"},
		ClientName:      "Cozy-desktop on my-new-laptop",
		ClientKind:      "desktop",
		ClientURI:       "https://docs.cozy.io/en/mobile/desktop.html",
		LogoURI:         "https://docs.cozy.io/assets/images/cozy-logo-docs.svg",
		PolicyURI:       "https://cozy.io/policy",
		SoftwareID:      "/github.com/cozy-labs/cozy-desktop",
		SoftwareVersion: "0.16.0",
	}
	regErr := client.Create(testInstance)
	assert.Nil(t, regErr)
	oauthClientID = client.ClientID

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/settings/clients", nil)
	assert.NoError(t, err)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data := result["data"].([]interface{})
	assert.Len(t, data, 2)
	obj := data[1].(map[string]interface{})
	assert.Equal(t, "io.cozy.oauth.clients", obj["type"].(string))
	assert.Equal(t, client.ClientID, obj["id"].(string))
	links := obj["links"].(map[string]interface{})
	self := links["self"].(string)
	assert.Equal(t, "/settings/clients/"+client.ClientID, self)
	attrs := obj["attributes"].(map[string]interface{})
	redirectURIs := attrs["redirect_uris"].([]interface{})
	assert.Len(t, redirectURIs, 1)
	assert.Equal(t, client.RedirectURIs[0], redirectURIs[0].(string))
	assert.Equal(t, client.ClientName, attrs["client_name"].(string))
	assert.Equal(t, client.ClientKind, attrs["client_kind"].(string))
	assert.Equal(t, client.ClientURI, attrs["client_uri"].(string))
	assert.Equal(t, client.LogoURI, attrs["logo_uri"].(string))
	assert.Equal(t, client.PolicyURI, attrs["policy_uri"].(string))
	assert.Equal(t, client.SoftwareID, attrs["software_id"].(string))
	assert.Equal(t, client.SoftwareVersion, attrs["software_version"].(string))
	assert.Nil(t, attrs["client_secret"])
}

func TestRevokeClient(t *testing.T) {
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/settings/clients/"+oauthClientID, nil)
	assert.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)

	req, err = http.NewRequest(http.MethodDelete, ts.URL+"/settings/clients/"+oauthClientID, nil)
	assert.NoError(t, err)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 204, res.StatusCode)

	req, err = http.NewRequest(http.MethodGet, ts.URL+"/settings/clients", nil)
	assert.NoError(t, err)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data := result["data"].([]interface{})
	assert.Len(t, data, 1)
}

func TestRedirectOnboardingSecret(t *testing.T) {
	url := tsB.URL + "/settings/onboarded"

	// Disable redirections
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	// Without onboarding
	res, err := client.Get(url)
	assert.NoError(t, err)
	assert.Equal(t, res.StatusCode, http.StatusSeeOther)
	loc, _ := res.Location()
	assert.Equal(t, loc.String(), testInstance.OnboardedRedirection().String())

	// With onboarding
	deeplink := "cozydrive://testinstance.com"
	oauthClient := &oauth.Client{
		RedirectURIs:     []string{deeplink},
		ClientName:       "CozyTest",
		SoftwareID:       "/github.com/cozy-labs/cozy-desktop",
		OnboardingSecret: "foobar",
		OnboardingApp:    "test",
	}

	oauthClient.Create(testInstance)
	res, err = client.Get(url)
	assert.NoError(t, err)
	assert.Equal(t, res.StatusCode, http.StatusSeeOther)

	loc, _ = res.Location()
	assert.NotEqual(t, loc.String(), testInstance.OnboardedRedirection().String())
	assert.Contains(t, loc.String(), "/auth/authorize")

	values := loc.Query()
	assert.Equal(t, values.Get("redirect_uri"), deeplink)
}

func TestPatchInstanceSameParams(t *testing.T) {
	doc1, err := testInstance.SettingsDocument()
	assert.NoError(t, err)

	body := `{
			"data": {
				"type": "io.cozy.settings",
				"id": "io.cozy.settings.instance",
				"meta": {
					"rev": "%s"
				},
				"attributes": {
					"tz": "Europe/Paris",
					"email": "alice@example.net",
					"locale": "fr"
				}
			}
		}`
	body = fmt.Sprintf(body, doc1.Rev())
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	content, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.NotEmpty(t, content)

	doc2, err := testInstance.SettingsDocument()
	assert.NoError(t, err)
	// Assert no changes
	assert.Equal(t, doc1.Rev(), doc2.Rev())
}

func TestPatchInstanceChangeParams(t *testing.T) {
	doc, err := testInstance.SettingsDocument()
	assert.NoError(t, err)

	body := `{
			"data": {
				"type": "io.cozy.settings",
				"id": "io.cozy.settings.instance",
				"meta": {
					"rev": "%s"
				},
				"attributes": {
					"tz": "Antarctica/McMurdo",
					"email": "alice@expat.eu",
					"locale": "de"
				}
			}
		}`
	body = fmt.Sprintf(body, doc.Rev())
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	content, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.NotEmpty(t, content)

	doc, err = testInstance.SettingsDocument()
	assert.NoError(t, err)

	assert.Equal(t, "Antarctica/McMurdo", doc.M["tz"].(string))
	assert.Equal(t, "alice@expat.eu", doc.M["email"].(string))
}

func TestPatchInstanceAddParam(t *testing.T) {
	doc1, err := testInstance.SettingsDocument()
	assert.NoError(t, err)

	body := `{
			"data": {
				"type": "io.cozy.settings",
				"id": "io.cozy.settings.instance",
				"meta": {
					"rev": "%s"
				},
				"attributes": {
					"tz": "Europe/Berlin",
					"email": "alice@example.com",
					"how_old_are_you": "42"
				}
			}
		}`
	body = fmt.Sprintf(body, doc1.Rev())
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	content, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.NotEmpty(t, content)

	doc2, err := testInstance.SettingsDocument()
	assert.NoError(t, err)
	assert.NotEqual(t, doc1.Rev(), doc2.Rev())
	assert.Equal(t, "42", doc2.M["how_old_are_you"].(string))
	assert.Equal(t, "Europe/Berlin", doc2.M["tz"].(string))
	assert.Equal(t, "alice@example.com", doc2.M["email"].(string))
}

func TestPatchInstanceRemoveParams(t *testing.T) {
	doc1, err := testInstance.SettingsDocument()
	assert.NoError(t, err)

	body := `{
			"data": {
				"type": "io.cozy.settings",
				"id": "io.cozy.settings.instance",
				"meta": {
					"rev": "%s"
				},
				"attributes": {
					"tz": "Europe/Berlin"
				}
			}
		}`
	body = fmt.Sprintf(body, doc1.Rev())
	req, _ := http.NewRequest("PUT", ts.URL+"/settings/instance", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	content, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.NotEmpty(t, content)

	doc2, err := testInstance.SettingsDocument()
	assert.NoError(t, err)
	assert.NotEqual(t, doc1.Rev(), doc2.Rev())
	assert.Equal(t, "Europe/Berlin", doc2.M["tz"].(string))
	_, ok := doc2.M["email"]
	assert.False(t, ok)
}

func TestFeatureFlags(t *testing.T) {
	_ = couchdb.DeleteDB(prefixer.GlobalPrefixer, consts.Settings)
	defer func() { _ = couchdb.DeleteDB(prefixer.GlobalPrefixer, consts.Settings) }()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/settings/flags", nil)
	assert.NoError(t, err)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data, _ := result["data"].(map[string]interface{})
	assert.Equal(t, "io.cozy.settings", data["type"])
	assert.Equal(t, "io.cozy.settings.flags", data["id"])
	attrs, ok := data["attributes"].(map[string]interface{})
	assert.True(t, ok)
	assert.Len(t, attrs, 0)

	testInstance.FeatureFlags = map[string]interface{}{
		"from_instance_flag":   true,
		"from_multiple_source": "instance_flag",
		"json_object":          map[string]interface{}{"foo": "bar"},
	}
	testInstance.FeatureSets = []string{"set1", "set2"}
	err = testInstance.Update()
	assert.NoError(t, err)
	cache := config.GetConfig().CacheStorage
	cacheKey := fmt.Sprintf("flags:%s:%v", testInstance.ContextName, testInstance.FeatureSets)
	buf, err := json.Marshal(map[string]interface{}{
		"from_feature_sets":    true,
		"from_multiple_source": "manager",
	})
	assert.NoError(t, err)
	cache.Set(cacheKey, buf, 5*time.Second)
	ctxFlags := couchdb.JSONDoc{Type: consts.Settings}
	ctxFlags.M = map[string]interface{}{
		"ratio_0": []map[string]interface{}{
			{"ratio": 0, "value": "context"},
		},
		"ratio_1": []map[string]interface{}{
			{"ratio": 1, "value": "context"},
		},
		"ratio_0.000001": []map[string]interface{}{
			{"ratio": 0.000001, "value": "context"},
		},
		"ratio_0.999999": []map[string]interface{}{
			{"ratio": 0.999999, "value": "context"},
		},
	}
	id := fmt.Sprintf("%s.%s", consts.ContextFlagsSettingsID, testInstance.ContextName)
	ctxFlags.SetID(id)
	err = couchdb.CreateNamedDocWithDB(prefixer.GlobalPrefixer, &ctxFlags)
	assert.NoError(t, err)
	defFlags := couchdb.JSONDoc{Type: consts.Settings}
	defFlags.M = map[string]interface{}{
		"ratio_0":              "defaults",
		"ratio_1":              "defaults",
		"ratio_0.000001":       "defaults",
		"ratio_0.999999":       "defaults",
		"from_multiple_source": "defaults",
		"from_defaults":        true,
	}
	defFlags.SetID(consts.DefaultFlagsSettingsID)
	err = couchdb.CreateNamedDocWithDB(prefixer.GlobalPrefixer, &defFlags)
	assert.NoError(t, err)

	res2, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res2.StatusCode)
	var result2 map[string]interface{}
	err = json.NewDecoder(res2.Body).Decode(&result2)
	assert.NoError(t, err)
	data2, _ := result2["data"].(map[string]interface{})
	assert.Equal(t, "io.cozy.settings", data2["type"])
	assert.Equal(t, "io.cozy.settings.flags", data2["id"])
	attrs2, _ := data2["attributes"].(map[string]interface{})
	assert.Equal(t, true, attrs2["from_instance_flag"])
	assert.Equal(t, true, attrs2["from_feature_sets"])
	assert.Equal(t, true, attrs2["from_defaults"])
	assert.EqualValues(t, testInstance.FeatureFlags["json_object"], attrs2["json_object"])
	assert.Equal(t, "instance_flag", attrs2["from_multiple_source"])
	assert.Equal(t, "defaults", attrs2["ratio_0"])
	assert.Equal(t, "defaults", attrs2["ratio_0.000001"])
	assert.Equal(t, "context", attrs2["ratio_0.999999"])
	assert.Equal(t, "context", attrs2["ratio_1"])
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	testutils.NeedCouchdb()
	setup := testutils.NewSetup(m, "settings_test")
	testInstance = setup.GetTestInstance(&lifecycle.Options{
		Locale:      "en",
		Timezone:    "Europe/Berlin",
		Email:       "alice@example.com",
		ContextName: "test-context",
	})
	scope := consts.Settings + " " + consts.OAuthClients
	_, token = setup.GetTestClient(scope)

	ts = setup.GetTestServer("/settings", Routes)
	ts.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler
	tsB = setup.GetTestServerMultipleRoutes(map[string]func(*echo.Group){
		"/auth": func(g *echo.Group) {
			g.Use(fakeAuthentication)
			auth.Routes(g)
		},
		"/settings": func(g *echo.Group) {
			g.Use(fakeAuthentication)
			Routes(g)
		},
	})
	tsB.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler

	setupFlagship := testutils.NewSetup(m, "settings_flagship_test")
	testInstanceFlagship = setupFlagship.GetTestInstance(&lifecycle.Options{
		Locale:      "en",
		Timezone:    "Europe/Berlin",
		Email:       "alice2@example.com",
		ContextName: "test-context",
	})
	tsC = setupFlagship.GetTestServer("/settings", Routes)
	tsC.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler

	os.Exit(setup.Run())
}

// Fake middleware used to inject a false session to mislead the authentication
// system
func fakeAuthentication(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		instance := c.Get("instance").(*instance.Instance)
		sess, _ := session.New(instance, session.LongRun)
		c.Set("session", sess)
		return next(c)
	}
}
