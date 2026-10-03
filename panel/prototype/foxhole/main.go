// PROTOTYPE. Throwaway, never merged. Ticket #417 on wayfinder map #412.
//
// Three variants of the Foxhole page, switchable via ?variant=A|B|C, on a
// throwaway server that wraps the panel's real layout and stylesheet around
// mock data held in memory. Every action posts a form and changes only that
// memory; nothing talks to Discord, the forum or Postgres.
//
// Run it from the repo root:
//
//	go run ./panel/prototype/foxhole
//
// then open http://localhost:8417/?variant=A. Add &noscript=1 to see the page
// without its script (ADR 0013).
package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	addr     = "localhost:8417"
	signedIn = "Hartmann.K"
	mapURL   = "https://github.com/7Cav/cavbot2/issues/412"
)

var variants = []struct{ Key, Name string }{
	{"A", "One list"},
	{"B", "A block per list"},
	{"C", "War steps and a side pane"},
}

// Open questions other tickets own. The prototype marks where each touches
// the page, so the driver can tell what this ticket decides and what it doesn't.
var (
	openPurge   = &openQ{"Does the panel's purge recreate the Foxhole role or remove it from each holder?", "https://github.com/7Cav/cavbot2/issues/423"}
	openLong    = &openQ{"How does the Foxhole page report a bulk action that outlasts the page load?", "https://github.com/7Cav/cavbot2/issues/418"}
	openPartial = &openQ{"What does the Foxhole page show while the member list is still arriving?", "https://github.com/7Cav/cavbot2/issues/421"}
	openAmbig   = &openQ{"Not yet specified: a username or display name that matches more than one member", mapURL}
)

type openQ struct{ Title, URL string }

type member struct {
	ID, Display, Username, Rank, Note string
	Internal, External, Approved      bool
	InServer                          bool
	// Forum is the forum username on the member's milpac, for roster rows.
	Forum string
}

// NoRank is the list's no-rank-role flag: an Internal holder with no rank role.
func (m *member) NoRank() bool { return m.Internal && m.InServer && m.Rank == "" }

// ApprovedNotHolding is an approved collaborator in the server without External.
func (m *member) ApprovedNotHolding() bool { return m.Approved && m.InServer && !m.External }

func (m *member) Flagged() bool { return m.NoRank() || m.ApprovedNotHolding() || !m.InServer }

func (m *member) SearchText() string {
	return strings.ToLower(strings.Join([]string{m.Display, m.Username, m.ID, m.Rank, m.Note}, " "))
}

func (m *member) Tags() string {
	var t []string
	if m.Internal {
		t = append(t, "internal")
	}
	if m.External {
		t = append(t, "external")
	}
	if m.Approved {
		t = append(t, "approved")
	}
	if m.Flagged() {
		t = append(t, "flagged")
	}
	return strings.Join(t, " ")
}

func (m *member) Matches(filter string) bool {
	if filter == "" {
		return true
	}
	return strings.Contains(" "+m.Tags()+" ", " "+filter+" ")
}

type noteChange struct{ Member, Before, After string }

type entry struct {
	At      time.Time
	Who     string
	Action  string
	Summary string
	Members []string
	Notes   []noteChange
}

type report struct {
	Title string
	Lines []string
	Open  *openQ
}

type field struct{ Name, Value string }

type prow struct{ Input, Member, Status, Class string }

// pending is a preview or a confirmation the page shows before anything
// changes. Scope says which list it belongs to, for variant B.
type pending struct {
	Scope     string
	Title     string
	Lines     []string
	InputHead string
	Rows      []prow
	Action    string
	Fields    []field
	Confirm   string
	Danger    bool
	Open      *openQ
	CancelURL string
	// Paste carries the paste box back, editable, above its preview.
	Paste    bool
	Text     string
	Role     string
	RoleName string
}

type counts struct{ All, Internal, External, Approved, Flagged, NoRank int }

type approvedSummary struct{ All, Holding, NotHolding, NotInServer int }

// Line is the approved list's state in one sentence.
func (a approvedSummary) Line() string {
	parts := []string{fmt.Sprintf("%d hold External", a.Holding)}
	if a.Holding == 1 {
		parts[0] = "1 holds External"
	}
	if a.NotHolding > 0 {
		parts = append(parts, plural(a.NotHolding, "doesn't", "don't"))
	}
	if a.NotInServer > 0 {
		parts = append(parts, plural(a.NotInServer, "isn't in the server", "aren't in the server"))
	}
	return plural(a.All, "approved collaborator", "approved collaborators") + ": " + strings.Join(parts, ", ") + "."
}

type unitSummary struct {
	Unit            string
	Roster, Holding int
}

type page struct {
	// The layout's fields.
	Title, Version, ForumURL, Username string
	SignedIn                           bool

	Variant, VariantName string
	Prev, Next           string
	NoScript             bool
	ScriptURL            string
	Q                    url.Values
	Ret                  string
	Filter               string
	Edit                 string
	Open                 string
	Rows                 []*member
	Internal, External   []*member
	Approved             []*member
	Detail               *member
	Counts               counts
	ApprovedSum          approvedSummary
	UnitSum              unitSummary
	Units                []string
	Pending              *pending
	Report               *report
	Log                  []entry
	OpenPurge, OpenLong  *openQ
	OpenPartial          *openQ
	Sample               string
}

type rowCtx struct {
	P *page
	M *member
}

type pasteCtx struct {
	P     *page
	Role  string
	Scope string
}

type state struct {
	mu      sync.Mutex
	members map[string]*member
	roster  map[string][]string // unit -> forum usernames
	noLink  map[string]bool     // forum usernames whose milpac has no Discord ID
	gone    map[string]string   // forum username -> Discord ID not in the server
	log     []entry
	flash   *report
	sample  string
	tmpl    map[bool]*template.Template
}

func main() {
	s := &state{}
	s.seed()
	if err := s.parse(); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("panel/static"))))
	mux.Handle("GET /proto/", http.StripPrefix("/proto/", http.FileServer(http.Dir("panel/prototype/foxhole"))))
	mux.HandleFunc("GET /{$}", s.get)
	mux.HandleFunc("POST /notes", s.notes)
	mux.HandleFunc("POST /bulk", s.bulk)
	mux.HandleFunc("POST /paste", s.paste)
	mux.HandleFunc("POST /unit", s.unit)
	mux.HandleFunc("POST /purge", s.purge)
	mux.HandleFunc("POST /readd", s.readd)
	mux.HandleFunc("POST /reset", s.reset)
	log.Printf("Foxhole page prototype on http://%s/?variant=A", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// parse wraps the panel's own layout around the prototype's content. The
// rail shows a Foxhole manager's navigation, and the head gains the
// prototype's stylesheet and script. The no-script set drops both scripts.
func (s *state) parse() error {
	raw, err := os.ReadFile("panel/templates/layout.html")
	if err != nil {
		return fmt.Errorf("run this from the repo root: %w", err)
	}
	layout := strings.Replace(string(raw),
		`<nav><a class="item is-selected" href="/">Hubs</a></nav>`,
		`<nav><a class="item is-selected" href="/?variant={{.Variant}}">Foxhole</a><a class="item" href="#" title="Not built yet">Recordings</a></nav>`, 1)
	layout = strings.Replace(layout, `</head>`,
		`<link rel="stylesheet" href="/proto/foxhole.css">
<script src="/proto/foxhole.js" defer></script>
</head>`, 1)
	noScript := strings.Replace(layout, `<script src="/static/panel.js" defer></script>`, "", 1)
	noScript = strings.Replace(noScript, `<script src="/proto/foxhole.js" defer></script>`, "", 1)

	funcs := template.FuncMap{
		"link": link,
		"join": strings.Join,
		"pick": func(c bool, a, b []*member) []*member {
			if c {
				return a
			}
			return b
		},
		"row": func(p *page, m *member) rowCtx { return rowCtx{p, m} },
		"paste": func(p *page, role, scope string) pasteCtx {
			return pasteCtx{p, role, scope}
		},
		"scoped": func(p *page, scope string) *pending {
			if p.Pending != nil && p.Pending.Scope == scope {
				return p.Pending
			}
			return nil
		},
	}
	s.tmpl = map[bool]*template.Template{}
	for script, src := range map[bool]string{true: layout, false: noScript} {
		t, err := template.New("layout.html").Funcs(funcs).Parse(src)
		if err != nil {
			return err
		}
		if _, err := t.ParseFiles("panel/prototype/foxhole/foxhole.html"); err != nil {
			return err
		}
		s.tmpl[script] = t
	}
	return nil
}

// link is the current page's address with some query values replaced. An
// empty value drops the key.
func link(q url.Values, kv ...string) string {
	out := url.Values{}
	for k, v := range q {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			out.Del(kv[i])
		} else {
			out.Set(kv[i], kv[i+1])
		}
	}
	return "/?" + out.Encode()
}

// retOf is the page the request came from: the GET's query, or the posted ret.
func retOf(r *http.Request) string {
	if r.Method == http.MethodGet {
		return r.URL.RawQuery
	}
	return r.FormValue("ret")
}

func (s *state) get(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.render(w, r, nil)
}

func (s *state) render(w http.ResponseWriter, r *http.Request, pend *pending) {
	ret := retOf(r)
	q, _ := url.ParseQuery(ret)
	v := strings.ToUpper(q.Get("variant"))
	idx := 0
	for i, x := range variants {
		if x.Key == v {
			idx = i
		}
	}
	v = variants[idx].Key
	q.Set("variant", v)
	p := &page{
		Title: "Foxhole", Version: "prototype", ForumURL: "#", SignedIn: true, Username: signedIn,
		Variant: v, VariantName: variants[idx].Name,
		Prev:     link(q, "variant", variants[(idx+len(variants)-1)%len(variants)].Key, "edit", "", "member", "", "open", "", "filter", ""),
		Next:     link(q, "variant", variants[(idx+1)%len(variants)].Key, "edit", "", "member", "", "open", "", "filter", ""),
		NoScript: q.Get("noscript") == "1",
		Q:        q, Ret: q.Encode(),
		Filter: q.Get("filter"), Edit: q.Get("edit"), Open: q.Get("open"),
		Units:       []string{"D/ACD"},
		Pending:     pend,
		Log:         s.log,
		OpenPurge:   openPurge,
		OpenLong:    openLong,
		OpenPartial: openPartial,
		Sample:      s.sample,
	}
	if p.NoScript {
		p.ScriptURL = link(q, "noscript", "")
	} else {
		p.ScriptURL = link(q, "noscript", "1")
	}
	if pend == nil && r.Method == http.MethodGet {
		p.Report = s.flash
		s.flash = nil
	}
	p.Rows = s.holders()
	for _, m := range p.Rows {
		p.Counts.All++
		if m.Internal {
			p.Counts.Internal++
			p.Internal = append(p.Internal, m)
		}
		if m.External {
			p.Counts.External++
			p.External = append(p.External, m)
		}
		if m.Approved {
			p.Counts.Approved++
			p.Approved = append(p.Approved, m)
			p.ApprovedSum.All++
			switch {
			case !m.InServer:
				p.ApprovedSum.NotInServer++
			case m.External:
				p.ApprovedSum.Holding++
			default:
				p.ApprovedSum.NotHolding++
			}
		}
		if m.Flagged() {
			p.Counts.Flagged++
		}
		if m.NoRank() {
			p.Counts.NoRank++
		}
	}
	p.UnitSum = unitSummary{Unit: "D/ACD", Roster: len(s.roster["D/ACD"])}
	for _, f := range s.roster["D/ACD"] {
		if m := s.byForum(f); m != nil && m.Internal {
			p.UnitSum.Holding++
		}
	}
	if id := q.Get("member"); id != "" {
		p.Detail = s.members[id]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl[!p.NoScript].ExecuteTemplate(w, "layout.html", p); err != nil {
		log.Print(err)
	}
}

func (s *state) holders() []*member {
	var out []*member
	for _, m := range s.members {
		if m.Internal || m.External || m.Approved {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display) })
	return out
}

func (s *state) byForum(forum string) *member {
	for _, m := range s.members {
		if m.Forum == forum {
			return m
		}
	}
	return nil
}

// done records an applied action and sends the browser back to the page it
// came from, minus the note being edited.
func (s *state) done(w http.ResponseWriter, r *http.Request, rep *report, anchor string) {
	s.flash = rep
	q, _ := url.ParseQuery(r.FormValue("ret"))
	q.Del("edit")
	q.Del("open")
	http.Redirect(w, r, "/?"+q.Encode()+anchor, http.StatusSeeOther)
}

func (s *state) logIt(action, summary string, members []string, notes []noteChange) {
	s.log = append([]entry{{At: time.Now().UTC(), Who: signedIn, Action: action, Summary: summary, Members: members, Notes: notes}}, s.log...)
}

func (s *state) picked(r *http.Request) []*member {
	var out []*member
	for _, id := range r.PostForm["id"] {
		if m := s.members[id]; m != nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display) })
	return out
}

func names(ms []*member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Display
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// seconds is the rough Discord time for n role changes, at about one a second.
func seconds(n int) string {
	if n < 60 {
		return fmt.Sprintf("about %d s", max(n, 1))
	}
	return fmt.Sprintf("about %d min", (n+30)/60)
}

func (s *state) notes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	m := s.members[r.FormValue("id")]
	if m == nil {
		s.done(w, r, &report{Title: "That member is gone."}, "")
		return
	}
	after := strings.TrimSpace(r.FormValue("note"))
	if after != m.Note {
		s.logIt("note", "Edited a note.", nil, []noteChange{{m.Display, orNone(m.Note), orNone(after)}})
		m.Note = after
	}
	s.done(w, r, &report{Title: "Saved the note on " + m.Display + "."}, "#m-"+m.ID)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func (s *state) bulk(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	op := r.FormValue("op")
	scope := r.FormValue("scope")
	anchor := ""
	if scope != "" {
		anchor = "#" + scope
	}
	if op == "save-notes" {
		var changes []noteChange
		for key, vals := range r.PostForm {
			id, ok := strings.CutPrefix(key, "note-")
			m := s.members[id]
			if !ok || m == nil || len(vals) == 0 {
				continue
			}
			after := strings.TrimSpace(vals[0])
			if after != m.Note {
				changes = append(changes, noteChange{m.Display, orNone(m.Note), orNone(after)})
				m.Note = after
			}
		}
		if len(changes) == 0 {
			s.done(w, r, &report{Title: "No note changed."}, anchor)
			return
		}
		sort.Slice(changes, func(i, j int) bool { return changes[i].Member < changes[j].Member })
		s.logIt("note", "Edited "+plural(len(changes), "note.", "notes."), nil, changes)
		s.done(w, r, &report{Title: "Saved " + plural(len(changes), "note", "notes") + "."}, anchor)
		return
	}
	ms := s.picked(r)
	if len(ms) == 0 {
		s.done(w, r, &report{Title: "Nothing selected. Tick members in the list first."}, anchor)
		return
	}
	switch op {
	case "approve":
		var done, already, skipped []string
		for _, m := range ms {
			switch {
			case m.Approved:
				already = append(already, m.Display)
			case !m.External:
				skipped = append(skipped, m.Display)
			default:
				m.Approved = true
				done = append(done, m.Display)
			}
		}
		rep := &report{Title: "Approved " + plural(len(done), "collaborator", "collaborators") + "."}
		if len(done) > 0 {
			rep.Lines = append(rep.Lines, "Approved: "+strings.Join(done, ", "))
			s.logIt("approve", "Approved "+plural(len(done), "collaborator.", "collaborators."), done, nil)
		}
		if len(already) > 0 {
			rep.Lines = append(rep.Lines, "Already approved: "+strings.Join(already, ", "))
		}
		if len(skipped) > 0 {
			rep.Lines = append(rep.Lines, "Skipped, they don't hold External: "+strings.Join(skipped, ", "))
		}
		s.done(w, r, rep, anchor)
	case "unapprove":
		var done []string
		for _, m := range ms {
			if m.Approved {
				m.Approved = false
				done = append(done, m.Display)
			}
		}
		if len(done) > 0 {
			s.logIt("clear approval", "Cleared "+plural(len(done), "approval.", "approvals."), done, nil)
		}
		s.done(w, r, &report{Title: "Cleared " + plural(len(done), "approval", "approvals") + ".", Lines: []string{strings.Join(done, ", ")}}, anchor)
	case "remove-internal", "remove-external":
		role := "Internal"
		if op == "remove-external" {
			role = "External"
		}
		holds := func(m *member) bool {
			if role == "Internal" {
				return m.Internal
			}
			return m.External
		}
		var hit, miss, cleared []*member
		for _, m := range ms {
			if holds(m) {
				hit = append(hit, m)
				if role == "External" && m.Approved {
					cleared = append(cleared, m)
				}
			} else {
				miss = append(miss, m)
			}
		}
		if r.FormValue("confirmed") != "1" {
			p := &pending{
				Scope:     scope,
				Title:     "Remove " + role + " from " + plural(len(hit), "member", "members") + "?",
				InputHead: "Member",
				Action:    "/bulk" + anchor,
				Fields:    []field{{"ret", r.FormValue("ret")}, {"op", op}, {"scope", scope}},
				Confirm:   "Remove " + role,
				Danger:    true,
				CancelURL: "/?" + r.FormValue("ret") + anchor,
			}
			p.Lines = append(p.Lines, "Discord takes "+seconds(len(hit))+".")
			if len(cleared) > 0 {
				p.Lines = append(p.Lines, "This also clears the approval of "+strings.Join(names(cleared), ", ")+". They won't get External back at the next re-add.")
			}
			for _, m := range hit {
				st, cl := "loses "+role, "tag--warn"
				if role == "External" && m.Approved {
					st = "loses External and approval"
				}
				p.Rows = append(p.Rows, prow{Member: m.Display, Status: st, Class: cl})
				p.Fields = append(p.Fields, field{"id", m.ID})
			}
			for _, m := range miss {
				p.Rows = append(p.Rows, prow{Member: m.Display, Status: "doesn't hold " + role + ", skipped", Class: ""})
			}
			if len(hit) == 0 {
				p.Confirm = ""
				p.Title = "None of the selected members hold " + role + "."
			}
			s.render(w, r, p)
			return
		}
		for _, m := range hit {
			if role == "Internal" {
				m.Internal = false
			} else {
				m.External = false
				m.Approved = false
			}
		}
		summary := "Removed " + role + " from " + plural(len(hit), "member.", "members.")
		if len(cleared) > 0 {
			summary += " Cleared " + plural(len(cleared), "approval.", "approvals.")
		}
		s.logIt("remove", summary, names(hit), nil)
		rep := &report{Title: summary, Lines: []string{strings.Join(names(hit), ", ")}, Open: openLong}
		s.done(w, r, rep, anchor)
	default:
		s.done(w, r, &report{Title: "Unknown action."}, anchor)
	}
}

var mention = regexp.MustCompile(`^<@!?(\d{17,20})>$`)
var snowflake = regexp.MustCompile(`^\d{17,20}$`)

// resolve matches one pasted line: an ID or mention exactly, then a username,
// then a display name. A display name more than one member carries is left
// unresolved, the map's open fog.
func (s *state) resolve(line string) (m *member, why string) {
	id := ""
	if g := mention.FindStringSubmatch(line); g != nil {
		id = g[1]
	} else if snowflake.MatchString(line) {
		id = line
	}
	if id != "" {
		if m := s.members[id]; m != nil && m.InServer {
			return m, ""
		}
		return nil, "no member with this ID in the server"
	}
	name := strings.ToLower(strings.TrimPrefix(line, "@"))
	for _, m := range s.members {
		if m.InServer && strings.ToLower(m.Username) == name {
			return m, ""
		}
	}
	var hits []*member
	for _, m := range s.members {
		if m.InServer && strings.ToLower(m.Display) == name {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 0:
		return nil, "no member matches"
	case 1:
		return hits[0], ""
	}
	return nil, fmt.Sprintf("matches %d members: %s", len(hits), strings.Join(func() []string {
		var o []string
		for _, h := range hits {
			o = append(o, "@"+h.Username)
		}
		sort.Strings(o)
		return o
	}(), ", "))
}

func (s *state) paste(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	role := r.FormValue("role")
	if role != "external" {
		role = "internal"
	}
	roleName := map[string]string{"internal": "Internal", "external": "External"}[role]
	scope := r.FormValue("scope")
	anchor := "#pending"
	text := r.FormValue("lines")
	type hit struct {
		line string
		m    *member
		why  string
	}
	var hits []hit
	seen := map[string]int{}
	var add []*member
	ambiguous := false
	n := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		n++
		m, why := s.resolve(line)
		if m != nil {
			if first, dup := seen[m.ID]; dup {
				hits = append(hits, hit{line, m, fmt.Sprintf("same member as line %d", first)})
				continue
			}
			seen[m.ID] = n
		}
		if strings.HasPrefix(why, "matches") {
			ambiguous = true
		}
		hits = append(hits, hit{line, m, why})
		if m != nil && why == "" && !((role == "internal" && m.Internal) || (role == "external" && m.External)) {
			add = append(add, m)
		}
	}
	if r.FormValue("step") != "confirm" {
		p := &pending{
			Scope:     scope,
			Title:     "Add to " + roleName + ": check what each line matched",
			InputHead: "Line",
			Action:    "/paste" + anchor,
			Fields:    []field{{"ret", r.FormValue("ret")}, {"role", role}, {"lines", text}, {"step", "confirm"}, {"scope", scope}},
			Confirm:   "Add " + plural(len(add), "member", "members") + " to " + roleName,
			CancelURL: "/?" + r.FormValue("ret"),
			Paste:     true, Text: text, Role: role, RoleName: roleName,
		}
		if ambiguous {
			p.Open = openAmbig
		}
		for _, h := range hits {
			row := prow{Input: h.line}
			switch {
			case h.m == nil:
				row.Status, row.Class = h.why, "tag--warn"
			case h.why != "":
				row.Member, row.Status = h.m.Display+" @"+h.m.Username, h.why
			case (role == "internal" && h.m.Internal) || (role == "external" && h.m.External):
				row.Member, row.Status = h.m.Display+" @"+h.m.Username, "already holds "+roleName
			default:
				row.Member, row.Status, row.Class = h.m.Display+" @"+h.m.Username, "gets "+roleName, "tag--on"
				if role == "internal" && h.m.Rank == "" {
					row.Status += ", holds no rank role"
				}
			}
			p.Rows = append(p.Rows, row)
		}
		if n == 0 {
			p.Title = "Paste at least one line."
		}
		if len(add) == 0 {
			p.Confirm = ""
			p.Lines = append(p.Lines, "Nothing to add.")
		} else {
			p.Lines = append(p.Lines, "Nothing changes until you confirm. Discord takes "+seconds(len(add))+".")
		}
		s.render(w, r, p)
		return
	}
	for _, m := range add {
		if role == "internal" {
			m.Internal = true
		} else {
			m.External = true
		}
	}
	summary := "Added " + plural(len(add), "member", "members") + " to " + roleName + "."
	s.logIt("add", summary, names(add), nil)
	rep := &report{Title: summary, Lines: []string{strings.Join(names(add), ", ")}, Open: openLong}
	if skipped := n - len(add); skipped > 0 {
		rep.Lines = append(rep.Lines, plural(skipped, "line was", "lines were")+" skipped: no match, more than one match, a repeat, or the member already held "+roleName+".")
	}
	s.done(w, r, rep, "")
}

func (s *state) unit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	unit := r.FormValue("unit")
	roster, ok := s.roster[unit]
	if !ok {
		s.done(w, r, &report{Title: "Not a validated internal unit."}, "")
		return
	}
	scope := r.FormValue("scope")
	var add []*member
	var rows []prow
	for _, f := range roster {
		row := prow{Input: f}
		switch m := s.byForum(f); {
		case s.noLink[f]:
			row.Status, row.Class = "no Discord account on the milpac", "tag--warn"
		case m == nil || !m.InServer:
			row.Status, row.Class = "not in the server", "tag--warn"
		case m.Internal:
			row.Member, row.Status = m.Display+" @"+m.Username, "already holds Internal"
		default:
			row.Member, row.Status, row.Class = m.Display+" @"+m.Username, "gets Internal", "tag--on"
			add = append(add, m)
		}
		rows = append(rows, row)
	}
	if r.FormValue("step") != "confirm" {
		p := &pending{
			Scope:     scope,
			Title:     "Add the " + unit + " roster to Internal?",
			Lines:     []string{fmt.Sprintf("%d on the roster. %s.", len(roster), plural(len(add), "member gets Internal", "members get Internal"))},
			InputHead: "Forum username",
			Rows:      rows,
			Action:    "/unit#pending",
			Fields:    []field{{"ret", r.FormValue("ret")}, {"unit", unit}, {"step", "confirm"}, {"scope", scope}},
			Confirm:   "Add " + plural(len(add), "member", "members") + " to Internal",
			CancelURL: "/?" + r.FormValue("ret"),
		}
		if len(add) == 0 {
			p.Confirm = ""
		}
		s.render(w, r, p)
		return
	}
	for _, m := range add {
		m.Internal = true
	}
	summary := fmt.Sprintf("Added the %s roster to Internal: %s.", unit, plural(len(add), "member", "members"))
	s.logIt("unit roster", summary, names(add), nil)
	rep := &report{Title: summary, Open: openLong}
	if len(add) > 0 {
		rep.Lines = append(rep.Lines, strings.Join(names(add), ", "))
	}
	for _, row := range rows {
		if row.Class == "tag--warn" {
			rep.Lines = append(rep.Lines, row.Input+": "+row.Status)
		}
	}
	s.done(w, r, rep, "")
}

func (s *state) purge(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	role := r.FormValue("role")
	scope := r.FormValue("scope")
	var ri, re bool
	switch role {
	case "internal":
		ri = true
	case "external":
		re = true
	default:
		role, ri, re = "both", true, true
	}
	var in, ex []*member
	for _, m := range s.members {
		if ri && m.Internal {
			in = append(in, m)
		}
		if re && m.External {
			ex = append(ex, m)
		}
	}
	what := map[string]string{"internal": "Internal", "external": "External", "both": "Internal and External"}[role]
	if r.FormValue("confirmed") != "1" {
		p := &pending{
			Scope:     scope,
			Title:     "Purge " + what + "?",
			Action:    "/purge#pending",
			Fields:    []field{{"ret", r.FormValue("ret")}, {"role", role}, {"confirmed", "1"}, {"scope", scope}},
			Confirm:   "Purge " + what,
			Danger:    true,
			Open:      openPurge,
			CancelURL: "/?" + r.FormValue("ret"),
		}
		if ri {
			p.Lines = append(p.Lines, "Takes Internal off all "+plural(len(in), "holder.", "holders."))
		}
		if re {
			p.Lines = append(p.Lines, "Takes External off all "+plural(len(ex), "holder", "holders")+", approved collaborators included. They get it back when you run Re-add approved collaborators.")
		}
		p.Lines = append(p.Lines, "Notes and approvals stay.")
		s.render(w, r, p)
		return
	}
	for _, m := range in {
		m.Internal = false
	}
	for _, m := range ex {
		m.External = false
	}
	summary := "Purged " + what + "."
	var touched []string
	var lines []string
	if ri {
		lines = append(lines, plural(len(in), "member", "members")+" no longer hold Internal.")
		touched = append(touched, names(in)...)
	}
	if re {
		lines = append(lines, plural(len(ex), "member", "members")+" no longer hold External. Run Re-add approved collaborators next.")
		touched = append(touched, names(ex)...)
	}
	sort.Strings(touched)
	s.logIt("purge", summary+" "+strings.Join(lines, " "), touched, nil)
	s.done(w, r, &report{Title: summary, Lines: lines, Open: openLong}, "")
}

func (s *state) readd(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	var gave, had, gone []string
	for _, m := range s.holders() {
		if !m.Approved {
			continue
		}
		switch {
		case !m.InServer:
			gone = append(gone, m.Display)
		case m.External:
			had = append(had, m.Display)
		default:
			m.External = true
			gave = append(gave, m.Display)
		}
	}
	summary := "Re-added approved collaborators: gave External to " + plural(len(gave), "member.", "members.")
	rep := &report{Title: summary}
	if len(gave) > 0 {
		rep.Lines = append(rep.Lines, "Gave External: "+strings.Join(gave, ", "))
	}
	if len(had) > 0 {
		rep.Lines = append(rep.Lines, "Already held External: "+strings.Join(had, ", "))
	}
	for _, g := range gone {
		rep.Lines = append(rep.Lines, "Skipped "+g+": not in the server. Approval and note kept.")
	}
	s.logIt("re-add", summary, gave, nil)
	anchor := ""
	if sc := r.FormValue("scope"); sc != "" {
		anchor = "#" + sc
	}
	s.done(w, r, rep, anchor)
}

func (s *state) reset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	s.seed()
	s.done(w, r, &report{Title: "Prototype data reset."}, "")
}
