package commands

// Shared report scaffolding.
//
// /promo and /billetaudit both attach a self-contained HTML table and a CSV
// of the same rows, and had grown their own copy of the document shell, the
// base stylesheet and the csv.Writer boilerplate. The two reports differ in
// their columns and in whether the table is interactive, so what is shared
// here is only the part that was genuinely identical: the document frame and
// the mechanical CSV write.

import (
	"encoding/csv"
	"fmt"
	"html"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// reportBaseCSS is the shared look: readable table, sticky header, striped
// rows. Callers append their own rules for anything beyond that.
const reportBaseCSS = `body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:1.5rem;color:#1a1a1a}
h1{font-size:1.25rem;margin:0 0 .25rem}
p.meta{color:#555;margin:0 0 1rem;font-size:.9rem}
table{border-collapse:collapse;width:100%;font-size:.875rem}
th,td{border:1px solid #ddd;padding:.4rem .6rem;text-align:left;vertical-align:top}
th{background:#f4f4f4;position:sticky;top:0}
tr:nth-child(even) td{background:#fafafa}
.note{margin-top:1.5rem;padding:.75rem;background:#fff8e1;border-left:3px solid #e6a700;font-size:.875rem}`

// writeReportHead opens an HTML document with the shared stylesheet plus any
// caller-specific rules, and emits the page heading. The title is escaped
// here so callers cannot forget: report titles carry scope names that come
// from user input.
func writeReportHead(b *strings.Builder, title, extraCSS string) {
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">\n")
	fmt.Fprintf(b, "<title>%s</title>\n", html.EscapeString(title))
	b.WriteString("<style>\n")
	b.WriteString(reportBaseCSS)
	if extraCSS != "" {
		b.WriteString("\n")
		b.WriteString(extraCSS)
	}
	b.WriteString("\n</style>\n</head><body>\n")
	fmt.Fprintf(b, "<h1>%s</h1>\n", html.EscapeString(title))
}

// writeReportFoot closes the document, optionally emitting a trailing note
// and a script block. The note is escaped; the script is not, since it is
// always a package constant rather than anything derived from a record.
func writeReportFoot(b *strings.Builder, note, script string) {
	if note != "" {
		fmt.Fprintf(b, "<p class=\"note\">%s</p>\n", html.EscapeString(note))
	}
	if script != "" {
		b.WriteString(script)
	}
	b.WriteString("</body></html>\n")
}

// csvReport renders a header and rows to CSV text. Writing to a
// strings.Builder cannot fail, so the per-write errors are discarded and only
// the deferred Flush error would be meaningful; it too can only surface an
// underlying writer failure that a Builder never produces.
func csvReport(header []string, rows [][]string) string {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	_ = w.Write(header)
	for _, row := range rows {
		_ = w.Write(row)
	}
	w.Flush()
	return sb.String()
}

// reportFile wraps report text as a Discord attachment.
func reportFile(name, contentType, body string) *discordgo.File {
	return &discordgo.File{
		Name:        name,
		ContentType: contentType,
		Reader:      strings.NewReader(body),
	}
}
