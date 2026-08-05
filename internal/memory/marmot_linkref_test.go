package memory

// F5 tests: `space add` memory-link parity — LinkReference (resolve → den
// link), old-binary degradation, marmotVault override, dry-run. Plus F12
// (Status refusals) and F23 (envelope warnings printed) regressions.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveArgs(t *testing.T) {
	got := strings.Join(ResolveArgs(ReferenceSpec{Name: "repo-b", URL: "/src/b", Ref: "main"}), " ")
	want := "resolve --name repo-b --url /src/b --ref main --json"
	if got != want {
		t.Fatalf("ResolveArgs = %q, want %q", got, want)
	}
	// Empty components omitted.
	got = strings.Join(ResolveArgs(ReferenceSpec{URL: "/src/b"}), " ")
	if got != "resolve --url /src/b --json" {
		t.Fatalf("ResolveArgs = %q", got)
	}
}

func TestLinkReferenceWarrenURL(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{
		"den --help": newDenUsage,
		"resolve":    `{"schema":1,"resolved_via":"warren-url","warren":"w2","project":"proj-b","vault_id":"pv","detail":"matched"}`,
		"den link":   `{"schema":1,"den_id":"t6","link":{"target":"w2/proj-b","mode":"link"},"warnings":["lk warn"]}`,
	}, &calls)
	var out strings.Builder
	res, err := m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID:   "t6",
		SpacePath: t.TempDir(),
		Spec:      ReferenceSpec{Name: "repo-b", URL: "/src/b"},
		Out:       &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Linked || res.Link.Ref != "w2/proj-b" || res.Link.Mode != "link" || res.Link.ResolvedVia != "warren-url" {
		t.Fatalf("result = %#v", res)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "resolve --name repo-b --url /src/b --json") {
		t.Fatalf("resolve argv missing:\n%s", joined)
	}
	if !strings.Contains(joined, "den link t6 --link w2/proj-b --json") {
		t.Fatalf("den link argv missing:\n%s", joined)
	}
	if !strings.Contains(out.String(), "reference repo-b → w2/proj-b (warren-url)") {
		t.Fatalf("output missing resolution line:\n%s", out.String())
	}
	// F23: den link envelope warnings surface with the warning: prefix.
	if !strings.Contains(out.String(), "warning: lk warn") {
		t.Fatalf("output missing envelope warning:\n%s", out.String())
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "lk warn" {
		t.Fatalf("warnings = %#v", res.Warnings)
	}
}

func TestLinkReferenceNoneAndCheckoutVault(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{
		"den --help": newDenUsage,
		"resolve":    `{"schema":1,"resolved_via":"none","detail":"no match"}`,
	}, &calls)
	var out strings.Builder
	res, err := m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-x", URL: "/src/x"}, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked || res.Link.ResolvedVia != "none" {
		t.Fatalf("result = %#v", res)
	}
	if !strings.Contains(out.String(), "reference repo-x → no memory found") {
		t.Fatalf("output = %q", out.String())
	}
	if strings.Contains(strings.Join(calls, "\n"), "den link") {
		t.Fatalf("no den link may be issued on none:\n%s", strings.Join(calls, "\n"))
	}

	// checkout-vault: v1 keeps it a notice, no link.
	calls = nil
	m = scriptedMarmot(t, map[string]string{
		"den --help": newDenUsage,
		"resolve":    `{"schema":1,"resolved_via":"checkout-vault","vault_id":"cv-1","detail":"in-checkout vault"}`,
	}, &calls)
	out.Reset()
	res, err = m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-y", URL: "/src/y"}, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked || res.Link.ResolvedVia != "checkout-vault" || res.Notice == "" {
		t.Fatalf("result = %#v", res)
	}
	if !strings.Contains(out.String(), "notice: reference repo-y resolves via checkout-vault (cv-1)") {
		t.Fatalf("output = %q", out.String())
	}
	if strings.Contains(strings.Join(calls, "\n"), "den link") {
		t.Fatalf("no den link may be issued on checkout-vault:\n%s", strings.Join(calls, "\n"))
	}
}

func TestLinkReferenceExplicitVaultAndOff(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{
		"den --help": newDenUsage,
		"den link":   `{"schema":1,"den_id":"t6","link":{"target":"vault-9","mode":"link"}}`,
	}, &calls)
	var out strings.Builder
	res, err := m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-b", URL: "/src/b", MarmotVault: "vault-9"}, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Linked || res.Link.Ref != "vault-9" || res.Link.ResolvedVia != "explicit" {
		t.Fatalf("result = %#v", res)
	}
	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "resolve") {
		t.Fatalf("explicit vault must skip resolution:\n%s", joined)
	}
	if !strings.Contains(out.String(), "reference repo-b → vault-9 (config marmotVault)") {
		t.Fatalf("output = %q", out.String())
	}

	// "off" is filtered upstream; defensively a no-op with zero subprocesses.
	calls = nil
	res, err = m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-b", MarmotVault: "off"},
	})
	if err != nil || res.Linked || len(calls) != 0 {
		t.Fatalf("off must be a no-op: res=%#v err=%v calls=%v", res, err, calls)
	}
}

func TestLinkReferenceOldBinaryDegrades(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{"den --help": oldDenUsage}, &calls)
	var out strings.Builder
	res, err := m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-b", URL: "/src/b"}, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked || res.Notice == "" {
		t.Fatalf("result = %#v", res)
	}
	if !strings.Contains(out.String(), "notice: installed marmot lacks resolve/den link support; reference repo-b added without a memory link") {
		t.Fatalf("output = %q", out.String())
	}
	joined := strings.Join(calls, "\n")
	for _, ban := range []string{"resolve", "den link"} {
		if strings.Contains(joined, ban) {
			t.Fatalf("old marmot must never see %q:\n%s", ban, joined)
		}
	}
}

func TestLinkReferenceDryRunNeverInvokes(t *testing.T) {
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { t.Fatal("dry-run must not resolve the binary"); return "", nil },
	}
	var out strings.Builder
	res, err := m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-b", URL: "/src/b"}, DryRun: true, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DryRunCommands) != 2 || !strings.Contains(res.DryRunCommands[0], "resolve --name repo-b") {
		t.Fatalf("dry-run commands = %#v", res.DryRunCommands)
	}
	if !strings.Contains(out.String(), "dry-run: marmot resolve") {
		t.Fatalf("output = %q", out.String())
	}
	// Explicit vault dry-run prints only the den link plan.
	out.Reset()
	res, err = m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-b", MarmotVault: "vault-9"}, DryRun: true, Out: &out,
	})
	if err != nil || len(res.DryRunCommands) != 1 {
		t.Fatalf("res=%#v err=%v", res, err)
	}
	if !strings.Contains(res.DryRunCommands[0], "den link t6 --link vault-9 --json") {
		t.Fatalf("dry-run commands = %#v", res.DryRunCommands)
	}
}

func TestLinkReferenceLookPathFail(t *testing.T) {
	m := &Marmot{LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	_, err := m.LinkReference(context.Background(), LinkReferenceOptions{
		StoreID: "t6", Spec: ReferenceSpec{Name: "repo-b", URL: "/src/b"},
	})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v", err)
	}
}

// F12: den status structured refusals become RefusalError (not raw blobs),
// with the raw payload preserved for display.
func TestStatusRefusalError(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den status": {Stdout: `{"schema":1,"error":{"code":"den_not_found","message":"no den sid","hint":"marmot den list"}}`, Code: 1},
	})
	st, err := NewMarmot(bin).Status(context.Background(), StatusOptions{StoreID: "sid"})
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Code != "den_not_found" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(st.RawJSON, "den_not_found") {
		t.Fatalf("raw JSON must survive for display: %#v", st)
	}
}

// F12: an unparseable or schema-less den status payload is now an error
// (schema negotiated like every other verb), not a silent raw dump.
func TestStatusUnparseableIsError(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den status": {Stdout: "plain text, not json", Code: 0},
	})
	if _, err := NewMarmot(bin).Status(context.Background(), StatusOptions{StoreID: "sid"}); err == nil {
		t.Fatal("expected decode error")
	}
}

// F23: attach create-envelope warnings print with the warning: prefix.
func TestAttachPrintsEnvelopeWarnings(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: `{"schema":1,"den_id":"sp","den_path":"/tmp/dens/sp","pointer_written":false,"warnings":["ref skipped: no match"]}`, Code: 0},
	})
	var out strings.Builder
	res, err := NewMarmot(bin).Attach(context.Background(), AttachOptions{
		SpaceID: "sp", SpacePath: t.TempDir(), Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "ref skipped: no match" {
		t.Fatalf("warnings = %#v", res.Warnings)
	}
	if !strings.Contains(out.String(), "warning: ref skipped: no match") {
		t.Fatalf("output missing warning line:\n%s", out.String())
	}
}

// F23: detach destroy-envelope warnings print with the warning: prefix.
func TestDetachDestroyPrintsEnvelopeWarnings(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den destroy": {Stdout: `{"schema":1,"destroyed":true,"warnings":["vault had unpushed edits"]}`, Code: 0},
	})
	var out strings.Builder
	res, err := NewMarmot(bin).Detach(context.Background(), DetachOptions{
		StoreID: "sp", Fate: FateDestroy, Owned: true, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "vault had unpushed edits" {
		t.Fatalf("warnings = %#v", res.Warnings)
	}
	if !strings.Contains(out.String(), "warning: vault had unpushed edits") {
		t.Fatalf("output missing warning line:\n%s", out.String())
	}
}

// P1: a den-held-by-live-process destroy refusal surfaces as a RefusalError
// with the source_in_use code (IsSourceInUse true) — never a raw exec error.
func TestDetachDestroySourceInUseRefusal(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den destroy": {Stdout: `{"schema":1,"error":{"code":"source_in_use","message":"cannot lock den \"sp\" lifecycle for destruction","hint":"retry after other den operations finish"}}`, Code: 1},
	})
	_, err := NewMarmot(bin).Detach(context.Background(), DetachOptions{
		StoreID: "sp", Fate: FateDestroy, Owned: true,
	})
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Code != CodeSourceInUse {
		t.Fatalf("err = %v", err)
	}
	if !IsSourceInUse(err) {
		t.Fatalf("IsSourceInUse must classify: %v", err)
	}
}
