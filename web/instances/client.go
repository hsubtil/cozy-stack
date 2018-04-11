package instances

import (
	"net/http"
	"time"

	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/oauth"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/echo"
)

func createToken(c echo.Context) error {
	domain := c.QueryParam("Domain")
	audience := c.QueryParam("Audience")
	scope := c.QueryParam("Scope")
	subject := c.QueryParam("Subject")
	in, err := instance.Get(domain)
	if err != nil {
		// FIXME https://issues.apache.org/jira/browse/COUCHDB-3336
		// With a cluster of couchdb, we can have a race condition where we
		// query an index before it has been updated for an instance that has
		// just been created.
		time.Sleep(1 * time.Second)
		in, err = instance.Get(domain)
		if err != nil {
			return wrapError(err)
		}
	}
	switch audience {
	case "app":
		audience = permissions.AppAudience
	case "konn", "konnector":
		audience = permissions.KonnectorAudience
	case "access-token":
		audience = permissions.AccessTokenAudience
	case "refresh-token":
		audience = permissions.RefreshTokenAudience
	case "cli":
		audience = permissions.CLIAudience
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Unknown audience %s", audience)
	}
	issuedAt := time.Now()
	token, err := in.MakeJWT(audience, subject, scope, "", issuedAt)
	if err != nil {
		return err
	}
	return c.String(http.StatusOK, token)
}

func registerClient(c echo.Context) error {
	in, err := instance.Get(c.QueryParam("Domain"))
	if err != nil {
		return wrapError(err)
	}
	client := oauth.Client{
		RedirectURIs: []string{c.QueryParam("RedirectURI")},
		ClientName:   c.QueryParam("ClientName"),
		SoftwareID:   c.QueryParam("SoftwareID"),
	}
	if regErr := client.Create(in); regErr != nil {
		return c.String(http.StatusBadRequest, regErr.Description)
	}
	return c.JSON(http.StatusOK, client)
}
