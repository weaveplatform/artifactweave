The cache fixture was produced on 2026-10-28 with Nix 2.35.4 from the immutable
`nixos/nix` image `sha256:f159a58f390677f41e8e900a74b6e2f9c7f27b364122ae6acf7a652ce6c551c0`.
It contains a directory with a `message` file whose bytes are `native nix fixture\n`.

```sh
mkdir /tmp/weave-fixture
printf 'native nix fixture\n' > /tmp/weave-fixture/message
path=$(nix-store --add /tmp/weave-fixture)
nix --extra-experimental-features nix-command copy --to 'file:///out/cache?compression=xz' "$path"
```
