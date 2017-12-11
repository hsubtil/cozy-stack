package permissions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo"
)

// exports all constants from pkg/permissions to avoid double imports
var (
	ALL    = permissions.ALL
	GET    = permissions.GET
	PUT    = permissions.PUT
	POST   = permissions.POST
	PATCH  = permissions.PATCH
	DELETE = permissions.DELETE
)

// ErrPatchCodeOrSet is returned when an attempt is made to patch both
// code & set in one request
var ErrPatchCodeOrSet = echo.NewHTTPError(http.StatusBadRequest,
	"The patch doc should have property 'codes' or 'permissions', not both")

// ContextPermissionSet is the key used in echo context to store permissions set
const ContextPermissionSet = "permissions_set"

// ContextClaims is the key used in echo context to store claims
// #nosec
const ContextClaims = "token_claims"

type APIPermission struct {
	*permissions.Permission
}

func (p *APIPermission) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.Permission)
}

// Relationships implements jsonapi.Doc
func (p *APIPermission) Relationships() jsonapi.RelationshipMap { return nil }

// Included implements jsonapi.Doc
func (p *APIPermission) Included() []jsonapi.Object { return nil }

// Links implements jsonapi.Doc
func (p *APIPermission) Links() *jsonapi.LinksList {
	links := &jsonapi.LinksList{Self: "/permissions/" + p.PID}
	parts := strings.SplitN(p.SourceID, "/", 2)
	if parts[0] == consts.Sharings {
		links.Related = "/sharings/" + parts[1]
	}
	return links
}

type getPermsFunc func(db couchdb.Database, id string) (*permissions.Permission, error)

func displayPermissions(c echo.Context) error {
	doc, err := GetPermission(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, doc.Permissions)
}

func createPermission(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	names := strings.Split(c.QueryParam("codes"), ",")
	parent, err := GetPermission(c)
	if err != nil {
		return err
	}

	var subdoc permissions.Permission
	if _, err = jsonapi.Bind(c.Request(), &subdoc); err != nil {
		return err
	}

	var codes map[string]string
	if names != nil {
		codes = make(map[string]string, len(names))
		for _, name := range names {
			codes[name], err = instance.CreateShareCode(name)
			if err != nil {
				return err
			}
		}
	}

	if parent == nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no parent")
	}

	pdoc, err := permissions.CreateShareSet(instance, parent, codes, subdoc.Permissions)
	if err != nil {
		return err
	}

	return jsonapi.Data(c, http.StatusOK, &APIPermission{pdoc}, nil)
}

const limitPermissionsByDoctype = 30

func listPermissionsByDoctype(c echo.Context, route, permType string) error {
	ins := middlewares.GetInstance(c)
	doctype := c.Param("doctype")
	if doctype == "" {
		return jsonapi.NewError(http.StatusBadRequest, "Missing doctype")
	}

	current, err := GetPermission(c)
	if err != nil {
		return err
	}

	if !current.Permissions.AllowWholeType(http.MethodGet, doctype) {
		return jsonapi.NewError(http.StatusForbidden,
			"You need GET permission on whole type to list its permissions")
	}

	cursor, err := jsonapi.ExtractPaginationCursor(c, limitPermissionsByDoctype)
	if err != nil {
		return err
	}

	perms, err := permissions.GetPermissionsByDoctype(ins, permType, doctype, cursor)
	if err != nil {
		return err
	}

	links := &jsonapi.LinksList{}
	if cursor.HasMore() {
		params, err := jsonapi.PaginationCursorToParams(cursor)
		if err != nil {
			return err
		}
		links.Next = fmt.Sprintf("/permissions/doctype/%s/%s?%s",
			doctype, route, params.Encode())
	}

	out := make([]jsonapi.Object, len(perms))
	for i, p := range perms {
		p.Codes = nil // Don't let an app get sharecodes for permissions it may not own
		out[i] = &APIPermission{&p}
	}

	return jsonapi.DataList(c, http.StatusOK, out, links)
}

func listByLinkPermissionsByDoctype(c echo.Context) error {
	return listPermissionsByDoctype(c, "shared-by-link", permissions.TypeShareByLink)
}

// listSharedWithMePermissionsByDoctype returns the list of all the permissions
// that apply for a given doctype for documents that were shared to the user.
func listSharedWithMePermissionsByDoctype(c echo.Context) error {
	return listPermissionsByDoctype(c, "shared-with-me", permissions.TypeSharedWithMe)
}

// listSharedByMePermissionsByDoctype returns the list of all the
// permissions that apply for a given doctype for documents that the user
// shared with others.
func listSharedByMePermissionsByDoctype(c echo.Context) error {
	return listPermissionsByDoctype(c, "shared-by-me", permissions.TypeSharedByMe)
}

type refAndVerb struct {
	ID      string               `json:"id"`
	DocType string               `json:"type"`
	Verbs   *permissions.VerbSet `json:"verbs"`
}

func listPermissions(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	references, err := jsonapi.BindRelations(c.Request())
	if err != nil {
		return err
	}
	ids := make(map[string][]string)
	for _, ref := range references {
		idSlice, ok := ids[ref.Type]
		if !ok {
			idSlice = []string{}
		}
		ids[ref.Type] = append(idSlice, ref.ID)
	}

	var out []refAndVerb
	for doctype, idSlice := range ids {
		result, err2 := permissions.GetPermissionsForIDs(instance, doctype, idSlice)
		if err2 != nil {
			return err2
		}
		for id, verbs := range result {
			out = append(out, refAndVerb{id, doctype, verbs})
		}
	}

	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	doc := jsonapi.Document{
		Data: (*json.RawMessage)(&data),
	}
	resp := c.Response()
	resp.Header().Set("Content-Type", jsonapi.ContentType)
	resp.WriteHeader(http.StatusOK)
	return json.NewEncoder(resp).Encode(doc)
}

func patchPermission(getPerms getPermsFunc, paramName string) echo.HandlerFunc {
	return func(c echo.Context) error {
		instance := middlewares.GetInstance(c)
		current, err := GetPermission(c)
		if err != nil {
			return err
		}

		var patch permissions.Permission
		if _, err = jsonapi.Bind(c.Request(), &patch); err != nil {
			return err
		}

		patchSet := patch.Permissions != nil && len(patch.Permissions) > 0
		patchCodes := len(patch.Codes) > 0

		if patchCodes == patchSet {
			return ErrPatchCodeOrSet
		}

		toPatch, err := getPerms(instance, c.Param(paramName))
		if err != nil {
			return err
		}

		if patchCodes {
			if !current.ParentOf(toPatch) {
				return permissions.ErrNotParent
			}
			toPatch.PatchCodes(patch.Codes)
		}

		if patchSet {
			for _, r := range patch.Permissions {
				if r.Type == "" {
					toPatch.RemoveRule(r)
				} else if current.Permissions.RuleInSubset(r) {
					toPatch.AddRules(r)
				} else {
					return permissions.ErrNotSubset
				}
			}
		}

		if err = couchdb.UpdateDoc(instance, toPatch); err != nil {
			return err
		}

		return jsonapi.Data(c, http.StatusOK, &APIPermission{toPatch}, nil)
	}
}

func revokePermission(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	current, err := GetPermission(c)
	if err != nil {
		return err
	}

	toRevoke, err := permissions.GetByID(instance, c.Param("permdocid"))
	if err != nil {
		return err
	}

	if !current.ParentOf(toRevoke) {
		return permissions.ErrNotParent
	}

	err = toRevoke.Revoke(instance)
	if err != nil {
		return err
	}

	return c.NoContent(http.StatusNoContent)
}

// Routes sets the routing for the permissions service
func Routes(router *echo.Group) {
	// API Routes
	router.POST("", createPermission)
	router.GET("/self", displayPermissions)
	router.POST("/exists", listPermissions)
	router.PATCH("/:permdocid", patchPermission(permissions.GetByID, "permdocid"))
	router.DELETE("/:permdocid", revokePermission)

	router.PATCH("/apps/:slug", patchPermission(permissions.GetForWebapp, "slug"))
	router.PATCH("/konnectors/:slug", patchPermission(permissions.GetForKonnector, "slug"))

	router.GET("/doctype/:doctype/shared-by-link", listByLinkPermissionsByDoctype)
	router.GET("/doctype/:doctype/shared-with-me", listSharedWithMePermissionsByDoctype)
	router.GET("/doctype/:doctype/shared-by-me", listSharedByMePermissionsByDoctype)

	// Legacy routes, kept here for compatibility reasons
	router.GET("/doctype/:doctype/sharedByLink", listByLinkPermissionsByDoctype)
	router.GET("/doctype/:doctype/sharedWithMe", listSharedWithMePermissionsByDoctype)
	router.GET("/doctype/:doctype/sharedWithOthers", listSharedByMePermissionsByDoctype)
}
