package bitwarden

import (
	"encoding/json"
	"net/http"

	"github.com/cozy/cozy-stack/model/bitwarden"
	"github.com/cozy/cozy-stack/model/bitwarden/settings"
	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/model/permission"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/metadata"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo/v4"
)

// https://github.com/bitwarden/jslib/blob/master/common/src/models/request/organizationCreateRequest.ts
type organizationRequest struct {
	Name           string `json:"name"`
	Key            string `json:"key"`
	CollectionName string `json:"collectionName"`
}

func (r *organizationRequest) toOrganization(inst *instance.Instance) *bitwarden.Organization {
	md := metadata.New()
	md.DocTypeVersion = bitwarden.DocTypeVersion
	return &bitwarden.Organization{
		Name: r.Name,
		Members: map[string]bitwarden.OrgMember{
			inst.Domain: {
				UserID:    inst.ID(),
				PublicKey: r.Key,
				Status:    bitwarden.OrgMemberConfirmed,
				Owner:     true,
			},
		},
		Collection: bitwarden.Collection{
			Name: r.CollectionName,
		},
		Metadata: *md,
	}
}

// https://github.com/bitwarden/jslib/blob/master/common/src/models/response/profileOrganizationResponse.ts
type organizationResponse struct {
	ID             string  `json:"Id"`
	Identifier     *string `json:"Identifier"`
	Name           string  `json:"Name"`
	Key            string  `json:"Key"`
	Email          string  `json:"BillingEmail"`
	Plan           string  `json:"Plan"`
	PlanType       int     `json:"PlanType"`
	Seats          int     `json:"Seats"`
	MaxCollections int     `json:"MaxCollections"`
	MaxStorage     int     `json:"MaxStorageGb"`
	SelfHost       bool    `json:"SelfHost"`
	Use2fa         bool    `json:"Use2fa"`
	UseDirectory   bool    `json:"UseDirectory"`
	UseEvents      bool    `json:"UseEvents"`
	UseGroups      bool    `json:"UseGroups"`
	UseTotp        bool    `json:"UseTotp"`
	UseAPI         bool    `json:"UseApi"`
	UsePolicies    bool    `json:"UsePolicies"`
	UseSSO         bool    `json:"UseSSO"`
	UseResetPass   bool    `json:"UseResetPassword"`
	HasKeys        bool    `json:"HasPublicAndPrivateKeys"`
	ResetPass      bool    `json:"ResetPasswordEnrolled"`
	Premium        bool    `json:"UsersGetPremium"`
	Enabled        bool    `json:"Enabled"`
	Status         int     `json:"Status"`
	Type           int     `json:"Type"`
	Object         string  `json:"Object"`
}

func newOrganizationResponse(inst *instance.Instance, org *bitwarden.Organization) *organizationResponse {
	m := org.Members[inst.Domain]
	email := inst.PassphraseSalt()
	return &organizationResponse{
		ID:             org.ID(),
		Identifier:     nil, // Not supported by us
		Name:           org.Name,
		Key:            m.PublicKey,
		Email:          string(email),
		Plan:           "TeamsAnnually",
		PlanType:       9,  // TeamsAnnually plan
		Seats:          10, // The value doesn't matter
		MaxCollections: 1,
		MaxStorage:     1,
		SelfHost:       true,
		Use2fa:         true,
		UseDirectory:   false,
		UseEvents:      false,
		UseGroups:      false,
		UseTotp:        true,
		UseAPI:         false,
		UsePolicies:    false,
		UseSSO:         false,
		UseResetPass:   false,
		HasKeys:        false, // The public/private keys are used for the Admin Reset Password feature, not implemented by us
		ResetPass:      false,
		Premium:        true,
		Enabled:        true,
		Status:         int(m.Status),
		Type:           2, // User
		Object:         "profileOrganization",
	}
}

// https://github.com/bitwarden/jslib/blob/master/common/src/models/response/collectionResponse.ts
type collectionResponse struct {
	ID             string `json:"Id"`
	OrganizationID string `json:"OrganizationId"`
	Name           string `json:"Name"`
	Object         string `json:"Object"`
}

func newCollectionResponse(coll *bitwarden.Collection, orgID string) *collectionResponse {
	return &collectionResponse{
		ID:             coll.ID(),
		OrganizationID: orgID,
		Name:           coll.Name,
		Object:         "collection",
	}
}

// CreateOrganization is the route used to create an organization (with a
// collection).
func CreateOrganization(c echo.Context) error {
	inst := middlewares.GetInstance(c)
	if err := middlewares.AllowWholeType(c, permission.POST, consts.BitwardenOrganizations); err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{
			"error": "invalid token",
		})
	}

	var req organizationRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "invalid JSON",
		})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "missing name",
		})
	}

	org := req.toOrganization(inst)
	collID, err := couchdb.UUID(inst)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}
	org.Collection.DocID = collID
	if err := couchdb.CreateDoc(inst, org); err != nil {
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}

	_ = settings.UpdateRevisionDate(inst, nil)
	res := newOrganizationResponse(inst, org)
	return c.JSON(http.StatusOK, res)
}

// GetOrganization is the route for getting information about an organization.
func GetOrganization(c echo.Context) error {
	inst := middlewares.GetInstance(c)

	if err := middlewares.AllowWholeType(c, permission.GET, consts.BitwardenOrganizations); err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{
			"error": "invalid token",
		})
	}

	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusNotFound, echo.Map{
			"error": "missing id",
		})
	}

	org := &bitwarden.Organization{}
	if err := couchdb.GetDoc(inst, consts.BitwardenOrganizations, id, org); err != nil {
		if couchdb.IsNotFoundError(err) {
			return c.JSON(http.StatusNotFound, echo.Map{
				"error": "not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}

	res := newOrganizationResponse(inst, org)
	return c.JSON(http.StatusOK, res)
}

type collectionsList struct {
	Data   []*collectionResponse `json:"Data"`
	Object string                `json:"Object"`
}

// GetCollections is the route for getting information about the collections
// inside an organization.
func GetCollections(c echo.Context) error {
	inst := middlewares.GetInstance(c)

	if err := middlewares.AllowWholeType(c, permission.GET, consts.BitwardenOrganizations); err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{
			"error": "invalid token",
		})
	}

	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusNotFound, echo.Map{
			"error": "missing id",
		})
	}

	org := &bitwarden.Organization{}
	if err := couchdb.GetDoc(inst, consts.BitwardenOrganizations, id, org); err != nil {
		if couchdb.IsNotFoundError(err) {
			return c.JSON(http.StatusNotFound, echo.Map{
				"error": "not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}

	coll := newCollectionResponse(&org.Collection, org.ID())
	res := &collectionsList{Object: "list"}
	res.Data = []*collectionResponse{coll}
	return c.JSON(http.StatusOK, res)
}

// https://github.com/bitwarden/jslib/blob/master/common/src/models/request/passwordVerificationRequest.ts
type passwordVerificationRequest struct {
	Hash string `json:"masterPasswordHash"`
}

// DeleteOrganization is the route for deleting an organization by its owner.
func DeleteOrganization(c echo.Context) error {
	inst := middlewares.GetInstance(c)
	if err := middlewares.AllowWholeType(c, permission.DELETE, consts.BitwardenOrganizations); err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{
			"error": "invalid token",
		})
	}

	var verification passwordVerificationRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&verification); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "invalid JSON",
		})
	}
	if err := lifecycle.CheckPassphrase(inst, []byte(verification.Hash)); err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{
			"error": "invalid password",
		})
	}

	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusNotFound, echo.Map{
			"error": "missing id",
		})
	}

	org := &bitwarden.Organization{}
	if err := couchdb.GetDoc(inst, consts.BitwardenOrganizations, id, org); err != nil {
		if couchdb.IsNotFoundError(err) {
			return c.JSON(http.StatusNotFound, echo.Map{
				"error": "not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}

	if m := org.Members[inst.Domain]; !m.Owner {
		return c.JSON(http.StatusUnauthorized, echo.Map{
			"error": "only the Owner can call this endpoint",
		})
	}

	if err := org.Delete(inst); err != nil {
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": err.Error(),
		})
	}

	_ = settings.UpdateRevisionDate(inst, nil)
	return c.NoContent(http.StatusOK)
}
