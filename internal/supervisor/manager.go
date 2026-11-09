package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Spec struct {
	Name     string
	Path     string
	Args     []string
	Env      []string
	Dir      string
	Logger   *slog.Logger
	MaxDelay time.Duration
}

type Manager struct {
	spec Spec

	mu     sync.Mutex
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan struct{}
}

func New(spec Spec) *Manager {
	if spec.Logger == nil {
		spec.Logger = slog.Default()
	}
	if spec.MaxDelay <= 0 {
		spec.MaxDelay = 30 * time.Second
	}
	return &Manager{spec: spec}
}

func (m *Manager) Start(parent context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return fmt.Errorf("process %s is already started", m.spec.Name)
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done
	go m.run(ctx, done)
	return nil
}

func (m *Manager) Stop(grace time.Duration) error {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}

	cancel()
	if err := m.signalCurrent(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		m.spec.Logger.Warn("could not terminate child process", "process", m.spec.Name, "error", err)
	}

	select {
	case <-done:
		return nil
	case <-time.After(grace):
		if err := m.signalCurrent(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill process %s: %w", m.spec.Name, err)
		}
		select {
		case <-done:
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("process %s did not exit after SIGKILL", m.spec.Name)
		}
	}
}

func (m *Manager) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	delay := time.Second
	for ctx.Err() == nil {
		cmd := exec.Command(m.spec.Path, m.spec.Args...)
		cmd.Dir = m.spec.Dir
		cmd.Env = append(os.Environ(), m.spec.Env...)
		cmd.Stdout = newLineWriter(m.spec.Logger, m.spec.Name, "stdout")
		cmd.Stderr = newLineWriter(m.spec.Logger, m.spec.Name, "stderr")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		startedAt := time.Now()
		m.mu.Lock()
		if ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		m.cmd = cmd
		if err := cmd.Start(); err != nil {
			m.cmd = nil
			m.mu.Unlock()
			m.spec.Logger.Error("could not start child process", "process", m.spec.Name, "error", err)
		} else {
			m.mu.Unlock()
			m.spec.Logger.Info("child process started", "process", m.spec.Name, "pid", cmd.Process.Pid)
			err := cmd.Wait()
			m.mu.Lock()
			m.cmd = nil
			m.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			code := 0
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			if time.Since(startedAt) > 30*time.Second {
				delay = time.Second
			}
			if err != nil {
				m.spec.Logger.Warn("child process exited; restarting", "process", m.spec.Name, "exit_code", code, "delay", delay.String(), "error", err)
			} else {
				m.spec.Logger.Warn("child process exited; restarting", "process", m.spec.Name, "exit_code", code, "delay", delay.String())
			}
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > m.spec.MaxDelay {
			delay = m.spec.MaxDelay
		}
	}
}

func (m *Manager) signalCurrent(signal syscall.Signal) error {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, signal)
}

type lineWriter struct {
	logger *slog.Logger
	name   string
	stream string

	mu     sync.Mutex
	buffer string
}

func newLineWriter(logger *slog.Logger, name, stream string) *lineWriter {
	return &lineWriter{logger: logger, name: name, stream: stream}
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer += string(data)
	if len(w.buffer) > 1024*1024 {
		w.buffer = w.buffer[len(w.buffer)-1024*1024:]
	}
	for {
		index := strings.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(w.buffer[:index])
		w.buffer = w.buffer[index+1:]
		if line != "" {
			w.logger.Info("child process output", "process", w.name, "stream", w.stream, "line", line)
		}
	}
	return len(data), nil
}
