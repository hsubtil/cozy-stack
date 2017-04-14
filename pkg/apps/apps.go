package apps

import (
	"io"
	"net/url"

	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/permissions"
)

const (
	// ManifestMaxSize is the manifest maximum size
	ManifestMaxSize = 2 << (2 * 10) // 2MB
	// WebappManifestName is the name of the manifest at the root of the
	// client-side application directory
	WebappManifestName = "manifest.webapp"
	// KonnectorManifestName is the name of the manifest at the root of the
	// konnector application directory
	KonnectorManifestName = "manifest.konnectors"
)

// State is the state of the application
type State string

const (
	// Available state
	Available State = "available"
	// Installing state
	Installing = "installing"
	// Upgrading state
	Upgrading = "upgrading"
	// Uninstalling state
	Uninstalling = "uninstalling"
	// Errored state
	Errored = "errored"
	// Ready state
	Ready = "ready"
)

// AppType is an enum to represent the type of application: webapp clientside
// or konnector serverside.
type AppType int

const (
	// Webapp is the clientside application type
	Webapp AppType = iota
	// Konnector is the serverside application type
	Konnector
)

// KonnectorArchiveName is the name of the archive created to store the
// konnectors sources.
const KonnectorArchiveName = "app.tar"

// Developer is the name and url of a developer.
type Developer struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// SubDomainer is an interface with a single method to build an URL from a slug
type SubDomainer interface {
	SubDomain(s string) *url.URL
}

// Manifest interface is used by installer to encapsulate the manifest metadata
// that can represent either a webapp or konnector manifest
type Manifest interface {
	couchdb.Doc
	Valid(field, expected string) bool
	ReadManifest(i io.Reader, slug, sourceURL string) error

	Create(db couchdb.Database) error
	Update(db couchdb.Database) error
	Delete(db couchdb.Database) error

	Permissions() permissions.Set
	Source() string
	Version() string
	Slug() string
	State() State
	Error() error

	SetState(state State)
	SetVersion(version string)
	SetError(err error)
}

// GetBySlug returns an app manifest identified by its slug
func GetBySlug(db couchdb.Database, slug string, appType AppType) (Manifest, error) {
	var man Manifest
	var err error
	switch appType {
	case Webapp:
		man, err = GetWebappBySlug(db, slug)
	case Konnector:
		man, err = GetKonnectorBySlug(db, slug)
	}
	if err != nil {
		return nil, err
	}
	return man, nil
}

func routeMatches(path, ctx []string) bool {
	for i, part := range ctx {
		if path[i] != part {
			return false
		}
	}
	return true
}
