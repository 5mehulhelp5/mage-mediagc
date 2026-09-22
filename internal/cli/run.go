package cli

import (
	"context"
	"fmt"

	"github.com/shuaiZend/mage-mediagc/internal/analyzer"
	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// analyzeOptions controls the shared scan+analyze pipeline.
type analyzeOptions struct {
	// showProgress prints progress lines to stderr.
	showProgress bool
}

// runAnalysis connects to the database, indexes the media tree, and returns
// the combined analysis. The caller owns the returned client and must close it.
//
// Reference statistics are not returned separately: the analyzer already
// copies them into Result.RefStats for reporting.
func (a *app) runAnalysis(ctx context.Context, opts analyzeOptions) (*analyzer.Result, *magento.Client, error) {
	if err := a.cfg.Validate(false); err != nil {
		return nil, nil, err
	}

	client, err := a.openDB(ctx)
	if err != nil {
		return nil, nil, err
	}

	if opts.showProgress {
		fmt.Fprintln(a.stderr, "collecting database references...")
	}
	refs, err := client.CollectRefs(ctx, magento.RefOptions{
		IncludeContentRefs: a.cfg.Scan.IncludeContentRefs,
	})
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("collect references: %w", err)
	}
	if opts.showProgress {
		fmt.Fprintf(a.stderr, "  %d distinct reference paths from %d sources\n", refs.Len(), len(refs.Stats))
		fmt.Fprintln(a.stderr, "indexing media tree...")
	}

	var scanProgress func(files int64)
	if opts.showProgress {
		printer := a.progressPrinter("  indexed")
		scanProgress = func(files int64) { printer(files, 0) }
	}
	scan, err := media.Scan(ctx, a.cfg.Magento.MediaPath, media.ScanOptions{
		Workers:      a.cfg.Scan.Workers,
		IncludeCache: a.cfg.Scan.IncludeCache,
		ExcludeGlobs: a.cfg.Scan.ExcludeGlobs,
		Progress:     scanProgress,
	})
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	if opts.showProgress {
		fmt.Fprintf(a.stderr, "\r  indexed %d files (%s)\n", len(scan.Files), media.HumanBytes(scan.Bytes))
	}

	res, err := analyzer.Analyze(ctx, scan, refs, analyzer.DefaultOptions())
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return res, client, nil
}

// targetInfo builds the report header for the resolved configuration.
func (a *app) targetInfo(ctx context.Context, client *magento.Client) (name, ver, commit string, targetDB, mysqlVersion string) {
	_, ver, commit = a.toolInfo()
	targetDB = a.cfg.DB.Name
	if client != nil {
		if v, err := client.Version(ctx); err == nil {
			mysqlVersion = v
		}
	}
	return "mage-mediagc", ver, commit, targetDB, mysqlVersion
}
