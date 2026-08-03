package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"raj/internal/keys"
)

// result is one measured chord: what we asked for vs what actually arrived.
type result struct {
	b       keys.Binding
	gotSeq  string
	gotName string
	ok      bool
	skipped bool
}

type checklist struct {
	items []keys.Binding
	idx   int
	res   []result

	stray     int    // consecutive unexpected events for the current item
	lastStray string // most recent unexpected chord, for the skip report
}

func newChecklist() *checklist {
	items := make([]keys.Binding, len(keys.Bindings))
	copy(items, keys.Bindings)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Group < items[j].Group })
	return &checklist{items: items}
}

func (c *checklist) done() bool { return c.idx >= len(c.items) }

func (c *checklist) prompt() {
	if c.done() {
		return
	}
	b := c.items[c.idx]
	fmt.Printf("\r\n[%d/%d] %-22s press %-18s (expect %s)  [ctrl+n skip]\r\n",
		c.idx+1, len(c.items), b.Action, b.Mac, `\e[`+b.Seq)
}

// record consumes one event against the current item. ONLY the expected chord
// advances the list: anything else is reported and ignored, so one stray key
// cannot consume a prompt and cascade through every remaining item. ctrl+n
// skips a chord that genuinely cannot be produced.
func (c *checklist) record(e keys.Event) {
	if c.done() {
		return
	}
	b := c.items[c.idx]
	got, want := keys.Escape(e.Raw), `\e[`+b.Seq
	if got != want {
		c.stray++
		c.lastStray = got
		if c.stray <= 8 {
			fmt.Printf("      ?? %-18s -> %-22s still waiting for %s\r\n", got, e.Chord(), want)
		}
		if c.stray == 8 {
			fmt.Print("      ?? lots of unexpected input. Is raj.conf loaded? ctrl+n skips.\r\n")
		}
		return
	}
	fmt.Printf("      got %-18s -> %-22s ok\r\n", got, e.Chord())
	c.res = append(c.res, result{b: b, gotSeq: got, gotName: e.Chord(), ok: true})
	c.advance()
}

func (c *checklist) advance() {
	c.stray, c.lastStray = 0, ""
	c.idx++
	c.prompt()
}

func (c *checklist) skip() {
	if c.done() {
		return
	}
	c.res = append(c.res, result{b: c.items[c.idx], skipped: true, gotSeq: c.lastStray})
	c.advance()
}

// report prints a summary plus a paste-ready keymap keyed on what ACTUALLY
// arrived, so raj's table is built from measurement rather than from my guess.
func (c *checklist) report() string {
	var sb strings.Builder
	var bad, skipped int
	sb.WriteString("\n// ---- keyprobe results ----\n")
	for _, r := range c.res {
		switch {
		case r.skipped:
			skipped++
			sb.WriteString(fmt.Sprintf("// NOT RECEIVED %-22s want %s", r.b.Action, `\e[`+r.b.Seq))
			if r.gotSeq != "" {
				sb.WriteString(" last stray " + r.gotSeq)
			}
			sb.WriteString("\n")
		case !r.ok:
			bad++
			sb.WriteString(fmt.Sprintf("// MISMATCH %-22s want %s got %s\n", r.b.Action, `\e[`+r.b.Seq, r.gotSeq))
		}
	}
	sb.WriteString(fmt.Sprintf("// %d ok, %d mismatched, %d skipped\n\n", len(c.res)-bad-skipped, bad, skipped))
	sb.WriteString("var Keymap = map[string]Action{\n")
	for _, r := range c.res {
		if r.skipped {
			continue
		}
		sb.WriteString(fmt.Sprintf("\t%-26s %s,\n", strconv.Quote(r.gotName)+":", identifier(string(r.b.Action))))
	}
	sb.WriteString("}\n")
	return sb.String()
}

// identifier renders a raj action name as a Go identifier.
func identifier(s string) string {
	out := "keys."
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		out += strings.ToUpper(part[:1]) + part[1:]
	}
	return out
}
