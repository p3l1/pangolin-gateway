set shell := ["bash", "-euo", "pipefail", "-c"]

image := "ghcr.io/p3l1/pangolin-gateway"
fake_image := "pangolin-fake"
tag := "dev"
cluster := "pangolin-gateway"
k3s_image := "rancher/k3s:v1.36.4-k3s1"
envtest_k8s := "1.37.0"
chart := "charts/pangolin-gateway"
namespace := "pangolin-gateway-system"

default:
    @just --list

# Verify required tools are present and install the commit hook.
setup:
    #!/usr/bin/env bash
    set -euo pipefail
    missing=0
    check() {
        if ! command -v "$1" >/dev/null 2>&1; then
            echo "missing: $1 — install with: $2" >&2
            missing=1
        fi
    }
    check go      "https://go.dev/dl/"
    check docker  "https://docs.docker.com/get-docker/"
    check helm    "brew install helm"
    check kubectl "brew install kubernetes-cli"
    check k3d     "brew install k3d"
    check kubeconform "brew install kubeconform"
    [ "$missing" -eq 0 ] || { echo "install the tools above, then re-run just setup" >&2; exit 1; }
    command -v syft >/dev/null 2>&1 || echo "note: syft absent; just sbom will not work (brew install syft)" >&2
    command -v helm-docs >/dev/null 2>&1 || echo "note: helm-docs absent; just docs will not work (brew install norwoodj/tap/helm-docs)" >&2
    # In a worktree .git is a file, not a directory, so the hooks path has to be
    # resolved rather than assumed.
    hooks=$(git rev-parse --git-path hooks)
    mkdir -p "$hooks"
    install -m 0755 hack/commit-msg "$hooks/commit-msg"
    echo "all required tools present; commit-msg hook installed in $hooks"

fmt:
    go fmt ./...

# Fails, without rewriting, on files go fmt would otherwise silently reformat.
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(gofmt -l .)
    if [ -n "$unformatted" ]; then
        echo "not gofmt-ed (run: just fmt):" >&2
        echo "$unformatted" >&2
        exit 1
    fi

vet:
    go vet ./...

# Placeholder values for rendering the chart in CI. The four required ones have
# no defaults on purpose: a controller pointed at the wrong endpoint, or at no
# organisation, must fail at install time rather than at the first 401.
lint_values := "--set pangolin.endpoint=https://api.example.com/v1 \
    --set pangolin.org=example-org \
    --set pangolin.defaultSite=example-site \
    --set pangolin.apiKeySecret.name=example-secret"

lint:
    go tool golangci-lint run ./...
    helm lint {{chart}} {{lint_values}}
    # GatewayClass is a Gateway API CRD kind, absent from kubeconform's default
    # schema catalogue; the rest of the chart is still checked strictly.
    helm template {{chart}} {{lint_values}} | kubeconform -strict -summary -skip GatewayClass -

# Resolves the Gateway API CRDs from the module cache, so the schemas the tests
# run against are the ones go.mod pins rather than a separately drifting copy.
# "{{{{" escapes to a literal "{{"; the closing braces need no escaping.
gateway-crds:
    @echo "$(go list -m -f '{{{{.Dir}}' sigs.k8s.io/gateway-api)/config/crd/standard"

# Fast tier: seconds, run on every change.
check: fmt-check vet lint
    #!/usr/bin/env bash
    set -euo pipefail
    # internal/controller needs a real API server (see `test`); excluded here so
    # this fast tier never depends on KUBEBUILDER_ASSETS being set.
    go test $(go list ./internal/... ./cmd/... ./test/pangolinfake/... | grep -v '/internal/controller$')

# Medium tier: envtest against a real API server, no cluster.
test:
    #!/usr/bin/env bash
    set -euo pipefail
    # --bin-dir must be absolute: go test runs the binary from the package dir, not
    # here, so a relative "-p path" result would no longer resolve at that point.
    export KUBEBUILDER_ASSETS="$(go tool setup-envtest use {{envtest_k8s}} --bin-dir {{justfile_directory()}}/.envtest -p path)"
    export GATEWAY_API_CRDS="$(just gateway-crds)"
    pkgs="./test/envtest/..."
    # internal/controller lands with the reconciler; go test errors on a package
    # path that does not exist yet, so only add it once it does.
    if [ -d internal/controller ] && compgen -G "internal/controller/*.go" >/dev/null; then
        pkgs="$pkgs ./internal/controller/..."
    fi
    go test $pkgs -count=1

build:
    go build -ldflags "-s -w -X github.com/p3l1/pangolin-gateway/internal/version.Version={{tag}} -X github.com/p3l1/pangolin-gateway/internal/version.Commit=$(git rev-parse --short HEAD)" -o bin/manager ./cmd

# This project defines no CRDs, so controller-gen runs for RBAC markers only.
generate:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -d internal/controller ] || ! compgen -G "internal/controller/*.go" >/dev/null; then
        echo "no internal/controller yet; nothing to generate"
        exit 0
    fi
    go tool controller-gen rbac:roleName=manager-role paths=./internal/controller/... output:rbac:artifacts:config=config/rbac
    # controller-gen exits 0 and writes nothing when it finds no +kubebuilder:rbac
    # markers at package level (a common cause: the comment block sits directly
    # above a func with no blank line, so it is discarded as that func's godoc).
    if [ ! -s config/rbac/role.yaml ]; then
        echo "internal/controller exists but controller-gen produced no RBAC rules" >&2
        echo "(check +kubebuilder:rbac marker placement — see hack/rbac-to-chart.sh)" >&2
        exit 1
    fi
    sh hack/rbac-to-chart.sh config/rbac/role.yaml {{chart}}/templates/rbac.yaml

# Fails when generated output is not committed, or the two chart versions drift.
verify: generate
    #!/usr/bin/env bash
    set -euo pipefail
    # --exit-code ignores untracked files, so it misses a file generated for the
    # first time; status --porcelain sees new and uncommitted files alike.
    changes=$(git status --porcelain -- config {{chart}})
    if [ -n "$changes" ]; then
        echo "generated output does not match what is committed:" >&2
        echo "$changes" >&2
        exit 1
    fi
    v=$(awk '/^version:/ {print $2; exit}' {{chart}}/Chart.yaml)
    a=$(awk '/^appVersion:/ {gsub(/"/, "", $2); print $2; exit}' {{chart}}/Chart.yaml)
    if [ "$v" != "$a" ]; then
        echo "chart version ($v) and appVersion ($a) differ" >&2
        exit 1
    fi
    echo "generated output is in sync; chart version $v"

# Creates only the cluster named above. Never touches any other k3d cluster.
cluster-up:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! k3d cluster list {{cluster}} >/dev/null 2>&1; then
        k3d cluster create {{cluster}} --image {{k3s_image}} --agents 0 --wait
    fi
    kubectl --context k3d-{{cluster}} cluster-info
    kubectl --context k3d-{{cluster}} apply --server-side -f "$(just gateway-crds)"
    # The HTTPRoute CRD is large enough that establishing it takes well over a
    # minute on k3d, and longer on CI runners.
    kubectl --context k3d-{{cluster}} wait --for condition=established --timeout 300s \
        crd/httproutes.gateway.networking.k8s.io \
        crd/gateways.gateway.networking.k8s.io \
        crd/gatewayclasses.gateway.networking.k8s.io

cluster-down:
    k3d cluster delete {{cluster}} || true

# The release image, built the way CI builds it.
docker-build:
    docker build -t {{image}}:{{tag}} --build-arg VERSION={{tag}} --build-arg COMMIT=$(git rev-parse --short HEAD) .

# Local images for e2e: the binary is cross-compiled on the host and only copied
# into the runtime layer. Compiling inside the container needs several GB, which
# Docker Desktop often does not have, and the host toolchain is already warm.
# Release images still come from the Dockerfiles, which CI uses unchanged.
images-local:
    #!/usr/bin/env bash
    set -euo pipefail
    arch=$(go env GOARCH)
    mkdir -p bin
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
        -ldflags "-s -w -X github.com/p3l1/pangolin-gateway/internal/version.Version={{tag}} -X github.com/p3l1/pangolin-gateway/internal/version.Commit=$(git rev-parse --short HEAD)" \
        -o bin/manager-linux ./cmd
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
        -ldflags "-s -w" -o bin/pangolin-fake-linux ./test/pangolinfake/cmd
    # bin/ is the build context, so the repository's .dockerignore does not apply.
    printf 'FROM gcr.io/distroless/static:nonroot\nCOPY manager-linux /manager\nUSER 65532:65532\nENTRYPOINT ["/manager"]\n' \
        | docker build -t {{image}}:{{tag}} -f - bin/
    printf 'FROM gcr.io/distroless/static:nonroot\nCOPY pangolin-fake-linux /pangolin-fake\nUSER 65532:65532\nEXPOSE 8080\nENTRYPOINT ["/pangolin-fake"]\n' \
        | docker build -t {{fake_image}}:{{tag}} -f - bin/

# The fake Pangolin API. Built from test/, so it can never reach the production image.
fake-build:
    docker build -t {{fake_image}}:{{tag}} -f test/pangolinfake/Dockerfile .

deploy: images-local cluster-up
    #!/usr/bin/env bash
    set -euo pipefail
    k3d image import {{image}}:{{tag}} {{fake_image}}:{{tag}} -c {{cluster}}
    kubectl --context k3d-{{cluster}} create namespace {{namespace}} \
        --dry-run=client -o yaml | kubectl --context k3d-{{cluster}} apply -f -
    kubectl --context k3d-{{cluster}} -n {{namespace}} apply -f test/pangolinfake/deploy.yaml
    kubectl --context k3d-{{cluster}} -n {{namespace}} rollout status deployment/pangolin-fake --timeout 2m
    kubectl --context k3d-{{cluster}} -n {{namespace}} create secret generic pangolin-api-key \
        --from-literal=apiKey=test_id.test_secret \
        --dry-run=client -o yaml | kubectl --context k3d-{{cluster}} -n {{namespace}} apply -f -
    # The image tag never changes, so the pod template needs a per-deploy stamp:
    # otherwise the Deployment is byte-identical and Kubernetes rolls nothing.
    helm upgrade --install pangolin-gateway {{chart}} \
        --kube-context k3d-{{cluster}} \
        --namespace {{namespace}} --create-namespace \
        --set image.repository={{image}} --set image.tag={{tag}} \
        --set image.pullPolicy=IfNotPresent \
        --set pangolin.endpoint=http://pangolin-fake.{{namespace}}.svc.cluster.local:8080/v1 \
        --set pangolin.org=test-org \
        --set pangolin.defaultSite=test-site \
        --set pangolin.apiKeySecret.name=pangolin-api-key \
        --set-string podAnnotations.deployedAt="$(date -u +%Y%m%dT%H%M%SZ)" \
        --wait --timeout 3m
    kubectl --context k3d-{{cluster}} -n {{namespace}} \
        rollout status deployment/pangolin-gateway --timeout 3m

# Full tier: minutes, run before a PR and in CI.
e2e: deploy
    go test ./test/e2e/... -count=1 -timeout 10m

sbom:
    syft scan {{image}}:{{tag}} -o spdx-json=controller.sbom.json
    syft scan dir:{{chart}} -o spdx-json=chart.sbom.json

docs:
    helm-docs --chart-search-root {{chart}}
