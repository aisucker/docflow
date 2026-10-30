FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /docflow .
FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends libreoffice-writer fonts-dejavu-core && rm -rf /var/lib/apt/lists/*
COPY pdf/requirements.txt /tmp/pdf-requirements.txt
RUN pip install --no-cache-dir -r /tmp/pdf-requirements.txt && useradd --create-home app && mkdir -p /app/data && chown -R app:app /app
WORKDIR /app
COPY --from=build /docflow /app/docflow
USER app
EXPOSE 8080
CMD ["/app/docflow"]
