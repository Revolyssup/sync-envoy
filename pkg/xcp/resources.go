package xcp

import "k8s.io/apimachinery/pkg/runtime/schema"

// XCPResourceTypes lists all known XCP CRD GroupVersionResources.
// Plural names verified from client-go/pkg/crds/customresourcedefinitions.gen.yaml.
var XCPResourceTypes = []schema.GroupVersionResource{
	// xcp.tetrate.io/v2
	{Group: "xcp.tetrate.io", Version: "v2", Resource: "workspaces"},
	{Group: "xcp.tetrate.io", Version: "v2", Resource: "workspacesettings"},
	{Group: "xcp.tetrate.io", Version: "v2", Resource: "clusters"},
	{Group: "xcp.tetrate.io", Version: "v2", Resource: "globalsettings"},
	{Group: "xcp.tetrate.io", Version: "v2", Resource: "apis"},

	// traffic.xcp.tetrate.io/v2
	{Group: "traffic.xcp.tetrate.io", Version: "v2", Resource: "trafficgroups"},
	{Group: "traffic.xcp.tetrate.io", Version: "v2", Resource: "trafficsettings"},
	{Group: "traffic.xcp.tetrate.io", Version: "v2", Resource: "serviceroutes"},
	{Group: "traffic.xcp.tetrate.io", Version: "v2", Resource: "servicetrafficsettings"},

	// gateway.xcp.tetrate.io/v2
	{Group: "gateway.xcp.tetrate.io", Version: "v2", Resource: "gateways"},
	{Group: "gateway.xcp.tetrate.io", Version: "v2", Resource: "gatewaygroups"},
	{Group: "gateway.xcp.tetrate.io", Version: "v2", Resource: "ingressgateways"},
	{Group: "gateway.xcp.tetrate.io", Version: "v2", Resource: "egressgateways"},
	{Group: "gateway.xcp.tetrate.io", Version: "v2", Resource: "tier1gateways"},
	{Group: "gateway.xcp.tetrate.io", Version: "v2", Resource: "sharedgatewayreferencegrants"},

	// security.xcp.tetrate.io/v2
	{Group: "security.xcp.tetrate.io", Version: "v2", Resource: "securitygroups"},
	{Group: "security.xcp.tetrate.io", Version: "v2", Resource: "securitysettings"},
	{Group: "security.xcp.tetrate.io", Version: "v2", Resource: "servicesecuritysettings"},

	// extension.xcp.tetrate.io/v2
	{Group: "extension.xcp.tetrate.io", Version: "v2", Resource: "wasmplugindefinitions"},

	// istiointernal.xcp.tetrate.io/v2
	{Group: "istiointernal.xcp.tetrate.io", Version: "v2", Resource: "istiointernalgroups"},
}

// XCPToIstioMapping maps lowercase XCP kind to the Istio kinds it translates into.
var XCPToIstioMapping = map[string][]string{
	"serviceroute":           {"VirtualService", "DestinationRule"},
	"trafficsetting":         {"Sidecar", "EnvoyFilter", "DestinationRule"},
	"servicetrafficsetting":  {"DestinationRule"},
	"securitysetting":        {"PeerAuthentication", "AuthorizationPolicy"},
	"servicesecuritysetting": {"AuthorizationPolicy"},
	"ingressgateway":         {"Gateway", "VirtualService", "EnvoyFilter", "ServiceEntry", "DestinationRule"},
	"gateway":                {"Gateway", "VirtualService", "EnvoyFilter", "ServiceEntry", "DestinationRule"},
	"egressgateway":          {"Gateway", "VirtualService", "ServiceEntry", "DestinationRule"},
	"tier1gateway":           {"Gateway", "VirtualService", "EnvoyFilter", "ServiceEntry", "DestinationRule"},
}

// XCPHierarchyLabels are the label keys XCP uses to associate resources
// with their parent workspace/group. Generated Istio resources carry the same labels.
var XCPHierarchyLabels = []string{
	"xcp.tetrate.io/workspace",
	"xcp.tetrate.io/trafficGroup",
	"xcp.tetrate.io/securityGroup",
	"xcp.tetrate.io/gatewayGroup",
}

// IstioGVRForKind returns the GVR for a known Istio kind.
func IstioGVRForKind(kind string) (schema.GroupVersionResource, bool) {
	gvr, ok := istioKindToGVR[kind]
	return gvr, ok
}

var istioKindToGVR = map[string]schema.GroupVersionResource{
	"VirtualService":        {Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"},
	"DestinationRule":       {Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"},
	"Gateway":               {Group: "networking.istio.io", Version: "v1", Resource: "gateways"},
	"ServiceEntry":          {Group: "networking.istio.io", Version: "v1", Resource: "serviceentries"},
	"Sidecar":               {Group: "networking.istio.io", Version: "v1", Resource: "sidecars"},
	"EnvoyFilter":           {Group: "networking.istio.io", Version: "v1", Resource: "envoyfilters"},
	"AuthorizationPolicy":   {Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies"},
	"PeerAuthentication":    {Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"},
	"RequestAuthentication": {Group: "security.istio.io", Version: "v1", Resource: "requestauthentications"},
}
