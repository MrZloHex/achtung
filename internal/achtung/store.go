package achtung

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	log "log/slog"
	"os"
	"path/filepath"
	"time"
)

// maxStateBytes is far more than maxJobs jobs take: a file larger than
// this is not one achtung wrote.
const maxStateBytes = 1 << 20

// Store persists the job set so a restart does not silently lose every
// timer and alarm. It is deliberately dumb: the whole set is rewritten on
// each change. The job count here is in the tens, so there is nothing to
// gain from anything cleverer.
type Store struct {
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

// Path is where this store reads and writes. Empty means persistence is
// disabled.
func (s *Store) Path() string { return s.path }

// Load reads the persisted jobs, dropping any that are no longer
// meaningful and re-arming the repeating ones.
//
// One-shot jobs whose due time has passed while the process was down are
// discarded: firing them at boot would be worse than silence. Repeating
// jobs are advanced to their next future occurrence.
//
// A file that cannot be right — too large, two jobs of one name, a job
// that is no job — is an error: achtung does not start, and the file is
// left for someone to look at, rather than written over by the next
// change with whatever loaded.
func (s *Store) Load(now time.Time) ([]Job, error) {
	if s.path == "" {
		return nil, nil
	}

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(b) > maxStateBytes {
		return nil, fmt.Errorf("%s: larger than achtung ever writes", s.path)
	}

	var stored []Job
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}

	out := make([]Job, 0, len(stored))
	seen := map[string]bool{}
	for _, j := range stored {
		if !j.Active {
			continue
		}
		if err := j.valid(); err != nil {
			return nil, fmt.Errorf("%s: job %q: %w", s.path, j.Name, err)
		}
		if j.Kind == KindEvery && j.Interval < time.Second {
			return nil, fmt.Errorf("%s: job %q fires more often than once a second", s.path, j.Name)
		}
		if seen[j.Name] {
			return nil, fmt.Errorf("%s: two jobs are named %q", s.path, j.Name)
		}
		seen[j.Name] = true
		if j.Repeating() {
			next, ok := j.NextAfter(now)
			if !ok {
				log.Warn("DROP JOB", "name", j.Name, "kind", j.Kind, "reason", "cannot compute next fire")
				continue
			}
			if next != j.Due {
				log.Info("RE-ARM JOB", "name", j.Name, "kind", j.Kind,
					"was", j.Due.Format(time.DateTime), "now", next.Format(time.DateTime))
			}
			j.Due = next
			out = append(out, j)
			continue
		}
		if !j.Due.After(now) {
			log.Info("DROP MISSED JOB", "name", j.Name, "kind", j.Kind,
				"due", j.Due.Format(time.DateTime))
			continue
		}
		out = append(out, j)
	}
	if len(out) > maxJobs {
		return nil, fmt.Errorf("%s: %d jobs, more than achtung keeps (%d)", s.path, len(out), maxJobs)
	}
	return out, nil
}

// Save atomically replaces the file. The temp file is created in the same
// directory so the rename cannot cross a filesystem boundary, and
// os.CreateTemp makes it readable by its owner only, which the rename keeps.
func (s *Store) Save(jobs []Job) error {
	if s.path == "" {
		return nil
	}

	active := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		if j.Active {
			active = append(active, j)
		}
	}

	b, err := json.MarshalIndent(active, "", "  ")
	if err != nil {
		return fmt.Errorf("encode jobs: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.path)
	// The directory is opened first: one that cannot be synced fails the save
	// before anything has changed.
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer d.Close()
	tmp, err := os.CreateTemp(dir, ".achtung-jobs-*")
	if err != nil {
		return fmt.Errorf("temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	defer func() {
		// No-op once the rename has succeeded.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename onto %s: %w", s.path, err)
	}
	// Committed: the new file is what any reader now gets, so a failed sync
	// of the directory is not a failed save — undoing the change would make
	// memory disagree with the file. A power cut could still bring the old
	// file back, and that is said loudly.
	if err := d.Sync(); err != nil {
		log.Error("JOBS SAVED BUT NOT SYNCED: a power cut could undo the last change", "dir", dir, "err", err)
	}
	return nil
}
