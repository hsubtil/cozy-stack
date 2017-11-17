package sessions

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/couchdb/mango"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/logger"
	"github.com/cozy/cozy-stack/pkg/notifications"
	"github.com/mssola/user_agent"
	maxminddb "github.com/oschwald/maxminddb-golang"
)

// LoginEntry stores informations associated with a new login. It is useful to
// provide the user with informations about the history of all the logins that
// may have happened on its domain.
type LoginEntry struct {
	DocID     string `json:"_id,omitempty"`
	DocRev    string `json:"_rev,omitempty"`
	SessionID string `json:"session_id"`
	IP        string `json:"ip"`
	City      string `json:"city,omitempty"`
	Country   string `json:"country,omitempty"`
	// XXX No omitempty on os and browser, because they are indexed in couchdb
	UA      string `json:"user_agent"`
	OS      string `json:"os"`
	Browser string `json:"browser"`
	// Used when the login entry is linked to an OAuth client registration
	ClientID  string    `json:"client_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DocType implements couchdb.Doc
func (l *LoginEntry) DocType() string { return consts.SessionsLogins }

// ID implements couchdb.Doc
func (l *LoginEntry) ID() string { return l.DocID }

// SetID implements couchdb.Doc
func (l *LoginEntry) SetID(v string) { l.DocID = v }

// Rev implements couchdb.Doc
func (l *LoginEntry) Rev() string { return l.DocRev }

// SetRev implements couchdb.Doc
func (l *LoginEntry) SetRev(v string) { l.DocRev = v }

// Clone implements couchdb.Doc
func (l *LoginEntry) Clone() couchdb.Doc {
	clone := *l
	return &clone
}

func lookupIP(ip, locale string) (city, country string) {
	geodb := config.GetConfig().GeoDB
	if geodb == "" {
		return
	}
	db, err := maxminddb.Open(geodb)
	if err != nil {
		logger.WithNamespace("sessions").Errorf("[geodb] cannot open the geodb: %s", err)
		return
	}
	defer db.Close()

	var record struct {
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
		Country struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"country"`
	}

	err = db.Lookup(net.ParseIP(ip), &record)
	if err != nil {
		logger.WithNamespace("sessions").Infof("[geodb] cannot lookup %s: %s", ip, err)
		return
	}
	if c, ok := record.City.Names[locale]; ok {
		city = c
	} else if c, ok := record.City.Names["en"]; ok {
		city = c
	}
	if c, ok := record.Country.Names[locale]; ok {
		country = c
	} else if c, ok := record.Country.Names["en"]; ok {
		country = c
	}
	return
}

// StoreNewLoginEntry creates a new login entry in the database associated with
// the given instance.
func StoreNewLoginEntry(i *instance.Instance, sessionID, clientID string, req *http.Request, notifEnabled bool) error {
	var ip string
	if forwardedFor := req.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		ip = strings.TrimSpace(strings.SplitN(forwardedFor, ",", 2)[0])
	}
	if ip == "" {
		ip = req.RemoteAddr
	}

	city, country := lookupIP(ip, i.Locale)
	ua := user_agent.New(req.UserAgent())

	browser, _ := ua.Browser()
	os := ua.OS()

	l := &LoginEntry{
		IP:        ip,
		City:      city,
		SessionID: sessionID,
		Country:   country,
		UA:        req.UserAgent(),
		OS:        os,
		Browser:   browser,
		ClientID:  clientID,
		CreatedAt: time.Now(),
	}

	if err := couchdb.CreateDoc(i, l); err != nil {
		return err
	}

	if clientID != "" {
		if err := PushLoginRegistration(i.Domain, l); err != nil {
			i.Logger().Errorf("Could not push login in registration queue: %s", err)
		}
	} else if notifEnabled {
		if err := sendLoginNotification(i, l, false); err != nil {
			i.Logger().Errorf("Could not send login notification: %s", err)
		}
	}

	return nil
}

func sendLoginNotification(i *instance.Instance, l *LoginEntry, registrationNotification bool) error {
	var sendNotification bool

	if registrationNotification && l.ClientID != "" {
		sendNotification = true
	} else {
		var results []*LoginEntry
		r := &couchdb.FindRequest{
			UseIndex: "by-os-browser-ip",
			Selector: mango.And(
				mango.Equal("os", l.OS),
				mango.Equal("browser", l.Browser),
				mango.Equal("ip", l.IP),
			),
			Limit: 1,
		}
		err := couchdb.FindDocs(i, consts.SessionsLogins, r, &results)
		sendNotification = err != nil || len(results) == 0
	}

	if !sendNotification {
		return nil
	}

	var title, content string
	if registrationNotification && l.ClientID != "" {
		devicesLink := i.SubDomain(consts.SettingsSlug)
		devicesLink.Fragment = "connectedDevices"

		revokeLink := i.SubDomain(consts.SettingsSlug)
		revokeLink.Fragment = "connectedDevices/" + url.PathEscape(l.ClientID)

		title = i.Translate("Session New connection for registration title")
		content = i.Translate("Session New connection for registration content", devicesLink, revokeLink)
	} else {
		title = i.Translate("Session New connection title")
		content = i.Translate("Session New connection content", l.City, l.Country, l.IP, l.Browser, l.OS)
	}

	notif := &notifications.Notification{
		Reference: "New connexion",
		Title:     title,
		Content:   content,
	}
	return notifications.Create(i, "stack", notif)
}
