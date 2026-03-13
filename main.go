package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"sync-envoy/pkg/envoy"
	"sync-envoy/pkg/file"
	"sync-envoy/pkg/k8s"
	"sync-envoy/pkg/logging"
	"sync-envoy/pkg/provider"
	"sync-envoy/pkg/xcp"
)

var (
	dir              string
	namespace        string
	workloadSelector string
	logLevelStr      string
	providerFilter   string
	csdsAddress      string
	istioconfigsPath = "istioconfigs"
	xcpconfigsPath   = "xcpconfigs"
)

const pidFile = "/tmp/sync-envoy.pid"

func main() {
	rootCmd := &cobra.Command{
		Use:   "sync-envoy",
		Short: "Sync Envoy configs and Istio CRs to/from local files",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if !logging.SetLevel(logLevelStr) {
				log.Fatalf("Invalid log level: %s", logLevelStr)
			}
			if dir == "" {
				dir = "envoyconfigs"
			}
			os.MkdirAll(dir, 0755)
			os.MkdirAll(istioconfigsPath, 0755)
			os.MkdirAll(xcpconfigsPath, 0755)
		},
	}

	rootCmd.PersistentFlags().StringVar(&dir, "dir", "envoyconfigs", "Directory to store Envoy configs")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Namespace filter")
	rootCmd.PersistentFlags().StringVarP(&workloadSelector, "workload-selector", "w", "", "Workload selector (e.g., app=httpbin)")
	rootCmd.PersistentFlags().StringVar(&logLevelStr, "log-level", "info", "Log level: debug, info, warn, error")
	rootCmd.PersistentFlags().StringVar(&providerFilter, "provider", "", "Comma-separated list of providers to enable (default: all). Options: kubernetes,file,envoy,xcp,xcp-file")
	rootCmd.PersistentFlags().StringVar(&csdsAddress, "csds-address", "", "istiod gRPC address for CSDS streaming (e.g., localhost:15010). If empty, falls back to admin/istioctl polling")

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the sync process (runs until interrupted)",
		Run:   runStart,
	}
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running sync process",
		Run:   runStop,
	}
	cleanupCmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete all generated files in envoyconfigs and istioconfigs",
		Run:   runCleanup,
	}

	howCmd := &cobra.Command{
		Use:   "how",
		Short: "Explain what sync-envoy does and how it works",
		Run:   runHow,
	}

	rootCmd.AddCommand(startCmd, stopCmd, cleanupCmd, howCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runStart(cmd *cobra.Command, args []string) {
	// Initialize K8s clients
	clients, err := k8s.NewClients()
	if err != nil {
		log.Fatalf("Failed to initialize Kubernetes clients: %v", err)
	}

	// Write PID file
	pid := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0644); err != nil {
		log.Fatalf("Failed to write PID file: %v", err)
	}
	defer os.Remove(pidFile)

	logging.Info("Starting sync-envoy with configuration:")
	logging.Info("  envoy dir: %s", dir)
	logging.Info("  istioconfigs dir: %s", istioconfigsPath)
	logging.Info("  xcpconfigs dir: %s", xcpconfigsPath)
	logging.Info("  namespace: %s", namespace)
	logging.Info("  workload selector: %s", workloadSelector)
	logging.Info("  provider filter: %s", providerFilter)
	logging.Info("  csds address: %s", csdsAddress)
	logging.Info("  log level: %s", logLevelStr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logging.Info("Shutting down...")
		cancel()
	}()

	// Build provider registry
	registry := provider.NewRegistry()

	// Kubernetes provider: watches K8s CRs, writes _current.yaml files
	registry.Register(provider.New(
		"kubernetes",
		k8s.NewCRWatcher(clients),
		file.NewCurrentFileUpdater(istioconfigsPath).WithSelectorCorrelation(clients, dir),
	))

	// File provider: watches _desired.yaml files, applies to K8s cluster
	registry.Register(provider.New(
		"file",
		file.NewDesiredFileWatcher(istioconfigsPath),
		k8s.NewCRUpdater(clients),
	))

	// Envoy provider: reads envoy configs, writes JSON files
	registry.Register(provider.New(
		"envoy",
		envoy.NewEnvoyWatcher(namespace, workloadSelector, csdsAddress),
		envoy.NewFileUpdater(dir, "last_updated").WithIstioconfigsPath(istioconfigsPath),
	))

	// XCP provider: watches XCP CRDs, writes _current.yaml + XCP→Istio correlation
	registry.Register(provider.New(
		"xcp",
		xcp.NewXCPWatcher(clients),
		xcp.NewXCPFileUpdater(xcpconfigsPath, istioconfigsPath, clients),
	))

	// XCP file provider: watches _desired.yaml in xcpconfigs/, applies to K8s cluster
	registry.Register(provider.New(
		"xcp-file",
		file.NewDesiredFileWatcher(xcpconfigsPath),
		k8s.NewCRUpdater(clients),
	))

	// Resolve which providers to run
	providers, err := registry.Get(providerFilter)
	if err != nil {
		log.Fatalf("Failed to resolve providers: %v", err)
	}

	logging.Info("Running %d provider(s)", len(providers))
	for _, p := range providers {
		logging.Info("  - %s", p.Name())
	}

	// Run all providers concurrently, block until ctx is cancelled
	provider.RunAll(ctx, providers)
}

func runStop(cmd *cobra.Command, args []string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		log.Fatalf("No PID file found (is sync-envoy running?): %v", err)
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		log.Fatalf("Failed to stop process: %v", err)
	}
	logging.Info("Sent SIGTERM to process %d", pid)
}

func runHow(cmd *cobra.Command, args []string) {
	const (
		reset  = "\033[0m"
		bold   = "\033[1m"
		dim    = "\033[2m"
		cyan   = "\033[36m"
		yellow = "\033[33m"
		green  = "\033[32m"
		blue   = "\033[34m"
		magenta = "\033[35m"
	)
	p := func(format string, a ...interface{}) { fmt.Printf(format+"\n", a...) }
	hr := func(label string) {
		p("\n%s%s── %s %s──────────────────────────────────────────%s", bold, dim, reset+bold, dim, reset)
		p("%s  %s%s", bold+cyan, label, reset)
	}

	p("")
	p("%s╔══════════════════════════════════════════════════════╗%s", bold+cyan, reset)
	p("%s║               sync-envoy                            ║%s", bold+cyan, reset)
	p("%s╚══════════════════════════════════════════════════════╝%s", bold+cyan, reset)
	p("")
	p("  A bidirectional sync tool for %sIstio CRs%s and %sEnvoy sidecar state%s.", bold, reset, bold, reset)
	p("  Three concurrent providers, each with a watcher (event producer)")
	p("  and an updater (event consumer).")

	hr("PROVIDERS")

	p("")
	p("  %s[kubernetes]%s  K8s CRs → files", bold+green, reset)
	p("  %sWatcher%s  Dynamic informers for 11 Istio CRD types:", bold, reset)
	p("           %sVirtualService, DestinationRule, Gateway, ServiceEntry,%s", dim, reset)
	p("           %sWorkloadEntry, WorkloadGroup, AuthorizationPolicy,%s", dim, reset)
	p("           %sPeerAuthentication, RequestAuthentication, Telemetry, WasmPlugin%s", dim, reset)
	p("  %sUpdater%s  Writes  %sistioconfigs/<ns>/<kind>/<name>_current.yaml%s", bold, reset, yellow, reset)
	p("           Skips write when content is unchanged (LCS diff check)")

	p("")
	p("  %s[file]%s  _desired.yaml → K8s cluster", bold+green, reset)
	p("  %sWatcher%s  fsnotify watches %sistioconfigs/%s recursively", bold, reset, yellow, reset)
	p("           Plain .yaml files are %sauto-renamed%s to _desired.yaml", bold, reset)
	p("           _current.yaml files are ignored")
	p("  %sUpdater%s  Parses YAML → k8s dynamic client Create/Update", bold, reset)
	p("           Fetches current cluster state, skips apply when identical")

	p("")
	p("  %s[envoy]%s  Envoy sidecars → files", bold+green, reset)
	p("  %sWatcher%s  Three strategies, tried in order:", bold, reset)
	p("")
	p("    %s1. CSDS gRPC streaming%s  %s(--csds-address istiod:15010)%s", bold+magenta, reset, dim, reset)
	p("       Long-running gRPC client to istiod; receives push updates.")
	p("       Uses go-control-plane protobuf types.")
	p("")
	p("    %s2. Admin endpoint polling%s  %s(every 5 s, fallback)%s", bold+magenta, reset, dim, reset)
	p("       %skubectl exec <pod> -c istio-proxy -- curl localhost:15000/config_dump%s", dim, reset)
	p("       Extracts: listener, cluster, route, endpoint, bootstrap, secret")
	p("")
	p("    %s3. istioctl proxy-config polling%s  %s(every 5 s, final fallback)%s", bold+magenta, reset, dim, reset)
	p("       %sistioctl proxy-config <type> <pod> -n <ns> -o json%s", dim, reset)
	p("")
	p("  %sUpdater%s  Writes  %senvoyconfigs/<ns>/<pod>/<type>.json%s", bold, reset, yellow, reset)
	p("           JSON includes: last_updated, pod_name, namespace, config_type, config")
	p("           Ignores %slast_updated%s when diffing (timestamp noise suppressed)", bold, reset)
	p("           Writes %scorrelation.json%s alongside each pod's configs", bold, reset)

	p("")
	p("  %s[xcp]%s  XCP CRs → files + XCP↔Istio correlation", bold+green, reset)
	p("  %sWatcher%s  Dynamic informers for XCP CRD types across 6 API groups:", bold, reset)
	p("           %sxcp.tetrate.io, traffic.xcp.tetrate.io, gateway.xcp.tetrate.io,%s", dim, reset)
	p("           %ssecurity.xcp.tetrate.io, extension.xcp.tetrate.io, istiointernal.xcp.tetrate.io%s", dim, reset)
	p("  %sUpdater%s  Writes  %sxcpconfigs/<ns>/<kind>/<name>_current.yaml%s", bold, reset, yellow, reset)
	p("           Correlates XCP → Istio via hierarchy labels + name matching")
	p("           Writes %scorrelation.json%s in xcpconfigs (forward) and", bold, reset)
	p("           %sxcp-correlation.json%s in istioconfigs (reverse)", bold, reset)

	p("")
	p("  %s[xcp-file]%s  _desired.yaml → K8s cluster (for XCP resources)", bold+green, reset)
	p("  %sWatcher%s  fsnotify watches %sxcpconfigs/%s recursively", bold, reset, yellow, reset)
	p("  %sUpdater%s  Reuses the same k8s CRUpdater as [file] provider", bold, reset)

	hr("FILE STRUCTURE")
	p("")
	p("  %sistioconfigs/%s", yellow, reset)
	p("  %s  <namespace>/%s", dim, reset)
	p("  %s    <kind>/%s", dim, reset)
	p("      <name>%s_current.yaml%s  ← live cluster state  %s(written by kubernetes provider)%s", green, reset, dim, reset)
	p("      <name>%s_desired.yaml%s  ← your proposed change %s(edit this)%s", blue, reset, dim, reset)
	p("      %scorrelation.json%s      ← which Envoy configs reference this resource", bold, reset)
	p("")
	p("  %senvoyconfigs/%s", yellow, reset)
	p("  %s  <namespace>/%s", dim, reset)
	p("  %s    <pod>/%s", dim, reset)
	p("      cluster.json, listener.json, route.json, ...")
	p("      %sdestinationrule-correlation.json%s   ← DRs that shaped clusters", bold, reset)
	p("      %svirtualservice-correlation.json%s    ← VSs that shaped routes", bold, reset)
	p("      %sgateway-correlation.json%s            ← Gateways in listener config", bold, reset)
	p("      %s<kind>-correlation.json%s             ← policy CRs matched by selector", bold, reset)
	p("")
	p("  %sxcpconfigs/%s", yellow, reset)
	p("  %s  <namespace>/%s", dim, reset)
	p("  %s    <kind>/%s", dim, reset)
	p("      <name>%s_current.yaml%s  ← live XCP CR state     %s(written by xcp provider)%s", green, reset, dim, reset)
	p("      <name>%s_desired.yaml%s  ← your proposed change  %s(edit this)%s", blue, reset, dim, reset)
	p("      %scorrelation.json%s      ← XCP resource → produced Istio resources", bold, reset)

	hr("CORRELATION")
	p("")
	p("  Pilot embeds %sistio.io/config%s metadata in each Envoy cluster,", bold, reset)
	p("  pointing back to its source Istio CR (e.g. VirtualService, DestinationRule).")
	p("")
	p("  Cluster name encodes the full relationship:")
	p("    %soutbound|9080|v1|reviews.default.svc.cluster.local%s", yellow, reset)
	p("                  %s^^  ^^^^^^^  ^^^^^^^%s", dim, reset)
	p("                  %ssubset  service  namespace%s", dim, reset)
	p("")
	p("  Parsing names + filter_metadata builds a bidirectional index:")
	p("    Istio CR   %s──→%s  affected Envoy files  %s(istioconfigs/.../correlation.json)%s", green, reset, dim, reset)
	p("    Envoy pod  %s──→%s  source Istio CRs      %s(envoyconfigs/.../correlation.json)%s", green, reset, dim, reset)

	hr("DIFF DETECTION")
	p("")
	p("  All updaters track last-written state in memory.")
	p("  Before each write/apply: LCS-based unified diff is computed.")
	p("    %s• No diff%s  → skip  %s(logged as \"No diff detected\")%s", green, reset, dim, reset)
	p("    %s• Diff%s    → write/apply  %s(unified diff logged for inspection)%s", yellow, reset, dim, reset)

	hr("TYPICAL WORKFLOW")
	p("")
	p("  %s$ sync-envoy start -n default -w app=httpbin%s", bold+cyan, reset)
	p("")
	p("  1. %sistioconfigs/default/virtualservice/httpbin_current.yaml%s appears", yellow, reset)
	p("  2. Copy or edit it as %shttpbin_desired.yaml%s with your changes", bold, reset)
	p("  3. Tool detects _desired.yaml → applies diff to cluster")
	p("  4. K8s informer fires → %shttpbin_current.yaml%s is updated", yellow, reset)
	p("  5. Check %scorrelation.json%s → click file path to see affected Envoy config", bold, reset)

	hr("FLAGS")
	p("")
	p("  %s--provider%s           kubernetes,file,envoy  %s(default: all three)%s", bold, reset, dim, reset)
	p("  %s--namespace%s      -n  target namespace       %s(default: default)%s", bold, reset, dim, reset)
	p("  %s--workload-selector%s  label selector         %s(e.g. app=httpbin)%s", bold, reset, dim, reset)
	p("  %s--csds-address%s       istiod gRPC address    %s(e.g. localhost:15010)%s", bold, reset, dim, reset)
	p("  %s--dir%s                envoy output dir       %s(default: envoyconfigs)%s", bold, reset, dim, reset)
	p("  %s--log-level%s          debug|info|warn|error  %s(default: info)%s", bold, reset, dim, reset)
	p("")
}

func runCleanup(cmd *cobra.Command, args []string) {
	dirs := []string{"envoyconfigs", istioconfigsPath, xcpconfigsPath}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			logging.Warn("Failed to remove %s: %v", d, err)
		} else {
			logging.Info("Removed %s", d)
		}
	}
}
