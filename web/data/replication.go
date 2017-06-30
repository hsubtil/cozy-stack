package data

import (
	"net/http"

	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/permissions"
	"github.com/cozy/echo"
)

func proxy(c echo.Context, path string) error {
	doctype := c.Get("doctype").(string)
	instance := middlewares.GetInstance(c)
	p := couchdb.Proxy(instance, doctype, path)
	p.ServeHTTP(c.Response(), c.Request())
	return nil
}

func getDesignDoc(c echo.Context) error {
	docid := c.Param("designdocid")
	doctype := c.Get("doctype").(string)

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	if paramIsTrue(c, "revs") {
		return proxy(c, "_design/"+docid)
	}

	return c.JSON(http.StatusBadRequest, echo.Map{
		"error": "_design docs are only readable for replication",
	})
}

func getLocalDoc(c echo.Context) error {
	doctype := c.Get("doctype").(string)
	docid := c.Param("docid")

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	return proxy(c, "_local/"+docid)

}

func setLocalDoc(c echo.Context) error {
	doctype := c.Get("doctype").(string)
	docid := c.Param("docid")

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	return proxy(c, "_local/"+docid)
}

func bulkGet(c echo.Context) error {
	doctype := c.Get("doctype").(string)

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	return proxy(c, "_bulk_get")
}

func fullCommit(c echo.Context) error {
	doctype := c.Get("doctype").(string)

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	return proxy(c, "_ensure_full_commit")
}

func dbStatus(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	doctype := c.Get("doctype").(string)

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	status, err := couchdb.DBStatus(instance, doctype)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, status)
}

func replicationRoutes(group *echo.Group) {
	// Routes used only for replication
	group.GET("/", dbStatus)
	group.GET("/_design/:designdocid", getDesignDoc)
	group.GET("/_changes", changesFeed)
	// POST=GET see http://docs.couchdb.org/en/2.0.0/api/database/changes.html#post--db-_changes)
	group.POST("/_changes", changesFeed)

	group.POST("/_ensure_full_commit", fullCommit)

	// useful for Pouchdb replication
	group.GET("/_bulk_get", bulkGet)

	// for storing checkpoints
	group.GET("/_local/:docid", getLocalDoc)
	group.PUT("/_local/:docid", setLocalDoc)
}
