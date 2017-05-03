package oauth

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/cozy/cozy-stack/pkg/accounts"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/labstack/echo"
	"github.com/stretchr/testify/assert"
)

var ts *httptest.Server
var testInstance *instance.Instance

func TestOauthFlow(t *testing.T) {
	u := ts.URL + "/oauth/start/test-service?scope=the+world&state=somesecretstate"

	res, err := http.Get(u)
	if !assert.NoError(t, err) {
		return
	}
	bb, err := ioutil.ReadAll(res.Body)
	if !assert.NoError(t, err) {
		return
	}
	res.Body.Close()
	okURL := string(bb)

	// the user click the oauth link
	stopBeforeDataConnectFail := func(req *http.Request, via []*http.Request) error {
		if strings.Contains(req.URL.String(), "data-connect") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	res2, err := (&http.Client{CheckRedirect: stopBeforeDataConnectFail}).Get(okURL)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, http.StatusSeeOther, res2.StatusCode)
	finalURL, err := res2.Location()
	if !assert.NoError(t, err) {
		return
	}
	if !assert.Contains(t, finalURL.String(), "data-connect") {
		return
	}

	var out couchdb.JSONDoc
	err = couchdb.GetDoc(testInstance, consts.Accounts, finalURL.Query().Get("account"), &out)
	assert.NoError(t, err)
	assert.Equal(t, "the-access-token", out.M["oauth"].(map[string]interface{})["access_token"])
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	testutils.NeedCouchdb()

	setup := testutils.NewSetup(m, "oauth-konnectors")
	testInstance = setup.GetTestInstance()
	couchdb.ResetDB(couchdb.GlobalSecretsDB, consts.AccountTypes)
	setup.AddCleanup(func() error {
		return couchdb.DeleteDB(couchdb.GlobalSecretsDB, consts.AccountTypes)
	})

	ts = setup.GetTestServer("/oauth", Routes)
	redirectURI := ts.URL + "/oauth/redirect/test-service"

	service := makeTestService(redirectURI)
	setup.AddCleanup(func() error { service.Close(); return nil })
	serviceType := accounts.AccountType{
		DocID:                 "test-service",
		GrantMode:             accounts.AuthorizationCode,
		ClientID:              "the-client-id",
		ClientSecret:          "the-client-secret",
		AuthEndpoint:          service.URL + "/oauth2/v2/auth",
		TokenEndpoint:         service.URL + "/oauth2/v4/token",
		RegisteredRedirectURI: redirectURI,
	}
	err := couchdb.CreateNamedDoc(couchdb.GlobalSecretsDB, &serviceType)
	if err != nil {
		panic(err)
	}

	res := m.Run()

	os.Exit(res)
}

func makeTestService(redirectURI string) *httptest.Server {
	serviceHandler := echo.New()
	serviceHandler.GET("/oauth2/v2/auth", func(c echo.Context) error {
		ok := c.QueryParam("scope") == "the world" &&
			c.QueryParam("client_id") == "the-client-id" &&
			c.QueryParam("response_type") == "code" &&
			c.QueryParam("redirect_uri") == redirectURI

		if !ok {
			return fmt.Errorf("Bad Params " + c.QueryParams().Encode())
		}
		opts := &url.Values{}
		opts.Add("code", "myaccesscode")
		opts.Add("state", c.QueryParam("state"))
		return c.String(200, c.QueryParam("redirect_uri")+"?"+opts.Encode())

	})
	serviceHandler.POST("/oauth2/v4/token", func(c echo.Context) error {
		ok := c.FormValue("code") == "myaccesscode" &&
			c.FormValue("client_id") == "the-client-id" &&
			c.FormValue("client_secret") == "the-client-secret"

		if !ok {
			vv, _ := c.FormParams()
			return fmt.Errorf("Bad Authorization Code " + vv.Encode())
		}
		return c.JSON(200, map[string]interface{}{
			"access_token":  "the-access-token",
			"refresh_token": "the-refresh-token",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	})
	return httptest.NewServer(serviceHandler)
}
