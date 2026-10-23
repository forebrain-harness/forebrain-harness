package llm

import (
	"path/filepath"
	"sync"
)

// credentialLocks holds one mutex per credentials file, so every client of a
// login file in this process refreshes it one at a time. A refresh token that
// rotates on use cannot be refreshed twice from the same old value: the second
// caller must wait, then read what the first one wrote.
var credentialLocks sync.Map // absolute path -> *sync.Mutex

// CredentialMutex returns the mutex that serializes token refresh for the
// credentials file at path. Two spellings of one file ("a/../f" and "f") share
// it; the lock lives for the life of the process.
func CredentialMutex(path string) *sync.Mutex {
	key := filepath.Clean(path)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	mu, _ := credentialLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
