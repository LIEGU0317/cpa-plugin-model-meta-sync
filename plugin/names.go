package main

import "strings"

// Name matching follows the scheme used by dsh-model-metadata: a model is
// looked up by its bare name first, then under a separator-free spelling, then
// with the decoration suffixes a catalog entry may carry. Every relaxation can
// only produce a match when it is unambiguous inside one provider's model list;
// a key claimed by two different upstream ids is dropped instead of guessed.
//
// This is what lets the plugin work against any gateway without being told
// which vendor naming style it uses.

// minNormalizedName is the shortest folded spelling still treated as a name
// rather than a coincidence.
const minNormalizedName = 4

// nameSuffixes are trailing decorations an upstream entry may carry while the
// configured name lacks them (`-exp`, `-preview`, ...). The direction is
// deliberately one-way: a configured name carrying one of these is a different
// endpoint as far as this plugin is concerned.
var nameSuffixes = []string{"exp", "experimental", "latest", "preview", "free"}

// deploymentQualifiers may be carried by the configured name but never by the
// upstream spelling. They mark the same model published twice by a gateway.
var deploymentQualifiers = []string{"self", "official"}

// bareName is everything after the last "/" in a model id.
func bareName(id string) string {
	id = strings.TrimSpace(id)
	if at := strings.LastIndex(id, "/"); at >= 0 {
		return id[at+1:]
	}
	return id
}

// normalizeName folds case and every separator away: "V4.1-Flash" and
// "v41_flash" both become "v41flash".
func normalizeName(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizedKeys returns the folded spellings a name may be found under.
func normalizedKeys(name string) []string {
	norm := normalizeName(name)
	if len(norm) < minNormalizedName {
		return nil
	}
	keys := make([]string, 0, 1+len(nameSuffixes))
	keys = append(keys, norm)
	for _, suffix := range nameSuffixes {
		keys = append(keys, norm+suffix)
	}
	return keys
}

// deploymentKeys returns the spellings left after stripping trailing
// deployment qualifiers from the *configured* name. They are looked up only
// after every normalizedKeys attempt failed, so a genuine `-official` entry
// still wins over its unqualified sibling.
func deploymentKeys(name string) []string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	if len(words) < 2 {
		return nil
	}
	keys := make([]string, 0, 4)
	norm := normalizeName(name)
	stripped := 0
	for len(words)-stripped >= 2 {
		qualifier := words[len(words)-stripped-1]
		if !containsString(deploymentQualifiers, qualifier) {
			break
		}
		remaining := words[:len(words)-stripped-1]
		hasRealWord := false
		for _, word := range remaining {
			if !containsString(deploymentQualifiers, word) {
				hasRealWord = true
				break
			}
		}
		if !hasRealWord {
			break
		}
		stripped++
		if len(norm) < len(qualifier) {
			break
		}
		norm = strings.TrimSuffix(norm, qualifier)
		if len(norm) < minNormalizedName {
			break
		}
		keys = append(keys, norm)
		for _, suffix := range nameSuffixes {
			keys = append(keys, norm+suffix)
		}
	}
	return keys
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// upstreamIndex holds every lookup spelling for one provider's model list.
// exact keeps ids and bare names; folded keeps the relaxed spellings, with
// ambiguous spellings recorded so they can be refused.
type upstreamIndex struct {
	exact     map[string]upstreamModel
	exactID   map[string]string
	folded    map[string]upstreamModel
	ambiguous map[string]bool
}

func buildUpstreamIndex(models []upstreamModel) *upstreamIndex {
	index := &upstreamIndex{
		exact:     make(map[string]upstreamModel, len(models)*2),
		exactID:   make(map[string]string, len(models)*2),
		folded:    make(map[string]upstreamModel, len(models)*2),
		ambiguous: make(map[string]bool),
	}
	for _, model := range models {
		id := model.ID
		addExact(index, id, id, model)
		addExact(index, strings.ToLower(id), id, model)
		// A bare name is only usable while exactly one id ends in it.
		if bare := bareName(id); bare != id && bare != "" {
			addExact(index, bare, id, model)
			addExact(index, strings.ToLower(bare), id, model)
		}
		for _, key := range normalizedKeys(bareName(id)) {
			addFolded(index, key, id, model)
		}
	}
	return index
}

func addExact(index *upstreamIndex, key, id string, model upstreamModel) {
	if key == "" {
		return
	}
	if existing, ok := index.exactID[key]; ok {
		if existing == id {
			// Duplicate ids keep the first entry, matching the upstream order.
			return
		}
		delete(index.exact, key)
		delete(index.exactID, key)
		index.ambiguous[key] = true
		return
	}
	if index.ambiguous[key] {
		return
	}
	index.exact[key] = model
	index.exactID[key] = id
}

func addFolded(index *upstreamIndex, key, id string, model upstreamModel) {
	if key == "" || index.ambiguous[key] {
		return
	}
	if existing, ok := index.folded[key]; ok {
		if existing.ID == id {
			return
		}
		delete(index.folded, key)
		index.ambiguous[key] = true
		return
	}
	index.folded[key] = model
}

// lookup resolves a configured model name (and optional alias) against the
// provider's upstream list. Exact spellings win; the folded spellings are only
// consulted when no exact form matched, and a folded spelling shared by two
// upstream ids never matches.
func (index *upstreamIndex) lookup(name, alias string) (upstreamModel, bool) {
	ident := name
	if ident == "" {
		ident = alias
	}
	if ident == "" {
		return upstreamModel{}, false
	}
	for _, candidate := range []string{name, alias, ident} {
		if candidate == "" {
			continue
		}
		if model, ok := index.exact[candidate]; ok {
			return model, true
		}
		if model, ok := index.exact[strings.ToLower(candidate)]; ok {
			return model, true
		}
	}
	bare := bareName(ident)
	for _, candidate := range []string{bare, strings.ToLower(bare)} {
		if candidate == "" {
			continue
		}
		if model, ok := index.exact[candidate]; ok {
			return model, true
		}
	}
	for _, key := range append(normalizedKeys(bare), deploymentKeys(bare)...) {
		if index.ambiguous[key] {
			continue
		}
		if model, ok := index.folded[key]; ok {
			return model, true
		}
	}
	return upstreamModel{}, false
}
