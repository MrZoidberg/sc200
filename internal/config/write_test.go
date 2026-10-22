package config

import (
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addition(port string) Enclosure {
	e := DefaultEnclosure()
	e.ID = "sc200-device"
	e.Port = port
	e.RequiredSensors = []int{0, 1}
	return e
}
func pathIdentity(p string) (string, error) { return p, nil }
func TestMergePreviewCreatePreserveAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.yaml")
	first := addition("/dev/a")
	preview, err := MergeFile(path, []Enclosure{first}, false, pathIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("preview wrote directory")
	}
	if _, err = Decode(bytes.NewReader(preview)); err != nil {
		t.Fatal(err)
	}
	data, err := MergeFile(path, []Enclosure{first}, true, pathIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, preview) {
		t.Fatal("preview mismatch")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0640 {
		t.Fatal(st.Mode())
	}
	custom := strings.Replace(string(data), "listen: 127.0.0.1:9700", "listen: 127.0.0.1:9800 # local endpoint", 1)
	custom = "# retained comment\n" + custom
	// Retain a disconnected disabled entry, including its comments and custom policy.
	custom += "\n    - id: offline\n      enabled: false # disconnected shelf\n      port: /dev/gone\n      min_speed: 25\n      curve: [{temperature: 25, speed: 25}, {temperature: 45, speed: 100}]\n"
	if err = os.WriteFile(path, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	identity := func(p string) (string, error) {
		if p == "/dev/gone" {
			return "", os.ErrNotExist
		}
		if p == "/dev/alias" {
			return "/dev/a", nil
		}
		return p, nil
	}
	data, err = MergeFile(path, []Enclosure{addition("/dev/alias"), addition("/dev/b")}, true, identity)
	if err != nil {
		t.Fatalf("%v\n%s", err, custom)
	}
	cfg, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Enclosures) != 3 || cfg.Enclosures[2].ID != "sc200-device-2" || cfg.Enclosures[1].Enabled || cfg.HTTP.Listen != "127.0.0.1:9800" {
		t.Fatalf("%+v", cfg)
	}
	if !strings.Contains(string(data), "# retained comment") || !strings.Contains(string(data), "# disconnected shelf") {
		t.Fatal(string(data))
	}
	before, _ := os.Stat(path)
	again, err := MergeFile(path, []Enclosure{addition("/dev/b")}, true, identity)
	if err != nil || !bytes.Equal(again, data) {
		t.Fatalf("%s %v", again, err)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("idempotent write replaced file")
	}
}
func TestMergeRejectsInvalidAndDuplicateIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	bad := []byte("enclosures: [oops]")
	os.WriteFile(path, bad, 0600)
	if _, err := MergeFile(path, []Enclosure{addition("/dev/a")}, true, pathIdentity); err == nil {
		t.Fatal("invalid existing accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, bad) {
		t.Fatal("overwrote invalid config")
	}
	data, err := MergeFile(filepath.Join(t.TempDir(), "a"), []Enclosure{addition("/dev/a"), addition("/dev/b")}, false, pathIdentity)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, data, 0600)
	if _, err = MergeFile(path, []Enclosure{addition("/dev/c")}, true, func(string) (string, error) { return "same", nil }); err == nil {
		t.Fatal("duplicate identities accepted")
	}
	if _, err = MergeFile(path, []Enclosure{addition("/dev/c")}, true, func(string) (string, error) { return "", os.ErrPermission }); err == nil {
		t.Fatal("identity error ignored")
	}
}
func TestMergeConcurrentEditDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original, err := MergeFile(path, []Enclosure{addition("/dev/a")}, true, pathIdentity)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]byte("# external change\n"), original...)
	changed := false
	_, err = MergeFile(path, []Enclosure{addition("/dev/b")}, true, func(p string) (string, error) {
		if !changed {
			changed = true
			if e := os.WriteFile(path, edited, 0600); e != nil {
				t.Fatal(e)
			}
		}
		return p, nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, edited) {
		t.Fatal("clobbered external edit")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".sc200-config-*"))
	if len(files) != 0 {
		t.Fatal(files)
	}
}
func TestMergeWriterLockAndErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err = MergeFile(path, []Enclosure{addition("/dev/a")}, true, pathIdentity); err == nil {
		t.Fatal("lock conflict ignored")
	}
	if _, err = MergeFile(filepath.Join(path+".lock", "x"), []Enclosure{addition("/dev/a")}, true, pathIdentity); err == nil {
		t.Fatal("directory error ignored")
	}
	if _, err = MergeFile(dir, []Enclosure{addition("/dev/a")}, false, pathIdentity); err == nil {
		t.Fatal("read error ignored")
	}
	data, err := MergeFile(path, nil, true, pathIdentity)
	if err != nil || len(data) != 0 {
		t.Fatalf("%s %v", data, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no discoveries wrote config")
	}
	bad := addition("/dev/a")
	bad.SafeSpeed = 1
	if _, err = MergeFile(filepath.Join(dir, "bad"), []Enclosure{bad}, true, pathIdentity); err == nil {
		t.Fatal("invalid merged config accepted")
	}
}
func TestMergeNewFileAppeared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, err := MergeFile(path, []Enclosure{addition("/dev/a")}, true, func(p string) (string, error) { os.WriteFile(path, []byte("external"), 0600); return p, nil })
	if err == nil || !strings.Contains(err.Error(), "appeared") {
		t.Fatal(err)
	}
}
