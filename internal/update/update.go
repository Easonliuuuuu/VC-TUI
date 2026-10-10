// Package update finds out whether a newer vsfleet has been released, and how
// this copy was installed, so that the operator can be told — or, where it is
// safe, helped — to upgrade.
//
// It makes exactly one kind of request: an anonymous GET of the latest release
// from GitHub's API, at most once a day, in the background. It sends no
// inventory, context names or identifiers beyond the User-Agent the vCenter
// connection already uses ("vsfleet/<version>"). It goes through the standard
// HTTPS_PROXY / NO_PROXY settings, never through a context's vCenter proxy,
// and every failure is silent: an estate that cannot reach GitHub simply never
// hears about new releases.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvNoUpdateNotifier turns off the release check, the launch prompt, the
	// header badge and the command-line notice when set to anything.
	EnvNoUpdateNotifier = "VSFLEET_NO_UPDATE_NOTIFIER"

	// LatestURL is the one endpoint the check reads.
	LatestURL = "https://api.github.com/repos/Easonliuuuuu/vsfleet/releases/latest"

	// CheckInterval is how long a check's answer is trusted before asking
	// again. A failed check counts too, so a network that cannot reach GitHub
	// is tried once a day rather than on every launch.
	CheckInterval = 24 * time.Hour
	// SnoozeInterval is how long "not now" keeps the launch prompt away.
	SnoozeInterval = 7 * 24 * time.Hour

	checkTimeout = 5 * time.Second
	maxBody      = 1 << 20
)

// Enabled reports whether this build checks for releases at all. A build from
// a source checkout ("dev") has nothing to upgrade to, and nobody is watching
// under CI.
func Enabled(current string, getenv func(string) string) bool {
	if current == "" || current == "dev" {
		return false
	}
	for _, k := range []string{EnvNoUpdateNotifier, "CI"} {
		if strings.TrimSpace(getenv(k)) != "" {
			return false
		}
	}
	return true
}

var releasePattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func parseRelease(v string) ([3]int, bool) {
	m := releasePattern.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// Newer reports whether candidate is a later release than current. Anything
// that is not a plain X.Y.Z on both sides — a pre-release, "dev", or whatever
// a misbehaving server sent — is never newer.
func Newer(candidate, current string) bool {
	c, ok := parseRelease(candidate)
	if !ok {
		return false
	}
	cur, ok := parseRelease(current)
	if !ok {
		return false
	}
	for i := range c {
		if c[i] != cur[i] {
			return c[i] > cur[i]
		}
	}
	return false
}

// ReleaseURL is the release notes page for version.
func ReleaseURL(version string) string {
	return "https://github.com/Easonliuuuuu/vsfleet/releases/tag/v" + strings.TrimPrefix(version, "v")
}

// Latest asks url for the newest published release and returns its version
// without the leading "v". The answer is only ever compared and displayed,
// never executed, and anything that is not a plain X.Y.Z is refused.
func Latest(ctx context.Context, client *http.Client, url, userAgent string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release check: %s", resp.Status)
	}
	var body struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return "", fmt.Errorf("release check: %w", err)
	}
	if body.Draft || body.Prerelease {
		return "", errors.New("release check: latest release is not a final release")
	}
	if _, ok := parseRelease(body.TagName); !ok {
		return "", fmt.Errorf("release check: unrecognised tag %q", body.TagName)
	}
	return strings.TrimPrefix(body.TagName, "v"), nil
}
