BINARY := badil
CMD := ./cmd/badil
VERSION ?= dev

.PHONY: build test lint schema contract-golden template docker-build dev-serve

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

# Start the server with the current .env.
dev-serve:
	set -a; . ./.env; set +a; go run $(CMD) serve
