#!/usr/bin/env bash
# update-frontend.sh — update Lampa frontend sources, apply our patches,
# build and deploy.
#
# Usage:
#   update-frontend.sh diff             show what changed upstream since the last update
#   update-frontend.sh update           full pipeline: fetch, summary, patches, overlay, build, deploy
#   update-frontend.sh build            rebuild from the current sources
#   update-frontend.sh deploy           deploy the last build to the deploy directory
#   update-frontend.sh new-patch NAME   save current working-tree changes as patches/<NAME>.patch
#
# Environment:
#   FE_DEPLOY_DIR   deploy directory (default: <repo>/deploy/web)
#   FORCE=1         run the full pipeline even when the upstream commit is unchanged
#
# Upstream ships no package-lock.json, so this pipeline keeps its own pinned
# copy at frontend/package-lock.json (see install_deps below).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND="$ROOT/frontend"
SOURCES="$FRONTEND/sources"
PATCH_DIR="$FRONTEND/patches"
OVERLAY_DIR="$FRONTEND/overlay"
ORIGIN_FILE="$FRONTEND/ORIGIN_COMMIT"
LOCK_FILE="$FRONTEND/package-lock.json"
DEPLOY_DIR="${FE_DEPLOY_DIR:-$ROOT/deploy/web}"
UPSTREAM="https://github.com/yumata/lampa-source.git"
BRANCH="main"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWARN\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR\033[0m %s\n' "$*" >&2; exit 1; }

origin_commit() {
  if [ -f "$ORIGIN_FILE" ]; then cat "$ORIGIN_FILE"; fi
}

require_sources() {
  [ -d "$SOURCES/.git" ] || die "no sources clone at $SOURCES — run 'update' first"
}

fetch_upstream() {
  if [ ! -d "$SOURCES/.git" ]; then
    log "cloning $UPSTREAM into $SOURCES"
    git clone -q "$UPSTREAM" "$SOURCES"
  fi
  git -C "$SOURCES" fetch -q origin "$BRANCH"
}

upstream_head() {
  git -C "$SOURCES" rev-parse FETCH_HEAD
}

# Files touched by our patches (from the '+++ b/<path>' header lines).
patch_files() {
  local patch
  for patch in "$PATCH_DIR"/*.patch; do
    [ -e "$patch" ] || return 0
    awk '/^\+\+\+ b\//{sub(/^\+\+\+ b\//, ""); print}' "$patch"
  done
}

warn_if_patches_conflict() {
  local base="$1" new="$2" pf cf
  ls "$PATCH_DIR"/*.patch >/dev/null 2>&1 || return 0
  pf="$(patch_files | sort -u)"
  [ -n "$pf" ] || return 0
  cf="$(comm -12 <(printf '%s\n' "$pf") <(git -C "$SOURCES" diff --name-only "$base..$new" | sort -u))"
  if [ -n "$cf" ]; then
    warn "upstream touched files covered by our patches — conflicts likely:"
    printf '%s\n' "$cf" | sed 's/^/  /' >&2
  fi
}

print_summary() {
  local base="$1" new="$2"
  log "changes ${base:0:12}..${new:0:12}:"
  git -C "$SOURCES" --no-pager diff --stat "$base..$new" | tail -n 1
  git -C "$SOURCES" --no-pager diff --name-status "$base..$new"
}

apply_patches() {
  ls "$PATCH_DIR"/*.patch >/dev/null 2>&1 || { log "no patches"; return 0; }
  local patch
  for patch in "$PATCH_DIR"/*.patch; do
    log "applying $(basename "$patch")"
    if ! git -C "$SOURCES" apply --check "$patch" 2>"$TMP_ERR"; then
      cat "$TMP_ERR" >&2
      die "patch $(basename "$patch") does not apply — resolve the conflict, refresh the patch, re-run"
    fi
    git -C "$SOURCES" apply "$patch"
  done
}

apply_overlay() {
  [ -d "$OVERLAY_DIR" ] || return 0
  log "copying overlay"
  cp -a "$OVERLAY_DIR/." "$SOURCES/"
}

# Stamp input: upstream package.json plus our pinned lockfile, so an upstream
# dependency change forces a reinstall instead of silently keeping the stale
# pinned set.
deps_checksum() {
  [ -f "$LOCK_FILE" ] || return 1
  cat "$SOURCES/package.json" "$LOCK_FILE" | md5sum | cut -d' ' -f1
}

# True when node_modules exists and was installed from the current
# package.json + lockfile pair.
deps_fresh() {
  [ -f "$SOURCES/node_modules/.fe-lock-stamp" ] || return 1
  [ "$(cat "$SOURCES/node_modules/.fe-lock-stamp")" = "$(deps_checksum)" ]
}

# Upstream ships no package-lock.json (it is listed in their .gitignore), so we
# keep our own pinned copy in $FRONTEND/package-lock.json and feed it to
# 'npm ci' for reproducible installs; without it we bootstrap one first.
install_deps() {
  if [ ! -f "$LOCK_FILE" ]; then
    log "no lockfile yet — resolving dependencies (npm install --package-lock-only)"
    (cd "$SOURCES" && npm install --package-lock-only --no-audit --no-fund)
    cp "$SOURCES/package-lock.json" "$LOCK_FILE"
    log "saved $LOCK_FILE — commit it to keep installs reproducible"
  fi
  log "installing npm dependencies (npm ci from the pinned lockfile)"
  cp "$LOCK_FILE" "$SOURCES/package-lock.json"
  if ! (cd "$SOURCES" && npm ci --no-audit --no-fund); then
    # 'npm ci' refuses to run when the lockfile is out of sync with
    # package.json — that means upstream changed its dependencies, so
    # re-resolve the pinned lockfile and retry.
    log "lockfile out of sync with upstream package.json — re-resolving"
    (cd "$SOURCES" && npm install --package-lock-only --no-audit --no-fund)
    cp "$SOURCES/package-lock.json" "$LOCK_FILE"
    log "updated $LOCK_FILE — commit it to keep installs reproducible"
    (cd "$SOURCES" && npm ci --no-audit --no-fund)
  fi
  deps_checksum > "$SOURCES/node_modules/.fe-lock-stamp"
}

build_frontend() {
  require_sources
  if ! deps_fresh; then
    install_deps
  fi
  log "building frontend (gulp build + pack_github)"
  (
    cd "$SOURCES"
    # 'build' comes from our patches/010-gulp-build-task.patch: upstream has
    # no non-interactive task that produces dest/app.js (the default watch
    # task builds it but never exits), and pack_github needs it.
    npx gulp build
    npx gulp pack_github
  )
  [ -d "$SOURCES/build/github/lampa" ] || die "build produced no output at sources/build/github/lampa"
}

deploy_frontend() {
  [ -d "$SOURCES/build/github/lampa" ] || die "no build output — run 'build' first"
  log "deploying to $DEPLOY_DIR"
  rm -rf "$DEPLOY_DIR.tmp"
  cp -a "$SOURCES/build/github/lampa" "$DEPLOY_DIR.tmp"
  rm -rf "$DEPLOY_DIR"
  mv "$DEPLOY_DIR.tmp" "$DEPLOY_DIR"
}

cmd_diff() {
  fetch_upstream
  local base new
  base="$(origin_commit)"
  new="$(upstream_head)"
  if [ -z "$base" ]; then
    log "no previous update recorded; upstream head is ${new:0:12}"
    return 0
  fi
  if [ "$base" = "$new" ]; then
    log "up to date: ${base:0:12}"
    return 0
  fi
  print_summary "$base" "$new"
  warn_if_patches_conflict "$base" "$new"
}

cmd_update() {
  fetch_upstream
  local base new
  base="$(origin_commit)"
  new="$(upstream_head)"

  if [ -n "$base" ] && [ "$base" = "$new" ] && [ "${FORCE:-0}" != "1" ]; then
    log "up to date at ${new:0:12} (use FORCE=1 to rebuild)"
    return 0
  fi

  # Reset the working tree to the new upstream commit.
  # 'clean -fd' drops our overlay files (they are re-added below) but keeps
  # ignored paths like node_modules/.
  log "checking out ${new:0:12}"
  git -C "$SOURCES" checkout -q -f FETCH_HEAD
  git -C "$SOURCES" clean -qfd

  # gulp-newer is mtime-incremental, so files deleted upstream would linger
  # in a reused build/. Updates are infrequent — rebuild from scratch;
  # node_modules is retained (standalone 'build' stays incremental).
  rm -rf "$SOURCES/build" "$SOURCES/dest"

  TMP_ERR="$(mktemp)"
  trap 'rm -f "$TMP_ERR"' EXIT
  apply_patches
  apply_overlay
  build_frontend
  deploy_frontend

  echo "$new" > "$ORIGIN_FILE"
  if [ -n "$base" ]; then
    print_summary "$base" "$new" || true
  fi
  log "update complete: ${base:-none} -> ${new:0:12}"
}

cmd_new_patch() {
  local name="${1:-}"
  [ -n "$name" ] || die "usage: update-frontend.sh new-patch <NNN-short-name>"
  require_sources

  # Intent-to-add makes untracked (new) files show up in 'git diff'; ignored
  # paths (build/, dest/, node_modules/) are not affected by -N.
  git -C "$SOURCES" add -N -A

  # Diff new changes only: exclude build churn, everything already covered by
  # existing patches (after an update they sit uncommitted in the tree), and
  # overlay-delivered files (they are re-copied on every update anyway).
  local excludes=()
  excludes+=(":(exclude)index/github/assembly.json")
  local f
  while IFS= read -r f; do
    [ -n "$f" ] && excludes+=(":(exclude)$f")
  done < <(patch_files | sort -u)
  while IFS= read -r f; do
    [ -n "$f" ] && excludes+=(":(exclude)${f#./}")
  done < <(cd "$OVERLAY_DIR" && find . -type f)

  if git -C "$SOURCES" diff --quiet -- "${excludes[@]}"; then
    git -C "$SOURCES" reset -q
    die "no new changes beyond existing patches and overlay"
  fi

  mkdir -p "$PATCH_DIR"
  git -C "$SOURCES" diff -- "${excludes[@]}" > "$PATCH_DIR/$name.patch"
  # Only drop the intent-to-add markers; deliberately do NOT try to revert the
  # captured edits (a file can mix applied-patch hunks with new ones, so
  # surgical revert is error-prone). The tree is left dirty with patches plus
  # the new edits — harmless, since the next 'update' resets it with
  # 'checkout -f', which also proves the patch applies on a fresh tree.
  git -C "$SOURCES" reset -q
  log "saved $PATCH_DIR/$name.patch (working tree left as-is; re-run 'update' to verify it applies cleanly)"
}

case "${1:-update}" in
  diff)      cmd_diff ;;
  update)    cmd_update ;;
  build)     build_frontend ;;
  deploy)    deploy_frontend ;;
  new-patch) shift; cmd_new_patch "$@" ;;
  *)         die "unknown command: $1 (diff|update|build|deploy|new-patch)" ;;
esac
