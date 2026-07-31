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

// writeCodexWritableRoots merges writable_roots into the
// [sandbox_workspace_write] table in .codex/config.toml. The writable_roots
// entry is stave-owned, but sibling entries in that table (for example
// network_access) and unrelated TOML content are preserved.
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
	var value strings.Builder
	value.WriteString("[")
	for i, root := range roots {
		if i > 0 {
			value.WriteString(", ")
		}
		fmt.Fprintf(&value, "%q", root)
	}
	value.WriteString("]")
	merged := replaceTOMLTableEntry(existing, codexWritableRootsSection, "writable_roots", value.String())
	return fsio.WriteFileAtomic(path, []byte(merged), 0o644)
}

// replaceTOMLTableEntry replaces key inside [section], or creates the missing
// entry/table. It deliberately edits lines instead of re-encoding the TOML so
// all content outside the stave-owned entry remains byte-stable. A prior
// multi-line array value is consumed through its closing bracket.
func replaceTOMLTableEntry(src, section, key, value string) string {
	lines := strings.Split(src, "\n")
	sectionStart := -1
	sectionEnd := len(lines)
	for i, line := range lines {
		header, arrayTable, ok := tomlTableHeader(line)
		if !ok {
			continue
		}
		if sectionStart < 0 {
			if !arrayTable && header == section {
				sectionStart = i
			}
			continue
		}
		sectionEnd = i
		break
	}

	entry := key + " = " + value
	if sectionStart < 0 {
		var b strings.Builder
		b.WriteString(src)
		if src != "" && !strings.HasSuffix(src, "\n") {
			b.WriteByte('\n')
		}
		if src != "" {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n%s\n", section, entry)
		return b.String()
	}

	for i := sectionStart + 1; i < sectionEnd; i++ {
		if !tomlEntryHasKey(lines[i], key) {
			continue
		}
		end := i + 1
		depth, sawArray := tomlArrayDepth(lines[i])
		for sawArray && depth > 0 && end < sectionEnd {
			delta, _ := tomlArrayDepth(lines[end])
			depth += delta
			end++
		}
		updated := make([]string, 0, len(lines)-(end-i)+1)
		updated = append(updated, lines[:i]...)
		updated = append(updated, entry)
		updated = append(updated, lines[end:]...)
		return strings.Join(updated, "\n")
	}

	updated := make([]string, 0, len(lines)+1)
	updated = append(updated, lines[:sectionEnd]...)
	updated = append(updated, entry)
	updated = append(updated, lines[sectionEnd:]...)
	return strings.Join(updated, "\n")
}

func tomlTableHeader(line string) (name string, arrayTable, ok bool) {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "[[") && strings.HasSuffix(trim, "]]") {
		return strings.TrimSpace(trim[2 : len(trim)-2]), true, true
	}
	if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
		return strings.TrimSpace(trim[1 : len(trim)-1]), false, true
	}
	return "", false, false
}

func tomlEntryHasKey(line, key string) bool {
	trim := strings.TrimSpace(line)
	if !strings.HasPrefix(trim, key) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trim, key))
	return strings.HasPrefix(rest, "=")
}

// tomlArrayDepth counts unquoted square brackets before an inline comment.
// It is intentionally narrow: writable_roots is defined as an array value.
func tomlArrayDepth(line string) (depth int, sawArray bool) {
	inString := false
	escaped := false
	for _, r := range line {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '#':
			return depth, sawArray
		case '[':
			depth++
			sawArray = true
		case ']':
			depth--
			sawArray = true
		}
	}
	return depth, sawArray
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
