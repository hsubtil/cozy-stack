package sharing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/job"
	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/mail"
)

// SendInvitations sends invitation mails to the recipients that were in the
// mail-not-sent status (owner only)
func (s *Sharing) SendInvitations(inst *instance.Instance, codes map[string]string) error {
	if !s.Owner {
		return ErrInvalidSharing
	}
	if len(s.Members) != len(s.Credentials)+1 {
		return ErrInvalidSharing
	}
	sharer, desc := s.getSharerAndDescription(inst)
	shortcut := newShortcutMsg(s, inst, desc)

	for i, m := range s.Members {
		if i == 0 || m.Status != MemberStatusMailNotSent { // i == 0 is for the owner
			continue
		}
		link := m.InvitationLink(inst, s, s.Credentials[i-1].State, codes)
		if m.Instance != "" {
			shortcut.addLink(link)
			if err := m.SendShortcut(inst, shortcut); err == nil {
				continue
			}
		}
		if m.Email == "" {
			return ErrInvitationNotSent
		}
		if err := m.SendMail(inst, s, sharer, desc, link); err != nil {
			inst.Logger().WithField("nspace", "sharing").
				Errorf("Can't send email for %#v: %s", m.Email, err)
			return ErrInvitationNotSent
		}
		s.Members[i].Status = MemberStatusPendingInvitation
	}

	return couchdb.UpdateDoc(inst, s)
}

// SendInvitationsToMembers sends mails from a recipient (open_sharing) to
// their contacts to invite them
func (s *Sharing) SendInvitationsToMembers(inst *instance.Instance, members []Member, states map[string]string) error {
	sharer, desc := s.getSharerAndDescription(inst)
	shortcut := newShortcutMsg(s, inst, desc)

	for _, m := range members {
		key := m.Email
		if key == "" {
			key = m.Instance
		}
		link := m.InvitationLink(inst, s, states[key], nil)
		if m.Instance != "" {
			shortcut.addLink(link)
			if err := m.SendShortcut(inst, shortcut); err == nil {
				continue
			}
		}
		if m.Email == "" {
			return ErrInvitationNotSent
		}
		if err := m.SendMail(inst, s, sharer, desc, link); err != nil {
			inst.Logger().WithField("nspace", "sharing").
				Errorf("Can't send email for %#v: %s", m.Email, err)
			return ErrInvitationNotSent
		}
		for i, member := range s.Members {
			if i == 0 {
				continue // skip the owner
			}
			var found bool
			if m.Email == "" {
				found = m.Instance == member.Instance
			} else {
				found = m.Email == member.Email
			}
			if found && member.Status == MemberStatusMailNotSent {
				s.Members[i].Status = MemberStatusPendingInvitation
				break
			}
		}
	}
	return couchdb.UpdateDoc(inst, s)
}

func (s *Sharing) getSharerAndDescription(inst *instance.Instance) (string, string) {
	sharer, _ := inst.PublicName()
	if sharer == "" {
		sharer = inst.Translate("Sharing Empty name")
	}
	desc := s.Description
	if desc == "" {
		desc = inst.Translate("Sharing Empty description")
	}
	return sharer, desc
}

// InvitationLink generates an HTTP link where the recipient can start the
// process of accepting the sharing
func (m *Member) InvitationLink(inst *instance.Instance, s *Sharing, state string, codes map[string]string) string {
	if s.Owner && s.PreviewPath != "" && codes != nil {
		if code, ok := codes[m.Email]; ok {
			u := inst.SubDomain(s.AppSlug)
			u.Path = s.PreviewPath
			u.RawQuery = url.Values{"sharecode": {code}}.Encode()
			return u.String()
		}
	}

	query := url.Values{"state": {state}}
	path := fmt.Sprintf("/sharings/%s/discovery", s.SID)
	return inst.PageURL(path, query)
}

// SendMail sends an invitation mail to a recipient
func (m *Member) SendMail(inst *instance.Instance, s *Sharing, sharer, description, link string) error {
	addr := &mail.Address{
		Email: m.Email,
		Name:  m.PrimaryName(),
	}
	sharerMail, _ := inst.SettingsEMail()
	var action string
	if s.ReadOnlyRules() {
		action = inst.Translate("Mail Sharing Request Action Read")
	} else {
		action = inst.Translate("Mail Sharing Request Action Write")
	}
	docType := getDocumentType(inst, s)
	mailValues := map[string]interface{}{
		"SharerPublicName": sharer,
		"SharerEmail":      sharerMail,
		"Action":           action,
		"Description":      description,
		"DocType":          docType,
		"SharingLink":      link,
	}
	msg, err := job.NewMessage(mail.Options{
		Mode:           "from",
		To:             []*mail.Address{addr},
		TemplateName:   "sharing_request",
		TemplateValues: mailValues,
		RecipientName:  addr.Name,
		Layout:         mail.CozyCloudLayout,
	})
	if err != nil {
		return err
	}
	_, err = job.System().PushJob(inst, &job.JobRequest{
		WorkerType: "sendmail",
		Message:    msg,
	})
	return err
}

func getDocumentType(inst *instance.Instance, s *Sharing) string {
	rule := s.FirstFilesRule()
	if rule == nil {
		return inst.Translate("Notification Sharing Type Document")
	}
	_, err := inst.VFS().FileByID(rule.Values[0])
	if err != nil {
		return inst.Translate("Notification Sharing Type Directory")
	}
	return inst.Translate("Notification Sharing Type File")
}

type shortcutMsg struct {
	Data shortcutData `json:"data"`
}

type shortcutData struct {
	Typ   string        `json:"type"`
	Attrs shortcutAttrs `json:"attributes"`
}

type shortcutAttrs struct {
	Name string                 `json:"name"`
	URL  string                 `json:"url"`
	Meta map[string]interface{} `json:"metadata"`
}

func newShortcutMsg(s *Sharing, inst *instance.Instance, name string) *shortcutMsg {
	doctype := ""
	if len(s.Rules) > 0 {
		doctype = s.Rules[0].DocType
	}
	target := map[string]interface{}{
		"cozyMetadata": map[string]interface{}{
			"instance": inst.PageURL("/", nil),
		},
		"_type": doctype,
	}
	if doctype == consts.Files {
		if s.Rules[0].FilesByID() && len(s.Rules[0].Values) > 0 {
			fileDoc, err := inst.VFS().FileByID(s.Rules[0].Values[0])
			if err == nil {
				target["mime"] = fileDoc.Mime
			}
			// err != nil probably means that the target is a directory and not
			// a file, and we leave the mime as blank in that case.
		}
	}
	meta := map[string]interface{}{
		"sharing": map[string]interface{}{
			"status": "new",
		},
		"target": target,
	}
	if sharer, err := inst.PublicName(); err == nil {
		meta["sharer"] = sharer
	}
	return &shortcutMsg{
		Data: shortcutData{
			Typ: consts.FilesShortcuts,
			Attrs: shortcutAttrs{
				Name: name + ".url",
				Meta: meta,
			},
		},
	}
}

func (s *shortcutMsg) addLink(link string) {
	s.Data.Attrs.URL = link
}

// SendShortcut sends the HTTP request to the cozy of the recipient for adding
// a shortcut on the recipient's instance.
func (m *Member) SendShortcut(inst *instance.Instance, shortcut *shortcutMsg) error {
	u, err := url.Parse(m.Instance)
	if err != nil {
		return err
	}
	body, err := json.Marshal(shortcut)
	if err != nil {
		return err
	}
	opts := &request.Options{
		Method: http.MethodPost,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/shortcuts",
		Body:   bytes.NewReader(body),
	}
	res, err := request.Req(opts)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// SendShortcutMail will send a notification mail after a shortcut for a
// sharing has been created.
func SendShortcutMail(inst *instance.Instance, sharerName string, fileDoc *vfs.FileDoc) error {
	if sharerName == "" {
		sharerName = inst.Translate("Sharing Empty name")
	}
	u := inst.SubDomain(consts.DriveSlug)
	u.Fragment = "/folder/" + fileDoc.DirID
	targetType := getTargetType(inst, fileDoc.Metadata)
	mailValues := map[string]interface{}{
		"SharerPublicName": sharerName,
		"TargetType":       targetType,
		"TargetName":       strings.TrimSuffix(fileDoc.DocName, ".url"),
		"SharingsLink":     u.String(),
	}
	msg, err := job.NewMessage(mail.Options{
		Mode:           "noreply",
		TemplateName:   "notifications_sharing",
		TemplateValues: mailValues,
		Layout:         mail.CozyCloudLayout,
	})
	if err != nil {
		return err
	}
	_, err = job.System().PushJob(inst, &job.JobRequest{
		WorkerType: "sendmail",
		Message:    msg,
	})
	return err
}

func getTargetType(inst *instance.Instance, metadata map[string]interface{}) string {
	target, _ := metadata["target"].(map[string]interface{})
	if target["_type"] != consts.Files {
		return inst.Translate("Notification Sharing Type Document")
	}
	if target["mime"] == nil || target["mime"] == "" {
		return inst.Translate("Notification Sharing Type Directory")
	}
	return inst.Translate("Notification Sharing Type File")
}

// InviteMsg is the struct for the invite route
type InviteMsg struct {
	Sharer      string `json:"sharer_public_name"`
	Description string `json:"description"`
	Link        string `json:"sharing_link"`
}

// SendInviteMail will send an invitation email to the owner of this cozy.
func SendInviteMail(inst *instance.Instance, invite *InviteMsg) error {
	action := inst.Translate("Mail Sharing Request Action Read")
	docType := inst.Translate("Notification Sharing Type Directory")
	mailValues := map[string]interface{}{
		"SharerPublicName": invite.Sharer,
		"SharerEmail":      "",
		"Action":           action,
		"Description":      invite.Description,
		"DocType":          docType,
		"SharingLink":      invite.Link,
	}
	msg, err := job.NewMessage(mail.Options{
		Mode:           "noreply",
		TemplateName:   "sharing_request",
		TemplateValues: mailValues,
		Layout:         mail.CozyCloudLayout,
	})
	if err != nil {
		return err
	}
	_, err = job.System().PushJob(inst, &job.JobRequest{
		WorkerType: "sendmail",
		Message:    msg,
	})
	return err
}
