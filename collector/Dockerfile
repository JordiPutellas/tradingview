FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/collector ./cmd/collector && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 collector
USER collector
# WORKDIR escribible: los comandos batch usan data/bulk-cache como staging.
WORKDIR /home/collector
COPY --from=build /out/collector /out/api /usr/local/bin/
ENTRYPOINT ["collector"]
CMD ["run"]
