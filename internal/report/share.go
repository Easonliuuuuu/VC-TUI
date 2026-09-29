package report

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
)

const (
	// ProfileSizingSummary is the reduced profile for planning and licensing
	// conversations: capacity and configuration, no IPs, MACs, UUIDs, folders,
	// annotations or per-device inventory.
	ProfileSizingSummary = "sizing-summary"
	// ProfileFullInventory keeps every sheet and column the ordinary export
	// writes so pseudonymization can be applied to the complete inventory.
	ProfileFullInventory = "full-inventory"

	shareSheetName       = "vsfleetShare"
	shareFieldsSheetName = "vsfleetShareFields"

	// MinShareKeyBytes is the shortest pseudonymization key accepted.
	MinShareKeyBytes = 16
	tokenHexChars    = 12

	actionKeep        = "keep"
	actionPseudonym   = "pseudonymize"
	actionScrub       = "scrub"
	actionOmit        = "omit"
	actionGenerated   = "generated"
	linkageThisExport = "this export only"
	linkageAcross     = "across exports of the same estate that use this key"
)

// shareProfile declares which worksheets and columns a named profile writes.
// A nil column list for a sheet means every column; a profile with nil sheets
// includes every sheet the ordinary export would write.
type shareProfile struct {
	summary string
	sheets  []profileSheet
}

type profileSheet struct {
	name    string
	columns []string
}

var shareProfiles = map[string]shareProfile{
	ProfileSizingSummary: {
		summary: "capacity and configuration for sizing and licensing: VMs, disks, hosts, clusters, datastores, vCenter versions and coverage; no IPs, MACs, UUIDs, folders, annotations, snapshots or device detail",
		sheets: []profileSheet{
			{"vInfo", []string{"VM", "Powerstate", "Template", "CPUs", "Memory", "In Use MiB", "Datacenter", "Cluster", "Host", "OS according to the configuration file", "VM ID", "vsfleet Context"}},
			{"vDisk", []string{"VM", "Powerstate", "Template", "Disk", "Disk Key", "Capacity MiB", "Thin", "Disk Mode", "Sharing mode", "Path", "VM ID", "vsfleet Context"}},
			{"vSource", []string{"Name", "OS type", "API type", "API version", "Version", "Patch level", "Build", "Fullname", "Product name", "Product version", "Product line", "Vendor", "vsfleet Context"}},
			{"vCluster", []string{"Name", "NumHosts", "numEffectiveHosts", "TotalCpu", "NumCpuCores", "TotalMemory", "HA enabled", "DRS enabled", "Datacenter", "vsfleet Context"}},
			{"vHost", []string{"Host", "Datacenter", "Cluster", "in Maintenance Mode", "Speed", "# Cores", "CPU usage %", "# Memory", "Memory usage %", "# VMs total", "ESX Version", "Vendor", "Model", "vsfleet Context"}},
			{"vDatastore", []string{"Name", "Datacenter", "Type", "Capacity MiB", "In Use MiB", "Free MiB", "Free %", "Accessible", "Maintenance mode", "vsfleet Context"}},
			{coverageSheetName, nil},
		},
	},
	ProfileFullInventory: {
		summary: "every worksheet and column of the ordinary export, with the same sensitivity handling",
	},
}

// ShareProfileNames lists the profile names in sorted order.
func ShareProfileNames() []string {
	names := make([]string, 0, len(shareProfiles))
	for name := range shareProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ShareOptions selects a profile and how identifying values are handled.
type ShareOptions struct {
	Profile string
	// Pseudonymize replaces identifying values with keyed tokens. It needs Key.
	Pseudonymize bool
	// LinkExports makes tokens stable across exports of the same estate made
	// with the same key. Without it tokens are also bound to the run ID.
	LinkExports bool
	// Key is the operator-supplied secret. It is never written to any output.
	Key []byte
}

// SharePlanColumn is one included column and how it is handled.
type SharePlanColumn struct {
	Name        string      `json:"name"`
	Sensitivity Sensitivity `json:"sensitivity"`
	Action      string      `json:"action"`
}

// SharePlanSheet is one worksheet the export will contain.
type SharePlanSheet struct {
	Name    string            `json:"name"`
	Rows    int               `json:"rows"`
	Columns []SharePlanColumn `json:"columns"`
	// Omitted lists the columns of the ordinary export left out by the profile.
	Omitted []string `json:"omitted_columns,omitempty"`
}

// ShareGap is one coverage gap carried into the export.
type ShareGap struct {
	Context string `json:"context,omitempty"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
}

// SharePlan is what an export will contain. The preview prints it and the
// export writes exactly it.
type SharePlan struct {
	Profile      string           `json:"profile"`
	Summary      string           `json:"summary"`
	Pseudonymize bool             `json:"pseudonymized"`
	Linkage      string           `json:"pseudonym_linkage,omitempty"`
	RunID        int64            `json:"run_id"`
	RunStatus    string           `json:"run_status"`
	Partial      bool             `json:"partial"`
	Contexts     []string         `json:"contexts"`
	Gaps         []ShareGap       `json:"coverage_gaps,omitempty"`
	Sheets       []SharePlanSheet `json:"sheets"`
	OmittedSheet []string         `json:"omitted_sheets,omitempty"`
	Warnings     []string         `json:"warnings"`
	// SensitiveClasses counts included columns by sensitivity class.
	SensitiveClasses map[Sensitivity]int `json:"sensitive_column_classes"`
}

// preparedShare is a plan together with the projected worksheets and the
// rule behind every column, ready for transformation.
type preparedShare struct {
	plan   SharePlan
	sheets []sheet
	rules  [][]colRule
	fields [][]any
	data   assessment.ExportData
	opts   ShareOptions
}

// PlanShare builds the plan for a profile without needing the key, so a
// preview never handles secret material.
func PlanShare(data assessment.ExportData, healthReport health.Report, opts ShareOptions) (SharePlan, error) {
	p, err := prepareShare(data, healthReport, opts)
	if err != nil {
		return SharePlan{}, err
	}
	return p.plan, nil
}

func prepareShare(data assessment.ExportData, healthReport health.Report, opts ShareOptions) (*preparedShare, error) {
	profile, ok := shareProfiles[opts.Profile]
	if !ok {
		return nil, fmt.Errorf("unknown export profile %q (supported: %s)", opts.Profile, strings.Join(ShareProfileNames(), ", "))
	}
	if opts.LinkExports && !opts.Pseudonymize {
		return nil, fmt.Errorf("linking exports only applies with pseudonymization")
	}
	all, err := rvtoolsSheets(data, healthReport)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]sheet, len(all))
	for _, s := range all {
		byName[s.name] = s
	}
	selected := profile.sheets
	if selected == nil {
		for _, s := range all {
			selected = append(selected, profileSheet{name: s.name})
		}
	}
	p := &preparedShare{data: data, opts: opts}
	p.plan = SharePlan{Profile: opts.Profile, Summary: profile.summary, Pseudonymize: opts.Pseudonymize,
		RunID: data.Run.ID, RunStatus: string(data.Run.Status), SensitiveClasses: map[Sensitivity]int{}}
	if opts.Pseudonymize {
		p.plan.Linkage = linkageThisExport
		if opts.LinkExports {
			p.plan.Linkage = linkageAcross
		}
	}
	included := map[string]bool{}
	for _, want := range selected {
		src, ok := byName[want.name]
		if !ok {
			return nil, fmt.Errorf("profile %s names worksheet %s, which the export does not produce", opts.Profile, want.name)
		}
		included[want.name] = true
		indexes, err := columnIndexes(src, want)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", opts.Profile, err)
		}
		out, rules, ps, err := project(src, indexes, opts)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", opts.Profile, err)
		}
		for _, c := range ps.Columns {
			p.plan.SensitiveClasses[c.Sensitivity]++
		}
		p.sheets = append(p.sheets, out)
		p.rules = append(p.rules, rules)
		p.plan.Sheets = append(p.plan.Sheets, ps)
	}
	for _, s := range all {
		if !included[s.name] {
			p.plan.OmittedSheet = append(p.plan.OmittedSheet, s.name)
		}
	}
	// Field manifest rows come from the same plan as the preview.
	for _, ps := range p.plan.Sheets {
		kept := map[string]SharePlanColumn{}
		for _, c := range ps.Columns {
			kept[c.Name] = c
		}
		for _, h := range byName[ps.Name].headers {
			if c, ok := kept[h]; ok {
				p.fields = append(p.fields, []any{ps.Name, h, string(c.Sensitivity), c.Action})
				continue
			}
			rule, _ := ruleFor(ps.Name, h)
			p.fields = append(p.fields, []any{ps.Name, h, string(rule.class), actionOmit})
		}
	}
	for _, name := range p.plan.OmittedSheet {
		p.fields = append(p.fields, []any{name, "*", "", actionOmit})
	}
	for _, c := range data.Contexts {
		p.plan.Contexts = append(p.plan.Contexts, c.Name)
	}
	p.plan.Gaps = shareGaps(data)
	p.plan.Partial = len(p.plan.Gaps) > 0
	p.plan.Warnings = shareWarnings(p.plan)
	// Generated sheets are part of the plan so the preview lists the whole
	// workbook. Their rows do not depend on the key.
	share := p.shareRows(func(_, v string) string { return v })
	p.plan.Sheets = append(p.plan.Sheets,
		SharePlanSheet{Name: shareSheetName, Rows: len(share), Columns: generatedColumns("Field", "Value")},
		SharePlanSheet{Name: shareFieldsSheetName, Rows: len(p.fields), Columns: generatedColumns("Sheet", "Column", "Sensitivity", "Action")})
	return p, nil
}

func generatedColumns(names ...string) []SharePlanColumn {
	cols := make([]SharePlanColumn, len(names))
	for i, n := range names {
		cols[i] = SharePlanColumn{Name: n, Sensitivity: ClassNone, Action: actionGenerated}
	}
	return cols
}

func columnIndexes(src sheet, want profileSheet) ([]int, error) {
	if want.columns == nil {
		idx := make([]int, len(src.headers))
		for i := range idx {
			idx[i] = i
		}
		return idx, nil
	}
	pos := make(map[string]int, len(src.headers))
	for i, h := range src.headers {
		pos[h] = i
	}
	seen := map[string]bool{}
	idx := make([]int, 0, len(want.columns))
	for _, c := range want.columns {
		i, ok := pos[c]
		if !ok {
			return nil, fmt.Errorf("worksheet %s has no column %q", src.name, c)
		}
		if seen[c] {
			return nil, fmt.Errorf("worksheet %s lists column %q twice", src.name, c)
		}
		seen[c] = true
		idx = append(idx, i)
	}
	return idx, nil
}

// project keeps the listed columns of src, resolving a rule for each. A
// column without a rule is an error rather than a guess.
func project(src sheet, indexes []int, opts ShareOptions) (sheet, []colRule, SharePlanSheet, error) {
	out := sheet{name: src.name}
	ps := SharePlanSheet{Name: src.name, Rows: len(src.rows)}
	remap := make(map[int]int, len(indexes))
	rules := make([]colRule, len(indexes))
	keptSet := map[int]bool{}
	for n, i := range indexes {
		h := src.headers[i]
		rule, ok := ruleFor(src.name, h)
		if !ok {
			return sheet{}, nil, ps, fmt.Errorf("worksheet %s column %q has no sensitivity rule", src.name, h)
		}
		rules[n] = rule
		remap[i] = n
		keptSet[i] = true
		out.headers = append(out.headers, h)
		action := actionKeep
		if opts.Pseudonymize {
			switch {
			case rule.scrub:
				action = actionScrub
			case rule.kind != "":
				action = actionPseudonym
			}
		}
		ps.Columns = append(ps.Columns, SharePlanColumn{Name: h, Sensitivity: rule.class, Action: action})
	}
	for i, h := range src.headers {
		if !keptSet[i] {
			ps.Omitted = append(ps.Omitted, h)
		}
	}
	remapCols := func(in []int) []int {
		var res []int
		for _, c := range in {
			if n, ok := remap[c]; ok {
				res = append(res, n)
			}
		}
		return res
	}
	out.dateCols, out.textCols, out.countCols = remapCols(src.dateCols), remapCols(src.textCols), remapCols(src.countCols)
	out.rows = make([][]any, len(src.rows))
	for r, row := range src.rows {
		next := make([]any, len(indexes))
		for n, i := range indexes {
			if i < len(row) {
				next[n] = row[i]
			}
		}
		out.rows[r] = next
	}
	return out, rules, ps, nil
}

func shareGaps(data assessment.ExportData) []ShareGap {
	var gaps []ShareGap
	if data.Run.Status != assessment.RunComplete {
		gaps = append(gaps, ShareGap{Kind: "run", Status: string(data.Run.Status)})
	}
	for _, c := range data.Contexts {
		if c.VMStatus != "success" && c.VMStatus != "empty" {
			gaps = append(gaps, ShareGap{Context: c.Name, Kind: "vm", Status: c.VMStatus})
		}
		for _, col := range c.Collections {
			if col.Status != "success" && col.Status != "empty" {
				gaps = append(gaps, ShareGap{Context: c.Name, Kind: col.Kind, Status: col.Status})
			}
		}
	}
	return gaps
}

func shareWarnings(plan SharePlan) []string {
	var w []string
	if plan.Pseudonymize {
		w = append(w, "Pseudonymized, not anonymous: tokens hide original values but keep equality and relationships, so the recipient can still see how the estate is shaped. Anyone holding the key can confirm a guessed value by recomputing its token.")
	} else {
		w = append(w, "Not pseudonymized: values are exported as written. The profile only limits which worksheets and columns are included.")
	}
	var scrubbed, topology, ids []string
	for _, s := range plan.Sheets {
		for _, c := range s.Columns {
			label := s.Name + "/" + c.Name
			switch {
			case c.Action == actionGenerated:
			case c.Sensitivity == ClassFreeText && c.Action != actionPseudonym:
				scrubbed = append(scrubbed, label)
			case c.Sensitivity == ClassTopology:
				topology = append(topology, label)
			case c.Sensitivity == ClassID && c.Action == actionKeep:
				ids = append(ids, label)
			}
		}
	}
	if plan.Pseudonymize && len(scrubbed) > 0 {
		w = append(w, "Free text is retained in "+strings.Join(scrubbed, ", ")+"; known names and addresses are replaced in it on a best-effort basis, but other identifying wording can remain.")
	}
	if !plan.Pseudonymize && len(scrubbed) > 0 {
		w = append(w, "Free text is exported as written in "+strings.Join(scrubbed, ", ")+".")
	}
	if len(topology) > 0 {
		w = append(w, "Network topology values are retained (VLAN, subnet, uplink, segment) in "+strings.Join(topology, ", ")+"; with counts and sizes they can still identify an environment.")
	}
	if len(ids) > 0 {
		w = append(w, "Identifiers are exported as written in "+strings.Join(ids, ", ")+".")
	}
	w = append(w, "Object counts, capacities, versions and timestamps are retained and can identify an environment. This reduced workbook is not guaranteed to be accepted by RVTools or any other importer.")
	if plan.Partial {
		w = append(w, "Partial assessment: run status "+plan.RunStatus+" with "+fmt.Sprint(len(plan.Gaps))+" coverage gap(s); absent rows do not mean absent objects. See "+coverageOrManifest(plan)+".")
	}
	if plan.Linkage == linkageAcross {
		w = append(w, "Pseudonyms are stable across exports made with this key, so a recipient holding several exports can correlate them.")
	}
	return w
}

func coverageOrManifest(plan SharePlan) string {
	for _, s := range plan.Sheets {
		if s.Name == coverageSheetName {
			return coverageSheetName + " and " + shareSheetName
		}
	}
	return shareSheetName
}

// shareRows renders the vsfleetShare key/value rows. tr maps a value to its
// exported form (identity for the plan, the pseudonymizer at write time).
func (p *preparedShare) shareRows(tr func(kind, value string) string) [][]any {
	plan := p.plan
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	rows := [][]any{
		{"Profile", plan.Profile},
		{"Profile summary", plan.Summary},
		{"Pseudonymized", yes(plan.Pseudonymize)},
	}
	if plan.Pseudonymize {
		rows = append(rows, []any{"Pseudonym method", "HMAC-SHA256 keyed by an operator-held secret, 48-bit tokens; the secret and any key identifier are not recorded"},
			[]any{"Pseudonym linkage", plan.Linkage})
	}
	rows = append(rows,
		[]any{"Run ID", fmt.Sprint(plan.RunID)},
		[]any{"Run status", plan.RunStatus},
		[]any{"Partial assessment", yes(plan.Partial)},
		[]any{"Source workbook", "reduced or transformed vsfleet export; not the ordinary RVTools-compatible export"},
	)
	for _, c := range plan.Contexts {
		rows = append(rows, []any{"Context", tr(kindContext, c)})
	}
	for _, g := range plan.Gaps {
		ctx := ""
		if g.Context != "" {
			ctx = tr(kindContext, g.Context) + " "
		}
		rows = append(rows, []any{"Coverage gap", ctx + g.Kind + ": " + g.Status})
	}
	for _, name := range plan.OmittedSheet {
		rows = append(rows, []any{"Omitted worksheet", name})
	}
	for _, w := range plan.Warnings {
		rows = append(rows, []any{"Warning", w})
	}
	return rows
}

// WriteShared writes a scoped sharing workbook. It fails closed: pseudonymizing
// without a usable key, or a column without a rule, is an error and nothing
// is written.
func WriteShared(w io.Writer, data assessment.ExportData, healthReport health.Report, opts ShareOptions) error {
	if opts.Pseudonymize && len(opts.Key) < MinShareKeyBytes {
		return fmt.Errorf("pseudonymization needs a key of at least %d bytes", MinShareKeyBytes)
	}
	p, err := prepareShare(data, healthReport, opts)
	if err != nil {
		return err
	}
	tr := func(_, v string) string { return v }
	sheets := p.sheets
	if opts.Pseudonymize {
		ps := newPseudonymizer(opts.Key, shareScope(data, opts))
		for i := range sheets {
			ps.transformSheet(&sheets[i], p.rules[i], false)
		}
		for i := range sheets {
			ps.transformSheet(&sheets[i], p.rules[i], true)
		}
		tr = func(kind, v string) string { return ps.token(kind, v) }
	}
	share := sheet{name: shareSheetName, headers: []string{"Field", "Value"}, rows: p.shareRows(tr), textCols: []int{0, 1}}
	fields := sheet{name: shareFieldsSheetName, headers: []string{"Sheet", "Column", "Sensitivity", "Action"}, rows: p.fields}
	sheets = append(sheets, share, fields)
	description := "Scoped export, profile " + opts.Profile + "; values are exported as written"
	if opts.Pseudonymize {
		description = "Scoped export, profile " + opts.Profile + "; pseudonymized, not anonymous"
	}
	return writeWorkbook(w, sheets, excelize.DocProperties{
		Title:       "vsfleet scoped export",
		Subject:     opts.Profile,
		Creator:     "vsfleet",
		Description: description,
		Created:     data.Run.StartedAt.UTC().Format(time.RFC3339),
		Modified:    data.Run.StartedAt.UTC().Format(time.RFC3339),
	})
}

func shareScope(data assessment.ExportData, opts ShareOptions) string {
	if opts.LinkExports {
		return "estate"
	}
	return fmt.Sprintf("run:%d", data.Run.ID)
}

type pseudonymizer struct {
	key   []byte
	scope string
	dict  map[string]string
}

func newPseudonymizer(key []byte, scope string) *pseudonymizer {
	return &pseudonymizer{key: append([]byte(nil), key...), scope: scope, dict: map[string]string{}}
}

// token is the deterministic pseudonym for value in the kind's namespace.
func (p *pseudonymizer) token(kind, value string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte("vsfleet-share-v1\x00"))
	m.Write([]byte(p.scope))
	m.Write([]byte{0})
	m.Write([]byte(kind))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return kind + "-" + hex.EncodeToString(m.Sum(nil))[:tokenHexChars]
}

func (p *pseudonymizer) remember(original, token string) {
	if len(original) >= 4 {
		p.dict[original] = token
	}
}

// transformSheet rewrites the sheet in place. scrubPass selects the second
// pass, which needs the dictionary the first pass collected.
func (p *pseudonymizer) transformSheet(s *sheet, rules []colRule, scrubPass bool) {
	var replacer *strings.Replacer
	if scrubPass {
		replacer = p.replacer()
	}
	for _, row := range s.rows {
		for i, rule := range rules {
			switch {
			case scrubPass && rule.scrub:
				if v, ok := row[i].(string); ok && v != "" && !isStaticNote(v) {
					row[i] = replacer.Replace(v)
				}
			case !scrubPass && rule.kind != "":
				row[i] = p.cell(rule, row[i])
			}
		}
	}
}

func (p *pseudonymizer) replacer() *strings.Replacer {
	keys := make([]string, 0, len(p.dict))
	for k := range p.dict {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	pairs := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		pairs = append(pairs, k, p.dict[k])
	}
	return strings.NewReplacer(pairs...)
}

func isStaticNote(v string) bool {
	return v == fileInfoNotCapturedNote || v == fileInfoNoRowsNote
}

var listSeparator = regexp.MustCompile(`\s*[,;]\s*`)

func (p *pseudonymizer) cell(rule colRule, v any) any {
	var s string
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		s = t
	case int, int32, int64, uint, uint32, uint64:
		s = fmt.Sprint(t)
	default:
		return v
	}
	if s == "" || isStaticNote(s) {
		return v
	}
	if !rule.list {
		return p.value(rule.kind, s)
	}
	seps := listSeparator.FindAllStringIndex(s, -1)
	var b strings.Builder
	last := 0
	for _, sep := range seps {
		b.WriteString(p.value(rule.kind, s[last:sep[0]]))
		b.WriteString(s[sep[0]:sep[1]])
		last = sep[1]
	}
	b.WriteString(p.value(rule.kind, s[last:]))
	return b.String()
}

func (p *pseudonymizer) value(kind, s string) string {
	if s == "" {
		return ""
	}
	switch kind {
	case kindPath:
		return p.path(s)
	case kindEndpoint:
		return p.endpoint(s)
	case kindID, kindUUID, kindMAC, kindText:
		return p.token(kind, s)
	}
	t := p.token(kind, s)
	p.remember(s, t)
	return t
}

// endpoint pseudonymizes the host of a URL, keeping the scheme and port, or
// the whole value when it is not a URL.
func (p *pseudonymizer) endpoint(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		t := p.token(kindEndpoint, s)
		p.remember(s, t)
		return t
	}
	host := u.Hostname()
	ht := p.token(kindEndpoint, host)
	p.remember(host, ht)
	out := u.Scheme + "://" + ht
	if port := u.Port(); port != "" {
		out += ":" + port
	}
	if rest := u.EscapedPath(); rest != "" && rest != "/" {
		out += p.path(rest)
	} else if rest == "/" {
		out += "/"
	}
	p.remember(s, out)
	return out
}

var extPattern = regexp.MustCompile(`^[A-Za-z]{1,8}$`)

// path pseudonymizes a datastore path or file path segment by segment. The
// bracketed datastore uses the same token as the Datastore column, so those
// joins survive, and a short alphabetic file extension is kept.
func (p *pseudonymizer) path(s string) string {
	var b strings.Builder
	rest := s
	if strings.HasPrefix(rest, "[") {
		if end := strings.IndexByte(rest, ']'); end > 0 {
			name := rest[1:end]
			t := p.token(kindDatastore, name)
			p.remember(name, t)
			b.WriteString("[" + t + "]")
			rest = rest[end+1:]
			if strings.HasPrefix(rest, " ") {
				b.WriteByte(' ')
				rest = rest[1:]
			}
		}
	}
	start := 0
	flush := func(end int) {
		if seg := rest[start:end]; seg != "" {
			b.WriteString(p.segment(seg))
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == '\\' {
			flush(i)
			b.WriteByte(rest[i])
			start = i + 1
		}
	}
	flush(len(rest))
	return b.String()
}

func (p *pseudonymizer) segment(seg string) string {
	if i := strings.LastIndexByte(seg, '.'); i > 0 && extPattern.MatchString(seg[i+1:]) {
		return p.token(kindPath, seg[:i]) + seg[i:]
	}
	return p.token(kindPath, seg)
}
