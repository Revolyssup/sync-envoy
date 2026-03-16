package lock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/revolyssup/sync-envoy/pkg/logging"
)

// Instance represents a single running sync-envoy process.
type Instance struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Directory string `json:"directory"`
	StartedAt string `json:"started_at"`
}

// LockFile represents the contents of ~/.sync-envoy.lock.json.
type LockFile struct {
	Instances []Instance `json:"instances"`
}

// lockFilePath returns the path to the global lock file.
func lockFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		logging.Warn("Cannot determine home directory, using /tmp for lock file: %v", err)
		return "/tmp/sync-envoy.lock.json"
	}
	return filepath.Join(home, ".sync-envoy.lock.json")
}

// withLockedFile opens the lock file with an exclusive flock, reads the current
// state, calls fn, and writes back the result. If fn returns a nil LockFile,
// nothing is written back.
func withLockedFile(fn func(lf *LockFile) (*LockFile, error)) error {
	path := lockFilePath()

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open lock file %s: %w", path, err)
	}
	defer f.Close()

	// Acquire exclusive lock (blocks until available).
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	// Read current contents.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read lock file: %w", err)
	}

	var lf LockFile
	if len(data) > 0 {
		if err := json.Unmarshal(data, &lf); err != nil {
			logging.Warn("Lock file corrupted, resetting: %v", err)
			lf = LockFile{}
		}
	}

	result, err := fn(&lf)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}

	// Write back.
	if len(result.Instances) == 0 {
		// No instances left — remove the file entirely.
		os.Remove(path)
		return nil
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock file: %w", err)
	}

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate lock file: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek lock file: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		return fmt.Errorf("write lock file: %w", err)
	}
	return nil
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// removeStale filters out instances whose PIDs are no longer alive.
func removeStale(instances []Instance) []Instance {
	alive := make([]Instance, 0, len(instances))
	for _, inst := range instances {
		if isProcessAlive(inst.PID) {
			alive = append(alive, inst)
		} else {
			logging.Debug("Removing stale lock entry: instance %s (pid %d, dir %s)", inst.ID, inst.PID, inst.Directory)
		}
	}
	return alive
}

// Acquire registers the current process as the owner of the given directory.
// Returns a unique instance ID on success. Returns an error if the directory
// is already locked by a live process.
func Acquire(dir string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}

	var instanceID string

	err = withLockedFile(func(lf *LockFile) (*LockFile, error) {
		// Remove stale entries.
		lf.Instances = removeStale(lf.Instances)

		// Check for conflict.
		for _, inst := range lf.Instances {
			if inst.Directory == absDir {
				return nil, fmt.Errorf("directory %s is already locked by instance %s (pid %d, started %s)",
					absDir, inst.ID, inst.PID, inst.StartedAt)
			}
		}

		// Register new instance.
		instanceID = uuid.New().String()
		lf.Instances = append(lf.Instances, Instance{
			ID:        instanceID,
			PID:       os.Getpid(),
			Directory: absDir,
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		})

		return lf, nil
	})

	return instanceID, err
}

// Release removes the instance entry with the given ID from the lock file.
func Release(instanceID string) error {
	if instanceID == "" {
		return nil
	}

	return withLockedFile(func(lf *LockFile) (*LockFile, error) {
		filtered := make([]Instance, 0, len(lf.Instances))
		for _, inst := range lf.Instances {
			if inst.ID != instanceID {
				filtered = append(filtered, inst)
			}
		}
		lf.Instances = filtered
		return lf, nil
	})
}

// CheckDirectory checks whether a directory is locked by an active instance.
// Returns the owner's PID and true if locked, or 0 and false if not.
func CheckDirectory(dir string) (int, bool, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return 0, false, fmt.Errorf("resolve absolute path: %w", err)
	}

	path := lockFilePath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0, false, nil
	}

	var ownerPID int
	var locked bool

	err = withLockedFile(func(lf *LockFile) (*LockFile, error) {
		// Remove stale entries (housekeeping).
		lf.Instances = removeStale(lf.Instances)

		for _, inst := range lf.Instances {
			if inst.Directory == absDir {
				ownerPID = inst.PID
				locked = true
				break
			}
		}

		// Write back cleaned state.
		return lf, nil
	})

	return ownerPID, locked, err
}
