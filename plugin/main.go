package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginName   = "model-meta-sync"
	pluginAuthor = "LIEGU0317"
	pluginRepo   = "https://github.com/LIEGU0317/cpa-plugin-model-meta-sync"
	// pluginLogo is the display asset CPA management clients render next to the
	// plugin. It is served from the repository so the plugin store and the
	// management UI show the same icon.
	pluginLogo  = "https://raw.githubusercontent.com/LIEGU0317/cpa-plugin-model-meta-sync/main/assets/logo.png"
	routePrefix = "/model-meta-sync"
)

// pluginVersion is the metadata version CPA reports and the store compares
// against the release tag. Release builds stamp it from the tag:
//
//	-ldflags "-X main.pluginVersion=0.3.1"
//
// The default keeps a plain `go build` working during development.
var pluginVersion = "0.3.1"

type pluginState struct {
	mu         sync.Mutex
	syncMu     sync.Mutex
	cfg        Config
	configYAML []byte
	pluginDir  string
	last       *RunReport
	startedAt  time.Time
	stop       chan struct{}
}

// startupSyncDelay lets the host finish registering before the first sync.
var startupSyncDelay = 3 * time.Second

// activeBackground owns the single background loop. The host re-registers the
// plugin on every config reload and a c-shared library is never unloaded, so
// the previous loop must be stopped - and waited for - before another starts
// or the plugin is shut down.
var activeBackground = struct {
	sync.Mutex
	stop chan struct{}
	done sync.WaitGroup
}{}

// stopBackground stops the running loop, cancels a sync it has in flight and
// waits for it to return.
func stopBackground() {
	activeBackground.Lock()
	if activeBackground.stop != nil {
		close(activeBackground.stop)
		activeBackground.stop = nil
	}
	activeBackground.Unlock()
	activeBackground.done.Wait()
}

func buildPlugin(configYAML []byte, pluginDir string) (*pluginState, error) {
	cfg, errParse := parseConfig(configYAML)
	if errParse != nil {
		return nil, errParse
	}
	p := &pluginState{
		cfg:        cfg,
		configYAML: append([]byte(nil), configYAML...),
		pluginDir:  pluginDir,
		startedAt:  time.Now().UTC(),
		stop:       make(chan struct{}),
	}
	p.startBackground()
	return p, nil
}

// metadata is the plugin descriptor CPA registers and the plugin store
// renders. Every value here is part of the plugin standard: the four identity
// fields are mandatory, Logo is the display asset and ConfigFields lets the
// management UI render the plugin-owned configuration form without the plugin
// shipping its own UI.
func (p *pluginState) metadata() pluginapi.Metadata {
	return pluginapi.Metadata{
		Name:             pluginName,
		Version:          pluginVersion,
		Author:           pluginAuthor,
		GitHubRepository: pluginRepo,
		Logo:             pluginLogo,
		ConfigFields: []pluginapi.ConfigField{
			{Name: "config_path", Type: pluginapi.ConfigFieldTypeString,
				Description: "Optional path to the CPA config.yaml; omitted = the host's -config flag or ./config.yaml."},
			{Name: "providers", Type: pluginapi.ConfigFieldTypeArray, Default: []string{},
				Description: "Optional provider-name filter; empty syncs every enabled provider."},
			{Name: "include_disabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false,
				Description: "Also sync providers marked disabled in the CPA config."},
			{Name: "mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"missing", "always"}, Default: "missing",
				Description: "missing fills empty fields only; always recomputes the configured fields."},
			{Name: "fields", Type: pluginapi.ConfigFieldTypeArray, Default: allFields,
				Description: "Subset of display-name, max-context-length, input-modalities, output-modalities, thinking."},
			{Name: "overwrite_fields", Type: pluginapi.ConfigFieldTypeArray, Default: []string{},
				Description: "Fields that stay authoritative upstream even in missing mode; empty means never overwrite."},
			{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Default: defaultTimeoutSec,
				Description: "Upstream model-list request timeout in seconds."},
			{Name: "backup_keep", Type: pluginapi.ConfigFieldTypeInteger, Default: defaultBackupKeep,
				Description: "Timestamped config backups to retain (deploy backups are never pruned)."},
			{Name: "sync_on_start", Type: pluginapi.ConfigFieldTypeBoolean, Default: true,
				Description: "Run one sync shortly after the plugin loads."},
			{Name: "dry_run_on_start", Type: pluginapi.ConfigFieldTypeBoolean, Default: false,
				Description: "Make the startup sync a preview: report only, write nothing."},
			{Name: "interval_minutes", Type: pluginapi.ConfigFieldTypeInteger, Default: 0,
				Description: "Repeat the sync every N minutes (0 disables)."},
			{Name: "models_paths", Type: pluginapi.ConfigFieldTypeObject, Default: map[string]string{},
				Description: "Per-provider model-list path override, e.g. {Spark: /models}; omitted = automatic /v1/models then /models."},
			{Name: "api_keys", Type: pluginapi.ConfigFieldTypeObject, Default: map[string]string{},
				Description: "Per-provider upstream key override, e.g. {Spark: sk-...}; omitted = the provider's first configured key."},
			{Name: "api_key", Type: pluginapi.ConfigFieldTypeString,
				Description: "Deprecated: single key override, only accepted with exactly one entry in providers; prefer api_keys."},
			{Name: "upstream_models_path", Type: pluginapi.ConfigFieldTypeString, Default: defaultUpstreamDir,
				Description: "Deprecated: global model-list path; prefer models_paths. Defaults to /v1/models."},
		},
	}
}

func (p *pluginState) RegisterManagement(_ context.Context, _ pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	makeRoute := func(method, path, description string) pluginapi.ManagementRoute {
		return pluginapi.ManagementRoute{
			Method:      method,
			Path:        path,
			Description: description,
			Handler:     p,
		}
	}
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			makeRoute("GET", routePrefix+"/status", "Last model metadata sync report"),
			makeRoute("GET", routePrefix+"/config", "Effective plugin configuration"),
			makeRoute("GET", routePrefix+"/preview", "Dry-run: compute the model metadata patch without writing"),
			makeRoute("POST", routePrefix+"/sync", "Fetch upstream /v1/models and write missing metadata into the provider config"),
		},
	}, nil
}

func (p *pluginState) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path := req.Path
	if idx := len("/v0/management"); len(path) > idx && path[:idx] == "/v0/management" {
		path = path[idx:]
	}
	switch {
	case req.Method == "GET" && path == routePrefix+"/status":
		return jsonResponse(200, p.statusPayload())
	case req.Method == "GET" && path == routePrefix+"/config":
		return jsonResponse(200, p.configPayload())
	case (req.Method == "GET" || req.Method == "POST") && path == routePrefix+"/preview":
		return p.handleSync(ctx, req, true)
	case req.Method == "POST" && path == routePrefix+"/sync":
		return p.handleSync(ctx, req, false)
	default:
		return jsonResponse(404, map[string]any{"ok": false, "error": "not found", "path": path, "method": req.Method})
	}
}

func (p *pluginState) handleSync(ctx context.Context, req pluginapi.ManagementRequest, forceDryRun bool) (pluginapi.ManagementResponse, error) {
	dryRun := forceDryRun
	if !forceDryRun && req.Query != nil {
		switch req.Query.Get("dry_run") {
		case "1", "true", "yes":
			dryRun = true
		}
	}
	provider := ""
	if req.Query != nil {
		provider = req.Query.Get("provider")
	}
	report, errRun := p.runSync(ctx, syncOptions{DryRun: dryRun, Provider: provider})
	if errRun != nil {
		status := 502
		if report != nil && (report.ConfigHashBefore == "" || strings.HasPrefix(report.Error, "no provider matched")) {
			status = 400
		}
		return jsonResponse(status, report)
	}
	return jsonResponse(200, report)
}

func (p *pluginState) statusPayload() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]any{
		"ok":         true,
		"plugin":     pluginName,
		"version":    pluginVersion,
		"started_at": p.startedAt.Format(time.RFC3339),
		"config":     redactedConfig(p.cfg),
	}
	if p.last != nil {
		out["last_run"] = p.last
	}
	return out
}

func (p *pluginState) configPayload() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]any{"ok": true, "config": redactedConfig(p.cfg), "raw_config_yaml": redactConfigYAML(p.configYAML)}
}

// redactConfigYAML masks api_key and every api_keys value in the raw plugin
// config subtree before it is returned by the management API.
func redactConfigYAML(raw []byte) string {
	parsed := map[string]any{}
	if errUnmarshal := yaml.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return "<unparseable plugin config>"
	}
	if value, ok := parsed["api_key"]; ok && value != nil && value != "" {
		parsed["api_key"] = "***"
	}
	if keys, ok := parsed["api_keys"].(map[string]any); ok {
		for name := range keys {
			keys[name] = "***"
		}
	} else if parsed["api_keys"] != nil {
		parsed["api_keys"] = "***"
	}
	out, errMarshal := yaml.Marshal(parsed)
	if errMarshal != nil {
		return "<redaction failed>"
	}
	return string(out)
}

func redactedConfig(cfg Config) Config {
	out := cfg
	if out.APIKey != "" {
		out.APIKey = "***"
	}
	if len(cfg.APIKeys) > 0 {
		out.APIKeys = make(map[string]string, len(cfg.APIKeys))
		for name := range cfg.APIKeys {
			out.APIKeys[name] = "***"
		}
	}
	return out
}

func (p *pluginState) storeReport(report *RunReport) {
	p.mu.Lock()
	p.last = report
	p.mu.Unlock()
}

func jsonResponse(status int, payload any) (pluginapi.ManagementResponse, error) {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		body = []byte(fmt.Sprintf(`{"ok":false,"error":%q}`, errMarshal.Error()))
		status = 500
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
		Body:       body,
	}, nil
}

// ---------------------------------------------------------------------------
// background scheduling
// ---------------------------------------------------------------------------

func (p *pluginState) startBackground() {
	stopBackground()

	interval := time.Duration(p.cfg.IntervalMinutes) * time.Minute
	if !p.cfg.SyncOnStart && interval <= 0 {
		return
	}
	stop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	activeBackground.Lock()
	activeBackground.stop = stop
	activeBackground.done.Add(1)
	activeBackground.Unlock()

	go func() {
		defer activeBackground.done.Done()
		defer cancel()
		// Closing stop cancels a sync that is already talking to an upstream.
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		if p.cfg.SyncOnStart {
			select {
			case <-ctx.Done():
				return
			case <-time.After(startupSyncDelay):
			}
			p.backgroundSync(ctx, p.cfg.DryRunOnStart)
		}
		if interval <= 0 {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.backgroundSync(ctx, false)
			}
		}
	}()
}

func (p *pluginState) backgroundSync(parent context.Context, dryRun bool) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	report, errRun := p.runSync(ctx, syncOptions{DryRun: dryRun})
	if errRun != nil && report != nil {
		report.Error = errRun.Error()
	}
	if parent.Err() != nil {
		// Stopped by shutdown or a reconfigure; the successor reports.
		return
	}
	logReport(report)
}

// logReport prints one compact line per run. It names providers only: hosts,
// ports and addresses never reach the journal.
func logReport(report *RunReport) {
	if report == nil {
		fmt.Println("[model-meta-sync] sync finished without a report")
		return
	}
	parts := make([]string, 0, len(report.Providers))
	for _, provider := range report.Providers {
		detail := provider.Name + ":matched=" + strconv.Itoa(provider.Matched) + "/" + strconv.Itoa(provider.Configured)
		if provider.Changed > 0 {
			detail += ",changed=" + strconv.Itoa(provider.Changed)
		}
		if provider.Skipped != "" {
			detail += ",skipped=" + provider.Skipped
		}
		if provider.UpstreamErr != "" {
			detail += ",error=" + provider.UpstreamErr
		}
		parts = append(parts, detail)
	}
	fmt.Printf("[model-meta-sync] dry_run=%t ok=%t written=%t changed_fields=%d providers=[%s] error=%q warnings=%q\n",
		report.DryRun, report.OK, report.Written, report.ChangedFields,
		strings.Join(parts, " | "), report.Error, strings.Join(report.Warnings, "; "))
}
