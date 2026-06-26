package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/egress"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/cireport"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/dlstats"
	"github.com/jverhoeks/escrow/internal/egresslog"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/cargo"
	"github.com/jverhoeks/escrow/internal/handler/composer"
	"github.com/jverhoeks/escrow/internal/handler/gomod"
	"github.com/jverhoeks/escrow/internal/handler/maven"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/handler/nuget"
	"github.com/jverhoeks/escrow/internal/handler/pypi"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/rescan"
	"github.com/jverhoeks/escrow/internal/server"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"
var version = "dev"

// dlStatsAdapter adapts *dlstats.Store to the rescan.DownloadStats interface so
// the rescan package doesn't import dlstats directly.
type dlStatsAdapter struct{ s *dlstats.Store }

func (a dlStatsAdapter) Get(eco, name, version string) (int, time.Time, bool) {
	st, ok := a.s.Get(eco, name, version)
	return st.Count, st.LastAt, ok
}

func main() {
	// Handle subcommands before flag parsing so they get their own flags.
	if len(os.Args) > 1 && os.Args[1] == "ci-report" {
		runCIReport(os.Args[2:])
		return
	}

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfgPath := flag.String("config", "escrow.toml", "config file path")
	hostFlag := flag.String("host", "", "listen host (overrides config; use 0.0.0.0 for all interfaces, default 127.0.0.1)")
	clearCache := flag.Bool("clear-cache", false, "flush all cached metadata and blobs on startup before serving")
	clearStats := flag.Bool("clear", false, "clear persisted event-log and egress-log stats on startup")
	// Signal overrides — each flag disables the corresponding policy check regardless of config.
	noAge := flag.Bool("no-age", false, "disable the age gate (ignore policy.age in config)")
	noOSV := flag.Bool("no-osv", false, "disable OSV vulnerability scan (ignore policy.osv in config)")
	noPublisher := flag.Bool("no-publisher", false, "disable publisher account age check (ignore policy.publisher in config)")
	noPopularity := flag.Bool("no-popularity", false, "disable popularity spike detection (ignore policy.popularity in config)")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("escrow %s\n", version)
		return
	}

	if flag.NArg() > 0 { // backward-compat: escrow [config-path]
		*cfgPath = flag.Arg(0)
	}

	generated, msg, err := config.GenerateIfMissing(*cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to generate config")
	}
	if generated {
		fmt.Println(msg)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	if *hostFlag != "" {
		cfg.Server.Host = *hostFlag
	}
	log.Info().Str("version", version).Msg("escrow starting")
	for _, err := range cfg.Validate() {
		log.Fatal().Err(err).Msg("invalid configuration")
	}
	for _, w := range cfg.Warnings() {
		log.Warn().Msg(w)
	}

	var c cache.Cache
	switch cfg.Storage.Backend {
	case "memory":
		c = cache.NewMemory()
		log.Warn().Msg("memory cache backend: metadata is capped with LRU eviction, but blobs are written to the OS temp dir and grow for the process lifetime; use it only for development/testing, never a shared/long-running instance (use disk or s3)")
	case "s3":
		c, err = cache.NewS3(cfg.Storage.S3.Bucket, cfg.Storage.S3.Region, cfg.Storage.S3.Endpoint, config.ExpandPath(cfg.Storage.S3.TempDir))
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init S3 cache")
		}
	default:
		diskPath := config.ExpandPath(cfg.Storage.Disk.Path)
		var maxBytes int64
		if cfg.Storage.Disk.MaxSizeGB > 0 {
			maxBytes = int64(cfg.Storage.Disk.MaxSizeGB) << 30
		}
		purgeM := cfg.Storage.Disk.PurgeIntervalM
		if purgeM == 0 {
			purgeM = 60
		}
		purgeInterval := time.Duration(purgeM) * time.Minute
		c, err = cache.NewDiskWithMax(diskPath, maxBytes, purgeInterval)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init disk cache")
		}
		logEvt := log.Info().Str("path", diskPath).Int("purge_interval_m", purgeM)
		if maxBytes > 0 {
			logEvt = logEvt.Int("max_size_gb", cfg.Storage.Disk.MaxSizeGB)
		}
		logEvt.Msg("disk cache initialised")
	}
	// Configure the opt-in stale-on-error metadata fallback. Zero (the default)
	// leaves cache behavior byte-for-byte unchanged (including eager
	// delete-on-expiry), preserving escrow's fail-closed posture.
	c.SetStaleMaxAge(time.Duration(cfg.Storage.StaleOnErrorMaxAgeM) * time.Minute)
	if cfg.Storage.StaleOnErrorMaxAgeM > 0 {
		log.Warn().Int("max_age_m", cfg.Storage.StaleOnErrorMaxAgeM).
			Msg("stale-on-error enabled: expired metadata may be served when upstream is unreachable, which can briefly re-expose a manifest-removed version")
	}
	defer c.Close()

	if *clearCache {
		if err := c.Flush(); err != nil {
			log.Fatal().Err(err).Msg("failed to flush cache")
		}
		log.Info().Msg("cache flushed")
	}

	// Collect active upstream URLs for /healthz probes.
	// Must be fully populated BEFORE server.New so the health handler has the complete map.
	upstreamURLs := make(map[string]string)
	if cfg.Ecosystems.NPM {
		upstreamURLs["npm"] = cfg.Ecosystems.EffectiveNPMUpstream()
	}
	if cfg.Ecosystems.PyPI {
		upstreamURLs["pypi"] = cfg.Ecosystems.EffectivePyPIUpstream()
	}
	if cfg.Ecosystems.Go {
		upstreamURLs["go"] = cfg.Ecosystems.EffectiveGoUpstream()
	}
	if cfg.Ecosystems.Cargo {
		upstreamURLs["cargo"] = "https://crates.io"
	}
	if cfg.Ecosystems.Composer {
		upstreamURLs["composer"] = cfg.Ecosystems.EffectiveComposerUpstream()
	}
	if cfg.Ecosystems.NuGet {
		upstreamURLs["nuget"] = cfg.Ecosystems.EffectiveNuGetUpstream()
	}
	if cfg.Ecosystems.Maven {
		upstreamURLs["maven"] = cfg.Ecosystems.EffectiveMavenUpstream()
	}

	// Map known registry hostnames → ecosystem for the upstream fetch log.
	// Derived from configured upstreams, plus well-known defaults so artifact
	// CDNs (which differ from the metadata host) are also classified.
	upstreamLog := upstreamlog.New(5000)

	// Resolve the effective egress-log path so the egress live view survives
	// restarts by default on the disk backend (mirrors the event log below). An
	// explicit egress_log_path always wins; memory/s3 backends stay in-memory.
	var egressLogPath, egressLogMsg string
	if cfg.EgressLogPath != "" {
		egressLogPath = config.ExpandPath(cfg.EgressLogPath)
		egressLogMsg = "egress log persistence enabled"
	} else if cfg.Storage.Backend == "disk" {
		egressLogPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow-egress.jsonl")
		egressLogMsg = "egress log persistence enabled (default path)"
	}

	var egressLog *egresslog.Log
	if egressLogPath != "" {
		if *clearStats {
			if err := os.Remove(egressLogPath); err != nil && !os.IsNotExist(err) {
				log.Warn().Err(err).Str("path", egressLogPath).Msg("failed to clear egress-log stats")
			} else {
				log.Info().Str("path", egressLogPath).Msg("cleared egress-log stats")
			}
		}
		var err error
		egressLog, err = egresslog.NewWithPath(5000, egressLogPath)
		if err != nil {
			log.Fatal().Err(err).Str("path", egressLogPath).Msg("failed to open egress log file")
		}
		log.Info().Str("path", egressLogPath).Msg(egressLogMsg)
	} else {
		egressLog = egresslog.New(5000)
	}
	defer egressLog.Close()

	hostEco := map[string]string{
		"registry.npmjs.org":     "npm",
		"pypi.org":               "pypi",
		"files.pythonhosted.org": "pypi",
		"crates.io":              "cargo",
		"static.crates.io":       "cargo",
		"proxy.golang.org":       "go",
		"repo1.maven.org":        "maven",
		"repo.maven.apache.org":  "maven",
		"repo.packagist.org":     "composer",
		"packagist.org":          "composer",
		"api.nuget.org":          "nuget",
	}
	for eco, raw := range upstreamURLs {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			hostEco[u.Hostname()] = eco
		}
	}

	httpClient := server.NewLoggingClientWithRecorder(upstream.New(), log.Logger, upstreamLog, hostEco)
	polEngine := policy.New(cfg.Policy)

	allowList, err := allow.New(config.ExpandPath(cfg.AllowlistPath))
	if err != nil {
		log.Fatal().Err(err).Str("path", cfg.AllowlistPath).Msg("failed to load allowlist")
	}
	if cfg.AllowlistPath == "" {
		log.Warn().Msg("allowlist_path not configured — allow list entries will not persist across restarts")
	}
	polEngine.WithAllowList(allowList)

	blockList, err := block.New(config.ExpandPath(cfg.BlocklistPath))
	if err != nil {
		log.Fatal().Err(err).Str("path", cfg.BlocklistPath).Msg("failed to load blocklist")
	}
	if cfg.BlocklistPath == "" {
		log.Warn().Msg("blocklist_path not configured — block list entries will not persist across restarts")
	}
	polEngine.WithBlockList(blockList)

	// Invalidate cached metadata whenever an allow/block entry changes (dashboard
	// mutation or rescan auto-block) so the new policy takes effect on the next
	// listing instead of lagging up to the meta TTL (24h for Go). Blobs are kept.
	// See #38.
	invalidateMeta := func() {
		if err := c.InvalidateMeta(); err != nil {
			log.Warn().Err(err).Msg("failed to invalidate cached metadata after a list change")
		}
	}
	allowList.SetOnChange(invalidateMeta)
	blockList.SetOnChange(invalidateMeta)

	// Resolve the effective event-log path so stats survive restarts by default
	// on the disk backend. An explicit cfg.EventLogPath always wins; otherwise
	// the disk backend defaults to escrow-events.jsonl inside the cache dir.
	// Memory/S3 backends have no local dir, so stats stay in-memory (ephemeral).
	var evLogPath, evLogMsg string
	if cfg.EventLogPath != "" {
		evLogPath = config.ExpandPath(cfg.EventLogPath)
		evLogMsg = "event log persistence enabled"
	} else if cfg.Storage.Backend == "disk" {
		evLogPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow-events.jsonl")
		evLogMsg = "event log persistence enabled (default path)"
	}

	var evLog *eventlog.Log
	if evLogPath != "" {
		if *clearStats {
			if err := os.Remove(evLogPath); err != nil && !os.IsNotExist(err) {
				log.Warn().Err(err).Str("path", evLogPath).Msg("failed to clear event-log stats")
			} else {
				log.Info().Str("path", evLogPath).Msg("cleared event-log stats")
			}
		}
		evLog, err = eventlog.NewWithPath(5000, evLogPath)
		if err != nil {
			log.Fatal().Err(err).Str("path", evLogPath).Msg("failed to open event log file")
		}
		defer evLog.Close()
		log.Info().Str("path", evLogPath).Msg(evLogMsg)
	} else {
		evLog = eventlog.New(5000)
	}

	// Download stats — persistent per-version counts, populated by subscribing
	// to the event log. Defaults to the cache dir on the disk backend.
	dlPath := config.ExpandPath(cfg.DownloadStatsPath)
	if dlPath == "" && cfg.Storage.Backend == "disk" {
		dlPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow-downloads.json")
	}
	dlStore, err := dlstats.New(dlPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", dlPath).Msg("failed to open download stats")
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	go dlstats.Consume(rootCtx, evLog, dlStore)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				_ = dlStore.Flush()
			}
		}
	}()

	var signals []trust.Signal
	if cfg.Policy != nil {
		if cfg.Policy.Age != nil && !*noAge {
			signals = append(signals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
			log.Info().Int("min_days", cfg.Policy.Age.MinDays).Str("action", cfg.Policy.Age.Action).
				Msg("age gate enabled")
		} else if *noAge {
			log.Warn().Msg("age gate disabled via --no-age flag")
		}
		if cfg.Policy.OSV != nil && !*noOSV {
			signals = append(signals, trust.NewOSVSignal(cfg.Policy.OSV.MinSeverity, httpClient, c, ""))
			log.Info().Str("min_severity", cfg.Policy.OSV.MinSeverity).Str("action", cfg.Policy.OSV.Action).
				Msg("OSV vulnerability scan enabled")
		} else if *noOSV {
			log.Warn().Msg("OSV vulnerability scan disabled via --no-osv flag")
		}
		if cfg.Policy.Publisher != nil && !*noPublisher {
			signals = append(signals, trust.NewPublisherSignal(cfg.Policy.Publisher.MaxAccountAgeDays, httpClient, c, "", ""))
			log.Info().Int("max_account_age_days", cfg.Policy.Publisher.MaxAccountAgeDays).Str("action", cfg.Policy.Publisher.Action).
				Msg("publisher account age check enabled")
		} else if *noPublisher {
			log.Warn().Msg("publisher check disabled via --no-publisher flag")
		}
		if cfg.Policy.Popularity != nil && !*noPopularity {
			signals = append(signals, trust.NewPopularitySignal(cfg.Policy.Popularity.SpikeFactor, httpClient, c, "", ""))
			log.Info().Float64("spike_factor", cfg.Policy.Popularity.SpikeFactor).Str("action", cfg.Policy.Popularity.Action).
				Msg("popularity spike detection enabled")
		} else if *noPopularity {
			log.Warn().Msg("popularity check disabled via --no-popularity flag")
		}
	} else {
		log.Warn().Msg("no policy configured — proxying transparently (no age gate, no vulnerability scan)")
	}
	trustEngine := trust.NewEngine(signals...)

	// Age-only engine for index listing — avoids per-version OSV/publisher network
	// calls when building the Simple API for large packages (starlette, pydantic-core, etc.).
	// OSV and publisher checks still run at download time via the full trustEngine.
	var listingSignals []trust.Signal
	if cfg.Policy != nil && cfg.Policy.Age != nil && !*noAge {
		listingSignals = append(listingSignals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
	}
	listingEngine := trust.NewEngine(listingSignals...)

	var wh *alerts.Webhook
	if cfg.Alerts.WebhookURL != "" {
		wh = alerts.NewWebhook(cfg.Alerts.WebhookURL, nil)
		log.Info().Str("url", cfg.Alerts.WebhookURL).Msg("webhook alerts enabled")
	}

	// Continuous CVE re-scanner.
	var scanner *rescan.Scanner
	{
		rc := cfg.Rescan
		// Both bools default to TRUE so a user who adds a [rescan] section to tweak
		// only the cadence/severity doesn't silently disable the scanner or
		// auto-block. They are *bool: nil (key omitted) keeps the default.
		enabled := true
		autoBlock := true
		interval := 24
		minSev := "HIGH"
		if cfg.Policy != nil && cfg.Policy.OSV != nil && cfg.Policy.OSV.MinSeverity != "" {
			minSev = cfg.Policy.OSV.MinSeverity
		}
		if rc != nil {
			if rc.Enabled != nil {
				enabled = *rc.Enabled
			}
			if rc.AutoBlock != nil {
				autoBlock = *rc.AutoBlock
			}
			if rc.MinSeverity != "" {
				minSev = rc.MinSeverity
			}
			if rc.IntervalHours > 0 {
				interval = rc.IntervalHours
			}
		}
		rescanOSV := trust.NewOSVSignal(minSev, httpClient, c, "")
		var alerter rescan.Alerter
		if wh != nil {
			alerter = wh
		}
		scanner = rescan.New(rescan.Deps{
			Log: evLog, OSV: rescanOSV, BlockList: blockList,
			Stats: dlStatsAdapter{dlStore}, Alerter: alerter,
			Logger: nil,
		}, rescan.Config{Enabled: enabled, IntervalHours: interval, AutoBlock: autoBlock, MinSeverity: minSev})
		scanner.Start(rootCtx)
		log.Info().Bool("auto_block", autoBlock).Int("interval_hours", interval).Str("min_severity", minSev).Msg("CVE re-scanner enabled")
	}

	// Egress proxy (Docker build protection, Phase 1): optional second listener.
	// nil section => disabled. Forward proxy only; no TLS interception.
	if ep := cfg.EgressProxy; ep != nil && (ep.Enabled == nil || *ep.Enabled) {
		pol, err := egress.NewPolicy(*ep)
		if err != nil {
			log.Fatal().Err(err).Msg("egress proxy: invalid policy")
		}
		port := ep.ForwardPort
		if port == 0 {
			port = 7889
		}
		if egress.ExposedBind(cfg.Server.Host) && !strings.EqualFold(ep.Policy, "whitelist") {
			log.Warn().Str("host", cfg.Server.Host).
				Msg("egress proxy is reachable off-host with policy=forward — this is an OPEN RELAY; set egress_proxy.policy=\"whitelist\" or firewall the egress port")
		}
		eproxy := egress.New(fmt.Sprintf("%s:%d", cfg.Server.Host, port), pol, egressLog, ep.RateLimitPerMin)
		go func() {
			log.Info().Int("port", port).Str("policy", ep.Policy).Msg("egress proxy listening")
			if err := eproxy.Serve(rootCtx); err != nil {
				log.Error().Err(err).Msg("egress proxy stopped")
			}
		}()
	}

	// egressFingerprint returns a stable string representation of the egress proxy
	// config fields that require a restart to change. *bool is dereferenced to
	// avoid comparing heap addresses across Load calls.
	egressFingerprint := func(ep *config.EgressProxyConfig) string {
		if ep == nil {
			return ""
		}
		enabled := false
		if ep.Enabled != nil {
			enabled = *ep.Enabled
		}
		return fmt.Sprintf("%v:%d:%s:%v:%v:%v:%v",
			enabled, ep.ForwardPort, ep.Policy,
			ep.AllowHosts, ep.BlockHosts, ep.AllowCIDRs, ep.BlockCIDRs)
	}

	// restartSnapshot captures the fields that cannot be applied live; a reload
	// reports any that changed as restart-required.
	restartSnapshot := func(c config.Config) map[string]string {
		return map[string]string{
			"server":      fmt.Sprintf("%s:%d:%s:%s", c.Server.Host, c.Server.Port, c.Server.TLSCertFile, c.Server.TLSKeyFile),
			"storage":     fmt.Sprintf("%s:%s", c.Storage.Backend, c.Storage.Disk.Path),
			"ecosystems":  fmt.Sprintf("%v", c.Ecosystems),
			"secret":      c.Dashboard.Secret,
			"paths":       fmt.Sprintf("%s:%s:%s:%s:%s", c.AllowlistPath, c.BlocklistPath, c.EventLogPath, c.Server.AccessLogPath, c.EgressLogPath),
			"egress_proxy": egressFingerprint(c.EgressProxy),
		}
	}
	startupSnapshot := restartSnapshot(cfg)

	// dash is declared here (before reloadFn) so the reload closure can call
	// UpdateCredentials. It is assigned below after dashboard.New(...) is called.
	var dash *dashboard.Dashboard

	reloadFn := func() (dashboard.ReloadResult, error) {
		newCfg, err := config.Load(*cfgPath)
		if err != nil {
			return dashboard.ReloadResult{}, err
		}
		if errs := newCfg.Validate(); len(errs) > 0 {
			return dashboard.ReloadResult{}, errs[0]
		}
		var restart []string
		now := restartSnapshot(newCfg)
		for k, v := range startupSnapshot {
			if now[k] != v {
				restart = append(restart, k)
			}
		}
		// Apply the live-reloadable subset.
		polEngine.SetConfig(newCfg.Policy)
		// A policy change can flip allow/block/age decisions, so drop cached
		// (post-filter) manifests; blobs are immutable and kept. See #38.
		invalidateMeta()
		rMinSev := "HIGH"
		if newCfg.Policy != nil && newCfg.Policy.OSV != nil && newCfg.Policy.OSV.MinSeverity != "" {
			rMinSev = newCfg.Policy.OSV.MinSeverity
		}
		rEnabled, rAutoBlock, rInterval := true, true, 24
		if rc := newCfg.Rescan; rc != nil {
			if rc.Enabled != nil {
				rEnabled = *rc.Enabled
			}
			if rc.AutoBlock != nil {
				rAutoBlock = *rc.AutoBlock
			}
			if rc.MinSeverity != "" {
				rMinSev = rc.MinSeverity
			}
			if rc.IntervalHours > 0 {
				rInterval = rc.IntervalHours
			}
		}
		if scanner != nil {
			scanner.SetConfig(rescan.Config{Enabled: rEnabled, IntervalHours: rInterval, AutoBlock: rAutoBlock, MinSeverity: rMinSev})
		}
		reloaded := []string{"policy", "rescan"}
		if wh != nil {
			// A webhook instance exists — URL changes (including clearing it) apply live.
			wh.SetURL(newCfg.Alerts.WebhookURL)
			reloaded = append(reloaded, "alerts")
		} else if newCfg.Alerts.WebhookURL != "" {
			// Started with no webhook; one can't be created live — needs a restart.
			restart = append(restart, "alerts")
		}
		if dash != nil {
			dash.UpdateCredentials(newCfg.Dashboard.Username, newCfg.Dashboard.Password, newCfg.Dashboard.Secret)
			reloaded = append(reloaded, "dashboard_credentials")
		}
		log.Info().Strs("reloaded", reloaded).Strs("restart_required", restart).Msg("config reloaded")
		return dashboard.ReloadResult{Reloaded: reloaded, RestartRequired: restart}, nil
	}

	srv := server.New(server.Options{
		Version:                  version,
		Host:                     cfg.Server.Host,
		Port:                     cfg.Server.Port,
		StorageBackend:           cfg.Storage.Backend,
		CacheHealth:              c.Healthy,
		WriteTimeoutSeconds:      cfg.Server.WriteTimeoutSeconds,
		ReadHeaderTimeoutSeconds: cfg.Server.ReadHeaderTimeoutSeconds,
		IdleTimeoutSeconds:       cfg.Server.IdleTimeoutSeconds,
		TLSCertFile:              cfg.Server.TLSCertFile,
		TLSKeyFile:               cfg.Server.TLSKeyFile,
		ProxyRateLimitPerMin:     cfg.Server.ProxyRateLimitPerMin,
		MaxRequestBodyMB:         cfg.Server.EffectiveMaxRequestBodyMB(),
		AccessLogPath:            config.ExpandPath(cfg.Server.AccessLogPath),
		AccessLogMaxDays:         cfg.Server.AccessLogMaxDays,
		UpstreamURLs:             upstreamURLs,
	}, log.Logger)
	r := srv.Router()

	if cfg.Ecosystems.NPM {
		h := npm.New(httpClient, cfg.Ecosystems.EffectiveNPMUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.PyPI {
		blockSdist := cfg.Policy != nil && cfg.Policy.PyPI != nil && cfg.Policy.PyPI.BlockSdist
		h := pypi.New(httpClient, cfg.Ecosystems.EffectivePyPIUpstream(), trustEngine, polEngine, c, blockSdist, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.Go {
		h := gomod.New(httpClient, cfg.Ecosystems.EffectiveGoUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("go modules proxy enabled at /go/")
	}
	if cfg.Ecosystems.Cargo {
		h := cargo.New(httpClient, trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("cargo sparse registry enabled at /cargo/")
	}
	if cfg.Ecosystems.Composer {
		h := composer.New(httpClient, cfg.Ecosystems.EffectiveComposerUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("composer proxy enabled at /composer/")
	}
	if cfg.Ecosystems.NuGet {
		h := nuget.New(httpClient, cfg.Ecosystems.EffectiveNuGetUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if cfg.Ecosystems.NuGetFlatcontainerURL != "" {
			h.SetFlatcontainerURL(cfg.Ecosystems.NuGetFlatcontainerURL)
		}
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("nuget proxy enabled at /nuget/")
	}
	if cfg.Ecosystems.Maven {
		h := maven.New(httpClient, cfg.Ecosystems.EffectiveMavenUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		if cfg.Ecosystems.MavenSnapshotUpstream != "" {
			h.SetSnapshotURL(cfg.Ecosystems.EffectiveMavenSnapshotUpstream())
			log.Info().Str("url", cfg.Ecosystems.MavenSnapshotUpstream).Msg("maven snapshot upstream configured")
		}
		h.Mount(r)
		log.Info().Msg("maven/gradle proxy enabled at /maven2/")
	}

	cireport.New(evLog, cfg.CIReportToken).Mount(r)
	if cfg.CIReportToken == "" {
		log.Warn().Msg("/ci-report is unauthenticated (exposes the blocklist + evaluated packages); set ci_report_token in escrow.toml to require a token")
	}

	if cfg.Dashboard.Enabled {
		dash = dashboard.New(cfg.Dashboard, evLog, log.Logger, allowList, blockList, c,
			srv.AccessRing(), upstreamLog, egressLog, dlStore, scanner, *cfgPath, reloadFn)
		dash.Mount(r)
		log.Info().Str("path", cfg.Dashboard.Path).Msg("dashboard enabled")
	}

	// PID file so `escrow-cli reload` can find and SIGHUP this process.
	pidPath := filepath.Join(filepath.Dir(*cfgPath), "escrow.pid")
	if cfg.Storage.Backend == "disk" {
		pidPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow.pid")
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err == nil {
		defer os.Remove(pidPath)
	}

	// Publish a CWD-independent runtime record so `escrow-cli live`/`reload` can
	// find the live process and its (absolute) event log from any directory.
	evLogAbs := evLogPath
	if evLogAbs != "" {
		if abs, aerr := filepath.Abs(evLogAbs); aerr == nil {
			evLogAbs = abs
		}
	}
	if err := config.WriteRuntime(config.Runtime{EventLogPath: evLogAbs, PID: os.Getpid(), Port: cfg.Server.Port}); err != nil {
		log.Debug().Err(err).Msg("could not write runtime discovery file")
	} else if rp, perr := config.RuntimePath(); perr == nil {
		defer os.Remove(rp) // remove on shutdown so the CLI never trusts a stale port
	}

	// SIGHUP → reload the live-reloadable config subset.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if _, err := reloadFn(); err != nil {
				log.Error().Err(err).Msg("SIGHUP reload failed; keeping previous config")
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// drained is closed once the graceful shutdown actually completes. main()
	// blocks on it below so the deferred Close()s (cache, event log, pid/runtime
	// files) run AFTER the drain, not while in-flight downloads are still being
	// served. srv.Start() returns http.ErrServerClosed the instant Shutdown
	// closes the listener — long before the drain finishes — so without this the
	// 10s grace window never actually applied. See #37.
	drained := make(chan struct{})
	go func() {
		<-quit
		rootCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx) // blocks until in-flight requests finish or grace elapses
		_ = dlStore.Close()
		close(drained)
	}()

	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("server stopped unexpectedly")
	}
	// Wait for the graceful drain to complete before returning — only then do the
	// deferred resource closers run.
	<-drained
}

// runCIReport fetches the CI report from a running escrow proxy and prints it to stdout.
// Intended for use in GitHub Actions: `escrow ci-report >> $GITHUB_STEP_SUMMARY`
func runCIReport(args []string) {
	fs := flag.NewFlagSet("ci-report", flag.ExitOnError)
	port := fs.Int("port", 7888, "escrow proxy port")
	n := fs.Int("n", 200, "max packages to show in the table")
	token := fs.String("token", os.Getenv("ESCROW_CI_REPORT_TOKEN"), "ci_report_token if the endpoint requires auth (default $ESCROW_CI_REPORT_TOKEN)")
	fs.Parse(args) //nolint:errcheck

	url := fmt.Sprintf("http://127.0.0.1:%d/ci-report?n=%d", *port, *n)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec — localhost only
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow ci-report: could not reach proxy on port %d: %v\n", *port, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintln(os.Stderr, "escrow ci-report: unauthorized — pass --token or set $ESCROW_CI_REPORT_TOKEN")
		return
	}
	io.Copy(os.Stdout, resp.Body) //nolint:errcheck
}
