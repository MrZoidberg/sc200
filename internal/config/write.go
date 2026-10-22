package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

// MergeFile previews or atomically extends a valid configuration. Identity must
// follow device aliases; absent configured devices retain their canonical paths.
func MergeFile(path string, additions []Enclosure, write bool, identity func(string) (string, error)) ([]byte, error) {
	fail := func(err error) ([]byte, error) {
		return nil, fmt.Errorf("configuration %s (check directory permissions and ownership): %w", path, err)
	}
	if write && len(additions) > 0 {
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			return fail(err)
		}
	}
	var lock *os.File
	if write && len(additions) > 0 {
		fd, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return fail(err)
		}
		lock = os.NewFile(uintptr(fd), path+".lock")
		defer func() { _ = lock.Close() }()
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return fail(fmt.Errorf("another config writer is active: %w", err))
		}
	}
	original, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if len(additions) == 0 {
		return original, nil
	}
	var root yaml.Node
	if exists {
		if _, err = Decode(bytes.NewReader(original)); err != nil {
			return fail(err)
		}
		if err = yaml.Unmarshal(original, &root); err != nil {
			return fail(err)
		}
	} else {
		c := Default()
		c.Enclosures = []Enclosure{}
		if err = root.Encode(c); err != nil {
			return fail(err)
		}
	}
	node := &root
	if node.Kind == yaml.DocumentNode {
		node = node.Content[0]
	}
	var sequence *yaml.Node
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == "enclosures" {
			sequence = node.Content[i+1]
		}
	}
	if sequence == nil {
		return fail(fmt.Errorf("missing enclosures sequence"))
	}
	if !exists {
		sequence.Style = 0
	}
	var existing []Enclosure
	if err = sequence.Decode(&existing); err != nil {
		return fail(err)
	}
	ids := map[string]bool{}
	devices := map[string]bool{}
	deviceID := func(p string) (string, error) {
		id, err := identity(p)
		if errors.Is(err, os.ErrNotExist) {
			return "path:" + filepath.Clean(p), nil
		}
		return id, err
	}
	for _, e := range existing {
		id, err := deviceID(e.Port)
		if err != nil {
			return fail(err)
		}
		if devices[id] {
			return fail(fmt.Errorf("conflicting device identities for %s", e.Port))
		}
		devices[id] = true
		ids[e.ID] = true
	}
	added := false
	for _, e := range additions {
		id, err := deviceID(e.Port)
		if err != nil {
			return fail(err)
		}
		if devices[id] {
			continue
		}
		devices[id] = true
		base := e.ID
		for n := 2; ids[e.ID]; n++ {
			e.ID = fmt.Sprintf("%s-%d", base, n)
		}
		ids[e.ID] = true
		var entry yaml.Node
		if err = entry.Encode(e); err != nil {
			return fail(err)
		}
		sequence.Content = append(sequence.Content, &entry)
		added = true
	}
	data, err := yaml.Marshal(&root)
	if err != nil {
		return fail(err)
	}
	if _, err = Decode(bytes.NewReader(data)); err != nil {
		return fail(err)
	}
	if !write || !added {
		return data, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sc200-config-*")
	if err != nil {
		return fail(err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	defer func() { _ = tmp.Close() }()
	mode := os.FileMode(0640)
	if exists {
		st, err := os.Stat(path)
		if err != nil {
			return fail(err)
		}
		mode = st.Mode().Perm()
	}
	if err = tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fail(err)
	}
	if err = tmp.Sync(); err != nil {
		return fail(err)
	}
	if err = tmp.Close(); err != nil {
		return fail(err)
	}
	current, readErr := os.ReadFile(path)
	if exists {
		if readErr != nil || !bytes.Equal(current, original) {
			return fail(fmt.Errorf("configuration changed during discovery; retry"))
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fail(fmt.Errorf("configuration appeared during discovery; retry"))
	}
	if err = os.Rename(name, path); err != nil {
		return fail(err)
	}
	return data, nil
}
