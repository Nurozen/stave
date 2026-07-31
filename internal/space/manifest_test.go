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
	// A saga-less manifest stamps 1, not the binary's ceiling, so older
	// binaries keep full read/write access to it.
	if loaded.Version != 1 {
		t.Fatalf("loaded.Version = %d, want 1", loaded.Version)
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
	if reloaded.Version != 1 {
		t.Fatalf("version after save = %d, want 1", reloaded.Version)
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

func TestManifestSagaVersionStamping(t *testing.T) {
	dir := t.TempDir()
	saga := Manifest{
		ID:        "saga-space",
		Kind:      KindSaga,
		CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		Repos:     []RepoManifest{},
		Saga: &SagaManifest{Members: []SagaMember{
			{ID: "member-a"},
			{ID: "member-b", After: []string{"member-a"}},
		}},
	}
	if err := SaveManifest(dir, saga); err != nil {
		t.Fatalf("SaveManifest saga: %v", err)
	}
	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 2 {
		t.Fatalf("saga manifest version = %d, want 2", loaded.Version)
	}
	if loaded.Saga == nil || len(loaded.Saga.Members) != 2 || loaded.Saga.Members[1].After[0] != "member-a" {
		t.Fatalf("saga round-trip = %#v", loaded.Saga)
	}

	// Saving the loaded v2 manifest again keeps it at 2.
	if err := SaveManifest(dir, loaded); err != nil {
		t.Fatalf("SaveManifest saga again: %v", err)
	}
	reloaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Version != 2 {
		t.Fatalf("resaved saga version = %d, want 2", reloaded.Version)
	}
}

func TestManifestPlainStaysV1UntilSagaAppears(t *testing.T) {
	dir := t.TempDir()
	plain := Manifest{
		ID:        "grows-a-saga",
		CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		Repos:     []RepoManifest{},
	}
	if err := SaveManifest(dir, plain); err != nil {
		t.Fatal(err)
	}
	// Load/save with no saga leaves the file at 1 however often it round-trips.
	for i := 0; i < 2; i++ {
		loaded, err := LoadManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Version != 1 {
			t.Fatalf("round-trip %d version = %d, want 1", i, loaded.Version)
		}
		if err := SaveManifest(dir, loaded); err != nil {
			t.Fatal(err)
		}
	}

	// Gaining a saga block promotes the file to 2.
	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Kind = KindSaga
	loaded.Saga = &SagaManifest{Members: []SagaMember{{ID: "member-a"}}}
	if err := SaveManifest(dir, loaded); err != nil {
		t.Fatal(err)
	}
	promoted, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Version != 2 {
		t.Fatalf("version after gaining saga = %d, want 2", promoted.Version)
	}
}

func TestManifestOldWriterRefusesV2File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestName)
	saga := Manifest{
		ID:        "saga-space",
		Kind:      KindSaga,
		CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		Repos:     []RepoManifest{},
		Saga:      &SagaManifest{Members: []SagaMember{{ID: "member-a"}}},
	}
	if err := SaveManifest(dir, saga); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A binary that only understands v1 must refuse to write over it.
	old := saga
	old.Saga = nil
	old.Kind = "ticket"
	err = saveManifestWithCeiling(path, old, 1)
	var tooNew *ErrManifestVersionTooNew
	if !errors.As(err, &tooNew) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if tooNew.OnDisk != 2 || tooNew.Current != 1 {
		t.Fatalf("ceiling err = %#v", tooNew)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("v2 file mutated by old writer:\n%s", after)
	}
}

func TestManifestValidateSaga(t *testing.T) {
	members := func(m ...SagaMember) *SagaManifest { return &SagaManifest{Members: m} }
	cases := []struct {
		name    string
		saga    *SagaManifest
		wantErr bool
	}{
		{name: "empty roster", saga: members()},
		{name: "chain", saga: members(
			SagaMember{ID: "a"},
			SagaMember{ID: "b", After: []string{"a"}},
			SagaMember{ID: "c", After: []string{"b"}},
		)},
		{name: "diamond", saga: members(
			SagaMember{ID: "a"},
			SagaMember{ID: "b", After: []string{"a"}},
			SagaMember{ID: "c", After: []string{"a"}},
			SagaMember{ID: "d", After: []string{"b", "c"}},
		)},
		{name: "bad member charset", saga: members(SagaMember{ID: "../x"}), wantErr: true},
		{name: "duplicate members", saga: members(
			SagaMember{ID: "a"},
			SagaMember{ID: "a"},
		), wantErr: true},
		{name: "after target not a member", saga: members(
			SagaMember{ID: "a", After: []string{"ghost"}},
		), wantErr: true},
		{name: "two cycle", saga: members(
			SagaMember{ID: "a", After: []string{"b"}},
			SagaMember{ID: "b", After: []string{"a"}},
		), wantErr: true},
		{name: "self cycle", saga: members(
			SagaMember{ID: "a", After: []string{"a"}},
		), wantErr: true},
		{name: "cycle behind an acyclic prefix", saga: members(
			SagaMember{ID: "a"},
			SagaMember{ID: "b", After: []string{"a", "c"}},
			SagaMember{ID: "c", After: []string{"b"}},
		), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Manifest{ID: "saga-space", Kind: KindSaga, Saga: tc.saga}
			err := m.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestManifestLoadHandWrittenV2Saga(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`version: 2
id: rollout
kind: saga
createdAt: 2026-07-14T00:00:00Z
repos: []
saga:
  members:
    - id: member-a
      createdAt: 2026-07-14T01:00:00Z
      prs:
        - repo: api
          number: 12
    - id: member-b
      after:
        - member-a
`)
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || m.Kind != KindSaga || m.Saga == nil {
		t.Fatalf("loaded = %#v", m)
	}
	if len(m.Saga.Members) != 2 {
		t.Fatalf("members = %#v", m.Saga.Members)
	}
	first := m.Saga.Members[0]
	if !first.CreatedAt.Equal(time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("member createdAt = %v", first.CreatedAt)
	}
	if len(first.PRs) != 1 || first.PRs[0].Repo != "api" || first.PRs[0].Number != 12 {
		t.Fatalf("member prs = %#v", first.PRs)
	}
	if second := m.Saga.Members[1]; len(second.After) != 1 || second.After[0] != "member-a" {
		t.Fatalf("member after = %#v", m.Saga.Members[1])
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
