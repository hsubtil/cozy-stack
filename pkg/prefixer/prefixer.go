package prefixer

type Prefixer interface {
	DBPrefix() string
	DomainName() string
}

type prefixer struct {
	domain string
	prefix string
}

func (p *prefixer) DBPrefix() string   { return p.prefix }
func (p *prefixer) DomainName() string { return p.domain }

// NewPrefixer returns a prefixer with the specified domain and prefix values.
func NewPrefixer(domain, prefix string) Prefixer {
	return &prefixer{
		domain: domain,
		prefix: prefix,
	}
}
