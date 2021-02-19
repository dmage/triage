FROM golang:1.16-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /go/src/github.com/dmage/triage
COPY . .
RUN go build ./cmd/scraper
RUN go build ./vendor/k8s.io/test-infra/triage/

FROM alpine
RUN apk add --no-cache git
COPY --from=builder /go/src/github.com/dmage/triage/scraper /usr/bin/scraper
COPY --from=builder /go/src/github.com/dmage/triage/triage /usr/bin/triage
COPY --from=builder /go/src/github.com/dmage/triage/updater.sh /usr/bin/updater.sh
COPY --from=builder /go/src/github.com/dmage/triage/server.sh /usr/bin/server.sh
WORKDIR /var/triage
ENV PRODUCTION=1
