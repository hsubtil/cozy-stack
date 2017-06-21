package sharings

import (
	"encoding/json"
	"net/http"
	"net/url"

	"errors"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/sharings"
	"github.com/cozy/cozy-stack/web/data"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/echo"
)

type apiSharing struct {
	*sharings.Sharing
}

func (s *apiSharing) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Sharing)
}
func (s *apiSharing) Links() *jsonapi.LinksList {
	return &jsonapi.LinksList{Self: "/sharings/" + s.SID}
}

// Relationships is part of the jsonapi.Object interface
// It is used to generate the recipients relationships
func (s *apiSharing) Relationships() jsonapi.RelationshipMap {
	l := len(s.RecipientsStatus)
	i := 0

	data := make([]couchdb.DocReference, l)
	for _, rec := range s.RecipientsStatus {
		r := rec.RefRecipient
		data[i] = couchdb.DocReference{ID: r.ID, Type: r.Type}
		i++
	}
	contents := jsonapi.Relationship{Data: data}
	return jsonapi.RelationshipMap{"recipients": contents}
}

// Included is part of the jsonapi.Object interface
func (s *apiSharing) Included() []jsonapi.Object {
	var included []jsonapi.Object
	for _, rec := range s.RecipientsStatus {
		r := rec.GetCachedRecipient()
		if r != nil {
			included = append(included, &apiRecipient{r})
		}
	}
	return included
}

type apiRecipient struct {
	*sharings.Recipient
}

func (r *apiRecipient) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.Recipient)
}

func (r *apiRecipient) Relationships() jsonapi.RelationshipMap { return nil }
func (r *apiRecipient) Included() []jsonapi.Object             { return nil }
func (r *apiRecipient) Links() *jsonapi.LinksList {
	return &jsonapi.LinksList{Self: "/recipients/" + r.RID}
}

var _ jsonapi.Object = (*apiSharing)(nil)
var _ jsonapi.Object = (*apiRecipient)(nil)

// SharingAnswer handles a sharing answer from the sharer side
func SharingAnswer(c echo.Context) error {
	var err error
	var u string

	state := c.QueryParam("state")
	clientID := c.QueryParam("client_id")
	accessCode := c.QueryParam("access_code")

	instance := middlewares.GetInstance(c)

	// The sharing is refused if there is no access code
	if accessCode != "" {
		u, err = sharings.SharingAccepted(instance, state, clientID, accessCode)
	} else {
		u, err = sharings.SharingRefused(instance, state, clientID)
	}
	if err != nil {
		return wrapErrors(err)
	}
	return c.Redirect(http.StatusFound, u)
}

// CreateRecipient adds a sharing Recipient.
func CreateRecipient(c echo.Context) error {

	recipient := new(sharings.Recipient)
	if err := c.Bind(recipient); err != nil {
		return err
	}
	instance := middlewares.GetInstance(c)

	err := sharings.CreateRecipient(instance, recipient)
	if err != nil {
		return wrapErrors(err)
	}

	return jsonapi.Data(c, http.StatusCreated, &apiRecipient{recipient}, nil)
}

// SharingRequest handles a sharing request from the recipient side.
// It creates a temporary sharing document and redirects to the authorize page.
func SharingRequest(c echo.Context) error {
	scope := c.QueryParam("scope")
	state := c.QueryParam("state")
	sharingType := c.QueryParam("sharing_type")
	desc := c.QueryParam("desc")
	clientID := c.QueryParam("client_id")

	instance := middlewares.GetInstance(c)

	sharing, err := sharings.CreateSharingRequest(instance, desc, state, sharingType, scope, clientID)
	if err != nil {
		return wrapErrors(err)
	}
	// Particular case for master-master: register the sharer
	if sharingType == consts.MasterMasterSharing {
		if err = sharings.RegisterSharer(instance, sharing); err != nil {
			return wrapErrors(err)
		}
		if err = sharings.SendClientID(sharing); err != nil {
			return wrapErrors(err)
		}
	}

	redirectAuthorize := instance.PageURL("/auth/authorize", c.QueryParams())
	return c.Redirect(http.StatusSeeOther, redirectAuthorize)
}

// CreateSharing initializes a sharing by creating the associated document,
// registering the sharer as a new OAuth client at each recipient as well as
// sending them a mail invitation.
func CreateSharing(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	sharing := new(sharings.Sharing)
	if err := c.Bind(sharing); err != nil {
		return err
	}

	err := sharings.CreateSharing(instance, sharing)
	if err != nil {
		return wrapErrors(err)
	}

	err = sharings.SendSharingMails(instance, sharing)
	if err != nil {
		return wrapErrors(err)
	}

	return jsonapi.Data(c, http.StatusCreated, &apiSharing{sharing}, nil)
}

// SendSharingMails sends the mails requests for the provided sharing.
func SendSharingMails(c echo.Context) error {
	// Fetch the instance.
	instance := middlewares.GetInstance(c)

	// Fetch the document id and then the sharing document.
	docID := c.Param("id")
	sharing := &sharings.Sharing{}
	err := couchdb.GetDoc(instance, consts.Sharings, docID, sharing)
	if err != nil {
		err = sharings.ErrSharingDoesNotExist
		return wrapErrors(err)
	}

	// Send the mails.
	err = sharings.SendSharingMails(instance, sharing)
	if err != nil {
		return wrapErrors(err)
	}

	return nil
}

// AddSharingRecipient adds an existing recipient to an existing sharing
func AddSharingRecipient(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	// Get sharing doc
	id := c.Param("id")
	sharing := &sharings.Sharing{}
	err := couchdb.GetDoc(instance, consts.Sharings, id, sharing)
	if err != nil {
		err = sharings.ErrSharingDoesNotExist
		return wrapErrors(err)
	}

	// Create recipient, register, and send mail
	ref := couchdb.DocReference{}
	if err = c.Bind(&ref); err != nil {
		return err
	}
	rs := &sharings.RecipientStatus{
		RefRecipient: ref,
	}
	sharing.RecipientsStatus = append(sharing.RecipientsStatus, rs)

	if err = sharings.RegisterRecipient(instance, rs); err != nil {
		return wrapErrors(err)
	}
	if err = sharings.SendSharingMails(instance, sharing); err != nil {
		return wrapErrors(err)
	}
	return jsonapi.Data(c, http.StatusOK, &apiSharing{sharing}, nil)

}

// RecipientRefusedSharing is called when the recipient refused the sharing.
//
// This function will delete the sharing document and inform the sharer by
// returning her the sharing id, the client id (oauth) and nothing else (more
// especially no scope and no access code).
func RecipientRefusedSharing(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	// We collect the information we need to send to the sharer: the client id,
	// the sharing id.
	sharingID := c.FormValue("state")
	if sharingID == "" {
		return wrapErrors(sharings.ErrMissingState)
	}
	clientID := c.FormValue("client_id")
	if clientID == "" {
		return wrapErrors(sharings.ErrNoOAuthClient)
	}

	redirect, err := sharings.RecipientRefusedSharing(instance, sharingID)
	if err != nil {
		return wrapErrors(err)
	}
	u, err := url.ParseRequestURI(redirect)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("state", sharingID)
	q.Set("client_id", clientID)
	u.RawQuery = q.Encode()
	u.Fragment = ""

	return c.Redirect(http.StatusFound, u.String()+"#")
}

// ReceiveClientID receives an OAuth ClientID in a master-master context.
// This is called from a recipient, after he registered himself to the sharer.
// The received clientID is called a HostClientID, as it refers to a client
// created by the sharer, i.e. the host here.
func ReceiveClientID(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	p := &sharings.SharingRequestParams{}
	if err := c.Bind(p); err != nil {
		return err
	}
	sharing, rec, err := sharings.FindSharingRecipient(instance, p.SharingID, p.ClientID)
	if err != nil {
		return wrapErrors(err)
	}
	rec.HostClientID = p.HostClientID
	err = couchdb.UpdateDoc(instance, sharing)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, nil)
}

// getAccessToken asks for an Access Token, from the recipient side.
// It is called in a master-master context, after the sharer received the
// answer from the recipient.
func getAccessToken(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	p := &sharings.SharingRequestParams{}
	if err := c.Bind(p); err != nil {
		return err
	}
	if p.SharingID == "" {
		return wrapErrors(sharings.ErrMissingState)
	}
	if p.Code == "" {
		return wrapErrors(sharings.ErrMissingCode)
	}
	sharing, err := sharings.FindSharing(instance, p.SharingID)
	if err != nil {
		return wrapErrors(err)
	}
	sharer := sharing.Sharer.SharerStatus
	err = sharings.ExchangeCodeForToken(instance, sharing, sharer, p.Code)
	if err != nil {
		return wrapErrors(err)
	}
	// Add triggers on the recipient side for each rule
	if sharing.SharingType == consts.MasterMasterSharing {
		for _, rule := range sharing.Permissions {
			err = sharings.AddTrigger(instance, rule, sharing.SharingID)
			if err != nil {
				return wrapErrors(err)
			}
		}
	}
	return c.JSON(http.StatusOK, nil)
}

// receiveDocument stores a shared document in the Cozy.
//
// If the document to store is a "io.cozy.files" our custom handler will be
// called, otherwise we will redirect to /data.
func receiveDocument(c echo.Context) error {
	ins := middlewares.GetInstance(c)
	sharingID := c.QueryParam(consts.QueryParamSharingID)
	if sharingID == "" {
		return jsonapi.BadRequest(errors.New("Missing sharing id"))
	}

	sharing, errf := sharings.FindSharing(ins, sharingID)
	if errf != nil {
		return errf
	}

	var err error
	switch c.Param("doctype") {
	case consts.Files:
		err = creationWithIDHandler(c, ins, sharing.AppSlug)
	default:
		doctype := c.Param("doctype")
		if doctypeExists(ins, doctype) {
			err = couchdb.CreateDB(ins, doctype)
			if err != nil {
				return err
			}
		}
		err = data.UpdateDoc(c)
	}

	if err != nil {
		return err
	}

	ins.Logger().Debugf("[sharings] Received %s: %s", c.Param("doctype"),
		c.Param("docid"))
	return c.JSON(http.StatusOK, nil)
}

// Depending on the doctype this function does two things:
// 1. If it's a file, its content is updated.
// 2. If it's a JSON document, its content is updated and a check is performed
//    to see if the document is still shared after the update. If not then it is
//    deleted.
func updateDocument(c echo.Context) error {
	ins := middlewares.GetInstance(c)
	ins.Logger().Debugf("[sharings] Updating %s: %s", c.Param("doctype"),
		c.Param("docid"))

	var err error
	switch c.Param("doctype") {
	case consts.Files:
		err = updateFile(c)
	default:
		err = data.UpdateDoc(c)
		if err != nil {
			return err
		}

		ins := middlewares.GetInstance(c)
		err = sharings.RemoveDocumentIfNotShared(ins, c.Param("doctype"),
			c.Param("docid"))
	}

	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, nil)
}

func deleteDocument(c echo.Context) error {
	ins := middlewares.GetInstance(c)
	ins.Logger().Debugf("[sharings] Deleting %s: %s", c.Param("doctype"),
		c.Param("docid"))

	var err error
	switch c.Param("doctype") {
	case consts.Files:
		err = trashHandler(c)

	default:
		err = data.DeleteDoc(c)
	}

	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, nil)
}

// Set sharing to revoked and delete all associated OAuth Clients.
func revokeSharing(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	sharingID := c.Param("id")
	sharing, err := sharings.FindSharing(instance, sharingID)
	if err != nil {
		return jsonapi.NotFound(err)
	}

	// TODO Add permission check

	err = sharings.RevokeSharing(instance, sharing)
	if err != nil {
		return wrapErrors(err)
	}

	return c.JSON(http.StatusOK, nil)
}

func revokeRecipient(c echo.Context) error {
	ins := middlewares.GetInstance(c)

	sharingID := c.Param("id")
	sharing, err := sharings.FindSharing(ins, sharingID)
	if err != nil {
		return jsonapi.NotFound(err)
	}

	// TODO Add permission check
	err = sharings.RevokeRecipient(ins, sharing, c.Param("recipient-client-id"))
	if err != nil {
		return wrapErrors(err)
	}

	return c.NoContent(http.StatusOK)
}

func setDestinationDirectory(c echo.Context) error {
	slug := c.QueryParam(consts.QueryParamAppSlug)
	if slug == "" {
		return jsonapi.BadRequest(errors.New("Missing app slug"))
	}

	// TODO check permissions for app

	doctype := c.QueryParam(consts.QueryParamDocType)
	if doctype == "" {
		return jsonapi.BadRequest(errors.New("Missing doctype"))
	}

	ins := middlewares.GetInstance(c)
	if doctypeExists(ins, doctype) {
		return jsonapi.BadRequest(errors.New("Doctype does not exist"))
	}

	dirID := c.QueryParam(consts.QueryParamDirID)
	if dirID == "" {
		return jsonapi.BadRequest(errors.New("Missing directory id"))
	}

	if _, err := ins.VFS().DirByID(dirID); err != nil {
		return jsonapi.BadRequest(errors.New("Directory does not exist"))
	}

	err := sharings.UpdateApplicationDestinationDirID(ins, slug, doctype, dirID)
	if err != nil {
		return err
	}

	return c.NoContent(http.StatusOK)
}

// Routes sets the routing for the sharing service
func Routes(router *echo.Group) {
	router.POST("/", CreateSharing)
	router.PUT("/:id/recipient", AddSharingRecipient)
	router.PUT("/:id/sendMails", SendSharingMails)
	router.GET("/request", SharingRequest)
	router.GET("/answer", SharingAnswer)
	router.POST("/formRefuse", RecipientRefusedSharing)
	router.POST("/recipient", CreateRecipient)
	router.POST("/access/client", ReceiveClientID)
	router.POST("/access/code", getAccessToken)

	router.DELETE("/:id", revokeSharing)
	router.DELETE("/:id/recipient/:recipient-client-id", revokeRecipient)

	router.DELETE("/files/:file-id/referenced_by", removeReferences)

	router.POST("/app/destinationDirectory", setDestinationDirectory)

	group := router.Group("/doc/:doctype", data.ValidDoctype)
	group.POST("/:docid", receiveDocument)
	group.PUT("/:docid", updateDocument)
	group.PATCH("/:docid", patchDirOrFile)
	group.DELETE("/:docid", deleteDocument)
}

// wrapErrors returns a formatted error
func wrapErrors(err error) error {
	switch err {
	case sharings.ErrBadSharingType:
		return jsonapi.InvalidParameter("sharing_type", err)
	case sharings.ErrRecipientDoesNotExist:
		return jsonapi.NotFound(err)
	case sharings.ErrMissingScope, sharings.ErrMissingState, sharings.ErrRecipientHasNoURL,
		sharings.ErrRecipientHasNoEmail:
		return jsonapi.BadRequest(err)
	case sharings.ErrSharingDoesNotExist, sharings.ErrPublicNameNotDefined:
		return jsonapi.NotFound(err)
	case sharings.ErrMailCouldNotBeSent:
		return jsonapi.InternalServerError(err)
	case sharings.ErrNoOAuthClient:
		return jsonapi.BadRequest(err)
	}
	return err
}

func doctypeExists(ins *instance.Instance, doctype string) bool {
	_, err := couchdb.DBStatus(ins, doctype)
	return err == nil
}
