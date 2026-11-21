BINARY := badil
CMD := ./cmd/badil
VERSION ?= dev

.PHONY: build test lint schema contract-golden template docker-build dev-bootstrap dev-verify dev-serve dev-cleanup

build:
	CGO_ENABLED=0 go build -o $(BINARY) $(CMD)

test:
	go test ./...

lint:
	golangci-lint run ./...

# Regenerates charts/preview-apps/values.schema.json from internal/chartvalues
# and schema/preview.v1beta1.schema.json from internal/preview.
schema:
	go generate ./cmd/schemagen

# Regenerates the preview-apps renders of the valid contract fixtures in
# testdata/contract/v1beta1/preview-apps, which the Charts workflow diffs against.
# Each fixture is rendered alone, as the ApplicationSet passes a preview file.
contract-golden:
	for f in testdata/contract/v1beta1/valid/*.yaml; do \
		n=$$(basename "$$f" .yaml); \
		helm template "$$n" charts/preview-apps -f "$$f" \
			> "testdata/contract/v1beta1/preview-apps/$$n.golden.yaml" || exit 1; \
	done

# Builds the gitops repo template into dist/template/ for VERSION (default dev).
template:
	scripts/build-template.sh $(VERSION)

docker-build:
	docker build -t $(BINARY):latest .

# Creates a GitHub App via the manifest flow and writes .env + PEM in this
# directory. The webhook goes to BADIL_WEBHOOK_URL (badil's public base
# URL; /webhooks/github is appended) when set, otherwise to the ngrok tunnel,
# which then must be running in another terminal (`ngrok http 8080`).
# Pass GITOPS_REPO_URL=<clone URL> to prefill it in .env, otherwise edit .env
# after the fact:
#   GITOPS_REPO_URL=https://github.com/myorg/my-gitops.git make dev-bootstrap
#   BADIL_WEBHOOK_URL=https://badil.example.com make dev-bootstrap
dev-bootstrap:
	go run $(CMD) bootstrap-app --gitops-repo-url "$(GITOPS_REPO_URL)" --webhook-url "$(BADIL_WEBHOOK_URL)"

# Sanity-check the App credentials currently in .env / dev.private-key.pem.
dev-verify:
	set -a; . ./.env; set +a; go run $(CMD) verify-app

# Start the server with the current .env.
dev-serve:
	set -a; . ./.env; set +a; go run $(CMD) serve

# Run one cleanup sweep with the current .env. Set CLEANUP_TTL (e.g. 168h) to
# also expire idle previews:
#   CLEANUP_TTL=168h make dev-cleanup
dev-cleanup:
	set -a; . ./.env; set +a; go run $(CMD) cleanup
