FROM golang:1.25.10-alpine AS builder

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/mathaix/openclawmachines/backend/pkg/version.Version=${VERSION} \
              -X github.com/mathaix/openclawmachines/backend/pkg/version.GitCommit=${GIT_COMMIT} \
              -X github.com/mathaix/openclawmachines/backend/pkg/version.BuildTime=${BUILD_TIME}" \
    -o /bin/server ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/server /usr/local/bin/server

EXPOSE 8080
CMD ["server"]
