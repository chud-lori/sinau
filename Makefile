# Sinau dev tasks.
#
# The SQLite driver (github.com/mattn/go-sqlite3) compiles FTS5 only when
# the `sqlite_fts5` build tag is set. /search needs it, so every build,
# test, and run goes through this Makefile to keep the tag on.

GO ?= go
TAGS = sqlite_fts5
BIN = bin/sinau

.PHONY: build test vet run tidy clean

build:
	@mkdir -p bin
	$(GO) build -tags $(TAGS) -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/sinau

test:
	$(GO) test -tags $(TAGS) ./...

vet:
	$(GO) vet -tags $(TAGS) ./...

run:
	$(GO) run -tags $(TAGS) ./cmd/sinau

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin
