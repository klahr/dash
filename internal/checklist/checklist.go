// Package checklist reads a project's TODO.md as a GitHub-style task list
// and changes it with minimal line edits: everything it doesn't touch stays
// byte for byte (order, wrapping, blank lines, CRLF or LF, the trailing
// newline or its absence).
//
// Items are "- [ ] text" or "- [x] text" (or with *, +), at any indentation;
// lines indented under an item continue it; # headings start sections;
// anything else is read-only text. Each item has a reference, its line number
// and a hash of its lines, so an edit made from a stale reading fails with
// ErrStale instead of changing the wrong line.
package checklist

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/klahr/dash/internal/textutil"
)

// Name is the file the checklist lives in, at the repository root.
const Name = "TODO.md"

// ErrStale means a reference no longer matches the file: it changed since
// it was read.
var ErrStale = errors.New("TODO.md changed since it was read; read it again")

// Doc is a parsed checklist.
type Doc struct {
	Sections []Section `json:"sections"`
}

// Section is a heading and what follows it, up to the next heading. The
// first section has no title when the file starts with something else.
type Section struct {
	Title   string  `json:"title"`
	Level   int     `json:"level"` // 1 for #, 2 for ##, …; 0 for the untitled start
	Line    int     `json:"line"`  // 1-based; 0 for the untitled start
	Entries []Entry `json:"entries"`
}

// Entry is one item, or read-only text (a paragraph, a quote, a code block).
type Entry struct {
	Item *Item  `json:"item,omitempty"`
	Text string `json:"text,omitempty"`
}

// Item is one task list item.
type Item struct {
	Ref     string `json:"ref"`
	Line    int    `json:"line"`   // 1-based line of its checkbox
	Indent  int    `json:"indent"` // columns of indentation (a tab counts 4)
	Checked bool   `json:"checked"`
	Text    string `json:"text"`           // the checkbox line's text
	More    string `json:"more,omitempty"` // its continuation lines, unindented, one per line
	Section string `json:"section,omitempty"`

	end      int    // index of its last line (continuation included)
	blockEnd int    // index of the last line of its nested items, or end
	box      int    // byte offset of the character between [ and ] in its line
	textAt   int    // byte offset of its text in its line
	contIn   string // indentation of its first continuation line
}

// Items lists every item, in file order.
func (d Doc) Items() []*Item {
	var out []*Item
	for _, s := range d.Sections {
		for _, e := range s.Entries {
			if e.Item != nil {
				out = append(out, e.Item)
			}
		}
	}
	return out
}

// Item finds an item by its reference.
func (d Doc) Item(ref string) (*Item, error) {
	for _, it := range d.Items() {
		if it.Ref == ref {
			return it, nil
		}
	}
	if _, _, ok := parseRef(ref); !ok {
		return nil, fmt.Errorf("%q is not a checklist reference (see read_checklist)", ref)
	}
	return nil, fmt.Errorf("%w: no item %s", ErrStale, ref)
}

// Full is the item's text with its continuation lines.
func (it Item) Full() string {
	if it.More == "" {
		return it.Text
	}
	return it.Text + "\n" + it.More
}

var (
	itemRe    = regexp.MustCompile(`^([ \t]*)([-*+])[ \t]+\[([ xX])\](?:[ \t]+(.*?))?[ \t]*$`)
	headingRe = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*#*[ \t]*$`)
	fenceRe   = regexp.MustCompile("^ {0,3}(```|~~~)")
)

// line is one line of the file, without its line ending.
type line struct{ text, eol string }

func splitLines(src string) []line {
	var out []line
	for src != "" {
		i := strings.IndexByte(src, '\n')
		if i < 0 {
			out = append(out, line{text: src})
			break
		}
		l := line{text: src[:i], eol: "\n"}
		if strings.HasSuffix(l.text, "\r") {
			l.text, l.eol = l.text[:len(l.text)-1], "\r\n"
		}
		out = append(out, l)
		src = src[i+1:]
	}
	return out
}

func indentOf(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// Parse reads a checklist.
func Parse(src string) Doc {
	d, _ := parse(splitLines(src))
	return d
}

// kind of each line, for edits.
const (
	kText = iota
	kBlank
	kHeading
	kItem
	kCont
)

func parse(lines []line) (Doc, []int) {
	kinds := make([]int, len(lines))
	doc := Doc{Sections: []Section{{}}}
	sec := &doc.Sections[0]
	var open []*Item // items lines may still be indented under, outermost first
	var text []string
	flush := func() {
		if len(text) > 0 {
			sec.Entries = append(sec.Entries, Entry{Text: strings.Join(text, "\n")})
			text = nil
		}
	}
	fence := ""
	for i, l := range lines {
		if fence != "" {
			kinds[i] = kText
			text = append(text, l.text)
			if m := fenceRe.FindStringSubmatch(l.text); m != nil && m[1] == fence {
				fence = ""
			}
			continue
		}
		if blank(l.text) {
			kinds[i] = kBlank
			flush()
			open = nil
			continue
		}
		ind := indentOf(l.text)
		for len(open) > 0 && ind <= open[len(open)-1].Indent {
			open = open[:len(open)-1]
		}
		if m := headingRe.FindStringSubmatch(l.text); m != nil && len(open) == 0 {
			flush()
			doc.Sections = append(doc.Sections, Section{Title: strings.TrimSpace(m[2]), Level: len(m[1]), Line: i + 1})
			sec = &doc.Sections[len(doc.Sections)-1]
			kinds[i] = kHeading
			continue
		}
		if m := itemRe.FindStringSubmatchIndex(l.text); m != nil {
			flush()
			it := &Item{
				Line: i + 1, Indent: ind, Checked: l.text[m[6]] != ' ', Section: sec.Title,
				end: i, blockEnd: i, box: m[6], textAt: len(l.text),
			}
			if m[8] >= 0 {
				it.Text, it.textAt = l.text[m[8]:m[9]], m[8]
			}
			for _, o := range open {
				o.blockEnd = i
			}
			open = append(open, it)
			kinds[i] = kItem
			sec.Entries = append(sec.Entries, Entry{Item: it})
			continue
		}
		if len(open) > 0 {
			// Indented under an item: it continues the item when it follows
			// it directly, else it only belongs to the item's block.
			it := open[len(open)-1]
			if it.end == i-1 {
				if it.contIn == "" {
					it.contIn = l.text[:indentBytes(l.text)]
				}
				if it.More != "" {
					it.More += "\n"
				}
				it.More += strings.TrimSpace(l.text)
				it.end = i
			}
			for _, o := range open {
				o.blockEnd = i
			}
			kinds[i] = kCont
			continue
		}
		if m := fenceRe.FindStringSubmatch(l.text); m != nil {
			fence = m[1]
		}
		kinds[i] = kText
		text = append(text, l.text)
	}
	flush()
	if len(doc.Sections[0].Entries) == 0 && len(doc.Sections) > 1 {
		doc.Sections = doc.Sections[1:]
	}
	for _, it := range doc.Items() {
		h := sha256.New()
		for j := it.Line - 1; j <= it.end; j++ {
			h.Write([]byte(lines[j].text))
			h.Write([]byte{'\n'})
		}
		it.Ref = strconv.Itoa(it.Line) + ":" + hex.EncodeToString(h.Sum(nil))[:10]
	}
	for i := range doc.Sections {
		if doc.Sections[i].Entries == nil {
			doc.Sections[i].Entries = []Entry{}
		}
	}
	return doc, kinds
}

func parseRef(ref string) (line int, hash string, ok bool) {
	n, h, ok := strings.Cut(ref, ":")
	if !ok || h == "" {
		return 0, "", false
	}
	line, err := strconv.Atoi(n)
	return line, h, err == nil && line > 0
}

// Op is one change. Refs name items as they were in the file the change is
// applied to; several ops in one Apply all refer to that same file.
type Op struct {
	// Op is "check" (Checked), "add" (Text, More, Section, Checked), "edit"
	// (Text; More replaces the continuation lines when set), "delete" or
	// "reorder" (to just before the item Before, just after the item After
	// and its nested items, or else at the end of Section; with its nested
	// items, indented as the item it lands next to).
	Op      string  `json:"op"`
	Ref     string  `json:"ref,omitempty"`
	Checked bool    `json:"checked,omitempty"`
	Text    string  `json:"text,omitempty"`
	More    *string `json:"more,omitempty"`
	Section string  `json:"section,omitempty"`
	Before  string  `json:"before,omitempty"`
	After   string  `json:"after,omitempty"`
}

// Result is a changed checklist.
type Result struct {
	Content string
	// Refs maps the reference each item had before to the one it has now,
	// for items that are still there.
	Refs map[string]string
	// Summary describes each op, e.g. "check off Write the docs".
	Summary []string
}

// Message is a commit message for the change of r.
func (r Result) Message() string {
	if len(r.Summary) == 0 {
		return Name + ": update"
	}
	subject := Name + ": " + r.Summary[0]
	if len(r.Summary) > 1 {
		subject += fmt.Sprintf(" (and %d more)", len(r.Summary)-1)
		return subject + "\n\n- " + strings.Join(r.Summary, "\n- ")
	}
	return subject
}

// Short shortens an item's text for a commit message or a title.
func Short(s string, n int) string { return textutil.OneLine(s, n) }

// Apply makes ops to src. A reorder is made to the file as the ops before it
// left it, and the ops after it to the file as it left that: refs in ops
// still name items as they were in src.
func Apply(src string, ops []Op) (Result, error) {
	if !slices.ContainsFunc(ops, func(o Op) bool { return o.Op == "reorder" }) {
		return apply(src, ops)
	}
	total := Result{Content: src, Refs: map[string]string{}}
	known := map[string]bool{}
	for _, it := range Parse(src).Items() {
		total.Refs[it.Ref], known[it.Ref] = it.Ref, true
	}
	ref := func(r string) (string, error) {
		if r == "" || !known[r] {
			return r, nil
		}
		if now, ok := total.Refs[r]; ok {
			return now, nil
		}
		return "", fmt.Errorf("%w: item %s was removed", ErrStale, r)
	}
	for len(ops) > 0 {
		n := 1
		for ops[0].Op != "reorder" && n < len(ops) && ops[n].Op != "reorder" {
			n++
		}
		seg := slices.Clone(ops[:n])
		ops = ops[n:]
		for i := range seg {
			var err error
			for _, r := range []*string{&seg[i].Ref, &seg[i].Before, &seg[i].After} {
				if *r, err = ref(*r); err != nil {
					return Result{}, err
				}
			}
		}
		res, err := apply(total.Content, seg)
		if err != nil {
			return Result{}, err
		}
		for orig, cur := range total.Refs {
			if now, ok := res.Refs[cur]; ok {
				total.Refs[orig] = now
			} else {
				delete(total.Refs, orig)
			}
		}
		total.Content = res.Content
		total.Summary = append(total.Summary, res.Summary...)
	}
	return total, nil
}

// outLine is a line written out, and the index of the line of the file it
// was (-1: a new one).
type outLine struct {
	text string
	from int
}

func fresh(ss []string) []outLine {
	out := make([]outLine, len(ss))
	for i, s := range ss {
		out[i] = outLine{s, -1}
	}
	return out
}

// reindent shifts a line by delta columns.
func reindent(s string, delta int) string {
	i := 0
	for delta < 0 && i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		if s[i] == '\t' {
			delta += 4
		} else {
			delta++
		}
		i++
	}
	if delta > 0 {
		return strings.Repeat(" ", delta) + s[i:]
	}
	return s[i:]
}

func apply(src string, ops []Op) (Result, error) {
	lines := splitLines(src)
	doc, kinds := parse(lines)
	eol := "\n"
	if len(lines) > 0 && lines[0].eol == "\r\n" {
		eol = "\r\n"
	}
	cur := make([]string, len(lines))
	for i, l := range lines {
		cur[i] = l.text
	}
	gone := make([]bool, len(lines))
	after := map[int][]outLine{} // new or moved lines after line i (-1: at the start)
	var tail []string            // new lines at the end (new sections)
	titled := len(lines) > 0     // a new file gets a "# TODO" title first
	title := func() {
		if !titled {
			tail = append(tail, "# TODO", "")
			titled = true
		}
	}
	var summary []string
	// sectionEnd is where to put lines at the end of a section: after its
	// last item, or else its last non-blank line (and then after a blank line).
	sectionEnd := func(sec Section) (anchor int, sep bool) {
		start, stop := sectionRange(doc, sec, len(lines))
		anchor = start - 1
		for j := stop - 1; j >= start; j-- {
			if kinds[j] != kBlank {
				anchor = j
				break
			}
		}
		for _, e := range sec.Entries {
			if e.Item != nil {
				anchor = max(e.Item.blockEnd, e.Item.end)
			}
		}
		return anchor, anchor < 0 || (kinds[anchor] != kItem && kinds[anchor] != kCont && len(after[anchor]) == 0)
	}

	find := func(ref string) (*Item, error) {
		it, err := doc.Item(ref)
		if err != nil {
			return nil, err
		}
		if gone[it.Line-1] {
			return nil, fmt.Errorf("%w: item %s was removed", ErrStale, ref)
		}
		return it, nil
	}
	for _, op := range ops {
		switch op.Op {
		case "check":
			it, err := find(op.Ref)
			if err != nil {
				return Result{}, err
			}
			l := cur[it.Line-1]
			c := " "
			if op.Checked {
				c = "x"
				if l[it.box] == 'X' {
					c = "X"
				}
			}
			cur[it.Line-1] = l[:it.box] + c + l[it.box+1:]
			if op.Checked {
				summary = append(summary, "check off "+Short(it.Text, 60))
			} else {
				summary = append(summary, "uncheck "+Short(it.Text, 60))
			}
		case "edit":
			it, err := find(op.Ref)
			if err != nil {
				return Result{}, err
			}
			text, more := splitText(op.Text, op.More)
			if text == "" {
				return Result{}, errors.New("an item needs text")
			}
			l := cur[it.Line-1]
			prefix := strings.TrimRight(l[:it.textAt], " \t")
			cur[it.Line-1] = prefix + " " + text
			if more != nil && *more != it.More {
				for j := it.Line; j <= it.end; j++ {
					gone[j] = true
				}
				in := it.contIn
				if in == "" {
					ws := prefix[:indentBytes(prefix)]
					in = ws + strings.Repeat(" ", len(prefix)-len(ws)+1)
				}
				after[it.end] = append(after[it.end], fresh(contLines(*more, in))...)
			}
			summary = append(summary, "edit "+Short(text, 60))
		case "delete":
			it, err := find(op.Ref)
			if err != nil {
				return Result{}, err
			}
			for j := it.Line - 1; j <= it.blockEnd; j++ {
				gone[j] = true
			}
			summary = append(summary, "remove "+Short(it.Text, 60))
		case "add":
			text, more := splitText(op.Text, op.More)
			if text == "" {
				return Result{}, errors.New("an item needs text")
			}
			box := "[ ]"
			if op.Checked {
				box = "[x]"
			}
			marker := "-"
			if items := doc.Items(); len(items) > 0 {
				marker = string(strings.TrimLeft(lines[items[0].Line-1].text, " \t")[0])
			}
			add := []string{marker + " " + box + " " + text}
			if more != nil {
				add = append(add, contLines(*more, strings.Repeat(" ", len(marker)+len(box)+2))...)
			}
			summary = append(summary, "add "+Short(text, 60))
			sec, ok := section(doc, op.Section)
			switch {
			case !ok:
				// A new section, at the end of the file.
				name := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(op.Section), "#"))
				title()
				if len(lines) > 0 || len(tail) > 0 && tail[len(tail)-1] != "" {
					tail = append(tail, "")
				}
				tail = append(tail, "## "+name, "")
				tail = append(tail, add...)
				doc.Sections = append(doc.Sections, Section{Title: name, Level: 2, Line: -1})
				continue
			case sec.Line == -1 || len(lines) == 0:
				// A section this batch made, or a new file.
				title()
				tail = append(tail, add...)
				continue
			}
			anchor, sep := sectionEnd(sec)
			if sep {
				add = append([]string{""}, add...)
			}
			after[anchor] = append(after[anchor], fresh(add)...)
		case "reorder":
			it, err := find(op.Ref)
			if err != nil {
				return Result{}, err
			}
			var at, ind int // its lines go after line at, indented ind
			var sep bool
			var where string
			switch {
			case op.Before != "" || op.After != "":
				to, err := find(op.Before + op.After)
				if err != nil {
					return Result{}, err
				}
				if op.Before != "" && op.After != "" {
					return Result{}, errors.New("reorder takes before or after, not both")
				}
				if to.Line >= it.Line && to.Line-1 <= it.blockEnd {
					return Result{}, errors.New("an item can't move next to itself or into its nested items")
				}
				at, ind, where = to.Line-2, to.Indent, "before "
				if op.After != "" {
					at, where = max(to.blockEnd, to.end), "after "
				}
				where += Short(to.Text, 40)
			default:
				sec, ok := section(doc, op.Section)
				if !ok || sec.Line == -1 {
					return Result{}, fmt.Errorf("no section %q", op.Section)
				}
				at, sep = sectionEnd(sec)
				for _, e := range sec.Entries {
					if e.Item != nil {
						ind = e.Item.Indent
						break
					}
				}
				where = "to the end of " + cmp.Or(sec.Title, "the file")
			}
			var moved []outLine
			if sep {
				moved = append(moved, outLine{"", -1})
			}
			for j := it.Line - 1; j <= it.blockEnd; j++ {
				if !gone[j] {
					moved = append(moved, outLine{reindent(cur[j], ind-it.Indent), j})
					gone[j] = true
				}
				for _, l := range after[j] {
					moved = append(moved, outLine{reindent(l.text, ind-it.Indent), l.from})
				}
				delete(after, j)
			}
			after[at] = append(after[at], moved...)
			summary = append(summary, "move "+Short(it.Text, 60)+" "+where)
		default:
			return Result{}, fmt.Errorf("unknown checklist op %q", op.Op)
		}
	}

	// Write it out, keeping every untouched line as it was.
	var out []line
	newLine := make([]int, len(lines))
	add := func(ls []outLine) {
		for _, l := range ls {
			out = append(out, line{l.text, eol})
			if l.from >= 0 {
				newLine[l.from] = len(out)
			}
		}
	}
	add(after[-1])
	for i, l := range lines {
		if !gone[i] {
			out = append(out, line{cur[i], l.eol})
			newLine[i] = len(out)
		}
		add(after[i])
	}
	add(fresh(tail))
	// Every line ends as it did; only the last one may lack a line ending,
	// when the file's did.
	final := len(lines) > 0 && lines[len(lines)-1].eol == ""
	var b strings.Builder
	for k, l := range out {
		switch {
		case k == len(out)-1 && final:
			l.eol = ""
		case l.eol == "":
			l.eol = eol
		}
		b.WriteString(l.text + l.eol)
	}
	res := Result{Content: b.String(), Refs: map[string]string{}, Summary: summary}
	now := map[int]string{}
	for _, it := range Parse(res.Content).Items() {
		now[it.Line] = it.Ref
	}
	for _, it := range doc.Items() {
		if newLine[it.Line-1] > 0 {
			if ref, ok := now[newLine[it.Line-1]]; ok {
				res.Refs[it.Ref] = ref
			}
		}
	}
	return res, nil
}

func indentBytes(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

// splitText takes a text's first line as the item's text and the rest as
// its continuation lines (when more isn't given separately).
func splitText(text string, more *string) (string, *string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	first, rest, multi := strings.Cut(strings.TrimSpace(text), "\n")
	first = strings.TrimSpace(first)
	if more != nil {
		m := strings.TrimSpace(strings.ReplaceAll(*more, "\r\n", "\n"))
		if multi {
			m = strings.TrimSpace(rest + "\n" + m)
		}
		return first, &m
	}
	if multi {
		m := strings.TrimSpace(rest)
		return first, &m
	}
	return first, nil
}

func contLines(more, indent string) []string {
	var out []string
	for _, l := range strings.Split(more, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, indent+l)
		}
	}
	return out
}

// section finds a section by title ("" is the whole file's last section).
func section(d Doc, title string) (Section, bool) {
	title = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(title), "#"))
	if title == "" {
		if len(d.Sections) == 0 {
			return Section{}, true
		}
		return d.Sections[len(d.Sections)-1], true
	}
	for _, s := range d.Sections {
		if strings.EqualFold(s.Title, title) {
			return s, true
		}
	}
	return Section{}, false
}

// sectionRange is the line indexes [start, stop) after s's heading up to the
// next heading.
func sectionRange(d Doc, s Section, n int) (start, stop int) {
	start, stop = s.Line, n // Line is 1-based: the index after the heading
	for _, o := range d.Sections {
		if o.Line > s.Line {
			return start, o.Line - 1
		}
	}
	return start, stop
}

// Prompt is a task for an item: its text and continuation lines, any items
// nested under it, and the section it was in.
func Prompt(src string, it Item) string {
	lines := splitLines(src)
	var b strings.Builder
	b.WriteString(it.Full())
	if it.blockEnd > it.end {
		b.WriteString("\n")
		for j := it.end + 1; j <= it.blockEnd && j < len(lines); j++ {
			b.WriteString("\n" + strings.TrimRight(lines[j].text, " \t"))
		}
	}
	if it.Section != "" {
		fmt.Fprintf(&b, "\n\n(From the %q section of %s.)", it.Section, Name)
	}
	return b.String()
}
