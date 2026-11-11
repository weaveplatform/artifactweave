# Client pins: apk-tools bundled with Alpine 3.24 (immutable image).
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# Retain the installed client identity in the native image build log.
RUN apk --version
