// PROTOTYPE mock data, sized to the count on day 33 of a war (#415): 62
// Internal holders and 14 External, one member holding both, six approved
// collaborators, and a pool of guild members the paste box and the roster
// add can find.
package main

import (
	"fmt"
	"strings"
	"time"
)

var surnames = []string{
	"Abernathy", "Alvarez", "Baptiste", "Barlow", "Becker", "Bishop", "Brandt", "Calloway", "Castillo", "Chen",
	"Coleman", "Dalton", "Delgado", "Doyle", "Eriksen", "Farrow", "Fischer", "Fontaine", "Garrison", "Gonzaga",
	"Grady", "Halvorsen", "Hayes", "Hendricks", "Holloway", "Ibarra", "Iverson", "Jansen", "Jovanovic", "Kaminski",
	"Keller", "Kowalski", "Lambert", "Larkin", "Lindgren", "Lozano", "Maddox", "Marsh", "Mercer", "Moreau",
	"Nakamura", "Novak", "Oakley", "Okafor", "Ortega", "Pacheco", "Pruitt", "Quinlan", "Radcliffe", "Reyes",
	"Rivera", "Rourke", "Salazar", "Sato", "Schaefer", "Sorensen", "Stroud", "Tanaka", "Thornton", "Underwood",
	"Valdez", "Vance", "Wexler", "Whitaker", "Yates", "Zielinski", "Ashford", "Brennan", "Cortez", "Draper",
	"Ellison", "Faulkner", "Gallagher", "Hargrove", "Ingram", "Jessup", "Kincaid", "Lockhart", "Monroe", "Nyberg",
}

var initials = "JMDKARTSCBLPEGNW"

var rankCycle = []string{"PFC", "SPC", "PVT", "SPC", "CPL", "SGT", "PFC", "SPC", "SSG", "PVT", "CPL", "SFC", "2LT", "SPC", "RCT", "1LT", "SGT", "PFC", "WO1", "CPT"}

var internalNotes = map[int]string{
	2:  "Joined for this war from A/1-7.",
	5:  "Logi lead for the regiment's Foxhole company.",
	8:  "Asked to keep Internal into the next war.",
	11: "Vouched for by Rivera.D.",
	14: "Back from LOA 20 Sep.",
	17: "Runs the Tuesday op.",
	21: "Discharged 12 Sep, kept Internal for the end of the war. Ask before re-adding.",
	24: "Facility builder, needs access to the logi channels.",
	29: "Joined for one war only.",
	33: "Second account is Kowalski.A, don't add that one.",
	38: "Partisan squad lead.",
	41: "Transferred from 2-7, keep an eye on the roster add.",
	47: "Joined mid-war on day 20.",
	52: "Mentor for the new members.",
	57: "Asked for External too for the coalition server.",
}

var externals = []struct {
	display, user, note string
	approved            bool
}{
	{"[TLR] Kestrel", "kestrel_tlr", "TLR regiment leader. Coalition contact.", true},
	{"[TLR] Wren", "wren.tlr", "TLR logistics officer.", true},
	{"[HVK] Mossgrave", "mossgrave", "HVK leader, joint ops on weekends.", true},
	{"[OSL] Petra", "petra_osl", "OSL liaison.", true},
	{"[HVK] Brannoch", "brannoch", "", false},
	{"[HVK] Sable", "sable_hvk", "Here for the joint op on day 30.", false},
	{"[OSL] Corvid", "corvid", "", false},
	{"[OSL] Lantern", "lantern.osl", "", false},
	{"Tamsin", "tamsin_fh", "Streamer, invited by Vance.R.", false},
	{"[TLR] Hollis", "hollis.tlr", "", false},
	{"[GRN] Oskar", "oskar_grn", "Asked to be approved, waiting on TLR to vouch.", false},
	{"Fennick", "fennick", "", false},
	{"[GRN] Maple", "maple_grn", "", false},
}

// id is a stable 18-digit snowflake-like ID for the i-th mock member.
func id(i int) string { return fmt.Sprintf("%d", 312480000000000000+int64(i)*1000003577) }

func (s *state) seed() {
	s.members = map[string]*member{}
	s.roster = map[string][]string{}
	s.noLink = map[string]bool{}
	n := 0
	add := func(m *member) *member {
		m.ID = id(n)
		n++
		s.members[m.ID] = m
		return m
	}
	cav := func(i int) (display, user string) {
		sn := surnames[i%len(surnames)]
		ini := string(initials[i%len(initials)])
		return sn + "." + ini, strings.ToLower(sn) + "." + strings.ToLower(ini)
	}

	// 62 Internal holders, three of them without a rank role.
	var internal []*member
	for i := 0; i < 62; i++ {
		d, u := cav(i)
		rank := rankCycle[i%len(rankCycle)]
		if i == 21 || i == 44 || i == 59 {
			rank = ""
		}
		m := add(&member{Display: d, Username: u, Forum: d, Rank: rank, Note: internalNotes[i], Internal: true, InServer: true})
		internal = append(internal, m)
	}
	// One Cav member holds External too.
	internal[57].External = true

	// 13 more External holders, four of them approved.
	for _, x := range externals {
		add(&member{Display: x.display, Username: x.user, Note: x.note, External: true, Approved: x.approved, InServer: true})
	}
	// An approved collaborator whose External someone removed by hand in Discord.
	add(&member{Display: "[GRN] Ivo", Username: "ivo.grn", Note: "GRN leader. External removed by hand on day 28, ask why.", Approved: true, InServer: true})
	// An approved collaborator who left the server.
	add(&member{Display: "[OSL] Halden", Username: "halden_osl", Note: "Former OSL leader, left the Discord in August.", Approved: true})

	// Guild members who hold no Foxhole role, for the paste box and the roster.
	var pool []*member
	for i := 62; i < 78; i++ {
		d, u := cav(i)
		pool = append(pool, add(&member{Display: d, Username: u, Forum: d, Rank: rankCycle[i%len(rankCycle)], InServer: true}))
	}
	add(&member{Display: "Ghost", Username: "ghost_42", InServer: true})
	add(&member{Display: "Ghost", Username: "ghostrider.fh", InServer: true})
	add(&member{Display: "Ghost", Username: "gh0st", InServer: true})
	add(&member{Display: "Wolfhound", Username: "wolfhound", InServer: true})
	add(&member{Display: "[TLR] Ardent", Username: "ardent_tlr", InServer: true})

	// The D/ACD roster: 14 who hold Internal, 2 who don't yet, one whose
	// Discord account isn't in the server, one with no Discord on the milpac.
	var dacd []string
	for _, i := range []int{0, 3, 6, 9, 12, 15, 18, 22, 25, 28, 31, 35, 40, 50} {
		dacd = append(dacd, internal[i].Forum)
	}
	dacd = append(dacd, pool[0].Forum, pool[3].Forum, "Petrov.A", "Lindqvist.E")
	s.noLink["Lindqvist.E"] = true
	s.roster["D/ACD"] = dacd

	// Lines for the paste box that hit every result: a new ID, a mention, a
	// current holder, a username, a unique display name, a display name three
	// members share, no match, an ID not in the server, and a repeat.
	s.sample = strings.Join([]string{
		pool[1].ID, "<@" + pool[2].ID + ">", internal[3].Username, "ardent_tlr", "Wolfhound",
		"Ghost", "nobody_here", "312480000000000999", pool[1].ID,
	}, "\n")

	base := time.Date(2026, 10, 2, 19, 40, 0, 0, time.UTC)
	s.log = []entry{
		{At: base, Who: "Vance.R", Action: "note", Summary: "Edited a note.", Notes: []noteChange{{"[OSL] Petra", "(none)", "OSL liaison."}}},
		{At: base.Add(-26 * time.Hour), Who: signedIn, Action: "add", Summary: "Added 3 members to Internal.", Members: []string{internal[44].Display, internal[47].Display, internal[52].Display}},
		{At: base.Add(-74 * time.Hour), Who: "Vance.R", Action: "approve", Summary: "Approved 1 collaborator.", Members: []string{"[HVK] Mossgrave"}},
	}
	s.flash = nil
}
