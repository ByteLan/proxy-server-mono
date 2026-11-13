package manager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"proxy-mono/internal/model"
	"time"
)

type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Open(dir, key string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	var raw []byte
	var err error
	if key != "" {
		raw, err = base64.StdEncoding.DecodeString(key)
	} else {
		path := filepath.Join(dir, "master.key")
		raw, err = os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			// Losing a master key must never silently create a new one for an existing database.
			if _, statErr := os.Stat(filepath.Join(dir, "manager.db")); statErr == nil {
				return nil, fmt.Errorf("master.key missing for existing database; restore the original key")
			}
			raw = make([]byte, 32)
			if _, err = rand.Read(raw); err == nil {
				err = os.WriteFile(path, raw, 0600)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("MASTER_KEY must contain 32 bytes (base64), or master.key must contain 32 raw bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "manager.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON", "PRAGMA cache_size=-4096", `CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,name TEXT NOT NULL,os TEXT NOT NULL,arch TEXT NOT NULL,version TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,last_seen INTEGER NOT NULL,config_hash TEXT NOT NULL DEFAULT '',cores TEXT NOT NULL DEFAULT '[]',endpoints BLOB NOT NULL,token_hash TEXT NOT NULL UNIQUE)`, `CREATE TABLE IF NOT EXISTS revoked_nodes(id TEXT PRIMARY KEY)`, `CREATE TABLE IF NOT EXISTS subscriptions(id TEXT PRIMARY KEY,name TEXT NOT NULL,node_ids TEXT NOT NULL,protocols TEXT NOT NULL,routes TEXT NOT NULL DEFAULT '{}',enabled INTEGER NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`} {
		if _, err = db.Exec(q); err != nil {
			return fail(err)
		}
	}
	if err = ensureSubscriptionRoutesColumn(db); err != nil {
		return fail(err)
	}
	if err = os.Chmod(filepath.Join(dir, "manager.db"), 0600); err != nil {
		return fail(err)
	}
	store := &Store{db, aead}
	if _, err := store.nodes(false); err != nil {
		return fail(fmt.Errorf("cannot decrypt database with the configured master key: %w", err))
	}
	return store, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) seal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, raw, nil), nil
}
func (s *Store) unseal(raw []byte, v any) error {
	n := s.aead.NonceSize()
	if len(raw) < n {
		return fmt.Errorf("invalid encrypted data")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}
func (s *Store) register(r model.RegisterRequest) (string, error) {
	token := RandomToken()
	enc, err := s.seal(r.Endpoints)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO nodes(id,name,os,arch,version,last_seen,endpoints,token_hash) VALUES(?,?,?,?,?,?,?,?)`, r.ID, r.Name, r.OS, r.Arch, r.Version, time.Now().Unix(), enc, hash(token))
	return token, err
}
func (s *Store) nodeAuthorized(id, token string) bool {
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM nodes WHERE id=? AND token_hash=?`, id, hash(token)).Scan(&count)
	return err == nil && count == 1
}
func (s *Store) heartbeat(r model.HeartbeatRequest) error {
	cores, err := json.Marshal(r.Cores)
	if err != nil {
		return err
	}
	if r.Endpoints != nil {
		enc, err := s.seal(*r.Endpoints)
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`UPDATE nodes SET version=?,last_seen=?,config_hash=?,cores=?,endpoints=? WHERE id=?`, r.Version, time.Now().Unix(), r.ConfigHash, string(cores), enc, r.ID)
		return err
	}
	_, err = s.db.Exec(`UPDATE nodes SET version=?,last_seen=?,cores=? WHERE id=?`, r.Version, time.Now().Unix(), string(cores), r.ID)
	return err
}
func (s *Store) nodes(redact bool) ([]model.Node, error) {
	rows, err := s.db.Query(`SELECT id,name,os,arch,version,enabled,last_seen,config_hash,cores,endpoints FROM nodes ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Node{}
	for rows.Next() {
		var n model.Node
		var cores string
		var enc []byte
		if err = rows.Scan(&n.ID, &n.Name, &n.OS, &n.Arch, &n.Version, &n.Enabled, &n.LastSeen, &n.ConfigHash, &cores, &enc); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(cores), &n.Cores); err != nil {
			return nil, err
		}
		if n.Cores == nil {
			n.Cores = []model.CoreStatus{}
		}
		if err = s.unseal(enc, &n.Endpoints); err != nil {
			return nil, err
		}
		if n.Endpoints == nil {
			n.Endpoints = []model.Endpoint{}
		}
		n.Online = time.Now().Unix()-n.LastSeen < 90
		if redact {
			for i := range n.Endpoints {
				e := &n.Endpoints[i]
				e.Password = ""
				e.Username = ""
				e.UUID = ""
				e.PublicKey = ""
				e.ShortID = ""
			}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) patchNode(id, name string, enabled bool) (bool, error) {
	res, err := s.db.Exec(`UPDATE nodes SET name=?,enabled=? WHERE id=?`, name, enabled, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
func (s *Store) deleteNode(id string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO revoked_nodes(id) SELECT id FROM nodes WHERE id=?`, id); err != nil {
		return false, err
	}
	res, err := tx.Exec(`DELETE FROM nodes WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	rows, err := tx.Query(`SELECT id,node_ids,routes FROM subscriptions`)
	if err != nil {
		return false, err
	}
	type subUpdate struct {
		nodes  string
		routes string
	}
	updates := map[string]subUpdate{}
	for rows.Next() {
		var sid, rawNodes, rawRoutes string
		if err = rows.Scan(&sid, &rawNodes, &rawRoutes); err != nil {
			rows.Close()
			return false, err
		}
		var ids []string
		if err = json.Unmarshal([]byte(rawNodes), &ids); err != nil {
			rows.Close()
			return false, err
		}
		var routes model.SiteRoutes
		if rawRoutes != "" {
			if err = json.Unmarshal([]byte(rawRoutes), &routes); err != nil {
				rows.Close()
				return false, err
			}
		}
		ids = removeID(ids, id)
		routes.Claude.NodeIDs = removeID(routes.Claude.NodeIDs, id)
		routes.ChatGPT.NodeIDs = removeID(routes.ChatGPT.NodeIDs, id)
		routes.Google.NodeIDs = removeID(routes.Google.NodeIDs, id)
		normalizeRoutes(&routes)
		nb, _ := json.Marshal(ids)
		rb, _ := json.Marshal(routes)
		updates[sid] = subUpdate{nodes: string(nb), routes: string(rb)}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	for sid, u := range updates {
		if _, err = tx.Exec(`UPDATE subscriptions SET node_ids=?,routes=?,updated_at=? WHERE id=?`, u.nodes, u.routes, now, sid); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
func ensureSubscriptionRoutesColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(subscriptions)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasRoutes := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "routes" {
			hasRoutes = true
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if hasRoutes {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE subscriptions ADD COLUMN routes TEXT NOT NULL DEFAULT '{}'`)
	return err
}
func removeID(ids []string, target string) []string {
	out := []string{}
	for _, v := range ids {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}
func normalizeRouteGroup(g *model.RouteGroup) {
	if !g.Custom || g.NodeIDs == nil {
		if !g.Custom {
			g.NodeIDs = []string{}
			return
		}
		g.NodeIDs = []string{}
	}
}
func normalizeRoutes(r *model.SiteRoutes) {
	normalizeRouteGroup(&r.Claude)
	normalizeRouteGroup(&r.ChatGPT)
	normalizeRouteGroup(&r.Google)
}
func scanSubscription(row interface{ Scan(...any) error }) (model.Subscription, error) {
	var p model.Subscription
	var nodes, protocols, routes string
	err := row.Scan(&p.ID, &p.Name, &nodes, &protocols, &routes, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(nodes), &p.NodeIDs); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(protocols), &p.Protocols); err != nil {
		return p, err
	}
	if routes != "" {
		if err = json.Unmarshal([]byte(routes), &p.Routes); err != nil {
			return p, err
		}
	}
	normalizeRoutes(&p.Routes)
	return p, nil
}

const subColumns = `id,name,node_ids,protocols,routes,enabled,created_at,updated_at`

func (s *Store) subscriptions() ([]model.Subscription, error) {
	rows, err := s.db.Query(`SELECT ` + subColumns + ` FROM subscriptions ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Subscription{}
	for rows.Next() {
		p, e := scanSubscription(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) subscription(id string) (model.Subscription, error) {
	return scanSubscription(s.db.QueryRow(`SELECT `+subColumns+` FROM subscriptions WHERE id=?`, id))
}
func (s *Store) subscriptionByToken(token string) (model.Subscription, error) {
	return scanSubscription(s.db.QueryRow(`SELECT `+subColumns+` FROM subscriptions WHERE token_hash=? AND enabled=1`, hash(token)))
}
func (s *Store) createSubscription(p model.Subscription) (model.Subscription, string, error) {
	p.ID = RandomToken()[:16]
	p.CreatedAt = time.Now().Unix()
	p.UpdatedAt = p.CreatedAt
	normalizeRoutes(&p.Routes)
	token := RandomToken()
	ids, _ := json.Marshal(p.NodeIDs)
	protocols, _ := json.Marshal(p.Protocols)
	routes, _ := json.Marshal(p.Routes)
	_, err := s.db.Exec(`INSERT INTO subscriptions(id,name,node_ids,protocols,routes,enabled,token_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, string(ids), string(protocols), string(routes), p.Enabled, hash(token), p.CreatedAt, p.UpdatedAt)
	return p, token, err
}
func (s *Store) updateSubscription(id string, p model.Subscription) (model.Subscription, error) {
	normalizeRoutes(&p.Routes)
	ids, _ := json.Marshal(p.NodeIDs)
	protocols, _ := json.Marshal(p.Protocols)
	routes, _ := json.Marshal(p.Routes)
	res, err := s.db.Exec(`UPDATE subscriptions SET name=?,node_ids=?,protocols=?,routes=?,enabled=?,updated_at=? WHERE id=?`, p.Name, string(ids), string(protocols), string(routes), p.Enabled, time.Now().Unix(), id)
	if err != nil {
		return p, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return p, sql.ErrNoRows
	}
	return s.subscription(id)
}
func (s *Store) rotateSubscription(id string) (string, error) {
	token := RandomToken()
	res, err := s.db.Exec(`UPDATE subscriptions SET token_hash=?,updated_at=? WHERE id=?`, hash(token), time.Now().Unix(), id)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", sql.ErrNoRows
	}
	return token, nil
}
func (s *Store) deleteSubscription(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM subscriptions WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
