package agent

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type AddManagerOptions struct {
	ConfigPath        string
	Manager           string
	CAFile            string
	AllowInsecureHTTP bool
	EnrollToken       string
}

type ManagerStatus struct {
	URL      string
	CAFile   string
	Enrolled bool
}

// AddManager appends a new control-plane manager target to an existing node
// configuration so the node registers and sends heartbeats to all configured
// managers concurrently. When CAFile is supplied, a private 0600 copy is placed
// inside the node state directory so the unprivileged proxy-node service user
// can always read it even if the source path lived under /root.
func AddManager(options AddManagerOptions) (Config, error) {
	if options.ConfigPath == "" {
		return Config{}, errors.New("config path is required")
	}
	configPath, err := filepath.Abs(options.ConfigPath)
	if err != nil {
		return Config{}, err
	}
	uid, gid, hasOwner := fileOwner(configPath)
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return Config{}, err
	}

	urlStr := strings.TrimRight(strings.TrimSpace(options.Manager), "/")
	enrollToken := strings.TrimSpace(options.EnrollToken)
	if enrollToken == "" {
		enrollToken = strings.TrimSpace(os.Getenv("ENROLL_TOKEN"))
	}
	if enrollToken == "" {
		enrollToken = strings.TrimSpace(os.Getenv("PROXY_ENROLL_TOKEN"))
	}
	if !validToken(enrollToken) {
		return Config{}, errors.New("ENROLL_TOKEN (or --enroll-token) is required to enroll with the new manager")
	}

	statePath := stateDirForConfig(configPath, cfg)
	if err := os.MkdirAll(statePath, 0700); err != nil {
		return Config{}, fmt.Errorf("prepare node state directory: %w", err)
	}
	if hasOwner {
		_ = os.Chown(statePath, uid, gid)
	}

	shortID := managerShortHash(urlStr)
	tokenFile := filepath.Join(statePath, "manager_"+shortID+".token")

	caDest := ""
	if strings.TrimSpace(options.CAFile) != "" {
		caBytes, err := os.ReadFile(options.CAFile)
		if err != nil {
			return Config{}, fmt.Errorf("read manager CA: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(caBytes) {
			return Config{}, errors.New("ca_file contains no valid CA certificates")
		}
		caDest = filepath.Join(statePath, "manager_"+shortID+"_ca.pem")
		if err := atomicWriteOwned(caDest, caBytes, uid, gid, hasOwner); err != nil {
			return Config{}, fmt.Errorf("persist manager CA: %w", err)
		}
	}

	target := ManagerTarget{
		URL:               urlStr,
		CAFile:            caDest,
		AllowInsecureHTTP: options.AllowInsecureHTTP,
		EnrollToken:       enrollToken,
		TokenFile:         tokenFile,
	}
	cfg.Managers = append(cfg.Managers, target)
	if err := cfg.Validate(); err != nil {
		if caDest != "" {
			_ = os.Remove(caDest)
		}
		return Config{}, err
	}

	if err := saveConfigOwned(configPath, cfg, uid, gid, hasOwner); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// RemoveManager removes a configured manager URL from config.json (while
// refusing to remove the last remaining manager) and cleans up any state-dir
// token/CA files dedicated to that manager.
func RemoveManager(configPath, managerURL string) (Config, error) {
	if configPath == "" {
		return Config{}, errors.New("config path is required")
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return Config{}, err
	}
	uid, gid, hasOwner := fileOwner(absPath)
	cfg, err := LoadConfig(absPath)
	if err != nil {
		return Config{}, err
	}
	targetURL := strings.TrimRight(strings.TrimSpace(managerURL), "/")
	all := cfg.EffectiveManagers()
	if len(all) <= 1 {
		return Config{}, errors.New("cannot remove the only configured manager")
	}

	var kept []ManagerTarget
	var removed *ManagerTarget
	for i := range all {
		if all[i].URL == targetURL && removed == nil {
			cp := all[i]
			removed = &cp
			continue
		}
		kept = append(kept, all[i])
	}
	if removed == nil {
		return Config{}, fmt.Errorf("manager %q is not configured", targetURL)
	}

	// Normalize back into primary Manager + additional Managers.
	primary := kept[0]
	cfg.Manager = primary.URL
	cfg.CAFile = primary.CAFile
	cfg.AllowInsecureHTTP = primary.AllowInsecureHTTP
	cfg.EnrollToken = primary.EnrollToken
	cfg.TokenFile = primary.TokenFile
	if len(kept) > 1 {
		cfg.Managers = kept[1:]
	} else {
		cfg.Managers = nil
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := saveConfigOwned(absPath, cfg, uid, gid, hasOwner); err != nil {
		return Config{}, err
	}

	statePath := stateDirForConfig(absPath, cfg)
	if isWithinDir(removed.TokenFile, statePath) {
		_ = os.Remove(removed.TokenFile)
	}
	if removed.CAFile != "" && isWithinDir(removed.CAFile, statePath) {
		_ = os.Remove(removed.CAFile)
	}
	return cfg, nil
}

func ListManagers(configPath string) ([]ManagerStatus, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	targets := cfg.EffectiveManagers()
	out := make([]ManagerStatus, 0, len(targets))
	for _, t := range targets {
		tok, err := readToken(t.TokenFile)
		enrolled := err == nil && tok != ""
		out = append(out, ManagerStatus{
			URL:      t.URL,
			CAFile:   t.CAFile,
			Enrolled: enrolled,
		})
	}
	return out, nil
}

func stateDirForConfig(configPath string, cfg Config) string {
	if cfg.TokenFile != "" && filepath.IsAbs(cfg.TokenFile) {
		return filepath.Dir(cfg.TokenFile)
	}
	return strings.TrimSuffix(configPath, filepath.Ext(configPath)) + ".d"
}

func isWithinDir(file, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(file))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func managerShortHash(urlStr string) string {
	sum := sha256.Sum256([]byte(urlStr))
	return hex.EncodeToString(sum[:6])
}

func fileOwner(path string) (int, int, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

func atomicWriteOwned(path string, data []byte, uid, gid int, hasOwner bool) error {
	if err := atomicWrite(path, data); err != nil {
		return err
	}
	if hasOwner && os.Geteuid() == 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func saveConfigOwned(configPath string, cfg Config, uid, gid int, hasOwner bool) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteOwned(configPath, append(b, '\n'), uid, gid, hasOwner)
}
