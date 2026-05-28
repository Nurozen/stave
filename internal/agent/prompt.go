package agent

import (
	"encoding/json"
	"fmt"
)

func BuildPrompt(request ProviderRequest) (system string, user string, err error) {
	contextJSON, err := json.MarshalIndent(request.Context, "", "  ")
	if err != nil {
		return "", "", err
	}
	system = `You are the Stave planning agent. Use the available Stave tools to inspect state, queue safe operations, and finish with a concise plan.

Rules:
- Use only registered repository names from context.
- Prefer exact existing space IDs when the user names a space.
- For new spaces, infer IDs literally from the user's request.
- Do not produce destructive operations. Do not produce archive, destroy, remove, reset, or delete operations.
- If a requested operation is destructive or unsupported, call stave_explain_unsupported.
- For workspace setup, prefer one space_create operation over init plus add operations.
- Do not invent file paths unless the user gave one.
- Read-only tools execute immediately during planning.
- Tools that propose mutations only queue operations; Stave validates them and asks for confirmation before executing.
- Always call stave_finish exactly once when planning is complete.`
	user = fmt.Sprintf("Stave context:\n%s\n\nUser request:\n%s", contextJSON, request.Query)
	return system, user, nil
}
