package archive

import (
	"regexp"
	"sort"
	"strings"
)

// A group without a clear key term is titled from what its members say stops
// them: what does it, the kind of stop, and what it stops. "The same
// restriction prevented creating the local HTML report" and "That constraint
// also prevented the HTML report" share a blocker and an object, so the item
// is "Restriction blocks local HTML report (pharos)" rather than either
// sentence.

// fixesObstacle is one member's blocker, kind of stop and object.
type fixesObstacle struct{ kind, subject, object string }

var (
	fixesObstacleVerb = regexp.MustCompile(`(?i)^(?:lacks?|lacked|lacking|prevent(?:s|ed)?|block(?:s|ed)?|denie[sd]|deny|reject(?:s|ed)?|refuse[sd]?|renamed)$`)
	fixesClauseCut    = regexp.MustCompile(`(?i)\s*(?:[,;:—–()]|\s-\s|\bso\b|\bbecause\b|\bwhich\b|\bwhile\b|\bbut\b|\bwith\b)\s*`)
	// Words around a blocker or object that say nothing about it.
	fixesSubjectTrail = fixesWordSet(`also already got has have had was were is are been may might probably all still again just now`)
	fixesSubjectLead  = fixesWordSet(`the a an this that these those same our my its their some several all it there one`)
	fixesObjectLead   = fixesWordSet(`me us them it him her from the a an its their our my his creation creating writing making running using of to`)
	fixesObjectStop   = fixesWordSet(`to for in on at with when and or by so because as than that which this these here there instead`)
	fixesPronoun      = fixesWordSet(`i we you i've i'd i'm we've`)
)

// fixesObstacleOf reads the first clause of a sentence that names a stop.
func fixesObstacleOf(sentence string) (fixesObstacle, bool) {
	for _, clause := range fixesClauseCut.Split(sentence, -1) {
		words := strings.Fields(clause)
		for i, word := range words {
			verb := strings.ToLower(strings.Trim(word, ".,!?\"“”'`*"))
			if !fixesObstacleVerb.MatchString(verb) {
				continue
			}
			kind := "block"
			switch {
			case strings.HasPrefix(verb, "lack"):
				kind = "lack"
			case verb == "renamed":
				kind = "rename"
			}
			subject := fixesSubjectWords(words[:i])
			after := fixesCleanWords(words[i+1:])
			if kind == "rename" {
				// "renamed by Conductor" names who did it; the thing renamed
				// is the branch, whichever way round the sentence goes.
				actor := ""
				for j, w := range after {
					if strings.EqualFold(w, "by") && j+1 < len(after) {
						actor = after[j+1]
						break
					}
				}
				if actor == "" && len(subject) > 0 && !strings.Contains(strings.ToLower(strings.Join(subject, " ")), "branch") {
					actor = subject[len(subject)-1]
				}
				// "It renamed…" and "renamed by someone" don't say who.
				if lower := strings.ToLower(actor); fixesSubjectLead[lower] || fixesPronoun[lower] || lower == "someone" || lower == "something" {
					actor = ""
				}
				object := ""
				for _, noun := range []string{"branch", "worktree", "workspace", "directory"} {
					if strings.Contains(strings.ToLower(sentence), noun) {
						object = noun
						break
					}
				}
				return fixesObstacle{kind: kind, subject: actor, object: object}, object != ""
			}
			object := fixesObjectWords(after)
			if object == "" {
				continue
			}
			return fixesObstacle{kind: kind, subject: strings.Join(subject, " "), object: object}, true
		}
	}
	return fixesObstacle{}, false
}

func fixesCleanWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, word := range words {
		if word = strings.Trim(word, ".,!?\"“”*"); word != "" {
			out = append(out, word)
		}
	}
	return out
}

// fixesSubjectWords are the last three words before the stop, without
// "also", articles or a trailing "I inspected".
func fixesSubjectWords(words []string) []string {
	words = fixesCleanWords(words)
	for i, word := range words {
		if fixesPronoun[strings.ToLower(word)] {
			words = words[:i]
			break
		}
	}
	for len(words) > 0 && fixesSubjectTrail[strings.ToLower(words[len(words)-1])] {
		words = words[:len(words)-1]
	}
	if len(words) > 3 {
		words = words[len(words)-3:]
	}
	for len(words) > 0 && fixesSubjectLead[strings.ToLower(words[0])] {
		words = words[1:]
	}
	return words
}

// fixesObjectWords are up to five words after the stop, from its first noun
// to the next preposition: "me from creating the local HTML report" is
// "local HTML report".
func fixesObjectWords(words []string) string {
	for {
		switch {
		case len(words) > 0 && fixesObjectLead[strings.ToLower(words[0])]:
			words = words[1:]
			continue
		case len(words) > 1 && strings.EqualFold(words[1], "from"):
			words = words[2:]
			continue
		}
		break
	}
	out := []string{}
	for _, word := range words {
		if len(out) > 0 && fixesObjectStop[strings.ToLower(word)] || len(out) == 5 {
			break
		}
		out = append(out, word)
	}
	return strings.Join(out, " ")
}

// fixesObstacleTitle titles an item from the stop most of its members name,
// or says nothing when they don't agree.
func fixesObstacleTitle(item *fixItem, place string) string {
	byKind := map[string][]fixesObstacle{}
	for _, e := range item.events {
		if o, ok := fixesObstacleOf(e.Sentence); ok {
			byKind[o.kind] = append(byKind[o.kind], o)
		}
	}
	kind, best := "", 0
	for _, k := range []string{"block", "lack", "rename"} {
		if len(byKind[k]) > best {
			kind, best = k, len(byKind[k])
		}
	}
	if best < 2 || float64(best) < 0.4*float64(len(item.events)) {
		return ""
	}
	found := byKind[kind]
	subjects, objects := []string{}, []string{}
	for _, o := range found {
		subjects = append(subjects, o.subject)
		objects = append(objects, o.object)
	}
	object, objectShare := fixesMostSaid(objects)
	if objectShare < 0.3 || objectShare*float64(len(objects)) < 2 {
		return ""
	}
	subject, subjectShare := fixesMostSaid(subjects)
	if subjectShare < 0.4 {
		subject = ""
		if head := fixesMostHead(subjects); head != "" {
			subject = "some " + head
		}
	}
	suffix := " (" + place + ")"
	switch {
	case kind == "lack" && subject == "":
		return fixesCapital("Agents' tools lack " + object + suffix)
	case kind == "lack" && !strings.HasSuffix(strings.ToLower(subject), "s"):
		return fixesCapital(subject + " lacks " + object + suffix)
	case kind == "lack":
		return fixesCapital(subject + " lack " + object + suffix)
	case kind == "rename" && subject == "":
		return fixesCapital("Something renames the " + object + " under the agent" + suffix)
	case kind == "rename":
		return fixesCapital(subject + " renames the " + object + " under the agent" + suffix)
	case subject == "":
		return fixesCapital(object + " keeps getting blocked" + suffix)
	}
	return fixesCapital(subject + " blocks " + object + suffix)
}

// fixesCapital starts a title with a capital, leaving code as written.
func fixesCapital(text string) string {
	runes := []rune(text)
	if len(runes) > 0 && runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 'a' - 'A'
	}
	return string(runes)
}

// fixesMostSaid is the phrase most members use, ignoring case and hyphens,
// in its commonest spelling, with its share of all of them.
func fixesMostSaid(phrases []string) (string, float64) {
	counts, spellings := map[string]int{}, map[string]map[string]int{}
	for _, phrase := range phrases {
		if phrase == "" {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(phrase, "-", " "))
		counts[key]++
		if spellings[key] == nil {
			spellings[key] = map[string]int{}
		}
		spellings[key][phrase]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return "", 0
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	key := keys[0]
	spelling, n := "", 0
	for s, c := range spellings[key] {
		if c > n || c == n && s < spelling {
			spelling, n = s, c
		}
	}
	return spelling, float64(counts[key]) / float64(len(phrases))
}

// fixesMostHead is the last word most blockers share ("parts" of "curved
// parts" and "lattice fence parts"), when two or more do.
func fixesMostHead(phrases []string) string {
	heads := []string{}
	for _, phrase := range phrases {
		if words := strings.Fields(phrase); len(words) > 0 {
			heads = append(heads, strings.ToLower(words[len(words)-1]))
		}
	}
	head, share := fixesMostSaid(heads)
	if head == "" || share*float64(len(heads)) < 2 {
		return ""
	}
	return head
}
