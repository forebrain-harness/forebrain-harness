package state

import (
	"os"
	"path/filepath"
	"strings"
)

func intermediatePath(home, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	return filepath.Join(home, "state", "intermediate", sessionID+".md")
}

func Read(home, sessionID string) (string, error) {
	p := intermediatePath(home, sessionID)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func Append(home, sessionID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	p := intermediatePath(home, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(text + "\n\n"); err != nil {
		return err
	}
	return nil
}

func Clear(home, sessionID string) error {
	p := intermediatePath(home, sessionID)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func Path(home, sessionID string) string {
	return intermediatePath(home, sessionID)
}
