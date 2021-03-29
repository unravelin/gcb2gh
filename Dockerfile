FROM gcr.io/cloud-builders/go AS build
ADD ./ /src
RUN cd /src && CGO_ENABLED=0 go build -o /bin/gcb2gh .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /bin/gcb2gh /gcb2gh
ENTRYPOINT [ "/gcb2gh" ]
