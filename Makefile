APP_NAME := Gemini Dictation
BIN_DIR := build/bin
VERSION := $(shell git describe --tags 2>/dev/null || echo 1.0.0)
IDENTITY ?= $(shell security find-identity -v -p codesigning 2>/dev/null | grep "Developer ID Application" | sed 's/.*"\(.*\)"/\1/' | head -1)
DEV_ID := $(shell security find-identity -v -p codesigning 2>/dev/null | sed -n 's/.*"\(gemini-dictation-dev\)".*/\1/p')
BUNDLE := $(BIN_DIR)/$(APP_NAME).app

.PHONY: build sign notarize release publish install clean

build: ## signed .app in build/bin (stable self-signed cert, adhoc fallback)
	wails build -clean -s -ldflags "-X main.AppVersion=$(VERSION)"
	@# stable "gemini-dictation-dev" identity keeps TCC grants across
	@# rebuilds (adhoc binds to a per-build CDHash — grants break on rebuild)
	codesign --force --deep --sign $(if $(DEV_ID),$(DEV_ID),-) -i vn.ops4vn.gemini-dictation "$(BUNDLE)"
	@echo "✅ $(BUNDLE) (version $(VERSION), signed: $(if $(DEV_ID),$(DEV_ID),adhoc))"

sign: build ## sign with Developer ID Application (requires cert in keychain)
	@if [ -z "$(IDENTITY)" ]; then echo "❌ no 'Developer ID Application' identity found — install your Apple Developer cert"; exit 1; fi
	codesign --force --deep --options runtime --timestamp \
		--entitlements scripts/entitlements.plist \
		--sign "$(IDENTITY)" "$(BUNDLE)"
	codesign --verify --deep --strict "$(BUNDLE)" && echo "✅ signed: $(IDENTITY)"

notarize: ## notarize (requires APPLE_ID + APPLE_APP_PW or AC_KEY_* env)
	@if [ -z "$(APPLE_ID)" ] && [ -z "$(AC_KEY_ID)" ]; then \
		echo "❌ notarization needs APPLE_ID + APPLE_APP_PW (or App Store Connect key envs)"; exit 1; fi
	xcrun notarytool submit "$(BUNDLE).zip" --wait \
		$(if $(APPLE_ID),--apple-id "$(APPLE_ID)" --password "$(APPLE_APP_PW)" --team-id "$(TEAM_ID)",\
		--key "$(AC_KEY_PATH)" --key-id "$(AC_KEY_ID)" --issuer "$(AC_ISSUER)")
	xcrun stapler staple "$(BUNDLE)"
	@echo "✅ notarized + stapled"

release: build sign ## zip + sha256 + latest.json into dist/
	@mkdir -p dist
	ditto -c -k --keepParent "$(BUNDLE)" "dist/gemini-dictation-$(VERSION)-macos.zip"
	shasum -a 256 "dist/gemini-dictation-$(VERSION)-macos.zip" | awk '{print $$1}' > dist/sha.txt
	@printf '{"version":"%s","sha256":"%s","url":"%s/gemini-dictation-%s-macos.zip"}\n' \
		"$(VERSION)" "$$(cat dist/sha.txt)" "$(UPDATE_BASE_URL)" "$(VERSION)" > dist/latest.json
	@echo "✅ dist/: zip + latest.json (UPDATE_BASE_URL=$(UPDATE_BASE_URL))"

publish: release ## upload dist/ to Cloudflare R2 (needs R2_REMOTE, e.g. r2:gemini-dictation)
	@if [ -z "$(R2_REMOTE)" ]; then echo "❌ set R2_REMOTE (rclone remote:bucket)"; exit 1; fi
	rclone copy dist/ "$(R2_REMOTE)/" --include "*.zip" --include "latest.json"
	@echo "✅ published to $(R2_REMOTE) — updater will pick up latest.json"

install: ## install the built .app locally + LaunchAgent autostart
	./scripts/install.sh

clean:
	rm -rf build/bin dist
