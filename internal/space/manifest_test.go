package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestManifestRoundTripWithAndWithoutMemories(t *testing.T) {
	dir := t.TempDir()

	// Without memories: omitempty must keep the key out of the file.
	plain := Manifest{
		ID:        "plain-space",
		CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		Repos:     []RepoManifest{},
	}
	if err := SaveManifest(dir, plain); err != nil {
		t.Fatalf("SaveManifest plain: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "memories:") {
		t.Fatalf("plain manifest should omit memories key:\n%s", raw)
	}
	if !strings.Contains(string(raw), "version:") {
		t.Fatalf("saved manifest should carry version:\n%s", raw)
	}
	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "plain-space" || loaded.Memories != nil {
		t.Fatalf("loaded plain = %#v", loaded)
	}
	if loaded.Version != CurrentManifestVersion {
		t.Fatalf("loaded.Version = %d, want %d", loaded.Version, CurrentManifestVersion)
	}

	// With memories: round-trip preserves fields.
	withMem := Manifest{
		ID:        "mem-space",
		CreatedAt: time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC),
		Repos:     []RepoManifest{},
		Memories: []MemoryManifest{
			{Name: "default", Provider: "marmot", ID: "mem-space", Owned: true},
			{Name: "shared", Provider: "marmot", ID: "durable-den", Owned: false},
		},
	}
	memDir := t.TempDir()
	if err := SaveManifest(memDir, withMem); err != nil {
		t.Fatalf("SaveManifest with memories: %v", err)
	}
	raw, err = os.ReadFile(filepath.Join(memDir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "memories:") {
		t.Fatalf("expected memories key:\n%s", raw)
	}
	if !strings.Contains(string(raw), "owned: true") && !strings.Contains(string(raw), "owned:true") {
		// yaml.v3 emits "owned: true"
		if !strings.Contains(string(raw), "owned:") {
			t.Fatalf("expected owned field:\n%s", raw)
		}
	}
	reloaded, err := LoadManifest(memDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Memories) != 2 {
		t.Fatalf("memories = %#v", reloaded.Memories)
	}
	if reloaded.Memories[0].Name != "default" || !reloaded.Memories[0].Owned {
		t.Fatalf("mem[0] = %#v", reloaded.Memories[0])
	}
	if reloaded.Memories[1].Owned {
		t.Fatalf("mem[1] owned should be false: %#v", reloaded.Memories[1])
	}
}

func TestManifestLoadPreS1Compat(t *testing.T) {
	dir := t.TempDir()
	// Pre-S1 file: no version, no memories.
	preS1 := []byte("id: legacy\ncreatedAt: 2026-01-01T00:00:00Z\nrepos: []\n")
	if err := os.WriteFile(filepath.Join(dir, ManifestName), preS1, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "legacy" || m.Version != 0 || m.Memories != nil {
		t.Fatalf("legacy load = %#v", m)
	}
	// Saving upgrades version but does not invent memories.
	if err := SaveManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "memories:") {
		t.Fatalf("save of memory-less should omit memories:\n%s", raw)
	}
	reloaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Version != CurrentManifestVersion {
		t.Fatalf("version after save = %d", reloaded.Version)
	}
}

func TestManifestVersionCeilingRefusesSave(t *testing.T) {
	dir := t.TempDir()
	// Plant a future-version file on disk.
	future := Manifest{
		Version:   CurrentManifestVersion + 5,
		ID:        "future",
		CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		Repos:     []RepoManifest{},
	}
	data, err := yaml.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Load is permissive.
	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentManifestVersion+5 {
		t.Fatalf("loaded version = %d", loaded.Version)
	}

	// Save must refuse.
	loaded.Kind = "ticket"
	err = SaveManifest(dir, loaded)
	if err == nil {
		t.Fatal("expected version ceiling error")
	}
	var tooNew *ErrManifestVersionTooNew
	if !errors.As(err, &tooNew) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if tooNew.OnDisk != CurrentManifestVersion+5 || tooNew.Current != CurrentManifestVersion {
		t.Fatalf("ceiling err = %#v", tooNew)
	}

	// On-disk content must be unchanged.
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var still Manifest
	if err := yaml.Unmarshal(raw, &still); err != nil {
		t.Fatal(err)
	}
	if still.Kind != "" {
		t.Fatalf("on-disk mutated despite ceiling: %#v", still)
	}
}

func TestManifestValidateMemoryNames(t *testing.T) {
	m := Manifest{
		ID: "ok",
		Memories: []MemoryManifest{
			{Name: "../bad", Provider: "marmot", ID: "x", Owned: true},
		},
	}
	if err := m.Validate(); err == nil {
		t.Fatal("expected invalid memory name")
	}
	m.Memories[0].Name = "ok"
	m.Memories[0].ID = "has/slash"
	if err := m.Validate(); err == nil {
		t.Fatal("expected invalid memory id")
	}
}

func TestFindMemory(t *testing.T) {
	m := Manifest{Memories: []MemoryManifest{
		{Name: "default", ID: "den-a", Provider: "marmot"},
		{Name: "shared", ID: "den-b", Provider: "marmot"},
	}}
	if _, _, ok := m.FindMemory(""); ok {
		t.Fatal("empty name with multiple should not resolve")
	}
	got, idx, ok := m.FindMemory("shared")
	if !ok || idx != 1 || got.ID != "den-b" {
		t.Fatalf("by name = %#v %d %v", got, idx, ok)
	}
	got, idx, ok = m.FindMemory("den-a")
	if !ok || idx != 0 {
		t.Fatalf("by id = %#v %d %v", got, idx, ok)
	}
	single := Manifest{Memories: []MemoryManifest{{Name: "only", ID: "one"}}}
	got, idx, ok = single.FindMemory("")
	if !ok || idx != 0 || got.ID != "one" {
		t.Fatalf("sole attachment = %#v %d %v", got, idx, ok)
	}
}
