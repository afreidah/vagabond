# -------------------------------------------------------------------------------
# Live Test Function Image
#
# Author: Alex Freidah
#
# A static binary on Fn's own base image, which is what Oracle's Go examples
# run on, as the unprivileged UID and GID 1000 OCI Functions runs every
# function's container under. The binary is built for the machine building it,
# so build on the architecture of the application's shape.
# -------------------------------------------------------------------------------

FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /func .

FROM fnproject/go:1.26-0.1.24
WORKDIR /function
COPY --from=build /func /function/func
USER 1000:1000
ENTRYPOINT ["./func"]
