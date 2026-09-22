package report

// messages holds every human-readable string the report renderers emit.
//
// Reports are the only localized surface; the JSON payload, log lines and
// error messages stay English on purpose, so automation and issue reports read
// the same no matter who ran the tool.
type messages struct {
	// Short field labels, reused by both renderers.
	mediaRoot   string
	magentoRoot string
	database    string

	// Console row labels.
	diskOriginals    string
	derivedCache     string
	dbReferences     string
	liveFiles        string
	orphanFiles      string
	missingFiles     string
	productCount     string
	referenceSources string
	orphanBuckets    string
	databaseOrphans  string
	reclaimable      string
	warnings         string
	skipped          string

	rowsUnit  string
	pathsUnit string

	// Console row formats. The caller prefixes the two-space indent.
	rowCountBytes        string
	rowCountOnly         string
	rowCountBytesPercent string
	rowRowsPaths         string
	orphanRowsLine       string
	reclaimDetail        string

	// Markdown headings and table headers.
	mdTitle            string
	mdGeneratedAt      string
	mdToolVersion      string
	mdSummary          string
	mdMetric           string
	mdCount            string
	mdSize             string
	mdOrphanRate       string
	mdMissing          string
	mdReclaimable      string
	mdRefSources       string
	mdSource           string
	mdRowsScanned      string
	mdPathsAdded       string
	mdNote             string
	mdSkippedPrefix    string
	mdDir              string
	mdOrphansOverTotal string
	mdOrphanBytes      string
	mdDBSummary        string
	mdTable            string
	mdShare            string
	// Section headings. These are deliberately distinct from the console
	// labels above: a console summary reads fine with lowercase labels, but a
	// document that gets attached to a ticket should have title-case headings.
	mdOrphanHotspots string
	mdDBOrphans      string
	mdWarnings       string
	mdOrphanRateHdr  string
	mdNextSteps      string
	mdMissingList    string
	mdMoreMissing    string
	mdSteps          []string
}

const (
	english = LangEnglish
	chinese = LangChinese
)

var messageTable = map[string]messages{
	english: {
		mediaRoot:   "media root",
		magentoRoot: "magento",
		database:    "database",

		diskOriginals:    "files on disk",
		derivedCache:     "thumbnail cache",
		dbReferences:     "db references",
		liveFiles:        "still used",
		orphanFiles:      "orphaned",
		missingFiles:     "missing on disk",
		productCount:     "products",
		referenceSources: "reference sources",
		orphanBuckets:    "orphan hotspots",
		databaseOrphans:  "database orphans",
		reclaimable:      "reclaimable",
		warnings:         "warnings",
		skipped:          "skipped",

		rowsUnit:  "rows",
		pathsUnit: "paths",

		rowCountBytes:        "%s\t%d\t%s\n",
		rowCountOnly:         "%s\t%d\n",
		rowCountBytesPercent: "%s\t%d\t%s (%.1f%%)\n",
		rowRowsPaths:         "%s\t%d %s\t+%d %s\t%s\n",
		orphanRowsLine:       "orphaned rows\t%d / %d\n",
		reclaimDetail:        "cache %s + orphans %s = %s",

		mdTitle:            "catalog media report",
		mdGeneratedAt:      "generated",
		mdToolVersion:      "tool",
		mdSummary:          "Summary",
		mdMetric:           "Metric",
		mdCount:            "Count",
		mdSize:             "Size",
		mdOrphanRate:       "orphan rate",
		mdMissing:          "referenced but missing on disk",
		mdReclaimable:      "total reclaimable",
		mdRefSources:       "Reference sources",
		mdSource:           "Source",
		mdRowsScanned:      "Rows scanned",
		mdPathsAdded:       "Paths added",
		mdNote:             "Notes",
		mdSkippedPrefix:    "skipped: ",
		mdDir:              "Directory",
		mdOrphansOverTotal: "Orphans / total",
		mdOrphanBytes:      "Orphan size",
		mdDBSummary:        "Live products: **%d**; orphaned rows: **%d** of %d.",
		mdTable:            "Table",
		mdShare:            "Share",
		mdOrphanHotspots:   "Orphan hotspots",
		mdDBOrphans:        "Database orphans",
		mdWarnings:         "Warnings",
		mdOrphanRateHdr:    "Orphan rate",
		mdNextSteps:        "Recommended next steps",
		mdMissingList:      "Missing files (first 50)",
		mdMoreMissing:      "... %d more; the full list is in the --format json output",
		mdSteps: []string{
			"Zero risk first: `mage-mediagc cache clean --apply` drops the derived thumbnail cache, which Magento rebuilds on demand.",
			"Rehearse the isolation: `mage-mediagc quarantine` reports exactly what would move and refuses to proceed if the orphan ratio looks implausible.",
			"Then move them: `mage-mediagc quarantine --apply`. Files leave the media tree but are never deleted; `mage-mediagc restore --apply` puts them back.",
			"After a full traffic cycle with no visible breakage: `mage-mediagc purge --apply` frees the space. This step is permanent.",
			"Independently: back up the database, review `mage-mediagc db-clean` (dry run, nothing changes), then run `mage-mediagc db-clean --apply`.",
			"Finish: `mage-mediagc verify`, then `php bin/magento indexer:reindex && php bin/magento cache:flush`.",
		},
	},

	chinese: {
		mediaRoot:   "媒体目录",
		magentoRoot: "Magento 根",
		database:    "数据库",

		diskOriginals:    "磁盘原图",
		derivedCache:     "派生缓存",
		dbReferences:     "数据库引用",
		liveFiles:        "存活文件",
		orphanFiles:      "孤儿碎片",
		missingFiles:     "缺失文件",
		productCount:     "产品总数",
		referenceSources: "引用来源",
		orphanBuckets:    "孤儿集中目录",
		databaseOrphans:  "数据库孤儿",
		reclaimable:      "可回收空间",
		warnings:         "警告",
		skipped:          "已跳过",

		rowsUnit:  "行",
		pathsUnit: "路径",

		rowCountBytes:        "%s\t%d 个\t%s\n",
		rowCountOnly:         "%s\t%d 条\n",
		rowCountBytesPercent: "%s\t%d 个\t%s (%.1f%%)\n",
		rowRowsPaths:         "%s\t%d %s\t+%d %s\t%s\n",
		orphanRowsLine:       "孤儿行合计\t%d / %d\n",
		reclaimDetail:        "缓存 %s + 碎片 %s = %s",

		mdTitle:            "媒体碎片分析报告",
		mdGeneratedAt:      "生成时间",
		mdToolVersion:      "工具版本",
		mdSummary:          "摘要",
		mdMetric:           "指标",
		mdCount:            "数量",
		mdSize:             "体积",
		mdOrphanRate:       "碎片率",
		mdMissing:          "缺失文件（DB 引用但磁盘无）",
		mdReclaimable:      "合计可回收",
		mdRefSources:       "引用来源明细",
		mdSource:           "来源",
		mdRowsScanned:      "扫描行数",
		mdPathsAdded:       "新增路径",
		mdNote:             "说明",
		mdSkippedPrefix:    "已跳过：",
		mdDir:              "目录",
		mdOrphansOverTotal: "孤儿 / 总数",
		mdOrphanBytes:      "孤儿体积",
		mdDBSummary:        "现存产品 **%d** 个；孤儿行合计 **%d** / %d。",
		mdTable:            "表",
		mdShare:            "占比",
		mdOrphanHotspots:   "孤儿集中目录",
		mdDBOrphans:        "数据库孤儿",
		mdWarnings:         "警告",
		mdOrphanRateHdr:    "孤儿率",
		mdNextSteps:        "建议的后续步骤",
		mdMissingList:      "缺失文件清单（前 50 条）",
		mdMoreMissing:      "... 其余 %d 条见 --format json 输出",
		mdSteps: []string{
			"先做零风险项：`mage-mediagc cache clean --apply` 清空派生缩略图缓存，Magento 会按需重建。",
			"先预演隔离：`mage-mediagc quarantine` 会报告将移动哪些文件；碎片率异常时会拒绝执行。",
			"确认后实际隔离：`mage-mediagc quarantine --apply`。文件只是移出媒体目录，并未删除；`mage-mediagc restore --apply` 可原样回滚。",
			"观察一个完整业务周期且无异常后：`mage-mediagc purge --apply` 才真正释放空间，此步不可逆。",
			"数据库侧独立进行：先备份数据库，核对 `mage-mediagc db-clean` 的统计，再执行 `mage-mediagc db-clean --apply`。",
			"收尾：`mage-mediagc verify`，然后执行 `php bin/magento indexer:reindex && php bin/magento cache:flush`。",
		},
	},
}

// messagesFor returns the message table for a language, falling back to
// English for anything unrecognized so a typo in a config file cannot produce
// a blank report.
func messagesFor(lang string) messages {
	if m, ok := messageTable[lang]; ok {
		return m
	}
	return messageTable[english]
}
