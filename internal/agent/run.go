package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"proxy-mono/internal/model"
)

// Runner exposes timings and transport for integration tests. Production uses
// zero values: a 30s heartbeat, bounded HTTP timeouts, and jittered backoff.
type Runner struct {
	HTTPClient        *http.Client
	HeartbeatInterval time.Duration
	RetryMin          time.Duration
	RetryMax          time.Duration
	ServiceProbe      func(ctx context.Context, unit string) (bool, string)
	ServiceStart      func(ctx context.Context, unit string) error
	ServiceRetryMin   time.Duration
	ServiceRetryMax   time.Duration
	Logf              func(string, ...any)
}

func Run(ctx context.Context, configPath string) error { return (&Runner{}).Run(ctx, configPath) }

type preparedTarget struct {
	target      ManagerTarget
	token       string
	enrollToken string
	client      *http.Client
}

func (r *Runner) Run(ctx context.Context, configPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	targets := cfg.EffectiveManagers()
	envEnroll := os.Getenv("ENROLL_TOKEN")
	if envEnroll == "" {
		envEnroll = os.Getenv("PROXY_ENROLL_TOKEN")
	}
	prepared := make([]preparedTarget, 0, len(targets))
	for _, target := range targets {
		token, err := readToken(target.TokenFile)
		if err != nil {
			return err
		}
		enrollToken := target.EnrollToken
		if enrollToken == "" {
			enrollToken = envEnroll
		}
		if token == "" && !validToken(enrollToken) {
			return fmt.Errorf("initial enrollment for %s requires ENROLL_TOKEN or config enroll_token", target.URL)
		}
		client := r.HTTPClient
		if client == nil {
			client, err = newHTTPClient(target.CAFile)
			if err != nil {
				return err
			}
		}
		// Never follow redirects carrying an enrollment key or a node token.
		copyClient := *client
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if copyClient.Timeout == 0 {
			copyClient.Timeout = 15 * time.Second
		}
		prepared = append(prepared, preparedTarget{
			target:      target,
			token:       token,
			enrollToken: enrollToken,
			client:      &copyClient,
		})
	}

	interval := r.HeartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	minimum, maximum := r.RetryMin, r.RetryMax
	if minimum <= 0 {
		minimum = time.Second
	}
	if maximum <= 0 {
		maximum = 30 * time.Second
	}
	if maximum < minimum {
		maximum = minimum
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	coreContext, stopCores := context.WithCancel(runCtx)
	supervisor := newSupervisor(coreContext, cfg.Commands)
	defer func() { stopCores(); supervisor.wait() }()

	var stateMu sync.Mutex
	curEndpoints := append([]model.Endpoint(nil), cfg.Endpoints...)
	curServices := append([]ExternalService(nil), cfg.Services...)

	starter := r.ServiceStart
	if starter == nil && r.ServiceProbe == nil {
		absConfig, absErr := filepath.Abs(configPath)
		if absErr == nil {
			starter = defaultServiceStarter(filepath.Dir(absConfig))
		} else {
			starter = defaultServiceStarter(filepath.Dir(configPath))
		}
	}
	monitor := newServiceMonitor(r.ServiceProbe, starter, r.ServiceRetryMin, r.ServiceRetryMax)

	snapshot := func(loopCtx context.Context) ([]model.Endpoint, []model.CoreStatus) {
		stateMu.Lock()
		// Reload connection material without sending unchanged secrets every 30s.
		// Identity, manager list and process commands require a restart.
		if next, loadErr := LoadConfig(configPath); loadErr != nil {
			r.log("config reload failed; retaining last valid configuration")
		} else if next.ID != cfg.ID {
			r.log("identity changed; restart agent to apply configuration")
		} else {
			if !sameManagerTargets(next.EffectiveManagers(), targets) {
				r.log("manager list changed; restart agent to apply configuration")
			}
			curEndpoints = append([]model.Endpoint(nil), next.Endpoints...)
			if len(cfg.Commands)+len(next.Services) <= 8 {
				curServices = append([]ExternalService(nil), next.Services...)
			}
		}
		eps := append([]model.Endpoint(nil), curEndpoints...)
		svcs := append([]ExternalService(nil), curServices...)
		stateMu.Unlock()

		cores := supervisor.status()
		cores = append(cores, monitor.check(loopCtx, svcs)...)
		return eps, cores
	}

	if len(prepared) == 1 {
		return r.runTarget(runCtx, cfg.ID, cfg.Name, prepared[0], interval, minimum, maximum, snapshot)
	}

	errCh := make(chan error, len(prepared))
	for _, item := range prepared {
		item := item
		go func() {
			err := r.runTarget(runCtx, cfg.ID, cfg.Name, item, interval, minimum, maximum, snapshot)
			if err != nil && runCtx.Err() == nil {
				r.log("manager %s stopped: %v", item.target.URL, err)
			}
			errCh <- err
		}()
	}

	var lastErr error
	for range prepared {
		if err := <-errCh; err != nil {
			lastErr = err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return lastErr
}

func sameManagerTargets(a, b []ManagerTarget) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].URL != b[i].URL || a[i].TokenFile != b[i].TokenFile || a[i].CAFile != b[i].CAFile {
			return false
		}
	}
	return true
}

func (r *Runner) runTarget(
	ctx context.Context,
	nodeID, nodeName string,
	p preparedTarget,
	interval, minimum, maximum time.Duration,
	snapshot func(context.Context) ([]model.Endpoint, []model.CoreStatus),
) error {
	token := p.token
	backoff := minimum
	lastHash := ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		endpoints, cores := snapshot(ctx)
		var err error
		if token == "" {
			var response struct {
				Token string `json:"token"`
			}
			err = post(ctx, p.client, p.target.URL+"/api/agent/register", p.enrollToken, model.RegisterRequest{
				ID: nodeID, Name: nodeName, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: Version, Endpoints: endpoints,
			}, &response)
			if err == nil {
				if !validToken(response.Token) {
					return fmt.Errorf("manager %s returned an invalid node token", p.target.URL)
				}
				if err := atomicWrite(p.target.TokenFile, []byte(response.Token+"\n")); err != nil {
					return fmt.Errorf("persist node token for %s: %w", p.target.URL, err)
				}
				token = response.Token
			}
			var status *httpStatusError
			if errors.As(err, &status) && (status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden || status.Status == http.StatusConflict) {
				return fmt.Errorf("enrollment refused by %s (HTTP %d); verify enrollment key or use a new node identity", p.target.URL, status.Status)
			}
		}
		if token != "" {
			hash := endpointsHash(endpoints)
			request := model.HeartbeatRequest{ID: nodeID, Version: Version, ConfigHash: hash, Cores: cores}
			if hash != lastHash {
				epsCopy := endpoints
				request.Endpoints = &epsCopy
			}
			err = post(ctx, p.client, p.target.URL+"/api/agent/heartbeat", token, request, nil)
			if err == nil {
				lastHash = hash
			}
			// A rejected persisted token is deliberately retained. In particular,
			// never use the enrollment key to bypass an administrator's revocation.
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := interval
		if err != nil {
			var status *httpStatusError
			if errors.As(err, &status) {
				r.log("manager %s request failed (HTTP %d); node token will not be replaced automatically", p.target.URL, status.Status)
			} else {
				r.log("manager %s unavailable; proxy cores continue running", p.target.URL)
			}
			delay = time.Duration(float64(backoff) * (0.75 + rand.Float64()*0.5))
			if backoff < maximum {
				backoff *= 2
				if backoff > maximum {
					backoff = maximum
				}
			}
		} else {
			backoff = minimum
		}
		if !waitContext(ctx, delay) {
			return nil
		}
	}
}

func (r *Runner) log(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func newHTTPClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		certs, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read manager CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(certs) {
			return nil, errors.New("ca_file contains no valid CA certificates")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}, nil
}

func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read node token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if !validToken(token) {
		return "", errors.New("node token file is empty or invalid; refusing automatic re-enrollment")
	}
	return token, nil
}

func validToken(s string) bool {
	if len(s) == 0 || len(s) > 4096 {
		return false
	}
	for _, c := range s {
		if c <= 32 || c > 126 {
			return false
		}
	}
	return true
}

func endpointsHash(endpoints []model.Endpoint) string {
	b, _ := json.Marshal(endpoints)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type httpStatusError struct{ Status int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("manager HTTP status %d", e.Status) }

func post(ctx context.Context, client *http.Client, url, token string, payload, output any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Error bodies may echo credentials. Never include them in errors/logs.
		return &httpStatusError{Status: response.StatusCode}
	}
	if output == nil {
		_, err := io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
