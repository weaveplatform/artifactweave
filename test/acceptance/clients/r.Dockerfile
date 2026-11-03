# Official R 4.6.1, pinned to the multi-platform manifest index.
FROM r-base:4.6.2@sha256:857b44eeca5a8c4f39b210d3912a879fa4cf4b47d9e57a5940b6a29d44c8f938

RUN Rscript -e 'stopifnot(as.character(getRversion()) == "4.6.1")'
