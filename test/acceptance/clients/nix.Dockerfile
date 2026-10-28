# Nix 2.35.4, current stable checked 2026-10-28; native amd64 and arm64 clients.
FROM nixos/nix:2.35.4@sha256:f159a58f390677f41e8e900a74b6e2f9c7f27b364122ae6acf7a652ce6c551c0
RUN nix --version | grep -F '2.35.4'
