FROM gcr.io/cloud-builders/go AS build-env
ADD ./ /src
RUN cd /src && CGO_ENABLED=0 go build -o /bin/gcb2gh .

FROM scratch
COPY --from=build-env /bin/gcb2gh /gcb2gh
ENTRYPOINT [ "/gcb2gh" ]
