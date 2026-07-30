package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLabServePlaysOnlyWhenAskedTo(t *testing.T) {
	// Arrange
	args := []string{"mercator", "lab", "serve", "--blueprint", "demo.json"}

	// Act
	options, err := parseLabServeOptions(args)

	// Assert
	if err != nil {
		t.Fatalf("parse Lab serve options: %v", err)
	}
	if options.play != 0 {
		t.Fatalf("play = %s, want zero so a served Blueprint waits for its operator", options.play)
	}
}

func TestLabServeAcceptsAPlayIntervalAndOpensByDefault(t *testing.T) {
	// Arrange
	args := []string{
		"mercator", "lab", "serve",
		"--blueprint", "demo.json",
		"--play", "1500ms",
	}

	// Act
	options, err := parseLabServeOptions(args)

	// Assert
	if err != nil {
		t.Fatalf("parse Lab serve options: %v", err)
	}
	if options.play != 1500*time.Millisecond {
		t.Fatalf("play = %s, want 1.5s", options.play)
	}
	if !options.open {
		t.Fatal("open should default on, because a console nobody opened is the whole complaint")
	}
}

func TestLabServeRejectsANegativePlayInterval(t *testing.T) {
	// Arrange
	args := []string{
		"mercator", "lab", "serve",
		"--blueprint", "demo.json",
		"--play", "-1s",
	}

	// Act
	_, err := parseLabServeOptions(args)

	// Assert
	if err == nil {
		t.Fatal("a negative play interval should fail where the flag is read")
	}
}

func TestOpeningIsSuppressible(t *testing.T) {
	// Arrange
	args := []string{
		"mercator", "lab", "serve",
		"--blueprint", "demo.json",
		"--open=false",
	}

	// Act
	options, err := parseLabServeOptions(args)

	// Assert
	if err != nil {
		t.Fatalf("parse Lab serve options: %v", err)
	}
	if options.open {
		t.Fatal("--open=false should suppress opening, for a headless host or a CI run")
	}
}

// The URL handed to the opener is built from a loopback listen address rather than
// from anything a caller typed, so a scheme that is not http or https means the
// caller changed. Refusing here is what stops a desktop opener from being handed
// something that is not a browser.
func TestOnlyWebSchemesReachABrowser(t *testing.T) {
	// Arrange
	refused := []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.invalid/",
	}

	for _, rawURL := range refused {
		// Act
		err := openInBrowser(rawURL)

		// Assert
		if err == nil {
			t.Fatalf("openInBrowser(%q) should refuse a non-web scheme", rawURL)
		}
		if !strings.Contains(err.Error(), "only http and https") {
			t.Fatalf("openInBrowser(%q) refused for the wrong reason: %v", rawURL, err)
		}
	}
}

func TestTheOpenerIsThePlatformsOwn(t *testing.T) {
	// Arrange
	want := map[string]string{"darwin": "open", "windows": "rundll32", "linux": "xdg-open"}

	// Act
	opener, _ := browserOpener()

	// Assert
	expected, known := want[runtime.GOOS]
	if !known {
		expected = "xdg-open"
	}
	if opener != expected {
		t.Fatalf("opener on %s = %q, want %q", runtime.GOOS, opener, expected)
	}
}
