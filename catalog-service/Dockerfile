FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go generate ./internal/api && CGO_ENABLED=0 go build -trimpath -o /catalog .

FROM alpine:3.23
RUN adduser -D -u 10001 marketplace
USER marketplace
COPY --from=build /catalog /catalog
EXPOSE 8080
ENTRYPOINT ["/catalog"]
