# Client pins: Nix 2.36.0.
FROM nixos/nix:2.36.0@sha256:746280551ddc1c72177d1a597c05eacac86dfaa18d5d649bf86411278bcd5470
RUN nix --version | grep -F '2.36.0'
