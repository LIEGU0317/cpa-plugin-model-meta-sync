package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// upstream model representation (Magpie /v1/models and compatible gateways)
// ---------------------------------------------------------------------------

type upstreamModel struct {
	ID            string
	OwnedBy       string
	DisplayName   string
	ContextLength int64
	Input         []string
	Output        []string
	Reasoning     bool
	Levels        []string
	hasModalities bool
}

type upstreamList struct {
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
	LastID  string            `json:"last_id"`
}

func (u upstreamModel) valueFor(field string) any {
	switch field {
	case "display-name":
		if u.DisplayName == "" {
			return nil
		}
		return u.DisplayName
	case "max-context-length":
		if u.ContextLength <= 0 {
			return nil
		}
		return u.ContextLength
	case "input-modalities":
		if len(u.Input) == 0 {
			return nil
		}
		return append([]string(nil), u.Input...)
	case "output-modalities":
		if len(u.Output) == 0 {
			return nil
		}
		return append([]string(nil), u.Output...)
	case "thinking":
		if !u.Reasoning || len(u.Levels) == 0 {
			return nil
		}
		return append([]string(nil), u.Levels...)
	}
	return nil
}

func parseUpstreamModel(raw json.RawMessage) upstreamModel {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return upstreamModel{}
	}
	m := upstreamModel{}
	m.ID = jsonString(obj["id"])
	m.OwnedBy = jsonString(obj["owned_by"])
	m.DisplayName = jsonString(obj["display_name"])
	if v := jsonString(obj["display-name"]); v != "" && m.DisplayName == "" {
		m.DisplayName = v
	}
	m.ContextLength = jsonInt(obj["context_length"])
	if m.ContextLength <= 0 {
		m.ContextLength = jsonInt(obj["context_window"])
	}
	if m.ContextLength <= 0 {
		m.ContextLength = jsonInt(obj["max_input_tokens"])
	}
	if mods, ok := obj["modalities"]; ok {
		var shape struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		}
		if json.Unmarshal(mods, &shape) == nil {
			m.Input = cleanStrings(shape.Input)
			m.Output = cleanStrings(shape.Output)
			m.hasModalities = len(m.Input) > 0 || len(m.Output) > 0
		}
	}
	if v, ok := obj["reasoning"]; ok {
		var flag bool
		if json.Unmarshal(v, &flag) == nil {
			m.Reasoning = flag
		}
	}
	m.Levels = cleanStrings(jsonStrings(obj["reasoning_levels"]))
	if len(m.Levels) == 0 {
		var entries []struct {
			Effort string `json:"effort"`
		}
		if rawLevels, ok := obj["supported_reasoning_levels"]; ok {
			if json.Unmarshal(rawLevels, &entries) == nil {
				for _, entry := range entries {
					if v := strings.TrimSpace(entry.Effort); v != "" {
						m.Levels = append(m.Levels, v)
					}
				}
			}
		}
	}
	return m
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func jsonInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if v, err := n.Int64(); err == nil {
			return v
		}
	}
	return 0
}

func jsonStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) == nil {
		return out
	}
	return nil
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		v := strings.TrimSpace(item)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ---------------------------------------------------------------------------
// upstream fetch
// ---------------------------------------------------------------------------

func modelsURL(baseURL, modelsPath string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", errors.New("provider base-url is empty")
	}
	if _, err := url.Parse(base); err != nil {
		return "", fmt.Errorf("invalid provider base-url: %w", err)
	}
	path := modelsPath
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path, nil
}

func fetchUpstream(ctx context.Context, cfg Config, baseURL, apiKey string) ([]upstreamModel, error) {
	target, err := modelsURL(baseURL, cfg.UpstreamModelsPath)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: cfg.timeout()}
	out := make([]upstreamModel, 0, 64)
	seen := map[string]bool{}
	next := target
	for page := 0; page < 20; page++ {
		req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if errNew != nil {
			return nil, errNew
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "cpa-model-meta-sync/"+pluginVersion)
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, errDo := client.Do(req)
		if errDo != nil {
			return nil, fmt.Errorf("GET %s: %w", redactURL(next), errDo)
		}
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if errRead != nil {
			return nil, fmt.Errorf("read %s: %w", redactURL(next), errRead)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("GET %s: HTTP %d: %s", redactURL(next), resp.StatusCode, snippet(body))
		}
		var list upstreamList
		if errUnmarshal := json.Unmarshal(body, &list); errUnmarshal != nil {
			return nil, fmt.Errorf("decode %s: %w", redactURL(next), errUnmarshal)
		}
		for _, raw := range list.Data {
			model := parseUpstreamModel(raw)
			if model.ID == "" || seen[model.ID] {
				continue
			}
			seen[model.ID] = true
			out = append(out, model)
		}
		if !list.HasMore || strings.TrimSpace(list.LastID) == "" {
			break
		}
		parsed, errParse := url.Parse(target)
		if errParse != nil {
			break
		}
		query := parsed.Query()
		query.Set("after", list.LastID)
		parsed.RawQuery = query.Encode()
		next = parsed.String()
	}
	return out, nil
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		parsed.User = url.User("***")
	}
	return parsed.String()
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// yaml helpers
// ---------------------------------------------------------------------------

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func mapLookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalarValue(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func scalarSeq(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

func newScalar(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func newStringSeq(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		seq.Content = append(seq.Content, newScalar(value, "!!str"))
	}
	return seq
}

var modelKeyOrder = []string{
	"alias",
	"display-name",
	"force-mapping",
	"image",
	"input-modalities",
	"is-compat",
	"max-context-length",
	"name",
	"output-modalities",
	"thinking",
	"use-max-completion-tokens",
}

func keyRank(key string) int {
	for i, item := range modelKeyOrder {
		if item == key {
			return i
		}
	}
	return len(modelKeyOrder)
}

// setMapValue inserts or replaces key in a mapping node, keeping canonical order
// for newly inserted keys.
func setMapValue(m *yaml.Node, key string, value *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	insertAt := len(m.Content)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if keyRank(m.Content[i].Value) > keyRank(key) {
			insertAt = i
			break
		}
	}
	next := make([]*yaml.Node, 0, len(m.Content)+2)
	next = append(next, m.Content[:insertAt]...)
	next = append(next, newScalar(key, "!!str"), value)
	next = append(next, m.Content[insertAt:]...)
	m.Content = next
}

// ---------------------------------------------------------------------------
// config document
// ---------------------------------------------------------------------------

type providerRef struct {
	index  int
	name   string
	node   *yaml.Node
	models *yaml.Node
}

type configDocument struct {
	path string
	raw  []byte
	mode os.FileMode
	doc  *yaml.Node
	hash string
}

func loadConfigDocument(path string) (*configDocument, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return nil, fmt.Errorf("stat config: %w", errStat)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("read config: %w", errRead)
	}
	doc, errDocs := decodeSingleDocument(raw)
	if errDocs != nil {
		return nil, errDocs
	}
	sum := sha256.Sum256(raw)
	return &configDocument{
		path: path,
		raw:  raw,
		mode: info.Mode().Perm(),
		doc:  doc,
		hash: hex.EncodeToString(sum[:]),
	}, nil
}

// decodeSingleDocument rejects multi-document YAML: the writer would silently
// drop every document after the first.
func decodeSingleDocument(raw []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	documents := make([]*yaml.Node, 0, 2)
	for {
		var node yaml.Node
		errDecode := decoder.Decode(&node)
		if errors.Is(errDecode, io.EOF) {
			break
		}
		if errDecode != nil {
			return nil, fmt.Errorf("parse config: %w", errDecode)
		}
		copyNode := node
		documents = append(documents, &copyNode)
	}
	if len(documents) == 0 {
		return nil, errors.New("config file is empty")
	}
	if len(documents) > 1 {
		return nil, fmt.Errorf("config file contains %d YAML documents; refusing to rewrite multi-document configs", len(documents))
	}
	return documents[0], nil
}

func (c *configDocument) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(c.doc); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if errClose := enc.Close(); errClose != nil {
		return nil, fmt.Errorf("close encoder: %w", errClose)
	}
	return buf.Bytes(), nil
}

func (c *configDocument) providers() ([]providerRef, error) {
	root := documentRoot(c.doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, errors.New("config root is not a mapping")
	}
	var list *yaml.Node
	if apiKeys := mapLookup(root, "api-keys"); apiKeys != nil && apiKeys.Kind == yaml.MappingNode {
		list = mapLookup(apiKeys, "openai-compatibility")
	}
	if list == nil {
		list = mapLookup(root, "openai-compatibility")
	}
	if list == nil || list.Kind != yaml.SequenceNode {
		return []providerRef{}, nil
	}
	out := make([]providerRef, 0, len(list.Content))
	for itemIndex, item := range list.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		name := scalarValue(mapLookup(item, "name"))
		out = append(out, providerRef{index: itemIndex, name: name, node: item, models: mapLookup(item, "models")})
	}
	return out, nil
}

func providerKey(p *yaml.Node) string {
	if p == nil {
		return ""
	}
	if keys := mapLookup(p, "keys"); keys != nil && keys.Kind == yaml.SequenceNode {
		for _, entry := range keys.Content {
			if entry.Kind == yaml.MappingNode {
				if key := scalarValue(mapLookup(entry, "api-key")); key != "" {
					return key
				}
				continue
			}
			if key := scalarValue(entry); key != "" {
				return key
			}
		}
	}
	if entries := mapLookup(p, "api-key-entries"); entries != nil && entries.Kind == yaml.SequenceNode {
		for _, entry := range entries.Content {
			if entry.Kind != yaml.MappingNode {
				continue
			}
			if key := scalarValue(mapLookup(entry, "api-key")); key != "" {
				return key
			}
		}
	}
	return scalarValue(mapLookup(p, "api-key"))
}

// ---------------------------------------------------------------------------
// report types
// ---------------------------------------------------------------------------

type FieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type ModelChange struct {
	Name   string        `json:"name"`
	Alias  string        `json:"alias,omitempty"`
	Fields []FieldChange `json:"fields"`
}

type ProviderReport struct {
	Name        string        `json:"name"`
	BaseURL     string        `json:"base_url"`
	Upstream    int           `json:"upstream_models"`
	Configured  int           `json:"configured_models"`
	Matched     int           `json:"matched"`
	Unmatched   []string      `json:"unmatched,omitempty"`
	Changed     int           `json:"changed_models"`
	FieldCount  int           `json:"changed_fields"`
	Changes     []ModelChange `json:"changes,omitempty"`
	Skipped     string        `json:"skipped,omitempty"`
	UpstreamErr string        `json:"upstream_error,omitempty"`
}

type RunReport struct {
	OK               bool             `json:"ok"`
	DryRun           bool             `json:"dry_run"`
	StartedAt        string           `json:"started_at"`
	DurationMS       int64            `json:"duration_ms"`
	ConfigPath       string           `json:"config_path"`
	ConfigHashBefore string           `json:"config_hash_before,omitempty"`
	ConfigHashAfter  string           `json:"config_hash_after,omitempty"`
	Mode             string           `json:"mode"`
	Fields           []string         `json:"fields"`
	Providers        []ProviderReport `json:"providers"`
	ChangedFields    int              `json:"changed_fields"`
	Written          bool             `json:"written"`
	Backup           string           `json:"backup,omitempty"`
	Mismatch         []string         `json:"unexpected_changes,omitempty"`
	Warnings         []string         `json:"warnings,omitempty"`
	Error            string           `json:"error,omitempty"`
	Retries          int              `json:"retries,omitempty"`
}

// ---------------------------------------------------------------------------
// merge engine
// ---------------------------------------------------------------------------

type syncOptions struct {
	DryRun   bool
	Provider string
}

func (p *pluginState) runSync(ctx context.Context, opts syncOptions) (*RunReport, error) {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()

	started := time.Now().UTC()
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()

	if opts.Provider != "" {
		cfg.Providers = []string{opts.Provider}
	}
	cfg = cfg.withDefaults()
	report := &RunReport{
		DryRun:     opts.DryRun,
		StartedAt:  started.Format(time.RFC3339),
		ConfigPath: cfg.ConfigPath,
		Mode:       cfg.Mode,
		Fields:     append([]string(nil), cfg.Fields...),
	}

	doc, errLoad := loadConfigDocument(cfg.ConfigPath)
	if errLoad != nil {
		report.Error = errLoad.Error()
		return report, errLoad
	}
	report.ConfigHashBefore = doc.hash

	providers, errProviders := doc.providers()
	if errProviders != nil {
		report.Error = errProviders.Error()
		return report, errProviders
	}
	if len(providers) == 0 {
		report.Error = "no openai-compatibility providers found in config"
		return report, errors.New(report.Error)
	}

	changedTotal := 0
	selectedProviders := 0
	upstreamFailures := 0
	selectedIdx := map[int]bool{}
	for _, prov := range providers {
		if !cfg.wantsProvider(prov.name) {
			continue
		}
		selectedProviders++
		selectedIdx[prov.index] = true
		prep := ProviderReport{Name: prov.name, BaseURL: scalarValue(mapLookup(prov.node, "base-url"))}
		if isTruthy(mapLookup(prov.node, "disabled")) {
			prep.Skipped = "provider disabled"
			report.Providers = append(report.Providers, prep)
			continue
		}
		if prov.models == nil || prov.models.Kind != yaml.SequenceNode {
			prep.Skipped = "provider has no models list"
			report.Providers = append(report.Providers, prep)
			continue
		}
		if yamlAliasesPresent(providers) {
			prep.Skipped = "provider model lists use yaml anchors/aliases; skipped for safety"
			report.Providers = append(report.Providers, prep)
			report.Warnings = append(report.Warnings, "yaml anchors/aliases detected in provider model lists; sync skipped for every selected provider")
			continue
		}
		apiKey := cfg.APIKey
		if apiKey == "" {
			apiKey = providerKey(prov.node)
		}
		upstream, errFetch := fetchUpstream(ctx, cfg, prep.BaseURL, apiKey)
		if errFetch != nil {
			prep.UpstreamErr = errFetch.Error()
			upstreamFailures++
			report.Providers = append(report.Providers, prep)
			continue
		}
		prep.Upstream = len(upstream)
		prep.Configured = len(prov.models.Content)
		index := buildUpstreamIndex(upstream)
		for _, modelNode := range prov.models.Content {
			if modelNode.Kind != yaml.MappingNode {
				continue
			}
			name := scalarValue(mapLookup(modelNode, "name"))
			alias := scalarValue(mapLookup(modelNode, "alias"))
			ident := name
			if ident == "" {
				ident = alias
			}
			match, okMatch := lookupUpstream(index, ident, alias)
			if !okMatch {
				prep.Unmatched = append(prep.Unmatched, ident)
				continue
			}
			prep.Matched++
			changes := applyUpstreamFields(modelNode, match, cfg)
			if len(changes) == 0 {
				continue
			}
			prep.Changed++
			prep.FieldCount += len(changes)
			prep.Changes = append(prep.Changes, ModelChange{Name: ident, Alias: alias, Fields: changes})
			changedTotal += len(changes)
		}
		report.Providers = append(report.Providers, prep)
	}

	if selectedProviders == 0 {
		report.OK = false
		report.Error = "no matching provider found for configured names: " + strings.Join(cfg.Providers, ", ")
		report.DurationMS = time.Since(started).Milliseconds()
		p.storeReport(report)
		return report, errors.New(report.Error)
	}
	if upstreamFailures > 0 {
		report.OK = false
		report.Error = "upstream model list fetch failed; nothing written"
		report.ChangedFields = changedTotal
		report.DurationMS = time.Since(started).Milliseconds()
		p.storeReport(report)
		return report, errors.New(report.Error)
	}
	if changedTotal == 0 {
		report.OK = true
		report.ChangedFields = 0
		report.DurationMS = time.Since(started).Milliseconds()
		p.storeReport(report)
		return report, nil
	}

	newRaw, errEncode := doc.encode()
	if errEncode != nil {
		report.Error = errEncode.Error()
		return report, errEncode
	}
	if errValidate := validateSemanticDiff(doc.raw, newRaw, cfg, selectedIdx); errValidate != nil {
		report.Error = errValidate.Error()
		return report, errValidate
	}
	sum := sha256.Sum256(newRaw)
	report.ConfigHashAfter = hex.EncodeToString(sum[:])
	report.ChangedFields = changedTotal
	report.OK = true

	if opts.DryRun {
		report.DurationMS = time.Since(started).Milliseconds()
		p.storeReport(report)
		return report, nil
	}

	backup, errWrite := writeConfigAtomic(cfg, doc, newRaw)
	if errWrite != nil {
		report.OK = false
		report.Error = errWrite.Error()
		return report, errWrite
	}
	report.Written = true
	report.Backup = backup
	report.DurationMS = time.Since(started).Milliseconds()
	p.storeReport(report)
	return report, nil
}

func buildUpstreamIndex(models []upstreamModel) map[string]upstreamModel {
	index := make(map[string]upstreamModel, len(models)*2)
	for _, model := range models {
		if _, exists := index[model.ID]; !exists {
			index[model.ID] = model
		}
		lower := strings.ToLower(model.ID)
		if _, exists := index[lower]; !exists {
			index[lower] = model
		}
	}
	return index
}

func lookupUpstream(index map[string]upstreamModel, name, alias string) (upstreamModel, bool) {
	for _, candidate := range []string{name, alias} {
		if candidate == "" {
			continue
		}
		if model, ok := index[candidate]; ok {
			return model, true
		}
		if model, ok := index[strings.ToLower(candidate)]; ok {
			return model, true
		}
	}
	return upstreamModel{}, false
}

func applyUpstreamFields(model *yaml.Node, up upstreamModel, cfg Config) []FieldChange {
	changes := make([]FieldChange, 0, 4)
	for _, field := range cfg.Fields {
		target := up.valueFor(field)
		if target == nil {
			continue
		}
		switch field {
		case "display-name":
			current := mapLookup(model, "display-name")
			if current != nil && scalarValue(current) != "" && !cfg.overwrites("display-name") {
				continue
			}
			value := target.(string)
			if current != nil && scalarValue(current) == value {
				continue
			}
			changes = append(changes, FieldChange{Field: field, Before: scalarValue(current), After: value})
			setMapValue(model, "display-name", newScalar(value, "!!str"))
		case "max-context-length":
			current := mapLookup(model, "max-context-length")
			existing, _ := strconv.ParseInt(scalarValue(current), 10, 64)
			if existing > 0 && !cfg.overwrites("max-context-length") {
				continue
			}
			value := target.(int64)
			if existing == value {
				continue
			}
			changes = append(changes, FieldChange{Field: field, Before: existing, After: value})
			setMapValue(model, "max-context-length", newScalar(strconv.FormatInt(value, 10), "!!int"))
		case "input-modalities", "output-modalities":
			current := mapLookup(model, field)
			existing := scalarSeq(current)
			if len(existing) > 0 && !cfg.overwrites(field) {
				continue
			}
			value := target.([]string)
			if sameStrings(existing, value) {
				continue
			}
			changes = append(changes, FieldChange{Field: field, Before: existing, After: value})
			setMapValue(model, field, newStringSeq(value))
		case "thinking":
			current := mapLookup(model, "thinking")
			existingLevels := scalarSeq(mapLookup(current, "levels"))
			if current != nil && current.Kind == yaml.MappingNode && len(existingLevels) > 0 && !cfg.overwrites("thinking") {
				continue
			}
			if current != nil && current.Kind == yaml.ScalarNode && current.Tag == "!!null" {
				current = nil
			}
			value := target.([]string)
			if sameStrings(existingLevels, value) {
				continue
			}
			changes = append(changes, FieldChange{Field: "thinking.levels", Before: existingLevels, After: value})
			thinking := current
			if thinking == nil || thinking.Kind != yaml.MappingNode {
				thinking = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			setMapValue(thinking, "levels", newStringSeq(value))
			setMapValue(model, "thinking", thinking)
		}
	}
	return changes
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func validateSemanticDiff(oldRaw, newRaw []byte, cfg Config, allowedProviders map[int]bool) error {
	var oldMap, newMap map[string]any
	if err := yaml.Unmarshal(oldRaw, &oldMap); err != nil {
		return fmt.Errorf("re-parse original config: %w", err)
	}
	if err := yaml.Unmarshal(newRaw, &newMap); err != nil {
		return fmt.Errorf("encode produced invalid yaml: %w", err)
	}
	paths := make([]string, 0, 32)
	collectDiff("", oldMap, newMap, &paths, 0)
	allowed := map[string]bool{
		"display-name":       cfg.hasField("display-name"),
		"max-context-length": cfg.hasField("max-context-length"),
		"input-modalities":   cfg.hasField("input-modalities"),
		"output-modalities":  cfg.hasField("output-modalities"),
		"thinking":           cfg.hasField("thinking"),
	}
	unexpected := make([]string, 0)
	for _, path := range paths {
		if pathAllowed(path, allowed, allowedProviders) {
			continue
		}
		unexpected = append(unexpected, path)
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("refusing to write: unexpected config changes: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func pathAllowed(path string, allowed map[string]bool, allowedProviders map[int]bool) bool {
	parts := strings.Split(path, ".")
	if len(parts) < 5 {
		return false
	}
	// Legacy: openai-compatibility.<provider>.<models>.<model>.<field>[.<sub>]
	// v8:     api-keys.openai-compatibility.<provider>.<models>.<model>.<field>[.<sub>]
	base := 0
	if len(parts) >= 2 && parts[0] == "api-keys" && parts[1] == "openai-compatibility" {
		base = 2
	} else if parts[0] == "openai-compatibility" {
		base = 1
	} else {
		return false
	}
	if base+3 >= len(parts) {
		return false
	}
	providerIndex, errProvider := strconv.Atoi(parts[base])
	if errProvider != nil || !allowedProviders[providerIndex] {
		return false
	}
	if parts[base+1] != "models" {
		return false
	}
	if _, errModel := strconv.Atoi(parts[base+2]); errModel != nil {
		return false
	}
	field := parts[base+3]
	if !allowed[field] {
		return false
	}
	rest := parts[base+4:]
	if len(rest) == 0 {
		return true
	}
	if field != "thinking" {
		return false
	}
	if len(rest) == 1 && rest[0] == "levels" {
		return true
	}
	return len(rest) == 2 && rest[0] == "levels" && isInteger(rest[1])
}

// hasAliasModels reports whether another provider aliases this provider's models
// list, which would make a rewrite leak across provider boundaries.
// yamlAliasesPresent reports whether any provider model list relies on YAML
// anchors or aliases. Rewriting a shared node would leak changes across provider
// boundaries, so such configs are skipped wholesale.
func yamlAliasesPresent(providers []providerRef) bool {
	for _, provider := range providers {
		models := provider.models
		if models == nil {
			continue
		}
		if models.Kind == yaml.AliasNode || models.Anchor != "" {
			return true
		}
		if models.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range models.Content {
			if item.Kind == yaml.AliasNode || item.Anchor != "" {
				return true
			}
			if item.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(item.Content); i += 2 {
				if item.Content[i+1].Kind == yaml.AliasNode || item.Content[i+1].Anchor != "" {
					return true
				}
			}
		}
	}
	return false
}

// isTruthy interprets a YAML boolean the way YAML 1.1/1.2 parsers do.
func isTruthy(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Tag == "!!bool" {
		return node.Value == "true" || node.Value == "True" || node.Value == "TRUE"
	}
	switch node.Value {
	case "true", "True", "TRUE", "yes", "Yes", "YES", "on", "On", "ON":
		return true
	}
	return false
}

func collectDiff(prefix string, oldVal, newVal any, out *[]string, depth int) {
	if depth > 12 {
		*out = append(*out, prefix)
		return
	}
	switch oldTyped := oldVal.(type) {
	case map[string]any:
		newTyped, ok := newVal.(map[string]any)
		if !ok {
			*out = append(*out, prefix)
			return
		}
		keys := make([]string, 0, len(oldTyped)+len(newTyped))
		seen := map[string]bool{}
		for key := range oldTyped {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range newTyped {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			oldChild, okOld := oldTyped[key]
			newChild, okNew := newTyped[key]
			if !okOld || !okNew {
				*out = append(*out, joinPath(prefix, key))
				continue
			}
			collectDiff(joinPath(prefix, key), oldChild, newChild, out, depth+1)
		}
	case []any:
		newTyped, ok := newVal.([]any)
		if !ok || len(newTyped) != len(oldTyped) {
			*out = append(*out, prefix)
			return
		}
		for i := range oldTyped {
			collectDiff(prefix+"."+strconv.Itoa(i), oldTyped[i], newTyped[i], out, depth+1)
		}
	default:
		if !sameScalar(oldVal, newVal) {
			*out = append(*out, prefix)
		}
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func sameScalar(a, b any) bool {
	switch av := a.(type) {
	case int:
		switch bv := b.(type) {
		case int:
			return av == bv
		case int64:
			return int64(av) == bv
		}
	case int64:
		switch bv := b.(type) {
		case int:
			return av == int64(bv)
		case int64:
			return av == bv
		}
	case string:
		if bv, ok := b.(string); ok {
			return av == bv
		}
	case bool:
		if bv, ok := b.(bool); ok {
			return av == bv
		}
	case nil:
		return b == nil
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// ---------------------------------------------------------------------------
// atomic write
// ---------------------------------------------------------------------------

func writeConfigAtomic(cfg Config, doc *configDocument, newRaw []byte) (string, error) {
	// Re-read right before writing: never clobber a concurrent management save.
	current, errRead := os.ReadFile(cfg.ConfigPath)
	if errRead != nil {
		return "", fmt.Errorf("re-read config: %w", errRead)
	}
	sum := sha256.Sum256(current)
	if hex.EncodeToString(sum[:]) != doc.hash {
		return "", errors.New("config changed while syncing; retry")
	}
	backup := fmt.Sprintf("%s.bak-modelmetasync-%s", cfg.ConfigPath, time.Now().Format("20060102-150405"))
	if errCopy := os.WriteFile(backup, current, doc.mode); errCopy != nil {
		return "", fmt.Errorf("write backup: %w", errCopy)
	}
	dir := filepath.Dir(cfg.ConfigPath)
	staged, errTemp := os.CreateTemp(dir, ".config-modelmetasync-*.yaml")
	if errTemp != nil {
		return "", fmt.Errorf("create staging config: %w", errTemp)
	}
	stagedName := staged.Name()
	defer func() { _ = os.Remove(stagedName) }()
	if _, errWrite := staged.Write(newRaw); errWrite != nil {
		_ = staged.Close()
		return "", fmt.Errorf("write staging config: %w", errWrite)
	}
	if errChmod := staged.Chmod(doc.mode); errChmod != nil {
		_ = staged.Close()
		return "", fmt.Errorf("chmod staging config: %w", errChmod)
	}
	if errSync := staged.Sync(); errSync != nil {
		_ = staged.Close()
		return "", fmt.Errorf("sync staging config: %w", errSync)
	}
	if errClose := staged.Close(); errClose != nil {
		return "", fmt.Errorf("close staging config: %w", errClose)
	}
	// Write in place: CPA watches the config file inode (inotify on the path's
	// inode), so an atomic rename surfaces only as Rename/Remove and never fires
	// the hot reload. A staged copy is kept for automatic recovery.
	if errInPlace := writeFileInPlace(cfg.ConfigPath, newRaw, doc.mode); errInPlace != nil {
		if stagedBytes, errStageRead := os.ReadFile(stagedName); errStageRead == nil {
			_ = writeFileInPlace(cfg.ConfigPath, stagedBytes, doc.mode)
		}
		return "", fmt.Errorf("replace config: %w", errInPlace)
	}
	if dirFile, errOpen := os.Open(dir); errOpen == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	pruneBackups(cfg.ConfigPath, cfg.BackupKeep)
	return backup, nil
}

func writeFileInPlace(path string, data []byte, mode os.FileMode) error {
	handle, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, mode)
	if errOpen != nil {
		return errOpen
	}
	if _, errWrite := handle.Write(data); errWrite != nil {
		_ = handle.Close()
		return errWrite
	}
	if errSync := handle.Sync(); errSync != nil {
		_ = handle.Close()
		return errSync
	}
	return handle.Close()
}

func pruneBackups(configPath string, keep int) {
	dir := filepath.Dir(configPath)
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	prefix := filepath.Base(configPath) + ".bak-modelmetasync-"
	matches := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Deployment backups are the rollback anchor; never prune them here.
		if strings.HasPrefix(name, prefix+"deploy-") {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, name := range matches[:len(matches)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func isInteger(raw string) bool {
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
