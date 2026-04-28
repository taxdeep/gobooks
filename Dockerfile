# 遵循project_guide.md

FROM golang:1.23-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build both the application binary and the migration runner.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/balanciz         ./cmd/balanciz
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/balanciz-migrate ./cmd/balanciz-migrate


FROM alpine:3.20

WORKDIR /app

COPY --from=build /out/balanciz         /app/balanciz
COPY --from=build /out/balanciz-migrate /app/balanciz-migrate
COPY internal/web/static               /app/internal/web/static
# SQL migration files read at runtime by balanciz-migrate.
COPY migrations                        /app/migrations

ENV APP_ADDR=:6768

EXPOSE 6768

CMD ["/app/balanciz"]
