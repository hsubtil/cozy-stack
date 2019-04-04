package mails

import (
	"bytes"
	"fmt"
	"html/template"
	"io/ioutil"

	"github.com/cozy/cozy-stack/pkg/i18n"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/statik/fs"
)

// MailTemplate is a struct to define a mail template with HTML and text parts.
type MailTemplate struct {
	Name    string
	Subject string
	Intro   string
	Outro   string
	Actions []MailAction
	Entries []MailEntry
}

// MailAction describes an action button in a mail template.
type MailAction struct {
	Instructions string
	Text         string
	Link         string
}

// MailEntry describes an row entry in a mail template.
type MailEntry struct {
	Key string
	Val string
}

func initMailTemplates() {
	mailTemplater = MailTemplater{
		{
			Name:    "passphrase_reset",
			Subject: "Mail Reset Passphrase Subject",
			Intro:   "Mail Reset Passphrase Intro",
			Actions: []MailAction{
				{
					Instructions: "Mail Reset Passphrase Button instruction",
					Text:         "Mail Reset Passphrase Button text",
					Link:         "{{.PassphraseResetLink}}",
				},
			},
			Outro: "Mail Reset Passphrase Outro",
		},
		{
			Name:    "archiver",
			Subject: "Mail Archive Subject",
			Intro:   "Mail Archive Intro",
			Actions: []MailAction{
				{
					Instructions: "Mail Archive Button instruction",
					Text:         "Mail Archive Button text",
					Link:         "{{.ArchiveLink}}",
				},
			},
		},
		{
			Name:    "two_factor",
			Subject: "Mail Two Factor Subject",
			Intro:   "Mail Two Factor Intro",
			Outro:   "Mail Two Factor Outro",
		},
		{
			Name:    "two_factor_mail_confirmation",
			Subject: "Mail Two Factor Mail Confirmation Subject",
			Intro:   "Mail Two Factor Mail Confirmation Intro",
			Outro:   "Mail Two Factor Mail Confirmation Outro",
		},
		{
			Name:    "new_connexion",
			Subject: "Mail New Connection Subject",
			Intro:   "Mail New Connection Intro",
			Entries: []MailEntry{
				{Key: "Mail New Connection Place", Val: "{{.City}}, {{.Country}}"},
				{Key: "Mail New Connection IP", Val: "{{.IP}}"},
				{Key: "Mail New Connection Browser", Val: "{{.Browser}}"},
				{Key: "Mail New Connection OS", Val: "{{.OS}}"},
			},
			Actions: []MailAction{
				{
					Instructions: "Mail New Connection Change Passphrase instruction",
					Text:         "Mail New Connection Change Passphrase text",
					Link:         "{{.ChangePassphraseLink}}",
				},
			},
			Outro: "Mail New Connection Outro",
		},
		{
			Name:    "new_registration",
			Subject: "Mail New Registration Subject",
			Intro:   "Mail New Registration Intro",
			Actions: []MailAction{
				{
					Instructions: "Mail New Registration Devices instruction",
					Text:         "Mail New Registration Devices text",
					Link:         "{{.DevicesLink}}",
				},
				// {
				//  Instructions: "Mail New Registration Revoke instruction",
				//  Text:         "Mail New Registration Revoke text",
				//  Link:         "{{.RevokeLink}}",
				// },
			},
		},
		{
			Name:    "sharing_request",
			Subject: "Mail Sharing Request Subject",
			Intro:   "Mail Sharing Request Intro",
			Actions: []MailAction{
				{
					Instructions: "Mail Sharing Request Button instruction",
					Text:         "Mail Sharing Request Button text",
					Link:         "{{.SharingLink}}",
				},
			},
		},

		// Notifications
		{
			Name:    "notifications_diskquota",
			Subject: "Notifications Disk Quota Subject",
			Intro:   "Notifications Disk Quota Intro",
			Actions: []MailAction{
				{
					Instructions: "Notifications Disk Quota offers instruction",
					Text:         "Notifications Disk Quota offers text",
					Link:         "{{.OffersLink}}",
				},
				{
					Instructions: "Notifications Disk Quota free instructions",
					Text:         "Notifications Disk Quota free text",
					Link:         "{{.CozyDriveLink}}",
				},
			},
		},
	}
}

// RenderMail returns a rendered mail for the given template name with the
// specified locale, recipient name and template data values.
func RenderMail(ctx *jobs.WorkerContext, name, locale, recipientName string, templateValues map[string]interface{}) (string, []*Part, error) {
	return mailTemplater.Execute(ctx, name, locale, recipientName, templateValues)
}

// MailTemplater is the list of templates for emails.
type MailTemplater []*MailTemplate // TODO use a map name -> subject

// Execute will execute the HTML and text templates for the template with the
// specified name. It returns the mail parts that should be added to the sent
// mail.
func (m MailTemplater) Execute(ctx *jobs.WorkerContext, name, locale string, recipientName string, data map[string]interface{}) (string, []*Part, error) {
	var tpl *MailTemplate
	for _, t := range m {
		if name == t.Name {
			tpl = t
			break
		}
	}
	if tpl == nil {
		err := fmt.Errorf("Could not find email named %q", name)
		return "", nil, err
	}

	subject := i18n.Translate(tpl.Subject, locale)
	funcMap := template.FuncMap{"t": i18n.Translator(locale)}
	data["Locale"] = locale

	text := new(bytes.Buffer)
	f, err := fs.Open("/mails/" + tpl.Name + ".text") // TODO dynamic assets
	if err != nil {
		return "", nil, err
	}
	b, err := ioutil.ReadAll(f)
	if err != nil {
		return "", nil, err
	}
	t, err := template.New("text").Funcs(funcMap).Parse(string(b))
	if err != nil {
		return "", nil, err
	}
	if err := t.Execute(text, data); err != nil {
		return "", nil, err
	}

	// TODO ignore errors on html
	buf := new(bytes.Buffer)
	f, err = fs.Open("/mails/" + tpl.Name + ".mjml") // TODO dynamic assets
	if err != nil {
		return "", nil, err
	}
	b, err = ioutil.ReadAll(f)
	if err != nil {
		return "", nil, err
	}
	t, err = template.New("content").Funcs(funcMap).Parse(string(b))
	if err != nil {
		return "", nil, err
	}
	f, err = fs.Open("/mails/layout.mjml") // TODO dynamic assets
	if err != nil {
		return "", nil, err
	}
	b, err = ioutil.ReadAll(f)
	if err != nil {
		return "", nil, err
	}
	t, err = t.New("layout").Funcs(funcMap).Parse(string(b))
	if err != nil {
		return "", nil, err
	}
	if err := t.Execute(buf, data); err != nil {
		return "", nil, err
	}
	html, err := execMjml(ctx, buf.Bytes())
	if err != nil {
		return "", nil, err
	}

	parts := []*Part{
		{Body: string(html), Type: "text/html"},
		{Body: text.String(), Type: "text/plain"},
	}
	return subject, parts, nil
}
