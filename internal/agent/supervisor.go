package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"proxy-mono/internal/model"
)

const maxCoreRecoveryAttempts = 10

type supervisorOptions struct {
	minBackoff   time.Duration
	maxBackoff   time.Duration
	healthyAfter time.Duration
	maxAttempts  int
}

type supervisor struct {
	mu     sync.Mutex
	states []model.CoreStatus
	opts   supervisorOptions
	wg     sync.WaitGroup
}

func newSupervisor(ctx context.Context, commands []Command) *supervisor {
	return newSupervisorWithOptions(ctx, commands, supervisorOptions{})
}

func newSupervisorWithOptions(ctx context.Context, commands []Command, opts supervisorOptions) *supervisor {
	if opts.minBackoff <= 0 {
		opts.minBackoff = time.Second
	}
	if opts.maxBackoff <= 0 {
		opts.maxBackoff = 30 * time.Second
	}
	if opts.maxBackoff < opts.minBackoff {
		opts.maxBackoff = opts.minBackoff
	}
	if opts.healthyAfter <= 0 {
		opts.healthyAfter = time.Minute
	}
	if opts.maxAttempts <= 0 {
		opts.maxAttempts = maxCoreRecoveryAttempts
	}
	s := &supervisor{states: make([]model.CoreStatus, len(commands)), opts: opts}
	for i, command := range commands {
		s.states[i] = model.CoreStatus{Name: command.Name}
		s.wg.Add(1)
		go s.run(ctx, i, command)
	}
	return s
}

func (s *supervisor) set(index int, running bool, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[index].Running = running
	s.states[index].Error = message
}

func (s *supervisor) run(ctx context.Context, index int, command Command) {
	defer s.wg.Done()
	backoff := s.opts.minBackoff
	restarts := 0
	for ctx.Err() == nil {
		process := exec.Command(command.Path, command.Args...)
		// Enrollment is only an Agent responsibility. Do not leak the bootstrap
		// key to proxy cores or wrappers through inherited environment variables.
		process.Env = coreEnvironment(os.Environ())
		// Core output can contain connection material. Let the core manage any
		// explicitly configured logs; never forward stdout/stderr to agent logs.
		started := time.Now()
		var lastErr string
		if err := process.Start(); err != nil {
			lastErr = "core failed to start; check executable path and permissions"
		} else {
			s.set(index, true, "")
			finished := make(chan error, 1)
			go func() { finished <- process.Wait() }()
			select {
			case err := <-finished:
				lastErr = "core exited"
				if exit, ok := err.(*exec.ExitError); ok {
					lastErr = fmt.Sprintf("core exited with status %d", exit.ExitCode())
				}
			case <-ctx.Done():
				_ = process.Process.Signal(syscall.SIGTERM)
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-finished:
				case <-timer.C:
					_ = process.Process.Kill()
					<-finished
				}
				timer.Stop()
				s.set(index, false, "")
				return
			}
		}
		if time.Since(started) >= s.opts.healthyAfter {
			restarts = 0
			backoff = s.opts.minBackoff
		}
		if restarts >= s.opts.maxAttempts {
			s.set(index, false, fmt.Sprintf("%s (gave up after %d recovery attempts)", lastErr, s.opts.maxAttempts))
			return
		}
		restarts++
		s.set(index, false, fmt.Sprintf("%s (recovery attempt %d/%d)", lastErr, restarts, s.opts.maxAttempts))
		if !waitContext(ctx, backoff) {
			return
		}
		if backoff < s.opts.maxBackoff {
			backoff *= 2
			if backoff > s.opts.maxBackoff {
				backoff = s.opts.maxBackoff
			}
		}
	}
}

func (s *supervisor) status() []model.CoreStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(make([]model.CoreStatus, 0, len(s.states)), s.states...)
}

func (s *supervisor) wait() { s.wg.Wait() }

type serviceRecoveryState struct {
	attempts    int
	nextAttempt time.Time
}

type serviceMonitor struct {
	mu          sync.Mutex
	probe       func(ctx context.Context, unit string) (bool, string)
	start       func(ctx context.Context, unit string) error
	now         func() time.Time
	minBackoff  time.Duration
	maxBackoff  time.Duration
	maxAttempts int
	states      map[string]*serviceRecoveryState
}

func newServiceMonitor(
	probe func(ctx context.Context, unit string) (bool, string),
	start func(ctx context.Context, unit string) error,
	minBackoff, maxBackoff time.Duration,
) *serviceMonitor {
	if probe == nil {
		probe = probeSystemdService
	}
	if minBackoff <= 0 {
		minBackoff = 2 * time.Second
	}
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Minute
	}
	if maxBackoff < minBackoff {
		maxBackoff = minBackoff
	}
	return &serviceMonitor{
		probe:       probe,
		start:       start,
		now:         time.Now,
		minBackoff:  minBackoff,
		maxBackoff:  maxBackoff,
		maxAttempts: maxCoreRecoveryAttempts,
		states:      make(map[string]*serviceRecoveryState),
	}
}

func (m *serviceMonitor) check(ctx context.Context, services []ExternalService) []model.CoreStatus {
	if len(services) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	activeUnits := make(map[string]struct{}, len(services))
	statuses := make([]model.CoreStatus, 0, len(services))
	for _, svc := range services {
		activeUnits[svc.Unit] = struct{}{}
		statuses = append(statuses, m.checkOneLocked(ctx, svc))
	}
	for unit := range m.states {
		if _, ok := activeUnits[unit]; !ok {
			delete(m.states, unit)
		}
	}
	return statuses
}

func (m *serviceMonitor) checkOneLocked(ctx context.Context, svc ExternalService) model.CoreStatus {
	running, msg := m.probe(ctx, svc.Unit)
	if running {
		delete(m.states, svc.Unit)
		return model.CoreStatus{Name: svc.Name, Running: true}
	}
	if msg == "" {
		msg = "systemd unit " + svc.Unit + " is not active"
	}
	if m.start == nil {
		return model.CoreStatus{Name: svc.Name, Running: false, Error: msg}
	}

	st, ok := m.states[svc.Unit]
	if !ok {
		st = &serviceRecoveryState{}
		m.states[svc.Unit] = st
	}
	if st.attempts >= m.maxAttempts {
		return model.CoreStatus{
			Name:    svc.Name,
			Running: false,
			Error:   fmt.Sprintf("%s (gave up after %d recovery attempts)", msg, m.maxAttempts),
		}
	}
	now := m.now()
	if !st.nextAttempt.IsZero() && now.Before(st.nextAttempt) {
		return model.CoreStatus{
			Name:    svc.Name,
			Running: false,
			Error:   fmt.Sprintf("%s (recovery attempt %d/%d failed; backing off)", msg, st.attempts, m.maxAttempts),
		}
	}

	st.attempts++
	_ = m.start(ctx, svc.Unit)
	if ctx.Err() == nil {
		if recovered, _ := m.probe(ctx, svc.Unit); recovered {
			delete(m.states, svc.Unit)
			return model.CoreStatus{Name: svc.Name, Running: true}
		}
	}
	if st.attempts >= m.maxAttempts {
		st.nextAttempt = time.Time{}
		return model.CoreStatus{
			Name:    svc.Name,
			Running: false,
			Error:   fmt.Sprintf("%s (gave up after %d recovery attempts)", msg, m.maxAttempts),
		}
	}
	st.nextAttempt = m.now().Add(m.backoffForAttempt(st.attempts))
	return model.CoreStatus{
		Name:    svc.Name,
		Running: false,
		Error:   fmt.Sprintf("%s (recovery attempt %d/%d failed)", msg, st.attempts, m.maxAttempts),
	}
}

func (m *serviceMonitor) backoffForAttempt(attempt int) time.Duration {
	delay := m.minBackoff
	for i := 1; i < attempt && delay < m.maxBackoff; i++ {
		delay *= 2
		if delay > m.maxBackoff {
			delay = m.maxBackoff
		}
	}
	return delay
}

func probeSystemdService(ctx context.Context, unit string) (bool, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "systemctl", "is-active", "--quiet", "--", unit)
	cmd.Env = coreEnvironment(os.Environ())
	if err := cmd.Run(); err != nil {
		return false, "systemd unit " + unit + " is not active"
	}
	return true, ""
}

func defaultServiceStarter(configDir string) func(ctx context.Context, unit string) error {
	return func(ctx context.Context, unit string) error {
		return startSystemdService(ctx, configDir, unit)
	}
}

func startSystemdService(ctx context.Context, configDir, unit string) error {
	if !validUnitName(unit) {
		return errors.New("invalid systemd unit name")
	}
	startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(startCtx, "systemctl", "start", "--no-ask-password", "--", unit)
	cmd.Env = coreEnvironment(os.Environ())
	if err := cmd.Run(); err == nil {
		return nil
	}
	if configDir == "" {
		return errors.New("systemctl start failed")
	}
	recoverDir := filepath.Join(configDir, "recover")
	info, err := os.Stat(recoverDir)
	if err != nil || !info.IsDir() {
		return errors.New("systemctl start failed and recovery trigger directory unavailable")
	}
	triggerPath := filepath.Join(recoverDir, unit+".trigger")
	if err := atomicWrite(triggerPath, []byte("1\n")); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !waitContext(startCtx, 150*time.Millisecond) {
			_ = os.Remove(triggerPath)
			return startCtx.Err()
		}
		if _, statErr := os.Stat(triggerPath); errors.Is(statErr, os.ErrNotExist) {
			if running, _ := probeSystemdService(startCtx, unit); running {
				return nil
			}
			return errors.New("scoped recovery unit completed but target unit is still inactive")
		}
	}
	_ = os.Remove(triggerPath)
	return errors.New("timed out waiting for scoped recovery unit")
}

func coreEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if name != "ENROLL_TOKEN" && name != "PROXY_ENROLL_TOKEN" {
			clean = append(clean, item)
		}
	}
	return clean
}
