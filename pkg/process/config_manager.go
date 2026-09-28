package process

import "sync"

// ActiveRunProbe reports active foreground runs without exposing their state.
type ActiveRunProbe interface {
	Active() int
}

// ConfigManager defers reload while foreground work is active.
type ConfigManager struct {
	mu       sync.Mutex
	probe    ActiveRunProbe
	apply    func() error
	pending  bool
	applying bool
}

// NewConfigManager creates a reload coordinator.
func NewConfigManager(probe ActiveRunProbe, apply func() error) *ConfigManager {
	return &ConfigManager{probe: probe, apply: apply}
}

// SetProbe replaces the activity source used to defer reloads.
func (m *ConfigManager) SetProbe(probe ActiveRunProbe) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.probe = probe
	m.mu.Unlock()
}

// Pending reports whether a reload is waiting for the foreground to go idle.
func (m *ConfigManager) Pending() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending
}

// Request marks a new config as pending and applies it when idle.
func (m *ConfigManager) Request() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.pending = true
	m.mu.Unlock()
	return m.applyIfIdle()
}

// Idle applies the latest pending config after a run completes.
func (m *ConfigManager) Idle() error {
	if m == nil {
		return nil
	}
	return m.applyIfIdle()
}

func (m *ConfigManager) applyIfIdle() error {
	for {
		m.mu.Lock()
		if !m.pending || m.applying || (m.probe != nil && m.probe.Active() > 0) {
			m.mu.Unlock()
			return nil
		}
		m.pending = false
		m.applying = true
		apply := m.apply
		m.mu.Unlock()

		var err error
		if apply != nil {
			err = apply()
		}
		m.mu.Lock()
		m.applying = false
		again := err == nil && m.pending && (m.probe == nil || m.probe.Active() == 0)
		m.mu.Unlock()
		if err != nil || !again {
			return err
		}
	}
}
