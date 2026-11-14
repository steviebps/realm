package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/steviebps/realm/helper/logging"
	realmhttp "github.com/steviebps/realm/http"
	storage "github.com/steviebps/realm/pkg/storage"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts realm server",
	Long:  "Starts realm server",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if err := runServer(cmd); err != nil {
			// Logged here rather than printed by cobra, so startup failures
			// stay in the server's structured log.
			logging.Ctx(ctx).ErrorCtx(ctx).Msg(err.Error())
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			return err
		}
		return nil
	},
}

// emptyChamber is the JSON of a chamber with no flags.
var emptyChamber = []byte(`{"rules":{}}`)

// runServer serves until a shutdown signal, returning startup and serving
// errors so deferred cleanup and the telemetry flush in Execute still run.
func runServer(cmd *cobra.Command) error {
	ctx := cmd.Context()
	flags := cmd.Flags()
	debug, _ := flags.GetBool("debug")
	devMode, _ := flags.GetBool("dev")
	logger := logging.Ctx(ctx)

	realmConfig := NewDefaultServerConfig()
	if !devMode {
		var configPath string
		var err error
		if realmConfig, configPath, err = loadConfig(cmd); err != nil {
			return err
		}
		if configPath == "" {
			return fmt.Errorf("%w (or use --dev)", errNoConfig())
		}
		logger.InfoCtx(ctx).Str("config", configPath).Msg("using config file")
	}
	serverConfig := realmConfig.Server

	if serverConfig.LogLevel != "" && !debug {
		level, err := zerolog.ParseLevel(serverConfig.LogLevel)
		if err != nil {
			return fmt.Errorf("invalid logLevel %q", serverConfig.LogLevel)
		}
		zerolog.SetGlobalLevel(level)
	}

	var shutdownDelay time.Duration
	if serverConfig.ShutdownDelay != "" {
		var err error
		if shutdownDelay, err = time.ParseDuration(serverConfig.ShutdownDelay); err != nil || shutdownDelay < 0 {
			return fmt.Errorf("invalid shutdownDelay %q: want a duration such as \"5s\"", serverConfig.ShutdownDelay)
		}
	}

	portStr, _ := flags.GetString("port")
	if portStr == "" {
		portStr = serverConfig.Port
	}
	if portStr == "" {
		portStr = defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %q: want a number from 1 to 65535", portStr)
	}

	if (serverConfig.CertFile == "") != (serverConfig.KeyFile == "") {
		return errors.New("certFile must be used in conjunction with keyFile")
	}
	var certs *certReloader
	if serverConfig.CertFile != "" {
		// load now, so a bad certificate stops startup
		if certs, err = newCertReloader(ctx, serverConfig.CertFile, serverConfig.KeyFile); err != nil {
			return err
		}
	}

	logger.InfoCtx(ctx).Str("port", portStr).Str("certFile", serverConfig.CertFile).Str("keyFile", serverConfig.KeyFile).Str("storage", serverConfig.StorageType).Bool("inheritable", serverConfig.Inheritable).Bool("debug", debug).Msg("server options")

	if len(serverConfig.StorageOptions) > 0 {
		logger.DebugCtx(ctx).Any("options", redactOptions(serverConfig.StorageOptions)).Msg("storage options")
	}

	stg, err := openStorage(serverConfig)
	if err != nil {
		return err
	}
	defer stg.Close(ctx)

	if serverConfig.Inheritable {
		if stg, err = storage.NewInheritableStorage(stg); err != nil {
			return err
		}
	}

	// Create the root chamber unless it exists; servers starting together
	// against shared storage create it once.
	if err := storage.Update(ctx, stg, []string{"/"}, func(cur map[string]*storage.StorageEntry) ([]storage.Op, error) {
		if cur["/"] != nil {
			return nil, nil
		}
		logger.DebugCtx(ctx).Msg("initializing root entry in storage")
		return []storage.Op{{Key: "/", Value: emptyChamber}}, nil
	}); err != nil {
		return fmt.Errorf("failed to initialize root entry during startup: %w", err)
	}

	switch {
	case serverConfig.Auth == nil:
		logger.WarnCtx(ctx).Msg("AUTHENTICATION IS DISABLED: anyone who can reach this server can read and change every chamber. Add an \"auth\" block to the server config to require sign-in and API keys.")
	case len(serverConfig.Auth.Providers) == 0:
		logger.InfoCtx(ctx).Msg("no sign-in providers are configured: only API keys authenticate, and nobody can sign in to the UI")
	}

	draining := make(chan struct{})
	if serverConfig.MaxStreams < 0 {
		return fmt.Errorf("invalid maxStreams %d: want a positive number, or 0 for the default", serverConfig.MaxStreams)
	}
	if serverConfig.MaxSubtreeChambers < 0 {
		return fmt.Errorf("invalid maxSubtreeChambers %d: want a positive number, or 0 for the default", serverConfig.MaxSubtreeChambers)
	}
	handler, err := realmhttp.NewHandler(ctx, realmhttp.HandlerConfig{Storage: stg, RequestTimeout: realmhttp.DefaultHandlerTimeout, Auth: serverConfig.Auth, Draining: draining, MaxStreams: serverConfig.MaxStreams, MaxSubtreeChambers: serverConfig.MaxSubtreeChambers})
	if err != nil {
		return err
	}

	// Requests run under baseCtx, which shutdown cancels: watch streams
	// never go idle on their own, so Shutdown would otherwise wait for them
	// until the process is killed.
	baseCtx, cancelBase := context.WithCancel(ctx)
	defer cancelBase()
	// Request bodies and responses are bounded per request by the handler
	// (ReadTimeout and WriteTimeout would also cut off watch streams).
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          serverErrorLog(logger),
	}
	if certs != nil {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate}
	}
	server.RegisterOnShutdown(cancelBase)

	served := make(chan error, 1)
	go func() {
		logger.InfoCtx(ctx).Msgf("Listening on port %d", port)
		if certs == nil {
			served <- server.ListenAndServe()
		} else {
			// the certificate comes from TLSConfig.GetCertificate
			served <- server.ListenAndServeTLS("", "")
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-served:
		return err
	case sig := <-sigChan:
		logger.InfoCtx(ctx).Str("signal", sig.String()).Dur("delay", shutdownDelay).Msg("shutting down")
	}

	// Fail readiness first, and keep serving while load balancers notice.
	close(draining)
	time.Sleep(shutdownDelay)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.ErrorCtx(ctx).Str("error", err.Error()).Msg("server did not shut down cleanly")
	}
	return nil
}

// shutdownTimeout bounds how long in-flight requests may finish after a
// shutdown signal.
const shutdownTimeout = 20 * time.Second

// defaultPort is the port the server listens on when neither --port nor the
// config names one.
const defaultPort = "8080"

// serverErrorLog routes what net/http logs itself (failed TLS handshakes,
// accept errors, misuse of a ResponseWriter) into the structured log.
func serverErrorLog(logger *logging.TracedLogger) *log.Logger {
	return log.New(serverLogWriter{logger}, "", 0)
}

type serverLogWriter struct{ logger *logging.TracedLogger }

func (w serverLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	event := w.logger.Warn()
	// clients that hang up mid-handshake (port scanners, TCP health checks)
	// are routine
	if strings.HasPrefix(msg, "http: TLS handshake error") || strings.Contains(msg, "error reading preface") {
		event = w.logger.Debug()
	}
	event.Str("source", "net/http").Msg(msg)
	return len(p), nil
}

// certCheckInterval is how often the server checks its certificate files for
// a renewed certificate.
var certCheckInterval = time.Minute

// certReloader serves the TLS certificate in certFile and keyFile, and loads
// it again when either file changes, so renewed certificates (from
// cert-manager or an ACME client, say) take effect without a restart.
type certReloader struct {
	certFile, keyFile string
	logger            *logging.TracedLogger

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

// newCertReloader loads the certificate in certFile and keyFile.
func newCertReloader(ctx context.Context, certFile, keyFile string) (*certReloader, error) {
	c := &certReloader{certFile: certFile, keyFile: keyFile, logger: logging.Ctx(ctx)}
	modTime, err := c.lastModified()
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("could not load the TLS certificate: %w", err)
	}
	c.cert, c.modTime, c.checked = &cert, modTime, time.Now()
	return c, nil
}

// lastModified returns when the certificate or its key last changed.
func (c *certReloader) lastModified() (time.Time, error) {
	var latest time.Time
	for _, name := range []string{c.certFile, c.keyFile} {
		fi, err := os.Stat(name)
		if err != nil {
			return time.Time{}, fmt.Errorf("could not read the TLS certificate: %w", err)
		}
		if fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	return latest, nil
}

// GetCertificate implements tls.Config.GetCertificate. While the files cannot
// be read or do not hold a matching pair (in the middle of a renewal, say),
// it keeps serving the certificate it has.
func (c *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) < certCheckInterval {
		return c.cert, nil
	}
	c.checked = time.Now()
	modTime, err := c.lastModified()
	if err != nil {
		c.logger.Warn().Str("error", err.Error()).Msg("keeping the current TLS certificate")
		return c.cert, nil
	}
	if modTime.Equal(c.modTime) {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		c.logger.Warn().Str("error", err.Error()).Msg("keeping the current TLS certificate")
		return c.cert, nil
	}
	c.cert, c.modTime = &cert, modTime
	c.logger.Info().Str("certFile", c.certFile).Msg("loaded a renewed TLS certificate")
	return c.cert, nil
}

func init() {
	serverCmd.Flags().String("port", "", "port to run server on")
	serverCmd.Flags().Bool("dev", false, "run server in dev mode")
	rootCmd.AddCommand(serverCmd)
}

// openStorage creates the storage backend named by the server config, without
// the inheritance wrapper.
func openStorage(c ServerConfig) (storage.Storage, error) {
	create, exists := storage.StorageOptions[c.StorageType]
	if !exists {
		return nil, fmt.Errorf("storage type %q does not exist", c.StorageType)
	}
	return create(c.StorageOptions)
}

// redactOptions returns storage options safe to log: credentials inside
// connection URLs (such as a Postgres url's password) are masked.
func redactOptions(options map[string]string) map[string]string {
	out := make(map[string]string, len(options))
	for k, v := range options {
		if u, err := url.Parse(v); err == nil && u.User != nil {
			v = u.Redacted()
		} else if strings.Contains(v, "password=") {
			// a keyword/value connection string
			v = "xxxxx"
		}
		out[k] = v
	}
	return out
}
