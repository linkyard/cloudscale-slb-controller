FROM golang:1.11-alpine AS builder

RUN apk add --no-cache git glide musl-dev \
  && mkdir -p /go/src/github.com/linkyard/cloudscale-slb-controller/ \
  && mkdir -p /go/src/github.com/linkyard/cloudscale-slb-controller/cmd/ \
  && mkdir -p /go/src/github.com/linkyard/cloudscale-slb-controller/cmd/cloudscale_slb_controller/ \
  && mkdir -p /go/src/github.com/linkyard/cloudscale-slb-controller/internal/

COPY cmd/cloudscale_slb_controller/* /go/src/github.com/linkyard/cloudscale-slb-controller/cmd/cloudscale_slb_controller/
COPY internal/* /go/src/github.com/linkyard/cloudscale-slb-controller/internal/
COPY glide.* /go/src/github.com/linkyard/cloudscale-slb-controller/

WORKDIR /go/src/github.com/linkyard/cloudscale-slb-controller

RUN glide install \
  && go test -v ./... \
  && GOOS=linux GOARCH=amd64 GCO_ENABLED=0 go build -ldflags "-s" -o /cloudscale-slb-controller ./cmd/cloudscale_slb_controller/main.go

FROM alpine

COPY --from=builder /cloudscale-slb-controller /usr/local/bin/cloudscale-slb-controller

RUN apk add --no-cache tini ca-certificates iproute2

ENTRYPOINT ["tini", "-g", "--"]

USER root
CMD ["cloudscale-slb-controller"]
