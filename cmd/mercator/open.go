package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// openInBrowser hands a URL to whatever the desktop uses to open one.
//
// The opener is the platform's, not ours: every system that has one already
// decides which browser a URL belongs to, and a Lab that picked for the operator
// would be answering a question the desktop already answered. A system with no
// opener is not an error worth failing a serve over, because the console is
// reachable by typing the URL and the API is reachable by curl, so the caller
// reports what happened and keeps serving.
func openInBrowser(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse console URL %q: %w", rawURL, err)
	}
	// Only http and https reach a browser. Anything else handed to a shell-adjacent
	// opener is a way to run something that is not a browser, and this URL is built
	// from a loopback listen address rather than from anything a caller typed, so a
	// scheme that is not one of the two means the caller changed and this check is
	// the place that notices.
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("refusing to open %q: only http and https reach a browser", rawURL)
	}

	opener, args := browserOpener()
	if opener == "" {
		return fmt.Errorf("no known browser opener for %s", runtime.GOOS)
	}
	path, err := exec.LookPath(opener)
	if err != nil {
		return fmt.Errorf("%s is not installed: %w", opener, err)
	}
	// Start rather than Run: the opener outlives this call on some desktops and
	// waiting on it would hold the serve command hostage to a browser's lifetime.
	return exec.Command(path, append(args, parsed.String())...).Start()
}

// browserOpener is the command each desktop uses to open a URL. It is stated as
// data rather than as branches inside openInBrowser so the platform list reads as
// the list it is.
func browserOpener() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		return "xdg-open", nil
	}
}
