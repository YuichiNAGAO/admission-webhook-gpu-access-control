FROM golang:alpine AS build-stage
COPY . /work
WORKDIR /work
RUN go mod download
RUN CGO_ENABLED=0 go build -o entrypoint .


FROM alpine:latest
COPY --from=build-stage /work/entrypoint /usr/local/bin/entrypoint
ENTRYPOINT ["/usr/local/bin/entrypoint"]