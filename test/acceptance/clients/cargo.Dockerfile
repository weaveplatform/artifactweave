# Client pins: Cargo/Rust 1.100.0.
FROM rust:1.100.0@sha256:223cc32ddca8f3ee90c436dbc94b6b5f052ea7f9653c5201e9612f08ad7d21b8

# Retain the installed client identity in the native image build log.
RUN cargo --version && rustc --version
