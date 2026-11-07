package model

// Endpoint contains client connection material only. Never upload server private keys.
type Endpoint struct {
	ID             string `json:"id" yaml:"-"`
	Name           string `json:"name" yaml:"-"`
	Type           string `json:"type" yaml:"-"`
	Server         string `json:"server" yaml:"-"`
	Port           int    `json:"port" yaml:"-"`
	Password       string `json:"password,omitempty" yaml:"-"`
	Username       string `json:"username,omitempty" yaml:"-"`
	UUID           string `json:"uuid,omitempty" yaml:"-"`
	SNI            string `json:"sni,omitempty" yaml:"-"`
	PublicKey      string `json:"public_key,omitempty" yaml:"-"`
	ShortID        string `json:"short_id,omitempty" yaml:"-"`
	SkipCertVerify bool   `json:"skip_cert_verify,omitempty" yaml:"-"`
	Fingerprint    string `json:"fingerprint,omitempty" yaml:"-"`
	TLS            bool   `json:"tls,omitempty" yaml:"-"`
	UDP            bool   `json:"udp,omitempty" yaml:"-"`
}

type CoreStatus struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type RegisterRequest struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	OS        string     `json:"os"`
	Arch      string     `json:"arch"`
	Version   string     `json:"version"`
	Endpoints []Endpoint `json:"endpoints"`
}

type HeartbeatRequest struct {
	ID         string       `json:"id"`
	Version    string       `json:"version"`
	ConfigHash string       `json:"config_hash"`
	Endpoints  *[]Endpoint  `json:"endpoints,omitempty"`
	Cores      []CoreStatus `json:"cores"`
}

type Node struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	OS         string       `json:"os"`
	Arch       string       `json:"arch"`
	Version    string       `json:"version"`
	Enabled    bool         `json:"enabled"`
	LastSeen   int64        `json:"last_seen"`
	Online     bool         `json:"online"`
	ConfigHash string       `json:"config_hash"`
	Cores      []CoreStatus `json:"cores"`
	Endpoints  []Endpoint   `json:"endpoints"`
}

type Subscription struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	NodeIDs   []string `json:"node_ids"`
	Protocols []string `json:"protocols"`
	Enabled   bool     `json:"enabled"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}
