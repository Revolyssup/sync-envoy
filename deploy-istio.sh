#!/usr/bin/env bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
CLUSTER_NAME="istio"
ISTIO_CONFIG_DIR="istioconfigs"
GRAFANA_DIR="${ISTIO_CONFIG_DIR}/grafana"
ISTIO_VERSION="1.20.3"  # Change as needed

echo -e "${GREEN}=== Deploy Istio and workloads ===${NC}"

# 1. Create istioconfigs/ folder if not exists
mkdir -p "${ISTIO_CONFIG_DIR}"
mkdir -p "${GRAFANA_DIR}"
echo -e "${GREEN}✓ Created directories: ${ISTIO_CONFIG_DIR}, ${GRAFANA_DIR}${NC}"

# 2. Create kind cluster if not exists
if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
    echo -e "${YELLOW}Creating kind cluster '${CLUSTER_NAME}'...${NC}"
    cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
EOF
    echo -e "${GREEN}✓ Kind cluster created${NC}"
else
    echo -e "${GREEN}✓ Kind cluster '${CLUSTER_NAME}' already exists${NC}"
fi

# 3. Set kubeconfig context
kubectl cluster-info --context "kind-${CLUSTER_NAME}" > /dev/null 2>&1
echo -e "${GREEN}✓ kubeconfig set to cluster '${CLUSTER_NAME}'${NC}"

# 4. Download istioctl if not present
if ! command -v istioctl &> /dev/null; then
    echo -e "${YELLOW}istioctl not found, downloading...${NC}"
    curl -L https://istio.io/downloadIstio | ISTIO_VERSION=${ISTIO_VERSION} sh -
    cd istio-${ISTIO_VERSION}
    export PATH=$PWD/bin:$PATH
    cd ..
    echo -e "${GREEN}✓ istioctl installed${NC}"
else
    echo -e "${GREEN}✓ istioctl already installed${NC}"
fi

# 5. Install Istio with demo profile (includes addons)
echo -e "${YELLOW}Installing Istio control plane...${NC}"
istioctl install --set profile=demo -y

# 6. Deploy addons (Kiali, Prometheus, Grafana)
echo -e "${YELLOW}Deploying Kiali, Prometheus, and Grafana...${NC}"
kubectl apply -f https://raw.githubusercontent.com/istio/istio/release-1.20/samples/addons/kiali.yaml
kubectl apply -f https://raw.githubusercontent.com/istio/istio/release-1.20/samples/addons/prometheus.yaml
kubectl apply -f https://raw.githubusercontent.com/istio/istio/release-1.20/samples/addons/grafana.yaml
kubectl rollout status deployment/kiali -n istio-system
kubectl rollout status deployment/prometheus -n istio-system
kubectl rollout status deployment/grafana -n istio-system
echo -e "${GREEN}✓ Istio and addons installed${NC}"

# 7. Deploy httpbin and sleep workloads
echo -e "${YELLOW}Deploying httpbin and sleep workloads...${NC}"
kubectl label namespace default istio-injection=enabled --overwrite
kubectl apply -f https://raw.githubusercontent.com/istio/istio/release-1.20/samples/httpbin/httpbin.yaml
kubectl apply -f https://raw.githubusercontent.com/istio/istio/release-1.20/samples/sleep/sleep.yaml
kubectl rollout status deployment/httpbin
kubectl rollout status deployment/sleep
echo -e "${GREEN}✓ Workloads deployed${NC}"

# 8. Save all applied configurations to istioconfigs/
echo -e "${YELLOW}Saving configurations to ${ISTIO_CONFIG_DIR}/...${NC}"
# Save Istio operator configuration (if any)
kubectl get istiooperator -A -o yaml > "${ISTIO_CONFIG_DIR}/istio-operator.yaml" 2>/dev/null || true
# Save all Istio CRs
for crd in $(kubectl get crd -o name | grep 'istio.io'); do
    resource=$(basename "${crd}")
    kind=$(echo "${resource}" | sed 's/\.istio\.io.*//')
    echo "Saving ${kind}..."
    kubectl get "${kind}" -A -o yaml > "${ISTIO_CONFIG_DIR}/${kind}-all.yaml" 2>/dev/null || true
done
# Save workloads
kubectl get deployment,service httpbin sleep -n default -o yaml > "${ISTIO_CONFIG_DIR}/workloads.yaml"
echo -e "${GREEN}✓ Configurations saved${NC}"

# 9. Create basic Grafana dashboards JSON
echo -e "${YELLOW}Fetching default Grafana dashboards...${NC}"
# Get dashboards from the Grafana configmap
kubectl get configmap -n istio-system istio-grafana-dashboards -o jsonpath='{.data}' | while IFS='=' read -r name content; do
    # Remove quotes and save to file
    echo "${content}" > "${GRAFANA_DIR}/${name}.json"
done
echo -e "${GREEN}✓ Grafana dashboards saved to ${GRAFANA_DIR}/${NC}"

echo -e "${GREEN}=== Deployment complete ===${NC}"
echo -e "Access Kiali:   kubectl port-forward svc/kiali -n istio-system 20001:20001"
echo -e "Access Grafana: kubectl port-forward svc/grafana -n istio-system 3000:3000"
echo -e "Access Prometheus: kubectl port-forward svc/prometheus -n istio-system 9090:9090"
