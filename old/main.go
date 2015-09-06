package old

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/cloudfoundry/gunk/command_runner"
	"github.com/cloudfoundry/gunk/localip"
	"github.com/docker/docker/daemon/graphdriver"
	_ "github.com/docker/docker/daemon/graphdriver/aufs"
	_ "github.com/docker/docker/daemon/graphdriver/vfs"
	"github.com/docker/docker/graph"
	"github.com/docker/docker/registry"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/garden-linux/container_pool"
	"github.com/cloudfoundry-incubator/garden-linux/container_repository"
	"github.com/cloudfoundry-incubator/garden-linux/linux_backend"
	"github.com/cloudfoundry-incubator/garden-linux/network"
	"github.com/cloudfoundry-incubator/garden-linux/network/bridgemgr"
	"github.com/cloudfoundry-incubator/garden-linux/network/devices"
	"github.com/cloudfoundry-incubator/garden-linux/network/iptables"
	"github.com/cloudfoundry-incubator/garden-linux/network/subnets"
	"github.com/cloudfoundry-incubator/garden-linux/old/port_pool"
	"github.com/cloudfoundry-incubator/garden-linux/old/quota_manager"
	"github.com/cloudfoundry-incubator/garden-linux/old/repository_fetcher"
	"github.com/cloudfoundry-incubator/garden-linux/old/rootfs_provider"
	"github.com/cloudfoundry-incubator/garden-linux/old/sysconfig"
	"github.com/cloudfoundry-incubator/garden-linux/old/system_info"
	"github.com/cloudfoundry-incubator/garden-linux/old/uid_pool"
	"github.com/cloudfoundry-incubator/garden/server"
	"github.com/cloudfoundry/dropsonde"
	"github.com/cloudfoundry/gunk/command_runner/linux_command_runner"
)

const (
	DefaultNetworkPool = "10.254.0.0/22"
	DefaultMTUSize     = 1500
)

var listenNetwork = flag.String(
	"listenNetwork",
	"unix",
	"how to listen on the address (unix, tcp, etc.)",
)

var listenAddr = flag.String(
	"listenAddr",
	"/tmp/garden.sock",
	"address to listen on",
)

var snapshotsPath = flag.String(
	"snapshots",
	"",
	"directory in which to store container state to persist through restarts",
)

var binPath = flag.String(
	"bin",
	"",
	"directory containing backend-specific scripts (i.e. ./create.sh)",
)

var depotPath = flag.String(
	"depot",
	"",
	"directory in which to store containers",
)

var overlaysPath = flag.String(
	"overlays",
	"",
	"directory in which to store containers mount points",
)

var rootFSPath = flag.String(
	"rootfs",
	"",
	"directory of the rootfs for the containers",
)

var disableQuotas = flag.Bool(
	"disableQuotas",
	false,
	"disable disk quotas",
)

var containerGraceTime = flag.Duration(
	"containerGraceTime",
	0,
	"time after which to destroy idle containers",
)

var portPoolStart = flag.Uint(
	"portPoolStart",
	61001,
	"start of ephemeral port range used for mapped container ports",
)

var portPoolSize = flag.Uint(
	"portPoolSize",
	5000,
	"size of port pool used for mapped container ports",
)

var uidPoolStart = flag.Uint(
	"uidPoolStart",
	10000,
	"start of per-container user ids",
)

var uidPoolSize = flag.Uint(
	"uidPoolSize",
	256,
	"size of the uid pool",
)

var networkPool = flag.String("networkPool",
	DefaultNetworkPool,
	"Pool of dynamically allocated container subnets")

var denyNetworks = flag.String(
	"denyNetworks",
	"",
	"CIDR blocks representing IPs to blacklist",
)

var allowNetworks = flag.String(
	"allowNetworks",
	"",
	"CIDR blocks representing IPs to whitelist",
)

var graphRoot = flag.String(
	"graph",
	"/var/lib/garden-docker-graph",
	"docker image graph",
)

var dockerRegistry = flag.String(
	"registry",
	registry.IndexServerAddress(),
	"docker registry API endpoint",
)

var insecureRegistries = flag.String(
	"insecureDockerRegistryList",
	"",
	"comma-separated list of docker registries to allow connection to even if they are not secure",
)

var tag = flag.String(
	"tag",
	"",
	"server-wide identifier used for 'global' configuration, must be less than 3 character long",
)

var dropsondeOrigin = flag.String(
	"dropsondeOrigin",
	"garden-linux",
	"Origin identifier for dropsonde-emitted metrics.",
)

var dropsondeDestination = flag.String(
	"dropsondeDestination",
	"localhost:3457",
	"Destination for dropsonde-emitted metrics.",
)

var allowHostAccess = flag.Bool(
	"allowHostAccess",
	false,
	"allow network access to host",
)

var iptablesLogMethod = flag.String(
	"iptablesLogMethod",
	"kernel",
	"type of iptable logging to use, one of 'kernel' or 'nflog' (default: kernel)",
)

var mtu = flag.Int(
	"mtu",
	DefaultMTUSize,
	"MTU size for container network interfaces")

var externalIP = flag.String(
	"externalIP",
	"",
	"IP address to use to reach container's mapped ports")

/***********************container directy network support*******************************/
var hostIfname = flag.String(
	"hostIfname",
	"",
	"specify physic ifname in the host")

var hostBrname = flag.String(
	"hostBrname",
	"",
	"specify bridge name which is configed follow hostIfname")

/***********************container directy network support*******************************/

func Main() {

	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())

	logger, reconfigurableSink := cf_lager.New("garden-linux")
	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		cf_debug_server.Run(dbgAddr, reconfigurableSink)
	}

	initializeDropsonde(logger)

	if *binPath == "" {
		missing("-bin")
	}

	if *depotPath == "" {
		missing("-depot")
	}

	if *overlaysPath == "" {
		missing("-overlays")
	}

	if len(*tag) > 2 {
		println("-tag parameter must be less than 3 characters long")
		println()
		flag.Usage()
		return
	}

	uidPool := uid_pool.New(uint32(*uidPoolStart), uint32(*uidPoolSize))

	_, dynamicRange, _ := net.ParseCIDR(*networkPool)
	subnetPool, _ := subnets.NewSubnets(dynamicRange)

	// TODO: use /proc/sys/net/ipv4/ip_local_port_range by default (end + 1)
	portPool := port_pool.New(uint32(*portPoolStart), uint32(*portPoolSize))

	useKernelLogging := true
	switch *iptablesLogMethod {
	case "nflog":
		useKernelLogging = false
	case "kernel":
		/* noop */
	default:
		println("-iptablesLogMethod value not recognized")
		println()
		flag.Usage()
		return
	}

	config := sysconfig.NewConfig(*tag, *allowHostAccess)

	runner := sysconfig.NewRunner(config, linux_command_runner.New())

	quotaManager := quota_manager.New(runner, getMountPoint(logger, *depotPath), *binPath)

	if *disableQuotas {
		quotaManager.Disable()
	}

	if err := os.MkdirAll(*graphRoot, 0755); err != nil {
		logger.Fatal("failed-to-create-graph-directory", err)
	}

	graphDriver, err := graphdriver.New(*graphRoot, nil)
	if err != nil {
		logger.Fatal("failed-to-construct-graph-driver", err)
	}

	graph, err := graph.NewGraph(*graphRoot, graphDriver)
	if err != nil {
		logger.Fatal("failed-to-construct-graph", err)
	}

	repoFetcher := repository_fetcher.Retryable{
		repository_fetcher.New(
			repository_fetcher.NewRepositoryProvider(
				*dockerRegistry,
				strings.Split(*insecureRegistries, ","),
			),
			graph,
		),
	}

	dockerRootFSProvider, err := rootfs_provider.NewDocker(repoFetcher, graphDriver, rootfs_provider.SimpleVolumeCreator{}, clock.NewClock())
	if err != nil {
		logger.Fatal("failed-to-construct-docker-rootfs-provider", err)
	}

	rootFSProviders := map[string]rootfs_provider.RootFSProvider{
		"":       rootfs_provider.NewOverlay(*binPath, *overlaysPath, *rootFSPath, runner),
		"docker": dockerRootFSProvider,
	}

	filterProvider := &provider{
		useKernelLogging: useKernelLogging,
		chainPrefix:      config.IPTables.Filter.InstancePrefix,
		runner:           runner,
		log:              logger,
	}

	if *externalIP == "" {
		ip, err := localip.LocalIP()
		if err != nil {
			panic("couldn't determine local IP to use for -externalIP parameter. You can use the -externalIP flag to pass an external IP")
		}

		externalIP = &ip
	}

	parsedExternalIP := net.ParseIP(*externalIP)
	if parsedExternalIP == nil {
		panic(fmt.Sprintf("Value of -externalIP %s could not be converted to an IP", *externalIP))
	}

	pool := container_pool.New(
		logger,
		*binPath,
		*depotPath,
		config,
		rootFSProviders,
		uidPool,
		parsedExternalIP,
		*mtu,
		subnetPool,
		bridgemgr.New("w"+config.Tag+"b-", &devices.Bridge{}, &devices.Link{}),
		filterProvider,
		iptables.NewGlobalChain(config.IPTables.Filter.DefaultChain, runner, logger.Session("global-chain")),
		portPool,
		strings.Split(*denyNetworks, ","),
		strings.Split(*allowNetworks, ","),
		runner,
		quotaManager,
		*hostIfname,
		*hostBrname,
	)

	systemInfo := system_info.NewProvider(*depotPath)

	backend := linux_backend.New(logger, pool, container_repository.New(), systemInfo, *snapshotsPath)

	err = backend.Setup()
	if err != nil {
		logger.Fatal("failed-to-set-up-backend", err)
	}

	graceTime := *containerGraceTime

	gardenServer := server.New(*listenNetwork, *listenAddr, graceTime, backend, logger)

	err = gardenServer.Start()
	if err != nil {
		logger.Fatal("failed-to-start-server", err)
	}

	signals := make(chan os.Signal, 1)

	go func() {
		<-signals
		gardenServer.Stop()
		os.Exit(0)
	}()

	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	logger.Info("started", lager.Data{
		"network": *listenNetwork,
		"addr":    *listenAddr,
	})

	select {}
}

func getMountPoint(logger lager.Logger, depotPath string) string {
	dfOut := new(bytes.Buffer)

	df := exec.Command("df", depotPath)
	df.Stdout = dfOut
	df.Stderr = os.Stderr

	err := df.Run()
	if err != nil {
		logger.Fatal("failed-to-get-mount-info", err)
	}

	dfOutputWords := strings.Split(string(dfOut.Bytes()), " ")

	return strings.Trim(dfOutputWords[len(dfOutputWords)-1], "\n")
}

func missing(flagName string) {
	println("missing " + flagName)
	println()
	flag.Usage()
}

func initializeDropsonde(logger lager.Logger) {
	err := dropsonde.Initialize(*dropsondeDestination, *dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

type provider struct {
	useKernelLogging bool
	chainPrefix      string
	runner           command_runner.CommandRunner
	log              lager.Logger
}

func (p *provider) ProvideFilter(containerId string) network.Filter {
	return network.NewFilter(iptables.NewLoggingChain(p.chainPrefix+containerId, p.useKernelLogging, p.runner, p.log.Session(containerId).Session("filter")))
}
