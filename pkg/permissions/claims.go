package permissions

import (
	"time"

	jwt "gopkg.in/dgrijalva/jwt-go.v3"
)

// #nosec
const (
	AppAudience               = "app"          // used by client-side apps
	KonnectorAudience         = "konn"         // used by konnectors
	CLIAudience               = "cli"          // used by command line interface
	ShareAudience             = "share"        // used for share by links code
	RegistrationTokenAudience = "registration" // OAuth registration tokens
	AccessTokenAudience       = "access"       // OAuth access tokens
	RefreshTokenAudience      = "refresh"      // OAuth refresh tokens
)

// TokenValidityDuration is the duration where a token is valid in seconds (1 week)
var (
	defaultValidityDuration = 24 * time.Hour

	appTokenValidityDuration       = 24 * time.Hour
	konnectorTokenValidityDuration = 30 * time.Minute
	cliTokenValidityDuration       = 30 * time.Minute

	accessTokenValidityDuration = 7 * 24 * time.Hour
)

// Claims is used for JWT used in OAuth2 flow and applications token
type Claims struct {
	jwt.StandardClaims
	Scope     string `json:"scope,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// IssuedAtUTC returns a time.Time struct of the IssuedAt field in UTC
// location.
func (claims *Claims) IssuedAtUTC() time.Time {
	return time.Unix(claims.IssuedAt, 0).UTC()
}

// Expired returns true if a Claim is expired
func (claims *Claims) Expired() bool {
	var validityDuration time.Duration
	switch claims.Audience {
	case AppAudience:
		if claims.SessionID == "" {
			// an app token with no session association is used for services which
			// should have tokens that have the same properties as the konnector's
			// tokens
			validityDuration = konnectorTokenValidityDuration
		} else {
			validityDuration = appTokenValidityDuration
		}

	case KonnectorAudience:
		validityDuration = konnectorTokenValidityDuration

	case CLIAudience:
		validityDuration = cliTokenValidityDuration

	case AccessTokenAudience:
		validityDuration = accessTokenValidityDuration

	// Share, RefreshToken and RegistrationToken never expire
	case ShareAudience, RegistrationTokenAudience, RefreshTokenAudience:
		return false

	default:
		validityDuration = defaultValidityDuration
	}
	validUntil := claims.IssuedAtUTC().Add(validityDuration)
	return validUntil.Before(time.Now().UTC())
}
