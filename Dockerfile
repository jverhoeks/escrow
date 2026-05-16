FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /escrow ./cmd/escrow

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /escrow /usr/local/bin/escrow
EXPOSE 8888
ENTRYPOINT ["escrow"]
