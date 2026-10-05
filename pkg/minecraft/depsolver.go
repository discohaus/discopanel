package minecraft

import (
	"sort"
	"strconv"
	"strings"
)

type DepIssueKind string

const (
	DepMissing   DepIssueKind = "missing"
	DepVersion   DepIssueKind = "version"
	DepDuplicate DepIssueKind = "duplicate"
	DepBreaks    DepIssueKind = "breaks"
)

// One provable dependency violation among installed mods
type DepIssue struct {
	Kind      DepIssueKind
	ModID     string // Affected mod, dep owner or the duplicated id
	File      string // Jar carrying the affected mod
	DepID     string // Dep or conflict partner mod id
	Range     string // Declared version range, raw loader syntax
	Found     string // Version actually present, when relevant
	OtherFile string // Partner jar for duplicate and breaks
}

// Renders the issue as one plain-language sentence
func (i DepIssue) Describe() string {
	switch i.Kind {
	case DepMissing:
		if i.Range != "" {
			return i.ModID + " needs " + i.DepID + " " + i.Range + ", which is not installed"
		}
		return i.ModID + " needs " + i.DepID + ", which is not installed"
	case DepVersion:
		return i.ModID + " needs " + i.DepID + " " + i.Range + ", but " + i.Found + " is installed"
	case DepDuplicate:
		return i.File + " and " + i.OtherFile + " both provide " + i.ModID
	case DepBreaks:
		return i.ModID + " declares itself incompatible with " + i.DepID + " (" + i.OtherFile + ")"
	}
	return string(i.Kind) + " " + i.ModID
}

// Installed things that satisfy or clash with deps
type depIndex struct {
	providers map[string][]providerRef
	declared  map[string][]providerRef
}

type providerRef struct {
	file    string
	version string
}

// Finds provable dependency violations, uncertainty never reports
func SolveDeps(metas []ModJarMeta, dialects []string) []DepIssue {
	if len(dialects) == 0 {
		return nil
	}
	metas = activeMetas(metas, dialects)
	idx := buildDepIndex(metas)
	var issues []DepIssue

	issues = append(issues, duplicateIssues(idx)...)
	seen := make(map[string]bool)
	for i := range metas {
		for _, dep := range metas[i].Deps {
			// Platform-provided ids never convict
			if dep.ID == "" || dep.Side == "client" || dialectBuiltin(dep.Dialect, dep.ID) {
				continue
			}
			key := string(depKind(dep)) + "|" + dep.Owner + "|" + dep.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			if issue := checkDep(&metas[i], dep, idx); issue != nil {
				issues = append(issues, *issue)
			}
		}
	}
	return issues
}

// Filters each jar to the manifest the platform reads
func activeMetas(metas []ModJarMeta, dialects []string) []ModJarMeta {
	out := make([]ModJarMeta, 0, len(metas))
	for i := range metas {
		m := metas[i]
		dialect := jarDialect(&m, dialects)
		m.Mods = filterMods(m.Mods, dialect)
		m.Deps = filterDeps(m.Deps, dialect)
		out = append(out, m)
	}
	return out
}

// Picks the most native manifest the jar carries
func jarDialect(m *ModJarMeta, dialects []string) string {
	for _, d := range dialects {
		for i := range m.Mods {
			if m.Mods[i].Dialect == d {
				return d
			}
		}
		for i := range m.Deps {
			if m.Deps[i].Dialect == d {
				return d
			}
		}
	}
	return dialects[len(dialects)-1]
}

func filterMods(mods []ModInfo, dialect string) []ModInfo {
	var out []ModInfo
	for _, m := range mods {
		if m.Dialect == dialect || m.Dialect == "" {
			out = append(out, m)
		}
	}
	return out
}

func filterDeps(deps []ModDep, dialect string) []ModDep {
	var out []ModDep
	for _, d := range deps {
		if d.Dialect == dialect || d.Dialect == "" {
			out = append(out, d)
		}
	}
	return out
}

func depKind(dep ModDep) DepIssueKind {
	if dep.Breaks {
		return DepBreaks
	}
	return DepMissing
}

func buildDepIndex(metas []ModJarMeta) *depIndex {
	idx := &depIndex{
		providers: make(map[string][]providerRef),
		declared:  make(map[string][]providerRef),
	}
	for i := range metas {
		for _, mod := range metas[i].Mods {
			ref := providerRef{file: metas[i].FileName, version: mod.Version}
			idx.providers[mod.ID] = append(idx.providers[mod.ID], ref)
			if mod.Declared {
				idx.declared[mod.ID] = append(idx.declared[mod.ID], ref)
			}
		}
	}
	return idx
}

// Jars declaring the same mod id refuse coexistence
func duplicateIssues(idx *depIndex) []DepIssue {
	var issues []DepIssue
	ids := make([]string, 0, len(idx.declared))
	for id := range idx.declared {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		refs := idx.declared[id]
		files := make(map[string]bool)
		for _, r := range refs {
			if r.file != "" {
				files[r.file] = true
			}
		}
		if len(files) < 2 {
			continue
		}
		sort.Slice(refs, func(a, b int) bool {
			return CompareVersions(refs[a].version, refs[b].version) > 0
		})
		// A jar never duplicates itself across its own manifests
		other := ""
		for _, r := range refs[1:] {
			if r.file != "" && r.file != refs[0].file {
				other = r.file
				break
			}
		}
		if other == "" {
			continue
		}
		issues = append(issues, DepIssue{
			Kind:      DepDuplicate,
			ModID:     id,
			File:      refs[0].file,
			Found:     refs[0].version,
			OtherFile: other,
		})
	}
	return issues
}

func checkDep(meta *ModJarMeta, dep ModDep, idx *depIndex) *DepIssue {
	providers := idx.providers[dep.ID]

	if dep.Breaks {
		for _, p := range providers {
			// Unknown versions cannot prove a break
			if p.version == "" && dep.Range != "" {
				continue
			}
			if dep.Range == "" || VersionSatisfies(p.version, dep.Range, dep.Dialect) {
				return &DepIssue{
					Kind: DepBreaks, ModID: dep.Owner, File: meta.FileName,
					DepID: dep.ID, Range: dep.Range, Found: p.version, OtherFile: p.file,
				}
			}
		}
		return nil
	}

	if !dep.Mandatory {
		return nil
	}
	if len(providers) == 0 {
		return &DepIssue{
			Kind: DepMissing, ModID: dep.Owner, File: meta.FileName,
			DepID: dep.ID, Range: dep.Range,
		}
	}
	if dep.Range == "" {
		return nil
	}
	found := ""
	for _, p := range providers {
		// Unknown versions satisfy, only provable mismatches report
		if p.version == "" || VersionSatisfies(p.version, dep.Range, dep.Dialect) {
			return nil
		}
		found = p.version
	}
	return &DepIssue{
		Kind: DepVersion, ModID: dep.Owner, File: meta.FileName,
		DepID: dep.ID, Range: dep.Range, Found: found,
	}
}

// Reports whether a version satisfies a range, unparseable satisfies
func VersionSatisfies(version, rawRange, dialect string) bool {
	rawRange = strings.TrimSpace(rawRange)
	if version == "" || rawRange == "" || rawRange == "*" {
		return true
	}
	if strings.HasPrefix(rawRange, "[") || strings.HasPrefix(rawRange, "(") {
		return mavenSatisfies(version, rawRange)
	}
	// A bare maven version is a soft hint, anything satisfies
	if dialectMavenRanges(dialect) {
		return true
	}
	return semverSatisfies(version, rawRange)
}

// Maven ranges are unions of bracketed intervals
func mavenSatisfies(version, rawRange string) bool {
	intervals, ok := parseMavenIntervals(rawRange)
	if !ok {
		return true
	}
	for _, iv := range intervals {
		if iv.contains(version) {
			return true
		}
	}
	return false
}

type mavenInterval struct {
	low, high         string
	lowIncl, highIncl bool
}

func (iv mavenInterval) contains(version string) bool {
	if iv.low != "" {
		c := CompareMavenVersions(version, iv.low)
		if c < 0 || (c == 0 && !iv.lowIncl) {
			return false
		}
	}
	if iv.high != "" {
		c := CompareMavenVersions(version, iv.high)
		if c > 0 || (c == 0 && !iv.highIncl) {
			return false
		}
	}
	return true
}

// Orders versions like maven so forge range checks agree
func CompareMavenVersions(a, b string) int {
	as := versionSegments(strings.ToLower(a))
	bs := versionSegments(strings.ToLower(b))
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := "", ""
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aErr := strconv.Atoi(av)
		bn, bErr := strconv.Atoi(bv)
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aErr == nil && bv != "":
			// Numbers outrank every qualifier like maven
			return 1
		case bErr == nil && av != "":
			return -1
		case aErr == nil:
			if an != 0 {
				return 1
			}
		case bErr == nil:
			if bn != 0 {
				return -1
			}
		default:
			ar, aq := mavenRanks(av)
			br, bq := mavenRanks(bv)
			if ar != br {
				return sign(ar - br)
			}
			if c := strings.Compare(aq, bq); c != 0 {
				return c
			}
		}
	}
	return 0
}

func mavenRanks(q string) (int, string) {
	switch q {
	case "alpha", "a":
		return -5, ""
	case "beta", "b":
		return -4, ""
	case "milestone", "m":
		return -3, ""
	case "rc", "cr":
		return -2, ""
	case "snapshot":
		return -1, ""
	case "", "ga", "final", "release":
		return 0, ""
	case "sp":
		return 1, ""
	}
	// Unknown qualifiers compare lexically
	return 2, q
}

func parseMavenIntervals(s string) ([]mavenInterval, bool) {
	var intervals []mavenInterval
	for len(s) > 0 {
		s = strings.TrimLeft(s, ", ")
		if s == "" {
			break
		}
		if s[0] != '[' && s[0] != '(' {
			return nil, false
		}
		end := strings.IndexAny(s, ")]")
		if end < 0 {
			return nil, false
		}
		body := s[1:end]
		iv := mavenInterval{lowIncl: s[0] == '[', highIncl: s[end] == ']'}
		parts := strings.Split(body, ",")
		switch len(parts) {
		case 1:
			// [1.0] pins exactly one version
			iv.low, iv.high = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[0])
		case 2:
			iv.low, iv.high = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		default:
			return nil, false
		}
		intervals = append(intervals, iv)
		s = s[end+1:]
	}
	return intervals, len(intervals) > 0
}

// Fabric predicates OR on pipes, AND on spaces
func semverSatisfies(version, rawRange string) bool {
	for _, group := range strings.Split(rawRange, "||") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if semverGroupSatisfies(version, group) {
			return true
		}
	}
	return false
}

func semverGroupSatisfies(version, group string) bool {
	for _, pred := range strings.Fields(group) {
		ok, parsed := semverPredicate(version, pred)
		if !parsed {
			return true
		}
		if !ok {
			return false
		}
	}
	return true
}

// Evaluates one predicate, second result false when unparseable
func semverPredicate(version, pred string) (bool, bool) {
	switch {
	case pred == "*":
		return true, true
	case strings.HasPrefix(pred, ">="):
		return CompareVersions(version, pred[2:]) >= 0, true
	case strings.HasPrefix(pred, "<="):
		return CompareVersions(version, pred[2:]) <= 0, true
	case strings.HasPrefix(pred, ">"):
		return CompareVersions(version, pred[1:]) > 0, true
	case strings.HasPrefix(pred, "<"):
		return CompareVersions(version, pred[1:]) < 0, true
	case strings.HasPrefix(pred, "="):
		return CompareVersions(version, pred[1:]) == 0, true
	case strings.HasPrefix(pred, "^"):
		return caretSatisfies(version, pred[1:]), true
	case strings.HasPrefix(pred, "~"):
		return tildeSatisfies(version, pred[1:]), true
	case strings.ContainsAny(pred, "xX*"):
		return wildcardSatisfies(version, pred), true
	case strings.ContainsAny(pred, "0123456789"):
		return CompareVersions(version, pred) == 0, true
	}
	return true, false
}

// Same major, at least the given version
func caretSatisfies(version, base string) bool {
	if CompareVersions(version, base) < 0 {
		return false
	}
	vSeg, bSeg := versionSegments(version), versionSegments(base)
	return len(vSeg) > 0 && len(bSeg) > 0 && vSeg[0] == bSeg[0]
}

// Same major.minor, at least the given version
func tildeSatisfies(version, base string) bool {
	if CompareVersions(version, base) < 0 {
		return false
	}
	vSeg, bSeg := versionSegments(version), versionSegments(base)
	if len(bSeg) < 2 {
		return len(vSeg) > 0 && len(bSeg) > 0 && vSeg[0] == bSeg[0]
	}
	return len(vSeg) > 1 && vSeg[0] == bSeg[0] && vSeg[1] == bSeg[1]
}

// Numeric segments must match until the wildcard position
func wildcardSatisfies(version, pattern string) bool {
	vSeg := versionSegments(version)
	for i, p := range versionSegments(pattern) {
		if p == "x" || p == "X" || p == "*" {
			return true
		}
		if i >= len(vSeg) || vSeg[i] != p {
			return false
		}
	}
	return true
}

// Lenient version ordering, numeric segments beat lexical ones
func CompareVersions(a, b string) int {
	aMain, aPre := splitPre(a)
	bMain, bPre := splitPre(b)
	if c := compareSegments(versionSegments(aMain), versionSegments(bMain)); c != 0 {
		return c
	}
	// A release outranks its own pre-releases
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	}
	return compareSegments(versionSegments(aPre), versionSegments(bPre))
}

func splitPre(v string) (string, string) {
	if idx := strings.IndexByte(v, '+'); idx >= 0 {
		v = v[:idx]
	}
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

// Splits like maven, letter suffixes become their own segment
func versionSegments(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	var out []string
	for _, f := range fields {
		start := 0
		for i := 1; i < len(f); i++ {
			if isDigit(f[i]) != isDigit(f[i-1]) {
				out = append(out, f[start:i])
				start = i
			}
		}
		out = append(out, f[start:])
	}
	return out
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func compareSegments(a, b []string) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := "", ""
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		an, aErr := strconv.Atoi(av)
		bn, bErr := strconv.Atoi(bv)
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aErr == nil:
			// Numbers outrank letters, missing loses to either
			if bv == "" {
				if an != 0 {
					return 1
				}
				continue
			}
			return 1
		case bErr == nil:
			if av == "" {
				if bn != 0 {
					return -1
				}
				continue
			}
			return -1
		default:
			if av == "" {
				return -1
			}
			if bv == "" {
				return 1
			}
			if c := strings.Compare(strings.ToLower(av), strings.ToLower(bv)); c != 0 {
				return c
			}
		}
	}
	return 0
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
