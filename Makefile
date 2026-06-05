# trino-goway build orchestration.
#
# The Go gateway embeds the web UI bundle via `//go:embed all:web/dist` in
# cmd/trino-goway/main.go. The React app in webapp/ builds with Vite (pnpm) to
# webapp/dist; the `webapp` target copies that output into the embed directory
# so `go build ./cmd/trino-goway` ships the real UI.

WEBAPP_DIR    := webapp
WEBAPP_DIST   := $(WEBAPP_DIR)/dist
EMBED_DIST    := cmd/trino-goway/web/dist

.PHONY: build webapp webapp-clean clean

# build produces the gateway binary with the embedded UI bundle. It depends on
# `webapp` so the embed directory holds the production build, not the placeholder.
build: webapp
	go build ./cmd/trino-goway

# webapp builds the React production bundle and syncs it into the Go embed
# directory. The UI uses pnpm (project convention) and base path /trino-gateway/.
webapp:
	cd $(WEBAPP_DIR) && pnpm install --frozen-lockfile && pnpm build
	rm -rf $(EMBED_DIST)/assets
	rm -f  $(EMBED_DIST)/index.html $(EMBED_DIST)/logo.svg
	cp -R $(WEBAPP_DIST)/. $(EMBED_DIST)/

# webapp-clean removes the generated UI bundle from the embed directory, leaving
# only the tracked .gitkeep. With no index.html present, the gateway serves the
# in-code placeholder shell (see admin.placeholderIndex).
webapp-clean:
	rm -rf $(EMBED_DIST)/assets
	rm -f  $(EMBED_DIST)/index.html $(EMBED_DIST)/logo.svg

clean: webapp-clean
	rm -f trino-goway
