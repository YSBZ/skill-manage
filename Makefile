BINARY := skillmanage
DIST    := dist
GUIDE   := docs/dist-readme.md
GOFLAGS := -trimpath -ldflags "-s -w"
# Windows: -H=windowsgui builds a windowless binary so a double-click runs the
# daemon in the background (it auto-opens the browser) instead of leaving a
# console window — and a quick exit no longer flashes a console.
WINFLAGS := -trimpath -ldflags "-s -w -H=windowsgui"

.PHONY: build test vet fmt build-all package clean clean-dist

# Host build.
build:
	go build $(GOFLAGS) -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Cross-compiled single binaries (UI embedded, no separate frontend build).
# WSL uses the linux build. Depends on clean-dist (NOT clean) so packaging does
# not delete the host ./skillmanage out from under a running dev daemon — that
# would break its GIT_ASKPASS (the daemon execs its own binary for credentials).
build-all: clean-dist
	GOOS=darwin  GOARCH=arm64 go build $(GOFLAGS) -o $(DIST)/skillmanage-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/skillmanage-darwin-amd64 .
	GOOS=windows GOARCH=amd64 go build $(WINFLAGS) -o $(DIST)/skillmanage-windows-amd64.exe .
	GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/skillmanage-linux-amd64 .

# Shareable per-platform zips: each bundles the single binary (named uniformly
# as skillmanage / skillmanage.exe) plus the recipient guide. Send one zip; the
# recipient needs no Go toolchain.
package: build-all
	@set -e; \
	for spec in \
	  "darwin-arm64:skillmanage:mac-apple-silicon" \
	  "darwin-amd64:skillmanage:mac-intel" \
	  "linux-amd64:skillmanage:linux-wsl" \
	  "windows-amd64.exe:skillmanage.exe:windows"; do \
	  src=$${spec%%:*}; rest=$${spec#*:}; name=$${rest%%:*}; label=$${rest#*:}; \
	  d="$(DIST)/pkg/skillmanage-$$label"; mkdir -p "$$d"; \
	  cp "$(DIST)/skillmanage-$$src" "$$d/$$name"; cp "$(GUIDE)" "$$d/README.md"; \
	  (cd "$(DIST)/pkg" && zip -q -r "skillmanage-$$label.zip" "skillmanage-$$label"); \
	  rm -rf "$$d"; \
	done; \
	rm -f $(DIST)/skillmanage-*; \
	ls -lh $(DIST)/pkg/*.zip

clean-dist:
	rm -rf $(DIST)

clean:
	rm -rf $(DIST) $(BINARY)
