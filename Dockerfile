FROM golang:1.25.4-alpine3.22 AS build
ADD ./ /src
RUN cd /src && go build -ldflags="-s" -o /bin/gcb2gh .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /bin/gcb2gh /gcb2gh
ENTRYPOINT ["/gcb2gh"]
