package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// Log levels
const (
	LevelDebug = iota
	LevelInfo
	LevelWarn
	LevelError
)

var logLevel = LevelInfo
var logLevelNames = map[string]int{
	"debug": LevelDebug,
	"info":  LevelInfo,
	"warn":  LevelWarn,
	"error": LevelError,
}

// Simple leveled logger
func debug(format string, v ...interface{}) {
	if logLevel <= LevelDebug {
		log.Printf("[DEBUG] "+format, v...)
	}
}
func info(format string, v ...interface{}) {
	if logLevel <= LevelInfo {
		log.Printf("[INFO] "+format, v...)
	}
}
func warn(format string, v ...interface{}) {
	if logLevel <= LevelWarn {
		log.Printf("[WARN] "+format, v...)
	}
}
func errorf(format string, v ...interface{}) {
	if logLevel <= LevelError {
		log.Printf("[ERROR] "+format, v...)
	}
}

// Global state
var (
	interval         time.Duration
	dir              string
	namespace        string
	workloadSelector string
	syncBack         bool
	logLevelStr      string
	dynamicClient    dynamic.Interface
	discoveryClient  *discovery.DiscoveryClient
	istioconfigsPath = "istioconfigs"
)

const pidFile = "/tmp/sync-envoy.pid"

func main() {
	rootCmd := &cobra.Command{
		Use:   "sync-envoy",
		Short: "Sync Envoy configs and Istio CRs to/from local files",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Set log level
			if lvl, ok := logLevelNames[logLevelStr]; ok {
				logLevel = lvl
			} else {
				log.Fatalf("Invalid log level: %s", logLevelStr)
			}
			if dir == "" {
				dir = "envoyconfigs"
			}
			os.MkdirAll(dir, 0755)
			os.MkdirAll(istioconfigsPath, 0755)
			// Initialize k8s clients
			initK8sClients()
		},
	}

	rootCmd.PersistentFlags().DurationVar(&interval, "interval", 2*time.Second, "Interval for Envoy config sync")
	rootCmd.PersistentFlags().StringVar(&dir, "dir", "envoyconfigs", "Directory to store Envoy configs")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Namespace filter for Envoy sync")
	rootCmd.PersistentFlags().StringVarP(&workloadSelector, "workload-selector", "w", "", "Workload selector (e.g., app=httpbin) for Envoy sync")
	rootCmd.PersistentFlags().BoolVar(&syncBack, "sync-back", true, "Apply changes from istioconfigs/ back to the cluster")
	rootCmd.PersistentFlags().StringVar(&logLevelStr, "log-level", "info", "Log level: debug, info, warn, error")

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

	rootCmd.AddCommand(startCmd, stopCmd, cleanupCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func initK8sClients() {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to build kubeconfig: %v", err)
		}
	}
	dynamicClient, err = dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create dynamic client: %v", err)
	}
	discoveryClient, err = discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create discovery client: %v", err)
	}
}

func runStart(cmd *cobra.Command, args []string) {
	pid := os.Getpid()
	if err := ioutil.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0644); err != nil {
		log.Fatalf("Failed to write PID file: %v", err)
	}
	defer os.Remove(pidFile)

	info("Starting sync-envoy with configuration:")
	info("  interval: %v", interval)
	info("  envoy dir: %s", dir)
	info("  namespace filter: %s", namespace)
	info("  workload selector: %s", workloadSelector)
	info("  sync-back: %v", syncBack)
	info("  log level: %s", logLevelStr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		info("Shutting down...")
		cancel()
	}()

	// Start Kubernetes CR watcher
	go watchIstioCRs(ctx)

	// Start file watcher for sync-back (if enabled)
	if syncBack {
		go watchFiles(ctx)
	}

	// Start periodic Envoy config sync
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncEnvoyConfigs()
		}
	}
}

func runStop(cmd *cobra.Command, args []string) {
	data, err := ioutil.ReadFile(pidFile)
	if err != nil {
		log.Fatalf("No PID file found (is sync-envoy running?): %v", err)
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		log.Fatalf("Failed to stop process: %v", err)
	}
	info("Sent SIGTERM to process %d", pid)
}

func runCleanup(cmd *cobra.Command, args []string) {
	dirs := []string{"envoyconfigs", istioconfigsPath}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			warn("Failed to remove %s: %v", d, err)
		} else {
			info("Removed %s", d)
		}
	}
}

// watchIstioCRs sets up informers for known Istio CRDs and writes YAML to istioconfigs/.
func watchIstioCRs(ctx context.Context) {
	// Common Istio resource types (group, version, resource)
	resourceTypes := []schema.GroupVersionResource{
		{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"},
		{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"},
		{Group: "networking.istio.io", Version: "v1", Resource: "gateways"},
		{Group: "networking.istio.io", Version: "v1", Resource: "serviceentries"},
		{Group: "networking.istio.io", Version: "v1", Resource: "workloadentries"},
		{Group: "networking.istio.io", Version: "v1", Resource: "workloadgroups"},
		{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies"},
		{Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"},
		{Group: "security.istio.io", Version: "v1", Resource: "requestauthentications"},
		{Group: "telemetry.istio.io", Version: "v1", Resource: "telemetries"},
		{Group: "extensions.istio.io", Version: "v1", Resource: "wasmplugins"},
	}

	for _, gvr := range resourceTypes {
		// Check if the resource exists in the cluster
		exists, err := resourceExists(discoveryClient, gvr)
		if err != nil {
			warn("Failed to check resource %s: %v", gvr.Resource, err)
			continue
		}
		if !exists {
			debug("Resource %s/%s not found, skipping watch.", gvr.GroupVersion(), gvr.Resource)
			continue
		}
		info("Watching resource: %s/%s", gvr.GroupVersion(), gvr.Resource)

		informer := dynamicinformer.NewFilteredDynamicInformer(
			dynamicClient,
			gvr,
			metav1.NamespaceAll,
			0,
			cache.Indexers{},
			nil,
		)
		informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				u := obj.(*unstructured.Unstructured)
				debug("K8s ADD: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				writeIstioCR(u)
			},
			UpdateFunc: func(old, new interface{}) {
				u := new.(*unstructured.Unstructured)
				debug("K8s UPDATE: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				writeIstioCR(u)
			},
			DeleteFunc: func(obj interface{}) {
				u := obj.(*unstructured.Unstructured)
				debug("K8s DELETE: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				deleteIstioCR(u)
			},
		})
		go informer.Informer().Run(ctx.Done())
	}
	<-ctx.Done()
}

// resourceExists checks if a given GVR exists in the cluster using discovery.
func resourceExists(discoveryClient discovery.DiscoveryInterface, gvr schema.GroupVersionResource) (bool, error) {
	resourceList, err := discoveryClient.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		// Group/version doesn't exist
		return false, nil
	}
	for _, resource := range resourceList.APIResources {
		if resource.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}

// writeIstioCR writes the YAML representation of an Istio CR to disk.
func writeIstioCR(obj *unstructured.Unstructured) {
	kind := strings.ToLower(obj.GetKind())
	name := obj.GetName()
	namespace := obj.GetNamespace()
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		errorf("Failed to marshal %s/%s: %v", namespace, name, err)
		return
	}

	var path string
	if namespace == "" {
		path = filepath.Join(istioconfigsPath, kind, name+".yaml")
	} else {
		path = filepath.Join(istioconfigsPath, namespace, kind, name+".yaml")
	}
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := ioutil.WriteFile(path, data, 0644); err != nil {
		errorf("Failed to write %s: %v", path, err)
	} else {
		debug("Written file: %s", path)
	}
}

// deleteIstioCR removes the file for a deleted CR.
func deleteIstioCR(obj *unstructured.Unstructured) {
	kind := strings.ToLower(obj.GetKind())
	name := obj.GetName()
	namespace := obj.GetNamespace()
	var path string
	if namespace == "" {
		path = filepath.Join(istioconfigsPath, kind, name+".yaml")
	} else {
		path = filepath.Join(istioconfigsPath, namespace, kind, name+".yaml")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		errorf("Failed to delete %s: %v", path, err)
	} else {
		debug("Deleted file: %s", path)
	}
}

// watchFiles monitors istioconfigs/ for changes and applies them back to the cluster.
func watchFiles(ctx context.Context) {
	var err error
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		errorf("Failed to create file watcher: %v", err)
		return
	}
	defer watcher.Close()

	// Watch the entire istioconfigs tree
	err = filepath.Walk(istioconfigsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		errorf("Failed to add watches: %v", err)
		return
	}

	info("File watcher started on %s", istioconfigsPath)

	// Debounce timer to group rapid events
	var debounceTimer *time.Timer
	const debounceDelay = 500 * time.Millisecond
	var pendingEvent fsnotify.Event

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only care about write and create events on .yaml files
			if !strings.HasSuffix(event.Name, ".yaml") {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			debug("File event: %s %s", event.Op, event.Name)

			// Debounce: if a timer is already running, stop and restart
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			pendingEvent = event
			debounceTimer = time.AfterFunc(debounceDelay, func() {
				handleFileEvent(pendingEvent)
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			errorf("Watcher error: %v", err)
		}
	}
}

// handleFileEvent processes a debounced file event.
func handleFileEvent(event fsnotify.Event) {
	debug("Handling file event for %s", event.Name)

	// Read the file
	data, err := ioutil.ReadFile(event.Name)
	if err != nil {
		errorf("Failed to read file %s: %v", event.Name, err)
		return
	}

	// Parse YAML into unstructured
	var obj map[string]interface{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		errorf("Failed to parse YAML from %s: %v", event.Name, err)
		return
	}
	u := &unstructured.Unstructured{Object: obj}

	// Determine namespace and kind from file path for validation
	relPath, err := filepath.Rel(istioconfigsPath, event.Name)
	if err != nil {
		errorf("Failed to get relative path for %s: %v", event.Name, err)
		return
	}
	parts := strings.Split(relPath, string(os.PathSeparator))
	if len(parts) < 2 {
		errorf("Unexpected file structure: %s", relPath)
		return
	}
	var fileNamespace, fileKind string
	if len(parts) == 2 {
		// cluster-scoped: kind/name.yaml
		fileKind = parts[0]
	} else {
		// namespaced: namespace/kind/name.yaml
		fileNamespace = parts[0]
		fileKind = parts[1]
	}

	// Validate that the kind from file path matches the kind in YAML
	if strings.ToLower(u.GetKind()) != fileKind {
		warn("Kind mismatch: file path implies kind %s but YAML has kind %s. Skipping apply.", fileKind, u.GetKind())
		return
	}
	// Validate namespace matches file path (if applicable)
	if fileNamespace != "" && u.GetNamespace() != fileNamespace {
		warn("Namespace mismatch: file path implies namespace %s but YAML has namespace %s. Skipping apply.", fileNamespace, u.GetNamespace())
		return
	}

	// Remove status and managed fields before applying
	unstructured.RemoveNestedField(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(u.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(u.Object, "metadata", "uid")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "metadata", "generation")

	// Determine GVR from GVK
	gvk := u.GroupVersionKind()
	if gvk.Kind == "" {
		errorf("Missing kind in YAML from %s", event.Name)
		return
	}

	// Use discovery to get the correct resource name
	resourceName, err := getResourceNameFromKind(discoveryClient, gvk)
	if err != nil {
		errorf("Failed to discover resource for kind %s: %v", gvk.Kind, err)
		return
	}
	gvr := schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: resourceName,
	}

	// Get current resource from cluster to compare
	var current *unstructured.Unstructured
	if fileNamespace == "" {
		current, err = dynamicClient.Resource(gvr).Get(context.TODO(), u.GetName(), metav1.GetOptions{})
	} else {
		current, err = dynamicClient.Resource(gvr).Namespace(fileNamespace).Get(context.TODO(), u.GetName(), metav1.GetOptions{})
	}

	if err != nil {
		// Resource does not exist – create it
		debug("Resource %s/%s not found in cluster, will create", fileNamespace, u.GetName())
		info("Creating %s %s/%s from file %s", gvk.Kind, fileNamespace, u.GetName(), event.Name)
		if fileNamespace == "" {
			_, err = dynamicClient.Resource(gvr).Create(context.TODO(), u, metav1.CreateOptions{})
		} else {
			_, err = dynamicClient.Resource(gvr).Namespace(fileNamespace).Create(context.TODO(), u, metav1.CreateOptions{})
		}
		if err != nil {
			errorf("Failed to create %s: %v", event.Name, err)
		} else {
			debug("Successfully created %s", event.Name)
		}
		return
	}

	// Resource exists – compare and possibly update
	// Capture resourceVersion BEFORE stripping it from current
	currentRV := current.GetResourceVersion()

	// Remove status and managed fields from current for comparison
	unstructured.RemoveNestedField(current.Object, "status")
	unstructured.RemoveNestedField(current.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(current.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(current.Object, "metadata", "uid")
	unstructured.RemoveNestedField(current.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(current.Object, "metadata", "generation")

	// Compare objects (ignoring status)
	currentJSON, _ := json.Marshal(current.Object)
	newJSON, _ := json.Marshal(u.Object)
	if bytes.Equal(currentJSON, newJSON) {
		debug("No changes detected for %s, skipping apply", event.Name)
		return
	}

	// Update: set resourceVersion from the captured value
	u.SetResourceVersion(currentRV)
	info("Updating %s %s/%s from file %s", gvk.Kind, fileNamespace, u.GetName(), event.Name)
	if fileNamespace == "" {
		_, err = dynamicClient.Resource(gvr).Update(context.TODO(), u, metav1.UpdateOptions{})
	} else {
		_, err = dynamicClient.Resource(gvr).Namespace(fileNamespace).Update(context.TODO(), u, metav1.UpdateOptions{})
	}
	if err != nil {
		errorf("Failed to update %s: %v", event.Name, err)
	} else {
		debug("Successfully updated %s", event.Name)
	}
}

// getResourceNameFromKind uses discovery to find the resource name for a given GVK.
func getResourceNameFromKind(discoveryClient discovery.DiscoveryInterface, gvk schema.GroupVersionKind) (string, error) {
	resourceList, err := discoveryClient.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return "", err
	}
	for _, resource := range resourceList.APIResources {
		if resource.Kind == gvk.Kind {
			return resource.Name, nil
		}
	}
	return "", fmt.Errorf("no resource found for kind %s in group %s", gvk.Kind, gvk.GroupVersion())
}

// syncEnvoyConfigs runs `istioctl proxy-config` on selected workloads and saves outputs.
func syncEnvoyConfigs() {
	pods, err := getPods(namespace, workloadSelector)
	if err != nil {
		errorf("Failed to list pods: %v", err)
		return
	}
	types := []string{"listener", "cluster", "endpoint", "route", "bootstrap", "secret"}

	for _, pod := range pods {
		podName := pod.name
		podNS := pod.namespace
		podDir := filepath.Join(dir, podNS, podName)
		os.MkdirAll(podDir, 0755)

		for _, typ := range types {
			out, err := runIstioctlProxyConfig(podName, podNS, typ)
			if err != nil {
				// Many pods won't have sidecars; skip silently
				continue
			}
			filePath := filepath.Join(podDir, typ+".json")
			if err := ioutil.WriteFile(filePath, out, 0644); err != nil {
				errorf("Failed to write %s: %v", filePath, err)
			} else {
				debug("Written Envoy config %s for %s/%s", typ, podNS, podName)
			}
		}
	}
}

type podInfo struct {
	name      string
	namespace string
}

func getPods(namespace, selector string) ([]podInfo, error) {
	args := []string{"get", "pods", "-n", namespace, "-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{.metadata.namespace}{\"\\n\"}{end}"}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pods []podInfo
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			pods = append(pods, podInfo{name: parts[0], namespace: parts[1]})
		}
	}
	return pods, nil
}

func runIstioctlProxyConfig(pod, namespace, typ string) ([]byte, error) {
	cmd := exec.Command("istioctl", "proxy-config", typ, pod, "-n", namespace, "-o", "json")
	return cmd.CombinedOutput()
}
