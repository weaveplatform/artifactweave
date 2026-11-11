# Client pins: Nix 2.35.5.
FROM nixos/nix:2.35.5@sha256:9305de534abed691d0f569502a4962a3a072ea8e888502c453ecfe35ae182867
RUN nix --version | grep -F '2.35.5'
