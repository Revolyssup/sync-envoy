package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/revolyssup/sync-envoy/pkg/envoy"
	"github.com/revolyssup/sync-envoy/pkg/file"
	"github.com/revolyssup/sync-envoy/pkg/k8s"
	"github.com/revolyssup/sync-envoy/pkg/lock"
	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/provider"
	"github.com/revolyssup/sync-envoy/pkg/topology"
	"github.com/revolyssup/sync-envoy/pkg/xcp"
)

var (
	dir              string
	workloadSelector string
	logLevelStr      string
	providerFilter   string
	csdsAddress      string
	showDiffFilter   string
	istioconfigsPath string
	xcpconfigsPath   string
	envoyConfigsPath string
	packName         string
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
				dir = "."
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				log.Fatalf("Failed to resolve absolute path for %s: %v", dir, err)
			}
			dir = absDir
			envoyConfigsPath = filepath.Join(dir, "syncconfigs", "envoy")
			istioconfigsPath = filepath.Join(dir, "syncconfigs", "istio")
			xcpconfigsPath = filepath.Join(dir, "syncconfigs", "xcp")
		},
	}

	rootCmd.PersistentFlags().StringVar(&dir, "dir", ".", "Base directory to store syncconfigs/ (created if it does not exist)")
	rootCmd.PersistentFlags().StringVarP(&workloadSelector, "workload-selector", "w", "", "Workload selector (e.g., app=httpbin)")
	rootCmd.PersistentFlags().StringVar(&logLevelStr, "log-level", "info", "Log level: debug, info, warn, error")
	rootCmd.PersistentFlags().StringVar(&providerFilter, "provider", "", "Comma-separated list of providers to enable (default: all). Options: kubernetes,istio-file,envoy,xcp,xcp-file")
	rootCmd.PersistentFlags().StringVar(&csdsAddress, "csds-address", "", "istiod gRPC address for CSDS streaming (e.g., localhost:15010). If empty, falls back to admin/istioctl polling")
	rootCmd.PersistentFlags().StringVar(&showDiffFilter, "show-diff", "kubernetes,xcp", "Comma-separated list of providers to show diffs for (default: kubernetes,xcp)")

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
		Short: "Delete all generated files in syncconfigs/",
		Run:   runCleanup,
	}

	howCmd := &cobra.Command{
		Use:   "how",
		Short: "Explain what sync-envoy does and how it works",
		Run:   runHow,
	}

	packCmd := &cobra.Command{
		Use:   "pack",
		Short: "Pack syncconfigs/ into a tar.gz archive",
		Run:   runPack,
	}
	packCmd.Flags().StringVar(&packName, "name", "packed-envoy-configs", "Name of the output tar.gz file (without extension)")

	unpackCmd := &cobra.Command{
		Use:   "unpack <tarfile>",
		Short: "Unpack a tar.gz archive into syncconfigs/",
		Args:  cobra.ExactArgs(1),
		Run:   runUnpack,
	}

	rootCmd.AddCommand(startCmd, stopCmd, cleanupCmd, howCmd, packCmd, unpackCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func ensureSyncDirs() {
	os.MkdirAll(dir, 0755)
	os.MkdirAll(envoyConfigsPath, 0755)
	os.MkdirAll(istioconfigsPath, 0755)
	os.MkdirAll(xcpconfigsPath, 0755)
}

func runStart(cmd *cobra.Command, args []string) {
	ensureSyncDirs()

	// Acquire directory lock to prevent concurrent instances on the same directory.
	syncDir := filepath.Join(dir, "syncconfigs")
	instanceID, err := lock.Acquire(syncDir)
	if err != nil {
		log.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Release(instanceID)

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
	logging.Info("  base dir: %s", dir)
	logging.Info("  envoy dir: %s", envoyConfigsPath)
	logging.Info("  istio dir: %s", istioconfigsPath)
	logging.Info("  xcp dir: %s", xcpconfigsPath)
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
		lock.Release(instanceID) // explicit release; defers don't run on os.Exit
		cancel()
	}()

	// Clean stale _current.yaml files so they are recreated fresh from cluster state.
	cleanCurrentFiles(istioconfigsPath, xcpconfigsPath)

	// Parse --show-diff filter into a set for quick lookup.
	showDiffSet := make(map[string]bool)
	for _, name := range strings.Split(showDiffFilter, ",") {
		showDiffSet[strings.TrimSpace(name)] = true
	}

	// Determine which CR providers are active so file providers know whether
	// to apply _desired.yaml at startup. When no filter is set, all run.
	providerActive := func(name string) bool {
		if providerFilter == "" {
			return true
		}
		for _, n := range strings.Split(providerFilter, ",") {
			if strings.TrimSpace(n) == name {
				return true
			}
		}
		return false
	}

	// Build provider registry
	registry := provider.NewRegistry()

	// Create topology writers
	istioTopology := topology.NewFile(istioconfigsPath, "Istio Resource Topology")
	xcpTopology := topology.NewFile(xcpconfigsPath, "XCP → Istio Resource Topology")

	// Kubernetes provider: watches K8s CRs, writes _current.yaml files + topology
	registry.Register(provider.New(
		"kubernetes",
		k8s.NewCRWatcher(clients),
		file.NewCurrentFileUpdater(istioconfigsPath).WithTopology(istioTopology).WithShowDiff(showDiffSet["kubernetes"]),
	))

	// Istio-file provider: watches _desired.yaml files in syncconfigs/istio/, applies to K8s cluster
	// Only applies pre-existing _desired at startup when kubernetes provider is also active.
	registry.Register(provider.New(
		"istio-file",
		file.NewDesiredFileWatcher(istioconfigsPath).WithApplyOnStartup(providerActive("kubernetes")),
		k8s.NewCRUpdater(clients).WithShowDiff(showDiffSet["istio-file"]),
	))

	// Envoy provider: reads envoy configs, writes JSON files
	registry.Register(provider.New(
		"envoy",
		envoy.NewEnvoyWatcher(workloadSelector, csdsAddress),
		envoy.NewFileUpdater(envoyConfigsPath, "last_updated").WithShowDiff(showDiffSet["envoy"]),
	))

	// XCP provider: watches XCP CRDs, writes _current.yaml + XCP→Istio topology
	registry.Register(provider.New(
		"xcp",
		xcp.NewXCPWatcher(clients),
		xcp.NewXCPFileUpdater(xcpconfigsPath, clients, xcpTopology).WithShowDiff(showDiffSet["xcp"]),
	))

	// XCP file provider: watches _desired.yaml in syncconfigs/xcp/, applies to K8s cluster
	// Only applies pre-existing _desired at startup when xcp provider is also active.
	registry.Register(provider.New(
		"xcp-file",
		file.NewDesiredFileWatcher(xcpconfigsPath).WithApplyOnStartup(providerActive("xcp")),
		k8s.NewCRUpdater(clients).WithShowDiff(showDiffSet["xcp-file"]),
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

	// Warn if file-applying providers are active and _desired.yaml files exist.
	if needsStartupWarning(providers) {
		fmt.Println("\033[33mWARNING:\033[0m An existing cluster state is detected on file system.")
		fmt.Println("These files will be applied on the running cluster.")
		fmt.Print("Please confirm that is okay and press y: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			logging.Info("User declined, exiting.")
			lock.Release(instanceID)
			os.Exit(0)
		}
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
	p("  %sUpdater%s  Writes  %ssyncconfigs/istio/<ns>/<kind>/<name>_current.yaml%s", bold, reset, yellow, reset)
	p("           Skips write when content is unchanged (LCS diff check)")

	p("")
	p("  %s[istio-file]%s  _desired.yaml → K8s cluster", bold+green, reset)
	p("  %sWatcher%s  fsnotify watches %ssyncconfigs/istio/%s recursively", bold, reset, yellow, reset)
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
	p("  %sUpdater%s  Writes  %ssyncconfigs/envoy/<ns>/<pod>/<type>.json%s", bold, reset, yellow, reset)
	p("           JSON includes: last_updated, pod_name, namespace, config_type, config")
	p("           Ignores %slast_updated%s when diffing (timestamp noise suppressed)", bold, reset)

	p("")
	p("  %s[xcp]%s  XCP CRs → files + XCP→Istio topology", bold+green, reset)
	p("  %sWatcher%s  Dynamic informers for XCP CRD types across 6 API groups:", bold, reset)
	p("           %sxcp.tetrate.io, traffic.xcp.tetrate.io, gateway.xcp.tetrate.io,%s", dim, reset)
	p("           %ssecurity.xcp.tetrate.io, extension.xcp.tetrate.io, istiointernal.xcp.tetrate.io%s", dim, reset)
	p("  %sUpdater%s  Writes  %ssyncconfigs/xcp/<ns>/<kind>/<name>_current.yaml%s", bold, reset, yellow, reset)
	p("           Maps XCP → Istio via hierarchy labels + name matching")
	p("           Writes %ssyncconfigs/xcp/topology.md%s (XCP→Istio resource map)", bold, reset)

	p("")
	p("  %s[xcp-file]%s  _desired.yaml → K8s cluster (for XCP resources)", bold+green, reset)
	p("  %sWatcher%s  fsnotify watches %ssyncconfigs/xcp/%s recursively", bold, reset, yellow, reset)
	p("  %sUpdater%s  Reuses the same k8s CRUpdater as [file] provider", bold, reset)

	hr("FILE STRUCTURE")
	p("")
	p("  %ssyncconfigs/istio/%s", yellow, reset)
	p("  %s  <namespace>/%s", dim, reset)
	p("  %s    <kind>/%s", dim, reset)
	p("      <name>%s_current.yaml%s  ← live cluster state  %s(written by kubernetes provider)%s", green, reset, dim, reset)
	p("      <name>%s_desired.yaml%s  ← your proposed change %s(edit this)%s", blue, reset, dim, reset)
	p("    %stopology.md%s              ← Istio resource relationship map", bold, reset)
	p("")
	p("  %ssyncconfigs/envoy/%s", yellow, reset)
	p("  %s  <namespace>/%s", dim, reset)
	p("  %s    <pod>/%s", dim, reset)
	p("      cluster.json, listener.json, route.json, ...")
	p("")
	p("  %ssyncconfigs/xcp/%s", yellow, reset)
	p("  %s  <namespace>/%s", dim, reset)
	p("  %s    <kind>/%s", dim, reset)
	p("      <name>%s_current.yaml%s  ← live XCP CR state     %s(written by xcp provider)%s", green, reset, dim, reset)
	p("      <name>%s_desired.yaml%s  ← your proposed change  %s(edit this)%s", blue, reset, dim, reset)
	p("    %stopology.md%s              ← XCP → Istio resource map", bold, reset)

	hr("TOPOLOGY")
	p("")
	p("  %ssyncconfigs/istio/topology.md%s  maps Istio resource relationships:", bold, reset)
	p("    VirtualService → Gateway (spec.gateways)")
	p("    VirtualService → destination hosts (spec.http[].route)")
	p("    Gateway → workload selector (spec.selector)")
	p("    DestinationRule → host + subsets")
	p("    AuthorizationPolicy/PeerAuth → workload selector")
	p("    ServiceEntry → hosts")
	p("    + XCP provenance labels when present")
	p("")
	p("  %ssyncconfigs/xcp/topology.md%s  maps XCP → Istio resources:", bold, reset)
	p("    e.g. IngressGateway/foo %s──→%s Gateway/foo, VirtualService/vs-foo", green, reset)

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
	p("  1. %ssyncconfigs/istio/default/virtualservice/httpbin_current.yaml%s appears", yellow, reset)
	p("  2. Copy or edit it as %shttpbin_desired.yaml%s with your changes", bold, reset)
	p("  3. Tool detects _desired.yaml → applies diff to cluster")
	p("  4. K8s informer fires → %shttpbin_current.yaml%s is updated", yellow, reset)
	p("  5. Check %stopology.md%s → see resource relationships at a glance", bold, reset)

	hr("FLAGS")
	p("")
	p("  %s--provider%s           kubernetes,istio-file,envoy,xcp,xcp-file  %s(default: all)%s", bold, reset, dim, reset)
	p("  %s--workload-selector%s  label selector         %s(e.g. app=httpbin)%s", bold, reset, dim, reset)
	p("  %s--csds-address%s       istiod gRPC address    %s(e.g. localhost:15010)%s", bold, reset, dim, reset)
	p("  %s--dir%s                base output dir        %s(default: current directory)%s", bold, reset, dim, reset)
	p("  %s--log-level%s          debug|info|warn|error  %s(default: info)%s", bold, reset, dim, reset)
	p("")
}

// cleanCurrentFiles removes all _current.yaml files from the given directories
// so they are recreated fresh from cluster state on startup.
func cleanCurrentFiles(dirs ...string) {
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && strings.HasSuffix(path, "_current.yaml") {
				os.Remove(path)
				logging.Debug("Removed stale current file: %s", path)
			}
			return nil
		})
	}
}

// needsStartupWarning returns true if file-applying providers are active
// and their directories contain _desired.yaml files that would be applied.
func needsStartupWarning(providers []*provider.Provider) bool {
	activeNames := make(map[string]bool)
	for _, p := range providers {
		activeNames[p.Name()] = true
	}
	if activeNames["istio-file"] && hasDesiredFiles(istioconfigsPath) {
		return true
	}
	if activeNames["xcp-file"] && hasDesiredFiles(xcpconfigsPath) {
		return true
	}
	return false
}

// hasDesiredFiles checks if a directory tree contains any _desired.yaml files.
func hasDesiredFiles(basePath string) bool {
	found := false
	filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "_desired.yaml") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func runCleanup(cmd *cobra.Command, args []string) {
	syncDir := filepath.Join(dir, "syncconfigs")

	// Refuse to clean up if another instance owns this directory.
	ownerPID, locked, err := lock.CheckDirectory(syncDir)
	if err != nil {
		logging.Warn("Failed to check lock: %v", err)
	} else if locked {
		log.Fatalf("Cannot clean up %s: directory is locked by process %d. Stop that instance first.", syncDir, ownerPID)
	}

	if err := os.RemoveAll(syncDir); err != nil {
		logging.Warn("Failed to remove %s: %v", syncDir, err)
	} else {
		logging.Info("Removed %s", syncDir)
	}
}

func runPack(cmd *cobra.Command, args []string) {
	syncDir := filepath.Join(dir, "syncconfigs")
	if _, err := os.Stat(syncDir); os.IsNotExist(err) {
		log.Fatalf("Directory %s does not exist — nothing to pack", syncDir)
	}

	outFile := packName + ".tar.gz"
	f, err := os.Create(outFile)
	if err != nil {
		log.Fatalf("Failed to create archive %s: %v", outFile, err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	packed := 0
	dirs := []string{envoyConfigsPath, istioconfigsPath, xcpconfigsPath}
	for _, d := range dirs {
		if _, err := os.Stat(d); os.IsNotExist(err) {
			logging.Warn("Skipping %s (does not exist)", d)
			continue
		}
		if err := filepath.Walk(d, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = path
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err = io.Copy(tw, file); err != nil {
				return err
			}
			packed++
			return nil
		}); err != nil {
			log.Fatalf("Failed to pack %s: %v", d, err)
		}
		logging.Info("Packed %s", d)
	}
	if packed == 0 {
		// Clean up the empty archive
		f.Close()
		os.Remove(outFile)
		log.Fatalf("No files found in %s — nothing to pack", syncDir)
	}
	logging.Info("Archive created: %s (%d files)", outFile, packed)
}

func runUnpack(cmd *cobra.Command, args []string) {
	ensureSyncDirs()

	tarFile := args[0]
	f, err := os.Open(tarFile)
	if err != nil {
		log.Fatalf("Failed to open archive %s: %v", tarFile, err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		log.Fatalf("Failed to read gzip stream: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	// Delete existing dirs before extracting
	dirs := []string{envoyConfigsPath, istioconfigsPath, xcpconfigsPath}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			logging.Warn("Failed to remove %s: %v", d, err)
		}
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("Failed to read archive entry: %v", err)
		}

		// Safety: prevent path traversal
		target := filepath.Clean(hdr.Name)
		if strings.HasPrefix(target, "..") {
			log.Fatalf("Unsafe path in archive: %s", hdr.Name)
		}

		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				log.Fatalf("Failed to create directory %s: %v", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			log.Fatalf("Failed to create parent dir for %s: %v", target, err)
		}
		out, err := os.Create(target)
		if err != nil {
			log.Fatalf("Failed to create file %s: %v", target, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			log.Fatalf("Failed to write file %s: %v", target, err)
		}
		out.Close()
	}
	logging.Info("Unpacked %s into %s", tarFile, dir)
}
