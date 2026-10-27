
all: vet test

clean:
	rm -rf gen

generate: clean
	mkdir -p gen; cd gen; go run ../cmd/generate-fix/generate-fix.go -pkg-root=github.com/appital/gofix/gen ../spec/*.xml

generate-udecimal: clean
	mkdir -p gen; cd gen; go run ../cmd/generate-fix/generate-fix.go -use-udecimal=true -pkg-root=github.com/appital/gofix/gen ../spec/*.xml

fmt:
	gofmt -l -w -s $(shell find . -type f -name '*.go')

vet:
	go vet `go list ./... | grep -v gofix/gen`
	cd metrics/prometheus && go vet ./...

test:
	go test -v -race -cover `go list ./... | grep -v gofix/gen`
	cd metrics/prometheus && go test -v -race -cover ./...

GOLANGCI_LINT_VERSION := v2.15.0

linters-install:
	@golangci-lint --version >/dev/null 2>&1 || { \
		echo "installing linting tools..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
	}

lint: linters-install
	golangci-lint run
	cd metrics/prometheus && golangci-lint run

# An easy way to run the linter without going through the install process -
# docker run -t --rm -v $(pwd):/app -w /app golangci/golangci-lint:v2.15.0 golangci-lint run -v
# See https://golangci-lint.run/welcome/install/ for more details.

# ---------------------------------------------------------------
# Targets related to benchmarking and profiling -
#
# The micro-benchmarks live in the root package and depend only on core APIs,
# so they run on a fresh clone. The loadgen lives under _test/ because it uses
# the generated gen/... packages; run `make generate` before `make loadgen`.
#
# Typical workflow:
#   make bench-baseline      # record where we are today
#   ...make a change...
#   make bench bench-compare # see what it did

BENCH ?= .
BENCH_COUNT ?= 10
BENCH_TIME ?= 1s
BENCH_GOMAXPROCS ?= 4

# Run the micro-benchmarks into .bench/current.txt.
bench:
	NAME=current BENCH='$(BENCH)' COUNT=$(BENCH_COUNT) BENCHTIME=$(BENCH_TIME) \
		GOMAXPROCS=$(BENCH_GOMAXPROCS) ./scripts/bench.sh

# Record a reference run to compare later changes against.
bench-baseline:
	NAME=baseline BENCH='$(BENCH)' COUNT=$(BENCH_COUNT) BENCHTIME=$(BENCH_TIME) \
		GOMAXPROCS=$(BENCH_GOMAXPROCS) ./scripts/bench.sh

bench-compare:
	./scripts/benchstat.sh .bench/baseline.txt .bench/current.txt

# Confirm the harness is quiet enough to trust: two runs of identical code
# should show no significant difference.
bench-selftest:
	NAME=selftest-a BENCH='$(BENCH)' COUNT=$(BENCH_COUNT) BENCHTIME=$(BENCH_TIME) \
		GOMAXPROCS=$(BENCH_GOMAXPROCS) ./scripts/bench.sh
	NAME=selftest-b BENCH='$(BENCH)' COUNT=$(BENCH_COUNT) BENCHTIME=$(BENCH_TIME) \
		GOMAXPROCS=$(BENCH_GOMAXPROCS) ./scripts/bench.sh
	./scripts/benchstat.sh .bench/selftest-a.txt .bench/selftest-b.txt

# Profile one benchmark. Pass BENCH= to pick it, e.g.
#   make bench-profile BENCH=BenchmarkRoundTrip
bench-profile:
	NAME=profile BENCH='$(BENCH)' COUNT=1 BENCHTIME=$(BENCH_TIME) \
		GOMAXPROCS=$(BENCH_GOMAXPROCS) \
		CPUPROFILE=.bench/cpu.prof MEMPROFILE=.bench/mem.prof ./scripts/bench.sh
	@echo
	@echo "top allocation sites:"
	@go tool pprof -top -nodecount=20 -sample_index=alloc_objects .bench/mem.prof 2>/dev/null | tail -22

# The loadgen needs gen/, so it builds out of _test like the acceptance server.
build-loadgen:
	cd _test; go build -v -o loadgen_bin ./loadgen/

LOAD_DURATION ?= 30s
LOAD_WARMUP ?= 5s
LOAD_RATE ?= 5000
LOAD_SESSIONS ?= 1
LOAD_MIX ?= orderflow
LOAD_SCENARIO ?= orders

# Run the full-stack load test and collect every profile.
loadtest: build-loadgen
	mkdir -p .bench
	cd _test; ./loadgen_bin -scenario=$(LOAD_SCENARIO) \
		-duration=$(LOAD_DURATION) -warmup=$(LOAD_WARMUP) \
		-rate=$(LOAD_RATE) -sessions=$(LOAD_SESSIONS) -mix=$(LOAD_MIX) \
		-cpuprofile=../.bench/loadgen-cpu.prof \
		-memprofile=../.bench/loadgen-mem.prof \
		-blockprofile=../.bench/loadgen-block.prof \
		-mutexprofile=../.bench/loadgen-mutex.prof \
		-json=../.bench/loadgen.json

.PHONY: bench bench-baseline bench-compare bench-selftest bench-profile build-loadgen loadtest
# ---------------------------------------------------------------

# ---------------------------------------------------------------
# Targets related to running acceptance tests -

ifdef STORE_TYPE
STORE := $(STORE_TYPE)
else
STORE := memory
endif

ifdef ACCEPTANCE_SET
TEST_SET := $(ACCEPTANCE_SET)
else
TEST_SET := server
endif

build-test-srv:
	cd _test; go build -v -o echo_server ./test-server/
fix40:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5001 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix41:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5002 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix42:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5003 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix43:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5004 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix44:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5005 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix50:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5006 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix50sp1:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5007 $(STORE) "definitions/$(TEST_SET)/$@/*.def"
fix50sp2:
	cd _test; ./runat.sh cfg/$(TEST_SET)/$@.cfg 5008 $(STORE) "definitions/$(TEST_SET)/$@/*.def"

ACCEPT_SUITE=fix40 fix41 fix42 fix43 fix44 fix50 fix50sp1 fix50sp2
accept: $(ACCEPT_SUITE)

.PHONY: test $(ACCEPT_SUITE)
# ---------------------------------------------------------------

# ---------------------------------------------------------------
# These targets are specific to the Github CI Runner -

build-src:
	go build -v `go list ./...`
	cd metrics/prometheus && go build -v ./...

build: build-src build-test-srv

test-ci:
	go test -v -race -cover `go list ./... | grep -v gofix/gen`
	cd metrics/prometheus && go test -v -race -cover ./...

test-ci-nodb:
	go test -race -cover `go list ./... | grep -v gofix/gen | grep -v -E '/(sql|composite)$$'`
	cd metrics/prometheus && go test -race -cover ./...

generate-ci: clean
	mkdir -p gen; cd gen; go run ../cmd/generate-fix/generate-fix.go -pkg-root=github.com/appital/gofix/gen ../spec/$(shell echo $(FIX_TEST) | tr  '[:lower:]' '[:upper:]').xml;

generate-ci-udecimal: clean
	mkdir -p gen; cd gen; go run ../cmd/generate-fix/generate-fix.go -use-udecimal=true -pkg-root=github.com/appital/gofix/gen ../spec/$(shell echo $(FIX_TEST) | tr  '[:lower:]' '[:upper:]').xml;

# ---------------------------------------------------------------
