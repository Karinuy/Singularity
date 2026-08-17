FROM golang:1.23-alpine AS build

RUN apk add --no-cache build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/singularity .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=build /out/singularity /app/singularity

ENV DATABASE_PATH=/data/singularity.db
VOLUME ["/data"]

CMD ["/app/singularity"]
