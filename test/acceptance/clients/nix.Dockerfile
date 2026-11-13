# Client pins: Nix 2.35.5.
FROM nixos/nix:2.35.6@sha256:b49cb33f3a7e5cf3933ad8774529de0e3807ec5cf54987aa7ccdfb49f2fc55ed
RUN nix --version | grep -F '2.35.5'
