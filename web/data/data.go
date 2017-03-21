// Package data provide simple CRUD operation on couchdb doc
package data

import (
	"net/http"
	"strconv"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/permissions"
	"github.com/labstack/echo"
)

func validDoctype(next echo.HandlerFunc) echo.HandlerFunc {
	// TODO extends me to verificate characters allowed in db name.
	return func(c echo.Context) error {
		doctype := c.Param("doctype")
		if doctype == "" {
			return jsonapi.NewError(http.StatusBadRequest, "Invalid doctype '%s'", doctype)
		}
		c.Set("doctype", doctype)
		return next(c)
	}
}

func allDoctypes(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	if err := permissions.AllowWholeType(c, permissions.GET, consts.Doctypes); err != nil {
		return err
	}

	types, err := couchdb.AllDoctypes(instance)
	if err != nil {
		return err
	}
	var doctypes []string
	for _, typ := range types {
		if CheckReadable(typ) == nil {
			doctypes = append(doctypes, typ)
		}
	}
	return c.JSON(http.StatusOK, doctypes)
}

// GetDoc get a doc by its type and id
func getDoc(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	doctype := c.Get("doctype").(string)
	docid := c.Param("docid")

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	if docid == "" {
		return dbStatus(c)
	}

	revs := c.QueryParam("revs")
	if revs == "true" {
		return proxy(c, docid)
	}

	var out couchdb.JSONDoc
	err := couchdb.GetDoc(instance, doctype, docid, &out)
	if err != nil {
		return err
	}

	out.Type = doctype

	if err := permissions.Allow(c, permissions.GET, &out); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, out.ToMapWithType())
}

// CreateDoc create doc from the json passed as body
func createDoc(c echo.Context) error {
	doctype := c.Get("doctype").(string)
	instance := middlewares.GetInstance(c)

	doc := couchdb.JSONDoc{Type: doctype}
	if err := c.Bind(&doc.M); err != nil {
		return jsonapi.NewError(http.StatusBadRequest, err)
	}

	if err := CheckWritable(doctype); err != nil {
		return err
	}

	if err := permissions.Allow(c, permissions.POST, &doc); err != nil {
		return err
	}

	if err := couchdb.CreateDoc(instance, doc); err != nil {
		return err
	}

	return c.JSON(http.StatusCreated, echo.Map{
		"ok":   true,
		"id":   doc.ID(),
		"rev":  doc.Rev(),
		"type": doc.DocType(),
		"data": doc.ToMapWithType(),
	})
}

func createNamedDoc(c echo.Context, doc couchdb.JSONDoc) error {
	instance := middlewares.GetInstance(c)

	err := permissions.Allow(c, permissions.POST, &doc)
	if err != nil {
		return err
	}

	err = couchdb.CreateNamedDoc(instance, doc)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, echo.Map{
		"ok":   true,
		"id":   doc.ID(),
		"rev":  doc.Rev(),
		"type": doc.DocType(),
		"data": doc.ToMapWithType(),
	})
}

func updateDoc(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	var doc couchdb.JSONDoc
	if err := c.Bind(&doc); err != nil {
		return jsonapi.NewError(http.StatusBadRequest, err)
	}

	doc.Type = c.Param("doctype")

	if err := CheckWritable(doc.Type); err != nil {
		return err
	}

	if (doc.ID() == "") != (doc.Rev() == "") {
		return jsonapi.NewError(http.StatusBadRequest,
			"You must either provide an _id and _rev in document (update) or neither (create with fixed id).")
	}

	if doc.ID() != "" && doc.ID() != c.Param("docid") {
		return jsonapi.NewError(http.StatusBadRequest, "document _id doesnt match url")
	}

	if doc.ID() == "" {
		doc.SetID(c.Param("docid"))
		return createNamedDoc(c, doc)
	}

	errWhole := permissions.AllowWholeType(c, permissions.PUT, doc.DocType())
	if errWhole != nil {

		errOld := fetchOldAndCheckPerm(c, permissions.PUT, doc.DocType(), doc.ID())
		if errOld != nil {
			return errOld
		}

		// also check if permissions set allows manipulating new doc
		errNew := permissions.Allow(c, permissions.PUT, &doc)
		if errNew != nil {
			return errNew
		}
	}

	errUpdate := couchdb.UpdateDoc(instance, doc)
	if errUpdate != nil {
		return errUpdate
	}

	return c.JSON(http.StatusOK, echo.Map{
		"ok":   true,
		"id":   doc.ID(),
		"rev":  doc.Rev(),
		"type": doc.DocType(),
		"data": doc.ToMapWithType(),
	})
}

func deleteDoc(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	doctype := c.Get("doctype").(string)
	docid := c.Param("docid")
	revHeader := c.Request().Header.Get("If-Match")
	revQuery := c.QueryParam("rev")
	rev := ""

	if revHeader != "" && revQuery != "" && revQuery != revHeader {
		return jsonapi.NewError(http.StatusBadRequest,
			"If-Match Header and rev query parameters mismatch")
	} else if revHeader != "" {
		rev = revHeader
	} else if revQuery != "" {
		rev = revQuery
	} else {
		return jsonapi.NewError(http.StatusBadRequest, "delete without revision")
	}

	if err := CheckWritable(doctype); err != nil {
		return err
	}

	errID := permissions.AllowTypeAndID(c, permissions.DELETE, doctype, docid)
	if errID != nil {
		errOld := fetchOldAndCheckPerm(c, permissions.DELETE, doctype, docid)
		if errOld != nil {
			return errOld
		}
	}

	tombrev, err := couchdb.Delete(instance, doctype, docid, rev)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, echo.Map{
		"ok":      true,
		"id":      docid,
		"rev":     tombrev,
		"type":    doctype,
		"deleted": true,
	})

}

func defineIndex(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	doctype := c.Get("doctype").(string)
	var definitionRequest map[string]interface{}

	if err := c.Bind(&definitionRequest); err != nil {
		return jsonapi.NewError(http.StatusBadRequest, err)
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	result, err := couchdb.DefineIndexRaw(instance, doctype, &definitionRequest)
	if couchdb.IsNoDatabaseError(err) {
		if err = couchdb.CreateDB(instance, doctype); err == nil {
			result, err = couchdb.DefineIndexRaw(instance, doctype, &definitionRequest)
		}
	}
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, result)
}

func findDocuments(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	doctype := c.Get("doctype").(string)
	var findRequest map[string]interface{}

	if err := c.Bind(&findRequest); err != nil {
		return jsonapi.NewError(http.StatusBadRequest, err)
	}

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	var results []couchdb.JSONDoc
	err := couchdb.FindDocsRaw(instance, doctype, &findRequest, &results)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, echo.Map{"docs": results})
}

var allowedChangesParams = map[string]bool{
	"feed":      true,
	"style":     true,
	"since":     true,
	"limit":     true,
	"timeout":   true,
	"heartbeat": true, // Pouchdb sends heartbeet even for non-continuous
	"_nonce":    true, // Pouchdb sends a request hash to avoid agressive caching by some browsers
}

func changesFeed(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	var doctype = c.Get("doctype").(string)

	// Drop a clear error for parameters not supported by stack
	for key := range c.QueryParams() {
		if !allowedChangesParams[key] {
			return jsonapi.NewError(http.StatusBadRequest, "Unsuported query parameter '%s'", key)
		}
	}

	feed, err := couchdb.ValidChangesMode(c.QueryParam("feed"))
	if err != nil {
		return jsonapi.NewError(http.StatusBadRequest, err)
	}

	feedStyle, err := couchdb.ValidChangesStyle(c.QueryParam("style"))
	if err != nil {
		return jsonapi.NewError(http.StatusBadRequest, err)
	}

	limitString := c.QueryParam("limit")
	limit := 0
	if limitString != "" {
		if limit, err = strconv.Atoi(limitString); err != nil {
			return jsonapi.NewError(http.StatusBadRequest, "Invalid limit value '%s'", err.Error())
		}
	}

	if err = permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	results, err := couchdb.GetChanges(instance, &couchdb.ChangesRequest{
		DocType: doctype,
		Feed:    feed,
		Style:   feedStyle,
		Since:   c.QueryParam("since"),
		Limit:   limit,
	})

	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, results)
}

func allDocs(c echo.Context) error {
	doctype := c.Get("doctype").(string)

	if err := CheckReadable(doctype); err != nil {
		return err
	}

	if err := permissions.AllowWholeType(c, permissions.GET, doctype); err != nil {
		return err
	}

	return proxy(c, "_all_docs")
}

// mostly just to prevent couchdb crash on replications
func dataAPIWelcome(c echo.Context) error {
	return c.JSON(http.StatusOK, echo.Map{
		"message": "welcome to a cozy API",
	})
}

func couchdbStyleErrorHandler(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		err := next(c)
		if err == nil {
			return nil
		}

		if ce, ok := err.(*couchdb.Error); ok {
			return c.JSON(ce.StatusCode, ce.JSON())
		}

		if he, ok := err.(*echo.HTTPError); ok {
			return c.JSON(he.Code, echo.Map{"error": he.Error()})
		}

		if je, ok := err.(*jsonapi.Error); ok {
			return c.JSON(je.Status, echo.Map{"error": je.Title})
		}

		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}
}

// Routes sets the routing for the status service
func Routes(router *echo.Group) {
	router.Use(couchdbStyleErrorHandler)

	// API Routes that don't depend on a doctype
	router.GET("/", dataAPIWelcome)
	router.GET("/_all_doctypes", allDoctypes)

	group := router.Group("/:doctype", validDoctype)

	replicationRoutes(group)

	// API Routes under /:doctype
	group.GET("/:docid", getDoc)
	group.PUT("/:docid", updateDoc)
	group.DELETE("/:docid", deleteDoc)
	group.GET("/:docid/relationships/references", listReferencesHandler)
	group.POST("/:docid/relationships/references", addReferencesHandler)
	group.DELETE("/:docid/relationships/references", removeReferencesHandler)
	group.POST("/", createDoc)
	group.GET("/_all_docs", allDocs)
	group.POST("/_all_docs", allDocs)
	group.POST("/_index", defineIndex)
	group.POST("/_find", findDocuments)
	// group.DELETE("/:docid", DeleteDoc)
}
