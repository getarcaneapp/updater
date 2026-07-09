set working-directory := './'

_default:
    @just --list

[group('quality')]
_format-go:
    gofmt -s -w .

[group('quality')]
_format-all:
    @just _format-go

[group('quality')]
format target="all":
    @just "_format-{{ target }}"

[group('quality')]
_lint-go:
    golangci-lint run ./...

[group('quality')]
_lint-all:
    @just _lint-go

[group('quality')]
lint target="all":
    @just "_lint-{{ target }}"

[group('test')]
test:
    go test ./...

# -----------------------------------------------------------------------------
# Release
# -----------------------------------------------------------------------------

# Create and push a new version tag.
#
# Usage:
#   just release          # auto-increments patch from latest tag
# just release 1.2.3    # explicit version
[group('release')]
release version="":
    #!/usr/bin/env bash
    set -euo pipefail

    if [ -z "{{ version }}" ]; then
        LATEST_TAG=$(git tag -l 'v*' --sort=-v:refname | head -n1 || echo "")
        if [ -z "$LATEST_TAG" ]; then
            echo "No existing tag found; defaulting to v0.0.1"
            NEW_VERSION="0.0.1"
        else
            VERSION="${LATEST_TAG#v}"
            IFS='.' read -r MAJOR MINOR PATCH <<< "$VERSION"
            NEW_PATCH=$((PATCH + 1))
            NEW_VERSION="${MAJOR}.${MINOR}.${NEW_PATCH}"
        fi
    else
        NEW_VERSION="{{ version }}"
    fi

    echo "==> Tagging v${NEW_VERSION}"
    git tag "v${NEW_VERSION}" -m "v${NEW_VERSION}"
    git push origin "v${NEW_VERSION}"
    echo "==> Pushed v${NEW_VERSION}"
