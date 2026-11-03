#!/usr/bin/env bash
# End-to-end tests: a kind cluster running dra-example-driver, a few good and
# bad claims, then the Go tests in this directory run the kubectl-dra binary
# against it.
#
# Usage: test/e2e/run.sh [up|test|down|all]   (default: all)
#
#   up    create the cluster, install the driver, apply the manifests
#   test  build kubectl-dra and run the e2e Go tests
#   down  delete the cluster
#   all   up, test, then down (down also runs if up or test fails)
#
# Set E2E_KEEP=1 with "all" to keep the cluster for debugging. The cluster
# gets its own kubeconfig under .e2e/; the user's kubeconfig is never touched.
#
# Requires docker, kubectl, helm and git. The Makefile builds a pinned kind.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E_DIR="${ROOT}/test/e2e"
WORK_DIR="${E2E_WORK_DIR:-${ROOT}/.e2e}"

: "${KIND:=kind}"
: "${KIND_CLUSTER_NAME:=kubectl-dra-e2e}"
: "${KIND_IMAGE:=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
: "${DRIVER_VERSION:=v0.5.0}"

export KUBECONFIG="${WORK_DIR}/kubeconfig"

log() { printf '\n==> %s\n' "$*"; }

# kind_config writes the cluster config for KIND_IMAGE's Kubernetes version
# and prints its path.
kind_config() {
	local tag="${KIND_IMAGE%@*}"
	tag="${tag##*:v}"
	local minor
	minor="$(cut -d. -f2 <<<"${tag}")"

	local config="${WORK_DIR}/kind-config.yaml"
	cp "${E2E_DIR}/kind-config.yaml" "${config}"
	# DRADeviceTaintRules exists from 1.35; it makes the API server serve
	# DeviceTaintRules in resource.k8s.io/v1 so the e2e covers them.
	if [[ "${minor}" =~ ^[0-9]+$ ]] && ((minor >= 35)); then
		printf 'featureGates:\n  DRADeviceTaintRules: true\n' >>"${config}"
	fi
	echo "${config}"
}

up() {
	mkdir -p "${WORK_DIR}"
	for tool in docker kubectl helm git "${KIND}"; do
		command -v "${tool}" >/dev/null || { echo "missing required tool: ${tool}" >&2; exit 1; }
	done

	if "${KIND}" get clusters | grep -qx "${KIND_CLUSTER_NAME}"; then
		log "Reusing kind cluster ${KIND_CLUSTER_NAME}"
		"${KIND}" export kubeconfig --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}"
	else
		log "Creating kind cluster ${KIND_CLUSTER_NAME} (${KIND_IMAGE%@*})"
		"${KIND}" create cluster \
			--name "${KIND_CLUSTER_NAME}" \
			--image "${KIND_IMAGE}" \
			--config "$(kind_config)" \
			--kubeconfig "${KUBECONFIG}" \
			--wait 2m
	fi

	local chart_dir="${WORK_DIR}/dra-example-driver-${DRIVER_VERSION}"
	if [[ ! -d "${chart_dir}" ]]; then
		log "Fetching dra-example-driver ${DRIVER_VERSION}"
		git clone --quiet --depth 1 --branch "${DRIVER_VERSION}" \
			https://github.com/kubernetes-sigs/dra-example-driver.git "${chart_dir}"
	fi

	# Taint gpu-7 before the driver publishes devices and before any
	# workload exists, so the scheduler never picks it. Clusters without
	# DeviceTaintRules in resource.k8s.io/v1 skip this; the tests notice.
	if kubectl api-resources --api-group=resource.k8s.io -o name | grep -qx 'devicetaintrules.resource.k8s.io'; then
		log "Applying DeviceTaintRule"
		kubectl apply -f "${E2E_DIR}/manifests/taint-rule.yaml"
	else
		log "DeviceTaintRules not served; skipping"
	fi

	log "Installing dra-example-driver ${DRIVER_VERSION}"
	helm upgrade --install --wait --timeout 3m \
		--create-namespace --namespace dra-example-driver \
		--set image.tag="${DRIVER_VERSION}" \
		dra-example-driver "${chart_dir}/deployments/helm/dra-example-driver"

	log "Waiting for each worker to publish a ResourceSlice"
	local workers slices=0 deadline=$((SECONDS + 180))
	workers="$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o name | wc -l | tr -d ' ')"
	until [[ "${slices}" -ge "${workers}" ]]; do
		if ((SECONDS >= deadline)); then
			echo "only ${slices} of ${workers} workers published a gpu.example.com ResourceSlice within 3m" >&2
			kubectl -n dra-example-driver get pods -o wide >&2
			exit 1
		fi
		sleep 2
		slices="$(kubectl get resourceslices \
			-o jsonpath='{range .items[?(@.spec.driver=="gpu.example.com")]}{.metadata.name}{"\n"}{end}' | wc -l | tr -d ' ')"
	done
	kubectl get resourceslices

	log "Applying test workloads"
	kubectl apply -f "${E2E_DIR}/manifests/incomplete-pool.yaml"
	kubectl apply -f "${E2E_DIR}/manifests/workloads.yaml"
	kubectl -n dra-e2e wait --for=condition=Ready pod/one-gpu pod/two-gpus --timeout 3m
	kubectl -n dra-e2e get pods,resourceclaims
}

run_tests() {
	log "Building kubectl-dra"
	make -C "${ROOT}" build

	log "Checking kubectl finds the plugin"
	PATH="${ROOT}/bin:${PATH}" kubectl dra version

	log "Running e2e tests"
	KUBECTL_DRA="${ROOT}/bin/kubectl-dra" go -C "${ROOT}" test -tags e2e -count 1 -v ./test/e2e/...
}

down() {
	log "Deleting kind cluster ${KIND_CLUSTER_NAME}"
	"${KIND}" delete cluster --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}"
}

case "${1:-all}" in
up) up ;;
test) run_tests ;;
down) down ;;
all)
	if [[ "${E2E_KEEP:-}" != "1" ]]; then
		trap down EXIT
	fi
	up
	run_tests
	;;
*)
	echo "usage: $0 [up|test|down|all]" >&2
	exit 2
	;;
esac
