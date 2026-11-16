# Client pins: Cargo/Rust 1.99.1.
FROM rust:1.100.0@sha256:e19840879045c951c905d0eaa9887f3439c62000e429963371b376bac8d38c31

# Retain the installed client identity in the native image build log.
RUN cargo --version && rustc --version
