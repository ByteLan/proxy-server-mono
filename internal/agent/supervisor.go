package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"proxy-mono/internal/model"
)

type supervisor struct {
	mu     sync.Mutex
	states []model.CoreStatus
	wg     sync.WaitGroup
}

func newSupervisor(ctx context.Context, commands []Command) *supervisor {
	s := &supervisor{states: make([]model.CoreStatus, len(commands))}
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
	backoff := time.Second
	for ctx.Err() == nil {
		process := exec.Command(command.Path, command.Args...)
		// Enrollment is only an Agent responsibility. Do not leak the bootstrap
		// key to proxy cores or wrappers through inherited environment variables.
		process.Env = coreEnvironment(os.Environ())
		// Core output can contain connection material. Let the core manage any
		// explicitly configured logs; never forward stdout/stderr to agent logs.
		started := time.Now()
		if err := process.Start(); err != nil {
			s.set(index, false, "core failed to start; check executable path and permissions")
		} else {
			s.set(index, true, "")
			finished := make(chan error, 1)
			go func() { finished <- process.Wait() }()
			select {
			case err := <-finished:
				message := "core exited"
				if exit, ok := err.(*exec.ExitError); ok {
					message = fmt.Sprintf("core exited with status %d", exit.ExitCode())
				}
				s.set(index, false, message)
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
		if time.Since(started) >= time.Minute {
			backoff = time.Second
		}
		if !waitContext(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
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
