package shadeform

import (
	"strings"
)

// shellJoin collapses Mercator's argv into the single string Shadeform's
// docker_configuration.args field requires. Shadeform passes that string
// through a shell before handing it to `docker run <image>`, so every argument
// is quoted to survive word-splitting exactly once.
func shellJoin(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// shellQuote single-quotes an argument for POSIX sh unless it is entirely
// shell-safe. Embedded single quotes use the standard '\” escape.
func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if isShellSafe(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func isShellSafe(arg string) bool {
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_-./:=@%+,", r):
		default:
			return false
		}
	}
	return true
}
