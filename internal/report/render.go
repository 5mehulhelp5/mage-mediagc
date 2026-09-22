// Package report renders analysis results as a console summary, JSON or a
// Markdown document suitable for attaching to a ticket.
//
// Reports are the only localized surface in mage-mediagc. Logs, errors and the
// JSON payload are always English, so an issue report or an automation script
// reads the same regardless of who ran the tool. Pick the report language with
// `output.language` or `--language`.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shuaiZend/mage-mediagc/internal/analyzer"
	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// ToolInfo identifies the producing binary.
type ToolInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// TargetInfo describes what was analyzed.
type TargetInfo struct {
	MediaRoot   string `json:"mediaRoot"`
	MagentoRoot string `json:"magentoRoot,omitempty"`
	Database    string `json:"database"`
	MySQL       string `json:"mysqlVersion,omitempty"`
}

// Payload is the machine-readable report.
type Payload struct {
	Tool        ToolInfo              `json:"tool"`
	GeneratedAt string                `json:"generatedAt"`
	Target      TargetInfo            `json:"target"`
	Analysis    *analyzer.Result      `json:"analysis"`
	Database    *magento.OrphanReport `json:"database,omitempty"`
}

// Options tunes rendering.
type Options struct {
	// Color enables ANSI styling on a TTY.
	Color bool
	// TopDirs is how many directory buckets to list.
	TopDirs int
	// Verbose adds the per-source breakdown and smaller tables.
	Verbose int
	// Language selects the report language: "en" (default) or "zh".
	Language string
}

// DefaultOptions returns sensible rendering defaults.
func DefaultOptions() Options {
	return Options{TopDirs: 10, Language: LangEnglish}
}

// Supported report languages.
const (
	LangEnglish = "en"
	LangChinese = "zh"
)

// Languages lists the accepted values of Options.Language.
var Languages = []string{LangEnglish, LangChinese}

// ---------------------------------------------------------------------------
// JSON
// ---------------------------------------------------------------------------

// RenderJSON writes the payload as indented JSON.
func RenderJSON(w io.Writer, p *Payload) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// ---------------------------------------------------------------------------
// Console table
// ---------------------------------------------------------------------------

// RenderTable writes a human-readable summary.
func RenderTable(w io.Writer, p *Payload, opts Options) error {
	if opts.TopDirs == 0 {
		opts.TopDirs = DefaultOptions().TopDirs
	}
	res := p.Analysis
	if res == nil {
		return fmt.Errorf("no analysis to render")
	}
	msg := messagesFor(opts.Language)

	dim := func(s string) string { return s }
	bold := func(s string) string { return s }
	if opts.Color {
		dim = func(s string) string { return "\x1b[2m" + s + "\x1b[0m" }
		bold = func(s string) string { return "\x1b[1m" + s + "\x1b[0m" }
	}

	fmt.Fprintf(w, "%s %s\n", bold("mage-mediagc"), dim(p.Tool.Version))
	fmt.Fprintf(w, "%s\n", dim(strings.Repeat("─", 62)))
	fmt.Fprintf(w, "%-10s %s\n", msg.mediaRoot, res.MediaRoot)
	if p.Target.MagentoRoot != "" {
		fmt.Fprintf(w, "%-10s %s\n", msg.magentoRoot, p.Target.MagentoRoot)
	}
	fmt.Fprintf(w, "%-10s %s%s\n", msg.database, p.Target.Database, versionSuffix(p.Target.MySQL))
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  "+msg.rowCountBytes, msg.diskOriginals, res.DiskFiles, media.HumanBytes(res.DiskBytes))
	fmt.Fprintf(tw, "  "+msg.rowCountBytes, msg.derivedCache, res.CacheFiles, media.HumanBytes(res.CacheBytes))
	fmt.Fprintf(tw, "  "+msg.rowCountOnly, msg.dbReferences, res.RefPaths)
	fmt.Fprintf(tw, "  "+msg.rowCountBytes, msg.liveFiles, res.LiveFiles, media.HumanBytes(res.LiveBytes))
	ratio := 0.0
	if res.DiskFiles > 0 {
		ratio = float64(res.OrphanFiles) / float64(res.DiskFiles) * 100
	}
	fmt.Fprintf(tw, "  "+msg.rowCountBytesPercent,
		msg.orphanFiles, res.OrphanFiles, media.HumanBytes(res.OrphanBytes), ratio)
	if len(res.Missing) > 0 {
		fmt.Fprintf(tw, "  "+msg.rowCountOnly, msg.missingFiles, len(res.Missing))
	}
	_ = tw.Flush()

	if res.CacheFiles > 0 || res.OrphanFiles > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", bold(msg.reclaimable))
		fmt.Fprintf(w, "  "+msg.reclaimDetail+"\n",
			media.HumanBytes(res.CacheBytes), media.HumanBytes(res.OrphanBytes),
			media.HumanBytes(res.CacheBytes+res.OrphanBytes))
	}

	if opts.Verbose > 0 && len(res.RefStats) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", bold(msg.referenceSources))
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range res.RefStats {
			if s.Skipped != "" {
				fmt.Fprintf(tw, "  %s\t%s\n", s.Source, dim(msg.skipped+": "+s.Skipped))
				continue
			}
			// The source name alone is often opaque (a raw table name), so the
			// human-readable description is carried as a trailing column rather
			// than dropped. The format already ends in a newline: adding
			// another would emit a blank line, which also breaks tabwriter's
			// column block and leaves every row unaligned.
			fmt.Fprintf(tw, "  "+msg.rowRowsPaths, s.Source, s.RowsScanned, msg.rowsUnit,
				s.PathsAdded, msg.pathsUnit, s.Description)
		}
		_ = tw.Flush()
	}

	top := res.TopOrphanDirs(opts.TopDirs)
	if len(top) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", bold(msg.orphanBuckets))
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, d := range top {
			pct := 0.0
			if d.Total > 0 {
				pct = float64(d.Orphans) / float64(d.Total) * 100
			}
			fmt.Fprintf(tw, "  %s\t%d / %d\t%s\t%.1f%%\n",
				d.Dir, d.Orphans, d.Total, media.HumanBytes(d.OrphanBytes), pct)
		}
		_ = tw.Flush()
	}

	if p.Database != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", bold(msg.databaseOrphans))
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "  "+msg.rowCountOnly, msg.productCount, p.Database.ProductCount)
		fmt.Fprintf(tw, "  "+msg.orphanRowsLine, p.Database.TotalOrphans, p.Database.TotalRows)
		_ = tw.Flush()
		if opts.Verbose > 0 {
			tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			for _, t := range p.Database.Tables {
				if t.Skipped != "" {
					fmt.Fprintf(tw, "  %s\t%s\n", t.Table, dim(msg.skipped))
					continue
				}
				fmt.Fprintf(tw, "  %s\t%d / %d\t%.1f%%\n", t.Table, t.Orphans, t.Total, t.Percent)
			}
			_ = tw.Flush()
		}
	}

	if len(res.Warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", bold(msg.warnings))
		for _, warn := range res.Warnings {
			fmt.Fprintf(w, "  ! %s\n", warn)
		}
	}
	return nil
}

func versionSuffix(v string) string {
	if v == "" {
		return ""
	}
	return " (" + v + ")"
}

// ---------------------------------------------------------------------------
// Markdown
// ---------------------------------------------------------------------------

// RenderMarkdown writes a self-contained report document.
func RenderMarkdown(w io.Writer, p *Payload, opts Options) error {
	if opts.TopDirs == 0 {
		opts.TopDirs = 20
	}
	res := p.Analysis
	if res == nil {
		return fmt.Errorf("no analysis to render")
	}
	msg := messagesFor(opts.Language)

	fmt.Fprintf(w, "# mage-mediagc %s\n\n", msg.mdTitle)
	fmt.Fprintf(w, "- %s: %s\n", msg.mdGeneratedAt, p.GeneratedAt)
	fmt.Fprintf(w, "- %s: %s %s\n", msg.mdToolVersion, p.Tool.Name, p.Tool.Version)
	fmt.Fprintf(w, "- %s: `%s`\n", msg.mediaRoot, res.MediaRoot)
	if p.Target.MagentoRoot != "" {
		fmt.Fprintf(w, "- %s: `%s`\n", msg.magentoRoot, p.Target.MagentoRoot)
	}
	fmt.Fprintf(w, "- %s: `%s`%s\n", msg.database, p.Target.Database, versionSuffix(p.Target.MySQL))
	fmt.Fprintln(w)

	ratio := 0.0
	if res.DiskFiles > 0 {
		ratio = float64(res.OrphanFiles) / float64(res.DiskFiles) * 100
	}
	reclaim := res.CacheBytes + res.OrphanBytes

	fmt.Fprintf(w, "## %s\n\n", msg.mdSummary)
	fmt.Fprintf(w, "| %s | %s | %s |\n|---|---:|---:|\n", msg.mdMetric, msg.mdCount, msg.mdSize)
	fmt.Fprintf(w, "| %s | %d | %s |\n", msg.diskOriginals, res.DiskFiles, media.HumanBytes(res.DiskBytes))
	fmt.Fprintf(w, "| %s | %d | %s |\n", msg.derivedCache, res.CacheFiles, media.HumanBytes(res.CacheBytes))
	fmt.Fprintf(w, "| %s | %d | — |\n", msg.dbReferences, res.RefPaths)
	fmt.Fprintf(w, "| **%s** | **%d** | **%s** |\n",
		msg.liveFiles, res.LiveFiles, media.HumanBytes(res.LiveBytes))
	fmt.Fprintf(w, "| **%s** | **%d** | **%s** |\n",
		msg.orphanFiles, res.OrphanFiles, media.HumanBytes(res.OrphanBytes))
	fmt.Fprintf(w, "| %s | **%.1f%%** | |\n", msg.mdOrphanRate, ratio)
	if len(res.Missing) > 0 {
		fmt.Fprintf(w, "| %s | %d | — |\n", msg.mdMissing, len(res.Missing))
	}
	fmt.Fprintf(w, "| **%s** | | **%s** |\n", msg.mdReclaimable, media.HumanBytes(reclaim))
	fmt.Fprintln(w)

	fmt.Fprintf(w, "## %s\n\n", msg.mdRefSources)
	fmt.Fprintf(w, "| %s | %s | %s | %s |\n|---|---:|---:|---|\n",
		msg.mdSource, msg.mdRowsScanned, msg.mdPathsAdded, msg.mdNote)
	for _, s := range res.RefStats {
		note := s.Description
		if s.Skipped != "" {
			note = msg.mdSkippedPrefix + s.Skipped
			fmt.Fprintf(w, "| `%s` | — | — | %s |\n", s.Source, note)
			continue
		}
		fmt.Fprintf(w, "| `%s` | %d | %d | %s |\n", s.Source, s.RowsScanned, s.PathsAdded, note)
	}
	fmt.Fprintln(w)

	top := res.TopOrphanDirs(opts.TopDirs)
	if len(top) > 0 {
		fmt.Fprintf(w, "## %s (%d)\n\n", msg.mdOrphanHotspots, len(top))
		fmt.Fprintf(w, "| %s | %s | %s | %s |\n|---|---:|---:|---:|\n",
			msg.mdDir, msg.mdOrphansOverTotal, msg.mdOrphanRateHdr, msg.mdOrphanBytes)
		for _, d := range top {
			pct := 0.0
			if d.Total > 0 {
				pct = float64(d.Orphans) / float64(d.Total) * 100
			}
			fmt.Fprintf(w, "| `%s` | %d / %d | %.1f%% | %s |\n",
				d.Dir, d.Orphans, d.Total, pct, media.HumanBytes(d.OrphanBytes))
		}
		fmt.Fprintln(w)
	}

	if p.Database != nil {
		fmt.Fprintf(w, "## %s\n\n", msg.mdDBOrphans)
		fmt.Fprintf(w, msg.mdDBSummary+"\n\n",
			p.Database.ProductCount, p.Database.TotalOrphans, p.Database.TotalRows)
		fmt.Fprintf(w, "| %s | %s | %s |\n|---|---:|---:|\n",
			msg.mdTable, msg.mdOrphansOverTotal, msg.mdShare)
		for _, t := range p.Database.Tables {
			if t.Skipped != "" {
				fmt.Fprintf(w, "| `%s` | — | %s |\n", t.Table, msg.skipped)
				continue
			}
			fmt.Fprintf(w, "| `%s` | %d / %d | %.1f%% |\n", t.Table, t.Orphans, t.Total, t.Percent)
		}
		fmt.Fprintln(w)
	}

	if len(res.Warnings) > 0 {
		fmt.Fprintf(w, "## %s\n\n", msg.mdWarnings)
		for _, warn := range res.Warnings {
			fmt.Fprintf(w, "- ⚠️ %s\n", warn)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "## %s\n\n", msg.mdNextSteps)
	for i, step := range msg.mdSteps {
		fmt.Fprintf(w, "%d. %s\n", i+1, step)
	}
	fmt.Fprintln(w)

	if len(res.Missing) > 0 {
		fmt.Fprintf(w, "## %s\n\n```\n", msg.mdMissingList)
		limit := len(res.Missing)
		if limit > 50 {
			limit = 50
		}
		for _, m := range res.Missing[:limit] {
			fmt.Fprintln(w, m)
		}
		if len(res.Missing) > limit {
			fmt.Fprintf(w, msg.mdMoreMissing+"\n", len(res.Missing)-limit)
		}
		fmt.Fprintf(w, "```\n")
	}
	return nil
}

// SortTopDirs is exported for callers that want deterministic ordering before
// rendering a subset.
func SortTopDirs(dirs []analyzer.DirStat) []analyzer.DirStat {
	out := make([]analyzer.DirStat, len(dirs))
	copy(out, dirs)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orphans != out[j].Orphans {
			return out[i].Orphans > out[j].Orphans
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

// NewPayload assembles a payload with generation metadata.
func NewPayload(tool ToolInfo, target TargetInfo, res *analyzer.Result, db *magento.OrphanReport) *Payload {
	return &Payload{
		Tool:        tool,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Target:      target,
		Analysis:    res,
		Database:    db,
	}
}
