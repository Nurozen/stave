package portal

import "strings"

func ParseCodexAuthStatus(output string) AuthStatus {
	text := strings.ToLower(output)
	switch {
	case strings.Contains(text, "not logged in"), strings.Contains(text, "not authenticated"), strings.Contains(text, "login required"):
		return AuthMissing
	case strings.Contains(text, "logged in"), strings.Contains(text, "authenticated"), strings.Contains(text, "status: ok"):
		return AuthOK
	case strings.Contains(text, "error"), strings.Contains(text, "failed"):
		return AuthError
	default:
		return AuthUnknown
	}
}

func ParseClaudeAuthStatus(output string) AuthStatus {
	text := strings.ToLower(output)
	switch {
	case strings.Contains(text, "not logged in"), strings.Contains(text, "not authenticated"), strings.Contains(text, "login required"):
		return AuthMissing
	case strings.Contains(text, "logged in"), strings.Contains(text, "authenticated"), strings.Contains(text, "valid"):
		return AuthOK
	case strings.Contains(text, "error"), strings.Contains(text, "failed"), strings.Contains(text, "expired"):
		return AuthError
	default:
		return AuthUnknown
	}
}

func ParseCursorAuthStatus(output string) AuthStatus {
	text := strings.ToLower(output)
	switch {
	case strings.Contains(text, "not logged in"), strings.Contains(text, "not authenticated"), strings.Contains(text, "login required"):
		return AuthMissing
	case strings.Contains(text, "logged in"), strings.Contains(text, "authenticated"), strings.Contains(text, "signed in"):
		return AuthOK
	case strings.Contains(text, "error"), strings.Contains(text, "failed"):
		return AuthError
	default:
		return AuthUnknown
	}
}
