package gitapi

import "strings"

const safeUnquoted = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@%_-+=:,./"

func BashQuote(s string) string {
	// Double escaping ~ neuters expansion and ~ is implicit.
	if strings.HasPrefix(s, "~/") {
		return s
	}
	if s == "" {
		return "''"
	}
	hasUnsafe := false
	for _, r := range s {
		if !strings.ContainsRune(safeUnquoted, r) {
			hasUnsafe = true
			break
		}
	}
	if !hasUnsafe {
		return s
	}
	return "'" + strings.Replace(s, "'", "'\"'\"'", -1) + "'"
}
