# Client pins: R 4.6.2.
FROM r-base:4.6.2@sha256:7bb56b8b7c849d1cea9a5cff981789622340326abdce817ed44c00ac45947a37

RUN Rscript -e 'stopifnot(as.character(getRversion()) == "4.6.2")'

# Retain the installed client identity in the native image build log.
RUN R --version
