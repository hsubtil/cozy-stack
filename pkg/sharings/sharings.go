package sharings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cozy/cozy-stack/client/auth"
	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/couchdb/mango"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/oauth"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/pkg/scheduler"
	"github.com/cozy/cozy-stack/pkg/stack"
	"github.com/cozy/cozy-stack/pkg/utils"
	"github.com/cozy/cozy-stack/pkg/vfs"
)

// Sharing contains all the information about a sharing.
// For clarification:
// * `SID` is generated by CouchDB and is the id of the sharing document.
// * `SharingID` is the actual id of the Sharing, generated by the stack.
type Sharing struct {
	SID         string `json:"_id,omitempty"`
	SRev        string `json:"_rev,omitempty"`
	Desc        string `json:"desc,omitempty"`
	SharingID   string `json:"sharing_id,omitempty"`
	SharingType string `json:"sharing_type"`
	AppSlug     string `json:"app_slug"`
	Owner       bool   `json:"owner"`
	Revoked     bool   `json:"revoked,omitempty"`

	Sharer           Sharer             `json:"sharer,omitempty"`
	Permissions      permissions.Set    `json:"permissions,omitempty"`
	RecipientsStatus []*RecipientStatus `json:"recipients,omitempty"`
}

// Sharer contains the information about the sharer from the recipient's
// perspective.
//
// ATTENTION: This structure will only be filled by the recipients as it is
// recipient specific. The `ClientID` is different for each recipient and only
// known by them.
type Sharer struct {
	URL          string           `json:"url"`
	SharerStatus *RecipientStatus `json:"sharer_status"`
}

// SharingRequestParams contains the basic information required to request
// a sharing party
type SharingRequestParams struct {
	SharingID    string `json:"state"`
	ClientID     string `json:"client_id"`
	HostClientID string `json:"host_client_id"`
	Code         string `json:"code"`
}

// SharingMessage describes the message that will be transmitted to the workers
// "sharing_update" and "share_data".
type SharingMessage struct {
	SharingID string           `json:"sharing_id"`
	Rule      permissions.Rule `json:"rule"`
}

// RecipientInfo describes the recipient information that will be transmitted to
// the sharing workers.
type RecipientInfo struct {
	URL         string
	Scheme      string
	Client      auth.Client
	AccessToken auth.AccessToken
}

// WorkerData describes the basic data the workers need to process the events
// they will receive.
type WorkerData struct {
	DocID      string
	SharingID  string
	Selector   string
	Values     []string
	DocType    string
	Recipients []*RecipientInfo
}

// ID returns the sharing qualified identifier
func (s *Sharing) ID() string { return s.SID }

// Rev returns the sharing revision
func (s *Sharing) Rev() string { return s.SRev }

// DocType returns the sharing document type
func (s *Sharing) DocType() string { return consts.Sharings }

// Clone implements couchdb.Doc
func (s *Sharing) Clone() couchdb.Doc {
	cloned := *s
	if s.RecipientsStatus != nil {
		var rStatus []*RecipientStatus
		cloned.RecipientsStatus = rStatus
		for _, v := range s.RecipientsStatus {
			rec := *v
			cloned.RecipientsStatus = append(cloned.RecipientsStatus, &rec)
		}
	}
	if s.Sharer.SharerStatus != nil {
		sharerStatus := *s.Sharer.SharerStatus
		cloned.Sharer.SharerStatus = &sharerStatus
	}
	return &cloned
}

// SetID changes the sharing qualified identifier
func (s *Sharing) SetID(id string) { s.SID = id }

// SetRev changes the sharing revision
func (s *Sharing) SetRev(rev string) { s.SRev = rev }

// RecStatus returns the sharing recipients status
func (s *Sharing) RecStatus(db couchdb.Database) ([]*RecipientStatus, error) {
	var rStatus []*RecipientStatus

	for _, rec := range s.RecipientsStatus {
		recipient, err := GetRecipient(db, rec.RefRecipient.ID)
		if err != nil {
			return nil, err
		}
		rec.recipient = recipient
		rStatus = append(rStatus, rec)
	}

	s.RecipientsStatus = rStatus
	return rStatus, nil
}

// Recipients returns the sharing recipients
func (s *Sharing) Recipients(db couchdb.Database) ([]*Recipient, error) {
	var recipients []*Recipient

	for _, rec := range s.RecipientsStatus {
		recipient, err := GetRecipient(db, rec.RefRecipient.ID)
		if err != nil {
			return nil, err
		}
		rec.recipient = recipient
		recipients = append(recipients, recipient)
	}

	return recipients, nil
}

// GetSharingRecipientFromClientID returns the Recipient associated with the
// given clientID.
func (s *Sharing) GetSharingRecipientFromClientID(db couchdb.Database, clientID string) (*RecipientStatus, error) {
	for _, recStatus := range s.RecipientsStatus {
		if recStatus.Client.ClientID == clientID {
			return recStatus, nil
		}
	}
	return nil, ErrRecipientDoesNotExist
}

// GetRecipientStatusFromRecipientID returns the RecipientStatus associated with the
// given recipient ID.
func (s *Sharing) GetRecipientStatusFromRecipientID(db couchdb.Database, recID string) (*RecipientStatus, error) {
	for _, recStatus := range s.RecipientsStatus {
		if recStatus.recipient == nil {
			r, err := GetRecipient(db, recStatus.RefRecipient.ID)
			if err != nil {
				return nil, err
			}
			recStatus.recipient = r
		}
		if recStatus.recipient.ID() == recID {
			return recStatus, nil
		}
	}
	return nil, ErrRecipientDoesNotExist
}

// CheckSharingType returns an error if the sharing type is incorrect
func CheckSharingType(sharingType string) error {
	switch sharingType {
	case consts.OneShotSharing, consts.MasterSlaveSharing, consts.MasterMasterSharing:
		return nil
	}
	return ErrBadSharingType
}

// FindSharing retrieves a sharing document gfrom its sharingID
func FindSharing(db couchdb.Database, sharingID string) (*Sharing, error) {
	var res []Sharing
	err := couchdb.FindDocs(db, consts.Sharings, &couchdb.FindRequest{
		UseIndex: "by-sharing-id",
		Selector: mango.Equal("sharing_id", sharingID),
	}, &res)
	if err != nil {
		return nil, err
	}
	if len(res) < 1 {
		return nil, ErrSharingDoesNotExist
	} else if len(res) > 2 {
		return nil, ErrSharingIDNotUnique
	}
	return &res[0], nil
}

// FindSharingRecipient retrieve a sharing recipient from its clientID and sharingID
func FindSharingRecipient(db couchdb.Database, sharingID, clientID string) (*Sharing, *RecipientStatus, error) {
	sharing, err := FindSharing(db, sharingID)
	if err != nil {
		return nil, nil, err
	}
	sRec, err := sharing.GetSharingRecipientFromClientID(db, clientID)
	if err != nil {
		return nil, nil, err
	}
	if sRec == nil {
		return nil, nil, ErrRecipientDoesNotExist
	}
	return sharing, sRec, nil
}

// AddTrigger creates a new trigger on the updates of the shared documents
func AddTrigger(instance *instance.Instance, rule permissions.Rule, sharingID string) error {
	sched := stack.GetScheduler()

	var eventArgs string
	if rule.Selector != "" {
		eventArgs = rule.Type + ":CREATED,UPDATED,DELETED:" +
			strings.Join(rule.Values, ",") + ":" + rule.Selector
	} else {
		eventArgs = rule.Type + ":UPDATED,DELETED:" +
			strings.Join(rule.Values, ",")
	}

	msg := SharingMessage{
		SharingID: sharingID,
		Rule:      rule,
	}

	workerArgs, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	t, err := scheduler.NewTrigger(&scheduler.TriggerInfos{
		Type:       "@event",
		WorkerType: consts.WorkerTypeSharingUpdates,
		Domain:     instance.Domain,
		Arguments:  eventArgs,
		Message: &jobs.Message{
			Type: jobs.JSONEncoding,
			Data: workerArgs,
		},
	})
	if err != nil {
		return err
	}
	instance.Logger().Infof("[sharings] AddTrigger: trigger created for "+
		"sharing %s", sharingID)

	return sched.Add(t)
}

// ShareDoc shares the documents specified in the Sharing structure to the
// specified recipient
func ShareDoc(instance *instance.Instance, sharing *Sharing, recStatus *RecipientStatus) error {
	for _, rule := range sharing.Permissions {
		if len(rule.Values) == 0 {
			return nil
		}
		docType := rule.Type
		// Trigger the updates if the sharing is not one-shot
		if sharing.SharingType != consts.OneShotSharing {
			err := AddTrigger(instance, rule, sharing.SharingID)
			if err != nil {
				return err
			}
		}

		var values []string

		// Dynamic sharing
		if rule.Selector != "" {
			// Particular case for referenced_by: use the existing view
			if rule.Selector == consts.SelectorReferencedBy {
				for _, val := range rule.Values {
					// A referenced_by selector implies Values in the form
					// ["refDocType/refId"]
					parts := strings.Split(val, permissions.RefSep)
					if len(parts) != 2 {
						return ErrBadPermission
					}
					refType := parts[0]
					refID := parts[1]
					req := &couchdb.ViewRequest{
						Key:    []string{refType, refID},
						Reduce: false,
					}
					var res couchdb.ViewResponse
					err := couchdb.ExecView(instance,
						consts.FilesReferencedByView, req, &res)
					if err != nil {
						return err
					}
					for _, row := range res.Rows {
						values = append(values, row.ID)
					}

				}
			} else {

				// Create index based on selector to retrieve documents to share
				indexName := "by-" + rule.Selector
				index := mango.IndexOnFields(docType, indexName,
					[]string{rule.Selector})
				err := couchdb.DefineIndex(instance, index)
				if err != nil {
					return err
				}

				var docs []couchdb.JSONDoc

				// Request the index for all values
				// NOTE: this is not efficient in case of many Values
				// We might consider a map-reduce approach in case of bottleneck
				for _, val := range rule.Values {
					err = couchdb.FindDocs(instance, docType,
						&couchdb.FindRequest{
							UseIndex: indexName,
							Selector: mango.Equal(rule.Selector, val),
						}, &docs)
					if err != nil {
						return err
					}
					// Save returned doc ids
					for _, d := range docs {
						values = append(values, d.ID())
					}
				}
			}
		} else {
			values = rule.Values
		}

		// Create a sharedata worker for each doc to send
		for _, val := range values {
			domain, scheme, err := recStatus.recipient.ExtractDomainAndScheme()
			if err != nil {
				return err
			}
			rec := &RecipientInfo{
				URL:         domain,
				Scheme:      scheme,
				AccessToken: recStatus.AccessToken,
				Client:      recStatus.Client,
			}

			workerMsg, err := jobs.NewMessage(jobs.JSONEncoding, WorkerData{
				DocID:      val,
				SharingID:  sharing.SharingID,
				Selector:   rule.Selector,
				Values:     rule.Values,
				DocType:    docType,
				Recipients: []*RecipientInfo{rec},
			})
			if err != nil {
				return err
			}
			_, err = stack.GetBroker().PushJob(&jobs.JobRequest{
				Domain:     instance.Domain,
				WorkerType: "sharedata",
				Options:    nil,
				Message:    workerMsg,
			})
			if err != nil {
				return err
			}

		}
	}
	return nil
}

// SharingAccepted handles an accepted sharing on the sharer side and returns
// the redirect url.
func SharingAccepted(instance *instance.Instance, state, clientID, accessCode string) (string, error) {
	sharing, recStatus, err := FindSharingRecipient(instance, state, clientID)
	if err != nil {
		return "", err
	}
	// Update the sharing status and asks the recipient for access
	recStatus.Status = consts.SharingStatusAccepted
	err = ExchangeCodeForToken(instance, sharing, recStatus, accessCode)
	if err != nil {
		return "", err
	}

	// Particular case for master-master sharing: the recipients needs credentials
	if sharing.SharingType == consts.MasterMasterSharing {
		err = SendCode(instance, sharing, recStatus)
		if err != nil {
			return "", err
		}
	}
	// Share all the documents with the recipient
	err = ShareDoc(instance, sharing, recStatus)

	// Redirect the recipient after acceptation
	redirect := recStatus.recipient.URL
	return redirect, err
}

// SharingRefused handles a rejected sharing on the sharer side and returns the
// redirect url.
func SharingRefused(db couchdb.Database, state, clientID string) (string, error) {
	sharing, recStatus, errFind := FindSharingRecipient(db, state, clientID)
	if errFind != nil {
		return "", errFind
	}
	recStatus.Status = consts.SharingStatusRefused

	// Persists the changes in the database.
	err := couchdb.UpdateDoc(db, sharing)
	if err != nil {
		return "", err
	}

	// Sanity check: as the `recipient` is private if the document is fetched
	// from the database it is nil.
	err = recStatus.GetRecipient(db)
	if err != nil {
		return "", nil
	}

	redirect := recStatus.recipient.URL
	return redirect, err
}

// RecipientRefusedSharing deletes the sharing document and returns the address
// at which the sharer can be informed for the refusal.
func RecipientRefusedSharing(db couchdb.Database, sharingID string) (string, error) {
	// We get the sharing document through its sharing id…
	var res []Sharing
	err := couchdb.FindDocs(db, consts.Sharings, &couchdb.FindRequest{
		Selector: mango.Equal("sharing_id", sharingID),
	}, &res)
	if err != nil {
		return "", err
	} else if len(res) < 1 {
		return "", ErrSharingDoesNotExist
	} else if len(res) > 1 {
		return "", ErrSharingIDNotUnique
	}
	sharing := &res[0]

	// … and we delete it because it is no longer needed.
	err = couchdb.DeleteDoc(db, sharing)
	if err != nil {
		return "", err
	}

	// We return where to send the refusal.
	u := fmt.Sprintf("%s/sharings/answer", sharing.Sharer.URL)
	return u, nil
}

// CreateSharingRequest checks fields integrity and creates a sharing document
// for an incoming sharing request
func CreateSharingRequest(db couchdb.Database, desc, state, sharingType, scope, clientID string) (*Sharing, error) {
	if state == "" {
		return nil, ErrMissingState
	}
	if err := CheckSharingType(sharingType); err != nil {
		return nil, err
	}
	if scope == "" {
		return nil, ErrMissingScope
	}
	if clientID == "" {
		return nil, ErrNoOAuthClient
	}
	permissions, err := permissions.UnmarshalScopeString(scope)
	if err != nil {
		return nil, err
	}

	sharerClient := &oauth.Client{}
	err = couchdb.GetDoc(db, consts.OAuthClients, clientID, sharerClient)
	if err != nil {
		return nil, ErrNoOAuthClient
	}
	sr := &RecipientStatus{
		HostClientID: clientID,
		recipient:    &Recipient{URL: sharerClient.ClientURI},
	}
	sharer := Sharer{
		URL:          sharerClient.ClientURI,
		SharerStatus: sr,
	}

	sharing := &Sharing{
		SharingType: sharingType,
		SharingID:   state,
		Permissions: permissions,
		Owner:       false,
		Desc:        desc,
		Sharer:      sharer,
		Revoked:     false,
	}

	err = couchdb.CreateDoc(db, sharing)
	return sharing, err
}

// RegisterRecipient registers a sharing recipient
func RegisterRecipient(instance *instance.Instance, rs *RecipientStatus) error {
	err := rs.Register(instance)
	if err != nil {
		if rs.recipient != nil {
			instance.Logger().Errorf("sharing] Could not register at %v : %v",
				rs.recipient.URL, err)
			rs.Status = consts.SharingStatusUnregistered
		} else {
			instance.Logger().Error("[sharing] Sharing recipient not found")
		}
	} else {
		rs.Status = consts.SharingStatusMailNotSent
	}
	return err
}

// RegisterSharer registers the sharer for master-master sharing
func RegisterSharer(instance *instance.Instance, sharing *Sharing) error {
	// Register the sharer as a recipient
	sharer := sharing.Sharer
	doc := &Recipient{
		URL: sharer.URL,
	}
	err := CreateRecipient(instance, doc)
	if err != nil {
		return err
	}
	ref := couchdb.DocReference{
		ID:   doc.ID(),
		Type: consts.Recipients,
	}
	sharer.SharerStatus.RefRecipient = ref
	err = sharer.SharerStatus.Register(instance)
	if err != nil {
		instance.Logger().Error("[sharing] Could not register at "+sharer.URL+" ", err)
		sharer.SharerStatus.Status = consts.SharingStatusUnregistered
	}
	return couchdb.UpdateDoc(instance, sharing)
}

// SendClientID sends the registered clientId to the sharer
func SendClientID(sharing *Sharing) error {
	domain, scheme, err := sharing.Sharer.SharerStatus.recipient.ExtractDomainAndScheme()
	if err != nil {
		return nil
	}
	path := "/sharings/access/client"
	newClientID := sharing.Sharer.SharerStatus.Client.ClientID
	params := SharingRequestParams{
		SharingID:    sharing.SharingID,
		ClientID:     sharing.Sharer.SharerStatus.HostClientID,
		HostClientID: newClientID,
	}
	return Request("POST", domain, scheme, path, params)
}

// SendCode generates and sends an OAuth code to a recipient
func SendCode(instance *instance.Instance, sharing *Sharing, recStatus *RecipientStatus) error {
	scope, err := sharing.Permissions.MarshalScopeString()
	if err != nil {
		return err
	}
	clientID := recStatus.Client.ClientID
	access, err := oauth.CreateAccessCode(instance, clientID, scope)
	if err != nil {
		return err
	}
	domain, scheme, err := recStatus.recipient.ExtractDomainAndScheme()
	if err != nil {
		return nil
	}
	path := "/sharings/access/code"
	params := SharingRequestParams{
		SharingID: sharing.SharingID,
		Code:      access.Code,
	}
	return Request("POST", domain, scheme, path, params)
}

// ExchangeCodeForToken asks for an AccessToken based on an AccessCode
func ExchangeCodeForToken(instance *instance.Instance, sharing *Sharing, recStatus *RecipientStatus, code string) error {
	// Fetch the access and refresh tokens.
	access, err := recStatus.getAccessToken(instance, code)
	if err != nil {
		return err
	}
	recStatus.AccessToken = *access
	return couchdb.UpdateDoc(instance, sharing)
}

// Request is a utility method to send request to remote sharing party
func Request(method, domain, scheme, path string, params interface{}) error {
	var body io.Reader
	var err error
	if params != nil {
		body, err = request.WriteJSON(params)
		if err != nil {
			return nil
		}
	}
	_, err = request.Req(&request.Options{
		Domain: domain,
		Scheme: scheme,
		Method: method,
		Path:   path,
		Headers: request.Headers{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
		Body: body,
	})
	return err
}

// CreateSharing checks the sharing, creates the document in
// base and starts the sharing process by registering the sharer at each
// recipient as a new OAuth client.
func CreateSharing(instance *instance.Instance, sharing *Sharing) error {
	sharingType := sharing.SharingType
	if err := CheckSharingType(sharingType); err != nil {
		return err
	}

	// Fetch the recipients in the database and populate RecipientsStatus.
	recStatus, err := sharing.RecStatus(instance)
	if err != nil {
		return err
	}

	// Register the sharer at each recipient and set the status accordingly.
	for _, rs := range recStatus {
		// If the URL is not known, a discovery mail will be sent later
		if rs.recipient.URL != "" {
			RegisterRecipient(instance, rs)
		}
	}

	sharing.Owner = true
	sharing.SharingID = utils.RandomString(32)

	return couchdb.CreateDoc(instance, sharing)
}

// RevokeSharing revokes the sharing and deletes all the OAuth client and
// triggers associated with it.
//
// Revoking a sharing consists of setting the field `Revoked` to `true`.
// When the sharing is of type "master-master" both recipients and sharer have
// trigger(s) and OAuth client(s) to delete.
// In every other cases only the sharer has trigger(s) to delete and only the
// recipients have an OAuth client to delete.
func RevokeSharing(ins *instance.Instance, sharing *Sharing) error {
	sharing.Revoked = true

	var err error
	if sharing.Owner {
		if sharing.SharingType == consts.MasterMasterSharing {
			for _, recipient := range sharing.RecipientsStatus {
				err = deleteOAuthClient(ins, recipient.HostClientID)
				if err != nil {
					return err
				}

				recipient.HostClientID = ""
			}
		}

		err = removeSharingTriggers(ins, sharing.SharingID)
		if err != nil {
			return err
		}

	} else {
		err = deleteOAuthClient(ins, sharing.Sharer.SharerStatus.HostClientID)
		if err != nil {
			return err
		}

		sharing.Sharer.SharerStatus.HostClientID = ""

		if sharing.SharingType == consts.MasterMasterSharing {
			err = removeSharingTriggers(ins, sharing.SharingID)
			if err != nil {
				return err
			}
		}
	}

	err = couchdb.UpdateDoc(ins, sharing)
	return err
}

// RemoveDocumentIfNotShared checks if the given document is still shared and
// removes it if not.
//
// To check if a document is still shared all the permissions associated with
// sharings that apply to its doctype are fetched. If at least one permission
// "matches" then the document is kept.
func RemoveDocumentIfNotShared(ins *instance.Instance, doctype, docID string) error {
	fs := ins.VFS()

	// TODO Using a cursor might lead to inconsistency. Change it if the need
	// arises.
	cursor := couchdb.NewSkipCursor(10000, 0)

	doc := couchdb.JSONDoc{}
	err := couchdb.GetDoc(ins, doctype, docID, &doc)
	if err != nil {
		return err
	}

	// The doctype is not always set, at least in the tests, and is required in
	// order to delete the document.
	if doc.DocType() == "" {
		doc.Type = doctype
	}

	for {
		perms, errg := permissions.GetSharedWithMePermissionsByDoctype(ins,
			doctype, cursor)
		if errg != nil {
			return errg
		}

		for _, perm := range perms {
			for _, rule := range perm.Permissions {
				if rule.ValuesValid(doc) {
					return nil
				}
			}
		}

		if !cursor.HasMore() {
			break
		}
	}

	switch doctype {
	case consts.Files:
		dirDoc, fileDoc, errd := fs.DirOrFileByID(docID)
		if errd != nil {
			return errd
		}

		if dirDoc != nil {
			_, errt := vfs.TrashDir(fs, dirDoc)
			return errt
		}

		_, errt := vfs.TrashFile(fs, fileDoc)
		return errt

	default:
		return couchdb.DeleteDoc(ins, doc)
	}
}

// RevokeRecipient revokes a recipient from the given sharing.
//
// If there are no more recipients the sharing is revoked and the corresponding
// trigger is deleted.
func RevokeRecipient(ins *instance.Instance, sharing *Sharing, recipientClientID string) error {
	var removed, hasRecipient bool
	for _, recipient := range sharing.RecipientsStatus {
		if recipient.HostClientID == recipientClientID {
			err := deleteOAuthClient(ins, recipientClientID)
			if err != nil {
				return err
			}

			recipient.Status = consts.SharingStatusRevoked
			recipient.HostClientID = ""
			removed = true
		} else {
			if recipient.Status != consts.SharingStatusRevoked &&
				recipient.Status != consts.SharingStatusRefused {
				hasRecipient = true
			}
		}

		if removed && hasRecipient {
			break
		}
	}

	if !removed {
		ins.Logger().Errorf("[sharing] RevokeRecipient: Recipient %s is not "+
			"in sharing: %s", recipientClientID, sharing.SharingID)
		return ErrRecipientDoesNotExist
	}

	if !hasRecipient {
		sharing.Revoked = true
		err := removeSharingTriggers(ins, sharing.SharingID)
		if err != nil {
			ins.Logger().Errorf("[sharings] RevokeRecipient: Could not remove "+
				"triggers for sharing %s: %s", sharing.SharingID, err)
		}
	}

	return couchdb.UpdateDoc(ins, sharing)
}

func removeSharingTriggers(ins *instance.Instance, sharingID string) error {
	sched := stack.GetScheduler()
	ts, err := sched.GetAll(ins.Domain)
	if err != nil {
		ins.Logger().Errorf("[sharings] removeSharingTriggers: Could not get "+
			"the list of triggers: %s", err)
		return err
	}

	for _, trigger := range ts {
		infos := trigger.Infos()
		if infos.WorkerType == consts.WorkerTypeSharingUpdates {
			msg := SharingMessage{}
			errm := infos.Message.Unmarshal(&msg)
			if errm != nil {
				ins.Logger().Errorf("[sharings] removeSharingTriggers: An "+
					"error occurred while trying to unmarshal trigger "+
					"message: %s", errm)
				continue
			}

			if msg.SharingID == sharingID {
				errd := sched.Delete(ins.Domain, trigger.ID())
				if errd != nil {
					ins.Logger().Errorf("[sharings] removeSharingTriggers: "+
						"Could not delete trigger %s: %s", trigger.ID(), errd)
				}
			}
		}
	}

	return nil
}

func deleteOAuthClient(ins *instance.Instance, id string) error {
	client, err := oauth.FindClient(ins, id)
	if err != nil {
		ins.Logger().Errorf("[sharings] deleteOAuthClient: Could not "+
			"find OAuth client %s: %s", id, err)
		return err
	}
	crErr := client.Delete(ins)
	if crErr != nil {
		ins.Logger().Errorf("[sharings] deleteOAuthClient: Could not "+
			"delete OAuth client %s: %s", id, err)
		return errors.New(crErr.Error)
	}
	return nil
}

var (
	_ couchdb.Doc = &Sharing{}
)
