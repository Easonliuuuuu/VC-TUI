// Package sshalias conservatively parses an operator's OpenSSH client
// configuration to find literal Host aliases that plausibly already reach a
// given IP address.
//
// It is not an OpenSSH parser. It exists only to produce a small, bounded
// candidate set: a Host block whose literal (non-wildcard, non-negated,
// non-tokenized) alias sits above a literal HostName equal to the target
// address. Anything it cannot confidently associate with the target — a
// wildcard pattern, a Match block, a tokenized HostName such as "%h", a
// dynamically constructed value — it silently skips rather than guesses
// about. A conservative false negative here is fine: the caller (see
// internal/tui/handoff.go) treats every candidate this package returns as
// unproven and validates it with a bounded "ssh -G" before trusting it, so
// this package never needs to be right, only never wrong.
package sshalias

import (
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Limits bound how much configuration this package will read before giving
// up, so a pathological Include fan-out or cycle cannot make discovery scan
// an unbounded amount of the filesystem or block the TUI. Candidates found
// before a limit is hit are still returned; ErrTruncated says the search may
// be incomplete rather than exhaustive.
type Limits struct {
	// MaxDepth bounds Include recursion.
	MaxDepth int
	// MaxFiles bounds how many files (the top config plus every file an
	// Include glob expands to, recursively) are read in total.
	MaxFiles int
	// MaxBytes bounds the total bytes read across every file.
	MaxBytes int64
	// MaxCandidates bounds how many distinct alias candidates are returned.
	MaxCandidates int
}

// DefaultLimits is what production discovery uses. They are generous enough
// for any config a person would hand-maintain and small enough that a
// worst-case Include fan-out finishes quickly.
func DefaultLimits() Limits {
	return Limits{MaxDepth: 16, MaxFiles: 64, MaxBytes: 1 << 20, MaxCandidates: 8}
}

// ErrTruncated reports that a discovery limit was reached. The candidates
// found so far are still returned alongside it — the caller decides whether
// a partial, deterministic result is good enough (it is: every candidate is
// still validated independently before use).
var ErrTruncated = errors.New("sshalias: discovery limit reached; results may be incomplete")

// Discover reads home's "~/.ssh/config" and every file its Include
// directives reach, and returns the literal Host aliases whose first literal
// HostName equals target. A missing top-level config is not an error — it
// just means there is nothing to discover.
func Discover(home string, target netip.Addr, lim Limits) ([]string, error) {
	target = target.Unmap()
	sshDir := filepath.Join(home, ".ssh")
	entry := filepath.Join(sshDir, "config")
	if _, err := os.Stat(entry); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	p := &parser{lim: lim, sshDir: sshDir, home: home, target: target, seenAlias: map[string]bool{}}
	err := p.processFile(entry, 0)
	p.closeBlock()
	if err != nil {
		return p.candidates, err
	}
	if p.truncated {
		return p.candidates, ErrTruncated
	}
	return p.candidates, nil
}

// parser holds the mutable state of one Discover call. Its block-tracking
// fields (curHosts, hostNameSeen, matched, inMatch) are deliberately not
// reset at the end of a file: an Include is spliced into the surrounding
// config exactly where it appears, so a Host block opened before an Include
// still governs the lines that follow it once the included file returns —
// the same semantics ssh(1) itself applies.
type parser struct {
	lim  Limits
	home string

	sshDir string
	target netip.Addr

	filesSeen int
	bytesSeen int64
	truncated bool

	candidates []string
	seenAlias  map[string]bool

	curHosts     []string
	hostNameSeen bool
	matched      bool
	inMatch      bool
}

func (p *parser) processFile(path string, depth int) error {
	if depth > p.lim.MaxDepth {
		p.truncated = true
		return nil
	}
	if p.filesSeen >= p.lim.MaxFiles {
		p.truncated = true
		return nil
	}
	p.filesSeen++
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		// ReadFile on a directory fails with a platform-specific error
		// (EISDIR on Unix, "Incorrect function" on Windows), so detect it
		// up front. ssh(1) cannot use a directory as config either.
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			// A dangling or unreadable Include target is not this parser's
			// problem to report — ssh(1) would fail the same way, and the
			// candidate set just stays whatever it already has.
			return nil
		}
		return err
	}
	p.bytesSeen += int64(len(data))
	if p.bytesSeen > p.lim.MaxBytes {
		p.truncated = true
		overshoot := p.bytesSeen - p.lim.MaxBytes
		if overshoot < int64(len(data)) {
			data = data[:int64(len(data))-overshoot]
		} else {
			data = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if err := p.processLine(line, depth); err != nil {
			return err
		}
		if p.truncated && p.filesSeen >= p.lim.MaxFiles {
			break
		}
	}
	return nil
}

func (p *parser) processLine(line string, depth int) error {
	key, val, ok := splitDirective(line)
	if !ok {
		return nil
	}
	switch strings.ToLower(key) {
	case "host":
		p.closeBlock()
		p.inMatch = false
		p.curHosts = literalPatterns(val)
	case "match":
		p.closeBlock()
		p.inMatch = true
		p.curHosts = nil
	case "hostname":
		if p.inMatch || p.hostNameSeen || len(p.curHosts) == 0 {
			return nil
		}
		p.hostNameSeen = true
		if addr, ok := literalHostAddr(val); ok && addr == p.target {
			p.matched = true
		}
	case "include":
		if p.inMatch {
			// A Match block's Include could bring in configuration that only
			// applies under conditions this parser does not evaluate;
			// following it could attribute a HostName to the wrong scope.
			return nil
		}
		for _, pattern := range splitFields(val) {
			resolved := resolveIncludePath(pattern, p.sshDir, p.home)
			matches, globErr := filepath.Glob(resolved)
			if globErr != nil {
				continue
			}
			sort.Strings(matches)
			for _, m := range matches {
				if p.filesSeen >= p.lim.MaxFiles {
					p.truncated = true
					return nil
				}
				if err := p.processFile(m, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// closeBlock finalizes whatever Host block was open, recording its literal
// aliases as candidates if a literal HostName matched the target. It is
// called whenever a new Host or Match directive starts, and once more after
// the top-level file finishes.
func (p *parser) closeBlock() {
	if p.matched {
		for _, h := range p.curHosts {
			if len(p.candidates) >= p.lim.MaxCandidates {
				p.truncated = true
				break
			}
			if p.seenAlias[h] {
				continue
			}
			p.seenAlias[h] = true
			p.candidates = append(p.candidates, h)
		}
	}
	p.curHosts = nil
	p.hostNameSeen = false
	p.matched = false
}

// splitDirective parses one configuration line into its keyword and value,
// accepting both "Key value" and "Key=value" (ssh_config allows either,
// with optional surrounding whitespace around "="). Blank lines and comments
// report ok=false.
func splitDirective(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, "", true
	}
	key = line[:i]
	val = strings.TrimLeft(line[i:], " \t")
	val = strings.TrimPrefix(val, "=")
	val = strings.TrimSpace(val)
	return key, stripInlineComment(val), true
}

// stripInlineComment removes a trailing "# ..." comment from an unquoted
// value. It deliberately does not try to honour a "#" inside quotes — a
// value that needs that is not the simple literal case this package handles.
func stripInlineComment(val string) string {
	if i := strings.Index(val, "#"); i >= 0 {
		val = val[:i]
	}
	return strings.TrimSpace(val)
}

// splitFields splits a directive's value on whitespace, honouring simple
// double-quoted tokens (ssh_config allows quoting a pattern that contains a
// space). It is not a full shell-style tokenizer, which this conservative
// parser has no need for.
func splitFields(val string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			fields = append(fields, cur.String())
			cur.Reset()
		}
	}
	for _, r := range val {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return fields
}

// literalPatterns returns the tokens in a Host directive's value that are
// safe candidate aliases: no glob metacharacters, no negation, no "%"
// tokenization. Every other token in the same line is simply dropped rather
// than disqualifying its literal siblings.
func literalPatterns(val string) []string {
	var out []string
	for _, tok := range splitFields(val) {
		if tok == "" || strings.HasPrefix(tok, "!") {
			continue
		}
		if strings.ContainsAny(tok, "*?%") {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// literalHostAddr reports whether val is a literal IP address with no
// tokens — never a hostname, and never something ssh(1) would expand
// per-connection.
func literalHostAddr(val string) (netip.Addr, bool) {
	val = strings.Trim(strings.TrimSpace(val), `"`)
	if val == "" || strings.Contains(val, "%") {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(val)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// resolveIncludePath expands an Include pattern the way ssh(1) does for a
// user configuration file: "~/" expands against home, and a relative
// pattern always resolves against sshDir (~/.ssh) — never against the
// directory of the file containing the Include directive, even when that
// file was itself reached through an earlier Include.
func resolveIncludePath(pattern, sshDir, home string) string {
	pattern = strings.Trim(strings.TrimSpace(pattern), `"`)
	switch {
	case strings.HasPrefix(pattern, "~/"):
		pattern = filepath.Join(home, pattern[2:])
	case !filepath.IsAbs(pattern):
		pattern = filepath.Join(sshDir, pattern)
	}
	return pattern
}
