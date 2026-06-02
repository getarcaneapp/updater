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
    golangci-lint run -c .github/.golangci.yml ./...

[group('quality')]
_lint-all:
    @just _lint-go

[group('quality')]
lint target="all":
    @just "_lint-{{ target }}"

[group('test')]
test:
    go test ./...
