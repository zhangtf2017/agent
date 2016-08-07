package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/2qif49lt/agent/api"
	apiserver "github.com/2qif49lt/agent/api/server"
	"github.com/2qif49lt/agent/api/server/middleware"
	"github.com/2qif49lt/agent/api/server/router"
	systemrouter "github.com/2qif49lt/agent/api/server/router/system"
	"github.com/2qif49lt/agent/cfg"
	"github.com/2qif49lt/agent/daemon"
	"github.com/2qif49lt/agent/pkg/connections/tlsconfig"
	"github.com/2qif49lt/agent/pkg/jsonlog"
	"github.com/2qif49lt/agent/pkg/listeners"
	"github.com/2qif49lt/agent/pkg/pidfile"
	"github.com/2qif49lt/agent/pkg/signal"
	"github.com/2qif49lt/agent/pkg/system"
	"github.com/2qif49lt/agent/utils"
	"github.com/2qif49lt/agent/version"
	"github.com/2qif49lt/logrus"
	flag "github.com/2qif49lt/pflag"
)

const (
	daemonConfigFileFlag = "config-file"
)

// DaemonCli represents the daemon CLI.
type DaemonCli struct {
	*daemon.Config
	commonFlags *cfg.CommonFlags
	configFile  *string

	api *apiserver.Server
	d   *daemon.Daemon
}

// NewDaemonCli returns a pre-configured daemon CLI
func NewDaemonCli() *DaemonCli {
	// TODO(tiborvass): remove InstallFlags?
	daemonConfig := new(daemon.Config)

	daemonConfig.InstallFlags(flag.CommandLine)
	configFile := flag.CommandLine.String(daemonConfigFileFlag, "", "Daemon configuration file")

	return &DaemonCli{
		Config:      daemonConfig,
		commonFlags: cliflags.InitCommonFlags(),
		configFile:  configFile,
	}
}

func (cli *DaemonCli) start() (err error) {
	stopc := make(chan bool)
	defer close(stopc)

	// read config

	if cli.Config.Debug {
		utils.EnableDebug()
	}

	if utils.ExperimentalBuild() {
		logrus.Warn("Running experimental build")
	}

	if err := setDefaultUmask(); err != nil {
		return fmt.Errorf("Failed to set umask: %v", err)
	}

	if cli.Pidfile != "" {
		pf, err := pidfile.New(cli.Pidfile)
		if err != nil {
			return fmt.Errorf("Error starting daemon: %v", err)
		}
		defer func() {
			if err := pf.Remove(); err != nil {
				logrus.Error(err)
			}
		}()
	}

	serverConfig := &apiserver.Config{
		Logging:     true,
		SocketGroup: cli.Config.SocketGroup,
		Version:     verison.SRV_VERSION,
		EnableCors:  cli.Config.EnableCors,
		CorsHeaders: cli.Config.CorsHeaders,
	}

	if cli.Config.TLS {
		tlsOptions := tlsconfig.Options{
			CAFile:   cli.Config.CommonTLSOptions.CAFile,
			CertFile: cli.Config.CommonTLSOptions.CertFile,
			KeyFile:  cli.Config.CommonTLSOptions.KeyFile,
		}

		if cli.Config.TLSVerify {
			// server requires and verifies client's certificate
			tlsOptions.ClientAuth = tls.RequireAndVerifyClientCert
		}
		tlsConfig, err := tlsconfig.Server(tlsOptions)
		if err != nil {
			return err
		}
		serverConfig.TLSConfig = tlsConfig
	}

	if len(cli.Config.Hosts) == 0 {
		cli.Config.Hosts = make([]string, 1)
	}

	api := apiserver.New(serverConfig)
	cli.api = api

	for i := 0; i < len(cli.Config.Hosts); i++ {
		var err error

		protoAddr := cli.Config.Hosts[i]
		protoAddrParts := strings.SplitN(protoAddr, "://", 2)
		if len(protoAddrParts) != 2 {
			return fmt.Errorf("bad format %s, expected PROTO://ADDR", protoAddr)
		}

		proto := protoAddrParts[0]
		addr := protoAddrParts[1]

		// It's a bad idea to bind to TCP without tlsverify.
		if proto == "tcp" && (serverConfig.TLSConfig == nil || serverConfig.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert) {
			logrus.Warn("[!] DON'T BIND ON ANY IP ADDRESS WITHOUT setting -tlsverify IF YOU DON'T KNOW WHAT YOU'RE DOING [!]")
		}
		ls, err := listeners.Init(proto, addr, serverConfig.SocketGroup, serverConfig.TLSConfig)
		if err != nil {
			return err
		}
		ls = wrapListeners(proto, ls)
		// If we're binding to a TCP port, make sure that a container doesn't try to use it.
		if proto == "tcp" {
			if err := allocateDaemonPort(addr); err != nil {
				return err
			}
		}
		logrus.Debugf("Listener created for HTTP on %s (%s)", protoAddrParts[0], protoAddrParts[1])
		api.Accept(protoAddrParts[1], ls...)
	}

	if err := migrateKey(); err != nil {
		return err
	}
	cli.TrustKeyPath = cli.commonFlags.TrustKey

	registryService := registry.NewService(cli.Config.ServiceOptions)
	containerdRemote, err := libcontainerd.New(cli.getLibcontainerdRoot(), cli.getPlatformRemoteOptions()...)
	if err != nil {
		return err
	}
	cli.api = api
	signal.Trap(func() {
		cli.stop()
		<-stopc // wait for daemonCli.start() to return
	})

	if err := pluginInit(cli.Config, containerdRemote, registryService); err != nil {
		return err
	}

	d, err := daemon.NewDaemon(cli.Config, registryService, containerdRemote)
	if err != nil {
		return fmt.Errorf("Error starting daemon: %v", err)
	}

	name, _ := os.Hostname()

	c, err := cluster.New(cluster.Config{
		Root:    cli.Config.Root,
		Name:    name,
		Backend: d,
	})
	if err != nil {
		logrus.Fatalf("Error creating cluster component: %v", err)
	}

	logrus.Info("Daemon has completed initialization")

	logrus.WithFields(logrus.Fields{
		"version":     dockerversion.Version,
		"commit":      dockerversion.GitCommit,
		"graphdriver": d.GraphDriverName(),
	}).Info("Docker daemon")

	cli.initMiddlewares(api, serverConfig)
	initRouter(api, d, c)

	cli.d = d
	cli.setupConfigReloadTrap()

	// The serve API routine never exits unless an error occurs
	// We need to start it as a goroutine and wait on it so
	// daemon doesn't exit
	serveAPIWait := make(chan error)
	go api.Wait(serveAPIWait) // 开始服务

	// after the daemon is done setting up we can notify systemd api
	notifySystem()

	// Daemon is fully initialized and handling API traffic
	// Wait for serve API to complete
	errAPI := <-serveAPIWait
	c.Cleanup()
	shutdownDaemon(d, 15)
	containerdRemote.Cleanup()
	if errAPI != nil {
		return fmt.Errorf("Shutting down due to ServeAPI error: %v", errAPI)
	}

	return nil
}

func (cli *DaemonCli) reloadConfig() {
	reload := func(config *daemon.Config) {
		if err := cli.d.Reload(config); err != nil {
			logrus.Errorf("Error reconfiguring the daemon: %v", err)
			return
		}
		if config.IsValueSet("debug") {
			debugEnabled := utils.IsDebugEnabled()
			switch {
			case debugEnabled && !config.Debug: // disable debug
				utils.DisableDebug()
				cli.api.DisableProfiler()
			case config.Debug && !debugEnabled: // enable debug
				utils.EnableDebug()
				cli.api.EnableProfiler()
			}

		}
	}

	if err := daemon.ReloadConfiguration(*cli.configFile, flag.CommandLine, reload); err != nil {
		logrus.Error(err)
	}
}

func (cli *DaemonCli) stop() {
	cli.api.Close()
}

// shutdownDaemon just wraps daemon.Shutdown() to handle a timeout in case
// d.Shutdown() is waiting too long to kill container or worst it's
// blocked there
func shutdownDaemon(d *daemon.Daemon, timeout time.Duration) {
	ch := make(chan struct{})
	go func() {
		d.Shutdown()
		close(ch)
	}()
	select {
	case <-ch:
		logrus.Debug("Clean shutdown succeeded")
	case <-time.After(timeout * time.Second):
		logrus.Error("Force shutdown daemon")
	}
}

func loadDaemonCliConfig(config *daemon.Config, flags *flag.FlagSet, commonConfig *cliflags.CommonFlags, configFile string) (*daemon.Config, error) {
	config.Debug = commonConfig.Debug
	config.Hosts = commonConfig.Hosts
	config.LogLevel = commonConfig.LogLevel
	config.TLS = commonConfig.TLS
	config.TLSVerify = commonConfig.TLSVerify
	config.CommonTLSOptions = daemon.CommonTLSOptions{}

	if commonConfig.TLSOptions != nil {
		config.CommonTLSOptions.CAFile = commonConfig.TLSOptions.CAFile
		config.CommonTLSOptions.CertFile = commonConfig.TLSOptions.CertFile
		config.CommonTLSOptions.KeyFile = commonConfig.TLSOptions.KeyFile
	}

	if configFile != "" {
		c, err := daemon.MergeDaemonConfigurations(config, flags, configFile)
		if err != nil {
			if flags.IsSet(daemonConfigFileFlag) || !os.IsNotExist(err) {
				return nil, fmt.Errorf("unable to configure the Docker daemon with file %s: %v\n", configFile, err)
			}
		}
		// the merged configuration can be nil if the config file didn't exist.
		// leave the current configuration as it is if when that happens.
		if c != nil {
			config = c
		}
	}

	if err := daemon.ValidateConfiguration(config); err != nil {
		return nil, err
	}

	// Regardless of whether the user sets it to true or false, if they
	// specify TLSVerify at all then we need to turn on TLS
	if config.IsValueSet(cliflags.TLSVerifyKey) {
		config.TLS = true
	}

	// ensure that the log level is the one set after merging configurations
	cliflags.SetDaemonLogLevel(config.LogLevel)

	return config, nil
}

func initRouter(s *apiserver.Server, d *daemon.Daemon, c *cluster.Cluster) {
	decoder := runconfig.ContainerDecoder{}

	routers := []router.Router{
		container.NewRouter(d, decoder),
		image.NewRouter(d, decoder),
		systemrouter.NewRouter(d, c),
		volume.NewRouter(d),
		build.NewRouter(dockerfile.NewBuildManager(d)),
		swarmrouter.NewRouter(c),
	}
	if d.NetworkControllerEnabled() {
		routers = append(routers, network.NewRouter(d, c))
	}
	routers = addExperimentalRouters(routers)

	s.InitRouter(utils.IsDebugEnabled(), routers...)
}

func (cli *DaemonCli) initMiddlewares(s *apiserver.Server, cfg *apiserver.Config) {
	v := cfg.Version

	vm := middleware.NewVersionMiddleware(v, api.DefaultVersion, api.MinVersion)
	s.UseMiddleware(vm)

	if cfg.EnableCors {
		c := middleware.NewCORSMiddleware(cfg.CorsHeaders)
		s.UseMiddleware(c)
	}

	u := middleware.NewUserAgentMiddleware(v)
	s.UseMiddleware(u)

	if len(cli.Config.AuthorizationPlugins) > 0 {
		authZPlugins := authorization.NewPlugins(cli.Config.AuthorizationPlugins)
		handleAuthorization := authorization.NewMiddleware(authZPlugins)
		s.UseMiddleware(handleAuthorization)
	}
}