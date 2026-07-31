package space

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
)

// codexWritableRootsSection is the TOML table stave owns in the saga root's
// .codex/config.toml.
const codexWritableRootsSection = "sandbox_workspace_write"

// writeSagaAgentSettings maintains the harness settings at the SAGA root that
// grant agents access to the member directories (Deviation 16): Claude Code's
// permissions.additionalDirectories (relative member paths) and Codex's
// sandbox_workspace_write writable_roots (absolute member paths). Both edits
// are merge-aware — only the stave-owned entry is replaced; everything else
// in an existing file survives. Runs on every membership change, inside the
// per-saga lock, AFTER the roster commit: the caller downgrades a returned
// error (e.g. an unparseable settings file, which is left untouched rather
// than clobbered) to a printed notice, so the verb itself still succeeds.
func (s Service) writeSagaAgentSettings(spacePath string, manifest Manifest) error {
	if manifest.Saga == nil {
		return nil
	}
	ids := make([]string, 0, len(manifest.Saga.Members))
	for _, member := range manifest.Saga.Members {
		ids = append(ids, member.ID)
	}
	sort.Strings(ids)
	relative := make([]string, len(ids))
	absolute := make([]string, len(ids))
	for i, id := range ids {
		relative[i] = "../" + id
		absolute[i] = s.SpacePath(id)
	}
	if err := writeClaudeAdditionalDirectories(spacePath, relative); err != nil {
		return err
	}
	return writeCodexWritableRoots(spacePath, absolute)
}

// writeClaudeAdditionalDirectories sets permissions.additionalDirectories in
// .claude/settings.json to exactly dirs, preserving every other key of an
// existing file (entry-level merge). A missing file is created minimal; an
// unparseable one is left untouched and reported rather than clobbered.
func writeClaudeAdditionalDirectories(spacePath string, dirs []string) error {
	dir := filepath.Join(spacePath, ".claude")
	if err := os.MkdirAll(dir, config.DefaultDirMode); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	permissions, _ := doc["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
	}
	list := make([]any, len(dirs))
	for i, d := range dirs {
		list[i] = d
	}
	permissions["additionalDirectories"] = list
	doc["permissions"] = permissions
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return fsio.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// writeCodexWritableRoots merges a [sandbox_workspace_write] writable_roots
// section into .codex/config.toml, replacing only that section and keeping
// unrelated TOML content byte-stable (the same section-replace approach as
// internal/memory's codex MCP config writer).
func writeCodexWritableRoots(spacePath string, roots []string) error {
	dir := filepath.Join(spacePath, ".codex")
	if err := os.MkdirAll(dir, config.DefaultDirMode); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.toml")
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}
	cleaned := stripTOMLSection(existing, codexWritableRootsSection)
	var b strings.Builder
	b.WriteString(cleaned)
	if cleaned != "" && !strings.HasSuffix(cleaned, "\n") {
		b.WriteString("\n")
	}
	if cleaned != "" {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "[%s]\n", codexWritableRootsSection)
	b.WriteString("writable_roots = [")
	for i, root := range roots {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", root)
	}
	b.WriteString("]\n")
	return fsio.WriteFileAtomic(path, []byte(b.String()), 0o644)
}

// stripTOMLSection removes [section] (and nested [section.*]) tables from a
// TOML document, leaving all other content intact — a minimal line-based
// edit mirroring internal/memory's codex MCP section handling.
func stripTOMLSection(src, section string) string {
	if src == "" || (!strings.Contains(src, "["+section+"]") && !strings.Contains(src, "["+section+".")) {
		return src
	}
	lines := strings.Split(src, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			// Trim repeated brackets so array-of-tables headers
			// ([[section.x]]) strip together with their parent table.
			header := strings.TrimSpace(strings.TrimRight(strings.TrimLeft(trim, "["), "]"))
			if header == section || strings.HasPrefix(header, section+".") {
				skipping = true
				continue
			}
			skipping = false
		}
		if skipping {
			continue
		}
		out = append(out, line)
	}
	// Collapse excessive blank lines left by section removal.
	var compact []string
	blank := 0
	for _, line := range out {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		compact = append(compact, line)
	}
	return strings.Join(compact, "\n")
}
