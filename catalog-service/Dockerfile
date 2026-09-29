FROM golang:1.26-alpine AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -o /catalog main.go

FROM scratch
COPY --from=build /catalog /catalog
EXPOSE 8080
ENTRYPOINT ["/catalog"]
