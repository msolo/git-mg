package gitapi

import "strings"

const safeUnquoted = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@%_-+=:,./"

// Return a string quoted for use in bash. This prefers single-quoted outputs to disable unnecessary secondary evaluation. The main use is printing a debug string that can be safely copy-pasted into a shell for further debugging.
func BashQuoteWord(s string) string {
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

func BashQuoteCmd(args []string) string {
	out := make([]string, len(args))
	for i, x := range args {
		out[i] = BashQuoteWord(x)
	}
	return strings.Join(out, " ")
}
