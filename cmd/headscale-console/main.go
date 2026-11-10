package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/guanzi008/headscale-console/internal/config"
	"github.com/guanzi008/headscale-console/internal/headplane"
	"github.com/guanzi008/headscale-console/internal/headscale"
	"github.com/guanzi008/headscale-console/internal/idp"
	"github.com/guanzi008/headscale-console/internal/proxy"
	runtimeenv "github.com/guanzi008/headscale-console/internal/runtime"
	"github.com/guanzi008/headscale-console/internal/secret"
	"github.com/guanzi008/headscale-console/internal/supervisor"
	"github.com/guanzi008/headscale-console/internal/version"
)

const oidcClientID = "headscale-console"

type application struct {
	configPath string
	dataDir    string
	cfg        config.Config
	bundle     runtimeenv.Bundle
	logger     *slog.Logger

	idpServer     *idp.Server
	idpHTTP       *httpService
	publicHTTP    *httpService
	headscaleProc *supervisor.Manager
	headplaneProc *supervisor.Manager
}

func main() {
	if err := run(); err != nil {
		slog.Error("headscale-console stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("HEADSCALE_CONSOLE_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	defaultDataDir, err := config.DefaultDataDir()
	if err != nil {
		return err
	}
	var (
		dataDirFlag = flag.String("data-dir", defaultDataDir, "data directory (or HEADSCALE_CONSOLE_DATA_DIR)")
		configFlag  = flag.String("config", os.Getenv("HEADSCALE_CONSOLE_CONFIG"), "configuration file (default: <data-dir>/config.yaml)")
		showVersion = flag.Bool("version", false, "print version information")
	)
	flag.Parse()

	if *showVersion {
		info := version.Get()
		fmt.Printf("headscale-console %s (%s, built %s, %s/%s)\n", info.Version, info.Commit, info.BuiltAt, info.GoOS, info.GoArch)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flag.Args(), " "))
	}

	dataDir, err := filepath.Abs(*dataDirFlag)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	configPath := *configFlag
	if configPath == "" {
		configPath = filepath.Join(dataDir, "config.yaml")
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	cfg, err := config.LoadOrCreate(configPath)
	if err != nil {
		return err
	}
	bundle, err := runtimeenv.Resolve(dataDir)
	if err != nil {
		return fmt.Errorf("resolve embedded runtime (run ./build.sh first): %w", err)
	}
	app := &application{
		configPath: configPath,
		dataDir:    dataDir,
		cfg:        cfg,
		bundle:     bundle,
		logger:     logger,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx)
}

func (a *application) Run(ctx context.Context) error {
	info := version.Get()
	a.logger.Info("starting Headscale Console", "version", info.Version, "config", a.configPath, "data_dir", a.dataDir)

	generatedDir := filepath.Join(a.dataDir, "generated")
	secretsDir := filepath.Join(a.dataDir, "secrets")
	for _, directory := range []string{
		generatedDir,
		secretsDir,
		filepath.Join(a.dataDir, "account"),
		filepath.Join(a.dataDir, "headplane"),
		filepath.Join(a.dataDir, "headscale"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create data directory %s: %w", directory, err)
		}
	}

	clientSecret, err := secret.LoadOrCreateToken(filepath.Join(secretsDir, "oidc-client-secret"), 48)
	if err != nil {
		return err
	}
	cookieSecret, err := secret.LoadOrCreateToken(filepath.Join(secretsDir, "headplane-cookie-secret"), 32)
	if err != nil {
		return err
	}

	identityProvider, err := idp.New(idp.Options{
		DataDir:       filepath.Join(a.dataDir, "account"),
		BaseURL:       a.cfg.Server.BaseURL,
		Issuer:        a.cfg.Issuer(),
		AdminPath:     a.cfg.Server.AdminPath,
		ClientID:      oidcClientID,
		ClientSecret:  clientSecret,
		SecureCookies: a.cfg.SecureCookies(),
		Registration:  a.cfg.Registration,
		Providers:     a.cfg.OAuthProviders,
		Logger:        a.logger,
	})
	if err != nil {
		return fmt.Errorf("start account center: %w", err)
	}
	a.idpServer = identityProvider
	defer a.shutdown()

	a.idpHTTP, err = startHTTP("account center", a.cfg.Server.Internal.IDP, identityProvider, "", "", a.logger)
	if err != nil {
		return err
	}
	if err := waitForHTTP(ctx, http.DefaultClient, a.cfg.IDPURL()+"/idp/healthz", 10*time.Second); err != nil {
		return err
	}

	idpURL, _ := url.Parse(a.cfg.IDPURL())
	headplaneURL, _ := url.Parse(a.cfg.HeadplaneURL())
	headscaleURL, _ := url.Parse(a.cfg.HeadscaleURL())
	router := proxy.New(a.cfg.Server.AdminPath, idpURL, headplaneURL, headscaleURL, a.logger)
	a.publicHTTP, err = startHTTP("public proxy", a.cfg.Server.Listen, router, a.cfg.Server.TLSCertFile, a.cfg.Server.TLSKeyFile, a.logger)
	if err != nil {
		return err
	}

	headscaleConfigPath := filepath.Join(generatedDir, "headscale.yaml")
	headscaleFiles, err := headscale.WriteConfig(headscaleConfigPath, headscale.Options{
		DataDir:        a.dataDir,
		PublicURL:      a.cfg.Server.BaseURL,
		HTTPListen:     a.cfg.Server.Internal.HeadscaleHTTP,
		GRPCListen:     a.cfg.Server.Internal.HeadscaleGRPC,
		MetricsListen:  a.cfg.Server.Internal.HeadscaleMetric,
		BaseDomain:     a.cfg.Headscale.BaseDomain,
		DERPURLs:       a.cfg.Headscale.DERPURLs,
		DNSNameservers: a.cfg.Headscale.DNSNameservers,
		Issuer:         a.cfg.Issuer(),
		ClientID:       oidcClientID,
		ClientSecret:   clientSecret,
	})
	if err != nil {
		return err
	}

	a.headscaleProc = supervisor.New(supervisor.Spec{
		Name:   "headscale",
		Path:   a.bundle.HeadscaleBinary,
		Args:   []string{"serve", "--config", headscaleFiles.Path},
		Dir:    filepath.Join(a.dataDir, "headscale"),
		Logger: a.logger,
	})
	if err := a.headscaleProc.Start(ctx); err != nil {
		return err
	}
	if err := waitForHTTP(ctx, http.DefaultClient, a.cfg.HeadscaleURL()+"/health", 90*time.Second); err != nil {
		return fmt.Errorf("Headscale did not become healthy: %w", err)
	}

	keyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	apiKey, err := headscale.EnsureAPIKey(
		keyCtx,
		a.bundle.HeadscaleBinary,
		headscaleFiles.Path,
		a.cfg.HeadscaleURL(),
		filepath.Join(secretsDir, "headscale-api-key"),
		a.logger,
	)
	cancel()
	if err != nil {
		return err
	}
	if err := headscale.EnsureDefaultPolicy(ctx, a.cfg.HeadscaleURL(), apiKey, a.logger); err != nil {
		return err
	}

	headplaneConfigPath := filepath.Join(generatedDir, "headplane.yaml")
	if err := headplane.WriteConfig(headplaneConfigPath, headplane.Options{
		Listen:          a.cfg.Server.Internal.Headplane,
		PublicURL:       a.cfg.Server.BaseURL,
		AdminPath:       a.cfg.Server.AdminPath,
		DataDir:         a.dataDir,
		HeadscaleURL:    a.cfg.HeadscaleURL(),
		HeadscaleConfig: headscaleFiles.Path,
		APIKey:          apiKey,
		CookieSecret:    cookieSecret,
		SecureCookies:   a.cfg.SecureCookies(),
		Issuer:          a.cfg.Issuer(),
		ClientID:        oidcClientID,
		ClientSecret:    clientSecret,
		DefaultRole:     a.cfg.Headplane.DefaultRole,
		PostLogoutURI:   a.cfg.PublicURL(a.cfg.Server.AdminPath + "/login?s=logout"),
	}); err != nil {
		return err
	}

	a.headplaneProc = supervisor.New(supervisor.Spec{
		Name: "headplane",
		Path: a.bundle.NodeBinary,
		Args: []string{filepath.Join("build", "server", "index.js")},
		Dir:  a.bundle.HeadplaneDir,
		Env: []string{
			"HEADPLANE_CONFIG_PATH=" + headplaneConfigPath,
			"HOME=" + a.dataDir,
			"NODE_ENV=production",
		},
		Logger: a.logger,
	})
	if err := a.headplaneProc.Start(ctx); err != nil {
		return err
	}
	headplaneHealth := a.cfg.HeadplaneURL() + a.cfg.Server.AdminPath + "/healthz"
	if err := waitForHTTP(ctx, http.DefaultClient, headplaneHealth, 90*time.Second); err != nil {
		return fmt.Errorf("Headplane did not become healthy: %w", err)
	}

	a.logger.Info("Headscale Console is ready",
		"account_center", a.cfg.PublicURL(config.DefaultIDPPath+"/"),
		"admin", a.cfg.PublicURL(a.cfg.Server.AdminPath+"/"),
		"headscale", a.cfg.Server.BaseURL,
	)

	serviceErrors := make(chan error, 2)
	go func() { serviceErrors <- a.publicHTTP.Wait() }()
	go func() { serviceErrors <- a.idpHTTP.Wait() }()
	select {
	case <-ctx.Done():
		a.logger.Info("shutdown signal received")
		return nil
	case err := <-serviceErrors:
		if err != nil {
			return fmt.Errorf("HTTP service stopped: %w", err)
		}
		return fmt.Errorf("HTTP service stopped unexpectedly")
	}
}

func (a *application) shutdown() {
	if a.headplaneProc != nil {
		if err := a.headplaneProc.Stop(10 * time.Second); err != nil {
			a.logger.Warn("could not stop Headplane cleanly", "error", err)
		}
	}
	if a.headscaleProc != nil {
		if err := a.headscaleProc.Stop(15 * time.Second); err != nil {
			a.logger.Warn("could not stop Headscale cleanly", "error", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a.publicHTTP != nil {
		_ = a.publicHTTP.Shutdown(shutdownCtx)
	}
	if a.idpHTTP != nil {
		_ = a.idpHTTP.Shutdown(shutdownCtx)
	}
	if a.idpServer != nil {
		if err := a.idpServer.Close(); err != nil {
			a.logger.Warn("could not close account center", "error", err)
		}
	}
}
