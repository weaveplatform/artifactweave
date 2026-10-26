# Official R 4.5.2, pinned to the multi-platform manifest index.
FROM r-base:4.6.1@sha256:ad9b3bbca751f3ead77704fed4c20246127647b3d567b2bf970b7789e8d7b9a8

RUN Rscript -e 'stopifnot(as.character(getRversion()) == "4.5.2")'
