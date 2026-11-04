install:
    go install github.com/nimling/sbump/cmd@latest
    mv "$(go env GOPATH)/bin/cmd" "$(go env GOPATH)/bin/sbump"

deploy:
    sbump patch --env APP_VERSION --push-version --auto --workflow

build:
    go build ./...

tidy:
    go mod tidy

test target='':
    #!/usr/bin/env bash
    set -e
    if [ -z "{{target}}" ]; then
        go test ./test/...
    else
        go test ./test/{{target}}/...
    fi
