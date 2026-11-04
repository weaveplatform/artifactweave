# Client pins: Cargo/Rust 1.99.1.
FROM rust:1.99.1@sha256:931c2c0531a8b143e044f0f3914dfe802f15bb3d1955f0b657e9359327ac2ffc

# Retain the installed client identity in the native image build log.
RUN cargo --version && rustc --version
