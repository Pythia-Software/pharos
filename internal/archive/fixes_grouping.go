package archive

// Grouping for Optimize → Fixes: which agent events say the same recurring
// problem. Ported from the Agent Experience lab's grouping v3 (sparse TF-IDF
// leader clustering, then a centroid merge), with its knobs:
//
//   - Words are compared after the lexicon's cue phrases and discourse words
//     ("root cause", "turns out", "failing") are removed, since every event of
//     a kind shares them. Code-like words (identifiers, paths, flags, files)
//     count double, and paraphrases fold ("ten-minute", "10 minutes").
//   - A term is specific when few events use it (1%, at least 3), or when it
//     is a key term: its events are alike apart from the term itself (mean
//     pairwise cosine without it, shrunk n/(n+3), at least 0.05).
//   - An event joins a leader at cosine 0.45 when they share two specific
//     concepts, one of them a key term (a phrase and its words count once);
//     groups merge at 0.40 on their top terms the same way.
//   - A group must span two work items, contain its most common specific
//     term in half its members, and have members at 0.3 of its centroid.
//
// Repository is never an input: it only decides where a fix belongs.

import (
	"math"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"sync"
	"unicode"
)

var fixesStop = fixesWordSet(`a an the and or but if then else of to in on at by for with from into onto as is are was were be been being it its
this that these those there here i we you they he she me my our your their them us so not no yes do does did done doing
have has had having can could should would will shall may might must just also only very more most less least much many
some any all each every both either neither than too again still yet already now later soon ever never always often
about above below over under after before between through during without within while because since until unless
which who whom whose what when where why how let lets let's i'm i'll i've it's that's there's don't doesn't didn't isn't
aren't wasn't weren't won't can't couldn't shouldn't wouldn't one two first second next last new old same other another
need needs needed want wants make makes made use used using get gets got go goes going see seems seem look looks like
way thing things something anything nothing actually really probably maybe likely sure think thought know knew
check checking checked run running ran test tests file files code change changes work working`)

var fixesDiscourse = fixesWordSet(`root cause causes caused found find finds finding confirmed confirm confirms confirming identified
traced trace traces tracing turns turned issue issues problem problems bug bugs fix fixed fixes fixing failed fail fails
failing failure failures error errors pass passed passes passing works worked broken break breaks real actual actually
clear clearly same case cases explains explain explained reason reasons why means meaning instead correct correctly
wrong properly expected expect behavior behaviour result results happens happened happening rather already currently
doesn isn wasn didn remains remain remaining pending blocked unavailable missing couldn could cannot unable able seems appears likely
probably here there this that these those because since though although however still just also only`)

func fixesWordSet(words string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(words) {
		out[word] = true
	}
	return out
}

var (
	fixesToken      = regexp.MustCompile(`[a-z0-9][a-z0-9_./:-]*[a-z0-9]|[a-z0-9]`)
	fixesCodeToken  = regexp.MustCompile("`[^`]+`|[A-Za-z_]\\w*[A-Z_]\\w*|[\\w.-]+/[\\w./-]+|[\\w-]+\\.[a-z]{1,5}\\b|--?[a-z][\\w-]+")
	fixesCodeish    = regexp.MustCompile("`([^`]{2,60})`|((?:[\\w.-]+/)+[\\w.-]+|\\b[\\w-]+\\.(?:md|go|ts|tsx|js|py|json|toml|yaml|yml|sh|swift|sql|txt)\\b)")
	fixesNumberWord = regexp.MustCompile(`(?i)\b(?:zero|one|two|three|four|five|six|seven|eight|nine|ten)\b`)
	fixesDuration   = regexp.MustCompile(`(?i)\b(\d+)[\s-]?(second|minute|hour|sec|min|s|m|h)s?\b`)
	fixesHyphenated = regexp.MustCompile(`[A-Za-z]+(?:-[A-Za-z]+)+`)
	fixesNumbers    = map[string]string{"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10"}
)

// Grouping knobs (v3).
const (
	fixesJoin        = 0.45 // an event joins a leader at this cosine
	fixesMerge       = 0.40 // groups merge at this centroid cosine
	fixesMinShared   = 2    // specific concepts two events (or groups) must share
	fixesRareShare   = 0.01 // a term in at most this share of events is specific
	fixesKeyFloor    = 0.05 // a key term's events' mean similarity without it
	fixesKeyShrink   = 3.0  // few events make a high mean by chance: n/(n+3)
	fixesVocabulary  = 6000
	fixesMinWork     = 2   // a recurring group spans at least two work items
	fixesAnchorShare = 0.5 // its most common specific term is in half its members
	fixesCohesion    = 0.3 // its members' mean cosine to its centroid
)

type fixesCue struct {
	pattern  *regexp.Regexp
	literals []string
}

// fixesCues are the lexicon's cue phrases, longest first, so a cue's longer
// form wins. Spans like "expected .{1,60} but" would eat content and are left
// out. A cue runs only on text holding one of its literals.
var fixesCues = sync.OnceValue(func() []fixesCue {
	_, kinds := agentEventLexicon()
	bodies := []string{}
	seen := map[string]bool{}
	for _, kind := range kinds {
		for _, cue := range kind.Cues {
			if strings.Contains(cue.Text, ".{") || seen[cue.Text] {
				continue
			}
			seen[cue.Text] = true
			bodies = append(bodies, strings.TrimPrefix(cue.Text, "^"))
		}
	}
	sort.SliceStable(bodies, func(i, j int) bool { return len(bodies[i]) > len(bodies[j]) })
	out := []fixesCue{}
	for _, body := range bodies {
		pattern := agentEventCompileCue(body)
		parsed, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			continue
		}
		compiled, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			continue
		}
		out = append(out, fixesCue{compiled, agentEventLiterals(parsed)})
	}
	return out
})

func fixesClean(text string) string {
	lower := strings.ToLower(text)
	for _, cue := range fixesCues() {
		if cue.literals != nil && !fixesHasAny(lower, cue.literals) {
			continue
		}
		if cue.pattern.MatchString(text) {
			text = cue.pattern.ReplaceAllString(text, " ")
			lower = strings.ToLower(text)
		}
	}
	return text
}

func fixesHasAny(text string, literals []string) bool {
	for _, literal := range literals {
		if strings.Contains(text, literal) {
			return true
		}
	}
	return false
}

func fixesWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// fixesTerms are an event text's terms: content words, their bigrams, and
// code-like words a second time. codeWords are the words written as code.
type fixesTerms struct {
	terms, words, codeWords []string
}

func fixesTokens(text string) fixesTerms {
	text = fixesClean(text)
	text = fixesNumberWord.ReplaceAllStringFunc(text, func(word string) string { return fixesNumbers[strings.ToLower(word)] })
	text = fixesDuration.ReplaceAllStringFunc(text, func(match string) string {
		parts := fixesDuration.FindStringSubmatch(match)
		return parts[1] + strings.ToLower(parts[2])
	})
	// Plain hyphenated words split ("all-indexed"); paths, flags and branch
	// names keep their hyphens.
	var b strings.Builder
	last := 0
	for _, span := range fixesHyphenated.FindAllStringIndex(text, -1) {
		before, after := byte(' '), byte(' ')
		if span[0] > 0 {
			before = text[span[0]-1]
		}
		if span[1] < len(text) {
			after = text[span[1]]
		}
		if fixesWordByte(before) || strings.IndexByte("/.`-", before) >= 0 || fixesWordByte(after) || strings.IndexByte("/.`-", after) >= 0 {
			continue
		}
		b.WriteString(text[last:span[0]])
		b.WriteString(strings.ReplaceAll(text[span[0]:span[1]], "-", " "))
		last = span[1]
	}
	b.WriteString(text[last:])
	text = b.String()
	code := map[string]bool{}
	codeList := []string{}
	for _, token := range fixesCodeToken.FindAllString(text, -1) {
		token = strings.ToLower(strings.Trim(token, "`"))
		if !code[token] {
			code[token] = true
			codeList = append(codeList, token)
		}
	}
	words := []string{}
	for _, word := range fixesToken.FindAllString(strings.ToLower(text), -1) {
		if len(word) <= 2 || fixesStop[word] || fixesDiscourse[word] || fixesDigits(word) {
			continue
		}
		words = append(words, word)
	}
	out := fixesTerms{words: words}
	out.terms = append(out.terms, words...)
	for i := 1; i < len(words); i++ {
		out.terms = append(out.terms, words[i-1]+" "+words[i])
	}
	for _, word := range words {
		boosted := code[word]
		if !boosted && len(word) > 3 {
			for _, token := range codeList {
				if strings.Contains(token, word) {
					boosted = true
					break
				}
			}
		}
		if boosted {
			out.terms = append(out.terms, word)
			out.codeWords = append(out.codeWords, word)
		}
	}
	return out
}

func fixesDigits(word string) bool {
	for _, r := range word {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return word != ""
}

// fixesAnchors are code-like spans an event names: backticked text, paths and
// file names.
func fixesAnchors(text string) []string {
	out := []string{}
	for _, match := range fixesCodeish.FindAllStringSubmatch(text, -1) {
		value := strings.TrimSpace(match[1])
		if value == "" {
			value = strings.TrimSpace(match[2])
		}
		if value != "" && len(value) <= 60 {
			out = append(out, value)
		}
	}
	return out
}

// fixesVector is a unit TF-IDF vector, its terms in first-use order so ties
// sort the same way every time.
type fixesVector struct {
	terms   []string
	weights map[string]float64
}

func (v fixesVector) top(n int) []string {
	terms := append([]string{}, v.terms...)
	sort.SliceStable(terms, func(i, j int) bool { return v.weights[terms[i]] > v.weights[terms[j]] })
	if len(terms) > n {
		terms = terms[:n]
	}
	return terms
}

func (v fixesVector) dot(other fixesVector) float64 {
	a, b := v, other
	if len(a.terms) > len(b.terms) {
		a, b = b, a
	}
	sum := 0.0
	for _, term := range a.terms {
		sum += a.weights[term] * b.weights[term]
	}
	return sum
}

func fixesNormalize(terms []string, weights map[string]float64) fixesVector {
	norm := 0.0
	for _, term := range terms {
		norm += weights[term] * weights[term]
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		norm = 1
	}
	out := fixesVector{terms: terms, weights: make(map[string]float64, len(terms))}
	for _, term := range terms {
		out.weights[term] = weights[term] / norm
	}
	return out
}

// fixesSharedConcepts counts shared specific terms, a bigram and the words
// inside it once: "context deadline", "context" and "deadline" are one.
func fixesSharedConcepts(terms []string) int {
	covered := map[string]bool{}
	for _, term := range terms {
		if strings.Contains(term, " ") {
			for _, word := range strings.Fields(term) {
				covered[word] = true
			}
		}
	}
	n := 0
	for _, term := range terms {
		if !covered[term] {
			n++
		}
	}
	return n
}

func fixesSharesWord(a, b string) bool {
	if a == b {
		return true
	}
	for _, x := range strings.Fields(a) {
		for _, y := range strings.Fields(b) {
			if x == y {
				return true
			}
		}
	}
	return false
}

// fixesKeyTerms are terms whose events are alike apart from the term itself:
// the mean pairwise cosine of those events, with the term (and phrases
// sharing a word with it) left out, shrunk toward zero for few events. The
// mean pairwise cosine of unit vectors is (|sum|^2 - Σ|v|^2) / (n (n - 1)).
func fixesKeyTerms(vectors []fixesVector, df map[string]int) map[string]bool {
	postings := map[string][]int{}
	order := []string{}
	for i, v := range vectors {
		for _, term := range v.terms {
			if df[term] >= 3 {
				if postings[term] == nil {
					order = append(order, term)
				}
				postings[term] = append(postings[term], i)
			}
		}
	}
	keys := map[string]bool{}
	for _, term := range order {
		docs := postings[term]
		n := float64(len(docs))
		if n < 2 {
			continue
		}
		sum := map[string]float64{}
		selves := 0.0
		for _, i := range docs {
			v := vectors[i]
			for _, x := range v.terms {
				if fixesSharesWord(term, x) {
					continue
				}
				w := v.weights[x]
				sum[x] += w
				selves += w * w
			}
		}
		square := 0.0
		for _, w := range sum {
			square += w * w
		}
		if (square-selves)/(n*(n-1))*n/(n+fixesKeyShrink) >= fixesKeyFloor {
			keys[term] = true
		}
	}
	return keys
}

// fixesCluster is one recurring group before it becomes a fix: its members
// (indexes into the items), centroid and pass-1 parts.
type fixesCluster struct {
	members  []int
	centroid map[string]float64
	parts    []fixesPart
}

type fixesPart struct {
	leader  int
	members []int
	merged  float64 // the cosine it merged at; 0 for the head
}

type fixesClustering struct {
	vectors []fixesVector
	rare    map[string]bool
	groups  []fixesCluster
}

// fixesClusterTexts groups texts given most confident first.
func fixesClusterTexts(docs []fixesTerms) fixesClustering {
	n := len(docs)
	out := fixesClustering{rare: map[string]bool{}}
	if n == 0 {
		return out
	}
	df := map[string]int{}
	firstSeen := map[string]int{}
	for _, doc := range docs {
		seen := map[string]bool{}
		for _, term := range doc.terms {
			if !seen[term] {
				seen[term] = true
				if _, ok := firstSeen[term]; !ok {
					firstSeen[term] = len(firstSeen)
				}
				df[term]++
			}
		}
	}
	vocabulary := make([]string, 0, len(df))
	for term := range df {
		vocabulary = append(vocabulary, term)
	}
	sort.Slice(vocabulary, func(i, j int) bool {
		if df[vocabulary[i]] != df[vocabulary[j]] {
			return df[vocabulary[i]] > df[vocabulary[j]]
		}
		return firstSeen[vocabulary[i]] < firstSeen[vocabulary[j]]
	})
	keep := map[string]bool{}
	for i, term := range vocabulary {
		if i >= fixesVocabulary {
			break
		}
		if df[term] >= 2 {
			keep[term] = true
		}
	}
	for _, doc := range docs {
		counts := map[string]int{}
		terms := []string{}
		for _, term := range doc.terms {
			if !keep[term] {
				continue
			}
			if counts[term] == 0 {
				terms = append(terms, term)
			}
			counts[term]++
		}
		weights := map[string]float64{}
		for _, term := range terms {
			weights[term] = (1 + math.Log(float64(counts[term]))) * (math.Log(float64(1+n)/float64(1+df[term])) + 1)
		}
		out.vectors = append(out.vectors, fixesNormalize(terms, weights))
	}
	keys := fixesKeyTerms(out.vectors, df)
	limit := max(3, int(fixesRareShare*float64(n)))
	for term := range keep {
		if df[term] <= limit || keys[term] {
			out.rare[term] = true
		}
	}
	// Joining needs shared specific concepts, one of them a key term.
	count := func(terms []string) int {
		for _, term := range terms {
			if keys[term] {
				return fixesSharedConcepts(terms)
			}
		}
		return 0
	}
	// Pass 1: each event joins the most similar leader, or leads.
	postings := map[string][]int{}
	leaders := []fixesVector{}
	groups := [][]int{}
	for i, v := range out.vectors {
		best, bestSim := -1, fixesJoin
		if len(v.terms) > 0 {
			candidates := []int{}
			seen := map[int]bool{}
			for _, term := range v.top(8) {
				for _, g := range postings[term] {
					if !seen[g] {
						seen[g] = true
						candidates = append(candidates, g)
					}
				}
			}
			for _, g := range candidates {
				lv := leaders[g]
				shared := []string{}
				for _, term := range v.terms {
					if _, ok := lv.weights[term]; ok && out.rare[term] {
						shared = append(shared, term)
					}
				}
				if count(shared) < fixesMinShared {
					continue
				}
				if sim := v.dot(lv); sim >= bestSim {
					best, bestSim = g, sim
				}
			}
		}
		if best >= 0 {
			groups[best] = append(groups[best], i)
			continue
		}
		g := len(leaders)
		leaders = append(leaders, v)
		groups = append(groups, []int{i})
		for _, term := range v.terms {
			postings[term] = append(postings[term], g)
		}
	}
	// Pass 2: groups with similar centroids merge, the largest leading, so
	// one problem worded several ways becomes one group.
	centroids := make([]fixesVector, len(groups))
	for g, members := range groups {
		sum := map[string]float64{}
		terms := []string{}
		for _, i := range members {
			for _, term := range out.vectors[i].terms {
				if _, ok := sum[term]; !ok {
					terms = append(terms, term)
				}
				sum[term] += out.vectors[i].weights[term]
			}
		}
		centroids[g] = fixesNormalize(terms, sum)
	}
	order := make([]int, len(groups))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(groups[order[a]]) > len(groups[order[b]]) })
	type head struct {
		vector fixesVector
		top    map[string]bool
		parts  []fixesPart
	}
	heads := []*head{}
	headPostings := map[string][]int{}
	for _, g := range order {
		v := centroids[g]
		top := map[string]bool{}
		for _, term := range v.top(10) {
			top[term] = true
		}
		best, bestSim := -1, fixesMerge
		candidates := map[int]bool{}
		for _, term := range v.top(8) {
			for _, h := range headPostings[term] {
				candidates[h] = true
			}
		}
		ids := make([]int, 0, len(candidates))
		for h := range candidates {
			ids = append(ids, h)
		}
		sort.Ints(ids)
		for _, h := range ids {
			shared := []string{}
			for _, term := range v.top(10) {
				if heads[h].top[term] && out.rare[term] {
					shared = append(shared, term)
				}
			}
			if count(shared) < fixesMinShared {
				continue
			}
			if sim := v.dot(heads[h].vector); sim >= bestSim {
				best, bestSim = h, sim
			}
		}
		if best < 0 {
			best, bestSim = len(heads), 0
			heads = append(heads, &head{vector: v, top: top})
			for _, term := range v.top(20) {
				headPostings[term] = append(headPostings[term], best)
			}
		}
		heads[best].parts = append(heads[best].parts, fixesPart{leader: groups[g][0], members: groups[g], merged: bestSim})
	}
	for _, h := range heads {
		cluster := fixesCluster{centroid: map[string]float64{}, parts: h.parts}
		for _, part := range h.parts {
			cluster.members = append(cluster.members, part.members...)
		}
		sort.Ints(cluster.members)
		for _, i := range cluster.members {
			for term, w := range out.vectors[i].weights {
				cluster.centroid[term] += w
			}
		}
		out.groups = append(out.groups, cluster)
	}
	return out
}

// fixesCohesion is how tight a group is: its members' mean cosine to its
// centroid, and its most common specific term with the share of members
// that contain it.
func (clustering fixesClustering) cohesion(group fixesCluster) (mean float64, anchor string, share float64) {
	terms := make([]string, 0, len(group.centroid))
	for term := range group.centroid {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	centroid := fixesNormalize(terms, group.centroid)
	counts := map[string]int{}
	firstSeen := map[string]int{}
	for _, i := range group.members {
		v := clustering.vectors[i]
		mean += v.dot(centroid)
		for _, term := range v.terms {
			if clustering.rare[term] {
				if _, ok := firstSeen[term]; !ok {
					firstSeen[term] = len(firstSeen)
				}
				counts[term]++
			}
		}
	}
	if len(group.members) == 0 {
		return 0, "", 0
	}
	mean /= float64(len(group.members))
	best := 0
	for term, n := range counts {
		if n > best || n == best && firstSeen[term] < firstSeen[anchor] {
			anchor, best = term, n
		}
	}
	return mean, anchor, float64(best) / float64(len(group.members))
}

// fixesSimilarity is a member's cosine to its group's centroid.
func (clustering fixesClustering) similarity(group fixesCluster, i int) float64 {
	terms := make([]string, 0, len(group.centroid))
	for term := range group.centroid {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return clustering.vectors[i].dot(fixesNormalize(terms, group.centroid))
}
