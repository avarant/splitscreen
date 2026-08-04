package slackx

import (
	"regexp"
	"strings"
)

// Slack does not speak CommonMark. It speaks "mrkdwn", which overlaps enough to
// be mistaken for Markdown and differs enough that posting Markdown verbatim
// renders **bold** as literal asterisks and swallows link syntax entirely.
//
// Harnesses emit CommonMark, so the surface converts. Doing it here rather than
// asking the agent to write mrkdwn is deliberate: the same output has to render
// on Discord and a web view later, and a harness should not have to know which
// surface it is talking to.

var (
	// Fenced code blocks are passed through untouched. They are extracted first
	// so nothing inside them is rewritten.
	fenceRe = regexp.MustCompile("(?s)```.*?```")
	// Inline code likewise, within prose.
	inlineCodeRe = regexp.MustCompile("`[^`\n]*`")

	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	boldRe     = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	boldUnder  = regexp.MustCompile(`__([^_\n]+)__`)
	italicStar = regexp.MustCompile(`\*([^*\n]+)\*`)
	strikeRe   = regexp.MustCompile(`~~([^~\n]+)~~`)
	headingRe  = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	bulletRe   = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	hrRe       = regexp.MustCompile(`(?m)^\s{0,3}(?:---+|\*\*\*+|___+)\s*$`)
)

// boldSentinel stands in for converted bold while single-asterisk italics are
// processed, so the italic pass cannot re-consume the asterisks bold just
// produced.
const (
	boldOpen  = "\x00B<"
	boldClose = "\x00B>"
)

// ToMrkdwn converts CommonMark to Slack mrkdwn.
func ToMrkdwn(s string) string {
	return mapOutside(s, fenceRe, func(prose string) string {
		return mapOutside(prose, inlineCodeRe, convertProse)
	})
}

// mapOutside applies fn to every part of s that does not match re, leaving the
// matches themselves untouched.
func mapOutside(s string, re *regexp.Regexp, fn func(string) string) string {
	spans := re.FindAllStringIndex(s, -1)
	if spans == nil {
		return fn(s)
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(fn(s[last:sp[0]]))
		b.WriteString(s[sp[0]:sp[1]])
		last = sp[1]
	}
	b.WriteString(fn(s[last:]))
	return b.String()
}

func convertProse(s string) string {
	// Headings first: Slack has no heading syntax, and bold is the closest thing
	// that survives. Emitted as the sentinel rather than literal asterisks, or
	// the italic pass below would immediately re-consume them.
	s = headingRe.ReplaceAllString(s, boldOpen+"${1}"+boldClose)

	// Horizontal rules would otherwise read as stray dashes, or worse be
	// mistaken for a bullet.
	s = hrRe.ReplaceAllString(s, "───────────")

	// Links: [text](url) -> <url|text>, and a bare [](url) -> <url>.
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		g := mdLinkRe.FindStringSubmatch(m)
		text, url := g[1], g[2]
		if strings.TrimSpace(text) == "" {
			return "<" + url + ">"
		}
		return "<" + url + "|" + text + ">"
	})

	// Bold to a sentinel, so the italic pass below cannot re-consume it.
	s = boldRe.ReplaceAllString(s, boldOpen+"${1}"+boldClose)
	s = boldUnder.ReplaceAllString(s, boldOpen+"${1}"+boldClose)

	// Single asterisks are italic in CommonMark but bold in mrkdwn, which is the
	// single most confusing difference between the two.
	//
	// ${1} rather than $1: Go reads "$1_" as a group *named* "1_", which does
	// not exist, and silently substitutes nothing.
	s = italicStar.ReplaceAllString(s, "_${1}_")

	s = strikeRe.ReplaceAllString(s, "~${1}~")

	// Slack renders a leading dash as a literal dash, not a bullet.
	s = bulletRe.ReplaceAllString(s, "${1}• ")

	s = strings.ReplaceAll(s, boldOpen, "*")
	s = strings.ReplaceAll(s, boldClose, "*")
	return s
}
