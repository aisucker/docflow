FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /docflow .
FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app && mkdir -p /app/data && chown -R app:app /app
WORKDIR /app
COPY --from=build /docflow /app/docflow
USER app
EXPOSE 8080
CMD ["/app/docflow"]
