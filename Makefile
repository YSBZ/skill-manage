BINARY := skillmanage
DIST    := dist
GUIDE   := docs/dist-readme.md
GOFLAGS := -trimpath -ldflags "-s -w"
# Windows: -H=windowsgui builds a windowless binary so a double-click runs the
# daemon in the background (it auto-opens the browser) instead of leaving a
# console window — and a quick exit no longer flashes a console.
WINFLAGS := -trimpath -ldflags "-s -w -H=windowsgui"

.PHONY: build test vet fmt build-all package desktop-app install-desktop desktop-dist desktop-dmg clean clean-dist

# macOS desktop app (Wails): a native window wrapping the daemon. Needs CGO +
# the UniformTypeIdentifiers framework (which `wails build` would add for us);
# we build both arches and lipo them into a universal SkillManage.app.
DESKTOP_APP     := SkillManage.app
DESKTOP_LDFLAGS := -s -w -extldflags '-framework UniformTypeIdentifiers'

desktop-app:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -tags "desktop,production" -ldflags "$(DESKTOP_LDFLAGS)" -o $(DIST)/smd-arm64 ./desktop
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -trimpath -tags "desktop,production" -ldflags "$(DESKTOP_LDFLAGS)" -o $(DIST)/smd-amd64 ./desktop
	@lipo -create -output $(DIST)/smd-universal $(DIST)/smd-arm64 $(DIST)/smd-amd64
	@rm -f $(DIST)/smd-arm64 $(DIST)/smd-amd64
	@rm -rf "$(DIST)/$(DESKTOP_APP)"
	@mkdir -p "$(DIST)/$(DESKTOP_APP)/Contents/MacOS" "$(DIST)/$(DESKTOP_APP)/Contents/Resources"
	@cp build/macos/Info.plist "$(DIST)/$(DESKTOP_APP)/Contents/Info.plist"
	@cp build/macos/AppIcon.icns "$(DIST)/$(DESKTOP_APP)/Contents/Resources/AppIcon.icns"
	@mv $(DIST)/smd-universal "$(DIST)/$(DESKTOP_APP)/Contents/MacOS/skillmanage-desktop"
	@chmod +x "$(DIST)/$(DESKTOP_APP)/Contents/MacOS/skillmanage-desktop"
	@codesign --force --sign - --timestamp=none "$(DIST)/$(DESKTOP_APP)/Contents/MacOS/skillmanage-desktop" 2>/dev/null || true
	@codesign --force --sign - --timestamp=none "$(DIST)/$(DESKTOP_APP)" 2>/dev/null || true
	@echo "built $(DIST)/$(DESKTOP_APP)"

# Drag-to-install disk image. Installing to /Applications via the dmg avoids App
# Translocation (which a quarantined zip-from-Downloads triggers) — worth trying
# if the app misbehaves when launched from a download. Still ad-hoc: first open
# needs right-click → Open (only notarization removes that).
desktop-dmg: desktop-app
	@rm -rf "$(DIST)/dmg" "$(DIST)/SkillManage.dmg"
	@mkdir -p "$(DIST)/dmg"
	@cp -R "$(DIST)/$(DESKTOP_APP)" "$(DIST)/dmg/"
	@cp build/macos/dist-readme.md "$(DIST)/dmg/安装说明.md"
	@ln -s /Applications "$(DIST)/dmg/Applications"
	@hdiutil create -volname "SkillManage" -srcfolder "$(DIST)/dmg" -ov -format UDZO "$(DIST)/SkillManage.dmg" >/dev/null
	@rm -rf "$(DIST)/dmg"
	@echo "built $(DIST)/SkillManage.dmg"

# Shareable zip of the desktop app + recipient guide (Gatekeeper instructions).
desktop-dist: desktop-app
	@mkdir -p "$(DIST)/pkg"
	@d="$(DIST)/pkg/SkillManage-macos"; rm -rf "$$d"; mkdir -p "$$d"; \
	cp -R "$(DIST)/$(DESKTOP_APP)" "$$d/"; cp build/macos/dist-readme.md "$$d/安装说明.md"; \
	(cd "$(DIST)/pkg" && rm -f SkillManage-macos.zip && zip -q -r SkillManage-macos.zip SkillManage-macos); \
	rm -rf "$$d"; ls -lh "$(DIST)/pkg/SkillManage-macos.zip"

# Install the desktop app: /Applications if writable, else ~/Applications.
install-desktop: desktop-app
	@set -e; \
	if [ -w /Applications ]; then dest=/Applications; else dest="$$HOME/Applications"; mkdir -p "$$dest"; fi; \
	rm -rf "$$dest/$(DESKTOP_APP)"; cp -R "$(DIST)/$(DESKTOP_APP)" "$$dest/"; \
	echo "installed $$dest/$(DESKTOP_APP)"

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
