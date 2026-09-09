package space

import (
	"context"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/memory"
)

// TestServiceRefusalsCarryCodes drives the refusals a scripted caller is most
// likely to hit through the service and asserts each one arrives with a stable
// code rather than the "unknown" fallback the --json envelope emits for a bare
// fmt.Errorf.
func TestServiceRefusalsCarryCodes(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.InitSpace(ctx, InitOptions{ID: "ec-1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "ec-saga"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		err  error
		code string
	}{
		{"empty repo spec ref", parseRepoSpecErr("repo-a:"), CodeInvalidArguments},
		{"reserved kind", svc.InitSpace(ctx, InitOptions{ID: "ec-2", Kind: KindSaga}), CodeInvalidArguments},
		{"--after without --saga", svc.Create(ctx, CreateOptions{ID: "ec-3", After: []string{"ec-1"}}), CodeInvalidArguments},
		{"unknown repo mode", svc.AddRepo(ctx, AddOptions{SpaceID: "ec-1", RepoName: "repo-a", Mode: "sideways", NoFetch: true}), CodeInvalidArguments},
		{"archive with destroy fate", svc.Archive(ctx, ArchiveOptions{SpaceID: "ec-1", MemoryFate: memory.FateDestroy}), CodeInvalidArguments},
		{"tethers disabled", expandCommonErr(svc), CodeInvalidArguments},
		{"edit repo into a saga", svc.AddRepo(ctx, AddOptions{SpaceID: "ec-saga", RepoName: "repo-a", Mode: ModeEdit, NoFetch: true}), CodeSagaSpace},
		{"saga verb on a plain space", svc.SagaAdd(ctx, "ec-1", "ec-2", nil, false), CodeNotASaga},
		{"saga status on a plain space", sagaStatusErr(ctx, svc, "ec-1"), CodeNotASaga},
		{"remove a non-member", svc.SagaRemove(ctx, "ec-saga", "ec-1"), CodeNotASagaMember},
		{"saga as its own member", svc.SagaAdd(ctx, "ec-saga", "ec-saga", nil, false), CodeInvalidArguments},
	}
	for _, tc := range cases {
		if tc.err == nil {
			t.Fatalf("%s: no error", tc.name)
		}
		if got := ErrorCode(tc.err); got != tc.code {
			t.Fatalf("%s: code = %q, want %q (%v)", tc.name, got, tc.code, tc.err)
		}
	}
}

// TestAddRepoPathTakenIsCoded covers the manifest-collision refusal. The path
// is derived from the repo name, so the collision needs a manifest whose entry
// already claims the directory under a different name — a defensive check, but
// one that a --json caller should still see a code for.
func TestAddRepoPathTakenIsCoded(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.Create(ctx, CreateOptions{ID: "pt-1", References: []RepoSpec{{Name: "repo-a"}}, NoFetch: true}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(svc.SpacePath("pt-1"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Repos = append(manifest.Repos, RepoManifest{
		Name: "repo-a", Mode: ModeEdit, Path: "references/repo-b", Base: "origin/main",
		Branch: DefaultBranch("pt-1", "repo-a"), BareRepoPath: cfg.Repos["repo-a"].BareRepoPath,
	})
	if err := SaveManifest(svc.SpacePath("pt-1"), manifest); err != nil {
		t.Fatal(err)
	}
	err = svc.AddRepo(ctx, AddOptions{SpaceID: "pt-1", RepoName: "repo-b", Mode: ModeReference, NoFetch: true})
	if ErrorCode(err) != CodeRepoPathTaken {
		t.Fatalf("path collision code = %q (%v)", ErrorCode(err), err)
	}
	if details := ErrorDetails(err); details["path"] != "references/repo-b" {
		t.Fatalf("details = %#v", details)
	}
}

func parseRepoSpecErr(raw string) error {
	_, err := ParseRepoSpec(raw)
	return err
}

func expandCommonErr(svc Service) error {
	cfg := svc.Config
	cfg.Tethers = config.TethersConfig{Enabled: boolPtr(false)}
	_, _, err := svc.ExpandCommonRefs(cfg, []RepoSpec{{Name: "repo-a"}}, nil, false)
	return err
}

func sagaStatusErr(ctx context.Context, svc Service, id string) error {
	_, err := svc.SagaStatus(ctx, id)
	return err
}

func boolPtr(v bool) *bool { return &v }
